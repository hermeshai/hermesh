package owner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/her/v1/herv1connect"
	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/peer"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// Her (docs/her-server.md): the owner's side of Her on its machines. Every
// call goes to the machine's HerService: this device's own in process,
// another through its tunnel. Turning Her on and connectors started as
// commands are admin commands signed by the root key (§8); the rest goes
// with this device's own key, an assistant's full access included.

// herMachine is HerService on one machine: *machine.Local for this
// device's own, herRemote for another.
type herMachine interface {
	HerOpen(ctx context.Context, req *herv1.OpenRequest, send func(*herv1.OpenEvent) error) error
	HerCall(ctx context.Context, req *herv1.CallRequest) (*herv1.CallResponse, error)
	HerUpload(ctx context.Context, start *herv1.UploadStart, r io.Reader) (*herv1.UploadResponse, error)
	HerDownload(ctx context.Context, req *herv1.DownloadRequest, send func(*herv1.DownloadResponse) error) error
	HerConsole() http.Handler
}

const (
	// herChunk: the size of the pieces a file is uploaded in.
	herChunk = 64 << 10
	// herRetry: how often the messages waiting here are tried again,
	// besides when the replica follows a machine again.
	herRetry = 30 * time.Second
)

// herAnswerWithin: how long reaching a machine, or opening an assistant's
// conversation there and handing it a message, may take before the message
// waits here instead (a variable for the tests).
var herAnswerWithin = 20 * time.Second

// herQuiet: the event classes a connection that only hands an assistant
// something leaves out.
var herQuiet = []string{"text_delta", "thinking_delta", "tool_call_delta", "tool_progress"}

// herOn reaches HerService on a machine: this device's own in process (a
// computer that is only a machine reaches no other), another through its
// tunnel.
func (o *Owner) herOn(ctx context.Context, machineID string) (herMachine, error) {
	if machineID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no machine"))
	}
	if o.herReach != nil {
		return o.herReach(ctx, machineID)
	}
	if m, ok, err := o.onlyMachine(); ok {
		if err == nil {
			err = onlyThisMachine(m, machineID)
		}
		if err != nil {
			return nil, err
		}
		return m, nil
	}
	if l := o.local(); l != nil && l.MachineID() == machineID {
		return l, nil
	}
	c, err := o.relay(ctx)
	if err != nil {
		return nil, err
	}
	n, err := c.Machine(ctx, machineID)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return herRemote{her: n.Her, http: n.HTTP(), forget: func() { c.ForgetMachine(n) }}, nil
}

// herRemote is HerService on another machine, through its tunnel (its
// HTTP/2 client); forget drops the tunnel.
type herRemote struct {
	her    herv1connect.HerServiceClient
	http   *http.Client
	forget func()
}

// tunnelError: a call that failed on the way, not at the machine.
func tunnelError(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeUnknown:
		return true
	}
	return false
}

// failed forgets the tunnel after an error on the way (not one the machine
// answered, nor its caller leaving), so that the next call dials again.
func (r herRemote) failed(ctx context.Context, err error) error {
	if err != nil && ctx.Err() == nil && tunnelError(err) {
		r.forget()
	}
	return err
}

func (r herRemote) HerOpen(ctx context.Context, req *herv1.OpenRequest, send func(*herv1.OpenEvent) error) error {
	stream, err := r.her.Open(ctx, req)
	if err != nil {
		return r.failed(ctx, err)
	}
	defer stream.Close()
	for stream.Receive() {
		if err := send(stream.Msg()); err != nil {
			return err
		}
	}
	return r.failed(ctx, stream.Err())
}

func (r herRemote) HerCall(ctx context.Context, req *herv1.CallRequest) (*herv1.CallResponse, error) {
	res, err := r.her.Call(ctx, req)
	return res, r.failed(ctx, err)
}

func (r herRemote) HerUpload(ctx context.Context, start *herv1.UploadStart, src io.Reader) (*herv1.UploadResponse, error) {
	sctx, cancel := context.WithCancel(ctx) // ends the upload when reading here fails
	defer cancel()
	stream, err := r.her.Upload(sctx)
	if err != nil {
		return nil, r.failed(ctx, err)
	}
	part := &herv1.UploadRequest{Part: &herv1.UploadRequest_Start{Start: start}}
	buf := make([]byte, herChunk)
	for stream.Send(part) == nil { // else the machine ended the upload: CloseAndReceive says why
		n, err := io.ReadFull(src, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return nil, err
		}
		if n == 0 {
			break
		}
		part = &herv1.UploadRequest{Part: &herv1.UploadRequest_Data{Data: buf[:n]}}
	}
	res, err := stream.CloseAndReceive()
	return res, r.failed(ctx, err)
}

func (r herRemote) HerDownload(ctx context.Context, req *herv1.DownloadRequest, send func(*herv1.DownloadResponse) error) error {
	stream, err := r.her.Download(ctx, req)
	if err != nil {
		return r.failed(ctx, err)
	}
	defer stream.Close()
	for stream.Receive() {
		if err := send(stream.Msg()); err != nil {
			return err
		}
	}
	return r.failed(ctx, stream.Err())
}

