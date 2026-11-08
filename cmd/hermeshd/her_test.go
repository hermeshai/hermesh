package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// The owner role's run, which stops this computer's machine and her with
// it, has returned before hermeshd goes on to exit: once it is asked to
// stop, and when serving fails at once.
func TestServeUntilStopped(t *testing.T) {
	for _, failing := range []bool{false, true} {
		ctx, stop := context.WithCancel(context.Background())
		var stopped atomic.Bool
		run := func(ctx context.Context) {
			<-ctx.Done()
			time.Sleep(50 * time.Millisecond) // stopping the machine and her
			stopped.Store(true)
		}
		serve := func(ctx context.Context) error {
			if failing {
				return errors.New("the socket is taken")
			}
			<-ctx.Done()
			return nil
		}
		if !failing {
			time.AfterFunc(10*time.Millisecond, stop) // SIGTERM
		}
		if err := serveUntilStopped(ctx, stop, run, serve); (err != nil) != failing || !stopped.Load() {
			t.Fatalf("failing %v: returned %v with the run stopped: %v", failing, err, stopped.Load())
		}
	}
}

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

// The browsers start the her the Mac app ships for the extension, never one
// HERMESH_HER_BIN names, as tests do.
func TestHerExtensionHost(t *testing.T) {
	t.Setenv("HERMESH_HER_BIN", "")
	if got := herExtensionHost(); got != (runtime.GOOS == "darwin") {
		t.Fatalf("the shipped her on %s: %v", runtime.GOOS, got)
	}
	t.Setenv("HERMESH_HER_BIN", "/dev/her")
	if herExtensionHost() {
		t.Fatal("a her of HERMESH_HER_BIN is registered with the browsers")
	}
}
