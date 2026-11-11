package herhost

import (
	"bytes"
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"
)

// allowed are the methods of her's protocol that the owner's devices may
// call, on HerService and on the console's routes alike (docs/her-server.md
// §6.1). Anything else is refused, methods unknown here included.
var allowed = map[string]bool{}

func init() {
	for _, m := range []string{
		// The Her entry: assistants, their conversations, memory, routines and files.
		"bots/subscribe", "bots/unsubscribe", "bots/open", "bots/read", "bots/unread", "bots/create", "bots/update",
		"bots/avatar", "bots/clear", "bots/delete", "bots/memory", "bots/memory_clear", "bots/routine", "bots/routine_delete", "bots/routine_run",
		"bots/routine_run_now", "bots/routine_runs", "bots/routine_set_enabled", "bots/routine_stop",
		"session/op", "session/history", "session/detach", "models/list", "files/list", "files/read",
		// The owner's answer to a secure card (docs/her-secure-cards.md §3.3).
		"cards/answer",
		// Whether computer use can work there, with macOS's permissions as her finds them.
		"desktop/status",
		// The console: models, connectors, skills, web search, the browser.
		"config/read", "config/write", "connections/list", "connections/discover", "connections/connect",
		"connections/disconnect", "connections/remove", "connections/retry", "connections/models",
		"credentials/set", "credentials/delete", "auth/set", "auth/delete", "chatgpt/status", "chatgpt/login",
		"chatgpt/complete", "chatgpt/logout", "chatgpt/cancel", "mcp/list", "mcp/check", "mcp/login",
		"mcp/login_complete", "mcp/login_cancel", "mcp/logout", "mcp/probe", "mcp/probe_cancel", "mcp/tools",
		"skills/list", "skills/read", "skills/files", "skills/install", "skills/remove", "skills/set_enabled",
		"web_search/status", "web_search/set_key", "web_search/test", "browser/allow", "browser/status",
		"browser/use_extension", "models/refresh", "models/show",
	} {
		allowed[m] = true
	}
}

// commandKeys are what an MCP server started as a command runs: the program,
// its arguments, environment and folder, or one of her's curated connectors.
var commandKeys = map[string]bool{"command": true, "args": true, "env": true, "env_vars": true, "cwd": true, "connector": true}

// writable is what config/write may change, as her's configuration has it
// (her-core config.rs, ConfigFile). It is a list of what may, not of what
// may not: most of her's settings decide what runs and how freely (tools.*,
// rules, extra_writable_roots, the prompt files, agents, desktop, mcp,
// skills.paths, a browser's executable, a connector's command or approval),
// and a setting her adds later must not slip through. A key maps to what may
// be written in it, nil to anything; "*" stands for any key.
var writable = node{
	"web_search": nil,
	// browser.extension: whether Her uses the owner's own Chrome, which only
	// connects once the owner has added the Hermesh extension to it by hand
	// on that Mac; that step is the decision, so no root key is asked.
	"browser": {"enabled": nil, "default": nil, "sites": nil, "extension": nil},
	// Connectors by address.
	"mcp_servers": {"*": {"name": nil, "url": nil, "transport": nil, "headers": nil, "bearer_token_env_var": nil, "auth": nil, "oauth": nil,
		"enabled": nil, "exposure": nil, "description": nil, "direct_tools": nil, "enabled_tools": nil, "disabled_tools": nil,
		"startup_timeout_secs": nil, "tool_timeout_secs": nil, "parallel_tools": nil, "required": nil}},
}

type node map[string]node