// HerConsole passes requests for /her/console/… on to the machine, through
// its tunnel, flushing as the answer comes (server-sent events).
func (r herRemote) HerConsole() http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host, pr.Out.Host = "https", "machine", "machine"
		},
		Transport:     r.http.Transport,
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0), // an event stream ends when its window closes
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			if req.Context().Err() == nil {
				r.forget()
			}
			http.Error(w, err.Error(), http.StatusBadGateway)
		},
	}
}

// ---------------------------------------------------------------------------
// Signed: turning Her on, connectors started as commands.

// errNoRootKey refuses, on a computer that is only a machine, what only the
// owner key of a trusted device signs.
func errNoRootKey(what string) error {
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("a trusted device has to %s: this computer is only a machine, without an owner key", what))
}

// SetHer turns Her on or off for a machine, signed by the root key. Turned
// on, she also becomes one of the owner's agents (docs/her-mesh.md §3.3):
// the same confirmation signs her grant, for the client key the machine
// keeps for her. Turned on again for a Her that has none yet, it signs the
// grant. Turned off, her grant is revoked, and with erase her data on the
// machine is deleted, and the messages waiting here for her
// (docs/her-server.md §5.6).
func (o *Owner) SetHer(ctx context.Context, machineID string, enabled, erase bool) error {
	if _, ok, _ := o.onlyMachine(); ok {
		return errNoRootKey("turn Her on or off")
	}
	if enabled && erase {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("her data is erased only with Her turned off"))
	}
	cmd := &identityv1.AdminCommand{OwnerId: o.ownerID, CommandId: identity.RandomCode(16), TargetMachineId: machineID,
		ExpiresAt: timestamppb.New(time.Now().Add(5 * time.Minute)),
		Command:   &identityv1.AdminCommand_SetHer{SetHer: &identityv1.SetHer{Enabled: enabled, Erase: erase}}}
	msgs := []proto.Message{cmd}
	var grant *identityv1.Grant
	if enabled {
		if grant = o.herGrant(ctx, machineID); grant != nil {
			msgs = append(msgs, grant)
		}
	}
	signed, err := o.signRoot(ctx, msgs...)
	if err != nil {
		return err
	}
	var local *machine.Local
	var remote *peer.Machine
	if l := o.local(); l != nil && l.MachineID() == machineID && grant != nil {
		local = l
	}
	c, err := o.relay(ctx)
	if err != nil {
		return err
	}
	if remote, err = c.Machine(ctx, machineID); err != nil {
		return connect.NewError(connect.CodeUnavailable, err)
	}
	res, err := remote.Admin.ApplyAdminCommand(ctx, &machinev1.ApplyAdminCommandRequest{Command: signed[0]})
	if err != nil {
		c.ForgetMachine(remote)
		return err
	}
	if !enabled {
		return o.herTurnedOff(ctx, machineID, erase, res.GetMessage())
	}
	if grant == nil {
		return nil
	}
	// Her is on from here; what follows only gives her the mesh, and the
	// owner can ask for it again (turning Her on once more).
	if local != nil {
		err = local.SetHerGrant(signed[1], o.trust.RootChain())
	} else {
		_, err = remote.Admin.SetHerGrant(ctx, &machinev1.SetHerGrantRequest{Grant: signed[1], RootChain: o.trust.RootChain()})
	}
	if err != nil {
		o.log.Warn("Her is on without the mesh: her machine did not take her grant", "machine", machineID, "err", err)
		return nil
	}
	ctl, _, err := o.control(ctx)
	if err == nil {
		_, err = ctl.PublishGrant(ctx, &relayv1.PublishGrantRequest{Grant: signed[1]})
	}
	if err != nil {
		o.log.Warn("Her's grant not published yet", "machine", machineID, "err", err)
		return nil
	}
	o.st.putGrant(signed[1], false)
	go o.refreshCards()
	return nil
}

// herTurnedOff follows Her turned off on a machine, which answered msg: her
// grants are revoked, and with erase, what waits here for her goes once her
// data is erased there, or the error says why it stays.
func (o *Owner) herTurnedOff(ctx context.Context, machineID string, erase bool, msg string) error {
	o.revokeHer(ctx, machineID)
	if !erase {
		return nil
	}
	switch msg {
	case machine.HerErased:
		o.dropHerWaiting(machineID)
		return nil
	case machine.HerOff:
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("Her is off, but her data stays: this machine's Hermesh is too old to erase it. Update it, then turn Her on and off again"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New(msg))
	}
}

// herGrant is the grant to sign for Her on a machine: nil when its hermeshd
// keeps no key for her (it is older, or can't run her) or she holds one.
func (o *Owner) herGrant(ctx context.Context, machineID string) *identityv1.Grant {
	var st *machinev1.MachineStatus
	if l := o.local(); l != nil && l.MachineID() == machineID {
		st = l.Info().Status
	} else if n, err := o.machineClient(ctx, machineID); err == nil {
		if info, err := n.Info.GetInfo(ctx, &machinev1.GetInfoRequest{}); err == nil {
			st = info.Status
		}
	}
	her := st.GetHer()
	if len(her.GetClientPublicKey()) == 0 || her.GetMesh() {
		return nil
	}
	name := st.GetName()
	return HerGrant(o.ownerID, machineID, name, her.ClientPublicKey)
}

