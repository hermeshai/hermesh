package owner

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// Zero-knowledge delta mailbox client on the owner's trusted device:
// Registers this device's persisted X25519 sync key with the relay (no APNs
// token or root-key prompt required), pulls unseen machine-signed encrypted
// deltas in 1 RTT, verifies machine signatures against the owner's root chain,
// decrypts locally, and advances her_bots / her_conversations monotonically so
// Her shows the latest messages even while the machine sleeps or is offline.

const kSyncKey = "sync_key"

const deltaMigrations = `
CREATE TABLE delta_cursors (
  stream_id TEXT PRIMARY KEY,
  seq INTEGER NOT NULL
);
`

func (s *store) deltaCursors() (map[string]uint64, error) {
	rows, err := s.db.Query(`SELECT stream_id, seq FROM delta_cursors`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]uint64{}
	for rows.Next() {
		var id string
		var seq uint64
		if err := rows.Scan(&id, &seq); err != nil {
			return nil, err
		}
		out[id] = seq
	}
	return out, rows.Err()
}

func (s *store) setDeltaCursor(streamID string, seq uint64) error {
	_, err := s.db.Exec(`INSERT INTO delta_cursors(stream_id, seq) VALUES(?, ?)
ON CONFLICT(stream_id) DO UPDATE SET seq = excluded.seq WHERE excluded.seq > delta_cursors.seq`, streamID, seq)
	return err
}

func (o *Owner) syncPrivateKey() ([]byte, []byte, error) {
	o.syncMu.Lock()
	defer o.syncMu.Unlock()
	priv, err := o.st.get(kSyncKey)
	if err != nil {
		return nil, nil, err
	}
	if len(priv) == 32 {
		pub, err := identity.SyncPublicKey(priv)
		if err == nil {
			return priv, pub, nil
		}
	}
	priv, pub, err := identity.NewSyncKey()
	if err != nil {
		return nil, nil, err
	}
	if err := o.st.set(kSyncKey, priv); err != nil {
		return nil, nil, err
	}
	return priv, pub, nil
}

func (o *Owner) rememberMachineCerts(list *relayv1.ListMachinesResponse) {
	if list == nil {
		return
	}
	o.mu.Lock()
	trust := o.trust
	o.mu.Unlock()
	if trust == nil {
		return
	}
	certs := map[string]*identityv1.Signed{}
	for _, m := range list.Machines {
		if m.GetMachineCert() == nil {
			continue
		}
		mc, _, err := trust.VerifyMachineCert(m.MachineCert)
		if err != nil {
			continue
		}
		certs[mc.MachineId] = m.MachineCert
	}
	o.mu.Lock()
	if o.machineCerts == nil {
		o.machineCerts = map[string]*identityv1.Signed{}
	}
	for id, cert := range certs {
		o.machineCerts[id] = cert
	}
	o.mu.Unlock()
}

func (o *Owner) registerSyncKey(ctx context.Context, ctl relayv1connect.OwnerControlServiceClient) error {
	o.mu.Lock()
	device, ownerID := o.device, o.ownerID
	o.mu.Unlock()
	if device == nil || ownerID == "" {
		return nil
	}
	_, pub, err := o.syncPrivateKey()
	if err != nil {
		return err
	}
	reg := &identityv1.SyncRegistration{
		OwnerId:         ownerID,
		DevicePublicKey: identity.MarshalPublicKey(device.Public()),
		SyncPublicKey:   pub,
		IssuedAt:        timestamppb.Now(),
	}
	signed, err := identity.Sign(reg, device)
	if err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err = ctl.RegisterSync(rctx, &relayv1.RegisterSyncRequest{Registration: signed})
	if err != nil && connect.CodeOf(err) == connect.CodeUnimplemented {
		return nil
	}
	return err
}

// PullDeltas pulls any unseen encrypted deltas from the relay mailbox and
// applies them to owner.db. Safe to call concurrently.
func (o *Owner) PullDeltas(ctx context.Context) (int, error) {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return 0, err
	}
	return o.pullDeltas(ctx, ctl)
}

