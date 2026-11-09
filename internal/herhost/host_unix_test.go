//go:build unix

package herhost

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/internal/herhost/hertest"
)

// startHost runs a Host on a private her until it answers.
func startHost(t *testing.T) (h *Host, home, sock string) {
	bin := hertest.Bin(t)
	home = hertest.Home(t, bin)
	sock = shortSocket(t)
	h = New(Config{Bin: bin, Socket: sock, Env: os.Environ, Version: "test"})
	h.Start()
	t.Cleanup(h.Stop)
	eventually(t, func() bool { return h.Status().Running })
	return h, home, sock
}

// shortSocket is a socket path in a new private folder, short enough for her.
func shortSocket(t *testing.T) string {
	dir, err := os.MkdirTemp("", "hh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "her.sock")
}

// lockPID is the her holding the lock beside sock.
func lockPID(t *testing.T, sock string) int {
	var rec struct {
		PID int `json:"pid"`
	}
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(sock), "her.lock"))
	json.Unmarshal(b, &rec)
	return rec.PID
}

func call(t *testing.T, h *Host, conn, method string, params any) json.RawMessage {
	t.Helper()
	var p []byte
	if params != nil {
		p, _ = json.Marshal(params)
	}
	res, err := h.Call(context.Background(), &herv1.CallRequest{ConnectionId: conn, Request: &herv1.Request{Method: method, Params: p}})
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	if e := res.Response.Error; e != nil {
		t.Fatalf("%s: her's error %d %s", method, e.Code, e.Message)
	}
	return res.Response.Result
}

// defaultBot is the id of her's default assistant in a bots/subscribe result.
func defaultBot(t *testing.T, result []byte) string {
	var list struct {
		Bots []struct {
			ID      string `json:"id"`
			Default bool   `json:"default"`
		} `json:"bots"`
	}
	json.Unmarshal(result, &list)
	for _, b := range list.Bots {
		if b.Default {
			return b.ID
		}
	}
	t.Fatalf("no default assistant in %s", result)
	return ""
}

// saysHello is a bots/changed in which an assistant's last message is "hello".
func saysHello(method string, params []byte) bool {
	var c struct {
		Bots []bot `json:"bots"`
	}
	json.Unmarshal(params, &c)
	return method == "bots/changed" && len(c.Bots) == 1 && c.Bots[0].Last != nil && c.Bots[0].Last.From == "bot" && c.Bots[0].Last.Text == "hello"
}

const hello = "[[mock:send:hello]] hi"

