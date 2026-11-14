package screen

import (
	"bytes"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v5/vnet"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// Exercise SRTP, RTCP NACK and the real viewer together. Losing the second
// packet of the first keyframe on a 550 ms RTT path must recover from the
// retransmission, without depending on a UI status callback or another
// keyframe. No replacement keyframe is sent by this test.
func TestViewerRecoversFirstFrameOnHighRTT(t *testing.T) {
	router, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR: "10.0.0.0/24", MinDelay: 275 * time.Millisecond,
		LoggerFactory: logging.NewDefaultLoggerFactory(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var mediaPackets atomic.Int32
	router.AddChunkFilter(func(c vnet.Chunk) bool {
		p := c.UserData()
		if len(p) >= 12 && p[0]&0xc0 == 0x80 && p[1]&0x7f == 102 {
			return mediaPackets.Add(1) != 2
		}
		return true
	})
	pc := func(ip string) *webrtc.PeerConnection {
		n, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{ip}})
		if err != nil {
			t.Fatal(err)
		}
		if err := router.AddNet(n); err != nil {
			t.Fatal(err)
		}
		api, err := newAPI(nil, n, nil)
		if err != nil {
			t.Fatal(err)
		}
		p, err := api.NewPeerConnection(webrtc.Configuration{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { p.Close() })
		return p
	}
	a, b := pc("10.0.0.1"), pc("10.0.0.2")
	if err := router.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { router.Stop() })
	frames := make(chan []byte, 8)
	v := &Viewer{pc: b, done: make(chan struct{}), cfg: ViewerConfig{
		Frame: func(_ bool, data []byte, _ *Trace) { frames <- data },
	}}
	b.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { go v.receive(track) })
	track, err := webrtc.NewTrackLocalStaticSample(h264, "screen", "screen")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := a.AddTrack(track)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			if _, _, err := sender.ReadRTCP(); err != nil {
				return
			}
		}
	}()
	dc, err := a.CreateDataChannel(inputChannel, nil)
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	dc.OnOpen(func() { close(ready) })
	local := func(p *webrtc.PeerConnection, d webrtc.SessionDescription) webrtc.SessionDescription {
		done := webrtc.GatheringCompletePromise(p)
		if err := p.SetLocalDescription(d); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("gather timeout")
		}
		return *p.LocalDescription()
	}
	offer, err := a.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.SetRemoteDescription(local(a, offer)); err != nil {
		t.Fatal(err)
	}
	answer, err := b.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetRemoteDescription(local(b, answer)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(12 * time.Second):
		t.Fatal("connection timeout")
	}
	stats, ok := b.SCTP().Transport().ICETransport().GetSelectedCandidatePairStats()
	if !ok || stats.CurrentRoundTripTime < 0.5 {
		t.Fatalf("test did not establish a high RTT path: %+v", stats)
	}
	sent := [][]byte{keyframe(0xa1), delta(0xb2), delta(0xc3)}
	for _, data := range sent {
		if err := track.WriteSample(media.Sample{Data: data, Duration: time.Second / 30}); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range sent {
		select {
		case got := <-frames:
			if !bytes.Equal(got, want) {
				t.Fatalf("frame %d differs after retransmission", i)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("frame %d never recovered after losing the first keyframe packet (RTT %.0f ms)", i, stats.CurrentRoundTripTime*1000)
		}
	}
}
