package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/wirecodec"
)

// Tests for node RPCs no test exercised before (gremlins: NOT COVERED).

func TestRemoteConstantsAndName(t *testing.T) {
	if remoteHTTPTimeout != 10*time.Second {
		t.Fatalf("remoteHTTPTimeout = %v, want 10s", remoteHTTPTimeout)
	}
	r := NewRemote(&model.Node{Name: "vienna"}, nil)
	if r.Name() != "node:vienna" {
		t.Fatalf("Name() = %q", r.Name())
	}
}

func TestRemoteFetchTrafficSnapshot(t *testing.T) {
	ctx := context.Background()
	full := map[string]string{
		"/panel/api/inbounds/list":          `[{"id":1,"tag":"a"}]`,
		"/panel/api/clients/onlinesByGuid":  `{"g1":["e1"]}`,
		"/panel/api/clients/onlines":        `["flat"]`,
		"/panel/api/clients/lastOnline":     `{"e1":5}`,
		"/panel/api/clients/activeInbounds": `{"g1":["a"]}`,
	}
	f := &fakeNode{obj: full}
	snap, err := f.start(t).FetchTrafficSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Inbounds) != 1 || len(snap.OnlineTree["g1"]) != 1 || len(snap.OnlineEmails) != 0 ||
		snap.LastOnlineMap["e1"] != 5 || len(snap.ActiveInboundTree["g1"]) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}

	// An old node without the GUID tree: the flat list is used.
	f = &fakeNode{
		obj:  map[string]string{"/panel/api/inbounds/list": `[]`, "/panel/api/clients/onlines": `["flat"]`},
		fail: map[string]bool{"/panel/api/clients/onlinesByGuid": true, "/panel/api/clients/lastOnline": true, "/panel/api/clients/activeInbounds": true},
	}
	snap, err = f.start(t).FetchTrafficSnapshot(ctx)
	if err != nil || len(snap.OnlineEmails) != 1 || len(snap.LastOnlineMap) != 0 || len(snap.ActiveInboundTree) != 0 {
		t.Fatalf("old node: snap=%+v err=%v", snap, err)
	}

	// An empty tree also falls back to the flat list.
	f = &fakeNode{obj: map[string]string{"/panel/api/inbounds/list": `[]`, "/panel/api/clients/onlinesByGuid": `{}`, "/panel/api/clients/onlines": `["flat"]`}}
	if snap, err = f.start(t).FetchTrafficSnapshot(ctx); err != nil || len(snap.OnlineEmails) != 1 {
		t.Fatalf("empty tree: snap=%+v err=%v", snap, err)
	}

	// The inbound list is required.
	f = &fakeNode{fail: map[string]bool{"/panel/api/inbounds/list": true}}
	if _, err := f.start(t).FetchTrafficSnapshot(ctx); err == nil {
		t.Error("a failed inbound list must fail the snapshot")
	}
	f = &fakeNode{obj: map[string]string{"/panel/api/inbounds/list": `{"not":"a list"}`}}
	if _, err := f.start(t).FetchTrafficSnapshot(ctx); err == nil || !strings.Contains(err.Error(), "decode inbound list") {
		t.Errorf("a bad inbound list: err = %v", err)
	}
}

// fetchers: each fails on a node error and on an undecodable obj, and decodes a good one.
func TestRemoteFetchers(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		path, good, bad string
		call            func(r *Remote) (int, error)
	}{
		{"/panel/api/server/getWebCertFiles", `{"webCertFile":"/c","webKeyFile":"/k"}`, `"x"`, func(r *Remote) (int, error) {
			f, err := r.GetWebCertFiles(ctx)
			if err != nil {
				return 0, err
			}
			return len(f.WebCertFile + f.WebKeyFile), nil
		}},
		{"/panel/api/hosts/list", `[{"inboundId":1}]`, `"x"`, func(r *Remote) (int, error) {
			g, err := r.FetchHostGroups(ctx)
			return len(g), err
		}},
		{"/panel/api/server/clientIps", `[{"clientEmail":"a"}]`, `"x"`, func(r *Remote) (int, error) {
			ips, err := r.FetchAllClientIps(ctx)
			return len(ips), err
		}},
		{"/panel/api/clients/clientIpsByGuid", `{"g":{"a":[]}}`, `"x"`, func(r *Remote) (int, error) {
			m, err := r.FetchClientIpsByGuid(ctx)
			return len(m), err
		}},
	}
	for _, c := range cases {
		if _, err := c.call((&fakeNode{fail: map[string]bool{c.path: true}}).start(t)); err == nil {
			t.Errorf("%s: a node error must be returned", c.path)
		}
		if _, err := c.call((&fakeNode{obj: map[string]string{c.path: c.bad}}).start(t)); err == nil {
			t.Errorf("%s: an undecodable obj must be an error", c.path)
		}
		if n, err := c.call((&fakeNode{obj: map[string]string{c.path: c.good}}).start(t)); err != nil || n == 0 {
			t.Errorf("%s: good obj gave (%d, %v)", c.path, n, err)
		}
	}
	// No obj at all: an old node, an empty map and no error.
	if m, err := (&fakeNode{}).start(t).FetchClientIpsByGuid(ctx); err != nil || len(m) != 0 {
		t.Errorf("FetchClientIpsByGuid without obj = (%v, %v)", m, err)
	}
}

