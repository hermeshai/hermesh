package owner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	"github.com/hermeshai/hermesh/internal/connaddr"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// Trusted devices (docs/trusted-devices.md): every device with the app is
// one of the owner's trusted devices, listed in the root chain. A device
// joins with the approval of one already there, and is removed by another.

// pendingJoin is this device asking to join an owner, until the person
// confirms they approved it on one of their devices.
type pendingJoin struct {
	code, relayURL, ownerID string
	account                 string // RequestJoinRequest.account
	rootPub                 []byte
	device                  *identity.SoftwareKey
	deviceCert              *identityv1.Signed
	machineCert             *identityv1.Signed // this device's own machine, if asked for
	machineName             string
	chain                   *identityv1.RootChain // once approved
	wrong                   int                   // match codes typed wrong
	// A machine only (machineonly.go): rootPub is the machine's key and
	// machineCert, once approved, the cert a trusted device issued; match:
	// ConfirmJoin needs the digits.
	machineOnly, match bool
	// A machine only joining as a trusted device: machineCert is its running
	// machine's, now this device's own.
	rebind bool
}

// RequestJoin asks to become one of an owner's trusted devices. One Touch
// ID signs the request (with the device's root key, proving it holds it)
// and, ahead of time, its device cert and its own machine's cert, which are
// valid once it joined.
func (o *Owner) RequestJoin(ctx context.Context, req *localv1.RequestJoinRequest) (*localv1.RequestJoinResponse, error) {
	if o.Initialized() {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("this device already belongs to an owner"))
	}
	if req.OwnerId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("owner id is required"))
	}
	if o.cfg.NoLocalMachine && (req.MachineOnly || req.LocalMachineName != "") {
		return nil, errNoLocalMachine
	}
	if err := o.checkRelay(ctx, normalizeRelayURL(req.RelayUrl)); err != nil {
		return nil, err
	}
	if req.MachineOnly {
		return o.requestMachineJoin(ctx, req)
	}
	var running *machine.Machine
	if o.machineOnly() {
		// A machine already: it stays one, as this device's own once it
		// joined (so removing the device removes it too).
		req.LocalMachineName = ""
		o.mu.Lock()
		running = o.localMachine
		o.mu.Unlock()
	}
	rootPub, err := o.rootKey(ctx, req.RootPublicKey)
	if err != nil {
		return nil, err
	}
	req.RootPublicKey = identity.MarshalPublicKey(rootPub)
	relayURL := normalizeRelayURL(req.RelayUrl)
	device, err := identity.GenerateKey()
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	o.mu.Lock()
	o.rootPub = rootPub
	o.mu.Unlock()
	msgs := []proto.Message{
		&identityv1.PairingRequest{Kind: identityv1.PairingKind_PAIRING_KIND_DEVICE, SubjectPublicKey: req.RootPublicKey, Label: req.RootLabel,
			Hostname: host, Platform: req.RootPlatform, OwnerId: req.OwnerId, CreatedAt: timestamppb.Now()},
		deviceCert(req.OwnerId, device, req.RootLabel)}
	if req.LocalMachineName != "" {
		nc, err := o.localMachineCert(req.OwnerId, rootPub)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, nc)
	}
	if running != nil {
		msgs = append(msgs, &identityv1.MachineCert{OwnerId: req.OwnerId, MachineId: running.MachineID(), MachinePublicKey: running.PublicKeySPKI(),
			IssuedAt: timestamppb.Now(), DeviceKeyId: identity.KeyID(rootPub)})
	}
	signed, err := o.signRoot(ctx, msgs...)
	if err != nil {
		return nil, err
	}
	res, err := o.publicClient(relayURL).SubmitPairing(ctx, &relayv1.SubmitPairingRequest{Request: signed[0]})
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	o.join = &pendingJoin{code: res.Code, relayURL: relayURL, ownerID: req.OwnerId, account: req.Account, rootPub: req.RootPublicKey, device: device, deviceCert: signed[1]}
	if len(signed) > 2 {
		o.join.machineCert, o.join.machineName, o.join.rebind = signed[2], req.LocalMachineName, running != nil
	}
	o.mu.Unlock()
	return &localv1.RequestJoinResponse{Code: res.Code, ExpiresAt: res.ExpiresAt,
		VerificationCode: identity.VerificationCode(identityv1.PairingKind_PAIRING_KIND_DEVICE, req.RootPublicKey, req.OwnerId)}, nil
}

