package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const sessionTTL = 24 * time.Hour

// SessionCookieName is the httpOnly cookie carrying the signed session JWT.
const SessionCookieName = "session"

type sessionClaims struct {
	jwt.RegisteredClaims
}

// IssueSessionJWT signs a short-lived token identifying userID (the user's
// Mongo ObjectID as a hex string).
func IssueSessionJWT(secret, userID string) (string, error) {
	claims := sessionClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(sessionTTL)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// ParseSessionJWT verifies the token and returns the embedded user ID.
func ParseSessionJWT(secret, tokenStr string) (string, error) {
	claims := &sessionClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil {
		return "", err
	}
	if !token.Valid {
		return "", errors.New("invalid token")
	}
	return claims.Subject, nil
}
