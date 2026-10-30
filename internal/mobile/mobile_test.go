package mobile

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
)

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func client(c *Core, token string) localv1connect.LocalServiceClient {
	return localv1connect.NewLocalServiceClient(&http.Client{Transport: bearer{token}}, fmt.Sprintf("http://127.0.0.1:%d", c.Port()))
}

func TestCoreServesLocalServiceToTheTokenOnly(t *testing.T) {
	c, err := Start(t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.o.Close) // after Pause: cleanups run last first
	t.Cleanup(c.Pause)
	ctx := context.Background()

	st, err := client(c, c.Token()).GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Initialized {
		t.Fatal("a new core is set up already")
	}
	for _, tok := range []string{"", "wrong"} {
		_, err := client(c, tok).GetStatus(ctx, &localv1.GetStatusRequest{})
		if connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("token %q: got %v, want unauthenticated", tok, err)
		}
	}
	// An iPhone is never a machine.
	_, err = client(c, c.Token()).RequestJoin(ctx, &localv1.RequestJoinRequest{RelayUrl: "https://relay.invalid", OwnerId: "o_x", MachineOnly: true})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("machine-only join: got %v, want failed precondition", err)
	}
	c.Pause()
	c.Resume()
	c.Pause()
}
