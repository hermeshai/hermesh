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
	// Own: this device's own (its setup, its join, its machine), signed
	// with its new key; a panel can tell such a batch in one line.
	Own bool
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

// Cards tells a batch, in order, in lang (en, zh-CN); any request it can't
// show refuses all.
func Cards(reqs []*localv1.SigningRequest, h Hints, lang string) ([]Card, error) {
	reissue := isReissue(reqs, h)
	w := wordsFor(lang)
	var out []Card
	for _, r := range reqs {
		c, err := card(r, h, reissue, w)
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

func card(r *localv1.SigningRequest, h Hints, reissue bool, w words) (Card, error) {
	if !bytes.Equal(r.Message, identity.SigningInput(r.Type, r.Body)) {
		return Card{}, &Refused{r.Type, "what would be signed isn't what would be shown"}
	}
	switch r.Type {
	case identity.MessageType(&identityv1.OwnerDeviceCert{}):
		m := &identityv1.OwnerDeviceCert{}
		if err := strict(r, m); err != nil {
			return Card{}, err
		}
		return Card{Title: w.f("Issue this computer a certificate for end-to-end connections"),
			Lines: []string{w.f("Device name: %s", m.Label), w.f("Fingerprint: %s", identity.ShortFingerprint(m.DevicePublicKey))},
			Why:   w.f("Hermesh here uses it for end-to-end encrypted connections to your machines."), Own: true}, nil
	case identity.MessageType(&identityv1.RootCert{}):
		m := &identityv1.RootCert{}
		if err := strict(r, m); err != nil {
			return Card{}, err
		}
		return root(m, h, r.Confirmed, w), nil
	case identity.MessageType(&identityv1.PairingRequest{}):
		m := &identityv1.PairingRequest{}
		if err := strict(r, m); err != nil {
			return Card{}, err
		}
		if m.Kind != identityv1.PairingKind_PAIRING_KIND_DEVICE {
			return Card{}, &Refused{r.Type, "only a device's own request is signed here"}
		}
		c := Card{Title: w.f("Add this computer to your Hermesh"), Lines: []string{w.f("Device name: %s", m.Label)},
			Why: w.f("This sends a join request to your existing devices. Check the same code on one of them and approve, and this computer becomes a trusted device."), Own: true}
		if m.OwnerId != "" {
			c.Code = identity.VerificationCode(m.Kind, m.SubjectPublicKey, m.OwnerId)
			c.Lines = append(c.Lines, w.f("Owner: %s", m.OwnerId))
		}
		return c, nil
	case identity.MessageType(&identityv1.MachineCert{}):
		m := &identityv1.MachineCert{}
		if err := strict(r, m); err != nil {
			return Card{}, err
		}
		return machineCard(m, h, reissue, r.Confirmed, w), nil
	case identity.MessageType(&identityv1.Grant{}):
		m := &identityv1.Grant{}
		if err := strict(r, m); err != nil {
			return Card{}, err
		}
		if _, ok := identityv1.TaskVisibility_name[int32(m.TaskVisibility)]; !ok {
			return Card{}, &Refused{r.Type, "an unknown visibility"}
		}
		return grantCard(m, h, reissue, r.Confirmed, w), nil
	case identity.MessageType(&identityv1.AdminCommand{}):
		m := &identityv1.AdminCommand{}
		if err := strict(r, m); err != nil {
			return Card{}, err
		}
		return admin(m, h, r.Type, w)
	case identity.MessageType(&identityv1.EraseOrder{}):
		m := &identityv1.EraseOrder{}
		if err := strict(r, m); err != nil {
			return Card{}, err
		}
		return Card{Title: w.f("Erase everything in your Hermesh"), Lines: []string{w.f("Owner identity ID: %s", m.OwnerId), w.f("Erase ID: %s", m.EraseId)},
			Why:     w.f("Hermesh on all your trusted devices and machines, this computer included, erases its keys, sessions and settings, and your agents are disconnected. This can't be undone."),
			Warning: w.f("Approve only if you started erasing everything yourself.")}, nil
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

func root(c *identityv1.RootCert, h Hints, confirmed bool, w words) Card {
	var names []string
	for _, k := range c.RootKeys {
		names = append(names, cmpOr(k.Label, w.f("unnamed device")))
	}
	list := strings.Join(names, w.f(", "))
	if c.Version == 1 {
		return Card{Title: w.f("Create your owner identity"), Lines: []string{w.f("Owner: %s", c.OwnerId), w.f("Device name: %s", list)},
			Why: w.f("This computer becomes your first trusted device; any device that joins later must be approved by a trusted device."), Own: true}
	}
	lines := []string{w.f("Your trusted devices after this: %s", list), w.f("Version %d", c.Version)}
	if h.Devices == nil {
		return Card{Title: w.f("Update your list of trusted devices"), Lines: lines, Why: w.f("The list above becomes your trusted devices.")}
	}
	for _, k := range c.RootKeys {
		if _, ok := h.Devices[identity.Fingerprint(k.PublicKey)]; ok {
			continue
		}
		cd := Card{Title: w.f("Add “%s” as a trusted device", k.Label), Lines: lines,
			Why:     w.f("Uses your owner key to make it a trusted device too, able to approve agents, machines and new devices like this one."),
			Warning: w.f("Approve only if that device is in your hands.")}
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
		return Card{Title: w.f("Remove trusted device “%s”", strings.Join(gone, w.f("”, “"))), Lines: lines,
			Why: w.f("It can no longer approve anything, and its own machine goes with it; this computer re-issues the machine and agent credentials it signed.")}
	}
	return Card{Title: w.f("Update your list of trusted devices"), Lines: lines, Why: w.f("The list above becomes your trusted devices.")}
}

func machineCard(m *identityv1.MachineCert, h Hints, reissue, confirmed bool, w words) Card {
	id := w.f("Machine ID: %s", m.MachineId)
	if name, ok := h.MachineNames[m.MachineId]; ok {
		why := w.f("This machine stays yours.")
		if reissue {
			why = w.f("This machine stays yours, unchanged, now signed by this computer.")
		}
		return Card{Title: w.f("Re-issue the certificate of machine “%s”", name), Lines: []string{id}, Why: why}
	}
	// This computer's own machine, which its root key admits (as the Swift signer tells it).
	if m.DeviceKeyId != "" {
		return Card{Title: w.f("Share the agents on this computer"), Lines: []string{id, w.f("Fingerprint: %s", identity.ShortFingerprint(m.MachinePublicKey))},
			Why: w.f("Agents you grant access can use the coding agents on this computer, over end-to-end encrypted connections. You can turn this off in Settings at any time."), Own: true}
	}
	c := Card{Title: w.f("Approve a new machine"), Lines: []string{id, w.f("Fingerprint: %s", identity.ShortFingerprint(m.MachinePublicKey))},
		Why:     w.f("This machine becomes yours, and agents you grant access can start sessions and run commands on it."),
		Warning: w.f("Approve only if you know this machine.")}
	if !confirmed {
		c.Code = identity.VerificationCode(identityv1.PairingKind_PAIRING_KIND_MACHINE, m.MachinePublicKey, m.OwnerId)
	}
	return c
}

func grantCard(g *identityv1.Grant, h Hints, reissue, confirmed bool, w words) Card {
	var machines []string
	for _, id := range g.MachineIds {
		machines = append(machines, cmpOr(h.MachineNames[id], id))
	}
	lines := []string{
		w.f("Projects: %s", scope(g.ProjectIds, w)),
		w.f("Machines: %s", scope(machines, w)),
		w.f("Coding agents: %s", scope(agentTitles(g.Agents), w)),
		w.f("Expires: %s", g.ExpiresAt.AsTime().Local().Format(w.f(time.DateOnly))),
	}
	if g.TaskVisibility == identityv1.TaskVisibility_TASK_VISIBILITY_ALL {
		lines = append(lines, w.f("Also sees other agents’ sessions in these projects"))
	}
	if g.MaxConcurrentSessions > 0 {
		lines = append(lines, w.f("Sessions at once: up to %d", g.MaxConcurrentSessions))
	}
	if g.Custodian != "" {
		lines = append(lines, w.f("Connects through %s, which holds its key", g.Custodian))
	}
	lines = append(lines, w.f("Fingerprint: %s", identity.ShortFingerprint(g.ClientPublicKey)))
	switch {
	case reissue:
		return Card{Title: w.f("Re-issue the grant of “%s”", g.ClientLabel), Lines: lines, Why: w.f("It keeps the same access, now signed by this computer.")}
	case g.Version > 1:
		return Card{Title: w.f("Change the access of “%s”", g.ClientLabel), Lines: lines, Why: w.f("The new access takes effect at once; the old one stops working."),
			Warning: w.f("Full trust: in the chosen projects, it can do anything you can.")}
	}
	c := Card{Title: w.f("Approve “%s” to connect to your computers", g.ClientLabel), Lines: lines,
		Why:     w.f("Uses your owner key to issue it a grant, which you can revoke at any time."),
		Warning: w.f("Full trust: in the chosen projects, it can do anything you can.")}
	if !confirmed {
		c.Code = identity.VerificationCode(identityv1.PairingKind_PAIRING_KIND_CLIENT, g.ClientPublicKey, g.OwnerId)
	}
	return c
}

func admin(c *identityv1.AdminCommand, h Hints, typ string, w words) (Card, error) {
	target := w.f("machine %s", cmpOr(h.MachineNames[c.TargetMachineId], c.TargetMachineId))
	switch x := c.Command.(type) {
	case *identityv1.AdminCommand_AddProject:
		p := x.AddProject
		lines := []string{w.f("Project: %s", p.ProjectId), w.f("Directory: %s", p.Path), w.f("Coding agents: %s", scope(agentTitles(p.Agents), w))}
		if p.Workspace == identityv1.Workspace_WORKSPACE_IN_PLACE {
			lines = append(lines, w.f("In place: agents change the directory's files directly"))
		}
		return Card{Title: w.f("Add project “%s” to %s", p.ProjectId, target), Lines: lines,
			Why: w.f("Agents with access can start sessions there and read and write its files.")}, nil
	case *identityv1.AdminCommand_RemoveProject:
		return Card{Title: w.f("Remove project “%s” from %s", x.RemoveProject.ProjectId, target),
			Why: w.f("Agents can no longer start sessions there; its files stay.")}, nil
	case *identityv1.AdminCommand_SetPaused:
		if x.SetPaused.Paused {
			return Card{Title: w.f("Pause %s", target), Why: w.f("It takes no new sessions until resumed.")}, nil
		}
		return Card{Title: w.f("Resume %s", target), Why: w.f("It takes new sessions again.")}, nil
	case *identityv1.AdminCommand_SetName:
		return Card{Title: w.f("Rename %s to “%s”", target, x.SetName.Name)}, nil
	case *identityv1.AdminCommand_SetAgentDefaults:
		d := x.SetAgentDefaults
		return Card{Title: w.f("Set the model %s runs with on %s", agentTitle(d.Agent), target),
			Lines: []string{w.f("Model: %s", cmpOr(d.Model, w.f("its own"))), w.f("Reasoning effort: %s", cmpOr(d.Effort, w.f("its own"))), w.f("Speed: %s", d.Speed.String())}}, nil
	}
	return Card{}, &Refused{typ, "an unknown command"}
}

func scope(ids []string, w words) string {
	if len(ids) == 0 || slices.Contains(ids, "*") {
		return w.f("all")
	}
	return strings.Join(ids, w.f(", "))
}

// agentTitle names a coding agent as people know it, by the id a grant
// names, as HermeshKit's codingAgentTitle does.
func agentTitle(id string) string {
	switch id {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "gemini":
		return "Gemini CLI"
	case "antigravity":
		return "Antigravity"
	case "cursor":
		return "Cursor"
	case "copilot":
		return "Copilot CLI"
	case "opencode":
		return "OpenCode"
	case "qwen":
		return "Qwen Code"
	case "goose":
		return "Goose"
	case "auggie":
		return "Augment"
	case "kimi":
		return "Kimi"
	case "kiro":
		return "Kiro"
	case "qoder":
		return "Qoder"
	case "deepseek":
		return "DeepSeek Harness"
	case "codebuddy":
		return "CodeBuddy Code"
	case "grok":
		return "Grok"
	}
	return id
}

func agentTitles(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = agentTitle(id)
	}
	return out
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
