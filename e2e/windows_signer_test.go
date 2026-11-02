package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// TestWindowsSigner: the Windows signer (cmd/hermesh-signer), built for
// tests with software keys that approve everything, makes a fresh owner
// role a trusted device over its pipe, and hermeshd tells of it in its
// status (docs/windows.md §7.3). Then an iPhone joins, approved here: the
// signer shows the two digits to type on it, and they let it in.
func TestWindowsSigner(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	if runtime.GOOS != "windows" {
		t.Skip("the Windows signer")
	}
	bin := binaries(t)
	signerBin := filepath.Join(t.TempDir(), "hermesh-signer.exe")
	if out, err := exec.Command("go", "build", "-tags", "testsigner", "-o", signerBin, "./cmd/hermesh-signer").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	tmp := t.TempDir()
	port := freePort(t)
	relayData := filepath.Join(tmp, "relay")
	relay := start(t, "relay", nil, filepath.Join(bin, "hermesh-relay"), "serve", "--data-dir", relayData, "--listen", fmt.Sprintf("127.0.0.1:%d", port))
	eventually(t, 20*time.Second, "relay up", func() bool { return strings.Contains(relay.output(), "relay listening") })
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, filepath.Join(bin, "hermesh-relay"), "claim-code", "--data-dir", relayData).stdout)
	sockDir, _ := os.MkdirTemp(shortTemp, "hm")
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "o.sock")
	start(t, "owner", nil, filepath.Join(bin, "hermeshd"), "run", "--owner", "--no-machine", "--data-dir", filepath.Join(tmp, "owner"), "--socket", sock)
	keys := filepath.Join(tmp, "keys")
	signerProc := start(t, "signer", nil, signerBin, "run", "--test", keys, "--socket", sock)

	c := localClient(t, sock)
	ctx := context.Background()
	eventually(t, 20*time.Second, "the signer watching", func() bool {
		st, err := c.GetStatus(ctx, &localv1.GetStatusRequest{})
		return err == nil && st.Signer.GetConnected()
	})
	if _, err := c.Setup(ctx, &localv1.SetupRequest{RelayUrl: fmt.Sprintf("http://127.0.0.1:%d", port), ClaimCode: claim, RootLabel: "PC", RootPlatform: "windows"}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	st, err := c.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil || !st.Initialized || st.Signer.GetBusy() {
		t.Fatalf("after setup: %v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(keys, st.DeviceKeyId+".pem")); err != nil {
		t.Fatalf("the root key is not the signer's: %v", err)
	}

	sock2 := filepath.Join(sockDir, "o2.sock")
	start(t, "owner iphone", nil, filepath.Join(bin, "hermeshd"), "run", "--owner", "--no-machine", "--data-dir", filepath.Join(tmp, "iphone"), "--socket", sock2)
	root2, _ := identity.GenerateKey()
	phone := newTestApp(t, sock2, root2)
	join, err := phone.client.RequestJoin(ctx, &localv1.RequestJoinRequest{RelayUrl: fmt.Sprintf("http://127.0.0.1:%d", port), OwnerId: st.OwnerId,
		RootPublicKey: rootPub(root2), RootLabel: "iPhone", RootPlatform: "ios"})
	if err != nil {
		t.Fatalf("request join: %v", err)
	}
	eventually(t, 15*time.Second, "the join request here", func() bool {
		res, err := c.ListPairingRequests(ctx, &localv1.ListPairingRequestsRequest{})
		return err == nil && len(res.Pairings) == 1 && res.Pairings[0].Code == join.Code
	})
	approval, err := c.ApproveDevicePairing(ctx, &localv1.ApproveDevicePairingRequest{Code: join.Code})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	var shown string
	eventually(t, 10*time.Second, "the signer shows the digits", func() bool {
		_, after, ok := strings.Cut(signerProc.output(), "HERMESH_SIGNER_MATCH=")
		shown, _, _ = strings.Cut(after, "\n")
		return ok && strings.Contains(after, "\n")
	})
	digits, label, _ := strings.Cut(strings.TrimSpace(shown), " ")
	if digits != approval.MatchCode || label != "iPhone" {
		t.Fatalf("the signer shows %q; hermeshd says %s", shown, approval.MatchCode)
	}
	eventually(t, 60*time.Second, "the approval reaches the iPhone", func() bool {
		w, err := phone.client.WaitJoin(ctx, &localv1.WaitJoinRequest{Code: join.Code})
		return err == nil && w.Done
	})
	if _, err := phone.client.ConfirmJoin(ctx, &localv1.ConfirmJoinRequest{Code: join.Code, Accept: true, MatchCode: digits}); err != nil {
		t.Fatalf("the digits the signer showed: %v", err)
	}
	if st2, err := phone.client.GetStatus(ctx, &localv1.GetStatusRequest{}); err != nil || st2.OwnerId != st.OwnerId {
		t.Fatalf("the iPhone did not join: %v %v", st2, err)
	}
}
