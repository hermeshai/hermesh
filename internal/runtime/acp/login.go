package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hermeshai/hermesh/internal/runtime"
)

// Login runs only the ACP authentication handshake, without creating a session
// or prompting the model.
func (p *Provider) Login(ctx context.Context, method string, output io.Writer) error {
	cmd := p.resolve(ctx)
	if cmd.probeErr != nil {
		return cmd.probeErr
	}
	if cmd.bin == "" {
		return errors.New(p.notFoundDetail() + "; install the official Antigravity CLI (agy) or standalone ACP archive (see docs/antigravity.md)")
	}
	proc, err := runtime.StartProc(ctx, cmd.bin, cmd.acpArgs(), "", append(runtime.ProbeEnv(p.PathEnv), cmd.env...))
	if err != nil {
		return err
	}
	defer proc.Stop(time.Second)
	s := newProbeSession(proc, ctx)
	go s.readLoop()
	done := make(chan error, 1)
	go func() {
		res, err := s.call(ctx, "initialize", map[string]any{
			"protocolVersion":    1,
			"clientInfo":         map[string]any{"name": "hermesh", "version": "0.1.0"},
			"clientCapabilities": map[string]any{},
		})
		if err == nil {
			var init struct {
				AuthMethods []struct {
					ID string `json:"id"`
				} `json:"authMethods"`
			}
			if err = json.Unmarshal(res, &init); err == nil {
				if authMethod([]string{method}, init.AuthMethods) == "" {
					var offered []string
					for _, m := range init.AuthMethods {
						offered = append(offered, m.ID)
					}
					err = fmt.Errorf("authentication method %q not offered (available: %s)", method, strings.Join(offered, ", "))
				} else {
					_, err = s.call(ctx, "authenticate", map[string]any{"methodId": method})
				}
			}
		}
		done <- err
	}()
	previous := ""
	printOutput := func() {
		current := proc.StderrTail()
		if current != previous {
			fmt.Fprint(output, strings.TrimPrefix(current, previous))
			previous = current
		}
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			printOutput()
			if err == nil && p.Name() == "antigravity" {
				p.clearChosen()
			}
			return err
		case <-tick.C:
			printOutput()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
