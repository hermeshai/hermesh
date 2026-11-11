package herhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

type fakeMesh struct{ group *MeshGroup }

func (m fakeMesh) Group() *MeshGroup { return m.group }
func (m fakeMesh) Describe(_ context.Context, bot, name string, args json.RawMessage) (string, []string) {
	return "Start " + name + " for " + bot, []string{"mesh", "mini/app"}
}
func (m fakeMesh) Call(_ context.Context, bot, callID, name string, args json.RawMessage) (string, bool) {
	return fmt.Sprintf("%s %s %s %s", bot, callID, name, args), name == "fails"
}

// The mesh host: her is offered the group (none: an empty one), her
// requests on the connection are answered, and news goes to her.
func TestMeshHost(t *testing.T) {
	offered := make(chan map[string]any, 4)
	answers := make(chan map[string]any, 4)
	news := make(chan map[string]any, 1)
	sock := fakeHer(t, func(req map[string]any, c net.Conn) {
		switch req["method"] {
		case "mesh/host":
			offered <- req["params"].(map[string]any)
			fmt.Fprintf(c, `{"jsonrpc":"2.0","id":%v,"result":{}}`+"\n", req["id"])
			fmt.Fprintln(c, `{"jsonrpc":"2.0","id":"r1","method":"mesh/describe","params":{"bot":"b1","name":"session_start","arguments":{"project":"mini/app"}}}`)
			fmt.Fprintln(c, `{"jsonrpc":"2.0","id":"r2","method":"mesh/call","params":{"bot":"b1","call_id":"c9","name":"session_get","arguments":{"session_id":"s_1"}}}`)
			fmt.Fprintln(c, `{"jsonrpc":"2.0","id":"r3","method":"mesh/call","params":{"bot":"b1","call_id":"c10","name":"fails","arguments":{}}}`)
		case "mesh/news":
			news <- req["params"].(map[string]any)
			fmt.Fprintf(c, `{"jsonrpc":"2.0","id":%v,"error":{"code":-32040,"message":"no such bot"}}`+"\n", req["id"])
		case nil:
			answers <- req
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := dial(ctx, sock, "test", true)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var mesh Mesh
	h := New(Config{Mesh: func() Mesh { return mesh }})
	c.mu.Lock()
	c.serve = h.serveMesh
	c.mu.Unlock()

	// No mesh: an empty group, and her requests are refused.
	if err := h.offerMesh(ctx, c); err != nil {
		t.Fatal(err)
	}
	if g := <-offered; len(g["tools"].([]any)) != 0 {
		t.Fatalf("a group without a mesh: %v", g)
	}
	for range 3 {
		if a := <-answers; a["error"] == nil {
			t.Fatalf("answered without a mesh: %v", a)
		}
	}

	mesh = fakeMesh{group: &MeshGroup{Summary: "s", Guidelines: []string{"g"}, Tools: []MeshTool{{Name: "session_get", Description: "d", Parameters: map[string]any{"type": "object"}}}}}
	if err := h.offerMesh(ctx, c); err != nil {
		t.Fatal(err)
	}
	if g := <-offered; g["summary"] != "s" || len(g["tools"].([]any)) != 1 {
		t.Fatalf("the group: %v", g)
	}
	got := map[any]map[string]any{}
	for range 3 {
		a := <-answers
		got[a["id"]], _ = a["result"].(map[string]any)
	}
	if r := got["r1"]; r["summary"] != "Start session_start for b1" || fmt.Sprint(r["rule"]) != "[mesh mini/app]" {
		t.Errorf("describe: %v", r)
	}
	if r := got["r2"]; r["output"] != `b1 c9 session_get {"session_id":"s_1"}` || r["is_error"] != false {
		t.Errorf("call: %v", r)
	}
	if r := got["r3"]; r["is_error"] != true {
		t.Errorf("a failed call: %v", r)
	}

	// News: her's refusal is final, a lost connection is not.
	h.mon = c
	if err := h.News(ctx, "b1", "msg_1", "s_1", "Codex in mini/app", "text"); err == nil || !errors.Is(err, ErrRefused) {
		t.Fatalf("news her refused: %v", err)
	}
	if n := <-news; n["bot"] != "b1" || n["id"] != "msg_1" || n["text"] != "text" || n["from"].(map[string]any)["name"] != "Codex in mini/app" {
		t.Errorf("news: %v", n)
	}
	c.Close()
	if err := h.News(ctx, "b1", "msg_1", "s_1", "x", "text"); err == nil || errors.Is(err, ErrRefused) {
		t.Fatalf("news on a closed connection: %v", err)
	}
}
