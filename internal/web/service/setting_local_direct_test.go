package service

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/sub/localdirect"
)

func TestUpdateAllSettingSubLocalDirect(t *testing.T) {
	tests := []struct {
		name        string
		country     string
		proxy       string
		wantErr     string
		wantCountry string
		wantProxy   string
	}{
		{name: "country is stored lowercase", country: " RU ", proxy: "telegram.org\r\n91.108.0.0/16", wantCountry: "ru", wantProxy: "telegram.org\n91.108.0.0/16"},
		{name: "off spelled out", country: "off", proxy: "t.me", wantCountry: "", wantProxy: "t.me"},
		{name: "blank list takes the built-in one", country: "ir", proxy: "  ", wantCountry: "ir", wantProxy: localdirect.DefaultProxyList},
		{name: "unknown country code", country: "xx", wantErr: `unknown country code "xx"`},
		{name: "three-letter code", country: "rus", wantErr: `unknown country code "rus"`},
		{name: "bad list line", country: "ru", proxy: "telegram.org\nDOMAIN-SUFFIX,t.me,PROXY", wantErr: "always-VPN list line 2"},
		{name: "bad list line while off", country: "", proxy: "not a domain", wantErr: "always-VPN list line 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupSettingTestDB(t)
			s := &SettingService{}
			settings, err := s.GetAllSetting()
			if err != nil {
				t.Fatal(err)
			}
			settings.SubLocalDirect = tt.country
			settings.SubLocalDirectProxy = tt.proxy
			err = s.UpdateAllSetting(settings, SecretClears{})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("UpdateAllSetting = %v, want the error %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("UpdateAllSetting: %v", err)
			}
			if got, _ := s.GetSubLocalDirect(); got != tt.wantCountry {
				t.Errorf("country = %q, want %q", got, tt.wantCountry)
			}
			if got, _ := s.GetSubLocalDirectProxy(); got != tt.wantProxy {
				t.Errorf("always-VPN list = %q, want %q", got, tt.wantProxy)
			}
		})
	}
}

// A panel upgraded from a release without the feature has neither row: it must
// read as off, and the settings page must show the built-in list to edit.
func TestSubLocalDirectDefaultsWithoutStoredRows(t *testing.T) {
	setupSettingTestDB(t)
	s := &SettingService{}
	settings, err := s.GetAllSetting()
	if err != nil {
		t.Fatal(err)
	}
	if settings.SubLocalDirect != "" || settings.SubLocalDirectProxy != localdirect.DefaultProxyList {
		t.Fatalf("defaults = %q, %q; want off and the built-in list", settings.SubLocalDirect, settings.SubLocalDirectProxy)
	}
}
