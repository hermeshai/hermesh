package screen

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"
)

// Measuring where the picture's delay comes from (docs/computer-control.md
// §5). With HERMESH_SCREEN_TRACE set on the machine, each frame carries the
// time it passed each stage there, as text in an SEI unit at its end; the
// viewer takes that unit off again, adds when the frame was whole, and
// hands the lot to whoever shows the frame. All times are microseconds
// since 1970, so the two ends' clocks must agree for the stage between them
// to mean anything (they do on one computer, which is where this is used).

// Trace is when one frame passed each stage; zero where not known.
type Trace struct {
	Displayed int64 // the machine's screen showed it
	Captured  int64 // the helper was handed it
	Encoded   int64 // it came out of the encoder
	Sent      int64 // hermeshd handed it to the connection
	Received  int64 // the viewer had all of it
	Bytes     int   // its size
	Key       bool
}

// traceID marks the SEI unit: user data, unregistered, under this UUID.
var traceID = []byte("hermesh-trace-01")

var startCode = []byte{0, 0, 0, 1}

// helperStamps are the three times in a frame from the helper.
func helperStamps(header []byte) (t Trace) {
	t.Displayed = int64(binary.BigEndian.Uint64(header[1:9]))
	t.Captured = int64(binary.BigEndian.Uint64(header[9:17]))
	t.Encoded = int64(binary.BigEndian.Uint64(header[17:25]))
	return t
}

// withTrace returns the frame with its trace in an SEI unit at the end.
func withTrace(frame []byte, t Trace) []byte {
	text := fmt.Sprintf("%d,%d,%d,%d", t.Displayed, t.Captured, t.Encoded, t.Sent)
	out := make([]byte, 0, len(frame)+32+len(text))
	out = append(out, frame...)
	out = append(out, startCode...)
	out = append(out, 0x06, 0x05, byte(len(traceID)+len(text))) // SEI, user data unregistered, its size
	out = append(out, traceID...)
	out = append(out, text...)
	return append(out, 0x80)
}

// takeTrace takes the trace off the end of a frame, when it has one.
func takeTrace(frame []byte) ([]byte, *Trace) {
	i := bytes.LastIndex(frame, startCode)
	if i < 0 || len(frame) < i+7+len(traceID) || frame[i+4] != 0x06 || frame[i+5] != 0x05 || !bytes.Equal(frame[i+7:i+7+len(traceID)], traceID) {
		return frame, nil
	}
	t := &Trace{Received: time.Now().UnixMicro()}
	text := bytes.TrimSuffix(frame[i+7+len(traceID):], []byte{0x80})
	if _, err := fmt.Sscanf(string(text), "%d,%d,%d,%d", &t.Displayed, &t.Captured, &t.Encoded, &t.Sent); err != nil {
		return frame, nil
	}
	return frame[:i], t
}
