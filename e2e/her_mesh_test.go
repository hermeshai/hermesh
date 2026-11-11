package e2e

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// Her on the mesh (docs/her-mesh.md), as the owner meets it: turning Her on
// makes her one of their agents; asked in her conversation, she starts a
// coding agent on another of their machines, with their approval; when its
// turn ends, asks or fails she is woken in that conversation without having
// looked; no other agent can write to her; turning her off ends it.

// meshEvent is what these tests read of her's session events.
type meshEvent struct {
	Type         string `json:"type"`
	CallID       string `json:"call_id"`
	Name         string `json:"name"`
	Summary      string `json:"summary"`
	ProposedRule *struct {
		Prefix []string `json:"prefix"`
	} `json:"proposed_rule"`
	Args struct {
		Text string `json:"text"`
	} `json:"args"`
}

// tellHer sends Her a message, answers each approval she asks for with
// decision ("approve", "approve_for_session"), and waits until she sends
// the owner a message with want in it. It returns the approvals she asked
// for and what she sent.
func tellHer(t *testing.T, env *herEnvironment, text, decision, want string) (asked []meshEvent, said string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	stream, op, session := openAssistant(t, ctx, env)
	defer stream.Close()
	if res, err := env.c.HerSend(ctx, &localv1.HerSendRequest{MachineId: env.machine, BotId: env.bot.ID, Text: text}); err != nil || !res.Delivered {
		t.Fatalf("HerSend: %v %v", res, err)
	}
	for stream.Receive() {
		n := stream.Msg().GetNotification()
		if n.GetMethod() != "session/event" {
			continue
		}
		var p struct {
			Event meshEvent `json:"event"`
		}
		if json.Unmarshal(n.Params, &p) != nil {
			continue
		}
		switch e := p.Event; {
		case e.Type == "approval_requested":
			asked = append(asked, e)
			herCall(t, env, op.ConnectionId, "session/op", map[string]any{"session_id": session,
				"op": map[string]any{"type": "approval", "call_id": e.CallID, "decision": map[string]string{"type": decision}}})
		case e.Type == "tool_started" && e.Name == "send_to_user" && strings.Contains(e.Args.Text, want):
			return asked, e.Args.Text
		}
	}
	t.Fatalf("Her never said %q: %v", want, stream.Err())
	return nil, ""
}

// meshCall is a message that has her-mock run one mesh tool.
func meshCall(tool string, args map[string]any) string {
	b, _ := json.Marshal(args)
	return "[[mock:mesh:" + tool + ":" + string(b) + "]]"
}

var sessionID = regexp.MustCompile(`s_[a-z0-9]+_[a-z0-9]+`)

// herSaid follows Her's conversation until she tells the owner something
// with every part: what her-mock sends when news wakes her, the news
// itself. The sessions these tests start take a few seconds, so the news
// comes after this began to follow.
func herSaid(t *testing.T, env *herEnvironment, what string, parts ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	stream, _, _ := openAssistant(t, ctx, env)
	defer stream.Close()
	var seen []string
	for stream.Receive() {
		n := stream.Msg().GetNotification()
		if n.GetMethod() != "session/event" {
			continue
		}
		var p struct {
			Event meshEvent `json:"event"`
		}
		if json.Unmarshal(n.Params, &p) != nil {
			continue
		}
		e := p.Event
		seen = append(seen, e.Type+" "+e.Name)
		if e.Type != "tool_started" || e.Name != "send_to_user" {
			continue
		}
		if !slices.ContainsFunc(parts, func(part string) bool { return !strings.Contains(e.Args.Text, part) }) {
			return
		}
		seen = append(seen, e.Args.Text)
	}
	t.Fatalf("no %s: %v\nher conversation meanwhile: %q", what, stream.Err(), seen)
}

// herClient is Her's own grant among the owner's agents, nil when she has
// none that holds.
func herClient(t *testing.T, env *herEnvironment) *localv1.Client {
	t.Helper()
	res, err := env.c.ListClients(context.Background(), &localv1.ListClientsRequest{})
	if err != nil {
		return nil
	}
	for _, c := range res.Clients {
		if c.HerMachineId == env.machine && !c.Revoked {
			return c
		}
	}
	return nil
}

