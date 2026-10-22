// Package mobile runs the owner role inside the iPhone app, in the app's own
// process: hermeshd's owner without a local machine (an iPhone approves,
// it never runs agents). It serves LocalService on a loopback port; only a
// caller with the token minted at each start may use it, since every app on
// the phone shares the loopback interface.
package mobile

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync"

	"github.com/hermeshai/hermesh/internal/owner"
)

// Core is the running owner role.
type Core struct {
	o     *owner.Owner
	ln    net.Listener
	srv   *http.Server
	token string

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// Start opens the owner's data under dataDir, serves LocalService and keeps
// the relay connection up (Resume).
func Start(dataDir string, log *slog.Logger) (*Core, error) {
	o, err := owner.Open(owner.Config{DataDir: filepath.Join(dataDir, "owner"), Logger: log, NoLocalMachine: true})
	if err != nil {
		return nil, err
	}
	b := make([]byte, 32)
	rand.Read(b)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	c := &Core{o: o, ln: ln, token: hex.EncodeToString(b)}
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	c.srv = &http.Server{Handler: c.authorize(o.LocalHandler()), Protocols: &protocols}
	go func() {
		if err := c.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("local API stopped", "err", err)
		}
	}()
	c.Resume()
	return c, nil
}

// Port is the loopback port LocalService is served on.
func (c *Core) Port() int { return c.ln.Addr().(*net.TCPAddr).Port }

// Token is what callers send as "Authorization: Bearer <token>".
func (c *Core) Token() string { return c.token }

// Resume keeps the owner's relay connection up (the app is in front).
func (c *Core) Resume() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.cancel, c.done = cancel, done
	go func() {
		defer close(done)
		c.o.Run(ctx)
	}()
}

// Pause closes the relay connection (the app went to the background); a
// call to LocalService still reconnects when it needs the relay.
func (c *Core) Pause() {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.cancel, c.done = nil, nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (c *Core) authorize(next http.Handler) http.Handler {
	want := []byte("Bearer " + c.token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"code":"unauthenticated","message":"missing or wrong token"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
