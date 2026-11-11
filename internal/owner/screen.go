package owner

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	screenv1 "github.com/hermeshai/hermesh/gen/go/hermesh/screen/v1"
	"github.com/hermeshai/hermesh/internal/peer"
	"github.com/hermeshai/hermesh/internal/screen"
	"github.com/hermeshai/hermesh/pkg/transport"
)

// Computer Control (docs/computer-control.md), the device's side: the app
// opens /screen/<machine_id> beside LocalService and the connection becomes
// a stream of messages both ways (internal/screen's framing): the frames,
// what the machine reports and the connection's state to the app; the
// owner's input from it. The session lasts as long as that connection.
//
//	GET /screen/<machine_id>?name=<this device>&pointer=<0|1>
//	Upgrade: hermesh-screen
//
// To the app: KindVideo (a frame), KindState (a ScreenEvent), KindStatus
// (screenStatus as JSON). From the app: KindInput and KindMove (an Input),
// KindKeyframe (it can't show the frames it has).
//
// It is served to the iPhone app, which asks for Face ID first, and not on
// a computer's local socket, where every program of the user's could open
// another machine's screen.

// screenStatus is the connection's state for the app: screen.Status, and
// the reason once it failed.
type screenStatus struct {
	screen.Status
	// Error is why the session ended, with Phase "failed".
	Error string `json:"error,omitempty"`
}

// screenConnect is ScreenService.Connect on a machine, through its tunnel.
func (o *Owner) screenConnect(machineID string) func(context.Context, *screenv1.ConnectRequest) (*screenv1.ConnectResponse, error) {
	return func(ctx context.Context, req *screenv1.ConnectRequest) (*screenv1.ConnectResponse, error) {
		c, err := o.relay(ctx)
		if err != nil {
			return nil, err
		}
		n, err := c.Machine(ctx, machineID)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		res, err := n.Screen.Connect(ctx, req)
		if err != nil && ctx.Err() == nil && tunnelError(err) {
			c.Forget(machineID)
		}
		return res, err
	}
}

// StopScreen ends the Computer Control session on this computer's own
// machine, if it has one and a session is under way.
func (l *localService) StopScreen(context.Context, *localv1.StopScreenRequest) (*localv1.StopScreenResponse, error) {
	o := l.o()
	m, _, _ := o.onlyMachine()
	if m == nil {
		m = o.local()
	}
	if m != nil {
		m.StopScreen()
	}
	return &localv1.StopScreenResponse{}, nil
}

// screenServers are the relay's STUN and TURN servers, as kept
// (warmScreenServers); none when it runs none, is an older relay, or does
// not answer in time.
func (o *Owner) screenServers(ctx context.Context) []*relayv1.IceServer {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	c, pc, err := o.control(ctx)
	if err != nil {
		return nil
	}
	return o.iceServers.Get(ctx, pc, fetchScreenServers(c))
}

// warmScreenServers asks for the relay's servers ahead of a session, on the
// iPhone (the device Computer Control is served on) once it has connected.
func (o *Owner) warmScreenServers(pc *peer.Client) {
	if !o.cfg.NoLocalMachine {
		return
	}
	hc, err := pc.Control()
	if err != nil {
		return
	}
	o.iceServers.Warm(pc, fetchScreenServers(relayv1connect.NewOwnerControlServiceClient(hc, "http://relay", transport.ClientNoCompression)))
}

func fetchScreenServers(c relayv1connect.OwnerControlServiceClient) func(context.Context) []*relayv1.IceServer {
	return func(ctx context.Context) []*relayv1.IceServer {
		res, err := c.GetScreenServers(ctx, &relayv1.GetScreenServersRequest{})
		if err != nil {
			return nil
		}
		return res.Servers
	}
}

// ScreenHandler serves /screen/<machine_id>, for a caller that provides its
// own listener and access control (the iPhone app's loopback server).
func (d *Device) ScreenHandler() http.Handler { return http.HandlerFunc(d.serveScreen) }