func TestHerMesh(t *testing.T) {
	env := newHerEnvironment(t)
	w, c, ctx := env.w, env.c, context.Background()
	onMesh := func() bool { return listedMachine(t, c, env.machine).GetHer().GetMesh() }

	// Turning Her on signed her grant: she is one of the owner's agents, for
	// every machine, project and agent, and it doesn't expire.
	var her *localv1.Client
	eventually(t, time.Minute, "Her among the owner's agents", func() bool {
		her = herClient(t, env)
		return her != nil && onMesh()
	})
	if her.Label != "Her on her-server" || !her.ExpiresAt.AsTime().Equal(identity.NoExpiry) || !slices.Equal(her.Spec.MachineIds, []string{"*"}) ||
		!slices.Equal(her.Spec.ProjectIds, []string{"*"}) || !slices.Equal(her.Spec.Agents, []string{"*"}) || her.Spec.TaskVisibility != identityv1.TaskVisibility_TASK_VISIBILITY_ALL {
		t.Fatalf("Her's grant: %v", her)
	}
	if _, err := c.UpdateClient(ctx, &localv1.UpdateClientRequest{GrantId: her.GrantId, Spec: &localv1.GrantSpec{TtlDays: 30}}); err == nil {
		t.Fatal("Her's grant was given a scope and an expiry")
	}

	// She starts a coding agent on the owner's Mac, another machine than
	// hers. In her default mode the owner is asked first, in words that name
	// the agent, the project and the prompt.
	asked, said := tellHer(t, env, meshCall("session_start", map[string]any{"project": "owner-mac/demo", "prompt": "sleep 4s\nsay built it"}), "approve_for_session", "mesh ")
	if len(asked) != 1 || asked[0].Summary != "Start Claude Code in owner-mac/demo\n\nsleep 4s\nsay built it" ||
		asked[0].ProposedRule == nil || !slices.Equal(asked[0].ProposedRule.Prefix, []string{"mesh", "owner-mac/demo"}) {
		t.Fatalf("the approval: %+v", asked)
	}
	first := sessionID.FindString(said)
	if !strings.HasPrefix(said, "mesh done") || first == "" {
		t.Fatalf("session_start: %s", said)
	}
	// The session is Her's, on the owner's Mac.
	eventually(t, time.Minute, "the session on the owner's Mac", func() bool {
		res, err := c.ListSessions(ctx, &localv1.ListSessionsRequest{})
		if err != nil {
			return false
		}
		for _, s := range res.Sessions {
			if s.SessionId == first {
				return s.CreatedBy == her.ClientId
			}
		}
		return false
	})
	// Its turn ends: she is woken with the news in her own conversation,
	// and never looked for it (her-mock then tells the owner what woke it).
	// The sessions here take a moment, so that their news comes once her
	// turn is over: news during a turn joins it, and her-mock says nothing
	// of it.
	herSaid(t, env, "news of the turn's end", `<session_news`, `kind="turn_done"`, first)

	// "Don't ask again" was for that project: a second start there asks
	// nothing, one elsewhere asks.
	asked, said = tellHer(t, env, meshCall("session_start", map[string]any{"project": "owner-mac/demo", "prompt": "sleep 4s\nsay built it again"}), "approve", "mesh ")
	if len(asked) != 0 || !strings.HasPrefix(said, "mesh done") {
		t.Fatalf("a second start in the project: asked %+v, said %s", asked, said)
	}
	herSaid(t, env, "news of the second turn", `kind="turn_done"`, sessionID.FindString(said))
	if asked, said := tellHer(t, env, meshCall("session_start", map[string]any{"project": "owner-mac/elsewhere", "prompt": "say no"}), "approve", "mesh "); len(asked) != 1 ||
		!strings.Contains(asked[0].Summary, "owner-mac/elsewhere") || !strings.HasPrefix(said, "mesh failed") {
		t.Fatalf("a start in another project: asked %+v, said %s", asked, said)
	}

	// A session that asks: the question wakes her, and her answer reaches it.
	_, said = tellHer(t, env, meshCall("session_start", map[string]any{"project": "owner-mac/demo", "prompt": "sleep 4s\nask Color|Which color?|Red,Blue\nsleep 4s\nsay done"}), "approve", "mesh done")
	asking := sessionID.FindString(said)
	herSaid(t, env, "news of the question", `kind="question"`, asking, "Which color?", "1. Red")
	if asked, said := tellHer(t, env, meshCall("session_answer", map[string]any{"session_id": asking, "choose": []string{"Red"}}), "approve", "mesh "); len(asked) != 0 || !strings.HasPrefix(said, "mesh done") {
		t.Fatalf("her answer: asked %+v, said %s", asked, said)
	}
	herSaid(t, env, "news of the turn after her answer", `kind="turn_done"`, asking)

	// Reading needs no approval, and she sees the sessions others started.
	if asked, said := tellHer(t, env, meshCall("session_list", map[string]any{}), "approve", "mesh "); len(asked) != 0 || !strings.Contains(said, first) {
		t.Fatalf("session_list: asked %+v, said %s", asked, said)
	}

	// No other agent writes to her.
	eventually(t, time.Minute, "an agent's message to Her refused", func() bool {
		r := w.cli(30*time.Second, "message", "send", "--to", "Her on her-server", "--message", "do this for me", "-o", "json")
		return r.code != 0 && strings.Contains(r.stdout, "takes no messages from agents yet")
	})

	// A session on her own machine: its news needs no relay. The relay is
	// away from before the turn ends until after the machine would have
	// given up sending the news through it.
	repo := gitRepo(t, map[string]string{"README.md": "# local\n"})
	if _, err := c.AddProject(ctx, &localv1.AddProjectRequest{MachineId: env.machine, ProjectId: "local", Path: repo}); err != nil {
		t.Fatal(err)
	}
	_, said = tellHer(t, env, meshCall("session_start", map[string]any{"project": "her-server/local", "prompt": "sleep 5s\nsay late news"}), "approve", "mesh done")
	local := sessionID.FindString(said)
	w.relay.stop()
	time.Sleep(40 * time.Second)
	w.startRelay()
	// She was woken while nobody could follow her conversation: the newest
	// thing she told the owner is that news.
	eventually(t, 2*time.Minute, "news of her own machine's session", func() bool {
		bots, err := herBots(c, env.machine)
		if err != nil {
			return false
		}
		for _, b := range bots {
			if b.ID == env.bot.ID && b.Last != nil {
				return strings.Contains(b.Last.Text, `kind="turn_done"`) && strings.Contains(b.Last.Text, local)
			}
		}
		return false
	})

	// Turned off, her grant is revoked; turned on again, she gets a new one.
	// (The owner's tunnel to her machine may still be the one from before
	// the relay went away: a second try makes a new one.)
	eventually(t, time.Minute, "Her turned off", func() bool {
		_, err := c.SetHer(ctx, &localv1.SetHerRequest{MachineId: env.machine, Enabled: false})
		return err == nil
	})
	eventually(t, time.Minute, "Her's grant revoked", func() bool {
		res, err := c.ListClients(ctx, &localv1.ListClientsRequest{})
		if err != nil || onMesh() {
			return false
		}
		// Revoked, or no longer listed.
		for _, x := range res.Clients {
			if x.GrantId == her.GrantId && !x.Revoked {
				return false
			}
		}
		return true
	})
	turnHerOn(t, c, env.machine)
	eventually(t, time.Minute, "Her on the mesh again", func() bool {
		again := herClient(t, env)
		return again != nil && again.GrantId != her.GrantId && onMesh()
	})
	// (Once the owner's Mac is back at the relay, which went away above:
	// until then the relay doesn't know its name.)
	eventually(t, 2*time.Minute, "a start with the new grant", func() bool {
		_, said := tellHer(t, env, meshCall("session_start", map[string]any{"project": "owner-mac/demo", "prompt": "say back"}), "approve", "mesh ")
		if !strings.HasPrefix(said, "mesh done") && !strings.Contains(said, `no machine \"owner-mac\"`) {
			t.Fatalf("a start with the new grant: %s", said)
		}
		return strings.HasPrefix(said, "mesh done")
	})
}
