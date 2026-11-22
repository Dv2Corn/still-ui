package service

import (
	"net/netip"
	"os"
	"path/filepath"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/sub/localdirect"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
	"github.com/mhsanaei/3x-ui/v3/internal/xray/geodata"
)

func (s *SettingService) GetSubLocalDirect() (string, error) {
	return s.getString("subLocalDirect")
}

func (s *SettingService) GetSubLocalDirectProxy() (string, error) {
	return s.getString("subLocalDirectProxy")
}

// The same parsers the sub server uses, so a value can never be saved as valid
// and then switch the feature off at request time.
func validateSubLocalDirect(allSetting *entity.AllSetting) error {
	country, err := localdirect.NormalizeCountry(allSetting.SubLocalDirect)
	if err != nil {
		return common.NewError("Local sites direct:", err.Error())
	}
	proxyList, err := localdirect.NormalizeProxyList(allSetting.SubLocalDirectProxy)
	if err != nil {
		return common.NewError("Local sites direct:", err.Error())
	}
	allSetting.SubLocalDirect, allSetting.SubLocalDirectProxy = country, proxyList
	return nil
}

// HasCategory reports whether a geo database in the Xray asset folder carries a category.
func (s *GeodataService) HasCategory(file, code string) bool {
	_, err := assetStore().Lookup(file, code)
	return err == nil
}

type amneziaWGTunnelKey struct {
	geoIP, country, proxyList string
	size, modTime             int64
}

var amneziaWGTunnelCache struct {
	sync.Mutex
	key    amneziaWGTunnelKey
	tunnel []netip.Prefix
}

// AmneziaWGLocalDirectTunnel returns the IPv4 prefixes AmneziaWG client configs tunnel under
// "local sites direct", or nil for the plain full tunnel: feature off, or geoip.dat unusable.
func (s *SettingService) AmneziaWGLocalDirectTunnel() []netip.Prefix {
	if on, err := s.getBool("subLocalDirectAwg"); err != nil || !on {
		return nil
	}
	country, err := s.GetSubLocalDirect()
	if err != nil {
		return nil
	}
	proxyList, err := s.GetSubLocalDirectProxy()
	if err != nil {
		return nil
	}
	plan, err := localdirect.NewPlan(country, proxyList, func(string, string) bool { return false })
	if err != nil || !plan.Enabled() {
		return nil
	}
	key := amneziaWGTunnelKey{geoIP: filepath.Join(assetDir(), localdirect.GeoIPFile), country: plan.Country, proxyList: proxyList}
	if info, err := os.Stat(key.geoIP); err == nil {
		key.size, key.modTime = info.Size(), info.ModTime().UnixNano()
	}
	amneziaWGTunnelCache.Lock()
	defer amneziaWGTunnelCache.Unlock()
	if amneziaWGTunnelCache.key == key {
		return amneziaWGTunnelCache.tunnel
	}
	var tunnel []netip.Prefix
	if cidrs, err := geoIPCategoryCIDRs(plan.Country); err != nil {
		logger.Warning("subLocalDirectAwg: AmneziaWG configs stay a full tunnel; geoip.dat category", plan.Country, "is unusable:", err)
	} else {
		tunnel = localdirect.AmneziaWGTunnel(cidrs, plan.ProxyIPs)
	}
	amneziaWGTunnelCache.key, amneziaWGTunnelCache.tunnel = key, tunnel
	return tunnel
}

func geoIPCategoryCIDRs(code string) ([]string, error) {
	var cidrs []string
	for {
		page, err := assetStore().Entries(localdirect.GeoIPFile, code, "", len(cidrs), geodata.MaxPageSize)
		if err != nil {
			return nil, err
		}
		for _, entry := range page.Items {
			cidrs = append(cidrs, entry.Value)
		}
		if len(page.Items) == 0 || len(cidrs) >= page.Total {
			return cidrs, nil
		}
	}
}
