package owner

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/kernel"
	kstore "github.com/hermeshai/hermesh/internal/kernel/store"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/relay"
)

// A finished turn's ResolveFiles answer is immutable once its checkpoint exists:
// a remote device (NoLocalMachine, as on the iPhone) serves repeat ResolveFiles
// calls for that turn from session_files_kept without asking the online machine
// again, including replies whose inline-code paths were all missing; a turn still
// running (no checkpoint yet) is asked again as files appear.
func TestResolveFilesKeptForFinishedTurn(t *testing.T) {
	dir := t.TempDir()
	st, err := relay.OpenStore(filepath.Join(dir, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	root, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	rootPub := identity.MarshalPublicKey(root.Public())
	ownerID := identity.OwnerID(rootPub)
	rc, err := identity.Sign(&identityv1.RootCert{
		OwnerId:  ownerID,
		Version:  1,
		IssuedAt: timestamppb.Now(),
		RootKeys: []*identityv1.RootKey{{PublicKey: rootPub, Label: "root"}},
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	chain := &identityv1.RootChain{Certs: []*identityv1.Signed{rc}}

	devKey, _ := identity.GenerateKey()
	devCert, err := identity.Sign(&identityv1.OwnerDeviceCert{
		OwnerId:         ownerID,
		DevicePublicKey: identity.MarshalPublicKey(devKey.Public()),
		Label:           "iPhone",
		IssuedAt:        timestamppb.Now(),
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveClaim(ownerID, chain, devCert); err != nil {
		t.Fatal(err)
	}

	mach, err := machine.Open(machine.Config{DataDir: filepath.Join(dir, "machine"), WorkspaceMode: "none"})
	if err != nil {
		t.Fatal(err)
	}
	machID := mach.MachineID()
	machCert, err := identity.Sign(&identityv1.MachineCert{
		OwnerId:          ownerID,
		MachineId:        machID,
		MachinePublicKey: mach.PublicKeySPKI(),
		IssuedAt:         timestamppb.Now(),
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutMachineCert(ownerID, machID, machCert); err != nil {
		t.Fatal(err)
	}

	rs, err := relay.New(relay.Config{Backend: st})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rs.Close() })
	srv := httptest.NewServer(rs.Handler())
	t.Cleanup(srv.Close)

	if err := mach.Install(ownerID, srv.URL, chain, machCert); err != nil {
		t.Fatal(err)
	}
	machCtx, machCancel := context.WithCancel(context.Background())
	machDone := make(chan struct{})
	go func() {
		defer close(machDone)
		_ = mach.Run(machCtx)
	}()
	t.Cleanup(func() {
		machCancel()
		<-machDone
		mach.Close()
	})

	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "docs", "demo.html"), []byte("<p>v1</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"add", "."},
		{"commit", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	eng := mach.Sessions()
	if err := eng.SyncWorkspace("demo", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sid := "s_" + kernel.SessionPrefix(machID) + "_01jtestsessionfiles0000001"
	cp1, err := eng.VCS().CaptureCheckpoint(ctx, repo, "cp_1", sid, "run_1", 1, "")
	if err != nil || cp1 == nil {
		t.Fatalf("CaptureCheckpoint: %v %+v", err, cp1)
	}
	sess := &sessionv1.Session{
		SessionId: sid,
		MachineId: machID,
		ProjectId: "demo",
		Status:    sessionv1.SessionStatus_SESSION_STATUS_IDLE,
		CreatedBy: ownerID,
	}
	if _, err := eng.Store().Mutate(func(tx *kstore.Tx) error {
		if err := tx.PutSession(sess); err != nil {
			return err
		}
		return tx.PutCheckpoint(cp1)
	}); err != nil {
		t.Fatal(err)
	}

	d, err := Open(Config{DataDir: filepath.Join(dir, "iphone"), NoLocalMachine: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	o := d.owner()
	if err := o.saveIdentity(ownerID, srv.URL, rootPub, chain, devKey, devCert, ""); err != nil {
		t.Fatal(err)
	}
	o.cacheSession(sess, &sessionv1.SessionProjection{
		Session:     sess,
		Checkpoints: []*sessionv1.Checkpoint{cp1},
	})
	svc := &ownerSessionService{d: d}

	// Wait until the remote machine is reachable over the relay.
	var first *sessionv1.ResolveFilesResponse
	for range 50 {
		first, err = svc.ResolveFiles(ctx, &sessionv1.ResolveFilesRequest{
			SessionId: sid,
			RunId:     "run_1",
			Paths:     []string{"docs/demo.html", "gone.md"},
		})
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("initial ResolveFiles: %v", err)
	}
	if len(first.GetFiles()) != 2 || first.Files[0].Kind != sessionv1.FileRefKind_FILE_REF_KIND_FILE || first.Files[0].Turn != 1 {
		t.Fatalf("initial ResolveFiles = %+v", first)
	}

	firstRead, err := svc.ReadFile(ctx, &sessionv1.ReadFileRequest{
		SessionId: sid,
		RunId:     "run_1",
		Path:      "docs/demo.html",
	})
	if err != nil || firstRead.GetText() != "<p>v1</p>" || firstRead.GetTurn() != 1 {
		t.Fatalf("initial ReadFile = %+v, err=%v", firstRead, err)
	}

	// A finished turn whose inline-code candidates are all missing (e.g. `fmt.Println`) is also settled.
	missOnly, err := svc.ResolveFiles(ctx, &sessionv1.ResolveFilesRequest{
		SessionId: sid,
		RunId:     "run_1",
		Paths:     []string{"fmt.Println", "res.files"},
	})
	if err != nil || len(missOnly.GetFiles()) != 2 || missOnly.Files[0].Kind != sessionv1.FileRefKind_FILE_REF_KIND_MISSING {
		t.Fatalf("missing-only ResolveFiles = %+v, err=%v", missOnly, err)
	}

	// A live turn with no checkpoint yet must NOT freeze a missing answer once the file is written.
	liveBefore, err := svc.ResolveFiles(ctx, &sessionv1.ResolveFilesRequest{
		SessionId: sid,
		RunId:     "run_live",
		Paths:     []string{"docs/live.html"},
	})
	if err != nil || liveBefore.Files[0].Kind != sessionv1.FileRefKind_FILE_REF_KIND_MISSING {
		t.Fatalf("live before write = %+v, err=%v", liveBefore, err)
	}
	if err := os.WriteFile(filepath.Join(repo, "docs", "live.html"), []byte("<p>live</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	liveAfter, err := svc.ResolveFiles(ctx, &sessionv1.ResolveFilesRequest{
		SessionId: sid,
		RunId:     "run_live",
		Paths:     []string{"docs/live.html"},
	})
	if err != nil || liveAfter.Files[0].Kind != sessionv1.FileRefKind_FILE_REF_KIND_FILE {
		t.Fatalf("live after write = %+v, err=%v", liveAfter, err)
	}

	// A blob stored on the remote machine is downloadable by session_id alone (as iPhone and Mac
	// user-message thumbnails ask for it) and kept locally afterwards.
	blobRef, err := eng.Store().PutBlob("shot.png", "image/png", []byte("png-data"))
	if err != nil {
		t.Fatal(err)
	}
	dl, err := svc.DownloadBlob(ctx, &sessionv1.DownloadBlobRequest{
		SessionId:  sid,
		BlobSha256: blobRef.BlobSha256,
	})
	if err != nil || string(dl.GetData()) != "png-data" || dl.GetMimeType() != "image/png" {
		t.Fatalf("remote DownloadBlob by session_id = %+v, err=%v", dl, err)
	}

	// Remove the session on the STILL-ONLINE machine: if Owner dials the machine again for run_1,
	// the machine returns NotFound instead of an offline error; kept finished-turn answers must win locally.
	if err := eng.DeleteSession(ctx, kernel.OwnerPrincipal(ownerID, "Owner"), sid); err != nil {
		t.Fatal(err)
	}

	// Once downloaded, small attachment blobs stay readable from local sessions_blobs without re-dialing.
	dlCached, err := svc.DownloadBlob(ctx, &sessionv1.DownloadBlobRequest{
		SessionId:  sid,
		BlobSha256: blobRef.BlobSha256,
	})
	if err != nil || string(dlCached.GetData()) != "png-data" {
		t.Fatalf("cached DownloadBlob = %+v, err=%v", dlCached, err)
	}

	cachedHit, err := svc.ResolveFiles(ctx, &sessionv1.ResolveFilesRequest{
		SessionId: sid,
		RunId:     "run_1",
		Paths:     []string{"docs/demo.html", "gone.md"},
	})
	if err != nil || len(cachedHit.GetFiles()) != 2 || cachedHit.Files[0].Kind != sessionv1.FileRefKind_FILE_REF_KIND_FILE {
		t.Fatalf("expected local kept answer for finished turn with files while machine online, got %+v, err=%v", cachedHit, err)
	}

	cachedMissOnly, err := svc.ResolveFiles(ctx, &sessionv1.ResolveFilesRequest{
		SessionId: sid,
		RunId:     "run_1",
		Paths:     []string{"fmt.Println", "res.files"},
	})
	if err != nil || len(cachedMissOnly.GetFiles()) != 2 || cachedMissOnly.Files[0].Kind != sessionv1.FileRefKind_FILE_REF_KIND_MISSING {
		t.Fatalf("expected local kept answer for finished turn with missing-only paths while machine online, got %+v, err=%v", cachedMissOnly, err)
	}

	cachedRead, err := svc.ReadFile(ctx, &sessionv1.ReadFileRequest{
		SessionId: sid,
		RunId:     "run_1",
		Path:      "docs/demo.html",
	})
	if err != nil || cachedRead.GetText() != "<p>v1</p>" || cachedRead.GetTurn() != 1 {
		t.Fatalf("expected local kept ReadFile answer for settled turn while machine online, got %+v, err=%v", cachedRead, err)
	}
}
