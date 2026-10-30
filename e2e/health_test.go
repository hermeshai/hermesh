package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
)

// userProcesses counts this user's processes, as the system limits them.
func userProcesses(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("ps", "-U", strconv.Itoa(os.Getuid()), "-o", "pid=").Output()
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Fields(string(out)))
}

// zombies starts an app ("Zombie Maker", "僵尸制造机" in Chinese) that
// forks n children and never reaps them, as the app that once filled a
// user's Mac with 2232 of them did; it holds them until the test kills it.
func zombies(t *testing.T, n int) *proc {
	t.Helper()
	app := filepath.Join(t.TempDir(), "ZombieMaker.app")
	bin := filepath.Join(app, "Contents/MacOS/zombies")
	for f, s := range map[string]string{
		"Info.plist": `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>CFBundleName</key><string>Zombie Maker</string></dict></plist>`,
		"Resources/zh-Hans.lproj/InfoPlist.strings": `"CFBundleDisplayName" = "僵尸制造机";`,
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(app, "Contents", f)), 0o755)
		os.WriteFile(filepath.Join(app, "Contents", f), []byte(s), 0o644)
	}
	// Its program is a copy of sleep (signed again, or it won't run). The
	// shell forks the children, then becomes it; they end once it has.
	os.MkdirAll(filepath.Dir(bin), 0o755)
	b, _ := os.ReadFile("/bin/sleep")
	os.WriteFile(bin, b, 0o755)
	if out, err := exec.Command("codesign", "-f", "-s", "-", bin).CombinedOutput(); err != nil {
		t.Fatalf("codesign: %v %s", err, out)
	}
	return start(t, "zombies", nil, "/bin/sh", "-c", `i=0; while [ $i -lt `+strconv.Itoa(n)+` ]; do /bin/sleep 2 & i=$((i+1)); done; echo ready; exec "$0" 600`, bin)
}

// TestOutOfProcesses: the owner's Mac runs out of processes. Its machine
// says so (and names what holds them) to every screen, says why each coding
// agent can't run instead of calling it not installed, tells an agent
// starting a session there why it can't, and recovers by itself once the
// culprit quits.
func TestOutOfProcesses(t *testing.T) {
	if testing.Short() || runtime.GOOS != "darwin" {
		t.Skip("e2e on macOS")
	}
	codex := filepath.Join(t.TempDir(), "codex")
	os.WriteFile(codex, []byte("#!/bin/sh\ncase \"$1\" in --version) echo 'codex-cli 0.48.0';; login) echo 'Logged in';; esac\n"), 0o755)
	w := newOwnerMac(t, []string{"HERMESH_CODEX_BIN=" + codex}, &localv1.GrantSpec{Agents: []string{"codex"}})
	ctx := context.Background()
	status := func() *machinev1.MachineStatus {
		res, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err != nil || len(res.Machines) == 0 {
			return nil
		}
		return res.Machines[0].Status
	}
	agent := func(name string) *machinev1.AgentStatus {
		for _, a := range status().GetAgents() {
			if a.Name == name {
				return a
			}
		}
		return nil
	}
	w.app.client.CheckAgents(ctx, &localv1.CheckAgentsRequest{})
	eventually(t, 30*time.Second, "codex available", func() bool { a := agent("codex"); return a != nil && a.Available })

	// Its hermeshd may start 80 more processes than this user runs now (only
	// it: the user's other programs keep the system's limit, far above)…
	w.nproc = userProcesses(t) + 80
	w.restartOwner(false)
	full := func(p *machinev1.MachineProblem) bool {
		return p.Kind == machinev1.MachineProblemKind_MACHINE_PROBLEM_KIND_PROCESSES && p.Used >= p.Limit
	}

	// …until a program that never reaps its children holds them all.
	culprit := zombies(t, 200)
	eventually(t, 20*time.Second, "the zombies are there", func() bool { return strings.Contains(culprit.output(), "ready") })
	var p *machinev1.MachineProblem
	eventually(t, 40*time.Second, "the machine says it can't start programs", func() bool {
		for _, x := range status().GetProblems() {
			if full(x) {
				p = x
			}
		}
		return p != nil
	})
	// It names the app holding the most as the owner sees it (this one,
	// unless one on this computer holds even more).
	if p.Culprit == "" || p.CulpritCount < 150 ||
		p.CulpritCount == 200 && p.Culprit != "僵尸制造机" && p.Culprit != "Zombie Maker" {
		t.Fatalf("the problem: %v", p)
	}
	w.app.client.CheckAgents(ctx, &localv1.CheckAgentsRequest{})
	eventually(t, 40*time.Second, "codex can't run for want of processes", func() bool {
		a := agent("codex")
		return a != nil && !a.Available && !a.Checking && a.Problem == machinev1.AgentProblem_AGENT_PROBLEM_NO_PROCESSES && a.Binary == codex
	})
	// An agent sending it a session is told why it can't start, and what to do.
	if _, r := w.startSession(time.Minute, "--agent", "codex", "--prompt", "hi"); r.code == 0 ||
		!strings.Contains(r.stdout, "this machine can't start programs now: the user's processes are at their limit") ||
		!strings.Contains(r.stdout, "start the session on another machine") {
		t.Fatalf("start while out of processes: %+v", r)
	}
	diag, err := w.app.client.GetDiagnostics(ctx, &localv1.GetDiagnosticsRequest{})
	if err != nil || !strings.Contains(diag.Text, "not reaped, under "+p.Culprit) || !strings.Contains(diag.Text, "cannot run (out of processes)") {
		t.Fatalf("diagnostics: %v\n%s", err, diag.GetText())
	}

	// The culprit quits: the problem goes, and codex is checked again by itself.
	culprit.kill()
	eventually(t, 60*time.Second, "the machine recovers", func() bool { return status() != nil && !slices.ContainsFunc(status().Problems, full) })
	eventually(t, 40*time.Second, "codex is available again", func() bool { a := agent("codex"); return a != nil && a.Available })
}