// HerGrant is Her's grant on a machine (docs/her-mesh.md §3.2): every
// machine, project and agent, every session to read, and no expiry. It ends
// when Her is turned off there or the machine is removed.
func HerGrant(ownerID, machineID, machineName string, clientKey []byte) *identityv1.Grant {
	label := "Her"
	if machineName != "" {
		label = "Her on " + machineName
	}
	return &identityv1.Grant{OwnerId: ownerID, GrantId: "g_" + strings.ToLower(ulid.Make().String()), Version: 1,
		ClientId: identity.ClientID(clientKey), ClientPublicKey: clientKey, ClientLabel: label,
		MachineIds: []string{"*"}, ProjectIds: []string{"*"}, Agents: []string{"*"},
		TaskVisibility: identityv1.TaskVisibility_TASK_VISIBILITY_ALL,
		NotBefore:      timestamppb.New(time.Now().Add(-time.Minute)), ExpiresAt: timestamppb.New(identity.NoExpiry),
		HerMachineId: machineID}
}

// herGrants are the ids of the grants Her holds on a machine.
func (o *Owner) herGrants(ctx context.Context, machineID string) []string {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return nil
	}
	res, err := ctl.ListClients(ctx, &relayv1.ListClientsRequest{})
	if err != nil {
		return nil
	}
	var ids []string
	for _, c := range res.Clients {
		g := &identityv1.Grant{}
		if identity.Unverified(c.Grant, g) == nil && g.OwnerId == o.ownerID && g.HerMachineId == machineID && !c.Revoked && !slices.Contains(ids, g.GrantId) {
			ids = append(ids, g.GrantId)
		}
	}
	return ids
}

// revokeHer revokes the grants of a machine's Her: she was turned off.
func (o *Owner) revokeHer(ctx context.Context, machineID string) {
	for _, id := range o.herGrants(ctx, machineID) {
		if _, err := o.RevokeClient(ctx, id); err != nil {
			o.log.Warn("revoking Her's grant", "machine", machineID, "grant", id, "err", err)
		}
	}
}

// SetHerCommandConnector adds, changes or removes a connector her starts as
// a command on a machine, signed by the root key.
func (o *Owner) SetHerCommandConnector(ctx context.Context, machineID string, c *identityv1.SetHerCommandConnector) error {
	switch {
	case c.GetName() == "":
		return connect.NewError(connect.CodeInvalidArgument, errors.New("a connector needs a name"))
	case !c.Remove && c.Command == "":
		return connect.NewError(connect.CodeInvalidArgument, errors.New("a connector needs a command"))
	}
	if _, ok, _ := o.onlyMachine(); ok {
		return errNoRootKey("add or remove connectors started as commands")
	}
	_, err := o.admin(ctx, machineID, func(cmd *identityv1.AdminCommand) {
		cmd.Command = &identityv1.AdminCommand_SetHerCommandConnector{SetHerCommandConnector: c}
	})
	return err
}

// SetHerAssistantMode sets how freely an assistant acts, on its
// conversation, as the apps do: full access too, as the owner chose it.
func (o *Owner) SetHerAssistantMode(ctx context.Context, machineID, botID, mode string) error {
	if botID == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("no assistant"))
	}
	switch mode {
	case "manual", "accept-edits", "bypass-permissions":
	default:
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown permission mode %q", mode))
	}
	h, err := o.herOn(ctx, machineID)
	if err != nil {
		return err
	}
	return onBot(ctx, h, botID, func(conn, session string) error {
		return herOp(ctx, h, conn, session, map[string]any{"type": "set_permission_mode", "mode": mode})
	})
}

// ---------------------------------------------------------------------------
// Her's protocol, passed on.

// HerOpen opens a her connection on a machine and passes on what it sends,
// keeping what it says of Her here (her_conversation.go).
func (o *Owner) HerOpen(ctx context.Context, machineID string, req *herv1.OpenRequest, send func(*herv1.OpenEvent) error) error {
	h, err := o.herOn(ctx, machineID)
	if err != nil {
		return err
	}
	if req == nil {
		req = &herv1.OpenRequest{}
	}
	f := &herFollower{o: o, machine: machineID, setup: req.Setup}
	return h.HerOpen(ctx, req, func(e *herv1.OpenEvent) error {
		if err := send(e); err != nil {
			return err
		}
		f.see(e)
		return nil
	})
}

// HerCall sends one her request to a machine.
func (o *Owner) HerCall(ctx context.Context, machineID string, req *herv1.CallRequest) (*herv1.CallResponse, error) {
	if req.GetRequest().GetMethod() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no request"))
	}
	h, err := o.herOn(ctx, machineID)
	if err != nil {
		return nil, err
	}
	res, err := h.HerCall(ctx, req)
	if err == nil {
		o.keepDeliveries(machineID, req.GetRequest(), res.GetResponse())
	}
	return res, err
}

// herResult is what a her request returned, or her's error.
func herResult(r *herv1.Response) ([]byte, error) {
	e := r.GetError()
	switch {
	case e == nil:
		return r.GetResult(), nil
	case e.Code == -32040: // bot_not_found
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("her: %s", e.Message))
	}
	return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("her: %s", e.Message))
}

