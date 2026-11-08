package e2e

import (
	"archive/zip"
	"bytes"
	"io"
	"maps"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestHerExport: the console's Data page exports an environment that is not
// this Mac. Through this Mac's hermeshd, the relay and the tunnel, the Her
// Server's hermeshd has her write the archive, sends it as a download and
// removes it there. It holds what was said in the conversation, the
// assistant and the settings, and none of the keys her keeps; data/export
// itself is not open to the console's JSON-RPC (docs/her-server.md §10).
func TestHerExport(t *testing.T) {
	env := newHerEnvironment(t)
	talk(t, env.c, env.machine, env.bot.ID, "Noted: the plan for Friday.")
	page := openConsolePage(t, env.console)
	page.call("credentials/set", map[string]string{"name": "mcp.tracker", "value": "tracker-token-in-the-store"}, nil)
	if b, _ := os.ReadFile(filepath.Join(env.home, "credentials.toml")); !bytes.Contains(b, []byte("tracker-token-in-the-store")) {
		t.Fatalf("no key in the Her Server's credential store: %q", b)
	}

	res, err := http.Get(env.console + "export")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, disposition, _ := mime.ParseMediaType(res.Header.Get("Content-Disposition"))
	if name := disposition["filename"]; res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "application/zip" ||
		!strings.HasPrefix(name, "her-export-") || !strings.HasSuffix(name, ".zip") {
		t.Fatalf("export: %d %v %.200q", res.StatusCode, res.Header, archive)
	}
	z, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range z.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		files[f.Name] = string(b)
	}
	said := false
	for name, content := range files {
		said = said || strings.HasPrefix(name, "sessions/") && strings.HasSuffix(name, ".jsonl") && strings.Contains(content, "Noted: the plan for Friday.")
		if strings.Contains(content, "tracker-token-in-the-store") {
			t.Errorf("%s holds the key", name)
		}
	}
	_, store := files["credentials.toml"]
	if !said || files["bots/"+env.bot.ID+"/bot.json"] == "" || files["config.toml"] == "" || store {
		t.Fatalf("the archive holds %v", slices.Sorted(maps.Keys(files)))
	}
	if left, _ := os.ReadDir(filepath.Join(env.home, "exports")); len(left) != 0 {
		t.Fatalf("left on the Her Server: %v", left)
	}

	if a := page.rpc("data/export", nil); a.status != http.StatusForbidden {
		t.Fatalf("data/export through the console's rpc: %d %s %s", a.status, a.text, a.Result)
	}
}
