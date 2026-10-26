package machine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	inboxv1 "github.com/hermeshai/hermesh/gen/go/hermesh/inbox/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// The sessions' own tools (docs/agent-messages.md §7.4). hermeshd listens
// on a Unix socket only its user can reach; each session's coding agent
// starts `hermeshd message-mcp` with a token naming its session, and writes
// to the owner's agents through it: hermeshd signs each message with the
// machine key, from the session, and records it in the conversation.

// sessionTools is the socket and the sessions' tokens.
type sessionTools struct {
	mu     sync.Mutex
	path   string
	tokens map[string]string // token → session id
	byID   map[string]string // session id → token
}

// toolsSocket is where the socket is: short enough for a Unix socket's path.
func (n *Machine) toolsSocket() string {
	p := filepath.Join(n.cfg.DataDir, "tools.sock")
	if len(p) > 100 {
		p = filepath.Join(os.TempDir(), "hermesh-"+strings.TrimPrefix(n.machineID, "m_")[:12]+".sock")
	}
	return p
}

// serveSessionTools listens until ctx ends.
func (n *Machine) serveSessionTools(ctx context.Context) {
	path := n.toolsSocket()
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		n.log.Warn("sessions' tools socket", "err", err)
		return
	}
	os.Chmod(path, 0o600)
	n.tools.mu.Lock()
	n.tools.path = path
	n.tools.mu.Unlock()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /whoami", n.toolHandler(n.toolWhoAmI))
	mux.HandleFunc("POST /contacts", n.toolHandler(n.toolContacts))
	mux.HandleFunc("POST /send", n.toolHandler(n.toolSend))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
		os.Remove(path)
	}()
	srv.Serve(ln)
}

// mcpServers are the tools a session's coding agent gets.
func (n *Machine) mcpServers(sessionID string) []provider.MCPServer {
	n.tools.mu.Lock()
	defer n.tools.mu.Unlock()
	if n.tools.path == "" {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	token := n.tools.byID[sessionID]
	if token == "" {
		b := make([]byte, 24)
		rand.Read(b)
		token = hex.EncodeToString(b)
		if n.tools.tokens == nil {
			n.tools.tokens, n.tools.byID = map[string]string{}, map[string]string{}
		}
		n.tools.tokens[token], n.tools.byID[sessionID] = sessionID, token
	}
	return []provider.MCPServer{{Name: "hermesh", Command: exe, Args: []string{"message-mcp"},
		Env: map[string]string{"HERMESH_TOOLS_SOCKET": n.tools.path, "HERMESH_TOOLS_TOKEN": token}}}
}

// ToolRequest is what `hermeshd message-mcp` asks of hermeshd.
type ToolRequest struct {
	Token   string `json:"token"`
	To      string `json:"to,omitempty"`
	Text    string `json:"text,omitempty"`
	Subject string `json:"subject,omitempty"`
	ReplyTo string `json:"reply_to,omitempty"`
}

// ToolContact is one of the owner's agents a session can write to.
type ToolContact struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Kind    string `json:"kind"`
	Takes   string `json:"takes_messages"`
}

// ToolWhoAmI is the session a token names.
type ToolWhoAmI struct {
	Session     string `json:"session"`
	Machine     string `json:"machine"`
	Requester   string `json:"requester"`
	RequesterID string `json:"requester_address"`
}

// ToolSent is a message a session wrote.
type ToolSent struct {
	MessageID string `json:"message_id"`
	To        string `json:"to"`
	Name      string `json:"name"`
}

