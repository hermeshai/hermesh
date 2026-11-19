package relay

import (
	"bytes"
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// Zero-knowledge encrypted delta mailbox:
// Trusted devices register an X25519 sync key (signed by their certified device
// key); the relay distributes verified SyncTargets to the owner's machines.
// Machines seal state deltas (e.g. Her conversation updates) with AES-256-GCM +
// per-device HPKE DEKs, sign the DeltaEnvelope with their machine key, and
// append the opaque ciphertext here. Each trusted device pulls unseen entries
// independently using its own after_seq cursor without deleting them.

const (
	defaultDeltaTTL       = 7 * 24 * time.Hour
	maxDeltaTTL           = 30 * 24 * time.Hour
	deltaMaxPerStream     = 256
	maxDeltaEnvelopeBytes = 512 << 10 // 512 KiB per sealed delta
	defaultPullLimit      = 64
	maxPullLimit          = 256
)

// DeltaEntry is one stored opaque delta record.
type DeltaEntry struct {
	StreamID  string
	Seq       uint64
	Envelope  []byte // marshaled identityv1.Signed
	StoredAt  time.Time
	ExpiresAt time.Time
}

// DeltaStore persists sync keys and encrypted deltas across restarts. A Backend
// may implement it (the SQLite Store does); otherwise an in-memory store is used.
type DeltaStore interface {
	SyncTargets(ownerID string) (map[string]*relayv1.SyncTarget, error)
	PutSyncTarget(ownerID, deviceKeyID string, t *relayv1.SyncTarget) error
	DeleteSyncTarget(ownerID, deviceKeyID string) error

	AppendDelta(ownerID, streamID string, envelope []byte, storedAt, expiresAt time.Time) (uint64, error)
	PullDeltas(ownerID string, cursors map[string]uint64, knownOnly bool, limit int, now time.Time) ([]DeltaEntry, bool, error)
	DeltaHeads(ownerID string, now time.Time) (map[string]uint64, error)
	PurgeDeltas(now time.Time) error
}

type memStream struct {
	nextSeq uint64
	entries []DeltaEntry
}

// memoryDeltas is the fallback DeltaStore when a Backend does not implement it.
type memoryDeltas struct {
	mu      sync.Mutex
	targets map[string]map[string]*relayv1.SyncTarget
	streams map[string]map[string]*memStream // ownerID -> streamID -> stream
}

func (m *memoryDeltas) SyncTargets(ownerID string) (map[string]*relayv1.SyncTarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]*relayv1.SyncTarget{}
	maps.Copy(out, m.targets[ownerID])
	return out, nil
}

func (m *memoryDeltas) PutSyncTarget(ownerID, key string, t *relayv1.SyncTarget) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.targets == nil {
		m.targets = map[string]map[string]*relayv1.SyncTarget{}
	}
	if m.targets[ownerID] == nil {
		m.targets[ownerID] = map[string]*relayv1.SyncTarget{}
	}
	m.targets[ownerID][key] = t
	return nil
}

func (m *memoryDeltas) DeleteSyncTarget(ownerID, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.targets[ownerID], key)
	return nil
}

func (m *memoryDeltas) AppendDelta(ownerID, streamID string, envelope []byte, storedAt, expiresAt time.Time) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.streams == nil {
		m.streams = map[string]map[string]*memStream{}
	}
	if m.streams[ownerID] == nil {
		m.streams[ownerID] = map[string]*memStream{}
	}
	st := m.streams[ownerID][streamID]
	if st == nil {
		st = &memStream{}
		m.streams[ownerID][streamID] = st
	}
	st.nextSeq++
	seq := st.nextSeq
	st.entries = append(st.entries, DeltaEntry{
		StreamID:  streamID,
		Seq:       seq,
		Envelope:  bytes.Clone(envelope),
		StoredAt:  storedAt,
		ExpiresAt: expiresAt,
	})
	if len(st.entries) > deltaMaxPerStream {
		st.entries = st.entries[len(st.entries)-deltaMaxPerStream:]
	}
	return seq, nil
}

func (m *memoryDeltas) PullDeltas(ownerID string, cursors map[string]uint64, knownOnly bool, limit int, now time.Time) ([]DeltaEntry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ownerStreams := m.streams[ownerID]
	if len(ownerStreams) == 0 {
		return nil, false, nil
	}
	names := make([]string, 0, len(ownerStreams))
	for name := range ownerStreams {
		if knownOnly {
			if _, ok := cursors[name]; !ok {
				continue
			}
		}
		names = append(names, name)
	}
	slices.Sort(names)
	var out []DeltaEntry
	hasMore := false
	for _, name := range names {
		after := cursors[name]
		st := ownerStreams[name]
		for _, e := range st.entries {
			if e.Seq <= after || !e.ExpiresAt.After(now) {
				continue
			}
			if len(out) >= limit {
				hasMore = true
				return out, hasMore, nil
			}
			out = append(out, e)
		}
	}
	return out, hasMore, nil
}

func (m *memoryDeltas) DeltaHeads(ownerID string, now time.Time) (map[string]uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]uint64{}
	for name, st := range m.streams[ownerID] {
		for i := len(st.entries) - 1; i >= 0; i-- {
			if st.entries[i].ExpiresAt.After(now) {
				out[name] = st.entries[i].Seq
				break
			}
		}
	}
	return out, nil
}

func (m *memoryDeltas) PurgeDeltas(now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ownerStreams := range m.streams {
		for name, st := range ownerStreams {
			kept := st.entries[:0]
			for _, e := range st.entries {
				if e.ExpiresAt.After(now) {
					kept = append(kept, e)
				}
			}
			st.entries = kept
			if len(st.entries) == 0 {
				delete(ownerStreams, name)
			}
		}
	}
	return nil
}

