// Package middleware provides HTTP middleware functions for the 3x-ui web panel,
// including domain validation utilities.
package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// DomainValidatorMiddleware returns a Gin middleware that validates the request domain.
// It extracts the host from the request, strips any port number, and compares it
// against the configured domain. Host names are case-insensitive and may carry
// the root's trailing dot, so both sides are compared in that normal form.
// Requests from unauthorized domains are rejected with HTTP 403 Forbidden status.
func DomainValidatorMiddleware(domain string) gin.HandlerFunc {
	domain = normalizeHost(domain)
	return func(c *gin.Context) {
		host := c.Request.Host
		if colonIndex := strings.LastIndex(host, ":"); colonIndex != -1 {
			host, _, _ = net.SplitHostPort(c.Request.Host)
		}

		if normalizeHost(host) != domain {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}

		c.Next()
	}
}

func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}
