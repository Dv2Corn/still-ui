package amneziawg

import "testing"

func TestValidatePeerKey(t *testing.T) {
	for _, ok := range []string{"", "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI="} {
		if err := ValidatePeerKey("publicKey", ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"not-a-key",
		"QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQg==",       // 31 bytes
		"QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJC",       // 33 bytes
		"QkJCQkJCQkJCQkJCQkJCQkJC\nQkJCQkJCQkJCQkJCQkJCQkI=", // wrapped
	} {
		if err := ValidatePeerKey("publicKey", bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
