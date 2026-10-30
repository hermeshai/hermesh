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
)

// TestWindowsSigner: the Windows signer (cmd/hermesh-signer), built for
// tests with software keys that approve everything, makes a fresh owner
// role a trusted device over its pipe, and hermeshd tells of it in its
// status (docs/windows.md §7.3).
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
	start(t, "signer", nil, signerBin, "run", "--test", keys, "--socket", sock)

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
}
