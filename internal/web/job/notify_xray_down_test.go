package job

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/discord"
)

// The daily Discord report (and the DB backup it carries) was skipped whenever
// Xray was down -- exactly when the admin needs it; the report states Xray's state.
func TestDiscordNotifyJobReportsWhileXrayIsDown(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "discord_job.db")); err != nil {
		t.Fatalf("database.InitDB failed: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
	settings := service.SettingService{}
	for _, err := range []error{
		settings.SetDiscordBotEnable(true),
		settings.SetDiscordBotToken("test-bot-token"),
		settings.SetDiscordChannelId("123456789012345678"),
		settings.SetDiscordBotBackup(false),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if (&service.XrayService{}).IsXrayRunning() {
		t.Skip("an Xray process is running in this test binary")
	}

	var mu sync.Mutex
	var posts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		posts = append(posts, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1"}`))
	}))
	t.Cleanup(srv.Close)
	ds := discord.NewDiscordService(settings)
	ds.SetBaseURL(srv.URL)
	ds.SetHTTPClient(srv.Client())

	NewDiscordNotifyJob(ds).Run()

	mu.Lock()
	defer mu.Unlock()
	for _, p := range posts {
		if strings.HasPrefix(p, "POST ") && strings.HasSuffix(p, "/messages") {
			return
		}
	}
	t.Fatalf("no report was posted while Xray was down; requests: %v", posts)
}

type countingReporter struct{ n int }

func (c *countingReporter) SendReport() { c.n++ }

// Same for the Telegram report: the job returned early while Xray was down.
func TestStatsNotifyJobReportsWhileXrayIsDown(t *testing.T) {
	if (&service.XrayService{}).IsXrayRunning() {
		t.Skip("an Xray process is running in this test binary")
	}
	r := &countingReporter{}
	(&StatsNotifyJob{tgbotService: r}).Run()
	if r.n != 1 {
		t.Fatalf("SendReport called %d times while Xray was down, want 1", r.n)
	}
}