// Her on: her runs and answers, its status kept current; an Open stream holds
// a connection that Calls act on, and her's answer comes as its
// notifications; the root-signed commands' ways work; her that dies is
// started again; Her off stops her.
func TestHost(t *testing.T) {
	h, home, sock := startHost(t)
	ctx := context.Background()
	if st := h.Status(); !st.Enabled || st.Version == "" || st.Error != "" {
		t.Fatalf("status %v", st)
	}
	eventually(t, func() bool { return h.Status().ModelReady })

	events := make(chan *herv1.OpenEvent, 4096)
	octx, cancel := context.WithCancel(ctx)
	defer cancel()
	openDone := make(chan error, 1)
	go func() {
		openDone <- h.Open(octx, &herv1.OpenRequest{Setup: []*herv1.Request{{Method: "bots/subscribe"}}}, func(e *herv1.OpenEvent) error {
			events <- e
			return nil
		})
	}()
	opened := (<-events).GetOpened()
	if opened.GetConnectionId() == "" || len(opened.GetResponses()) != 1 {
		t.Fatalf("opened %v", opened)
	}
	botID := defaultBot(t, opened.Responses[0].Result)
	var open struct {
		SessionID string `json:"session_id"`
	}
	json.Unmarshal(call(t, h, opened.ConnectionId, "bots/open", map[string]any{"bot_id": botID}), &open)
	call(t, h, opened.ConnectionId, "session/op", map[string]any{"session_id": open.SessionID, "op": map[string]any{"type": "prompt", "input": []any{map[string]string{"type": "text", "text": hello}}}})
	for deadline := time.After(30 * time.Second); ; {
		select {
		case e := <-events:
			if n := e.GetNotification(); n != nil && saysHello(n.Method, n.Params) {
				goto answered
			}
		case <-deadline:
			t.Fatal("no answer on the stream")
		}
	}
answered:
	eventually(t, func() bool {
		st := h.Status()
		return st.Latest.GetText() == "hello" && st.Latest.GetBotId() == botID && st.Latest.GetBotName() != "" && st.Unread > 0
	})

	call(t, h, "", "models/list", nil)
	_, err := h.Call(ctx, &herv1.CallRequest{ConnectionId: "gone", Request: &herv1.Request{Method: "models/list"}})
	code(t, err, connect.CodeNotFound)
	// Full access, as the owner's devices set it: on the conversation.
	if _, err := h.Call(ctx, &herv1.CallRequest{ConnectionId: opened.ConnectionId, Request: &herv1.Request{Method: "session/op",
		Params: []byte(`{"session_id":"` + open.SessionID + `","op":{"type":"set_permission_mode","mode":"bypass-permissions"}}`)}}); err != nil {
		t.Fatal(err)
	}

	// SetHerAssistantMode's way, which older devices sign.
	if err := h.AssistantMode(ctx, botID, "bypass-permissions"); err != nil {
		t.Fatal(err)
	}
	var snap struct {
		Snapshot struct {
			Session struct {
				PermissionMode string `json:"permission_mode"`
			} `json:"session"`
		} `json:"snapshot"`
	}
	json.Unmarshal(call(t, h, "", "bots/open", map[string]any{"bot_id": botID, "history_limit": 0}), &snap)
	if snap.Snapshot.Session.PermissionMode != "bypass-permissions" {
		t.Fatalf("mode %q", snap.Snapshot.Session.PermissionMode)
	}
	code(t, h.AssistantMode(ctx, botID, "full-auto"), connect.CodeInvalidArgument)

	// SetHerCommandConnector's way: added, changed, removed.
	servers := func() map[string]map[string]any {
		var r struct {
			Config struct {
				MCPServers map[string]map[string]any `json:"mcp_servers"`
			} `json:"config"`
		}
		json.Unmarshal(call(t, h, "", "config/read", map[string]any{}), &r)
		return r.Config.MCPServers
	}
	if err := h.CommandConnector(ctx, &identityv1.SetHerCommandConnector{Name: "notes", Command: "/bin/cat", Args: []string{"-u"}, Env: map[string]string{"NOTES": "1"}}); err != nil {
		t.Fatal(err)
	}
	if s := servers()["notes"]; s["command"] != "/bin/cat" || len(s["args"].([]any)) != 1 || s["env"].(map[string]any)["NOTES"] != "1" {
		t.Fatalf("added %v", s)
	}
	if err := h.CommandConnector(ctx, &identityv1.SetHerCommandConnector{Name: "notes", Command: "/bin/cat"}); err != nil {
		t.Fatal(err)
	}
	if s := servers()["notes"]; s["command"] != "/bin/cat" || s["args"] != nil || s["env"] != nil {
		t.Fatalf("changed %v", s)
	}
	if err := h.CommandConnector(ctx, &identityv1.SetHerCommandConnector{Name: "notes", Remove: true}); err != nil {
		t.Fatal(err)
	}
	if s, ok := servers()["notes"]; ok {
		t.Fatalf("still there: %v", s)
	}
	code(t, h.CommandConnector(ctx, &identityv1.SetHerCommandConnector{Name: "a.b", Command: "/bin/cat"}), connect.CodeInvalidArgument)

	// Files into the assistant's folder and back.
	p, err := upload(h, botID, "a.txt", 3, "abc")
	if want, _ := filepath.EvalSymlinks(filepath.Join(home, "bots", botID, "files")); err != nil || p != filepath.Join(want, "uploads", "a.txt") {
		t.Fatalf("%s, %v", p, err)
	}
	if _, data, err := download(h, botID, "uploads/a.txt"); err != nil || data != "abc" {
		t.Fatalf("%q, %v", data, err)
	}

	// her dies: the stream holding a connection ends, and her is started again.
	pid := lockPID(t, sock)
	syscall.Kill(pid, syscall.SIGKILL)
	select {
	case err := <-openDone:
		code(t, err, connect.CodeUnavailable)
	case <-time.After(30 * time.Second):
		t.Fatal("the stream outlived her")
	}
	eventually(t, func() bool { return h.Status().Running && lockPID(t, sock) != pid })
	pid = lockPID(t, sock)

	h.Stop()
	if st := h.Status(); st.Enabled || st.Running {
		t.Fatalf("status %v", st)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatal("her still runs")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket: %v", err)
	}
	_, err = h.Call(ctx, &herv1.CallRequest{Request: &herv1.Request{Method: "models/list"}})
	code(t, err, connect.CodeFailedPrecondition)
}

// A her an earlier hermeshd left running on the socket is stopped, so that
// the new one can listen there.
func TestHostStopsAStaleHer(t *testing.T) {
	bin := hertest.Bin(t)
	hertest.Home(t, bin)
	sock := shortSocket(t)
	stale := exec.Command(bin, "serve", "--listen", "unix://"+sock)
	stale.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := stale.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stale.Process.Kill() })
	exited := make(chan struct{})
	go func() {
		stale.Wait()
		close(exited)
	}()
	eventually(t, func() bool { return lockPID(t, sock) == stale.Process.Pid })

	h := New(Config{Bin: bin, Socket: sock, Env: os.Environ, Version: "test"})
	h.Start()
	t.Cleanup(h.Stop)
	eventually(t, func() bool { return h.Status().Running })
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("the stale her still runs")
	}
	if lockPID(t, sock) == stale.Process.Pid {
		t.Fatal("the socket is still the stale her's")
	}
}