func (s *Server) deltaStore() DeltaStore {
	if ds, ok := s.store.(DeltaStore); ok {
		return ds
	}
	return &s.deltaMem
}

func verifySyncTarget(t *tenant, st *relayv1.SyncTarget) (*identityv1.SyncRegistration, string, error) {
	dc, _, err := t.trust.VerifyDeviceCert(st.DeviceCert)
	if err != nil {
		return nil, "", err
	}
	reg := &identityv1.SyncRegistration{}
	pub, err := identity.SelfSigned(st.Registration, reg, func(r *identityv1.SyncRegistration) []byte { return r.DevicePublicKey })
	if err != nil {
		return nil, "", err
	}
	if !bytes.Equal(reg.DevicePublicKey, dc.DevicePublicKey) || reg.OwnerId != t.ownerID || len(reg.SyncPublicKey) != 32 {
		return nil, "", errf(connect.CodeInvalidArgument, "sync registration is invalid for this device")
	}
	return reg, identity.KeyID(pub), nil
}

func (s *Server) syncTargets(t *tenant) map[string]*relayv1.SyncTarget {
	all, err := s.deltaStore().SyncTargets(t.ownerID)
	if err != nil {
		return nil
	}
	for key, st := range all {
		if _, _, err := verifySyncTarget(t, st); err != nil {
			s.deltaStore().DeleteSyncTarget(t.ownerID, key)
			delete(all, key)
		}
	}
	return all
}

func (s *Server) broadcastSyncTargets(t *tenant) {
	list := &relayv1.SyncTargets{}
	for _, st := range s.syncTargets(t) {
		list.Targets = append(list.Targets, st)
	}
	s.broadcastMachineUpdate(t, &relayv1.MachineUpdate{Update: &relayv1.MachineUpdate_SyncTargets{SyncTargets: list}})
}

func (o *ownerControl) RegisterSync(ctx context.Context, req *relayv1.RegisterSyncRequest) (*relayv1.RegisterSyncResponse, error) {
	p := peerFrom(ctx)
	t := p.t
	if p.deviceCert == nil {
		return nil, errf(connect.CodePermissionDenied, "only a trusted device registers a sync key")
	}
	st := &relayv1.SyncTarget{Registration: req.Registration, DeviceCert: p.deviceCert}
	_, key, err := verifySyncTarget(t, st)
	if err != nil {
		return nil, errf(connect.CodeInvalidArgument, err.Error())
	}
	if key != identity.KeyID(p.subject.PublicKey) {
		return nil, errf(connect.CodePermissionDenied, "a device registers only itself")
	}
	if err := o.s.deltaStore().PutSyncTarget(t.ownerID, key, st); err != nil {
		return nil, err
	}
	o.s.broadcastSyncTargets(t)
	return &relayv1.RegisterSyncResponse{}, nil
}

func (m *machineControl) AppendDelta(ctx context.Context, req *relayv1.AppendDeltaRequest) (*relayv1.AppendDeltaResponse, error) {
	p := peerFrom(ctx)
	t := p.t
	streamID := strings.TrimSpace(req.GetStreamId())
	if streamID == "" || len(streamID) > 128 {
		return nil, errf(connect.CodeInvalidArgument, "invalid stream_id")
	}
	// A machine may only append to streams scoped to its own machine ID (e.g. "her:<machine_id>").
	if !strings.HasSuffix(streamID, ":"+p.subject.ID) && streamID != p.subject.ID {
		return nil, errf(connect.CodePermissionDenied, "machine may only append to its own streams")
	}
	if req.GetEnvelope() == nil {
		return nil, errf(connect.CodeInvalidArgument, "missing envelope")
	}
	raw, err := proto.Marshal(req.GetEnvelope())
	if err != nil {
		return nil, err
	}
	if len(raw) > maxDeltaEnvelopeBytes {
		return nil, errf(connect.CodeInvalidArgument, "delta envelope too large")
	}
	ttl := defaultDeltaTTL
	if req.GetTtlSeconds() > 0 {
		ttl = min(time.Duration(req.GetTtlSeconds())*time.Second, maxDeltaTTL)
	}
	now := time.Now()
	seq, err := m.s.deltaStore().AppendDelta(t.ownerID, streamID, raw, now, now.Add(ttl))
	if err != nil {
		return nil, err
	}
	m.s.wakeOwner(t)
	return &relayv1.AppendDeltaResponse{Seq: seq}, nil
}

func (o *ownerControl) PullDeltas(ctx context.Context, req *relayv1.PullDeltasRequest) (*relayv1.PullDeltasResponse, error) {
	p := peerFrom(ctx)
	t := p.t
	cursors := make(map[string]uint64, len(req.GetCursors()))
	for _, c := range req.GetCursors() {
		if c.GetStreamId() != "" {
			cursors[c.GetStreamId()] = c.GetAfterSeq()
		}
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = defaultPullLimit
	}
	if limit > maxPullLimit {
		limit = maxPullLimit
	}
	entries, hasMore, err := o.s.deltaStore().PullDeltas(t.ownerID, cursors, req.GetKnownOnly(), limit, time.Now())
	if err != nil {
		return nil, err
	}
	out := make([]*relayv1.DeltaRecord, 0, len(entries))
	for _, e := range entries {
		env := &identityv1.Signed{}
		if err := proto.Unmarshal(e.Envelope, env); err != nil {
			continue
		}
		out = append(out, &relayv1.DeltaRecord{
			StreamId: e.StreamID,
			Seq:      e.Seq,
			Envelope: env,
			StoredAt: timestamppb.New(e.StoredAt),
		})
	}
	return &relayv1.PullDeltasResponse{Deltas: out, HasMore: hasMore}, nil
}
