package sub

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/panelimport"
	"github.com/mhsanaei/3x-ui/v3/internal/panelimport/marzbantest"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

const marzbanSubPort = 22000

func importMarzbanFixture(t *testing.T) {
	t.Helper()
	seedSubDB(t)
	t.Setenv("XUI_BACKUP_FOLDER", t.TempDir())
	dbPath, cfg := marzbantest.Build(t, marzbanSubPort)
	plan, err := panelimport.ReadMarzban(dbPath, cfg)
	if err != nil {
		t.Fatalf("ReadMarzban: %v", err)
	}
	if _, err := (&service.PanelImportService{}).Run(plan, service.PanelImportOptions{}); err != nil {
		t.Fatalf("import: %v", err)
	}
	gin.SetMode(gin.TestMode)
}

// subIDOf is the random subscription id the import gave a client.
func subIDOf(t *testing.T, email string) string {
	t.Helper()
	var rec model.ClientRecord
	if err := database.GetDB().Where("email = ?", email).Take(&rec).Error; err != nil || rec.SubID == "" {
		t.Fatalf("client %q: %v, sub id %q", email, err, rec.SubID)
	}
	return rec.SubID
}

func fetchSub(t *testing.T, id string) (int, []string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://sub.example.com/sub/"+id, nil)
	resp := httptest.NewRecorder()
	newSubscriptionTestRouter(subscriptionTestRouterConfig{}).ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		return resp.Code, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(resp.Body.String())
	if err != nil {
		t.Fatalf("subscription body is not base64: %v", err)
	}
	return resp.Code, strings.Split(strings.TrimSpace(string(decoded)), "\n")
}

var vmessDefaults = map[string]string{"type": "none", "scy": "auto", "aid": "0"}

// connection reduces a link to what decides how a client connects: no link name, no empty
// or default parameter, no client-side hint (Marzban's XHTTP extra; this panel's spx, mlkem).
func connection(t *testing.T, link string) string {
	t.Helper()
	if rest, ok := strings.CutPrefix(link, "vmess://"); ok {
		raw, err := base64.StdEncoding.DecodeString(rest)
		if err != nil {
			t.Fatalf("vmess link %q: %v", link, err)
		}
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatalf("vmess link %q: %v", link, err)
		}
		out := map[string]string{}
		for k, v := range fields {
			s := fmt.Sprint(v)
			if s == "" || k == "v" || k == "ps" || vmessDefaults[k] == s {
				continue
			}
			out[k] = s
		}
		b, _ := json.Marshal(out)
		return "vmess " + string(b)
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("link %q: %v", link, err)
	}
	q := u.Query()
	params := map[string]string{}
	for k := range q {
		v := q.Get(k)
		if v == "" || k == "extra" || k == "spx" || k == "support-x25519mlkem768" ||
			(k == "encryption" && v == "none") || (k == "headerType" && v == "none") || (k == "mode" && v == "gun") {
			continue
		}
		params[k] = v
	}
	user := u.User.String()
	if u.Scheme == "ss" {
		if raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(u.User.Username(), "=")); err == nil {
			user = string(raw)
		} else if raw, err := base64.StdEncoding.DecodeString(u.User.Username()); err == nil {
			user = string(raw)
		}
	}
	b, _ := json.Marshal(params)
	return fmt.Sprintf("%s %s@%s %s", u.Scheme, user, u.Host, b)
}

