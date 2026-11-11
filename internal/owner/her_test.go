package owner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/her/v1/herv1connect"
	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

// herSide is a machine's side of Her in these tests: its HerService and its
// console, served over HTTP/2 on a connection of their own, as through a
// tunnel.
type herSide struct {
	herv1connect.UnimplementedHerServiceHandler
	console http.Handler

	mu      sync.Mutex
	conns   int
	opens   []string          // each setup request: its method and params
	ops     []string          // each call: its connection, method and params
	uploads map[string]string // by assistant/name
	files   map[string]string // what Download serves, by path
	failBot string            // bots/open fails for this assistant
	refuse  string            // her refuses a call that holds this
	silent  bool              // Open never answers: the machine froze
}

func (h *herSide) Open(ctx context.Context, req *herv1.OpenRequest, s *connect.ServerStream[herv1.OpenEvent]) error {
	h.mu.Lock()
	h.conns++
	opened := &herv1.Opened{ConnectionId: fmt.Sprintf("c%d", h.conns)}
	for _, r := range req.Setup {
		h.opens = append(h.opens, r.Method+" "+string(r.Params))
		var p struct {
			BotID string `json:"bot_id"`
		}
		json.Unmarshal(r.Params, &p)
		res := &herv1.Response{Result: []byte(`{"session_id":"conv-` + p.BotID + `"}`)}
		if p.BotID != "" && p.BotID == h.failBot {
			res = &herv1.Response{Error: &herv1.Error{Code: -32040, Message: "no assistant " + p.BotID}}
		}
		opened.Responses = append(opened.Responses, res)
	}
	silent := h.silent
	h.mu.Unlock()
	if silent {
		<-ctx.Done() // the caller's deadline, which connect passes on
		return ctx.Err()
	}
	if err := s.Send(&herv1.OpenEvent{Kind: &herv1.OpenEvent_Opened{Opened: opened}}); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func (h *herSide) Call(_ context.Context, req *herv1.CallRequest) (*herv1.CallResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ops = append(h.ops, req.ConnectionId+" "+req.Request.Method+" "+string(req.Request.Params))
	if h.refuse != "" && strings.Contains(string(req.Request.Params), h.refuse) {
		return &herv1.CallResponse{Response: &herv1.Response{Error: &herv1.Error{Code: -32602, Message: "her won't take it"}}}, nil
	}
	return &herv1.CallResponse{Response: &herv1.Response{Result: []byte("{}")}}, nil
}

func (h *herSide) Upload(_ context.Context, s *connect.ClientStream[herv1.UploadRequest]) (*herv1.UploadResponse, error) {
	var start *herv1.UploadStart
	var data []byte
	for s.Receive() {
		switch p := s.Msg().Part.(type) {
		case *herv1.UploadRequest_Start:
			start = p.Start
		case *herv1.UploadRequest_Data:
			data = append(data, p.Data...)
		}
	}
	if start == nil || uint64(len(data)) != start.Size {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("the bytes don't add up"))
	}
	h.mu.Lock()
	h.uploads[start.BotId+"/"+start.Name] = string(data)
	h.mu.Unlock()
	return &herv1.UploadResponse{Path: "/her/bots/" + start.BotId + "/files/uploads/" + start.Name}, nil
}

// Download sends the file under the name it was asked by (folders and all),
// in pieces of 4 bytes; "truncated" says it is longer than it is.
func (h *herSide) Download(_ context.Context, req *herv1.DownloadRequest, s *connect.ServerStream[herv1.DownloadResponse]) error {
	content, ok := h.files[req.Path]
	if !ok {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("no file %s", req.Path))
	}
	size := uint64(len(content))
	if req.Path == "truncated" {
		size += 5
	}
	if err := s.Send(&herv1.DownloadResponse{Part: &herv1.DownloadResponse_Start{Start: &herv1.DownloadStart{Name: req.Path, Size: size}}}); err != nil {
		return err
	}
	for b := []byte(content); len(b) > 0; b = b[min(4, len(b)):] {
		if err := s.Send(&herv1.DownloadResponse{Part: &herv1.DownloadResponse_Data{Data: b[:min(4, len(b))]}}); err != nil {
			return err
		}
	}
	return nil
}

// prompts are what the assistants were handed, in order, as
// "conversation: text" (or the op and its mode).
func (h *herSide) prompts(t *testing.T) []string {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, op := range h.ops {
		_, call, _ := strings.Cut(op, " ")
		method, params, _ := strings.Cut(call, " ")
		var p struct {
			SessionID string `json:"session_id"`
			Op        struct {
				Type, Mode string
				Input      []struct{ Type, Text string }
			}
		}
		if method != "session/op" || json.Unmarshal([]byte(params), &p) != nil {
			t.Fatalf("not an op: %s", op)
		}
		if p.Op.Type == "prompt" && len(p.Op.Input) == 1 && p.Op.Input[0].Type == "text" {
			out = append(out, p.SessionID+": "+p.Op.Input[0].Text)
		} else {
			out = append(out, p.SessionID+": "+p.Op.Type+" "+p.Op.Mode)
		}
	}
	return out
}

