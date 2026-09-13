package auth

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"golang.org/x/oauth2"

	"commit-notification-app/backend/internal/config"
	"commit-notification-app/backend/internal/crypto"
	appdb "commit-notification-app/backend/internal/db"
)

// ValidToken returns a usable Bitbucket access token for user, transparently
// refreshing it (and persisting the new tokens) if it has expired.
// Callers that need to hit the Bitbucket API (repo validation, commit
// polling) should go through this instead of reading user.AccessTokenEnc
// directly.
func ValidToken(ctx context.Context, cfg config.Config, oauthCfg *oauth2.Config, db *mongo.Database, user *appdb.User) (*oauth2.Token, error) {
	key := crypto.DeriveKey(cfg.JWTSecret)

	accessTok, err := crypto.Decrypt(key, user.AccessTokenEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypting access token: %w", err)
	}
	refreshTok, err := crypto.Decrypt(key, user.RefreshTokenEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypting refresh token: %w", err)
	}

	current := &oauth2.Token{
		AccessToken:  accessTok,
		RefreshToken: refreshTok,
		Expiry:       user.TokenExpiresAt,
	}

	// oauth2's reuseTokenSource only calls out to Bitbucket if current is
	// already expired (with a small skew) - otherwise it just hands back
	// `current` unchanged.
	fresh, err := oauthCfg.TokenSource(ctx, current).Token()
	if err != nil {
		return nil, fmt.Errorf("refreshing bitbucket token: %w", err)
	}

	if fresh.AccessToken == current.AccessToken {
		return fresh, nil // no refresh happened
	}

	newAccessEnc, err := crypto.Encrypt(key, fresh.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("encrypting refreshed access token: %w", err)
	}
	// Bitbucket may or may not rotate the refresh token - keep the old one if absent.
	refreshToStore := fresh.RefreshToken
	if refreshToStore == "" {
		refreshToStore = refreshTok
	}
	newRefreshEnc, err := crypto.Encrypt(key, refreshToStore)
	if err != nil {
		return nil, fmt.Errorf("encrypting refreshed refresh token: %w", err)
	}

	_, err = db.Collection("users").UpdateOne(ctx,
		bson.M{"_id": user.ID},
		bson.M{"$set": bson.M{
			"accessTokenEnc":  newAccessEnc,
			"refreshTokenEnc": newRefreshEnc,
			"tokenExpiresAt":  fresh.Expiry,
			"updatedAt":       time.Now(),
		}},
	)
	if err != nil {
		return nil, fmt.Errorf("persisting refreshed token: %w", err)
	}

	return fresh, nil
}
