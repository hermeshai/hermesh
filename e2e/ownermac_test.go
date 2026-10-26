package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// ownerMac is an owner Mac (the owner daemon, also running its local machine
// with project "demo") behind a real relay, and one paired agent CLI.
type ownerMac struct {
	t          *testing.T
	bin, tmp   string
	repo       string
	daemonEnv  []string
	daemonArgs []string
	cliEnv     []string
	owner      *proc
	app        *testApp
	root       *identity.SoftwareKey
	sock       string
	logs       []*proc
	relay      *proc
	relayArgs  []string
	relayDebug string // where the relay serves /metrics
	ownerDebug string // where the owner's hermeshd serves /metrics
	repoHead   string // the owner's HEAD, which sessions must never move
	addr       string // the owner's connection address
	// nproc, when set, is the owner daemon's limit on the user's processes
	// (ulimit -u): it then can't start programs once they reach it.
	nproc int
}

// newOwnerMac starts it; daemonEnv and daemonArgs configure the owner
// daemon, grant is what the owner approves for the agent.
func newOwnerMac(t *testing.T, daemonEnv []string, grant *localv1.GrantSpec, daemonArgs ...string) *ownerMac {
	t.Helper()
	return newOwnerMacSigned(t, nil, daemonEnv, grant, daemonArgs...)
}

// newOwnerMacSigned is newOwnerMac with another signer in place of the Go
// stand-in (nil: the stand-in). signer starts it for w.sock and returns
// the file it keeps the root key in (identity.SaveKey's format).
func newOwnerMacSigned(t *testing.T, signer func(w *ownerMac) string, daemonEnv []string, grant *localv1.GrantSpec, daemonArgs ...string) *ownerMac {
	t.Helper()
	return newOwnerMacAt(t, signer, 0, daemonEnv, grant, daemonArgs...)
}

// newFarOwnerMac is newOwnerMac with the relay oneWay away from everyone
// (delayProxy).
func newFarOwnerMac(t *testing.T, oneWay time.Duration, daemonEnv []string, grant *localv1.GrantSpec) *ownerMac {
	t.Helper()
	return newOwnerMacAt(t, nil, oneWay, daemonEnv, grant)
}

func newOwnerMacAt(t *testing.T, signer func(w *ownerMac) string, oneWay time.Duration, daemonEnv []string, grant *localv1.GrantSpec, daemonArgs ...string) *ownerMac {
	t.Helper()
	w := &ownerMac{t: t, bin: binaries(t), tmp: t.TempDir(), daemonArgs: daemonArgs}
	w.ownerDebug = fmt.Sprintf("127.0.0.1:%d", freePort(t))
	w.daemonEnv = append(slices.Clone(daemonEnv), "HERMESH_DEBUG_ADDR="+w.ownerDebug)
	sockDir, _ := os.MkdirTemp("/tmp", "hm")
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	w.sock = filepath.Join(sockDir, "o.sock")
	t.Cleanup(func() {
		if t.Failed() {
			for _, p := range w.logs {
				t.Logf("==== %s output ====\n%s", p.name, p.output())
			}
		}
	})
	ctx := context.Background()
	relayBin := filepath.Join(w.bin, "hermesh-relay")
	port := freePort(t)
	relayData := filepath.Join(w.tmp, "relay")
	w.relayDebug = fmt.Sprintf("127.0.0.1:%d", freePort(t))
	w.relayArgs = []string{"serve", "--data-dir", relayData, "--listen", fmt.Sprintf("127.0.0.1:%d", port), "--debug-addr", w.relayDebug}
	w.startRelay()
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, relayBin, "claim-code", "--data-dir", relayData).stdout)

	w.startOwner()
	var keyFile string
	if signer == nil {
		w.root, _ = identity.GenerateKey()
		w.app = newTestApp(t, w.sock, w.root)
	} else {
		keyFile = signer(w)
		w.app = &testApp{t: t, client: localClient(t, w.sock), cancel: func() {}}
	}
	// No root key with the request: hermeshd asks the signer to create one,
	// as it does for the desktop app.
	relayURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if oneWay > 0 {
		relayURL = "http://" + delayProxy(t, relayURL[len("http://"):], oneWay)
	}
	setup, err := w.app.client.Setup(ctx, &localv1.SetupRequest{RelayUrl: relayURL, ClaimCode: claim, RootLabel: "test-mac"})
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, w.owner.output())
	}
	if keyFile != "" {
		if w.root, err = identity.LoadKey(keyFile); err != nil {
			t.Fatal(err)
		}
	}
	if setup.OwnerId != identity.OwnerID(rootPub(w.root)) {
		t.Fatalf("owner %s is not named by the key the signer created", setup.OwnerId)
	}
	localMachine, err := w.app.client.SetLocalMachine(ctx, &localv1.SetLocalMachineRequest{Enabled: true, MachineName: "owner-mac"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "machine online", func() bool {
		res, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		return err == nil && len(res.Machines) == 1 && res.Machines[0].Online && res.Machines[0].Status != nil
	})
	w.repo = gitRepo(t, map[string]string{"README.md": "# demo\n"})
	w.repoHead = gitOut(t, w.repo, "rev-parse", "HEAD")
	if _, err := w.app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: localMachine.MachineId, ProjectId: "demo", Path: w.repo}); err != nil {
		t.Fatal(err)
	}
	w.cliEnv = []string{"HERMESH_HOME=" + filepath.Join(w.tmp, "agent")}
	w.addr = setup.ConnectAddress
	login := start(t, "login", w.cliEnv, filepath.Join(w.bin, "hermesh"), "auth", "login", setup.ConnectAddress, "--name", "bot")
	if _, err := w.app.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{Code: login.pairingCode(t), Spec: grant}); err != nil {
		t.Fatal(err)
	}
	if c := login.wait(t, 30*time.Second); c != 0 {
		t.Fatalf("login: %s", login.output())
	}
	return w
}

