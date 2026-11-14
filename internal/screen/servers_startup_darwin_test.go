package screen

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	screenv1 "github.com/hermeshai/hermesh/gen/go/hermesh/screen/v1"
	"google.golang.org/protobuf/proto"
)

// Exercise the actual Host, Viewer, helper, SRTP frames and input channel.
// The STUN server is silent and the control-plane refresh never finishes
// until after media and input succeed. A usable stale cache must not put
// either dependency back on the LAN connection's critical path.
func TestSessionLANWhileServerRefreshPending(t *testing.T) {
	silent, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	list := []*relayv1.IceServer{{Urls: []string{"stun:" + silent.LocalAddr().String()}}}
	key := new(int)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	defer finish()
	fetch := func(ctx context.Context) []*relayv1.IceServer {
		entered <- struct{}{}
		select {
		case <-release:
			return list
		case <-ctx.Done():
			return nil
		}
	}
	var viewerCache, hostCache Servers
	for _, cache := range []*Servers{&viewerCache, &hostCache} {
		cache.Get(context.Background(), key, func(context.Context) []*relayv1.IceServer { return list })
		cache.mu.Lock()
		cache.at = time.Now().Add(-ServersFresh - time.Minute)
		cache.mu.Unlock()
		cache.Warm(key, fetch)
		<-entered
	}
	defer func() {
		finish()
		startupWaitRefresh(t, &viewerCache)
		startupWaitRefresh(t, &hostCache)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	inputLog := filepath.Join(t.TempDir(), "input.jsonl")
	h := NewHost(HostConfig{
		Bin: helperBin(t), Args: []string{"--test-source", "--input-log", inputLog},
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Servers: func(ctx context.Context) []*relayv1.IceServer { return hostCache.Get(ctx, key, fetch) },
	})
	defer h.Stop()
	frames := make(chan time.Time, 1)
	var once sync.Once
	started := time.Now()
	var answered time.Time
	v, err := Dial(ctx, ViewerConfig{
		Servers: viewerCache.Get(ctx, key, fetch), Candidates: h.Candidates,
		Connect: func(ctx context.Context, req *screenv1.ConnectRequest) (*screenv1.ConnectResponse, error) {
			res, err := h.Connect(ctx, req)
			answered = time.Now()
			return res, err
		},
		Frame: func(bool, []byte, *Trace) { once.Do(func() { frames <- time.Now() }) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	var first time.Time
	select {
	case first = <-frames:
	case <-ctx.Done():
		t.Fatal("LAN first frame waited for the silent STUN/control server")
	}
	input, err := proto.Marshal(&screenv1.Input{Event: &screenv1.Input_Text{Text: &screenv1.Text{Text: "while refresh waits"}}})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if err := v.Send(false, input); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("input channel did not open")
		case <-time.After(10 * time.Millisecond):
		}
	}
	for {
		data, _ := os.ReadFile(inputLog)
		if strings.Contains(string(data), "while refresh waits") {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("input did not reach the real helper")
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case <-release:
		t.Fatal("server refresh completed before media/input")
	default:
	}
	if took := first.Sub(started); took > 1500*time.Millisecond {
		t.Errorf("LAN first frame took %v with usable cached servers", took)
	}
	t.Logf("both server refreshes pending: first frame=%v, answer-to-frame=%v; input reached helper", first.Sub(started).Round(time.Millisecond), first.Sub(answered).Round(time.Millisecond))
}

func startupWaitRefresh(t *testing.T, s *Servers) {
	t.Helper()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		s.mu.Lock()
		busy := s.busy
		s.mu.Unlock()
		if !busy {
			return
		}
	}
	t.Fatal("server refresh did not finish")
}
