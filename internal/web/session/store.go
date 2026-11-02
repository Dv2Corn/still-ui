package session

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"gorm.io/gorm/clause"
)

// defaultSessionLifetime is gorilla's securecookie default: how long the server
// takes a session cookie when the panel sets no lifetime of its own.
const defaultSessionLifetime = 30 * 24 * time.Hour

var sessionLifetime struct {
	sync.RWMutex
	d time.Duration
}

// NewStore is the panel's cookie store. A positive options.MaxAge (seconds) is
// the session lifetime: it bounds the signed cookie's age on the server too, and
// GetLoginUser counts it from the login itself, which re-saving cannot move.
func NewStore(secret []byte, options sessions.Options) cookie.Store {
	store := cookie.NewStore(secret)
	store.Options(options)
	lifetime := defaultSessionLifetime
	if options.MaxAge > 0 {
		if limited, ok := store.(interface{ MaxAge(int) }); ok {
			limited.MaxAge(options.MaxAge)
		}
		lifetime = time.Duration(options.MaxAge) * time.Second
	}
	sessionLifetime.Lock()
	sessionLifetime.d = lifetime
	sessionLifetime.Unlock()
	return store
}

func loginLifetime() time.Duration {
	sessionLifetime.RLock()
	defer sessionLifetime.RUnlock()
	if sessionLifetime.d <= 0 {
		return defaultSessionLifetime
	}
	return sessionLifetime.d
}

// loginExpired reports whether a session logged in at loginAt (unix seconds) has
// outlived the lifetime; a session without a login time predates it and is refused.
func loginExpired(loginAt any, now time.Time) bool {
	at, ok := loginAt.(int64)
	if !ok || at <= 0 {
		return true
	}
	return now.Sub(time.Unix(at, 0)) > loginLifetime()
}

// revokeSession records a logged-out session id in the database until its
// lifetime would have ended, and drops records that are past it.
func revokeSession(id string, loginAt any) error {
	db := database.GetDB()
	if id == "" || db == nil {
		return nil
	}
	now := time.Now()
	expires := now.Add(loginLifetime()).Unix()
	if at, ok := loginAt.(int64); ok && at > 0 {
		expires = time.Unix(at, 0).Add(loginLifetime()).Unix()
	}
	if err := db.Where("expires_at < ?", now.Unix()).Delete(&model.RevokedSession{}).Error; err != nil {
		return err
	}
	return db.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&model.RevokedSession{ID: id, ExpiresAt: expires}).Error
}

// sessionRevoked fails closed: a lookup error refuses the session.
func sessionRevoked(id string) bool {
	db := database.GetDB()
	if db == nil {
		return true
	}
	var count int64
	if err := db.Model(&model.RevokedSession{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return true
	}
	return count > 0
}

func newSessionID() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
