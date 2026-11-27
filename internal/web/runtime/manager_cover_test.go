package runtime

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

type egressStub string

func (e egressStub) NodeEgressProxyURL(int) string { return string(e) }

func TestManagerOverridesAndEgress(t *testing.T) {
	m := NewManager(LocalDeps{})
	if m.NodeEgressProxyURL(1) != "" {
		t.Fatal("no resolver must give no proxy")
	}
	m.SetNodeEgressResolver(egressStub("socks5://e"))
	if m.NodeEgressProxyURL(1) != "socks5://e" {
		t.Fatal("resolver not used")
	}

	if rt, err := m.RuntimeFor(nil); err != nil || rt != m.Local() {
		t.Fatalf("RuntimeFor(nil) = %v, %v; want the local runtime", rt, err)
	}
	fake := NewLocal(LocalDeps{})
	m.SetLocalRuntimeOverride(fake)
	if rt, _ := m.RuntimeFor(nil); rt != fake {
		t.Fatal("local override ignored")
	}
	m.SetLocalRuntimeOverride(nil)

	id := 5
	m.SetRuntimeOverride(id, fake)
	m.SetRuntimeOverride(id+1, fake) // a second override keeps the first
	if rt, err := m.RuntimeFor(&id); err != nil || rt != fake {
		t.Fatalf("node override = %v, %v", rt, err)
	}
	m.SetRuntimeOverride(id, nil)
	if _, ok := m.overrides[id]; ok {
		t.Fatal("a nil override must clear it")
	}
	if _, ok := m.overrides[id+1]; !ok {
		t.Fatal("clearing one override dropped another")
	}
}

func TestSameRemoteIdentityNil(t *testing.T) {
	n := &model.Node{Id: 1}
	if !sameRemoteIdentity(nil, nil) || sameRemoteIdentity(nil, n) || sameRemoteIdentity(n, nil) {
		t.Fatal("nil identities compare wrong")
	}
}

func TestManagerRuntimeForLoadsTheNode(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	db := database.GetDB()
	on := &model.Node{Name: "on", Enable: true, Address: "127.0.0.1", Port: 2053, Scheme: "http"}
	off := &model.Node{Name: "off", Enable: false, Address: "127.0.0.1", Port: 2054, Scheme: "http"}
	for _, n := range []*model.Node{on, off} {
		if err := db.Create(n).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(off).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	m := NewManager(LocalDeps{})

	rt, err := m.RuntimeFor(&on.Id)
	if err != nil || rt.Name() != "node:on" {
		t.Fatalf("enabled node: %v, %v", rt, err)
	}
	if again, _ := m.RuntimeFor(&on.Id); again != rt {
		t.Fatal("the runtime must be cached")
	}
	if _, err := m.RuntimeFor(&off.Id); err == nil || err.Error() != "node off is disabled" {
		t.Fatalf("disabled node: err = %v", err)
	}
	missing := 999999
	if _, err := m.RuntimeFor(&missing); err == nil {
		t.Fatal("an unknown node must be an error")
	}
}
