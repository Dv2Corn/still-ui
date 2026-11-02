package controller

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
)

// The web base path is injected into the page shell twice: into asset src/href
// attributes and into an inline script. A quote or </script> in it broke out of
// both and left the login page blank (or ran whatever followed).
func TestServeDistPageKeepsTheBasePathInsideItsContext(t *testing.T) {
	prev := distFS
	t.Cleanup(func() { distFS = prev })
	SetDistFS(fstest.MapFS{"dist/login.html": {Data: []byte(
		`<html><head><script type="module" src="/assets/app.js"></script><link href="/assets/app.css"></head><body></body></html>`)}})

	const base = `/p"x</script><script>alert(1)</script>&/`
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("3x-ui", cookie.NewStore([]byte("01234567890123456789012345678901"))))
	router.GET("/login", func(c *gin.Context) {
		c.Set("base_path", base)
		serveDistPage(c, "login.html")
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	body, _ := io.ReadAll(rec.Body)
	page := string(body)

	if strings.Contains(page, `<script>alert(1)`) {
		t.Fatalf("base path closed the inline script:\n%s", page)
	}
	if strings.Contains(page, `src="/p"x`) || strings.Contains(page, `href="/p"x`) {
		t.Fatalf("base path closed an asset attribute:\n%s", page)
	}
	if !strings.Contains(page, `src="/p&#34;x&lt;/script&gt;`) {
		t.Fatalf("asset src does not carry the escaped base path:\n%s", page)
	}
	if !strings.Contains(page, `window.X_UI_BASE_PATH="/p\"x\u003c/script\u003e\u003cscript\u003ealert(1)\u003c/script\u003e\u0026/"`) {
		t.Fatalf("inline script does not carry the JS-escaped base path:\n%s", page)
	}
}
