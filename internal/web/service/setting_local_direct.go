package service

import (
	"github.com/mhsanaei/3x-ui/v3/internal/sub/localdirect"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
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
