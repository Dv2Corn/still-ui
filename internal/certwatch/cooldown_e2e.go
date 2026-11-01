//go:build e2e

package certwatch

import (
	"os"
	"time"
)

// STILL_CERT_RESTART_COOLDOWN lets an end-to-end test watch several cooldowns in minutes.
func init() {
	if d, err := time.ParseDuration(os.Getenv("STILL_CERT_RESTART_COOLDOWN")); err == nil && d >= 10*time.Second {
		restartCooldown = d
	}
}
