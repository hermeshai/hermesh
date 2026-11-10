package screen

import (
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
)

// assembler turns RTP packets back into the frames that were sent, whole
// and in order, with no wait added: a frame is ready the moment its last
// packet is in. Every frame but a keyframe builds on the one before, so a
// frame is never skipped: while a packet is missing the frames after it
// wait for it to be sent again, and when that takes too long the assembler
// starts over from the next keyframe (wantKey says to ask for one).
type assembler struct {
	mu      sync.Mutex
	depack  codecs.H264Packet
	held    map[uint16]*rtp.Packet // packets after a missing one
	next    uint16                 // the packet the frame under way continues with
	started bool                   // a keyframe began; packets from next on are wanted
	frame   []byte                 // the frame under way, as Annex B
	key     bool                   // … which is a keyframe
	stuck   time.Time              // since when a packet has been missing; zero when none is
	open    time.Time              // when the frame under way last grew; zero between frames
}

const (
	// stallAfter: how long frames wait for a missing packet before the
	// assembler asks for a keyframe.
	stallAfter = 250 * time.Millisecond
	// heldMax: more packets than a lost one could reasonably hold up.
	heldMax = 2048
)

type frame struct {
	key  bool
	data []byte
}

func newAssembler() *assembler { return &assembler{held: map[uint16]*rtp.Packet{}} }

// beginsKeyframe: whether an RTP payload starts a keyframe, which the
// sender always begins with the parameter sets (alone, or several units in
// one packet).
func beginsKeyframe(p []byte) bool {
	if len(p) == 0 {
		return false
	}
	switch p[0] & 0x1F {
	case 7: // sequence parameter set
		return true
	case 24: // STAP-A: a 2-byte length, then the first unit
		return len(p) > 3 && p[3]&0x1F == 7
	}
	return false
}

// push takes one packet and returns the frames it completed.
func (a *assembler) push(p *rtp.Packet, now time.Time) []frame {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.started {
		if !beginsKeyframe(p.Payload) {
			return nil
		}
		a.reset()
		a.started, a.next = true, p.SequenceNumber
	}
	if d := p.SequenceNumber - a.next; d >= 0x8000 {
		return nil // before the frame under way: a packet sent twice
	}
	a.held[p.SequenceNumber] = p
	var out []frame
	for {
		q, ok := a.held[a.next]
		if !ok {
			break
		}
		delete(a.held, a.next)
		a.next++
		if a.open.IsZero() { // a frame begins
			a.key = beginsKeyframe(q.Payload)
		}
		b, err := a.depack.Unmarshal(q.Payload)
		if err != nil {
			a.started = false
			return out
		}
		a.frame = append(a.frame, b...)
		a.open = now
		if q.Marker {
			out = append(out, frame{key: a.key, data: a.frame})
			a.frame, a.open = nil, time.Time{}
		}
	}
	switch {
	case len(a.held) == 0:
		a.stuck = time.Time{}
	case len(a.held) > heldMax:
		a.started = false
	case a.stuck.IsZero():
		a.stuck = now
	case len(out) > 0:
		a.stuck = now // the missing packet came, and another is missing
	}
	return out
}

func (a *assembler) reset() {
	a.depack = codecs.H264Packet{}
	clear(a.held)
	a.frame, a.key, a.stuck, a.open = nil, false, time.Time{}, time.Time{}
}

// wantKey says whether the viewer should ask for a keyframe: none has come
// yet, or the picture has been held up for too long, by a missing packet
// or by a frame whose end never came (the assembler then drops what it has
// and waits for the keyframe).
func (a *assembler) wantKey(now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	late := func(t time.Time) bool { return !t.IsZero() && now.Sub(t) > stallAfter }
	if a.started && (late(a.stuck) || late(a.open)) {
		a.started = false
	}
	return !a.started
}
