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
		{"the owner's Chrome, connected or not", "browser/status", `{}`, 0},
		{"which Chrome Her uses", "browser/use_extension", `{"instance":"i"}`, 0},
		{"computer use's permissions", "desktop/status", ``, 0},

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
		{"full access escaped", "session/op", strings.ReplaceAll(`{"session_id":"s","op":{"type":"set_permission_mode","mo#u0064e":"bypass-permissions"}}`, "#", string(rune(92))), connect.CodePermissionDenied},

		{"probing a server by address", "mcp/probe", `{"probe_id":"p","server":{"url":"https://example.com/mcp"}}`, 0},
		{"probing a command, which starts it", "mcp/probe", `{"probe_id":"p","server":{"command":"sh","args":["-c","id"]}}`, connect.CodePermissionDenied},
		{"probing a curated connector, which starts it", "mcp/probe", `{"probe_id":"p","server":{"connector":"github"}}`, connect.CodePermissionDenied},
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

// config/write takes what the console writes, and refuses whatever lets Her
// do more on the machine, at any key path, inside tables written at a parent
// path, in either layer: that needs the owner's root key.
func TestConfigWrite(t *testing.T) {
	write := func(edits ...string) string {
		return `{"layer":"user","edits":[` + strings.Join(edits, ",") + `]}`
	}
	no := connect.CodePermissionDenied
	for _, c := range []struct {
		name, params string
		code         connect.Code // 0: allowed
	}{
		{"the model", write(`{"key_path":"model","value":"openai/gpt-5"}`), 0},
		{"thinking", write(`{"key_path":"thinking","value":"high"}`), 0},
		{"asking first by default", write(`{"key_path":"permission_mode","value":"manual"}`), 0},
		{"accepting edits by default", write(`{"key_path":"permission_mode","value":"accept-edits"}`), 0},
		{"the default mode removed", write(`{"key_path":"permission_mode","value":null}`), 0},
		{"the old key, asking", write(`{"key_path":"approval_mode","value":"ask"}`), 0},
		{"instructions", write(`{"key_path":"instructions.extra","value":"Answer in French."}`), 0},
		{"web search", write(`{"key_path":"web_search.enabled","value":false}`, `{"key_path":"web_search.order","value":["brave"]}`), 0},
		{"web search, whole", write(`{"key_path":"web_search","value":{"enabled":true,"disabled":["exa"]}}`), 0},
		{"a site the browser may open", write(`{"key_path":"browser.sites.\"https://example.com\"","value":"navigate"}`), 0},
		{"the browser on, where it opens", write(`{"key_path":"browser.enabled","value":true}`, `{"key_path":"browser.default","value":"builtin"}`), 0},
		{"the browser's sites, upserted", write(`{"key_path":"browser","merge":"upsert","value":{"sites":{"https://example.com":"act"}}}`), 0},
		{"the owner's own Chrome, once its extension is added", write(`{"key_path":"browser.extension","value":true}`), 0},
		{"a connector by address", write(`{"key_path":"mcp_servers.gh","value":{"url":"https://example.com/mcp","transport":"streamable_http","headers":{"X-Team":"${credential:team}"},"auth":"oauth","enabled":true}}`), 0},
		{"a connector switched off", write(`{"key_path":"mcp_servers.gh.enabled","value":false}`), 0},
		{"a connector's header", write(`{"key_path":"mcp_servers.gh.headers.Authorization","value":"Bearer ${credential:gh}"}`), 0},
		{"a connector's tools", write(`{"key_path":"mcp_servers.gh","merge":"upsert","value":{"disabled_tools":["delete_repo"],"exposure":"direct"}}`), 0},
		{"a connector removed", write(`{"key_path":"mcp_servers.fs","value":null}`), 0},
		{"the connectors, whole", write(`{"key_path":"mcp_servers","value":{"gh":{"url":"https://x"}}}`), 0},
		{"in a project's layer", `{"layer":"user_project","cwd":"/p","edits":[{"key_path":"model","value":"mock/mock-1"}]}`, 0},

		{"bash's shell", write(`{"key_path":"tools.bash.shell","value":"/tmp/evil"}`), no},
		{"bash", write(`{"key_path":"tools.bash","value":{"pass_provider_keys":true}}`), no},
		{"the tools on", write(`{"key_path":"tools.enabled","value":["bash"]}`), no},
		{"the tools, upserted", write(`{"key_path":"tools","merge":"upsert","value":{"limits":{"max_parallel":64}}}`), no},
		{"a rule that allows", write(`{"key_path":"rules","value":[{"prefix":["rm"],"decision":"allow"}]}`), no},
		{"a rule that asks: rules are not the console's at all", write(`{"key_path":"rules","value":[{"prefix":["git","push"],"decision":"ask"}]}`), no},
		{"the rules removed", write(`{"key_path":"rules","value":null}`), no},
		{"writable folders", write(`{"key_path":"extra_writable_roots","value":["/"]}`), no},
		{"the system prompt", write(`{"key_path":"system_prompt_file","value":"/tmp/p.md"}`), no},
		{"the assistants' prompt", write(`{"key_path":"bot_prompt_file","value":"/tmp/p.md"}`), no},
		{"sub-agents", write(`{"key_path":"agents.enabled","value":true}`), no},
		{"sub-agents, whole", write(`{"key_path":"agents","value":{"enabled":true}}`), no},
		{"computer use", write(`{"key_path":"desktop.enabled","value":true}`), no},
		{"a project's .mcp.json", write(`{"key_path":"mcp.project_json","value":true}`), no},
		{"skills from anywhere", write(`{"key_path":"skills.paths","value":["/tmp/skills"]}`), no},
		{"instruction files", write(`{"key_path":"instructions.files","value":["/etc/passwd"]}`), no},
		{"a browser program", write(`{"key_path":"browser.executable","value":"/tmp/evil"}`), no},
		{"the browser replaced, dropping its limits", write(`{"key_path":"browser","value":{"sites":{}}}`), no},
		{"the browser removed", write(`{"key_path":"browser","value":null}`), no},
		{"a connector approved for good", write(`{"key_path":"mcp_servers.gh.approval","value":"never"}`), no},
		{"a connector approved for good, whole", write(`{"key_path":"mcp_servers.gh","value":{"url":"https://x","approval":"never"}}`), no},
		{"a connector approved for good, among all", write(`{"key_path":"mcp_servers","value":{"gh":{"url":"https://x","approval":"never"}}}`), no},
		{"a connector started as a command", write(`{"key_path":"mcp_servers.fs","value":{"command":"sh","args":["-c","id"]}}`), no},
		{"a command upserted", write(`{"key_path":"mcp_servers.fs","merge":"upsert","value":{"args":["--evil"]}}`), no},
		{"a command's key", write(`{"key_path":"mcp_servers.fs.command","value":"sh"}`), no},
		{"a command's environment", write(`{"key_path":"mcp_servers.fs.env.PATH","value":"/tmp"}`), no},
		{"a command behind quotes", write(`{"key_path":"\"mcp_servers\".\"fs\".command","value":"sh"}`), no},
		{"a curated connector", write(`{"key_path":"mcp_servers.gh.connector","value":"github"}`), no},
		{"a command after a harmless edit", write(`{"key_path":"model","value":"mock/mock-1"}`, `{"key_path":"mcp_servers.fs.cwd","value":"/"}`), no},
		{"full access by default", write(`{"key_path":"permission_mode","value":"bypass-permissions"}`), no},
		{"full access by default, old key", write(`{"key_path":"approval_mode","value":"full-auto"}`), no},
		{"full access in a project's layer", `{"layer":"user_project","cwd":"/p","edits":[{"key_path":"permission_mode","value":"bypass-permissions"}]}`, no},
		{"full access in a table of its own", write(`{"key_path":"bots.b1.permission_mode","value":"bypass-permissions"}`), no},
		{"full access inside a table written", write(`{"key_path":"projects.p","merge":"upsert","value":{"permission_mode":"bypass-permissions"}}`), no},
		{"full access in a list of tables", write(`{"key_path":"profiles","value":[{"name":"x","permission_mode":"bypass-permissions"}]}`), no},
		{"hooks", write(`{"key_path":"hooks.pre_tool","value":["/tmp/hook"]}`), no},
		{"a setting her has not yet", write(`{"key_path":"sandbox","value":"off"}`), no},
		{"a key path her refuses", write(`{"key_path":"mcp_servers..fs","value":1}`), connect.CodeInvalidArgument},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := check("config/write", []byte(c.params))
			if got := connect.CodeOf(err); err == nil && c.code != 0 || err != nil && got != c.code {
				t.Fatalf("check(config/write, %s) = %v, want code %v", c.params, err, c.code)
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
