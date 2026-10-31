package acp

import "runtime"

// Agents are the ACP agents Hermesh knows how to start. Commands follow
// each vendor's documented ACP entry point; flags that pre-approve tools
// are not needed because the gate answers permission requests.
var Agents = []Agent{
	{Name: "gemini", Command: "gemini", Args: []string{"--acp"}},
	antigravityAgent(runtime.GOOS),
	{Name: "cursor", Command: "cursor-agent", Args: []string{"acp"}, DesktopApps: []string{"Cursor.app"}, Auth: []string{"cursor_login"}},
	{Name: "copilot", Command: "copilot", Args: []string{"--acp"}},
	{Name: "opencode", Command: "opencode", Args: []string{"acp"}, Paths: []string{".opencode/bin/opencode"}},
	{Name: "qwen", Command: "qwen", Args: []string{"--acp"}},
	{Name: "goose", Command: "goose", Args: []string{"acp"}},
	{Name: "auggie", Command: "auggie", Args: []string{"--acp"}},
	{Name: "kimi", Command: "kimi", Args: []string{"acp"}},
	{Name: "kiro", Command: "kiro-cli", Args: []string{"acp"}, DesktopApps: []string{"Kiro.app"}},
	{Name: "qoder", Command: "qodercli", Args: []string{"--acp"}, DesktopApps: []string{"Qoder.app"}},
	// The DSH Desktop app keeps its own dsh, with no `dsh` command.
	{Name: "deepseek", Command: "dsh", Args: []string{"--profile", "acp"}, Apps: []App{{Bundle: "DSH Desktop.app",
		Exec: "Contents/MacOS/DSH Desktop", Script: "Contents/Resources/app.asar.unpacked/lib/desktop-cli.js"}}},
	{Name: "codebuddy", Command: "codebuddy", Args: []string{"--acp"}},
	{Name: "grok", Command: "grok", Args: []string{"--no-auto-update", "agent", "stdio"}, Auth: []string{"cached_token", "xai.api_key"}},
}

// Google's ACP distribution is separate from both the desktop app and agy.
// Keep localharness_external beside the server when installing the archive.
func antigravityAgent(goos string) Agent {
	command := "agy_acp_server.par"
	var args []string
	if goos == "windows" {
		command = "agy_acp_server.exe"
	} else if goos == "linux" {
		args = []string{"--uid="} // the entry point in Google's ACP registry
	}
	return Agent{Name: "antigravity", Command: command, Args: args,
		Paths:       []string{".local/share/antigravity-acp/" + command},
		DesktopApps: []string{"Antigravity.app"}}
}

// Providers returns a provider for every known ACP agent.
func Providers(pathEnv string) []*Provider {
	ps := make([]*Provider, len(Agents))
	for i, a := range Agents {
		ps[i] = &Provider{Agent: a, PathEnv: pathEnv}
	}
	return ps
}
