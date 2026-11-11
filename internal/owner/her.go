package owner

import (
	"context"
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
	return herRemote{her: n.Her, http: n.HTTP(), forget: func() { c.Forget(machineID) }}, nil
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
		c.Forget(machineID)
		return err
	}
	if !enabled {
		o.revokeHer(ctx, machineID)
		if !erase {
			return nil
		}
		switch msg := res.GetMessage(); msg {
		case machine.HerErased:
			o.dropHerWaiting(machineID)
			return nil
		case machine.HerOff:
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("Her is off, but her data stays: this machine's Hermesh is too old to erase it. Update it, then turn Her on and off again"))
		default:
			return connect.NewError(connect.CodeInternal, errors.New(msg))
		}
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
	return h.HerCall(ctx, req)
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
// conversation (bots/open, without the events of herQuiet) and runs fn
// with the connection and the conversation; the connection closes after.
func onBot(ctx context.Context, h herMachine, botID string, fn func(conn, session string) error) error {
	params, err := json.Marshal(map[string]any{"bot_id": botID, "events": map[string]any{"exclude": herQuiet}})
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

// HerSend sends an assistant a message: now when its machine answers,
// else once it does (deliverWaiting), behind the messages to the same
// assistant that wait already.
func (o *Owner) HerSend(ctx context.Context, req *localv1.HerSendRequest) (*localv1.HerSendResponse, error) {
	switch {
	case req.MachineId == "" || req.BotId == "":
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a message goes to a machine's assistant"))
	case req.Text == "" && len(req.LocalPaths) == 0 && len(req.Pictures) == 0:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("an empty message"))
	}
	for _, p := range req.LocalPaths {
		f, _, err := openFile(p)
		if err != nil {
			return nil, err
		}
		f.Close()
	}
	w := &localv1.HerWaiting{Id: "hw_" + strings.ToLower(ulid.Make().String()), MachineId: req.MachineId, BotId: req.BotId, Text: req.Text,
		LocalPaths: slices.Clone(req.LocalPaths), CreatedAt: timestamppb.Now()}
	if err := o.keepPictures(w, req.Pictures); err != nil {
		return nil, err
	}
	sent, err := o.herSendNow(ctx, w)
	if err != nil || sent.Delivered {
		o.dropPictures(w.Id)
	}
	return sent, err
}

// herSendNow delivers w now, or keeps it to deliver later.
func (o *Owner) herSendNow(ctx context.Context, w *localv1.HerWaiting) (*localv1.HerSendResponse, error) {
	before, err := o.st.herWaiting(w.MachineId, w.BotId)
	if err != nil {
		return nil, err
	}
	switch {
	case len(before) > 0:
	case o.rep.offline(w.MachineId):
		w.Error = "offline"
	default:
		err := o.herDeliver(ctx, w)
		if err == nil {
			return &localv1.HerSendResponse{Delivered: true}, nil
		}
		if !errors.As(err, new(unreachable)) {
			return nil, err
		}
		w.Error = err.Error()
	}
	if err := o.st.addHerWaiting(w); err != nil {
		return nil, err
	}
	o.wakeHer()
	return &localv1.HerSendResponse{Waiting: w}, nil
}

// herDeliver delivers w: its files into the assistant's folder, then the
// message naming where they landed, on the conversation bots/open loads.
func (o *Owner) herDeliver(ctx context.Context, w *localv1.HerWaiting) error {
	rctx, cancel := context.WithTimeout(ctx, herAnswerWithin)
	h, err := o.herOn(rctx, w.MachineId)
	cancel()
	if err != nil {
		if ctx.Err() == nil && (tunnelError(err) || errors.Is(err, context.DeadlineExceeded)) {
			return unreachable{err}
		}
		return err
	}
	text := w.Text
	var paths []string
	for _, p := range w.LocalPaths {
		path, err := herUpload(ctx, h, w.BotId, p)
		if err != nil {
			return onTheWay(ctx, err)
		}
		paths = append(paths, path)
	}
	if len(paths) > 0 {
		text = attached(text, paths)
	}
	octx, cancel := context.WithTimeout(ctx, herAnswerWithin)
	defer cancel()
	err = onBot(octx, h, w.BotId, func(conn, session string) error {
		return herOp(octx, h, conn, session, map[string]any{"type": "prompt", "input": []any{map[string]any{"type": "text", "text": text}}})
	})
	// Out of time, here or at the machine (connect passes the deadline on).
	if err != nil && ctx.Err() == nil && (octx.Err() != nil || connect.CodeOf(err) == connect.CodeDeadlineExceeded) {
		if r, ok := h.(herRemote); ok {
			r.forget() // it stopped answering: dial again next time
		}
		return unreachable{connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf("the machine did not answer within %s", herAnswerWithin))}
	}
	return onTheWay(ctx, err)
}

// attached is a message followed by where the files sent with it are.
func attached(text string, paths []string) string {
	var b strings.Builder
	b.WriteString(text)
	if text != "" {
		b.WriteString("\n\n")
	}
	b.WriteString("Attached files:")
	for _, p := range paths {
		b.WriteString("\n- " + p)
	}
	return b.String()
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

// flushWaiting delivers the messages waiting here, oldest first, to the
// machines that answer. An assistant's later messages wait behind one that
// failed, so that they arrive in order; the failure is kept with it.
func (o *Owner) flushWaiting(ctx context.Context) {
	all, err := o.st.herWaiting("", "")
	if err != nil {
		o.log.Warn("Her's waiting messages not read", "err", err)
		return
	}
	down, stuck := map[string]bool{}, map[string]bool{}
	for _, w := range all {
		bot := w.MachineId + "/" + w.BotId
		if down[w.MachineId] || stuck[bot] {
			continue
		}
		if o.rep.offline(w.MachineId) {
			down[w.MachineId] = true
			continue
		}
		err := o.herDeliver(ctx, w)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			_, err = o.st.deleteHerWaiting(w.MachineId, w.Id)
			o.dropPictures(w.Id)
		} else {
			stuck[bot] = true
			if errors.As(err, new(unreachable)) {
				down[w.MachineId] = true
			}
			w.Error = err.Error()
			err = o.st.updateHerWaiting(w)
		}
		if err != nil {
			o.log.Warn("Her's waiting message not kept", "id", w.Id, "err", err)
		}
	}
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
	for _, q := range []string{`DELETE FROM her_waiting WHERE machine_id=?`, `DELETE FROM her_status WHERE machine_id=?`} {
		if _, err := o.st.db.Exec(q, machineID); err != nil {
			o.log.Warn("forgetting a removed machine's Her", "machine", machineID, "err", err)
		}
	}
	if err := o.st.dropHerKept(machineID); err != nil {
		o.log.Warn("forgetting a removed machine's Her", "machine", machineID, "err", err)
	}
}

// dropHerWaiting drops the messages waiting here for a machine's Her, and
// their pictures, once her data there is erased.
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
