package job

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"

	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/proxy/vless"
	"google.golang.org/grpc"
	"gorm.io/gorm"
)

// A reverse client has to be left alone: the cycle drops its reverse outbound
// handler and re-adds an account without a reverse, which no retry restores.
func TestDisconnectClientTemporarilySkipsReverseClient(t *testing.T) {
	setupIntegrationDB(t)

	const email = "rev-limit-probe"
	inbound := &model.Inbound{
		Id:       1,
		Protocol: model.VLESS,
		Tag:      "rev-limit-probe-tag",
		Settings: `{"clients":[]}`,
	}
	clients := []model.Client{{Email: email, ID: "11111111-1111-1111-1111-111111111111", Reverse: &model.ClientReverse{Tag: "rev-out"}}}

	(&CheckClientIpJob{}).disconnectClientTemporarily(inbound, email, clients)

	var skipped, attempted bool
	for _, line := range logger.GetLogs(500, "warning") {
		if strings.Contains(line, "Not disconnecting "+email) {
			skipped = true
		}
		if strings.Contains(line, "Failed to remove user "+email) {
			attempted = true
		}
	}
	if attempted {
		t.Fatal("a reverse client must not be removed and re-added")
	}
	if !skipped {
		t.Fatal("the skip must be reported, not silent")
	}
}

// The protocol gate must let hysteria through: XrayAPI supports it, and the
// skip left over-limit Hysteria2 sessions alive until the fail2ban ban caught up.
func TestDisconnectClientTemporarilyAllowsHysteria(t *testing.T) {
	setupIntegrationDB(t)

	const email = "hy2-limit-probe"
	inbound := &model.Inbound{
		Id:       1,
		Protocol: model.Hysteria,
		Tag:      "hy2-limit-probe-tag",
		Settings: `{"clients":[]}`,
	}
	clients := []model.Client{{Email: email, Auth: "secret"}}

	(&CheckClientIpJob{}).disconnectClientTemporarily(inbound, email, clients)

	var unsupported, attempted bool
	for _, line := range logger.GetLogs(500, "warning") {
		if strings.Contains(line, "Temporary disconnect is not supported for protocol hysteria") {
			unsupported = true
		}
		if strings.Contains(line, "Failed to remove user "+email) {
			attempted = true
		}
	}
	if unsupported {
		t.Fatal("hysteria was rejected by the protocol gate")
	}
	if !attempted {
		t.Fatal("expected a remove attempt against the Xray API for hysteria")
	}
}

// fakeCoreAPI stands in for the core's HandlerService: it records the user
// operations it is asked for and runs onOp inside each, where another job would act.
type fakeCoreAPI struct {
	command.UnimplementedHandlerServiceServer
	mu    sync.Mutex
	ops   []string
	tags  []string
	onOp  func(op string)
	onAdd func(vlessID string)
	// failAdd makes the core refuse every AddUser; addExists answers it "already exists".
	failAdd   bool
	addExists bool
}

func (f *fakeCoreAPI) AlterInbound(_ context.Context, req *command.AlterInboundRequest) (*command.AlterInboundResponse, error) {
	operation, err := req.Operation.GetInstance()
	if err != nil {
		return nil, err
	}
	op := "add"
	if _, ok := operation.(*command.RemoveUserOperation); ok {
		op = "remove"
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, op)
	f.tags = append(f.tags, req.Tag)
	if add, ok := operation.(*command.AddUserOperation); ok && f.onAdd != nil {
		account, err := add.User.Account.GetInstance()
		if err != nil {
			return nil, err
		}
		if vlessAccount, ok := account.(*vless.Account); ok {
			f.onAdd(vlessAccount.Id)
		}
	}
	f.onOp(op)
	if op == "add" && f.failAdd {
		return nil, errors.New("core refuses the user")
	}
	if op == "add" && f.addExists {
		return nil, errors.New("user already exists")
	}
	return &command.AlterInboundResponse{}, nil
}

