package runtime

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestLocalWithAPITreatsZeroPortAsStopped(t *testing.T) {
	local := NewLocal(LocalDeps{APIPort: func() int { return 0 }})
	err := local.withAPI(func(*xray.XrayAPI) error {
		t.Fatal("API callback ran although the local core has no API port")
		return nil
	})
	if err == nil || err.Error() != "local xray is not running" {
		t.Fatalf("withAPI error = %v, want local xray is not running", err)
	}
}

func TestManagerRemoteForClonesNonNilEmptyInboundTags(t *testing.T) {
	m := NewManager(LocalDeps{})
	n := &model.Node{Id: 10, Scheme: "https", Address: "node.example.com", Port: 2053, InboundTags: []string{}}
	if _, err := m.RemoteFor(n); err != nil {
		t.Fatalf("RemoteFor: %v", err)
	}
	n.InboundTags = append(n.InboundTags, "added-later")
	if got := m.remotes[n.Id].node.InboundTags; got == nil || len(got) != 0 {
		t.Fatalf("cached tags = %#v, want a non-nil empty slice independent of the input", got)
	}
}

