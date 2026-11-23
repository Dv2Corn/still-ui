package model

// AuditLog is one admin action in the tamper-evident journal. Hash is the SHA-256 of PrevHash
// and the entry's canonical content, so an edited or deleted row breaks the chain after it.
type AuditLog struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement" example:"42"`
	// Time is unix milliseconds, UTC.
	Time  int64  `json:"time" gorm:"not null;default:0;index" example:"1735689600000"`
	Actor string `json:"actor" gorm:"not null;default:'';index" example:"admin"`
	IP    string `json:"ip" gorm:"column:ip;not null;default:''" example:"203.0.113.9"`
	// Action is the method and route template, or LOGIN / PRUNE.
	Action  string `json:"action" gorm:"not null;default:'';index" example:"POST /panel/api/inbounds/update/:id"`
	Target  string `json:"target" gorm:"not null;default:''" example:"id=7"`
	Outcome string `json:"outcome" gorm:"not null;default:''" example:"success"`
	Detail  string `json:"detail" gorm:"not null;default:''" example:"HTTP 200"`
	// PrevHash and Hash are hex SHA-256 digests.
	PrevHash string `json:"prevHash" gorm:"column:prev_hash;not null;default:''"`
	Hash     string `json:"hash" gorm:"not null;default:''"`
}
