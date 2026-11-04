package model

// ReachProbe is an external agent that checks the panel's inbounds from inside
// a censored network. Only the SHA-256 of its token is stored; the plaintext is
// shown once. ClientEmail names the panel-managed client the agent connects as.
type ReachProbe struct {
	Id           int    `gorm:"primaryKey;autoIncrement"`
	Name         string `gorm:"uniqueIndex;not null"`
	TokenHash    string `gorm:"column:token_hash;uniqueIndex;not null"`
	Enabled      bool   `gorm:"default:true"`
	ClientEmail  string `gorm:"column:client_email;not null"`
	InboundIds   []int  `gorm:"column:inbound_ids;serializer:json"`
	TestURL      string `gorm:"column:test_url;not null"`
	DirectURL    string `gorm:"column:direct_url;not null;default:''"`
	IntervalSec  int    `gorm:"column:interval_sec;not null;default:600"`
	LastSeenAt   int64  `gorm:"column:last_seen_at;not null;default:0"`
	AgentVersion string `gorm:"column:agent_version;not null;default:''"`
	StaleAlerted bool   `gorm:"column:stale_alerted;not null;default:false"`
	CreatedAt    int64  `gorm:"autoCreateTime:milli"`
	// PanelHost is the panel address the admin used when saving the probe; jobs are built for it,
	// never for the Host header an agent sends.
	PanelHost string `gorm:"column:panel_host;not null;default:''"`
}

// ReachProbeResult is one check of one target by one probe; At is unix ms.
// Rows older than the history window are pruned when the probe reports and by the hourly cleanup.
type ReachProbeResult struct {
	Id        int    `gorm:"primaryKey;autoIncrement"`
	ProbeId   int    `gorm:"column:probe_id;not null;index:idx_reach_results_probe_at,priority:1"`
	TargetKey string `gorm:"column:target_key;not null"`
	InboundId int    `gorm:"column:inbound_id;not null;default:0"`
	Label     string `gorm:"not null;default:''"`
	Ok        bool   `gorm:"not null"`
	ErrorText string `gorm:"column:error_text;not null;default:''"`
	TtfbMs    int    `gorm:"column:ttfb_ms;not null;default:0"`
	TotalMs   int    `gorm:"column:total_ms;not null;default:0"`
	Bytes     int64  `gorm:"not null;default:0"`
	At        int64  `gorm:"not null;index:idx_reach_results_probe_at,priority:2"`
}

// ReachProbeTarget is the alert state of one target of one probe, kept in the
// database so a panel restart neither forgets an outage nor re-sends its alert.
type ReachProbeTarget struct {
	Id               int    `gorm:"primaryKey;autoIncrement"`
	ProbeId          int    `gorm:"column:probe_id;not null;uniqueIndex:idx_reach_target,priority:1"`
	TargetKey        string `gorm:"column:target_key;not null;uniqueIndex:idx_reach_target,priority:2"`
	Status           string `gorm:"not null;default:'unknown'"`
	ConsecutiveFails int    `gorm:"column:consecutive_fails;not null;default:0"`
	ChangedAt        int64  `gorm:"column:changed_at;not null;default:0"`
}
