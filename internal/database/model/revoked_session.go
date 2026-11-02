package model

// RevokedSession marks a logged-out panel session, so a copy of its cookie is
// refused even after a restart. ExpiresAt (unix seconds) is when the session's
// lifetime would have ended anyway; the row can go after that.
type RevokedSession struct {
	ID        string `gorm:"primaryKey"`
	ExpiresAt int64  `gorm:"not null;index"`
}