// her that cannot start says why in the status, and requests say so too.
func TestHostSaysWhyHerFails(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "her")
	os.WriteFile(bin, []byte("#!/bin/sh\necho 'her: the keychain is locked' >&2\nexit 3\n"), 0o755)
	h := New(Config{Bin: bin, Socket: shortSocket(t), Env: os.Environ})
	h.Start()
	defer h.Stop()
	eventually(t, func() bool { return strings.Contains(h.Status().Error, "the keychain is locked") })
	if st := h.Status(); !st.Enabled || st.Running {
		t.Fatalf("status %v", st)
	}
	_, err := h.Call(context.Background(), &herv1.CallRequest{Request: &herv1.Request{Method: "models/list"}})
	if code(t, err, connect.CodeUnavailable); !strings.Contains(err.Error(), "the keychain is locked") {
		t.Fatal(err)
	}
}

// On a Mac her runs with computer use on unless the owner's configuration
// turns it off (HER_DESKTOP=on); hermeshd's own HER_DESKTOP wins, and
// elsewhere none is set.
func TestHostTurnsComputerUseOnOnAMac(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "her")
	os.WriteFile(bin, []byte("#!/bin/sh\necho \"her: desktop ${HER_DESKTOP-unset}\" >&2\nexit 3\n"), 0o755)
	without := func() []string {
		return slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "HER_DESKTOP=") })
	}
	for _, c := range []struct {
		env  func() []string
		want string
	}{
		{without, map[bool]string{true: "on", false: "unset"}[runtime.GOOS == "darwin"]},
		{func() []string { return append(without(), "HER_DESKTOP=off") }, "off"},
	} {
		h := New(Config{Bin: bin, Socket: shortSocket(t), Env: c.env})
		h.Start()
		eventually(t, func() bool { return strings.Contains(h.Status().Error, "her: desktop ") })
		if err := h.Status().Error; !strings.Contains(err, "her: desktop "+c.want) {
			t.Errorf("want HER_DESKTOP %s: %s", c.want, err)
		}
		h.Stop()
	}
}

// The console over HTTP, as packages/her-client's httpTransport uses it: a
// connection as server-sent events, `opened` first, then her's
// notifications as unnamed events; requests on it by Her-Connection, or on a
// connection of their own when it names none still open.
func TestConsoleWithHer(t *testing.T) {
	h, _, _ := startHost(t)
	srv := httptest.NewServer(h)
	defer srv.Close()
	res, err := http.Get(srv.URL + "/her/console/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("%d %s", res.StatusCode, res.Header)
	}
	events := bufio.NewReader(res.Body)
	// next is the next event: its name ("" for unnamed) and data.
	next := func() (string, string) {
		var name, data string
		for {
			line, err := events.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			switch line = strings.TrimSuffix(line, "\n"); {
			case line == "":
				return name, data
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			}
		}
	}
	name, data := next()
	var opened struct {
		ConnectionID string `json:"connection_id"`
	}
	if json.Unmarshal([]byte(data), &opened); name != "opened" || opened.ConnectionID == "" {
		t.Fatalf("first event %q %q", name, data)
	}
	rpc := func(conn, body string) (int, map[string]json.RawMessage) {
		req, _ := http.NewRequest("POST", srv.URL+"/her/console/rpc", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if conn != "" {
			req.Header.Set("Her-Connection", conn)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]json.RawMessage
		json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	status, out := rpc(opened.ConnectionID, `{"jsonrpc":"2.0","id":"a-7","method":"bots/subscribe"}`)
	if status != 200 || string(out["id"]) != `"a-7"` || out["result"] == nil {
		t.Fatalf("%d %s", status, out)
	}
	botID := defaultBot(t, out["result"])
	status, out = rpc(opened.ConnectionID, `{"jsonrpc":"2.0","id":8,"method":"bots/open","params":{"bot_id":"`+botID+`"}}`)
	var open struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(out["result"], &open); status != 200 || open.SessionID == "" {
		t.Fatalf("%d %s", status, out)
	}
	if status, out = rpc(opened.ConnectionID, `{"jsonrpc":"2.0","id":9,"method":"session/op","params":{"session_id":"`+open.SessionID+`","op":{"type":"prompt","input":[{"type":"text","text":"`+hello+`"}]}}}`); status != 200 {
		t.Fatalf("%d %s", status, out)
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		if time.Now().After(deadline) {
			t.Fatal("no answer on the stream")
		}
		name, data := next()
		var n struct {
			JSONRPC string          `json:"jsonrpc"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if json.Unmarshal([]byte(data), &n); name != "" || n.JSONRPC != "2.0" || n.Method == "" {
			t.Fatalf("event %q %q", name, data)
		}
		if saysHello(n.Method, n.Params) {
			break
		}
	}
	// her's own errors are answers; a connection named that is gone is replaced.
	if status, out = rpc("gone", `{"jsonrpc":"2.0","id":10,"method":"bots/open","params":{"bot_id":"no-such-bot"}}`); status != 200 || out["error"] == nil || out["result"] != nil {
		t.Fatalf("%d %s", status, out)
	}
	if status, out = rpc("", `{"jsonrpc":"2.0","id":11,"method":"models/list"}`); status != 200 || out["result"] == nil {
		t.Fatalf("%d %s", status, out)
	}
}
