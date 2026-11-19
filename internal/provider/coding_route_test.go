package provider

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodingRouteBindsConfiguredRecipient(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "recording-agent")
	if err := os.WriteFile(binary, []byte("fictional installed adapter"), 0700); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + dir}
	codex := InspectCodingRoute("codex", binary, dir, env)
	if codex.Recipient != "OpenAI" || codex.Fingerprint == "" {
		t.Fatalf("default Codex: %+v", codex)
	}
	if err := os.Mkdir(filepath.Join(dir, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".codex", "config.toml")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("model_provider = 'openai'\nopenai_base_url = 'https://api.openai.com/v1'\n")
	same := InspectCodingRoute("codex", binary, dir, env)
	if same.Recipient != "OpenAI" || same.Fingerprint == codex.Fingerprint {
		t.Fatalf("explicit Codex config did not change binding: %+v", same)
	}
	write(`"model\u005fprovider" = 'fictional-gateway'`)
	if r := InspectCodingRoute("codex", binary, dir, env); r.Fingerprint != "" {
		t.Fatalf("custom provider reported as first-party: %+v", r)
	}

	claude := InspectCodingRoute("claude", binary, dir, env)
	vertex := InspectCodingRoute("claude", binary, dir, append(env, "CLAUDE_CODE_USE_VERTEX=1"))
	if claude.Recipient != "Anthropic" || vertex.Recipient != "Google Cloud (Vertex AI)" ||
		claude.Fingerprint == "" || claude.Fingerprint == vertex.Fingerprint {
		t.Fatalf("Claude routing was not distinguished: %+v versus %+v", claude, vertex)
	}
	if r := InspectCodingRoute("claude", binary, dir, append(env, "ANTHROPIC_BASE_URL=https://fictional-gateway.invalid")); r.Fingerprint != "" {
		t.Fatalf("custom address reported as Anthropic: %+v", r)
	}
	if r := InspectCodingRoute("opencode", binary, dir, env); r.Fingerprint != "" {
		t.Fatalf("unidentified ACP routing: %+v", r)
	}
	if r := InspectCodingRoute("copilot", binary, dir, append(env, "COPILOT_PROVIDER_API_KEY=fictional-key")); r.Fingerprint != "" {
		t.Fatalf("Copilot BYOK reported as GitHub: %+v", r)
	}
}
