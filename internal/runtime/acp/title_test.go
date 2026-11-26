package acp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/runtime"
)

func TestGenerateTitleSwitchesToSmallestModelAndLowestEffort(t *testing.T) {
	dir := t.TempDir()
	// Symlink fakeBin under a non-"fakeacp" name so GenerateTitle exercises the
	// real ACP stdio JSON-RPC path instead of the fakeacp shortcut.
	agentBin := filepath.Join(dir, "mock-agy")
	if err := os.Symlink(fakeBin, agentBin); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	logFile := filepath.Join(dir, "methods.log")

	drv := NewDriver(os.Getenv("PATH"))
	cfg := &sessionv1.ProviderInstanceConfig{
		InstanceId: "antigravity",
		Driver:     "acp",
		AcpFlavor:  "antigravity",
		BinaryPath: agentBin,
		LaunchArgs: []string{"acp"},
		Enabled:    true,
		Env: map[string]string{
			"FAKE_ACP_STATE":   filepath.Join(dir, "state"),
			"FAKE_ACP_LOG":     logFile,
			"FAKE_ACP_CONFIG":  "1",
			"FAKE_ACP_MODELS":  "gemini-4-neon-high,gemini-3.8-flash",
			"FAKE_ACP_EFFORTS": "gemini-4-neon-high=low,high;gemini-3.8-flash=low,high",
		},
	}

	// `settings` makes fakeacp emit `settings: <curModel> <curEffort>` before our JSON object;
	// ParseTitleGenOutput extracts the JSON object, while we can also verify via `settings`!
	res, err := drv.GenerateTitle(context.Background(), cfg, runtime.TitleGenRequest{
		Prompt: "Task title\nsettings\nsay {\"title\":\"移植液态玻璃顶栏\",\"provisional\":false}",
	})
	if err != nil {
		t.Fatalf("GenerateTitle failed: %v", err)
	}
	if res.Title != "移植液态玻璃顶栏" || res.Provisional {
		t.Fatalf("unexpected result: %+v", res)
	}

	rawMethods, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read method log: %v", err)
	}
	gotMethods := strings.Join(strings.Fields(string(rawMethods)), " ")
	// Must switch both model (gemini-4-neon-high -> gemini-3.8-flash) and effort (high -> low)
	// inline after session/new with zero extra model-discovery calls!
	wantMethods := "initialize session/new session/set_config_option session/set_config_option session/prompt"
	if gotMethods != wantMethods {
		t.Fatalf("ACP RPC sequence = %q, want %q", gotMethods, wantMethods)
	}

	// Verify the selected model & effort inside fakeacp actually became gemini-3.8-flash + low:
	resCheck, err := drv.GenerateTitle(context.Background(), cfg, runtime.TitleGenRequest{
		Prompt: "Task title\nsettings",
	})
	if err != nil {
		t.Fatalf("GenerateTitle settings check failed: %v", err)
	}
	if resCheck.Title != "settings: gemini-3.8-flash low" {
		t.Fatalf("expected active ACP session settings to be 'gemini-3.8-flash low', got %q", resCheck.Title)
	}
}