// check is why an owner's device may not send her this request, and else
// the params to send: for a request whose params are looked into, what was
// looked at (decoded once and encoded again, so her reads what was checked).
func check(method string, params []byte) (json.RawMessage, error) {
	if !allowed[method] {
		return nil, denied("her's %q is not open to the owner's devices", method)
	}
	if len(params) == 0 {
		return nil, nil
	}
	if !json.Valid(params) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("the params of %s are not JSON", method))
	}
	switch method {
	case "session/op", "config/write", "mcp/probe", "mcp/login", "cards/answer":
	default:
		return params, nil
	}
	d := json.NewDecoder(bytes.NewReader(params))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := refuse(method, v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// refuse is why params, decoded, may not go: what lets Her do more on the
// machine needs the owner's root key (docs/her-server.md §8).
func refuse(method string, params any) error {
	p, _ := params.(map[string]any)
	switch method {
	case "config/write":
		edits, _ := p["edits"].([]any)
		for _, e := range edits {
			e, _ := e.(map[string]any)
			path, _ := e["key_path"].(string)
			keys, err := parseKeyPath(path)
			if err != nil {
				return connect.NewError(connect.CodeInvalidArgument, err)
			}
			if !writes(keys, e["value"], e["merge"] == "upsert") {
				return denied("the owner's devices may not change %s: settings that let Her do more on this machine need the owner's root key", path)
			}
		}
	case "mcp/probe", "mcp/login":
		// Probing a server starts it.
		if runsCommand(p["server"]) {
			return denied("only SetHerCommandConnector, signed by the owner's root key, adds or changes a connector started as a command")
		}
	case "cards/answer":
		// What a card does on approve is console calls, each checked as the
		// console's are: a card never changes more than the console could.
		answer, _ := p["answer"].(map[string]any)
		ops, _ := answer["ops"].([]any)
		for _, op := range ops {
			op, _ := op.(map[string]any)
			m, _ := op["method"].(string)
			if m == "cards/answer" {
				return denied("a card's op can't answer a card")
			}
			params, _ := json.Marshal(op["params"])
			if _, err := check(m, params); err != nil {
				return err
			}
		}
	}
	return nil
}

func runsCommand(server any) bool {
	s, _ := server.(map[string]any)
	for k := range s {
		if commandKeys[k] {
			return true
		}
	}
	return false
}

// writes: whether a config/write edit setting keys to value changes only
// what writable allows. A table is written key by key; it may be replaced
// whole, which drops what it held, or removed, only where that is harmless:
// connectors.
func writes(keys []string, value any, upsert bool) bool {
	n := writable
	for _, k := range keys {
		next, ok := n[k]
		if !ok {
			next, ok = n["*"]
		}
		if !ok {
			return false
		}
		if next == nil {
			return true
		}
		n = next
	}
	connectors := keys[0] == "mcp_servers"
	t, ok := value.(map[string]any)
	switch {
	case value == nil:
		return connectors
	case !ok || !upsert && !connectors:
		return false
	}
	for k, v := range t {
		if !writes(append(keys[:len(keys):len(keys)], k), v, true) {
			return false
		}
	}
	return true
}

// parseKeyPath splits a config key path as her does (her-core config_edit):
// `a.b."c.d"` is a, b, c.d.
func parseKeyPath(path string) ([]string, error) {
	var keys []string
	for i := 0; ; {
		var key []byte
		if i < len(path) && path[i] == '"' {
			for i++; ; i++ {
				if i >= len(path) {
					return nil, fmt.Errorf("unfinished quote in key path %q", path)
				}
				if path[i] == '"' {
					i++
					break
				}
				if path[i] == '\\' {
					if i++; i >= len(path) {
						return nil, fmt.Errorf("unfinished quote in key path %q", path)
					}
				}
				key = append(key, path[i])
			}
		} else {
			for ; i < len(path) && path[i] != '.'; i++ {
				c := path[i]
				if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '-') {
					return nil, fmt.Errorf("key path %q: quote the key containing %q", path, c)
				}
				key = append(key, c)
			}
			if len(key) == 0 {
				return nil, fmt.Errorf("empty key in key path %q", path)
			}
		}
		keys = append(keys, string(key))
		if i == len(path) {
			return keys, nil
		}
		if path[i] != '.' {
			return nil, fmt.Errorf("key path %q: unexpected %q", path, path[i])
		}
		i++
	}
}

func denied(format string, a ...any) error {
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf(format, a...))
}
