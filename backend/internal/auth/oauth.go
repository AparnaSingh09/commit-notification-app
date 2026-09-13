package auth

import (
	"golang.org/x/oauth2"

	"commit-notification-app/backend/internal/config"
)

// NewOAuthConfig builds the golang.org/x/oauth2 config for Bitbucket, using
// values from the environment (see internal/config).
func NewOAuthConfig(cfg config.Config) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     cfg.BitbucketClientID,
		ClientSecret: cfg.BitbucketClientSecret,
		RedirectURL:  cfg.BitbucketCallbackURL,
		Scopes:       []string{"account", "repository"},
		Endpoint: oauth2.Endpoint{
			AuthURL:   "https://bitbucket.org/site/oauth2/authorize",
			TokenURL:  "https://bitbucket.org/site/oauth2/access_token",
			AuthStyle: oauth2.AuthStyleInHeader, // Bitbucket expects HTTP Basic auth for the token exchange
		},
	}
}
