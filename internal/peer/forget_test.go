package peer

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hermeshai/hermesh/pkg/transport"
)

// Two independent RPC failures on an old tunnel must not close a replacement
// installed between the two failure callbacks. Uses real HTTP/2 connections.
func TestLateOldRPCFailureKeepsReplacementTunnel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var dials atomic.Int32
	entered := make(chan struct{}, 2)
	d := &Dialer{Link: func(context.Context, string) (net.Conn, string, error) {
		dials.Add(1)
		a, b := net.Pipe()
		go transport.ServeH2(ctx, b, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/wait" {
				entered <- struct{}{}
				<-r.Context().Done()
				return
			}
			io.WriteString(w, "ready")
		}))
		return a, "machine", nil
	}}
	c, err := d.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	old, err := c.Machine(ctx, "m")
	if err != nil {
		t.Fatal(err)
	}
	// The first caller will time out. The second is still waiting on that same
	// old tunnel, and its independent error callback is deliberately delayed.
	firstCtx, firstCancel := context.WithCancel(ctx)
	defer firstCancel()
	firstDone, oldFailed := make(chan error, 1), make(chan error, 1)
	issue := func(callCtx context.Context, result chan<- error) {
		r, _ := http.NewRequestWithContext(callCtx, "GET", "https://machine/wait", nil)
		res, err := old.HTTP().Do(r)
		if res != nil {
			res.Body.Close()
		}
		result <- err
	}
	go issue(firstCtx, firstDone)
	go issue(ctx, oldFailed)
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	firstCancel()
	if err := <-firstDone; err == nil {
		t.Fatal("first RPC should be cancelled")
	}
	c.ForgetMachine(old) // first RPC's error recovery
	if err := <-oldFailed; err == nil {
		t.Fatal("old in-flight RPC should fail")
	}
	replacement, err := c.Machine(ctx, "m")
	if err != nil {
		t.Fatal(err)
	}
	if replacement == old {
		t.Fatal("did not make a replacement")
	}
	r, _ := http.NewRequestWithContext(ctx, "GET", "https://machine/ready", nil)
	res, err := replacement.HTTP().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	c.ForgetMachine(old) // delayed old RPC's error recovery
	got, err := c.Machine(ctx, "m")
	if err != nil {
		t.Fatal(err)
	}
	if got != replacement {
		t.Fatalf("late old RPC failure closed healthy replacement: dials=%d; want 2", dials.Load())
	}
	res, err = replacement.HTTP().Do(r.Clone(ctx))
	if err != nil {
		t.Fatalf("replacement RPC after late failure: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil || string(body) != "ready" {
		t.Fatalf("replacement response: %q, %v", body, err)
	}
}