// marzbanLinksForAlice is what app/subscription/v2ray.py V2rayShareLink renders for the
// fixture's alice, worked out by hand; {SERVER_IP} is the host the subscription is read from.
func marzbanLinksForAlice() []string {
	p := func(n int) int { return marzbanSubPort + n }
	reality := "&sni=www.example.org&fp=chrome&pbk=" + marzbantest.RealityPublicKey + "&sid=" + marzbantest.ShortID
	vmess := func(fields string) string {
		return "vmess://" + base64.StdEncoding.EncodeToString([]byte(fields))
	}
	return []string{
		fmt.Sprintf("vless://%s@vpn.example.com:%d?security=reality&type=tcp&headerType=&flow=xtls-rprx-vision&path=&host=%s#alice%%20VLESS%%20TCP%%20REALITY", marzbantest.AliceVless, p(1), reality),
		fmt.Sprintf("vless://%s@cdn.example.com:443?security=tls&type=ws&headerType=&path=%%2Fcdn-path&host=host.example.com&sni=sni.example.com&fp=firefox&alpn=h2%%2Chttp%%2F1.1#alice%%20cdn", marzbantest.AliceVless),
		fmt.Sprintf("vless://%s@sub.example.com:%d?security=tls&type=ws&headerType=&path=%%2Fvl&host=&sni=vpn.example.com&fp=#alice%%20direct", marzbantest.AliceVless, p(2)),
		fmt.Sprintf("vless://%s@vpn.example.com:%d?security=tls&type=tcp&headerType=&flow=xtls-rprx-vision&path=&host=&sni=vpn.example.com&fp=#alice%%20VLESS%%20TCP%%20TLS", marzbantest.AliceVless, p(3)),
		vmess(fmt.Sprintf(`{"add": "vpn.example.com", "aid": "0", "host": "ws.example.com", "id": "%s", "net": "ws", "path": "/vm", "port": %d, "ps": "alice VMESS WS", "scy": "auto", "tls": "none", "type": "", "v": "2"}`, marzbantest.AliceVmess, p(4))),
		vmess(fmt.Sprintf(`{"add": "vpn.example.com", "aid": "0", "host": "http.example.com", "id": "%s", "net": "tcp", "path": "/", "port": %d, "ps": "alice VMESS TCP HTTP", "scy": "auto", "tls": "none", "type": "http", "v": "2"}`, marzbantest.AliceVmess, p(5))),
		fmt.Sprintf("trojan://%s@vpn.example.com:%d?security=tls&type=grpc&headerType=&serviceName=trgrpc&authority=&mode=gun&sni=vpn.example.com&fp=#alice%%20TROJAN%%20GRPC%%20TLS", marzbantest.AliceTrojan, p(6)),
		fmt.Sprintf("vless://%s@vpn.example.com:%d?security=tls&type=httpupgrade&headerType=&path=%%2Fhu&host=hu.example.com&sni=vpn.example.com&fp=#alice%%20VLESS%%20HTTPUPGRADE%%20TLS", marzbantest.AliceVless, p(7)),
		fmt.Sprintf(`vless://%s@vpn.example.com:%d?security=reality&type=xhttp&headerType=&path=%%2Fxh&host=&mode=auto&extra=%s%s#alice%%20VLESS%%20XHTTP%%20REALITY`, marzbantest.AliceVless, p(8),
			url.QueryEscape(`{"scMaxEachPostBytes": 1000000, "scMaxConcurrentPosts": 100, "scMinPostsIntervalMs": 30, "xPaddingBytes": "100-1000", "noGRPCHeader": false}`), reality),
		fmt.Sprintf("ss://%s@vpn.example.com:%d#alice%%20Shadowsocks%%20TCP", base64.StdEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:"+marzbantest.AliceSS)), p(9)),
	}
}

func TestMarzbanImport_LinksEqualMarzbans(t *testing.T) {
	importMarzbanFixture(t)
	code, got := fetchSub(t, subIDOf(t, "alice"))
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	want := marzbanLinksForAlice()
	gotSet := map[string]string{}
	for _, link := range got {
		gotSet[connection(t, link)] = link
	}
	for _, link := range want {
		key := connection(t, link)
		if _, ok := gotSet[key]; !ok {
			t.Errorf("no link equal to Marzban's\n  marzban: %s\n  as:      %s", link, key)
		}
		delete(gotSet, key)
	}
	for key, link := range gotSet {
		t.Errorf("a link Marzban does not give\n  still-ui: %s\n  as:       %s", link, key)
	}
}

func TestMarzbanImport_OldSubscriptionLinksKeepWorking(t *testing.T) {
	importMarzbanFixture(t)
	aliceSub := subIDOf(t, "alice")
	_, want := fetchSub(t, aliceSub)
	for name, token := range map[string]string{"current token": marzbantest.AliceToken, "old JWT token": marzbantest.AliceJWT} {
		code, got := fetchSub(t, token)
		if code != http.StatusOK || strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: status %d, %d links; want alice's %d links", name, code, len(got), len(want))
		}
	}
	if code, _ := fetchSub(t, marzbantest.FrankToken); code != http.StatusOK {
		t.Errorf("frank's link issued after his revoke: status %d", code)
	}
	for name, token := range map[string]string{
		"a link revoked in Marzban":      marzbantest.FrankRevokedToken,
		"a link signed by another panel": marzbantest.AliceToken[:len(marzbantest.AliceToken)-2] + "zz",
	} {
		if code, links := fetchSub(t, token); code == http.StatusOK {
			t.Errorf("%s was served: %v", name, links)
		}
	}
	if err := service.ForgetLegacySubLinks(); err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{
		"current token": marzbantest.AliceToken, "old JWT token": marzbantest.AliceJWT,
		// What the old secret and the username used to be enough to compute.
		"an id derived from Marzban's secret": marzbantest.DerivableAliceSubID,
	} {
		if code, _ := fetchSub(t, token); code == http.StatusOK {
			t.Errorf("%s is still served after the links were forgotten", name)
		}
	}
	if secret, err := (&service.SettingService{}).GetMarzbanSubSecret(); err != nil || secret != "" {
		t.Errorf("Marzban's secret is still stored after the links were forgotten (%v)", err)
	}
	if code, got := fetchSub(t, aliceSub); code != http.StatusOK || strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("alice's own subscription changed: status %d, %d links", code, len(got))
	}
}

