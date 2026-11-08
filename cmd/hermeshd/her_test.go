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

	// In the Mac app her is in Contents/Helpers, and its console in
	// Contents/Resources; console/ beside her still comes first.
	app := filepath.Join(t.TempDir(), "Hermesh.app", "Contents")
	os.MkdirAll(filepath.Join(app, "Resources", "console"), 0o755)
	bundled := filepath.Join(app, "Helpers", "her")
	if got := herConsole(bundled); got != filepath.Join(app, "Resources", "console") {
		t.Fatalf("herConsole in the app = %q", got)
	}
	os.MkdirAll(filepath.Join(app, "Helpers", "console"), 0o755)
	if got := herConsole(bundled); got != filepath.Join(app, "Helpers", "console") {
		t.Fatalf("herConsole with console/ beside her = %q", got)
	}
}
