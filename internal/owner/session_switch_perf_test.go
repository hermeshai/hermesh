package owner

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1/machinev1connect"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/session/v1/sessionv1connect"
	"github.com/hermeshai/hermesh/internal/kernel"
	kstore "github.com/hermeshai/hermesh/internal/kernel/store"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

func startMeteredLatencyProxy(t *testing.T, targetAddr string, oneWayDelay time.Duration, rxBytes, txBytes *atomic.Int64) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	pipe := func(dst, src net.Conn, counter *atomic.Int64) {
		defer dst.Close()
		buf := make([]byte, 32*1024)
		for {
			n, rErr := src.Read(buf)
			if n > 0 {
				if counter != nil {
					counter.Add(int64(n))
				}
				chunk := append([]byte(nil), buf[:n]...)
				if oneWayDelay > 0 {
					time.Sleep(oneWayDelay)
				}
				if _, wErr := dst.Write(chunk); wErr != nil {
					return
				}
			}
			if rErr != nil {
				if rErr != io.EOF {
					_ = src.Close()
				}
				return
			}
		}
	}

	go func() {
		for {
			c, aErr := ln.Accept()
			if aErr != nil {
				return
			}
			up, dErr := net.Dial("tcp", targetAddr)
			if dErr != nil {
				_ = c.Close()
				continue
			}
			// c is client (iPhone Owner), up is server (Relay):
			// up -> c is RX to iPhone; c -> up is TX from iPhone.
			go pipe(up, c, txBytes)
			go pipe(c, up, rxBytes)
		}
	}()
	return "http://" + ln.Addr().String()
}

