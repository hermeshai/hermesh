package machine

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
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
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/internal/herhost/hertest"
	"github.com/hermeshai/hermesh/internal/paths"
	"github.com/hermeshai/hermesh/internal/provider"
)

func openHerMachine(t *testing.T, dir, bin string) *Machine {
	t.Helper()
	n, err := Open(Config{DataDir: dir, HerBin: bin, Providers: map[string]provider.Provider{}})
	if err != nil {
		t.Fatal(err)
	}
	// A long data directory's socket folder is in the temp folder.
	if sock := filepath.Dir(paths.Her(dir)); !strings.HasPrefix(sock, dir) {
		t.Cleanup(func() { os.RemoveAll(sock) })
	}
	return n
}

func setHer(on bool) *identityv1.AdminCommand {
	return &identityv1.AdminCommand{Command: &identityv1.AdminCommand_SetHer{SetHer: &identityv1.SetHer{Enabled: on}}}
}

// Her's commands are root-signed only, never settings.
func TestHerCommandsAreSignedOnly(t *testing.T) {
	n := openHerMachine(t, t.TempDir(), "")
	defer n.Close()
	for _, cmd := range []*identityv1.AdminCommand{
		setHer(true),
		{Command: &identityv1.AdminCommand_SetHerAssistantMode{SetHerAssistantMode: &identityv1.SetHerAssistantMode{BotId: "b", PermissionMode: "bypass-permissions"}}},
		{Command: &identityv1.AdminCommand_SetHerCommandConnector{SetHerCommandConnector: &identityv1.SetHerCommandConnector{Name: "x", Command: "sh"}}},
	} {
		if _, err := n.ApplySettings(context.Background(), cmd); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("unsigned %T: %v", cmd.Command, err)
		}
	}
	if n.Status().Her.Enabled {
		t.Fatal("Her went on unsigned")
	}
}

// Whether Her is on is kept across starts; a machine without her (or on
// Windows) says why it can't run it, while Her may be on all the same; agents
// never see Her's status.
func TestHerSetting(t *testing.T) {
	dir := t.TempDir()
	n := openHerMachine(t, dir, "")
	why := "no her"
	if runtime.GOOS == "windows" {
		why = "windows"
	}
	if st := n.Status().Her; st.Enabled || st.Unsupported != why {
		t.Fatalf("status %v", st)
	}
	msg, err := n.execAdmin(context.Background(), setHer(true))
	if err != nil || !strings.Contains(msg, why) {
		t.Fatalf("%q, %v", msg, err)
	}
	if _, err := n.Local().HerCall(context.Background(), &herv1.CallRequest{Request: &herv1.Request{Method: "models/list"}}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatal(err)
	}
	n.Close()

	n = openHerMachine(t, dir, "")
	defer n.Close()
	if st := n.Status().Her; !st.Enabled || st.Unsupported != why || st.Running {
		t.Fatalf("after a restart: %v", st)
	}
	if n.statusFor(&Principal{Kind: PrincipalClient}).Her != nil || n.statusFor(&Principal{Kind: PrincipalOwner}).Her == nil {
		t.Fatal("Her's status for an agent, or none for the owner")
	}
	if _, err := n.execAdmin(context.Background(), setHer(false)); err != nil || n.Status().Her.Enabled {
		t.Fatalf("off: %v %v", err, n.Status().Her)
	}
}

