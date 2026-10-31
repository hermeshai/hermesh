package preview

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
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

// The preview vectors (docs/desktop-app.md §5.6): signing requests, and
// what every signer must make of them, whatever its words: whether it
// refuses the batch, each card's verification code, and the values from
// the signed bytes each card shows. This Go signer and HermeshKit's
// SigningPreview (the Mac's and the iPhone's) both pass them
// (PreviewVectorTests.swift). `go test ./internal/preview -run
// TestVectors -update` writes them again, with new keys.
var update = flag.Bool("update", false, "write testdata/vectors.json again")

const vectorsFile = "testdata/vectors.json"

type vector struct {
	Name     string          `json:"name"`
	Requests []vectorRequest `json:"requests"`
	Hints    vectorHints     `json:"hints"`
	Refused  bool            `json:"refused,omitempty"`
	Cards    []vectorCard    `json:"cards,omitempty"`
}

type vectorRequest struct {
	Type string `json:"type"`
	Body []byte `json:"body"`
	// Message, when it isn't the body's signing input: what would be signed.
	Message   []byte `json:"message,omitempty"`
	Confirmed bool   `json:"confirmed,omitempty"`
}

type vectorHints struct {
	MachineNames map[string]string `json:"machineNames,omitempty"`
	// Devices: key id → name, before the request; absent when not listed.
	Devices map[string]string `json:"devices,omitempty"`
}

type vectorCard struct {
	Code  string   `json:"code,omitempty"`
	Shows []string `json:"shows"`
}

