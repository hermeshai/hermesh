package screen

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestInputChannelRecoversFastAfterBlackhole(t *testing.T) {
	var blocked atomic.Bool
	sysA, err := newSystemNet()
	if err != nil {
		t.Fatal(err)
	}
	sysB, err := newSystemNet()
	if err != nil {
		t.Fatal(err)
	}
	netA := &blockNet{systemNet: sysA, open: func(net.Addr) bool { return !blocked.Load() }}
	netB := &blockNet{systemNet: sysB, open: func(net.Addr) bool { return !blocked.Load() }}

	apiA, err := newAPI(nil, netA, nil)
	if err != nil {
		t.Fatal(err)
	}
	apiB, err := newAPI(nil, netB, nil)
	if err != nil {
		t.Fatal(err)
	}
	pcA, err := apiA.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pcA.Close()
	pcB, err := apiB.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pcB.Close()

	recv := make(chan string, 8)
	pcB.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			recv <- string(msg.Data)
		})
	})

	open := make(chan struct{})
	dc, err := pcA.CreateDataChannel(inputChannel, nil)
	if err != nil {
		t.Fatal(err)
	}
	dc.OnOpen(func() { close(open) })

	offer, err := pcA.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gA, err := gather(pcA, offer, nil)
	if err != nil {
		t.Fatal(err)
	}
	gA.wait(false)

	if err := pcB.SetRemoteDescription(*pcA.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := pcB.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gB, err := gather(pcB, answer, nil)
	if err != nil {
		t.Fatal(err)
	}
	gB.wait(false)

	if err := pcA.SetRemoteDescription(*pcB.LocalDescription()); err != nil {
		t.Fatal(err)
	}

	select {
	case <-open:
	case <-time.After(5 * time.Second):
		t.Fatal("data channel did not open")
	}

	if err := dc.SendText("ready"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-recv:
		if got != "ready" {
			t.Fatalf("got %q, want ready", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("initial message not received")
	}

	// Blackhole UDP across two SCTP RTO intervals (1s + 2s). Without RTOMax=1s
	// backoff jumps to 4s and delivery stalls ~3.8s after unblocking.
	blocked.Store(true)
	if err := dc.SendText("post-blackhole"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3200 * time.Millisecond)
	blocked.Store(false)
	unblocked := time.Now()

	select {
	case got := <-recv:
		if got != "post-blackhole" {
			t.Fatalf("got %q, want post-blackhole", got)
		}
		if elapsed := time.Since(unblocked); elapsed > 1800*time.Millisecond {
			t.Fatalf("data channel recovery took %v after unblock, expected <= 1.8s", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("data channel stalled > 2s after blackhole cleared (unbounded SCTP RTO backoff)")
	}
}
