package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/internal/paths"
)

// TestPerfSnapshotE2E verifies that `hermeshd perf-snapshot` captures a full
// CPU, wakeup, and memory observability bundle across a real `hermeshd run --owner`
// daemon (with no --debug-addr TCP port) and a real Electron `Hermesh` desktop
// instance sharing the same data directory.
func TestPerfSnapshotE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("no display for Electron")
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not installed")
	}

	bin := binaries(t)
	hermeshd := filepath.Join(bin, "hermeshd")

	dataDir, err := os.MkdirTemp(shortTemp, "hmperf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dataDir) })

	// Start hermeshd in production owner mode (no --debug-addr flag).
	daemon := start(t, "hermeshd-owner", []string{"HERMESH_FAKE_PROVIDERS=1"}, hermeshd, "run", "--owner", "--data-dir", dataDir)
	t.Cleanup(daemon.stop)

	sock := paths.LocalAPI(dataDir)
	lc := localClient(t, sock)
	eventually(t, 15*time.Second, "hermeshd local API ready", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := lc.GetStatus(ctx, &localv1.GetStatusRequest{})
		return err == nil
	})

	// Build and start the real Electron desktop app pointed at dataDir.
	deskDir, _ := filepath.Abs(filepath.Join("apps", "desktop"))
	appOut := filepath.Join(t.TempDir(), "app")
	if r := run(t, []string{"HERMESH_DESKTOP_OUT=" + appOut, "VERSION=0.1.0-rc7"}, 5*time.Minute, "pnpm", "--dir", deskDir, "build"); r.code != 0 {
		t.Fatalf("desktop build: %+v", r)
	}
	if r := run(t, nil, 5*time.Minute, "node", filepath.Join(deskDir, "node_modules", "electron", "install.js")); r.code != 0 {
		t.Fatalf("electron install: %+v", r)
	}

	bg := filepath.Join(deskDir, "e2e", "background.cjs")
	mainJS := filepath.Join(appOut, "main", "index.js")
	electronBin := filepath.Join(deskDir, "node_modules", ".bin", "electron")
	if runtime.GOOS == "windows" {
		electronBin += ".cmd"
	}
	desk := start(t, "hermesh-desktop", []string{"HERMESH_DATA_DIR=" + dataDir, "ELECTRON_RUN_AS_NODE="}, electronBin, "-r", bg, mainJS)
	t.Cleanup(desk.stop)

	perfEndpoint := paths.DesktopPerf(dataDir)
	eventually(t, 20*time.Second, "desktop perf endpoint and renderer ready", func() bool {
		if _, err := os.Stat(perfEndpoint); err != nil {
			return false
		}
		probeOut := filepath.Join(t.TempDir(), "probe")
		r := run(t, nil, 15*time.Second, hermeshd, "--data-dir", dataDir, "perf-snapshot", "--duration", "0s", "--trace", "0s", "--out", probeOut)
		if r.code != 0 {
			return false
		}
		b, err := os.ReadFile(filepath.Join(probeOut, "desktop-snapshot.json"))
		return err == nil && strings.Contains(string(b), `"domNodes":`) && !strings.Contains(string(b), `"domNodes": 0`)
	})

	bundleDir := filepath.Join(t.TempDir(), "snapshot")
	res := run(t, nil, 60*time.Second, hermeshd, "--data-dir", dataDir, "perf-snapshot", "--duration", "2s", "--trace", "1s", "--out", bundleDir)
	if res.code != 0 {
		t.Fatalf("perf-snapshot failed (code %d):\nstdout:\n%s\nstderr:\n%s\ndaemon:\n%s\ndesktop:\n%s",
			res.code, res.stdout, res.stderr, daemon.output(), desk.output())
	}
	t.Logf("perf-snapshot summary:\n%s", res.stdout)

	for _, f := range []string{
		"summary.json",
		"summary.md",
		"hermeshd-metrics-before.prom",
		"hermeshd-metrics-after.prom",
		"hermeshd-cpu.pprof",
		"hermeshd-heap.pprof",
		"hermeshd-allocs.pprof",
		"hermeshd-goroutines.txt",
		"hermeshd-trace.out",
		"desktop-snapshot.json",
		"desktop-main.cpuprofile",
	} {
		st, err := os.Stat(filepath.Join(bundleDir, f))
		if err != nil || st.Size() == 0 {
			t.Fatalf("missing or empty %s: err=%v", f, err)
		}
	}

	entries, _ := os.ReadDir(bundleDir)
	hasRendererProf := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "desktop-renderer-") && strings.HasSuffix(e.Name(), ".cpuprofile") {
			hasRendererProf = true
		}
	}
	if !hasRendererProf {
		t.Fatalf("expected at least one desktop-renderer-*.cpuprofile in %v", entries)
	}

	var sum struct {
		Processes []struct {
			PID  int    `json:"pid"`
			Role string `json:"role"`
		} `json:"processes"`
		DaemonMetrics *struct {
			PID int `json:"pid"`
		} `json:"daemonMetrics"`
		Desktop *struct {
			MainPID   int   `json:"mainPid"`
			Renderers []any `json:"renderers"`
		} `json:"desktop"`
	}
	rawSum, err := os.ReadFile(filepath.Join(bundleDir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawSum, &sum); err != nil {
		t.Fatal(err)
	}
	if sum.DaemonMetrics == nil || sum.DaemonMetrics.PID <= 0 {
		t.Fatalf("missing daemonMetrics in summary.json: %s", rawSum)
	}
	if sum.Desktop == nil || sum.Desktop.MainPID <= 0 || len(sum.Desktop.Renderers) == 0 {
		t.Fatalf("missing desktop or renderers in summary.json: %s", rawSum)
	}
	if len(sum.Processes) < 2 {
		t.Fatalf("expected full process tree (hermeshd + Hermesh), got %d rows", len(sum.Processes))
	}
}