func startFakeCoreAPI(t *testing.T, core *fakeCoreAPI) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	command.RegisterHandlerServiceServer(server, core)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	binDir := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", binDir)
	config := fmt.Sprintf(`{"inbounds":[{"tag":"api","port":%d}]}`, listener.Addr().(*net.TCPAddr).Port)
	if err := os.WriteFile(filepath.Join(binDir, "config.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Whatever took the client out of service since the scan has already been to the core,
// so the account must not come back.
func TestDisconnectClientTemporarilyDoesNotRestoreDroppedClient(t *testing.T) {
	const email = "readd-probe"
	trafficJobDisables := func(db *gorm.DB, _ *model.Inbound) error {
		return db.Model(&xray.ClientTraffic{}).Where("email = ?", email).Update("enable", false).Error
	}
	tests := []struct {
		name    string
		during  string
		drop    func(db *gorm.DB, inbound *model.Inbound) error
		wantOps []string
	}{
		{name: "still served", wantOps: []string{"remove", "add"}},
		{name: "expired while removed", during: "remove", drop: trafficJobDisables, wantOps: []string{"remove"}},
		{name: "expired while being re-added", during: "add", drop: trafficJobDisables, wantOps: []string{"remove", "add", "remove"}},
		{name: "disabled by the operator", during: "remove", wantOps: []string{"remove"}, drop: func(db *gorm.DB, _ *model.Inbound) error {
			return db.Model(&model.ClientRecord{}).Where("email = ?", email).Update("enable", false).Error
		}},
		{name: "detached from the inbound", during: "remove", wantOps: []string{"remove"}, drop: func(db *gorm.DB, inbound *model.Inbound) error {
			return db.Where("inbound_id = ?", inbound.Id).Delete(&model.ClientInbound{}).Error
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupIntegrationDB(t)
			db := database.GetDB()
			inbound := seedLinkedInboundWithClient(t, "readd-probe-tag", email, 1)
			if err := db.Model(&model.ClientRecord{}).Where("email = ?", email).Update("enable", true).Error; err != nil {
				t.Fatal(err)
			}
			stored := `{"clients":[{"email":"` + email + `","id":"11111111-1111-1111-1111-111111111111","enable":true}]}`
			if err := db.Model(&model.Inbound{}).Where("id = ?", inbound.Id).Update("settings", stored).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&xray.ClientTraffic{InboundId: inbound.Id, Email: email, Enable: true}).Error; err != nil {
				t.Fatal(err)
			}

			core := &fakeCoreAPI{}
			core.onOp = func(op string) {
				if op == tt.during {
					if err := tt.drop(db, inbound); err != nil {
						t.Error(err)
					}
				}
			}
			startFakeCoreAPI(t, core)

			clients := []model.Client{{Email: email, ID: "11111111-1111-1111-1111-111111111111", Enable: true}}
			(&CheckClientIpJob{}).disconnectClientTemporarily(inbound, email, clients)

			core.mu.Lock()
			defer core.mu.Unlock()
			if !slices.Equal(core.ops, tt.wantOps) {
				t.Fatalf("core saw %v, want %v", core.ops, tt.wantOps)
			}
		})
	}
}

// An admin who rotates a client's credential after the scan has already sent the new one to
// the core; the re-add must restore that one, not the scan's copy of the retired credential.
func TestDisconnectClientTemporarilyRestoresTheStoredCredential(t *testing.T) {
	const email = "rotate-probe"
	const retired = "11111111-1111-1111-1111-111111111111"
	const current = "22222222-2222-2222-2222-222222222222"
	setupIntegrationDB(t)
	db := database.GetDB()
	inbound := seedLinkedInboundWithClient(t, "rotate-probe-tag", email, 1)
	if err := db.Model(&model.ClientRecord{}).Where("email = ?", email).Update("enable", true).Error; err != nil {
		t.Fatal(err)
	}
	rotated := `{"clients":[{"email":"` + email + `","id":"` + current + `","enable":true}]}`
	if err := db.Model(&model.Inbound{}).Where("id = ?", inbound.Id).Update("settings", rotated).Error; err != nil {
		t.Fatal(err)
	}
	var added []string
	core := &fakeCoreAPI{}
	core.onAdd = func(id string) { added = append(added, id) }
	core.onOp = func(string) {}
	startFakeCoreAPI(t, core)

	scan := []model.Client{{Email: email, ID: retired, Enable: true}}
	(&CheckClientIpJob{}).disconnectClientTemporarily(inbound, email, scan)

	core.mu.Lock()
	defer core.mu.Unlock()
	if !slices.Equal(added, []string{current}) {
		t.Fatalf("core re-added ids %v, want only the stored %s", added, current)
	}
}

// After RemoveUser no failure may leave the client out of the core for good: a passing
// failure is retried at once, a lasting one stays owed and the next run puts the client back.
func TestDisconnectClientTemporarilyDoesNotStrandTheClient(t *testing.T) {
	const email = "strand-probe"
	const stored = `{"clients":[{"email":"` + email + `","id":"11111111-1111-1111-1111-111111111111","enable":true}]}`
	breakSettings := func(db *gorm.DB, inbound *model.Inbound, broken bool) error {
		settings := stored
		if broken {
			settings = `{"clients":[{"email":`
		}
		return db.Model(&model.Inbound{}).Where("id = ?", inbound.Id).Update("settings", settings).Error
	}
	breakServedCheck := func(db *gorm.DB, _ *model.Inbound, broken bool) error {
		from, to := "client_inbounds_gone", "client_inbounds"
		if broken {
			from, to = to, from
		}
		return db.Exec("ALTER TABLE " + from + " RENAME TO " + to).Error
	}
	for _, tc := range []struct {
		name string
		// fail breaks or mends what the restore needs; nil leaves the database alone.
		fail        func(db *gorm.DB, inbound *model.Inbound, broken bool) error
		healOnRetry bool
		failAdd     bool
		wantOps     []string
		wantOwed    bool
	}{
		{name: "the settings read fails once", fail: breakSettings, healOnRetry: true, wantOps: []string{"remove", "add"}},
		{name: "the settings read keeps failing", fail: breakSettings, wantOps: []string{"remove"}, wantOwed: true},
		{name: "the served check keeps failing", fail: breakServedCheck, wantOps: []string{"remove"}, wantOwed: true},
		{name: "the re-add fails", failAdd: true, wantOps: []string{"remove", "add", "add", "add"}, wantOwed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupIntegrationDB(t)
			db := database.GetDB()
			inbound := seedLinkedInboundWithClient(t, "strand-probe-tag", email, 1)
			if err := db.Model(&model.ClientRecord{}).Where("email = ?", email).Update("enable", true).Error; err != nil {
				t.Fatal(err)
			}
			if err := breakSettings(db, inbound, false); err != nil {
				t.Fatal(err)
			}
			mend := func() {
				if tc.fail != nil {
					if err := tc.fail(db, inbound, false); err != nil {
						t.Error(err)
					}
				}
			}
			oldPause := limitIPRetryPause
			limitIPRetryPause = func() {
				if tc.healOnRetry {
					mend()
				}
			}
			t.Cleanup(func() { limitIPRetryPause = oldPause })

			core := &fakeCoreAPI{failAdd: tc.failAdd}
			core.onOp = func(op string) {
				if op == "remove" && tc.fail != nil {
					if err := tc.fail(db, inbound, true); err != nil {
						t.Error(err)
					}
				}
			}
			startFakeCoreAPI(t, core)

			job := &CheckClientIpJob{}
			clients := []model.Client{{Email: email, ID: "11111111-1111-1111-1111-111111111111", Enable: true}}
			job.disconnectClientTemporarily(inbound, email, clients)

			ops := func() []string {
				core.mu.Lock()
				defer core.mu.Unlock()
				return slices.Clone(core.ops)
			}
			if got := ops(); !slices.Equal(got, tc.wantOps) {
				t.Fatalf("core saw %v, want %v", got, tc.wantOps)
			}
			if got := len(job.owedRestores) == 1; got != tc.wantOwed {
				t.Fatalf("restores owed = %v, want owed %v", job.owedRestores, tc.wantOwed)
			}
			if !tc.wantOwed {
				return
			}
			// What failed works again, the inbound was renamed and the panel restarted (a new
			// job, the same core): the next run re-adds the client under the tag stored now.
			if !tc.healOnRetry {
				mend()
			}
			if err := db.Model(&model.Inbound{}).Where("id = ?", inbound.Id).Update("tag", "strand-probe-renamed").Error; err != nil {
				t.Fatal(err)
			}
			core.mu.Lock()
			core.failAdd = false
			core.mu.Unlock()
			next := &CheckClientIpJob{}
			next.settleOwedRestores()
			core.mu.Lock()
			lastOp, lastTag := core.ops[len(core.ops)-1], core.tags[len(core.tags)-1]
			core.mu.Unlock()
			if len(next.owedRestores) != 0 || lastOp != "add" || lastTag != "strand-probe-renamed" {
				t.Fatalf("after the next run: last op %s on %q, still owed %v; want the client re-added under the new tag", lastOp, lastTag, next.owedRestores)
			}
			again := &CheckClientIpJob{}
			again.settleOwedRestores()
			if got := ops(); len(got) != len(tc.wantOps)+1 {
				t.Fatalf("a settled restore was replayed: core saw %v", got)
			}
		})
	}
}

