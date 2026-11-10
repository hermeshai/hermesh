// Package screen is Computer Control (docs/computer-control.md): a machine
// sends its screen to one of the owner's devices and takes that device's
// mouse and keyboard, as WebRTC between the two.
//
//   - Host is the machine's side: it runs the helper that captures, encodes
//     and posts input (apps/screen/macos), and sends its frames.
//   - Viewer is the device's side: it receives the frames whole and in
//     order, and sends input.
//
// The two agree through one call, ScreenService.Connect, which travels in
// the end-to-end tunnel.
package screen

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

// Kind is what a message on a pipe holds. The same framing is used between
// hermeshd and the helper, and between the iPhone's core and its app: a
// 4-byte big-endian length, the kind, the payload.
type Kind byte

const (
	// To the helper.
	KindStart    Kind = 's' // JSON: max_edge, fps, bitrate, pointer
	KindBitrate  Kind = 'b' // JSON: bps
	KindKeyframe Kind = 'k'
	KindInput    Kind = 'i' // hermesh.screen.v1.Input: JSON to the helper, protobuf from the app
	KindPointer  Kind = 'c' // JSON: shown
	// From the helper, and from the core to the app.
	KindFormat Kind = 'f' // JSON: width, height
	KindVideo  Kind = 'v' // 1 byte of flags (1: keyframe), 8 bytes of capture time (µs), Annex B
	KindState  Kind = 'e' // from the helper JSON: error, detail, can_control; to the app a ScreenEvent
	KindLog    Kind = 'l'
	// Between the core and the app only.
	KindMove   Kind = 'm' // an Input that may be lost: a pointer move
	KindStatus Kind = 'p' // JSON: Status
)

const maxMessage = 16 << 20

// VideoHeader is what precedes the Annex B bytes of a KindVideo message.
const VideoHeader = 9

// WriteMessage writes one message, in one Write.
func WriteMessage(w io.Writer, k Kind, payload []byte) error {
	buf := make([]byte, 5, 5+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(len(payload)+1))
	buf[4] = byte(k)
	_, err := w.Write(append(buf, payload...))
	return err
}

// ReadMessage reads one message.
func ReadMessage(r *bufio.Reader) (Kind, []byte, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(head[:])
	if n < 1 || n > maxMessage {
		return 0, nil, errors.New("screen: bad message length")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return Kind(body[0]), body[1:], nil
}
