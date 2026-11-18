// Package update installs newer Hermesh releases over the programs in
// place, for `hermeshd update`, `hermesh update` and hermeshd's background
// updater (docs/machine-updates.md).
package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/hermeshai/hermesh/internal/flock"
	"github.com/hermeshai/hermesh/internal/machine"
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

// Programs are the programs an update replaces, where they are (Windows:
// with hermeshdw, the windowless hermeshd its service runs; elsewhere with
// her, Her's kernel, and hermesh-screen, Computer Control's helper, which
// hermeshd runs from beside itself).
var Programs = programs()

func programs() []string {
	if runtime.GOOS == "windows" {
		return []string{"hermeshd.exe", "hermeshdw.exe", "hermesh.exe"}
	}
	return []string{"hermeshd", "hermesh", "her", "hermesh-screen"}
}

// herConsole is the folder of her's console beside her, and
// herConsoleAsset its archive in a release (docs/her-server.md §6.4).
// herComputer is the folder of agent-desktop, which her's computer use
// runs on a Linux desktop (docs/linux.md §5), and herPDF the folder of her
// PDF worker and PDFium (§4), which must be her's own release's.
const (
	herConsole      = "her-console"
	herConsoleAsset = "her-console.tar.gz"
	herConsoleEntry = "index.html"
	herComputer     = "her-computer"
	herPDF          = "her-pdf"
)

// folders are the folders beside her that an update replaces whole, each
// with its archive in a release.
var folders = map[string]string{
	herConsole:  herConsoleAsset,
	herComputer: herComputer + "-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz",
	herPDF:      herPDF + "-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz",
}

// asset is a program's file in a release: hermeshd-linux-arm64,
// hermeshd-windows-amd64.exe.
func asset(name string) string {
	base := strings.TrimSuffix(name, ".exe")
	return base + "-" + runtime.GOOS + "-" + runtime.GOARCH + name[len(base):]
}

// newPath and previousPath are where a program's next and last versions
// wait beside it, keeping its extension (Windows runs .exe files only).
func newPath(dir, name string) string { return sibling(dir, name, "new") }

func previousPath(dir, name string) string { return sibling(dir, name, "previous") }

func sibling(dir, name, what string) string {
	base := strings.TrimSuffix(name, ".exe")
	return filepath.Join(dir, "."+base+"."+what+name[len(base):])
}

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
		return "", errors.New("this hermeshd is part of the Hermesh app, which updates itself")
	}
	// The Windows app copies its programs into the folder install.ps1 uses,
	// and updates them itself (docs/windows.md §6.2).
	if runtime.GOOS == "windows" {
		if _, err := os.Stat(filepath.Join(os.Getenv("LOCALAPPDATA"), "HermeshApp", "Update.exe")); err == nil {
			return "", errors.New("this hermeshd is the Hermesh app's, which updates it")
		}
	}
	return filepath.Dir(exe), nil
}

// missingHer lists the signed Her components a Linux machine needs to
// restore. The machine key distinguishes it from a CLI or trusted device
// installation. Like install.sh, the auxiliary tools need glibc.
func (r *Release) missingHer(dataDir, dir string) []string {
	if runtime.GOOS != "linux" || r.sums[asset("her")] == "" {
		return nil
	}
	if st, err := os.Stat(machine.KeyPath(filepath.Join(dataDir, "machine"))); err != nil || !st.Mode().IsRegular() {
		return nil
	}
	if st, err := os.Stat(filepath.Join(dir, "hermeshd")); err != nil || !st.Mode().IsRegular() {
		return nil
	}

	components := []string{"her", herConsole}
	_, amd64Err := os.Stat("/lib64/ld-linux-x86-64.so.2")
	_, arm64Err := os.Stat("/lib/ld-linux-aarch64.so.1")
	if amd64Err == nil || arm64Err == nil {
		components = append(components, herComputer, herPDF)
	}
	var missing []string
	for _, name := range components {
		file := folders[name]
		if name == "her" {
			file = asset(name)
		}
		if r.sums[file] == "" {
			continue
		}
		path := filepath.Join(dir, name)
		st, err := os.Lstat(path)
		absent := errors.Is(err, os.ErrNotExist)
		if name == herConsole && err == nil && st.IsDir() {
			absent = consoleEntryMissing(path)
		}
		if absent {
			missing = append(missing, name)
		}
	}
	return missing
}

func consoleEntryMissing(path string) bool {
	entry, err := os.Stat(filepath.Join(path, herConsoleEntry))
	return errors.Is(err, os.ErrNotExist) || (err == nil && !entry.Mode().IsRegular())
}

func (r *Release) repairsHer(current, dataDir, dir string) bool {
	return r.Version == current && len(r.missingHer(dataDir, dir)) > 0
}

