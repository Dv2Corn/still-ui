package service

import (
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
)

func initAuditDB(t *testing.T) {
	t.Helper()
	t.Setenv("XUI_DB_FOLDER", t.TempDir())
	dbtest.InitDB(t, config.GetDBPath())
}

// recordAudit appends n entries and returns them in chain order.
func recordAudit(t *testing.T, s *AuditService, n int, at time.Time) []model.AuditLog {
	t.Helper()
	for i := range n {
		err := s.Record(model.AuditLog{
			Time: at.Add(time.Duration(i) * time.Second).UnixMilli(), Actor: "admin", IP: "203.0.113.9",
			Action: "POST /panel/api/inbounds/update/:id", Target: "id=" + string(rune('1'+i)), Outcome: AuditSuccess,
		})
		if err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	var rows []model.AuditLog
	if err := database.GetDB().Order("id").Find(&rows).Error; err != nil {
		t.Fatalf("read journal: %v", err)
	}
	return rows
}

func TestAuditVerifyReportsTheFirstBrokenEntry(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		// tamper changes the journal and returns the index of the entry Verify must name.
		tamper func(t *testing.T, rows []model.AuditLog) int
	}{
		{"edited row", auditReasonChanged, func(t *testing.T, rows []model.AuditLog) int {
			if err := database.GetDB().Model(&model.AuditLog{}).Where("id = ?", rows[1].Id).Update("outcome", AuditFailure).Error; err != nil {
				t.Fatal(err)
			}
			return 1
		}},
		{"deleted middle row", auditReasonMissing, func(t *testing.T, rows []model.AuditLog) int {
			if err := database.GetDB().Delete(&model.AuditLog{}, rows[1].Id).Error; err != nil {
				t.Fatal(err)
			}
			return 2
		}},
		{"deleted first row", auditReasonMissing, func(t *testing.T, rows []model.AuditLog) int {
			if err := database.GetDB().Delete(&model.AuditLog{}, rows[0].Id).Error; err != nil {
				t.Fatal(err)
			}
			return 1
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initAuditDB(t)
			s := &AuditService{}
			rows := recordAudit(t, s, 4, time.Now())
			if got, err := s.Verify(); err != nil || !got.Ok || got.Checked != 4 {
				t.Fatalf("untouched journal: Verify() = %+v, %v; want ok with 4 checked", got, err)
			}
			want := rows[tc.tamper(t, rows)].Id
			got, err := s.Verify()
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if got.Ok || got.BrokenId != want || got.Reason != tc.reason {
				t.Fatalf("Verify() = %+v; want broken at entry %d: %s", got, want, tc.reason)
			}
		})
	}
}

func TestAuditRecordsLoginAttemptsFromTheBus(t *testing.T) {
	initAuditDB(t)
	s := &AuditService{}
	at := time.UnixMilli(1735689600000)
	s.HandleEvent(eventbus.Event{Type: eventbus.EventCPUHigh, Timestamp: at})
	for _, status := range []string{"fail", "success"} {
		s.HandleEvent(eventbus.Event{Type: eventbus.EventLoginAttempt, Timestamp: at, Data: &eventbus.LoginEventData{
			Username: "admin", IP: "203.0.113.9", Status: status, Reason: map[string]string{"fail": "invalid credentials"}[status],
		}})
	}
	var rows []model.AuditLog
	if err := database.GetDB().Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("journal = %+v; want the two logins and not the cpu event", rows)
	}
	want := []model.AuditLog{
		{Time: at.UnixMilli(), Actor: "admin", IP: "203.0.113.9", Action: AuditActionLogin, Outcome: AuditFailure, Detail: "invalid credentials"},
		{Time: at.UnixMilli(), Actor: "admin", IP: "203.0.113.9", Action: AuditActionLogin, Outcome: AuditSuccess},
	}
	for i := range rows {
		rows[i].Id, rows[i].PrevHash, rows[i].Hash = 0, "", ""
		if rows[i] != want[i] {
			t.Errorf("entry %d = %+v; want %+v", i, rows[i], want[i])
		}
	}
}

func TestAuditPruneKeepsTheChainVerifiable(t *testing.T) {
	initAuditDB(t)
	s := &AuditService{}
	now := time.Now()
	recordAudit(t, s, 3, now.AddDate(0, 0, -100))
	kept := recordAudit(t, s, 2, now.AddDate(0, 0, -1))[3:]

	pruned, err := s.Prune(now)
	if err != nil || pruned != 3 {
		t.Fatalf("Prune() = %d, %v; want 3 entries older than the default 90 days", pruned, err)
	}
	var rows []model.AuditLog
	if err := database.GetDB().Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Id != kept[0].Id || rows[1].Id != kept[1].Id {
		t.Fatalf("after prune: %+v; want the 2 recent entries and the prune record", rows)
	}
	if last := rows[2]; last.Action != AuditActionPrune || last.Target != "count=3" || last.Actor != AuditActorSystem {
		t.Fatalf("prune record = %+v", last)
	}
	if got, err := s.Verify(); err != nil || !got.Ok || got.Checked != 3 {
		t.Fatalf("Verify() after prune = %+v, %v; want ok with 3 checked", got, err)
	}
}