// A restore that fails while the stored list cannot be read must not replace that list: what
// an earlier panel run still owes is kept, and both are stored once the database answers.
func TestOwedRestoresAreNotOverwrittenWhileTheStoredListIsUnread(t *testing.T) {
	setupIntegrationDB(t)
	db := database.GetDB()
	earlier, later := owedRestore{InboundID: 1, Email: "earlier"}, owedRestore{InboundID: 2, Email: "later"}
	before := &CheckClientIpJob{owedRestores: []owedRestore{earlier}}
	before.saveOwedRestores()

	job := &CheckClientIpJob{}
	if err := db.Exec("ALTER TABLE settings RENAME TO settings_gone").Error; err != nil {
		t.Fatal(err)
	}
	job.owedRestores = append(job.owedRestores, later)
	job.saveOwedRestores()
	if err := db.Exec("ALTER TABLE settings_gone RENAME TO settings").Error; err != nil {
		t.Fatal(err)
	}
	if !job.owedUnsaved {
		t.Fatal("the job forgot it still has to store its list")
	}
	job.saveOwedRestores()

	after := &CheckClientIpJob{}
	after.loadOwedRestores()
	if !slices.Contains(after.owedRestores, earlier) || !slices.Contains(after.owedRestores, later) || len(after.owedRestores) != 2 {
		t.Fatalf("stored owed restores = %v, want both %v and %v", after.owedRestores, earlier, later)
	}
}

