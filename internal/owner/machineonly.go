package owner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/connaddr"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// A Mac can join its owner as a machine only, not as a trusted device (a
// work Mac, say): it holds no root key, approves nothing and sees only its
// own projects and sessions, which it settles as the owner would (answer,
// stop, archive). Its machine asks to join like any other and is
// approved on a trusted device. When the owner id came from the center (the
// person signed in, rather than typing the connection address), the person
// then types here the two digits the approving device shows, computed from
// the machine cert it signed: a center that handed the request to another
// owner cannot make the person's own device show them.

const kMachineOnly = "machine_only"

func (o *Owner) machineOnly() bool {
	v, _ := o.st.get(kMachineOnly)
	return string(v) == "1"
}

// onlyMachine is this machine's in-process access when this device is only
// a machine; ok is false when it is not one.
func (o *Owner) onlyMachine() (l *machine.Local, ok bool, err error) {
	if o.Initialized() || !o.machineOnly() {
		return nil, false, nil
	}
	o.mu.Lock()
	n := o.localMachine
	o.mu.Unlock()
	if n == nil {
		return nil, true, connect.NewError(connect.CodeUnavailable, errors.New("this Mac's machine is not running"))
	}
	return n.Local(), true, nil
}

// requestMachineJoin is RequestJoin for a machine only: this device's
// machine signs its own pairing request, pinned to the owner.
func (o *Owner) requestMachineJoin(ctx context.Context, req *localv1.RequestJoinRequest) (*localv1.RequestJoinResponse, error) {
	if o.machineOnly() {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("this Mac is already a machine of an owner"))
	}
	relayURL := normalizeRelayURL(req.RelayUrl)
	n, err := o.openLocalMachine()
	if err != nil {
		return nil, err
	}
	defer n.Close()
	label := req.RootLabel
	if label == "" {
		label = machine.HostLabel("")
	}
	for len(label) > 64 { // what a relay accepts
		_, size := utf8.DecodeLastRuneInString(label)
		label = label[:len(label)-size]
	}
	host, _ := os.Hostname()
	spki := n.PublicKeySPKI()
	signed, err := n.SignPairingRequest(&identityv1.PairingRequest{Kind: identityv1.PairingKind_PAIRING_KIND_MACHINE, SubjectPublicKey: spki,
		Label: label, Hostname: host, Platform: runtime.GOOS + "/" + runtime.GOARCH, OwnerId: req.OwnerId, CreatedAt: timestamppb.Now(),
		ConfirmMatch: !req.OwnerTyped})
	if err != nil {
		return nil, err
	}
	res, err := o.publicClient(relayURL).SubmitPairing(ctx, &relayv1.SubmitPairingRequest{Request: signed})
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	o.join = &pendingJoin{code: res.Code, relayURL: relayURL, ownerID: req.OwnerId, rootPub: spki, machineName: label,
		machineOnly: true, match: !req.OwnerTyped}
	o.mu.Unlock()
	return &localv1.RequestJoinResponse{Code: res.Code, ExpiresAt: res.ExpiresAt,
		VerificationCode: identity.VerificationCode(identityv1.PairingKind_PAIRING_KIND_MACHINE, spki, req.OwnerId)}, nil
}

// waitMachineJoin checks the credentials a trusted device issued: a chain
// of the owner asked for, and a machine cert for this machine's key.
func (o *Owner) waitMachineJoin(j *pendingJoin, creds *identityv1.Credentials) (*localv1.WaitJoinResponse, error) {
	trust := identity.NewTrust(j.ownerID)
	if _, err := trust.AcceptRootChain(creds.GetRootChain()); err != nil {
		return nil, fmt.Errorf("the approval does not check out: %w", err)
	}
	cert := creds.GetMachineCert()
	_, pub, err := trust.VerifyMachineCert(cert)
	if err != nil {
		return nil, fmt.Errorf("the approval does not check out: %w", err)
	}
	if !bytes.Equal(identity.MarshalPublicKey(pub), j.rootPub) {
		return nil, errors.New("the approval is for another machine")
	}
	o.mu.Lock()
	j.chain, j.machineCert = creds.GetRootChain(), cert
	o.mu.Unlock()
	res := &localv1.WaitJoinResponse{Done: true, MatchRequired: j.match}
	for _, rk := range trust.Root().RootKeys {
		pub, _ := identity.ParsePublicKey(rk.PublicKey)
		if identity.KeyID(pub) == cert.SignerKeyId {
			res.ApprovedBy = rk.Label
		}
		res.Devices = append(res.Devices, &localv1.Device{KeyId: identity.KeyID(pub), Label: rk.Label, Platform: rk.Platform})
	}
	return res, nil
}

