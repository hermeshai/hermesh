package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/her/v1/herv1connect"
	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1/machinev1connect"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/internal/herhost/hertest"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

// TestHer: Her on one of the owner's machines, reached as the owner's
// devices reach it, through the relay and an end-to-end tunnel. A
// root-signed SetHer starts her (an unsigned one is refused); HerService
// talks to the default assistant and the machine's status follows; what only
// the root key may do is refused on HerService and goes by signed commands;
// files and the console travel on the same tunnel; an agent reaches none of
// it; SetHer off stops her.
func TestHer(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	if runtime.GOOS == "windows" {
		t.Skip("her does not run on Windows")
	}
	bin := hertest.Bin(t)
	hertest.Home(t, bin)
	console := t.TempDir()
	os.WriteFile(filepath.Join(console, "index.html"), []byte("<title>Her</title>"), 0o644)
	w := newWorld(t, func(c *machine.Config) { c.HerBin, c.HerConsole = bin, console })
	alpha := w.machines[0]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// An owner's device: a key of its own certified by the root key, as the
	// apps' are; its tunnel to alpha goes through the relay.
	dev, _ := identity.GenerateKey()
	cert, err := identity.Sign(&identityv1.OwnerDeviceCert{OwnerId: w.ownerID, DevicePublicKey: identity.MarshalPublicKey(dev.Public()),
		Label: "test device", IssuedAt: timestamppb.Now()}, w.app.root)
	if err != nil {
		t.Fatal(err)
	}
	creds := func() *identityv1.Credentials {
		return &identityv1.Credentials{RootChain: w.rootChain, Subject: &identityv1.Credentials_DeviceCert{DeviceCert: cert}}
	}
	relay, err := (&transport.Dialer{RelayURL: w.relayURL, Role: relayv1.Role_ROLE_OWNER, Key: dev, Credentials: creds, HTTPClient: http.DefaultClient}).Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	raw, err := relay.OpenTunnel(alpha.MachineID())
	if err != nil {
		t.Fatal(err)
	}
	tc, _, err := transport.ClientHandshake(ctx, raw, dev.CryptoSigner(), creds, w.trust, alpha.MachineID())
	if err != nil {
		t.Fatal(err)
	}
	h2, err := transport.NewH2Client(tc)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	const base = "https://machine"
	info := machinev1connect.NewMachineServiceClient(h2, base, transport.ClientNoCompression)
	admin := machinev1connect.NewMachineAdminServiceClient(h2, base, transport.ClientNoCompression)
	her := herv1connect.NewHerServiceClient(h2, base, transport.ClientNoCompression)
	herStatus := func() *machinev1.HerStatus {
		res, err := info.GetInfo(ctx, &machinev1.GetInfoRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return res.Status.Her
	}
	command := func(c *identityv1.AdminCommand) *identityv1.AdminCommand {
		c.OwnerId, c.TargetMachineId, c.CommandId, c.ExpiresAt = w.ownerID, alpha.MachineID(), identity.RandomCode(16), timestamppb.New(time.Now().Add(5*time.Minute))
		return c
	}
	apply := func(c *identityv1.AdminCommand) {
		t.Helper()
		signed, err := identity.Sign(command(c), w.app.root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := admin.ApplyAdminCommand(ctx, &machinev1.ApplyAdminCommandRequest{Command: signed}); err != nil {
			t.Fatalf("%T: %v", c.Command, err)
		}
	}
	refused := func(err error) {
		t.Helper()
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("not refused: %v", err)
		}
	}
	setHer := func(on bool) *identityv1.AdminCommand {
		return &identityv1.AdminCommand{Command: &identityv1.AdminCommand_SetHer{SetHer: &identityv1.SetHer{Enabled: on}}}
	}

	if st := herStatus(); st.Enabled || st.Running || st.Unsupported != "" {
		t.Fatalf("before: %v", st)
	}
	_, err = admin.ApplyAdminCommand(ctx, &machinev1.ApplyAdminCommandRequest{Settings: command(setHer(true))})
	refused(err)
	apply(setHer(true))
	eventually(t, 30*time.Second, "her running", func() bool { st := herStatus(); return st.Enabled && st.Running && st.ModelReady && st.Version != "" })

	// The default assistant answers on a connection an Open stream holds.
	stream, err := her.Open(ctx, &herv1.OpenRequest{Setup: []*herv1.Request{{Method: "bots/subscribe"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if !stream.Receive() {
		t.Fatal(stream.Err())
	}
	opened := stream.Msg().GetOpened()
	var list struct {
		Bots []struct {
			ID      string `json:"id"`
			Default bool   `json:"default"`
		} `json:"bots"`
	}
	json.Unmarshal(opened.GetResponses()[0].GetResult(), &list)
	if len(list.Bots) != 1 || !list.Bots[0].Default {
		t.Fatalf("assistants: %s", opened.GetResponses()[0].GetResult())
	}
	bot := list.Bots[0].ID
	call := func(conn, method, params string) ([]byte, error) {
		res, err := her.Call(ctx, &herv1.CallRequest{ConnectionId: conn, Request: &herv1.Request{Method: method, Params: []byte(params)}})
		if err == nil && res.Response.Error != nil {
			err = errors.New(res.Response.Error.Message)
		}
		return res.GetResponse().GetResult(), err
	}
	result, err := call(opened.ConnectionId, "bots/open", `{"bot_id":"`+bot+`"}`)
	var open struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(result, &open); err != nil || open.SessionID == "" {
		t.Fatalf("bots/open: %s %v", result, err)
	}
	if _, err := call(opened.ConnectionId, "session/op", `{"session_id":"`+open.SessionID+`","op":{"type":"prompt","input":[{"type":"text","text":"[[mock:send:hello]] hi"}]}}`); err != nil {
		t.Fatal(err)
	}
	for answered := false; !answered; {
		if !stream.Receive() {
			t.Fatalf("the stream ended: %v", stream.Err())
		}
		n := stream.Msg().GetNotification()
		var c struct {
			Bots []struct {
				Last *struct{ From, Text string } `json:"last"`
			} `json:"bots"`
		}
		json.Unmarshal(n.GetParams(), &c)
		answered = n.GetMethod() == "bots/changed" && len(c.Bots) == 1 && c.Bots[0].Last != nil && c.Bots[0].Last.From == "bot" && c.Bots[0].Last.Text == "hello"
	}
	eventually(t, 30*time.Second, "the answer in the machine's status", func() bool {
		st := herStatus()
		return st.Latest.GetText() == "hello" && st.Latest.GetBotId() == bot && st.Latest.GetKind() == machinev1.HerLatestKind_HER_LATEST_KIND_MESSAGE && st.Unread > 0
	})

	// What only the root key may do; an assistant's full access is the
	// owner's choice on any of their devices, and older ones sign it.
	_, err = call("", "server/shutdown", ``)
	refused(err)
	_, err = call("", "config/write", `{"layer":"user","edits":[{"key_path":"mcp_servers.notes","value":{"command":"/bin/cat"}}]}`)
	refused(err)
	apply(&identityv1.AdminCommand{Command: &identityv1.AdminCommand_SetHerAssistantMode{SetHerAssistantMode: &identityv1.SetHerAssistantMode{BotId: bot, PermissionMode: "bypass-permissions"}}})
	apply(&identityv1.AdminCommand{Command: &identityv1.AdminCommand_SetHerCommandConnector{SetHerCommandConnector: &identityv1.SetHerCommandConnector{Name: "notes", Command: "/bin/cat"}}})
	result, err = call("", "bots/open", `{"bot_id":"`+bot+`","history_limit":0}`)
	if err != nil || !strings.Contains(string(result), `"permission_mode":"bypass-permissions"`) {
		t.Fatalf("the assistant's mode: %s %v", result, err)
	}
	if result, err = call("", "config/read", `{}`); err != nil || !strings.Contains(string(result), `"/bin/cat"`) {
		t.Fatalf("the connector: %s %v", result, err)
	}

	// A file into the assistant's folder and back.
	up, err := her.Upload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	up.Send(&herv1.UploadRequest{Part: &herv1.UploadRequest_Start{Start: &herv1.UploadStart{BotId: bot, Name: "brief.md", Size: 8}}})
	up.Send(&herv1.UploadRequest{Part: &herv1.UploadRequest_Data{Data: []byte("# Brief\n")}})
	uploaded, err := up.CloseAndReceive()
	if err != nil || !strings.HasSuffix(uploaded.Path, filepath.Join("bots", bot, "files", "uploads", "brief.md")) {
		t.Fatalf("upload: %v %v", uploaded, err)
	}
	down, err := her.Download(ctx, &herv1.DownloadRequest{BotId: bot, Path: uploaded.Path})
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	var start *herv1.DownloadStart
	for down.Receive() {
		if s := down.Msg().GetStart(); s != nil {
			start = s
		}
		got = append(got, down.Msg().GetData()...)
	}
	if down.Err() != nil || start.GetName() != "brief.md" || start.GetSize() != 8 || string(got) != "# Brief\n" {
		t.Fatalf("download: %v %v %q", down.Err(), start, got)
	}
	down, _ = her.Download(ctx, &herv1.DownloadRequest{BotId: bot, Path: "../bot.json"})
	for down.Receive() {
	}
	refused(down.Err())

	// The console's HTTP on the same tunnel.
	get := func(h *transport.H2Client, method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		res, err := h.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, page := get(h2, "GET", "/her/console/", ""); code != 200 || page != "<title>Her</title>" {
		t.Fatalf("console: %d %s", code, page)
	}
	if code, body := get(h2, "POST", "/her/console/rpc", `{"jsonrpc":"2.0","id":1,"method":"models/list"}`); code != 200 || !strings.Contains(body, `"result"`) {
		t.Fatalf("console rpc: %d %s", code, body)
	}
	if code, _ := get(h2, "POST", "/her/console/rpc", `{"jsonrpc":"2.0","id":2,"method":"server/shutdown"}`); code != http.StatusForbidden {
		t.Fatalf("console rpc refused: %d", code)
	}

	// An agent reaches none of it, and never sees Her's status.
	agent, err := w.evilTunnel(t, alpha.MachineID(), w.agentKey, w.agentCreds(w.grant), alpha.MachineID())
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	res, err := machinev1connect.NewMachineServiceClient(agent, base).GetInfo(ctx, &machinev1.GetInfoRequest{})
	if err != nil || res.Status.Her != nil {
		t.Fatalf("an agent's GetInfo: %v %v", res.GetStatus().GetHer(), err)
	}
	watch, err := machinev1connect.NewMachineServiceClient(agent, base).Watch(ctx, &machinev1.WatchRequest{})
	if err != nil || !watch.Receive() || watch.Msg().Status == nil || watch.Msg().Status.Her != nil {
		t.Fatalf("an agent's Watch: %v %v", watch.Msg().GetStatus().GetHer(), err)
	}
	watch.Close()
	_, err = herv1connect.NewHerServiceClient(agent, base).Call(ctx, &herv1.CallRequest{Request: &herv1.Request{Method: "models/list"}})
	refused(err)
	if code, _ := get(agent, "GET", "/her/console/", ""); code != http.StatusForbidden {
		t.Fatalf("an agent's console: %d", code)
	}

	// Off: her stops, and the stream holding a connection ends, saying so:
	// the tunnel is fine, so the owner's device keeps it.
	apply(setHer(false))
	if st := herStatus(); st.Enabled || st.Running {
		t.Fatalf("off: %v", st)
	}
	for stream.Receive() {
	}
	if connect.CodeOf(stream.Err()) != connect.CodeFailedPrecondition {
		t.Fatalf("the stream after Her went off: %v", stream.Err())
	}
}
