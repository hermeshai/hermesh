package identity

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
)

// deviceCard signs an agent's card with a trusted device's device key, and
// returns it with that device's cert.
func (o *testOwner) deviceCard(t *testing.T, root, dev, client *SoftwareKey, name string) (card, deviceCert *identityv1.Signed) {
	t.Helper()
	dc, err := Sign(&identityv1.OwnerDeviceCert{OwnerId: o.ownerID, DevicePublicKey: MarshalPublicKey(dev.Public()), Label: "mac"}, root)
	if err != nil {
		t.Fatal(err)
	}
	spki := MarshalPublicKey(client.Public())
	card, err = Sign(&identityv1.ContactCard{OwnerId: o.ownerID, ClientId: ClientID(spki), ClientPublicKey: spki, Name: name, Kind: "cli", IssuedAt: timestamppb.Now()}, dev)
	if err != nil {
		t.Fatal(err)
	}
	return card, dc
}

func TestSealedMessages(t *testing.T) {
	priv, pub, err := NewInboxKey()
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := InboxPublicKey(priv); !proto.Equal(&identityv1.Signed{Body: p}, &identityv1.Signed{Body: pub}) {
		t.Fatal("the public half differs")
	}
	body := &identityv1.ContactCard{Name: "hello"}
	sealed, err := SealMessage(pub, "c_a", "msg_1", body)
	if err != nil {
		t.Fatal(err)
	}
	got := &identityv1.ContactCard{}
	if err := OpenMessage(priv, "c_a", "msg_1", sealed, got); err != nil || got.Name != "hello" {
		t.Fatalf("open: %v %v", got, err)
	}
	// Moved to another inbox or id, it doesn't open.
	if OpenMessage(priv, "c_b", "msg_1", sealed, got) == nil || OpenMessage(priv, "c_a", "msg_2", sealed, got) == nil {
		t.Fatal("a message opened for another inbox or id")
	}
	other, _, _ := NewInboxKey()
	if OpenMessage(other, "c_a", "msg_1", sealed, got) == nil {
		t.Fatal("a message opened with another key")
	}
}

func TestContacts(t *testing.T) {
	o := newTestOwner(t)
	trust := NewTrust(o.ownerID)
	if _, err := trust.AcceptRootChain(o.chain); err != nil {
		t.Fatal(err)
	}
	dev, _ := GenerateKey()
	client, _ := GenerateKey()
	card, dc := o.deviceCard(t, o.first, dev, client, "bot")
	_, inboxPub, _ := NewInboxKey()
	reg, err := SignRegistration(client, o.ownerID, ClientID(MarshalPublicKey(client.Public())), inboxPub, "poll")
	if err != nil {
		t.Fatal(err)
	}
	info, err := trust.VerifyContact(&identityv1.Contact{Card: card, DeviceCert: dc, Registration: reg})
	if err != nil || info.Name != "bot" || info.Kind != "cli" || info.TakesMessages != "poll" || len(info.InboxKey) != 32 {
		t.Fatalf("contact: %+v %v", info, err)
	}

	// A card signed by a key no trusted device holds (the relay's own).
	stranger, _ := GenerateKey()
	forged, _ := Sign(&identityv1.ContactCard{OwnerId: o.ownerID, ClientId: ClientID(MarshalPublicKey(stranger.Public())), ClientPublicKey: MarshalPublicKey(stranger.Public()), Name: "bot", Kind: "cli"}, stranger)
	if _, err := trust.VerifyContact(&identityv1.Contact{Card: forged, DeviceCert: dc}); err == nil {
		t.Fatal("a card no trusted device signed verified")
	}
	// A device cert no root key signed.
	selfCert, _ := Sign(&identityv1.OwnerDeviceCert{OwnerId: o.ownerID, DevicePublicKey: MarshalPublicKey(stranger.Public())}, stranger)
	if _, err := trust.VerifyContact(&identityv1.Contact{Card: forged, DeviceCert: selfCert}); err == nil {
		t.Fatal("a card from a device the owner never certified verified")
	}
	// The relay swapping in an inbox key of its own: the registration isn't
	// signed by the agent's key.
	_, relayPub, _ := NewInboxKey()
	swapped, _ := SignRegistration(stranger, o.ownerID, ClientID(MarshalPublicKey(client.Public())), relayPub, "poll")
	if _, err := trust.VerifyContact(&identityv1.Contact{Card: card, DeviceCert: dc, Registration: swapped}); err == nil {
		t.Fatal("a registration another key signed verified")
	}
	// A registration for another inbox.
	elsewhere, _ := SignRegistration(client, o.ownerID, "c_other", inboxPub, "poll")
	if _, err := trust.VerifyContact(&identityv1.Contact{Card: card, DeviceCert: dc, Registration: elsewhere}); err == nil {
		t.Fatal("a registration for another inbox verified")
	}
}
