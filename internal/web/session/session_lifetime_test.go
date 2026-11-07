package session

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
)

func sessionTestDB(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	dbtest.InitDB(t, filepath.Join(dbDir, "x-ui.db"))
}

// sessionTestPanel serves login, an authenticated probe, the CSRF endpoint and
// logout over the store the panel builds at start, with lifetimeSeconds as the
// configured session max age. Building it again is a panel restart.
func sessionTestPanel(t *testing.T, lifetimeSeconds int) *gin.Engine {
	t.Helper()
	router := gin.New()
	router.Use(sessions.Sessions(sessionCookieName, NewStore([]byte("01234567890123456789012345678901"), sessions.Options{
		Path: "/", HttpOnly: true, MaxAge: lifetimeSeconds,
	})))
	router.GET("/login", func(c *gin.Context) {
		user := &model.User{}
		if err := database.GetDB().First(user).Error; err != nil {
			t.Fatalf("load first user: %v", err)
		}
		if err := SetLoginUser(c, user); err != nil {
			t.Fatalf("SetLoginUser: %v", err)
		}
		c.Status(http.StatusNoContent)
	})
	router.GET("/me", func(c *gin.Context) {
		if !IsLogin(c) {
			c.Status(http.StatusUnauthorized)
			return
		}
		c.Status(http.StatusOK)
	})
	router.GET("/csrf", func(c *gin.Context) {
		if _, err := EnsureCSRFToken(c); err != nil {
			t.Fatalf("EnsureCSRFToken: %v", err)
		}
		c.Status(http.StatusNoContent)
	})
	router.POST("/logout", func(c *gin.Context) {
		if err := ClearSession(c); err != nil {
			t.Fatalf("ClearSession: %v", err)
		}
		c.Status(http.StatusOK)
	})
	return router
}

func sessionLogin(t *testing.T, router *gin.Engine) []*http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login set no session cookie")
	}
	return cookies
}

func sessionRequest(router *gin.Engine, method, path string, cookies []*http.Cookie) int {
	code, _ := sessionExchange(router, method, path, cookies)
	return code
}

// sessionExchange also returns the cookies the response set, as a browser keeps them.
func sessionExchange(router *gin.Engine, method, path string, cookies []*http.Cookie) (int, []*http.Cookie) {
	req := httptest.NewRequest(method, path, nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if set := rec.Result().Cookies(); len(set) > 0 {
		return rec.Code, set
	}
	return rec.Code, cookies
}

// A copy of the cookie taken before logout (another tab, a proxy log, a stolen
// cookie) kept working: logout only told this browser to forget it.
func TestLogoutInvalidatesTheSessionButNotOtherLogins(t *testing.T) {
	sessionTestDB(t)
	router := sessionTestPanel(t, 3600)
	loggedOut := sessionLogin(t, router)
	other := sessionLogin(t, router)

	if code := sessionRequest(router, http.MethodGet, "/me", loggedOut); code != http.StatusOK {
		t.Fatalf("fresh session status = %d, want 200", code)
	}
	sessionRequest(router, http.MethodPost, "/logout", loggedOut)

	if code := sessionRequest(router, http.MethodGet, "/me", loggedOut); code != http.StatusUnauthorized {
		t.Fatalf("replayed cookie after logout status = %d, want 401", code)
	}
	if code := sessionRequest(router, http.MethodGet, "/me", other); code != http.StatusOK {
		t.Fatalf("another login's session status = %d after an unrelated logout, want 200", code)
	}
}

// A failed revocation returned before the cookie was cleared, so the browser kept
// a live session while logout reported success.
func TestLogoutClearsTheCookieWhenRevocationFails(t *testing.T) {
	sessionTestDB(t)
	router := sessionTestPanel(t, 3600)
	var logoutErr error
	router.POST("/logout-broken-db", func(c *gin.Context) {
		logoutErr = ClearSession(c)
		c.Status(http.StatusOK)
	})
	cookies := sessionLogin(t, router)
	if err := database.GetDB().Migrator().DropTable(&model.RevokedSession{}); err != nil {
		t.Fatalf("drop revoked_sessions: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/logout-broken-db", nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if logoutErr == nil {
		t.Fatal("ClearSession hid the failed revocation")
	}
	cleared := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == sessionCookieName && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("logout left the session cookie in the browser; Set-Cookie: %q", rec.Header().Values("Set-Cookie"))
	}
}

// The configured session lifetime only reached the browser's cookie expiry; the
// server accepted the same cookie for gorilla's default 30 days.
func TestSessionLifetimeIsEnforcedByTheServer(t *testing.T) {
	sessionTestDB(t)
	router := sessionTestPanel(t, 1)
	cookies := sessionLogin(t, router)
	if code := sessionRequest(router, http.MethodGet, "/me", cookies); code != http.StatusOK {
		t.Fatalf("fresh session status = %d, want 200", code)
	}
	time.Sleep(2100 * time.Millisecond)
	if code := sessionRequest(router, http.MethodGet, "/me", cookies); code != http.StatusUnauthorized {
		t.Fatalf("session older than its lifetime status = %d, want 401", code)
	}
}

// The revocation must outlive the process: after a panel restart the replayed
// copy of a logged-out cookie must still be refused.
func TestLogoutSurvivesAPanelRestart(t *testing.T) {
	sessionTestDB(t)
	router := sessionTestPanel(t, 3600)
	cookies := sessionLogin(t, router)
	sessionRequest(router, http.MethodPost, "/logout", cookies)

	restarted := sessionTestPanel(t, 3600)
	if code := sessionRequest(restarted, http.MethodGet, "/me", cookies); code != http.StatusUnauthorized {
		t.Fatalf("logged-out cookie after a panel restart status = %d, want 401", code)
	}
}

// Fetching a CSRF token saves the session again with a fresh cookie timestamp;
// that must not bring a logged-out session, or an expired one, back to life.
func TestCSRFTokenRequestDoesNotReviveASession(t *testing.T) {
	t.Run("logged out", func(t *testing.T) {
		sessionTestDB(t)
		router := sessionTestPanel(t, 3600)
		cookies := sessionLogin(t, router)
		sessionRequest(router, http.MethodPost, "/logout", cookies)

		_, refreshed := sessionExchange(router, http.MethodGet, "/csrf", cookies)
		if code := sessionRequest(router, http.MethodGet, "/me", refreshed); code != http.StatusUnauthorized {
			t.Fatalf("logged-out session after a CSRF request status = %d, want 401", code)
		}
		if code := sessionRequest(sessionTestPanel(t, 3600), http.MethodGet, "/me", refreshed); code != http.StatusUnauthorized {
			t.Fatalf("logged-out session after a CSRF request and a restart status = %d, want 401", code)
		}
	})
	t.Run("past its lifetime", func(t *testing.T) {
		sessionTestDB(t)
		router := sessionTestPanel(t, 2)
		cookies := sessionLogin(t, router)
		// Re-saved within the lifetime, the cookie's own timestamp starts over.
		time.Sleep(1500 * time.Millisecond)
		_, refreshed := sessionExchange(router, http.MethodGet, "/csrf", cookies)
		time.Sleep(1500 * time.Millisecond)
		if code := sessionRequest(router, http.MethodGet, "/me", refreshed); code != http.StatusUnauthorized {
			t.Fatalf("session past its lifetime since login status = %d, want 401", code)
		}
	})
}
