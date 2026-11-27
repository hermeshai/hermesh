package machine

import (
	"context"
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
	for _, switched := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-rename", true: "after-rename"}[switched], func(t *testing.T) {
			cfg := Config{DataDir: t.TempDir(), Discoverers: map[string]AgentDiscoverer{}}
			n, err := Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			op := &iv.MaintainProvider{RequestId: "activation-001", Provider: "codex", Action: "update", ExpectedVersion: "0.171.0", TargetVersion: "0.172.0"}
			m := &n.maintenance
			root := filepath.Join(cfg.DataDir, "providers")
			if err = os.MkdirAll(filepath.Join(root, "bin"), 0700); err != nil {
				t.Fatal(err)
			}
			previous, staged := filepath.Join(root, "previous"), filepath.Join(root, "staged")
			dest := previous
			if switched {
				dest = staged
			}
			if err = os.Symlink(dest, filepath.Join(root, "bin", "codex")); err != nil {
				n.Close()
				t.Skipf("symlink unavailable: %v", err)
			}
			m.mu.Lock()
			m.state.Managed["codex"] = op.ExpectedVersion
			m.state.Paths["codex"] = previous
			m.state.Pending["codex"] = staged
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
			want := op.ExpectedVersion
			if switched {
				want = op.TargetVersion
			}
			if m.state.Managed["codex"] != want || m.state.Paths["codex"] != dest || len(m.state.Pending) != 0 || m.state.Jobs[0].Phase != "interrupted" {
				t.Fatalf("recovered state: %+v", m.state)
			}
		})
	}
}
