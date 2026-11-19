package owner

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/relay"
)

// Real-socket end-to-end test for Milestone 2 (Zero-Knowledge Encrypted Delta Mailbox):
//  1. Real Relay (SQLite-backed) + real Machine + two real Owner devices connect over TCP/WebSocket.
//  2. Both devices register X25519 sync keys automatically (no root-key / APNs prompt).
//  3. Machine publishes a Her conversation delta containing a reply + private thinking.
//  4. Relay's SQLite file contains ONLY ciphertext (never plaintext reply or private thinking).
//  5. Machine shuts down completely (offline/asleep).
//  6. Both Owner devices pull and decrypt the delta independently in 1 RTT while the Machine is dead,
//     and GetHerConversation serves the newest Her reply without internal thinking.
func TestDeltaMailboxOfflineHerSyncMultiDeviceE2E(t *testing.T) {
	dir := t.TempDir()
	relayDB := filepath.Join(dir, "relay.db")
	st, err := relay.OpenStore(relayDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	root, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	rootPub := identity.MarshalPublicKey(root.Public())
	ownerID := identity.OwnerID(rootPub)
	rc, err := identity.Sign(&identityv1.RootCert{
		OwnerId:  ownerID,
		Version:  1,
		IssuedAt: timestamppb.Now(),
		RootKeys: []*identityv1.RootKey{{PublicKey: rootPub, Label: "root"}},
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	chain := &identityv1.RootChain{Certs: []*identityv1.Signed{rc}}

	dev1, _ := identity.GenerateKey()
	dev1Cert, err := identity.Sign(&identityv1.OwnerDeviceCert{
		OwnerId:         ownerID,
		DevicePublicKey: identity.MarshalPublicKey(dev1.Public()),
		Label:           "iPhone-1",
		IssuedAt:        timestamppb.Now(),
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	dev2, _ := identity.GenerateKey()
	dev2Cert, err := identity.Sign(&identityv1.OwnerDeviceCert{
		OwnerId:         ownerID,
		DevicePublicKey: identity.MarshalPublicKey(dev2.Public()),
		Label:           "iPhone-2",
		IssuedAt:        timestamppb.Now(),
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveClaim(ownerID, chain, dev1Cert); err != nil {
		t.Fatal(err)
	}

	// Open real Machine and certify it under owner's root key.
	machDir := filepath.Join(dir, "machine")
	mach, err := machine.Open(machine.Config{DataDir: machDir, WorkspaceMode: "none"})
	if err != nil {
		t.Fatal(err)
	}
	machID := mach.MachineID()
	machCert, err := identity.Sign(&identityv1.MachineCert{
		OwnerId:          ownerID,
		MachineId:        machID,
		MachinePublicKey: mach.PublicKeySPKI(),
		IssuedAt:         timestamppb.Now(),
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutMachineCert(ownerID, machID, machCert); err != nil {
		t.Fatal(err)
	}

	rs, err := relay.New(relay.Config{Backend: st})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rs.Close() })
	srv := httptest.NewServer(rs.Handler())
	t.Cleanup(srv.Close)

	if err := mach.Install(ownerID, srv.URL, chain, machCert); err != nil {
		t.Fatal(err)
	}
	machCtx, machCancel := context.WithCancel(context.Background())
	machDone := make(chan struct{})
	go func() {
		defer close(machDone)
		_ = mach.Run(machCtx)
	}()
	machClosed := false
	stopMachine := func() {
		if machClosed {
			return
		}
		machClosed = true
		machCancel()
		<-machDone
		mach.Close()
	}
	t.Cleanup(stopMachine)

	// Open two real Owner devices.
	openDev := func(name string, devKey *identity.SoftwareKey, devCert *identityv1.Signed) (*Device, *Owner) {
		d, err := Open(Config{DataDir: filepath.Join(dir, name), NoLocalMachine: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		o := d.owner()
		if err := o.saveIdentity(ownerID, srv.URL, rootPub, chain, devKey, devCert, ""); err != nil {
			t.Fatal(err)
		}
		return d, o
	}
	d1, o1 := openDev("iphone1", dev1, dev1Cert)
	d2, o2 := openDev("iphone2", dev2, dev2Cert)

	// Both devices connect and register their X25519 sync keys.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, o := range []*Owner{o1, o2} {
		ctl, _, err := o.control(ctx)
		if err != nil {
			t.Fatalf("owner control connect: %v", err)
		}
		if err := o.registerSyncKey(ctx, ctl); err != nil {
			t.Fatalf("registerSyncKey: %v", err)
		}
	}

	// Wait until the Machine has received both SyncTargets over WatchUpdates and published the delta.
	const secretReply = "SECRET_OFFLINE_HER_REPLY_98765"
	const privateThinking = "PRIVATE_THINKING_DO_NOT_UPLOAD"
	botJSON := []byte(`{"id":"her","name":"Her","default":true}`)
	openResult := []byte(`{
		"session_id":"conv-offline-1",
		"snapshot":{
			"session":{"session_id":"conv-offline-1","title":"Her"},
			"last_seq":12,
			"transcript":{"items":[
				{"type":"message","message_id":"u1","message":{"role":"user","content":[{"type":"text","text":"hi her"}],"ts":1}},
				{"type":"message","message_id":"a1","message":{"role":"assistant","content":[
					{"type":"thinking","text":"` + privateThinking + `"},
					{"type":"text","text":"internal monologue"},
					{"type":"tool_call","id":"c1","name":"send_to_user","arguments":{"text":"` + secretReply + `"}}
				],"ts":2}},
				{"type":"tool_result","call_id":"c1","result":{"tool_name":"send_to_user","content":[],"is_error":false}},
				{"type":"task_end","task_id":"t1","outcome":"completed","ts":3}
			]},
			"usage":{"per_model":{}}
		}
	}`)

	deadline := time.Now().Add(8 * time.Second)
	for {
		if err := mach.PublishHerDelta(ctx, botJSON, openResult); err != nil {
			t.Fatalf("PublishHerDelta: %v", err)
		}
		entries, _, err := st.PullDeltas(ownerID, map[string]uint64{"her:" + machID: 0}, true, 10, time.Now())
		if err == nil && len(entries) > 0 {
			last := entries[len(entries)-1]
			var signed identityv1.Signed
			var env identityv1.DeltaEnvelope
			if proto.Unmarshal(last.Envelope, &signed) == nil && proto.Unmarshal(signed.Body, &env) == nil && len(env.Keys) >= 2 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("delta wrapped for both devices never reached relay mailbox (entries=%d)", len(entries))
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Zero-knowledge assertion: Relay's SQLite file on disk must NOT contain plaintext reply or thinking.
	dbBytes, err := os.ReadFile(relayDB)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(dbBytes, []byte(secretReply)) {
		t.Fatal("ZERO-KNOWLEDGE VIOLATION: plaintext Her reply found in relay SQLite file!")
	}
	if bytes.Contains(dbBytes, []byte(privateThinking)) {
		t.Fatal("PRIVACY VIOLATION: private thinking found in relay SQLite file!")
	}

	// Completely shut down the Machine (Mac is asleep / offline!).
	stopMachine()

	// Both Owner devices independently pull & decrypt the delta while Machine is dead!
	for i, tc := range []struct {
		d *Device
		o *Owner
	}{{d1, o1}, {d2, o2}} {
		n, err := tc.o.PullDeltas(ctx)
		if err != nil || n == 0 {
			t.Fatalf("device %d PullDeltas while machine offline: n=%d err=%v", i+1, n, err)
		}
		res, err := (&localService{d: tc.d}).GetHerConversation(ctx, &localv1.GetHerConversationRequest{MachineId: machID})
		if err != nil {
			t.Fatalf("device %d GetHerConversation: %v", i+1, err)
		}
		if !strings.Contains(string(res.Snapshot), secretReply) {
			t.Fatalf("device %d snapshot missing %q: %s", i+1, secretReply, string(res.Snapshot))
		}
		if strings.Contains(string(res.Snapshot), privateThinking) {
			t.Fatalf("device %d snapshot leaked private thinking: %s", i+1, string(res.Snapshot))
		}
		var b struct{ ID, Name string }
		if err := json.Unmarshal(res.Bot, &b); err != nil || b.ID != "her" {
			t.Fatalf("device %d bot=%s err=%v", i+1, string(res.Bot), err)
		}
		// Second pull sees 0 new deltas (cursor persisted).
		n2, err := tc.o.PullDeltas(ctx)
		if err != nil || n2 != 0 {
			t.Fatalf("device %d second PullDeltas expected 0, got n=%d err=%v", i+1, n2, err)
		}
	}
}
