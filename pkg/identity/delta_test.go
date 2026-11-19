package identity

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
)

func TestDeltaSealAndOpenMultiDeviceAndTamperResistance(t *testing.T) {
	o := newTestOwner(t)
	trust := NewTrust(o.ownerID)
	if _, err := trust.AcceptRootChain(o.chain); err != nil {
		t.Fatal(err)
	}

	machKey, _ := GenerateKey()
	machSPKI := MarshalPublicKey(machKey.Public())
	machID := MachineID(machSPKI)
	machCert, err := Sign(&identityv1.MachineCert{
		OwnerId:          o.ownerID,
		MachineId:        machID,
		MachinePublicKey: machSPKI,
		IssuedAt:         timestamppb.Now(),
	}, o.first)
	if err != nil {
		t.Fatal(err)
	}

	dev1Key, _ := GenerateKey()
	dev2Key, _ := GenerateKey()
	dev3Key, _ := GenerateKey()
	sync1Priv, sync1Pub, _ := NewSyncKey()
	sync2Priv, sync2Pub, _ := NewSyncKey()
	sync3Priv, _, _ := NewSyncKey()

	payload := []byte(`{"session_id":"s1","last_seq":42,"text":"hello from Her while Mac sleeps"}`)
	sealed, err := SealDelta(machKey, o.ownerID, machID, "her:"+machID, 7, payload, []SyncRecipient{
		{DeviceKeyID: KeyID(dev1Key.Public()), PublicKey: sync1Pub},
		{DeviceKeyID: KeyID(dev2Key.Public()), PublicKey: sync2Pub},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Both authorized devices decrypt independently.
	for i, tc := range []struct {
		keyID string
		priv  []byte
	}{
		{KeyID(dev1Key.Public()), sync1Priv},
		{KeyID(dev2Key.Public()), sync2Priv},
	} {
		env, got, err := OpenDelta(trust, tc.keyID, tc.priv, sealed, machCert)
		if err != nil {
			t.Fatalf("device %d open: %v", i+1, err)
		}
		if env.MachineSeq != 7 || env.StreamId != "her:"+machID || !bytes.Equal(got, payload) {
			t.Fatalf("device %d got env=%+v payload=%q", i+1, env, got)
		}
	}

	// Device 3 (not in recipients) cannot decrypt.
	if _, _, err := OpenDelta(trust, KeyID(dev3Key.Public()), sync3Priv, sealed, machCert); err == nil {
		t.Fatal("device 3 unexpectedly decrypted delta")
	}

	// Tampering with signed payload fails signature check.
	tampered := &identityv1.Signed{
		Type:        sealed.Type,
		Body:        append([]byte(nil), sealed.Body...),
		SignerKeyId: sealed.SignerKeyId,
		Alg:         sealed.Alg,
		Signature:   append([]byte(nil), sealed.Signature...),
	}
	tampered.Body[len(tampered.Body)-1] ^= 0xff
	if _, _, err := OpenDelta(trust, KeyID(dev1Key.Public()), sync1Priv, tampered, machCert); err == nil {
		t.Fatal("tampered envelope unexpectedly verified")
	}

	// Forged by another key fails verification against machineCert.
	attacker, _ := GenerateKey()
	forged, err := SealDelta(attacker, o.ownerID, machID, "her:"+machID, 7, payload, []SyncRecipient{
		{DeviceKeyID: KeyID(dev1Key.Public()), PublicKey: sync1Pub},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenDelta(trust, KeyID(dev1Key.Public()), sync1Priv, forged, machCert); err == nil {
		t.Fatal("forged envelope unexpectedly verified")
	}
}
