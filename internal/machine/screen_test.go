package machine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	screenv1 "github.com/hermeshai/hermesh/gen/go/hermesh/screen/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/screen/v1/screenv1connect"
	"github.com/hermeshai/hermesh/internal/provider"
)

// Computer Control is the owner's devices' alone: agents are refused its
// service as they are the admin calls, and their view of the machine's
// status says nothing of it. A machine without the helper offers no screen.
func TestScreenIsTheOwnersOnly(t *testing.T) {
	if !isAdminProcedure(screenv1connect.ScreenServiceConnectProcedure) {
		t.Fatal("agents may call ScreenService")
	}
	helper := filepath.Join(t.TempDir(), "hermesh-screen")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	n, err := Open(Config{DataDir: t.TempDir(), ScreenBin: helper, Providers: map[string]provider.Provider{}})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if st := n.statusFor(&Principal{Kind: PrincipalOwner}).Screen; st == nil || !st.Available || st.Viewer != "" {
		t.Fatalf("the owner's view: %v", st)
	}
	if st := n.statusFor(&Principal{Kind: PrincipalClient}).Screen; st != nil {
		t.Fatalf("an agent's view: %v", st)
	}

	without, err := Open(Config{DataDir: t.TempDir(), Providers: map[string]provider.Provider{}})
	if err != nil {
		t.Fatal(err)
	}
	defer without.Close()
	if st := without.statusFor(&Principal{Kind: PrincipalOwner}).Screen; st != nil {
		t.Fatalf("no helper, yet: %v", st)
	}
	if _, err := without.screen.Connect(context.Background(), &screenv1.ConnectRequest{Offer: "v=0"}); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("no helper, yet Connect: %v", err)
	}
}
