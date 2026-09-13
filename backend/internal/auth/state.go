package auth

import (
	"crypto/rand"
	"encoding/base64"
)

// stateCookieName holds the CSRF state value between /auth/login and
// /auth/callback. Short-lived and httpOnly - never read by JS.
const stateCookieName = "oauth_state"

func generateState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
