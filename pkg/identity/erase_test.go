package identity

import (
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
)

// An erase order counts only signed by one of the owner's root keys, for
// that owner; an ack only for the subject its own key names.
func TestEraseOrderAndAck(t *testing.T) {
	o := newTestOwner(t)
	second, _ := GenerateKey()
	o.extend(t, o.first, o.first, second)
	trust := NewTrust(o.ownerID)
	if _, err := trust.AcceptRootChain(o.chain); err != nil {
		t.Fatal(err)
	}
	order := func(ownerID, eraseID string, s Signer) *identityv1.Signed {
		signed, err := Sign(&identityv1.EraseOrder{OwnerId: ownerID, EraseId: eraseID, IssuedAt: timestamppb.Now()}, s)
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	if got, err := trust.VerifyEraseOrder(order(o.ownerID, "e1", second)); err != nil || got.EraseId != "e1" {
		t.Fatalf("an order from a trusted device: %v %v", got, err)
	}
	stranger, _ := GenerateKey()
	other := newTestOwner(t)
	for name, s := range map[string]*identityv1.Signed{
		"signed by a stranger":   order(o.ownerID, "e1", stranger),
		"for another owner":      order(other.ownerID, "e1", o.first),
		"without an erase id":    order(o.ownerID, "", o.first),
		"of another owner's key": order(o.ownerID, "e1", other.first),
	} {
		if _, err := trust.VerifyEraseOrder(s); err == nil {
			t.Errorf("an order %s verified", name)
		}
	}

	spki := MarshalPublicKey(stranger.Public())
	for _, subject := range []string{MachineID(spki), ClientID(spki)} {
		ack, _ := SignEraseAck("e1", subject, nil, stranger)
		if a, err := VerifyEraseAck(ack, nil); err != nil || a.EraseId != "e1" || a.Subject != subject {
			t.Errorf("ack as %s: %v %v", subject, a, err)
		}
	}
	for name, subject := range map[string]string{"another key's machine": MachineID(MarshalPublicKey(second.Public())), "a root key": KeyID(stranger.Public())} {
		ack, _ := SignEraseAck("e1", subject, nil, stranger)
		if _, err := VerifyEraseAck(ack, trust); err == nil {
			t.Errorf("an ack for %s verified", name)
		}
	}

	// A trusted device acks with its device key, which its device cert,
	// signed by its root key, names.
	device, _ := GenerateKey()
	cert, _ := Sign(&identityv1.OwnerDeviceCert{OwnerId: o.ownerID, DevicePublicKey: MarshalPublicKey(device.Public()), Label: "mac2"}, second)
	ack, _ := SignEraseAck("e1", KeyID(second.Public()), cert, device)
	if a, err := VerifyEraseAck(ack, trust); err != nil || a.Subject != KeyID(second.Public()) {
		t.Fatalf("a device's ack: %v %v", a, err)
	}
	for name, ack := range map[string]func() *identityv1.Signed{
		"for another root key": func() *identityv1.Signed { a, _ := SignEraseAck("e1", KeyID(o.first.Public()), cert, device); return a },
		"by another key": func() *identityv1.Signed {
			a, _ := SignEraseAck("e1", KeyID(second.Public()), cert, stranger)
			return a
		},
		"with a stranger's cert": func() *identityv1.Signed {
			c, _ := Sign(&identityv1.OwnerDeviceCert{OwnerId: o.ownerID, DevicePublicKey: MarshalPublicKey(device.Public())}, stranger)
			a, _ := SignEraseAck("e1", KeyID(stranger.Public()), c, device)
			return a
		},
	} {
		if _, err := VerifyEraseAck(ack(), trust); err == nil {
			t.Errorf("a device's ack %s verified", name)
		}
	}
	if _, err := VerifyEraseAck(ack, nil); err == nil {
		t.Error("a device's ack verified without its owner")
	}
}
