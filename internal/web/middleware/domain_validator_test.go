package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func domainStatus(domain, host string) int {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(DomainValidatorMiddleware(domain))
	router.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = host
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec.Code
}

// Host names are case-insensitive and browsers send them lower-case, so a domain
// saved with capitals or a stray space locked every client out with 403.
func TestDomainValidatorComparesHostNamesCaseInsensitively(t *testing.T) {
	allowed := []struct{ domain, host string }{
		{"panel.example.com", "panel.example.com"},
		{"Panel.Example.com", "panel.example.com"},
		{"panel.example.com ", "panel.example.com:2053"},
		{"panel.example.com", "PANEL.example.com"},
		{"panel.example.com", "panel.example.com.:443"},
		{"2001:db8::1", "[2001:db8::1]:2053"},
	}
	for _, tc := range allowed {
		if code := domainStatus(tc.domain, tc.host); code != http.StatusOK {
			t.Errorf("domain %q, host %q: status %d, want 200", tc.domain, tc.host, code)
		}
	}
	refused := []struct{ domain, host string }{
		{"panel.example.com", "evil.example.com"},
		{"panel.example.com", "panel.example.com.evil.net"},
		{"panel.example.com", ""},
	}
	for _, tc := range refused {
		if code := domainStatus(tc.domain, tc.host); code != http.StatusForbidden {
			t.Errorf("domain %q, host %q: status %d, want 403", tc.domain, tc.host, code)
		}
	}
}
