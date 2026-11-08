package e2e

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
)

// Phase 2 of docs/her-server.md (§11, "everything an environment can do"),
// on an environment that is not this Mac: what the owner does in the Her
// entry and the console, through this Mac's hermeshd (LocalService and the
// console's HTTP), the relay and the end-to-end tunnel to the machine's
// hermeshd, which runs her.

// herEnvironment is Her on one of the owner's machines other than this Mac:
// a second hermeshd on this computer standing in for the cloud machine, or
// with HERMESH_DOCKER=1 a Linux machine in Docker (startLinuxHer).
type herEnvironment struct {
	w         *ownerMac
	c         localv1connect.LocalServiceClient
	machine   string // its machine id
	home      string // her's home there
	console   string // its console on this Mac (GetHerConsole), ending in "/"
	bot       herBot // Her, its default assistant
	container string // the Linux machine's container; "" for the hermeshd on this computer
	mac       string // this computer, as the machine reaches it
	mcpServer string // her-mcp-server in the container, when there is a Linux one
}

// newHerEnvironment pairs the machine to the owner Mac, as Add Machine pairs
// a server, turns Her on there (signed by the test's signer) and opens its
// console.
func newHerEnvironment(t *testing.T) *herEnvironment {
	t.Helper()
	if testing.Short() {
		t.Skip("e2e")
	}
	docker := os.Getenv("HERMESH_DOCKER") == "1"
	var herLinux string
	if docker {
		herLinux = herLinuxBinary(t)
	}
	her, mockBin := herBinaries(t)
	mock := startHerMock(t, mockBin)
	w := newOwnerMac(t, nil, &localv1.GrantSpec{})
	env := &herEnvironment{w: w, c: w.app.client, mac: "127.0.0.1"}
	ctx := context.Background()
	if docker {
		// With her-mcp-server, when there is a Linux one beside her, for
		// connectors started as commands.
		var args []string
		p := filepath.Join(filepath.Dir(herLinux), "her-mcp-server")
		if _, err := os.Stat(p); err == nil {
			env.mcpServer = "/opt/her/her-mcp-server"
			args = []string{"-v", p + ":" + env.mcpServer + ":ro"}
		}
		env.machine, env.container = startLinuxHer(t, w, herLinux, mock, herConsole(t), args...)
		env.home, env.mac = "/data/her", "host.docker.internal"
	} else {
		env.machine, env.home = startHerServer(t, w, her, mock)
	}

	turnHerOn(t, env.c, env.machine)
	env.bot = defaultBot(t, env.c, env.machine)
	con, err := env.c.GetHerConsole(ctx, &localv1.GetHerConsoleRequest{MachineId: env.machine})
	if err != nil {
		t.Fatal(err)
	}
	env.console = con.Url
	return env
}

// startHerServer starts a second hermeshd on this computer, standing in for
// the cloud machine, and pairs it to the owner Mac as Add Machine pairs a
// server. It returns the machine's id and her's home there, once it is
// online.
func startHerServer(t *testing.T, w *ownerMac, her string, mock *herMock) (machineID, home string) {
	t.Helper()
	// Its home as the machine names it, symbolic links resolved (/private/var
	// on macOS).
	home, err := filepath.EvalSymlinks(herHome(t, mock.models))
	if err != nil {
		t.Fatal(err)
	}
	c := w.app.client
	daemon := filepath.Join(w.bin, "hermeshd")
	data := filepath.Join(w.tmp, "her-server")
	pair := start(t, "pair her-server", nil, daemon, "pair", w.addr, "--name", "her-server", "--no-service", "--data-dir", data)
	approved, err := c.ApproveMachinePairing(context.Background(), &localv1.ApproveMachinePairingRequest{Code: pair.pairingCode(t), MachineName: "her-server"})
	if err != nil {
		t.Fatal(err)
	}
	if code := pair.wait(t, 30*time.Second); code != 0 {
		t.Fatalf("pair: %s", pair.output())
	}
	server := start(t, "her-server", herEnv(her, herConsole(t), home, mock.base), daemon, "run", "--data-dir", data)
	w.logs = append(w.logs, server)
	// Stopped as a service is, so that it stops her: killed, it would leave
	// her running, in a process group of its own.
	t.Cleanup(server.stop)
	eventually(t, 30*time.Second, "her-server online", func() bool {
		m := listedMachine(t, c, approved.MachineId)
		return m != nil && m.Online && m.Status != nil
	})
	return approved.MachineId, home
}

