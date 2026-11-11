package screen

import (
	"bytes"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
)

// unit is one NAL unit of the given type, n bytes long.
func unit(typ byte, n int, fill byte) []byte {
	b := bytes.Repeat([]byte{fill}, n)
	b[0] = typ
	return b
}

func annexB(units ...[]byte) []byte {
	var out []byte
	for _, u := range units {
		out = append(append(out, 0, 0, 0, 1), u...)
	}
	return out
}

// packets cuts frames into RTP packets as the sender does.
func packets(t *testing.T, frames ...[]byte) [][]*rtp.Packet {
	t.Helper()
	pk := rtp.NewPacketizer(1200, 102, 1, &codecs.H264Payloader{}, rtp.NewFixedSequencer(65530), 90000)
	var out [][]*rtp.Packet
	for _, f := range frames {
		out = append(out, pk.Packetize(f, 3000))
	}
	return out
}

func keyframe(fill byte) []byte {
	return annexB(unit(7, 20, fill), unit(8, 6, fill), unit(5, 5000, fill))
}
func delta(fill byte) []byte { return annexB(unit(1, 3000, fill)) }

func TestAssemblerInOrder(t *testing.T) {
	sent := [][]byte{keyframe(0xA1), delta(0xB2), delta(0xC3)}
	a, now := newAssembler(), time.Now()
	var got []frame
	for _, f := range packets(t, sent...) {
		for _, p := range f {
			got = append(got, a.push(p, now)...)
		}
	}
	if len(got) != 3 {
		t.Fatalf("frames: %d, want 3", len(got))
	}
	for i, f := range got {
		if !bytes.Equal(f.data, sent[i]) {
			t.Errorf("frame %d differs from what was sent (%d bytes, want %d)", i, len(f.data), len(sent[i]))
		}
		if f.key != (i == 0) {
			t.Errorf("frame %d: key = %v", i, f.key)
		}
	}
	if a.wantKey(now.Add(time.Second)) {
		t.Error("asks for a keyframe though nothing is missing")
	}
}

// A packet that arrives late holds the frames after it back, and none is
// skipped or shown twice.
func TestAssemblerWaitsForMissingPacket(t *testing.T) {
	sent := [][]byte{keyframe(0xA1), delta(0xB2), delta(0xC3)}
	fs := packets(t, sent...)
	a, now := newAssembler(), time.Now()
	var got []frame
	late := fs[1][1]
	for i, f := range fs {
		for j, p := range f {
			if i == 1 && j == 1 {
				continue
			}
			got = append(got, a.push(p, now)...)
		}
	}
	if len(got) != 1 {
		t.Fatalf("frames before the late packet: %d, want only the keyframe", len(got))
	}
	if a.wantKey(now.Add(stallAfter / 2)) {
		t.Error("gives up on the missing packet too soon")
	}
	got = append(got, a.push(late, now)...)
	got = append(got, a.push(late, now)...) // and once more, sent twice
	if len(got) != 3 || !bytes.Equal(got[1].data, sent[1]) || !bytes.Equal(got[2].data, sent[2]) {
		t.Fatalf("after the late packet: %d frames", len(got))
	}
}

// A packet that never comes: the assembler asks for a keyframe and starts
// over from it, showing nothing built on the broken frame.
func TestAssemblerStartsOverFromKeyframe(t *testing.T) {
	fs := packets(t, keyframe(0xA1), delta(0xB2), delta(0xC3), keyframe(0xD4), delta(0xE5))
	a, now := newAssembler(), time.Now()
	if a.wantKey(now) != true {
		t.Fatal("does not ask for the first keyframe")
	}
	n := 0
	for _, p := range fs[0] {
		n += len(a.push(p, now))
	}
	for _, p := range fs[1][1:] { // the first packet of the second frame is lost
		n += len(a.push(p, now))
	}
	for _, p := range fs[2] {
		n += len(a.push(p, now))
	}
	if n != 1 {
		t.Fatalf("frames: %d, want only the first keyframe", n)
	}
	if !a.wantKey(now.Add(2 * stallAfter)) {
		t.Fatal("does not ask for a keyframe after the wait")
	}
	var got []frame
	for _, f := range fs[3:] {
		for _, p := range f {
			got = append(got, a.push(p, now.Add(time.Second))...)
		}
	}
	if len(got) != 2 || !got[0].key || got[1].key {
		t.Fatalf("after the keyframe: %+v", len(got))
	}
}

// The end of the last frame is lost and nothing follows it (a still
// screen): the assembler asks again.
func TestAssemblerLostTail(t *testing.T) {
	fs := packets(t, keyframe(0xA1), delta(0xB2))
	a, now := newAssembler(), time.Now()
	for _, p := range fs[0] {
		a.push(p, now)
	}
	for _, p := range fs[1][:len(fs[1])-1] {
		a.push(p, now)
	}
	if a.wantKey(now.Add(stallAfter / 2)) {
		t.Error("gives up too soon")
	}
	if !a.wantKey(now.Add(2 * stallAfter)) {
		t.Error("does not ask for a keyframe when a frame's end never comes")
	}
}
