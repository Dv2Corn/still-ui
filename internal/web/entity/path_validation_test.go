package entity

import "testing"

func TestPathHasForbiddenChar(t *testing.T) {
	valid := []string{
		"",
		"/",
		"/sub/",
		"/json/",
		"/a/b/c/",
		"/My-Path_123/",
	}
	for _, p := range valid {
		if pathHasForbiddenChar(p) {
			t.Errorf("pathHasForbiddenChar(%q) = true, want false", p)
		}
	}

	invalid := []string{
		"/sub path/",
		"/back\\slash/",
		"/tab\there/",
		"/new\nline/",
		"/\x7f/",
	}
	for _, p := range invalid {
		if !pathHasForbiddenChar(p) {
			t.Errorf("pathHasForbiddenChar(%q) = false, want true", p)
		}
	}
}

// A quote or angle bracket in a URI path has no use and breaks the HTML the
// panel injects it into, so saving one is refused.
func TestCheckValidRefusesMarkupCharactersInPaths(t *testing.T) {
	for _, bad := range []string{`/a"b/`, `/a'b/`, `/</script>/`, `/a>b/`} {
		s := &AllSetting{WebPort: 2053, SubPort: 2096, WebBasePath: bad}
		if err := s.CheckValid(); err == nil {
			t.Errorf("web base path %q: want error, got nil", bad)
		}
		s = &AllSetting{WebPort: 2053, SubPort: 2096, SubPath: bad}
		if err := s.CheckValid(); err == nil {
			t.Errorf("subscription path %q: want error, got nil", bad)
		}
	}
}
