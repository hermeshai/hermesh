package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// Notifications for the owner's trusted devices while their app is not
// running (an iPhone). A device registers where to reach it (an APNs
// token) and a notice key, signed with its device key; the relay hands the
// registrations to the owner's machines, which seal each notice to the
// device's notice key; the relay forwards the ciphertext to the push
// service. It learns the token and when something happened, not what.

// Push is one notification for the push service.
type Push struct {
	Token string
	// "production" or "development".
	Environment string
	// The APNs JSON payload.
	Payload []byte
	// Urgent notices (a question, a request to connect) are sent at once
	// and break through Focus as time-sensitive; others may wait.
	Urgent bool
	// Groups one session's notifications.
	Thread string
}

// Pusher delivers notifications (APNs, or a spool directory in tests).
type Pusher interface {
	Push(ctx context.Context, p Push) error
}

// PushStore keeps push registrations across restarts. A Backend may
// implement it (the SQLite store does); otherwise they live in memory until
// each device registers again (it does whenever its app starts).
type PushStore interface {
	PushTargets(ownerID string) (map[string]*relayv1.PushTarget, error)
	PutPushTarget(ownerID, deviceKeyID string, t *relayv1.PushTarget) error
	DeletePushTarget(ownerID, deviceKeyID string) error
}

// memoryPush is the PushStore of a Backend without one.
type memoryPush struct {
	mu sync.Mutex
	m  map[string]map[string]*relayv1.PushTarget
}

func (m *memoryPush) PushTargets(ownerID string) (map[string]*relayv1.PushTarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]*relayv1.PushTarget{}
	for k, v := range m.m[ownerID] {
		out[k] = v
	}
	return out, nil
}

func (m *memoryPush) PutPushTarget(ownerID, key string, t *relayv1.PushTarget) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string]map[string]*relayv1.PushTarget{}
	}
	if m.m[ownerID] == nil {
		m.m[ownerID] = map[string]*relayv1.PushTarget{}
	}
	m.m[ownerID][key] = t
	return nil
}

func (m *memoryPush) DeletePushTarget(ownerID, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.m[ownerID], key)
	return nil
}

func (s *Server) pushStore() PushStore {
	if ps, ok := s.store.(PushStore); ok {
		return ps
	}
	return &s.pushMem
}

// verifyPushTarget checks a target against the owner's current trust: its
// device cert verifies, and the registration is signed by that device's
// key, for this owner. It returns the registration and its device key id.
func verifyPushTarget(t *tenant, pt *relayv1.PushTarget) (*identityv1.PushRegistration, string, error) {
	dc, _, err := t.trust.VerifyDeviceCert(pt.DeviceCert)
	if err != nil {
		return nil, "", err
	}
	reg := &identityv1.PushRegistration{}
	pub, err := identity.SelfSigned(pt.Registration, reg, func(r *identityv1.PushRegistration) []byte { return r.DevicePublicKey })
	if err != nil {
		return nil, "", err
	}
	if !bytes.Equal(reg.DevicePublicKey, dc.DevicePublicKey) || reg.OwnerId != t.ownerID {
		return nil, "", errf(connect.CodeInvalidArgument, "push registration is not for this device")
	}
	return reg, identity.KeyID(pub), nil
}

// pushTargets lists the owner's registrations that still verify (a removed
// device's no longer do, and are forgotten).
func (s *Server) pushTargets(t *tenant) map[string]*relayv1.PushTarget {
	all, err := s.pushStore().PushTargets(t.ownerID)
	if err != nil {
		return nil
	}
	for key, pt := range all {
		if _, _, err := verifyPushTarget(t, pt); err != nil {
			s.pushStore().DeletePushTarget(t.ownerID, key)
			delete(all, key)
		}
	}
	return all
}

func (s *Server) broadcastPushTargets(t *tenant) {
	list := &relayv1.PushTargets{}
	for _, pt := range s.pushTargets(t) {
		list.Targets = append(list.Targets, pt)
	}
	s.broadcastMachineUpdate(t, &relayv1.MachineUpdate{Update: &relayv1.MachineUpdate_PushTargets{PushTargets: list}})
}

