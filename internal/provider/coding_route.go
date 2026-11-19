package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// CodingRoute describes the service the adapter is configured to use. It is
// a local configuration observation, not a statement about the agent's TLS
// connection or the service's downstream processors.
type CodingRoute struct {
	Recipient, Description, Fingerprint string
}

// CodingRouter is implemented by adapters with identifiable service routes.
type CodingRouter interface {
	CodingRoute(workDir string, env []string) CodingRoute
}

// InspectCodingRoute recognizes built-in service routes and binds the
// observation to the executable and route settings used to launch it.
func InspectCodingRoute(agent, bin, dir string, env []string) CodingRoute {
	if bin == "" {
		return CodingRoute{}
	}
	fi, err := os.Stat(bin)
	if err != nil {
		return CodingRoute{}
	}
	h := sha256.New()
	fmt.Fprintln(h, bin, fi.Size(), fi.ModTime().UnixNano())
	settings := make(map[string]string)
	for _, e := range env {
		key, val, _ := strings.Cut(e, "=")
		settings[strings.ToUpper(key)] = val
	}
	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	home := settings["HOME"]
	if home == "" {
		home = settings["USERPROFILE"]
	}
	if home == "" {
		return CodingRoute{}
	}
	var recipient, route string
	switch agent {
	case "codex":
		recipient, route = "OpenAI", "Codex using its built-in OpenAI service"
	case "claude":
		recipient, route = "Anthropic", "Claude Code using its built-in Anthropic service"
		for _, cloud := range []struct{ env, recipient string }{
			{"CLAUDE_CODE_USE_BEDROCK", "Amazon Web Services (Bedrock)"},
			{"CLAUDE_CODE_USE_VERTEX", "Google Cloud (Vertex AI)"},
			{"CLAUDE_CODE_USE_FOUNDRY", "Microsoft Azure (Foundry)"},
		} {
			if !enabledRouteSetting(settings[cloud.env]) {
				continue
			}
			if recipient != "Anthropic" {
				return CodingRoute{}
			}
			recipient, route = cloud.recipient, "Claude Code using "+cloud.recipient
		}
	case "gemini":
		recipient, route = "Google", "Gemini CLI using Google's Gemini service"
		if enabledRouteSetting(settings["GOOGLE_GENAI_USE_VERTEXAI"]) {
			recipient, route = "Google Cloud (Vertex AI)", "Gemini CLI using Google Cloud Vertex AI"
		}
	case "antigravity":
		recipient, route = "Google", "Antigravity ACP using Google's Antigravity service"
	case "cursor":
		recipient, route = "Cursor (Anysphere)", "Cursor's managed coding service"
	case "copilot":
		recipient, route = "GitHub", "GitHub Copilot's managed coding service"
	default:
		return CodingRoute{}
	}
	for _, key := range keys {
		val := settings[key]
		if val == "" {
			continue
		}
		if routeOverride(agent, key, val) {
			return CodingRoute{}
		}
		if routeSetting(agent, key) || key == "HOME" || key == "USERPROFILE" {
			fmt.Fprintln(h, key, val)
		}
	}
	read := func(path string, bound bool, valid func(string) bool) bool {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return true
		}
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
		f, err := os.Open(path)
		if err != nil {
			return false
		}
		defer f.Close()
		const max = 2 << 20
		b, err := io.ReadAll(io.LimitReader(f, max+1))
		if err != nil || len(b) > max || !valid(string(b)) {
			return false
		}
		if bound {
			fmt.Fprintf(h, "%s %x\n", filepath.Base(path), sha256.Sum256(b))
		}
		return true
	}
	readConfig := func(path string) bool {
		return read(path, true, func(config string) bool {
			lower := strings.ToLower(config)
			switch agent {
			case "codex":
				var parsed map[string]any
				return toml.Unmarshal([]byte(config), &parsed) == nil && knownCodexSettings(parsed)
			case "claude":
				return !containsAny(lower, "\\u", "base_url", "baseurl", "endpoint_url", "use_bedrock", "use_vertex", "use_foundry", "use_agent_platform", "proxy", "managed_by_host", "apikeyhelper", "api_key_helper")
			default:
				return !containsAny(lower, "\\u", "base_url", "baseurl", "endpoint", "proxy", "vertex", "models.providers", "modelprovider")
			}
		})
	}
	homeSetting := func(key, fallback string) string {
		if v := settings[key]; v != "" {
			return v
		}
		if filepath.IsAbs(fallback) {
			return fallback
		}
		return filepath.Join(home, fallback)
	}
	projectSettings := func(files ...string) bool {
		for d := dir; d != "" && d != filepath.Dir(d); d = filepath.Dir(d) {
			for _, f := range files {
				if !readConfig(filepath.Join(d, f)) {
					return false
				}
			}
			if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
				break
			}
		}
		return true
	}
	switch agent {
	case "codex":
		config := homeSetting("CODEX_HOME", ".codex")
		if !readConfig(filepath.Join(config, "config.toml")) ||
			!readConfig(filepath.Join(config, "managed_config.toml")) ||
			!readConfig("/etc/codex/config.toml") || !readConfig("/etc/codex/requirements.toml") ||
			!readConfig("/Library/Application Support/OpenAI/Codex/requirements.toml") ||
			!projectSettings(".codex/config.toml") {
			return CodingRoute{}
		}
	case "claude":
		if recipient == "Amazon Web Services (Bedrock)" &&
			!readConfig(homeSetting("AWS_CONFIG_FILE", ".aws/config")) {
			return CodingRoute{}
		}
		if !readConfig(filepath.Join(homeSetting("CLAUDE_CONFIG_DIR", ".claude"), "settings.json")) ||
			!readConfig("/etc/claude-code/managed-settings.json") ||
			!readConfig("/Library/Application Support/ClaudeCode/managed-settings.json") ||
			!readConfig(filepath.Join(settings["PROGRAMFILES"], "ClaudeCode", "managed-settings.json")) ||
			!projectSettings(".claude/settings.json", ".claude/settings.local.json", ".mcp.json") {
			return CodingRoute{}
		}
	case "antigravity":
		if !readConfig(filepath.Join(homeSetting("GEMINI_HOME", ".gemini"), "antigravity-acp", "settings.json")) {
			return CodingRoute{}
		}
	case "gemini":
		gemini := filepath.Join(homeSetting("GEMINI_CLI_HOME", "."), ".gemini")
		system := "/etc/gemini-cli"
		switch runtime.GOOS {
		case "darwin":
			system = "/Library/Application Support/GeminiCli"
		case "windows":
			system = filepath.Join(settings["PROGRAMDATA"], "gemini-cli")
		}
		if !readConfig(homeSetting("GEMINI_CLI_SYSTEM_SETTINGS_PATH", filepath.Join(system, "settings.json"))) ||
			!readConfig(homeSetting("GEMINI_CLI_SYSTEM_DEFAULTS_PATH", filepath.Join(system, "system-defaults.json"))) ||
			!readConfig(filepath.Join(gemini, "settings.json")) ||
			!readConfig(filepath.Join(gemini, ".env")) ||
			!readConfig(filepath.Join(home, ".env")) ||
			!projectSettings(".gemini/settings.json", ".gemini/.env", ".env") {
			return CodingRoute{}
		}
	case "cursor":
		if !readConfig(filepath.Join(home, ".cursor", "cli-config.json")) ||
			!projectSettings(".cursor/cli-config.json") {
			return CodingRoute{}
		}
	case "copilot":
		config := homeSetting("COPILOT_HOME", ".copilot")
		providers := settings["COPILOT_PROVIDERS_CONFIG"]
		if providers == "" {
			providers = filepath.Join(config, "providers.json")
		}
		if !read(providers, true, func(s string) bool { return strings.TrimSpace(s) == "{}" }) ||
			!readConfig(filepath.Join(config, "config.json")) || !projectSettings(".github/copilot/config.json") {
			return CodingRoute{}
		}
	}
	return CodingRoute{Recipient: recipient, Description: route + ". Hermesh reads local route settings. The agent manages its connection, so Hermesh cannot verify the exact network endpoint.", Fingerprint: hex.EncodeToString(h.Sum(nil))}
}

