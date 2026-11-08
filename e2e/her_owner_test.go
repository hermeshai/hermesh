package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
)

// Her on the owner's machines (docs/her-server.md), as the Mac app uses it:
// her with a home of its own, and her-mock as its model service.

// herBinaries are her and her-mock for this computer: HERMESH_HER and
// HERMESH_HER_MOCK, else the debug builds in her/target.
func herBinaries(t *testing.T) (her, mock string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("her has no Windows build")
	}
	find := func(env, name string) string {
		p := os.Getenv(env)
		if p == "" {
			p, _ = filepath.Abs(filepath.Join("her", "target", "debug", name))
		}
		if _, err := os.Stat(p); err != nil {
			t.Skipf("no %s (%v): build her (cargo build in her/) or set %s", name, err, env)
		}
		return p
	}
	return find("HERMESH_HER", "her"), find("HERMESH_HER_MOCK", "her-mock")
}

// herMock is her-mock serving models: base is its address, models the
// models.toml naming it, as it printed it.
type herMock struct {
	base, models string
}

func startHerMock(t *testing.T, bin string) *herMock {
	t.Helper()
	p := start(t, "her-mock", nil, bin, "127.0.0.1:0")
	eventually(t, 20*time.Second, "her-mock up", func() bool { return strings.Contains(p.output(), "cost = ") })
	_, models, _ := strings.Cut(p.output(), "\n") // after its first line
	m := regexp.MustCompile(`base_url = "(http://[^/"]+)`).FindStringSubmatch(models)
	if m == nil {
		t.Fatalf("her-mock printed no address:\n%s", p.output())
	}
	return &herMock{base: m[1], models: models}
}

