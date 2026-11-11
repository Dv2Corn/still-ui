package sub

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	yaml "github.com/goccy/go-yaml"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

const (
	hwidTestClientUUID = "11111111-2222-4333-8444-555555555555"
	limitOneNotice     = "Device limit 1 reached. Remove a device in your account or ask the seller."
)

// ?view=raw only tells /json/ and /clash/ to serve the body instead of the
// HTML page, so it stays gated like the plain route (#GHSA-7ww3).
func hwidBodyRoutes(subID string) []string {
	return []string{
		"/sub/" + subID,
		"/json/" + subID,
		"/clash/" + subID,
		"/json/" + subID + "?view=raw",
		"/clash/" + subID + "?view=RaW",
	}
}

// serverNames lists what the response offers the app as servers, whatever the format: the
// fragment of each raw link, each Xray config's remarks, each Clash proxy name.
func serverNames(t *testing.T, path string, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status = %d, want 200; body=%q", path, rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	switch {
	case strings.HasPrefix(path, "/json/"):
		var configs []map[string]any
		if err := json.Unmarshal(body, &configs); err != nil {
			var single map[string]any
			if err := json.Unmarshal(body, &single); err != nil {
				t.Fatalf("%s body is not JSON: %v; body=%q", path, err, body)
			}
			configs = []map[string]any{single}
		}
		names := make([]string, 0, len(configs))
		for _, config := range configs {
			name, _ := config["remarks"].(string)
			names = append(names, name)
		}
		return names
	case strings.HasPrefix(path, "/clash/"):
		var doc struct {
			Proxies []struct {
				Name string `yaml:"name"`
			} `yaml:"proxies"`
		}
		if err := yaml.Unmarshal(body, &doc); err != nil {
			t.Fatalf("%s body is not YAML: %v; body=%q", path, err, body)
		}
		names := make([]string, 0, len(doc.Proxies))
		for _, proxy := range doc.Proxies {
			names = append(names, proxy.Name)
		}
		return names
	default:
		decoded, err := base64.StdEncoding.DecodeString(string(body))
		if err != nil {
			t.Fatalf("%s body is not base64: %v; body=%q", path, err, body)
		}
		var names []string
		for line := range strings.Lines(string(decoded)) {
			link, fragment, _ := strings.Cut(strings.TrimSpace(line), "#")
			if link == "" {
				continue
			}
			name, err := url.PathUnescape(fragment)
			if err != nil {
				t.Fatalf("%s link fragment %q: %v", path, fragment, err)
			}
			names = append(names, name)
		}
		return names
	}
}

func assertServesRealServers(t *testing.T, path string, rec *httptest.ResponseRecorder) {
	t.Helper()
	names := serverNames(t, path, rec)
	if len(names) != 1 || strings.Contains(names[0], "Device limit") {
		t.Fatalf("%s servers = %q, want the client's one real server", path, names)
	}
	if announce := rec.Header().Get("Announce"); announce != "" {
		t.Fatalf("%s carries an announcement %q on a served subscription", path, announce)
	}
	if rec.Header().Get("X-Hwid-Limit") != "" || rec.Header().Get("X-Hwid-Max-Devices-Reached") != "" {
		t.Fatalf("%s tells a working device the limit is reached: %#v", path, rec.Header())
	}
}

func assertServesNotice(t *testing.T, path string, rec *httptest.ResponseRecorder, notice string) {
	t.Helper()
	if names := serverNames(t, path, rec); len(names) != 1 || names[0] != notice {
		t.Fatalf("%s servers = %q, want only the notice %q", path, names, notice)
	}
	if strings.Contains(rec.Body.String(), hwidTestClientUUID) {
		t.Fatalf("%s leaked the client's credentials to a refused device: %q", path, rec.Body.String())
	}
	want := "base64:" + base64.StdEncoding.EncodeToString([]byte(notice))
	if got := rec.Header().Get("Announce"); got != want {
		t.Fatalf("%s Announce = %q, want the notice %q", path, got, want)
	}
}

// The device over the limit gets a valid answer for its app whose only server is the notice,
// on every body route; the devices registered before it keep getting the real servers.
func TestDeviceLimitServesNoticeInsteadOfServers(t *testing.T) {
	router, subID := initHwidSubRouter(t, 1)
	routes := hwidBodyRoutes(subID)

	for range 2 {
		for _, path := range routes {
			rec := requestSub(t, router, http.MethodGet, path, "device-one", "")
			assertServesRealServers(t, path, rec)
			if rec.Header().Get("X-Hwid-Active") != "true" {
				t.Fatalf("%s allowed response missing the active HWID header", path)
			}
		}
	}

	for _, path := range routes {
		rec := requestSub(t, router, http.MethodGet, path, "device-two", "")
		assertServesNotice(t, path, rec, limitOneNotice)
		if rec.Header().Get("X-Hwid-Max-Devices-Reached") != "true" || rec.Header().Get("X-Hwid-Limit") != "true" {
			t.Fatalf("%s limit headers missing: %#v", path, rec.Header())
		}
	}
	if rec := requestSub(t, router, http.MethodHead, "/sub/"+subID, "device-two", ""); rec.Code != http.StatusOK || rec.Header().Get("X-Hwid-Max-Devices-Reached") != "true" {
		t.Fatalf("HEAD over the limit = %d %#v, want 200 with the limit header", rec.Code, rec.Header())
	}

	for _, path := range routes {
		assertServesRealServers(t, path, requestSub(t, router, http.MethodGet, path, "device-one", ""))
	}
	// Twelve fetches from the registered device are still one device.
	assertHwidStatus(t, requestSub(t, router, http.MethodGet, "/sub/"+subID+"/hwid-status", "", ""), true, 1, 1, 0, true)
}

