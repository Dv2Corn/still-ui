package config

// still-ui: user-visible names of this fork of 3x-ui (MHSanaei, GPL-3.0).
//
// Internal names -- the Go module path, the x-ui binary, the x-ui.db database
// file and the XUI_* environment variables -- stay 3x-ui's so upstream changes
// merge cleanly. What an operator sees on the host is still-ui's: the systemd
// service, the install, database and log folders, and the GitHub repository the
// installer and the in-panel updater download releases from.
const (
	// ServiceName is the systemd (and OpenRC) service the installer creates.
	ServiceName = "still-ui"
	// DefaultMainFolder is where the installer unpacks a release (XUI_MAIN_FOLDER).
	DefaultMainFolder = "/usr/local/still-ui"
	// DefaultDBFolder holds x-ui.db on Linux (XUI_DB_FOLDER overrides it).
	DefaultDBFolder = "/etc/still-ui"
	// DefaultLogFolder holds the panel and Xray logs (XUI_LOG_FOLDER overrides it).
	DefaultLogFolder = "/var/log/still-ui"
	// ReleaseRepo is the GitHub owner/name that publishes still-ui releases.
	ReleaseRepo = "Dv2Corn/still-ui"
	// UpstreamRepo is the project still-ui is based on.
	UpstreamRepo = "MHSanaei/3x-ui"
)
