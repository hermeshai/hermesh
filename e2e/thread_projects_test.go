package e2e

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// TestThreadProjectsAndGrants: what adding a project on another machine does
// to the grants, end to end with the real binaries (docs/thread-composer.md
// §3.2, §8). work-mac joins as a machine; two agents are connected, bot with
// every project and relay-bot with the project relay (and the temporary
// workspace, named). A project added on work-mac keeps its folder: adding the
// name again for another folder is refused, the same folder changes its
// settings. Both agents can start sessions in the new project at once. The
// agents it is added with limit nothing (docs/settings-environments.md D5):
// Codex works there too, for an agent and the owner. "Every project" leaves
// out the temporary workspace; naming it lets in.
func TestThreadProjectsAndGrants(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	fakes := []string{"HERMESH_FAKE_PROVIDERS=1"}
	w := newOwnerMac(t, fakes, &localv1.GrantSpec{ProjectIds: []string{"*"}, Agents: []string{"claude", "codex"}})
	ctx := context.Background()
	home, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	work, _ := w.otherMac("work-mac", fakes)
	workID := w.joinTyped(work, home, "work-mac").LocalMachineId
	eventually(t, 30*time.Second, "work-mac online", func() bool {
		ms, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err != nil {
			return false
		}
		for _, m := range ms.Machines {
			if m.MachineId == workID && m.Online && m.Status != nil {
				return true
			}
		}
		return false
	})

	// relay-bot: work-mac's project relay, and its temporary workspace by name.
	relayEnv := []string{"HERMESH_HOME=" + filepath.Join(w.tmp, "relay-bot"), "HERMESH_LINK=off"}
	login := start(t, "login relay-bot", relayEnv, filepath.Join(w.bin, "hermesh"), "auth", "login", w.addr, "--name", "relay-bot")
	if _, err := w.app.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{Code: login.pairingCode(t),
		Spec: &localv1.GrantSpec{MachineIds: []string{workID}, ProjectIds: []string{"relay", "scratch"}, Agents: []string{"claude", "codex"}}}); err != nil {
		t.Fatal(err)
	}
	if c := login.wait(t, 30*time.Second); c != 0 {
		t.Fatalf("login: %s", login.output())
	}
	botEnv := append(append([]string{}, w.cliEnv...), "HERMESH_LINK=off")

	// relay on work-mac, sent as for Claude Code only. Another folder by its name is refused, naming the folder it is;
	// the same folder again changes its settings.
	first := gitRepo(t, map[string]string{"README.md": "# relay\n"})
	other := gitRepo(t, map[string]string{"README.md": "# another\n"})
	add := func(path string, agents ...string) error {
		_, err := w.app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: workID, ProjectId: "relay", Path: path, Agents: agents})
		return err
	}
	if err := add(first, "claude"); err != nil {
		t.Fatalf("add relay: %v", err)
	}
	if err := add(other, "claude"); connect.CodeOf(err) != connect.CodeAlreadyExists || !strings.Contains(err.Error(), filepath.Base(first)) {
		t.Fatalf("relay to another folder: %v", err)
	}
	if err := add(first, "claude", "codex"); err != nil {
		t.Fatalf("relay's settings changed: %v", err)
	}
	if err := add(first, "claude"); err != nil {
		t.Fatalf("relay's settings changed back: %v", err)
	}
	ps, err := w.app.client.ListProjects(ctx, &localv1.ListProjectsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps.Projects {
		if p.MachineId == workID && p.ProjectId == "relay" && filepath.Base(p.Path) != filepath.Base(first) {
			t.Fatalf("relay moved to %s", p.Path)
		}
	}

	launch := func(env []string, project, provider string) result {
		target := "work-mac"
		if project != "" {
			target = "work-mac/" + project
		}
		args := []string{"session", "start", "--project", target, "--agent", provider, "--prompt", "say from " + provider, "--wait", "-o", "json"}
		return run(t, env, time.Minute, filepath.Join(w.bin, "hermesh"), args...)
	}
	// Both grants reach the new project at once: bot by "every project", relay-bot by its name.
	for name, env := range map[string][]string{"bot": botEnv, "relay-bot": relayEnv} {
		if r := launch(env, "relay", "claude"); r.code != 0 {
			t.Fatalf("%s in relay: %+v", name, r)
		}
	}

	// Codex works in relay too, for an agent and for the owner: the project is for every provider.
	if r := launch(relayEnv, "relay", "codex"); r.code != 0 {
		t.Fatalf("relay-bot with Codex in relay: %+v", r)
	}
	var sel sessionv1.ModelSelection
	sel.InstanceId = "codex"
	_, err = w.app.sessions.StartSession(ctx, &sessionv1.StartSessionRequest{MachineId: workID, ProjectId: "relay", InitialPrompt: "say from the owner", ModelSelection: &sel})
	if err != nil {
		t.Fatalf("the owner with Codex in relay: %v", err)
	}

	// The temporary workspace: not in "every project"; named, it is in.
	if r := launch(botEnv, "scratch", "claude"); r.code == 0 {
		t.Fatalf("bot in the temporary workspace: %+v", r)
	}
	if r := launch(relayEnv, "scratch", "claude"); r.code != 0 {
		t.Fatalf("relay-bot in the temporary workspace: %+v", r)
	}
}
