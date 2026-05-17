package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func APIKeyAuth(authenticator *Authenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := extractAPIKey(c)
		if key == "" || !authenticator.Validate(c.Request.Context(), key) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{
					"message": "Incorrect API key provided",
					"type":    "invalid_request_error",
					"code":    "invalid_api_key",
				},
			})
			return
		}
		c.Set("api_key", key)
		c.Next()
	}
}

func extractAPIKey(c *gin.Context) string {
	if h := c.GetHeader("Authorization"); h != "" {
		const prefix = "Bearer "
		if strings.HasPrefix(h, prefix) {
			return strings.TrimSpace(h[len(prefix):])
		}
	}
	if k := c.GetHeader("X-Api-Key"); k != "" {
		return strings.TrimSpace(k)
	}
	return ""
}
