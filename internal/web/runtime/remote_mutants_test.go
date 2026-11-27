package runtime

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/wirecodec"
)

// Boundary tests written against surviving mutants reported by gremlins (nightly mutation run).

// fakeNode answers every request with {"success":true}, except paths in fail (HTTP 500)
// and paths in obj (success with that obj). It records the last request.
type fakeNode struct {
	fail     map[string]bool
	obj      map[string]string
	lastPath string
	lastEnc  string
}

func (f *fakeNode) start(t *testing.T) *Remote {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastPath, f.lastEnc = r.URL.RequestURI(), r.Header.Get("Content-Encoding")
		w.Header().Set(wirecodec.CapsHeader, wirecodec.CapZstd)
		if f.fail[r.URL.Path] {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if o, ok := f.obj[r.URL.Path]; ok {
			_, _ = w.Write([]byte(`{"success":true,"obj":` + o + `}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	t.Cleanup(srv.Close)
	return NewRemote(nodeForPlainServer(t, srv, "verify", "tok"), nil)
}

func TestRemoteAddInboundCachesOnlyAValidCreatedInbound(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		obj, tag string
		cached   bool
	}{
		{`{"id":9,"tag":"in-9"}`, "in-9", true},
		{`{"id":0,"tag":"in-0"}`, "in-0", false},
		{`{"id":9,"tag":""}`, "", false},
	} {
		f := &fakeNode{obj: map[string]string{"/panel/api/inbounds/add": c.obj}}
		r := f.start(t)
		if err := r.AddInbound(ctx, &model.Inbound{Tag: "in-x", Protocol: "vless", Settings: "{}"}); err != nil {
			t.Fatalf("AddInbound(%s): %v", c.obj, err)
		}
		if _, ok := r.cacheGet(c.tag); ok != c.cached {
			t.Errorf("obj %s: tag %q cached = %v, want %v", c.obj, c.tag, ok, c.cached)
		}
	}
}

func TestRemoteUpdateInboundMovesTheCachedTag(t *testing.T) {
	ctx := context.Background()
	f := &fakeNode{}
	r := f.start(t)
	r.cacheSet("old", 5)
	if err := r.UpdateInbound(ctx, &model.Inbound{Tag: "old"}, &model.Inbound{Tag: "new", Protocol: "vless", Settings: "{}"}); err != nil {
		t.Fatalf("UpdateInbound: %v", err)
	}
	if _, ok := r.cacheGet("old"); ok {
		t.Error("the old tag stayed cached after a rename")
	}
	if id, ok := r.cacheGet("new"); !ok || id != 5 {
		t.Errorf("new tag cached as (%d, %v), want (5, true)", id, ok)
	}

	f.fail = map[string]bool{"/panel/api/inbounds/update/5": true}
	if err := r.UpdateInbound(ctx, &model.Inbound{Tag: "new"}, &model.Inbound{Tag: "newer", Protocol: "vless", Settings: "{}"}); err == nil {
		t.Fatal("a failed node update must be reported")
	}
	if _, ok := r.cacheGet("newer"); ok {
		t.Error("a failed update still cached the new tag")
	}
}

func TestRemoteClientCallsReportNodeFailures(t *testing.T) {
	ctx := context.Background()
	f := &fakeNode{fail: map[string]bool{"/panel/api/clients/add": true}}
	r := f.start(t)
	r.cacheSet("in", 3)
	ib := &model.Inbound{Tag: "in"}
	if err := r.AddClient(ctx, ib, model.Client{Email: "a"}); err == nil {
		t.Error("AddClient: a failed node call must be reported")
	}
	f.fail = map[string]bool{"/panel/api/clients/update/a": true}
	if err := r.UpdateUser(ctx, ib, "", model.Client{Email: "a"}); err == nil {
		t.Error("UpdateUser: a failed node call must be reported")
	}
	f.fail = nil
	if err := r.UpdateUser(ctx, ib, "", model.Client{Email: "a"}); err != nil || f.lastPath != "/panel/api/clients/update/a?inboundIds=3" {
		t.Errorf("UpdateUser without the old email: err=%v path=%q, want the payload email in the path", err, f.lastPath)
	}
}

func TestRemoteDoEmptyErrorBodyHasNoQuotedSnippet(t *testing.T) {
	f := &fakeNode{fail: map[string]bool{"/probe": true}}
	r := f.start(t)
	_, err := r.do(context.Background(), http.MethodGet, "probe", nil)
	if err == nil || !strings.HasSuffix(err.Error(), "HTTP 500") {
		t.Fatalf("error = %v, want it to end with HTTP 500 and no empty snippet", err)
	}
}

func TestRemoteDoCompressesFromExactlyTheThreshold(t *testing.T) {
	ctx := context.Background()
	f := &fakeNode{}
	r := f.start(t)
	if _, err := r.do(ctx, http.MethodGet, "ping", nil); err != nil { // learn the zstd capability
		t.Fatal(err)
	}
	// {"k":"…"} is the value plus 8 bytes.
	for _, c := range []struct {
		n   int
		enc string
	}{{zstdMinBodyBytes - 8, wirecodec.EncodingZstd}, {zstdMinBodyBytes - 9, ""}} {
		if _, err := r.do(ctx, http.MethodPost, "body", map[string]string{"k": strings.Repeat("a", c.n)}); err != nil {
			t.Fatal(err)
		}
		if f.lastEnc != c.enc {
			t.Errorf("body of %d bytes: Content-Encoding = %q, want %q", c.n+8, f.lastEnc, c.enc)
		}
	}
}

func TestRemoteDoAcceptsAResponseOfExactlyTheCap(t *testing.T) {
	body := []byte(`{"success":true}`)
	body = append(body, bytes.Repeat([]byte(" "), maxRemoteResponseBytes-len(body))...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	r := NewRemote(nodeForPlainServer(t, srv, "verify", "tok"), nil)
	if _, err := r.do(context.Background(), http.MethodGet, "big", nil); err != nil {
		t.Fatalf("a response of exactly %d bytes must be accepted: %v", maxRemoteResponseBytes, err)
	}
}

func TestNodeTransportIdleTimeout(t *testing.T) {
	if got := newNodeTransport(nil).IdleConnTimeout; got != 60*time.Second {
		t.Fatalf("IdleConnTimeout = %v, want 60s", got)
	}
}