// confirmMachineJoin installs the machine cert and starts the machine.
func (o *Owner) confirmMachineJoin(ctx context.Context, j *pendingJoin) (*localv1.ConfirmJoinResponse, error) {
	n, err := o.openLocalMachine()
	if err != nil {
		return nil, err
	}
	err = n.Install(j.ownerID, j.relayURL, j.chain, j.machineCert)
	if err == nil {
		err = n.SetName(j.machineName)
	}
	n.Close()
	if err != nil {
		return nil, err
	}
	if err := o.st.set(kMachineOnly, []byte("1")); err != nil {
		return nil, err
	}
	o.mu.Lock()
	o.join = nil
	o.mu.Unlock()
	if err := o.startLocalMachine(); err != nil {
		return nil, err
	}
	return &localv1.ConfirmJoinResponse{OwnerId: j.ownerID, ConnectAddress: connaddr.Format(j.relayURL, j.ownerID)}, nil
}

// rebindLocalMachine publishes the cert that makes this device's running
// machine its own, and hands it the cert and the chain listing the device.
func (o *Owner) rebindLocalMachine(ctx context.Context, chain *identityv1.RootChain, cert *identityv1.Signed) error {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return err
	}
	if _, err := ctl.PublishMachineCert(ctx, &relayv1.PublishMachineCertRequest{MachineCert: cert}); err != nil {
		return err
	}
	o.mu.Lock()
	n := o.localMachine
	o.mu.Unlock()
	if n != nil {
		n.Adopt(chain, cert)
	}
	return nil
}

// machineOnlyStatus describes this device when it is only a machine.
func (o *Owner) machineOnlyStatus(st *localv1.GetStatusResponse) {
	st.MachineOnly = true
	o.mu.Lock()
	n := o.localMachine
	o.mu.Unlock()
	if n == nil {
		return
	}
	st.OwnerId, st.RelayUrl = n.OwnerID(), n.RelayURL()
	st.RelayConnected, st.Removed = n.Connected(), n.Refused()
}

// leaveAsMachine stops the turns under way, has the relay remove this
// machine from its owner (when it can be reached; otherwise the owner's
// devices list it offline until they remove it), and retires it: it keeps
// its projects and sessions, and joins again, to any owner, under a new id.
func (o *Owner) leaveAsMachine(ctx context.Context) error {
	o.mu.Lock()
	n := o.localMachine
	o.mu.Unlock()
	if n != nil {
		n.Local().InterruptActive(ctx)
		lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if err := n.LeaveOwner(lctx); err != nil {
			o.log.Warn("leaving: the relay did not remove this machine; the owner's devices list it until they do", "err", err)
		}
		cancel()
	}
	o.stopLocalMachine()
	if err := machine.Retire(o.localMachineConfig()); err != nil {
		return err
	}
	return o.st.set(kMachineOnly, []byte{})
}

// ---------------------------------------------------------------------------
// The local API on a machine only: this machine, in process.

func (o *Owner) onlyMachineList(l *machine.Local) []*localv1.Machine {
	info := l.Info()
	o.mu.Lock()
	n := o.localMachine
	o.mu.Unlock()
	return []*localv1.Machine{{MachineId: info.MachineId, MachineName: info.MachineName, Online: n != nil && n.Connected(), Version: machine.Version,
		Local: true, Status: info.Status}}
}

func onlyMachineProjects(ctx context.Context, l *machine.Local) (*localv1.ListProjectsResponse, error) {
	res, err := l.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	id := l.Info().MachineId
	out := &localv1.ListProjectsResponse{}
	for _, p := range res.Projects {
		out.Projects = append(out.Projects, &localv1.Project{MachineId: id, ProjectId: p.ProjectId, DefaultBaseRef: p.DefaultBaseRef, Workspace: p.Workspace,
			Description: p.Description, Agents: p.Agents, Path: p.Path})
	}
	return out, nil
}

// onlyThisMachine refuses a machine id other than this one.
func onlyThisMachine(l *machine.Local, machineID string) error {
	if info := l.Info(); machineID != "" && machineID != info.MachineId && machineID != info.MachineName {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("no machine %q", machineID))
	}
	return nil
}

func onlyMachineSettings(ctx context.Context, l *machine.Local, machineID string, build func(*identityv1.AdminCommand)) error {
	if err := onlyThisMachine(l, machineID); err != nil {
		return err
	}
	_, err := l.ApplySettings(ctx, build)
	return err
}

// onlyMachineWatch tells the watcher this machine is reachable, then sends
// its session changes.
func onlyMachineWatch(ctx context.Context, l *machine.Local, send func(*localv1.WatchSessionsResponse) error) error {
	if err := send(&localv1.WatchSessionsResponse{MachinesChanged: true}); err != nil {
		return err
	}
	return l.WatchSessions(ctx, func(m *sessionv1.WatchSessionsResponse) error {
		if len(m.Sessions) == 0 {
			return nil
		}
		return send(&localv1.WatchSessionsResponse{Sessions: m.Sessions})
	})
}
