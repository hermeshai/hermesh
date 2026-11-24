package mobile

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"
)

// In-process binary Protobuf FFI bridge for LocalService & SessionService:
// Calls owner.Device.LocalHandler() directly in memory using Connect's native
// binary Protobuf codecs (application/proto and application/connect+proto),
// eliminating 127.0.0.1 loopback sockets, iOS TN2277 listener reclaim races,
// HTTP/1.1 framing, and JSON/Base64 encoding overhead on every mobile RPC.

const localServicePrefix = "/hermesh.local.v1.LocalService/"

func procedurePath(method string) string {
	if strings.HasPrefix(method, "/") {
		return method
	}
	return localServicePrefix + method
}

type ffiFrame struct {
	payload []byte
	done    bool
	errCode string
	errMsg  string
}

type ffiStream struct {
	cancel context.CancelFunc
	frames chan ffiFrame
	closed chan struct{}
}

// InvokeUnary executes one LocalService unary RPC in-process using binary Protobuf.
func (c *Core) InvokeUnary(method string, reqProto []byte, timeout time.Duration) (resProto []byte, errCode, errMsg string) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://inproc"+procedurePath(method), bytes.NewReader(reqProto))
	if err != nil {
		return nil, "invalid_argument", err.Error()
	}
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Accept-Encoding", "identity")

	rec := httptest.NewRecorder()
	c.handler.ServeHTTP(rec, req)
	body := rec.Body.Bytes()
	if rec.Code == http.StatusOK {
		return bytes.Clone(body), "", ""
	}
	code, msg := parseConnectError(body, rec.Code)
	return nil, code, msg
}

// OpenStream starts a LocalService server-streaming RPC in-process using binary
// Protobuf and returns a handle for NextStreamFrame / CloseStream.
func (c *Core) OpenStream(method string, reqProto []byte) int64 {
	ctx, cancel := context.WithCancel(context.Background())
	s := &ffiStream{
		cancel: cancel,
		frames: make(chan ffiFrame, 32),
		closed: make(chan struct{}),
	}
	c.streamsMu.Lock()
	c.nextID++
	id := c.nextID
	if c.streams == nil {
		c.streams = map[int64]*ffiStream{}
	}
	c.streams[id] = s
	c.streamsMu.Unlock()

	envelope := make([]byte, 5+len(reqProto))
	envelope[0] = 0
	binary.BigEndian.PutUint32(envelope[1:5], uint32(len(reqProto)))
	copy(envelope[5:], reqProto)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://inproc"+procedurePath(method), bytes.NewReader(envelope))
	if err != nil {
		s.push(ffiFrame{errCode: "invalid_argument", errMsg: err.Error()})
		close(s.frames)
		return id
	}
	req.Header.Set("Content-Type", "application/connect+proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Connect-Accept-Encoding", "identity")

	go func() {
		defer close(s.frames)
		w := &ffiStreamWriter{hdr: http.Header{}, stream: s}
		c.handler.ServeHTTP(w, req)
		w.finish()
	}()
	return id
}

// NextStreamFrame blocks until the next binary Protobuf message, EOF, or error
// arrives on streamID.
func (c *Core) NextStreamFrame(streamID int64) (payload []byte, done bool, errCode, errMsg string) {
	c.streamsMu.Lock()
	s := c.streams[streamID]
	c.streamsMu.Unlock()
	if s == nil {
		return nil, true, "", ""
	}
	f, ok := <-s.frames
	if !ok {
		c.CloseStream(streamID)
		return nil, true, "", ""
	}
	if f.done || f.errCode != "" {
		c.CloseStream(streamID)
	}
	return f.payload, f.done, f.errCode, f.errMsg
}

// CloseStream cancels and releases streamID. Safe to call multiple times.
func (c *Core) CloseStream(streamID int64) {
	c.streamsMu.Lock()
	s := c.streams[streamID]
	delete(c.streams, streamID)
	c.streamsMu.Unlock()
	if s == nil {
		return
	}
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	s.cancel()
}

func (s *ffiStream) push(f ffiFrame) bool {
	select {
	case s.frames <- f:
		return true
	case <-s.closed:
		return false
	}
}

type ffiStreamWriter struct {
	hdr     http.Header
	status  int
	stream  *ffiStream
	buf     []byte
	errBody bytes.Buffer
	ended   bool
}

func (w *ffiStreamWriter) Header() http.Header { return w.hdr }

func (w *ffiStreamWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *ffiStreamWriter) Flush() {}

func (w *ffiStreamWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status != http.StatusOK {
		return w.errBody.Write(p)
	}
	w.buf = append(w.buf, p...)
	for len(w.buf) >= 5 {
		flags := w.buf[0]
		n := int(binary.BigEndian.Uint32(w.buf[1:5]))
		if len(w.buf) < 5+n {
			break
		}
		payload := bytes.Clone(w.buf[5 : 5+n])
		w.buf = w.buf[5+n:]
		if flags&0x02 != 0 {
			w.ended = true
			if code, msg, hasErr := parseConnectEndStreamError(payload); hasErr {
				w.stream.push(ffiFrame{errCode: code, errMsg: msg})
			} else {
				w.stream.push(ffiFrame{done: true})
			}
			continue
		}
		if !w.stream.push(ffiFrame{payload: payload}) {
			return 0, context.Canceled
		}
	}
	return len(p), nil
}

func (w *ffiStreamWriter) finish() {
	if w.ended {
		return
	}
	if w.status != 0 && w.status != http.StatusOK {
		code, msg := parseConnectError(w.errBody.Bytes(), w.status)
		w.stream.push(ffiFrame{errCode: code, errMsg: msg})
		return
	}
	w.stream.push(ffiFrame{done: true})
}

func parseConnectError(body []byte, status int) (string, string) {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil && e.Code != "" {
		return e.Code, e.Message
	}
	return httpStatusCode(status), string(body)
}

func parseConnectEndStreamError(body []byte) (string, string, bool) {
	var end struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &end) == nil && end.Error != nil {
		code := end.Error.Code
		if code == "" {
			code = "unknown"
		}
		return code, end.Error.Message, true
	}
	return "", "", false
}

func httpStatusCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_argument"
	case http.StatusUnauthorized:
		return "unauthenticated"
	case http.StatusForbidden:
		return "permission_denied"
	case http.StatusNotFound:
		return "unimplemented"
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return "unavailable"
	default:
		return "unknown"
	}
}