// TestSessionSwitchE2EPerf measures full-stack iPhone Owner <-> WAN Relay (160ms RTT) <-> Mac Machine Daemon
// performance across:
//  1. Concurrent multi-turn ResolveFiles storm with realistic inline-code candidates (git subprocesses + wall latency)
//  2. Cold session open (GetSession wire bytes + latency + SQLite cache blocking + SubscribeSession 120ms race)
//  3. Warm session switch (cachedOnly GetSession + SubscribeSession incremental catch-up wire bytes + redundant RPCs)
//  4. On-demand tool input/output/diff expansion via DownloadBlob
func TestSessionSwitchE2EPerf(t *testing.T) {
	dir := t.TempDir()

	// Install a transparent git wrapper on PATH to count exact git subprocesses spawned by hermeshd.
	realGitBytes, err := exec.Command("which", "git").Output()
	if err != nil {
		t.Fatal(err)
	}
	realGit := strings.TrimSpace(string(realGitBytes))
	gitLog := filepath.Join(dir, "git-invocations.log")
	gitBinDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(gitBinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wrapper := fmt.Sprintf("#!/bin/sh\necho x >> %q\nexec %q \"$@\"\n", gitLog, realGit)
	if err := os.WriteFile(filepath.Join(gitBinDir, "git"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	countGitCalls := func() int {
		b, err := os.ReadFile(gitLog)
		if err != nil {
			return 0
		}
		return strings.Count(string(b), "\n")
	}

	// Build a realistic git repository before installing the PATH wrapper so setup git calls aren't counted.
	repo := filepath.Join(dir, "repo")
	for _, sub := range []string{"docs", "internal/kernel", "internal/owner", "apps/ios/Views"} {
		if err := os.MkdirAll(filepath.Join(repo, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	repoFiles := map[string]string{
		"README.md":                        "# Hermesh\n",
		"docs/Architecture.md":             "## Architecture\n",
		"internal/kernel/files.go":         "package kernel\n",
		"internal/owner/sessions.go":       "package owner\n",
		"apps/ios/Views/SessionPage.swift": "import SwiftUI\n",
	}
	for rel, body := range repoFiles {
		if err := os.WriteFile(filepath.Join(repo, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "perf@example.com"},
		{"config", "user.name", "Perf"},
		{"add", "."},
		{"commit", "-m", "initial"},
	} {
		if out, err := exec.Command(realGit, append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	t.Setenv("PATH", gitBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Stand up Mac Machine Engine + deterministic 160ms-RTT Relay Tunnel + iPhone Owner Daemon.
	ids := makeTestIdentities(t)
	ownerID := ids.ownerID
	machID := ids.machineID
	trust := identity.NewTrust(ownerID)
	if _, err := trust.AcceptRootChain(ids.chain); err != nil {
		t.Fatal(err)
	}

	mach, err := machine.Open(machine.Config{DataDir: filepath.Join(dir, "machine"), WorkspaceMode: "none"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mach.Close() })
	eng := mach.Sessions()
	if err := eng.SyncWorkspace("hermesh", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil); err != nil {
		t.Fatal(err)
	}

	var remoteGetSessionCalls atomic.Int32
	sessionHandler := kernel.NewService(eng, func(context.Context) *kernel.Principal {
		return kernel.OwnerPrincipal(ownerID, "Owner")
	})
	_, rawHandler := sessionv1connect.NewSessionServiceHandler(sessionHandler)
	countedSessionHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/GetSession") {
			remoteGetSessionCalls.Add(1)
		}
		rawHandler.ServeHTTP(w, r)
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, aErr := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"hermesh.v1"}})
		if aErr != nil {
			return
		}
		conn := transport.NetConnFromWS(ws)
		defer conn.Close()
		_ = transport.WriteFrame(conn, &relayv1.Challenge{Nonce: make([]byte, 32), RelayId: "relay"})
		var hello relayv1.Hello
		if transport.ReadFrame(conn, &hello) != nil {
			return
		}
		_ = transport.WriteFrame(conn, &relayv1.HelloResult{Ok: true})
		ymx, yErr := yamux.Server(conn, transport.YamuxConfig())
		if yErr != nil {
			return
		}
		defer ymx.Close()
		for {
			st, sErr := ymx.AcceptStream()
			if sErr != nil {
				return
			}
			go func() {
				defer st.Close()
				var h relayv1.StreamHeader
				if transport.ReadFrame(st, &h) != nil || h.Kind != relayv1.StreamKind_STREAM_KIND_TUNNEL {
					return
				}
				tc, _, hErr := transport.ServerHandshake(context.Background(), st, ids.machineKey.CryptoSigner(), ids.machineCreds, trust, func(*identity.Subject) error { return nil })
				if hErr != nil {
					return
				}
				defer tc.Close()
				mux := http.NewServeMux()
				mux.Handle(machinev1connect.NewMachineServiceHandler(stubMachineInfo{}))
				mux.Handle("/hermesh.session.v1.SessionService/", countedSessionHandler)
				transport.ServeH2(context.Background(), tc, mux)
			}()
		}
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Seed heavy Session ("优化侧边栏 / 上下文压缩" scale: 14 runs with git checkpoints, 560 items, ~2.2 MB raw projection).
	sid := "s_" + kernel.SessionPrefix(machID) + "_01jswitchperfe2e000000001"
	var checkpoints []*sessionv1.Checkpoint
	for r := 1; r <= 14; r++ {
		_ = os.WriteFile(filepath.Join(repo, "docs", "Architecture.md"), []byte(fmt.Sprintf("## Architecture Turn %d\n", r)), 0o644)
		cp, cErr := eng.VCS().CaptureCheckpoint(ctx, repo, fmt.Sprintf("cp_%d", r), sid, fmt.Sprintf("run_%d", r), uint32(r), "")
		if cErr != nil {
			t.Fatalf("CaptureCheckpoint %d: %v", r, cErr)
		}
		checkpoints = append(checkpoints, cp)
	}

	padIn := strings.Repeat("{\"context\":\"tool_input_argument_line\"}\n", 45)                   // ~1.75 KB
	padOut := strings.Repeat("tool_execution_output_line_0123456789abcdef\n", 65)                 // ~2.9 KB
	padDiff := strings.Repeat("+added_optimized_implementation_line_012345\n", 55)                // ~2.4 KB
	padReason := strings.Repeat("Evaluating context compaction invariant and cache reuse. ", 180) // ~10.2 KB

	var seq uint64
	var sampleCmdID, sampleEditID string
	sess := &sessionv1.Session{
		SessionId: sid,
		MachineId: machID,
		ProjectId: "hermesh",
		Title:     "Optimize Her Context Compression",
		Status:    sessionv1.SessionStatus_SESSION_STATUS_IDLE,
		CreatedBy: ownerID,
		UpdatedAt: timestamppb.Now(),
	}

	if _, err := eng.Store().Mutate(func(tx *kstore.Tx) error {
		for r := 1; r <= 14; r++ {
			runID := fmt.Sprintf("run_%d", r)
			if err := tx.PutRun(&sessionv1.Run{
				RunId:        runID,
				SessionId:    sid,
				Ordinal:      uint32(r),
				Status:       sessionv1.RunStatus_RUN_STATUS_COMPLETED,
				CheckpointId: fmt.Sprintf("cp_%d", r),
				Prompt:       fmt.Sprintf("Turn %d prompt", r),
			}); err != nil {
				return err
			}
			if err := tx.PutCheckpoint(checkpoints[r-1]); err != nil {
				return err
			}
			seq++
			if err := tx.PutTurnItem(&sessionv1.TurnItem{
				ItemId: fmt.Sprintf("u_%d", r), SessionId: sid, RunId: runID, Seq: seq,
				Kind: sessionv1.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE, Status: "completed",
				Text: fmt.Sprintf("Analyze turn %d bottlenecks", r),
			}); err != nil {
				return err
			}
			seq++
			if err := tx.PutTurnItem(&sessionv1.TurnItem{
				ItemId: fmt.Sprintf("th_%d", r), SessionId: sid, RunId: runID, Seq: seq,
				Kind: sessionv1.TurnItemKind_TURN_ITEM_KIND_REASONING, Status: "completed",
				Text: padReason,
			}); err != nil {
				return err
			}
			for k := 0; k < 36; k++ {
				seq++
				itemID := fmt.Sprintf("tool_%d_%d", r, k)
				it := &sessionv1.TurnItem{
					ItemId:    itemID,
					SessionId: sid,
					RunId:     runID,
					Seq:       seq,
					Status:    "completed",
				}
				if k%6 == 5 {
					it.Kind = sessionv1.TurnItemKind_TURN_ITEM_KIND_FILE_CHANGE
					it.ToolName = "Edit"
					it.FilePath = "internal/kernel/files.go"
					it.Diff = fmt.Sprintf("@@ -%d,4 +%d,8 @@\n%s", k+1, k+1, padDiff)
					it.Additions = 55
					sampleEditID = itemID
				} else if k%2 == 0 {
					it.Kind = sessionv1.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION
					it.ToolName = "Bash"
					it.Command = fmt.Sprintf("go test ./internal/pkg%d/...", k)
					it.Input = fmt.Sprintf("{\"command\":\"go test %d_%d\"}\n%s", r, k, padIn)
					it.InputSize = uint64(len(it.Input))
					it.Output = fmt.Sprintf("PASS %d_%d\n%s", r, k, padOut)
					it.OutputSize = uint64(len(it.Output))
					sampleCmdID = itemID
				} else {
					it.Kind = sessionv1.TurnItemKind_TURN_ITEM_KIND_TOOL_CALL
					it.ToolName = "Read"
					it.FilePath = "internal/owner/sessions.go"
					it.Input = fmt.Sprintf("{\"path\":\"internal/owner/sessions.go\",\"part\":%d}\n%s", k, padIn)
					it.InputSize = uint64(len(it.Input))
					it.Output = fmt.Sprintf("// File slice %d_%d\n%s", r, k, padOut)
					it.OutputSize = uint64(len(it.Output))
				}
				if err := tx.PutTurnItem(it); err != nil {
					return err
				}
			}
			seq++
			if err := tx.PutTurnItem(&sessionv1.TurnItem{
				ItemId: fmt.Sprintf("a_%d", r), SessionId: sid, RunId: runID, Seq: seq,
				Kind: sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE, Status: "completed",
				Text: strings.Repeat(fmt.Sprintf("Detailed Markdown reply for turn %d referencing `internal/kernel/files.go` and `session.status`. ", r), 40),
			}); err != nil {
				return err
			}
		}
		sess.Progress = &sessionv1.SessionProgress{TurnCount: 14, LastEventSeq: seq}
		if err := tx.PutSession(sess); err != nil {
			return err
		}
		return tx.AppendEvent(&sessionv1.SessionEvent{
			SessionId: sid,
			Type:      "session.updated",
			Session:   sess,
		})
	}); err != nil {
		t.Fatal(err)
	}
	if fullProj, err := eng.GetSession(ctx, kernel.OwnerPrincipal(ownerID, "Owner"), sid); err == nil {
		sess = fullProj.Session
	}

	var rxBytes, txBytes atomic.Int64
	proxyURL := startMeteredLatencyProxy(t, strings.TrimPrefix(srv.URL, "http://"), 80*time.Millisecond, &rxBytes, &txBytes)

	d, err := Open(Config{DataDir: filepath.Join(dir, "iphone"), NoLocalMachine: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	o := d.owner()
	if err := o.saveIdentity(ownerID, proxyURL, ids.rootPub, ids.chain, ids.device, ids.deviceCert, ""); err != nil {
		t.Fatal(err)
	}
	o.mu.Lock()
	o.seen = machID
	o.mu.Unlock()
	// Seed session header (as WatchSessions does) without projection_payload, and warm the E2E tunnel first.
	o.cacheSession(sess, nil)
	ownerSrv := httptest.NewServer(d.LocalHandler())
	t.Cleanup(ownerSrv.Close)
	sc := sessionv1connect.NewSessionServiceClient(ownerSrv.Client(), ownerSrv.URL)

	for range 40 {
		if _, err := sc.ListCheckpoints(ctx, &sessionv1.ListCheckpointsRequest{SessionId: sid}); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// --- Scenario 1: Cold Open Session (GetSession + SubscribeSession) ---
	rxBytes.Store(0)
	txBytes.Store(0)
	remoteGetSessionCalls.Store(0)
	tCold := time.Now()
	gotRes, err := sc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: sid, Compact: true})
	if err != nil {
		t.Fatalf("Cold GetSession: %v", err)
	}
	coldGetMs := time.Since(tCold).Milliseconds()
	projBytes, _ := proto.Marshal(gotRes.GetProjection())
	coldGetRX := rxBytes.Load()
	coldGetCalls := remoteGetSessionCalls.Load()

	// SubscribeSession immediately after GetSession (as SessionPage.followSession does).
	rxBytes.Store(0)
	remoteGetSessionCalls.Store(0)
	subCtx1, subCancel1 := context.WithCancel(ctx)
	tSub1 := time.Now()
	subStream1, err := sc.SubscribeSession(subCtx1, &sessionv1.SubscribeSessionRequest{
		SessionId: sid,
		AfterSeq:  gotRes.GetProjection().GetSession().GetProgress().GetLastEventSeq(),
	})
	if err != nil {
		t.Fatalf("SubscribeSession #1 dial: %v", err)
	}
	if !subStream1.Receive() {
		t.Fatalf("SubscribeSession #1 receive: %v", subStream1.Err())
	}
	subReady1Ms := time.Since(tSub1).Milliseconds()
	// Wait 450ms to capture any trailing in-flight redundant 120ms GetSession payload arriving over 160ms RTT.
	time.Sleep(450 * time.Millisecond)
	sub1ExtraRX := rxBytes.Load()
	sub1DupGets := remoteGetSessionCalls.Load()
	subCancel1()
	_ = subStream1.Close()

	// --- Scenario 2: 14 Concurrent ResolveFiles RPCs with inline-code candidates (as MarkdownText fires on open) ---
	_ = os.Remove(gitLog)
	rxBytes.Store(0)
	var candidatePaths []string
	candidatePaths = append(candidatePaths, "internal/kernel/files.go", "internal/owner", "docs/Architecture.md", "/etc/hosts")
	for i := 0; i < 60; i++ {
		candidatePaths = append(candidatePaths, fmt.Sprintf("session.prop_%d", i), fmt.Sprintf("pkg%d/missing_%d", i%8, i))
	}
	tResolve := time.Now()
	var wg sync.WaitGroup
	errCh := make(chan error, 14)
	for r := 1; r <= 14; r++ {
		wg.Add(1)
		go func(ord int) {
			defer wg.Done()
			res, rErr := sc.ResolveFiles(ctx, &sessionv1.ResolveFilesRequest{
				SessionId: sid,
				RunId:     fmt.Sprintf("run_%d", ord),
				Paths:     candidatePaths,
			})
			if rErr != nil {
				errCh <- rErr
				return
			}
			if len(res.GetFiles()) != len(candidatePaths) ||
				res.Files[0].Kind != sessionv1.FileRefKind_FILE_REF_KIND_FILE ||
				res.Files[1].Kind != sessionv1.FileRefKind_FILE_REF_KIND_FOLDER ||
				res.Files[3].Kind != sessionv1.FileRefKind_FILE_REF_KIND_OUTSIDE ||
				res.Files[4].Kind != sessionv1.FileRefKind_FILE_REF_KIND_MISSING {
				errCh <- fmt.Errorf("unexpected ResolveFiles kinds: %+v", res.Files[:5])
			}
		}(r)
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Fatalf("ResolveFiles: %v", e)
	}
	resolveWallMs := time.Since(tResolve).Milliseconds()
	gitSubprocesses := countGitCalls()

	// --- Scenario 3: Warm Switch Back (cachedOnly GetSession + SubscribeSession(afterSeq)) ---
	rxBytes.Store(0)
	remoteGetSessionCalls.Store(0)
	tWarm := time.Now()
	warmRes, err := sc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: sid, CachedOnly: true, Compact: true})
	if err != nil || warmRes.GetProjection() == nil {
		t.Fatalf("Warm GetSession(cachedOnly): %v %+v", err, warmRes)
	}
	warmCacheMs := time.Since(tWarm).Milliseconds()

	subCtx2, subCancel2 := context.WithCancel(ctx)
	tSub2 := time.Now()
	subStream2, err := sc.SubscribeSession(subCtx2, &sessionv1.SubscribeSessionRequest{
		SessionId: sid,
		AfterSeq:  warmRes.GetProjection().GetSession().GetProgress().GetLastEventSeq(),
	})
	if err != nil {
		t.Fatalf("SubscribeSession #2 dial: %v", err)
	}
	if !subStream2.Receive() {
		t.Fatalf("SubscribeSession #2 receive: %v", subStream2.Err())
	}
	warmSubMs := time.Since(tSub2).Milliseconds()
	time.Sleep(450 * time.Millisecond)
	warmSubRX := rxBytes.Load()
	warmDupGets := remoteGetSessionCalls.Load()
	subCancel2()
	_ = subStream2.Close()

	// --- Scenario 4: On-Demand Tool Input/Output/Diff via DownloadBlob (Online + Offline Cache) ---
	var gotCmdItem, gotEditItem *sessionv1.TurnItem
	for _, it := range gotRes.GetProjection().GetItems() {
		if it.GetItemId() == sampleCmdID {
			gotCmdItem = it
		} else if it.GetItemId() == sampleEditID {
			gotEditItem = it
		}
	}
	if gotCmdItem == nil || gotCmdItem.Input != "" || gotCmdItem.Output != "" || gotCmdItem.InputBlobSha256 == "" || gotCmdItem.OutputBlobSha256 == "" {
		t.Fatalf("expected compacted command TurnItem with virtual blob keys, got %+v", gotCmdItem)
	}
	if gotEditItem == nil || gotEditItem.Diff != "" || gotEditItem.OutputBlobSha256 == "" {
		t.Fatalf("expected compacted edit TurnItem with virtual diff blob key, got %+v", gotEditItem)
	}
	tExpand := time.Now()
	inBlob, err := sc.DownloadBlob(ctx, &sessionv1.DownloadBlobRequest{SessionId: sid, BlobSha256: gotCmdItem.InputBlobSha256})
	if err != nil || !strings.Contains(string(inBlob.GetData()), `"command":"go test`) {
		t.Fatalf("DownloadBlob(cmd input): err=%v len=%d", err, len(inBlob.GetData()))
	}
	outBlob, err := sc.DownloadBlob(ctx, &sessionv1.DownloadBlobRequest{SessionId: sid, BlobSha256: gotCmdItem.OutputBlobSha256})
	if err != nil || !strings.Contains(string(outBlob.GetData()), "PASS ") {
		t.Fatalf("DownloadBlob(cmd output): err=%v len=%d", err, len(outBlob.GetData()))
	}
	diffBlob, err := sc.DownloadBlob(ctx, &sessionv1.DownloadBlobRequest{SessionId: sid, BlobSha256: gotEditItem.OutputBlobSha256})
	if err != nil || !strings.Contains(string(diffBlob.GetData()), "+added_optimized_implementation_line") {
		t.Fatalf("DownloadBlob(edit diff): err=%v len=%d", err, len(diffBlob.GetData()))
	}
	expand3BlobsMs := time.Since(tExpand).Milliseconds()

	// Shut down the relay server and verify previously opened tool details are served offline from sessions_blobs!
	srv.Close()
	offOut, err := sc.DownloadBlob(ctx, &sessionv1.DownloadBlobRequest{SessionId: sid, BlobSha256: gotCmdItem.OutputBlobSha256})
	if err != nil || string(offOut.GetData()) != string(outBlob.GetData()) {
		t.Fatalf("Offline DownloadBlob(cmd output): err=%v len=%d", err, len(offOut.GetData()))
	}

	_ = coldGetCalls

	t.Logf("E2E_SWITCH_METRICS | coldGetMs=%d projKB=%.1f coldGetRX_KB=%.1f sub1Ms=%d sub1DupGets=%d sub1ExtraRX_KB=%.1f | resolve14RunsMs=%d gitProcs=%d | warmCacheMs=%d warmSubMs=%d warmDupGets=%d warmSubRX_KB=%.1f | expand3BlobsMs=%d",
		coldGetMs, float64(len(projBytes))/1024.0, float64(coldGetRX)/1024.0,
		subReady1Ms, sub1DupGets, float64(sub1ExtraRX)/1024.0,
		resolveWallMs, gitSubprocesses,
		warmCacheMs, warmSubMs, warmDupGets, float64(warmSubRX)/1024.0,
		expand3BlobsMs,
	)
}
