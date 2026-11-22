// Package her adapts Hermesh's personal assistant ("her") to the v2 runtime.Driver
// contract when Her is configured on a machine.
package her

import (
	"context"
	"errors"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/internal/runtime"
)

type PromptFunc func(ctx context.Context, sessionID, prompt string) (reply string, nextSessionID string, err error)

type Driver struct {
	Available func() bool
	Send      PromptFunc
}

func NewDriver(available func() bool, send PromptFunc) *Driver {
	return &Driver{Available: available, Send: send}
}

func (d *Driver) Kind() string { return "her" }

func (d *Driver) Capabilities(_ string) *threadv2.ProviderCapabilities {
	return &threadv2.ProviderCapabilities{
		NativeResume: true,
		SupportedRuntimeModes: []identityv1.RuntimeMode{
			identityv1.RuntimeMode_RUNTIME_MODE_AUTO,
			identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS,
		},
	}
}

func (d *Driver) Probe(_ context.Context, _ *threadv2.ProviderInstanceConfig, _ string) runtime.ProbeResult {
	ok := d.Available != nil && d.Available()
	if !ok {
		return runtime.ProbeResult{
			Installed:            false,
			Compatibility:        threadv2.CompatibilityStatus_COMPATIBILITY_STATUS_UNSUPPORTED,
			CompatibilityMessage: "Her is not enabled on this machine",
		}
	}
	return runtime.ProbeResult{
		Installed:     true,
		Authenticated: true,
		AuthLabel:     "Her Assistant",
		Version:       "2.0",
		Compatibility: threadv2.CompatibilityStatus_COMPATIBILITY_STATUS_SUPPORTED,
	}
}

func (d *Driver) StartSession(_ context.Context, spec runtime.StartSpec) (runtime.Session, error) {
	if d.Send == nil {
		return nil, errors.New("her assistant bridge is not configured")
	}
	s := &herSession{
		drv:    d,
		cursor: spec.ResumeCursor,
		events: make(chan runtime.Event, 32),
		alive:  true,
	}
	if s.cursor != "" {
		s.events <- runtime.Event{Kind: runtime.EventNativeSession, NativeID: s.cursor, ResumeCursor: s.cursor}
	}
	return s, nil
}

type herSession struct {
	drv    *Driver
	cursor string
	events chan runtime.Event
	alive  bool
}

func (s *herSession) Events() <-chan runtime.Event { return s.events }
func (s *herSession) Alive() bool                  { return s.alive }
func (s *herSession) Steer(context.Context, string, []runtime.TurnAttachment) error {
	return runtime.ErrSteerUnsupported
}
func (s *herSession) SetSelection(context.Context, *threadv2.ModelSelection) error { return nil }
func (s *herSession) SetModes(context.Context, identityv1.RuntimeMode, threadv2.InteractionMode) error {
	return nil
}
func (s *herSession) Interrupt(context.Context) error { return nil }
func (s *herSession) ForceStop() error                { return s.Close() }
func (s *herSession) Close() error {
	if s.alive {
		s.alive = false
		close(s.events)
	}
	return nil
}

func (s *herSession) StartTurn(ctx context.Context, in runtime.TurnInput) error {
	go func() {
		reply, nextID, err := s.drv.Send(ctx, s.cursor, in.Prompt)
		if err != nil {
			s.events <- runtime.Event{Kind: runtime.EventTurnFailed, ErrorCode: "her_error", ErrorMessage: err.Error()}
			return
		}
		if nextID != "" && nextID != s.cursor {
			s.cursor = nextID
			s.events <- runtime.Event{Kind: runtime.EventNativeSession, NativeID: nextID, ResumeCursor: nextID}
		}
		s.events <- runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: "msg_" + in.RunID, Text: reply}
		s.events <- runtime.Event{Kind: runtime.EventTurnCompleted}
	}()
	return nil
}
