package service

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/nfttable"
	"github.com/mhsanaei/3x-ui/v3/internal/porthop"
)

type fakePortHopHost struct {
	unavailable error
	redirects   []porthop.Redirect
	protected   []uint16
	ensured     int
}

func (f *fakePortHopHost) Available() error { return f.unavailable }

func (f *fakePortHopHost) Ensure(redirects []porthop.Redirect, protected []uint16) error {
	f.ensured++
	f.redirects, f.protected = redirects, slices.Clone(protected)
	slices.Sort(f.protected)
	return nil
}

func (f *fakePortHopHost) Status() porthop.Status {
	return porthop.Status{Supported: true, NftInstalled: true, Permitted: true, Active: len(f.redirects) > 0, Packets: 9}
}

func useFakePortHopHost(t *testing.T) *fakePortHopHost {
	t.Helper()
	fake := &fakePortHopHost{}
	prev := portHopHostImpl
	portHopHostImpl = fake
	t.Cleanup(func() { portHopHostImpl, portHopErr = prev, "" })
	return fake
}

func hopStream(ports string, redirect bool) string {
	flag := "false"
	if redirect {
		flag = "true"
	}
	return `{"network":"hysteria","finalmask":{"quicParams":{"udpHop":{"ports":"` + ports + `","interval":"5-10","redirect":` + flag + `}}}}`
}

func hopInbound(port int, ports string, redirect bool) *model.Inbound {
	return &model.Inbound{
		Enable: true, Listen: "", Port: port, Protocol: model.Hysteria,
		StreamSettings: hopStream(ports, redirect), Settings: `{"clients":[]}`,
	}
}

func inboundCount(t *testing.T) int64 {
	t.Helper()
	var n int64
	if err := database.GetDB().Model(&model.Inbound{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAddInboundRefusesAPortHopRedirectItCannotInstall(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*model.Inbound, *fakePortHopHost)
		wantErr string
	}{
		{"malformed range", func(in *model.Inbound, _ *fakePortHopHost) { in.StreamSettings = hopStream("20000-abc", true) }, `Port hopping: port range "20000-abc" is not a port or a low-high range`},
		{"well-known ports", func(in *model.Inbound, _ *fakePortHopHost) { in.StreamSettings = hopStream("53-2000", true) }, `Port hopping: port range "53-2000" starts below 1024`},
		{"numeric range", func(in *model.Inbound, _ *fakePortHopHost) {
			in.StreamSettings = `{"network":"hysteria","finalmask":{"quicParams":{"udpHop":{"ports":20000,"redirect":true}}}}`
		}, `Port hopping: port range 20000 is not a string`},
		{"no range", func(in *model.Inbound, _ *fakePortHopHost) {
			in.StreamSettings = `{"network":"hysteria","finalmask":{"quicParams":{"udpHop":{"redirect":true}}}}`
		}, `Port hopping: port range "" has an empty entry`},
		{"specific listen address", func(in *model.Inbound, _ *fakePortHopHost) { in.Listen = "203.0.113.7" }, "needs the inbound to listen on all addresses"},
		{"no nft", func(_ *model.Inbound, h *fakePortHopHost) { h.unavailable = nfttable.ErrNftMissing }, "is not available on this server: nft is not installed"},
		{"container without NET_ADMIN", func(_ *model.Inbound, h *fakePortHopHost) { h.unavailable = porthop.ErrNotPermitted }, "is not available on this server: nftables rules cannot be changed here"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupConflictDB(t)
			host := useFakePortHopHost(t)
			in := hopInbound(443, "20000-50000", true)
			tc.mutate(in, host)
			_, _, err := (&InboundService{}).AddInbound(in)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("AddInbound error = %v, want one containing %q", err, tc.wantErr)
			}
			if n := inboundCount(t); n != 0 {
				t.Fatalf("the refused inbound was stored (%d rows)", n)
			}
		})
	}
}

