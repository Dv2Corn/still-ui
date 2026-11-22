package sub

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/middleware"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

var subRotationStepOrder = []string{
	service.SubRotationStepInbound,
	service.SubRotationStepCreate,
	service.SubRotationStepFetch,
	service.SubRotationStepReissue,
	service.SubRotationStepNewLink,
	service.SubRotationStepOldLink,
	service.SubRotationStepCleanup,
}

// rotationCheckAgainst runs the check against the real subscription controller behind an
// httptest server; wrap lets a test break the server the way a faulty deployment would.
func rotationCheckAgainst(t *testing.T, wrap func(http.Handler) http.Handler, settings ...[2]string) service.SubRotationReport {
	t.Helper()
	router, _ := initHwidSubRouter(t, 0)
	for _, kv := range settings {
		setSubSetting(t, kv[0], kv[1])
	}
	var handler http.Handler = router
	if wrap != nil {
		handler = wrap(router)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	check := &service.SubRotationCheck{
		Clients:  &service.ClientService{},
		Inbounds: &service.InboundService{},
		SubURL:   func(subID string) string { return srv.URL + "/sub/" + subID },
		HTTP:     srv.Client(),
	}
	return check.Run(t.Context())
}

func assertNoTestClientLeft(t *testing.T, report service.SubRotationReport) {
	t.Helper()
	if !strings.HasPrefix(report.Client, service.SubRotationClientPrefix) {
		t.Fatalf("test client %q is not named with %q", report.Client, service.SubRotationClientPrefix)
	}
	var rec model.ClientRecord
	err := database.GetDB().Where("email = ?", report.Client).First(&rec).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("test client %q still stored after the check (err=%v)", report.Client, err)
	}
}

func stepNames(report service.SubRotationReport) []string {
	names := make([]string, 0, len(report.Steps))
	for _, step := range report.Steps {
		names = append(names, step.Name)
	}
	return names
}

func TestSubRotationCheckPassesOnRealRotation(t *testing.T) {
	report := rotationCheckAgainst(t, nil)
	if !report.Passed {
		t.Fatalf("check failed on a correct rotation: %+v", report.Steps)
	}
	if got := strings.Join(stepNames(report), ","); got != strings.Join(subRotationStepOrder, ",") {
		t.Fatalf("steps = %s, want %s", got, strings.Join(subRotationStepOrder, ","))
	}
	for _, step := range report.Steps {
		if !step.OK {
			t.Fatalf("step %s failed on a passing report: %q", step.Name, step.Detail)
		}
	}
	assertNoTestClientLeft(t, report)
}

// A device gate that refuses apps sending no device id must not fail the check: it sends one
// like a real client, so the gate gives it a slot instead of the notice entry.
func TestSubRotationCheckPassesBehindTheDeviceLimit(t *testing.T) {
	report := rotationCheckAgainst(t, nil,
		[2]string{"subHwidDefaultLimit", "1"},
		[2]string{"subHwidNoIdPolicy", service.HwidNoIdRefuse})
	if !report.Passed {
		t.Fatalf("check failed behind the device limit: %+v", report.Steps)
	}
	assertNoTestClientLeft(t, report)
}

// staleCache serves a path it once answered with 200 from memory, as a misconfigured cache or
// CDN in front of the subscription server would after a rotation.
func staleCache(next http.Handler) http.Handler {
	var mu sync.Mutex
	cached := map[string][]byte{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		body, hit := cached[r.URL.Path]
		mu.Unlock()
		if hit {
			_, _ = w.Write(body)
			return
		}
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)
		if rec.Code == http.StatusOK {
			mu.Lock()
			cached[r.URL.Path] = bytes.Clone(rec.Body.Bytes())
			mu.Unlock()
		}
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}

