// Package update installs newer Hermesh releases over the programs in
// place, for `hermeshd update`, `hermesh update` and hermeshd's background
// updater (docs/machine-updates.md).
package update

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/hermeshai/hermesh/internal/flock"
)

// Build-time constants: test builds set them with -ldflags -X.
var (
	// releases is where the releases are.
	releases = "https://github.com/hermeshai/hermesh/releases"
	// publicKey is the release signing key, scripts/release-pubkey.pem as
	// base64 DER.
	publicKey = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEApiOFHMULL0spm5OGRgNHe9owgNMcYWJ2JeVFlfetdHEVad0neJzOYU5C4UNcBTSrasXA3GvZmGd5h1WAv1u3Q=="
	// testInterval, when set, replaces every wait of the background updater.
	testInterval = ""
)

// Programs are the programs an update replaces, where they are.
var Programs = []string{"hermeshd", "hermesh"}

// Release is a signed release: its version and its files' hashes.
type Release struct {
	Version string
	sums    map[string]string
}

var client = &http.Client{Timeout: 5 * time.Minute}

// Latest reads the latest release's signed checksum list and version.
func Latest(ctx context.Context) (*Release, error) {
	base := releases + "/latest/download/"
	sums, err := fetch(ctx, base+"checksums.txt", 1<<20)
	if err != nil {
		return nil, err
	}
	sig, err := fetch(ctx, base+"checksums.txt.sig", 4<<10)
	if err != nil {
		return nil, err
	}
	version, err := fetch(ctx, base+"version", 1<<10)
	if err != nil {
		return nil, err
	}
	return verify(sums, sig, version)
}

// verify checks the list's signature and the version against the list.
func verify(sums, sig, version []byte) (*Release, error) {
	der, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	pub, ok := key.(*ecdsa.PublicKey)
	digest := sha256.Sum256(sums)
	if !ok || !ecdsa.VerifyASN1(pub, digest[:], sig) {
		return nil, errors.New("the release's checksum signature does not verify")
	}
	r := &Release{sums: map[string]string{}}
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			r.sums[f[1]] = f[0]
		}
	}
	if h := sha256.Sum256(version); r.sums["version"] != hex.EncodeToString(h[:]) {
		return nil, errors.New("the release's version is not in its signed checksum list")
	}
	r.Version = strings.TrimSpace(string(version))
	if !semver.IsValid("v" + r.Version) {
		return nil, fmt.Errorf("the release's version %q is not a version", r.Version)
	}
	return r, nil
}

// Newer reports whether r is a higher version than current.
func (r *Release) Newer(current string) bool {
	return semver.Compare("v"+r.Version, "v"+current) > 0
}

func fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, limit))
}

var executable = os.Executable

// Dir is the folder of the running program, which an update writes.
func Dir() (string, error) {
	exe, err := executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	if strings.Contains(exe, ".app/Contents/") {
		return "", errors.New("this hermeshd is part of Hermesh.app, which updates itself")
	}
	if runtime.GOOS == "windows" {
		return "", errors.New("updating is not on Windows yet; run the install command again")
	}
	return filepath.Dir(exe), nil
}

// Install puts r's programs in dir over the ones there, keeping each old
// one as .<name>.previous. Each new program must start and say r's version
// before any is replaced.
func Install(ctx context.Context, dataDir, dir string, r *Release) ([]string, error) {
	lock, err := flock.Acquire(filepath.Join(dataDir, "update"))
	if err != nil {
		return nil, errors.New("another update is running")
	}
	defer lock.Release()
	var names []string
	for _, name := range Programs {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			names = append(names, name)
		}
	}
	if names == nil {
		return nil, fmt.Errorf("no Hermesh programs in %s", dir)
	}
	for _, name := range names {
		if err := download(ctx, dir, name, r); err != nil {
			for _, n := range names {
				os.Remove(filepath.Join(dir, "."+n+".new"))
			}
			return nil, err
		}
	}
	for _, name := range names {
		path := filepath.Join(dir, name)
		prev := filepath.Join(dir, "."+name+".previous")
		os.Remove(prev)
		os.Link(path, prev)
		if err := os.Rename(filepath.Join(dir, "."+name+".new"), path); err != nil {
			return nil, err
		}
	}
	return names, nil
}

func download(ctx context.Context, dir, name string, r *Release) error {
	asset := name + "-" + runtime.GOOS + "-" + runtime.GOARCH
	want := r.sums[asset]
	if want == "" {
		return fmt.Errorf("%s is not in the release's signed checksum list", asset)
	}
	body, err := fetch(ctx, releases+"/download/v"+r.Version+"/"+asset, 512<<20)
	if err != nil {
		return err
	}
	if h := sha256.Sum256(body); hex.EncodeToString(h[:]) != want {
		return fmt.Errorf("%s does not match its checksum", asset)
	}
	tmp := filepath.Join(dir, "."+name+".new")
	if err := os.WriteFile(tmp, body, 0o755); err != nil {
		return fmt.Errorf("cannot write %s: %w", dir, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, tmp, "version").Output()
	if err != nil || strings.TrimSpace(string(out)) != r.Version {
		return fmt.Errorf("the downloaded %s does not start as %s: %v %s", name, r.Version, err, bytes.TrimSpace(out))
	}
	return nil
}

// State is what the updater keeps in <data dir>/update/update.json.
type State struct {
	Off       bool      `json:"off,omitempty"`       // automatic updates turned off
	Checked   time.Time `json:"checked,omitzero"`    // the last successful check
	Available string    `json:"available,omitempty"` // a newer version it found
	From      string    `json:"from,omitempty"`      // the last update's versions
	To        string    `json:"to,omitempty"`
	Updated   time.Time `json:"updated,omitzero"`
}

func statePath(dataDir string) string { return filepath.Join(dataDir, "update", "update.json") }

// Load reads the state; none yet is the zero State (automatic updates on).
func Load(dataDir string) State {
	var s State
	if b, err := os.ReadFile(statePath(dataDir)); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

// Edit changes the state.
func Edit(dataDir string, f func(*State)) error {
	s := Load(dataDir)
	f(&s)
	b, _ := json.MarshalIndent(s, "", "  ")
	path := statePath(dataDir)
	os.MkdirAll(filepath.Dir(path), 0o700)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
