package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hermeshai/hermesh/internal/provider"
)

// Login runs only the ACP authentication handshake, without creating a session
// or prompting the model. The server's login instructions go to the terminal;
// credentials remain with the agent. Normal sessions never initiate this flow.
func (p *Provider) Login(ctx context.Context, method string, output io.Writer) error {
	bin, pre, env := p.command()
	if bin == "" {
		return errors.New(p.Command + " not found; install the official Antigravity ACP archive (see docs/antigravity.md)")
	}
	proc, err := provider.StartProc(ctx, bin, append(pre, p.Args...), "", append(provider.ProbeEnv(p.PathEnv), env...))
	if err != nil {
		return err
	}
	defer proc.Stop(time.Second)
	s := &session{name: p.Name(), proc: proc, ctx: ctx, events: make(chan provider.Event, 256), pending: map[int64]func(rpcResult){}}
	go s.read()
	done := make(chan error, 1)
	go func() {
		res, err := s.call(ctx, "initialize", map[string]any{"protocolVersion": 1,
			"clientInfo": map[string]any{"name": "hermesh", "version": "0.1.0"}, "clientCapabilities": map[string]any{}})
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
	// Proc keeps a bounded stderr tail. Forward new text so a browser-login
	// URL is visible while authenticate waits, not only after it completes.
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
			return err
		case <-tick.C:
			printOutput()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