func TestRemoteDeleteAndResetClient(t *testing.T) {
	ctx := context.Background()
	f := &fakeNode{}
	r := f.start(t)
	if err := r.DeleteClient(ctx, ""); err != nil || f.lastPath != "" {
		t.Fatalf("empty email: err=%v path=%q, want no request", err, f.lastPath)
	}
	if err := r.DeleteClient(ctx, "a b"); err != nil || f.lastPath != "/panel/api/clients/del/a%20b" {
		t.Fatalf("DeleteClient: err=%v path=%q", err, f.lastPath)
	}
	f.msgFail = map[string]string{"/panel/api/clients/del/gone": "Client Not Found"}
	if err := r.DeleteClient(ctx, "gone"); err != nil {
		t.Fatalf("a client already gone is not an error: %v", err)
	}
	f.msgFail = map[string]string{"/panel/api/clients/del/x": "database is locked"}
	if err := r.DeleteClient(ctx, "x"); err == nil {
		t.Fatal("other node errors must be returned")
	}
	if err := r.ResetClientTraffic(ctx, nil, "a b"); err != nil || f.lastPath != "/panel/api/clients/resetTraffic/a%20b" {
		t.Fatalf("ResetClientTraffic: err=%v path=%q", err, f.lastPath)
	}
}

func TestRemoteCapabilityChecksProbeTheNode(t *testing.T) {
	ctx := context.Background()
	ok := &fakeNode{caps: wirecodec.CapWireguardDevices + "," + wirecodec.CapPreserveClientTraffic}
	r := ok.start(t)
	if err := r.CheckWireguardDevices(ctx); err != nil {
		t.Errorf("CheckWireguardDevices on a capable node: %v", err)
	}
	if err := r.CheckPreserveClientTraffic(ctx); err != nil {
		t.Errorf("CheckPreserveClientTraffic on a capable node: %v", err)
	}
	down := (&fakeNode{fail: map[string]bool{"/panel/api/server/status": true}}).start(t)
	if err := down.CheckWireguardDevices(ctx); err == nil || !strings.Contains(err.Error(), "cannot verify") {
		t.Errorf("CheckWireguardDevices on a failing node: %v", err)
	}
	if err := down.CheckPreserveClientTraffic(ctx); err == nil || !strings.Contains(err.Error(), "cannot verify") {
		t.Errorf("CheckPreserveClientTraffic on a failing node: %v", err)
	}
}

func TestRemoteListRemoteTags(t *testing.T) {
	ctx := context.Background()
	r := (&fakeNode{obj: map[string]string{"/panel/api/inbounds/list": `[{"id":3,"tag":"a"},{"id":4,"tag":"b"}]`}}).start(t)
	tags, err := r.ListRemoteTags(ctx)
	if err != nil || len(tags) != 2 {
		t.Fatalf("ListRemoteTags = (%v, %v)", tags, err)
	}
	if _, err := (&fakeNode{fail: map[string]bool{"/panel/api/inbounds/list": true}}).start(t).ListRemoteTags(ctx); err == nil {
		t.Fatal("a failed refresh must be returned")
	}
}

func TestRemoteForgetPushedInboundEitherSpelling(t *testing.T) {
	r := NewRemote(&model.Node{Id: 1}, nil)
	for _, tag := range []string{nodeInboundTagPrefix(1) + "x", "x"} {
		r.RecordAdoptedInbound(&model.Inbound{Tag: nodeInboundTagPrefix(1) + "x", Settings: "{}"})
		r.ForgetPushedInbound(tag)
		if len(r.pushedFP) != 0 {
			t.Fatalf("ForgetPushedInbound(%q) left %v", tag, r.pushedFP)
		}
	}
}
