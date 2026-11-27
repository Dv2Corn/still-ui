package database

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// The count a migration logs is its only output when it succeeds: these tests pin that a
// migration with nothing to do logs nothing, and one with two rows to fix logs exactly "2".

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })
	return &buf
}

func mustExec(t *testing.T, q string, args ...any) {
	t.Helper()
	if err := db.Exec(q, args...).Error; err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func twoInbounds(t *testing.T) {
	t.Helper()
	for i, port := range []int{40201, 40202} {
		ib := &model.Inbound{Port: port, Tag: "mig-" + itoa(i), Protocol: "vless", Settings: `{"clients":[]}`, StreamSettings: "{}", Sniffing: "{}"}
		if err := db.Create(ib).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrationsLogExactCounts(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T)
		run   func() error
		msg   string // logged for two fixed rows
		quiet string // a prefix that must not appear when nothing is to be done
	}{
		{"orphaned hosts", func(t *testing.T) {
			for i := 0; i < 2; i++ {
				h := &model.Host{InboundId: 999, GroupId: "g" + itoa(i), Remark: "r", Address: "a.example.com", Port: 443, RandomCount: 1}
				if err := db.Create(h).Error; err != nil {
					t.Fatal(err)
				}
			}
		}, pruneOrphanedHosts, "Pruned 2 orphaned hosts row(s)", "Pruned"},
		{"orphaned client_inbounds", func(t *testing.T) {
			mustExec(t, "INSERT INTO client_inbounds (client_id, inbound_id) VALUES (1, 999), (2, 998)")
		}, pruneOrphanedClientInbounds, "Pruned 2 orphaned client_inbounds row(s)", "Pruned"},
		{"legacy socks", func(t *testing.T) {
			twoInbounds(t)
			mustExec(t, "UPDATE inbounds SET protocol = 'socks' WHERE tag LIKE 'mig-%'")
		}, migrateLegacySocksInboundsToMixed, "Migrated 2 legacy socks inbound(s) to mixed", "Migrated"},
		{"sub_sort_index", func(t *testing.T) {
			twoInbounds(t)
			mustExec(t, "UPDATE inbounds SET sub_sort_index = 0 WHERE tag LIKE 'mig-%'")
		}, normalizeInboundSubSortIndex, "Normalized sub_sort_index on 2 inbound(s)", "Normalized"},
		{"overflowed traffic", func(t *testing.T) {
			twoInbounds(t)
			mustExec(t, "UPDATE inbounds SET up = -5 WHERE tag LIKE 'mig-%'")
		}, repairOverflowedTrafficCounters, "Repaired 2 overflowed inbounds.up value(s)", "Repaired"},
		{"removed shadowsocks cipher", func(t *testing.T) {
			twoInbounds(t)
			mustExec(t, `UPDATE inbounds SET protocol = 'shadowsocks', settings = '{"method":"none","clients":[]}' WHERE tag LIKE 'mig-%'`)
		}, migrateShadowsocksRemovedCiphers, "Rewrote removed shadowsocks cipher on 2 inbound(s)", "Rewrote"},
		{"removed vmess security on clients", func(t *testing.T) {
			for i := 0; i < 2; i++ {
				if err := db.Create(&model.ClientRecord{Email: "vm" + itoa(i) + "@x", Security: "none", Enable: true}).Error; err != nil {
					t.Fatal(err)
				}
			}
		}, migrateVmessRemovedSecurities, "Migrated 2 client(s) off removed vmess security values", "Migrated"},
		{"removed vmess security on inbounds", func(t *testing.T) {
			twoInbounds(t)
			mustExec(t, `UPDATE inbounds SET protocol = 'vmess', settings = '{"clients":[{"email":"a@x","security":"zero"}]}' WHERE tag LIKE 'mig-%'`)
		}, migrateVmessRemovedSecurities, "Rewrote removed vmess security values on 2 inbound(s)", "Rewrote"},
		{"external link enable", func(t *testing.T) {
			for i := 0; i < 2; i++ {
				if err := db.Create(&model.ClientExternalLink{ClientId: i + 1}).Error; err != nil {
					t.Fatal(err)
				}
			}
			mustExec(t, "UPDATE client_external_links SET enable = NULL")
		}, normalizeClientExternalLinkEnable, "Normalized enable on 2 client external link(s)", "Normalized"},
		{"external link timestamps", func(t *testing.T) {
			for i := 0; i < 2; i++ {
				if err := db.Create(&model.ClientExternalLink{ClientId: i + 1}).Error; err != nil {
					t.Fatal(err)
				}
			}
			mustExec(t, "UPDATE client_external_links SET expiry_time = NULL, last_fetch_at = NULL")
		}, normalizeClientExternalLinkTimestamps, "Normalized timestamps on 4 client external link(s)", "Normalized"},
		{"duplicate clients", func(t *testing.T) {
			twoInbounds(t)
			mustExec(t, `UPDATE inbounds SET settings = '{"clients":[{"email":"a@x"},{"email":"A@x"}]}' WHERE tag LIKE 'mig-%'`)
		}, dedupeInboundSettingsClients, "Removed duplicate client entries from 2 inbound(s)", "Removed duplicate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			initTestDB(t)
			buf := captureLog(t)
			if err := c.run(); err != nil {
				t.Fatalf("with nothing to do: %v", err)
			}
			if strings.Contains(buf.String(), c.quiet) {
				t.Fatalf("logged with nothing to do: %q", buf.String())
			}
			c.setup(t)
			buf.Reset()
			if err := c.run(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(buf.String(), c.msg) {
				t.Fatalf("log = %q, want %q", buf.String(), c.msg)
			}
		})
	}
}

// InitDB logs these lines only when the step fails; a normal start logs none of them.
func TestInitDBLogsNoStepFailures(t *testing.T) {
	buf := captureLog(t)
	initTestDB(t)
	initTestDB(t) // a second InitDB also closes the replaced pool
	for _, s := range []string{"close the replaced database pool", "clean SQLite backup directories", "restrict SQLite file permissions"} {
		if strings.Contains(buf.String(), s) {
			t.Errorf("normal InitDB logged %q", s)
		}
	}
}
