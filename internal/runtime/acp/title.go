package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/runtime"
)

func (d *Driver) GenerateTitle(ctx context.Context, cfg *sessionv1.ProviderInstanceConfig, req runtime.TitleGenRequest) (runtime.TitleGenResult, error) {
	var instEnv map[string]string
	if cfg != nil {
		instEnv = cfg.Env
	}
	baseEnv := runtime.BuildEnv(d.PathEnv, instEnv)
	prov, cmd := d.resolveLaunch(ctx, cfg, baseEnv)
	if cmd.probeErr != nil {
		return runtime.TitleGenResult{}, cmd.probeErr
	}
	if !runtime.Exists(cmd.bin) {
		return runtime.TitleGenResult{}, errors.New(prov.notFoundDetail())
	}
	if strings.Contains(strings.ToLower(filepath.Base(cmd.bin)), "fakeacp") {
		if os.Getenv("HERMESH_FAKE_TITLE_GEN") == "1" {
			return runtime.FakeGenerateTitle(req), nil
		}
		return runtime.TitleGenResult{}, errors.New("fakeacp title generation disabled")
	}

	tmpDir, err := os.MkdirTemp("", "hermesh-acp-title-")
	if err != nil {
		return runtime.TitleGenResult{}, err
	}
	defer os.RemoveAll(tmpDir)

	env := append(slices.Clip(baseEnv), cmd.env...)
	proc, err := runtime.StartProc(ctx, cmd.bin, cmd.acpArgs(), tmpDir, env)
	if err != nil {
		return runtime.TitleGenResult{}, err
	}
	defer proc.Stop(time.Second)

	var (
		nextID   atomic.Int64
		mu       sync.Mutex
		pending  = map[int64]chan rpcRes{}
		output   strings.Builder
		toolUsed atomic.Bool
	)

	go func() {
		_ = proc.Lines(func(line []byte) {
			var m struct {
				ID     *int64          `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(line, &m) != nil {
				return
			}
			// Server-initiated request (e.g. tool or permission): decline immediately.
			if m.ID != nil && m.Method != "" {
				toolUsed.Store(true)
				resp, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"id":      *m.ID,
					"error":   map[string]any{"code": -32601, "message": "tools disabled for title generation"},
				})
				_ = proc.WriteLine(resp)
				return
			}
			if m.ID != nil {
				mu.Lock()
				ch := pending[*m.ID]
				delete(pending, *m.ID)
				mu.Unlock()
				if ch != nil {
					var r rpcRes
					if m.Error != nil {
						r.err = fmt.Errorf("acp error %d: %s", m.Error.Code, m.Error.Message)
					} else {
						r.result = m.Result
					}
					ch <- r
				}
				return
			}
			if m.Method == "session/update" {
				var u struct {
					Update struct {
						SessionUpdate string `json:"sessionUpdate"`
						Content       struct {
							Type string `json:"type"`
							Text string `json:"text"`
						} `json:"content"`
					} `json:"update"`
				}
				if json.Unmarshal(m.Params, &u) == nil {
					if u.Update.SessionUpdate == "tool_call" || u.Update.SessionUpdate == "tool_call_update" {
						toolUsed.Store(true)
						return
					}
					if u.Update.SessionUpdate == "agent_message_chunk" && u.Update.Content.Type == "text" {
						mu.Lock()
						if output.Len() < 16384 {
							output.WriteString(u.Update.Content.Text)
						}
						mu.Unlock()
					}
				}
			}
		})
	}()

	call := func(method string, params any) (json.RawMessage, error) {
		id := nextID.Add(1)
		ch := make(chan rpcRes, 1)
		mu.Lock()
		pending[id] = ch
		mu.Unlock()
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if err := proc.WriteLine(b); err != nil {
			return nil, err
		}
		select {
		case r := <-ch:
			return r.result, r.err
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-proc.Done():
			return nil, fmt.Errorf("acp process exited: %v", proc.Err())
		}
	}

	initRes, err := call("initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{},
		"clientInfo":         map[string]any{"name": "hermesh", "version": "2.0.0"},
	})
	if err != nil {
		return runtime.TitleGenResult{}, err
	}
	var caps struct {
		AuthMethods []struct {
			ID string `json:"id"`
		} `json:"authMethods"`
	}
	_ = json.Unmarshal(initRes, &caps)
	authID := authMethod(prov.Auth, caps.AuthMethods)
	if authID == "" {
		authID = pickAuthMethod(caps.AuthMethods, env)
	}
	if authID != "" {
		if _, err := call("authenticate", map[string]any{"methodId": authID}); err != nil {
			return runtime.TitleGenResult{}, err
		}
	}

	newRes, err := call("session/new", map[string]any{
		"cwd":        tmpDir,
		"mcpServers": []any{},
	})
	if err != nil {
		return runtime.TitleGenResult{}, err
	}
	var nr struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(newRes, &nr)
	if nr.SessionID == "" {
		return runtime.TitleGenResult{}, errors.New("acp session/new returned empty sessionId")
	}

	wrappedPrompt := "Do not call any tools, read files, or run commands. Reply ONLY with the requested JSON object.\n\n" + req.Prompt
	if _, err := call("session/prompt", map[string]any{
		"sessionId": nr.SessionID,
		"prompt":    []any{map[string]any{"type": "text", "text": wrappedPrompt}},
	}); err != nil {
		return runtime.TitleGenResult{}, err
	}
	if toolUsed.Load() {
		return runtime.TitleGenResult{}, errors.New("acp agent attempted tool use during title generation")
	}

	mu.Lock()
	rawText := output.String()
	mu.Unlock()
	return runtime.ParseTitleGenOutput(rawText)
}
