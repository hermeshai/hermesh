package owner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// Erasing everything on a trusted device (docs/erase-everything.md). The
// device that starts it signs the order and keeps running until
// FinishErase, to show the others acking; any other device of the owner
// erases itself when the relay refuses it and shows it the order.

// kErased: what the last erase did (an Erased), kept in the empty profile
// that replaced the erased one, for its first run to say.
const kErased = "erased"

// finishAfter: when the device that erased everything erases itself if the
// app did not ask it to (FinishErase) by then.
const finishAfter = 2 * time.Minute

// eraseRun is an erase this device started: what there was, and the id
// each acks as.
type eraseRun struct {
	id       string
	items    []*localv1.EraseItem
	subjects []string
}

// EraseEverything signs the owner's erase order (Touch ID) and hands it to
// the relay, which forgets the owner and refuses every device, machine and
// agent of it from then on.
func (o *Owner) EraseEverything(ctx context.Context) (string, error) {
	if err := o.requireInit(); err != nil {
		return "", err
	}
	o.mu.Lock()
	started := o.erasing != nil
	o.mu.Unlock()
	if started {
		return "", connect.NewError(connect.CodeFailedPrecondition, errors.New("this device is already erasing everything"))
	}
	run, err := o.eraseItems(ctx)
	if err != nil {
		return "", err
	}
	b := make([]byte, 16)
	rand.Read(b)
	run.id = hex.EncodeToString(b)
	o.mu.Lock()
	ownerID := o.ownerID
	o.mu.Unlock()
	signed, err := o.signRoot(ctx, &identityv1.EraseOrder{OwnerId: ownerID, EraseId: run.id, IssuedAt: timestamppb.Now()})
	if err != nil {
		return "", err
	}
	ctl, _, err := o.control(ctx)
	if err != nil {
		return "", err
	}
	o.mu.Lock()
	o.erasing = run
	o.mu.Unlock()
	if _, err := ctl.EraseOwner(ctx, &relayv1.EraseOwnerRequest{Order: signed[0]}); err != nil {
		o.mu.Lock()
		if o.erasing == run {
			o.erasing = nil
		}
		o.mu.Unlock()
		return "", err
	}
	// The app calls FinishErase once the others had their moment; a window
	// closed meanwhile doesn't leave this device half erased.
	time.AfterFunc(finishAfter, func() {
		o.mu.Lock()
		still, onErase := o.erasing == run, o.onErase
		o.mu.Unlock()
		if still && onErase != nil {
			onErase(o, run.id, "")
		}
	})
	return run.id, nil
}