func (o *Owner) pendingJoin(code string) (*pendingJoin, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.join == nil || o.join.code != identity.NormalizePairingCode(code) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such join request; ask again"))
	}
	return o.join, nil
}

// WaitJoin waits (up to about 25s) for a trusted device to approve. The
// chain it returns must name the owner asked for and list this device; it
// is kept aside until ConfirmJoin.
func (o *Owner) WaitJoin(ctx context.Context, code string) (*localv1.WaitJoinResponse, error) {
	j, err := o.pendingJoin(code)
	if err != nil {
		return nil, err
	}
	w, err := o.publicClient(j.relayURL).WaitPairing(ctx, &relayv1.WaitPairingRequest{Code: j.code, KnownOwnerId: j.ownerID})
	if err != nil {
		return nil, err
	}
	if w.Expired {
		return &localv1.WaitJoinResponse{Expired: true}, nil
	}
	if !w.Done {
		return &localv1.WaitJoinResponse{}, nil
	}
	if j.machineOnly {
		return o.waitMachineJoin(j, w.GetCredentials())
	}
	chain := w.GetCredentials().GetRootChain()
	trust := identity.NewTrust(j.ownerID)
	if _, err := trust.AcceptRootChain(chain); err != nil {
		return nil, fmt.Errorf("the approval does not check out: %w", err)
	}
	if !hasKey(trust.Root(), j.rootPub) {
		return nil, errors.New("the approval does not list this device")
	}
	if _, _, err := trust.VerifyDeviceCert(j.deviceCert); err != nil {
		return nil, err
	}
	if j.machineCert != nil {
		if _, _, err := trust.VerifyMachineCert(j.machineCert); err != nil {
			return nil, err
		}
	}
	o.mu.Lock()
	j.chain = chain
	o.mu.Unlock()
	last := chain.Certs[len(chain.Certs)-1]
	res := &localv1.WaitJoinResponse{Done: true, MatchRequired: true}
	for _, rk := range trust.Root().RootKeys {
		pub, _ := identity.ParsePublicKey(rk.PublicKey)
		if identity.KeyID(pub) == last.SignerKeyId {
			res.ApprovedBy = rk.Label
		}
		res.Devices = append(res.Devices, &localv1.Device{KeyId: identity.KeyID(pub), Label: rk.Label, Platform: rk.Platform, ThisDevice: bytes.Equal(rk.PublicKey, j.rootPub)})
	}
	return res, nil
}

// ConfirmJoin keeps the owner once the person typed the two digits the
// approving device showed (the owner id came from the center, which is not
// trusted: only their own device can show the digits of this root cert),
// or forgets it.
func (o *Owner) ConfirmJoin(ctx context.Context, code string, accept bool, match string) (*localv1.ConfirmJoinResponse, error) {
	j, err := o.pendingJoin(code)
	if err != nil {
		return nil, err
	}
	if !accept {
		o.mu.Lock()
		o.join = nil
		o.mu.Unlock()
		return &localv1.ConfirmJoinResponse{}, nil
	}
	if j.chain == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("not approved yet"))
	}
	admitted := j.chain.Certs[len(j.chain.Certs)-1] // the cert that admits it
	if j.machineOnly {
		admitted = j.machineCert
	}
	if (j.match || !j.machineOnly) && match != identity.JoinMatchCode(admitted.GetBody()) {
		o.mu.Lock()
		j.wrong++
		if j.wrong >= 3 {
			o.join = nil
		}
		o.mu.Unlock()
		if j.wrong >= 3 {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("the number did not match three times; ask to join again"))
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("the number does not match the one your other device shows"))
	}
	if j.machineOnly {
		return o.confirmMachineJoin(ctx, j)
	}
	if err := o.saveIdentity(j.ownerID, j.relayURL, j.rootPub, j.chain, j.device, j.deviceCert, j.account); err != nil {
		return nil, err
	}
	o.mu.Lock()
	o.join = nil
	o.mu.Unlock()
	if o.machineOnly() {
		// It was only a machine of this owner: its machine runs on, now this
		// trusted device's own.
		o.st.set(kLocalMachine, []byte("1"))
		o.st.set(kMachineOnly, []byte{})
	}
	if j.rebind {
		if err := o.rebindLocalMachine(ctx, j.chain, j.machineCert); err != nil {
			return nil, fmt.Errorf("this Mac as a machine: %w", err)
		}
	} else if j.machineCert != nil {
		if err := o.installLocalMachine(ctx, j.machineCert, j.machineName); err != nil {
			return nil, fmt.Errorf("this Mac as a machine: %w", err)
		}
	}
	return &localv1.ConfirmJoinResponse{OwnerId: j.ownerID, ConnectAddress: connaddr.Format(j.relayURL, j.ownerID)}, nil
}