func (o *ownerControl) RegisterPush(ctx context.Context, req *relayv1.RegisterPushRequest) (*relayv1.RegisterPushResponse, error) {
	p := peerFrom(ctx)
	t := p.t
	if p.deviceCert == nil {
		return nil, errf(connect.CodePermissionDenied, "only a trusted device registers for notifications")
	}
	pt := &relayv1.PushTarget{Registration: req.Registration, DeviceCert: p.deviceCert}
	reg, key, err := verifyPushTarget(t, pt)
	if err != nil {
		return nil, errf(connect.CodeInvalidArgument, err.Error())
	}
	if key != identity.KeyID(p.subject.PublicKey) {
		return nil, errf(connect.CodePermissionDenied, "a device registers only itself")
	}
	switch {
	case reg.Token == "":
		err = o.s.pushStore().DeletePushTarget(t.ownerID, key)
	case len(reg.Token) > 200 || len(reg.NoticePublicKey) != 32 || (reg.Environment != "production" && reg.Environment != "development") || len(reg.Kinds) > 8:
		return nil, errf(connect.CodeInvalidArgument, "bad push registration")
	default:
		err = o.s.pushStore().PutPushTarget(t.ownerID, key, pt)
	}
	if err != nil {
		return nil, err
	}
	o.s.broadcastPushTargets(t)
	return &relayv1.RegisterPushResponse{}, nil
}

// Notices a machine may send per minute: a runaway session must not flood the owner's phone.
const noticesPerMinute = 60

func (n *machineControl) Notify(ctx context.Context, req *relayv1.NotifyRequest) (*relayv1.NotifyResponse, error) {
	s := n.s
	p := peerFrom(ctx)
	if s.cfg.Pusher == nil || len(req.Notices) == 0 {
		return &relayv1.NotifyResponse{}, nil
	}
	if len(req.Notices) > 16 {
		return nil, errf(connect.CodeInvalidArgument, "too many notices at once")
	}
	if !s.noticeLimit.Allow("m:" + p.subject.ID) {
		return nil, errf(connect.CodeResourceExhausted, "too many notices")
	}
	targets := s.pushTargets(p.t)
	for _, sn := range req.Notices {
		pt := targets[sn.DeviceKeyId]
		if pt == nil || len(sn.Sealed) == 0 || len(sn.Sealed) > 3000 || len(sn.Thread) > 64 {
			continue
		}
		s.deliver(pt, sn.Sealed, sn.Kind, sn.Thread)
	}
	return &relayv1.NotifyResponse{}, nil
}

// notifyPairing tells the owner's devices of a new request to connect: the
// relay knows its contents anyway (it is public), and seals it like a
// machine's notice so the phone reads both alike.
func (s *Server) notifyPairing(ownerID, code string, pr *identityv1.PairingRequest) {
	if s.cfg.Pusher == nil || ownerID == "" {
		return
	}
	t, err := s.loadTenant(ownerID)
	if err != nil || t == nil {
		return
	}
	notice := &sessionv1.Notice{Kind: "pairing", At: timestamppb.Now(), PairingKind: pr.Kind, PairingLabel: pr.Label,
		VerificationCode: identity.VerificationCode(pr.Kind, pr.SubjectPublicKey, ownerID), PairingCode: code}
	for _, pt := range s.pushTargets(t) {
		reg := &identityv1.PushRegistration{}
		identity.Unverified(pt.Registration, reg) // verified by pushTargets
		sealed, err := identity.SealNotice(reg.NoticePublicKey, notice)
		if err != nil {
			continue
		}
		s.deliver(pt, sealed, "pairing", "pairing")
	}
}

// deliver hands one sealed notice to the push service. What the person
// sees before their phone opens it says nothing ("Hermesh: something needs
// you"); the app's notification extension replaces it with the content.
func (s *Server) deliver(pt *relayv1.PushTarget, sealed []byte, kind, thread string) {
	reg := &identityv1.PushRegistration{}
	identity.Unverified(pt.Registration, reg)
	urgent := kind == "question" || kind == "pairing"
	level := "active"
	if urgent {
		level = "time-sensitive"
	}
	payload, _ := json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert":              map[string]string{"title-loc-key": "notice.title", "loc-key": "notice.body"},
			"mutable-content":    1,
			"sound":              "default",
			"thread-id":          threadID(thread),
			"interruption-level": level,
		},
		"hermesh": map[string]string{"n": base64.StdEncoding.EncodeToString(sealed), "k": kind},
	})
	p := Push{Token: reg.Token, Environment: reg.Environment, Payload: payload, Urgent: urgent, Thread: threadID(thread)}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.cfg.Pusher.Push(ctx, p); err != nil {
			s.log.Warn("push failed", "owner", reg.OwnerId, "err", err)
		}
	}()
}

// threadID is an opaque, stable name for a thread (the push service sees it).
func threadID(thread string) string {
	if thread == "" {
		return ""
	}
	h := sha256.Sum256([]byte("hermesh-thread\x00" + thread))
	return hex.EncodeToString(h[:8])
}