// onMachine runs a command on the machine and returns its output.
func (env *herEnvironment) onMachine(args ...string) ([]byte, error) {
	if env.container != "" {
		args = append([]string{"docker", "exec", env.container}, args...)
	}
	return exec.Command(args[0], args[1:]...).Output()
}

// herMCPServer is her-mock's MCP test server, her-mcp-server, which
// `cargo build -p her-mock` builds beside her-mock.
func herMCPServer(t *testing.T) string {
	t.Helper()
	_, mock := herBinaries(t)
	p := filepath.Join(filepath.Dir(mock), "her-mcp-server")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("no her-mcp-server beside her-mock (%v): cargo build -p her-mock in her/", err)
	}
	return p
}

// mcpServerThere is her-mcp-server where the machine can start it, for a
// connector started as a command: this computer's, or the Linux one in the
// container.
func (env *herEnvironment) mcpServerThere(t *testing.T) string {
	t.Helper()
	if env.container == "" {
		return herMCPServer(t)
	}
	if env.mcpServer == "" {
		t.Skip("no her-mcp-server beside HERMESH_HER_LINUX: build it for Linux with her (cargo build -p her-mock --bin her-mcp-server)")
	}
	return env.mcpServer
}

// browser is the owner's browser on this Mac. The Linux machine in Docker
// reaches this computer as host.docker.internal, so services here give it
// addresses there; the browser follows them to this computer, as a public
// name would lead both to the same service.
var browser = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
	if host, port, err := net.SplitHostPort(addr); err == nil && host == "host.docker.internal" {
		addr = net.JoinHostPort("127.0.0.1", port)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}}}

// askHer sends Her a message in her conversation, as the Her entry does,
// approves what she asks to do on the way, and waits for her to send the
// owner a message with want in it. It returns the tools she asked to use
// and what she sent.
func askHer(t *testing.T, env *herEnvironment, text, want string) (asked []string, said string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	stream, op, session := openAssistant(t, ctx, env)
	defer stream.Close()
	if res, err := env.c.HerSend(ctx, &localv1.HerSendRequest{MachineId: env.machine, BotId: env.bot.ID, Text: text}); err != nil || !res.Delivered {
		t.Fatalf("HerSend: %v %v", res, err)
	}
	e := untilEvent(t, stream, "Her's answer with "+want, func(e herEvent) bool {
		if e.Type == "approval_requested" {
			asked = append(asked, e.Name)
			herCall(t, env, op.ConnectionId, "session/op", map[string]any{"session_id": session,
				"op": map[string]any{"type": "approval", "call_id": e.CallID, "decision": map[string]string{"type": "approve"}}})
		}
		return e.sentText(want)
	})
	return asked, e.Args.Text
}

// openAssistant opens an assistant's conversation as the Her entry follows
// it: a HerOpen stream holding a connection with bots/open, without the
// events that only animate a reply. It returns the conversation too.
func openAssistant(t *testing.T, ctx context.Context, env *herEnvironment) (*connect.ServerStreamForClient[herv1.OpenEvent], *herv1.Opened, string) {
	t.Helper()
	stream, op := herOpen(t, ctx, env.c, env.machine, herSetup("bots/open", map[string]any{"bot_id": env.bot.ID,
		"events": map[string]any{"exclude": []string{"text_delta", "thinking_delta", "tool_call_delta", "tool_progress"}}}))
	var conv struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(op.Responses[0].Result, &conv); conv.SessionID == "" {
		t.Fatalf("bots/open: %s", op.Responses[0].Result)
	}
	return stream, op, conv.SessionID
}

// herEvent is one of her's session events, as session/event carries it.
type herEvent struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Name   string `json:"name"`
	Args   struct {
		Text string `json:"text"`
	} `json:"args"`
	Result struct {
		ToolName string          `json:"tool_name"`
		IsError  bool            `json:"is_error"`
		Details  json.RawMessage `json:"details"`
	} `json:"result"`
}

// sentText: whether e is the assistant sending the owner a message with s
// in it.
func (e herEvent) sentText(s string) bool {
	return e.Type == "tool_started" && e.Name == "send_to_user" && strings.Contains(e.Args.Text, s)
}

