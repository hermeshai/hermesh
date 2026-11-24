package acp

import goruntime "runtime"

// Agents are the ACP agents Hermesh knows how to start.
var Agents = []Agent{
	{Name: "gemini", Command: "gemini", Args: []string{"--acp"}},
	antigravityAgent(goruntime.GOOS),
	{Name: "cursor", Command: "cursor-agent", Args: []string{"acp"}, DesktopApps: []string{"Cursor.app"}, Auth: []string{"cursor_login"}},
	{Name: "copilot", Command: "copilot", Args: []string{"--acp"}},
	{Name: "opencode", Command: "opencode", Args: []string{"acp"}, Paths: []string{".opencode/bin/opencode"}},
	{Name: "qwen", Command: "qwen", Args: []string{"--acp"}},
	{Name: "goose", Command: "goose", Args: []string{"acp"}},
	{Name: "auggie", Command: "auggie", Args: []string{"--acp"}},
	{Name: "kimi", Command: "kimi", Args: []string{"acp"}},
	{Name: "kiro", Command: "kiro-cli", Args: []string{"acp"}, DesktopApps: []string{"Kiro.app"}},
	{Name: "qoder", Command: "qodercli", Args: []string{"--acp"}, DesktopApps: []string{"Qoder.app"}},
	{Name: "deepseek", Command: "dsh", Args: []string{"--profile", "acp"}, Apps: []App{
		{Bundle: "DSH Desktop.app", Exec: "Contents/MacOS/DSH Desktop", Script: "Contents/Resources/app.asar.unpacked/lib/desktop-cli.js"},
		{Bundle: "DeepSeek Harness", Exec: "DeepSeek Harness.exe", Script: "resources/app.asar/dsh/node_modules/@deepseek-ai/dsh/lib/bin.js"},
	}},
	{Name: "codebuddy", Command: "codebuddy", Args: []string{"--acp"}},
	{Name: "grok", Command: "grok", Args: []string{"--no-auto-update", "agent", "stdio"}, Auth: []string{"cached_token", "xai.api_key"}},
}

func AgentByName(name string) (Agent, bool) {
	for _, a := range Agents {
		if a.Name == name {
			return a, true
		}
	}
	return Agent{Name: name, Command: name, Args: []string{"--acp"}}, false
}

func antigravityAgent(goos string) Agent {
	standalone := "agy_acp_server.par"
	var standaloneArgs []string
	if goos == "windows" {
		standalone = "agy_acp_server.exe"
	} else if goos == "linux" {
		standaloneArgs = []string{"--uid="}
	}
	return Agent{
		Name:        "antigravity",
		Command:     "agy",
		Args:        []string{"acp"},
		DesktopApps: []string{"Antigravity.app"},
		fallback: &fallbackAgent{
			command: standalone,
			args:    standaloneArgs,
			paths:   []string{".local/share/antigravity-acp/" + standalone},
		},
	}
}

// Providers returns a discovery/login handle for every known ACP agent.
func Providers(pathEnv string) []*Provider {
	ps := make([]*Provider, len(Agents))
	for i, a := range Agents {
		ps[i] = &Provider{Agent: a, PathEnv: pathEnv}
	}
	return ps
}
