package service

import (
	"strings"
	"testing"
)

func TestUpdateAllSettingSubClashAutoGroup(t *testing.T) {
	tests := []struct {
		name          string
		groupType     string
		url           string
		interval      int
		tolerance     int
		wantErr       string
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
		{name: "unknown type", groupType: "load-balance", interval: 300, wantErr: "type must be url-test or fallback"},
		{name: "relative url", url: "/generate_204", interval: 300, wantErr: "check URL must be an absolute http or https URL"},
		{name: "non-http url", url: "ftp://example.com/x", interval: 300, wantErr: "check URL must be an absolute http or https URL"},
		{name: "interval too short", interval: 5, wantErr: "check interval must be"},
		{name: "interval too long", interval: 86401, wantErr: "check interval must be"},
		{name: "negative tolerance", interval: 300, tolerance: -1, wantErr: "tolerance must be 0 to"},
		{name: "tolerance too large", interval: 300, tolerance: 10001, wantErr: "tolerance must be 0 to"},
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
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("UpdateAllSetting = %v, want the error %q", err, tt.wantErr)
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