func TestSubRotationCheckNamesTheStepWhenTheOldLinkStillServes(t *testing.T) {
	report := rotationCheckAgainst(t, staleCache)
	if report.Passed {
		t.Fatalf("check passed while the old link kept serving: %+v", report.Steps)
	}
	var failed []string
	for _, step := range report.Steps {
		if !step.OK {
			failed = append(failed, step.Name+": "+step.Detail)
		}
	}
	if len(failed) != 1 || !strings.HasPrefix(failed[0], service.SubRotationStepOldLink+": ") || !strings.Contains(failed[0], "HTTP 200") {
		t.Fatalf("failed steps = %q, want only %s reporting HTTP 200", failed, service.SubRotationStepOldLink)
	}
	if got := strings.Join(stepNames(report), ","); got != strings.Join(subRotationStepOrder, ",") {
		t.Fatalf("steps = %s, want %s", got, strings.Join(subRotationStepOrder, ","))
	}
	assertNoTestClientLeft(t, report)
}

// A failure right after the client is created still ends in its deletion: the server below
// fails every fetch, and it sees the client stored while it does.
func TestSubRotationCheckDeletesTheClientWhenAStepFails(t *testing.T) {
	var sawClient bool
	failing := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var n int64
			database.GetDB().Model(&model.ClientRecord{}).Where("email LIKE ?", service.SubRotationClientPrefix+"%").Count(&n)
			sawClient = sawClient || n == 1
			w.WriteHeader(http.StatusInternalServerError)
		})
	}
	report := rotationCheckAgainst(t, failing)
	if report.Passed {
		t.Fatalf("check passed against a failing server: %+v", report.Steps)
	}
	if !sawClient {
		t.Fatal("the test client was not stored while the subscription was fetched")
	}
	want := []string{service.SubRotationStepInbound, service.SubRotationStepCreate, service.SubRotationStepFetch, service.SubRotationStepCleanup}
	if got := strings.Join(stepNames(report), ","); got != strings.Join(want, ",") {
		t.Fatalf("steps = %s, want %s", got, strings.Join(want, ","))
	}
	fetch, cleanup := report.Steps[2], report.Steps[3]
	if fetch.OK || !strings.Contains(fetch.Detail, "HTTP 500") {
		t.Fatalf("fetch step = %+v, want a failure naming HTTP 500", fetch)
	}
	if !cleanup.OK {
		t.Fatalf("cleanup step = %+v, want it to succeed", cleanup)
	}
	assertNoTestClientLeft(t, report)
}

// The CLI and the panel aim the check through the sub settings alone: the listener's port over
// loopback, the links path, and the sub domain as Host, which the real domain gate demands.
func TestSubRotationCheckReachesTheConfiguredSubServer(t *testing.T) {
	for _, domain := range []string{"", "sub.example.com"} {
		t.Run("domain="+domain, func(t *testing.T) {
			router, _ := initHwidSubRouter(t, 0)
			var handler http.Handler = router
			if domain != "" {
				gated := gin.New()
				gated.Use(middleware.DomainValidatorMiddleware(domain))
				gated.Any("/*path", func(c *gin.Context) { router.HandleContext(c) })
				handler = gated
			}
			srv := httptest.NewServer(handler)
			t.Cleanup(srv.Close)
			port := srv.Listener.Addr().(*net.TCPAddr).Port
			setSubSetting(t, "subPort", strconv.Itoa(port))
			setSubSetting(t, "subDomain", domain)
			setSubSetting(t, "subPath", "/sub/")

			check, err := service.NewSubRotationCheck(&service.SettingService{}, &service.ClientService{}, &service.InboundService{})
			if err != nil {
				t.Fatalf("NewSubRotationCheck: %v", err)
			}
			report := check.Run(t.Context())
			if !report.Passed {
				t.Fatalf("check through the sub settings failed: %+v", report.Steps)
			}
			assertNoTestClientLeft(t, report)
		})
	}
}

func TestSubRotationCheckRefusesADisabledSubServer(t *testing.T) {
	initHwidSubRouter(t, 0)
	setSubSetting(t, "subEnable", "false")
	_, err := service.NewSubRotationCheck(&service.SettingService{}, &service.ClientService{}, &service.InboundService{})
	if err == nil || err.Error() != "the subscription server is disabled" {
		t.Fatalf("err = %v, want the disabled subscription server named", err)
	}
}
