package owner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// A network that blocks the relay (here: a proxy answering 403, to every
// request or only to the WebSocket) fails setting up and joining before the
// signer is asked for anything, so no Touch ID is spent on it, and says so
// in the form the apps recognise.
func TestBlockedRelayFailsBeforeTouchID(t *testing.T) {
	for _, tc := range []struct {
		name   string
		passes string // the one path the proxy lets through
		detail string
	}{
		{"everything blocked", "", "403 Forbidden"},
		{"WebSocket blocked", "/hermesh.relay.v1.RelayPublicService/GetInfo", "403"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.passes {
					w.Header().Set("Content-Type", "application/proto") // an empty GetInfoResponse
					return
				}
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte("<html>Blocked by policy</html>"))
			}))
			defer proxy.Close()
			d, err := Open(Config{DataDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Close)
			o := d.owner()
			asked, stop := o.hub.watch(false)
			defer stop()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			host := strings.TrimPrefix(proxy.URL, "http://")
			for _, call := range []func() error{
				func() error {
					_, err := o.Setup(ctx, &localv1.SetupRequest{RelayUrl: proxy.URL, ClaimCode: "ABCD", RootLabel: "Mac"})
					return err
				},
				func() error {
					_, err := o.RequestJoin(ctx, &localv1.RequestJoinRequest{RelayUrl: proxy.URL, OwnerId: "o_x", RootLabel: "Mac"})
					return err
				},
			} {
				err := call()
				if connect.CodeOf(err) != connect.CodeUnavailable || !strings.HasPrefix(err.(*connect.Error).Message(), "relay unreachable ("+host+"): ") ||
					!strings.Contains(err.Error(), tc.detail) {
					t.Fatalf("got %v, want relay unreachable (%s): …%s", err, host, tc.detail)
				}
			}
			select {
			case r := <-asked:
				t.Fatalf("the signer was asked: %v", r)
			default:
			}
		})
	}
}

// A relay's address without a scheme is HTTPS, unless it is an IP address:
// the relay gets certificates for domain names only.
func TestNormalizeRelayURL(t *testing.T) {
	for in, want := range map[string]string{
		"relay.example.com":          "https://relay.example.com",
		"relay.example.com:8443/":    "https://relay.example.com:8443",
		"203.0.113.7:8080":           "http://203.0.113.7:8080",
		"203.0.113.7":                "http://203.0.113.7",
		"[2001:db8::1]:8080":         "http://[2001:db8::1]:8080",
		"https://203.0.113.7":        "https://203.0.113.7",
		" http://relay.example.com ": "http://relay.example.com",
	} {
		if got := normalizeRelayURL(in); got != want {
			t.Errorf("normalizeRelayURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// The status tells whether a signer watches and whether one waits on the
// owner; ShowSigner reaches only the signers that asked for it.
func TestSignerState(t *testing.T) {
	h := newSignHub()
	if st := h.state(); st.Connected || st.Busy {
		t.Fatalf("no signer: %v", st)
	}
	plain, stopPlain := h.watch(false)
	defer stopPlain()
	shows, stopShows := h.watch(true)
	defer stopShows()
	if st := h.state(); !st.Connected || st.Busy {
		t.Fatalf("watching: %v", st)
	}
	h.mu.Lock()
	h.post(&localv1.SigningRequest{RequestId: "r1", Type: "hermesh.identity.v1.Grant"})
	h.mu.Unlock()
	if st := h.state(); !st.Busy {
		t.Fatalf("a request waits: %v", st)
	}
	<-plain
	<-shows
	h.show()
	if r := <-shows; !r.Show {
		t.Fatalf("show: %v", r)
	}
	select {
	case r := <-plain:
		t.Fatalf("a signer that didn't ask was shown: %v", r)
	default:
	}
	h.complete(&localv1.CompleteSigningRequestRequest{RequestId: "r1", Rejected: true})
	if st := h.state(); st.Busy {
		t.Fatalf("answered: %v", st)
	}
}

// A signer that restarts once or twice replays pending requests, while one
// that keeps crashing after receiving a request fails the call promptly
// instead of waiting out the confirmation timeout.
func TestSignerCrashLoopFailsPromptly(t *testing.T) {
	root, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("recovers after transient restart", func(t *testing.T) {
		h := newSignHub()
		done := make(chan error, 1)
		go func() {
			_, err := h.signBatch(context.Background(), root.Public(), []proto.Message{&identityv1.Grant{GrantId: "g1"}})
			done <- err
		}()
		// Wait until signBatch has posted the request, then simulate two crashes
		// and one unread disconnect before a healthy signer completes it.
		for !h.hasPending() {
			time.Sleep(time.Millisecond)
		}
		_, stopUnread := h.watch(false)
		stopUnread() // never drained ch: does not count as a crash
		for range 2 {
			ch, stop := h.watch(false)
			<-ch
			stop()
		}
		ch, stop := h.watch(false)
		defer stop()
		req := <-ch
		sig, err := root.SignMessage(req.Message)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.complete(&localv1.CompleteSigningRequestRequest{RequestId: req.RequestId, Signature: sig}); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatalf("expected recovery after transient restarts, got %v", err)
		}
	})

	t.Run("fails after repeated crashes", func(t *testing.T) {
		h := newSignHub()
		done := make(chan error, 1)
		go func() {
			_, err := h.signBatch(context.Background(), root.Public(), []proto.Message{
				&identityv1.RootCert{OwnerId: "o1"},
				&identityv1.OwnerDeviceCert{OwnerId: "o1"},
			})
			done <- err
		}()
		for !h.hasPending() {
			time.Sleep(time.Millisecond)
		}
		for range maxSignerDrops {
			ch, stop := h.watch(false)
			<-ch
			<-ch
			stop()
		}
		select {
		case err := <-done:
			if !errors.Is(err, ErrSignerUnavailable) {
				t.Fatalf("got %v, want %v", err, ErrSignerUnavailable)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("signBatch did not fail promptly after repeated signer exits")
		}
		if h.hasPending() {
			t.Fatal("pending batch was not cleaned up")
		}
	})
}

func (h *signHub) hasPending() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.pending) > 0
}
