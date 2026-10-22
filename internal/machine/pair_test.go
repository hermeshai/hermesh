package machine

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHostLabel(t *testing.T) {
	for _, c := range []struct{ prefix, host, want string }{
		{"", "HomeBook.local", "HomeBook"},
		{"agent@", "vps-1", "agent@vps-1"},
		// GitHub's macOS runners: longer than the 64 bytes a relay accepts.
		{"", "sat12-bu160-a6c7fe88-0b0e-4c40-a6a9-26c42f3ac91d-0d1b7e2d9f3a4c5b8e6f-runner", "sat12-bu160-a6c7fe88-0b0e-4c40-a6a9-26c42f3ac91d-0d1b7e2d9f3a4c5"},
	} {
		if got := hostLabel(c.prefix, c.host); got != c.want {
			t.Errorf("hostLabel(%q, %q) = %q, want %q", c.prefix, c.host, got, c.want)
		}
	}
	long := hostLabel("agent@", strings.Repeat("机", 40))
	if len(long) > 64 || !utf8.ValidString(long) {
		t.Fatalf("not cut on a rune boundary within 64 bytes: %q (%d)", long, len(long))
	}
}

func TestPairingLink(t *testing.T) {
	for console, want := range map[string]string{
		"https://hermesh.io/overview": "https://hermesh.io/p/ABCD-EFGH-JKMN",
		"http://localhost:13000/":     "http://localhost:13000/p/ABCD-EFGH-JKMN",
		"":                            "",
		"javascript:alert(1)":         "",
		"https:///overview":           "",
		"ftp://hermesh.io/overview":   "",
	} {
		if got := PairingLink(console, "ABCD-EFGH-JKMN"); got != want {
			t.Errorf("PairingLink(%q) = %q, want %q", console, got, want)
		}
	}
}
