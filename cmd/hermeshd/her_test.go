package main

import (
	"os"
	"path/filepath"
	"testing"
)

// her is HERMESH_HER_BIN's or the one beside hermeshd, when it is there;
// its console is HERMESH_HER_CONSOLE's or console/ beside her.
func TestHerLookup(t *testing.T) {
	her := filepath.Join(t.TempDir(), "her")
	os.WriteFile(her, []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("HERMESH_HER_BIN", her)
	t.Setenv("HERMESH_HER_CONSOLE", "")
	if got := herBin(); got != her {
		t.Fatalf("herBin = %q", got)
	}
	if got := herConsole(her); got != filepath.Join(filepath.Dir(her), "console") {
		t.Fatalf("herConsole = %q", got)
	}
	t.Setenv("HERMESH_HER_CONSOLE", "/opt/console")
	if got := herConsole(her); got != "/opt/console" {
		t.Fatalf("herConsole = %q", got)
	}
	t.Setenv("HERMESH_HER_BIN", her+"-missing")
	if got := herBin(); got != "" {
		t.Fatalf("a missing her: %q", got)
	}
	// The test binary has no her beside it.
	t.Setenv("HERMESH_HER_BIN", "")
	if got := herBin(); got != "" {
		t.Fatalf("herBin = %q", got)
	}
	t.Setenv("HERMESH_HER_CONSOLE", "")
	if got := herConsole(""); got != "" {
		t.Fatalf("herConsole without her = %q", got)
	}
}
