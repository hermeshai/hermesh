package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/hermeshai/hermesh/internal/localipc"
	"github.com/hermeshai/hermesh/internal/paths"
	"github.com/hermeshai/hermesh/pkg/metrics"
)

func perfSnapshotCommand(dataDir *string) *cobra.Command {
	var duration, traceDur time.Duration
	var outDir string
	var rendererProfile, blocking bool
	cmd := &cobra.Command{
		Use:   "perf-snapshot",
		Short: "Capture a CPU, wakeup, and memory observability bundle of hermeshd and the Hermesh desktop app",
		Long: `Captures a point-in-time performance and memory bundle across the Hermesh
process tree (hermeshd, her, spawn helpers, and the Hermesh desktop app when
running): OS CPU% & idle wakeups, macOS physical footprint, Go pprof profiles
(CPU, heap, allocs, goroutines, optional mutex/block and execution trace),
Prometheus metrics deltas, and Electron main + renderer CPU profiles, event-loop
utilization, menu/IPC counters, DOM counts, and active animations.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := outDir
			if dir == "" {
				dir = filepath.Join(*dataDir, "logs", "perf-"+time.Now().Format("20060102-150405"))
			}
			return runPerfSnapshot(cmd.Context(), *dataDir, dir, duration, traceDur, rendererProfile, blocking, cmd.OutOrStdout())
		},
	}
	cmd.Flags().DurationVarP(&duration, "duration", "d", 10*time.Second, "sampling window for CPU profiles, wakeups, and rate deltas (0 for instant state)")
	cmd.Flags().DurationVar(&traceDur, "trace", 3*time.Second, "Go runtime execution trace duration captured after the CPU window (0 = skip)")
	cmd.Flags().StringVarP(&outDir, "out", "o", "", "output directory (default <data-dir>/logs/perf-<timestamp>)")
	cmd.Flags().BoolVar(&rendererProfile, "renderer-profile", true, "capture V8 CPU profiles of open Electron renderer windows")
	cmd.Flags().BoolVar(&blocking, "blocking", false, "enable Go mutex and block profiling during the sampling window")
	return cmd
}

type perfSummary struct {
	CapturedAt      string                 `json:"capturedAt"`
	DurationSeconds float64                `json:"durationSeconds"`
	OutDir          string                 `json:"outDir"`
	DaemonSocket    string                 `json:"daemonSocket,omitempty"`
	DesktopSocket   string                 `json:"desktopSocket,omitempty"`
	Processes       []procSummaryRow       `json:"processes,omitempty"`
	Totals          procTotals             `json:"totals"`
	DaemonMetrics   *daemonMetricsSummary  `json:"daemonMetrics,omitempty"`
	Desktop         *desktopPerfWire       `json:"desktop,omitempty"`
	TopMainJS       []v8HotFunc            `json:"topMainJS,omitempty"`
	TopRendererJS   map[string][]v8HotFunc `json:"topRendererJS,omitempty"`
	Files           []string               `json:"files"`
}

type procSummaryRow struct {
	PID            int     `json:"pid"`
	PPID           int     `json:"ppid,omitempty"`
	Role           string  `json:"role"`
	Command        string  `json:"command"`
	CPUPct         float64 `json:"cpuPct"`
	IdleWakeupsSec float64 `json:"idleWakeupsPerSec,omitempty"`
	FootprintMB    float64 `json:"footprintMB,omitempty"`
	RSSMB          float64 `json:"rssMB,omitempty"`
}

type procTotals struct {
	CPUPct         float64 `json:"cpuPct"`
	IdleWakeupsSec float64 `json:"idleWakeupsPerSec"`
	FootprintMB    float64 `json:"footprintMB"`
	RSSMB          float64 `json:"rssMB"`
}

type daemonMetricsSummary struct {
	PID            int                `json:"pid"`
	Goroutines     float64            `json:"goroutines"`
	OpenFDs        float64            `json:"openFDs"`
	HeapObjectsMB  float64            `json:"heapObjectsMB"`
	HeapUnusedMB   float64            `json:"heapUnusedMB"`
	HeapReleasedMB float64            `json:"heapReleasedMB"`
	OSStacksMB     float64            `json:"osStacksMB"`
	TotalGoMemMB   float64            `json:"totalGoMemMB"`
	AllocRateMBPS  float64            `json:"allocRateMBPerSec"`
	GCCyclesPerSec float64            `json:"gcCyclesPerSec"`
	GoCPUPct       float64            `json:"goCpuPct"`
	GCCPUPct       float64            `json:"gcCpuPct"`
	OwnerDBMB      float64            `json:"ownerDbMB"`
	OwnerWALMB     float64            `json:"ownerWalMB"`
	MachineDBMB    float64            `json:"machineDbMB"`
	MachineWALMB   float64            `json:"machineWalMB"`
	ActiveStreams  map[string]float64 `json:"activeStreams,omitempty"`
	Deltas         map[string]float64 `json:"deltas,omitempty"`
	RatesPerSec    map[string]float64 `json:"ratesPerSec,omitempty"`
}

type desktopPerfWire struct {
	At                string                `json:"at"`
	MainPID           int                   `json:"mainPid"`
	WindowSeconds     float64               `json:"windowSeconds"`
	Processes         []desktopProcWire     `json:"processes"`
	MainMemoryMB      map[string]float64    `json:"mainMemoryMB"`
	ActiveHandles     int                   `json:"activeHandles"`
	EventLoop         map[string]float64    `json:"eventLoop"`
	CountersAfter     map[string]any        `json:"countersAfter"`
	CountersDelta     map[string]float64    `json:"countersDelta"`
	GatewayCallsDelta map[string]float64    `json:"gatewayCallsDelta"`
	Renderers         []desktopRendererWire `json:"renderers"`
	MainCPUProfile    json.RawMessage       `json:"mainCpuProfile,omitempty"`
}

type desktopProcWire struct {
	PID               int     `json:"pid"`
	Type              string  `json:"type"`
	Name              string  `json:"name,omitempty"`
	CPUPct            float64 `json:"cpuPct"`
	IdleWakeupsPerSec float64 `json:"idleWakeupsPerSec"`
	WorkingSetMB      float64 `json:"workingSetMB"`
}

type desktopRendererWire struct {
	WebContentsID       int             `json:"webContentsId"`
	OSProcessID         int             `json:"osProcessId"`
	Type                string          `json:"type"`
	URL                 string          `json:"url"`
	Route               string          `json:"route"`
	WindowVisible       *bool           `json:"windowVisible"`
	WindowFocused       *bool           `json:"windowFocused"`
	WindowMinimized     *bool           `json:"windowMinimized"`
	BackgroundThrottled bool            `json:"backgroundThrottled"`
	VisibilityState     *string         `json:"visibilityState"`
	Hidden              *bool           `json:"hidden"`
	DOMNodes            int             `json:"domNodes"`
	JSEventListeners    int             `json:"jsEventListeners"`
	JSHeapUsedMB        float64         `json:"jsHeapUsedMB"`
	JSHeapTotalMB       float64         `json:"jsHeapTotalMB"`
	BusyPct             *float64        `json:"busyPct,omitempty"`
	ScriptPct           *float64        `json:"scriptPct,omitempty"`
	StyleLayoutPct      *float64        `json:"styleLayoutPct,omitempty"`
	LayoutsPerSec       *float64        `json:"layoutsPerSec,omitempty"`
	Animations          *animWire       `json:"animations,omitempty"`
	CPUProfile          json.RawMessage `json:"cpuProfile,omitempty"`
}

type animWire struct {
	Total             int `json:"total"`
	Running           int `json:"running"`
	Infinite          int `json:"infinite"`
	CompositorOnly    int `json:"compositorOnly"`
	MainThreadCount   int `json:"mainThreadCount"`
	MainThreadSamples []struct {
		Target string   `json:"target"`
		Props  []string `json:"props"`
	} `json:"mainThreadSamples"`
}

func ipcClient(sock string, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return localipc.Dial(ctx, sock)
			},
		},
		Timeout: timeout,
	}
}

func probeDaemonSocket(ctx context.Context, dataDir string) (string, *http.Client) {
	candidates := []string{
		paths.LocalAPI(dataDir),
		paths.Control(resolveMachineDir(dataDir)),
	}
	for _, sock := range candidates {
		hc := ipcClient(sock, 3*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://hermeshd/metrics", nil)
		if res, err := hc.Do(req); err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return sock, ipcClient(sock, 2*time.Minute)
			}
		}
	}
	return "", nil
}

func probeDesktopSocket(ctx context.Context, dataDir string) (string, *http.Client) {
	sock := paths.DesktopPerf(dataDir)
	hc := ipcClient(sock, 3*time.Second)
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://desktop/snapshot?seconds=0&main_profile=0", nil)
	if res, err := hc.Do(req); err == nil {
		res.Body.Close()
		if res.StatusCode == http.StatusOK {
			return sock, ipcClient(sock, 2*time.Minute)
		}
	}
	return "", nil
}

func fetchBytes(ctx context.Context, hc *http.Client, method, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: HTTP %d: %s", url, res.StatusCode, strings.TrimSpace(string(b)))
	}
	return b, nil
}

func runPerfSnapshot(ctx context.Context, dataDir, outDir string, dur, traceDur time.Duration, rendererProf, blocking bool, out io.Writer) error {
	if dur < 0 {
		dur = 0
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return err
	}
	var savedMu sync.Mutex
	var saved []string
	writeFile := func(name string, data []byte) error {
		if err := os.WriteFile(filepath.Join(outDir, name), data, 0o600); err != nil {
			return err
		}
		savedMu.Lock()
		saved = append(saved, name)
		savedMu.Unlock()
		return nil
	}

	daemonSock, daemonHC := probeDaemonSocket(ctx, dataDir)
	desktopSock, desktopHC := probeDesktopSocket(ctx, dataDir)
	if daemonHC == nil && desktopHC == nil {
		return fmt.Errorf("neither hermeshd nor the Hermesh desktop app is reachable under %s", dataDir)
	}

	sec := int(math.Round(dur.Seconds()))
	if dur > 0 && sec < 1 {
		sec = 1
	}
	fmt.Fprintf(out, "Capturing snapshot (%ds) into %s …\n", sec, outDir)

	if daemonHC != nil && blocking {
		fetchBytes(ctx, daemonHC, "POST", "http://hermeshd/debug/pprof/blocking?enable=1")
		defer fetchBytes(context.Background(), daemonHC, "POST", "http://hermeshd/debug/pprof/blocking?enable=0")
	}

	var promBefore, promAfter []byte
	if daemonHC != nil {
		if b, err := fetchBytes(ctx, daemonHC, "GET", "http://hermeshd/metrics"); err == nil {
			promBefore = b
			_ = writeFile("hermeshd-metrics-before.prom", b)
		}
	}

	ps0 := sampleOSProcs()
	t0 := time.Now()

	var wg sync.WaitGroup
	var desk *desktopPerfWire
	if daemonHC != nil && sec > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			url := fmt.Sprintf("http://hermeshd/debug/pprof/profile?seconds=%d", sec)
			if b, err := fetchBytes(ctx, daemonHC, "GET", url); err == nil {
				_ = writeFile("hermeshd-cpu.pprof", b)
			}
		}()
	}
	if desktopHC != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rp := "0"
			if rendererProf && sec > 0 {
				rp = "1"
			}
			mp := "0"
			if sec > 0 {
				mp = "1"
			}
			url := fmt.Sprintf("http://desktop/snapshot?seconds=%d&main_profile=%s&renderer_profile=%s", sec, mp, rp)
			if b, err := fetchBytes(ctx, desktopHC, "GET", url); err == nil {
				var d desktopPerfWire
				if json.Unmarshal(b, &d) == nil {
					desk = &d
				}
			}
		}()
	}
	if daemonHC == nil && desktopHC == nil && dur > 0 {
		time.Sleep(dur)
	}
	wg.Wait()
	elapsed := math.Max(0.001, time.Since(t0).Seconds())
	ps1 := sampleOSProcs()

	var daemonPID int
	if daemonHC != nil {
		if b, err := fetchBytes(ctx, daemonHC, "GET", "http://hermeshd/metrics"); err == nil {
			promAfter = b
			_ = writeFile("hermeshd-metrics-after.prom", b)
			m := parseProm(b)
			daemonPID = int(m["process_pid"])
		}
		for _, p := range []struct{ url, file string }{
			{"http://hermeshd/debug/pprof/heap", "hermeshd-heap.pprof"},
			{"http://hermeshd/debug/pprof/allocs", "hermeshd-allocs.pprof"},
			{"http://hermeshd/debug/pprof/goroutine?debug=2", "hermeshd-goroutines.txt"},
		} {
			if b, err := fetchBytes(ctx, daemonHC, "GET", p.url); err == nil {
				_ = writeFile(p.file, b)
			}
		}
		if blocking {
			for _, p := range []struct{ url, file string }{
				{"http://hermeshd/debug/pprof/mutex", "hermeshd-mutex.pprof"},
				{"http://hermeshd/debug/pprof/block", "hermeshd-block.pprof"},
			} {
				if b, err := fetchBytes(ctx, daemonHC, "GET", p.url); err == nil {
					_ = writeFile(p.file, b)
				}
			}
		}
		if traceSec := int(math.Round(traceDur.Seconds())); traceSec > 0 {
			url := fmt.Sprintf("http://hermeshd/debug/pprof/trace?seconds=%d", traceSec)
			if b, err := fetchBytes(ctx, daemonHC, "GET", url); err == nil {
				_ = writeFile("hermeshd-trace.out", b)
			}
		}
	}

	var topMain []v8HotFunc
	topRenderers := map[string][]v8HotFunc{}
	if desk != nil {
		if len(desk.MainCPUProfile) > 0 && string(desk.MainCPUProfile) != "null" {
			_ = writeFile("desktop-main.cpuprofile", desk.MainCPUProfile)
			topMain = summarizeV8Profile(desk.MainCPUProfile, 12)
			desk.MainCPUProfile = nil
		}
		for i := range desk.Renderers {
			r := &desk.Renderers[i]
			if len(r.CPUProfile) > 0 && string(r.CPUProfile) != "null" {
				tag := sanitizeFileTag(r.Route)
				name := fmt.Sprintf("desktop-renderer-%d-%s.cpuprofile", r.WebContentsID, tag)
				_ = writeFile(name, r.CPUProfile)
				topRenderers[fmt.Sprintf("wc#%d (%s)", r.WebContentsID, r.Route)] = summarizeV8Profile(r.CPUProfile, 10)
				r.CPUProfile = nil
			}
		}
		if clean, err := json.MarshalIndent(desk, "", "  "); err == nil {
			_ = writeFile("desktop-snapshot.json", clean)
		}
	}

	procs, totals := buildProcessTreeSummary(ps0, ps1, elapsed, daemonPID, desk)
	var dm *daemonMetricsSummary
	if len(promAfter) > 0 {
		dm = summarizeDaemonMetrics(promBefore, promAfter, elapsed)
	}

	sort.Strings(saved)
	sum := perfSummary{
		CapturedAt:      time.Now().Format(time.RFC3339),
		DurationSeconds: round2f(elapsed),
		OutDir:          outDir,
		DaemonSocket:    daemonSock,
		DesktopSocket:   desktopSock,
		Processes:       procs,
		Totals:          totals,
		DaemonMetrics:   dm,
		Desktop:         desk,
		TopMainJS:       topMain,
		TopRendererJS:   topRenderers,
		Files:           saved,
	}
	if b, err := json.MarshalIndent(sum, "", "  "); err == nil {
		_ = writeFile("summary.json", b)
	}
	md := formatSummaryMarkdown(sum)
	_ = writeFile("summary.md", []byte(md))
	fmt.Fprint(out, "\n"+md)
	return nil
}

func buildProcessTreeSummary(ps0, ps1 map[int]osProcSample, dt float64, daemonPID int, desk *desktopPerfWire) ([]procSummaryRow, procTotals) {
	roots := map[int]string{}
	if daemonPID > 0 {
		roots[daemonPID] = "hermeshd"
	}
	deskByPID := map[int]desktopProcWire{}
	if desk != nil {
		if desk.MainPID > 0 {
			roots[desk.MainPID] = "Hermesh (Main)"
		}
		for _, p := range desk.Processes {
			deskByPID[p.PID] = p
			roots[p.PID] = "Hermesh (" + p.Type + ")"
		}
	}
	// Walk descendants in ps1 from all known root PIDs.
	included := map[int]string{}
	for pid, role := range roots {
		included[pid] = role
	}
	if len(ps1) > 0 {
		children := map[int][]int{}
		for pid, p := range ps1 {
			children[p.PPID] = append(children[p.PPID], pid)
		}
		queue := make([]int, 0, len(roots))
		for pid := range roots {
			queue = append(queue, pid)
		}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, ch := range children[cur] {
				if _, seen := included[ch]; !seen {
					included[ch] = classifyChildRole(ps1[ch].Command)
					queue = append(queue, ch)
				}
			}
		}
	}

	pids := make([]int, 0, len(included))
	var needWakePIDs []int
	for pid := range included {
		pids = append(pids, pid)
		if _, fromElectron := deskByPID[pid]; !fromElectron {
			needWakePIDs = append(needWakePIDs, pid)
		}
	}
	sort.Ints(pids)
	footprints := sampleFootprintBytes(pids)
	darwinWakes := sampleDarwinWakeups(needWakePIDs)

	var rows []procSummaryRow
	var totals procTotals
	for _, pid := range pids {
		p1, ok1 := ps1[pid]
		dp, hasDesk := deskByPID[pid]
		if !ok1 && !hasDesk {
			continue
		}
		row := procSummaryRow{
			PID:     pid,
			PPID:    p1.PPID,
			Role:    included[pid],
			Command: filepath.Base(p1.Command),
			RSSMB:   round2f(float64(p1.RSSKB) / 1024.0),
		}
		if row.Command == "." || row.Command == "" {
			row.Command = dp.Type
		}
		if hasDesk {
			row.CPUPct = dp.CPUPct
			row.IdleWakeupsSec = dp.IdleWakeupsPerSec
			if row.RSSMB == 0 {
				row.RSSMB = dp.WorkingSetMB
			}
		} else if p0, ok0 := ps0[pid]; ok0 && dt >= 0.2 {
			row.CPUPct = round2f(math.Max(0, (p1.CPUSec-p0.CPUSec)/dt*100.0))
		}
		if !hasDesk {
			if w, ok := darwinWakes[pid]; ok {
				row.IdleWakeupsSec = round2f(w)
			}
		}
		if fb, ok := footprints[pid]; ok && fb > 0 {
			row.FootprintMB = round2f(float64(fb) / (1024 * 1024))
		} else {
			row.FootprintMB = row.RSSMB
		}
		totals.CPUPct = round2f(totals.CPUPct + row.CPUPct)
		totals.IdleWakeupsSec = round2f(totals.IdleWakeupsSec + row.IdleWakeupsSec)
		totals.FootprintMB = round2f(totals.FootprintMB + row.FootprintMB)
		totals.RSSMB = round2f(totals.RSSMB + row.RSSMB)
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CPUPct != rows[j].CPUPct {
			return rows[i].CPUPct > rows[j].CPUPct
		}
		return rows[i].FootprintMB > rows[j].FootprintMB
	})
	return rows, totals
}

func classifyChildRole(cmd string) string {
	base := strings.ToLower(filepath.Base(cmd))
	switch {
	case strings.Contains(base, "spawn-helper") || strings.Contains(cmd, "spawn-helper"):
		return "spawn-helper"
	case base == "her" || strings.HasPrefix(base, "her."):
		return "her (Rust kernel)"
	case strings.Contains(base, "signer"):
		return "Hermesh Signer"
	case strings.Contains(base, "screen"):
		return "hermesh-screen"
	case strings.Contains(base, "agent-desktop"):
		return "agent-desktop"
	case strings.Contains(base, "claude") || strings.Contains(base, "codex"):
		return "coding-agent"
	default:
		return "child"
	}
}

func parseProm(b []byte) map[string]float64 {
	out := map[string]float64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.LastIndexAny(line, " \t")
		if idx <= 0 {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(line[idx+1:]), 64)
		if err == nil {
			out[strings.TrimSpace(line[:idx])] = v
		}
	}
	return out
}

func summarizeDaemonMetrics(beforeBytes, afterBytes []byte, dt float64) *daemonMetricsSummary {
	m0 := parseProm(beforeBytes)
	m1 := parseProm(afterBytes)
	const mb = 1024.0 * 1024.0
	s := &daemonMetricsSummary{
		PID:            int(m1["process_pid"]),
		Goroutines:     m1["go_sched_goroutines_goroutines"],
		OpenFDs:        m1["process_open_fds"],
		HeapObjectsMB:  round2f(m1["go_memory_classes_heap_objects_bytes"] / mb),
		HeapUnusedMB:   round2f(m1["go_memory_classes_heap_unused_bytes"] / mb),
		HeapReleasedMB: round2f(m1["go_memory_classes_heap_released_bytes"] / mb),
		OSStacksMB:     round2f(m1["go_memory_classes_os_stacks_bytes"] / mb),
		TotalGoMemMB:   round2f(m1["go_memory_classes_total_bytes"] / mb),
		OwnerDBMB:      round2f(m1["hermesh_owner_db_bytes"] / mb),
		OwnerWALMB:     round2f(m1["hermesh_owner_wal_bytes"] / mb),
		MachineDBMB:    round2f(m1["hermesh_machine_db_bytes"] / mb),
		MachineWALMB:   round2f(m1["hermesh_machine_wal_bytes"] / mb),
		ActiveStreams:  map[string]float64{},
		Deltas:         map[string]float64{},
		RatesPerSec:    map[string]float64{},
	}
	for k, v := range m1 {
		if strings.HasPrefix(k, "hermesh_local_streams_active") && v != 0 {
			s.ActiveStreams[k] = v
		}
	}
	if len(m0) > 0 && dt >= 0.2 {
		s.AllocRateMBPS = round2f(math.Max(0, m1["go_gc_heap_allocs_bytes"]-m0["go_gc_heap_allocs_bytes"]) / mb / dt)
		s.GCCyclesPerSec = round2f(math.Max(0, m1["go_gc_cycles_total_gc_cycles"]-m0["go_gc_cycles_total_gc_cycles"]) / dt)
		totalCPU := m1["go_cpu_classes_total_cpu_seconds"] - m0["go_cpu_classes_total_cpu_seconds"]
		idleCPU := m1["go_cpu_classes_idle_cpu_seconds"] - m0["go_cpu_classes_idle_cpu_seconds"]
		s.GoCPUPct = round2f(math.Max(0, totalCPU-idleCPU) / dt * 100.0)
		s.GCCPUPct = round2f(math.Max(0, m1["go_cpu_classes_gc_total_cpu_seconds"]-m0["go_cpu_classes_gc_total_cpu_seconds"]) / dt * 100.0)
		for k, v1 := range m1 {
			if !strings.HasPrefix(k, "hermesh_") || strings.Contains(k, "_bucket{") {
				continue
			}
			if strings.HasSuffix(k, "_total") || strings.Contains(k, "_total{") || strings.HasSuffix(k, "_count") || strings.Contains(k, "_count{") || strings.HasSuffix(k, "_sum") || strings.Contains(k, "_sum{") {
				d := v1 - m0[k]
				if d > 0 {
					s.Deltas[k] = round2f(d)
					s.RatesPerSec[k] = round2f(d / dt)
				}
			}
		}
	}
	return s
}

type v8HotFunc struct {
	Function string  `json:"function"`
	Location string  `json:"location,omitempty"`
	SelfMS   float64 `json:"selfMs"`
	SelfPct  float64 `json:"selfPct"`
}

func summarizeV8Profile(raw []byte, limit int) []v8HotFunc {
	var p struct {
		Nodes []struct {
			ID        int `json:"id"`
			HitCount  int `json:"hitCount"`
			CallFrame struct {
				FunctionName string `json:"functionName"`
				URL          string `json:"url"`
				LineNumber   int    `json:"lineNumber"`
			} `json:"callFrame"`
		} `json:"nodes"`
		Samples    []int   `json:"samples"`
		TimeDeltas []int64 `json:"timeDeltas"`
	}
	if json.Unmarshal(raw, &p) != nil || len(p.Nodes) == 0 {
		return nil
	}
	selfUS := map[int]int64{}
	var totalUS int64
	if len(p.Samples) > 0 && len(p.Samples) == len(p.TimeDeltas) {
		for i, id := range p.Samples {
			d := p.TimeDeltas[i]
			if d < 0 {
				d = 0
			}
			selfUS[id] += d
			totalUS += d
		}
	} else {
		for _, n := range p.Nodes {
			us := int64(n.HitCount) * 500
			selfUS[n.ID] += us
			totalUS += us
		}
	}
	if totalUS <= 0 {
		return nil
	}
	type agg struct {
		fn, loc string
		us      int64
	}
	byFn := map[string]*agg{}
	for _, n := range p.Nodes {
		us := selfUS[n.ID]
		if us == 0 {
			continue
		}
		fn := n.CallFrame.FunctionName
		if fn == "" {
			fn = "(anonymous)"
		}
		if fn == "(idle)" || fn == "(program)" {
			continue
		}
		loc := ""
		if n.CallFrame.URL != "" {
			loc = fmt.Sprintf("%s:%d", filepath.Base(n.CallFrame.URL), n.CallFrame.LineNumber+1)
		}
		key := fn + "@" + loc
		a := byFn[key]
		if a == nil {
			a = &agg{fn: fn, loc: loc}
			byFn[key] = a
		}
		a.us += us
	}
	list := make([]v8HotFunc, 0, len(byFn))
	for _, a := range byFn {
		list = append(list, v8HotFunc{
			Function: a.fn,
			Location: a.loc,
			SelfMS:   round2f(float64(a.us) / 1000.0),
			SelfPct:  round2f(float64(a.us) / float64(totalUS) * 100.0),
		})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].SelfMS > list[j].SelfMS })
	if len(list) > limit {
		list = list[:limit]
	}
	return list
}

func formatSummaryMarkdown(s perfSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Hermesh Performance Snapshot\n\n")
	fmt.Fprintf(&b, "- **Window**: `%.2fs`  ·  **Output**: `%s`\n", s.DurationSeconds, s.OutDir)
	fmt.Fprintf(&b, "- **Total Tree**: `%.1f%% CPU`  ·  `%.1f wakeups/s`  ·  `%.1f MB footprint` (`%.1f MB RSS`)\n\n",
		s.Totals.CPUPct, s.Totals.IdleWakeupsSec, s.Totals.FootprintMB, s.Totals.RSSMB)

	if len(s.Processes) > 0 {
		fmt.Fprint(&b, "## 1. Process Tree\n\n")
		fmt.Fprint(&b, "| PID | Role | Command | % CPU | Wakeups/s | Footprint MB | RSS MB |\n")
		fmt.Fprint(&b, "|---:|---|---|---:|---:|---:|---:|\n")
		for _, p := range s.Processes {
			fmt.Fprintf(&b, "| %d | %s | `%s` | %.1f | %.1f | %.1f | %.1f |\n",
				p.PID, p.Role, p.Command, p.CPUPct, p.IdleWakeupsSec, p.FootprintMB, p.RSSMB)
		}
		fmt.Fprintln(&b)
	}

	if d := s.DaemonMetrics; d != nil {
		fmt.Fprintf(&b, "## 2. hermeshd (PID %d)\n\n", d.PID)
		fmt.Fprintf(&b, "- **Runtime**: `%.0f` goroutines · `%.0f` open FDs · Go CPU `%.1f%%` (GC `%.1f%%`) · Alloc rate `%.2f MB/s` · GC `%.2f /s`\n",
			d.Goroutines, d.OpenFDs, d.GoCPUPct, d.GCCPUPct, d.AllocRateMBPS, d.GCCyclesPerSec)
		fmt.Fprintf(&b, "- **Go Memory**: Total `%.1f MB` · Heap live `%.1f MB` · Heap unused `%.1f MB` (released `%.1f MB`) · Stacks `%.1f MB`\n",
			d.TotalGoMemMB, d.HeapObjectsMB, d.HeapUnusedMB, d.HeapReleasedMB, d.OSStacksMB)
		fmt.Fprintf(&b, "- **SQLite**: owner DB `%.2f MB` (WAL `%.2f MB`) · machine DB `%.2f MB` (WAL `%.2f MB`)\n",
			d.OwnerDBMB, d.OwnerWALMB, d.MachineDBMB, d.MachineWALMB)
		if len(d.ActiveStreams) > 0 {
			keys := sortedKeys(d.ActiveStreams)
			var parts []string
			for _, k := range keys {
				parts = append(parts, fmt.Sprintf("`%s=%.0f`", k, d.ActiveStreams[k]))
			}
			fmt.Fprintf(&b, "- **Active Streams**: %s\n", strings.Join(parts, ", "))
		}
		if len(d.Deltas) > 0 {
			fmt.Fprintf(&b, "\n| Metric | Window Delta | Rate (/s) |\n|---|---:|---:|\n")
			keys := sortedKeys(d.Deltas)
			for _, k := range keys {
				fmt.Fprintf(&b, "| `%s` | %.2f | %.2f |\n", k, d.Deltas[k], d.RatesPerSec[k])
			}
		}
		fmt.Fprintln(&b)
	}

	if desk := s.Desktop; desk != nil {
		fmt.Fprintf(&b, "## 3. Hermesh Desktop (Main PID %d)\n\n", desk.MainPID)
		fmt.Fprintf(&b, "- **Main Event Loop**: ELU `%.1f%%` · Delay p50 `%.2f ms` / p99 `%.2f ms` / max `%.2f ms` · Active handles `%d`\n",
			desk.EventLoop["utilizationPct"], desk.EventLoop["delayP50Ms"], desk.EventLoop["delayP99Ms"], desk.EventLoop["delayMaxMs"], desk.ActiveHandles)
		fmt.Fprintf(&b, "- **Main Memory**: RSS `%.1f MB` · V8 Heap `%.1f / %.1f MB` · External `%.1f MB`\n",
			desk.MainMemoryMB["rss"], desk.MainMemoryMB["heapUsed"], desk.MainMemoryMB["heapTotal"], desk.MainMemoryMB["external"])
		fmt.Fprintf(&b, "- **Main Activity (window delta)**: Watch msgs `%.0f` · World broadcasts `%.0f` · AppMenu rebuilds `%.0f` (`%.1f ms`) · DockMenu rebuilds `%.0f` (`%.1f ms`) · Tray icon draws `%.0f` (`%.1f ms`)\n",
			desk.CountersDelta["watchMessages"], desk.CountersDelta["worldBroadcasts"],
			desk.CountersDelta["appMenuBuilds"], desk.CountersDelta["appMenuBuildMs"],
			desk.CountersDelta["dockMenuBuilds"], desk.CountersDelta["dockMenuBuildMs"],
			desk.CountersDelta["trayIconDraws"], desk.CountersDelta["trayIconDrawMs"])
		if len(desk.Renderers) > 0 {
			fmt.Fprint(&b, "\n| WC | PID | Route | Visible/Focused/Throttled | Busy % | Heap MB | DOM Nodes | Listeners | Running Anims (Main-Thread) |\n")
			fmt.Fprint(&b, "|---:|---:|---|---|---:|---:|---:|---:|---:|\n")
			for _, r := range desk.Renderers {
				busy := 0.0
				if r.BusyPct != nil {
					busy = *r.BusyPct
				}
				runAnim, mtAnim := 0, 0
				if r.Animations != nil {
					runAnim, mtAnim = r.Animations.Running, r.Animations.MainThreadCount
				}
				fmt.Fprintf(&b, "| %d | %d | `%s` | `%v/%v/%v` | %.1f | %.1f | %d | %d | %d (%d) |\n",
					r.WebContentsID, r.OSProcessID, r.Route,
					boolVal(r.WindowVisible), boolVal(r.WindowFocused), r.BackgroundThrottled,
					busy, r.JSHeapUsedMB, r.DOMNodes, r.JSEventListeners, runAnim, mtAnim)
			}
		}
		fmt.Fprintln(&b)
	}

	if len(s.TopMainJS) > 0 {
		fmt.Fprintf(&b, "### Top Electron Main JS Functions (Self Time)\n\n")
		for _, fn := range s.TopMainJS {
			fmt.Fprintf(&b, "- `%.1f ms` (`%.1f%%`) `%s` %s\n", fn.SelfMS, fn.SelfPct, fn.Function, fn.Location)
		}
		fmt.Fprintln(&b)
	}
	for label, fns := range s.TopRendererJS {
		if len(fns) == 0 {
			continue
		}
		fmt.Fprintf(&b, "### Top Renderer JS Functions — %s\n\n", label)
		for _, fn := range fns {
			fmt.Fprintf(&b, "- `%.1f ms` (`%.1f%%`) `%s` %s\n", fn.SelfMS, fn.SelfPct, fn.Function, fn.Location)
		}
		fmt.Fprintln(&b)
	}

	fmt.Fprintf(&b, "## 4. Raw Profiles Saved\n\n")
	fmt.Fprintf(&b, "- `go tool pprof -top %s/hermeshd-cpu.pprof`\n", s.OutDir)
	fmt.Fprintf(&b, "- `go tool pprof -top -sample_index=inuse_space %s/hermeshd-heap.pprof`\n", s.OutDir)
	fmt.Fprintf(&b, "- `go tool trace %s/hermeshd-trace.out`\n", s.OutDir)
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func boolVal(p *bool) string {
	if p == nil {
		return "-"
	}
	if *p {
		return "yes"
	}
	return "no"
}

func sanitizeFileTag(s string) string {
	if s == "" {
		return "page"
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "page"
	}
	return b.String()
}

func round2f(v float64) float64 {
	return math.Round(v*100) / 100
}

// startDaemonPerfLog appends a lightweight in-process telemetry heartbeat every
// 60s to <dataDir>/logs/daemon-perf.jsonl (rotated at 10 MB) and automatically
// saves a 5s CPU + heap pprof bundle when sustained high daemon CPU/allocs occur.
func startDaemonPerfLog(ctx context.Context, dataDir string, log *slog.Logger) {
	logDir := filepath.Join(dataDir, "logs")
	logFile := filepath.Join(logDir, "daemon-perf.jsonl")
	snapshotBytes := func() []byte {
		var buf bytes.Buffer
		metrics.Default.WritePrometheus(&buf)
		return buf.Bytes()
	}
	go func() {
		prev := snapshotBytes()
		prevAt := time.Now()
		var lastSpike time.Time
		wait := 10 * time.Second
		for {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case now := <-timer.C:
				wait = 60 * time.Second
				cur := snapshotBytes()
				dt := now.Sub(prevAt).Seconds()
				prevAt = now
				s := summarizeDaemonMetrics(prev, cur, dt)
				prev = cur

				rec := struct {
					At            string  `json:"at"`
					WindowSeconds float64 `json:"windowSeconds"`
					*daemonMetricsSummary
				}{
					At:                   now.UTC().Format(time.RFC3339),
					WindowSeconds:        round2f(dt),
					daemonMetricsSummary: s,
				}
				if b, err := json.Marshal(rec); err == nil {
					_ = os.MkdirAll(logDir, 0o700)
					if st, err := os.Stat(logFile); err == nil && st.Size() > 10<<20 {
						_ = os.Rename(logFile, logFile+".1")
					}
					if f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
						_, _ = f.Write(append(b, '\n'))
						_ = f.Close()
					}
				}

				if dt >= 20 && (s.GoCPUPct >= 15 || s.AllocRateMBPS >= 20) && now.Sub(lastSpike) >= 10*time.Minute {
					lastSpike = now
					spikeDir := filepath.Join(logDir, "auto-daemon-spike-"+now.Format("20060102-150405"))
					if os.MkdirAll(spikeDir, 0o700) == nil {
						log.Info("capturing auto perf profile", "dir", spikeDir, "goCpuPct", s.GoCPUPct, "allocMBPS", s.AllocRateMBPS)
						if cf, err := os.OpenFile(filepath.Join(spikeDir, "cpu.pb.gz"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600); err == nil {
							if pprof.StartCPUProfile(cf) == nil {
								select {
								case <-ctx.Done():
								case <-time.After(5 * time.Second):
								}
								pprof.StopCPUProfile()
							}
							_ = cf.Close()
						}
						if hf, err := os.OpenFile(filepath.Join(spikeDir, "heap.pb.gz"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600); err == nil {
							_ = pprof.WriteHeapProfile(hf)
							_ = hf.Close()
						}
						if gf, err := os.OpenFile(filepath.Join(spikeDir, "goroutine.txt"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600); err == nil {
							if gp := pprof.Lookup("goroutine"); gp != nil {
								_ = gp.WriteTo(gf, 1)
							}
							_ = gf.Close()
						}
					}
				}
			}
		}
	}()
}
