package commits

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"commit-notification-app/backend/internal/auth"
	appdb "commit-notification-app/backend/internal/db"
)

// Handlers serves the Commit Feed API.
type Handlers struct {
	db           *mongo.Database
	defaultLimit int
	maxLimit     int
}

func NewHandlers(db *mongo.Database, defaultLimit, maxLimit int) *Handlers {
	return &Handlers{db: db, defaultLimit: defaultLimit, maxLimit: maxLimit}
}

// feedItem is one row of the commit feed - a commit joined with which
// repo it belongs to, for display and the "load older" cursor.
type feedItem struct {
	ID            bson.ObjectID `bson:"_id" json:"id"`
	Workspace     string        `bson:"workspace" json:"workspace"`
	RepoSlug      string        `bson:"repoSlug" json:"repoSlug"`
	Message       string        `bson:"message" json:"message"`
	AuthorName    string        `bson:"authorName" json:"authorName"`
	CommittedAt   time.Time     `bson:"committedAt" json:"committedAt"`
	BitbucketURL  string        `bson:"bitbucketUrl" json:"bitbucketUrl"`
	AISummary     string        `bson:"aiSummary" json:"aiSummary"`
	SummaryStatus string        `bson:"summaryStatus" json:"summaryStatus"`
}

// ListFeed returns commits across the current user's subscribed repos,
// newest first. Supports cursor pagination via ?before=<RFC3339 timestamp>
// (the previous page's last commit's committedAt) and ?limit=.
func (h *Handlers) ListFeed(c *gin.Context) {
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

	limit := h.defaultLimit
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > h.maxLimit {
		limit = h.maxLimit
	}

	subCursor, err := h.db.Collection("repoSubscriptions").Find(ctx, bson.M{"userId": oid})
	if err != nil {
		log.Printf("commit feed: loading subscriptions failed: %v", err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	var subs []appdb.RepoSubscription
	if err := subCursor.All(ctx, &subs); err != nil {
		log.Printf("commit feed: decoding subscriptions failed: %v", err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if len(subs) == 0 {
		c.JSON(http.StatusOK, gin.H{"commits": []feedItem{}, "nextCursor": ""})
		return
	}
	repoIDs := make([]bson.ObjectID, len(subs))
	for i, s := range subs {
		repoIDs[i] = s.RepoID
	}

	match := bson.M{"repoId": bson.M{"$in": repoIDs}}
	if before := c.Query("before"); before != "" {
		if t, err := time.Parse(time.RFC3339Nano, before); err == nil {
			match["committedAt"] = bson.M{"$lt": t}
		}
	}

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: match}},
		{{Key: "$sort", Value: bson.M{"committedAt": -1}}},
		{{Key: "$limit", Value: limit}},
		{{Key: "$lookup", Value: bson.M{
			"from":         "repos",
			"localField":   "repoId",
			"foreignField": "_id",
			"as":           "repo",
		}}},
		{{Key: "$unwind", Value: "$repo"}},
		{{Key: "$project", Value: bson.M{
			"_id":           1,
			"message":       1,
			"authorName":    1,
			"committedAt":   1,
			"bitbucketUrl":  1,
			"aiSummary":     1,
			"summaryStatus": 1,
			"workspace":     "$repo.workspace",
			"repoSlug":      "$repo.repoSlug",
		}}},
	}

	cursor, err := h.db.Collection("commits").Aggregate(ctx, pipeline)
	if err != nil {
		log.Printf("commit feed: aggregation failed: %v", err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	defer cursor.Close(ctx)

	items := []feedItem{}
	if err := cursor.All(ctx, &items); err != nil {
		log.Printf("commit feed: decoding commits failed: %v", err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	nextCursor := ""
	if len(items) == limit {
		nextCursor = items[len(items)-1].CommittedAt.Format(time.RFC3339Nano)
	}

	c.JSON(http.StatusOK, gin.H{"commits": items, "nextCursor": nextCursor})
}
