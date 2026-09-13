// Package repos implements the "Manage Repos" feature: subscribing to a
// Bitbucket repo (validated against the Bitbucket API), listing the current
// user's subscriptions, and removing one.
package repos

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"golang.org/x/oauth2"

	"commit-notification-app/backend/internal/auth"
	"commit-notification-app/backend/internal/bitbucket"
	"commit-notification-app/backend/internal/config"
	appdb "commit-notification-app/backend/internal/db"
)

var errAlreadySubscribed = errors.New("already subscribed to this repo")

type Handlers struct {
	cfg      config.Config
	oauthCfg *oauth2.Config
	db       *mongo.Database
}

func NewHandlers(cfg config.Config, oauthCfg *oauth2.Config, db *mongo.Database) *Handlers {
	return &Handlers{cfg: cfg, oauthCfg: oauthCfg, db: db}
}

type addRepoRequest struct {
	Workspace string `json:"workspace"`
	RepoSlug  string `json:"repoSlug"`
}

// currentUser loads the full user document for the authenticated request -
// handlers need it for a valid Bitbucket token, not just the ID.
func (h *Handlers) currentUser(ctx context.Context, c *gin.Context) (*appdb.User, bool) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return nil, false
	}
	oid, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return nil, false
	}
	var user appdb.User
	if err := h.db.Collection("users").FindOne(ctx, bson.M{"_id": oid}).Decode(&user); err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return nil, false
	}
	return &user, true
}

// AddRepo validates workspace/repoSlug against the Bitbucket API, then
// upserts the shared "repos" doc and creates a subscription for this user -
// both inside a transaction so the two writes stay consistent.
func (h *Handlers) AddRepo(c *gin.Context) {
	ctx := c.Request.Context()

	var req addRepoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	req.Workspace = strings.TrimSpace(req.Workspace)
	req.RepoSlug = strings.TrimSpace(req.RepoSlug)
	if req.Workspace == "" || req.RepoSlug == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "workspace and repoSlug are required"})
		return
	}

	user, ok := h.currentUser(ctx, c)
	if !ok {
		return
	}

	token, err := auth.ValidToken(ctx, h.cfg, h.oauthCfg, h.db, user)
	if err != nil {
		log.Printf("getting valid bitbucket token: %v", err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	httpClient := h.oauthCfg.Client(ctx, token)

	if _, err := bitbucket.FetchRepo(httpClient, req.Workspace, req.RepoSlug); err != nil {
		if errors.Is(err, bitbucket.ErrRepoNotFound) {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "repo not found or you don't have access to it"})
			return
		}
		log.Printf("validating repo failed: %v", err)
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "could not reach bitbucket"})
		return
	}

	session, err := h.db.Client().StartSession()
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	defer session.EndSession(ctx)

	result, err := session.WithTransaction(ctx, func(sc context.Context) (any, error) {
		now := time.Now()

		var repo appdb.Repo
		repoFilter := bson.M{"workspace": req.Workspace, "repoSlug": req.RepoSlug}
		repoUpdate := bson.M{
			"$setOnInsert": bson.M{
				"workspace": req.Workspace,
				"repoSlug":  req.RepoSlug,
				"createdAt": now,
			},
		}
		repoOpts := options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)
		if err := h.db.Collection("repos").FindOneAndUpdate(sc, repoFilter, repoUpdate, repoOpts).Decode(&repo); err != nil {
			return nil, err
		}

		sub := appdb.RepoSubscription{
			UserID:    user.ID,
			RepoID:    repo.ID,
			CreatedAt: now,
		}
		if _, err := h.db.Collection("repoSubscriptions").InsertOne(sc, sub); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return nil, errAlreadySubscribed
			}
			return nil, err
		}

		return repo, nil
	})

	if err != nil {
		if errors.Is(err, errAlreadySubscribed) {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": errAlreadySubscribed.Error()})
			return
		}
		log.Printf("subscribing to repo failed: %v", err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	repo := result.(appdb.Repo)
	c.JSON(http.StatusCreated, gin.H{
		"workspace": repo.Workspace,
		"repoSlug":  repo.RepoSlug,
	})
}

type repoSubscriptionView struct {
	ID           bson.ObjectID `bson:"_id" json:"id"`
	Workspace    string        `bson:"workspace" json:"workspace"`
	RepoSlug     string        `bson:"repoSlug" json:"repoSlug"`
	SubscribedAt time.Time     `bson:"createdAt" json:"subscribedAt"`
}

// ListRepos returns the current user's repo subscriptions, joined with the
// shared repo docs for workspace/repoSlug.
func (h *Handlers) ListRepos(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}
	oid, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"userId": oid}}},
		{{Key: "$lookup", Value: bson.M{
			"from":         "repos",
			"localField":   "repoId",
			"foreignField": "_id",
			"as":           "repo",
		}}},
		{{Key: "$unwind", Value: "$repo"}},
		{{Key: "$project", Value: bson.M{
			"_id":       1,
			"workspace": "$repo.workspace",
			"repoSlug":  "$repo.repoSlug",
			"createdAt": 1,
		}}},
		{{Key: "$sort", Value: bson.M{"createdAt": -1}}},
	}

	cursor, err := h.db.Collection("repoSubscriptions").Aggregate(ctx, pipeline)
	if err != nil {
		log.Printf("listing repos failed: %v", err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	defer cursor.Close(ctx)

	subs := []repoSubscriptionView{}
	if err := cursor.All(ctx, &subs); err != nil {
		log.Printf("decoding repo list failed: %v", err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	c.JSON(http.StatusOK, subs)
}

// RemoveRepo deletes one of the current user's subscriptions. It never
// deletes the shared "repos" doc itself, even if no one else is subscribed -
// harmless to leave behind, and cheap to skip that bookkeeping for now.
func (h *Handlers) RemoveRepo(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}
	userOID, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}

	subOID, err := bson.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid subscription id"})
		return
	}

	res, err := h.db.Collection("repoSubscriptions").DeleteOne(ctx, bson.M{
		"_id":    subOID,
		"userId": userOID, // scoped to the caller - can't delete someone else's subscription
	})
	if err != nil {
		log.Printf("removing repo subscription failed: %v", err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if res.DeletedCount == 0 {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "subscription not found"})
		return
	}

	c.Status(http.StatusNoContent)
}
