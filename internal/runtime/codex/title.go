package codex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/runtime"
)

func (d *Driver) GenerateTitle(ctx context.Context, cfg *sessionv1.ProviderInstanceConfig, req runtime.TitleGenRequest) (runtime.TitleGenResult, error) {
	bin := d.resolveBinary(cfg)
	if !runtime.Exists(bin) {
		return runtime.TitleGenResult{}, errors.New("codex CLI not found")
	}
	tmpDir, err := os.MkdirTemp("", "hermesh-codex-title-")
	if err != nil {
		return runtime.TitleGenResult{}, err
	}
	defer os.RemoveAll(tmpDir)

	schemaPath := filepath.Join(tmpDir, "schema.json")
	outPath := filepath.Join(tmpDir, "output.json")
	if err := os.WriteFile(schemaPath, []byte(runtime.TitleGenSchema), 0o600); err != nil {
		return runtime.TitleGenResult{}, err
	}

	var imageArgs []string
	for i, img := range req.Images {
		if !strings.HasPrefix(strings.ToLower(img.MimeType), "image/") {
			continue
		}
		p := img.Path
		if p == "" && len(img.Data) > 0 {
			p = filepath.Join(tmpDir, fmt.Sprintf("img_%d", i))
			if os.WriteFile(p, img.Data, 0o600) != nil {
				continue
			}
		}
		if p != "" {
			imageArgs = append(imageArgs, "--image", p)
		}
	}

	var instEnv map[string]string
	if cfg != nil {
		instEnv = cfg.Env
	}
	env := runtime.BuildEnv(d.PathEnv, instEnv)

	args := []string{
		"exec",
		"--ephemeral",
		"--skip-git-repo-check",
		"-s", "read-only",
		"--config", "features.connectors=false",
		"--config", "features.plugins=false",
		"--config", "features.apps=false",
		"--config", "features.codex_hooks=false",
		"--config", "features.hooks=false",
		"--config", `model_reasoning_effort="low"`,
	}
	if m := strings.TrimSpace(req.ModelHint); m != "" && m != "default" {
		args = append(args, "--model", m)
	}
	args = append(args,
		"--output-schema", schemaPath,
		"--output-last-message", outPath,
	)
	args = append(args, imageArgs...)
	args = append(args, "-")

	cmd := runtime.Command(ctx, bin, args...)
	cmd.Dir = tmpDir
	cmd.Env = env
	cmd.Stdin = strings.NewReader(req.Prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail != "" {
			return runtime.TitleGenResult{}, fmt.Errorf("codex title exec: %s", detail)
		}
		return runtime.TitleGenResult{}, err
	}

	rawBytes, err := os.ReadFile(outPath)
	if err != nil || len(bytes.TrimSpace(rawBytes)) == 0 {
		rawBytes = stdout.Bytes()
	}
	return runtime.ParseTitleGenOutput(string(rawBytes))
}
