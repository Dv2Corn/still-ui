package model

// NodeInvite is a one-time invitation that lets a new server add itself as a
// node (Nodes → Add with a command). Only the SHA-256 of its token is stored;
// the invitation string is shown once. Rows stay as the audit trail of who
// joined, when and from where, until they are pruned a month after use.
type NodeInvite struct {
	Id        int    `json:"id" gorm:"primaryKey;autoIncrement"`
	TokenHash string `json:"-" gorm:"column:token_hash;uniqueIndex;not null"`
	// Name and Address, when set by the admin, win over what the node reports.
	Name                string `json:"name" gorm:"not null;default:''"`
	Address             string `json:"address" gorm:"not null;default:''"`
	AllowPrivateAddress bool   `json:"allowPrivateAddress" gorm:"not null;default:false"`
	// NodeAdmin makes the node issue a node-admin token, so the master may
	// also update the node's panel; node-sync otherwise.
	NodeAdmin bool   `json:"nodeAdmin" gorm:"column:node_admin;not null;default:false"`
	MasterUrl string `json:"masterUrl" gorm:"column:master_url;not null;default:''"`
	CreatedAt int64  `json:"createdAt" gorm:"not null;default:0"`
	ExpiresAt int64  `json:"expiresAt" gorm:"not null;default:0"`
	// ClaimedAt marks an enrollment in progress, so a replay of the same token
	// cannot run a second one alongside it.
	ClaimedAt  int64  `json:"-" gorm:"column:claimed_at;not null;default:0"`
	Attempts   int    `json:"attempts" gorm:"not null;default:0"`
	UsedAt     int64  `json:"usedAt" gorm:"not null;default:0"`
	UsedFromIp string `json:"usedFromIp" gorm:"column:used_from_ip;not null;default:''"`
	NodeId     int    `json:"nodeId" gorm:"not null;default:0"`
	RevokedAt  int64  `json:"revokedAt" gorm:"not null;default:0"`
	LastError  string `json:"lastError" gorm:"not null;default:''"`
}
