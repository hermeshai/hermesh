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

	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
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
// its console is HERMESH_HER_CONSOLE's or her-console/ beside her.
func TestHerLookup(t *testing.T) {
	her := filepath.Join(t.TempDir(), "her")
	os.WriteFile(her, []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("HERMESH_HER_BIN", her)
	t.Setenv("HERMESH_HER_CONSOLE", "")
	if got := herBin(); got != her {
		t.Fatalf("herBin = %q", got)
	}
	if got := herConsole(her); got != filepath.Join(filepath.Dir(her), "her-console") {
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
	// Contents/Resources; her-console/ beside her still comes first.
	app := filepath.Join(t.TempDir(), "Hermesh.app", "Contents")
	os.MkdirAll(filepath.Join(app, "Resources", "console"), 0o755)
	bundled := filepath.Join(app, "Helpers", "her")
	if got := herConsole(bundled); got != filepath.Join(app, "Resources", "console") {
		t.Fatalf("herConsole in the app = %q", got)
	}
	os.MkdirAll(filepath.Join(app, "Helpers", "her-console"), 0o755)
	if got := herConsole(bundled); got != filepath.Join(app, "Helpers", "her-console") {
		t.Fatalf("herConsole with her-console/ beside her = %q", got)
	}
}

// The browsers start the her Hermesh ships for the extension, on a Mac and
// on Linux, never one HERMESH_HER_BIN names, as tests do.
func TestHerExtensionHost(t *testing.T) {
	t.Setenv("HERMESH_HER_BIN", "")
	if got := herExtensionHost(); got != (runtime.GOOS != "windows") {
		t.Fatalf("the shipped her on %s: %v", runtime.GOOS, got)
	}
	t.Setenv("HERMESH_HER_BIN", "/dev/her")
	if herExtensionHost() {
		t.Fatal("a her of HERMESH_HER_BIN is registered with the browsers")
	}
}

// doctor and status say whether her is installed, which, whether Her is on
// and whether her runs.
func TestHerLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("her has no Windows build")
	}
	her := filepath.Join(t.TempDir(), "her")
	os.WriteFile(her, []byte("#!/bin/sh\necho her 0.2.0\n"), 0o755)
	t.Setenv("HERMESH_HER_BIN", her)
	for _, c := range []struct {
		st   *machinev1.HerStatus
		up   bool
		want string
	}{
		{&machinev1.HerStatus{Unsupported: "no her"}, true, "not installed"},
		{&machinev1.HerStatus{Unsupported: "windows"}, true, "can't run here (windows)"},
		{&machinev1.HerStatus{}, true, "0.2.0, off"},
		{&machinev1.HerStatus{Enabled: true, Running: true, Version: "0.2.0"}, true, "0.2.0, on, running"},
		{&machinev1.HerStatus{Enabled: true, Error: "her stopped: exit status 1"}, true, "0.2.0, on, NOT RUNNING: her stopped: exit status 1"},
		{&machinev1.HerStatus{Enabled: true}, true, "0.2.0, on, starting"},
		{&machinev1.HerStatus{Enabled: true}, false, "0.2.0, on"},
	} {
		if got := herLine(c.st, c.up); got != c.want {
			t.Errorf("herLine(%v, %v) = %q, want %q", c.st, c.up, got, c.want)
		}
	}
}
