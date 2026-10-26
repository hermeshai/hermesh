package mcpgw

import (
	"testing"

	"github.com/hermeshai/hermesh/internal/cli"
)

func TestAdvertisedWaitLimits(t *testing.T) {
	for _, tool := range cli.Tools() {
		if _, ok := toolDescriptions[tool.Name]; !ok {
			t.Errorf("%s needs an MCP-specific description", tool.Name)
		}
		if tool.Name != "session_start" && tool.Name != "message_send" && tool.Name != "session_answer" {
			continue
		}
		for _, timeout := range []string{"", "0s", "30s", "50s", "60s"} {
			values := map[string]any{"wait": true}
			if timeout != "" {
				values["timeout"] = timeout
			}
			capWait(tool, values)
			want := "50s"
			if timeout == "30s" {
				want = timeout
			}
			if values["timeout"] != want {
				t.Errorf("%s timeout=%q: got %v, want %s", tool.Name, timeout, values["timeout"], want)
			}
		}
	}
}
