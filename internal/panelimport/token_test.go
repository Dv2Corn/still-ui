package panelimport

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/panelimport/marzbantest"
)

func TestVerifyMarzbanToken(t *testing.T) {
	cases := []struct {
		name, secret, token string
		user                string
		issued              int64
		ok                  bool
	}{
		{"current format", marzbantest.Secret, marzbantest.AliceToken, "alice", 1760000000, true},
		{"old JWT format", marzbantest.Secret, marzbantest.AliceJWT, "alice", 1760000000, true},
		{"another panel's secret", "0000" + marzbantest.Secret[4:], marzbantest.AliceToken, "", 0, false},
		{"JWT with another secret", "0000" + marzbantest.Secret[4:], marzbantest.AliceJWT, "", 0, false},
		{"one signature character changed", marzbantest.Secret, marzbantest.AliceToken[:len(marzbantest.AliceToken)-1] + "x", "", 0, false},
		{"user name changed, signature kept", marzbantest.Secret, "Ym9iLDE3NjAwMDAwMDA" + marzbantest.AliceToken[len(marzbantest.AliceToken)-10:], "", 0, false},
		{"a still-ui subscription id", marzbantest.Secret, "c9735a7b68cab525", "", 0, false},
		{"no secret stored", "", marzbantest.AliceToken, "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user, issued, ok := VerifyMarzbanToken(tc.secret, tc.token)
			if user != tc.user || issued != tc.issued || ok != tc.ok {
				t.Fatalf("got (%q, %d, %v), want (%q, %d, %v)", user, issued, ok, tc.user, tc.issued, tc.ok)
			}
		})
	}
}
