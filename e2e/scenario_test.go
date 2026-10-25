package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// TestUserJourney walks the P1 acceptance scenarios with real binaries:
// relay, the owner daemon (driven like Hermesh.app over its Unix socket),
// a separate execution machine and the agent CLI.
//
// By default the coding agents are scripted fakes. With HERMESH_REAL_CLI=1
// the machines run this computer's real Claude Code and Codex.
func TestUserJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	real := os.Getenv("HERMESH_REAL_CLI") == "1"
	bin := binaries(t)
	relayBin, daemonBin, cliBin := filepath.Join(bin, "hermesh-relay"), filepath.Join(bin, "hermeshd"), filepath.Join(bin, "hermesh")
	tmp := t.TempDir()
	sockDir, _ := os.MkdirTemp("/tmp", "hm")
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	var agentEnv []string
	if !real {
		agentEnv = []string{"HERMESH_FAKE_PROVIDERS=1"}
	} else {
		agentEnv = []string{"PATH=" + os.Getenv("PATH") + ":/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin"}
	}
	ctx := context.Background()
	var logs []*proc
	t.Cleanup(func() {
		if t.Failed() {
			for _, p := range logs {
				t.Logf("==== %s output ====\n%s", p.name, p.output())
			}
		}
	})

	// --- Relay (A1) ---------------------------------------------------------
	port := freePort(t)
	relayURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	relayData := filepath.Join(tmp, "relay")
	relay := start(t, "relay", nil, relayBin, "serve", "--data-dir", relayData, "--listen", fmt.Sprintf("127.0.0.1:%d", port))
	logs = append(logs, relay)
	eventually(t, 20*time.Second, "relay up", func() bool { return strings.Contains(relay.output(), "relay listening") })
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, relayBin, "claim-code", "--data-dir", relayData).stdout)
	if len(claim) != 19 {
		t.Fatalf("claim code %q", claim)
	}

	// --- Owner Mac: hermeshd --owner + the "app" ---------------------------
	ownerData := filepath.Join(tmp, "owner-mac")
	sock := filepath.Join(sockDir, "o.sock")
	startOwner := func(dataDir string) *proc {
		p := start(t, "owner", agentEnv, daemonBin, "run", "--owner", "--data-dir", dataDir, "--socket", sock)
		logs = append(logs, p)
		return p
	}
	ownerProc := startOwner(ownerData)
	root, _ := identity.GenerateKey()
	app := newTestApp(t, sock, root)
	setup, err := app.client.Setup(ctx, &localv1.SetupRequest{RelayUrl: relayURL, ClaimCode: claim, RootPublicKey: rootPub(root), RootLabel: "test-mac", RootPlatform: "macos"})
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, ownerProc.output())
	}
	if setup.OwnerId != identity.OwnerID(rootPub(root)) {
		t.Fatalf("owner %s is not named by the first device's key", setup.OwnerId)
	}
	addr := setup.ConnectAddress
	t.Logf("connect address %s", addr)
	if _, err := app.client.Setup(ctx, &localv1.SetupRequest{RelayUrl: relayURL, ClaimCode: claim, RootPublicKey: rootPub(root)}); err == nil {
		t.Fatal("second setup must fail")
	}

	localMachine, err := app.client.SetLocalMachine(ctx, &localv1.SetLocalMachineRequest{Enabled: true, MachineName: "owner-mac"})
	if err != nil {
		t.Fatalf("local machine: %v\n%s", err, ownerProc.output())
	}

	// --- A2: pair a separate machine with the pairing code ----------------
	n1Data := filepath.Join(tmp, "vps-1")
	pairProc := start(t, "pair", agentEnv, daemonBin, "pair", addr, "--name", "my vps", "--no-service", "--data-dir", n1Data)
	code := pairProc.pairingCode(t)
	details, err := app.client.LookupPairing(ctx, &localv1.LookupPairingRequest{Code: strings.ToLower(code)})
	if err != nil {
		t.Fatal(err)
	}
	if details.Details.Kind != identityv1.PairingKind_PAIRING_KIND_MACHINE || details.Details.Label != "my vps" {
		t.Fatalf("pairing details %+v", details.Details)
	}
	// A self-hosted relay lists it to the signer too.
	if l, err := app.client.ListPairingRequests(ctx, &localv1.ListPairingRequestsRequest{}); err != nil || len(l.Pairings) != 1 || l.Pairings[0].Code != code {
		t.Fatalf("pending requests on the relay: %v %v", l, err)
	}
	if _, err := app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: code, MachineName: "vps-1"}); err != nil {
		t.Fatal(err)
	}
	if c := pairProc.wait(t, 30*time.Second); c != 0 {
		t.Fatalf("pair exited %d:\n%s", c, pairProc.output())
	}
	startMachine1 := func() *proc {
		p := start(t, "vps-1", agentEnv, daemonBin, "run", "--data-dir", n1Data)
		logs = append(logs, p)
		return p
	}
	machine1 := startMachine1()
	var vps1 *localv1.Machine
	eventually(t, 30*time.Second, "both machines online", func() bool {
		res, err := app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err != nil {
			return false
		}
		online := 0
		for _, n := range res.Machines {
			// Online, and calling itself by its name: vps-1 asked as "my vps"
			// and is named at approval, which reaches it once it connects.
			if n.Online && n.Status != nil && n.Status.Name == n.MachineName {
				online++
			}
			if n.MachineName == "vps-1" {
				vps1 = n
			}
		}
		return online == 2
	})
	// The relay knows a machine's name and version, and nothing else of it.
	if vps1.Version == "" {
		t.Fatalf("vps-1 listed without its version: %v", vps1)
	}

	// --- Projects ------------------------------------------------------------
	repo1 := gitRepo(t, map[string]string{"README.md": "# demo on vps\n"})
	repo2 := gitRepo(t, map[string]string{"README.md": "# demo on mac\n"})
	// Projects are settings, not permissions: no Touch ID (nothing signed).
	before := len(app.signedTypes())
	for _, p := range []*localv1.AddProjectRequest{{MachineId: vps1.MachineId, ProjectId: "demo", Path: repo1, Description: "Demo web app (the VPS copy)"},
		{MachineId: localMachine.MachineId, ProjectId: "demo", Path: repo2, Description: "Demo web app"}} {
		if _, err := app.client.AddProject(ctx, p); err != nil {
			t.Fatalf("add project: %v", err)
		}
	}
	if got := app.signedTypes()[before:]; len(got) != 0 {
		t.Fatalf("adding projects signed %v", got)
	}
	if _, err := app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: vps1.MachineId, ProjectId: "bad", Path: tmp, Workspace: identityv1.Workspace_WORKSPACE_WORKTREE}); err == nil {
		t.Fatal("a worktree project without git accepted")
	}
	// In place: a plain directory (the mode is picked by itself), and a git
	// repository the owner wants worked on directly.
	notesDir := t.TempDir()
	os.WriteFile(filepath.Join(notesDir, "notes.md"), []byte("Remember the falcon.\n"), 0o644)
	inPlaceRepo := gitRepo(t, map[string]string{"README.md": "# worked on in place\n"})
	for _, p := range []*localv1.AddProjectRequest{{MachineId: vps1.MachineId, ProjectId: "notes", Path: notesDir, Description: "Plain notes, no git"},
		{MachineId: localMachine.MachineId, ProjectId: "live", Path: inPlaceRepo, Workspace: identityv1.Workspace_WORKSPACE_IN_PLACE}} {
		if _, err := app.client.AddProject(ctx, p); err != nil {
			t.Fatalf("add in-place project: %v", err)
		}
	}
	if ps, err := app.client.ListProjects(ctx, &localv1.ListProjectsRequest{}); err != nil || len(ps.Projects) != 4 {
		t.Fatalf("owner projects: %v %v", ps, err)
	} else {
		for _, p := range ps.Projects {
			if want := p.ProjectId == "notes" || p.ProjectId == "live"; want != (p.Workspace == identityv1.Workspace_WORKSPACE_IN_PLACE) {
				t.Fatalf("project %s is %v", p.ProjectId, p.Workspace)
			}
		}
	}

	// --- A3: agent pairing via `hermesh auth login` ------------------------------
	grokHome := filepath.Join(tmp, "grok")
	grokEnv := []string{"HERMESH_HOME=" + grokHome}
	cli := func(env []string, timeout time.Duration, args ...string) result {
		return run(t, env, timeout, cliBin, args...)
	}
	pairClient := func(env []string, name string, spec *localv1.GrantSpec) {
		login := start(t, "login "+name, env, cliBin, "auth", "login", addr, "--name", name)
		code := login.pairingCode(t)
		if _, err := app.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{Code: code, Spec: spec}); err != nil {
			t.Fatal(err)
		}
		if c := login.wait(t, 30*time.Second); c != 0 {
			t.Fatalf("login exited %d:\n%s", c, login.output())
		}
		if !strings.Contains(login.output(), "Paired") {
			t.Fatalf("login output: %s", login.output())
		}
	}
	pairClient(grokEnv, "grok", &localv1.GrantSpec{})
	if r := cli(grokEnv, 20*time.Second, "auth", "login", addr); !strings.Contains(r.stdout, "Already paired") {
		t.Fatalf("re-login: %+v", r)
	}
	if fi, err := os.Stat(filepath.Join(grokHome, "client.key")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("client key permissions: %v %v", fi, err)
	}

	// The one list an agent picks from: every project as machine/project,
	// with the coding agents it may use there and whether it can start now.
	if _, err := app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: vps1.MachineId, ProjectId: "long", Path: repo1, Description: strings.Repeat("x", 201)}); err == nil {
		t.Fatal("a 201-character description accepted")
	}
	r := cli(grokEnv, 30*time.Second, "project", "list")
	for _, want := range []string{"owner-mac/demo", "vps-1/demo", "vps-1/notes", "owner-mac/live", "Demo web app (the VPS copy)", "Plain notes, no git",
		"claude, codex", "ready", "in place", "worktree from main", runtime.GOOS} {
		if r.code != 0 || !strings.Contains(r.stdout, want) {
			t.Fatalf("projects: no %q in %+v", want, r)
		}
	}
	listed := func(env []string) map[string]map[string]any {
		out := map[string]map[string]any{}
		ps, _ := jsonDoc(t, cli(env, 30*time.Second, "project", "list", "-o", "json"))["projects"].([]any)
		for _, p := range ps {
			p := p.(map[string]any)
			out[p["project"].(string)] = p
		}
		return out
	}
	if ps := listed(grokEnv); len(ps) != 4 || ps["vps-1/notes"]["workspace"] != "in_place" || ps["vps-1/demo"]["machine"] != "vps-1" ||
		ps["owner-mac/demo"]["state"] != "ready" || len(ps["owner-mac/demo"]["agents"].([]any)) != 2 ||
		ps["vps-1/demo"]["description"] != "Demo web app (the VPS copy)" || ps["vps-1/demo"]["path"] != repo1 || ps["vps-1/demo"]["base"] != "main" ||
		ps["vps-1/notes"]["base"] != "" || ps["vps-1/demo"]["platform"] != runtime.GOOS+"/"+runtime.GOARCH ||
		ps["vps-1/demo"]["active_sessions"] != float64(0) || ps["vps-1/demo"]["running_sessions"] != nil {
		t.Fatalf("projects json: %v", ps)
	}
	// A machine's name is a setting: renaming signs nothing, the agent sees
	// the new name (from the machine itself) and the old one is gone.
	renameFrom := len(app.signedTypes())
	if _, err := app.client.RenameMachine(ctx, &localv1.RenameMachineRequest{MachineId: vps1.MachineId, MachineName: "vps-one"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "the agent sees the new name", func() bool { return listed(grokEnv)["vps-one/demo"] != nil })
	// The old name: unknown, or (while the relay's list lags) refused by
	// the machine itself.
	if r := cli(grokEnv, 30*time.Second, "session", "start", "--project", "vps-1/demo", "--prompt", "x"); r.code == 0 {
		t.Fatalf("the old name still resolves: %+v", r)
	}
	if _, err := app.client.RenameMachine(ctx, &localv1.RenameMachineRequest{MachineId: vps1.MachineId, MachineName: "vps-1"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "the name back", func() bool { return listed(grokEnv)["vps-1/demo"] != nil })
	if got := app.signedTypes()[renameFrom:]; len(got) != 0 {
		t.Fatalf("renaming signed %v", got)
	}
	// A name on several machines is picked by its address; the error lists them.
	if r := cli(grokEnv, 30*time.Second, "session", "start", "--project", "demo", "--prompt", "x"); r.code != 2 ||
		!strings.Contains(r.stderr, "owner-mac/demo") || !strings.Contains(r.stderr, "vps-1/demo") {
		t.Fatalf("ambiguous project: %+v", r)
	}
	for _, bad := range []string{"vps-9/demo", "vps-1/nope", "nope"} {
		if r := cli(grokEnv, 30*time.Second, "session", "start", "--project", bad, "--prompt", "x"); r.code != 2 || !strings.Contains(r.stderr, "hermesh project list") {
			t.Fatalf("start --project %s: %+v", bad, r)
		}
	}
	if r := cli(grokEnv, 30*time.Second, "projects"); r.code != 2 {
		t.Fatalf("projects is now project list: %+v", r)
	}
	if r := cli(grokEnv, 30*time.Second, "machines"); r.code != 2 {
		t.Fatalf("machines is gone: %+v", r)
	}
	if r := cli(grokEnv, 30*time.Second, "session", "start", "--machine", "vps-1", "--project", "demo", "--prompt", "x"); r.code != 2 {
		t.Fatalf("--machine is gone: %+v", r)
	}
	// A paused machine's projects say so.
	if _, err := app.client.SetMachinePaused(ctx, &localv1.SetMachinePausedRequest{MachineId: localMachine.MachineId, Paused: true}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "paused machine listed", func() bool { return listed(grokEnv)["owner-mac/demo"]["state"] == "paused" })
	if r := cli(grokEnv, 30*time.Second, "project", "list"); !strings.Contains(r.stdout, "paused") {
		t.Fatalf("projects, paused: %+v", r)
	}
	if _, err := app.client.SetMachinePaused(ctx, &localv1.SetMachinePausedRequest{MachineId: localMachine.MachineId, Paused: false}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "machine resumed", func() bool { return listed(grokEnv)["owner-mac/demo"]["state"] == "ready" })

	// --- A3/A4: sessions with Claude (vps-1) and Codex (owner Mac) ---------
	prompt := func(script, natural string) string {
		if real {
			return natural
		}
		return script
	}
	model := []string{}
	if real {
		model = []string{"--model", "haiku"}
	}
	start1 := cli(grokEnv, 6*time.Minute, append([]string{"session", "start", "--project", "vps-1/demo", "--agent", "claude", "--wait", "-o", "json",
		"--idempotency-key", "e2e-s1", "--prompt", prompt("say working\nwrite hello.txt hello from hermesh", "Create a file hello.txt containing exactly: hello from hermesh. Then reply: done")}, model...)...)
	if start1.code != 0 {
		t.Fatalf("claude session: %+v", start1)
	}
	var s1 struct {
		SessionID string `json:"session_id"`
		Status    string `json:"status"`
	}
	unmarshalSession(start1.stdout, &s1)
	if s1.Status != "idle" || !strings.HasPrefix(s1.SessionID, "s_") {
		t.Fatalf("session after wait: %s", start1.stdout)
	}
	diff := cli(grokEnv, 30*time.Second, "session", "diff", s1.SessionID)
	if diff.code != 0 || !strings.Contains(diff.stdout, "hello.txt") || !strings.Contains(diff.stdout, "+hello from hermesh") {
		t.Fatalf("diff: %+v", diff)
	}
	if b, _ := os.ReadFile(filepath.Join(repo1, "hello.txt")); len(b) != 0 {
		t.Fatal("session wrote into the owner's working tree")
	}

	// A5: follow-up turn keeps context.
	send := cli(grokEnv, 6*time.Minute, "session", "send", s1.SessionID, "--wait", "--message",
		prompt("say second turn\nwrite second.txt two", "Create second.txt containing: two. Then tell me the name of the first file you created, nothing else."))
	if send.code != 0 {
		t.Fatalf("send: %+v", send)
	}
	if real && !strings.Contains(send.stdout, "hello.txt") {
		t.Fatalf("second turn lost context: %+v", send)
	}
	if d := cli(grokEnv, 30*time.Second, "session", "diff", s1.SessionID, "--turn", "2"); !strings.Contains(d.stdout, "second.txt") || strings.Contains(d.stdout, "hello.txt") {
		t.Fatalf("turn 2 diff: %+v", d)
	}

	codex := cli(grokEnv, 8*time.Minute, "session", "start", "--project", "owner-mac/demo", "--agent", "codex", "--wait", "-o", "json",
		"--prompt", prompt("write codex.txt from codex\nsay done", "Create a file codex.txt containing exactly: from codex. Then reply: done"))
	if codex.code != 0 {
		t.Fatalf("codex session: %+v", codex)
	}
	var s2 struct {
		SessionID string `json:"session_id"`
	}
	unmarshalSession(codex.stdout, &s2)
	if d := cli(grokEnv, 30*time.Second, "session", "diff", s2.SessionID); !strings.Contains(d.stdout, "codex.txt") {
		t.Fatalf("codex diff: %+v", d)
	}

	// A5: a message sent while the agent works joins the running turn: the
	// caller gets the answer to it, and the session still has one turn.
	for _, c := range []struct {
		machine, agent string
		model          []string
	}{{"vps-1", "claude", model}, {"owner-mac", "codex", nil}} {
		busy := cli(grokEnv, 2*time.Minute, append([]string{"session", "start", "--project", c.machine + "/demo", "--agent", c.agent, "-o", "json",
			"--prompt", prompt("say working\ntool-sleep 3s\nsay first", "Run the shell command `sleep 20`, then reply: first")}, c.model...)...)
		var sb struct {
			SessionID string `json:"session_id"`
		}
		unmarshalSession(busy.stdout, &sb)
		if busy.code != 0 || sb.SessionID == "" {
			t.Fatalf("%s busy session: %+v", c.agent, busy)
		}
		for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(500 * time.Millisecond) {
			g := cli(grokEnv, 30*time.Second, "session", "get", sb.SessionID, "-o", "json")
			var now struct {
				CurrentTool string `json:"current_tool"`
			}
			if unmarshalSession(g.stdout, &now) == nil && strings.Contains(now.CurrentTool, "sleep") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s never ran the tool: %s\n%s", c.agent, g.stdout, cli(grokEnv, 30*time.Second, "session", "events", sb.SessionID, "--limit", "1000").stdout)
			}
		}
		steer := cli(grokEnv, 6*time.Minute, "session", "send", sb.SessionID, "--wait", "--message",
			prompt("write steer.txt steered\nsay BANANA", "Additional instruction: also create a file steer.txt containing exactly: steered. End your final reply with the word BANANA."))
		if steer.code != 0 || !strings.Contains(steer.stdout, "BANANA") {
			t.Fatalf("%s steer: %+v", c.agent, steer)
		}
		g := cli(grokEnv, 30*time.Second, "session", "get", sb.SessionID, "-o", "json")
		var after struct {
			Status string `json:"status"`
			Turns  int    `json:"turns"`
		}
		if unmarshalSession(g.stdout, &after) != nil || after.Status != "idle" || after.Turns != 1 {
			t.Fatalf("%s after the steer: %s", c.agent, g.stdout)
		}
		if d := cli(grokEnv, 30*time.Second, "session", "diff", sb.SessionID); !strings.Contains(d.stdout, "+steered") {
			t.Fatalf("%s steer diff: %+v", c.agent, d)
		}
	}

	// Files: an image the agent looks at and a text file it reads, sent
	// with the CLI's --file.
	green := filepath.Join(tmp, "green.png")
	notes := filepath.Join(tmp, "notes.txt")
	writePNG(t, green, color.RGBA{0, 0x80, 0, 0xff})
	os.WriteFile(notes, []byte("The secret number is 4217.\n"), 0o644)
	for _, c := range []struct {
		machine, agent string
		model          []string
	}{{"vps-1", "claude", model}, {"owner-mac", "codex", nil}} {
		r := cli(grokEnv, 6*time.Minute, append([]string{"session", "start", "--project", c.machine + "/demo", "--agent", c.agent, "--wait",
			"-f", green, "--file", notes, "--prompt", prompt("images", "What single color fills the attached image? Also read the attached notes.txt. Reply with only: <color> <secret number>")}, c.model...)...)
		if r.code != 0 {
			t.Fatalf("%s with files: %+v", c.agent, r)
		}
		got := strings.ToLower(r.stdout)
		if (!real && !strings.Contains(got, "images: green.png")) || (real && (!strings.Contains(got, "green") || !strings.Contains(got, "4217"))) {
			t.Fatalf("%s did not see the files: %+v", c.agent, r)
		}
		id := sessionIDOf(r.stderr)
		logs := cli(grokEnv, 30*time.Second, "session", "events", id, "--limit", "1000")
		if !strings.Contains(logs.stdout, "attached: green.png (image/png") || !strings.Contains(logs.stdout, "attached: notes.txt (text/plain") {
			t.Fatalf("%s logs: %+v", c.agent, logs)
		}
		if d := cli(grokEnv, 30*time.Second, "session", "diff", id); strings.Contains(d.stdout, "notes.txt") {
			t.Fatalf("%s: attachments leaked into the diff: %s", c.agent, d.stdout)
		}
		// A follow-up with only a file.
		blue := filepath.Join(tmp, c.agent+"-blue.png")
		writePNG(t, blue, color.RGBA{0, 0, 0xff, 0xff})
		r = cli(grokEnv, 6*time.Minute, "session", "send", id, "--wait", "-f", blue, "--message", prompt("images", "And this one? Reply with only the color."))
		if r.code != 0 || (!real && !strings.Contains(r.stdout, "-blue.png")) || (real && !strings.Contains(strings.ToLower(r.stdout), "blue")) {
			t.Fatalf("%s follow-up with a file: %+v", c.agent, r)
		}
	}
	if r := cli(grokEnv, 30*time.Second, "session", "start", "--project", "vps-1/demo", "-f", tmp); r.code != 2 {
		t.Fatalf("attaching a directory: %+v", r)
	}

	// Questions: the agent asks mid-turn; --wait returns with exit code 8
	// and the question; `answer` (or a message) resumes the turn.
	askPrompt := prompt("ask Color|Which color do you prefer?|Red,Blue\nsay done",
		"Ask me which color I prefer, Red or Blue, with your tool for asking the user a question, and wait for my answer. Then reply with only the chosen color.")
	for _, c := range []struct {
		machine, agent string
		model          []string
		answer         []string
	}{{"vps-1", "claude", model, []string{"answer", "--choose", "2"}}, {"owner-mac", "codex", nil, []string{"send", "--message", "Blue"}}} {
		r := cli(grokEnv, 6*time.Minute, append([]string{"session", "start", "--project", c.machine + "/demo", "--agent", c.agent, "--wait", "--prompt", askPrompt}, c.model...)...)
		id := sessionIDOf(r.stderr)
		if r.code != 8 || !strings.Contains(r.stdout, "? ") || !strings.Contains(r.stdout, "Blue") || !strings.Contains(r.stderr, "hermesh session answer "+id) {
			t.Fatalf("%s question: %+v", c.agent, r)
		}
		if g := cli(grokEnv, 30*time.Second, "session", "get", id); !strings.Contains(g.stdout, "waiting_input") || !strings.Contains(g.stdout, "? [Color]") {
			t.Fatalf("%s get while waiting: %+v", c.agent, g)
		}
		args := append([]string{"session", c.answer[0], id, "--wait"}, c.answer[1:]...)
		r = cli(grokEnv, 6*time.Minute, args...)
		if r.code != 0 || (real && !strings.Contains(strings.ToLower(r.stdout), "blue")) {
			t.Fatalf("%s answered: %+v", c.agent, r)
		}
		logs := cli(grokEnv, 30*time.Second, "session", "events", id, "--limit", "1000")
		if !strings.Contains(logs.stdout, "↳ answered by c_") || (!real && !strings.Contains(logs.stdout, "answer: Blue")) {
			t.Fatalf("%s logs: %s", c.agent, logs.stdout)
		}
		if r := cli(grokEnv, 30*time.Second, "session", "answer", id, "--text", "late"); r.code != 2 {
			t.Fatalf("%s answer with no question: %+v", c.agent, r)
		}
	}
	// The owner answers from the app.
	r = cli(grokEnv, 6*time.Minute, append([]string{"session", "start", "--project", "vps-1/demo", "--agent", "claude", "--wait", "--prompt", askPrompt}, model...)...)
	ownerQ := sessionIDOf(r.stderr)
	if r.code != 8 {
		t.Fatalf("question for the owner: %+v", r)
	}
	if _, err := app.client.AnswerQuestion(ctx, &localv1.AnswerQuestionRequest{SessionId: ownerQ, Answers: []*sessionv1.Answer{{Selected: []string{"Blue"}}}}); err != nil {
		t.Fatalf("owner answer: %v", err)
	}
	eventually(t, 4*time.Minute, "the owner's answer is used", func() bool {
		logs := cli(grokEnv, 30*time.Second, "session", "events", ownerQ, "--limit", "1000")
		return strings.Contains(logs.stdout, "↳ answered by owner: Blue") && strings.Contains(logs.stdout, "✓ turn 1 done")
	})
	r = cli(grokEnv, 6*time.Minute, append([]string{"session", "start", "--project", "vps-1/demo", "--agent", "claude", "--wait",
		"--question-timeout", "0", "--prompt", askPrompt}, model...)...)
	if r.code != 0 {
		t.Fatalf("a question nobody answers: %+v", r)
	}
	if logs := cli(grokEnv, 30*time.Second, "session", "events", sessionIDOf(r.stderr), "--limit", "1000"); !strings.Contains(logs.stdout, "no answer: the agent continues on its own") {
		t.Fatalf("logs: %s", logs.stdout)
	}

	// The owner's app sees a reply on another machine as it is written: drafts
	// come through the relay, end to end, and add up to the stored message.
	r = cli(grokEnv, time.Minute, append([]string{"session", "start", "--project", "vps-1/demo", "--agent", "claude", "-o", "json",
		"--prompt", prompt("sleep 2s\ndraft Hello |from |the |VPS, |as it |is |written\nsleep 500ms\nsay Hello from the VPS, as it is written",
			"Count from one to forty in English words, separated by spaces, and nothing else.")}, model...)...)
	var streamed struct {
		SessionID string `json:"session_id"`
	}
	unmarshalSession(r.stdout, &streamed)
	func() {
		sctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		defer cancel()
		stream, err := app.client.StreamSessionEvents(sctx, &localv1.StreamSessionEventsRequest{SessionId: streamed.SessionID, Follow: true, Partial: true})
		if err != nil {
			t.Fatalf("stream with drafts: %v", err)
		}
		defer stream.Close()
		var draft, lastDraft, message string
		drafts := 0
		for stream.Receive() {
			e := stream.Msg()
			if d := e.GetAgentDraft(); d != nil {
				drafts++
				if d.Whole {
					draft = d.Text
				} else {
					draft += d.Text
				}
				lastDraft = draft
				continue
			}
			if m := e.GetAgentMessage(); m != nil {
				message = m.Text
			}
			if e.GetTurnCompleted() != nil {
				break
			}
		}
		if drafts < 2 || message == "" || (!real && lastDraft != message) {
			t.Fatalf("drafts through the relay: %d, last %q, message %q (%v)", drafts, lastDraft, message, stream.Err())
		}
	}()

	// In-place projects: the agent works in the directory itself.
	if ps := listed(grokEnv); ps["vps-1/notes"]["workspace"] != "in_place" || ps["vps-1/demo"]["workspace"] != "worktree" {
		t.Fatalf("projects: %v", ps)
	}
	if r := cli(grokEnv, 30*time.Second, "session", "start", "--project", "vps-1/notes", "--base", "HEAD", "--prompt", "x"); r.code != 2 {
		t.Fatalf("--base on an in-place project: %+v", r)
	}
	r = cli(grokEnv, 6*time.Minute, append([]string{"session", "start", "--project", "notes", "--agent", "claude", "--wait",
		"--prompt", prompt("write todo.txt buy milk\nsay done", "Create a file todo.txt containing exactly: buy milk. Then reply: done")}, model...)...)
	if b, _ := os.ReadFile(filepath.Join(notesDir, "todo.txt")); r.code != 0 || !strings.Contains(string(b), "buy milk") {
		t.Fatalf("in-place claude session: %+v, todo.txt %q", r, b)
	}
	notesSession := sessionIDOf(r.stderr)
	if real {
		r = cli(grokEnv, 6*time.Minute, "session", "send", notesSession, "--wait", "--message", "What does notes.md say? Reply with its text only.")
		if r.code != 0 || !strings.Contains(strings.ToLower(r.stdout), "falcon") {
			t.Fatalf("in-place session reading the owner's file: %+v", r)
		}
	}
	if d := cli(grokEnv, 30*time.Second, "session", "diff", notesSession); d.code == 0 || !strings.Contains(d.stderr, "works in place") {
		t.Fatalf("diff of an in-place session: %+v", d)
	}
	headBefore := gitIn(t, inPlaceRepo, "rev-parse", "HEAD")
	r = cli(grokEnv, 8*time.Minute, "session", "start", "--project", "live", "--agent", "codex", "--wait",
		"--prompt", prompt("write feature.txt from codex\nsay done", "Create a file feature.txt containing exactly: from codex. Do not commit. Then reply: done"))
	if b, _ := os.ReadFile(filepath.Join(inPlaceRepo, "feature.txt")); r.code != 0 || !strings.Contains(string(b), "from codex") {
		t.Fatalf("in-place codex session: %+v, feature.txt %q", r, b)
	}
	if h := gitIn(t, inPlaceRepo, "rev-parse", "HEAD"); h != headBefore || gitIn(t, inPlaceRepo, "branch", "--list", "hermesh/*") != "" ||
		!strings.Contains(gitIn(t, inPlaceRepo, "status", "--porcelain"), "?? feature.txt") {
		t.Fatalf("in-place git repo: HEAD %s (was %s), branches %q", h, headBefore, gitIn(t, inPlaceRepo, "branch", "--list"))
	}

	// A5: cancel a running session.
	long := cli(grokEnv, 30*time.Second, "session", "start", "--project", "vps-1/demo", "--agent", "claude", "-o", "json",
		"--prompt", prompt("sleep 60s", "Run the shell command `sleep 60` and then reply done."))
	var s3 struct {
		SessionID string `json:"session_id"`
	}
	unmarshalSession(long.stdout, &s3)
	eventually(t, 60*time.Second, "long session running", func() bool {
		g := cli(grokEnv, 30*time.Second, "session", "get", s3.SessionID, "-o", "json")
		return strings.Contains(g.stdout, "running")
	})
	// The project counts it while its turn runs, and not once it is interrupted.
	if n := listed(grokEnv)["vps-1/demo"]["active_sessions"]; n != float64(1) {
		t.Fatalf("vps-1/demo with a turn running: active_sessions %v", n)
	}
	if c := cli(grokEnv, 30*time.Second, "session", "interrupt", s3.SessionID); c.code != 0 {
		t.Fatalf("interrupt: %+v", c)
	}
	eventually(t, 30*time.Second, "vps-1/demo idle after the interrupt", func() bool {
		return listed(grokEnv)["vps-1/demo"]["active_sessions"] == float64(0)
	})

	// A5: the owner sees every session across machines.
	all, err := app.client.ListSessions(ctx, &localv1.ListSessionsRequest{})
	if err != nil || len(all.Sessions) < 3 {
		t.Fatalf("owner session list: %v %v", all, err)
	}

	// A9 (part): kill -9 the machine; the lists show it offline (and
	// still succeed); after a restart it reconnects and the history is
	// intact.
	machine1.kill()
	eventually(t, 30*time.Second, "vps-1 offline", func() bool {
		var list struct {
			Sessions []struct{ SessionID string }      `json:"sessions"`
			Offline  []struct{ Machine, Error string } `json:"offline"`
		}
		r := cli(grokEnv, 30*time.Second, "session", "list", "-o", "json")
		json.Unmarshal([]byte(r.stdout), &list)
		return r.code == 0 && len(list.Sessions) > 0 && len(list.Offline) == 1 && list.Offline[0].Machine == "vps-1" && list.Offline[0].Error == ""
	})
	// Its projects cannot be listed while it is away: the list has a row
	// for the machine instead.
	r = cli(grokEnv, 30*time.Second, "project", "list")
	if r.code != 0 || strings.Contains(r.stdout, "vps-1/demo") || !strings.Contains(r.stdout, "owner-mac/demo") || r.stderr != "" ||
		!regexp.MustCompile(`(?m)^vps-1/\*\s+-\s+-\s+-\s+offline\s+last seen .* ago$`).MatchString(r.stdout) {
		t.Fatalf("projects, vps-1 down: %+v", r)
	}
	if r := cli(grokEnv, 30*time.Second, "session", "list"); r.code != 0 || !strings.Contains(r.stdout, "\noffline: vps-1 (last seen ") {
		t.Fatalf("session list, vps-1 down: %+v", r)
	}
	// What needs vps-1 fails, and may work later; a project name found on
	// no machine online may be on vps-1, so it is not "not found".
	for _, args := range [][]string{{"session", "get", s1.SessionID}, {"session", "start", "--project", "nosuch", "--prompt", "x"}} {
		var doc struct {
			ExitCode int `json:"exit_code"`
			Error    struct {
				Kind      string
				Retryable bool
			} `json:"error"`
		}
		r := cli(grokEnv, 30*time.Second, append(args, "-o", "json")...)
		if json.Unmarshal([]byte(r.stdout), &doc); r.code != 4 || doc.ExitCode != 4 || doc.Error.Kind != "unreachable" || !doc.Error.Retryable {
			t.Fatalf("%v, vps-1 down: %+v", args, r)
		}
	}
	machine1 = startMachine1()
	eventually(t, 40*time.Second, "vps-1 back", func() bool {
		g := cli(grokEnv, 30*time.Second, "session", "events", s1.SessionID, "--limit", "1000")
		return g.code == 0 && strings.Contains(g.stdout, "turn 2 done")
	})
	// A retried start (same idempotency key) after the crash returns the
	// session that was started, not a new one.
	retry := cli(grokEnv, 30*time.Second, "session", "start", "--project", "vps-1/demo", "--agent", "claude", "-o", "json",
		"--idempotency-key", "e2e-s1", "--prompt", "ignored")
	if !strings.Contains(retry.stdout, s1.SessionID) {
		t.Fatalf("idempotent retry after restart: %+v", retry)
	}

	// A6: narrow the agent to Codex only; the change applies to new sessions
	// and the old (broader) grant version stops working.
	clients, err := app.client.ListClients(ctx, &localv1.ListClientsRequest{})
	if err != nil || len(clients.Clients) != 1 {
		t.Fatalf("clients: %v %v", clients, err)
	}
	grok := clients.Clients[0]
	spec := grok.Spec
	spec.Agents = []string{"codex"}
	upd, err := app.client.UpdateClient(ctx, &localv1.UpdateClientRequest{GrantId: grok.GrantId, Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	okAcks := 0
	for _, a := range upd.Acks {
		if a.Ok {
			okAcks++
		}
	}
	if okAcks != 2 {
		t.Fatalf("update acks: %v", upd.Acks)
	}
	if r := cli(grokEnv, 30*time.Second, "session", "start", "--project", "vps-1/demo", "--agent", "claude", "--prompt", "say x"); r.code != 3 {
		t.Fatalf("agent outside the updated grant: %+v", r)
	}
	if w := cli(grokEnv, 30*time.Second, "auth", "status"); !strings.Contains(w.stdout, "v2") {
		t.Fatalf("auth status after update: %+v", w)
	}

	// A10 prep: a second agent that will survive losing the first Mac untouched.
	scoutHome := filepath.Join(tmp, "scout")
	scoutEnv := []string{"HERMESH_HOME=" + scoutHome}
	pairClient(scoutEnv, "scout", &localv1.GrantSpec{MachineIds: []string{vps1.MachineId}, Agents: []string{"claude"}})
	// An agent granted no project that exists sees an empty list that says what to do.
	idleEnv := []string{"HERMESH_HOME=" + filepath.Join(tmp, "idle")}
	pairClient(idleEnv, "idle", &localv1.GrantSpec{ProjectIds: []string{"nothing-here"}})
	if r := cli(idleEnv, 30*time.Second, "project", "list"); r.code != 0 || !strings.Contains(r.stdout, "ask the owner") {
		t.Fatalf("empty project list: %+v", r)
	}
	// It sees only its machine's projects, and only the coding agent it may use.
	if ps := listed(scoutEnv); len(ps) != 2 || ps["vps-1/demo"] == nil || len(ps["vps-1/demo"]["agents"].([]any)) != 1 {
		t.Fatalf("scoped agent sees: %v", ps)
	}

	// A8: revoke grok.
	rev, err := app.client.RevokeClient(ctx, &localv1.RevokeClientRequest{GrantId: grok.GrantId})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range rev.Acks {
		if !a.Ok {
			t.Fatalf("revoke ack: %v", a)
		}
	}
	if r := cli(grokEnv, 30*time.Second, "project", "list"); r.code != 3 {
		t.Fatalf("revoked agent still works: %+v", r)
	}

	// A10: a second Mac joins, approved on the first (docs/trusted-devices.md
	// 4.2). Then the first is lost and removed from the second (4.3): the
	// machine and the agents keep working, the first Mac's own machine goes.
	sock2 := filepath.Join(sockDir, "o2.sock")
	owner2 := start(t, "owner second-mac", agentEnv, daemonBin, "run", "--owner", "--data-dir", filepath.Join(tmp, "second-mac"), "--socket", sock2)
	logs = append(logs, owner2)
	root2, _ := identity.GenerateKey()
	app2 := newTestApp(t, sock2, root2)
	join, err := app2.client.RequestJoin(ctx, &localv1.RequestJoinRequest{RelayUrl: relayURL, OwnerId: setup.OwnerId,
		RootPublicKey: rootPub(root2), RootLabel: "second-mac", RootPlatform: "macos"})
	if err != nil {
		t.Fatalf("request join: %v\n%s", err, owner2.output())
	}
	var asked *localv1.PairingDetails
	eventually(t, 15*time.Second, "join request on the first Mac", func() bool {
		res, err := app.client.ListPairingRequests(ctx, &localv1.ListPairingRequestsRequest{})
		for _, p := range res.GetPairings() {
			if p.Kind == identityv1.PairingKind_PAIRING_KIND_DEVICE {
				asked = p
			}
		}
		return err == nil && asked != nil
	})
	if asked.Code != join.Code || asked.VerificationCode != join.VerificationCode || asked.Label != "second-mac" {
		t.Fatalf("the first Mac shows %+v; the second asked %+v", asked, join)
	}
	if _, err := app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: join.Code}); err == nil {
		t.Fatal("a device was approved as a machine")
	}
	approval, err := app.client.ApproveDevicePairing(ctx, &localv1.ApproveDevicePairingRequest{Code: join.Code})
	if err != nil || len(approval.GetMatchCode()) != 2 {
		t.Fatalf("approve device: %v %v", approval, err)
	}
	var joined *localv1.WaitJoinResponse
	eventually(t, 60*time.Second, "approval reaches the second Mac", func() bool {
		joined, err = app2.client.WaitJoin(ctx, &localv1.WaitJoinRequest{Code: join.Code})
		return err == nil && joined.Done
	})
	if joined.ApprovedBy != "test-mac" || len(joined.Devices) != 2 {
		t.Fatalf("join result: %+v", joined)
	}
	if st, _ := app2.client.GetStatus(ctx, &localv1.GetStatusRequest{}); st.GetInitialized() {
		t.Fatal("the second Mac kept the owner before the person confirmed")
	}
	// The person types the two digits the first Mac shows; a wrong number
	// is refused.
	wrong := "00"
	if approval.MatchCode == wrong {
		wrong = "01"
	}
	if _, err := app2.client.ConfirmJoin(ctx, &localv1.ConfirmJoinRequest{Code: join.Code, Accept: true, MatchCode: wrong}); err == nil {
		t.Fatal("a wrong number was accepted")
	}
	if _, err := app2.client.ConfirmJoin(ctx, &localv1.ConfirmJoinRequest{Code: join.Code, Accept: true, MatchCode: approval.MatchCode}); err != nil {
		t.Fatal(err)
	}
	st2, err := app2.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil || !st2.Initialized || st2.OwnerId != setup.OwnerId || st2.DeviceKeyId != identity.KeyID(root2.Public()) {
		t.Fatalf("second Mac status: %v %v", st2, err)
	}
	devices, err := app2.client.ListDevices(ctx, &localv1.ListDevicesRequest{})
	if err != nil || len(devices.Devices) != 2 {
		t.Fatalf("devices: %v %v", devices, err)
	}
	var first *localv1.Device
	for _, d := range devices.Devices {
		if !d.ThisDevice {
			first = d
		}
	}
	if first == nil || first.Label != "test-mac" || first.MachineId != localMachine.MachineId {
		t.Fatalf("the first Mac as the second sees it: %+v (its machine %s)", first, localMachine.MachineId)
	}
	if _, err := app2.client.RemoveDevice(ctx, &localv1.RemoveDeviceRequest{KeyId: st2.DeviceKeyId}); err == nil {
		t.Fatal("a device removed itself")
	}

	// The first Mac is lost; the second removes it.
	ownerProc.stop()
	app.cancel()
	removed, err := app2.client.RemoveDevice(ctx, &localv1.RemoveDeviceRequest{KeyId: first.KeyId})
	if err != nil {
		t.Fatalf("remove device: %v\n%s", err, owner2.output())
	}
	// vps-1 re-issued (the first Mac's own machine is revoked instead);
	// scout's and idle's grants re-issued (grok's was revoked).
	if removed.ReissuedMachines != 1 || removed.ReissuedGrants != 2 {
		t.Fatalf("remove result: %+v", removed)
	}
	eventually(t, 30*time.Second, "vps-1 trusts the second Mac", func() bool {
		res, err := app2.client.ListProjects(ctx, &localv1.ListProjectsRequest{MachineId: vps1.MachineId})
		return err == nil && len(res.Projects) == 2 // demo and notes
	})
	if ms, _ := app2.client.ListMachines(ctx, &localv1.ListMachinesRequest{}); slices.ContainsFunc(ms.GetMachines(), func(m *localv1.Machine) bool { return m.MachineId == localMachine.MachineId }) {
		t.Fatal("the removed Mac's own machine is still listed")
	}
	// vps-1 reconnects with its re-issued cert; the agent sees it back before starting.
	eventually(t, time.Minute, "the agent sees vps-1 again", func() bool {
		r := cli(scoutEnv, 30*time.Second, "project", "list")
		return r.code == 0 && strings.Contains(r.stdout, "vps-1/demo")
	})
	scout := cli(scoutEnv, 6*time.Minute, append([]string{"session", "start", "--project", "demo", "--agent", "claude", "--wait",
		"--prompt", prompt("say after removal", "Reply with exactly: after removal")}, model...)...)
	if scout.code != 0 || !strings.Contains(strings.ToLower(scout.stdout), "after removal") {
		t.Fatalf("agent after the first Mac was removed: %+v\nvps-1:\n%s", scout, machine1.output())
	}

	// The first Mac comes back: refused, it learns it was removed and resets.
	ownerProc = startOwner(ownerData)
	app = newTestApp(t, sock, root)
	eventually(t, 60*time.Second, "the first Mac knows it was removed", func() bool {
		st, err := app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
		return err == nil && st.Removed
	})
	if _, err := app.client.Leave(ctx, &localv1.LeaveRequest{}); err != nil {
		t.Fatal(err)
	}
	if st, _ := app.client.GetStatus(ctx, &localv1.GetStatusRequest{}); st.GetInitialized() {
		t.Fatal("still set up after leaving")
	}
	if _, err := app2.client.Leave(ctx, &localv1.LeaveRequest{}); err == nil {
		t.Fatal("a device that is still trusted left")
	}
	t.Logf("signed by the second Mac: %v", app2.signedTypes())
}

func writePNG(t *testing.T, path string, c color.RGBA) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for x := 0; x < 64; x++ {
		for y := 0; y < 64; y++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// sessionIDOf finds the session id in `--wait` stderr ("session s_… on …").
func sessionIDOf(stderr string) string {
	for _, f := range strings.Fields(stderr) {
		if strings.HasPrefix(f, "s_") {
			return f
		}
	}
	return ""
}
