package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// release is a signed release served the way GitHub serves one.
type release struct {
	t     *testing.T
	key   *ecdsa.PrivateKey
	files map[string][]byte // under /download/v<version>/ and /latest/download/
	srv   *httptest.Server
}

func newRelease(t *testing.T) *release {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	r := &release{t: t, key: key, files: map[string][]byte{}}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		name := filepath.Base(req.URL.Path)
		if b, ok := r.files[name]; ok && (strings.HasPrefix(req.URL.Path, "/releases/latest/download/") || strings.HasPrefix(req.URL.Path, "/releases/download/v")) {
			w.Write(b)
			return
		}
		http.NotFound(w, req)
	}))
	t.Cleanup(r.srv.Close)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	setVar(t, &publicKey, base64.StdEncoding.EncodeToString(der))
	setVar(t, &releases, r.srv.URL+"/releases")
	return r
}

// fakePrograms are programs that print a version, by version: a shell
// script, or on Windows a program built once.
var fakePrograms sync.Map

func fakeProgram(t *testing.T, version string) []byte {
	if runtime.GOOS != "windows" {
		return []byte("#!/bin/sh\necho " + version + "\n")
	}
	if b, ok := fakePrograms.Load(version); ok {
		return b.([]byte)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\""+version+"\") }\n"), 0o644)
	exe := filepath.Join(dir, "p.exe")
	if out, err := exec.Command("go", "build", "-o", exe, filepath.Join(dir, "main.go")).CombinedOutput(); err != nil {
		t.Fatalf("go build: %v %s", err, out)
	}
	b, _ := os.ReadFile(exe)
	fakePrograms.Store(version, b)
	return b
}

func setVar(t *testing.T, v *string, to string) {
	old := *v
	*v = to
	t.Cleanup(func() { *v = old })
}

// publish makes version the latest, with programs that say it, and her's
// console.
func (r *release) publish(version string) {
	r.files = map[string][]byte{"version": []byte(version + "\n"), herConsoleAsset: consoleArchive(r.t, "./", "console "+version)}
	for _, name := range Programs {
		r.files[asset(name)] = fakeProgram(r.t, version)
	}
	r.sign()
}

// consoleArchive is a console's .tar.gz, as release.sh makes it (tar -C
// dist .), its index.html in folder.
func consoleArchive(t *testing.T, folder, index string) []byte {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	tw := tar.NewWriter(zw)
	tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: folder, Mode: 0o755})
	tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: folder + "assets/", Mode: 0o755})
	for name, body := range map[string]string{folder + "index.html": index, folder + "assets/app.js": "app"} {
		tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(body))})
		tw.Write([]byte(body))
	}
	if tw.Close() != nil || zw.Close() != nil {
		t.Fatal("console archive")
	}
	return b.Bytes()
}

// hermesh is the agent CLI among Programs.
var hermesh = map[bool]string{true: "hermesh.exe", false: "hermesh"}[runtime.GOOS == "windows"]

func (r *release) sign() {
	var sums strings.Builder
	for name, b := range r.files {
		if !strings.HasPrefix(name, "checksums") {
			h := sha256.Sum256(b)
			fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), name)
		}
	}
	r.files["checksums.txt"] = []byte(sums.String())
	digest := sha256.Sum256(r.files["checksums.txt"])
	r.files["checksums.txt.sig"], _ = ecdsa.SignASN1(rand.Reader, r.key, digest[:])
}

// programsAt is a folder with the programs at version, and her's console.
func programsAt(t *testing.T, version string) string {
	dir := t.TempDir()
	for _, name := range Programs {
		os.WriteFile(filepath.Join(dir, name), fakeProgram(t, version), 0o755)
	}
	os.MkdirAll(filepath.Join(dir, herConsole, "assets"), 0o755)
	os.WriteFile(filepath.Join(dir, herConsole, "index.html"), []byte("console "+version), 0o644)
	os.WriteFile(filepath.Join(dir, herConsole, "assets", "old.js"), []byte("old"), 0o644)
	return dir
}

// console is the index.html of the console in dir's folder name.
func console(dir, name string) string {
	b, _ := os.ReadFile(filepath.Join(dir, name, "index.html"))
	return string(b)
}

