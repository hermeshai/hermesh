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

	releaseSlot, slotErr := prov.acquireStartSlot(ctx)
	if slotErr != nil {
		return runtime.TitleGenResult{}, slotErr
	}
	defer releaseSlot()

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

	newParams := map[string]any{
		"cwd":        tmpDir,
		"mcpServers": []any{},
	}
	newRes, err := call("session/new", newParams)
	if err != nil && strings.Contains(err.Error(), "acp error -32603:") && ctx.Err() == nil {
		select {
		case <-proc.Done():
		case <-ctx.Done():
			err = ctx.Err()
		case <-time.After(prov.drv.sessionStartRetryDelay()):
			newRes, err = call("session/new", newParams)
		}
	}
	releaseSlot()
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

	// Switch the new ACP session to its smallest offered model and lowest
	// reasoning effort directly from session/new's inline options (0 extra probes).
	opts := optionsOf(newRes)
	if opts.model != nil {
		var offered []runtime.Model
		for _, c := range opts.model.values() {
			offered = append(offered, runtime.Model{ID: c.Value, Name: c.Name})
		}
		wantModel := strings.TrimSpace(req.ModelHint)
		if wantModel == "" || !slices.ContainsFunc(offered, func(m runtime.Model) bool { return m.ID == wantModel }) {
			wantModel, _ = runtime.PickSmallModelID(offered)
		}
		if wantModel != "" && wantModel != opts.model.current() {
			if setRes, sErr := call("session/set_config_option", map[string]any{
				"sessionId": nr.SessionID,
				"configId":  opts.model.ID,
				"value":     wantModel,
			}); sErr == nil {
				opts = optionsOf(setRes)
			}
		}
	} else if len(opts.legacy) > 0 {
		wantModel := strings.TrimSpace(req.ModelHint)
		if wantModel == "" || !slices.ContainsFunc(opts.legacy, func(m runtime.Model) bool { return m.ID == wantModel }) {
			wantModel, _ = runtime.PickSmallModelID(opts.legacy)
		}
		if wantModel != "" && wantModel != opts.current {
			_, _ = call("session/set_model", map[string]any{
				"sessionId": nr.SessionID,
				"modelId":   wantModel,
			})
		}
	}
	if opts.thought != nil {
		if wantEffort := runtime.PickLowestEffort(opts.efforts()); wantEffort != "" && wantEffort != opts.thought.current() {
			_, _ = call("session/set_config_option", map[string]any{
				"sessionId": nr.SessionID,
				"configId":  opts.thought.ID,
				"value":     wantEffort,
			})
		}
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