// knownCodexSettings checks provider selection in config and legacy profiles.
func knownCodexSettings(config map[string]any) bool {
	for k, v := range config {
		switch k {
		case "model_provider":
			if v != "openai" {
				return false
			}
		case "openai_base_url":
			if v != "https://api.openai.com/v1" && v != "https://api.openai.com/v1/" {
				return false
			}
		case "chatgpt_base_url":
			if v != "https://chatgpt.com/backend-api/" && v != "https://chatgpt.com/backend-api" {
				return false
			}
		case "model_providers":
			if providers, ok := v.(map[string]any); !ok || providers["openai"] != nil {
				return false
			}
		case "include", "config_file":
			return false
		case "profiles":
			profiles, ok := v.(map[string]any)
			if !ok {
				return false
			}
			for _, profile := range profiles {
				if p, ok := profile.(map[string]any); !ok || !knownCodexSettings(p) {
					return false
				}
			}
		}
	}
	return true
}

func routeSetting(agent, key string) bool {
	if strings.HasPrefix(key, strings.ToUpper(agent)+"_") {
		return true
	}
	switch agent {
	case "codex":
		return strings.HasPrefix(key, "OPENAI_")
	case "claude":
		return strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "AWS_") ||
			strings.HasPrefix(key, "GOOGLE_") || strings.HasPrefix(key, "AZURE_")
	case "gemini", "antigravity":
		return strings.HasPrefix(key, "GOOGLE_") || strings.HasPrefix(key, "GEMINI_")
	}
	return false
}

func routeOverride(agent, key, val string) bool {
	if key == "HTTP_PROXY" || key == "HTTPS_PROXY" || key == "ALL_PROXY" ||
		key == "SSL_CERT_FILE" || key == "NODE_EXTRA_CA_CERTS" {
		return true
	}
	if agent == "copilot" && strings.HasPrefix(key, "COPILOT_PROVIDER_") {
		return true
	}
	if !routeSetting(agent, key) {
		return false
	}
	if agent == "codex" && key == "OPENAI_BASE_URL" {
		return strings.TrimSuffix(val, "/") != "https://api.openai.com/v1"
	}
	if agent == "claude" && key == "ANTHROPIC_BASE_URL" {
		return strings.TrimSuffix(val, "/") != "https://api.anthropic.com"
	}
	if containsAny(key, "BASE_URL", "BASEURL", "ENDPOINT", "PROXY", "PROVIDER_MANAGED_BY_HOST") {
		return true
	}
	return agent == "claude" && key == "CLAUDE_CODE_USE_AGENT_PLATFORM" && enabledRouteSetting(val)
}

func enabledRouteSetting(v string) bool {
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}
