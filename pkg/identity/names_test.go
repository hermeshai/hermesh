package identity

import "testing"

// TestNormalizeName: the ways a bot may write its own name are one name.
func TestNormalizeName(t *testing.T) {
	for in, want := range map[string]string{
		"Tech Demos": "tech-demos", "tech_demos": "tech-demos", " tech--demos! ": "tech-demos",
		"周报 助手": "周报-助手", "Ｔｅｃｈ": "tech", "0199a3b2-7c1d-4e5f-8a9b-0c1d2e3f4a5b": "0199a3b2-7c1d-4e5f-8a9b-0c1d2e3f4a5b",
	} {
		if got, err := NormalizeName(in); err != nil || got != want {
			t.Errorf("NormalizeName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "  ", "--", "a1234567890123456789012345678901234567890"} {
		if got, err := NormalizeName(bad); err == nil {
			t.Errorf("NormalizeName(%q) = %q, want an error", bad, got)
		}
	}
}
