package peer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

// relayOutage keeps the first outer connection answering yamux Pings while
// its machine streams no longer deliver bytes. A replacement outer connection
// serves the same machine with real mutually authenticated TLS and HTTP/2.
func relayOutage(t *testing.T) (*Dialer, string, *atomic.Int32) {
	t.Helper()
	root, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	device, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	machine, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerID := identity.OwnerID(identity.MarshalPublicKey(root.Public()))
	machineID := identity.MachineID(identity.MarshalPublicKey(machine.Public()))
	rc, err := identity.Sign(&identityv1.RootCert{OwnerId: ownerID, Version: 1, IssuedAt: timestamppb.Now(), RootKeys: []*identityv1.RootKey{{PublicKey: identity.MarshalPublicKey(root.Public())}}}, root)
	if err != nil {
		t.Fatal(err)
	}
	chain := &identityv1.RootChain{Certs: []*identityv1.Signed{rc}}
	dc, err := identity.Sign(&identityv1.OwnerDeviceCert{OwnerId: ownerID, DevicePublicKey: identity.MarshalPublicKey(device.Public()), IssuedAt: timestamppb.Now()}, root)
	if err != nil {
		t.Fatal(err)
	}
	mc, err := identity.Sign(&identityv1.MachineCert{OwnerId: ownerID, MachineId: machineID, MachinePublicKey: identity.MarshalPublicKey(machine.Public()), IssuedAt: timestamppb.Now()}, root)
	if err != nil {
		t.Fatal(err)
	}
	deviceCreds := func() *identityv1.Credentials {
		return &identityv1.Credentials{RootChain: chain, Subject: &identityv1.Credentials_DeviceCert{DeviceCert: dc}}
	}
	machineCreds := func() *identityv1.Credentials {
		return &identityv1.Credentials{RootChain: chain, Subject: &identityv1.Credentials_MachineCert{MachineCert: mc}}
	}
	dials := new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"hermesh.v1"}})
		if err != nil {
			return
		}
		conn := transport.NetConnFromWS(ws)
		defer conn.Close()
		number := dials.Add(1)
		if err := transport.WriteFrame(conn, &relayv1.Challenge{Nonce: make([]byte, 32), RelayId: "relay"}); err != nil {
			return
		}
		var hello relayv1.Hello
		if err := transport.ReadFrame(conn, &hello); err != nil {
			return
		}
		if err := transport.WriteFrame(conn, &relayv1.HelloResult{Ok: true}); err != nil {
			return
		}
		sess, err := yamux.Server(conn, transport.YamuxConfig())
		if err != nil {
			t.Error(err)
			return
		}
		defer sess.Close()
		for {
			st, err := sess.AcceptStream()
			if err != nil {
				return
			}
			go func() {
				defer st.Close()
				var h relayv1.StreamHeader
				if err := transport.ReadFrame(st, &h); err != nil {
					return
				}
				if h.Kind != relayv1.StreamKind_STREAM_KIND_TUNNEL {
					return
				}
				if number == 1 {
					io.Copy(io.Discard, st)
					return
				}
				tc, _, err := transport.ServerHandshake(context.Background(), st, machine.CryptoSigner(), machineCreds, identity.NewTrust(ownerID), func(*identity.Subject) error { return nil })
				if err != nil {
					return
				}
				defer tc.Close()
				transport.ServeH2(context.Background(), tc, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/slow" {
						<-r.Context().Done()
						return
					}
					fmt.Fprint(w, "recovered")
				}))
			}()
		}
	}))
	t.Cleanup(srv.Close)
	return &Dialer{RelayURL: srv.URL, Role: relayv1.Role_ROLE_OWNER, Key: device, Credentials: deviceCreds, Trust: identity.NewTrust(ownerID)}, machineID, dials
}

