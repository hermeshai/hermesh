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
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/hermeshai/hermesh/internal/owner"
)

// Core is the running owner role.
type Core struct {
	o       *owner.Device
	handler http.Handler
	port    int
	srv     *http.Server
	token   string
	log     *slog.Logger

	mu     sync.Mutex
	ln     net.Listener // while in front (listen)
	cancel context.CancelFunc
	done   chan struct{}

	streamsMu sync.Mutex
	nextID    int64
	streams   map[int64]*ffiStream
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
	localHandler := o.LocalHandler()
	c := &Core{o: o, handler: localHandler, port: ln.Addr().(*net.TCPAddr).Port, token: hex.EncodeToString(b), log: log, streams: map[int64]*ffiStream{}}
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	mux := http.NewServeMux()
	mux.Handle("/", localHandler)
	mux.Handle("/screen/", o.ScreenHandler()) // Computer Control's picture and input (owner.ScreenHandler)
	c.srv = &http.Server{Handler: c.authorize(mux), Protocols: &protocols}
	c.mu.Lock()
	c.serveLocked(ln)
	c.mu.Unlock()
	c.Resume()
	return c, nil
}

// Port is the loopback port LocalService is served on.
func (c *Core) Port() int { return c.port }

// listenLocked serves LocalService on its port again, unless it is.
func (c *Core) listenLocked() {
	if c.ln != nil {
		return
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", c.port))
	if err != nil {
		c.log.Error("local API not served", "err", err)
		return
	}
	c.serveLocked(ln)
}

func (c *Core) serveLocked(ln net.Listener) {
	c.ln = ln
	go func() {
		if err := c.srv.Serve(ln); err != nil && !errors.Is(err, net.ErrClosed) {
			c.log.Error("local API stopped", "err", err)
		}
	}()
}

// Token is what callers send as "Authorization: Bearer <token>".
func (c *Core) Token() string { return c.token }

// Resume serves LocalService again and keeps the owner's relay connection
// up (the app is in front).
func (c *Core) Resume() {
	started := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listenLocked()
	if c.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.cancel, c.done = cancel, done
	c.log.Info("mobile: core resume", "listen_ms", time.Since(started).Milliseconds())
	go func() {
		defer close(done)
		c.o.Run(ctx)
	}()
}

// Pause stops listening and following the machines, and keeps the relay
// connection to try again first (the app went to the background). iOS may
// take a suspended app's listening socket back without a word, to listen
// no more after it resumed (Apple's TN2277), so it is closed here and
// opened again by Resume; calls under way go on.
func (c *Core) Pause() {
	started := time.Now()
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.cancel, c.done = nil, nil
	if c.ln != nil {
		c.ln.Close()
		c.ln = nil
	}
	c.mu.Unlock()
	c.o.Pause()
	if cancel != nil {
		cancel()
		<-done
	}
	c.log.Info("mobile: core pause", "after_ms", time.Since(started).Milliseconds())
}

// SetCellularOnly records whether the phone currently uses cellular only (no
// Wi-Fi or wired interface) so direct dialing skips unreachable RFC1918 LAN IPs.
func (c *Core) SetCellularOnly(v bool) { c.o.SetCellularOnly(v) }

// Reconnect connects to the relay and the machines again: the network
// changed (Wi-Fi to cellular, say).
func (c *Core) Reconnect() { c.o.Reconnect() }

// PathChanged is Reconnect when the device's addresses changed: the system
// reports a change of the network that left its interfaces as they were.
func (c *Core) PathChanged() { c.o.PathChanged() }

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