func TestLatest(t *testing.T) {
	r := newRelease(t)
	r.publish("0.2.0")
	rel, err := Latest(context.Background())
	if err != nil || rel.Version != "0.2.0" {
		t.Fatalf("%v %v", rel, err)
	}
	if !rel.Newer("0.1.1-beta.5") || rel.Newer("0.2.0") || rel.Newer("0.10.0") {
		t.Error("Newer compares versions")
	}

	// A version the list doesn't sign.
	r.files["version"] = []byte("9.9.9\n")
	if _, err := Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "not in its signed checksum list") {
		t.Errorf("unsigned version: %v", err)
	}
	// A list signed by another key.
	r.publish("0.2.0")
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	digest := sha256.Sum256(r.files["checksums.txt"])
	r.files["checksums.txt.sig"], _ = ecdsa.SignASN1(rand.Reader, other, digest[:])
	if _, err := Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "does not verify") {
		t.Errorf("another key: %v", err)
	}
	// No release yet.
	r.files = map[string][]byte{}
	if _, err := Latest(context.Background()); err == nil {
		t.Error("no release: no error")
	}
}

// The built-in key is the release key.
func TestPublicKeyIsTheReleaseKey(t *testing.T) {
	b, err := os.ReadFile("../../scripts/release-pubkey.pem")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(b)
	if base64.StdEncoding.EncodeToString(block.Bytes) != publicKey {
		t.Error("publicKey is not scripts/release-pubkey.pem")
	}
}

func TestInstall(t *testing.T) {
	r := newRelease(t)
	r.publish("0.2.0")
	rel, _ := Latest(context.Background())
	dir := programsAt(t, "0.1.0")
	names, err := Install(context.Background(), t.TempDir(), dir, rel)
	if err != nil || strings.Join(names, " ") != strings.Join(append(Programs, herConsole), " ") {
		t.Fatalf("%v %v", names, err)
	}
	// The console, whole: the old one's files are gone with it.
	if c, prev := console(dir, herConsole), console(dir, previousPath(".", herConsole)); c != "console 0.2.0" || prev != "console 0.1.0" {
		t.Errorf("console %q, previous %q", c, prev)
	}
	if _, err := os.Stat(filepath.Join(dir, herConsole, "assets", "old.js")); err == nil {
		t.Error("the old console's files are left in the new one")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, herConsole, "assets", "app.js")); string(b) != "app" {
		t.Errorf("the new console's assets: %q", b)
	}
	if _, err := os.Stat(newPath(dir, herConsole)); err == nil {
		t.Error("the console's new left behind")
	}
	for _, name := range Programs {
		if b, _ := os.ReadFile(filepath.Join(dir, name)); !strings.Contains(string(b), "0.2.0") {
			t.Errorf("%s: %s", name, b)
		}
		if b, _ := os.ReadFile(previousPath(dir, name)); !strings.Contains(string(b), "0.1.0") {
			t.Errorf("%s's previous: %.40q", name, b)
		}
		if _, err := os.Stat(newPath(dir, name)); err == nil {
			t.Errorf("%s's new left behind", name)
		}
	}

	// Only the programs there: an agent CLI alone.
	cli := t.TempDir()
	os.WriteFile(filepath.Join(cli, hermesh), fakeProgram(t, "0.1.0"), 0o755)
	if names, err := Install(context.Background(), t.TempDir(), cli, rel); err != nil || strings.Join(names, " ") != hermesh {
		t.Errorf("CLI only: %v %v", names, err)
	}

	// A release without her and its console, as before them: the others
	// go, they stay.
	r.publish("0.3.0")
	delete(r.files, asset("her"))
	delete(r.files, herConsoleAsset)
	r.sign()
	rel, _ = Latest(context.Background())
	os.WriteFile(filepath.Join(dir, "her"), fakeProgram(t, "0.2.0"), 0o755)
	if names, err := Install(context.Background(), t.TempDir(), dir, rel); err != nil || slices.Contains(names, "her") || slices.Contains(names, herConsole) {
		t.Fatalf("a release without her: %v %v", names, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "her")); !strings.Contains(string(b), "0.2.0") || console(dir, herConsole) != "console 0.2.0" {
		t.Errorf("her %q and its console %q, from a release without them", b, console(dir, herConsole))
	}
}

