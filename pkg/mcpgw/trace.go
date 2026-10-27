package mcpgw

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

// An opt-in log of what agent products send with their MCP calls, to learn
// what tells one caller from another (several Grok bots share a connection:
// docs/agent-messages.md §6.3). HERMESH_MCP_TRACE lists the connection,
// account or owner ids whose requests are logged. It never logs the token,
// cookies, client addresses or tool arguments.

// traceSkip are the headers a trace leaves out.
var traceSkip = map[string]bool{"authorization": true, "cookie": true, "cf-connecting-ip": true, "true-client-ip": true,
	"x-forwarded-for": true, "x-real-ip": true, "forwarded": true, "cf-ipcountry": true}

// rpcTrace is what a trace keeps of one JSON-RPC message.
type rpcTrace struct {
	Method string `json:"method,omitempty"`
	Params struct {
		Name            string          `json:"name,omitempty"`
		ProtocolVersion string          `json:"protocolVersion,omitempty"`
		ClientInfo      json.RawMessage `json:"clientInfo,omitempty"`
		Capabilities    json.RawMessage `json:"capabilities,omitempty"`
		Meta            json.RawMessage `json:"_meta,omitempty"`
		Arguments       json.RawMessage `json:"arguments,omitempty"`
	} `json:"params"`
}

// trace logs the MCP requests of the connections HERMESH_MCP_TRACE names.
func (g *Gateway) trace(next http.Handler) http.Handler {
	ids := map[string]bool{}
	for _, id := range strings.Split(os.Getenv("HERMESH_MCP_TRACE"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids[id] = true
		}
	}
	if len(ids) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := ""
		if info := auth.TokenInfoFromContext(r.Context()); info != nil {
			id, _ = info.Extra["connection"].(string)
		}
		c, err := g.db.GetConnection(r.Context(), id)
		if err != nil || c == nil || !(ids[c.ID] || ids[c.AccountID] || ids[c.OwnerID]) {
			next.ServeHTTP(w, r)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		r.Body = io.NopCloser(bytes.NewReader(body))
		headers := map[string]string{}
		for k, v := range r.Header {
			if !traceSkip[strings.ToLower(k)] {
				headers[k] = strings.Join(v, ", ")
			}
		}
		var msgs []rpcTrace
		if json.Unmarshal(body, &msgs) != nil {
			var one rpcTrace
			json.Unmarshal(body, &one)
			msgs = []rpcTrace{one}
		}
		var rpc []map[string]any
		for _, m := range msgs {
			e := map[string]any{"method": m.Method}
			if m.Params.Name != "" {
				e["tool"] = m.Params.Name
			}
			if m.Params.ClientInfo != nil {
				e["clientInfo"], e["protocolVersion"], e["capabilities"] = m.Params.ClientInfo, m.Params.ProtocolVersion, m.Params.Capabilities
			}
			if m.Params.Meta != nil {
				e["_meta"] = m.Params.Meta
			}
			if m.Params.Arguments != nil {
				var args map[string]json.RawMessage
				json.Unmarshal(m.Params.Arguments, &args)
				var names []string
				for k := range args {
					names = append(names, k)
				}
				e["argumentNames"] = names
			}
			rpc = append(rpc, e)
		}
		g.log.Info("mcp trace", "connection", c.ID, "client", c.ClientName, "http", r.Method, "headers", headers, "rpc", rpc)
		next.ServeHTTP(w, r)
	})
}
