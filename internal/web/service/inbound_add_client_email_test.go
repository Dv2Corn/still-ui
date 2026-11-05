package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// #55 review: /clients/add refused the reserved names, while /inbounds/add
// still created exactly those records from the inbound's settings.
func TestAddInboundRefusesClientNamesTheClientAPIRefuses(t *testing.T) {
	for i, tc := range []struct{ email, want string }{
		{".", "client email must not be '.' or '..'\n"},
		{"..", "client email must not be '.' or '..'\n"},
		{"a/b", "client email contains an invalid character: a/b\n"},
	} {
		t.Run(tc.email, func(t *testing.T) {
			setupConflictDB(t)
			ib := makeInboundWithSubSortIndex("in-reserved-email", 7101+i, 0)
			ib.Settings = clientsSettings(t, []model.Client{{Email: tc.email, ID: "11111111-1111-1111-1111-111111111111", Enable: true}})
			_, _, err := (&InboundService{}).AddInbound(ib)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("AddInbound with client %q: err = %v, want %q", tc.email, err, tc.want)
			}
		})
	}
}

// A node takes what its master already holds, legacy names included; refusing
// the push would only leave the node out of sync.
func TestAddInboundFromMasterPushKeepsALegacyDotName(t *testing.T) {
	setupConflictDB(t)
	ib := makeInboundWithSubSortIndex("in-legacy-dot", 7111, 0)
	ib.Settings = clientsSettings(t, []model.Client{{Email: ".", ID: "11111111-1111-1111-1111-111111111111", Enable: true}})
	if _, _, err := (&InboundService{FromNodeSync: true}).AddInbound(ib); err != nil {
		t.Fatalf("master push with a legacy '.' client: %v", err)
	}
	if rec := lookupClientRecord(t, "."); rec.Email != "." {
		t.Fatalf("stored record email = %q, want the pushed '.'", rec.Email)
	}
}
