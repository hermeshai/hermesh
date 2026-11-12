package owner

import (
	"strings"
	"testing"
)

func TestFreeName(t *testing.T) {
	taken := map[string]bool{"macbook-pro": true, "macbook-pro-2": true}
	if got := freeName("macbook-pro", taken); got != "macbook-pro-3" {
		t.Fatalf("freeName = %q", got)
	}
	long := strings.Repeat("a", 63)
	if got := freeName(long, map[string]bool{long: true}); len(got) > 63 || !strings.HasSuffix(got, "-2") {
		t.Fatalf("freeName of a 63-byte name = %q", got)
	}
}

// A rename keeps what it can of the name typed, and refuses one with nothing
// left (all Chinese, full-width letters) instead of saving "machine".
func TestRenameTo(t *testing.T) {
	for in, want := range map[string]string{"My PC": "my-pc", "办公室Win": "win", "工作站 2": "2", "studio-mac": "studio-mac", "machine": "machine"} {
		if got, err := renameTo(in); err != nil || got != want {
			t.Errorf("renameTo(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"我的电脑", "ｗｏｒｋ", " - ", ""} {
		if got, err := renameTo(in); err == nil {
			t.Errorf("renameTo(%q) = %q, want an error", in, got)
		}
	}
}
