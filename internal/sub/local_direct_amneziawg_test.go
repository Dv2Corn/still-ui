package sub

import (
	"encoding/base64"
	"strings"
	"testing"

	xraygeodata "github.com/xtls/xray-core/common/geodata"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/sub/localdirect"
)

// From the Python reference: RU = 0.0.0.0/1 minus private ranges, Telegram put back, DNS 8.8.8.8 added.
const awgLocalDirectAllowedIPs = "8.8.8.8/32, 91.108.0.0/16, 95.161.64.0/20, 128.0.0.0/3, 160.0.0.0/5, " +
	"168.0.0.0/8, 169.0.0.0/9, 169.128.0.0/10, 169.192.0.0/11, 169.224.0.0/12, 169.240.0.0/13, " +
	"169.248.0.0/14, 169.252.0.0/15, 169.255.0.0/16, 170.0.0.0/7, 172.0.0.0/12, 172.32.0.0/11, " +
	"172.64.0.0/10, 172.128.0.0/9, 173.0.0.0/8, 174.0.0.0/7, 176.0.0.0/4, 192.0.0.0/9, 192.128.0.0/11, " +
	"192.160.0.0/13, 192.169.0.0/16, 192.170.0.0/15, 192.172.0.0/14, 192.176.0.0/12, 192.192.0.0/10, " +
	"193.0.0.0/8, 194.0.0.0/7, 196.0.0.0/6, 200.0.0.0/5, 208.0.0.0/4, 224.0.0.0/3, ::/0"

func saveSubSetting(t *testing.T, key, value string) {
	t.Helper()
	db := database.GetDB()
	if err := db.Where("key = ?", key).Delete(&model.Setting{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Setting{Key: key, Value: value}).Error; err != nil {
		t.Fatal(err)
	}
}

func writeGeoIP(t *testing.T, dir, code string) {
	t.Helper()
	writeGeoFile(t, dir, localdirect.GeoIPFile, &xraygeodata.GeoIPList{Entry: []*xraygeodata.GeoIP{
		{Code: code, Cidr: []*xraygeodata.CIDR{{Ip: []byte{0, 0, 0, 0}, Prefix: 1}}},
	}})
}

func awgPeerAllowedIPs(t *testing.T, link string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(link, "vpn://"))
	if err != nil {
		t.Fatalf("decode %q: %v", link, err)
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if value, ok := strings.CutPrefix(line, "AllowedIPs = "); ok {
			return value
		}
	}
	t.Fatalf("no AllowedIPs line in:\n%s", raw)
	return ""
}

// The vpn:// config leaves the full tunnel only when the country and the AmneziaWG opt-in are both
// set and geoip.dat has the country; anything else must keep producing a working full tunnel.
func TestAmneziaWGLinkLocalDirect(t *testing.T) {
	seedSubDB(t)
	assets := t.TempDir()
	t.Setenv("XRAY_LOCATION_ASSET", assets)
	inbound := &model.Inbound{
		Listen: "203.0.113.7", Port: 51820, Protocol: model.AmneziaWG, Remark: "awg",
		Settings: `{"server":{"publicKey":"serverPub","primaryDns":"8.8.8.8"},` +
			`"clients":[{"email":"user","privateKey":"clientPriv","allowedIPs":["10.8.1.2/32"]}]}`,
	}
	tests := []struct {
		name, country, awg, geoipCode, want string
	}{
		{"both off", "", "false", "RU", localdirect.FullTunnelAllowedIPs},
		{"country only", "ru", "false", "RU", localdirect.FullTunnelAllowedIPs},
		{"opt-in without a country", "", "true", "RU", localdirect.FullTunnelAllowedIPs},
		{"both on", "ru", "true", "RU", awgLocalDirectAllowedIPs},
		{"geoip lacks the country", "ru", "true", "OTHER", localdirect.FullTunnelAllowedIPs},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeGeoIP(t, assets, tt.geoipCode)
			saveSubSetting(t, "subLocalDirect", tt.country)
			saveSubSetting(t, "subLocalDirectAwg", tt.awg)
			s := &SubService{}
			s.PrepareForRequest("sub.example.com")
			if got := awgPeerAllowedIPs(t, s.genAmneziaWGLink(inbound, "user")); got != tt.want {
				t.Fatalf("AllowedIPs =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}
