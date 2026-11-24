package machine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

// Encrypted delta mirroring for Her (zero-knowledge relay mailbox):
// Whenever Her's default assistant updates, the machine reads a compact,
// shown-only conversation snapshot, seals it with AES-256-GCM + per-device
// HPKE DEKs for every verified trusted device SyncTarget, signs the
// DeltaEnvelope with the machine key, and appends it to the relay's delta
// mailbox ("her:<machine_id>"). Trusted devices can then catch up in 1 RTT
// even while this machine sleeps or is offline.

type deltaState struct {
	mu      sync.Mutex
	targets []identity.SyncRecipient
	seq     uint64
	// pending holds the newest Her delta waiting for SyncTargets or a relay
	// connection; superseded automatically by newer snapshots.
	pendingBot  []byte
	pendingSnap []byte
	lastHash    string
	sending     bool
}

// herDeltaPayload is the decrypted JSON payload carried inside a DeltaEnvelope
// for stream "her:<machine_id>".
type herDeltaPayload struct {
	Bot      json.RawMessage `json:"bot,omitempty"`
	Snapshot json.RawMessage `json:"snapshot,omitempty"`
}

func (n *Machine) acceptSyncTargets(sts *relayv1.SyncTargets) {
	n.mu.Lock()
	trust, owner := n.trust, n.ownerID
	n.mu.Unlock()
	if trust == nil {
		return
	}
	var out []identity.SyncRecipient
	for _, st := range sts.GetTargets() {
		dc, _, err := trust.VerifyDeviceCert(st.DeviceCert)
		if err != nil {
			continue
		}
		reg := &identityv1.SyncRegistration{}
		pub, err := identity.SelfSigned(st.Registration, reg, func(r *identityv1.SyncRegistration) []byte { return r.DevicePublicKey })
		if err != nil || reg.OwnerId != owner || !bytes.Equal(reg.DevicePublicKey, dc.DevicePublicKey) || len(reg.SyncPublicKey) != 32 {
			continue
		}
		out = append(out, identity.SyncRecipient{
			DeviceKeyID: identity.KeyID(pub),
			PublicKey:   append([]byte(nil), reg.SyncPublicKey...),
		})
	}
	n.deltas.mu.Lock()
	n.deltas.targets = out
	hasPending := len(n.deltas.pendingSnap) > 0
	n.deltas.mu.Unlock()
	if hasPending && len(out) > 0 {
		go n.flushPendingDelta(nil)
	}
}

func (n *Machine) herDeltaChanged(botID string, botRaw []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	openRes, err := n.her.OpenBotSnapshot(ctx, botID)
	if err != nil {
		return
	}
	_ = n.PublishHerDelta(ctx, botRaw, openRes)
}

// PublishHerDelta compacts a bots/open result (or pre-compacted snapshot),
// seals it for all verified trusted devices, and appends it to the relay's
// delta mailbox. If the relay is disconnected or no SyncTargets have arrived
// yet, it retains the latest snapshot and flushes it once connected.
func (n *Machine) PublishHerDelta(ctx context.Context, botRaw, openOrSnap []byte) error {
	snap := compactHerOpenForDelta(openOrSnap)
	if len(snap) == 0 {
		return errors.New("empty Her snapshot")
	}
	n.deltas.mu.Lock()
	n.deltas.pendingBot = append([]byte(nil), botRaw...)
	n.deltas.pendingSnap = snap
	n.deltas.mu.Unlock()
	return n.flushPendingDelta(ctx)
}

func (n *Machine) flushPendingDelta(ctx context.Context) error {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
	}
	n.mu.Lock()
	conn, ownerID := n.conn, n.ownerID
	n.mu.Unlock()
	if conn == nil || ownerID == "" {
		return nil
	}
	return n.flushPendingDeltaOn(ctx, conn, ownerID)
}

func (n *Machine) sendPendingDeltas(conn *transport.RelayConn) {
	n.mu.Lock()
	ownerID := n.ownerID
	n.mu.Unlock()
	if ownerID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = n.flushPendingDeltaOn(ctx, conn, ownerID)
}