// herOp sends op on the conversation session, which the connection conn
// opened.
func herOp(ctx context.Context, h herMachine, conn, session string, op any) error {
	params, err := json.Marshal(map[string]any{"session_id": session, "op": op})
	if err != nil {
		return err
	}
	res, err := h.HerCall(ctx, &herv1.CallRequest{ConnectionId: conn, Request: &herv1.Request{Method: "session/op", Params: params}})
	if err != nil {
		return err
	}
	_, err = herResult(res.GetResponse())
	return err
}

// onBot opens a her connection on h with the assistant botID's
// conversation (bots/open, without its history or the events of herQuiet)
// and runs fn with the connection and the conversation; the connection
// closes after.
func onBot(ctx context.Context, h herMachine, botID string, fn func(conn, session string) error) error {
	params, err := json.Marshal(map[string]any{"bot_id": botID, "events": map[string]any{"exclude": herQuiet}, "history_limit": 0})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	opened, ended := make(chan *herv1.Opened, 1), make(chan error, 1)
	go func() {
		ended <- h.HerOpen(ctx, &herv1.OpenRequest{Setup: []*herv1.Request{{Method: "bots/open", Params: params}}}, func(e *herv1.OpenEvent) error {
			if op := e.GetOpened(); op != nil {
				select {
				case opened <- op:
				default:
				}
			}
			return nil
		})
	}()
	var op *herv1.Opened
	select {
	case op = <-opened:
	case err := <-ended:
		select {
		case op = <-opened:
		default:
			if err == nil {
				err = connect.NewError(connect.CodeUnavailable, errors.New("her closed the connection before it opened"))
			}
			return err
		}
	}
	if len(op.Responses) != 1 {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("bots/open came back with %d answers", len(op.Responses)))
	}
	result, err := herResult(op.Responses[0])
	if err != nil {
		return err
	}
	var conv struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(result, &conv) != nil || conv.SessionID == "" {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("bots/open came back without a conversation: %s", result))
	}
	return fn(op.ConnectionId, conv.SessionID)
}

// ---------------------------------------------------------------------------
// Messages, delivered now or kept here until the machine answers (§5.5).

// unreachable is a delivery that failed on the way to the machine, not at
// it: the message waits.
type unreachable struct{ error }

func (e unreachable) Unwrap() error { return e.error }

// onTheWay marks err unreachable when it happened on the way to the machine.
func onTheWay(ctx context.Context, err error) error {
	if err != nil && ctx.Err() == nil && tunnelError(err) {
		return unreachable{err}
	}
	return err
}

// HerSend sends an assistant a message (docs/her-sending.md §4): it is kept
// here first, under the id the app gave it, then delivered with the
// assistant's earlier waiting messages, in order. It answers delivered, or
// waiting with why; an error means the message is not kept and was not
// taken (a wrong request, or her refused it).
func (o *Owner) HerSend(ctx context.Context, req *localv1.HerSendRequest) (*localv1.HerSendResponse, error) {
	switch {
	case req.MachineId == "" || req.BotId == "":
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a message goes to a machine's assistant"))
	case req.Text == "" && len(req.LocalPaths) == 0 && len(req.Pictures) == 0:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("an empty message"))
	case req.Id != "" && !herInputID(req.Id):
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q can't be a message's id: 1 to 64 of A-Z a-z 0-9 _ -", req.Id))
	}
	for _, p := range req.LocalPaths {
		f, _, err := openFile(p)
		if err != nil {
			return nil, err
		}
		f.Close()
	}
	id := req.Id
	if id == "" {
		id = "hw_" + strings.ToLower(ulid.Make().String())
	}
	bot := o.herBotLock(req.MachineId, req.BotId)
	bot.Lock()
	defer bot.Unlock()
	w, err := o.herKeptOne(req.MachineId, req.BotId, id)
	if err != nil {
		return nil, err
	}
	fresh := w == nil
	if fresh { // else a send tried again: the one kept goes
		w = &localv1.HerWaiting{Id: id, MachineId: req.MachineId, BotId: req.BotId, Text: req.Text,
			LocalPaths: slices.Clone(req.LocalPaths), CreatedAt: timestamppb.Now(), Reply: req.Reply}
		if err := o.keepPictures(w, req.Pictures); err != nil {
			return nil, err
		}
		if err := o.st.addHerWaiting(w); err != nil {
			o.dropPictures(w.Id)
			return nil, err
		}
	}
	on := herConv{conn: req.ConnectionId, session: req.SessionId}
	if refused, _ := o.flushBot(ctx, req.MachineId, req.BotId, w.Id, fresh, on); refused != nil {
		return nil, refused
	}
	if w, err = o.herKeptOne(req.MachineId, req.BotId, id); err != nil || w == nil {
		return &localv1.HerSendResponse{Delivered: err == nil}, err
	}
	return &localv1.HerSendResponse{Waiting: w}, nil
}

// herInputID: what her takes as a prompt's id (her-protocol's valid_input_id).
func herInputID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// herKeptOne is the message id waiting for an assistant, or nil.
func (o *Owner) herKeptOne(machineID, botID, id string) (*localv1.HerWaiting, error) {
	list, err := o.st.herWaiting(machineID, botID)
	for _, w := range list {
		if w.Id == id {
			return w, err
		}
	}
	return nil, err
}

