package nodee2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

type billingEntry struct {
	M float64 `json:"m"`
}

// nodeBilling reads the multipliers a node stores for its masters, read-only beside the live panel.
func nodeBilling(t *testing.T, p *panel) map[string]billingEntry {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(p.dir, "db", "x-ui.db")+"?mode=ro&_busy_timeout=5000"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := db.DB(); err == nil {
		defer func() { _ = sqlDB.Close() }()
	}
	var rows []model.Setting
	if err := db.Where("key = ?", "nodeTrafficMultipliers").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := map[string]billingEntry{}
	if len(rows) == 1 {
		if err := json.Unmarshal([]byte(rows[0].Value), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestNodeBillingMultiplierReachesNode(t *testing.T) {
	bin := panelBinary(t)
	master := newPanel(t, bin, "billing-master")
	node := newPanel(t, bin, "billing-node")
	master.start()
	node.start()
	var nodeView struct {
		Id int `json:"id"`
	}
	obj := master.call(http.MethodPost, "/panel/api/nodes/add", map[string]any{
		"name": node.name, "scheme": "http", "address": "127.0.0.1", "port": node.port, "basePath": "/",
		"apiToken": node.mintToken("billing-upstream", "node-sync"), "enable": true, "allowPrivateAddress": true,
	})
	if err := json.Unmarshal(obj, &nodeView); err != nil || nodeView.Id == 0 {
		t.Fatalf("decode node add: %v (%s)", err, obj)
	}
	const email = "billed@e2e"
	port := freePort(t)
	master.call(http.MethodPost, "/panel/api/inbounds/add", vlessInbound("billed", port, &nodeView.Id, vlessClient(email)))
	eventually(t, 90*time.Second, "node holds the billed client", func() (bool, string) {
		ib, ok := node.inboundOnPort(port)
		return ok && ib.client(email) != nil, "not on the node yet"
	})
	limit := func(m float64) {
		master.call(http.MethodPost, "/panel/api/nodes/trafficLimit/"+strconv.Itoa(nodeView.Id), map[string]any{
			"trafficLimitBytes": 0, "trafficResetDay": 1, "trafficWarnPercent": 80,
			"trafficLimitAction": "notify", "trafficMultiplier": m,
		})
	}
	limit(0.5)
	node.call(http.MethodPost, "/panel/api/clients/updateTraffic/"+email, map[string]any{"upload": 1000, "download": 2000})
	eventually(t, 90*time.Second, "master bills the node's raw usage at 0.5", func() (bool, string) {
		up, down := panelTraffic(t, master, email)
		return up == 500 && down == 1000, fmt.Sprintf("master usage=%d/%d", up, down)
	})
	eventually(t, 90*time.Second, "node learns the master's 0.5", func() (bool, string) {
		got := nodeBilling(t, node)
		return len(got) == 1 && firstM(got) == 0.5, fmt.Sprintf("node billing=%v", got)
	})
	node.stop()
	node.start()
	if got := nodeBilling(t, node); len(got) != 1 || firstM(got) != 0.5 {
		t.Fatalf("node forgot the multiplier across a restart: %v", got)
	}
	limit(1)
	eventually(t, 90*time.Second, "the node records the master's ×1", func() (bool, string) {
		got := nodeBilling(t, node)
		return len(got) == 1 && firstM(got) == 1, fmt.Sprintf("node billing=%v", got)
	})
}

func firstM(m map[string]billingEntry) float64 {
	for _, v := range m {
		return v.M
	}
	return 0
}
