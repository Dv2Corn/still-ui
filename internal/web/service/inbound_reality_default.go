package service

import "encoding/json"

// DefaultRealityFingerprint is the uTLS fingerprint a new REALITY inbound gets
// when the request leaves it unset (still-ui). The "ios" ClientHello is ~517
// bytes and fits a single TCP segment; chrome/firefox send ~1800 bytes, and
// mobile networks that lose the second segment of the handshake stall the
// connection before it starts. Only new inbounds are affected: an existing
// inbound keeps whatever it stores, and an explicit value is never replaced.
const DefaultRealityFingerprint = "ios"

// applyNewRealityFingerprintDefault returns streamSettings with
// realitySettings.settings.fingerprint set to DefaultRealityFingerprint when the
// inbound uses REALITY and the fingerprint is missing or empty. Anything else
// (not REALITY, unparsable JSON, an explicit fingerprint) is returned unchanged.
func applyNewRealityFingerprintDefault(streamSettings string) string {
	if streamSettings == "" {
		return streamSettings
	}
	var stream map[string]any
	if err := json.Unmarshal([]byte(streamSettings), &stream); err != nil {
		return streamSettings
	}
	if security, _ := stream["security"].(string); security != "reality" {
		return streamSettings
	}
	reality, ok := stream["realitySettings"].(map[string]any)
	if !ok {
		return streamSettings
	}
	settings, ok := reality["settings"].(map[string]any)
	if !ok {
		if reality["settings"] != nil {
			return streamSettings
		}
		settings = map[string]any{}
		reality["settings"] = settings
	}
	if fp, _ := settings["fingerprint"].(string); fp != "" {
		return streamSettings
	}
	settings["fingerprint"] = DefaultRealityFingerprint
	out, err := json.MarshalIndent(stream, "", "  ")
	if err != nil {
		return streamSettings
	}
	return string(out)
}