func (w *ownerMac) startRelay() {
	w.relay = start(w.t, "relay", nil, filepath.Join(w.bin, "hermesh-relay"), w.relayArgs...)
	w.logs = append(w.logs, w.relay)
	r := w.relay
	eventually(w.t, 20*time.Second, "relay up", func() bool { return strings.Contains(r.output(), "relay listening") })
}

// restartRelay stops the relay and starts it again on the same address.
func (w *ownerMac) restartRelay() {
	w.relay.stop()
	w.startRelay()
}

// worktree is where this Mac's machine, in the current profile, keeps a
// session's worktree.
func (w *ownerMac) worktree(id string) string {
	cur, _ := os.ReadFile(filepath.Join(w.tmp, "owner-mac", "owner", "current"))
	return filepath.Join(w.tmp, "owner-mac", "owner", "profiles", string(cur), "machine", "worktrees", id)
}

func (w *ownerMac) startOwner() {
	args := append([]string{"run", "--owner", "--data-dir", filepath.Join(w.tmp, "owner-mac"), "--socket", w.sock}, w.daemonArgs...)
	bin := filepath.Join(w.bin, "hermeshd")
	if w.nproc > 0 {
		args = append([]string{"-c", fmt.Sprintf(`ulimit -u %d && exec "$0" "$@"`, w.nproc), bin}, args...)
		bin = "/bin/sh"
	}
	w.owner = start(w.t, "owner", w.daemonEnv, bin, args...)
	w.logs = append(w.logs, w.owner)
}

// restartOwner stops the owner daemon (gracefully, or with kill -9) and
// starts it again, waiting until the machine is back online.
func (w *ownerMac) restartOwner(kill bool) {
	if kill {
		w.owner.kill()
	} else {
		w.owner.stop()
	}
	w.app.cancel()
	w.startOwner()
	w.app = newTestApp(w.t, w.sock, w.root)
	eventually(w.t, 30*time.Second, "machine back", func() bool {
		r := w.cli(30*time.Second, "project", "list")
		return r.code == 0 && strings.Contains(r.stdout, "owner-mac/demo")
	})
}

func (w *ownerMac) cli(timeout time.Duration, args ...string) result {
	return run(w.t, w.cliEnv, timeout, filepath.Join(w.bin, "hermesh"), args...)
}

// sessionJSON is the part of the contract's session the tests read.
type sessionJSON struct {
	SessionID   string `json:"session_id"`
	Status      string `json:"status"`
	Title       string `json:"title"`
	Ref         string `json:"agent_session_id"`
	Result      string `json:"result"`
	LastFailure *struct {
		Kind      string `json:"kind"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
		Hint      string `json:"hint"`
	} `json:"last_failure"`
}

// startSession runs `hermesh session start --project demo -o json args...`.
func (w *ownerMac) startSession(timeout time.Duration, args ...string) (sessionJSON, result) {
	r := w.cli(timeout, append([]string{"session", "start", "--project", "demo", "-o", "json"}, args...)...)
	return firstSession(r.stdout), r
}

// firstSession decodes the session of a command's JSON document.
func firstSession(out string) sessionJSON {
	var doc struct{ Session sessionJSON }
	json.Unmarshal([]byte(out), &doc)
	return doc.Session
}

// unmarshalSession decodes the session of a command's JSON document into v.
func unmarshalSession(out string, v any) error {
	var doc struct{ Session json.RawMessage }
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return err
	}
	return json.Unmarshal(doc.Session, v)
}

// send runs `hermesh session send id --wait -o json --message text`.
func (w *ownerMac) send(timeout time.Duration, id, text string) (sessionJSON, result) {
	r := w.cli(timeout, "session", "send", id, "--wait", "-o", "json", "--message", text)
	return firstSession(r.stdout), r
}

func (w *ownerMac) get(id string) sessionJSON {
	return firstSession(w.cli(30*time.Second, "session", "get", id, "-o", "json").stdout)
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
