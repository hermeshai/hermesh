package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// TestRelayImage runs the relay image as the README says, a volume of its
// own and settings by -e, as a nonroot user:
//
//   - with a domain, it takes ports 80 and 443 and its MCP address is on
//     that domain (no certificate is asked for: nothing reaches it by
//     that name here);
//   - behind a proxy that serves HTTPS (played here by plain HTTP), the
//     claim code is in its log and by docker exec; the owner sets up with
//     it and is shown the same MCP address as mcp-url prints; restarted,
//     it keeps its owner and that address.
//
// HERMESH_DOCKER=1.
func TestRelayImage(t *testing.T) {
	if os.Getenv("HERMESH_DOCKER") != "1" {
		t.Skip("set HERMESH_DOCKER=1")
	}
	bin := binaries(t)
	ctx := context.Background()
	id := fmt.Sprint(time.Now().UnixNano())
	image := "hermesh-relay:e2e-" + id
	if out, err := exec.Command("docker", "build", "-q", "-f", filepath.Join("deploy", "relay", "Dockerfile"), "-t", image, ".").CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v %s", err, out)
	}
	t.Cleanup(func() { exec.Command("docker", "rmi", image).Run() })
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	relay := func(name string, ports []string, env ...string) {
		t.Helper()
		args := []string{"run", "-d", "--name", name, "--restart", "unless-stopped", "-v", name + ":/data"}
		for _, p := range ports {
			args = append(args, "-p", p)
		}
		for _, e := range env {
			args = append(args, "-e", e)
		}
		docker(append(args, image)...)
		t.Cleanup(func() {
			exec.Command("docker", "rm", "-f", name).Run()
			exec.Command("docker", "volume", "rm", name).Run()
		})
		eventually(t, 20*time.Second, name+" up", func() bool { return strings.Contains(docker("logs", name), "relay listening") })
	}

	// With a domain.
	web, tls := freePort(t), freePort(t)
	relay("hermesh-e2e-domain-"+id, []string{fmt.Sprintf("127.0.0.1:%d:80", web), fmt.Sprintf("127.0.0.1:%d:443", tls)},
		"HERMESH_RELAY_DOMAIN=relay.example.test", "HERMESH_RELAY_MCP=1")
	if u := docker("exec", "hermesh-e2e-domain-"+id, "hermesh-relay", "mcp-url"); !regexp.MustCompile(`^https://relay\.example\.test/mcp/[0-9a-z]{26}$`).MatchString(u) {
		t.Fatalf("mcp-url with a domain: %q", u)
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", web), nil)
	req.Host = "relay.example.test" // as a browser that typed the name asks
	res, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if loc := res.Header.Get("Location"); !strings.HasPrefix(loc, "https://relay.example.test/") {
		t.Fatalf("port 80: %d %q", res.StatusCode, loc)
	}

	// Behind a proxy.
	port := freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	name := "hermesh-e2e-proxied-" + id
	relay(name, []string{fmt.Sprintf("127.0.0.1:%d:8080", port)}, "HERMESH_RELAY_PUBLIC_URL="+base, "HERMESH_RELAY_MCP=1")
	m := regexp.MustCompile(`Claim code: (\S+)`).FindStringSubmatch(docker("logs", name))
	if m == nil {
		t.Fatalf("no claim code in the log:\n%s", docker("logs", name))
	}
	if c := docker("exec", name, "hermesh-relay", "claim-code"); c != m[1] {
		t.Fatalf("claim-code %q, the log's %q", c, m[1])
	}
	mcpURL := docker("exec", name, "hermesh-relay", "mcp-url")
	sockDir, _ := os.MkdirTemp("/tmp", "hm")
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "o.sock")
	start(t, "owner", nil, filepath.Join(bin, "hermeshd"), "run", "--owner", "--data-dir", filepath.Join(t.TempDir(), "owner"), "--socket", sock)
	root, _ := identity.GenerateKey()
	app := newTestApp(t, sock, root)
	if _, err := app.client.Setup(ctx, &localv1.SetupRequest{RelayUrl: base, ClaimCode: m[1], RootPublicKey: rootPub(root), RootLabel: "test-mac", RootPlatform: "macos"}); err != nil {
		t.Fatalf("setup: %v\n%s", err, docker("logs", name))
	}
	if got := mcpAddress(t, app); got != mcpURL {
		t.Fatalf("the MCP address on the Mac %q, mcp-url's %q", got, mcpURL)
	}

	docker("restart", name)
	if c := docker("exec", name, "hermesh-relay", "claim-code"); c != "already claimed" {
		t.Fatalf("claim-code after a restart: %q", c)
	}
	if u := docker("exec", name, "hermesh-relay", "mcp-url"); u != mcpURL {
		t.Fatalf("mcp-url after a restart %q, before %q", u, mcpURL)
	}
	eventually(t, 30*time.Second, "the owner back on the relay", func() bool {
		st, err := app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
		return err == nil && st.RelayConnected && st.McpUrl == mcpURL
	})
}