// TestAgentsOnTheLoginShellsPath: hermeshd runs with launchd's bare PATH,
// so it reads the login shell's. A coding agent installed where only that
// PATH reaches (a Node CLI and its node, from nvm) is found and runs; with
// a login shell that hangs, the machine says it can't read the PATH, and
// the agent says what is missing instead of being not installed.
func TestAgentsOnTheLoginShellsPath(t *testing.T) {
	unixOnly(t, "it is about the login shell's PATH")
	if testing.Short() {
		t.Skip("e2e")
	}
	dir := t.TempDir()
	nvm := filepath.Join(dir, "nvm", "bin")
	os.MkdirAll(nvm, 0o755)
	os.WriteFile(filepath.Join(nvm, "hermesh-test-node"), []byte("#!/bin/sh\necho 'codex-cli 0.48.0'\n"), 0o755)
	codex := filepath.Join(nvm, "codex")
	os.WriteFile(codex, []byte("#!/usr/bin/env hermesh-test-node\n"), 0o755)
	shell := filepath.Join(dir, "zsh")
	os.WriteFile(shell, []byte("#!/bin/sh\nprintf '__HERMESH_PATH__%s' '"+nvm+":/usr/bin:/bin'\n"), 0o755)
	slow := filepath.Join(dir, "slowsh")
	os.WriteFile(slow, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755)

	env := []string{"HERMESH_CODEX_BIN=" + codex, "PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	w := newOwnerMac(t, append(env, "SHELL="+shell), &localv1.GrantSpec{Agents: []string{"codex"}})
	ctx := context.Background()
	status := func() *machinev1.MachineStatus {
		res, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err != nil || len(res.Machines) == 0 {
			return nil
		}
		return res.Machines[0].Status
	}
	codexStatus := func() *machinev1.AgentStatus {
		for _, a := range status().GetAgents() {
			if a.Name == "codex" {
				return a
			}
		}
		return nil
	}
	w.app.client.CheckAgents(ctx, &localv1.CheckAgentsRequest{})
	eventually(t, 30*time.Second, "codex runs with node from the login shell's PATH", func() bool { a := codexStatus(); return a != nil && a.Available })

	// The login shell hangs: the PATH can't be read, and codex says node is missing.
	w.daemonEnv = append(slices.Clone(w.daemonEnv), "SHELL="+slow)
	w.restartOwner(false)
	w.app.client.CheckAgents(ctx, &localv1.CheckAgentsRequest{})
	eventually(t, time.Minute, "codex says what it misses", func() bool {
		a := codexStatus()
		return a != nil && !a.Available && !a.Checking && a.Problem == machinev1.AgentProblem_AGENT_PROBLEM_MISSING_RUNTIME && a.Detail == "hermesh-test-node"
	})
	eventually(t, 30*time.Second, "the machine says it can't read the PATH", func() bool {
		for _, p := range status().GetProblems() {
			if p.Kind == machinev1.MachineProblemKind_MACHINE_PROBLEM_KIND_SHELL_PATH {
				return p.Detail == "slowsh -lic did not finish within 10 s"
			}
		}
		return false
	})
}