// herBotLock is an assistant's delivery lock: HerSend and flushWaiting
// deliver its messages one at a time, so that none goes twice at once and
// they arrive in the order they were written.
func (o *Owner) herBotLock(machineID, botID string) *sync.Mutex {
	mu, _ := o.herBots.LoadOrStore(machineID+"/"+botID, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// herConv is a conversation an app has open on the machine, to deliver on.
type herConv struct{ conn, session string }

// flushBot delivers an assistant's waiting messages, oldest first, with its
// lock held, and stops at the first that does not go: kept with why, it
// holds back the later ones. down says the machine did not answer. The
// message mine (HerSend's) is marked offline when nothing was tried; when
// it is fresh (kept by this send), her's refusal drops it and is returned.
func (o *Owner) flushBot(ctx context.Context, machineID, botID, mine string, fresh bool, on herConv) (refused error, down bool) {
	list, err := o.st.herWaiting(machineID, botID)
	if err != nil {
		o.log.Warn("Her's waiting messages not read", "err", err)
		return nil, false
	}
	if o.rep.offline(machineID) {
		for _, w := range list {
			if w.Id == mine && w.Error == "" {
				w.Error = "offline"
				o.keepWaiting(w)
			}
		}
		return nil, true
	}
	for _, w := range list {
		err := o.herDeliver(ctx, w, on)
		if ctx.Err() != nil {
			return nil, false // asked again later, by its id
		}
		if err == nil {
			if _, err := o.st.deleteHerWaiting(machineID, w.Id); err != nil {
				o.log.Warn("a message delivered to Her still kept", "id", w.Id, "err", err)
			}
			o.dropPictures(w.Id)
			continue
		}
		lost := errors.As(err, new(unreachable))
		if w.Id == mine && fresh && !lost {
			if _, err := o.st.deleteHerWaiting(machineID, w.Id); err != nil {
				o.log.Warn("a message her refused still kept", "id", w.Id, "err", err)
			}
			o.dropPictures(w.Id)
			return err, false
		}
		w.Error = err.Error()
		o.keepWaiting(w)
		return nil, lost
	}
	return nil, false
}

// keepWaiting writes down why a waiting message waits.
func (o *Owner) keepWaiting(w *localv1.HerWaiting) {
	if err := o.st.updateHerWaiting(w); err != nil {
		o.log.Warn("Her's waiting message not kept", "id", w.Id, "err", err)
	}
}

// herDeliver delivers w as one prompt under its id (docs/her-sending.md
// §4.4): its files into the assistant's folder first, named after its
// words, and its pictures as pictures; on the conversation the app has open
// (on), or else on a connection of its own.
func (o *Owner) herDeliver(ctx context.Context, w *localv1.HerWaiting, on herConv) error {
	rctx, cancel := context.WithTimeout(ctx, herAnswerWithin)
	h, err := o.herOn(rctx, w.MachineId)
	cancel()
	if err != nil {
		if ctx.Err() == nil && (tunnelError(err) || errors.Is(err, context.DeadlineExceeded)) {
			return unreachable{err}
		}
		return err
	}
	input := []any{}
	if text := strings.TrimSpace(w.Text); text != "" {
		input = append(input, map[string]any{"type": "text", "text": text})
	}
	if block := replyBlock(w.Reply); block != "" {
		input = append(input, map[string]any{"type": "text", "text": block})
	}
	var paths []string
	for _, p := range w.LocalPaths {
		if picture := o.herPicture(p); picture != nil {
			input = append(input, picture)
			continue
		}
		path, err := herUpload(ctx, h, w.BotId, p)
		if err != nil {
			return onTheWay(ctx, err)
		}
		paths = append(paths, path)
	}
	if len(paths) > 0 {
		input = append(input, map[string]any{"type": "text", "text": attachedBlock(paths)})
	}
	op := map[string]any{"type": "prompt", "id": w.Id, "input": input}
	octx, cancel := context.WithTimeout(ctx, herAnswerWithin)
	defer cancel()
	if on.conn != "" && on.session != "" {
		err := herOp(octx, h, on.conn, on.session, op)
		if err == nil || ctx.Err() != nil || octx.Err() != nil || tunnelError(err) {
			return o.herLate(ctx, octx, h, err)
		}
		// Not on the way: that connection is gone, or no longer on the
		// conversation. One of its own says.
	}
	err = onBot(octx, h, w.BotId, func(conn, session string) error {
		return herOp(octx, h, conn, session, op)
	})
	return o.herLate(ctx, octx, h, err)
}

// herLate is a delivery's error as HerSend keeps it: out of time, here or at
// the machine (connect passes the deadline on), or lost on the way, the
// message waits; the machine that stopped answering is dialled again next
// time.
func (o *Owner) herLate(ctx, octx context.Context, h herMachine, err error) error {
	if err != nil && ctx.Err() == nil && (octx.Err() != nil || connect.CodeOf(err) == connect.CodeDeadlineExceeded) {
		if r, ok := h.(herRemote); ok {
			r.forget()
		}
		return unreachable{connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf("the machine did not answer within %s", herAnswerWithin))}
	}
	return onTheWay(ctx, err)
}

// herPicture is a picture kept with a message as her takes it (an image,
// base64), or nil for a path that is not one: a file to upload.
func (o *Owner) herPicture(path string) map[string]any {
	if !strings.HasPrefix(path, o.herPictures()+string(filepath.Separator)) {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	mime := http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") {
		return nil
	}
	return map[string]any{"type": "image", "mime_type": mime, "data": base64.StdEncoding.EncodeToString(data)}
}

// attachedBlock names the files uploaded for a message, after its words,
// as the apps' composers did (her-client's attachedBlock): workbooks with
// how to read them.
func attachedBlock(paths []string) string {
	prefix := "Attached files:\n"
	for _, p := range paths {
		if ext := strings.ToLower(filepath.Ext(p)); ext == ".xlsx" || ext == ".xlsm" {
			prefix = "Attached files. For XLSX and XLSM workbooks, use the deferred doc tools (doc_open, then doc_read) instead of unpacking them or parsing them with shell/Python:\n"
			break
		}
	}
	return prefix + strings.Join(paths, "\n")
}

// herReplyMax is how much of a message a reply quotes, in characters.
const herReplyMax = 2000

// replyBlock tells her what a message replies to, after its words
// (docs/her-reply.md §4.1): the quoted words, each line marked, in the text
// her kernel takes as an embedded resource, so that nothing in them reads
// as the owner's own (a /skill: mention). The apps show it as a quote, not
// as text (her-client's replyOf, HermeshHer's). "" for a reply that names
// no message or quotes nothing: the message goes without it.
func replyBlock(r *localv1.HerReply) string {
	text := strings.TrimSpace(strings.ReplaceAll(r.GetText(), "\r\n", "\n"))
	if text == "" || !herReplyRef(r.GetRef()) {
		return ""
	}
	whose, part := "yours", r.Partial
	if r.Own {
		whose = "mine"
	}
	if runes := []rune(text); len(runes) > herReplyMax {
		text, part = strings.TrimSpace(string(runes[:herReplyMax]))+"…", true
	}
	quoted := "full"
	if part {
		quoted = "part"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<resource uri=\"her-reply:%s\">\nReplying to this earlier message of %s, quoted in %s:\n", r.Ref, whose, quoted)
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimRight(line, " \t\r"); line == "" {
			b.WriteString(">\n")
		} else {
			b.WriteString("> " + line + "\n")
		}
	}
	b.WriteString("</resource>")
	return b.String()
}