func TestTunnelTimeoutReplacesUnresponsiveRelay(t *testing.T) {
	for _, manualDrop := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual_drop_%t", manualDrop), func(t *testing.T) {
			d, id, dials := relayOutage(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, err := d.Connect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if err := c.Ping(ctx); err != nil {
				t.Fatalf("outer yamux does answer ping: %v", err)
			}
			attempt, stop := context.WithTimeout(ctx, 150*time.Millisecond)
			_, err = c.Machine(attempt, id)
			stop()
			if err == nil {
				t.Fatal("first connection should not deliver the TLS handshake")
			}
			t.Logf("first tunnel result: %v", err)
			failed := err
			for _, ordinary := range []error{context.DeadlineExceeded, context.Canceled, transport.ErrAdmission} {
				if c.RecoverTunnel(ordinary) {
					t.Fatalf("ordinary error reset relay: %v", ordinary)
				}
			}
			if err := c.Ping(ctx); err != nil {
				t.Fatalf("ordinary error damaged the relay: %v", err)
			}
			if manualDrop {
				c.Drop()
			} else if !c.RecoverTunnel(failed) {
				t.Fatal("confirmed tunnel timeout did not reset captured relay")
			}
			attempt, stop = context.WithTimeout(ctx, 700*time.Millisecond)
			defer stop()
			n, err := c.Machine(attempt, id)
			if err != nil {
				t.Fatalf("fresh tunnel after confirmed timeout did not recover, outer dials=%d: %v", dials.Load(), err)
			}
			req, _ := http.NewRequestWithContext(ctx, "GET", "https://machine/ready", nil)
			res, err := n.HTTP().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			if err != nil || string(body) != "recovered" {
				t.Fatalf("response=%q err=%v", body, err)
			}
			if dials.Load() != 2 {
				t.Fatalf("outer dials=%d want2", dials.Load())
			}
			var wg sync.WaitGroup
			var reset atomic.Bool
			for range 16 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if c.RecoverTunnel(failed) {
						reset.Store(true)
					}
				}()
			}
			wg.Wait()
			if reset.Load() {
				t.Fatal("concurrent late old errors reset new relay")
			}
			if err := c.Ping(ctx); err != nil {
				t.Fatalf("late old error damaged new relay: %v", err)
			}
		})
	}
}

// A caller leaving and a machine presenting another valid identity are not
// transport-outage evidence. Their shared relay remains usable by a valid call.
func TestTunnelRecoveryIgnoresCancellationAndIdentity(t *testing.T) {
	d, id, dials := relayOutage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := d.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	gone, stop := context.WithCancel(ctx)
	stop()
	_, err = c.Machine(gone, id)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled handshake: %v", err)
	}
	if c.RecoverTunnel(err) {
		t.Fatal("caller cancellation reset relay")
	}
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	c.Drop() // the fixture's next outer connection answers TLS normally
	_, err = c.Machine(ctx, id+"-another")
	if err == nil {
		t.Fatal("wrong machine identity accepted")
	}
	if c.RecoverTunnel(err) {
		t.Fatalf("identity rejection reset relay: %v", err)
	}
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("identity rejection damaged shared relay: %v", err)
	}
	n, err := c.Machine(ctx, id)
	if err != nil {
		t.Fatalf("valid machine afterward: %v", err)
	}
	short, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	r, _ := http.NewRequestWithContext(short, "GET", "https://machine/slow", nil)
	_, err = n.HTTP().Do(r)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow RPC did not time out: %v", err)
	}
	if c.RecoverTunnel(err) {
		t.Fatal("ordinary RPC deadline reset shared relay")
	}
	r, _ = http.NewRequestWithContext(ctx, "GET", "https://machine/ready", nil)
	res, err := n.HTTP().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil || string(b) != "recovered" {
		t.Fatalf("response=%q err=%v", b, err)
	}
	if dials.Load() != 2 {
		t.Fatalf("identity rejection redialed relay: dials=%d", dials.Load())
	}
}
