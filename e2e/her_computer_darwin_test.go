package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
)

// TestHerComputerUse: computer use on this Mac's Her (docs/her-server.md
// §4.4). her's desktop tools run agent-desktop through hermeshd, and
// desktop/status, as the Her entry asks it, says what macOS lets
// agent-desktop do from there. The tools agree with it: listing the apps
// needs nothing, reading a window needs Accessibility, and without it the
// result says what to switch on and where. macOS asks it of whatever started
// the test, as it asks it of hermeshd under launchd. Needs agent-desktop:
// HERMESH_AGENT_DESKTOP, else the one beside her.
func TestHerComputerUse(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	her, mockBin := herBinaries(t)
	program := os.Getenv("HERMESH_AGENT_DESKTOP")
	if program == "" {
		program = filepath.Join(filepath.Dir(her), "agent-desktop")
	}
	if _, err := os.Stat(program); err != nil {
		t.Skipf("no agent-desktop (%v): set HERMESH_AGENT_DESKTOP", err)
	}
	mock := startHerMock(t, mockBin)
	home := herHome(t, mock.models)
	// Finder's window read without asking. Computer use itself is on by
	// default on a Mac (desktop/status says so below).
	config, err := os.OpenFile(filepath.Join(home, "config.toml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(config, "\n[desktop]\nexecutable = %q\n\n[desktop.apps]\n\"com.apple.finder\" = \"allow\"\n", program)
	config.Close()
	w := newOwnerMac(t, herEnv(her, herConsole(t), home, mock.base), &localv1.GrantSpec{})
	c := w.app.client
	mac := localMachine(t, c)
	turnHerOn(t, c, mac)

	res, err := c.HerCall(context.Background(), &localv1.HerCallRequest{MachineId: mac, Call: &herv1.CallRequest{Request: herSetup("desktop/status", nil)}})
	if err != nil || res.Response.GetError() != nil {
		t.Fatalf("desktop/status: %v %v", res, err)
	}
	var status struct {
		Enabled         bool   `json:"enabled"`
		Program         string `json:"program"`
		Accessibility   string `json:"accessibility"`
		ScreenRecording string `json:"screen_recording"`
	}
	known := []string{"granted", "denied"}
	if err := json.Unmarshal(res.Response.Result, &status); err != nil || !status.Enabled || status.Program != program ||
		!slices.Contains(known, status.Accessibility) || !slices.Contains(known, status.ScreenRecording) {
		t.Fatalf("desktop/status: %s %v", res.Response.Result, err)
	}

	bot := defaultBot(t, c, mac)
	if apps := cua(t, c, mac, bot.ID, "apps"); !strings.Contains(apps, "(com.apple.finder)") {
		t.Fatalf("cua_apps: %s", apps)
	}
	read := cua(t, c, mac, bot.ID, "snapshot:com.apple.finder")
	if denied := strings.Contains(read, "PERM_DENIED"); denied != (status.Accessibility == "denied") {
		t.Fatalf("desktop/status says Accessibility is %s, and cua_snapshot: %s", status.Accessibility, read)
	}
	if status.Accessibility == "denied" && (!strings.Contains(read, "System Settings › Privacy & Security › Accessibility") ||
		!strings.Contains(read, "hermeshd when Hermesh runs her")) {
		t.Fatalf("what to switch on, unsaid: %s", read)
	}
}

// cua has an assistant run a desktop tool (her-mock's cua scenario, such as
// "apps" or "snapshot:<app>") and returns what it sent: "cua done: " and the
// tool's result.
func cua(t *testing.T, c localv1connect.LocalServiceClient, machineID, botID, tool string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	stream, _ := herOpen(t, ctx, c, machineID, herSetup("bots/open", map[string]string{"bot_id": botID}))
	defer stream.Close()
	if res, err := c.HerSend(ctx, &localv1.HerSendRequest{MachineId: machineID, BotId: botID, Text: "[[mock:cua:" + tool + "]] go"}); err != nil || !res.Delivered {
		t.Fatalf("HerSend: %v %v", res, err)
	}
	for stream.Receive() {
		if n := stream.Msg().GetNotification(); n != nil && n.Method == "session/event" {
			if text, ok := firstString(n.Params, "cua done: "); ok {
				return text
			}
		}
	}
	t.Fatalf("no result of cua_%s: %v", tool, stream.Err())
	return ""
}

// firstString is the first string in a JSON document that starts with prefix.
func firstString(doc []byte, prefix string) (string, bool) {
	var walk func(v any) (string, bool)
	walk = func(v any) (string, bool) {
		switch v := v.(type) {
		case string:
			return v, strings.HasPrefix(v, prefix)
		case map[string]any:
			for _, x := range v {
				if s, ok := walk(x); ok {
					return s, true
				}
			}
		case []any:
			for _, x := range v {
				if s, ok := walk(x); ok {
					return s, true
				}
			}
		}
		return "", false
	}
	var v any
	if json.Unmarshal(doc, &v) != nil {
		return "", false
	}
	return walk(v)
}