// herReplyRef: what names a message of a conversation, the owner's by its
// id and hers by her call's.
func herReplyRef(ref string) bool {
	if len(ref) == 0 || len(ref) > 128 {
		return false
	}
	for _, c := range ref {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_:.-", c)) {
			return false
		}
	}
	return true
}

// wakeHer has the waiting messages tried now: a machine may answer again.
func (o *Owner) wakeHer() {
	select {
	case o.herWake <- struct{}{}:
	default:
	}
}

// deliverWaiting delivers the messages waiting here once their machines
// answer: when woken (wakeHer), and every herRetry.
func (o *Owner) deliverWaiting(ctx context.Context) {
	tick := time.NewTicker(herRetry)
	defer tick.Stop()
	for {
		o.flushWaiting(ctx)
		select {
		case <-ctx.Done():
			return
		case <-o.herWake:
		case <-tick.C:
		}
	}
}

// flushWaiting delivers the messages waiting here to the machines that
// answer, side by side, so that one that doesn't holds no other back; on
// each, an assistant at a time (flushBot).
func (o *Owner) flushWaiting(ctx context.Context) {
	all, err := o.st.herWaiting("", "")
	if err != nil {
		o.log.Warn("Her's waiting messages not read", "err", err)
		return
	}
	bots := map[string][]string{} // a machine's assistants, in the order their first waits
	for _, w := range all {
		if !slices.Contains(bots[w.MachineId], w.BotId) {
			bots[w.MachineId] = append(bots[w.MachineId], w.BotId)
		}
	}
	var wg sync.WaitGroup
	for machineID, ids := range bots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, botID := range ids {
				bot := o.herBotLock(machineID, botID)
				bot.Lock()
				_, down := o.flushBot(ctx, machineID, botID, "", false, herConv{})
				bot.Unlock()
				if down || ctx.Err() != nil {
					return
				}
			}
		}()
	}
	wg.Wait()
}

// DeleteHerWaiting drops a message kept here, undelivered; the assistant's
// later ones, which waited behind it, are tried at once.
func (o *Owner) DeleteHerWaiting(machineID, id string) error {
	if machineID == "" || id == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("a waiting message is named by its machine and its id"))
	}
	gone, err := o.st.deleteHerWaiting(machineID, id)
	if err != nil {
		return err
	}
	if !gone {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("no message %s waits for %s", id, machineID))
	}
	o.dropPictures(id)
	o.wakeHer()
	return nil
}

// herPictureMax is the largest picture kept with a message, as the Mac app
// takes them (apps/desktop's attachments.ts).
const herPictureMax = 10 << 20

