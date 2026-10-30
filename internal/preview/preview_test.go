package preview

import (
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

func request(t *testing.T, m proto.Message) *localv1.SigningRequest {
	t.Helper()
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	typ := identity.MessageType(m)
	return &localv1.SigningRequest{Type: typ, Body: body, Message: identity.SigningInput(typ, body)}
}

func spki(t *testing.T) []byte {
	k, _ := identity.GenerateKey()
	return identity.MarshalPublicKey(k.Public())
}

func TestRefusals(t *testing.T) {
	g := request(t, &identityv1.Grant{OwnerId: "o_x", ClientLabel: "bot", ClientPublicKey: spki(t), ExpiresAt: timestamppb.Now(), Version: 1})
	var refused *Refused

	// What is signed must be what is shown.
	bad := proto.Clone(g).(*localv1.SigningRequest)
	bad.Message = identity.SigningInput(g.Type, []byte("other"))
	if _, err := Cards([]*localv1.SigningRequest{bad}, Hints{}); !errors.As(err, &refused) {
		t.Fatalf("a message that isn't the body: %v", err)
	}
	// A field this version doesn't know.
	unknown := proto.Clone(g).(*localv1.SigningRequest)
	unknown.Body = protowire.AppendVarint(protowire.AppendTag(unknown.Body, 999, protowire.VarintType), 1)
	unknown.Message = identity.SigningInput(g.Type, unknown.Body)
	if _, err := Cards([]*localv1.SigningRequest{unknown}, Hints{}); !errors.As(err, &refused) {
		t.Fatalf("an unknown field: %v", err)
	}
	// A type it doesn't know.
	other := request(t, &identityv1.RevocationList{OwnerId: "o_x"})
	if _, err := Cards([]*localv1.SigningRequest{other}, Hints{}); !errors.As(err, &refused) {
		t.Fatalf("an unknown type: %v", err)
	}
	// One refusal refuses the batch.
	if _, err := Cards([]*localv1.SigningRequest{g, other}, Hints{}); err == nil {
		t.Fatal("a batch with one it can't show")
	}
}

// An erase order says what it does and that it can't be undone.
func TestEraseCard(t *testing.T) {
	cs, err := Cards([]*localv1.SigningRequest{request(t, &identityv1.EraseOrder{OwnerId: "o_x", EraseId: "e1"})}, Hints{})
	if err != nil || cs[0].Title != "Erase everything in your Hermesh" || !strings.Contains(cs[0].Why, "can't be undone") || cs[0].Warning == "" ||
		!strings.Contains(strings.Join(cs[0].Lines, "\n"), "Erase ID: e1") {
		t.Fatalf("erase: %+v %v", cs, err)
	}
}

func TestCards(t *testing.T) {
	client := spki(t)
	g := &identityv1.Grant{OwnerId: "o_x", ClientLabel: "bot", ClientPublicKey: client, ProjectIds: []string{"*"}, MachineIds: []string{"m_1"},
		Agents: []string{"claude"}, ExpiresAt: timestamppb.New(time.Now().Add(30 * 24 * time.Hour)), Version: 1}
	cs, err := Cards([]*localv1.SigningRequest{request(t, g)}, Hints{MachineNames: map[string]string{"m_1": "vps-1"}})
	if err != nil {
		t.Fatal(err)
	}
	c := cs[0]
	if c.Title != "Approve “bot” to connect to your computers" || c.Code != identity.VerificationCode(identityv1.PairingKind_PAIRING_KIND_CLIENT, client, "o_x") ||
		!strings.Contains(strings.Join(c.Lines, "\n"), "Machines: vps-1") || !strings.Contains(strings.Join(c.Lines, "\n"), "Projects: all") {
		t.Fatalf("grant: %+v", c)
	}
	// One the person confirmed has nothing to compare.
	r := request(t, g)
	r.Confirmed = true
	if cs, _ := Cards([]*localv1.SigningRequest{r}, Hints{}); cs[0].Code != "" {
		t.Fatalf("a confirmed grant shows a code: %+v", cs[0])
	}

	// A new trusted device: by the devices before it.
	old, added := spki(t), spki(t)
	root := &identityv1.RootCert{OwnerId: "o_x", Version: 2, RootKeys: []*identityv1.RootKey{{PublicKey: old, Label: "phone"}, {PublicKey: added, Label: "vps-2"}}}
	h := Hints{Devices: map[string]string{identity.Fingerprint(old): "phone"}}
	cs, err = Cards([]*localv1.SigningRequest{request(t, root)}, h)
	if err != nil || cs[0].Title != "Add “vps-2” as a trusted device" || cs[0].Code == "" {
		t.Fatalf("adding a device: %+v %v", cs, err)
	}
	r = request(t, root)
	r.Confirmed = true // looked up by its code
	if cs, _ := Cards([]*localv1.SigningRequest{r}, h); cs[0].Code != "" {
		t.Fatalf("a device found by its code shows a code: %+v", cs[0])
	}
	// Removing one, with what it signed re-issued.
	gone := &identityv1.RootCert{OwnerId: "o_x", Version: 3, RootKeys: []*identityv1.RootKey{{PublicKey: added, Label: "vps-2"}}}
	h = Hints{Devices: map[string]string{identity.Fingerprint(old): "phone", identity.Fingerprint(added): "vps-2"}, MachineNames: map[string]string{"m_1": "vps-1"}}
	cs, err = Cards([]*localv1.SigningRequest{request(t, gone), request(t, &identityv1.MachineCert{OwnerId: "o_x", MachineId: "m_1", MachinePublicKey: spki(t)})}, h)
	if err != nil || cs[0].Title != "Remove trusted device “phone”" || cs[1].Title != "Re-issue the certificate of machine “vps-1”" ||
		!strings.Contains(cs[1].Why, "now signed by this computer") {
		t.Fatalf("removing a device: %+v %v", cs, err)
	}

	// Joining: this computer's request, and its own machine, admitted by its key.
	join := &identityv1.PairingRequest{Kind: identityv1.PairingKind_PAIRING_KIND_DEVICE, Label: "pc", OwnerId: "o_x", SubjectPublicKey: spki(t)}
	own := &identityv1.MachineCert{OwnerId: "o_x", MachineId: "m_2", MachinePublicKey: spki(t), DeviceKeyId: "k_1"}
	cs, err = Cards([]*localv1.SigningRequest{request(t, join), request(t, own)}, Hints{})
	if err != nil || cs[0].Title != "Add this computer to your Hermesh" || cs[0].Code == "" ||
		cs[1].Title != "Share the agents on this computer" || cs[1].Code != "" || cs[1].Warning != "" {
		t.Fatalf("joining: %+v %v", cs, err)
	}

	// A project added from here.
	add := &identityv1.AdminCommand{TargetMachineId: "m_1", Command: &identityv1.AdminCommand_AddProject{AddProject: &identityv1.AddProject{ProjectId: "app", Path: "~/app"}}}
	if cs, err := Cards([]*localv1.SigningRequest{request(t, add)}, Hints{MachineNames: map[string]string{"m_1": "vps-1"}}); err != nil || cs[0].Title != "Add project “app” to machine vps-1" {
		t.Fatalf("add project: %+v %v", cs, err)
	}
}
