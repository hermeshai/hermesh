package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	"github.com/hermeshai/hermesh/internal/localipc"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/owner"
	"github.com/hermeshai/hermesh/internal/paths"
)

func TestPerfSnapshotEndToEnd(t *testing.T) {
	dataDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	dev, err := owner.Open(owner.Config{
		DataDir:        filepath.Join(dataDir, "owner"),
		Logger:         quiet,
		MachineConfig:  machine.Config{DataDir: filepath.Join(dataDir, "machine"), Logger: quiet},
		NoLocalMachine: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	go dev.Run(ctx)

	sock := paths.LocalAPI(dataDir)
	doneServe := make(chan error, 1)
	go func() {
		doneServe <- dev.ServeLocal(ctx, sock)
	}()

	// Wait for ServeLocal to accept connections and exercise an RPC + Watch stream.
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return localipc.Dial(ctx, sock)
	}}}
	lc := localv1connect.NewLocalServiceClient(hc, "http://local")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := lc.GetStatus(ctx, &localv1.GetStatusRequest{}); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ServeLocal did not become ready")
		}
		time.Sleep(30 * time.Millisecond)
	}
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	stream, err := lc.Watch(watchCtx, &localv1.WatchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() {
		t.Fatalf("Watch first message failed: %v", stream.Err())
	}

	// Stand up a mock desktop perf endpoint at paths.DesktopPerf(dataDir)
	// serving a realistic DesktopPerfSnapshot + V8 .cpuprofile.
	deskLn, err := localipc.Listen(paths.DesktopPerf(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	defer deskLn.Close()
	deskMux := http.NewServeMux()
	deskMux.HandleFunc("GET /snapshot", func(w http.ResponseWriter, r *http.Request) {
		payload := map[string]any{
			"at":            time.Now().UTC().Format(time.RFC3339),
			"mainPid":       os.Getpid(),
			"windowSeconds": 1.0,
			"processes": []map[string]any{
				{"pid": os.Getpid(), "type": "Browser", "cpuPct": 4.2, "idleWakeupsPerSec": 18.5, "workingSetMB": 64.0},
			},
			"mainMemoryMB":  map[string]any{"rss": 64.0, "heapTotal": 22.0, "heapUsed": 14.5, "external": 2.1},
			"activeHandles": 9,
			"eventLoop":     map[string]any{"utilizationPct": 3.5, "delayP50Ms": 0.4, "delayP99Ms": 2.1, "delayMaxMs": 4.0},
			"countersAfter": map[string]any{"watchMessages": 3},
			"countersDelta": map[string]any{
				"watchMessages":   2,
				"worldBroadcasts": 2,
				"appMenuBuilds":   2,
				"appMenuBuildMs":  3.4,
				"dockMenuBuilds":  2,
				"dockMenuBuildMs": 2.8,
				"trayIconDraws":   1,
				"trayIconDrawMs":  0.9,
			},
			"gatewayCallsDelta": map[string]any{"GetStatus": 1},
			"renderers": []map[string]any{
				{
					"webContentsId": 1, "osProcessId": os.Getpid(), "type": "window", "url": "app://hermesh/index.html",
					"route": "overview", "windowVisible": true, "windowFocused": true, "windowMinimized": false,
					"backgroundThrottled": false, "domNodes": 420, "jsEventListeners": 85,
					"jsHeapUsedMB": 12.3, "jsHeapTotalMB": 18.0, "busyPct": 5.1,
					"animations": map[string]any{"total": 90, "running": 90, "infinite": 90, "compositorOnly": 89, "mainThreadCount": 1},
				},
			},
			"mainCpuProfile": map[string]any{
				"nodes": []map[string]any{
					{"id": 1, "hitCount": 0, "callFrame": map[string]any{"functionName": "(root)", "url": "", "lineNumber": 0}},
					{"id": 2, "hitCount": 8, "callFrame": map[string]any{"functionName": "dockMenu", "url": "file:///tray.js", "lineNumber": 178}},
				},
				"samples":    []int{2, 2},
				"timeDeltas": []int64{5000, 7000},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	})
	deskSrv := &http.Server{Handler: deskMux}
	defer deskSrv.Close()
	go deskSrv.Serve(deskLn)

	// Invoke CLI command `hermeshd --data-dir <dir> perf-snapshot -d 1s --trace 1s -o <out>`
	outDir := filepath.Join(t.TempDir(), "bundle")
	var stdout bytes.Buffer
	root := rootCommand()
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs([]string{"--data-dir", dataDir, "perf-snapshot", "--duration", "1s", "--trace", "1s", "--blocking", "--out", outDir})
	if err := root.Execute(); err != nil {
		t.Fatalf("perf-snapshot failed: %v\noutput:\n%s", err, stdout.String())
	}

	for _, want := range []string{
		"summary.json",
		"summary.md",
		"hermeshd-metrics-before.prom",
		"hermeshd-metrics-after.prom",
		"hermeshd-cpu.pprof",
		"hermeshd-heap.pprof",
		"hermeshd-allocs.pprof",
		"hermeshd-goroutines.txt",
		"hermeshd-mutex.pprof",
		"hermeshd-block.pprof",
		"hermeshd-trace.out",
		"desktop-snapshot.json",
		"desktop-main.cpuprofile",
	} {
		st, err := os.Stat(filepath.Join(outDir, want))
		if err != nil || st.Size() == 0 {
			t.Fatalf("expected non-empty artifact %s: err=%v", want, err)
		}
	}

	md, err := os.ReadFile(filepath.Join(outDir, "summary.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, snippet := range []string{
		"Process Tree",
		"hermeshd (PID",
		"hermesh_local_streams_active{method=\"Watch\"}=1",
		"Hermesh Desktop",
		"dockMenu",
	} {
		if !strings.Contains(string(md), snippet) {
			t.Fatalf("summary.md missing %q:\n%s", snippet, md)
		}
	}
}

func TestSummarizeDaemonMetricsSubtractsIdleCPU(t *testing.T) {
	before := []byte("process_pid 42\ngo_cpu_classes_total_cpu_seconds 100\ngo_cpu_classes_idle_cpu_seconds 95\ngo_cpu_classes_gc_total_cpu_seconds 1\n")
	after := []byte("process_pid 42\ngo_cpu_classes_total_cpu_seconds 220\ngo_cpu_classes_idle_cpu_seconds 214.5\ngo_cpu_classes_gc_total_cpu_seconds 1.2\n")
	s := summarizeDaemonMetrics(before, after, 10.0)
	// Active Go CPU = (120 - 119.5)s / 10s = 5.0%, NOT 1200%.
	if s.GoCPUPct != 5.0 {
		t.Fatalf("GoCPUPct = %v, want 5.0", s.GoCPUPct)
	}
	if s.GCCPUPct != 2.0 {
		t.Fatalf("GCCPUPct = %v, want 2.0", s.GCCPUPct)
	}
}
