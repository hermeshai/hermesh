package e2e

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestHerMeshReal talks to the Her of a hermeshd that is really in use
// (docs/her-mesh.md), with her real model and the owner's real machines:
// for trying what the mock can't show, how she uses the mesh tools. It
// sends her one message and prints her conversation while it follows it.
//
//	HERMESH_REAL_HER_SOCKET   the hermeshd's local API socket; unset: skipped
//	HERMESH_REAL_HER_MACHINE  the machine Her runs on (default: this one's own)
//	HERMESH_REAL_HER_GRANT=1  give a Her without her grant the grant first
//	                          (the owner confirms it with the root key)
//	HERMESH_REAL_HER_ASK      the message; unset: only her status is printed
//	HERMESH_REAL_HER_APPROVE  how approvals she asks for are answered:
//	                          approve, approve_for_session or deny; unset:
//	                          they are left for the owner, in the app
//	HERMESH_REAL_HER_FOLLOW   how long to follow (default 10m); it also ends
//	                          once she has been idle for HERMESH_REAL_HER_IDLE
//	                          (default 90s) after telling the owner something
func TestHerMeshReal(t *testing.T) {
	sock := os.Getenv("HERMESH_REAL_HER_SOCKET")
	if sock == "" {
		t.Skip("HERMESH_REAL_HER_SOCKET is not set")
	}
	dur := func(name string, def time.Duration) time.Duration {
		if d, err := time.ParseDuration(os.Getenv(name)); err == nil {
			return d
		}
		return def
	}
	c := localClient(t, sock)
	ctx, cancel := context.WithTimeout(context.Background(), dur("HERMESH_REAL_HER_FOLLOW", 10*time.Minute))
	defer cancel()
	machine := os.Getenv("HERMESH_REAL_HER_MACHINE")
	if machine == "" {
		machine = localMachine(t, c)
	}
	status := func() {
		m := listedMachine(t, c, machine)
		her := m.GetHer()
		t.Logf("Her on %s (%s): enabled=%v running=%v model_ready=%v mesh=%v has_key=%v", m.GetMachineName(), machine,
			her.GetEnabled(), her.GetRunning(), her.GetModelReady(), her.GetMesh(), len(her.GetClientPublicKey()) > 0)
	}
	status()
	if her := listedMachine(t, c, machine).GetHer(); her.GetEnabled() && !her.GetMesh() && os.Getenv("HERMESH_REAL_HER_GRANT") == "1" {
		t.Log("asking the owner to sign Her's grant…")
		if _, err := c.SetHer(ctx, &localv1.SetHerRequest{MachineId: machine, Enabled: true}); err != nil {
			t.Fatalf("SetHer: %v", err)
		}
		eventually(t, time.Minute, "Her on the mesh", func() bool { return listedMachine(t, c, machine).GetHer().GetMesh() })
		status()
	}
	ask := os.Getenv("HERMESH_REAL_HER_ASK")
	if ask == "" {
		return
	}
	env := &herEnvironment{c: c, machine: machine, bot: defaultBot(t, c, machine)}
	stream, op, session := openAssistant(t, ctx, env)
	defer stream.Close()
	if res, err := c.HerSend(ctx, &localv1.HerSendRequest{MachineId: machine, BotId: env.bot.ID, Text: ask}); err != nil || !res.Delivered {
		t.Fatalf("HerSend: %v %v", res, err)
	}
	t.Logf(">>> %s", ask)
	idle := dur("HERMESH_REAL_HER_IDLE", 90*time.Second)
	events := make(chan json.RawMessage, 256)
	go func() {
		defer close(events)
		for stream.Receive() {
			if n := stream.Msg().GetNotification(); n.GetMethod() == "session/event" {
				events <- n.Params
			}
		}
	}()
	said, working := false, true
	for {
		wait := time.Hour
		if said && !working {
			wait = idle
		}
		select {
		case <-time.After(wait):
			return
		case raw, ok := <-events:
			if !ok {
				t.Logf("the conversation's stream ended: %v", stream.Err())
				return
			}
			var p struct {
				Event map[string]any `json:"event"`
			}
			if json.Unmarshal(raw, &p) != nil {
				continue
			}
			e := p.Event
			typ, _ := e["type"].(string)
			name, _ := e["name"].(string)
			switch typ {
			case "task_started", "turn_started":
				working = true
			case "task_completed":
				working = false
			case "approval_requested":
				t.Logf("APPROVAL %s: %v (rule %v)", name, e["summary"], e["proposed_rule"])
				if d := os.Getenv("HERMESH_REAL_HER_APPROVE"); d != "" {
					herCall(t, env, op.ConnectionId, "session/op", map[string]any{"session_id": session,
						"op": map[string]any{"type": "approval", "call_id": e["call_id"], "decision": map[string]string{"type": d}}})
					t.Logf("  answered: %s", d)
				}
				continue
			case "tool_started":
				if name == "send_to_user" {
					said = true
				}
			}
			if typ == "tool_running" || typ == "message_started" || typ == "usage" {
				continue
			}
			b, _ := json.Marshal(e)
			if s := string(b); len(s) > 1800 {
				t.Logf("%s… (%d bytes)", strings.ToValidUTF8(s[:1800], ""), len(s))
			} else {
				t.Logf("%s", b)
			}
		}
	}
}
