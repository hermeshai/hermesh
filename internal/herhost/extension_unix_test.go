//go:build unix

package herhost

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hermeshai/hermesh/internal/herhost/hertest"
)

const extensionOrigin = "chrome-extension://igeolfcjdhlfopmimahmhleloehmfkdc/"

// With ExtensionHost, Start registers her with the browsers, for her's home
// and this socket, before her starts; hermeshd stopping keeps that, and Her
// off takes it back. Without, the browsers are never touched.
func TestExtensionHost(t *testing.T) {
	t.Setenv("HER_HOME", "/hers")
	sock := shortSocket(t)
	install, serve, uninstall := "/hers browser extension install --socket "+sock, "/hers serve --listen unix://"+sock, "/hers browser extension uninstall"
	bin, calls := hertest.Fake(t)
	runs := func(want ...string) func() bool {
		return func() bool { return slices.Equal(hertest.Calls(calls), want) }
	}
	h := New(Config{Bin: bin, Socket: sock, Env: os.Environ, Version: "test", ExtensionHost: true})
	h.Start()
	eventually(t, runs(install, serve))
	h.Stop()
	h.Start()
	eventually(t, runs(install, serve, install, serve))
	h.Off()
	if !runs(install, serve, install, serve, uninstall)() {
		t.Fatalf("runs %q", hertest.Calls(calls))
	}

	bin, calls = hertest.Fake(t)
	h = New(Config{Bin: bin, Socket: sock, Env: os.Environ, Version: "test"})
	h.Start()
	eventually(t, runs(serve))
	h.Off()
	if !runs(serve)() {
		t.Fatalf("runs %q", hertest.Calls(calls))
	}
}

// The owner's Chrome reaching the her a Host runs on its own socket, as
// Chrome does it: with Her on, a browser installed here (in a HOME of the
// test's) has a manifest whose program, started as Chrome starts a host,
// reaches that her. The console, through its routes as its Browser section
// uses them, hears browser/changed, sees the extension in browser/status,
// picks it and has Her use it. hermeshd stopping keeps the host; Her off
// takes it away.
func TestExtensionHostReachesHer(t *testing.T) {
	bin := hertest.Bin(t)
	home := hertest.Home(t, bin)
	user := t.TempDir()
	chrome := filepath.Join(user, "Library", "Application Support", "Google", "Chrome")
	if runtime.GOOS != "darwin" {
		chrome = filepath.Join(user, ".config", "google-chrome")
	}
	if err := os.MkdirAll(chrome, 0o755); err != nil {
		t.Fatal(err)
	}
	sock := shortSocket(t)
	h := New(Config{Bin: bin, Socket: sock, Env: func() []string { return append(os.Environ(), "HOME="+user) }, Version: "test", ExtensionHost: true})
	h.Start()
	t.Cleanup(h.Stop)
	eventually(t, func() bool { return h.Status().Running })

	manifestPath := filepath.Join(chrome, "NativeMessagingHosts", "com.hermesh.browser.json")
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Path           string   `json:"path"`
		AllowedOrigins []string `json:"allowed_origins"`
	}
	json.Unmarshal(b, &manifest)
	if manifest.Path != filepath.Join(home, "browser", "native-host") || !slices.Contains(manifest.AllowedOrigins, extensionOrigin) {
		t.Fatalf("manifest %s", b)
	}

	// The console's connection, as its Browser section holds it: her's
	// notifications as events, and requests sent on that connection.
	srv := httptest.NewServer(h)
	defer srv.Close()
	notes, rpc := consoleConnection(t, srv.URL)
	type browserStatus struct {
		Extension *struct {
			Browser  string `json:"browser"`
			Instance string `json:"instance"`
		} `json:"extension"`
	}
	var st browserStatus
	if json.Unmarshal(rpc("browser/status", `{}`), &st); st.Extension != nil {
		t.Fatalf("an extension before any host: %+v", st.Extension)
	}

	// Chrome runs the manifest's program with the extension's origin, in an
	// environment of its own, and speaks Native Messaging on its stdio.
	host := exec.Command(manifest.Path, extensionOrigin)
	host.Env = []string{"HOME=" + user, "PATH=/usr/bin:/bin"}
	stdin, _ := host.StdinPipe()
	stdout, _ := host.StdoutPipe()
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		host.Process.Kill()
		host.Wait()
	}()
	hello, _ := json.Marshal(map[string]any{"hello": map[string]any{"browser": "Google Chrome 142.0.7444.60", "extension_version": "0.2.1",
		"bridge": map[string]int{"min": 1, "max": 2}, "instance": "test-profile"}})
	frame := binary.NativeEndian.AppendUint32(nil, uint32(len(hello)))
	if _, err := stdin.Write(append(frame, hello...)); err != nil {
		t.Fatal(err)
	}
	if said := hostStatus(t, stdout); said != "connected" {
		t.Fatalf("the host says %q", said)
	}
	// The console hears it, asks, and has Her use that Chrome.
	for deadline := time.After(30 * time.Second); ; {
		var n struct {
			Method string        `json:"method"`
			Params browserStatus `json:"params"`
		}
		select {
		case line := <-notes:
			json.Unmarshal([]byte(line), &n)
		case <-deadline:
			t.Fatal("no browser/changed with the extension")
		}
		if n.Method == "browser/changed" && n.Params.Extension != nil && n.Params.Extension.Browser == "Google Chrome 142.0.7444.60" {
			break
		}
	}
	if json.Unmarshal(rpc("browser/status", `{}`), &st); st.Extension == nil || st.Extension.Instance != "test-profile" {
		t.Fatalf("browser/status: %+v", st.Extension)
	}
	rpc("browser/use_extension", `{"instance":"test-profile"}`)
	rpc("config/write", `{"layer":"user","edits":[{"key_path":"browser.extension","value":true}]}`)
	var cfg struct {
		Config struct {
			Browser struct {
				Extension bool `json:"extension"`
			} `json:"browser"`
		} `json:"config"`
	}
	if json.Unmarshal(rpc("config/read", `{}`), &cfg); !cfg.Config.Browser.Extension {
		t.Fatal("Her does not use the owner's Chrome")
	}

	h.Stop()
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("hermeshd stopping took the host away: %v", err)
	}
	h.Start()
	eventually(t, func() bool { return h.Status().Running })
	h.Off()
	for _, p := range []string{manifestPath, manifest.Path} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s after Her off: %v", p, err)
		}
	}
}

