package screen

import (
	"bytes"
	"testing"
	"time"
)

// A frame's trace rides at its end through the packets and comes off again
// on the other side, leaving the frame as it was sent.
func TestTraceRidesWithTheFrame(t *testing.T) {
	sent := Trace{Displayed: 1794322000000001, Captured: 1794322000004002, Encoded: 1794322000009003, Sent: 1794322000009504}
	frames := [][]byte{keyframe(0xA1), delta(0xB2)}
	a, now := newAssembler(), time.Now()
	var got []frame
	for _, f := range packets(t, withTrace(frames[0], sent), withTrace(frames[1], sent)) {
		for _, p := range f {
			got = append(got, a.push(p, now)...)
		}
	}
	if len(got) != 2 {
		t.Fatalf("frames: %d", len(got))
	}
	for i, f := range got {
		data, tr := takeTrace(f.data)
		if tr == nil {
			t.Fatalf("frame %d: no trace found in %d bytes ending %x", i, len(f.data), f.data[len(f.data)-12:])
		}
		if !bytes.Equal(data, frames[i]) {
			t.Errorf("frame %d is not what was sent once the trace is off", i)
		}
		if tr.Displayed != sent.Displayed || tr.Captured != sent.Captured || tr.Encoded != sent.Encoded || tr.Sent != sent.Sent || tr.Received == 0 {
			t.Errorf("frame %d: trace %+v", i, tr)
		}
	}
	if data, tr := takeTrace(frames[1]); tr != nil || !bytes.Equal(data, frames[1]) {
		t.Error("a frame without a trace is taken for one with")
	}
}
