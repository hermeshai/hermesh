// Package her adapts Hermesh's personal assistant ("her") to the v2 runtime.Driver
// contract when Her is configured on a machine.
package her

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

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
		closed: make(chan struct{}),
	}
	s.alive.Store(true)
	if s.cursor != "" {
		s.emit(runtime.Event{Kind: runtime.EventNativeSession, NativeID: s.cursor, ResumeCursor: s.cursor})
	}
	return s, nil
}

type herSession struct {
	drv       *Driver
	events    chan runtime.Event
	closed    chan struct{}
	alive     atomic.Bool
	closeOnce sync.Once

	emitMu       sync.RWMutex
	eventsClosed bool

	mu         sync.Mutex
	cursor     string
	turnCancel context.CancelFunc
}

func (s *herSession) Events() <-chan runtime.Event { return s.events }
func (s *herSession) Alive() bool                  { return s.alive.Load() }
func (s *herSession) Steer(context.Context, string, []runtime.TurnAttachment) error {
	return runtime.ErrSteerUnsupported
}
func (s *herSession) SetSelection(context.Context, *threadv2.ModelSelection) error { return nil }
func (s *herSession) SetModes(context.Context, identityv1.RuntimeMode, threadv2.InteractionMode) error {
	return nil
}

func (s *herSession) Interrupt(context.Context) error {
	s.mu.Lock()
	cancel := s.turnCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (s *herSession) ForceStop() error { return s.Close() }

func (s *herSession) Close() error {
	s.alive.Store(false)
	s.closeOnce.Do(func() {
		s.mu.Lock()
		cancel := s.turnCancel
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		close(s.closed)
		s.emitMu.Lock()
		if !s.eventsClosed {
			s.eventsClosed = true
			close(s.events)
		}
		s.emitMu.Unlock()
	})
	return nil
}

func (s *herSession) emit(ev runtime.Event) bool {
	s.emitMu.RLock()
	defer s.emitMu.RUnlock()
	if s.eventsClosed {
		return false
	}
	select {
	case s.events <- ev:
		return true
	case <-s.closed:
		return false
	}
}

func (s *herSession) StartTurn(ctx context.Context, in runtime.TurnInput) error {
	if !s.Alive() {
		return runtime.ErrSessionClosed
	}
	tctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.turnCancel = cancel
	cur := s.cursor
	s.mu.Unlock()

	go func() {
		defer cancel()
		reply, nextID, err := s.drv.Send(tctx, cur, in.Prompt)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				s.emit(runtime.Event{Kind: runtime.EventTurnInterrupted})
			} else {
				s.emit(runtime.Event{Kind: runtime.EventTurnFailed, ErrorCode: "her_error", ErrorMessage: err.Error()})
			}
			return
		}
		if nextID != "" && nextID != cur {
			s.mu.Lock()
			s.cursor = nextID
			s.mu.Unlock()
			s.emit(runtime.Event{Kind: runtime.EventNativeSession, NativeID: nextID, ResumeCursor: nextID})
		}
		s.emit(runtime.Event{Kind: runtime.EventAssistantMessage, ItemID: "msg_" + in.RunID, Text: reply})
		s.emit(runtime.Event{Kind: runtime.EventTurnCompleted})
	}()
	return nil
}
