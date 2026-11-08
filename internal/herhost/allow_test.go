package herhost

import (
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

// The allowlist (docs/her-server.md §6.1): the Her entry's and the
// console's methods pass, anything else is refused; full access and
// connectors started as commands are refused however they are written,
// since only root-signed commands give them.
func TestCheck(t *testing.T) {
	for _, c := range []struct {
		name, method, params string
		code                 connect.Code // 0: allowed
	}{
		{"an assistant's list", "bots/subscribe", ``, 0},
		{"a prompt", "session/op", `{"session_id":"s","op":{"type":"prompt","input":[{"type":"text","text":"hi"}]}}`, 0},
		{"a console setting", "config/read", `{}`, 0},
		{"an API key", "auth/set", `{"provider":"openai","key":"sk-1"}`, 0},
		{"models, for both", "models/list", ``, 0},

		{"a method off the list", "server/shutdown", ``, connect.CodePermissionDenied},
		{"a session of its own", "session/new", `{"cwd":"/"}`, connect.CodePermissionDenied},
		{"a document panel", "doc/open", `{}`, connect.CodePermissionDenied},
		{"the rest of browser/*", "browser/reveal", `{}`, connect.CodePermissionDenied},
		{"initialize is hermeshd's", "initialize", `{"protocol_version":2}`, connect.CodePermissionDenied},
		{"an unknown method", "bots/everything", ``, connect.CodePermissionDenied},
		{"no method", "", ``, connect.CodePermissionDenied},
		{"params that are not JSON", "bots/open", `{"bot_id":`, connect.CodeInvalidArgument},

		{"asking first", "session/op", `{"session_id":"s","op":{"type":"set_permission_mode","mode":"manual"}}`, 0},
		{"accepting edits", "session/op", `{"session_id":"s","op":{"type":"set_permission_mode","mode":"accept-edits"}}`, 0},
		{"full access", "session/op", `{"session_id":"s","op":{"type":"set_permission_mode","mode":"bypass-permissions"}}`, connect.CodePermissionDenied},
		{"full access by its old name", "session/op", `{"session_id":"s","op":{"type":"set_permission_mode","mode":"full-auto"}}`, connect.CodePermissionDenied},
		{"full access by the op's old name", "session/op", `{"session_id":"s","op":{"type":"set_approval_mode","mode":"bypass-permissions"}}`, connect.CodePermissionDenied},
		{"a mode her may yet take as full access", "session/op", `{"session_id":"s","op":{"type":"set_permission_mode","mode":"auto"}}`, connect.CodePermissionDenied},
		{"full access behind a duplicate key", "session/op", `{"session_id":"s","op":{"type":"set_permission_mode","mode":"manual","mode":"bypass-permissions"}}`, connect.CodePermissionDenied},
		{"full access escaped", "session/op", `{"session_id":"s","op":{"type":"set_permission_mode","mode":"bypass-permissions"}}`, connect.CodePermissionDenied},

		{"a connector by address", "config/write", `{"layer":"user","edits":[{"key_path":"mcp_servers.gh","value":{"url":"https://example.com/mcp"}}]}`, 0},
		{"a connector switched off", "config/write", `{"layer":"user","edits":[{"key_path":"mcp_servers.fs.enabled","value":false}]}`, 0},
		{"a connector removed", "config/write", `{"layer":"user","edits":[{"key_path":"mcp_servers.fs","value":null}]}`, 0},
		{"a connector started as a command", "config/write", `{"layer":"user","edits":[{"key_path":"mcp_servers.fs","value":{"command":"sh","args":["-c","id"]}}]}`, connect.CodePermissionDenied},
		{"a command upserted", "config/write", `{"layer":"user","edits":[{"key_path":"mcp_servers.fs","merge":"upsert","value":{"args":["--evil"]}}]}`, connect.CodePermissionDenied},
		{"a command's key", "config/write", `{"layer":"user","edits":[{"key_path":"mcp_servers.fs.command","value":"sh"}]}`, connect.CodePermissionDenied},
		{"a command's environment", "config/write", `{"layer":"user","edits":[{"key_path":"mcp_servers.fs.env.PATH","value":"/tmp"}]}`, connect.CodePermissionDenied},
		{"a command behind quotes", "config/write", `{"layer":"user","edits":[{"key_path":"\"mcp_servers\".\"fs\".command","value":"sh"}]}`, connect.CodePermissionDenied},
		{"a command in the whole table", "config/write", `{"layer":"user","edits":[{"key_path":"mcp_servers","value":{"gh":{"url":"https://x"},"fs":{"command":"sh"}}}]}`, connect.CodePermissionDenied},
		{"a command after a harmless edit", "config/write", `{"layer":"user","edits":[{"key_path":"model","value":"mock/mock-1"},{"key_path":"mcp_servers.fs.cwd","value":"/"}]}`, connect.CodePermissionDenied},
		{"a key path her refuses", "config/write", `{"layer":"user","edits":[{"key_path":"mcp_servers..fs","value":1}]}`, connect.CodeInvalidArgument},

		{"the default mode asking", "config/write", `{"layer":"user","edits":[{"key_path":"permission_mode","value":"accept-edits"}]}`, 0},
		{"the default mode removed", "config/write", `{"layer":"user","edits":[{"key_path":"permission_mode","value":null}]}`, 0},
		{"full access by default", "config/write", `{"layer":"user","edits":[{"key_path":"permission_mode","value":"bypass-permissions"}]}`, connect.CodePermissionDenied},
		{"full access by default, old key", "config/write", `{"layer":"user","edits":[{"key_path":"approval_mode","value":"full-auto"}]}`, connect.CodePermissionDenied},
		{"full access in a project's layer", "config/write", `{"layer":"user_project","cwd":"/p","edits":[{"key_path":"permission_mode","value":"bypass-permissions"}]}`, connect.CodePermissionDenied},
		{"full access in a table of its own", "config/write", `{"layer":"user","edits":[{"key_path":"bots.b1.permission_mode","value":"bypass-permissions"}]}`, connect.CodePermissionDenied},
		{"full access inside a table written", "config/write", `{"layer":"user","edits":[{"key_path":"projects.p","merge":"upsert","value":{"permission_mode":"bypass-permissions"}}]}`, connect.CodePermissionDenied},
		{"full access in a list of tables", "config/write", `{"layer":"user","edits":[{"key_path":"profiles","value":[{"name":"x","permission_mode":"bypass-permissions"}]}]}`, connect.CodePermissionDenied},

		{"probing a server by address", "mcp/probe", `{"probe_id":"p","server":{"url":"https://example.com/mcp"}}`, 0},
		{"probing a command, which starts it", "mcp/probe", `{"probe_id":"p","server":{"command":"sh","args":["-c","id"]}}`, connect.CodePermissionDenied},
		{"signing in to a server by address", "mcp/login", `{"name":"gh","server":{"url":"https://example.com/mcp"}}`, 0},
		{"signing in to a command", "mcp/login", `{"name":"fs","server":{"command":"sh"}}`, connect.CodePermissionDenied},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := check(c.method, []byte(c.params))
			if got := connect.CodeOf(err); err == nil && c.code != 0 || err != nil && got != c.code {
				t.Fatalf("check(%s, %s) = %v, want code %v", c.method, c.params, err, c.code)
			}
		})
	}
}