func (o *Owner) pullDeltas(ctx context.Context, ctl relayv1connect.OwnerControlServiceClient) (int, error) {
	o.pullMu.Lock()
	defer o.pullMu.Unlock()

	o.mu.Lock()
	device, trust := o.device, o.trust
	o.mu.Unlock()
	if device == nil || trust == nil {
		return 0, nil
	}
	priv, _, err := o.syncPrivateKey()
	if err != nil {
		return 0, err
	}
	deviceKeyID := identity.KeyID(device.Public())
	applied := 0

	for page := 0; page < 8; page++ {
		cursors, err := o.st.deltaCursors()
		if err != nil {
			return applied, err
		}
		req := &relayv1.PullDeltasRequest{Limit: 64}
		for streamID, seq := range cursors {
			req.Cursors = append(req.Cursors, &relayv1.StreamCursor{StreamId: streamID, AfterSeq: seq})
		}
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		res, err := ctl.PullDeltas(pctx, req)
		cancel()
		if err != nil {
			if connect.CodeOf(err) == connect.CodeUnimplemented {
				return applied, nil
			}
			return applied, err
		}
		if len(res.Deltas) == 0 {
			break
		}
		for _, rec := range res.Deltas {
			if rec.GetEnvelope() == nil {
				continue
			}
			machineID, ok := strings.CutPrefix(rec.GetStreamId(), "her:")
			if !ok || machineID == "" {
				_ = o.st.setDeltaCursor(rec.GetStreamId(), rec.GetSeq())
				continue
			}
			cert := o.machineCert(ctx, ctl, machineID)
			if cert == nil {
				continue
			}
			_, plain, err := identity.OpenDelta(trust, deviceKeyID, priv, rec.Envelope, cert)
			if err != nil {
				o.log.Debug("delta not opened", "stream", rec.StreamId, "seq", rec.Seq, "err", err)
				// Advance cursor past entries not sealed for this device (e.g.
				// published before this device registered its sync key) so we
				// don't re-pull them forever.
				_ = o.st.setDeltaCursor(rec.StreamId, rec.Seq)
				continue
			}
			if o.applyHerDelta(machineID, plain) {
				applied++
			}
			_ = o.st.setDeltaCursor(rec.StreamId, rec.Seq)
		}
		if !res.HasMore {
			break
		}
	}
	if applied > 0 {
		o.log.Info("her delta sync applied", "count", applied)
		if o.rep != nil {
			o.rep.kick()
		}
	}
	return applied, nil
}

func (o *Owner) machineCert(ctx context.Context, ctl relayv1connect.OwnerControlServiceClient, machineID string) *identityv1.Signed {
	o.mu.Lock()
	cert := o.machineCerts[machineID]
	o.mu.Unlock()
	if cert != nil {
		return cert
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	res, err := ctl.ListMachines(lctx, &relayv1.ListMachinesRequest{})
	cancel()
	if err == nil {
		o.rememberMachineCerts(res)
	}
	o.mu.Lock()
	cert = o.machineCerts[machineID]
	o.mu.Unlock()
	return cert
}

func (o *Owner) applyHerDelta(machineID string, plain []byte) bool {
	var p struct {
		Bot      json.RawMessage `json:"bot"`
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if json.Unmarshal(plain, &p) != nil {
		return false
	}
	changed := false
	if len(p.Bot) > 0 {
		if _, err := o.st.db.Exec(`INSERT INTO her_bots(machine_id,bot) VALUES(?,?) ON CONFLICT(machine_id) DO UPDATE SET bot=excluded.bot WHERE bot<>excluded.bot`, machineID, []byte(p.Bot)); err == nil {
			changed = true
		}
	}
	if len(p.Snapshot) > 0 {
		var incoming herSnapshot
		if json.Unmarshal(p.Snapshot, &incoming) != nil || len(incoming.Session) == 0 {
			return changed
		}
		var inHdr struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(incoming.Session, &inHdr) != nil || inHdr.SessionID == "" {
			return changed
		}
		// Monotonic guard: never roll back a newer snapshot for the same session.
		_, curRaw, err := o.st.herKept(machineID)
		if err == nil && len(curRaw) > 0 {
			var cur herSnapshot
			if json.Unmarshal(curRaw, &cur) == nil {
				var curHdr struct {
					SessionID string `json:"session_id"`
				}
				if json.Unmarshal(cur.Session, &curHdr) == nil && curHdr.SessionID == inHdr.SessionID && cur.LastSeq >= incoming.LastSeq {
					return changed
				}
			}
		}
		if _, err := o.st.db.Exec(`INSERT INTO her_conversations(machine_id,snapshot) VALUES(?,?) ON CONFLICT(machine_id) DO UPDATE SET snapshot=excluded.snapshot`, machineID, []byte(p.Snapshot)); err == nil {
			changed = true
		}
	}
	return changed
}