func TestDeviceLimitNoticeTextIsConfigurable(t *testing.T) {
	router, subID := initHwidSubRouter(t, 1)
	setSubSetting(t, "subHwidLimitNotice", "Лимит устройств: {{LIMIT}}. Напишите продавцу.")
	requestSub(t, router, http.MethodGet, "/sub/"+subID, "device-one", "")

	path := "/sub/" + subID
	assertServesNotice(t, path, requestSub(t, router, http.MethodGet, path, "device-two", ""), "Лимит устройств: 1. Напишите продавцу.")
}

// Apps that send no device id share one slot by default, so dropping the header is no way past
// the limit; "allow" serves them uncounted and "refuse" answers with its own notice.
func TestDeviceLimitNoIdPolicy(t *testing.T) {
	router, subID := initHwidSubRouter(t, 1)
	routes := hwidBodyRoutes(subID)
	statusPath := "/sub/" + subID + "/hwid-status"
	path := "/sub/" + subID

	for _, path := range routes {
		assertServesRealServers(t, path, requestSub(t, router, http.MethodGet, path, "", ""))
	}
	assertHwidStatus(t, requestSub(t, router, http.MethodGet, statusPath, "", ""), true, 1, 1, 0, true)
	assertServesNotice(t, path, requestSub(t, router, http.MethodGet, path, "device-one", ""), limitOneNotice)

	setSubSetting(t, "subHwidNoIdPolicy", service.HwidNoIdAllow)
	for _, path := range routes {
		assertServesRealServers(t, path, requestSub(t, router, http.MethodGet, path, "", ""))
	}
	assertHwidStatus(t, requestSub(t, router, http.MethodGet, statusPath, "", ""), true, 1, 0, 1, false)
	assertServesRealServers(t, path, requestSub(t, router, http.MethodGet, path, "device-one", ""))

	setSubSetting(t, "subHwidNoIdPolicy", service.HwidNoIdRefuse)
	for _, path := range routes {
		rec := requestSub(t, router, http.MethodGet, path, "", "")
		assertServesNotice(t, path, rec, service.DefaultSubHwidNoIdNotice)
		if rec.Header().Get("X-Hwid-Active") != "true" || rec.Header().Get("X-Hwid-Not-Supported") != "true" {
			t.Fatalf("%s missing HWID headers = %#v", path, rec.Header())
		}
	}
	assertServesRealServers(t, path, requestSub(t, router, http.MethodGet, path, "device-one", ""))
}

// With no limit on the client and none on the panel the gate changes nothing: every device,
// with or without an id, gets the same body and no gate header.
func TestDeviceLimitOffLeavesOutputUntouched(t *testing.T) {
	router, subID := initHwidSubRouter(t, 0)

	for _, path := range hwidBodyRoutes(subID) {
		baseline := requestSub(t, router, http.MethodGet, path, "", "")
		assertServesRealServers(t, path, baseline)
		for _, hwid := range []string{"", "device-one", "device-two", "device-three"} {
			rec := requestSub(t, router, http.MethodGet, path, hwid, "")
			if rec.Code != baseline.Code || rec.Body.String() != baseline.Body.String() {
				t.Fatalf("%s for %q differs from the body without a device id:\n%q\nvs\n%q", path, hwid, rec.Body.String(), baseline.Body.String())
			}
			for _, header := range []string{"X-Hwid-Active", "X-Hwid-Limit", "X-Hwid-Not-Supported", "X-Hwid-Max-Devices-Reached", "Announce"} {
				if value := rec.Header().Get(header); value != "" {
					t.Fatalf("%s for %q carries %s = %q with the limit off", path, hwid, header, value)
				}
			}
		}
	}
}