// What is looked into is what goes to her: params are decoded once and
// encoded again, so a duplicate key reaches her as the value checked.
func TestCheckSendsWhatItChecked(t *testing.T) {
	params, err := check("session/op", []byte(`{"session_id":"s","op":{"type":"set_permission_mode","mode":"bypass-permissions","mode":"manual"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(params), `"mode"`) != 1 || !strings.Contains(string(params), `"mode":"manual"`) {
		t.Fatalf("sent %s", params)
	}
	// Others go as they came, numbers whole.
	raw := `{"bot_id":"b","history_limit":12345678901234567890}`
	if params, _ := check("bots/open", []byte(raw)); string(params) != raw {
		t.Fatalf("sent %s", params)
	}
	params, _ = check("config/write", []byte(`{"layer":"user","expected_version":12345678901234567890,"edits":[]}`))
	var v map[string]json.Number
	json.Unmarshal(params, &v)
	if v["expected_version"] != "12345678901234567890" {
		t.Fatalf("sent %s", params)
	}
}

func TestParseKeyPath(t *testing.T) {
	for path, want := range map[string]string{
		"mcp_servers.github.enabled":        "mcp_servers|github|enabled",
		`mcp_servers.x.headers."X-Team.Id"`: "mcp_servers|x|headers|X-Team.Id",
		`"a\"b".c`:                          `a"b|c`,
		`"".x`:                              "|x",
		"a..b":                              "error",
		"a.b c":                             "error",
		`a."b`:                              "error",
		"":                                  "error",
		"a.":                                "error",
	} {
		keys, err := parseKeyPath(path)
		got := strings.Join(keys, "|")
		if err != nil {
			got = "error"
		}
		if got != want {
			t.Errorf("parseKeyPath(%q) = %q, want %q", path, got, want)
		}
	}
}
