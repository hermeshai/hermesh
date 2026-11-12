package screen

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	screenv1 "github.com/hermeshai/hermesh/gen/go/hermesh/screen/v1"
	"github.com/pion/webrtc/v4"
)

// A response that arrives during an offer must wait until the offer has
// changed generations, then be discarded without touching the connection.
func TestCandidatesWaitForOffer(t *testing.T) {
	v := &Viewer{done: make(chan struct{}), offers: 1}
	v.omu.Lock()
	started, applied := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		applied <- v.addCandidates(1, []string{"not a candidate"})
	}()
	<-started
	select {
	case err := <-applied:
		v.omu.Unlock()
		t.Fatalf("candidate batch did not wait for the offer: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	v.mu.Lock()
	v.offers++
	v.mu.Unlock()
	v.omu.Unlock()
	select {
	case err := <-applied:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("old response did not finish")
	}
}

func TestInitialSignalRetriesOnlyTransientErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		session string
		calls   int
	}{
		{"deadline", context.DeadlineExceeded, "", 2},
		{"tunnel lost", connect.NewError(connect.CodeUnavailable, errors.New("connection lost")), "", 2},
		{"permission", connect.NewError(connect.CodePermissionDenied, errors.New("denied")), "", 1},
		{"restart", context.DeadlineExceeded, "live", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			v := &Viewer{cfg: ViewerConfig{Connect: func(ctx context.Context, req *screenv1.ConnectRequest) (*screenv1.ConnectResponse, error) {
				calls++
				if req.SessionId == "" {
					if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 6*time.Second {
						t.Error("signal attempt has no bounded deadline")
					}
				}
				if calls == 1 {
					return nil, tc.err
				}
				return &screenv1.ConnectResponse{}, nil
			}}}
			v.connect(context.Background(), &screenv1.ConnectRequest{SessionId: tc.session})
			if calls != tc.calls {
				t.Fatalf("calls=%d, want %d", calls, tc.calls)
			}
		})
	}
}

func TestInitialSignalHasLimitedAttempts(t *testing.T) {
	calls := 0
	v := &Viewer{cfg: ViewerConfig{Connect: func(context.Context, *screenv1.ConnectRequest) (*screenv1.ConnectResponse, error) {
		calls++
		return nil, context.DeadlineExceeded
	}}}
	_, err := v.connect(context.Background(), &screenv1.ConnectRequest{})
	if calls != 3 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("calls=%d, err=%v", calls, err)
	}
}

// Relayed, the two ends are said to be nearby when the other end has an
// address on one of this end's networks, whatever a proxy makes of their
// public addresses. The same address on both (a VPN's) says nothing.
func TestOnNetworks(t *testing.T) {
	network := func(cidr string) *net.IPNet {
		ip, n, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		n.IP = ip
		return n
	}
	mine := []*net.IPNet{network("192.168.1.5/24"), network("198.18.0.1/16"), network("2001:db8:1::5/64")}
	for _, c := range []struct {
		theirs []string
		want   bool
	}{
		{[]string{"192.168.1.9"}, true},
		{[]string{"10.0.0.9", "2001:db8:1::9"}, true},
		{[]string{"192.168.2.9", "2001:db8:2::9"}, false},
		{[]string{"198.18.0.1"}, false},
		{[]string{"192.168.1.5"}, false},
		{nil, false},
	} {
		if got := onNetworks(mine, c.theirs); got != c.want {
			t.Errorf("%v: %v, want %v", c.theirs, got, c.want)
		}
	}
	sdp := &webrtc.SessionDescription{SDP: "a=candidate:1 1 udp 2130706431 192.168.1.9 50000 typ host\r\na=candidate:2 1 udp 1694498815 203.0.113.9 50001 typ srflx raddr 0.0.0.0 rport 0\r\n"}
	later := []string{"candidate:3 1 udp 2130706431 10.0.0.9 50002 typ host"}
	if got := strings.Join(addresses("host", sdp, later), " "); got != "192.168.1.9 10.0.0.9" {
		t.Errorf("host addresses: %q", got)
	}
	if got := strings.Join(addresses("srflx", sdp, later), " "); got != "203.0.113.9" {
		t.Errorf("srflx addresses: %q", got)
	}
}
