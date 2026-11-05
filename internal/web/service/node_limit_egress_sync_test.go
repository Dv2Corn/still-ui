package service

import (
	"context"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/egressfilter"
)

// The per-node traffic limit and the egress filter both reach the same node: the
// limit by turning its inbounds off (pushed with the node's config), the filter by
// its own managed-filter call. Neither may undo or block the other.
func TestNodeTrafficLimitAndEgressFilterSyncCoexist(t *testing.T) {
	db := initTrafficTestDB(t)
	collectNodeTrafficEvents(t)
	useFakeEgress(t)
	pushes := recordPushes(t, nil)

	seedNodeRow(t, db, &model.Node{Id: 1, Name: "n1", Address: "127.0.0.1", Port: 2096, ApiToken: "tok", Enable: true})
	setNodeColumns(t, db, 1, map[string]any{"status": "online"})
	createNodeInbound(t, db, 1, "n1-a", 41001)

	egress := &EgressFilterService{}
	if _, err := egress.SetLocal(EgressFilterSettings{Filter: egressfilter.Filter{SMTP: true}, NodeScope: EgressNodeScopeAll}); err != nil {
		t.Fatal(err)
	}
	if got := pushes(); len(got) != 1 || got[0].node != 1 || got[0].req.Filter == nil || !got[0].req.Filter.SMTP {
		t.Fatalf("pushes = %+v, want the filter handed to the node", got)
	}

	nodes := &NodeService{}
	if err := nodes.SetTrafficLimit(1, &NodeTrafficLimitRequest{
		TrafficLimitBytes: 1000, TrafficResetDay: 1, TrafficLimitAction: NodeTrafficActionDisable, TrafficMultiplier: 1,
	}); err != nil {
		t.Fatal(err)
	}
	loc := nodes.trafficLocation()
	may := time.Date(2027, time.May, 20, 12, 0, 0, 0, loc)
	nodes.EvaluateTrafficLimits(may)
	setNodeColumns(t, db, 1, map[string]any{"traffic_used": 1000, "config_dirty": false})
	nodes.EvaluateTrafficLimits(may.Add(time.Minute))
	if nodeInboundEnabled(t, db, "n1-a") {
		t.Fatal("the traffic limit did not turn the node's inbound off")
	}

	// The limit does not make the filter push again, and a filter round leaves the
	// limit's bookkeeping on the node row untouched.
	egress.SyncNodes(true)
	if got := pushes(); len(got) != 0 {
		t.Fatalf("a synced filter was pushed again after the limit: %+v", got)
	}
	n := readNodeRow(t, db, 1)
	if !n.TrafficLimited || n.TrafficUsed != 1000 || len(n.TrafficDisabledInbounds) != 1 {
		t.Fatalf("after a filter sync limited=%v used=%d disabled=%v", n.TrafficLimited, n.TrafficUsed, n.TrafficDisabledInbounds)
	}

	// A limited node is still enabled and online, so a changed filter still reaches it.
	if _, err := egress.SetLocal(EgressFilterSettings{Filter: egressfilter.Filter{SMTP: true, Private: true}, NodeScope: EgressNodeScopeAll}); err != nil {
		t.Fatal(err)
	}
	if got := pushes(); len(got) != 1 || got[0].node != 1 || got[0].req.Filter == nil || !got[0].req.Filter.Private {
		t.Fatalf("pushes = %+v, want the changed filter handed to the limited node", got)
	}
	if !readNodeRow(t, db, 1).TrafficLimited || nodeInboundEnabled(t, db, "n1-a") {
		t.Fatal("handing out the filter lifted the traffic limit")
	}

	// The period reset brings the inbound back without touching the filter's state.
	nodes.EvaluateTrafficLimits(time.Date(2027, time.June, 1, 0, 0, 5, 0, loc))
	if !nodeInboundEnabled(t, db, "n1-a") {
		t.Fatal("the reset did not restore the node's inbound")
	}
	egress.SyncNodes(true)
	if got := pushes(); len(got) != 0 {
		t.Fatalf("the reset made the filter push again: %+v", got)
	}
	for _, s := range egress.Status().Nodes {
		if s.Id == 1 && (!s.Synced || s.Error != "") {
			t.Fatalf("node filter state after the reset = %+v", s)
		}
	}
}

// A node added by one-command enrollment is a node like any other: it starts with
// the traffic-limit defaults (no limit, x1, warn at 80%, notify) and the egress
// filter handed to all nodes reaches it once it is online.
func TestEnrolledNodeGetsTrafficDefaultsAndEgressFilter(t *testing.T) {
	f := newEnrollFixture(t)
	useFakeEgress(t)
	pushes := recordPushes(t, nil)
	egress := &EgressFilterService{}
	if _, err := egress.SetLocal(EgressFilterSettings{Filter: egressfilter.Filter{SMTP: true}, NodeScope: EgressNodeScopeAll}); err != nil {
		t.Fatal(err)
	}
	pushes()

	_, inv := f.invite(t, NodeInviteInput{})
	resp, err := f.svc.Enroll(context.Background(), inv.Token, goodRequest(), "203.0.113.7")
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	n := readNodeRow(t, database.GetDB(), resp.NodeId)
	if n.TrafficLimitBytes != 0 || n.TrafficResetDay != 1 || n.TrafficWarnPercent != 80 ||
		n.TrafficLimitAction != NodeTrafficActionNotify || n.TrafficMultiplier != 1 ||
		n.TrafficUsed != 0 || n.TrafficLimited || len(n.TrafficDisabledInbounds) != 0 {
		t.Fatalf("enrolled node traffic fields = limit %d day %d warn %d action %q x%v used %d limited %v disabled %v",
			n.TrafficLimitBytes, n.TrafficResetDay, n.TrafficWarnPercent, n.TrafficLimitAction,
			n.TrafficMultiplier, n.TrafficUsed, n.TrafficLimited, n.TrafficDisabledInbounds)
	}
	view, err := (&NodeService{}).GetById(resp.NodeId)
	if err != nil {
		t.Fatal(err)
	}
	if v := toNodeView(view); v.TrafficMultiplier != 1 || v.TrafficLimitAction != NodeTrafficActionNotify || v.TrafficLimited {
		t.Fatalf("enrolled node view = %+v", v)
	}
	// No limit set: evaluating the quota leaves the new node alone.
	(&NodeService{}).EvaluateTrafficLimits(f.now)
	if n := readNodeRow(t, database.GetDB(), resp.NodeId); n.TrafficLimited || !n.Enable {
		t.Fatalf("quota check touched an unlimited enrolled node: %+v", n)
	}

	setNodeColumns(t, database.GetDB(), resp.NodeId, map[string]any{"status": "online"})
	egress.SyncNodes(false)
	if got := pushes(); len(got) != 1 || got[0].node != resp.NodeId || got[0].req.Filter == nil || !got[0].req.Filter.SMTP {
		t.Fatalf("pushes = %+v, want the filter handed to the enrolled node", got)
	}
}
