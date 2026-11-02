package totp

import (
	"encoding/base32"
	"strings"
	"time"

	"github.com/xlzd/gotp"
)

// SkewWindows is how many 30s steps around now VerifyWithSkew accepts.
// Standard TOTP clock-drift tolerance, see MHSanaei/3x-ui#6535.
const SkewWindows = 1

// NormalizeSecret turns a base32 key the way authenticator apps show it (lower
// case, grouped by spaces, padded) into the bare upper-case form gotp decodes.
func NormalizeSecret(secret string) string {
	secret = strings.ToUpper(strings.Join(strings.Fields(secret), ""))
	return strings.TrimRight(secret, "=")
}

// ValidSecret reports whether secret is a non-empty base32 key once normalized.
func ValidSecret(secret string) bool {
	secret = NormalizeSecret(secret)
	if secret == "" {
		return false
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	return err == nil && len(key) > 0
}

// VerifyWithSkew accepts the code for the current step plus/minus SkewWindows.
// A secret that is not base32 verifies nothing: gotp panics on it.
func VerifyWithSkew(secret, code string, now time.Time) bool {
	if !ValidSecret(secret) {
		return false
	}
	totp := gotp.NewDefaultTOTP(NormalizeSecret(secret))
	for i := -SkewWindows; i <= SkewWindows; i++ {
		if totp.AtTime(now.Add(time.Duration(i*30)*time.Second)) == code {
			return true
		}
	}
	return false
}