// herOwner is an owner whose machine m_1 is h, reached as through its
// tunnel (herRemote), or not at all while down is set; forgot counts the
// tunnels it dropped.
func herOwner(t *testing.T, h *herSide) (*Device, *Owner, *atomic.Bool, *atomic.Int32) {
	t.Helper()
	d, err := Open(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if h.uploads == nil {
		h.uploads = map[string]string{}
	}
	mux := http.NewServeMux()
	mux.Handle(herv1connect.NewHerServiceHandler(h))
	mux.HandleFunc("/her/console/", func(w http.ResponseWriter, r *http.Request) { h.console.ServeHTTP(w, r) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	machineSide, ownerSide := net.Pipe()
	go transport.ServeH2(ctx, machineSide, mux)
	h2, err := transport.NewH2Client(ownerSide)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h2.Close() })
	var down atomic.Bool
	var forgot atomic.Int32
	remote := herRemote{her: herv1connect.NewHerServiceClient(h2, "https://machine", transport.ClientNoCompression), http: h2.Client,
		forget: func() { forgot.Add(1) }}
	o := d.owner()
	o.herReach = func(_ context.Context, machineID string) (herMachine, error) {
		switch {
		case machineID != "m_1":
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no machine %q", machineID))
		case down.Load():
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("m_1 is offline"))
		}
		return remote, nil
	}
	return d, o, &down, &forgot
}

// A message goes to the assistant at once while its machine answers: its
// files first, into the assistant's folder, then the text naming where they
// landed, on a connection that opened its conversation without the deltas.
func TestHerSendDelivers(t *testing.T) {
	h := &herSide{}
	_, o, _, _ := herOwner(t, h)
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(file, []byte("some notes"), 0o644)
	res, err := o.HerSend(ctx, &localv1.HerSendRequest{MachineId: "m_1", BotId: "b1", Text: "have a look", LocalPaths: []string{file}})
	if err != nil || !res.Delivered || res.Waiting != nil {
		t.Fatalf("send: %v %v", res, err)
	}
	if h.uploads["b1/notes.txt"] != "some notes" {
		t.Fatalf("uploaded: %v", h.uploads)
	}
	if want := `bots/open {"bot_id":"b1","events":{"exclude":["text_delta","thinking_delta","tool_call_delta","tool_progress"]}}`; len(h.opens) != 1 || h.opens[0] != want {
		t.Fatalf("opened %q, want %q", h.opens, want)
	}
	if got := h.prompts(t); len(got) != 1 || got[0] != "conv-b1: have a look\n\nAttached files:\n- /her/bots/b1/files/uploads/notes.txt" {
		t.Fatalf("handed %q", got)
	}
	if !strings.HasPrefix(h.ops[0], "c1 ") {
		t.Fatalf("not on the connection that opened the conversation: %q", h.ops[0])
	}
	if list, _ := o.st.herWaiting("", ""); len(list) != 0 {
		t.Fatalf("kept: %v", list)
	}
	for _, bad := range []*localv1.HerSendRequest{
		{MachineId: "m_1", BotId: "b1"},
		{MachineId: "m_1", Text: "to nobody"},
		{MachineId: "m_1", BotId: "b1", Text: "x", LocalPaths: []string{"notes.txt"}},
		{MachineId: "m_1", BotId: "b1", Text: "x", LocalPaths: []string{filepath.Join(t.TempDir(), "gone")}},
	} {
		if _, err := o.HerSend(ctx, bad); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%v: %v", bad, err)
		}
	}
}

// A message to a machine that doesn't answer waits here, and so does the
// next one to the same assistant, behind it, though the machine answers by
// then; once they are tried again they arrive in order and wait no more.
func TestHerSendWaits(t *testing.T) {
	h := &herSide{}
	d, o, down, _ := herOwner(t, h)
	l := &localService{d: d}
	ctx := context.Background()
	down.Store(true)
	first, err := o.HerSend(ctx, &localv1.HerSendRequest{MachineId: "m_1", BotId: "b1", Text: "one"})
	if err != nil || first.Delivered || first.Waiting.GetText() != "one" || !strings.Contains(first.Waiting.Error, "offline") || first.Waiting.CreatedAt == nil {
		t.Fatalf("first: %v %v", first, err)
	}
	down.Store(false)
	second, err := o.HerSend(ctx, &localv1.HerSendRequest{MachineId: "m_1", BotId: "b1", Text: "two"})
	if err != nil || second.Delivered {
		t.Fatalf("second: %v %v", second, err)
	}
	if res, err := o.HerSend(ctx, &localv1.HerSendRequest{MachineId: "m_1", BotId: "b2", Text: "three"}); err != nil || !res.Delivered {
		t.Fatalf("to another assistant: %v %v", res, err)
	}
	list, err := l.ListHerWaiting(ctx, &localv1.ListHerWaitingRequest{MachineId: "m_1"})
	if err != nil || len(list.Waiting) != 2 || list.Waiting[0].Id != first.Waiting.Id || list.Waiting[1].Id != second.Waiting.Id {
		t.Fatalf("waiting: %v %v", list, err)
	}
	if list, _ := l.ListHerWaiting(ctx, &localv1.ListHerWaitingRequest{MachineId: "m_1", BotId: "b2"}); len(list.Waiting) != 0 {
		t.Fatalf("b2's: %v", list)
	}
	o.flushWaiting(ctx)
	if got, want := h.prompts(t), []string{"conv-b2: three", "conv-b1: one", "conv-b1: two"}; !equal(got, want) {
		t.Fatalf("handed %q, want %q", got, want)
	}
	if list, _ := l.ListHerWaiting(ctx, &localv1.ListHerWaitingRequest{}); len(list.Waiting) != 0 {
		t.Fatalf("still waiting: %v", list)
	}
}

