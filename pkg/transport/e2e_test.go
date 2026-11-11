package transport

import (
	"context"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// silent is a tunnel whose far end says nothing, and whose Close leaves a
// read waiting, as closing a yamux stream does: only its deadline ends one.
type silent struct {
	net.Conn
	mu       sync.Mutex
	deadline time.Time
}

func (s *silent) Write(p []byte) (int, error) { return len(p), nil }
func (s *silent) Close() error                { return nil }

func (s *silent) SetDeadline(t time.Time) error {
	s.mu.Lock()
	s.deadline = t
	s.mu.Unlock()
	return nil
}

func (s *silent) Read([]byte) (int, error) {
	for {
		s.mu.Lock()
		d := s.deadline
		s.mu.Unlock()
		if !d.IsZero() && time.Now().After(d) {
			return 0, os.ErrDeadlineExceeded
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The handshake with a machine that never answers ends within its time
// limit: the limit is the tunnel's deadline too.
func TestHandshakeTimeLimit(t *testing.T) {
	key, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := ClientHandshake(ctx, &silent{}, key.CryptoSigner(), func() *identityv1.Credentials { return &identityv1.Credentials{} },
			identity.NewTrust("o_owner"), "m_machine")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a handshake with nobody succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the handshake is still waiting, well past its time limit")
	}
}
