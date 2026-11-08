package herhost

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func serve(h *Host, method, path, body string, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// The console's files come from its folder; without one, a page says it is
// not installed.
func TestConsoleFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<title>Console</title>"), 0o644)
	os.MkdirAll(filepath.Join(dir, "assets"), 0o755)
	os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("main()"), 0o644)
	h := New(Config{ConsoleDir: dir})
	if w := serve(h, "GET", "/her/console/", ""); w.Code != 200 || w.Body.String() != "<title>Console</title>" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := serve(h, "GET", "/her/console/assets/app.js", ""); w.Code != 200 || w.Body.String() != "main()" || !strings.Contains(w.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("%d %s %s", w.Code, w.Header(), w.Body)
	}
	if w := serve(h, "GET", "/her/console/../../etc/passwd", ""); w.Code == 200 && strings.Contains(w.Body.String(), "root:") {
		t.Fatal("served a file outside the console")
	}
	if w := serve(h, "POST", "/her/console/index.html", ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("%d", w.Code)
	}
	w := serve(New(Config{ConsoleDir: filepath.Join(dir, "missing")}), "GET", "/her/console/", "")
	if w.Code != 404 || !strings.Contains(w.Body.String(), "not installed") || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("%d %s %s", w.Code, w.Header(), w.Body)
	}
}

// hermeshd's own refusals are short texts with an HTTP status, before her
// is asked anything.
func TestConsoleRefusals(t *testing.T) {
	h := New(Config{})
	for _, c := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/her/console/rpc", `{"jsonrpc":"2.0","id":1,"method":"server/shutdown"}`, 403},
		{"POST", "/her/console/rpc", `{"jsonrpc":"2.0","id":1,"method":"config/write","params":{"layer":"user","edits":[{"key_path":"mcp_servers.x.command","value":"sh"}]}}`, 403},
		{"POST", "/her/console/rpc", `[{"jsonrpc":"2.0","id":1,"method":"models/list"}]`, 400},
		{"POST", "/her/console/rpc", `{"jsonrpc":"2.0","method":"models/list"}`, 400},
		{"GET", "/her/console/rpc", ``, 405},
		{"POST", "/her/console/rpc", `{"jsonrpc":"2.0","id":1,"method":"models/list"}`, 400}, // Her is off
		{"GET", "/her/console/events", ``, 400},
		{"POST", "/her/console/upload", `x`, 400},
		{"GET", "/her/console/export", ``, 400},
	} {
		w := serve(h, c.method, c.path, c.body, "Her-File-Name", "x.txt")
		if w.Code != c.status || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") || w.Body.Len() == 0 {
			t.Errorf("%s %s %s: %d %q, want %d", c.method, c.path, c.body, w.Code, w.Body, c.status)
		}
	}
}

// A file the console uploads is kept in a private folder of her home, up to
// 50 MiB, and staged files a day old go when the next comes.
func TestConsoleUpload(t *testing.T) {
	noHerHere(t)
	home := t.TempDir()
	h := onAt(t, home)
	staged := filepath.Join(home, "uploads", "console")
	os.MkdirAll(staged, 0o700)
	old, fresh := filepath.Join(staged, "0-old.zip"), filepath.Join(staged, "1-fresh.zip")
	os.WriteFile(old, []byte("old"), 0o600)
	os.WriteFile(fresh, []byte("fresh"), 0o600)
	os.Chtimes(old, time.Now().Add(-25*time.Hour), time.Now().Add(-25*time.Hour))

	w := serve(h, "POST", "/her/console/upload", "PK\x03\x04 a skill", "Her-File-Name", "my-skill.zip")
	var res struct{ Path string }
	if json.Unmarshal(w.Body.Bytes(), &res); w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	real, _ := filepath.EvalSymlinks(staged)
	if filepath.Dir(res.Path) != real || !strings.HasSuffix(res.Path, "-my-skill.zip") {
		t.Fatalf("staged at %s", res.Path)
	}
	if b, _ := os.ReadFile(res.Path); string(b) != "PK\x03\x04 a skill" {
		t.Fatalf("%q", b)
	}
	if st, _ := os.Stat(staged); st.Mode().Perm() != 0o700 {
		t.Fatalf("staging folder %v", st.Mode())
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("a day-old staged file is still there")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("a fresh staged file went")
	}

	for _, name := range []string{"", "../x.zip", "a/b.zip", ".."} {
		if w := serve(h, "POST", "/her/console/upload", "x", "Her-File-Name", name); w.Code != 400 {
			t.Errorf("name %q: %d", name, w.Code)
		}
	}
	// Too large, said up front or found while reading.
	r := httptest.NewRequest("POST", "/her/console/upload", strings.NewReader("x"))
	r.Header.Set("Her-File-Name", "big.zip")
	r.ContentLength = maxStaged + 1
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("%d", w.Code)
	}
	r = httptest.NewRequest("POST", "/her/console/upload", io.LimitReader(zeros{}, maxStaged+1))
	r.Header.Set("Her-File-Name", "big.zip")
	r.ContentLength = -1
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("%d", w.Code)
	}
	if left, _ := filepath.Glob(filepath.Join(staged, "*-big.zip")); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
}

