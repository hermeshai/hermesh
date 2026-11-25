package claude

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/runtime"
)

func (d *Driver) GenerateTitle(ctx context.Context, cfg *sessionv1.ProviderInstanceConfig, req runtime.TitleGenRequest) (runtime.TitleGenResult, error) {
	bin := d.resolveBinary(cfg)
	if !runtime.Exists(bin) {
		return runtime.TitleGenResult{}, errors.New("claude CLI not found")
	}
	tmpDir, err := os.MkdirTemp("", "hermesh-claude-title-")
	if err != nil {
		return runtime.TitleGenResult{}, err
	}
	defer os.RemoveAll(tmpDir)

	var instEnv map[string]string
	if cfg != nil {
		instEnv = cfg.Env
	}
	env := runtime.BuildEnv(d.PathEnv, instEnv)
	model := cmp.Or(strings.TrimSpace(req.ModelHint), "haiku")

	imageStdin := buildClaudeImagePromptLine(req.Prompt, req.Images)
	withImages := imageStdin != ""

	out, err := runClaudeTitleAttempt(ctx, bin, tmpDir, env, model, req.Prompt, imageStdin, true, withImages)
	if err != nil && strings.Contains(err.Error(), "no-session-persistence") {
		out, err = runClaudeTitleAttempt(ctx, bin, tmpDir, env, model, req.Prompt, imageStdin, false, withImages)
	}
	if err != nil && withImages {
		out, err = runClaudeTitleAttempt(ctx, bin, tmpDir, env, model, req.Prompt, "", true, false)
	}
	if err != nil {
		return runtime.TitleGenResult{}, err
	}
	return parseClaudeTitleEnvelope(out)
}

func buildClaudeImagePromptLine(prompt string, images []runtime.TurnAttachment) string {
	var content []any
	content = append(content, map[string]any{"type": "text", "text": prompt})
	added := false
	for _, img := range images {
		if !strings.HasPrefix(strings.ToLower(img.MimeType), "image/") {
			continue
		}
		data := img.Data
		if len(data) == 0 && img.Path != "" {
			data, _ = os.ReadFile(img.Path)
		}
		if len(data) == 0 {
			continue
		}
		content = append(content, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": img.MimeType,
				"data":       base64.StdEncoding.EncodeToString(data),
			},
		})
		added = true
		break
	}
	if !added {
		return ""
	}
	b, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": content,
		},
	})
	if err != nil {
		return ""
	}
	return string(b) + "\n"
}

func runClaudeTitleAttempt(ctx context.Context, bin, dir string, env []string, model, prompt, imageStdin string, noPersist, withImages bool) ([]byte, error) {
	streamJSON := withImages && imageStdin != ""
	args := []string{"-p"}
	if streamJSON {
		args = append(args, "--input-format", "stream-json", "--output-format", "stream-json", "--verbose")
	} else {
		args = append(args, "--output-format", "json")
	}
	args = append(args,
		"--json-schema", runtime.TitleGenSchema,
		"--model", model,
		"--tools", "",
		"--setting-sources", "",
		"--disable-slash-commands",
		"--strict-mcp-config",
		"--permission-mode", "dontAsk",
		"--settings", `{"disableAllHooks":true}`,
	)
	if noPersist {
		args = append(args, "--no-session-persistence")
	}
	stdinText := prompt
	if streamJSON {
		stdinText = imageStdin
	}

	cmd := runtime.Command(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdinText)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail != "" {
			return nil, fmt.Errorf("claude title: %s", detail)
		}
		return nil, err
	}
	raw := bytes.TrimSpace(stdout.Bytes())
	if streamJSON {
		lines := bytes.Split(raw, []byte("\n"))
		raw = bytes.TrimSpace(lines[len(lines)-1])
	}
	return raw, nil
}

func parseClaudeTitleEnvelope(raw []byte) (runtime.TitleGenResult, error) {
	var env struct {
		StructuredOutput json.RawMessage `json:"structured_output"`
		Result           string          `json:"result"`
		IsError          bool            `json:"is_error"`
	}
	if json.Unmarshal(raw, &env) == nil {
		if env.IsError {
			return runtime.TitleGenResult{}, fmt.Errorf("claude returned error: %s", env.Result)
		}
		if len(env.StructuredOutput) > 0 && string(env.StructuredOutput) != "null" {
			return runtime.ParseTitleGenOutput(string(env.StructuredOutput))
		}
		if strings.TrimSpace(env.Result) != "" {
			return runtime.ParseTitleGenOutput(env.Result)
		}
	}
	return runtime.ParseTitleGenOutput(string(raw))
}