// herHome is a home for her: the mock's models, its model the default.
func herHome(t *testing.T, models string) string {
	t.Helper()
	home := t.TempDir()
	for name, content := range map[string]string{"models.toml": models, "config.toml": "model = \"mock/mock-1\"\n"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// herConsole is a console's files: an index.html that says it is the test's.
func herConsole(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>Her console</title>the e2e console\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// herEnv is what a hermeshd needs to run her: her, the console, and
// her's own settings for a test (her home, credentials in a file, no
// browser, English, the mock's model catalog).
func herEnv(her, console, home, mockBase string) []string {
	return []string{"HERMESH_HER_BIN=" + her, "HERMESH_HER_CONSOLE=" + console, "HER_HOME=" + home, "HER_CREDENTIAL_STORE=file",
		"HER_NO_BROWSER=1", "HER_LANG=en", "HER_MODEL_CATALOG_URL=" + mockBase + "/catalog/api.json"}
}

// localMachine is the id of the owner Mac's own machine.
func localMachine(t *testing.T, c localv1connect.LocalServiceClient) string {
	t.Helper()
	res, err := c.ListMachines(context.Background(), &localv1.ListMachinesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range res.Machines {
		if m.Local {
			return m.MachineId
		}
	}
	t.Fatalf("no local machine: %v", res.Machines)
	return ""
}

// listedMachine is a machine as ListMachines shows it.
func listedMachine(t *testing.T, c localv1connect.LocalServiceClient, machineID string) *localv1.Machine {
	t.Helper()
	res, err := c.ListMachines(context.Background(), &localv1.ListMachinesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range res.Machines {
		if m.MachineId == machineID {
			return m
		}
	}
	return nil
}

// turnHerOn turns Her on for a machine (signed by the test's signer) and
// waits until Machine.her says her runs with a model ready.
func turnHerOn(t *testing.T, c localv1connect.LocalServiceClient, machineID string) {
	t.Helper()
	if _, err := c.SetHer(context.Background(), &localv1.SetHerRequest{MachineId: machineID, Enabled: true}); err != nil {
		t.Fatalf("SetHer: %v", err)
	}
	var her *machinev1.HerStatus
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if m := listedMachine(t, c, machineID); m != nil {
			her = m.Her
		}
		if her.GetRunning() && her.GetModelReady() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Her on %s is not running with a model: %v", machineID, her)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// herSetup is one her request, as HerOpen's setup takes it.
func herSetup(method string, params any) *herv1.Request {
	r := &herv1.Request{Method: method}
	if params != nil {
		r.Params, _ = json.Marshal(params)
	}
	return r
}

// herOpen opens a her connection on a machine with setup and returns the
// stream and what `opened` brought.
func herOpen(t *testing.T, ctx context.Context, c localv1connect.LocalServiceClient, machineID string, setup ...*herv1.Request) (*connect.ServerStreamForClient[herv1.OpenEvent], *herv1.Opened) {
	t.Helper()
	stream, err := c.HerOpen(ctx, &localv1.HerOpenRequest{MachineId: machineID, Open: &herv1.OpenRequest{Setup: setup}})
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() || stream.Msg().GetOpened() == nil {
		t.Fatalf("HerOpen on %s: %v %v", machineID, stream.Msg(), stream.Err())
	}
	op := stream.Msg().GetOpened()
	for i, r := range op.Responses {
		if r.Error != nil {
			t.Fatalf("%s on %s: %v", setup[i].Method, machineID, r.Error)
		}
	}
	return stream, op
}

// herBot is an assistant as bots/subscribe lists it.
type herBot struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
	Last    *struct {
		Text string `json:"text"`
	} `json:"last"`
}

// herBots lists a machine's assistants.
func herBots(c localv1connect.LocalServiceClient, machineID string) ([]herBot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream, err := c.HerOpen(ctx, &localv1.HerOpenRequest{MachineId: machineID, Open: &herv1.OpenRequest{Setup: []*herv1.Request{herSetup("bots/subscribe", nil)}}})
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	if !stream.Receive() {
		return nil, fmt.Errorf("bots/subscribe: %v", stream.Err())
	}
	op := stream.Msg().GetOpened()
	if op == nil || len(op.Responses) != 1 || op.Responses[0].Error != nil {
		return nil, fmt.Errorf("bots/subscribe: %v", stream.Msg())
	}
	var res struct {
		Bots []herBot `json:"bots"`
	}
	err = json.Unmarshal(op.Responses[0].Result, &res)
	return res.Bots, err
}

// defaultBot is a machine's default assistant, Her (HerBot in an older
// her).
func defaultBot(t *testing.T, c localv1connect.LocalServiceClient, machineID string) herBot {
	t.Helper()
	bots, err := herBots(c, machineID)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bots {
		if b.Default && (b.Name == "Her" || b.Name == "HerBot") {
			return b
		}
	}
	t.Fatalf("no default assistant on %s: %+v", machineID, bots)
	return herBot{}
}

// mockSend is a message her-mock answers by sending the user reply.
func mockSend(reply string) string { return "[[mock:send:" + reply + "]] hi" }

// talk sends an assistant a message and waits to see her send_to_user
// reply on the assistant's conversation, as the Her entry follows it.
func talk(t *testing.T, c localv1connect.LocalServiceClient, machineID, botID, reply string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	stream, _ := herOpen(t, ctx, c, machineID, herSetup("bots/open", map[string]string{"bot_id": botID}))
	defer stream.Close()
	res, err := c.HerSend(ctx, &localv1.HerSendRequest{MachineId: machineID, BotId: botID, Text: mockSend(reply)})
	if err != nil || !res.Delivered {
		t.Fatalf("HerSend to %s: %v %v", machineID, res, err)
	}
	for stream.Receive() {
		n := stream.Msg().GetNotification()
		if n != nil && n.Method == "session/event" && bytes.Contains(n.Params, []byte("send_to_user")) && bytes.Contains(n.Params, []byte(reply)) {
			return
		}
	}
	t.Fatalf("no send_to_user %q on %s: %v", reply, machineID, stream.Err())
}

// TestHerOnThisMac turns Her on for the owner Mac's own machine and uses
// her as the Mac app does, in process: a message and her reply, files both
// ways, the console with its JSON-RPC and a sign-in coming back to this Mac,
// and what the machine refuses.
func TestHerOnThisMac(t *testing.T) {
	her, mockBin := herBinaries(t)
	mock := startHerMock(t, mockBin)
	w := newOwnerMac(t, herEnv(her, herConsole(t), herHome(t, mock.models), mock.base), &localv1.GrantSpec{})
	c := w.app.client
	ctx := context.Background()
	mac := localMachine(t, c)

	// The console's address, from this Mac's own hermeshd: a sign-in's
	// redirect comes back to it, as the browser brings it.
	con, err := c.GetHerConsole(ctx, &localv1.GetHerConsoleRequest{MachineId: mac})
	if err != nil {
		t.Fatal(err)
	}
	code, body := httpDo(t, http.MethodPost, con.Url+"signin", `{"path":"/auth/callback"}`)
	var signin struct {
		ID          string `json:"id"`
		RedirectURI string `json:"redirect_uri"`
	}
	if json.Unmarshal([]byte(body), &signin); code != 200 || !strings.HasPrefix(signin.RedirectURI, "http://127.0.0.1:") || !strings.HasSuffix(signin.RedirectURI, "/auth/callback") {
		t.Fatalf("signin: %d %s", code, body)
	}
	if code, page := httpDo(t, http.MethodGet, signin.RedirectURI+"?code=c0de&state=st4te", ""); code != 200 || !strings.Contains(page, "go back to Hermesh") {
		t.Fatalf("the browser back: %d %s", code, page)
	}
	if code, body := httpDo(t, http.MethodGet, con.Url+"signin/"+signin.ID, ""); code != 200 || strings.TrimSpace(body) != `{"code":"c0de","state":"st4te"}` {
		t.Fatalf("what it brought: %d %s", code, body)
	}

	turnHerOn(t, c, mac)
	bot := defaultBot(t, c, mac)
	talk(t, c, mac, bot.ID, "hello from mac")
	if list, err := c.ListHerWaiting(ctx, &localv1.ListHerWaitingRequest{MachineId: mac}); err != nil || len(list.Waiting) != 0 {
		t.Fatalf("waiting: %v %v", list, err)
	}

	// A file up into the assistant's folder, and back down here.
	local := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(local, []byte("notes for Her\n"), 0o644)
	up, err := c.HerUpload(ctx, &localv1.HerUploadRequest{MachineId: mac, BotId: bot.ID, LocalPath: local})
	if err != nil || !filepath.IsAbs(up.Path) {
		t.Fatalf("upload: %v %v", up, err)
	}
	down, err := c.HerDownload(ctx, &localv1.HerDownloadRequest{MachineId: mac, BotId: bot.ID, Path: up.Path, LocalDir: filepath.Dir(local)})
	if b, _ := os.ReadFile(down.GetLocalPath()); err != nil || down.LocalPath == local || string(b) != "notes for Her\n" {
		t.Fatalf("download: %v %v %q", down, err, b)
	}

	// The console itself, and her's JSON-RPC through it.
	if code, page := httpDo(t, http.MethodGet, con.Url, ""); code != 200 || !strings.Contains(page, "the e2e console") {
		t.Fatalf("console page: %d %q", code, page)
	}
	if code, body := httpDo(t, http.MethodPost, con.Url+"rpc", `{"jsonrpc":"2.0","id":1,"method":"models/list"}`); code != 200 ||
		!strings.Contains(body, `"result"`) || !strings.Contains(body, "mock-1") {
		t.Fatalf("models/list through the console: %d %s", code, body)
	}

	// What the machine lets no device do: FinxWork's document panels, and
	// full access without a signature.
	_, err = c.HerCall(ctx, &localv1.HerCallRequest{MachineId: mac, Call: &herv1.CallRequest{Request: herSetup("doc/open", map[string]string{"path": "/tmp/x.xlsx"})}})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("doc/open: %v", err)
	}
	octx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	stream, op := herOpen(t, octx, c, mac, herSetup("bots/open", map[string]string{"bot_id": bot.ID}))
	var conv struct {
		SessionID string `json:"session_id"`
	}
	json.Unmarshal(op.Responses[0].Result, &conv)
	_, err = c.HerCall(ctx, &localv1.HerCallRequest{MachineId: mac, Call: &herv1.CallRequest{ConnectionId: op.ConnectionId,
		Request: herSetup("session/op", map[string]any{"session_id": conv.SessionID, "op": map[string]string{"type": "set_permission_mode", "mode": "bypass-permissions"}})}})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("full access unsigned: %v", err)
	}
	stream.Close()
	// Asking more goes unsigned; full access is signed, here by the test's signer.
	for _, mode := range []string{"accept-edits", "bypass-permissions", "manual"} {
		if _, err := c.SetHerAssistantMode(ctx, &localv1.SetHerAssistantModeRequest{MachineId: mac, BotId: bot.ID, PermissionMode: mode}); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
	}

	// A connector started as a command, from the console, which can't sign:
	// this Mac's hermeshd asks its signer.
	for _, body := range []string{`{"name":"e2e-echo","command":"/bin/echo","args":["hi"]}`, `{"name":"e2e-echo","remove":true}`} {
		if code, text := httpDo(t, http.MethodPost, con.Url+"command-connector", body); code != 200 {
			t.Fatalf("command connector %s: %d %s", body, code, text)
		}
	}
}

func httpDo(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestHerTwoEnvironments has Her on this Mac and on a Linux machine in
// Docker (standing in for a cloud server), talks to each, then writes to
// the Linux one while it is frozen: the message waits on this device, and
// arrives, answered, once the machine is back (docs/her-server.md §11,
// phase 1). HERMESH_DOCKER=1, and HERMESH_HER_LINUX: a Linux build of her
// for the container (alpine), static with musl.
func TestHerTwoEnvironments(t *testing.T) {
	if os.Getenv("HERMESH_DOCKER") != "1" {
		t.Skip("set HERMESH_DOCKER=1")
	}
	herLinux := os.Getenv("HERMESH_HER_LINUX")
	if _, err := os.Stat(herLinux); herLinux == "" || err != nil {
		t.Skip("set HERMESH_HER_LINUX to a Linux build of her")
	}
	her, mockBin := herBinaries(t)
	mock := startHerMock(t, mockBin)
	console := herConsole(t)
	w := newOwnerMac(t, herEnv(her, console, herHome(t, mock.models), mock.base), &localv1.GrantSpec{})
	c := w.app.client
	ctx := context.Background()
	mac := localMachine(t, c)

	// The Linux machine: its hermeshd, cross-compiled, and her, with the
	// mock reached as the container reaches this computer.
	tmp := t.TempDir()
	linuxBin := filepath.Join(tmp, "linux")
	build := exec.Command("go", "build", "-o", filepath.Join(linuxBin, "hermeshd"), "./cmd/hermeshd")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	linuxHome := herHome(t, strings.ReplaceAll(mock.models, "127.0.0.1", "host.docker.internal"))
	mockForLinux := strings.Replace(mock.base, "127.0.0.1", "host.docker.internal", 1)
	name := fmt.Sprintf("hermesh-her-e2e-%d", time.Now().UnixNano())
	script := strings.Join([]string{
		"mkdir -p /data/her && cp /herinit/* /data/her/",
		"/opt/hermesh/hermeshd pair " + strings.Replace(w.addr, "127.0.0.1", "host.docker.internal", 1) + " --name linux-her --no-service --data-dir /data",
		"exec /opt/hermesh/hermeshd run --data-dir /data",
	}, " && ")
	args := []string{"run", "--rm", "-d", "--name", name, "--add-host", "host.docker.internal:host-gateway",
		"-v", linuxBin + ":/opt/hermesh:ro", "-v", herLinux + ":/opt/her/her:ro", "-v", console + ":/opt/her/console:ro", "-v", linuxHome + ":/herinit:ro"}
	for _, e := range herEnv("/opt/her/her", "/opt/her/console", "/data/her", mockForLinux) {
		args = append(args, "-e", e)
	}
	if out, err := exec.Command("docker", append(args, "alpine:3", "sh", "-c", script)...).CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v %s", err, out)
	}
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	logs := func() string { o, _ := exec.Command("docker", "logs", name).CombinedOutput(); return string(o) }
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("==== linux machine ====\n%s", logs())
		}
	})
	var code string
	eventually(t, 90*time.Second, "linux pairing code", func() bool {
		m := codeRE.FindStringSubmatch(logs())
		if m != nil {
			code = m[1]
		}
		return m != nil
	})
	approved, err := c.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: code, MachineName: "linux-her"})
	if err != nil {
		t.Fatal(err)
	}
	linux := approved.MachineId
	online := func(want bool) func() bool {
		return func() bool {
			m := listedMachine(t, c, linux)
			return m != nil && m.Online == want && (!want || m.Status != nil)
		}
	}
	eventually(t, 60*time.Second, "linux machine online", online(true))

	// Two environments, each with its own Her.
	turnHerOn(t, c, mac)
	turnHerOn(t, c, linux)
	macBot, linuxBot := defaultBot(t, c, mac), defaultBot(t, c, linux)
	talk(t, c, mac, macBot.ID, "hello from mac")
	talk(t, c, linux, linuxBot.ID, "hello from linux")

	// The Linux machine freezes; once it is seen offline, a message to it
	// waits here, and its Her stays listed as last seen.
	if out, err := exec.Command("docker", "pause", name).CombinedOutput(); err != nil {
		t.Fatalf("docker pause: %v %s", err, out)
	}
	paused := true
	t.Cleanup(func() {
		if paused {
			exec.Command("docker", "unpause", name).Run()
		}
	})
	eventually(t, 2*time.Minute, "linux machine offline", online(false))
	if m := listedMachine(t, c, linux); !m.GetHer().GetRunning() {
		t.Fatalf("an offline environment's Her: %v", m.GetHer())
	}
	sent, err := c.HerSend(ctx, &localv1.HerSendRequest{MachineId: linux, BotId: linuxBot.ID, Text: mockSend("back again")})
	if err != nil || sent.Delivered || sent.Waiting == nil {
		t.Fatalf("HerSend to a frozen machine: %v %v", sent, err)
	}
	list, err := c.ListHerWaiting(ctx, &localv1.ListHerWaitingRequest{MachineId: linux})
	if err != nil || len(list.Waiting) != 1 || list.Waiting[0].Id != sent.Waiting.Id {
		t.Fatalf("waiting: %v %v", list, err)
	}
	// A second one, thought better of: dropped, it never goes.
	dropped, err := c.HerSend(ctx, &localv1.HerSendRequest{MachineId: linux, BotId: linuxBot.ID, Text: mockSend("never mind")})
	if err != nil || dropped.Waiting == nil {
		t.Fatalf("a second message: %v %v", dropped, err)
	}
	if _, err := c.DeleteHerWaiting(ctx, &localv1.DeleteHerWaitingRequest{MachineId: linux, Id: dropped.Waiting.Id}); err != nil {
		t.Fatalf("DeleteHerWaiting: %v", err)
	}
	list, err = c.ListHerWaiting(ctx, &localv1.ListHerWaitingRequest{MachineId: linux})
	if err != nil || len(list.Waiting) != 1 || list.Waiting[0].Id != sent.Waiting.Id {
		t.Fatalf("waiting after dropping one: %v %v", list, err)
	}

	// Back: the message goes, the waiting list empties, and her answers.
	if out, err := exec.Command("docker", "unpause", name).CombinedOutput(); err != nil {
		t.Fatalf("docker unpause: %v %s", err, out)
	}
	paused = false
	eventually(t, 3*time.Minute, "the waiting message delivered", func() bool {
		list, err := c.ListHerWaiting(ctx, &localv1.ListHerWaitingRequest{MachineId: linux})
		return err == nil && len(list.Waiting) == 0
	})
	eventually(t, 2*time.Minute, "her's answer to it", func() bool {
		bots, _ := herBots(c, linux)
		for _, b := range bots {
			if b.ID == linuxBot.ID && b.Last != nil && b.Last.Text == "back again" {
				return true
			}
		}
		return false
	})
}
