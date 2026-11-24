package machine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/kernel"
	rt "github.com/hermeshai/hermesh/internal/runtime"
	"github.com/hermeshai/hermesh/internal/runtime/scripted"
	"github.com/hermeshai/hermesh/pkg/identity"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestStandaloneRunHotPairAndDemote(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "machine")

	// Headless machine without AllowUnpaired still refuses to run unpaired.
	strict, err := Open(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := strict.Run(context.Background()); err == nil {
		t.Fatal("expected unpaired machine without AllowUnpaired to refuse Run")
	}
	_ = strict.Close()

	// Embedded standalone machine runs unpaired with a default sanitized host name.
	cfg := Config{
		DataDir:       dir,
		AllowUnpaired: true,
		Drivers: map[string]rt.Driver{
			"scripted": scripted.NewDriver(),
		},
	}
	n, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	if n.Paired() {
		t.Fatal("fresh machine should not be paired")
	}
	if n.Name() == "" {
		t.Fatal("standalone machine should initialize a default name")
	}
	initialID := n.MachineID()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- n.Run(ctx) }()

	// Create a local session while unpaired.
	created, err := n.Sessions().StartSession(ctx, kernel.OwnerPrincipal("owner", "Owner"), &sessionv1.StartSessionRequest{
		CommandId:     "cmd-standalone-1",
		ProjectId:     "scratch",
		InitialPrompt: "hello from standalone",
		ModelSelection: &sessionv1.ModelSelection{
			InstanceId: "scripted",
			Model:      "scripted-default",
		},
	})
	if err != nil {
		t.Fatalf("StartSession unpaired: %v", err)
	}
	sessID := created.GetSession().GetSessionId()
	if sessID == "" || created.GetSession().GetMachineId() != initialID {
		t.Fatalf("unexpected session: %+v", created.GetSession())
	}

	// Hot-pair via Install while running; verify session and machine ID stay intact.
	rootKey, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	rootSPKI := identity.MarshalPublicKey(rootKey.Public())
	ownerID := identity.OwnerID(rootSPKI)
	rootCert, err := identity.Sign(&identityv1.RootCert{
		OwnerId:  ownerID,
		Version:  1,
		IssuedAt: timestamppb.Now(),
		RootKeys: []*identityv1.RootKey{{PublicKey: rootSPKI, Label: "mac", Platform: "macos"}},
	}, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	mCert, err := identity.Sign(&identityv1.MachineCert{
		OwnerId:          ownerID,
		MachineId:        initialID,
		MachinePublicKey: n.PublicKeySPKI(),
		IssuedAt:         timestamppb.Now(),
		DeviceKeyId:      identity.KeyID(rootKey.Public()),
	}, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Install(ownerID, "http://127.0.0.1:1", &identityv1.RootChain{Certs: []*identityv1.Signed{rootCert}}, mCert); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !n.Paired() || n.OwnerID() != ownerID || n.MachineID() != initialID {
		t.Fatalf("after Install: paired=%v owner=%q id=%q", n.Paired(), n.OwnerID(), n.MachineID())
	}

	// Wait briefly so mesh listeners start, then demote back to standalone in-place.
	time.Sleep(50 * time.Millisecond)
	newID, err := n.DemoteToLocal()
	if err != nil {
		t.Fatalf("DemoteToLocal: %v", err)
	}
	if newID == "" || newID == initialID {
		t.Fatalf("expected rotated machine ID, got %q (was %q)", newID, initialID)
	}
	if n.Paired() || n.OwnerID() != "" || n.RelayURL() != "" {
		t.Fatalf("machine still paired after DemoteToLocal: paired=%v owner=%q relay=%q", n.Paired(), n.OwnerID(), n.RelayURL())
	}
	if _, err := os.Stat(filepath.Join(dir, "keys", "retired", initialID+".key")); err != nil {
		t.Fatalf("retired key missing: %v", err)
	}

	// Existing session is preserved and rebound to newID.
	got, err := n.Sessions().GetSession(ctx, kernel.OwnerPrincipal("owner", "Owner"), sessID)
	if err != nil {
		t.Fatalf("GetSession after DemoteToLocal: %v", err)
	}
	if got.GetSession().GetMachineId() != newID {
		t.Fatalf("session MachineId = %q, want %q", got.GetSession().GetMachineId(), newID)
	}

	// Audit chain + retired checkpoint signatures verify cleanly.
	if records, err := n.VerifyAudit(); err != nil || records == 0 {
		t.Fatalf("VerifyAudit: records=%d err=%v", records, err)
	}

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit cleanly")
	}
}