// Install stages and verifies the programs and assets before replacing
// them. An upgrade keeps replaced files as .<name>.previous. A repair
// restores missing Her components or an incomplete console, keeping
// any existing backups.
func Install(ctx context.Context, dataDir, dir string, r *Release, repair bool) ([]string, error) {
	lock, err := flock.Acquire(filepath.Join(dataDir, "update"))
	if err != nil {
		return nil, errors.New("another update is running")
	}
	defer lock.Release()
	var names []string
	if !repair {
		for _, name := range Programs {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil && (!ownVersion(name) || r.sums[asset(name)] != "") {
				names = append(names, name)
			}
		}
		if names == nil {
			return nil, fmt.Errorf("no Hermesh programs in %s", dir)
		}
		for _, name := range []string{herConsole, herComputer, herPDF} {
			if st, err := os.Stat(filepath.Join(dir, name)); err == nil && st.IsDir() && r.sums[folders[name]] != "" {
				names = append(names, name)
			}
		}
	}
	for _, name := range r.missingHer(dataDir, dir) {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	for _, name := range names {
		if err := download(ctx, dir, name, r); err != nil {
			for _, n := range names {
				os.RemoveAll(newPath(dir, n))
			}
			return nil, err
		}
	}
	for _, name := range names {
		path := filepath.Join(dir, name)
		if _, err := os.Lstat(path); err == nil {
			if repair && name == herConsole {
				if err := replaceIncompleteConsole(dir, path); err != nil {
					return nil, err
				}
				continue
			}
			prev := previousPath(dir, name)
			os.RemoveAll(prev)
			if folders[name] != "" { // a folder, moved aside whole
				if err := os.Rename(path, prev); err != nil {
					return nil, err
				}
			} else if runtime.GOOS == "windows" {
				older, _ := filepath.Glob(prev + ".*")
				for _, o := range older {
					os.Remove(o) // unless it runs
				}
				// The one before may still run (the service's supervisor):
				// it stays, and this one moves beside it.
				if err := os.Rename(path, prev); err != nil {
					if err := os.Rename(path, fmt.Sprintf("%s.%d", prev, time.Now().UnixNano())); err != nil {
						return nil, err
					}
				}
			} else {
				os.Link(path, prev)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err := os.Rename(newPath(dir, name), path); err != nil {
			return nil, err
		}
	}
	return names, nil
}

// replaceIncompleteConsole sets aside an incomplete folder without touching
// .her-console.previous. Restore it if placing the verified archive fails.
func replaceIncompleteConsole(dir, path string) error {
	staged := newPath(dir, herConsole)
	defer os.RemoveAll(staged)
	if st, err := os.Lstat(path); err != nil || !st.IsDir() || !consoleEntryMissing(path) {
		return fmt.Errorf("%s changed during repair", path)
	}
	recovery, err := os.MkdirTemp(dir, ".her-console.incomplete-")
	if err != nil {
		return err
	}
	defer os.Remove(recovery) // leave the old folder here if restoration fails
	incomplete := filepath.Join(recovery, herConsole)
	if err := os.Rename(path, incomplete); err != nil {
		return err
	}
	if err := os.Rename(staged, path); err != nil {
		return errors.Join(err, os.Rename(incomplete, path))
	}
	os.RemoveAll(incomplete)
	return nil
}

// download puts name's next version beside it, checked.
func download(ctx context.Context, dir, name string, r *Release) error {
	file := asset(name)
	if folders[name] != "" {
		file = folders[name]
	}
	want := r.sums[file]
	if want == "" {
		return fmt.Errorf("%s is not in the release's signed checksum list", file)
	}
	body, err := fetch(ctx, releases+"/download/v"+r.Version+"/"+file, 512<<20)
	if err != nil {
		return err
	}
	if h := sha256.Sum256(body); hex.EncodeToString(h[:]) != want {
		return fmt.Errorf("%s does not match its checksum", file)
	}
	tmp := newPath(dir, name)
	if folders[name] != "" {
		os.RemoveAll(tmp)
		if err := unpack(body, tmp); err != nil {
			return fmt.Errorf("cannot unpack %s: %w", file, err)
		}
		if name == herConsole {
			if st, err := os.Stat(filepath.Join(tmp, herConsoleEntry)); err != nil || !st.Mode().IsRegular() {
				return fmt.Errorf("%s is missing %s", file, herConsoleEntry)
			}
		}
		return nil
	}
	if err := os.WriteFile(tmp, body, 0o755); err != nil {
		return fmt.Errorf("cannot write %s: %w", dir, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if ownVersion(name) {
		if out, err := exec.CommandContext(ctx, tmp, "--version").Output(); err != nil {
			return fmt.Errorf("the downloaded %s does not start: %v %s", name, err, bytes.TrimSpace(out))
		}
		return nil
	}
	out, err := exec.CommandContext(ctx, tmp, "version").Output()
	if err != nil || strings.TrimSpace(string(out)) != r.Version {
		return fmt.Errorf("the downloaded %s does not start as %s: %v %s", name, r.Version, err, bytes.TrimSpace(out))
	}
	return nil
}

// ownVersion: her and hermesh-screen have versions of their own, and go
// only from a release that carries them.
func ownVersion(name string) bool { return name == "her" || name == "hermesh-screen" }

// unpack writes the folders and files of a .tar.gz into dir, its programs
// as programs.
func unpack(body []byte, dir string) error {
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.FromSlash(h.Name)
		if !filepath.IsLocal(name) {
			return fmt.Errorf("%q is outside the archive's folder", h.Name)
		}
		path := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(path, 0o755)
		case tar.TypeReg:
			var b []byte
			if b, err = io.ReadAll(tr); err == nil {
				os.MkdirAll(filepath.Dir(path), 0o755)
				mode := os.FileMode(0o644)
				if h.Mode&0o111 != 0 {
					mode = 0o755
				}
				err = os.WriteFile(path, b, mode)
			}
		}
		if err != nil {
			return err
		}
	}
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
