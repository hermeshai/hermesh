package machine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	iv "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	mv "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	rt "github.com/hermeshai/hermesh/internal/runtime"
)

type waitingProvider struct{ entered chan struct{} }

func (p waitingProvider) Discover(ctx context.Context) rt.Info {
	close(p.entered)
	<-ctx.Done()
	return rt.Info{Problem: rt.ProblemNotFound}
}

func writeFakeCLI(t *testing.T, path, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\necho %s\n", version)
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestProviderMaintenanceCancellation(t *testing.T) {
	p := waitingProvider{make(chan struct{})}
	cfg := Config{DataDir: t.TempDir(), Discoverers: map[string]AgentDiscoverer{"gemini": p}}
	n, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	op := &iv.MaintainProvider{RequestId: "signed-receipt-001", Provider: "gemini", Action: "verify"}
	if _, err = n.ApplySettings(context.Background(), &iv.AdminCommand{Command: &iv.AdminCommand_MaintainProvider{MaintainProvider: op}}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatal("unsigned operation", err)
	}
	if err = n.startProviderOperation(op); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("preflight did not start")
	}
	if err = n.startProviderOperation(op); err != nil {
		t.Fatal("identical retry", err)
	}
	if err = n.startProviderOperation(&iv.MaintainProvider{RequestId: "signed-receipt-002", Provider: "gemini", Action: "verify"}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatal("concurrent operation", err)
	}
	cancel := &iv.MaintainProvider{RequestId: op.RequestId, Provider: op.Provider, Action: "cancel"}
	if err = n.startProviderOperation(cancel); err != nil {
		t.Fatal(err)
	}
	if err = n.Close(); err != nil {
		t.Fatal(err)
	}
	n, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if err = n.startProviderOperation(op); err != nil {
		t.Fatal("original request retried after restart", err)
	}
	if err = n.startProviderOperation(cancel); err != nil {
		t.Fatal("cancel retried after restart", err)
	}
	st := &mv.MachineStatus{}
	n.providerStatus(st)
	if len(st.ProviderOperations) != 1 || st.ProviderOperations[0].Phase != "cancelled" {
		t.Fatal(st.ProviderOperations)
	}
	external := n.statusFor(&Principal{Kind: PrincipalClient})
	if len(external.ProviderMaintenance)+len(external.ProviderOperations) != 0 {
		t.Fatal("client saw owner maintenance data")
	}
}

func TestProviderInterruptedActivation(t *testing.T) {
	for _, written := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-write", true: "after-write"}[written], func(t *testing.T) {
			cfg := Config{DataDir: t.TempDir(), Discoverers: map[string]AgentDiscoverer{}}
			n, err := Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			op := &iv.MaintainProvider{RequestId: "activation-001", Provider: "codex", Action: "update", ExpectedVersion: "0.171.0", TargetVersion: "0.172.0"}
			prefix := t.TempDir()
			bin := filepath.Join(prefix, "bin", "codex")
			ver := op.ExpectedVersion
			if written {
				ver = op.TargetVersion
			}
			writeFakeCLI(t, bin, ver)
			m := &n.maintenance
			m.mu.Lock()
			m.state.Managed["codex"] = op.ExpectedVersion
			m.state.Paths["codex"] = bin
			m.state.Pending["codex"] = bin
			m.state.Jobs = []*mv.ProviderOperation{{Request: op, Phase: "verifying"}}
			err = n.saveProviders()
			m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if err = n.Close(); err != nil {
				t.Fatal(err)
			}
			n, err = Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer n.Close()
			m = &n.maintenance
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.state.Managed["codex"] != ver || m.state.Paths["codex"] != bin || len(m.state.Pending) != 0 || m.state.Jobs[0].Phase != "interrupted" {
				t.Fatalf("recovered state: %+v", m.state)
			}
		})
	}
}

func TestProviderInterruptedActivationCustomCommand(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), Discoverers: map[string]AgentDiscoverer{}}
	n, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	op := &iv.MaintainProvider{RequestId: "qoder-001", Provider: "qoder", Action: "install", TargetVersion: "1.1.100"}
	prefix := t.TempDir()
	bin := filepath.Join(prefix, "bin", "qodercli")
	writeFakeCLI(t, bin, "1.1.100")
	m := &n.maintenance
	m.mu.Lock()
	m.state.Pending["qoder"] = bin
	m.state.Jobs = []*mv.ProviderOperation{{Request: op, Phase: "verifying"}}
	err = n.saveProviders()
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = n.Close(); err != nil {
		t.Fatal(err)
	}
	n, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	m = &n.maintenance
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Managed["qoder"] != "1.1.100" || m.state.Paths["qoder"] != bin {
		t.Fatalf("qoder custom command recovery failed: %+v", m.state)
	}
}

func TestResolveInstallPrefixAndNpmOwnership(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)

	// 1. Writable default npm prefix (e.g. /opt/homebrew or ~/.nvm): used directly.
	writableGlobal := filepath.Join(t.TempDir(), "global")
	if err := os.MkdirAll(filepath.Join(writableGlobal, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(writableGlobal, "lib", "node_modules"), 0755); err != nil {
		t.Fatal(err)
	}
	fakeNpm := filepath.Join(t.TempDir(), "npm")
	if err := os.WriteFile(fakeNpm, []byte("#!/bin/sh\necho \""+writableGlobal+"\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveInstallPrefix(ctx, fakeNpm, providerInstallEnv())
	if err != nil || !sameExecutable(got, writableGlobal) {
		t.Fatalf("expected writable global prefix %s, got %s (err=%v)", writableGlobal, got, err)
	}

	// 2. Non-writable default npm prefix (e.g. root-owned /usr/local): falls back to ~/.local.
	readOnlyGlobal := filepath.Join(t.TempDir(), "readonly")
	if err := os.MkdirAll(filepath.Join(readOnlyGlobal, "lib", "node_modules"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(readOnlyGlobal, "lib", "node_modules"), 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(readOnlyGlobal, "lib", "node_modules"), 0755)
	if err := os.WriteFile(fakeNpm, []byte("#!/bin/sh\necho \""+readOnlyGlobal+"\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	fallback, err := resolveInstallPrefix(ctx, fakeNpm, providerInstallEnv())
	wantFallback := filepath.Join(home, ".local")
	if err != nil || !sameExecutable(fallback, wantFallback) {
		t.Fatalf("expected fallback %s, got %s (err=%v)", wantFallback, fallback, err)
	}

	// 3. npmPackagePrefix recognizes npm global symlinks and ignores Homebrew Cellar binaries.
	pkgEntry := filepath.Join(writableGlobal, "lib", "node_modules", "@qoder-ai", "qodercli", "bin", "qodercli.js")
	writeFakeCLI(t, pkgEntry, "1.1.90")
	link := filepath.Join(writableGlobal, "bin", "qodercli")
	if err := os.Symlink(pkgEntry, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if p := npmPackagePrefix(link, "@qoder-ai/qodercli"); !sameExecutable(p, writableGlobal) {
		t.Fatalf("expected npm package prefix %s, got %s", writableGlobal, p)
	}
	cellarEntry := filepath.Join(writableGlobal, "Cellar", "codex", "0.171.0", "bin", "codex")
	writeFakeCLI(t, cellarEntry, "0.171.0")
	brewLink := filepath.Join(writableGlobal, "bin", "codex")
	if err := os.Symlink(cellarEntry, brewLink); err == nil {
		if p := npmPackagePrefix(brewLink, "@openai/codex"); p != "" {
			t.Fatalf("Homebrew Cellar symlink must not be claimed as npm package, got %s", p)
		}
	}
}
