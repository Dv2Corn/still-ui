package service

import (
	"net/url"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
)

// The Clash/Mihomo subscription's "Auto" group: clients health-check every
// proxy through the tunnel and move to a working one on their own.
const (
	SubClashAutoGroupURLTest          = "url-test"
	SubClashAutoGroupFallback         = "fallback"
	DefaultSubClashAutoGroupType      = SubClashAutoGroupURLTest
	DefaultSubClashAutoGroupURL       = "http://www.gstatic.com/generate_204"
	DefaultSubClashAutoGroupInterval  = 300
	DefaultSubClashAutoGroupTolerance = 50
	minSubClashAutoGroupInterval      = 10
	maxSubClashAutoGroupInterval      = 86400
	maxSubClashAutoGroupTolerance     = 10000
)

func (s *SettingService) GetSubClashAutoGroup() (bool, error) {
	return s.getBool("subClashAutoGroup")
}

func (s *SettingService) GetSubClashAutoGroupType() (string, error) {
	return s.getString("subClashAutoGroupType")
}

func (s *SettingService) GetSubClashAutoGroupUrl() (string, error) {
	return s.getString("subClashAutoGroupUrl")
}

func (s *SettingService) GetSubClashAutoGroupInterval() (int, error) {
	return s.getInt("subClashAutoGroupInterval")
}

func (s *SettingService) GetSubClashAutoGroupTolerance() (int, error) {
	return s.getInt("subClashAutoGroupTolerance")
}

// ValidSubClashAutoGroupURL accepts only an absolute http(s) URL with a host:
// Clash clients fetch it through every proxy to measure it.
func ValidSubClashAutoGroupURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Host == "" {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// validateSubClashAutoGroup fills unset values with the defaults (older API
// clients do not send the fields) and rejects values Clash clients would refuse.
func validateSubClashAutoGroup(allSetting *entity.AllSetting) error {
	allSetting.SubClashAutoGroupType = strings.TrimSpace(allSetting.SubClashAutoGroupType)
	switch allSetting.SubClashAutoGroupType {
	case "":
		allSetting.SubClashAutoGroupType = DefaultSubClashAutoGroupType
	case SubClashAutoGroupURLTest, SubClashAutoGroupFallback:
	default:
		return common.NewError("Clash/Mihomo Auto group type must be url-test or fallback")
	}
	allSetting.SubClashAutoGroupUrl = strings.TrimSpace(allSetting.SubClashAutoGroupUrl)
	if allSetting.SubClashAutoGroupUrl == "" {
		allSetting.SubClashAutoGroupUrl = DefaultSubClashAutoGroupURL
	} else if !ValidSubClashAutoGroupURL(allSetting.SubClashAutoGroupUrl) {
		return common.NewError("Clash/Mihomo Auto group check URL must be an absolute http or https URL")
	}
	if allSetting.SubClashAutoGroupInterval == 0 {
		allSetting.SubClashAutoGroupInterval = DefaultSubClashAutoGroupInterval
	}
	if allSetting.SubClashAutoGroupInterval < minSubClashAutoGroupInterval || allSetting.SubClashAutoGroupInterval > maxSubClashAutoGroupInterval {
		return common.NewErrorf("Clash/Mihomo Auto group check interval must be %d to %d seconds", minSubClashAutoGroupInterval, maxSubClashAutoGroupInterval)
	}
	if allSetting.SubClashAutoGroupTolerance < 0 || allSetting.SubClashAutoGroupTolerance > maxSubClashAutoGroupTolerance {
		return common.NewErrorf("Clash/Mihomo Auto group tolerance must be 0 to %d ms", maxSubClashAutoGroupTolerance)
	}
	return nil
}