func hasKey(rc *identityv1.RootCert, spki []byte) bool {
	return slices.ContainsFunc(rc.GetRootKeys(), func(rk *identityv1.RootKey) bool { return bytes.Equal(rk.PublicKey, spki) })
}

// adoptChain keeps a longer root chain the relay handed over.
func (o *Owner) adoptChain(c *identityv1.RootChain) {
	o.mu.Lock()
	trust := o.trust
	o.mu.Unlock()
	if c == nil || trust == nil {
		return
	}
	changed, err := trust.AcceptRootChain(c)
	if err != nil {
		o.log.Warn("root chain from the relay refused", "err", err)
		return
	}
	if changed {
		b, _ := proto.Marshal(trust.RootChain())
		o.st.set(kRootChain, b)
	}
}

// latestChain brings the root chain up to date before changing it.
func (o *Owner) latestChain(ctx context.Context) (relayv1connect.OwnerControlServiceClient, error) {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return nil, err
	}
	res, err := ctl.ListMachines(ctx, &relayv1.ListMachinesRequest{})
	if err != nil {
		return nil, err
	}
	o.adoptChain(res.RootChain)
	return ctl, nil
}

// extendChain signs the next root cert, with msgs in the same Touch ID, and
// publishes the longer chain; it returns the signed msgs.
func (o *Owner) extendChain(ctx context.Context, ctl relayv1connect.OwnerControlServiceClient, keys []*identityv1.RootKey, msgs ...proto.Message) ([]*identityv1.Signed, error) {
	cur := o.trust.Root()
	next := &identityv1.RootCert{OwnerId: o.ownerID, RootKeys: keys, Version: cur.Version + 1, IssuedAt: timestamppb.Now()}
	signed, err := o.signRoot(ctx, append([]proto.Message{next}, msgs...)...)
	if err != nil {
		return nil, err
	}
	chain := o.trust.RootChain()
	chain.Certs = append(chain.Certs, signed[0])
	if _, err := o.trust.AcceptRootChain(chain); err != nil {
		return nil, err
	}
	b, _ := proto.Marshal(chain)
	o.st.set(kRootChain, b)
	if _, err := ctl.PublishRootChain(ctx, &relayv1.PublishRootChainRequest{RootChain: chain}); err != nil {
		return nil, err
	}
	return signed, nil
}

