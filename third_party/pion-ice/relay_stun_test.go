// SPDX-License-Identifier: MIT

package ice

import (
	"bytes"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun/v4"
)

type blockedRelaySTUN struct {
	Candidate
	address string
	writes  atomic.Int32
	started chan []byte
	release chan struct{}
}

func (*blockedRelaySTUN) Type() CandidateType { return CandidateTypeRelay }
func (*blockedRelaySTUN) ID() string          { return "local relay" }
func (c *blockedRelaySTUN) Address() string {
	if c.address != "" {
		return c.address
	}

	return "127.0.0.1" // a TURN server on this computer
}
func (c *blockedRelaySTUN) writeTo(raw []byte, _ Candidate) (int, error) {
	c.writes.Add(1)
	c.started <- raw
	<-c.release
	return len(raw), nil
}

func TestRelaySTUNDoesNotBlockOrQueueDuplicateChecks(t *testing.T) {
	a := &Agent{}
	local := &blockedRelaySTUN{started: make(chan []byte, 3), release: make(chan struct{})}
	defer close(local.release)
	remote, err := NewCandidateHost(&CandidateHostConfig{Network: "udp", Address: "127.0.0.1", Port: 1234, Component: 1})
	if err != nil {
		t.Fatal(err)
	}
	request := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	expected := append([]byte(nil), request.Raw...)
	returned := make(chan struct{})
	go func() {
		a.sendSTUN(request, local, remote)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("the permission blocked the ICE caller")
	}
	// The caller owns the message again once sendSTUN returns.
	clear(request.Raw)
	select {
	case raw := <-local.started:
		if !bytes.Equal(raw, expected) {
			t.Fatal("the asynchronous write reused the caller's message buffer")
		}
	case <-time.After(time.Second):
		t.Fatal("the relay write did not start")
	}
	for range 100 {
		a.sendSTUN(request, local, remote)
	}
	// A reply must not be suppressed by our outstanding request.
	response := stun.MustBuild(stun.TransactionID, stun.BindingSuccess)
	a.sendSTUN(response, local, remote)
	select {
	case <-local.started:
	case <-time.After(time.Second):
		t.Fatal("an outstanding request suppressed its reply")
	}
	if got := local.writes.Load(); got != 2 {
		t.Fatalf("%d blocked writes, want one request and one response", got)
	}
}

// A TURN server on the internet forwards to no private address, and the
// TURN client would ask it for the permission again at every check: such a
// pair is never written to. A server on a private network reaches them.
func TestRelaySTUNSkipsPrivatePeersOfAPublicServer(t *testing.T) {
	host := func(address string) Candidate {
		c, err := NewCandidateHost(&CandidateHostConfig{Network: "udp", Address: address, Port: 1234, Component: 1})
		if err != nil {
			t.Fatal(err)
		}

		return c
	}
	for _, c := range []struct {
		relay, peer string
		written     bool
	}{
		{"203.0.113.5", "192.168.1.9", false},
		{"203.0.113.5", "10.0.0.2", false},
		{"203.0.113.5", "fd00::9", false},
		{"203.0.113.5", "169.254.3.4", false},
		{"203.0.113.5", "127.0.0.1", false},
		{"203.0.113.5", "198.51.100.7", true},
		{"203.0.113.5", "2001:db8::7", true},
		{"192.168.1.2", "192.168.1.9", true},
		{"127.0.0.1", "10.0.0.2", true},
	} {
		a := &Agent{}
		local := &blockedRelaySTUN{address: c.relay, started: make(chan []byte, 1), release: make(chan struct{})}
		close(local.release)
		a.sendSTUN(stun.MustBuild(stun.TransactionID, stun.BindingRequest), local, host(c.peer))
		select {
		case <-local.started:
			if !c.written {
				t.Errorf("a check from the relay %s to %s was written", c.relay, c.peer)
			}
		case <-time.After(200 * time.Millisecond):
			if c.written {
				t.Errorf("no check from the relay %s to %s", c.relay, c.peer)
			}
		}
	}
}
