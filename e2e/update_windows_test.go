package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// TestWindowsUpdate is docs/machine-updates.md §7 on a Windows machine
// that install.ps1 set up, with its logon task, and three signed releases
// A < B < C served by the test as GitHub serves them:
//
//  1. install.ps1 installs A and pairs the machine;
//  2. B is published: the machine finds it, installs it beside its
//     running programs, and its supervisor starts B;
//  3. with automatic updates off, C is published: nothing happens until
//     `hermeshd update --yes`, which installs C and restarts the task.
func TestWindowsUpdate(t *testing.T) {
	bin := binaries(t)
	tmp := t.TempDir()
	ctx := context.Background()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	releasesURL := fmt.Sprintf("http://%s/releases", ln.Addr())
	var mu sync.Mutex
	latest := ""
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		l := latest
		mu.Unlock()
		path := strings.Replace(r.URL.Path, "/releases/latest/download/", "/releases/download/v"+l+"/", 1)
		version, name, ok := strings.Cut(strings.TrimPrefix(path, "/releases/download/v"), "/")
		if !ok || l == "" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(tmp, "releases", version, filepath.Base(name)))
	}))
	t.Cleanup(func() { ln.Close() })
	publish := func(v string) {
		mu.Lock()
		latest = v
		mu.Unlock()
	}

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	script, err := os.ReadFile("scripts/install.ps1")
	if err != nil {
		t.Fatal(err)
	}
	pub := strings.TrimSpace(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})))
	for _, v := range []string{"0.0.1", "0.0.2", "0.0.3"} {
		dir := filepath.Join(tmp, "releases", v)
		os.MkdirAll(dir, 0o755)
		// As scripts/release.sh builds a release, but naming the test's
		// releases and key.
		ldflags := "-X github.com/hermeshai/hermesh/internal/machine.Version=" + v +
			" -X github.com/hermeshai/hermesh/internal/update.releases=" + releasesURL +
			" -X github.com/hermeshai/hermesh/internal/update.publicKey=" + base64.StdEncoding.EncodeToString(der) +
			" -X github.com/hermeshai/hermesh/internal/update.testInterval=2s"
		var sums bytes.Buffer
		for _, b := range []struct{ name, cmd, flags string }{
			{"hermeshd", "./cmd/hermeshd", ""},
			{"hermeshdw", "./cmd/hermeshd", " -H windowsgui"},
			{"hermesh", "./cmd/hermesh", ""},
		} {
			asset := b.name + "-windows-" + runtime.GOARCH + ".exe"
			cmd := exec.Command("go", "build", "-ldflags", ldflags+b.flags, "-o", filepath.Join(dir, asset), b.cmd)
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("build %s %s: %v %s", b.name, v, err, out)
			}
			raw, _ := os.ReadFile(filepath.Join(dir, asset))
			h := sha256.Sum256(raw)
			fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), asset)
		}
		h := sha256.Sum256([]byte(v + "\n"))
		fmt.Fprintf(&sums, "%s  version\n", hex.EncodeToString(h[:]))
		digest := sha256.Sum256(sums.Bytes())
		sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
		for name, body := range map[string][]byte{
			"version":           []byte(v + "\n"),
			"checksums.txt":     sums.Bytes(),
			"checksums.txt.sig": sig,
			"install.ps1":       bytes.Replace(script, []byte("__HERMESH_RELEASE_PUBKEY__"), []byte(pub), 1),
		} {
			os.WriteFile(filepath.Join(dir, name), body, 0o644)
		}
	}
	publish("0.0.1")

	// The owner, on a relay here.
	port := freePort(t)
	relayData := filepath.Join(tmp, "relay")
	relay := start(t, "relay", nil, filepath.Join(bin, "hermesh-relay"), "serve", "--data-dir", relayData, "--listen", fmt.Sprintf("127.0.0.1:%d", port))
	eventually(t, 20*time.Second, "relay up", func() bool { return strings.Contains(relay.output(), "relay listening") })
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, filepath.Join(bin, "hermesh-relay"), "claim-code", "--data-dir", relayData).stdout)
	sock := filepath.Join(tmp, "o.sock")
	start(t, "owner", nil, filepath.Join(bin, "hermeshd"), "run", "--owner", "--data-dir", filepath.Join(tmp, "owner"), "--socket", sock)
	root, _ := identity.GenerateKey()
	app := newTestApp(t, sock, root)
	setup, err := app.client.Setup(ctx, &localv1.SetupRequest{RelayUrl: fmt.Sprintf("http://127.0.0.1:%d", port), ClaimCode: claim, RootPublicKey: rootPub(root)})
	if err != nil {
		t.Fatal(err)
	}

	// 1. install.ps1, as a person runs it: A, with its logon task.
	binDir, data := filepath.Join(tmp, "bin"), filepath.Join(tmp, "machine")
	env := []string{"HERMESH_DOWNLOAD_BASE=" + releasesURL + "/latest/download", "HERMESH_BIN_DIR=" + binDir, "HERMESH_NO_PATH=1", "HERMESH_DATA_DIR=" + data}
	hermeshd := filepath.Join(binDir, "hermeshd.exe")
	t.Cleanup(func() {
		if t.Failed() {
			log, _ := os.ReadFile(filepath.Join(data, "logs", "hermeshd.log"))
			t.Logf("==== the machine's log ====\n%s", log)
		}
		exec.Command(hermeshd, "service", "uninstall").Run()
	})
	installScript := filepath.Join(tmp, "releases", "0.0.1", "install.ps1")
	install := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", installScript, setup.ConnectAddress, "-Name", "win-1")
	install.Env = append(os.Environ(), env...)
	var installOut syncBuffer
	install.Stdout, install.Stderr = &installOut, &installOut
	if err := install.Start(); err != nil {
		t.Fatal(err)
	}
	var code string
	eventually(t, 2*time.Minute, "pairing code", func() bool {
		if m := codeRE.FindStringSubmatch(installOut.String()); m != nil {
			code = m[1]
		}
		return code != ""
	})
	approved, err := app.client.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: code, MachineName: "win-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := install.Wait(); err != nil {
		t.Fatalf("install: %v\n%s", err, installOut.String())
	}
	machineVersion := func() string {
		res, err := app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err != nil {
			return ""
		}
		for _, m := range res.Machines {
			if m.MachineId == approved.MachineId && m.Online && m.Status != nil {
				return m.Status.Version
			}
		}
		return ""
	}
	eventually(t, 60*time.Second, "the machine online on 0.0.1", func() bool { return machineVersion() == "0.0.1" })

	// 2. B: the machine updates itself, and its supervisor starts B.
	publish("0.0.2")
	eventually(t, 2*time.Minute, "the machine on 0.0.2", func() bool { return machineVersion() == "0.0.2" })
	if r := run(t, nil, time.Minute, filepath.Join(binDir, "hermesh.exe"), "version"); strings.TrimSpace(r.stdout) != "0.0.2" {
		t.Errorf("hermesh after the update: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(binDir, ".hermeshd.previous.exe")); err != nil {
		t.Errorf("the previous hermeshd: %v", err)
	}

	// 3. Automatic updates off.
	if r := run(t, env, time.Minute, hermeshd, "update", "auto", "off"); r.code != 0 || !strings.Contains(r.stdout, "off") {
		t.Fatalf("auto off: %+v", r)
	}
	publish("0.0.3")
	time.Sleep(8 * time.Second) // several checks' time
	if v := machineVersion(); v != "0.0.2" {
		t.Fatalf("updated to %q with automatic updates off", v)
	}
	if r := run(t, env, time.Minute, hermeshd, "update", "--check"); !strings.Contains(r.stdout, "0.0.3 is available") {
		t.Errorf("--check: %+v", r)
	}
	if r := run(t, env, 2*time.Minute, hermeshd, "update", "--yes"); r.code != 0 || !strings.Contains(r.stdout, "the service restarted on 0.0.3") {
		t.Fatalf("update: %+v", r)
	}
	eventually(t, 60*time.Second, "the machine on 0.0.3", func() bool { return machineVersion() == "0.0.3" })
}
