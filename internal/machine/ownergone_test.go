package machine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
)

type infoRelay struct {
	relayv1connect.UnimplementedRelayPublicServiceHandler
	owner  string
	hosted bool
}

func (r *infoRelay) GetInfo(_ context.Context, req *relayv1.GetInfoRequest) (*relayv1.GetInfoResponse, error) {
	if r.hosted && req.OwnerId != r.owner {
		return &relayv1.GetInfoResponse{Hosted: true}, nil
	}
	return &relayv1.GetInfoResponse{OwnerId: r.owner, Hosted: r.hosted}, nil
}

// An owner is gone when its relay serves another (or none: it erased
// everything), on a server of one's own as on a center; never when the
// relay can't be reached.
func TestOwnerGone(t *testing.T) {
	for _, c := range []struct {
		name  string
		relay infoRelay
		gone  bool
	}{
		{"its own relay, still its owner", infoRelay{owner: "o_old"}, false},
		{"its own relay, claimed by another", infoRelay{owner: "o_new"}, true},
		{"its own relay, unclaimed", infoRelay{}, true},
		{"a center that serves it", infoRelay{owner: "o_old", hosted: true}, false},
		{"a center that no longer does", infoRelay{owner: "o_new", hosted: true}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, h := relayv1connect.NewRelayPublicServiceHandler(&c.relay)
			srv := httptest.NewServer(h)
			defer srv.Close()
			if got := OwnerGone(context.Background(), srv.Client(), srv.URL, "o_old"); got != c.gone {
				t.Fatalf("gone = %v, want %v", got, c.gone)
			}
		})
	}
	if OwnerGone(context.Background(), http.DefaultClient, "http://127.0.0.1:1", "o_old") {
		t.Fatal("an unreachable relay counts as gone")
	}
}
