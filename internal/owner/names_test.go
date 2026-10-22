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
