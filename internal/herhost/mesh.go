package herhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// The mesh host (docs/her-mesh.md §4.1): hermeshd offers Her's own
// conversation a group of tools, runs them for her, says what a write would
// do before the owner is asked, and puts the news of her sessions into her
// conversation.

// Mesh is what serves her's mesh tools: Her's use of Hermesh as one of the
// owner's agents (internal/herlink).
type Mesh interface {
	// Group is the group her is offered; nil while Her has no grant.
	Group() *MeshGroup
	// Describe says what a call that writes would do, for the owner, and
	// the rule that "don't ask again" would keep (nil: none).
	Describe(ctx context.Context, bot, name string, args json.RawMessage) (summary string, rule []string)
	// Call runs one tool and returns its JSON document.
	Call(ctx context.Context, bot, callID, name string, args json.RawMessage) (output string, isError bool)
}

// MeshGroup is the params of `mesh/host`.
type MeshGroup struct {
	Summary    string     `json:"summary"`
	Guidelines []string   `json:"guidelines"`
	Tools      []MeshTool `json:"tools"`
}

type MeshTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Writes      bool           `json:"writes"`
}

func (h *Host) mesh() Mesh {
	if h.cfg.Mesh == nil {
		return nil
	}
	return h.cfg.Mesh()
}

// offerMesh tells her on c which mesh group there is now: the Mesh's, or
// none.
func (h *Host) offerMesh(ctx context.Context, c *conn) error {
	g := &MeshGroup{Guidelines: []string{}, Tools: []MeshTool{}}
	if m := h.mesh(); m != nil {
		if mg := m.Group(); mg != nil {
			g = mg
		}
	}
	params, _ := json.Marshal(g)
	res, err := c.call(ctx, "mesh/host", params)
	return answered("mesh/host", res, err)
}

// MeshChanged offers her the mesh group again, after Her got or lost her
// grant. Nothing to do while her is not running: she is offered it when
// she starts.
func (h *Host) MeshChanged() {
	h.mu.Lock()
	mon := h.mon
	h.mu.Unlock()
	if mon == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dialWait)
	defer cancel()
	if err := h.offerMesh(ctx, mon); err != nil {
		h.log.Warn("offering her the mesh group", "err", err)
	}
}

// News puts news into an assistant's conversation and wakes her
// (`mesh/news`). It returns once her has it in the conversation, which may
// take as long as the step she is in.
func (h *Host) News(ctx context.Context, bot, id, fromID, fromName, text string) error {
	h.mu.Lock()
	mon := h.mon
	h.mu.Unlock()
	if mon == nil {
		return errOff
	}
	params, _ := json.Marshal(map[string]any{"bot": bot, "id": id, "from": map[string]string{"id": fromID, "name": fromName}, "text": text})
	res, err := mon.call(ctx, "mesh/news", params)
	if err != nil {
		return err
	}
	if res.Error != nil {
		if res.Error.Code == noSuchBot {
			return fmt.Errorf("%w: %s", ErrRefused, res.Error.Message)
		}
		return fmt.Errorf("mesh/news: %s", res.Error.Message)
	}
	return nil
}

// noSuchBot is her's error code for an assistant she doesn't have.
const noSuchBot = -32040

// ErrRefused: the news is for an assistant that is no more; trying again
// changes nothing. Her's other errors (the conversation closed before it
// took the news) pass, and the news is to be delivered again.
var ErrRefused = errors.New("her has no such assistant")

// Bots are the ids of her's assistants, as last seen.
func (h *Host) Bots() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.snap == nil {
		return nil
	}
	ids := make([]string, 0, len(h.snap.bots))
	for id := range h.snap.bots {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// serveMesh answers her's requests to the mesh host.
func (h *Host) serveMesh(ctx context.Context, method string, params json.RawMessage) (any, error) {
	m := h.mesh()
	if m == nil {
		return nil, errors.New("Her has no access to the mesh")
	}
	var p struct {
		Bot       string          `json:"bot"`
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	switch method {
	case "mesh/describe":
		summary, rule := m.Describe(ctx, p.Bot, p.Name, p.Arguments)
		return map[string]any{"summary": summary, "rule": rule}, nil
	case "mesh/call":
		out, isErr := m.Call(ctx, p.Bot, p.CallID, p.Name, p.Arguments)
		return map[string]any{"output": out, "is_error": isErr}, nil
	}
	return nil, fmt.Errorf("unknown method %s", method)
}
