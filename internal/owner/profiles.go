package owner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"github.com/oklog/ulid/v2"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/internal/flock"
)

// Device is the owner role on one device, for every owner it set up or
// joined (docs/trusted-devices.md 4.8). Each has a profile: a directory of
// its own (its owner.db, device key and machine) and a root key of its own
// in the signer, which all profiles share. One profile is current at a
// time: it alone runs (its relay connection and its machine), and the
// local API acts on it.
type Device struct {
	cfg Config
	hub *signHub

	mu       sync.Mutex
	profiles map[string]*Owner
	current  string
	runCtx   context.Context
	stopRun  context.CancelFunc
	runDone  chan struct{}
}

// Open opens the device's profiles under cfg.DataDir, and an empty one to
// set up when there is none.
func Open(cfg Config) (*Device, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if _, err := flock.Acquire(cfg.DataDir); err != nil {
		return nil, err
	}
	d := &Device{cfg: cfg, hub: newSignHub(), profiles: map[string]*Owner{}}
	if err := d.migrate(); err != nil {
		return nil, fmt.Errorf("moving the owner into a profile: %w", err)
	}
	entries, err := os.ReadDir(d.profilesDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		o, err := d.openProfile(e.Name())
		if err != nil {
			return nil, err
		}
		d.profiles[e.Name()] = o
	}
	cur, _ := os.ReadFile(d.currentFile())
	d.current = strings.TrimSpace(string(cur))
	if d.profiles[d.current] == nil {
		id, err := d.emptyProfile()
		if err != nil {
			return nil, err
		}
		if err := d.setCurrent(id); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// migrate moves the one owner a device had before profiles (owner.db and
// keys at the top of DataDir) into a profile of its own, made current. Its
// machine stays where it was, beside DataDir, since the git metadata of
// its worktrees names that path: the profile records it (kMachineDir).
func (d *Device) migrate() error {
	if _, err := os.Stat(filepath.Join(d.cfg.DataDir, "owner.db")); err != nil {
		return nil
	}
	id := strings.ToLower(ulid.Make().String())
	dir := filepath.Join(d.profilesDir(), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, name := range []string{"keys", "owner.db-wal", "owner.db-shm", "owner.db"} { // owner.db last: it marks what is left to move
		if err := os.Rename(filepath.Join(d.cfg.DataDir, name), filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	machineDir := filepath.Join(filepath.Dir(d.cfg.DataDir), "machine")
	if _, err := os.Stat(machineDir); err == nil {
		st, err := openStore(filepath.Join(dir, "owner.db"))
		if err != nil {
			return err
		}
		err = st.set(kMachineDir, []byte(machineDir))
		st.db.Close()
		if err != nil {
			return err
		}
	}
	d.cfg.Logger.Info("moved the owner into a profile", "profile", id, "machine", machineDir)
	return os.WriteFile(d.currentFile(), []byte(id), 0o600)
}

func (d *Device) profilesDir() string { return filepath.Join(d.cfg.DataDir, "profiles") }
func (d *Device) currentFile() string { return filepath.Join(d.cfg.DataDir, "current") }

func (d *Device) openProfile(id string) (*Owner, error) {
	cfg := d.cfg
	cfg.DataDir = filepath.Join(d.profilesDir(), id)
	return openProfile(cfg, d.hub)
}

// emptyProfile returns a profile that is not set up, opening a new one when
// there is none. Callers hold d.mu, or own d alone.
func (d *Device) emptyProfile() (string, error) {
	for _, id := range d.ids() {
		if o := d.profiles[id]; !o.Initialized() && !o.machineOnly() {
			return id, nil
		}
	}
	id := strings.ToLower(ulid.Make().String())
	o, err := d.openProfile(id)
	if err != nil {
		return "", err
	}
	d.profiles[id] = o
	return id, nil
}

// ids lists the profiles oldest first (their ids are ULIDs).
func (d *Device) ids() []string {
	ids := make([]string, 0, len(d.profiles))
	for id := range d.profiles {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (d *Device) setCurrent(id string) error {
	if err := os.WriteFile(d.currentFile(), []byte(id), 0o600); err != nil {
		return err
	}
	d.current = id
	return nil
}

// owner is the current profile.
func (d *Device) owner() *Owner {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.profiles[d.current]
}

// Run runs the current profile, and the one that becomes current after it,
// until ctx ends.
func (d *Device) Run(ctx context.Context) {
	d.mu.Lock()
	d.runCtx = ctx
	d.startLocked()
	d.mu.Unlock()
	<-ctx.Done()
	d.mu.Lock()
	d.runCtx = nil
	d.mu.Unlock()
	d.stop()
}

// startLocked starts the current profile when the device runs. Callers hold
// d.mu, and stopped the profile that ran before.
func (d *Device) startLocked() {
	if d.runCtx == nil || d.runCtx.Err() != nil {
		return
	}
	ctx, cancel := context.WithCancel(d.runCtx)
	done := make(chan struct{})
	o := d.profiles[d.current]
	go func() {
		defer close(done)
		o.Run(ctx)
	}()
	d.stopRun, d.runDone = cancel, done
}

// stop stops the profile that runs, and waits until it did.
func (d *Device) stop() {
	d.mu.Lock()
	cancel, done := d.stopRun, d.runDone
	d.stopRun, d.runDone = nil, nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// Pause stops what keeps the current profile's world current, its relay
// connection and its replica's sources, keeping what they hold: the iPhone
// app left the front and is about to be suspended. Run, the next watch or
// list start them again.
func (d *Device) Pause() {
	o := d.owner()
	o.rep.pause()
	o.dropRelay()
}

// Reconnect drops the current profile's relay connection and follows its
// machines again from what it holds: the network changed, and the
// connections made on the old one may be dead without anything noticing.
func (d *Device) Reconnect() {
	o := d.owner()
	o.dropRelay()
	o.rep.restart()
}

// restart runs the current profile again, as it now is (signed in or out).
func (d *Device) restart() {
	d.stop()
	d.mu.Lock()
	d.startLocked()
	d.mu.Unlock()
}

// switchTo makes the profile id current.
func (d *Device) switchTo(id string) error {
	d.mu.Lock()
	same := id == d.current
	d.mu.Unlock()
	if same {
		return nil
	}
	d.stop()
	d.mu.Lock()
	defer d.mu.Unlock()
	err := d.setCurrent(id)
	d.startLocked()
	return err
}

// ListProfiles lists the profiles that are set up, and the current one.
func (d *Device) ListProfiles() []*localv1.Profile {
	d.mu.Lock()
	ids, cur := d.ids(), d.current
	profiles := make([]*Owner, len(ids))
	for i, id := range ids {
		profiles[i] = d.profiles[id]
	}
	d.mu.Unlock()
	var out []*localv1.Profile
	for i, o := range profiles {
		st := o.Status()
		if !st.Initialized && !st.MachineOnly && ids[i] != cur {
			continue
		}
		out = append(out, &localv1.Profile{Id: ids[i], OwnerId: st.OwnerId, RelayUrl: st.RelayUrl, Account: st.Account,
			Current: ids[i] == cur, SignedOut: st.SignedOut, MachineOnly: st.MachineOnly})
	}
	return out
}

// SwitchProfile makes a profile that is set up current.
func (d *Device) SwitchProfile(id string) error {
	d.mu.Lock()
	o := d.profiles[id]
	d.mu.Unlock()
	if o == nil || (!o.Initialized() && !o.machineOnly()) {
		return connect.NewError(connect.CodeNotFound, errors.New("no such account on this device"))
	}
	return d.switchTo(id)
}

// AddProfile makes an empty profile current, to set up or join an owner.
func (d *Device) AddProfile() (string, error) {
	d.mu.Lock()
	id, err := d.emptyProfile()
	d.mu.Unlock()
	if err != nil {
		return "", err
	}
	return id, d.switchTo(id)
}

// SignOut signs the current profile out.
func (d *Device) SignOut(ctx context.Context) error {
	if err := d.owner().SignOut(ctx); err != nil {
		return err
	}
	d.restart()
	return nil
}

// SignIn signs the current profile in again.
func (d *Device) SignIn(ctx context.Context, req *localv1.SignInRequest) error {
	if err := d.owner().SignIn(ctx, req); err != nil {
		return err
	}
	d.restart()
	return nil
}

// Leave forgets the current profile's owner, with the profile stopped so
// that its machine is not starting while it is retired. A machine only
// leaves while it runs: its machine asks the relay to remove it.
func (d *Device) Leave(ctx context.Context) error {
	o := d.owner()
	if !o.Initialized() {
		return o.Leave(ctx)
	}
	d.stop()
	defer d.restart()
	return o.Leave(ctx)
}

// checkNewOwner refuses joining an owner another profile already belongs to.
func (d *Device) checkNewOwner(ownerID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, o := range d.profiles {
		if id == d.current {
			continue
		}
		o.mu.Lock()
		has := o.ownerID != "" && o.ownerID == ownerID
		o.mu.Unlock()
		if has {
			return connect.NewError(connect.CodeAlreadyExists, errors.New("this device already has this account; switch to it"))
		}
	}
	return nil
}
