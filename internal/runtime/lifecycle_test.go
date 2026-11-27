package runtime

import (
	"context"
	"os"
	"os/exec"
	stdruntime "runtime"
	"testing"
	"time"
)

func TestTurnStepTrackerStateMachine(t *testing.T) {
	cfg := TurnStepConfig{
		Debounce:          100 * time.Millisecond,
		StreamingQuiet:    30 * time.Second,
		PostToolModelWait: 3 * time.Minute,
	}
	var tr TurnStepTracker
	if tr.Active() {
		t.Fatal("expected initial tracker to be idle")
	}
	if _, ok := tr.NextSettleDelay(false, cfg); ok {
		t.Fatal("idle tracker must not arm settle timer")
	}

	// 1. Model wakes up, calls a tool, and emits step usage BEFORE tool finishes:
	tr.NoteToolActive()
	tr.NoteUsageOrEnd()
	if _, ok := tr.NextSettleDelay(true, cfg); ok {
		t.Fatal("open foreground tool must block settle timer even after step usage")
	}

	// 2. Tool completes: tracker MUST enter StepAwaitingPostToolModel (3m wait, NOT 6s or 100ms debounce!):
	tr.NoteToolCompleted()
	if tr.Phase() != StepAwaitingPostToolModel {
		t.Fatalf("phase after NoteToolCompleted = %v, want StepAwaitingPostToolModel", tr.Phase())
	}
	if d, ok := tr.NextSettleDelay(false, cfg); !ok || d != 3*time.Minute {
		t.Fatalf("NextSettleDelay after tool completion = (%v, %v), want (3m, true)", d, ok)
	}

	// 3. Model begins streaming final report:
	tr.NoteReplyChunk()
	if !tr.SawReply() || tr.Phase() != StepStreamingReply {
		t.Fatalf("expected StepStreamingReply with SawReply=true, got phase=%v sawReply=%v", tr.Phase(), tr.SawReply())
	}
	if d, ok := tr.NextSettleDelay(false, cfg); !ok || d != 30*time.Second {
		t.Fatalf("NextSettleDelay while streaming reply = (%v, %v), want (30s, true)", d, ok)
	}

	// 4. Final usage_update arrives: settles with short debounce!
	tr.NoteUsageOrEnd()
	if d, ok := tr.NextSettleDelay(false, cfg); !ok || d != 100*time.Millisecond {
		t.Fatalf("NextSettleDelay after final usage = (%v, %v), want (100ms, true)", d, ok)
	}

	tr.Reset()
	if tr.Active() || tr.SawReply() {
		t.Fatal("Reset must return tracker to idle state")
	}
}

func TestIsDaemonCommand(t *testing.T) {
	daemons := [][2]string{
		{"npm run dev", ""},
		{"pnpm dev --port 4000", ""},
		{"yarn start", ""},
		{"bun run preview", ""},
		{"npx vite --host", ""},
		{"next dev -p 3001", ""},
		{"uvicorn app.main:app --reload", ""},
		{"python3 -m http.server 8080", ""},
		{"tsc --watch", ""},
		{"tail -f /tmp/build.log", ""},
		{"docker compose up", ""},
		{"./bin/custom-server", "Start local dev server"},
	}
	for _, tc := range daemons {
		if !IsDaemonCommand(tc[0], tc[1]) {
			t.Errorf("IsDaemonCommand(%q, %q) = false, want true", tc[0], tc[1])
		}
	}

	awaited := [][2]string{
		{"xcodebuild test -scheme Hermesh", "Run unit tests"},
		{"pytest -n auto", "Parallel pytest"},
		{"go test -race ./...", "Run race suite"},
		{"cargo test --workspace", ""},
		{"pnpm test:ci", ""},
		{"npm run build", ""},
		{"npx start-server-and-test dev http://localhost:3000 test", ""},
		{"docker compose up -d", ""},
		{"docker compose up --abort-on-container-exit", ""},
		{"sleep 5 && echo done", ""},
	}
	for _, tc := range awaited {
		if IsDaemonCommand(tc[0], tc[1]) {
			t.Errorf("IsDaemonCommand(%q, %q) = true, want false (awaited)", tc[0], tc[1])
		}
	}
}

func TestProcBaselineAndActiveDescendants(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("process table descendant tracking is Unix-only")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found")
	}
	// Start a shell reading commands from stdin; spawn one long-lived baseline sidecar first,
	// snapshot baseline, then spawn a transient tool child and verify HasActiveDescendants tracks it!
	p, err := StartProc(context.Background(), sh, nil, t.TempDir(), os.Environ())
	if err != nil {
		t.Fatalf("StartProc: %v", err)
	}
	defer p.Stop(500 * time.Millisecond)

	ready := make(chan string, 4)
	go p.Lines(func(b []byte) {
		ready <- string(b)
	})

	// 1. Spawn standing MCP-like sidecar child and wait until ready:
	if err := p.WriteLine([]byte("sleep 30 &\necho sidecar_started")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for sidecar_started")
	}
	if !p.HasActiveDescendants() {
		t.Fatal("expected sidecar child to appear before SnapshotBaseline")
	}

	// 2. Snapshot baseline: sidecar must now be ignored!
	p.SnapshotBaseline()
	if p.HasActiveDescendants() {
		t.Fatal("expected HasActiveDescendants=false immediately after SnapshotBaseline")
	}

	// 3. Spawn a foreground tool command (sleep) and verify HasActiveDescendants=true until it exits:
	if err := p.WriteLine([]byte("sleep 0.35 &\ntool_pid=$!\necho tool_started\nwait $tool_pid\necho tool_finished")); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-ready:
		if msg != "tool_started" {
			t.Fatalf("unexpected line %q", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for tool_started")
	}
	if !p.HasActiveDescendants() {
		t.Fatal("expected HasActiveDescendants=true while tool child is running")
	}

	select {
	case msg := <-ready:
		if msg != "tool_finished" {
			t.Fatalf("unexpected line %q", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for tool_finished")
	}
	if p.HasActiveDescendants() {
		t.Fatal("expected HasActiveDescendants=false after tool child exited (sidecar still ignored)")
	}
}
