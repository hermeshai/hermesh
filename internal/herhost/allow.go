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
		// The Her entry: assistants, their conversations, routines and files.
		"bots/subscribe", "bots/unsubscribe", "bots/open", "bots/read", "bots/unread", "bots/create", "bots/update",
		"bots/avatar", "bots/clear", "bots/delete", "bots/routine", "bots/routine_delete", "bots/routine_run",
		"bots/routine_run_now", "bots/routine_runs", "bots/routine_set_enabled", "bots/routine_stop",
		"session/op", "session/history", "session/detach", "models/list", "files/list", "files/read",
		// The console: models, connectors, skills, web search, the browser.
		"config/read", "config/write", "connections/list", "connections/discover", "connections/connect",
		"connections/disconnect", "connections/remove", "connections/retry", "connections/models",
		"credentials/set", "credentials/delete", "auth/set", "auth/delete", "chatgpt/status", "chatgpt/login",
		"chatgpt/complete", "chatgpt/logout", "chatgpt/cancel", "mcp/list", "mcp/check", "mcp/login",
		"mcp/login_complete", "mcp/login_cancel", "mcp/logout", "mcp/probe", "mcp/probe_cancel", "mcp/tools",
		"skills/list", "skills/read", "skills/files", "skills/install", "skills/remove", "skills/set_enabled",
		"web_search/status", "web_search/set_key", "web_search/test", "browser/allow", "browser/status",
		"models/refresh", "models/show",
	} {
		allowed[m] = true
	}
}

// safeModes are the permission modes short of full access, with the names
// her took before protocol version 2 ("full-auto" was full access).
var safeModes = map[string]bool{"manual": true, "accept-edits": true, "ask": true, "read-only": true, "auto-edit": true}

// stdioKeys are what an MCP server started as a command runs: the program,
// its arguments, environment and folder.
var stdioKeys = map[string]bool{"command": true, "args": true, "env": true, "env_vars": true, "cwd": true}

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
	case "session/op", "config/write", "mcp/probe", "mcp/login":
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

// refuse is why params, decoded, may not go: what only a root-signed command
// may do (docs/her-server.md §8).
func refuse(method string, params any) error {
	p, _ := params.(map[string]any)
	switch method {
	case "session/op":
		op, _ := p["op"].(map[string]any)
		if t := op["type"]; (t == "set_permission_mode" || t == "set_approval_mode") && fullAccess(op["mode"]) {
			return denied("only SetHerAssistantMode, signed by the owner's root key, gives an assistant full access")
		}
	case "config/write":
		edits, _ := p["edits"].([]any)
		for _, e := range edits {
			e, _ := e.(map[string]any)
			path, _ := e["key_path"].(string)
			keys, err := parseKeyPath(path)
			if err != nil {
				return connect.NewError(connect.CodeInvalidArgument, err)
			}
			switch {
			case keys[0] == "mcp_servers" && commandEdit(keys[1:], e["value"]):
				return denied("only SetHerCommandConnector, signed by the owner's root key, adds or changes a connector started as a command")
			case modeKey(keys[len(keys)-1]) && fullAccess(e["value"]) || setsFullAccess(e["value"]):
				return denied("only SetHerAssistantMode, signed by the owner's root key, gives an assistant full access")
			}
		}
	case "mcp/probe", "mcp/login":
		// Probing a server starts it.
		if runsCommand(p["server"]) {
			return denied("only SetHerCommandConnector, signed by the owner's root key, adds or changes a connector started as a command")
		}
	}
	return nil
}

// fullAccess: whether mode, a permission mode's name, is full access or one
// her may take as it (none: a mode left out).
func fullAccess(mode any) bool {
	s, ok := mode.(string)
	return ok && !safeModes[s]
}

// modeKey: whether a configuration key is a permission mode, at the top or
// in a table of its own (a project's, say); approval_mode is its old name.
func modeKey(key string) bool { return key == "permission_mode" || key == "approval_mode" }

// setsFullAccess: whether a value written, a table or a list of them, sets
// a permission mode in it to full access.
func setsFullAccess(v any) bool {
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			if modeKey(k) && fullAccess(x) || setsFullAccess(x) {
				return true
			}
		}
	case []any:
		for _, x := range v {
			if setsFullAccess(x) {
				return true
			}
		}
	}
	return false
}

// commandEdit: whether a config/write edit of mcp_servers (keys: the path
// below it) adds or changes a server started as a command. Removing a
// server is not that.
func commandEdit(keys []string, value any) bool {
	switch len(keys) {
	case 0:
		servers, _ := value.(map[string]any)
		for _, s := range servers {
			if runsCommand(s) {
				return true
			}
		}
		return false
	case 1:
		return runsCommand(value)
	}
	return stdioKeys[keys[1]]
}

func runsCommand(server any) bool {
	s, _ := server.(map[string]any)
	for k := range s {
		if stdioKeys[k] {
			return true
		}
	}
	return false
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