func equal(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }

// A waiting message the machine refuses stays, with why, and that
// assistant's later ones wait behind it; the others' go. One refused while
// the machine answers is not kept: the caller is told.
func TestHerWaitingRefused(t *testing.T) {
	h := &herSide{failBot: "gone"}
	_, o, down, _ := herOwner(t, h)
	ctx := context.Background()
	down.Store(true)
	for _, m := range []struct{ bot, text string }{{"gone", "one"}, {"gone", "two"}, {"b1", "three"}} {
		if _, err := o.HerSend(ctx, &localv1.HerSendRequest{MachineId: "m_1", BotId: m.bot, Text: m.text}); err != nil {
			t.Fatal(err)
		}
	}
	down.Store(false)
	o.flushWaiting(ctx)
	if got := h.prompts(t); !equal(got, []string{"conv-b1: three"}) {
		t.Fatalf("handed %q", got)
	}
	list, _ := o.st.herWaiting("m_1", "")
	if len(list) != 2 || list[0].Text != "one" || !strings.Contains(list[0].Error, "no assistant gone") || list[1].Text != "two" || list[1].Error != "" { // not tried: behind "one"
		t.Fatalf("waiting: %v", list)
	}
	h.mu.Lock()
	h.failBot = "nobody"
	h.mu.Unlock()
	if _, err := o.HerSend(ctx, &localv1.HerSendRequest{MachineId: "m_1", BotId: "nobody", Text: "hi"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("to an assistant that isn't there: %v", err)
	}
	if list, _ := o.st.herWaiting("", ""); len(list) != 2 {
		t.Fatalf("kept the refused one: %v", list)
	}
}

// A waiting message her keeps refusing can be dropped: the assistant's later
// ones, held behind it, are then tried at once and go; the dropped one never
// does. Only a message kept for the machine named is dropped.
func TestHerDeleteWaiting(t *testing.T) {
	h := &herSide{refuse: "too big"}
	d, o, down, _ := herOwner(t, h)
	l := &localService{d: d}
	ctx := context.Background()
	send := func(text string) *localv1.HerWaiting {
		t.Helper()
		res, err := o.HerSend(ctx, &localv1.HerSendRequest{MachineId: "m_1", BotId: "b1", Text: text})
		if err != nil || res.Waiting == nil {
			t.Fatalf("send %q: %v %v", text, res, err)
		}
		return res.Waiting
	}
	down.Store(true)
	refused, later := send("too big"), send("fine")
	down.Store(false)
	o.flushWaiting(ctx)
	if list, _ := o.st.herWaiting("m_1", ""); len(list) != 2 || !strings.Contains(list[0].Error, "her won't take it") {
		t.Fatalf("waiting: %v", list)
	}
	select { // what sending woke
	case <-o.herWake:
	default:
	}
	if _, err := l.DeleteHerWaiting(ctx, &localv1.DeleteHerWaitingRequest{MachineId: "m_1", Id: refused.Id}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-o.herWake:
	default:
		t.Fatal("the later ones are not tried at once")
	}
	o.flushWaiting(ctx)
	if got := h.prompts(t); !equal(got, []string{"conv-b1: too big", "conv-b1: fine"}) {
		t.Fatalf("handed %q", got)
	}
	if list, _ := o.st.herWaiting("", ""); len(list) != 0 {
		t.Fatalf("still waiting: %v", list)
	}

	down.Store(true)
	kept := send("for m_1")
	for _, c := range []struct {
		req  *localv1.DeleteHerWaitingRequest
		code connect.Code
	}{
		{&localv1.DeleteHerWaitingRequest{MachineId: "m_1", Id: refused.Id}, connect.CodeNotFound}, // dropped already
		{&localv1.DeleteHerWaitingRequest{MachineId: "m_1", Id: later.Id}, connect.CodeNotFound},   // delivered
		{&localv1.DeleteHerWaitingRequest{MachineId: "m_2", Id: kept.Id}, connect.CodeNotFound},    // another machine's
		{&localv1.DeleteHerWaitingRequest{MachineId: "m_1"}, connect.CodeInvalidArgument},          // no id
		{&localv1.DeleteHerWaitingRequest{Id: kept.Id}, connect.CodeInvalidArgument},               // no machine
	} {
		if _, err := l.DeleteHerWaiting(ctx, c.req); connect.CodeOf(err) != c.code {
			t.Errorf("%v: %v, want %v", c.req, err, c.code)
		}
	}
	if list, _ := o.st.herWaiting("m_1", ""); len(list) != 1 || list[0].Id != kept.Id {
		t.Fatalf("waiting: %v", list)
	}
}

// A machine that stops answering with its tunnel still up: the message waits
// after herAnswerWithin, and the tunnel is dropped.
func TestHerSendToAFrozenMachine(t *testing.T) {
	defer func(d time.Duration) { herAnswerWithin = d }(herAnswerWithin)
	herAnswerWithin = 300 * time.Millisecond
	h := &herSide{silent: true}
	_, o, _, forgot := herOwner(t, h)
	res, err := o.HerSend(context.Background(), &localv1.HerSendRequest{MachineId: "m_1", BotId: "b1", Text: "hello"})
	if err != nil || res.Delivered || !strings.Contains(res.Waiting.GetError(), "did not answer") {
		t.Fatalf("send: %v %v", res, err)
	}
	if forgot.Load() == 0 {
		t.Fatal("the tunnel is kept")
	}
}

// A file goes up in pieces, and comes down into the folder asked, under its
// own name, or with a number added when that is taken; never outside it.
func TestHerFiles(t *testing.T) {
	h := &herSide{files: map[string]string{"report.pdf": "pdf bytes", "../../etc/passwd": "root", "truncated": "abc"}}
	_, o, _, _ := herOwner(t, h)
	ctx := context.Background()
	big := strings.Repeat("0123456789", 20_000) // three pieces and some
	up := filepath.Join(t.TempDir(), "big.txt")
	os.WriteFile(up, []byte(big), 0o644)
	if p, err := o.HerUpload(ctx, "m_1", "b1", up); err != nil || p != "/her/bots/b1/files/uploads/big.txt" || h.uploads["b1/big.txt"] != big {
		t.Fatalf("upload: %q %v (%d bytes there)", p, err, len(h.uploads["b1/big.txt"]))
	}
	if _, err := o.HerUpload(ctx, "m_1", "b1", "big.txt"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a relative path: %v", err)
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "report.pdf"), []byte("mine"), 0o644)
	for _, want := range []string{"report 2.pdf", "report 3.pdf"} {
		p, err := o.HerDownload(ctx, &localv1.HerDownloadRequest{MachineId: "m_1", BotId: "b1", Path: "report.pdf", LocalDir: dir})
		if b, _ := os.ReadFile(p); err != nil || p != filepath.Join(dir, want) || string(b) != "pdf bytes" {
			t.Fatalf("download: %q %v %q", p, err, b)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "report.pdf")); string(b) != "mine" {
		t.Fatalf("overwrote the file there: %q", b)
	}
	if p, err := o.HerDownload(ctx, &localv1.HerDownloadRequest{MachineId: "m_1", BotId: "b1", Path: "../../etc/passwd", LocalDir: dir}); err != nil || p != filepath.Join(dir, "passwd") {
		t.Fatalf("a name with folders: %q %v", p, err)
	}
	if p, err := o.HerDownload(ctx, &localv1.HerDownloadRequest{MachineId: "m_1", BotId: "b1", Path: "truncated", LocalDir: dir}); err == nil {
		t.Fatalf("a short file: %q", p)
	}
	if _, err := os.Stat(filepath.Join(dir, "truncated")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a short file is left: %v", err)
	}
	if _, err := o.HerDownload(ctx, &localv1.HerDownloadRequest{MachineId: "m_1", BotId: "b1", Path: "missing", LocalDir: dir}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("a missing file: %v", err)
	}
	if _, err := o.HerDownload(ctx, &localv1.HerDownloadRequest{MachineId: "m_1", BotId: "b1", Path: "report.pdf", LocalDir: "here"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a relative folder: %v", err)
	}
}

// An assistant's mode goes on its conversation, unsigned, full access too:
// the owner chose it. Turning Her on and connectors started as commands are
// signed by the root key, and never sent unsigned.
func TestHerAssistantModeAndSignedCommands(t *testing.T) {
	h := &herSide{}
	_, o, _, _ := herOwner(t, h)
	ctx := context.Background()
	for _, mode := range []string{"accept-edits", "bypass-permissions"} {
		if err := o.SetHerAssistantMode(ctx, "m_1", "b1", mode); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.prompts(t); !equal(got, []string{"conv-b1: set_permission_mode accept-edits", "conv-b1: set_permission_mode bypass-permissions"}) {
		t.Fatalf("handed %q", got)
	}
	if err := o.SetHerAssistantMode(ctx, "m_1", "b1", "yolo"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("an unknown mode: %v", err)
	}

	k, _ := identity.GenerateKey()
	o.rootPub = k.Public()
	asked, stop := o.hub.watch(false)
	defer stop()
	connector := &identityv1.SetHerCommandConnector{Name: "github", Command: "npx", Args: []string{"-y", "server-github"}}
	for _, c := range []struct {
		name string
		call func() error
		want proto.Message
		got  func(*identityv1.AdminCommand) proto.Message
	}{
		{"Her on", func() error { return o.SetHer(ctx, "m_1", true, false) }, &identityv1.SetHer{Enabled: true},
			func(c *identityv1.AdminCommand) proto.Message { return c.GetSetHer() }},
		{"Her off, her data erased", func() error { return o.SetHer(ctx, "m_1", false, true) }, &identityv1.SetHer{Erase: true},
			func(c *identityv1.AdminCommand) proto.Message { return c.GetSetHer() }},
		{"a connector", func() error { return o.SetHerCommandConnector(ctx, "m_1", connector) }, connector,
			func(c *identityv1.AdminCommand) proto.Message { return c.GetSetHerCommandConnector() }},
	} {
		done := make(chan error, 1)
		go func() { done <- c.call() }()
		var r *localv1.SigningRequest
		select {
		case r = <-asked:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the signer is not asked", c.name)
		}
		cmd := &identityv1.AdminCommand{}
		if r.Type != identity.MessageType(cmd) || proto.Unmarshal(r.Body, cmd) != nil || cmd.TargetMachineId != "m_1" {
			t.Fatalf("%s: signs %s", c.name, r.Type)
		}
		if !proto.Equal(c.got(cmd), c.want) {
			t.Fatalf("%s: signs %v", c.name, cmd)
		}
		o.hub.complete(&localv1.CompleteSigningRequestRequest{RequestId: r.RequestId, Rejected: true})
		if err := <-done; !errors.Is(err, ErrSigningRejected) {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	if got := h.prompts(t); len(got) != 2 {
		t.Fatalf("sent unsigned: %q", got)
	}
	if err := o.SetHer(ctx, "m_1", true, true); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Her on, her data erased: %v", err)
	}
	if err := o.SetHerCommandConnector(ctx, "m_1", &identityv1.SetHerCommandConnector{Name: "x"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a connector without a command: %v", err)
	}
}

// A computer that is only a machine holds no owner key: what needs one is
// refused, saying a trusted device has to do it.
func TestHerOnAMachineOnly(t *testing.T) {
	d, err := Open(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	o := d.owner()
	o.st.set(kMachineOnly, []byte("1"))
	ctx := context.Background()
	for name, err := range map[string]error{
		"Her on":      o.SetHer(ctx, "m_1", true, false),
		"a connector": o.SetHerCommandConnector(ctx, "m_1", &identityv1.SetHerCommandConnector{Name: "x", Command: "y"}),
	} {
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "a trusted device has to") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The rest goes to its own machine, in process: not running here.
	for _, mode := range []string{"manual", "bypass-permissions"} {
		if err := o.SetHerAssistantMode(ctx, "m_1", "b1", mode); connect.CodeOf(err) != connect.CodeUnavailable {
			t.Errorf("%s: %v", mode, err)
		}
	}
}

// The console is served here on 127.0.0.1, under its machine and a token
// in the path, as the machine's /her/console/…; server-sent events come as
// they are sent. Without the token nothing is served.
func TestHerConsole(t *testing.T) {
	next, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	h := &herSide{console: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/her/console/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: opened\ndata: {\"connection_id\":\"c9\"}\n\n")
			w.(http.Flusher).Flush()
			for _, wait := range []chan struct{}{next, release} {
				select {
				case <-wait:
				case <-r.Context().Done():
					return
				}
				io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"bots/changed\",\"params\":{}}\n\n")
				w.(http.Flusher).Flush()
			}
			return
		}
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s %s?%s %s", r.Method, r.URL.Path, r.URL.RawQuery, body)
	})}
	_, o, _, _ := herOwner(t, h)
	url, err := o.GetHerConsole("m_1")
	if err != nil || !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.Contains(url, "/her/m_1/") || !strings.HasSuffix(url, "/") {
		t.Fatalf("url %q %v", url, err)
	}
	if again, _ := o.GetHerConsole("m_1"); again != url {
		t.Fatalf("another address: %q", again)
	}
	get := func(u string) (int, string) {
		resp, err := http.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get(url + "app.js?v=2"); code != 200 || body != "GET /her/console/app.js?v=2 " {
		t.Fatalf("a file: %d %q", code, body)
	}
	resp, err := http.Post(url+"rpc", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"models/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != `POST /her/console/rpc? {"jsonrpc":"2.0","id":1,"method":"models/list"}` {
		t.Fatalf("rpc: %q", b)
	}

	resp, err = http.Get(url + "events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// Each event comes as it is sent: `opened`, then a notification, while
	// the stream stays open.
	events := make(chan string, 2)
	go func() {
		br := bufio.NewReader(resp.Body)
		for {
			var ev strings.Builder
			for {
				l, err := br.ReadString('\n')
				if err != nil {
					return
				}
				if l == "\n" {
					break
				}
				ev.WriteString(l)
			}
			events <- ev.String()
		}
	}()
	for i, want := range []string{"event: opened\ndata: {\"connection_id\":\"c9\"}\n", "data: {\"jsonrpc\":\"2.0\",\"method\":\"bots/changed\",\"params\":{}}\n"} {
		select {
		case ev := <-events:
			if ev != want {
				t.Fatalf("event %d: %q, want %q", i, ev, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("event %d did not come while the stream is open", i)
		}
		if i == 0 {
			next <- struct{}{}
		}
	}

	base, token, _ := strings.Cut(strings.TrimSuffix(url, "/"), "/her/m_1/")
	for _, u := range []string{base + "/her/m_1/" + strings.Repeat("0", len(token)) + "/app.js", base + "/her/m_1/app.js", base + "/"} {
		if code, _ := get(u); code != http.StatusNotFound {
			t.Errorf("%s: %d", u, code)
		}
	}
	if code, _ := get(base + "/her/m_2/" + token + "/"); code != http.StatusBadGateway {
		t.Errorf("an unknown machine: %d", code)
	}
}

// signins drives the console's sign-in calls against o's console.
type signins struct {
	t    *testing.T
	base string
}

func newSignins(t *testing.T, o *Owner) *signins {
	base, err := o.GetHerConsole("m_1")
	if err != nil {
		t.Fatal(err)
	}
	return &signins{t: t, base: base}
}

func (s *signins) start(path string) (id, redirect string) {
	s.t.Helper()
	resp, err := http.Post(s.base+"signin", "application/json", strings.NewReader(`{"path":"`+path+`"}`))
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		ID          string `json:"id"`
		RedirectURI string `json:"redirect_uri"`
	}
	json.NewDecoder(resp.Body).Decode(&got)
	if resp.StatusCode != 200 || got.ID == "" || !strings.HasPrefix(got.RedirectURI, "http://127.0.0.1:") || !strings.HasSuffix(got.RedirectURI, path) {
		s.t.Fatalf("start: %d %+v", resp.StatusCode, got)
	}
	return got.ID, got.RedirectURI
}

func (s *signins) poll(id string) (int, map[string]any) {
	resp, err := http.Get(s.base + "signin/" + id)
	if err != nil {
		s.t.Error(err)
		return 0, nil
	}
	defer resp.Body.Close()
	var got map[string]any
	json.NewDecoder(resp.Body).Decode(&got)
	return resp.StatusCode, got
}

// browse is the browser coming back from the provider.
func browse(t *testing.T, u, lang string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	if lang != "" {
		req.Header.Set("Accept-Language", lang)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("%s: %d %s", u, resp.StatusCode, b)
	}
	return string(b)
}

// A sign-in started on a machine comes back to this device: the console
// asks for a catcher, the browser lands on it once and is told to go back
// to Hermesh in its language, and the console collects what it brought,
// once, as soon as it comes.
func TestHerSignin(t *testing.T) {
	defer func(d time.Duration) { signinWait = d }(signinWait)
	signinWait = time.Second
	_, o, _, _ := herOwner(t, &herSide{})
	s := newSignins(t, o)
	id, redirect := s.start("/auth/callback")
	if code, got := s.poll(id); code != 200 || got["pending"] != true {
		t.Fatalf("before the browser came back: %d %v", code, got)
	}
	polled := make(chan map[string]any, 1)
	go func() {
		_, got := s.poll(id)
		polled <- got
	}()
	time.Sleep(100 * time.Millisecond) // the poll waits
	if page := browse(t, redirect+"?code=abc&state=xyz&client_id=app_1&iss=https%3A%2F%2Fauth.example&scope=x", "zh-CN,zh;q=0.9,en;q=0.8"); !strings.Contains(page, "回到 Hermesh") {
		t.Fatalf("page: %s", page)
	}
	select {
	case got := <-polled:
		want := map[string]any{"code": "abc", "state": "xyz", "client_id": "app_1", "iss": "https://auth.example"}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("collected %v, want %v", got, want)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("the poll did not hear of the redirect as it came")
	}
	if code, got := s.poll(id); code != http.StatusNotFound {
		t.Fatalf("told twice: %d %v", code, got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for { // it caught its one redirect: it no longer listens
		resp, err := http.Get(redirect)
		if err != nil {
			break
		}
		resp.Body.Close()
		if time.Now().After(deadline) {
			t.Fatal("the catcher still listens")
		}
		time.Sleep(20 * time.Millisecond)
	}

	id, redirect = s.start("/callback")
	if page := browse(t, redirect+"?error=access_denied&error_description=No+thanks", ""); !strings.Contains(page, "You can close this window and go back to Hermesh.") {
		t.Fatalf("page: %s", page)
	}
	if _, got := s.poll(id); fmt.Sprint(got) != fmt.Sprint(map[string]any{"error": "access_denied", "error_description": "No thanks"}) {
		t.Fatalf("a refusal: %v", got)
	}
	_, redirect = s.start("/cb")
	if resp, err := http.Get(strings.TrimSuffix(redirect, "/cb") + "/favicon.ico"); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("another path: %v %v", resp, err)
	}
	for _, body := range []string{`{"path":"auth"}`, `{"path":"/a?b"}`, `nonsense`} {
		resp, err := http.Post(s.base+"signin", "application/json", strings.NewReader(body))
		if err != nil || resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %v %v", body, resp, err)
		}
	}
	if code, _ := s.poll("unknown"); code != http.StatusNotFound {
		t.Errorf("an unknown sign-in: %d", code)
	}
}

// Sign-ins come back to the same port, kept, while it is free: another one
// that waits meanwhile gets a port of its own; when something else took the
// kept one, a new one is kept from then on.
func TestHerSigninKeepsItsPort(t *testing.T) {
	_, o, _, _ := herOwner(t, &herSide{})
	s := newSignins(t, o)
	port := func(redirect string) string {
		u, err := url.Parse(redirect)
		if err != nil {
			t.Fatal(err)
		}
		return u.Port()
	}
	done := func(id, redirect string) {
		browse(t, redirect+"?code=c&state=s", "")
		if _, got := s.poll(id); got["code"] != "c" {
			t.Fatalf("collected %v", got)
		}
	}
	id, redirect := s.start("/auth/callback")
	kept := port(redirect)
	done(id, redirect)
	id, redirect = s.start("/callback")
	if port(redirect) != kept {
		t.Fatalf("port %s, then %s", kept, port(redirect))
	}
	other, otherRedirect := s.start("/callback") // while the first waits
	if port(otherRedirect) == kept {
		t.Fatal("two sign-ins on one port")
	}
	done(id, redirect)
	done(other, otherRedirect)
	id, redirect = s.start("/callback")
	if port(redirect) != kept {
		t.Fatalf("the kept port %s was given up for %s", kept, port(redirect))
	}
	done(id, redirect)

	squatter, err := net.Listen("tcp", "127.0.0.1:"+kept)
	if err != nil {
		t.Fatal(err)
	}
	id, redirect = s.start("/callback")
	moved := port(redirect)
	done(id, redirect)
	squatter.Close()
	if moved == kept {
		t.Fatal("a port something else holds")
	}
	if id, redirect = s.start("/callback"); port(redirect) != moved {
		t.Fatalf("the new port %s is not kept: %s", moved, port(redirect))
	}
}

// A catcher nobody comes back to gives up, and says so.
func TestHerSigninGivesUp(t *testing.T) {
	defer func(d time.Duration) { signinGiveUp = d }(signinGiveUp)
	signinGiveUp = 100 * time.Millisecond
	_, o, _, _ := herOwner(t, &herSide{})
	s := newSignins(t, o)
	id, redirect := s.start("/auth/callback")
	if code, got := s.poll(id); code != 200 || !strings.Contains(fmt.Sprint(got["error"]), "in time") {
		t.Fatalf("gave up: %d %v", code, got)
	}
	if resp, err := http.Get(redirect); err == nil {
		resp.Body.Close()
		t.Fatal("it still listens")
	}
}

// The console can't sign: a connector started as a command goes through
// the console's address, which asks this device's signer and waits; why it
// didn't happen comes back in a few words.
func TestHerConsoleCommandConnector(t *testing.T) {
	_, o, _, _ := herOwner(t, &herSide{})
	k, _ := identity.GenerateKey()
	o.rootPub = k.Public()
	asked, stop := o.hub.watch(false)
	defer stop()
	base, _ := o.GetHerConsole("m_1")
	post := func(body string) (int, string) {
		resp, err := http.Post(base+"command-connector", "application/json", strings.NewReader(body))
		if err != nil {
			t.Error(err)
			return 0, ""
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	const body = `{"name":"github","command":"npx","args":["-y","server-github"],"env":{"GITHUB_TOKEN":"t0ken"},"remove":false}`
	want := &identityv1.SetHerCommandConnector{Name: "github", Command: "npx", Args: []string{"-y", "server-github"}, Env: map[string]string{"GITHUB_TOKEN": "t0ken"}}
	for _, sign := range []bool{false, true} {
		type answer struct {
			code int
			text string
		}
		done := make(chan answer, 1)
		go func() {
			code, text := post(body)
			done <- answer{code, text}
		}()
		var r *localv1.SigningRequest
		select {
		case r = <-asked:
		case <-time.After(5 * time.Second):
			t.Fatal("the signer is not asked")
		}
		cmd := &identityv1.AdminCommand{}
		if proto.Unmarshal(r.Body, cmd) != nil || cmd.TargetMachineId != "m_1" || !proto.Equal(cmd.GetSetHerCommandConnector(), want) {
			t.Fatalf("signs %v", cmd)
		}
		if !sign {
			o.hub.complete(&localv1.CompleteSigningRequestRequest{RequestId: r.RequestId, Rejected: true})
			if a := <-done; a.code != http.StatusForbidden || !strings.Contains(a.text, "declined") {
				t.Fatalf("declined: %+v", a)
			}
			continue
		}
		sig, _ := k.SignMessage(r.Message)
		o.hub.complete(&localv1.CompleteSigningRequestRequest{RequestId: r.RequestId, Signature: sig})
		// Signed, it goes nowhere: this device has no relay to send it through.
		if a := <-done; a.code < 400 || !strings.Contains(a.text, "not set up") {
			t.Fatalf("not sent: %+v", a)
		}
	}
	for _, bad := range []string{`{"name":"x"}`, `{"command":"y"}`, `nonsense`} {
		if code, text := post(bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", bad, code, text)
		}
	}
}

// Talking to Her and moving her files change nothing the screens show:
// they are not told to look again; changing Her's settings does tell them.
func TestHerWrites(t *testing.T) {
	for proc, want := range map[string]bool{
		localv1connect.LocalServiceHerCallProcedure:                false,
		localv1connect.LocalServiceHerSendProcedure:                false,
		localv1connect.LocalServiceHerUploadProcedure:              false,
		localv1connect.LocalServiceHerDownloadProcedure:            false,
		localv1connect.LocalServiceListHerWaitingProcedure:         false,
		localv1connect.LocalServiceGetHerConsoleProcedure:          false,
		localv1connect.LocalServiceDeleteHerWaitingProcedure:       false,
		localv1connect.LocalServiceGetHerConversationProcedure:     false,
		localv1connect.LocalServiceSetHerProcedure:                 true,
		localv1connect.LocalServiceSetHerAssistantModeProcedure:    true,
		localv1connect.LocalServiceSetHerCommandConnectorProcedure: true,
		localv1connect.LocalServiceAddProjectProcedure:             true,
	} {
		if writes(proc) != want {
			t.Errorf("%s: writes %v", proc, !want)
		}
	}
}

// A machine's Her is its status's while it answers; while it doesn't, as
// last seen here, after a restart too; none for one where Her was never on,
// answering or not.
func TestMachineHer(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	o := d.owner()
	running := &machinev1.HerStatus{Enabled: true, Running: true, ModelReady: true, Version: "0.3.0", Unread: 2}
	windows := &machinev1.HerStatus{Unsupported: "windows"}
	live := []*localv1.Machine{
		{MachineId: "m_her", Status: &machinev1.MachineStatus{Her: running}},
		{MachineId: "m_win", Status: &machinev1.MachineStatus{Her: windows}},
		{MachineId: "m_old", Status: &machinev1.MachineStatus{}},
	}
	o.withHer(live)
	if !proto.Equal(live[0].Her, running) || live[1].Her != nil || live[2].Her != nil {
		t.Fatalf("while they answer: %v", live)
	}
	d.Close()
	d, err = Open(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	o = d.owner()
	away := func() []*localv1.Machine {
		ms := []*localv1.Machine{{MachineId: "m_her"}, {MachineId: "m_win"}, {MachineId: "m_old"}, {MachineId: "m_new"}}
		o.withHer(ms)
		return ms
	}
	if ms := away(); !proto.Equal(ms[0].Her, running) || ms[1].Her != nil || ms[2].Her != nil || ms[3].Her != nil {
		t.Fatalf("offline: %v", ms)
	}
	off := &machinev1.HerStatus{Version: "0.3.0"} // turned off since
	o.withHer([]*localv1.Machine{{MachineId: "m_her", Status: &machinev1.MachineStatus{Her: off}}})
	if ms := away(); !proto.Equal(ms[0].Her, off) {
		t.Fatalf("turned off: %v", ms[0].Her)
	}
	o.forgetHer("m_her")
	if ms := away(); ms[0].Her != nil {
		t.Fatalf("removed: %v", ms[0].Her)
	}
}

// LocalService passes Her's calls on: a connection's events as they come,
// one request, a message.
func TestHerThroughLocalService(t *testing.T) {
	h := &herSide{}
	d, _, _, _ := herOwner(t, h)
	srv := httptest.NewUnstartedServer(d.LocalHandler())
	srv.EnableHTTP2 = true
	srv.Start()
	defer srv.Close()
	c := localv1connect.NewLocalServiceClient(srv.Client(), srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := c.HerOpen(ctx, &localv1.HerOpenRequest{MachineId: "m_1", Open: &herv1.OpenRequest{Setup: []*herv1.Request{{Method: "bots/subscribe"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() || stream.Msg().GetOpened().GetConnectionId() == "" || len(stream.Msg().GetOpened().Responses) != 1 {
		t.Fatalf("opened: %v %v", stream.Msg(), stream.Err())
	}
	stream.Close()
	res, err := c.HerCall(ctx, &localv1.HerCallRequest{MachineId: "m_1", Call: &herv1.CallRequest{Request: &herv1.Request{Method: "models/list"}}})
	if err != nil || string(res.Response.Result) != "{}" {
		t.Fatalf("call: %v %v", res, err)
	}
	if _, err := c.HerCall(ctx, &localv1.HerCallRequest{MachineId: "m_1"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("no request: %v", err)
	}
	if sent, err := c.HerSend(ctx, &localv1.HerSendRequest{MachineId: "m_1", BotId: "b1", Text: "hi"}); err != nil || !sent.Delivered {
		t.Fatalf("send: %v %v", sent, err)
	}
	stream, err = c.HerOpen(ctx, &localv1.HerOpenRequest{MachineId: "m_2"})
	if err == nil && stream.Receive() {
		t.Fatalf("opened on an unknown machine: %v", stream.Msg())
	}
	if err == nil {
		err = stream.Err()
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("an unknown machine: %v", err)
	}
}

// A pasted picture waits with its message, kept here as a file of its own
// name, goes to the assistant's folder with it, and is deleted once the
// message is delivered, or dropped.
func TestHerSendKeepsPictures(t *testing.T) {
	h := &herSide{}
	_, o, down, _ := herOwner(t, h)
	ctx := context.Background()
	down.Store(true)
	res, err := o.HerSend(ctx, &localv1.HerSendRequest{MachineId: "m_1", BotId: "b1", Pictures: []*localv1.HerPicture{{Name: "image.png", Data: []byte("png")}}})
	if err != nil || res.Waiting == nil || len(res.Waiting.LocalPaths) != 1 {
		t.Fatalf("send: %v %v", res, err)
	}
	kept := res.Waiting.LocalPaths[0]
	if b, err := os.ReadFile(kept); err != nil || string(b) != "png" || filepath.Base(kept) != "image.png" {
		t.Fatalf("kept %s: %q %v", kept, b, err)
	}
	down.Store(false)
	o.flushWaiting(ctx)
	if h.uploads["b1/image.png"] != "png" {
		t.Fatalf("uploaded: %v", h.uploads)
	}
	if got := h.prompts(t); len(got) != 1 || !strings.Contains(got[0], "/uploads/image.png") {
		t.Fatalf("handed %q", got)
	}
	if left, _ := os.ReadDir(o.herPictures()); len(left) != 0 {
		t.Fatalf("pictures left after delivery: %v", left)
	}

	down.Store(true)
	res, err = o.HerSend(ctx, &localv1.HerSendRequest{MachineId: "m_1", BotId: "b1", Text: "this one", Pictures: []*localv1.HerPicture{{Name: "a.png", Data: []byte("a")}}})
	if err != nil || res.Waiting == nil {
		t.Fatalf("send: %v %v", res, err)
	}
	if err := o.DeleteHerWaiting("m_1", res.Waiting.Id); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(o.herPictures()); len(left) != 0 {
		t.Fatalf("pictures left after dropping: %v", left)
	}
	big := make([]byte, herPictureMax+1)
	if _, err := o.HerSend(ctx, &localv1.HerSendRequest{MachineId: "m_1", BotId: "b1", Pictures: []*localv1.HerPicture{{Name: "big.png", Data: big}}}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a picture too large: %v", err)
	}
}