// untilEvent reads a HerOpen stream until a session event that done
// accepts.
func untilEvent(t *testing.T, stream *connect.ServerStreamForClient[herv1.OpenEvent], what string, done func(herEvent) bool) herEvent {
	t.Helper()
	for stream.Receive() {
		n := stream.Msg().GetNotification()
		if n.GetMethod() != "session/event" {
			continue
		}
		var p struct {
			Event herEvent `json:"event"`
		}
		if json.Unmarshal(n.Params, &p) == nil && done(p.Event) {
			return p.Event
		}
	}
	t.Fatalf("no %s: %v", what, stream.Err())
	return herEvent{}
}

// herCall sends her one request through LocalService, on the connection
// conn ("": one of its own), and returns her's result.
func herCall(t *testing.T, env *herEnvironment, conn, method string, params any) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := env.c.HerCall(ctx, &localv1.HerCallRequest{MachineId: env.machine,
		Call: &herv1.CallRequest{ConnectionId: conn, Request: herSetup(method, params)}})
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	if e := res.Response.GetError(); e != nil {
		t.Fatalf("%s: her: %s", method, e.Message)
	}
	return res.Response.GetResult()
}

// consolePage is the console as its page talks to her (packages/her-client's
// httpTransport): server-sent events at `events`, whose first event names
// the connection, and requests on that connection at `rpc`.
type consolePage struct {
	t     *testing.T
	base  string
	conn  string
	notes chan herNote
}