// consoleConnection opens a connection as the console does: her's
// notifications on it come as events, each line on notes; rpc sends a
// request on it and returns her's result.
func consoleConnection(t *testing.T, base string) (notes <-chan string, rpc func(method, params string) json.RawMessage) {
	t.Helper()
	res, err := http.Get(base + "/her/console/events")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(nil, 1<<20)
	var opened struct {
		ConnectionID string `json:"connection_id"`
	}
	for opened.ConnectionID == "" && sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			json.Unmarshal([]byte(data), &opened)
		}
	}
	if opened.ConnectionID == "" {
		t.Fatal("no opened event")
	}
	lines := make(chan string, 256)
	go func() {
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				lines <- data
			}
		}
	}()
	id := 0
	return lines, func(method, params string) json.RawMessage {
		t.Helper()
		id++
		req, _ := http.NewRequest("POST", base+"/her/console/rpc", strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, params)))
		req.Header.Set("Her-Connection", opened.ConnectionID)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out struct {
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.NewDecoder(res.Body).Decode(&out); res.StatusCode != 200 || out.Error != nil {
			t.Fatalf("%s: %d %+v", method, res.StatusCode, out.Error)
		}
		return out.Result
	}
}

// hostStatus is the status of the host's first message that has one,
// skipping the rest.
func hostStatus(t *testing.T, out io.Reader) string {
	t.Helper()
	said := make(chan string, 1)
	go func() {
		for {
			var n uint32
			if binary.Read(out, binary.NativeEndian, &n) != nil {
				said <- ""
				return
			}
			b := make([]byte, n)
			if _, err := io.ReadFull(out, b); err != nil {
				said <- ""
				return
			}
			var m struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(b, &m) == nil && m.Status != "" {
				said <- m.Status
				return
			}
		}
	}()
	select {
	case s := <-said:
		return s
	case <-time.After(30 * time.Second):
		t.Fatal("the host said nothing")
		return ""
	}
}
