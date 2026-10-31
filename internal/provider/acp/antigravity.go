package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// The official server requires an explicitly selected auth method, saved by
// authenticate. This only checks configuration; the server validates credentials
// when a session starts. Never read or copy the desktop app's credentials.
func antigravityConfigured(env []string) bool {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".gemini")
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "GEMINI_HOME="); ok && v != "" {
			dir = v
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "antigravity-acp", "settings.json"))
	if err != nil {
		return false
	}
	var settings struct {
		Auth struct {
			Type string `json:"type"`
		} `json:"auth"`
	}
	return json.Unmarshal(b, &settings) == nil && settings.Auth.Type != ""
}
