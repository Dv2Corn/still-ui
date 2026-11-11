package service

import (
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/porthop"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"

	"gorm.io/gorm"
)

// PortHopRule is the redirect one inbound asks for.
type PortHopRule struct {
	InboundID int    `json:"inboundId" example:"7"`
	Remark    string `json:"remark" example:"hy2"`
	Ports     string `json:"ports" example:"20000-50000"`
	ToPort    int    `json:"toPort" example:"443"`
}

// PortHopStatus is what this host does for Hysteria2 port hopping.
type PortHopStatus struct {
	Supported    bool `json:"supported" example:"true"`
	NftInstalled bool `json:"nftInstalled" example:"true"`
	// Permitted is false when nft runs but may not change rules (no NET_ADMIN).
	Permitted bool          `json:"permitted" example:"true"`
	Active    bool          `json:"active" example:"true"`
	Packets   uint64        `json:"packets" example:"1204"`
	Rules     []PortHopRule `json:"rules"`
	Error     string        `json:"error" example:""`
}

// portHopHost is the nftables side; swapped out in tests.
type portHopHost interface {
	Available() error
	Ensure(redirects []porthop.Redirect, protected []uint16) error
	Status() porthop.Status
}

var (
	portHopHostImpl portHopHost = porthop.Default()
	portHopMu       sync.Mutex
	portHopErr      string
)

// PortHopService keeps the host redirect in step with the Hysteria2 inbounds that ask for one.
type PortHopService struct{}

// inboundPortHop returns the ranges an inbound wants redirected to its port, or nil when
// the option is off. Only a Hysteria inbound hops; the range lives in quicParams.udpHop.
func inboundPortHop(ib *model.Inbound) ([]porthop.Range, error) {
	if ib.Protocol != model.Hysteria || ib.StreamSettings == "" {
		return nil, nil
	}
	var stream struct {
		FinalMask struct {
			QuicParams struct {
				UDPHop struct {
					Ports    json.RawMessage `json:"ports"`
					Redirect bool            `json:"redirect"`
				} `json:"udpHop"`
			} `json:"quicParams"`
		} `json:"finalmask"`
	}
	if json.Unmarshal([]byte(ib.StreamSettings), &stream) != nil || !stream.FinalMask.QuicParams.UDPHop.Redirect {
		return nil, nil
	}
	var ports string
	if raw := stream.FinalMask.QuicParams.UDPHop.Ports; len(raw) > 0 && json.Unmarshal(raw, &ports) != nil {
		return nil, fmt.Errorf("port range %s is not a string", raw)
	}
	return porthop.ParseRanges(ports)
}

// validatePortHop refuses at save a redirect this panel could not install as asked.
func validatePortHop(inbound *model.Inbound) error {
	ranges, err := inboundPortHop(inbound)
	if err != nil {
		return common.NewError("Port hopping:", err.Error())
	}
	if ranges == nil {
		return nil
	}
	// The redirect rewrites the destination to the interface's primary address.
	if !isAnyListen(inbound.Listen) {
		return common.NewError("Port hopping: the server-side redirect needs the inbound to listen on all addresses; clear its listen address.")
	}
	// A node's own panel runs this check when the inbound reaches it.
	if inbound.NodeID == nil {
		if err := portHopHostImpl.Available(); err != nil {
			return common.NewError("Port hopping: the server-side redirect is not available on this server:", err.Error())
		}
	}
	return nil
}

func inboundDisplayName(ib *model.Inbound) string {
	if ib.Remark != "" {
		return fmt.Sprintf("'%s' (#%d)", ib.Remark, ib.Id)
	}
	return fmt.Sprintf("#%d", ib.Id)
}

func inRanges(ranges []porthop.Range, port int) bool {
	return slices.ContainsFunc(ranges, func(r porthop.Range) bool { return r.Contains(port) })
}

// forwardedPortsIn lists the client forwards of a userspace AmneziaWG inbound inside the
// ranges: a forward binds its UDP port itself, outside the port column.
func forwardedPortsIn(row *model.Inbound, ranges []porthop.Range) []int {
	inst, ok := amneziawg.InstanceFromInbound(row)
	if !ok || amneziawgKernelMode(row) {
		return nil
	}
	var out []int
	for _, peer := range inst.Peers {
		for _, port := range amneziawg.ExpandForwardedPorts(peer.ForwardedPorts) {
			if inRanges(ranges, port) {
				out = append(out, port)
			}
		}
	}
	return out
}