// herPictures is where pasted pictures wait with their message, one folder
// each, so that each keeps its own name when it is uploaded.
func (o *Owner) herPictures() string { return filepath.Join(o.cfg.DataDir, "her-pictures") }

// keepPictures keeps w's pasted pictures here, as files w names.
func (o *Owner) keepPictures(w *localv1.HerWaiting, pictures []*localv1.HerPicture) error {
	for i, p := range pictures {
		if len(p.Data) == 0 || len(p.Data) > herPictureMax {
			o.dropPictures(w.Id)
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("a picture of %d bytes: at most %d", len(p.Data), herPictureMax))
		}
		name := filepath.Base(p.Name)
		if name == "." || name == string(filepath.Separator) {
			name = "picture"
		}
		path := filepath.Join(o.herPictures(), fmt.Sprintf("%s-%d", w.Id, i), name)
		err := os.MkdirAll(filepath.Dir(path), 0o700)
		if err == nil {
			err = os.WriteFile(path, p.Data, 0o600)
		}
		if err != nil {
			o.dropPictures(w.Id)
			return err
		}
		w.LocalPaths = append(w.LocalPaths, path)
	}
	return nil
}

// dropPictures deletes the pictures kept with the message id, once it was
// delivered or dropped.
func (o *Owner) dropPictures(id string) {
	dirs, _ := filepath.Glob(filepath.Join(o.herPictures(), id+"-*"))
	for _, dir := range dirs {
		if err := os.RemoveAll(dir); err != nil {
			o.log.Warn("a picture kept with a message to Her not deleted", "dir", dir, "err", err)
		}
	}
}

// ---------------------------------------------------------------------------
// Files.

// openFile opens a file on this device to send, by its absolute path.
func openFile(path string) (*os.File, uint64, error) {
	if !filepath.IsAbs(path) {
		return nil, 0, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q is not an absolute path", path))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, connect.NewError(connect.CodeInvalidArgument, err)
	}
	st, err := f.Stat()
	if err == nil && !st.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a file", path)
	}
	if err != nil {
		f.Close()
		return nil, 0, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return f, uint64(st.Size()), nil
}