// herNote is a notification of her's.
type herNote struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// consoleAnswer is what the console's rpc answered: her's response, or
// hermeshd's refusal (a status other than 200, with a short text).
type consoleAnswer struct {
	status int
	text   string
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// openConsolePage opens the console at base as its page does.
func openConsolePage(t *testing.T, base string) *consolePage {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"events", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("the console's events: %d %s", res.StatusCode, b)
	}
	r := bufio.NewReader(res.Body)
	event, data, err := readEvent(r)
	var opened struct {
		ConnectionID string `json:"connection_id"`
	}
	if json.Unmarshal([]byte(data), &opened); err != nil || event != "opened" || opened.ConnectionID == "" {
		t.Fatalf("the console's first event: %q %q %v", event, data, err)
	}
	p := &consolePage{t: t, base: base, conn: opened.ConnectionID, notes: make(chan herNote, 1024)}
	go func() {
		defer close(p.notes)
		defer res.Body.Close()
		for {
			event, data, err := readEvent(r)
			if err != nil {
				return
			}
			var n herNote
			if event == "" && json.Unmarshal([]byte(data), &n) == nil {
				select {
				case p.notes <- n:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return p
}

// readEvent reads one server-sent event: its name ("" for none) and data.
func readEvent(r *bufio.Reader) (event, data string, err error) {
	var lines []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", "", err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "" && (event != "" || lines != nil):
			return event, strings.Join(lines, "\n"), nil
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
}

// rpc sends one JSON-RPC request on the page's connection.
func (p *consolePage) rpc(method string, params any) consoleAnswer {
	p.t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"rpc", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Her-Connection", p.conn)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatalf("%s: %v", method, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	a := consoleAnswer{status: res.StatusCode}
	if res.StatusCode != http.StatusOK {
		a.text = string(b)
	} else if err := json.Unmarshal(b, &a); err != nil {
		p.t.Fatalf("%s: %v %s", method, err, b)
	}
	return a
}

// call is rpc that her answered with a result, decoded into out (unless nil).
func (p *consolePage) call(method string, params, out any) json.RawMessage {
	p.t.Helper()
	a := p.rpc(method, params)
	if a.status != http.StatusOK || a.Error != nil || a.Result == nil {
		p.t.Fatalf("%s: %d %s %+v", method, a.status, a.text, a.Error)
	}
	if out != nil {
		if err := json.Unmarshal(a.Result, out); err != nil {
			p.t.Fatalf("%s: %v %s", method, err, a.Result)
		}
	}
	return a.Result
}

// until waits for a notification that match accepts.
func (p *consolePage) until(what string, timeout time.Duration, match func(herNote) bool) herNote {
	p.t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case n, ok := <-p.notes:
			if !ok {
				p.t.Fatalf("the console's events ended before %s", what)
			}
			if match(n) {
				return n
			}
		case <-deadline:
			p.t.Fatalf("no %s on the console's events within %s", what, timeout)
		}
	}
}

// config is her's merged configuration, as config/read gives it.
func (p *consolePage) config() map[string]any {
	p.t.Helper()
	var read struct {
		Config map[string]any `json:"config"`
	}
	p.call("config/read", map[string]any{}, &read)
	return read.Config
}

// mcpChecked is what mcp/check says of a connector.
type mcpChecked struct {
	Server struct {
		Status struct {
			State string `json:"state"`
			Error string `json:"error"`
		} `json:"status"`
		AuthState string `json:"auth_state"`
	} `json:"server"`
	Tools []struct {
		Name string `json:"name"`
	} `json:"tools"`
}

func (c mcpChecked) has(tool string) bool {
	for _, t := range c.Tools {
		if t.Name == tool {
			return true
		}
	}
	return false
}

// pngOf is a picture w by h pixels.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// zipOf is a zip archive of files, by their paths in it.
func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, content := range files {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(f, content)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestHerFilesBothWays: files dragged into a conversation go up into the
// assistant's folder on the machine and the message names them; Her sends
// files back, the picture with what the app needs to preview it, and the
// app saves them on this Mac, a second save of the same name numbered
// (docs/her-server.md §9.1 "Files", §11 phase 2.1).
func TestHerFilesBothWays(t *testing.T) {
	env := newHerEnvironment(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	local := t.TempDir()
	notes, pic := filepath.Join(local, "notes.txt"), filepath.Join(local, "pic.png")
	sent := map[string][]byte{"notes.txt": []byte("notes for Her\n"), "pic.png": pngOf(t, 3, 2)}
	for name, b := range sent {
		if err := os.WriteFile(filepath.Join(local, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stream, op, session := openAssistant(t, ctx, env)
	defer stream.Close()

	// A message with two files: they go up first, and the message names where
	// they landed. her-mock's Her answers with the message she got.
	res, err := env.c.HerSend(ctx, &localv1.HerSendRequest{MachineId: env.machine, BotId: env.bot.ID, Text: "[[mock:send]] Two files for you.",
		LocalPaths: []string{notes, pic}})
	if err != nil || !res.Delivered {
		t.Fatalf("HerSend with files: %v %v", res, err)
	}
	said := untilEvent(t, stream, "Her's answer naming the files", func(e herEvent) bool { return e.sentText("Attached files:") })
	named := regexp.MustCompile(`(?m)^- (/.+)$`).FindAllStringSubmatch(said.Args.Text, -1)
	if len(named) != 2 || !strings.HasSuffix(named[0][1], "/uploads/notes.txt") || !strings.HasSuffix(named[1][1], "/uploads/pic.png") {
		t.Fatalf("the files the message names: %q", said.Args.Text)
	}

	// her finds them in the assistant's folder, and they come back down byte
	// for byte by the paths the message named.
	var listed struct {
		Entries []struct {
			Name string `json:"name"`
			Size int    `json:"size"`
		} `json:"entries"`
	}
	json.Unmarshal(herCall(t, env, op.ConnectionId, "files/list", map[string]string{"session_id": session, "path": "uploads"}), &listed)
	found := map[string]int{}
	for _, e := range listed.Entries {
		found[e.Name] = e.Size
	}
	if found["notes.txt"] != len(sent["notes.txt"]) || found["pic.png"] != len(sent["pic.png"]) {
		t.Fatalf("the assistant's uploads, as her lists them: %+v", listed.Entries)
	}
	back := t.TempDir()
	for _, m := range named {
		down, err := env.c.HerDownload(ctx, &localv1.HerDownloadRequest{MachineId: env.machine, BotId: env.bot.ID, Path: m[1], LocalDir: back})
		if err != nil {
			t.Fatalf("download %s: %v", m[1], err)
		}
		if b, _ := os.ReadFile(down.LocalPath); !bytes.Equal(b, sent[filepath.Base(m[1])]) {
			t.Fatalf("%s came back as %s, %d bytes", m[1], down.LocalPath, len(b))
		}
	}

	// Asked for them back, Her sends them (her-mock's attach): each where it is
	// on the machine, the picture with its size, as the app previews it.
	if res, err := env.c.HerSend(ctx, &localv1.HerSendRequest{MachineId: env.machine, BotId: env.bot.ID,
		Text: "[[mock:attach:uploads/notes.txt,uploads/pic.png]] Send them back, please."}); err != nil || !res.Delivered {
		t.Fatalf("HerSend: %v %v", res, err)
	}
	var attachment struct {
		Files []struct {
			Path   string `json:"path"`
			Size   int    `json:"size"`
			Image  bool   `json:"image"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"files"`
	}
	untilEvent(t, stream, "Her's files", func(e herEvent) bool {
		return e.Type == "tool_completed" && e.Result.ToolName == "send_to_user" && !e.Result.IsError &&
			json.Unmarshal(e.Result.Details, &attachment) == nil && len(attachment.Files) > 0
	})
	f := attachment.Files
	if len(f) != 2 || filepath.Base(f[0].Path) != "notes.txt" || f[0].Image || f[0].Size != len(sent["notes.txt"]) ||
		filepath.Base(f[1].Path) != "pic.png" || !f[1].Image || f[1].Width != 3 || f[1].Height != 2 || f[1].Size != len(sent["pic.png"]) {
		t.Fatalf("the files Her sent: %+v", f)
	}

	// Saved on this Mac, under its name; saved again, with a number added.
	saved := t.TempDir()
	for _, want := range []string{"pic.png", "pic 2.png"} {
		down, err := env.c.HerDownload(ctx, &localv1.HerDownloadRequest{MachineId: env.machine, BotId: env.bot.ID, Path: f[1].Path, LocalDir: saved})
		if err != nil {
			t.Fatalf("saving the picture: %v", err)
		}
		if b, _ := os.ReadFile(down.LocalPath); down.LocalPath != filepath.Join(saved, want) || !bytes.Equal(b, sent["pic.png"]) {
			t.Fatalf("saved as %s, %d bytes; want %s", down.LocalPath, len(b), want)
		}
	}
}

// TestHerConnectorSignIn: a connector that needs a sign-in, added by its
// address in the console, signed in from this Mac: the sign-in starts on the
// Her Server, the browser comes back to this Mac's hermeshd, and the console
// brings what the redirect carried to her, who exchanges it and keeps the
// token; then Her uses it in a conversation (docs/her-server.md §5.4, §11
// phase 2.2–2.3).
func TestHerConnectorSignIn(t *testing.T) {
	mcpServer := herMCPServer(t)
	env := newHerEnvironment(t)
	// The connector's service, with OAuth: it registers a client for the
	// redirect and approves at once, standing in for the person. It runs on
	// this computer, under the name the machine reaches it by.
	tracker := start(t, "tracker", nil, mcpServer, "--http", "127.0.0.1:0", "--public-host", env.mac, "--oauth", "--no-metadata-document", "--name", "tracker")
	var address string
	eventually(t, 20*time.Second, "the tracker's address", func() bool {
		address = regexp.MustCompile(`http://\S+/mcp`).FindString(tracker.output())
		return address != ""
	})
	page := openConsolePage(t, env.console)
	page.call("mcp/list", map[string]any{}, nil) // the Connectors section, which hears of changes

	// Added by its address, it needs a sign-in.
	page.call("config/write", map[string]any{"layer": "user", "edits": []any{map[string]any{"key_path": "mcp_servers.tracker",
		"value": map[string]any{"url": address, "auth": "oauth"}}}}, nil)
	var before mcpChecked
	if page.call("mcp/check", map[string]string{"name": "tracker"}, &before); before.Server.AuthState != "needs_login" {
		t.Fatalf("before signing in: %+v", before)
	}

	// The browser is to come back to this Mac, where hermeshd catches it.
	code, body := httpDo(t, http.MethodPost, env.console+"signin", `{"path":"/callback"}`)
	var signin struct {
		ID          string `json:"id"`
		RedirectURI string `json:"redirect_uri"`
	}
	if json.Unmarshal([]byte(body), &signin); code != http.StatusOK || !strings.HasPrefix(signin.RedirectURI, "http://127.0.0.1:") {
		t.Fatalf("signin: %d %s", code, body)
	}
	var login struct {
		AuthorizeURL string `json:"authorize_url"`
		LoginID      string `json:"login_id"`
	}
	page.call("mcp/login", map[string]string{"name": "tracker", "redirect_uri": signin.RedirectURI}, &login)

	// The browser: the service approves and redirects to this Mac's catcher,
	// which tells the person to go back to Hermesh.
	res, err := browser.Get(login.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	landed, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(landed), "go back to Hermesh") || !strings.HasPrefix(res.Request.URL.String(), signin.RedirectURI+"?") {
		t.Fatalf("the browser: %d at %s: %s", res.StatusCode, res.Request.URL, landed)
	}
	code, body = httpDo(t, http.MethodGet, env.console+"signin/"+signin.ID, "")
	var caught struct {
		Code  string `json:"code"`
		State string `json:"state"`
		Iss   string `json:"iss"`
	}
	if json.Unmarshal([]byte(body), &caught); code != http.StatusOK || caught.Code == "" || caught.State == "" || caught.Iss == "" {
		t.Fatalf("what the redirect brought: %d %s", code, body)
	}

	// A state that isn't this sign-in's is refused, and the sign-in waits on:
	// the right one then completes it.
	complete := func(state string) map[string]string {
		return map[string]string{"login_id": login.LoginID, "code": caught.Code, "state": state, "iss": caught.Iss}
	}
	if a := page.rpc("mcp/login_complete", complete("not-"+caught.State)); a.status != http.StatusOK || a.Error == nil || a.Error.Code != -32602 {
		t.Fatalf("a wrong state: %d %s %s %+v", a.status, a.text, a.Result, a.Error)
	}
	page.call("mcp/login_complete", complete(caught.State), nil)
	ended := page.until("the sign-in's end", 30*time.Second, func(n herNote) bool {
		return n.Method == "mcp/changed" && bytes.Contains(n.Params, []byte(login.LoginID))
	})
	if bytes.Contains(ended.Params, []byte("login_error")) {
		t.Fatalf("the sign-in failed: %s", ended.Params)
	}

	// Signed in: the connector connects with the token, which stays on the
	// Her Server, in its credential store (a file in this test).
	var after mcpChecked
	if page.call("mcp/check", map[string]string{"name": "tracker"}, &after); after.Server.Status.State != "ok" || after.Server.AuthState != "logged_in" || !after.has("echo") {
		t.Fatalf("after signing in: %+v", after)
	}
	if b, _ := env.onMachine("cat", filepath.Join(env.home, "credentials.toml")); !bytes.Contains(b, []byte("mcp-oauth.tracker")) {
		t.Fatalf("no token in the Her Server's credential store: %q", b)
	}

	// Her uses it in a conversation: she calls its tool, which is not
	// read-only, once the owner approves, and her answer carries what the
	// tool gave.
	asked, said := askHer(t, env, "[[mock:mcp:tracker:Apollo]] What is Apollo's release code?", "mcp done: Release code for Apollo: FX-")
	if !slices.Equal(asked, []string{"call_tool"}) {
		t.Fatalf("Her asked to use %v before answering %q", asked, said)
	}
}

// TestHerCommandConnector: the console can't add a connector started as a
// command (it runs a program on the Her Server) through her's JSON-RPC; it
// goes through this Mac's hermeshd, which has its signer sign it, and her
// then starts it and speaks MCP with it (docs/her-server.md §8).
func TestHerCommandConnector(t *testing.T) {
	env := newHerEnvironment(t)
	mcpServer := env.mcpServerThere(t)
	page := openConsolePage(t, env.console)
	connectors := func() map[string]any {
		s, _ := page.config()["mcp_servers"].(map[string]any)
		return s
	}

	// Straight through the console's JSON-RPC: refused, and not written.
	a := page.rpc("config/write", map[string]any{"layer": "user", "edits": []any{map[string]any{"key_path": "mcp_servers.notes",
		"value": map[string]any{"command": mcpServer, "args": []string{"--name", "notes"}}}}})
	if a.status != http.StatusForbidden || !strings.Contains(a.text, "root key") {
		t.Fatalf("a command connector through rpc: %d %s %s", a.status, a.text, a.Result)
	}
	if _, ok := connectors()["notes"]; ok {
		t.Fatalf("written anyway: %v", connectors())
	}

	// Through hermeshd's route: signed here, applied there.
	before := len(env.w.app.signedTypes())
	if code, text := httpDo(t, http.MethodPost, env.console+"command-connector",
		`{"name":"notes","command":"`+mcpServer+`","args":["--name","notes"],"env":{"NOTES_MOOD":"calm"}}`); code != http.StatusOK {
		t.Fatalf("command connector: %d %s", code, text)
	}
	if got := env.w.app.signedTypes()[before:]; !slices.Equal(got, []string{"hermesh.identity.v1.AdminCommand"}) {
		t.Fatalf("signed: %v", got)
	}
	notes, _ := connectors()["notes"].(map[string]any)
	if b, _ := json.Marshal(notes); notes["command"] != mcpServer || !strings.Contains(string(b), `"args":["--name","notes"]`) ||
		!strings.Contains(string(b), `"NOTES_MOOD":"calm"`) {
		t.Fatalf("the connector in her's configuration: %s", b)
	}
	var checked mcpChecked
	if page.call("mcp/check", map[string]string{"name": "notes"}, &checked); checked.Server.Status.State != "ok" || !checked.has("echo") {
		t.Fatalf("her starting the connector: %+v", checked)
	}

	// Removed the same way.
	if code, text := httpDo(t, http.MethodPost, env.console+"command-connector", `{"name":"notes","remove":true}`); code != http.StatusOK {
		t.Fatalf("removing it: %d %s", code, text)
	}
	if _, ok := connectors()["notes"]; ok {
		t.Fatalf("still there: %v", connectors())
	}
}

// TestHerSkillUpload: a skill, zipped on this Mac, uploaded through the
// console and installed on the Her Server: checked first without writing,
// then installed, listed, used by Her in a conversation, and turned off
// (docs/her-server.md §10 "Skills").
func TestHerSkillUpload(t *testing.T) {
	env := newHerEnvironment(t)
	page := openConsolePage(t, env.console)
	type skill struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		Scope string `json:"scope"`
		State string `json:"state"`
	}
	listed := func() map[string]skill {
		var list struct {
			Skills []skill `json:"skills"`
		}
		page.call("skills/list", map[string]any{}, &list)
		out := map[string]skill{}
		for _, s := range list.Skills {
			out[s.Name] = s
		}
		return out
	}
	if _, ok := listed()["weekly-report"]; ok {
		t.Fatal("installed before the upload")
	}

	archive := zipOf(t, map[string]string{
		"weekly-report/SKILL.md":             "---\nname: weekly-report\ndescription: Writes the weekly report from the week's notes.\n---\n\nRead `references/format.md`, then write the report.\n",
		"weekly-report/references/format.md": "# Format\n\nDone, doing, blocked.\n",
	})
	req, _ := http.NewRequest(http.MethodPost, env.console+"upload", bytes.NewReader(archive))
	req.Header.Set("Her-File-Name", "weekly-report.zip")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var up struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(b, &up); res.StatusCode != http.StatusOK || !strings.HasPrefix(up.Path, env.home) || !strings.HasSuffix(up.Path, "weekly-report.zip") {
		t.Fatalf("upload: %d %s", res.StatusCode, b)
	}

	// Checked first: what it would install, and where; nothing written.
	var dry struct {
		Skill  skill  `json:"skill"`
		Target string `json:"target"`
	}
	page.call("skills/install", map[string]any{"from": up.Path, "scope": "user", "dry_run": true}, &dry)
	target := filepath.Join(env.home, "skills", "weekly-report")
	if dry.Skill.Name != "weekly-report" || dry.Target != target {
		t.Fatalf("dry run: %+v", dry)
	}
	if _, err := env.onMachine("test", "-e", target); err == nil {
		t.Fatalf("a dry run wrote %s", target)
	}

	// Installed, for every assistant of the environment.
	var installed struct {
		Skill skill `json:"skill"`
	}
	page.call("skills/install", map[string]any{"from": up.Path, "scope": "user"}, &installed)
	s := listed()["weekly-report"]
	if s.Path != filepath.Join(target, "SKILL.md") || s.Scope != "user" || s.State != "active" {
		t.Fatalf("the installed skill: %+v (installed as %+v)", s, installed.Skill)
	}
	if b, err := env.onMachine("cat", filepath.Join(target, "references", "format.md")); err != nil || !bytes.Contains(b, []byte("blocked")) {
		t.Fatalf("its files: %q %v", b, err)
	}

	// Her uses it in a conversation: she reads its SKILL.md, then the file it
	// names, and her answer carries what the skill gave.
	if asked, said := askHer(t, env, "[[mock:skill:weekly-report]] Write this week's report.", "skill done: "); !strings.Contains(said, "Done, doing, blocked.") {
		t.Fatalf("Her's answer: %q (asked to use %v)", said, asked)
	}

	// Turned off.
	page.call("skills/set_enabled", map[string]any{"path": s.Path, "enabled": false}, nil)
	if s := listed()["weekly-report"]; s.State != "disabled" {
		t.Fatalf("turned off: %+v", s)
	}
}

// TestHerRoutine: the owner asks Her for a reminder a minute later; Her
// makes a routine, once the owner approves it; it runs by itself, and its
// report wakes Her, who passes it on to the owner, in the conversation and
// in the machine's status the sidebar and notifications follow
// (docs/her-server.md §11 phase 2.4).
func TestHerRoutine(t *testing.T) {
	env := newHerEnvironment(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	stream, op, session := openAssistant(t, ctx, env)
	defer stream.Close()

	if res, err := env.c.HerSend(ctx, &localv1.HerSendRequest{MachineId: env.machine, BotId: env.bot.ID,
		Text: "[[mock:routine:Stretch|in 1m|say:Time to stretch.]] Remind me to stretch in a minute."}); err != nil || !res.Delivered {
		t.Fatalf("HerSend: %v %v", res, err)
	}
	var asked bool
	var routine string
	untilEvent(t, stream, "Her saying the routine is saved", func(e herEvent) bool {
		switch {
		case e.Type == "approval_requested" && e.Name == "manage_routine":
			asked = true
			herCall(t, env, op.ConnectionId, "session/op", map[string]any{"session_id": session,
				"op": map[string]any{"type": "approval", "call_id": e.CallID, "decision": map[string]string{"type": "approve"}}})
		case e.Type == "tool_completed" && e.Result.ToolName == "manage_routine":
			var d struct {
				RoutineID string `json:"routine_id"`
			}
			json.Unmarshal(e.Result.Details, &d)
			routine = d.RoutineID
		}
		return e.sentText(`Saved routine "Stretch"`)
	})
	if !asked || routine == "" {
		t.Fatalf("asked to approve: %v; the routine made: %q", asked, routine)
	}

	// About a minute later, with nobody asking: what the run found, as Her
	// passes it on.
	untilEvent(t, stream, "the routine's report, passed on", func(e herEvent) bool {
		return e.Type == "tool_started" && e.Name == "send_to_user" && e.Args.Text == "Time to stretch."
	})
	eventually(t, 30*time.Second, "the report as Her's latest message", func() bool {
		l := listedMachine(t, env.c, env.machine).GetHer().GetLatest()
		return l.GetBotId() == env.bot.ID && l.GetKind() == machinev1.HerLatestKind_HER_LATEST_KIND_MESSAGE &&
			l.GetText() == "Time to stretch."
	})
	var runs struct {
		Runs []struct {
			Outcome   string `json:"outcome"`
			Report    string `json:"report"`
			Delivered string `json:"delivered"`
		} `json:"runs"`
	}
	ran := func() bool {
		json.Unmarshal(herCall(t, env, "", "bots/routine_runs", map[string]string{"bot_id": env.bot.ID, "routine_id": routine}), &runs)
		return len(runs.Runs) == 1 && runs.Runs[0].Outcome == "reported" && runs.Runs[0].Report == "Time to stretch." && runs.Runs[0].Delivered != ""
	}
	for deadline := time.Now().Add(30 * time.Second); !ran(); time.Sleep(time.Second) {
		if time.Now().After(deadline) {
			t.Fatalf("the routine's runs: %+v", runs.Runs)
		}
	}
}

// TestHerConsoleSettings: what the console may write in her's settings
// through its JSON-RPC, and what needs the owner's root key because it
// would let Her do more on the machine (docs/her-server.md §8).
func TestHerConsoleSettings(t *testing.T) {
	env := newHerEnvironment(t)
	page := openConsolePage(t, env.console)
	for _, s := range []struct {
		key   string
		value any
		ok    bool
	}{
		{"instructions.extra", "Answer in plain English.", true},
		{"model", "mock/mock-1", true},
		{"permission_mode", "accept-edits", true},
		{"permission_mode", "bypass-permissions", false},
		{"tools.bash", map[string]any{"pass_provider_keys": true}, false},
		{"system_prompt_file", "/tmp/another-prompt.md", false},
	} {
		a := page.rpc("config/write", map[string]any{"layer": "user", "edits": []any{map[string]any{"key_path": s.key, "value": s.value}}})
		switch {
		case s.ok && (a.status != http.StatusOK || a.Error != nil):
			t.Errorf("%s = %v refused: %d %s %+v", s.key, s.value, a.status, a.text, a.Error)
		case !s.ok && (a.status != http.StatusForbidden || !strings.Contains(a.text, "root key")):
			t.Errorf("%s = %v not refused: %d %s %s", s.key, s.value, a.status, a.text, a.Result)
		}
	}

	// What was taken is her's configuration now; nothing refused reached it.
	cfg := page.config()
	instructions, _ := cfg["instructions"].(map[string]any)
	tools, _ := cfg["tools"].(map[string]any)
	if instructions["extra"] != "Answer in plain English." || cfg["model"] != "mock/mock-1" || cfg["permission_mode"] != "accept-edits" ||
		tools["bash"] != nil || cfg["system_prompt_file"] != nil {
		b, _ := json.Marshal(cfg)
		t.Fatalf("her's configuration: %s", b)
	}
}
