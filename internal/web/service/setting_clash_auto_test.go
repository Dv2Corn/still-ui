package service

import (
	"testing"
)

func TestSubClashAutoGroupFactoryDefaults(t *testing.T) {
	setupSettingTestDB(t)
	s := &SettingService{}
	defaults := s.GetFactoryDefaults()
	want := map[string]string{
		"subClashAutoGroup":          "true",
		"subClashAutoGroupType":      "url-test",
		"subClashAutoGroupUrl":       "http://www.gstatic.com/generate_204",
		"subClashAutoGroupInterval":  "300",
		"subClashAutoGroupTolerance": "50",
	}
	for key, value := range want {
		if got := defaults[key]; got != value {
			t.Errorf("factory %s = %q, want %q", key, got, value)
		}
	}
	all, err := s.GetAllSetting()
	if err != nil {
		t.Fatal(err)
	}
	if !all.SubClashAutoGroup || all.SubClashAutoGroupType != SubClashAutoGroupURLTest ||
		all.SubClashAutoGroupUrl != DefaultSubClashAutoGroupURL ||
		all.SubClashAutoGroupInterval != DefaultSubClashAutoGroupInterval ||
		all.SubClashAutoGroupTolerance != DefaultSubClashAutoGroupTolerance {
		t.Fatalf("GetAllSetting Auto group = %v %q %q %d %d, want the factory defaults",
			all.SubClashAutoGroup, all.SubClashAutoGroupType, all.SubClashAutoGroupUrl,
			all.SubClashAutoGroupInterval, all.SubClashAutoGroupTolerance)
	}
}

func TestUpdateAllSettingSubClashAutoGroup(t *testing.T) {
	tests := []struct {
		name          string
		groupType     string
		url           string
		interval      int
		tolerance     int
		wantErr       bool
		wantType      string
		wantURL       string
		wantInterval  int
		wantTolerance int
	}{
		{
			name: "fallback with custom check", groupType: "fallback", url: " https://cp.cloudflare.com/generate_204 ", interval: 120, tolerance: 0,
			wantType: "fallback", wantURL: "https://cp.cloudflare.com/generate_204", wantInterval: 120, wantTolerance: 0,
		},
		{name: "unset fields take defaults", wantType: "url-test", wantURL: DefaultSubClashAutoGroupURL, wantInterval: 300, wantTolerance: 0},
		{name: "unknown type", groupType: "load-balance", interval: 300, wantErr: true},
		{name: "relative url", url: "/generate_204", interval: 300, wantErr: true},
		{name: "non-http url", url: "ftp://example.com/x", interval: 300, wantErr: true},
		{name: "interval too short", interval: 5, wantErr: true},
		{name: "interval too long", interval: 86401, wantErr: true},
		{name: "negative tolerance", interval: 300, tolerance: -1, wantErr: true},
		{name: "tolerance too large", interval: 300, tolerance: 10001, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupSettingTestDB(t)
			s := &SettingService{}
			settings, err := s.GetAllSetting()
			if err != nil {
				t.Fatal(err)
			}
			settings.SubClashAutoGroup = false
			settings.SubClashAutoGroupType = tt.groupType
			settings.SubClashAutoGroupUrl = tt.url
			settings.SubClashAutoGroupInterval = tt.interval
			settings.SubClashAutoGroupTolerance = tt.tolerance
			err = s.UpdateAllSetting(settings, SecretClears{})
			if tt.wantErr {
				if err == nil {
					t.Fatal("UpdateAllSetting accepted an invalid Auto group setting")
				}
				return
			}
			if err != nil {
				t.Fatalf("UpdateAllSetting: %v", err)
			}
			if on, err := s.GetSubClashAutoGroup(); err != nil || on {
				t.Fatalf("GetSubClashAutoGroup = %v, %v; want false saved", on, err)
			}
			if got, _ := s.GetSubClashAutoGroupType(); got != tt.wantType {
				t.Errorf("type = %q, want %q", got, tt.wantType)
			}
			if got, _ := s.GetSubClashAutoGroupUrl(); got != tt.wantURL {
				t.Errorf("url = %q, want %q", got, tt.wantURL)
			}
			if got, _ := s.GetSubClashAutoGroupInterval(); got != tt.wantInterval {
				t.Errorf("interval = %d, want %d", got, tt.wantInterval)
			}
			if got, _ := s.GetSubClashAutoGroupTolerance(); got != tt.wantTolerance {
				t.Errorf("tolerance = %d, want %d", got, tt.wantTolerance)
			}
		})
	}
}