// herUpload sends the file at path, on this device, into the assistant's
// folder on h, and returns where it landed there.
func herUpload(ctx context.Context, h herMachine, botID, path string) (string, error) {
	f, size, err := openFile(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	res, err := h.HerUpload(ctx, &herv1.UploadStart{BotId: botID, Name: filepath.Base(path), Size: size}, f)
	if err != nil {
		return "", err
	}
	return res.Path, nil
}

// HerUpload sends a file on this device into an assistant's folder.
func (o *Owner) HerUpload(ctx context.Context, machineID, botID, localPath string) (string, error) {
	h, err := o.herOn(ctx, machineID)
	if err != nil {
		return "", err
	}
	return herUpload(ctx, h, botID, localPath)
}

// HerDownload saves a file from an assistant's folder into localDir, under
// its name or, when that is taken, with a number added.
func (o *Owner) HerDownload(ctx context.Context, req *localv1.HerDownloadRequest) (string, error) {
	if st, err := os.Stat(req.LocalDir); !filepath.IsAbs(req.LocalDir) || err != nil || !st.IsDir() {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q is not a folder", req.LocalDir))
	}
	h, err := o.herOn(ctx, req.MachineId)
	if err != nil {
		return "", err
	}
	var f *os.File
	var want, got uint64
	err = h.HerDownload(ctx, &herv1.DownloadRequest{BotId: req.BotId, Path: req.Path}, func(m *herv1.DownloadResponse) error {
		switch p := m.Part.(type) {
		case *herv1.DownloadResponse_Start:
			if f != nil {
				return errors.New("the machine started the file twice")
			}
			var err error
			f, err = createFree(req.LocalDir, p.Start.Name)
			want = p.Start.Size
			return err
		case *herv1.DownloadResponse_Data:
			if f == nil {
				return errors.New("the machine sent the file before its name")
			}
			got += uint64(len(p.Data))
			_, err := f.Write(p.Data)
			return err
		}
		return nil
	})
	if f == nil {
		if err == nil {
			err = errors.New("the machine sent no file")
		}
		return "", err
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && got != want {
		err = fmt.Errorf("the machine sent %d of the file's %d bytes", got, want)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// createFree creates a file named name, without its folders, in dir; when
// that is taken, with a number added ("notes 2.txt"), as the Finder names
// copies.
func createFree(dir, name string) (*os.File, error) {
	name = filepath.Base(filepath.Clean(string(filepath.Separator) + name))
	if name == "." || name == string(filepath.Separator) {
		name = "file"
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		try := name
		if i > 1 {
			try = fmt.Sprintf("%s %d%s", stem, i, ext)
		}
		f, err := os.OpenFile(filepath.Join(dir, try), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if !errors.Is(err, fs.ErrExist) {
			return f, err
		}
	}
}

// ---------------------------------------------------------------------------
// Machine.her: each machine's Her, as last seen.

// withHer gives each machine its Her: from its status while it answers,
// else as last seen here; none where Her was never on (status.her still
// tells such a machine's, while it answers).
func (o *Owner) withHer(machines []*localv1.Machine) {
	for _, m := range machines {
		if m.Status != nil {
			o.keepHer(m.MachineId, m.Status.Her)
		}
		m.Her = o.keptHer(m.MachineId)
	}
}

// keepHer keeps a machine's Her as it answered, once Her was on there.
// Turned off there, what was said there is no longer kept.
func (o *Owner) keepHer(machineID string, her *machinev1.HerStatus) {
	if her == nil {
		return
	}
	o.herMu.Lock()
	defer o.herMu.Unlock()
	old, ok := o.herSeen[machineID]
	if (!ok && !her.Enabled && !her.Running) || proto.Equal(old, her) {
		return
	}
	o.herSeen[machineID] = proto.CloneOf(her)
	b, _ := proto.Marshal(her)
	if _, err := o.st.db.Exec(`INSERT INTO her_status(machine_id,status) VALUES(?,?) ON CONFLICT(machine_id) DO UPDATE SET status=excluded.status`, machineID, b); err != nil {
		o.log.Warn("Her's status not kept", "machine", machineID, "err", err)
	}
	if !her.Enabled {
		if err := o.st.dropHerKept(machineID); err != nil {
			o.log.Warn("forgetting Her's conversation", "machine", machineID, "err", err)
		}
	}
}

func (o *Owner) keptHer(machineID string) *machinev1.HerStatus {
	o.herMu.Lock()
	defer o.herMu.Unlock()
	if h := o.herSeen[machineID]; h != nil {
		return proto.CloneOf(h)
	}
	return nil
}

// ---------------------------------------------------------------------------
// The owner's store: messages waiting, and each machine's Her as last seen.

var herMigrations = `
CREATE TABLE her_waiting (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  machine_id TEXT NOT NULL,
  bot_id TEXT NOT NULL,
  waiting BLOB NOT NULL
);
CREATE TABLE her_status (
  machine_id TEXT PRIMARY KEY,
  status BLOB NOT NULL
);
`

func (s *store) addHerWaiting(w *localv1.HerWaiting) error {
	b, _ := proto.Marshal(w)
	_, err := s.db.Exec(`INSERT INTO her_waiting(id,machine_id,bot_id,waiting) VALUES(?,?,?,?)`, w.Id, w.MachineId, w.BotId, b)
	return err
}

func (s *store) updateHerWaiting(w *localv1.HerWaiting) error {
	b, _ := proto.Marshal(w)
	_, err := s.db.Exec(`UPDATE her_waiting SET waiting=? WHERE id=?`, b, w.Id)
	return err
}

// deleteHerWaiting drops one of a machine's waiting messages; gone says
// whether there was one.
func (s *store) deleteHerWaiting(machineID, id string) (gone bool, err error) {
	res, err := s.db.Exec(`DELETE FROM her_waiting WHERE id=? AND machine_id=?`, id, machineID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// herWaiting lists the messages waiting, oldest first: a machine's (all
// when empty), an assistant's (all of the machine's when empty).
func (s *store) herWaiting(machineID, botID string) ([]*localv1.HerWaiting, error) {
	rows, err := s.db.Query(`SELECT waiting FROM her_waiting WHERE (?1='' OR machine_id=?1) AND (?2='' OR bot_id=?2) ORDER BY seq`, machineID, botID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*localv1.HerWaiting
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		w := &localv1.HerWaiting{}
		if proto.Unmarshal(b, w) == nil {
			out = append(out, w)
		}
	}
	return out, rows.Err()
}

// forgetHer forgets what this device kept of a removed machine's Her: its
// waiting messages can't be delivered now, and what was said there is not
// shown.
func (o *Owner) forgetHer(machineID string) {
	o.herMu.Lock()
	defer o.herMu.Unlock()
	delete(o.herSeen, machineID)
	o.dropHerWaiting(machineID)
	if _, err := o.st.db.Exec(`DELETE FROM her_status WHERE machine_id=?`, machineID); err != nil {
		o.log.Warn("forgetting a removed machine's Her", "machine", machineID, "err", err)
	}
	if err := o.st.dropHerKept(machineID); err != nil {
		o.log.Warn("forgetting a removed machine's Her", "machine", machineID, "err", err)
	}
}

// dropHerWaiting drops the messages waiting here for a machine's Her, and
// their pictures: her data there is erased, or the machine removed.
func (o *Owner) dropHerWaiting(machineID string) {
	ws, err := o.st.herWaiting(machineID, "")
	for _, w := range ws {
		if _, err = o.st.deleteHerWaiting(machineID, w.Id); err != nil {
			break
		}
		o.dropPictures(w.Id)
	}
	if err != nil {
		o.log.Warn("dropping the messages waiting for an erased Her", "machine", machineID, "err", err)
	}
}

// herStatuses is each machine's Her as last seen.
func (s *store) herStatuses() (map[string]*machinev1.HerStatus, error) {
	out := map[string]*machinev1.HerStatus{}
	rows, err := s.db.Query(`SELECT machine_id, status FROM her_status`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var b []byte
		if err := rows.Scan(&id, &b); err != nil {
			return out, err
		}
		h := &machinev1.HerStatus{}
		if proto.Unmarshal(b, h) == nil {
			out[id] = h
		}
	}
	return out, rows.Err()
}