// A program that doesn't match its hash, or doesn't say its version, a her
// that doesn't start, and a console with a file outside its folder, replace
// nothing.
func TestInstallRefuses(t *testing.T) {
	r := newRelease(t)
	cli := asset(hermesh)
	changes := map[string]func(){
		"checksum": func() { r.files[cli] = append(fakeProgram(t, "0.2.0"), ' ') },
		"start as": func() {
			r.files[cli] = fakeProgram(t, "0.1.9")
			r.sign()
		},
		"outside": func() {
			r.files[herConsoleAsset] = consoleArchive(t, "../", "elsewhere")
			r.sign()
		},
	}
	if runtime.GOOS != "windows" {
		changes["her does not start"] = func() {
			r.files[asset("her")] = []byte("#!/bin/sh\nexit 1\n")
			r.sign()
		}
	}
	for what, change := range changes {
		r.publish("0.2.0")
		change()
		rel, _ := Latest(context.Background())
		dir := programsAt(t, "0.1.0")
		if _, err := Install(context.Background(), t.TempDir(), dir, rel); err == nil || !strings.Contains(err.Error(), what) {
			t.Errorf("%s: %v", what, err)
		}
		for _, name := range Programs {
			if b, _ := os.ReadFile(filepath.Join(dir, name)); !strings.Contains(string(b), "0.1.0") {
				t.Errorf("%s: %s replaced", what, name)
			}
			if _, err := os.Stat(newPath(dir, name)); err == nil {
				t.Errorf("%s: %s's new left behind", what, name)
			}
		}
		if c := console(dir, herConsole); c != "console 0.1.0" {
			t.Errorf("%s: the console replaced: %q", what, c)
		}
		if _, err := os.Stat(newPath(dir, herConsole)); err == nil {
			t.Errorf("%s: the console's new left behind", what)
		}
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
			t.Errorf("%s: a file written outside", what)
		}
	}
}

func TestDirRefusesTheApp(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The Windows app's programs, in the folder install.ps1 uses.
		local := t.TempDir()
		t.Setenv("LOCALAPPDATA", local)
		os.MkdirAll(filepath.Join(local, "HermeshApp"), 0o755)
		os.WriteFile(filepath.Join(local, "HermeshApp", "Update.exe"), nil, 0o755)
		executable = func() (string, error) { return filepath.Join(local, "Programs", "Hermesh", "bin", "hermeshd.exe"), nil }
		t.Cleanup(func() { executable = os.Executable })
		os.MkdirAll(filepath.Join(local, "Programs", "Hermesh", "bin"), 0o755)
		os.WriteFile(filepath.Join(local, "Programs", "Hermesh", "bin", "hermeshd.exe"), nil, 0o755)
		if _, err := Dir(); err == nil || !strings.Contains(err.Error(), "the Hermesh app") {
			t.Errorf("%v", err)
		}
		return
	}
	app := filepath.Join(t.TempDir(), "Hermesh.app", "Contents", "Resources")
	os.MkdirAll(app, 0o755)
	os.WriteFile(filepath.Join(app, "hermeshd"), nil, 0o755)
	executable = func() (string, error) { return filepath.Join(app, "hermeshd"), nil }
	t.Cleanup(func() { executable = os.Executable })
	if _, err := Dir(); err == nil || !strings.Contains(err.Error(), "the Hermesh app") {
		t.Errorf("%v", err)
	}
}

// The background updater waits while a session runs, installs once none
// does, and does nothing while automatic updates are off.
func TestBackground(t *testing.T) {
	r := newRelease(t)
	r.publish("0.2.0")
	dir := programsAt(t, "0.1.0")
	executable = func() (string, error) { return filepath.Join(dir, Programs[0]), nil }
	t.Cleanup(func() { executable = os.Executable })
	setVar(t, &testInterval, "20ms")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	data := t.TempDir()

	Edit(data, func(s *State) { s.Off = true })
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	if Background(ctx, data, "0.1.0", func() int { return 0 }, log) {
		t.Error("installed while off")
	}
	cancel()
	if s := Load(data); !s.Checked.IsZero() {
		t.Errorf("checked while off: %+v", s)
	}

	Edit(data, func(s *State) { s.Off = false })
	var busy atomic.Int32
	busy.Store(1)
	done := make(chan bool)
	go func() {
		done <- Background(context.Background(), data, "0.1.0", func() int { return int(busy.Load()) }, log)
	}()
	time.Sleep(300 * time.Millisecond)
	if b, _ := os.ReadFile(filepath.Join(dir, Programs[0])); !strings.Contains(string(b), "0.1.0") {
		t.Fatal("installed while a session runs")
	}
	if s := Load(data); s.Available != "0.2.0" || s.Checked.IsZero() {
		t.Errorf("state while waiting: %+v", s)
	}
	busy.Store(0)
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("Background returned false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not installed once idle")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, Programs[0])); !strings.Contains(string(b), "0.2.0") {
		t.Error("not installed")
	}
	if s := Load(data); s.From != "0.1.0" || s.To != "0.2.0" || s.Available != "" {
		t.Errorf("state after: %+v", s)
	}
	if line := Line(data); line != "automatic · checked just now" {
		t.Errorf("Line: %q", line)
	}
}
