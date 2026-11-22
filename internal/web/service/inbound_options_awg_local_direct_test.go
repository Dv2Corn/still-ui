package service

import (
	"os"
	"path/filepath"
	"testing"

	xraygeodata "github.com/xtls/xray-core/common/geodata"
	"google.golang.org/protobuf/proto"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// The clients page renders AmneziaWG downloads/QRs in the browser, which cannot read geoip.dat:
// the options list must hand it the same AllowedIPs the vpn:// link carries, and nothing when off.
func TestGetInboundOptions_AmneziaWGLocalDirectAllowedIPs(t *testing.T) {
	setupConflictDB(t)
	assets := t.TempDir()
	t.Setenv("XRAY_LOCATION_ASSET", assets)
	data, err := proto.Marshal(&xraygeodata.GeoIPList{Entry: []*xraygeodata.GeoIP{
		{Code: "RU", Cidr: []*xraygeodata.CIDR{{Ip: []byte{0, 0, 0, 0}, Prefix: 1}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "geoip.dat"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{
		UserId: 1, Tag: "in-51820-awg", Enable: true, Port: 51820, Protocol: model.AmneziaWG,
		Settings: `{"server":{"publicKey":"serverPub","primaryDns":"8.8.8.8"},"clients":[]}`,
	}
	if err := database.GetDB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}

	tests := []struct{ name, awg, want string }{
		{"opt-in off", "false", ""},
		{"opt-in on", "true", "8.8.8.8/32, 91.108.0.0/16, 95.161.64.0/20, 128.0.0.0/3, 160.0.0.0/5, " +
			"168.0.0.0/8, 169.0.0.0/9, 169.128.0.0/10, 169.192.0.0/11, 169.224.0.0/12, 169.240.0.0/13, " +
			"169.248.0.0/14, 169.252.0.0/15, 169.255.0.0/16, 170.0.0.0/7, 172.0.0.0/12, 172.32.0.0/11, " +
			"172.64.0.0/10, 172.128.0.0/9, 173.0.0.0/8, 174.0.0.0/7, 176.0.0.0/4, 192.0.0.0/9, 192.128.0.0/11, " +
			"192.160.0.0/13, 192.169.0.0/16, 192.170.0.0/15, 192.172.0.0/14, 192.176.0.0/12, 192.192.0.0/10, " +
			"193.0.0.0/8, 194.0.0.0/7, 196.0.0.0/6, 200.0.0.0/5, 208.0.0.0/4, 224.0.0.0/3, ::/0"},
	}
	settings := &SettingService{}
	if err := settings.saveSetting("subLocalDirect", "ru"); err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := settings.saveSetting("subLocalDirectAwg", tt.awg); err != nil {
				t.Fatal(err)
			}
			options, err := (&InboundService{}).GetInboundOptions(1)
			if err != nil || len(options) != 1 {
				t.Fatalf("GetInboundOptions = %d options, %v", len(options), err)
			}
			if got := options[0].AwgAllowedIPs; got != tt.want {
				t.Fatalf("AwgAllowedIPs =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}
