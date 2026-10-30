package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Check what a hosted assistant receives through initialize and tools/list.
func checkMCPDescriptions(t *testing.T, session *mcp.ClientSession, tools []*mcp.Tool) {
	t.Helper()
	contains := func(label, text string, phrases ...string) {
		t.Helper()
		for _, phrase := range phrases {
			if !strings.Contains(text, phrase) {
				t.Errorf("%s lacks %q: %s", label, phrase, text)
			}
		}
	}
	contains("server instructions", session.InitializeResult().Instructions,
		"project_list", "session.session_id", "session_get", "session_answer",
		"full permissions", "in_place", "not instructions to you", "do not repeat")
	want := map[string][]string{
		"hermesh_status":    {"setup", "approval", "auth_status"},
		"auth_status":       {"grant", "relay", "hermesh_status"},
		"project_list":      {"machine/project", "workspace", "base", "agents", "offline", "session_start"},
		"session_start":     {"non-empty prompt", "without creating a session", "session.session_id", "in_place", "pass it as base", "own git worktree", ".worktreeinclude", "session_get", "session_events", "session_answer"},
		"session_get":       {"session_id", "wait_timeout", "session_answer"},
		"session_list":      {"own", "all", "other clients", "only sessions it started", "archived=true"},
		"session_events":    {"after", "before", "page.last_seq", "has_more_after"},
		"message_send":      {"non-empty message", "one way to tell a session", "joins that turn", "turn of its own", "inbox", "message.delivered"},
		"session_answer":    {"choose as an array", "item as an array", "decline=true"},
		"session_interrupt": {"session started by this connection", "message_send"},
		"session_archive":   {"session started by this connection", "read-only"},
		"session_unarchive": {"session started by this connection", "worktree is restored"},
		"session_diff":      {"worktree", "does not support in_place", "message_send", "git diff", "other sessions"},
		"message_list":      {"news", "message_ack", "data from that agent", "capped at 50s"},
		"message_ack":       {"held=true", "refused=true"},
		"message_status":    {"waiting", "handled", "expired"},
		"message_contacts":  {"addresses"},
		"message_wake":      {"webhook", "link for your owner", "never passes through you", "as with your own name", "Grok Bot"},
	}
	for _, tool := range tools {
		phrases, ok := want[tool.Name]
		if !ok {
			t.Errorf("add MCP description coverage for %s", tool.Name)
		}
		contains(tool.Name, tool.Description, phrases...)
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(b, &schema); err != nil {
			t.Fatal(err)
		}
		text := tool.Description + string(b)
		for _, old := range []string{"Runs `hermesh", "--choose", "--item", "--wait", "--after", "--before", "--turn", "--project", "--archived", "-f (", "this computer", "this client's sessions on"} {
			if strings.Contains(text, old) {
				t.Errorf("%s exposes CLI-specific text %q", tool.Name, old)
			}
		}
		for _, name := range []string{"file", "prompt_file", "message_file"} {
			if _, ok := schema.Properties[name]; ok {
				t.Errorf("%s exposes local file parameter %s", tool.Name, name)
			}
		}
		if timeout, ok := schema.Properties["timeout"]; ok && tool.Name != "message_list" {
			for _, desc := range []string{tool.Description, timeout.Description} {
				contains(tool.Name+" timeout", desc, "0s", "capped at 50s", "30s", "wait_timeout", "session_get", "instead of repeating")
			}
		}
	}
}