// An imported client must never land on a subscription link someone here already holds.
func TestPanelImport_SubscriptionOfAnotherClientIsNotShared(t *testing.T) {
	seedSubDB(t)
	t.Setenv("XUI_BACKUP_FOLDER", t.TempDir())
	gin.SetMode(gin.TestMode)
	const zedSub = "zedsub0123456789"
	mine := &model.Inbound{
		Tag: "mine", Enable: true, Listen: "0.0.0.0", Port: marzbanSubPort + 40, Protocol: model.VLESS,
		StreamSettings: `{"network":"tcp","security":"none"}`,
		Settings:       `{"clients":[{"id":"99999999-9999-4999-8999-999999999999","email":"zed","subId":"` + zedSub + `","enable":true}],"decryption":"none"}`,
	}
	if _, _, err := (&service.InboundService{}).AddInbound(mine); err != nil {
		t.Fatalf("seed inbound: %v", err)
	}
	service.InvalidateSubData()
	code, before := fetchSub(t, zedSub)
	if code != http.StatusOK || len(before) != 1 {
		t.Fatalf("zed's subscription before the import: status %d, %v", code, before)
	}

	plan := func() *panelimport.Plan {
		return &panelimport.Plan{Source: panelimport.SourceXUI, Inbounds: []*model.Inbound{{
			Remark: "theirs", Enable: true, Port: marzbanSubPort + 41, Protocol: model.VLESS, Tag: "theirs",
			StreamSettings: `{"network":"tcp","security":"none"}`,
			Settings: `{"clients":[{"id":"11111111-1111-4111-8111-111111111111","email":"eve","subId":"` + zedSub + `","enable":true},` +
				`{"id":"22222222-2222-4222-8222-222222222222","email":"fay","subId":"faysub0123456789","enable":true}],"decryption":"none"}`,
		}}}
	}
	const note = `inbound "theirs": client "eve" has the subscription id of client "zed" of this panel, skipped`
	for _, opts := range []service.PanelImportOptions{{DryRun: true}, {Merge: true}} {
		report, err := (&service.PanelImportService{}).Run(plan(), opts)
		if err != nil {
			t.Fatalf("dry run %v: %v", opts.DryRun, err)
		}
		if report.Clients != 1 || len(report.Notes) != 1 || report.Notes[0] != note {
			t.Errorf("dry run %v: %d clients, notes %q; want fay alone and the note about eve", opts.DryRun, report.Clients, report.Notes)
		}
	}
	var eve int64
	database.GetDB().Model(&model.ClientRecord{}).Where("email = ?", "eve").Count(&eve)
	if eve != 0 {
		t.Error("eve was imported onto zed's subscription")
	}
	if code, after := fetchSub(t, zedSub); code != http.StatusOK || strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("zed's subscription changed:\nbefore %q\nafter  %q", before, after)
	}
	if code, links := fetchSub(t, "faysub0123456789"); code != http.StatusOK || len(links) != 1 {
		t.Errorf("fay's subscription: status %d, %v", code, links)
	}
}

// A new panel has a random subscription path; Marzban's links still arrive at /sub/.
func TestMarzbanImport_OldLinksWorkBesideAnotherSubPath(t *testing.T) {
	importMarzbanFixture(t)
	router := gin.New()
	NewSUBController(router.Group("/"), WithSUBPath("/k3y9p1/"))
	status := func(path string) int {
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "http://sub.example.com"+path, nil))
		return resp.Code
	}
	cases := map[string]int{
		"/sub/" + marzbantest.AliceToken:        http.StatusOK,
		"/k3y9p1/" + subIDOf(t, "alice"):        http.StatusOK,
		"/k3y9p1/" + marzbantest.AliceToken:     http.StatusOK,
		"/sub/" + subIDOf(t, "alice"):           http.StatusNotFound,
		"/sub/" + marzbantest.FrankRevokedToken: http.StatusNotFound,
	}
	for path, want := range cases {
		if got := status(path); got != want {
			t.Errorf("GET %s = %d, want %d", path, got, want)
		}
	}
	// Paths around /sub/ must neither break route registration nor hide the old links.
	for _, subPath := range []string{"/", "/sub/deeper/", "/sub/"} {
		other := gin.New()
		NewSUBController(other.Group("/"), WithSUBPath(subPath), WithSUBJsonEnabled(true), WithSUBClashEnabled(true))
		resp := httptest.NewRecorder()
		other.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "http://sub.example.com/sub/"+marzbantest.AliceToken, nil))
		if resp.Code != http.StatusOK {
			t.Errorf("sub path %q: GET /sub/<token> = %d, want 200", subPath, resp.Code)
		}
	}
}
