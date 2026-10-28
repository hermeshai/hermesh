// Package preview tells what a signing request signs, from the exact bytes
// that will be signed (never from text hermeshd supplies), for a terminal
// signer: the Go port of the essentials of HermeshKit's SigningPreview
// (hermesh docs/onboarding.md §9.3). A request it cannot show in full is
// refused.
package preview

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// Card is one request as the terminal shows it.
type Card struct {
	Title string
	// A verification code to compare with what the other side shows.
	Code    string
	Lines   []string
	Why     string
	Warning string
}

// Hints are names from hermeshd's lists; only ids come from the signed bytes.
type Hints struct {
	MachineNames map[string]string // machine id → name
	// Devices are the trusted devices (key id → name) before this request;
	// nil when they could not be listed.
	Devices map[string]string
}

// Refused: the request cannot be shown in full, so it is not signed.
type Refused struct{ Type, Why string }

func (r *Refused) Error() string { return fmt.Sprintf("can't show %s in full: %s", r.Type, r.Why) }

// Cards tells a batch, in order; any request it can't show refuses all.
func Cards(reqs []*localv1.SigningRequest, h Hints) ([]Card, error) {
	reissue := isReissue(reqs, h)
	var out []Card
	for _, r := range reqs {
		c, err := card(r, h, reissue)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// strict parses the body as m and refuses one that does not re-serialize to
// exactly those bytes (an unknown field, or an encoding hermeshd would not
// produce): what is shown is then all that is signed.
func strict(r *localv1.SigningRequest, m proto.Message) error {
	if err := (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(r.Body, m); err != nil {
		return &Refused{r.Type, "it can't be parsed"}
	}
	again, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil || !bytes.Equal(again, r.Body) {
		return &Refused{r.Type, "it holds something this version doesn't know; update Hermesh"}
	}
	return nil
}

func card(r *localv1.SigningRequest, h Hints, reissue bool) (Card, error) {
	if !bytes.Equal(r.Message, identity.SigningInput(r.Type, r.Body)) {
		return Card{}, &Refused{r.Type, "what would be signed isn't what would be shown"}
	}
	switch r.Type {
	case identity.MessageType(&identityv1.OwnerDeviceCert{}):
		m := &identityv1.OwnerDeviceCert{}
		if err := strict(r, m); err != nil {
			return Card{}, err
		}
		return Card{Title: "Issue this computer a certificate for end-to-end connections",
			Lines: []string{"Device name: " + m.Label, "Fingerprint: " + identity.ShortFingerprint(m.DevicePublicKey)},
			Why:   "Hermesh here uses it for end-to-end encrypted connections to your machines."}, nil
	case identity.MessageType(&identityv1.RootCert{}):
		m := &identityv1.RootCert{}
		if err := strict(r, m); err != nil {
			return Card{}, err
		}
		return root(m, h, r.Confirmed), nil
	case identity.MessageType(&identityv1.PairingRequest{}):
		m := &identityv1.PairingRequest{}
		if err := strict(r, m); err != nil {
			return Card{}, err
		}
		if m.Kind != identityv1.PairingKind_PAIRING_KIND_DEVICE {
			return Card{}, &Refused{r.Type, "only a device's own request is signed here"}
		}
		c := Card{Title: "Ask to join your Hermesh as a trusted device", Lines: []string{"Device name: " + m.Label},
			Why: "Your iPhone or Mac approves it, and shows two digits to type here."}
		if m.OwnerId != "" {
			c.Code = identity.VerificationCode(m.Kind, m.SubjectPublicKey, m.OwnerId)
			c.Lines = append(c.Lines, "Owner: "+m.OwnerId)
		}
		return c, nil
	case identity.MessageType(&identityv1.MachineCert{}):
		m := &identityv1.MachineCert{}
		if err := strict(r, m); err != nil {
			return Card{}, err
		}
		return machineCard(m, h, reissue, r.Confirmed), nil
	case identity.MessageType(&identityv1.Grant{}):
		m := &identityv1.Grant{}
		if err := strict(r, m); err != nil {
			return Card{}, err
		}
		if _, ok := identityv1.TaskVisibility_name[int32(m.TaskVisibility)]; !ok {
			return Card{}, &Refused{r.Type, "an unknown visibility"}
		}
		return grantCard(m, h, reissue, r.Confirmed), nil
	case identity.MessageType(&identityv1.AdminCommand{}):
		m := &identityv1.AdminCommand{}
		if err := strict(r, m); err != nil {
			return Card{}, err
		}
		return admin(m, h, r.Type)
	}
	return Card{}, &Refused{r.Type, "Hermesh doesn't know this kind of request"}
}

// isReissue: a batch that removes a trusted device re-issues, unchanged,
// what that device signed. Told from the signed root cert.
func isReissue(reqs []*localv1.SigningRequest, h Hints) bool {
	if len(reqs) < 2 || h.Devices == nil {
		return false
	}
	for _, r := range reqs {
		c := &identityv1.RootCert{}
		if r.Type != identity.MessageType(c) || strict(r, c) != nil || c.Version < 2 {
			continue
		}
		keys := keyIDs(c)
		for id := range h.Devices {
			if !keys[id] {
				return true
			}
		}
	}
	return false
}

func keyIDs(c *identityv1.RootCert) map[string]bool {
	out := map[string]bool{}
	for _, k := range c.RootKeys {
		out[identity.Fingerprint(k.PublicKey)] = true
	}
	return out
}

func root(c *identityv1.RootCert, h Hints, confirmed bool) Card {
	var names []string
	for _, k := range c.RootKeys {
		names = append(names, cmpOr(k.Label, "unnamed device"))
	}
	if c.Version == 1 {
		return Card{Title: "Create your owner identity", Lines: []string{"Owner: " + c.OwnerId, "Device name: " + strings.Join(names, ", ")},
			Why: "This computer becomes your first trusted device; any device that joins later must be approved by a trusted device."}
	}
	lines := []string{"Your trusted devices after this: " + strings.Join(names, ", "), fmt.Sprintf("Version %d", c.Version)}
	if h.Devices == nil {
		return Card{Title: "Update your list of trusted devices", Lines: lines, Why: "The list above becomes your trusted devices."}
	}
	for _, k := range c.RootKeys {
		if _, ok := h.Devices[identity.Fingerprint(k.PublicKey)]; ok {
			continue
		}
		cd := Card{Title: fmt.Sprintf("Add “%s” as a trusted device", k.Label), Lines: lines,
			Why:     "Uses your owner key to make it a trusted device too, able to approve agents, machines and new devices like this one.",
			Warning: "Approve only if that device is in your hands."}
		if !confirmed { // found by its code here: the code binds its key
			cd.Code = identity.VerificationCode(identityv1.PairingKind_PAIRING_KIND_DEVICE, k.PublicKey, c.OwnerId)
		}
		return cd
	}
	keys := keyIDs(c)
	var gone []string
	for id, name := range h.Devices {
		if !keys[id] {
			gone = append(gone, name)
		}
	}
	if len(gone) > 0 {
		slices.Sort(gone)
		return Card{Title: fmt.Sprintf("Remove trusted device “%s”", strings.Join(gone, "”, “")), Lines: lines,
			Why: "It can no longer approve anything, and its own machine goes with it; this computer re-issues the machine and agent credentials it signed."}
	}
	return Card{Title: "Update your list of trusted devices", Lines: lines, Why: "The list above becomes your trusted devices."}
}

func machineCard(m *identityv1.MachineCert, h Hints, reissue, confirmed bool) Card {
	id := "Machine ID: " + m.MachineId
	if name, ok := h.MachineNames[m.MachineId]; ok {
		why := "This machine stays yours."
		if reissue {
			why = "This machine stays yours, unchanged, now signed by this computer."
		}
		return Card{Title: fmt.Sprintf("Re-issue the certificate of machine “%s”", name), Lines: []string{id}, Why: why}
	}
	c := Card{Title: "Approve a new machine", Lines: []string{id, "Fingerprint: " + identity.ShortFingerprint(m.MachinePublicKey)},
		Why:     "This machine becomes yours, and agents you grant access can start sessions and run commands on it.",
		Warning: "Approve only if you know this machine."}
	if !confirmed {
		c.Code = identity.VerificationCode(identityv1.PairingKind_PAIRING_KIND_MACHINE, m.MachinePublicKey, m.OwnerId)
	}
	return c
}

func grantCard(g *identityv1.Grant, h Hints, reissue, confirmed bool) Card {
	var machines []string
	for _, id := range g.MachineIds {
		machines = append(machines, cmpOr(h.MachineNames[id], id))
	}
	lines := []string{
		"Projects: " + scope(g.ProjectIds),
		"Machines: " + scope(machines),
		"Coding agents: " + scope(g.Agents),
		"Expires: " + g.ExpiresAt.AsTime().Local().Format(time.DateOnly),
	}
	if g.TaskVisibility == identityv1.TaskVisibility_TASK_VISIBILITY_ALL {
		lines = append(lines, "Also sees other agents’ sessions in these projects")
	}
	if g.MaxConcurrentSessions > 0 {
		lines = append(lines, fmt.Sprintf("Sessions at once: up to %d", g.MaxConcurrentSessions))
	}
	if g.Custodian != "" {
		lines = append(lines, "Connects through "+g.Custodian+", which holds its key")
	}
	lines = append(lines, "Fingerprint: "+identity.ShortFingerprint(g.ClientPublicKey))
	switch {
	case reissue:
		return Card{Title: fmt.Sprintf("Re-issue the grant of “%s”", g.ClientLabel), Lines: lines, Why: "It keeps the same access, now signed by this computer."}
	case g.Version > 1:
		return Card{Title: fmt.Sprintf("Change the access of “%s”", g.ClientLabel), Lines: lines, Why: "The new access takes effect at once; the old one stops working.",
			Warning: "Full trust: in the chosen projects, it can do anything you can."}
	}
	c := Card{Title: fmt.Sprintf("Approve “%s” to connect to your computers", g.ClientLabel), Lines: lines,
		Why:     "Uses your owner key to issue it a grant, which you can revoke at any time.",
		Warning: "Full trust: in the chosen projects, it can do anything you can."}
	if !confirmed {
		c.Code = identity.VerificationCode(identityv1.PairingKind_PAIRING_KIND_CLIENT, g.ClientPublicKey, g.OwnerId)
	}
	return c
}

func admin(c *identityv1.AdminCommand, h Hints, typ string) (Card, error) {
	target := "machine " + cmpOr(h.MachineNames[c.TargetMachineId], c.TargetMachineId)
	switch x := c.Command.(type) {
	case *identityv1.AdminCommand_AddProject:
		p := x.AddProject
		lines := []string{"Project: " + p.ProjectId, "Directory: " + p.Path, "Coding agents: " + scope(p.Agents)}
		if p.Workspace == identityv1.Workspace_WORKSPACE_IN_PLACE {
			lines = append(lines, "In place: agents change the directory's files directly")
		}
		return Card{Title: fmt.Sprintf("Add project “%s” to %s", p.ProjectId, target), Lines: lines,
			Why: "Agents with access can start sessions there and read and write its files."}, nil
	case *identityv1.AdminCommand_RemoveProject:
		return Card{Title: fmt.Sprintf("Remove project “%s” from %s", x.RemoveProject.ProjectId, target),
			Why: "Agents can no longer start sessions there; its files stay."}, nil
	case *identityv1.AdminCommand_SetPaused:
		if x.SetPaused.Paused {
			return Card{Title: "Pause " + target, Why: "It takes no new sessions until resumed."}, nil
		}
		return Card{Title: "Resume " + target, Why: "It takes new sessions again."}, nil
	case *identityv1.AdminCommand_SetName:
		return Card{Title: fmt.Sprintf("Rename %s to “%s”", target, x.SetName.Name)}, nil
	case *identityv1.AdminCommand_SetAgentDefaults:
		d := x.SetAgentDefaults
		return Card{Title: fmt.Sprintf("Set the model %s runs with on %s", d.Agent, target),
			Lines: []string{"Model: " + cmpOr(d.Model, "its own"), "Reasoning effort: " + cmpOr(d.Effort, "its own"), "Speed: " + d.Speed.String()}}, nil
	}
	return Card{}, &Refused{typ, "an unknown command"}
}

func scope(ids []string) string {
	if len(ids) == 0 || slices.Contains(ids, "*") {
		return "all"
	}
	return strings.Join(ids, ", ")
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