// The console's export: her writes the archive, which comes down as a
// download and is then removed from the machine. Nothing else is served: not
// a file her names outside her home's exports/, nor one a link there leads
// to, and those stay where they are. her failing says why; data/export is not
// open to the console's JSON-RPC, as only this route asks for it.
func TestConsoleExport(t *testing.T) {
	noHerHere(t)
	home := t.TempDir()
	exports := filepath.Join(home, "exports")
	os.MkdirAll(exports, 0o700)
	secret := filepath.Join(home, "credentials.toml")
	os.WriteFile(secret, []byte("model-key"), 0o600)
	link := filepath.Join(exports, "her-export-link.zip")
	os.Symlink(secret, link)
	var mu sync.Mutex
	var answer string // what her answers to data/export
	sock := fakeHer(t, func(req map[string]any, c net.Conn) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(c, `{"jsonrpc":"2.0","id":%v,%s}`+"\n", req["id"], answer)
	})
	h := New(Config{Socket: sock, Version: "test"})
	h.stop, h.home, h.snap = func() {}, home, newSnapshot("9.9", nil)
	export := func(said string) *httptest.ResponseRecorder {
		mu.Lock()
		answer = said
		mu.Unlock()
		return serve(h, "GET", "/her/console/export", "")
	}
	wrote := func(path string) string { return `"result":{"path":` + mustJSON(t, path) + `,"bytes":16}` }

	archive := filepath.Join(exports, "her-export-20261108-090000.zip")
	os.WriteFile(archive, []byte("PK\x03\x04 the archive"), 0o600)
	w := export(wrote(archive))
	if w.Code != 200 || w.Body.String() != "PK\x03\x04 the archive" || w.Header().Get("Content-Type") != "application/zip" ||
		w.Header().Get("Content-Disposition") != "attachment; filename=her-export-20261108-090000.zip" ||
		w.Header().Get("Content-Length") != "16" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v %q", w.Code, w.Header(), w.Body)
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatalf("the archive is still on the machine: %v", err)
	}

	for _, p := range []string{secret, filepath.Join(exports, "..", "credentials.toml"), link, exports} {
		if w := export(wrote(p)); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "model-key") {
			t.Errorf("her naming %s: %d %q", p, w.Code, w.Body)
		}
	}
	if b, _ := os.ReadFile(secret); string(b) != "model-key" {
		t.Fatal("a file outside exports/ was removed")
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("the link was removed: %v", err)
	}

	w = export(`"error":{"code":-32603,"message":"cannot write the archive: no space left on device"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "no space left on device") {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	if w := serve(h, "POST", "/her/console/rpc", `{"jsonrpc":"2.0","id":1,"method":"data/export"}`); w.Code != http.StatusForbidden {
		t.Fatalf("data/export through rpc: %d %q", w.Code, w.Body)
	}
	if w := serve(h, "POST", "/her/console/export", ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", w.Code)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
