package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const contextUserIDKey = "userID"

// Middleware verifies the session JWT cookie and stores the user ID in the
// request context for handlers to read via UserIDFromContext.
func Middleware(jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		cookie, err := c.Cookie(SessionCookieName)
		if err != nil || cookie == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
			return
		}

		userID, err := ParseSessionJWT(jwtSecret, cookie)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
			return
		}

		c.Set(contextUserIDKey, userID)
		c.Next()
	}
}

// UserIDFromContext reads the user ID that Middleware stored.
func UserIDFromContext(c *gin.Context) (string, bool) {
	v, ok := c.Get(contextUserIDKey)
	if !ok {
		return "", false
	}
	id, ok := v.(string)
	return id, ok
}
