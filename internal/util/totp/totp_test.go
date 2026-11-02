package totp

import (
	"testing"
	"time"

	"github.com/xlzd/gotp"
)

func TestVerifyWithSkew(t *testing.T) {
	secret := "JBSWY3DPEHPK3PXP"
	totp := gotp.NewDefaultTOTP(secret)
	// Anchor mid-window so a step boundary can't fall between sampling and verify.
	now := time.Unix((time.Now().Unix()/30)*30+15, 0).UTC()

	if !VerifyWithSkew(secret, totp.AtTime(now), now) {
		t.Fatal("current window code should verify")
	}
	if !VerifyWithSkew(secret, totp.AtTime(now.Add(-30*time.Second)), now) {
		t.Fatal("previous window code should verify (clock skew)")
	}
	if !VerifyWithSkew(secret, totp.AtTime(now.Add(30*time.Second)), now) {
		t.Fatal("next window code should verify (clock skew)")
	}
	if VerifyWithSkew(secret, totp.AtTime(now.Add(-60*time.Second)), now) {
		t.Fatal("code two windows old should not verify")
	}
	if VerifyWithSkew(secret, totp.AtTime(now.Add(60*time.Second)), now) {
		t.Fatal("code two windows ahead should not verify")
	}
	if VerifyWithSkew(secret, "000000", now) {
		t.Fatal("wrong code should not verify")
	}
}

// Authenticator apps show the key in lower case or in groups; gotp decodes only
// upper-case base32 and panics on anything else, which turned every login into a 500.
func TestVerifyWithSkewAcceptsDisplayFormsAndNeverPanics(t *testing.T) {
	const secret = "JBSWY3DPEHPK3PXP"
	now := time.Unix((time.Now().Unix()/30)*30+15, 0).UTC()
	code := gotp.NewDefaultTOTP(secret).AtTime(now)

	for _, display := range []string{"jbswy3dpehpk3pxp", "JBSW Y3DP EHPK 3PXP", " JBSWY3DPEHPK3PXP "} {
		if !VerifyWithSkew(display, code, now) {
			t.Fatalf("code for %q rejected", display)
		}
	}
	for _, bad := range []string{"", "   ", "totp-secret!", "18901890"} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("VerifyWithSkew(%q) panicked: %v", bad, r)
				}
			}()
			if VerifyWithSkew(bad, code, now) {
				t.Fatalf("secret %q accepted a code", bad)
			}
		}()
	}
}

func TestValidSecret(t *testing.T) {
	for _, ok := range []string{"JBSWY3DPEHPK3PXP", "jbswy3dpehpk3pxp", "JBSW Y3DP EHPK 3PXP"} {
		if !ValidSecret(ok) {
			t.Fatalf("ValidSecret(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "  ", "totp-secret", "ATTACKERSECRET567890"} {
		if ValidSecret(bad) {
			t.Fatalf("ValidSecret(%q) = true", bad)
		}
	}
	if got := NormalizeSecret(" jbsw y3dp ehpk 3pxp "); got != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("NormalizeSecret = %q", got)
	}
}