// Turned off with erase, her data goes once she stopped; on with erase is
// refused. Erase Everything erases it where Her was ever on, and only there.
func TestHerErased(t *testing.T) {
	home := filepath.Join(t.TempDir(), "her")
	t.Setenv("HER_HOME", home)
	t.Setenv("HER_CREDENTIAL_STORE", "file") // never the Keychain
	mkHome := func() {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(home, "bots", "b1"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	gone := func() bool { _, err := os.Stat(home); return os.IsNotExist(err) }
	erase := func(on bool) *identityv1.AdminCommand {
		return &identityv1.AdminCommand{Command: &identityv1.AdminCommand_SetHer{SetHer: &identityv1.SetHer{Enabled: on, Erase: true}}}
	}
	dir := t.TempDir()
	cfg := Config{DataDir: dir, Providers: map[string]provider.Provider{}}
	mkHome()

	// Never on here: Erase Everything leaves a her of the user's own, and
	// the machine tells of no data of Her's.
	if err := EraseHer(cfg); err != nil || gone() {
		t.Fatalf("Her never on: %v, home gone %v", err, gone())
	}
	if h, ok, err := HerData(cfg); err != nil || ok || h != home {
		t.Fatalf("HerData, Her never on: %q %v %v", h, ok, err)
	}
	n := openHerMachine(t, dir, "")
	ctx := context.Background()
	if n.Status().Her.Data {
		t.Fatal("data of a Her never on")
	}
	if _, err := n.execAdmin(ctx, erase(true)); connect.CodeOf(err) != connect.CodeInvalidArgument || n.Status().Her.Enabled {
		t.Fatalf("on with erase: %v", err)
	}
	if _, err := n.execAdmin(ctx, setHer(true)); err != nil {
		t.Fatal(err)
	}
	if _, err := n.execAdmin(ctx, setHer(false)); err != nil || gone() || !n.Status().Her.Data {
		t.Fatalf("off: %v, home gone %v, %v", err, gone(), n.Status().Her)
	}
	n.Close()
	// Kept across a restart, and told to uninstall and erase.
	if h, ok, err := HerData(cfg); err != nil || !ok || h != home {
		t.Fatalf("HerData, her data kept: %q %v %v", h, ok, err)
	}
	n = openHerMachine(t, dir, "")
	if !n.Status().Her.Data {
		t.Fatal("her data kept, after a restart")
	}
	msg, err := n.execAdmin(ctx, erase(false))
	if err != nil || msg != HerErased || !gone() || n.Status().Her.Enabled || n.Status().Her.Data {
		t.Fatalf("off with erase: %q %v, home gone %v, %v", msg, err, gone(), n.Status().Her)
	}
	n.Close()

	mkHome()
	if err := EraseHer(cfg); err != nil || !gone() {
		t.Fatalf("Her once on: %v, home gone %v", err, gone())
	}
}

// A machine its owner removed turns Her off, her data staying.
func TestHerOffOnARemovedMachine(t *testing.T) {
	home := filepath.Join(t.TempDir(), "her")
	t.Setenv("HER_HOME", home)
	t.Setenv("HER_CREDENTIAL_STORE", "file")
	os.MkdirAll(home, 0o700)
	dir := t.TempDir()
	n := openHerMachine(t, dir, "")
	if _, err := n.execAdmin(context.Background(), setHer(true)); err != nil {
		t.Fatal(err)
	}
	n.stopHerRemoved()
	if st := n.Status().Her; st.Enabled || !st.Data {
		t.Fatalf("removed: %v", st)
	}
	n.Close()
	n = openHerMachine(t, dir, "")
	defer n.Close()
	if st := n.Status().Her; st.Enabled {
		t.Fatalf("removed, after a restart: %v", st)
	}
}

// With HerExtensionHost, turning Her on registers her with the browsers for
// the machine's socket, and turning it off takes that back; closing the
// machine (hermeshd stopping) leaves it.
func TestHerExtensionHost(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("her does not run on Windows")
	}
	bin, calls := hertest.Fake(t)
	t.Setenv("HER_HOME", "/hers")
	dir := t.TempDir()
	n, err := Open(Config{DataDir: dir, HerBin: bin, Providers: map[string]provider.Provider{}, HerExtensionHost: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	install, uninstall := "/hers browser extension install --socket "+paths.Her(dir), "/hers browser extension uninstall"
	has := func(line string) func() bool {
		return func() bool {
			return slices.ContainsFunc(hertest.Calls(calls), func(call string) bool { return call == line || strings.HasPrefix(call, line+" ") })
		}
	}
	if _, err := n.execAdmin(ctx, setHer(true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "her registered", has(install))
	if _, err := n.execAdmin(ctx, setHer(false)); err != nil || !has(uninstall)() {
		t.Fatalf("Her off: %v %q", err, hertest.Calls(calls))
	}
	os.Remove(calls)
	if _, err := n.execAdmin(ctx, setHer(true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "her registered again", has(install))
	n.Close()
	if has(uninstall)() {
		t.Fatalf("closing the machine unregistered her: %q", hertest.Calls(calls))
	}
}

// Her on a machine, in process as this Mac's owner role reaches it: SetHer
// starts her, HerService's calls talk to the default assistant and the
// status follows, the root-signed commands reach her, the console answers,
// and closing the machine stops her.
func TestHerInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("her does not run on Windows")
	}
	bin := hertest.Bin(t)
	home := hertest.Home(t, bin)
	dir := t.TempDir()
	n := openHerMachine(t, dir, bin)
	closed := false
	defer func() {
		if !closed {
			n.Close()
		}
	}()
	ctx := context.Background()
	if _, err := n.execAdmin(ctx, setHer(true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "her running", func() bool { st := n.Status().Her; return st.Running && st.ModelReady })

	l := n.Local()
	events := make(chan *herv1.OpenEvent, 4096)
	octx, cancel := context.WithCancel(ctx)
	defer cancel()
	go l.HerOpen(octx, &herv1.OpenRequest{Setup: []*herv1.Request{{Method: "bots/subscribe"}}}, func(e *herv1.OpenEvent) error {
		events <- e
		return nil
	})
	opened := (<-events).GetOpened()
	var list struct {
		Bots []struct {
			ID      string `json:"id"`
			Default bool   `json:"default"`
		} `json:"bots"`
	}
	json.Unmarshal(opened.GetResponses()[0].GetResult(), &list)
	if len(list.Bots) == 0 || !list.Bots[0].Default {
		t.Fatalf("bots %s", opened.GetResponses()[0].GetResult())
	}
	bot := list.Bots[0].ID
	herCall := func(conn, method, params string) []byte {
		t.Helper()
		res, err := l.HerCall(ctx, &herv1.CallRequest{ConnectionId: conn, Request: &herv1.Request{Method: method, Params: []byte(params)}})
		if err != nil || res.Response.Error != nil {
			t.Fatalf("%s: %v %v", method, err, res.GetResponse().GetError())
		}
		return res.Response.Result
	}
	var open struct {
		SessionID string `json:"session_id"`
	}
	json.Unmarshal(herCall(opened.ConnectionId, "bots/open", `{"bot_id":"`+bot+`"}`), &open)
	herCall(opened.ConnectionId, "session/op", `{"session_id":"`+open.SessionID+`","op":{"type":"prompt","input":[{"type":"text","text":"[[mock:send:hello]] hi"}]}}`)
	waitFor(t, "the answer in the status", func() bool {
		st := n.Status().Her
		return st.Latest.GetText() == "hello" && st.Latest.GetKind() == machinev1.HerLatestKind_HER_LATEST_KIND_MESSAGE && st.Unread > 0
	})

	if _, err := n.execAdmin(ctx, &identityv1.AdminCommand{Command: &identityv1.AdminCommand_SetHerAssistantMode{
		SetHerAssistantMode: &identityv1.SetHerAssistantMode{BotId: bot, PermissionMode: "bypass-permissions"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := n.execAdmin(ctx, &identityv1.AdminCommand{Command: &identityv1.AdminCommand_SetHerCommandConnector{
		SetHerCommandConnector: &identityv1.SetHerCommandConnector{Name: "notes", Command: "/bin/cat"}}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(herCall("", "config/read", `{}`)), `"/bin/cat"`) {
		t.Fatal("the connector is not in her's configuration")
	}

	res, err := l.HerUpload(ctx, &herv1.UploadStart{BotId: bot, Name: "a.txt", Size: 3}, strings.NewReader("abc"))
	if err != nil || !strings.HasSuffix(res.Path, filepath.Join("bots", bot, "files", "uploads", "a.txt")) {
		t.Fatalf("%v, %v", res, err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "bots", bot, "files", "uploads", "a.txt")); string(b) != "abc" {
		t.Fatalf("%q", b)
	}
	var got strings.Builder
	if err := l.HerDownload(ctx, &herv1.DownloadRequest{BotId: bot, Path: "uploads/a.txt"}, func(r *herv1.DownloadResponse) error {
		got.Write(r.GetData())
		return nil
	}); err != nil || got.String() != "abc" {
		t.Fatalf("%q, %v", got.String(), err)
	}

	w := httptest.NewRecorder()
	l.HerConsole().ServeHTTP(w, httptest.NewRequest("POST", "/her/console/rpc", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"models/list"}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"result"`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}

	var rec struct {
		PID int `json:"pid"`
	}
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(paths.Her(dir)), "her.lock"))
	json.Unmarshal(b, &rec)
	n.Close()
	closed = true
	if _, err := os.Stat(paths.Her(dir)); !os.IsNotExist(err) || rec.PID == 0 {
		t.Fatalf("her's socket after closing the machine: %v (pid %d)", err, rec.PID)
	}
	if p, err := os.FindProcess(rec.PID); err == nil && p.Signal(syscall.Signal(0)) == nil {
		t.Fatal("her outlived the machine")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
