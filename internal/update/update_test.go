package update

import (
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
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// release is a signed release served the way GitHub serves one.
type release struct {
	key   *ecdsa.PrivateKey
	files map[string][]byte // under /download/v<version>/ and /latest/download/
	srv   *httptest.Server
}

func newRelease(t *testing.T) *release {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	r := &release{key: key, files: map[string][]byte{}}
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

func setVar(t *testing.T, v *string, to string) {
	old := *v
	*v = to
	t.Cleanup(func() { *v = old })
}

// publish makes version the latest, with programs that say it.
func (r *release) publish(version string) {
	r.files = map[string][]byte{"version": []byte(version + "\n")}
	for _, name := range Programs {
		r.files[name+"-"+runtime.GOOS+"-"+runtime.GOARCH] = []byte("#!/bin/sh\necho " + version + "\n")
	}
	r.sign()
}

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

// programsAt is a folder with both programs at version.
func programsAt(t *testing.T, version string) string {
	dir := t.TempDir()
	for _, name := range Programs {
		os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\necho "+version+"\n"), 0o755)
	}
	return dir
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
	if err != nil || strings.Join(names, " ") != "hermeshd hermesh" {
		t.Fatalf("%v %v", names, err)
	}
	for _, name := range Programs {
		if b, _ := os.ReadFile(filepath.Join(dir, name)); !strings.Contains(string(b), "0.2.0") {
			t.Errorf("%s: %s", name, b)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "."+name+".previous")); !strings.Contains(string(b), "0.1.0") {
			t.Errorf(".%s.previous: %s", name, b)
		}
		if _, err := os.Stat(filepath.Join(dir, "."+name+".new")); err == nil {
			t.Errorf(".%s.new left behind", name)
		}
	}

	// Only the programs there: an agent CLI alone.
	cli := t.TempDir()
	os.WriteFile(filepath.Join(cli, "hermesh"), []byte("#!/bin/sh\necho 0.1.0\n"), 0o755)
	if names, err := Install(context.Background(), t.TempDir(), cli, rel); err != nil || strings.Join(names, " ") != "hermesh" {
		t.Errorf("CLI only: %v %v", names, err)
	}
}

// A program that doesn't match its hash, or doesn't say its version,
// replaces nothing.
func TestInstallRefuses(t *testing.T) {
	r := newRelease(t)
	for what, change := range map[string]func(){
		"checksum": func() { r.files["hermesh-"+runtime.GOOS+"-"+runtime.GOARCH] = []byte("#!/bin/sh\necho 0.2.0 \n") },
		"start as": func() {
			r.files["hermesh-"+runtime.GOOS+"-"+runtime.GOARCH] = []byte("#!/bin/sh\necho 0.1.9\n")
			r.sign()
		},
	} {
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
			if _, err := os.Stat(filepath.Join(dir, "."+name+".new")); err == nil {
				t.Errorf("%s: .%s.new left behind", what, name)
			}
		}
	}
}

func TestDirRefusesTheApp(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Hermesh.app", "Contents", "Resources")
	os.MkdirAll(app, 0o755)
	os.WriteFile(filepath.Join(app, "hermeshd"), nil, 0o755)
	executable = func() (string, error) { return filepath.Join(app, "hermeshd"), nil }
	t.Cleanup(func() { executable = os.Executable })
	if _, err := Dir(); err == nil || !strings.Contains(err.Error(), "Hermesh.app") {
		t.Errorf("%v", err)
	}
}

// The background updater waits while a session runs, installs once none
// does, and does nothing while automatic updates are off.
func TestBackground(t *testing.T) {
	r := newRelease(t)
	r.publish("0.2.0")
	dir := programsAt(t, "0.1.0")
	executable = func() (string, error) { return filepath.Join(dir, "hermeshd"), nil }
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
	if b, _ := os.ReadFile(filepath.Join(dir, "hermeshd")); !strings.Contains(string(b), "0.1.0") {
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
	if b, _ := os.ReadFile(filepath.Join(dir, "hermeshd")); !strings.Contains(string(b), "0.2.0") {
		t.Error("not installed")
	}
	if s := Load(data); s.From != "0.1.0" || s.To != "0.2.0" || s.Available != "" {
		t.Errorf("state after: %+v", s)
	}
	if line := Line(data); line != "automatic · checked just now" {
		t.Errorf("Line: %q", line)
	}
}
