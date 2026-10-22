package owner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
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
			o, err := Open(Config{DataDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			asked, stop := o.hub.watch()
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
