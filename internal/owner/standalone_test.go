package owner

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/session/v1/sessionv1connect"
	"github.com/hermeshai/hermesh/internal/machine"
	rt "github.com/hermeshai/hermesh/internal/runtime"
	"github.com/hermeshai/hermesh/internal/runtime/scripted"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/relay"
)

func TestStandaloneColdStartHotUpgradeAndSmoothLeave(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	d, err := Open(Config{
		DataDir: filepath.Join(dir, "owner"),
		MachineConfig: machine.Config{
			WorkspaceMode: "none",
			Drivers: map[string]rt.Driver{
				"scripted": scripted.NewDriver(),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		d.Run(runCtx)
	}()
	t.Cleanup(func() {
		cancelRun()
		<-runDone
		d.Close()
	})

	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	apiSrv := httptest.NewUnstartedServer(d.LocalHandler())
	apiSrv.Config.Protocols = &protocols
	apiSrv.Start()
	t.Cleanup(apiSrv.Close)

	lc := localv1connect.NewLocalServiceClient(apiSrv.Client(), apiSrv.URL)
	sc := sessionv1connect.NewSessionServiceClient(apiSrv.Client(), apiSrv.URL)
	ctx := context.Background()

	// 1. Cold start: zero login, zero relay, zero signer -> Standalone local machine online.
	var st *localv1.GetStatusResponse
	deadline := time.Now().Add(10 * time.Second)
	for {
		var sErr error
		st, sErr = lc.GetStatus(ctx, &localv1.GetStatusRequest{})
		if sErr == nil && st.LocalMachineEnabled && st.LocalMachineId != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("standalone machine did not start: st=%+v err=%v", st, sErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st.Initialized || st.MachineOnly || st.SignedOut || st.GetRelayHealth().GetPhase() != "local" {
		t.Fatalf("unexpected standalone status: %+v", st)
	}
	initialMachineID := st.LocalMachineId

	// Watch streams full standalone snapshot without Relay.
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	wstream, err := lc.Watch(wctx, &localv1.WatchRequest{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if !wstream.Receive() {
		t.Fatalf("Watch first frame failed: %v", wstream.Err())
	}
	first := wstream.Msg()
	wstream.Close()
	wcancel()
	if !first.Reset_ || first.GetStatus().GetRelayHealth().GetPhase() != "local" || len(first.Machines) != 1 || first.Machines[0].GetTunnel().GetPath() != "local" {
		t.Fatalf("unexpected Watch snapshot: %+v", first)
	}

	// Local machine management + SessionService work completely offline without Touch ID.
	if _, err := lc.RenameMachine(ctx, &localv1.RenameMachineRequest{MachineId: initialMachineID, MachineName: "solo-mac"}); err != nil {
		t.Fatalf("RenameMachine: %v", err)
	}
	if _, err := lc.AddProject(ctx, &localv1.AddProjectRequest{
		MachineId: initialMachineID,
		ProjectId: "demo",
		Path:      repoDir,
		Workspace: identityv1.Workspace_WORKSPACE_IN_PLACE,
	}); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	projs, err := lc.ListProjects(ctx, &localv1.ListProjectsRequest{})
	if err != nil || len(projs.Projects) != 1 || projs.Projects[0].ProjectId != "demo" {
		t.Fatalf("ListProjects: %+v err=%v", projs, err)
	}

	started, err := sc.StartSession(ctx, &sessionv1.StartSessionRequest{
		ProjectId: "demo",
		ModelSelection: &sessionv1.ModelSelection{
			InstanceId: "scripted",
			Model:      "scripted-default",
		},
		InitialPrompt: "hello offline world",
	})
	if err != nil {
		t.Fatalf("StartSession in standalone: %v", err)
	}
	sessID := started.GetSession().GetSessionId()
	for time.Now().Before(time.Now().Add(5 * time.Second)) {
		got, gErr := sc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: sessID})
		if gErr == nil && got.GetProjection().GetSession().GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_IDLE {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 2. Multi-profile isolation: AddProfile opens empty sign-in profile; SwitchProfile returns to standalone.
	profilesBefore, err := lc.ListProfiles(ctx, &localv1.ListProfilesRequest{})
	if err != nil || len(profilesBefore.Profiles) != 1 {
		t.Fatalf("ListProfiles before AddProfile: %+v err=%v", profilesBefore, err)
	}
	standaloneProfileID := profilesBefore.Profiles[0].Id
	if _, err := lc.AddProfile(ctx, &localv1.AddProfileRequest{}); err != nil {
		t.Fatalf("AddProfile: %v", err)
	}
	addedSt, err := lc.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil || addedSt.Initialized || addedSt.LocalMachineEnabled || addedSt.GetRelayHealth().GetPhase() != "idle" {
		t.Fatalf("added profile status should be empty sign-in: %+v err=%v", addedSt, err)
	}
	if _, err := lc.SwitchProfile(ctx, &localv1.SwitchProfileRequest{Id: standaloneProfileID}); err != nil {
		t.Fatalf("SwitchProfile back to standalone: %v", err)
	}
	profilesAfter, err := lc.ListProfiles(ctx, &localv1.ListProfilesRequest{})
	if err != nil || len(profilesAfter.Profiles) != 1 || profilesAfter.Profiles[0].Id != standaloneProfileID {
		t.Fatalf("abandoned empty profile should not be listed: %+v err=%v", profilesAfter, err)
	}
	for {
		st, _ = lc.GetStatus(ctx, &localv1.GetStatusRequest{})
		if st.GetLocalMachineEnabled() && st.GetLocalMachineId() == initialMachineID {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 3. Hot-upgrade via Setup: single signing batch, same MachineId, active/existing sessions preserved.
	rs, err := relay.New(relay.Config{DataDir: filepath.Join(dir, "relay1")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rs.Close() })
	relaySrv := httptest.NewServer(rs.Handler())
	t.Cleanup(relaySrv.Close)
	claim, err := rs.ClaimCode()
	if err != nil {
		t.Fatal(err)
	}

	rootKey, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	rootKeyID := identity.KeyID(rootKey.Public())
	var signedTypes []string
	var deletedKeys []string
	signCtx, cancelSign := context.WithCancel(ctx)
	t.Cleanup(cancelSign)
	go func() {
		stream, sErr := lc.WatchSigningRequests(signCtx, &localv1.WatchSigningRequestsRequest{})
		if sErr != nil {
			return
		}
		defer stream.Close()
		for stream.Receive() {
			req := stream.Msg()
			if req.DeleteRootKey {
				deletedKeys = append(deletedKeys, req.RootKeyId)
				_, _ = lc.CompleteSigningRequest(signCtx, &localv1.CompleteSigningRequestRequest{RequestId: req.RequestId})
				continue
			}
			if !bytes.Equal(req.Message, identity.SigningInput(req.Type, req.Body)) {
				_, _ = lc.CompleteSigningRequest(signCtx, &localv1.CompleteSigningRequestRequest{RequestId: req.RequestId, Rejected: true})
				continue
			}
			signedTypes = append(signedTypes, req.Type)
			sig, _ := rootKey.SignMessage(req.Message)
			_, _ = lc.CompleteSigningRequest(signCtx, &localv1.CompleteSigningRequestRequest{RequestId: req.RequestId, Signature: sig})
		}
	}()

	setupRes, err := lc.Setup(ctx, &localv1.SetupRequest{
		RelayUrl:         relaySrv.URL,
		ClaimCode:        claim,
		RootPublicKey:    identity.MarshalPublicKey(rootKey.Public()),
		RootLabel:        "test-mac",
		RootPlatform:     "macos",
		LocalMachineName: "cloud-mac",
	})
	if err != nil {
		t.Fatalf("Setup hot-upgrade: %v", err)
	}
	wantBatch := []string{"hermesh.identity.v1.RootCert", "hermesh.identity.v1.OwnerDeviceCert", "hermesh.identity.v1.MachineCert"}
	if !slices.Equal(signedTypes, wantBatch) {
		t.Fatalf("Setup signed %v, want %v", signedTypes, wantBatch)
	}
	upgradedSt, err := lc.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil || !upgradedSt.Initialized || upgradedSt.OwnerId != setupRes.OwnerId || upgradedSt.LocalMachineId != initialMachineID || upgradedSt.LocalMachineName != "cloud-mac" {
		t.Fatalf("status after Setup: %+v err=%v", upgradedSt, err)
	}

	// 4. Smooth in-place Leave (solo device): revokes on relay, deletes root key, rotates machine key, keeps local session.
	if _, err := lc.Leave(ctx, &localv1.LeaveRequest{}); err != nil {
		t.Fatalf("Leave solo device: %v", err)
	}
	leftSt, err := lc.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if leftSt.Initialized || !leftSt.LocalMachineEnabled || leftSt.GetRelayHealth().GetPhase() != "local" {
		t.Fatalf("status after Leave: %+v", leftSt)
	}
	if leftSt.LocalMachineId == "" || leftSt.LocalMachineId == initialMachineID {
		t.Fatalf("expected rotated LocalMachineId after Leave, got %q (was %q)", leftSt.LocalMachineId, initialMachineID)
	}
	if !slices.Equal(deletedKeys, []string{rootKeyID}) {
		t.Fatalf("expected root key %s deleted on Leave, got %v", rootKeyID, deletedKeys)
	}

	// Local session created before upgrade+leave is still accessible (both live and CachedOnly) and rebound to new MachineId.
	for _, cachedOnly := range []bool{false, true} {
		got, err := sc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: sessID, CachedOnly: cachedOnly})
		if err != nil {
			t.Fatalf("GetSession(cachedOnly=%v) after Leave: %v", cachedOnly, err)
		}
		if got.GetProjection().GetSession().GetMachineId() != leftSt.LocalMachineId {
			t.Fatalf("GetSession(cachedOnly=%v) MachineId = %q, want %q", cachedOnly, got.GetProjection().GetSession().GetMachineId(), leftSt.LocalMachineId)
		}
	}

	// 5. Force-local escape hatch when multi-device owner's relay is dead.
	rs2, err := relay.New(relay.Config{DataDir: filepath.Join(dir, "relay2")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rs2.Close() })
	relaySrv2 := httptest.NewServer(rs2.Handler())
	claim2, _ := rs2.ClaimCode()
	if _, err := lc.Setup(ctx, &localv1.SetupRequest{
		RelayUrl:         relaySrv2.URL,
		ClaimCode:        claim2,
		RootPublicKey:    identity.MarshalPublicKey(rootKey.Public()),
		RootLabel:        "mac-1",
		RootPlatform:     "macos",
		LocalMachineName: "mac-1",
	}); err != nil {
		t.Fatalf("second Setup: %v", err)
	}
	// Simulate a second trusted device in the root chain so non-forced Leave refuses, while ForceLocal succeeds offline.
	root2, _ := identity.GenerateKey()
	rc2, err := identity.Sign(&identityv1.RootCert{
		OwnerId:  identity.OwnerID(identity.MarshalPublicKey(rootKey.Public())),
		Version:  2,
		IssuedAt: timestamppb.Now(),
		RootKeys: []*identityv1.RootKey{
			{PublicKey: identity.MarshalPublicKey(rootKey.Public()), Label: "mac-1"},
			{PublicKey: identity.MarshalPublicKey(root2.Public()), Label: "iphone-2"},
		},
	}, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	d.owner().adoptChain(&identityv1.RootChain{Certs: append(d.owner().trust.RootChain().Certs, rc2)})

	if _, err := lc.Leave(ctx, &localv1.LeaveRequest{ForceLocal: false}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("expected multi-device Leave(force=false) to fail with FailedPrecondition, got %v", err)
	}
	relaySrv2.Close()
	startForce := time.Now()
	if _, err := lc.Leave(ctx, &localv1.LeaveRequest{ForceLocal: true}); err != nil {
		t.Fatalf("Leave(ForceLocal=true) with dead relay: %v", err)
	}
	if elapsed := time.Since(startForce); elapsed > 2*time.Second {
		t.Fatalf("Leave(ForceLocal=true) took %v, expected < 2s", elapsed)
	}
	finalSt, _ := lc.GetStatus(ctx, &localv1.GetStatusRequest{})
	if finalSt.Initialized || !finalSt.LocalMachineEnabled || finalSt.GetRelayHealth().GetPhase() != "local" {
		t.Fatalf("final status after ForceLocal Leave: %+v", finalSt)
	}
}

func TestLocalModeMaintainProviderAndHer(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(Config{
		DataDir: filepath.Join(dir, "owner"),
		MachineConfig: machine.Config{
			WorkspaceMode: "none",
			Drivers: map[string]rt.Driver{
				"scripted": scripted.NewDriver(),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		d.Run(runCtx)
	}()
	t.Cleanup(func() {
		cancelRun()
		<-runDone
		d.Close()
	})

	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	apiSrv := httptest.NewUnstartedServer(d.LocalHandler())
	apiSrv.Config.Protocols = &protocols
	apiSrv.Start()
	t.Cleanup(apiSrv.Close)

	lc := localv1connect.NewLocalServiceClient(apiSrv.Client(), apiSrv.URL)
	ctx := context.Background()

	var localID string
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, sErr := lc.GetStatus(ctx, &localv1.GetStatusRequest{})
		if sErr == nil && st.LocalMachineEnabled && st.LocalMachineId != "" {
			localID = st.LocalMachineId
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("standalone machine did not start: st=%+v err=%v", st, sErr)
		}
		time.Sleep(50 * time.Millisecond)
	}

	assertLocalOps := func(label, reqID string) {
		t.Helper()
		// Targeting another machine must be rejected locally.
		if _, err := lc.MaintainProvider(ctx, &localv1.MaintainProviderRequest{
			MachineId: "m_other",
			Operation: &identityv1.MaintainProvider{RequestId: reqID + "-other", Provider: "codex", Action: "verify"},
		}); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("%s MaintainProvider other machine: got %v, want NotFound", label, err)
		}
		if _, err := lc.MaintainProvider(ctx, &localv1.MaintainProviderRequest{
			MachineId: localID,
			Operation: &identityv1.MaintainProvider{RequestId: reqID, Provider: "codex", Action: "verify"},
		}); err != nil {
			t.Fatalf("%s MaintainProvider local: %v", label, err)
		}
		waitOp := time.Now().Add(5 * time.Second)
		for {
			ms, mErr := lc.ListMachines(ctx, &localv1.ListMachinesRequest{})
			found := false
			if mErr == nil && len(ms.Machines) == 1 {
				for _, op := range ms.Machines[0].GetStatus().GetProviderOperations() {
					if op.GetRequest().GetRequestId() == reqID && (op.Phase == "succeeded" || op.Phase == "failed") {
						found = true
					}
				}
			}
			if found {
				break
			}
			if time.Now().After(waitOp) {
				t.Fatalf("%s provider op %s did not finish", label, reqID)
			}
			time.Sleep(50 * time.Millisecond)
		}

		if _, err := lc.SetHer(ctx, &localv1.SetHerRequest{MachineId: localID, Enabled: true}); err != nil {
			t.Fatalf("%s SetHer(true): %v", label, err)
		}
		if ms, err := lc.ListMachines(ctx, &localv1.ListMachinesRequest{}); err != nil || len(ms.Machines) != 1 || !ms.Machines[0].GetStatus().GetHer().GetEnabled() {
			t.Fatalf("%s Her not enabled: %+v err=%v", label, ms, err)
		}
		if _, err := lc.SetHer(ctx, &localv1.SetHerRequest{MachineId: localID, Enabled: false}); err != nil {
			t.Fatalf("%s SetHer(false): %v", label, err)
		}
		if ms, err := lc.ListMachines(ctx, &localv1.ListMachinesRequest{}); err != nil || len(ms.Machines) != 1 || ms.Machines[0].GetStatus().GetHer().GetEnabled() {
			t.Fatalf("%s Her not disabled: %+v err=%v", label, ms, err)
		}
		// Command connector on local machine takes local path (fails with Her not running, never asks signer).
		if _, err := lc.SetHerCommandConnector(ctx, &localv1.SetHerCommandConnectorRequest{
			MachineId: localID,
			Connector: &identityv1.SetHerCommandConnector{Name: "github", Command: "npx", Args: []string{"-y", "server-github"}},
		}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("%s SetHerCommandConnector: got %v, want FailedPrecondition (Her not running)", label, err)
		}
	}

	// 1. Uninitialized standalone mode (no signer connected).
	assertLocalOps("standalone", "verify-standalone-001")

	// 2. Setup, then SignOut: local machine stays in local-only mode and MUST NOT ask the signer.
	rs, err := relay.New(relay.Config{DataDir: filepath.Join(dir, "relay")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rs.Close() })
	relaySrv := httptest.NewServer(rs.Handler())
	t.Cleanup(relaySrv.Close)
	claim, err := rs.ClaimCode()
	if err != nil {
		t.Fatal(err)
	}

	rootKey, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	var allowSign atomic.Bool
	allowSign.Store(true)
	unexpectedSign := make(chan string, 8)
	signCtx, cancelSign := context.WithCancel(ctx)
	t.Cleanup(cancelSign)
	go func() {
		stream, sErr := lc.WatchSigningRequests(signCtx, &localv1.WatchSigningRequestsRequest{})
		if sErr != nil {
			return
		}
		defer stream.Close()
		for stream.Receive() {
			req := stream.Msg()
			if !allowSign.Load() {
				unexpectedSign <- req.Type
				_, _ = lc.CompleteSigningRequest(signCtx, &localv1.CompleteSigningRequestRequest{RequestId: req.RequestId, Rejected: true})
				continue
			}
			sig, _ := rootKey.SignMessage(req.Message)
			_, _ = lc.CompleteSigningRequest(signCtx, &localv1.CompleteSigningRequestRequest{RequestId: req.RequestId, Signature: sig})
		}
	}()

	if _, err := lc.Setup(ctx, &localv1.SetupRequest{
		RelayUrl:         relaySrv.URL,
		ClaimCode:        claim,
		RootPublicKey:    identity.MarshalPublicKey(rootKey.Public()),
		RootLabel:        "test-mac",
		RootPlatform:     "macos",
		LocalMachineName: "test-mac",
	}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if _, err := lc.SignOut(ctx, &localv1.SignOutRequest{}); err != nil {
		t.Fatalf("SignOut: %v", err)
	}
	allowSign.Store(false)

	assertLocalOps("signed-out", "verify-signedout-001")

	// Direct signRoot while signed out must fail with FailedPrecondition without asking signer.
	if _, err := d.owner().signRoot(ctx, &identityv1.AdminCommand{TargetMachineId: localID}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("signRoot while signed out: got %v, want FailedPrecondition", err)
	}
	select {
	case typ := <-unexpectedSign:
		t.Fatalf("signed-out local operation triggered unexpected signing request: %s", typ)
	default:
	}
}