// eraseItems lists what an erase reaches: the trusted devices, machines
// and agents, with the id each acks as. A device's own machine is the
// device's: it erases with it.
func (o *Owner) eraseItems(ctx context.Context) (*eraseRun, error) {
	run := &eraseRun{}
	add := func(kind, name, subject string, this, online bool) {
		run.items = append(run.items, &localv1.EraseItem{Kind: kind, Name: name, ThisDevice: this, Online: online})
		run.subjects = append(run.subjects, subject)
	}
	devices, err := o.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	for _, d := range devices {
		add("device", d.Label, d.KeyId, d.ThisDevice, d.MachineOnline && !d.ThisDevice)
	}
	machines, err := o.machines(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range machines {
		if m.DeviceKeyId == "" {
			add("machine", m.MachineName, m.MachineId, false, m.Online)
		}
	}
	clients, err := o.ListClients(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range clients {
		if !c.Revoked {
			// A connector's key is the center's: ending its connection with
			// the owner is all there is to do, and it is done at once.
			subject := c.ClientId
			if c.Custodian != "" {
				subject = ""
			}
			add("agent", c.Label, subject, false, false)
		}
	}
	return run, nil
}

// EraseStatus says which of an erase's devices, machines and agents acked;
// without an erase id, what erasing everything would reach now.
func (o *Owner) EraseStatus(ctx context.Context, eraseID string) (*localv1.EraseStatusResponse, error) {
	if eraseID == "" {
		if err := o.requireInit(); err != nil {
			return nil, err
		}
		run, err := o.eraseItems(ctx)
		if err != nil {
			return nil, err
		}
		res := &localv1.EraseStatusResponse{Items: run.items}
		if o.Status().Hosted {
			ctl, _, err := o.control(ctx)
			if err != nil {
				return nil, err
			}
			a, err := ctl.GetAccount(ctx, &relayv1.GetAccountRequest{})
			if err != nil {
				return nil, err
			}
			res.Account = a.Account
		}
		return res, nil
	}
	o.mu.Lock()
	run, relayURL, ownerID := o.erasing, o.relayURL, o.ownerID
	o.mu.Unlock()
	if run == nil || run.id != eraseID {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no erase in progress with this id"))
	}
	acked := map[string]bool{}
	if info, err := o.publicClient(relayURL).GetInfo(ctx, &relayv1.GetInfoRequest{OwnerId: ownerID}); err == nil {
		for _, a := range info.EraseAcks {
			acked[a] = true
		}
	}
	res := &localv1.EraseStatusResponse{}
	for i, it := range run.items {
		it := proto.CloneOf(it)
		it.Done = !it.ThisDevice && (run.subjects[i] == "" || acked[run.subjects[i]])
		res.Items = append(res.Items, it)
	}
	return res, nil
}

// eraseOrder is the owner's erase order the relay shows a refused device,
// verified; nil for none.
func (o *Owner) eraseOrder(info *relayv1.GetInfoResponse) *identityv1.EraseOrder {
	o.mu.Lock()
	trust := o.trust
	o.mu.Unlock()
	if info.EraseOrder == nil || trust == nil {
		return nil
	}
	trust.AcceptRootChain(info.RootChain)
	order, err := trust.VerifyEraseOrder(info.EraseOrder)
	if err != nil {
		o.log.Warn("an erase order that does not verify", "err", err)
		return nil
	}
	return order
}

// deviceName is the trusted device whose root key is keyID, by its name.
func (o *Owner) deviceName(keyID string) string {
	o.mu.Lock()
	trust := o.trust
	o.mu.Unlock()
	for _, k := range trust.Root().GetRootKeys() {
		if pub, err := identity.ParsePublicKey(k.PublicKey); err == nil && identity.KeyID(pub) == keyID {
			return k.Label
		}
	}
	return ""
}

// eraseData erases what this profile keeps besides its directory: it acks
// (its device key, and its machine's), then erases its machine, and has the
// signer delete its root key. It returns the worktrees kept.
func (o *Owner) eraseData(ctx context.Context, eraseID, keepDir string) []string {
	o.mu.Lock()
	relayURL, rootPub, device, cert := o.relayURL, o.rootPub, o.device, o.deviceCert
	o.mu.Unlock()
	hc := o.cfg.HTTPClient
	if eraseID != "" && device != nil && rootPub != nil {
		if err := machine.AckErase(ctx, hc, relayURL, eraseID, identity.KeyID(rootPub), cert, device); err != nil {
			o.log.Warn("telling the owner this device is erasing", "err", err)
		}
	}
	o.stopLocalMachine()
	cfg := o.localMachineConfig()
	var kept []string
	if key, err := identity.LoadKey(machine.KeyPath(cfg.DataDir)); err == nil {
		if eraseID != "" { // "": the machine acked itself
			if err := machine.AckErase(ctx, hc, relayURL, eraseID, identity.MachineID(identity.MarshalPublicKey(key.Public())), nil, key); err != nil {
				o.log.Warn("telling the owner this device's machine is erasing", "err", err)
			}
		}
		if kept, err = machine.Erase(cfg, keepDir); err != nil {
			o.log.Warn("erasing this device's machine", "err", err)
		}
	}
	if rootPub != nil {
		if err := o.hub.deleteKey(ctx, identity.KeyID(rootPub)); err != nil {
			o.log.Warn("erasing: the root key stays in the signer", "err", err)
		}
	}
	o.st.db.Close()
	return kept
}

func (o *Owner) setErased(e *localv1.Erased) {
	b, _ := proto.Marshal(e)
	o.st.set(kErased, b)
	o.mu.Lock()
	o.erased = e
	o.mu.Unlock()
}

// EraseEverything starts an erase on the current profile.
func (d *Device) EraseEverything(ctx context.Context) (string, error) {
	return d.owner().EraseEverything(ctx)
}

// FinishErase erases this device after the others had their moment: the
// current profile goes, an empty one opens on its first run.
func (d *Device) FinishErase(ctx context.Context) error {
	d.mu.Lock()
	id, o := d.current, d.profiles[d.current]
	d.mu.Unlock()
	o.mu.Lock()
	run := o.erasing
	o.mu.Unlock()
	if run == nil {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("no erase in progress"))
	}
	var pending []string
	if st, err := o.EraseStatus(ctx, run.id); err == nil {
		for _, it := range st.Items {
			if !it.Done && !it.ThisDevice {
				pending = append(pending, it.Name)
			}
		}
	}
	return d.eraseProfile(ctx, id, o, run.id, pending, "")
}

// erasedBy erases profile o, whose owner erased everything from another
// device.
func (d *Device) erasedBy(o *Owner, eraseID, by string) {
	d.mu.Lock()
	id := ""
	for pid, p := range d.profiles {
		if p == o {
			id = pid
		}
	}
	d.mu.Unlock()
	if id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := d.eraseProfile(ctx, id, o, eraseID, nil, by); err != nil {
		o.log.Warn("erasing this device", "err", err)
	}
}

// eraseProfile erases profile id: its data, then its directory. The current
// one is replaced by an empty profile, which says what was done.
func (d *Device) eraseProfile(ctx context.Context, id string, o *Owner, eraseID string, pending []string, by string) error {
	o.mu.Lock()
	again := o.wiping
	o.wiping = true
	o.mu.Unlock()
	if again {
		return nil
	}
	d.mu.Lock()
	current := id == d.current
	d.mu.Unlock()
	if current {
		d.stop()
	}
	// Worktrees with uncommitted changes are kept beside the profiles.
	kept := o.eraseData(ctx, eraseID, filepath.Join(d.cfg.DataDir, "kept-worktrees"))
	dir := filepath.Join(d.profilesDir(), id)
	if err := machine.RemoveExcept(dir, kept); err != nil {
		o.log.Warn("erasing this device's profile", "err", err)
	}
	os.Remove(dir) // left when a worktree could not be moved out
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.profiles, id)
	if !current {
		return nil
	}
	next, err := d.emptyProfile()
	if err != nil {
		return err
	}
	d.profiles[next].setErased(&localv1.Erased{EraseId: eraseID, At: timestamppb.Now(), Pending: pending, KeptWorktrees: kept, By: by})
	if err := d.setCurrent(next); err != nil {
		return err
	}
	d.startLocked()
	return nil
}
