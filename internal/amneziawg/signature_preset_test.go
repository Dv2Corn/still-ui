package amneziawg

import (
	"strings"
	"testing"
)

func TestSignaturePacketPresetsPassTheServerValidator(t *testing.T) {
	for _, preset := range []string{SignaturePresetQUIC, SignaturePresetDNS} {
		t.Run(preset, func(t *testing.T) {
			for range 200 {
				o := GenerateObfuscation31()
				spec, err := SignaturePacket(preset)
				if err != nil {
					t.Fatal(err)
				}
				o.I1 = spec
				if err := ValidateServerObfuscation(o); err != nil {
					t.Fatalf("I1 %q refused: %v", spec, err)
				}
				// The UAPI and the client config are line based; a space inside a value would cut it.
				if strings.ContainsAny(spec, "\r\n") || strings.Contains(spec, "  ") {
					t.Fatalf("I1 %q is not a single clean config value", spec)
				}
			}
		})
	}
}

func TestSignaturePacketRefusesAnUnknownPreset(t *testing.T) {
	if _, err := SignaturePacket("http"); err == nil || err.Error() != `unknown signature packet preset "http"` {
		t.Fatalf("err = %v, want the unknown-preset error", err)
	}
}
