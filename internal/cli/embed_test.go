package cli

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/hermeshai/hermesh/pkg/identity"
)

func TestToolsAreTheCommands(t *testing.T) {
	tools := map[string]Tool{}
	for _, tl := range Tools() {
		tools[tl.Name] = tl
	}
	for _, name := range []string{"auth_status", "project_list", "session_start", "session_get", "session_list", "session_events",
		"session_answer", "session_interrupt", "session_archive", "session_unarchive", "session_diff",
		"message_send", "message_list", "message_ack", "message_status", "message_contacts"} {
		if _, ok := tools[name]; !ok {
			t.Errorf("no tool %s", name)
		}
	}
	for _, name := range []string{"auth_login", "auth_logout", "version", "session_send"} {
		if _, ok := tools[name]; ok {
			t.Errorf("%s is the local CLI's own", name)
		}
	}
	start := tools["session_start"]
	param := func(tl Tool, name string) *ToolParam {
		i := slices.IndexFunc(tl.Params, func(p ToolParam) bool { return p.Name == name })
		if i < 0 {
			return nil
		}
		return &tl.Params[i]
	}
	if p := param(start, "project"); p == nil || !p.Required || p.Type != "string" {
		t.Errorf("project: %+v", p)
	}
	if p := param(start, "wait"); p == nil || p.Type != "boolean" {
		t.Errorf("wait: %+v", p)
	}
	if p := param(start, "question_timeout"); p == nil || p.Type != "duration" || p.Flag != "question-timeout" {
		t.Errorf("question_timeout: %+v", p)
	}
	for _, local := range []string{"file", "prompt_file", "output"} {
		if param(start, local) != nil {
			t.Errorf("%s reads this computer's files", local)
		}
	}
	if get := tools["session_get"]; get.Arg == nil || get.Arg.Name != "session_id" || !get.Arg.Required {
		t.Errorf("session_get's argument: %+v", get.Arg)
	}
	if p := param(tools["session_answer"], "choose"); p == nil || p.Type != "array" {
		t.Errorf("choose: %+v", p)
	}
	if p := param(tools["session_events"], "limit"); p == nil || p.Type != "integer" {
		t.Errorf("limit: %+v", p)
	}
	// A list of ids as the positional argument.
	if ack := tools["message_ack"]; ack.Arg == nil || ack.Arg.Name != "message_id" || ack.Arg.Type != "array" {
		t.Errorf("message_ack's argument: %+v", ack.Arg)
	} else if args, err := ack.Args(map[string]any{"message_id": []any{"msg_a", "msg_b"}, "held": true}); err != nil ||
		!slices.Equal(args, []string{"message", "ack", "--held=true", "--", "msg_a", "msg_b"}) {
		t.Errorf("message_ack's args: %v %v", args, err)
	}
	if p := param(tools["message_send"], "to"); p == nil || !p.Required {
		t.Errorf("message_send's to: %+v", p)
	}
}

func TestToolArgs(t *testing.T) {
	var answer Tool
	for _, tl := range Tools() {
		if tl.Name == "session_answer" {
			answer = tl
		}
	}
	args, err := answer.Args(map[string]any{"session_id": "-s_x", "choose": []any{"Yes", "2"}, "wait": true, "text": "--danger"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"session", "answer", "--choose=Yes", "--choose=2", "--text=--danger", "--wait=true", "--", "-s_x"}
	for _, w := range want {
		if !slices.Contains(args, w) {
			t.Fatalf("args %q lack %q", args, w)
		}
	}
	if args[len(args)-2] != "--" {
		t.Fatalf("the session id comes after --: %q", args)
	}
	if _, err := answer.Args(map[string]any{}); err == nil {
		t.Fatal("the session id is required")
	}
	if _, err := answer.Args(map[string]any{"session_id": "s", "wait": "yes"}); err == nil {
		t.Fatal("wait is a boolean")
	}
}

func TestExecWithoutGrant(t *testing.T) {
	key, _ := identity.GenerateKey()
	doc, code := Exec(context.Background(), ExecOptions{Identity: Identity{RelayURL: "http://relay.invalid", OwnerID: "o_x", Key: key}}, []string{"project", "list"})
	var d struct {
		OK    bool `json:"ok"`
		Error struct {
			Kind string `json:"kind"`
		} `json:"error"`
	}
	if err := json.Unmarshal(doc, &d); err != nil || d.OK || d.Error.Kind != "not_paired" || code != exitDenied {
		t.Fatalf("doc %s code %d", doc, code)
	}
	// A usage error is a document too.
	doc, code = Exec(context.Background(), ExecOptions{}, []string{"session", "get"})
	if json.Unmarshal(doc, &d) != nil || d.OK || code != exitUsage {
		t.Fatalf("usage: %s %d", doc, code)
	}
}