// ListDevices lists the owner's trusted devices, with their own machines.
func (o *Owner) ListDevices(ctx context.Context) ([]*localv1.Device, error) {
	if err := o.requireInit(); err != nil {
		return nil, err
	}
	machines, err := o.ListMachines(ctx) // also brings the chain up to date
	if err != nil {
		return nil, err
	}
	own := o.Status().DeviceKeyId
	var out []*localv1.Device
	for _, rk := range o.trust.Root().GetRootKeys() {
		pub, err := identity.ParsePublicKey(rk.PublicKey)
		if err != nil {
			continue
		}
		d := &localv1.Device{KeyId: identity.KeyID(pub), Label: rk.Label, Platform: rk.Platform}
		d.ThisDevice = d.KeyId == own
		for _, m := range machines {
			if m.DeviceKeyId == d.KeyId {
				d.MachineId, d.MachineOnline = m.MachineId, m.Online
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// ApproveDevicePairing adds a device that asked to join: the next root cert
// lists it, signed by this device. It returns the device's key id and the
// two digits to type on it.
func (o *Owner) ApproveDevicePairing(ctx context.Context, code string) (string, string, error) {
	pr, _, err := o.lookupPairing(ctx, code)
	if err != nil {
		return "", "", err
	}
	if pr.Kind != identityv1.PairingKind_PAIRING_KIND_DEVICE {
		return "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("this code does not belong to a device"))
	}
	ctl, err := o.latestChain(ctx)
	if err != nil {
		return "", "", err
	}
	pub, err := identity.ParsePublicKey(pr.SubjectPublicKey)
	if err != nil {
		return "", "", err
	}
	cur := o.trust.Root()
	var last *identityv1.Signed
	if hasKey(cur, pr.SubjectPublicKey) {
		chain := o.trust.RootChain().Certs // approved before; answer again
		last = chain[len(chain)-1]
	} else {
		keys := append(slices.Clone(cur.RootKeys), &identityv1.RootKey{PublicKey: pr.SubjectPublicKey, Label: pr.Label, Platform: pr.Platform})
		signed, err := o.extendChain(ctx, ctl, keys)
		if err != nil {
			return "", "", err
		}
		last = signed[0]
	}
	if _, err := ctl.CompletePairing(ctx, &relayv1.CompletePairingRequest{Code: identity.NormalizePairingCode(code), Credential: last}); err != nil {
		return "", "", err
	}
	return identity.KeyID(pub), identity.JoinMatchCode(last.GetBody()), nil
}

// RemoveDevice removes another trusted device in one Touch ID: the next
// root cert without it, the machine certs and grants it had signed
// re-issued by this device (so they keep working), and its own machine
// revoked. A device cannot remove itself: what it signed must be re-issued
// by one that stays.
func (o *Owner) RemoveDevice(ctx context.Context, keyID string) (*localv1.RemoveDeviceResponse, error) {
	if err := o.requireInit(); err != nil {
		return nil, err
	}
	if keyID == o.Status().DeviceKeyId {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("remove this device from another of your devices"))
	}
	ctl, err := o.latestChain(ctx)
	if err != nil {
		return nil, err
	}
	cur := o.trust.Root()
	var keys []*identityv1.RootKey
	for _, rk := range cur.RootKeys {
		if pub, err := identity.ParsePublicKey(rk.PublicKey); err == nil && identity.KeyID(pub) == keyID {
			continue
		}
		keys = append(keys, rk)
	}
	if len(keys) == len(cur.RootKeys) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such trusted device"))
	}
	machines, err := ctl.ListMachines(ctx, &relayv1.ListMachinesRequest{})
	if err != nil {
		return nil, err
	}
	clients, err := ctl.ListClients(ctx, &relayv1.ListClientsRequest{})
	if err != nil {
		return nil, err
	}
	var msgs []proto.Message
	var gone []string
	for _, e := range machines.Machines {
		nc := &identityv1.MachineCert{}
		if identity.Unverified(e.MachineCert, nc) != nil || nc.OwnerId != o.ownerID {
			continue
		}
		switch {
		case nc.DeviceKeyId == keyID:
			gone = append(gone, nc.MachineId)
		case e.MachineCert.SignerKeyId == keyID:
			nc.IssuedAt = timestamppb.Now()
			msgs = append(msgs, nc)
		}
	}
	nMachines := len(msgs)
	for _, c := range clients.Clients {
		g := &identityv1.Grant{}
		if c.Revoked || c.Grant.GetSignerKeyId() != keyID || identity.Unverified(c.Grant, g) != nil || g.OwnerId != o.ownerID {
			continue
		}
		g.Version++
		msgs = append(msgs, g)
	}
	signed, err := o.extendChain(ctx, ctl, keys, msgs...)
	if err != nil {
		return nil, err
	}
	for i, s := range signed[1:] {
		if i < nMachines {
			_, err = ctl.PublishMachineCert(ctx, &relayv1.PublishMachineCertRequest{MachineCert: s})
		} else {
			_, err = ctl.PublishGrant(ctx, &relayv1.PublishGrantRequest{Grant: s})
			o.st.putGrant(s, false)
		}
		if err != nil {
			return nil, err
		}
	}
	if len(gone) > 0 {
		if _, err := o.revoke(ctx, &identityv1.RevocationList{OwnerId: o.ownerID, RevokedMachineIds: gone}); err != nil {
			return nil, err
		}
	}
	return &localv1.RemoveDeviceResponse{ReissuedMachines: uint32(nMachines), ReissuedGrants: uint32(len(signed) - 1 - nMachines)}, nil
}

// checkRemoved asks the relay, after it refused this device, whether the
// device is still one of the owner's: another may have removed it, or the
// owner started over on a hosted center.
func (o *Owner) checkRemoved() {
	o.mu.Lock()
	ownerID, relayURL, rootPub, trust := o.ownerID, o.relayURL, o.rootPub, o.trust
	o.mu.Unlock()
	if ownerID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := o.publicClient(relayURL).GetInfo(ctx, &relayv1.GetInfoRequest{OwnerId: ownerID})
	if err != nil {
		return
	}
	removed := info.OwnerId != ownerID
	if !removed {
		o.adoptChain(info.RootChain)
		removed = !hasKey(trust.Root(), identity.MarshalPublicKey(rootPub))
	}
	o.mu.Lock()
	if o.ownerID == ownerID { // not left meanwhile
		o.removed = removed
	}
	o.mu.Unlock()
}

// Leave forgets the owner on this device once it was removed, so it can
// be set up or join again, and has the signer delete its root key. Its own
// machine, removed with it, is retired: it keeps its projects and comes
// back under a new id.
func (o *Owner) Leave(ctx context.Context) error {
	if !o.Initialized() && o.machineOnly() {
		return o.leaveAsMachine(ctx)
	}
	o.mu.Lock()
	removed := o.removed
	o.mu.Unlock()
	if !removed {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("this device is still one of the owner's; remove it from another device first"))
	}
	o.stopLocalMachine()
	if err := machine.Retire(o.localMachineConfig()); err != nil {
		return err
	}
	o.mu.Lock()
	if o.client != nil {
		o.client.Close()
		o.client = nil
	}
	rootPub := o.rootPub
	o.ownerID, o.relayURL, o.rootPub, o.trust, o.device, o.deviceCert, o.relayInfo, o.removed = "", "", nil, nil, nil, nil, nil, false
	o.signedOut, o.account = false, ""
	o.mu.Unlock()
	for _, k := range []string{kOwnerID, kSignedOut, kAccount} {
		if err := o.st.set(k, []byte{}); err != nil {
			return err
		}
	}
	if err := o.st.clearGrants(); err != nil {
		return err
	}
	if err := o.hub.deleteKey(ctx, identity.KeyID(rootPub)); err != nil {
		o.log.Warn("leaving: the old root key stays in the signer", "err", err)
	}
	return nil
}

// SignOut stops using this profile's keys without giving them up: it stops
// its notifications, and Run (restarted by the device) neither connects nor
// starts its machine until SignIn.
func (o *Owner) SignOut(ctx context.Context) error {
	if err := o.requireInit(); err != nil {
		return err
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if err := o.RegisterPush(pctx, &localv1.RegisterPushRequest{}); err != nil {
		o.log.Debug("signing out: notifications not stopped", "err", err)
	}
	cancel()
	if err := o.st.set(kSignedOut, []byte("1")); err != nil {
		return err
	}
	o.mu.Lock()
	o.signedOut = true
	o.mu.Unlock()
	return nil
}

// SignIn takes this profile's keys up again. A hosted center first checks
// that the person signed in to the account that owns it (Claim with
// resume). If the owner meanwhile removed this device, or started over, it
// signs in removed, for Leave.
func (o *Owner) SignIn(ctx context.Context, req *localv1.SignInRequest) error {
	if !o.Initialized() {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("Hermesh is not set up yet"))
	}
	if !o.isSignedOut() {
		return nil
	}
	o.mu.Lock()
	ownerID, relayURL, rootPub, trust, deviceCert := o.ownerID, o.relayURL, o.rootPub, o.trust, o.deviceCert
	o.mu.Unlock()
	info, err := o.publicClient(relayURL).GetInfo(ctx, &relayv1.GetInfoRequest{OwnerId: ownerID})
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("relay unreachable: %w", err))
	}
	removed := info.OwnerId != ownerID
	if !removed {
		if info.Hosted {
			if req.ClaimCode == "" {
				return connect.NewError(connect.CodeInvalidArgument, errors.New("sign in to the account in the browser first"))
			}
			if _, err := o.publicClient(relayURL).Claim(ctx, &relayv1.ClaimRequest{ClaimCode: req.ClaimCode, ClaimVerifier: req.ClaimVerifier,
				RootChain: trust.RootChain(), DeviceCert: deviceCert, Resume: true}); err != nil {
				return err
			}
		}
		o.adoptChain(info.RootChain)
		removed = !hasKey(trust.Root(), identity.MarshalPublicKey(rootPub))
	}
	if err := o.st.set(kSignedOut, []byte{}); err != nil {
		return err
	}
	if req.Account != "" {
		if err := o.st.set(kAccount, []byte(req.Account)); err != nil {
			return err
		}
	}
	o.mu.Lock()
	o.signedOut, o.removed = false, removed
	if req.Account != "" {
		o.account = req.Account
	}
	o.mu.Unlock()
	return nil
}
