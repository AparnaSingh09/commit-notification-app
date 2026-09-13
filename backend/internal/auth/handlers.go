package auth

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"golang.org/x/oauth2"

	"commit-notification-app/backend/internal/bitbucket"
	"commit-notification-app/backend/internal/config"
	"commit-notification-app/backend/internal/crypto"
	appdb "commit-notification-app/backend/internal/db"
)

// Handlers holds the dependencies the auth HTTP handlers need.
type Handlers struct {
	cfg           config.Config
	oauthCfg      *oauth2.Config
	db            *mongo.Database
	sessionSecret string // ephemeral - see NewEphemeralSessionSecret
}

func NewHandlers(cfg config.Config, oauthCfg *oauth2.Config, db *mongo.Database, sessionSecret string) *Handlers {
	return &Handlers{cfg: cfg, oauthCfg: oauthCfg, db: db, sessionSecret: sessionSecret}
}

// Login redirects the browser to Bitbucket's OAuth authorize page.
func (h *Handlers) Login(c *gin.Context) {
	state, err := generateState()
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "could not start login"})
		return
	}

	// short-lived, httpOnly - just here to be compared back in Callback
	c.SetCookie(stateCookieName, state, 300, "/auth", "", false, true)

	c.Redirect(http.StatusFound, h.oauthCfg.AuthCodeURL(state))
}

// Callback handles Bitbucket's redirect back after the user approves access:
// validates state, exchanges the code for tokens, fetches the Bitbucket
// profile, upserts the user, and issues our own session JWT cookie.
func (h *Handlers) Callback(c *gin.Context) {
	ctx := c.Request.Context()

	cookieState, err := c.Cookie(stateCookieName)
	if err != nil || cookieState == "" || cookieState != c.Query("state") {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid oauth state"})
		return
	}
	c.SetCookie(stateCookieName, "", -1, "/auth", "", false, true) // clear it, one-time use

	code := c.Query("code")
	if code == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "missing code"})
		return
	}

	token, err := h.oauthCfg.Exchange(ctx, code)
	if err != nil {
		log.Printf("oauth exchange failed: %v", err)
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "could not complete bitbucket login"})
		return
	}

	httpClient := h.oauthCfg.Client(ctx, token)
	bbUser, err := bitbucket.FetchCurrentUser(httpClient)
	if err != nil {
		log.Printf("fetching bitbucket user failed: %v", err)
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "could not fetch bitbucket profile"})
		return
	}

	key := crypto.DeriveKey(h.cfg.JWTSecret)
	accessEnc, err := crypto.Encrypt(key, token.AccessToken)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	refreshEnc, err := crypto.Encrypt(key, token.RefreshToken)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	now := time.Now()
	filter := bson.M{"bitbucketUuid": bbUser.UUID}
	update := bson.M{
		"$set": bson.M{
			"bitbucketUuid":   bbUser.UUID,
			"username":        bbUser.Username,
			"displayName":     bbUser.DisplayName,
			"avatarUrl":       bbUser.Links.Avatar.Href,
			"accessTokenEnc":  accessEnc,
			"refreshTokenEnc": refreshEnc,
			"tokenExpiresAt":  token.Expiry,
			"updatedAt":       now,
		},
		"$setOnInsert": bson.M{
			"createdAt": now,
		},
	}
	opts := options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)

	var user appdb.User
	err = h.db.Collection("users").FindOneAndUpdate(ctx, filter, update, opts).Decode(&user)
	if err != nil {
		log.Printf("upserting user failed: %v", err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	sessionJWT, err := IssueSessionJWT(h.sessionSecret, user.ID.Hex())
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.SetCookie(SessionCookieName, sessionJWT, int(sessionTTL.Seconds()), "/", "", false, true)

	c.Redirect(http.StatusFound, h.cfg.FrontendURL)
}

// Logout clears the session cookie. It doesn't need Middleware - logging out
// an already-invalid/expired session should still succeed (nothing to do
// server-side beyond clearing the cookie, since sessions are stateless JWTs).
func (h *Handlers) Logout(c *gin.Context) {
	c.SetCookie(SessionCookieName, "", -1, "/", "", false, true)
	c.Status(http.StatusNoContent)
}

// Me returns the current authenticated user's public profile.
// Requires Middleware to have run first.
func (h *Handlers) Me(c *gin.Context) {
	userID, ok := UserIDFromContext(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}

	oid, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}

	var user appdb.User
	err = h.db.Collection("users").FindOne(context.Background(), bson.M{"_id": oid}).Decode(&user)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":          user.ID.Hex(),
		"username":    user.Username,
		"displayName": user.DisplayName,
		"avatarUrl":   user.AvatarURL,
	})
}