// An inbound that only advertises a range, as before this option existed, is never checked.
func TestAddInboundKeepsAdvertiseOnlyHopRangesUnchecked(t *testing.T) {
	setupConflictDB(t)
	host := useFakePortHopHost(t)
	host.unavailable = nfttable.ErrUnsupported
	in := hopInbound(443, "1-65535", false)
	in.Listen = "203.0.113.7"
	if _, _, err := (&InboundService{}).AddInbound(in); err != nil {
		t.Fatalf("AddInbound: %v", err)
	}
	(&PortHopService{}).EnsureHost()
	if len(host.redirects) != 0 {
		t.Fatalf("an advertise-only range was redirected: %+v", host.redirects)
	}
}

// The master cannot probe a node's host; the node's own panel refuses when the inbound reaches it.
func TestValidatePortHopDoesNotProbeThisHostForANodeInbound(t *testing.T) {
	host := useFakePortHopHost(t)
	host.unavailable = nfttable.ErrNftMissing
	in := hopInbound(443, "20000-50000", true)
	nodeID := 3
	in.NodeID = &nodeID
	if err := validatePortHop(in); err != nil {
		t.Fatalf("node inbound refused by the master's own host state: %v", err)
	}
	in.NodeID = nil
	if err := validatePortHop(in); err == nil {
		t.Fatal("a local inbound must be refused on a host without nft")
	}
}

func TestPortHopRangesAndUDPPortsOfOneHostStayApart(t *testing.T) {
	setupConflictDB(t)
	useFakePortHopHost(t)
	svc := &InboundService{}
	mustAdd := func(in *model.Inbound) *model.Inbound {
		t.Helper()
		out, _, err := svc.AddInbound(in)
		if err != nil {
			t.Fatalf("AddInbound(port %d): %v", in.Port, err)
		}
		return out
	}
	wantRefused := func(in *model.Inbound, want string) {
		t.Helper()
		before := inboundCount(t)
		if _, _, err := svc.AddInbound(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("AddInbound(port %d) error = %v, want one containing %q", in.Port, err, want)
		}
		if inboundCount(t) != before {
			t.Fatalf("the refused inbound on port %d was stored", in.Port)
		}
	}
	tcp := func(port int) *model.Inbound {
		return &model.Inbound{Enable: true, Port: port, Protocol: model.VLESS, StreamSettings: `{"network":"tcp"}`, Settings: `{"clients":[]}`}
	}
	udp := func(port int) *model.Inbound {
		return &model.Inbound{Enable: true, Port: port, Protocol: model.Hysteria, StreamSettings: `{"network":"hysteria"}`, Settings: `{"clients":[]}`}
	}

	mustAdd(udp(25000))
	mustAdd(tcp(26000))
	wantRefused(hopInbound(443, "20000-30000", true), "the range 20000-30000 contains UDP port 25000 of inbound #1")

	// A TCP listener is out of a UDP redirect's reach, so it does not block the range.
	first := mustAdd(hopInbound(443, "26000-30000", true))
	wantRefused(hopInbound(8443, "30000-31000", true), "the range 30000-31000 overlaps the range 26000-30000 of inbound #"+strconv.Itoa(first.Id))
	wantRefused(udp(27000), "UDP port 27000 is inside the port hopping range 26000-30000 of inbound #"+strconv.Itoa(first.Id))
	mustAdd(tcp(27000))
	mustAdd(hopInbound(8443, "30001-31000", true))

	// The same inbound may be saved again without tripping over its own range.
	update := *first
	update.Remark = "renamed"
	if _, _, err := svc.UpdateInbound(&update); err != nil {
		t.Fatalf("UpdateInbound of the hopping inbound itself: %v", err)
	}
	update.StreamSettings = hopStream("24000-30000", true)
	if _, _, err := svc.UpdateInbound(&update); err == nil || !strings.Contains(err.Error(), "the range 24000-30000 contains UDP port 25000") {
		t.Fatalf("UpdateInbound widening over a UDP port: err = %v", err)
	}
	update.StreamSettings, update.Listen = first.StreamSettings, "203.0.113.7"
	if _, _, err := svc.UpdateInbound(&update); err == nil || !strings.Contains(err.Error(), "listen on all addresses") {
		t.Fatalf("UpdateInbound to one listen address: err = %v", err)
	}
}