// Two new devices racing for the last slot: exactly one may get the servers.
func TestDeviceLimitConcurrentFirstFetchesDoNotOverAdmit(t *testing.T) {
	router, subID := initHwidSubRouter(t, 2)
	path := "/sub/" + subID
	assertServesRealServers(t, path, requestSub(t, router, http.MethodGet, path, "device-one", ""))

	// Widen the window between the gate's device count and its insert, so an
	// unserialized gate over-admits every time and not once in a while.
	db := database.GetDB()
	if err := db.Callback().Query().After("gorm:query").Register("t:device-limit:widen", func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "client_hwids" {
			time.Sleep(20 * time.Millisecond)
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("t:device-limit:widen") })

	const racers = 4
	served := make([]bool, racers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range racers {
		wg.Go(func() {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Host = "sub.example.com"
			req.Header.Set("X-HWID", "racing-device-"+string(rune('a'+i)))
			rec := httptest.NewRecorder()
			<-start
			router.ServeHTTP(rec, req)
			served[i] = rec.Code == http.StatusOK && rec.Header().Get("X-Hwid-Max-Devices-Reached") == ""
		})
	}
	close(start)
	wg.Wait()

	admitted := 0
	for _, ok := range served {
		if ok {
			admitted++
		}
	}
	var registered int64
	if err := database.GetDB().Model(&model.ClientHwid{}).Where("sub_id = ?", subID).Count(&registered).Error; err != nil {
		t.Fatalf("count devices: %v", err)
	}
	if admitted != 1 || registered != 2 {
		t.Fatalf("admitted=%d registered=%d of %d racing devices, want 1 admitted and 2 registered with limit 2", admitted, registered, racers)
	}
}

// A device that stopped fetching for longer than the expiry frees its slot for a new one.
func TestDeviceLimitExpiryFreesSlotOnFetch(t *testing.T) {
	router, subID := initHwidSubRouter(t, 1)
	path := "/sub/" + subID
	assertServesRealServers(t, path, requestSub(t, router, http.MethodGet, path, "device-one", ""))
	assertServesNotice(t, path, requestSub(t, router, http.MethodGet, path, "device-two", ""), limitOneNotice)

	stale := time.Now().AddDate(0, 0, -31).UnixMilli()
	if err := database.GetDB().Model(&model.ClientHwid{}).Where("sub_id = ?", subID).UpdateColumn("last_seen", stale).Error; err != nil {
		t.Fatalf("age the device: %v", err)
	}
	assertServesRealServers(t, path, requestSub(t, router, http.MethodGet, path, "device-two", ""))
}

// The panel shows "last subscription fetch"; a refused device fetched a notice, not the subscription.
func TestDeviceLimitNoticeIsNotRecordedAsFetch(t *testing.T) {
	router, subID := initHwidSubRouter(t, 1)
	db := database.GetDB()
	if err := db.Create(&xray.ClientTraffic{Email: "route@example.com"}).Error; err != nil {
		t.Fatalf("seed traffic row: %v", err)
	}
	lastFetch := func() int64 {
		t.Helper()
		var traffic xray.ClientTraffic
		if err := db.Where("email = ?", "route@example.com").First(&traffic).Error; err != nil {
			t.Fatalf("read traffic row: %v", err)
		}
		return traffic.LastSubFetch
	}
	path := "/sub/" + subID
	requestSub(t, router, http.MethodGet, path, "device-one", "")
	served := lastFetch()
	if served == 0 {
		t.Fatal("a served fetch did not record last_sub_fetch")
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", "route@example.com").Update("last_sub_fetch", 0).Error; err != nil {
		t.Fatalf("reset last_sub_fetch: %v", err)
	}
	assertServesNotice(t, path, requestSub(t, router, http.MethodGet, path, "device-two", ""), limitOneNotice)
	if got := lastFetch(); got != 0 {
		t.Fatalf("last_sub_fetch after a refused fetch = %d, want it untouched", got)
	}
}

// The public page tells the subscriber how many devices are registered, and nothing about them.
func TestSubInfoShowsDeviceCountOnlyUnderLimit(t *testing.T) {
	info := func(t *testing.T, router *gin.Engine, subID string) map[string]any {
		t.Helper()
		rec := requestSub(t, router, http.MethodGet, "/sub/"+subID+"?format=info", "", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("info status = %d, body=%q", rec.Code, rec.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode info %q: %v", rec.Body.String(), err)
		}
		return got
	}

	t.Run("limited", func(t *testing.T) {
		router, subID := initHwidSubRouter(t, 3)
		for _, hwid := range []string{"device-one", "device-two"} {
			requestSub(t, router, http.MethodGet, "/sub/"+subID, hwid, "")
		}
		got := info(t, router, subID)
		if got["deviceLimit"] != float64(3) || got["deviceCount"] != float64(2) {
			t.Fatalf("deviceLimit=%v deviceCount=%v, want 3 and 2", got["deviceLimit"], got["deviceCount"])
		}
		if body, _ := json.Marshal(got); strings.Contains(string(body), "device-one") || strings.Contains(string(body), "fingerprint") {
			t.Fatalf("info names a device: %s", body)
		}
	})

	t.Run("unlimited", func(t *testing.T) {
		router, subID := initHwidSubRouter(t, 0)
		requestSub(t, router, http.MethodGet, "/sub/"+subID, "device-one", "")
		got := info(t, router, subID)
		for _, key := range []string{"deviceLimit", "deviceCount"} {
			if _, present := got[key]; present {
				t.Fatalf("info carries %s = %v with no device limit", key, got[key])
			}
		}
	})
}