// checkPortHopConflict keeps a redirected range and the UDP ports of the same host apart:
// a UDP inbound inside another inbound's range would lose its traffic or leave dead hop ports.
func checkPortHopConflict(db *gorm.DB, inbound *model.Inbound, ignoreId int) error {
	ranges, err := inboundPortHop(inbound)
	if err != nil {
		return common.NewError("Port hopping:", err.Error())
	}
	usesUDP := inboundTransports(inbound.Protocol, inbound.StreamSettings, inbound.Settings)&transportUDP != 0
	if ranges == nil && !usesUDP {
		return nil
	}
	others := func(where string, args ...any) ([]*model.Inbound, error) {
		var rows []*model.Inbound
		q := db.Model(model.Inbound{}).Where(where, args...)
		if ignoreId > 0 {
			q = q.Where("id != ?", ignoreId)
		}
		if err := q.Find(&rows).Error; err != nil {
			return nil, err
		}
		return slices.DeleteFunc(rows, func(row *model.Inbound) bool { return !sameNode(row.NodeID, inbound.NodeID) }), nil
	}
	hopping, err := others("protocol = ?", model.Hysteria)
	if err != nil {
		return err
	}
	for _, other := range hopping {
		otherRanges, _ := inboundPortHop(other)
		for _, o := range otherRanges {
			if usesUDP && o.Contains(inbound.Port) {
				return common.NewError(fmt.Sprintf("UDP port %d is inside the port hopping range %s of inbound %s.", inbound.Port, o, inboundDisplayName(other)))
			}
			for _, r := range ranges {
				if r.Overlaps(o) {
					return common.NewError(fmt.Sprintf("Port hopping: the range %s overlaps the range %s of inbound %s.", r, o, inboundDisplayName(other)))
				}
			}
		}
	}
	if ranges != nil {
		forwarding, err := others("protocol = ?", model.AmneziaWG)
		if err != nil {
			return err
		}
		for _, other := range forwarding {
			if ports := forwardedPortsIn(other, ranges); len(ports) > 0 {
				return common.NewError(fmt.Sprintf("Port hopping: the range contains port %d, which inbound %s forwards to a client.", ports[0], inboundDisplayName(other)))
			}
		}
	}
	for _, r := range ranges {
		inside, err := others("port BETWEEN ? AND ?", int(r.From), int(r.To))
		if err != nil {
			return err
		}
		for _, other := range inside {
			if inboundTransports(other.Protocol, other.StreamSettings, other.Settings)&transportUDP != 0 {
				return common.NewError(fmt.Sprintf("Port hopping: the range %s contains UDP port %d of inbound %s.", r, other.Port, inboundDisplayName(other)))
			}
		}
	}
	return nil
}

// desired reads the redirects of this host's enabled inbounds, and the UDP ports inside their
// ranges that keep their own listener: the saves refuse those, a restored row may still hold one.
func (s *PortHopService) desired() ([]porthop.Redirect, []PortHopRule, []uint16, error) {
	db := database.GetDB()
	var hopping []*model.Inbound
	if err := db.Model(model.Inbound{}).Where("protocol = ? AND node_id IS NULL AND enable = ?", model.Hysteria, true).Find(&hopping).Error; err != nil {
		return nil, nil, nil, err
	}
	var (
		redirects []porthop.Redirect
		rules     []PortHopRule
		protected []uint16
	)
	for _, ib := range hopping {
		// A stored row that no longer validates is skipped, not half-applied.
		ranges, err := inboundPortHop(ib)
		if err != nil || ranges == nil || !isAnyListen(ib.Listen) || ib.Port <= 0 || ib.Port > 65535 {
			continue
		}
		redirects = append(redirects, porthop.Redirect{Ranges: ranges, To: uint16(ib.Port)})
		protected = appendPort(protected, ib.Port)
		for _, r := range ranges {
			rules = append(rules, PortHopRule{InboundID: ib.Id, Remark: ib.Remark, Ports: r.String(), ToPort: ib.Port})
			var inside []*model.Inbound
			if err := db.Model(model.Inbound{}).Where("node_id IS NULL AND enable = ? AND port BETWEEN ? AND ?", true, int(r.From), int(r.To)).Find(&inside).Error; err != nil {
				return nil, nil, nil, err
			}
			for _, other := range inside {
				if inboundTransports(other.Protocol, other.StreamSettings, other.Settings)&transportUDP != 0 {
					protected = appendPort(protected, other.Port)
				}
			}
		}
	}
	if len(redirects) == 0 {
		return nil, nil, nil, nil
	}
	var forwarding []*model.Inbound
	if err := db.Model(model.Inbound{}).Where("protocol = ? AND node_id IS NULL AND enable = ?", model.AmneziaWG, true).Find(&forwarding).Error; err != nil {
		return nil, nil, nil, err
	}
	for _, ib := range forwarding {
		for _, r := range redirects {
			for _, port := range forwardedPortsIn(ib, r.Ranges) {
				protected = appendPort(protected, port)
			}
		}
	}
	return redirects, rules, protected, nil
}

// EnsureHost converges the nftables table; it runs on start and on a short cadence, so a
// saved, disabled or deleted inbound and a flushed ruleset are all picked up without a hook.
func (s *PortHopService) EnsureHost() {
	// Read under the lock: a pass that read earlier must not apply after one that read later.
	portHopMu.Lock()
	defer portHopMu.Unlock()
	redirects, _, protected, err := s.desired()
	if err == nil {
		err = portHopHostImpl.Ensure(redirects, protected)
	}
	recordHostErr(&portHopErr, "port hopping", err)
}

// Status is safe to poll: it never changes the host.
func (s *PortHopService) Status() PortHopStatus {
	_, rules, _, _ := s.desired()
	portHopMu.Lock()
	defer portHopMu.Unlock()
	host := portHopHostImpl.Status()
	if rules == nil {
		rules = []PortHopRule{}
	}
	return PortHopStatus{
		Supported: host.Supported, NftInstalled: host.NftInstalled, Permitted: host.Permitted,
		Active: host.Active, Packets: host.Packets, Rules: rules, Error: portHopErr,
	}
}
