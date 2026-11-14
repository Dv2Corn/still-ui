package session

import (
	"encoding/gob"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
)

const (
	loginUserKey      = "LOGIN_USER"
	loginEpochKey     = "LOGIN_EPOCH"
	loginSessionKey   = "LOGIN_SID"
	loginAtKey        = "LOGIN_AT"
	apiAuthUserKey    = "api_auth_user"
	sessionCookieName = "3x-ui"
)

func init() {
	gob.Register(model.User{})
}

func SetLoginUser(c *gin.Context, user *model.User) error {
	if user == nil {
		return nil
	}
	s := sessions.Default(c)
	// Anyone can mint the anonymous session this request carried and plant it in a browser,
	// so nothing in it, the CSRF token above all, outlives the privilege change.
	s.Clear()
	id, err := newSessionID()
	if err != nil {
		return err
	}
	s.Set(loginUserKey, user.Id)
	s.Set(loginEpochKey, user.LoginEpoch)
	// Logout revokes this id, so a copy of the cookie dies with it, and the
	// lifetime counts from this login however often the cookie is saved again.
	s.Set(loginSessionKey, id)
	s.Set(loginAtKey, time.Now().Unix())
	return s.Save()
}

func SetAPIAuthUser(c *gin.Context, user *model.User) {
	if user == nil {
		return
	}
	c.Set(apiAuthUserKey, user)
}

func GetLoginUser(c *gin.Context) *model.User {
	if v, ok := c.Get(apiAuthUserKey); ok {
		if u, ok2 := v.(*model.User); ok2 {
			return u
		}
	}
	s := sessions.Default(c)
	obj := s.Get(loginUserKey)
	if obj == nil {
		return nil
	}
	userID, ok := sessionUserID(obj)
	sid, _ := s.Get(loginSessionKey).(string)
	if ok && (sid == "" || loginExpired(s.Get(loginAtKey), time.Now()) || sessionRevoked(sid)) {
		// Logged out, past its lifetime, or minted before sessions carried an id.
		dropLogin(s)
		if err := s.Save(); err != nil {
			logger.Warning("session: failed to drop ended session:", err)
		}
		return nil
	}
	if !ok {
		s.Delete(loginUserKey)
		s.Delete(loginEpochKey)
		if err := s.Save(); err != nil {
			logger.Warning("session: failed to drop stale user payload:", err)
		}
		return nil
	}
	if legacyUserID, ok := legacySessionUserID(obj); ok {
		s.Set(loginUserKey, legacyUserID)
		if err := s.Save(); err != nil {
			logger.Warning("session: failed to migrate legacy user payload:", err)
		}
	}
	user, err := getUserByID(userID)
	if err != nil {
		logger.Warning("session: failed to load user:", err)
		s.Delete(loginUserKey)
		s.Delete(loginEpochKey)
		if saveErr := s.Save(); saveErr != nil {
			logger.Warning("session: failed to drop missing user:", saveErr)
		}
		return nil
	}
	if !sessionEpochMatches(s.Get(loginEpochKey), user.LoginEpoch) {
		s.Delete(loginUserKey)
		s.Delete(loginEpochKey)
		if saveErr := s.Save(); saveErr != nil {
			logger.Warning("session: failed to drop stale epoch:", saveErr)
		}
		return nil
	}
	return user
}

func sessionEpochMatches(cookieVal any, userEpoch int64) bool {
	var got int64
	switch v := cookieVal.(type) {
	case nil:
	case int64:
		got = v
	case int:
		got = int64(v)
	case int32:
		got = int64(v)
	case float64:
		got = int64(v)
	default:
		return false
	}
	return got == userEpoch
}

func IsLogin(c *gin.Context) bool {
	return GetLoginUser(c) != nil
}

// Login is the cookie session a request authenticated with, kept so a connection
// that outlives the request can be checked again later.
type Login struct {
	userID int
	epoch  any
	sid    string
	at     any
}

// CurrentLogin is the request's cookie login once GetLoginUser accepts it; an API token gives none.
func CurrentLogin(c *gin.Context) (Login, bool) {
	if _, api := c.Get(apiAuthUserKey); api || GetLoginUser(c) == nil {
		return Login{}, false
	}
	s := sessions.Default(c)
	userID, _ := sessionUserID(s.Get(loginUserKey))
	sid, _ := s.Get(loginSessionKey).(string)
	return Login{userID: userID, epoch: s.Get(loginEpochKey), sid: sid, at: s.Get(loginAtKey)}, true
}

// Valid repeats GetLoginUser's checks: not logged out, within its lifetime, same login epoch.
func (l Login) Valid() bool {
	if l.sid == "" || loginExpired(l.at, time.Now()) || sessionRevoked(l.sid) {
		return false
	}
	user, err := getUserByID(l.userID)
	return err == nil && sessionEpochMatches(l.epoch, user.LoginEpoch)
}

var (
	revokedMu sync.Mutex
	revokedCh = make(chan struct{})
)

// Revoked is closed by the next NotifyRevoked, so open connections re-check their Login at once.
func Revoked() <-chan struct{} {
	revokedMu.Lock()
	defer revokedMu.Unlock()
	return revokedCh
}

// NotifyRevoked follows every logout and login-epoch change.
func NotifyRevoked() {
	revokedMu.Lock()
	defer revokedMu.Unlock()
	close(revokedCh)
	revokedCh = make(chan struct{})
}

func sessionUserID(obj any) (int, bool) {
	switch v := obj.(type) {
	case int:
		return v, v > 0
	case int64:
		return int(v), v > 0
	case int32:
		return int(v), v > 0
	case float64:
		id := int(v)
		return id, v == float64(id) && id > 0
	case model.User:
		return v.Id, v.Id > 0
	case *model.User:
		if v == nil {
			return 0, false
		}
		return v.Id, v.Id > 0
	default:
		return 0, false
	}
}

func legacySessionUserID(obj any) (int, bool) {
	switch v := obj.(type) {
	case model.User:
		return v.Id, v.Id > 0
	case *model.User:
		if v == nil {
			return 0, false
		}
		return v.Id, v.Id > 0
	default:
		return 0, false
	}
}

func getUserByID(id int) (*model.User, error) {
	db := database.GetDB()
	if db == nil {
		return nil, http.ErrServerClosed
	}
	user := &model.User{}
	if err := db.Model(model.User{}).Where("id = ?", id).First(user).Error; err != nil {
		return nil, err
	}
	return user, nil
}

// dropLogin removes the login from a session, keeping anything anonymous in it.
func dropLogin(s sessions.Session) {
	s.Delete(loginUserKey)
	s.Delete(loginEpochKey)
	s.Delete(loginSessionKey)
	s.Delete(loginAtKey)
}

// ClearSession always tells the browser to drop the cookie; a failed revocation is
// returned, since a copy of the cookie taken earlier then stays valid.
func ClearSession(c *gin.Context) error {
	s := sessions.Default(c)
	var revokeErr error
	if sid, ok := s.Get(loginSessionKey).(string); ok {
		revokeErr = revokeSession(sid, s.Get(loginAtKey))
		NotifyRevoked()
	}
	s.Clear()
	cookiePath := c.GetString("base_path")
	if cookiePath == "" {
		cookiePath = "/"
	}
	secure := c.Request.TLS != nil
	s.Options(sessions.Options{
		Path:     cookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	if err := s.Save(); err != nil {
		return errors.Join(revokeErr, err)
	}
	if cookiePath != "/" {
		http.SetCookie(c.Writer, &http.Cookie{
			Name:     sessionCookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			Expires:  time.Unix(0, 0),
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		})
	}
	return revokeErr
}