func TestVectors(t *testing.T) {
	if *update {
		b, _ := json.MarshalIndent(makeVectors(t), "", "  ")
		if err := os.WriteFile(vectorsFile, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(vectorsFile)
	if err != nil {
		t.Fatal(err)
	}
	var vs []vector
	if err := json.Unmarshal(b, &vs); err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		t.Run(v.Name, func(t *testing.T) {
			var reqs []*localv1.SigningRequest
			for _, r := range v.Requests {
				msg := r.Message
				if msg == nil {
					msg = identity.SigningInput(r.Type, r.Body)
				}
				reqs = append(reqs, &localv1.SigningRequest{Type: r.Type, Body: r.Body, Message: msg, Confirmed: r.Confirmed})
			}
			cs, err := Cards(reqs, Hints{MachineNames: v.Hints.MachineNames, Devices: v.Hints.Devices}, "en")
			var refused *Refused
			if v.Refused {
				if !errors.As(err, &refused) {
					t.Fatalf("not refused: %+v %v", cs, err)
				}
				return
			}
			if err != nil || len(cs) != len(v.Cards) {
				t.Fatalf("%d cards, want %d: %v", len(cs), len(v.Cards), err)
			}
			for i, want := range v.Cards {
				c := cs[i]
				if c.Code != want.Code {
					t.Errorf("card %d: code %q, want %q", i, c.Code, want.Code)
				}
				text := strings.Join(append([]string{c.Title, c.Why, c.Warning}, c.Lines...), "\n")
				for _, s := range want.Shows {
					if !strings.Contains(text, s) {
						t.Errorf("card %d doesn't show %q:\n%s", i, s, text)
					}
				}
			}
		})
	}
}

// makeVectors builds the vectors, with new keys.
func makeVectors(t *testing.T) []vector {
	t.Helper()
	req := func(m proto.Message) vectorRequest {
		body, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return vectorRequest{Type: identity.MessageType(m), Body: body}
	}
	confirmed := func(r vectorRequest) vectorRequest { r.Confirmed = true; return r }
	key := func() []byte { k, _ := identity.GenerateKey(); return identity.MarshalPublicKey(k.Public()) }
	code := identity.VerificationCode
	in30 := timestamppb.New(time.Date(2027, 1, 30, 12, 0, 0, 0, time.UTC))
	machines := map[string]string{"m_1": "vps-1"}

	client := key()
	grant := &identityv1.Grant{OwnerId: "o_x", ClientLabel: "bot", ClientPublicKey: client, ProjectIds: []string{"app"}, MachineIds: []string{"m_1"},
		Agents: []string{"claude"}, ExpiresAt: in30, Version: 1}

	phone, added := key(), key()
	devices := map[string]string{identity.Fingerprint(phone): "phone"}
	addDevice := &identityv1.RootCert{OwnerId: "o_x", Version: 2, RootKeys: []*identityv1.RootKey{{PublicKey: phone, Label: "phone"}, {PublicKey: added, Label: "vps-2"}}}
	removeDevice := &identityv1.RootCert{OwnerId: "o_x", Version: 3, RootKeys: []*identityv1.RootKey{{PublicKey: added, Label: "vps-2"}}}
	both := map[string]string{identity.Fingerprint(phone): "phone", identity.Fingerprint(added): "vps-2"}

	joining := key()
	join := &identityv1.PairingRequest{Kind: identityv1.PairingKind_PAIRING_KIND_DEVICE, Label: "pc", OwnerId: "o_x", SubjectPublicKey: joining}
	own := &identityv1.MachineCert{OwnerId: "o_x", MachineId: "m_2", MachinePublicKey: key(), DeviceKeyId: "k_1"}
	machineKey := key()
	newMachine := &identityv1.MachineCert{OwnerId: "o_x", MachineId: "m_3", MachinePublicKey: machineKey}

	admin := func(c *identityv1.AdminCommand) *identityv1.AdminCommand { c.TargetMachineId = "m_1"; return c }

	unknownField := req(grant)
	unknownField.Body = protowire.AppendVarint(protowire.AppendTag(unknownField.Body, 999, protowire.VarintType), 1)
	mismatch := req(grant)
	mismatch.Message = identity.SigningInput(mismatch.Type, []byte("other"))

	return []vector{
		{Name: "a personal agent asks to connect", Requests: []vectorRequest{req(grant)}, Hints: vectorHints{MachineNames: machines},
			Cards: []vectorCard{{Code: code(identityv1.PairingKind_PAIRING_KIND_CLIENT, client, "o_x"), Shows: []string{"bot", "vps-1", "app", "Claude Code"}}}},
		{Name: "a personal agent the owner confirmed by its code", Requests: []vectorRequest{confirmed(req(grant))}, Hints: vectorHints{MachineNames: machines},
			Cards: []vectorCard{{Shows: []string{"bot", "vps-1"}}}},
		{Name: "a new machine", Requests: []vectorRequest{req(newMachine)},
			Cards: []vectorCard{{Code: code(identityv1.PairingKind_PAIRING_KIND_MACHINE, machineKey, "o_x"), Shows: []string{"m_3"}}}},
		{Name: "another trusted device joins", Requests: []vectorRequest{req(addDevice)}, Hints: vectorHints{Devices: devices},
			Cards: []vectorCard{{Code: code(identityv1.PairingKind_PAIRING_KIND_DEVICE, added, "o_x"), Shows: []string{"vps-2"}}}},
		{Name: "a trusted device found by its pairing code", Requests: []vectorRequest{confirmed(req(addDevice))}, Hints: vectorHints{Devices: devices},
			Cards: []vectorCard{{Shows: []string{"vps-2"}}}},
		{Name: "a trusted device removed, what it signed re-issued", Requests: []vectorRequest{req(removeDevice), req(&identityv1.MachineCert{OwnerId: "o_x", MachineId: "m_1", MachinePublicKey: key()})},
			Hints: vectorHints{MachineNames: machines, Devices: both},
			Cards: []vectorCard{{Shows: []string{"phone"}}, {Shows: []string{"vps-1"}}}},
		{Name: "this computer joins, with its own machine", Requests: []vectorRequest{req(join), req(own)},
			Cards: []vectorCard{{Code: code(identityv1.PairingKind_PAIRING_KIND_DEVICE, joining, "o_x"), Shows: []string{"pc"}}, {Shows: []string{"m_2"}}}},
		{Name: "a project added", Requests: []vectorRequest{req(admin(&identityv1.AdminCommand{Command: &identityv1.AdminCommand_AddProject{AddProject: &identityv1.AddProject{ProjectId: "app", Path: "~/code/app"}}}))},
			Hints: vectorHints{MachineNames: machines}, Cards: []vectorCard{{Shows: []string{"app", "vps-1", "~/code/app"}}}},
		{Name: "a project removed", Requests: []vectorRequest{req(admin(&identityv1.AdminCommand{Command: &identityv1.AdminCommand_RemoveProject{RemoveProject: &identityv1.RemoveProject{ProjectId: "app"}}}))},
			Hints: vectorHints{MachineNames: machines}, Cards: []vectorCard{{Shows: []string{"app", "vps-1"}}}},
		{Name: "a machine renamed", Requests: []vectorRequest{req(admin(&identityv1.AdminCommand{Command: &identityv1.AdminCommand_SetName{SetName: &identityv1.SetName{Name: "build-box"}}}))},
			Hints: vectorHints{MachineNames: machines}, Cards: []vectorCard{{Shows: []string{"vps-1", "build-box"}}}},
		{Name: "a coding agent's model set", Requests: []vectorRequest{req(admin(&identityv1.AdminCommand{Command: &identityv1.AdminCommand_SetAgentDefaults{SetAgentDefaults: &identityv1.AgentDefaults{Agent: "codex", Model: "gpt-5-codex", Effort: "high"}}}))},
			Hints: vectorHints{MachineNames: machines}, Cards: []vectorCard{{Shows: []string{"Codex", "vps-1", "gpt-5-codex", "high"}}}},
		{Name: "everything erased", Requests: []vectorRequest{req(&identityv1.EraseOrder{OwnerId: "o_x", EraseId: "e1"})},
			Cards: []vectorCard{{Shows: []string{"e1"}}}},
		{Name: "refused: what would be signed isn't what is shown", Requests: []vectorRequest{mismatch}, Refused: true},
		{Name: "refused: a field this version doesn't know", Requests: []vectorRequest{unknownField}, Refused: true},
		{Name: "refused: a kind of request it doesn't know", Requests: []vectorRequest{req(&identityv1.RevocationList{OwnerId: "o_x"})}, Refused: true},
		{Name: "refused: a batch with one it can't show", Requests: []vectorRequest{req(grant), req(&identityv1.RevocationList{OwnerId: "o_x"})}, Refused: true},
	}
}
