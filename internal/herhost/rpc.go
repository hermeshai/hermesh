package herhost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
)

// her's JSON-RPC 2.0 (her/docs/cli/json-rpc.md): one JSON object per line
// on her's Unix socket. A conn matches answers to its requests by id and
// keeps her's notifications for the one who reads its notes.

const (
	// maxLine is her's own limit on a message (server.max_frame_bytes).
	maxLine = 128 << 20
	// queuedNotes notifications may wait for their reader; past them the
	// reader is too slow, and the connection is dropped.
	queuedNotes = 4096
	// writeWait bounds a write her does not read.
	writeWait = 30 * time.Second
)

// note is a notification of her's.
type note struct {
	method string
	params json.RawMessage
	line   []byte // the whole message, as her wrote it
}

type conn struct {
	nc      net.Conn
	notes   chan note // nil: notifications are dropped
	version string    // her's, as initialize gave it

	wmu     sync.Mutex // one write at a time
	mu      sync.Mutex
	next    int64
	pending map[int64]chan *message
	done    chan struct{} // closed with err set
	err     error
}

// message is a line of her's: an answer (id, result or error) or a
// notification (method, params).
type message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int32           `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
}

var errClosed = errors.New("the connection to her closed")

// dial connects to her on sock and initializes the connection; with notes,
// her's notifications on it are kept for the caller to read.
func dial(ctx context.Context, sock, version string, notes bool) (*conn, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return nil, err
	}
	c := &conn{nc: nc, pending: map[int64]chan *message{}, done: make(chan struct{})}
	if notes {
		c.notes = make(chan note, queuedNotes)
	}
	go c.read()
	params, _ := json.Marshal(map[string]any{"protocol_version": 2, "client_info": map[string]string{"name": "hermeshd", "version": version}})
	res, err := c.call(ctx, "initialize", params)
	if err == nil && res.Error != nil {
		err = fmt.Errorf("initialize: %s", res.Error.Message)
	}
	if err != nil {
		c.close(err)
		return nil, err
	}
	var init struct {
		ServerInfo struct {
			Version string `json:"version"`
		} `json:"server_info"`
	}
	json.Unmarshal(res.Result, &init)
	c.version = init.ServerInfo.Version
	return c, nil
}

// call sends one request (params: JSON, or nil for none) and waits for her's
// answer: its result or her's error. err is the connection failing, or ctx
// ending first.
func (c *conn) call(ctx context.Context, method string, params json.RawMessage) (*herv1.Response, error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return nil, c.err
	}
	c.next++
	id := c.next
	answer := make(chan *message, 1)
	c.pending[id] = answer
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	// Marshalling compacts params: the request stays on one line.
	line, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int64           `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}{"2.0", id, method, params})
	if err != nil {
		return nil, err
	}
	if err := c.write(append(line, '\n')); err != nil {
		return nil, err
	}
	select {
	case m := <-answer:
		if m.Error != nil {
			return &herv1.Response{Error: &herv1.Error{Code: m.Error.Code, Message: m.Error.Message, Data: m.Error.Data}}, nil
		}
		return &herv1.Response{Result: m.Result}, nil
	case <-c.done:
		return nil, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *conn) write(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.nc.SetWriteDeadline(time.Now().Add(writeWait))
	if _, err := c.nc.Write(b); err != nil {
		err = fmt.Errorf("writing to her: %w", err)
		c.close(err)
		return err
	}
	return nil
}

func (c *conn) read() {
	sc := bufio.NewScanner(c.nc)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		var m message
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.Method != "" {
			if c.notes == nil {
				continue
			}
			select {
			case c.notes <- note{method: m.Method, params: m.Params, line: bytes.Clone(sc.Bytes())}:
			default:
				c.close(errors.New("her's notifications were not read in time"))
				return
			}
			continue
		}
		var id int64
		if json.Unmarshal(m.ID, &id) != nil {
			continue
		}
		c.mu.Lock()
		answer := c.pending[id]
		c.mu.Unlock()
		if answer != nil {
			select {
			case answer <- &m:
			default:
			}
		}
	}
	err := sc.Err()
	if err == nil {
		err = errClosed
	}
	c.close(err)
}

// close ends the connection: calls waiting on it fail with err.
func (c *conn) close(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	c.err = err
	close(c.done)
	c.nc.Close()
}

func (c *conn) Close() { c.close(errClosed) }