func (d *Device) serveScreen(w http.ResponseWriter, r *http.Request) {
	machineID := strings.TrimPrefix(r.URL.Path, "/screen/")
	hj, ok := w.(http.Hijacker)
	if machineID == "" || strings.Contains(machineID, "/") || r.Header.Get("Upgrade") != "hermesh-screen" || !ok {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	o := d.owner()
	conn, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Time{})
	rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: hermesh-screen\r\n\r\n")
	if rw.Flush() != nil {
		return
	}
	var wmu sync.Mutex
	send := func(k screen.Kind, b []byte) {
		wmu.Lock()
		defer wmu.Unlock()
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second)) // an app that stopped reading
		if screen.WriteMessage(conn, k, b) != nil {
			conn.Close()
		}
	}
	status := func(st screenStatus) {
		b, _ := json.Marshal(st)
		send(screen.KindStatus, b)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The app's messages, from the start: when it leaves, the session ends.
	input := make(chan func(*screen.Viewer), 64)
	go func() {
		defer cancel()
		br := bufio.NewReader(rw.Reader)
		for {
			k, p, err := screen.ReadMessage(br)
			if err != nil {
				return
			}
			var f func(*screen.Viewer)
			switch k {
			case screen.KindInput, screen.KindMove:
				f = func(v *screen.Viewer) { v.Send(k == screen.KindMove, p) }
			case screen.KindKeyframe:
				f = (*screen.Viewer).RequestKeyframe
			default:
				continue
			}
			select {
			case input <- f:
			default: // not connected yet, and the app keeps sending
			}
		}
	}()

	q := r.URL.Query()
	lost := make(chan struct{}, 1)
	v, err := screen.Dial(ctx, screen.ViewerConfig{
		Connect: o.screenConnect(machineID), Name: q.Get("name"), Pointer: q.Get("pointer") != "0", Servers: o.screenServers(ctx),
		Frame: func(key bool, data []byte, t *screen.Trace) {
			b := make([]byte, screen.VideoHeader, screen.VideoHeader+len(data))
			if key {
				b[0] = 1
			}
			if t != nil {
				binary.BigEndian.PutUint64(b[1:], uint64(t.Sent))
			}
			send(screen.KindVideo, append(b, data...))
			if t != nil {
				traceFrame(t, time.Now().UnixMicro())
			}
		},
		Event: func(ev *screenv1.ScreenEvent) {
			if b, err := proto.Marshal(ev); err == nil {
				send(screen.KindState, b)
			}
		},
		Status: func(st screen.Status) {
			status(screenStatus{Status: st})
			if st.Phase == "reconnecting" {
				select {
				case lost <- struct{}{}:
				default:
				}
			}
		},
	})
	if err != nil {
		if ctx.Err() == nil {
			status(screenStatus{Status: screen.Status{Phase: "failed"}, Error: errorText(err)})
		}
		return
	}
	defer v.Close()
	var restarting atomic.Bool
	for {
		select {
		case <-ctx.Done():
			return
		case <-v.Done():
			msg := ""
			if err := v.Err(); err != nil {
				msg = err.Error()
			}
			status(screenStatus{Status: screen.Status{Phase: "failed"}, Error: msg})
			return
		case f := <-input:
			f(v)
		case <-lost:
			// The path broke (this device changed networks): find another,
			// through the tunnel, which may itself be coming back.
			if !restarting.CompareAndSwap(false, true) {
				continue
			}
			go func() {
				defer restarting.Store(false)
				for i := 0; i < 5 && ctx.Err() == nil; i++ {
					time.Sleep(1500 * time.Millisecond)
					if v.Connected() {
						return
					}
					rctx, done := context.WithTimeout(ctx, 10*time.Second)
					err := v.Restart(rctx)
					done()
					if err == nil {
						return
					}
				}
			}()
		}
	}
}

// traceFrame adds a line to the file $HERMESH_SCREEN_TRACE names, for a
// frame whose path is measured (screen.Trace): when it passed each stage,
// and when the app had been handed it.
func traceFrame(t *screen.Trace, handed int64) {
	traceOnce.Do(func() {
		if path := os.Getenv("HERMESH_SCREEN_TRACE"); path != "" {
			traceFile, _ = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		}
	})
	if traceFile != nil {
		fmt.Fprintf(traceFile, "%d,%d,%d,%d,%d,%d,%d,%t\n", t.Displayed, t.Captured, t.Encoded, t.Sent, t.Received, handed, t.Bytes, t.Key)
	}
}

var (
	traceOnce sync.Once
	traceFile *os.File
)

// errorText is an error as the app shows it: a connect error's message
// without its code.
func errorText(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Message()
	}
	return err.Error()
}
