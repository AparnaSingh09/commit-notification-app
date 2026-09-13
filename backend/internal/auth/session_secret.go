package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// NewEphemeralSessionSecret generates a fresh random secret each time the
// process starts, used only to sign/verify session JWTs (never persisted,
// never derived from JWT_SECRET).
//
// This is a deliberate, temporary stand-in for a real logout: until a
// logout endpoint exists, this is what forces every user back to the login
// screen whenever the backend restarts - any cookie signed with a previous
// run's secret fails verification. Restarting the backend mid-session is
// the only thing affected; the session stays valid for as long as the
// process keeps running (up to the normal JWT expiry).
//
// It is intentionally NOT used for anything else - JWT_SECRET from .env
// still derives the key that encrypts stored Bitbucket tokens (see
// internal/crypto), which must stay stable across restarts or previously
// stored tokens would become undecryptable.
func NewEphemeralSessionSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating session secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