func (n *Machine) flushPendingDeltaOn(ctx context.Context, conn *transport.RelayConn, ownerID string) error {
	n.deltas.mu.Lock()
	if n.deltas.sending || len(n.deltas.pendingSnap) == 0 || len(n.deltas.targets) == 0 {
		n.deltas.mu.Unlock()
		return nil
	}
	botRaw := append([]byte(nil), n.deltas.pendingBot...)
	snap := append([]byte(nil), n.deltas.pendingSnap...)
	targets := append([]identity.SyncRecipient(nil), n.deltas.targets...)
	n.deltas.seq++
	seq := n.deltas.seq
	n.deltas.sending = true
	n.deltas.mu.Unlock()

	defer func() {
		n.deltas.mu.Lock()
		n.deltas.sending = false
		n.deltas.mu.Unlock()
	}()

	payload, err := json.Marshal(herDeltaPayload{Bot: botRaw, Snapshot: snap})
	if err != nil {
		return err
	}
	myID := n.MachineID()
	streamID := "her:" + myID
	sealed, err := identity.SealDelta(n.currentKey(), ownerID, myID, streamID, seq, payload, targets)
	if err != nil {
		return err
	}
	c, err := n.control(conn)
	if err != nil {
		return err
	}
	if _, err := c.AppendDelta(ctx, &relayv1.AppendDeltaRequest{
		StreamId: streamID,
		Envelope: sealed,
	}); err != nil {
		if connect.CodeOf(err) == connect.CodeUnimplemented {
			// Older relay without delta mailbox: drop pending quietly.
			n.deltas.mu.Lock()
			if bytes.Equal(n.deltas.pendingSnap, snap) {
				n.deltas.pendingBot, n.deltas.pendingSnap = nil, nil
			}
			n.deltas.mu.Unlock()
			return nil
		}
		return err
	}
	n.deltas.mu.Lock()
	if bytes.Equal(n.deltas.pendingSnap, snap) {
		n.deltas.pendingBot, n.deltas.pendingSnap = nil, nil
	}
	n.deltas.mu.Unlock()
	return nil
}

// compactHerOpenForDelta strips internal thinking/monologue/tool calls from a
// bots/open result so only what the Her entry shows (user messages,
// send_to_user/manage_routine calls + their tool results, task_end,
// card_resolved) is ever encrypted and uploaded.
func compactHerOpenForDelta(raw []byte) []byte {
	var open struct {
		SessionID string `json:"session_id"`
		Snapshot  struct {
			Session    json.RawMessage `json:"session"`
			LastSeq    int64           `json:"last_seq"`
			Transcript struct {
				Items []json.RawMessage `json:"items"`
			} `json:"transcript"`
			Usage json.RawMessage `json:"usage"`
		} `json:"snapshot"`
	}
	var snap struct {
		Session    json.RawMessage `json:"session"`
		LastSeq    int64           `json:"last_seq"`
		Transcript struct {
			Items []json.RawMessage `json:"items"`
		} `json:"transcript"`
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(raw, &open) == nil && open.SessionID != "" && len(open.Snapshot.Session) > 0 {
		snap = open.Snapshot
	} else if json.Unmarshal(raw, &snap) != nil || len(snap.Session) == 0 {
		return nil
	}

	sends := map[string]bool{"send_to_user": true, "manage_routine": true}
	keptCalls := map[string]bool{}
	items := make([]json.RawMessage, 0, len(snap.Transcript.Items))
	for _, it := range snap.Transcript.Items {
		var hdr struct {
			Type    string `json:"type"`
			CallID  string `json:"call_id"`
			Message *struct {
				Role    string            `json:"role"`
				Content []json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(it, &hdr) != nil {
			continue
		}
		switch hdr.Type {
		case "message":
			if hdr.Message == nil {
				continue
			}
			if hdr.Message.Role != "assistant" {
				items = append(items, it)
				continue
			}
			var shown []json.RawMessage
			for _, b := range hdr.Message.Content {
				var blk struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				}
				if json.Unmarshal(b, &blk) == nil && blk.Type == "tool_call" && sends[blk.Name] {
					shown = append(shown, b)
					if blk.ID != "" {
						keptCalls[blk.ID] = true
					}
				}
			}
			if len(shown) == 0 {
				continue
			}
			var m map[string]json.RawMessage
			var msg map[string]json.RawMessage
			if json.Unmarshal(it, &m) == nil && json.Unmarshal(m["message"], &msg) == nil {
				msg["content"], _ = json.Marshal(shown)
				m["message"], _ = json.Marshal(msg)
				if b, err := json.Marshal(m); err == nil {
					items = append(items, b)
				}
			}
		case "tool_result":
			if keptCalls[hdr.CallID] {
				items = append(items, it)
			}
		case "task_end", "card_resolved":
			items = append(items, it)
		}
	}
	if len(items) > 100 {
		items = items[len(items)-100:]
	}
	snap.Transcript.Items = items
	out, err := json.Marshal(snap)
	if err != nil {
		return nil
	}
	return out
}