func TestEnsureHostFollowsTheInbounds(t *testing.T) {
	setupConflictDB(t)
	host := useFakePortHopHost(t)
	svc := &InboundService{}
	hop := &PortHopService{}

	in, _, err := svc.AddInbound(hopInbound(443, "20000-29999, 40000", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.AddInbound(&model.Inbound{Enable: true, Port: 21000, Protocol: model.VLESS, StreamSettings: `{"network":"tcp"}`, Settings: `{"clients":[]}`}); err != nil {
		t.Fatal(err)
	}
	// A restored backup or a hand-edited row can put a UDP inbound inside the range unchecked.
	seedInboundConflict(t, "in-22000-udp", "", 22000, model.Hysteria, `{"network":"hysteria"}`, `{"clients":[]}`)

	hop.EnsureHost()
	want := []porthop.Redirect{{Ranges: []porthop.Range{{From: 20000, To: 29999}, {From: 40000, To: 40000}}, To: 443}}
	if len(host.redirects) != 1 || host.redirects[0].To != 443 || !slices.Equal(host.redirects[0].Ranges, want[0].Ranges) {
		t.Fatalf("redirects = %+v, want %+v", host.redirects, want)
	}
	// Only UDP listeners are carved out: a TCP port inside a range would become a dead hop port.
	if !slices.Equal(host.protected, []uint16{443, 22000}) {
		t.Fatalf("protected = %v, want the inbound's own port and the UDP inbound inside its range [443 22000]", host.protected)
	}
	if err := database.GetDB().Where("tag = ?", "in-22000-udp").Delete(&model.Inbound{}).Error; err != nil {
		t.Fatal(err)
	}
	status := hop.Status()
	if len(status.Rules) != 2 || status.Rules[0] != (PortHopRule{InboundID: in.Id, Ports: "20000-29999", ToPort: 443}) || !status.Active || status.Packets != 9 {
		t.Fatalf("status = %+v", status)
	}

	if _, err := svc.SetInboundEnable(in.Id, false); err != nil {
		t.Fatal(err)
	}
	hop.EnsureHost()
	if len(host.redirects) != 0 {
		t.Fatalf("a disabled inbound kept its redirect: %+v", host.redirects)
	}

	if _, err := svc.SetInboundEnable(in.Id, true); err != nil {
		t.Fatal(err)
	}
	hop.EnsureHost()
	if len(host.redirects) != 1 {
		t.Fatalf("re-enabling did not restore the redirect: %+v", host.redirects)
	}

	off := *in
	off.StreamSettings = hopStream("20000-29999, 40000", false)
	if _, _, err := svc.UpdateInbound(&off); err != nil {
		t.Fatal(err)
	}
	hop.EnsureHost()
	if len(host.redirects) != 0 {
		t.Fatalf("turning the option off kept the redirect: %+v", host.redirects)
	}

	on := *in
	if _, _, err := svc.UpdateInbound(&on); err != nil {
		t.Fatal(err)
	}
	hop.EnsureHost()
	if len(host.redirects) != 1 {
		t.Fatalf("turning the option back on did not restore the redirect: %+v", host.redirects)
	}
	if _, err := svc.DelInbound(in.Id); err != nil {
		t.Fatal(err)
	}
	hop.EnsureHost()
	if len(host.redirects) != 0 {
		t.Fatalf("a deleted inbound kept its redirect: %+v", host.redirects)
	}
}

func TestEnsureHostRecordsWhyTheRedirectIsNotApplied(t *testing.T) {
	setupConflictDB(t)
	useFakePortHopHost(t)
	if _, _, err := (&InboundService{}).AddInbound(hopInbound(443, "20000-50000", true)); err != nil {
		t.Fatal(err)
	}
	portHopHostImpl = &failingPortHopHost{}
	hop := &PortHopService{}
	hop.EnsureHost()
	if got := hop.Status().Error; got != "nft: exit status 1: Error: Could not process rule" {
		t.Fatalf("status error = %q, want the nft failure", got)
	}
}

type failingPortHopHost struct{ fakePortHopHost }

func (*failingPortHopHost) Ensure([]porthop.Redirect, []uint16) error {
	return errors.New("nft: exit status 1: Error: Could not process rule")
}