func (n *Machine) toolHandler(f func(ctx context.Context, session string, req ToolRequest) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ToolRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		n.tools.mu.Lock()
		session := n.tools.tokens[req.Token]
		n.tools.mu.Unlock()
		if session == "" {
			http.Error(w, "unknown token", http.StatusForbidden)
			return
		}
		out, err := f(r.Context(), session, req)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

func (n *Machine) toolWhoAmI(ctx context.Context, session string, _ ToolRequest) (any, error) {
	r, err := n.st.Session(session)
	if err != nil || r == nil {
		return nil, errors.New("no such session")
	}
	out := ToolWhoAmI{Session: session, Machine: n.Name(), RequesterID: r.CreatedBy, Requester: n.knownClients()[r.CreatedBy].Label}
	return out, nil
}

// agentContacts are the owner's agents, checked.
func (n *Machine) agentContacts(ctx context.Context) ([]*identity.ContactInfo, error) {
	n.mu.Lock()
	conn := n.conn
	n.mu.Unlock()
	if conn == nil {
		return nil, errors.New("this machine is not connected: messages wait until it is")
	}
	ms, err := n.messageService(conn)
	if err != nil {
		return nil, err
	}
	res, err := ms.Contacts(ctx, &relayv1.ContactsRequest{})
	if err != nil {
		return nil, err
	}
	n.contacts.mu.Lock()
	n.contacts.list, n.contacts.at = res.Contacts, time.Now()
	n.contacts.mu.Unlock()
	var out []*identity.ContactInfo
	for _, c := range res.Contacts {
		if info, err := n.trust.VerifyContact(c); err == nil && info.Kind != "machine" {
			out = append(out, info)
		}
	}
	return out, nil
}

func (n *Machine) toolContacts(ctx context.Context, _ string, _ ToolRequest) (any, error) {
	list, err := n.agentContacts(ctx)
	if err != nil {
		return nil, err
	}
	out := []ToolContact{}
	for _, c := range list {
		out = append(out, ToolContact{Name: c.Name, Address: c.Inbox, Kind: c.Kind, Takes: c.TakesMessages})
	}
	return out, nil
}

// toolSend writes one of the owner's agents a message from the session.
func (n *Machine) toolSend(ctx context.Context, session string, req ToolRequest) (any, error) {
	if strings.TrimSpace(req.Text) == "" {
		return nil, errors.New("the message is empty")
	}
	to := strings.TrimSpace(req.To)
	if to == "" { // the agent that started the session, in its conversation
		r, err := n.st.Session(session)
		if err != nil || r == nil {
			return nil, errors.New("no such session")
		}
		to = r.CreatedBy
		if r.Conversation != "" {
			to += "/" + r.Conversation
		}
	}
	inbox, conv, _ := strings.Cut(to, "/")
	if strings.HasPrefix(inbox, "m_") || strings.HasPrefix(inbox, "s_") {
		return nil, errors.New("sessions write to agents, not to other sessions")
	}
	list, err := n.agentContacts(ctx)
	if err != nil {
		return nil, err
	}
	var found []*identity.ContactInfo
	for _, c := range list {
		if c.Inbox == inbox || (!strings.HasPrefix(to, "c_") && c.Name == to) {
			found = append(found, c)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no agent %q among the owner's: see message_contacts", to)
	case 1:
	default:
		return nil, fmt.Errorf("%d agents are called %q: give an address from message_contacts", len(found), to)
	}
	c := found[0]
	if !strings.HasPrefix(to, "c_") {
		conv = ""
	}
	m := &inboxv1.Message{MessageId: "msg_" + strings.ToLower(ulid.Make().String()), From: &inboxv1.Address{Inbox: n.machineID, Session: session},
		To: &inboxv1.Address{Inbox: c.Inbox, Session: conv}, SentAt: timestamppb.Now(), InReplyTo: req.ReplyTo, Subject: req.Subject, Text: req.Text}
	if err := n.sendMessage(m); err != nil {
		return nil, err
	}
	n.sessions.record(session, &sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_MessageSent{MessageSent: &sessionv1.MessageSent{
		MessageId: m.MessageId, To: inboxAddress(m.To), ToName: c.Name, Text: m.Text, InReplyTo: m.InReplyTo}}})
	return ToolSent{MessageID: m.MessageId, To: inboxAddress(m.To), Name: c.Name}, nil
}
