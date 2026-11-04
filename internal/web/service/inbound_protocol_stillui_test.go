package service

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// A still-ui node reports its own release (1.0.0 and up) as the panel version and
// the 3x-ui release it is based on separately; the gate reads the latter.
func TestCheckNodeCanHostProtocol_StillUIVersions(t *testing.T) {
	setupConflictDB(t)
	tests := []struct {
		name     string
		panel    string
		upstream string
		wantErr  string
	}{
		{"still-ui on 3.11.0", "1.0.0", "3.11.0", ""},
		{"still-ui on an old base", "1.0.0", "3.6.0", "need v3.7.0 or newer"},
		{"3x-ui node", "v3.6.0", "", "need v3.7.0 or newer"},
		{"still-ui before 1.0.0", "3.11.0-still.5", "", ""},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			node := &model.Node{
				Name: "still-node-" + tc.name, Address: "127.0.0.1", Port: 3096 + i,
				PanelVersion: tc.panel, UpstreamVersion: tc.upstream,
			}
			if err := database.GetDB().Create(node).Error; err != nil {
				t.Fatalf("create node: %v", err)
			}
			err := checkNodeCanHostProtocol(database.GetDB(), node.Id, model.AmneziaWG)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("panel %q upstream %q: unexpected error %v", tc.panel, tc.upstream, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("panel %q upstream %q: error %v, want %q", tc.panel, tc.upstream, err, tc.wantErr)
			}
		})
	}
}