// When the served check fails right after the re-add, a commit may have removed the client
// meanwhile, so the restore is not done: it stays owed and the next run asks again.
func TestRestoreStaysOwedWhenTheCheckAfterTheReAddFails(t *testing.T) {
	for _, tc := range []struct {
		name      string
		addExists bool
		want      []string
	}{
		// Not known to be served: the user this re-add put back comes out until a later run can check.
		{"re-added", false, []string{"add", "remove"}},
		// A user something else put back is not this restore's to take out.
		{"already there", true, []string{"add"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const email = "after-add-probe"
			setupIntegrationDB(t)
			db := database.GetDB()
			inbound := seedLinkedInboundWithClient(t, "after-add-probe-tag", email, 1)
			if err := db.Model(&model.ClientRecord{}).Where("email = ?", email).Update("enable", true).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&model.Inbound{}).Where("id = ?", inbound.Id).Update("settings", `{"clients":[{"email":"`+email+`","id":"11111111-1111-1111-1111-111111111111","enable":true}]}`).Error; err != nil {
				t.Fatal(err)
			}
			core := &fakeCoreAPI{addExists: tc.addExists}
			core.onOp = func(op string) {
				if op == "add" {
					if err := db.Exec("ALTER TABLE client_inbounds RENAME TO client_inbounds_gone").Error; err != nil {
						t.Error(err)
					}
				}
			}
			startFakeCoreAPI(t, core)
			var xrayAPI xray.XrayAPI
			if err := xrayAPI.Init((&CheckClientIpJob{}).resolveXrayAPIPort()); err != nil {
				t.Fatal(err)
			}
			defer xrayAPI.Close()
			if err := (&CheckClientIpJob{}).restoreRemovedClient(&xrayAPI, owedRestore{InboundID: inbound.Id, Email: email}); err == nil {
				t.Fatal("the restore counted as done though the check after the re-add failed")
			}
			core.mu.Lock()
			defer core.mu.Unlock()
			if !slices.Equal(core.ops, tc.want) {
				t.Fatalf("core operations = %v, want %v", core.ops, tc.want)
			}
		})
	}
}

// The restore is stored before RemoveUser: a panel killed right after the removal comes back
// owing the client, and a restore that succeeds leaves nothing stored.
func TestDisconnectClientTemporarilyStoresTheRestoreBeforeRemoving(t *testing.T) {
	const email = "kill-probe"
	setupIntegrationDB(t)
	db := database.GetDB()
	inbound := seedLinkedInboundWithClient(t, "kill-probe-tag", email, 1)
	if err := db.Model(&model.ClientRecord{}).Where("email = ?", email).Update("enable", true).Error; err != nil {
		t.Fatal(err)
	}
	settings := `{"clients":[{"email":"` + email + `","id":"33333333-3333-3333-3333-333333333333","enable":true}]}`
	if err := db.Model(&model.Inbound{}).Where("id = ?", inbound.Id).Update("settings", settings).Error; err != nil {
		t.Fatal(err)
	}
	stored := func() string {
		var row model.Setting
		if err := db.Where("key = ?", owedRestoresKey).Limit(1).Find(&row).Error; err != nil {
			t.Fatal(err)
		}
		return row.Value
	}
	atRemove := ""
	core := &fakeCoreAPI{}
	core.onOp = func(op string) {
		if op == "remove" {
			atRemove = stored()
		}
	}
	startFakeCoreAPI(t, core)

	(&CheckClientIpJob{}).disconnectClientTemporarily(inbound, email, []model.Client{{Email: email, Enable: true}})

	if !strings.Contains(atRemove, `"email":"`+email+`"`) {
		t.Fatalf("stored owed restores when the core removed the client = %q, want %s owed", atRemove, email)
	}
	if after := stored(); after != "" {
		t.Fatalf("stored owed restores after a successful re-add = %q, want none", after)
	}
}
