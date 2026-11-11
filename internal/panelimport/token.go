package panelimport

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
)

// marzbanJWTHeader is the fixed header of the HS256 subscription tokens old Marzban issued.
const marzbanJWTHeader = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9."

// VerifyMarzbanToken checks a Marzban subscription token against the panel's JWT secret
// (app/utils/jwt.py get_subscription_payload) and returns its user and issue time.
func VerifyMarzbanToken(secret, token string) (username string, issuedAt int64, ok bool) {
	if secret == "" || len(token) < 15 {
		return "", 0, false
	}
	if strings.HasPrefix(token, marzbanJWTHeader) {
		return verifyMarzbanJWT(secret, token)
	}
	body, sig := token[:len(token)-10], token[len(token)-10:]
	sum := sha256.Sum256([]byte(body + secret))
	want := base64.URLEncoding.EncodeToString(sum[:])[:10]
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return "", 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(body, "="))
	if err != nil {
		return "", 0, false
	}
	name, stamp, found := strings.Cut(string(raw), ",")
	issuedAt, err = strconv.ParseInt(stamp, 10, 64)
	if !found || err != nil || name == "" {
		return "", 0, false
	}
	return name, issuedAt, true
}

func verifyMarzbanJWT(secret, token string) (string, int64, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", 0, false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return "", 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", 0, false
	}
	var claims struct {
		Sub    string  `json:"sub"`
		Access string  `json:"access"`
		Iat    float64 `json:"iat"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Access != "subscription" || claims.Sub == "" {
		return "", 0, false
	}
	return claims.Sub, int64(claims.Iat), true
}
