package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/rpmode"
)

// PrivateRPSessionMiddleware rejects unresolved frontend identity before routing.
// Model discovery does not have a conversation and is intentionally unaffected.
func PrivateRPSessionMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if c.Request.Method == http.MethodPost && (strings.HasSuffix(path, "/chat/completions") || strings.HasSuffix(path, "/responses")) {
			if _, _, err := rpmode.FrontendSession(c.Request.Header); err != nil {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": err.Error()}})
				return
			}
		}
		c.Next()
	}
}
