package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	ws "github.com/gorilla/websocket"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/panel"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
	"github.com/mhsanaei/3x-ui/v3/internal/web/websocket"
)

// The live-update feed carries client credentials, so a socket must end with the login that
// opened it: a logout or a login-epoch bump (password change, 2FA enrolment) closes it.
func TestWebSocketEndsWithItsLogin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		revoke func(t *testing.T, panelURL, cookie string)
	}{
		{"logout", func(t *testing.T, panelURL, cookie string) {
			req, _ := http.NewRequest(http.MethodGet, panelURL+"/logout", nil)
			req.Header.Set("Cookie", cookie)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("logout: %v", err)
			}
			res.Body.Close()
		}},
		{"login epoch bump", func(t *testing.T, _, _ string) {
			if err := (&panel.UserService{}).BumpLoginEpoch(); err != nil {
				t.Fatalf("bump login epoch: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			dir := t.TempDir()
			t.Setenv("XUI_DB_FOLDER", dir)
			dbtest.InitDB(t, filepath.Join(dir, "x-ui.db"))
			hub := websocket.NewHub()
			go hub.Run()
			t.Cleanup(hub.Stop)

			r := gin.New()
			r.Use(sessions.Sessions("3x-ui", session.NewStore([]byte("01234567890123456789012345678901"), sessions.Options{Path: "/", HttpOnly: true, MaxAge: 3600})))
			r.GET("/login", func(c *gin.Context) {
				user := &model.User{}
				if err := database.GetDB().First(user).Error; err != nil {
					t.Errorf("load user: %v", err)
				}
				if err := session.SetLoginUser(c, user); err != nil {
					t.Errorf("set login: %v", err)
				}
				c.Status(http.StatusOK)
			})
			r.GET("/logout", func(c *gin.Context) {
				if err := session.ClearSession(c); err != nil {
					t.Errorf("clear session: %v", err)
				}
				c.Status(http.StatusOK)
			})
			r.GET("/ws", NewWebSocketController(panel.NewWebSocketService(hub)).HandleWebSocket)
			srv := httptest.NewServer(r)
			t.Cleanup(srv.Close)

			res, err := http.Get(srv.URL + "/login")
			if err != nil {
				t.Fatalf("login: %v", err)
			}
			res.Body.Close()
			cookie := strings.SplitN(res.Header.Get("Set-Cookie"), ";", 2)[0]
			conn, _, err := ws.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", http.Header{"Cookie": {cookie}})
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			t.Cleanup(func() { conn.Close() })
			for i := 0; hub.GetClientCount() == 0 && i < 100; i++ {
				time.Sleep(10 * time.Millisecond)
			}

			tc.revoke(t, srv.URL, cookie)
			hub.Broadcast(websocket.MessageTypeNotification, map[string]string{"settings": "client-credential"})

			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			_, msg, err := conn.ReadMessage()
			var closed *ws.CloseError
			if !errors.As(err, &closed) || closed.Code != ws.ClosePolicyViolation {
				t.Fatalf("socket after %s: message %q, err %v; want a policy-violation close and no data", tc.name, msg, err)
			}
		})
	}
}
