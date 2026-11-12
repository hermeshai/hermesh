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
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	screenv1 "github.com/hermeshai/hermesh/gen/go/hermesh/screen/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/screen/v1/screenv1connect"
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

// screenCall is a call of ScreenService on a machine, through its tunnel.
func screenCall[Req, Res any](o *Owner, machineID string, call func(screenv1connect.ScreenServiceClient, context.Context, *Req) (*Res, error)) func(context.Context, *Req) (*Res, error) {
	return func(ctx context.Context, req *Req) (*Res, error) {
		started := time.Now()
		method := fmt.Sprintf("%T", req)
		c, err := o.relay(ctx)
		if err != nil {
			o.log.Warn("screen: signal relay", "method", method, "err", err, "after", time.Since(started).Round(time.Millisecond))
			return nil, err
		}
		n, err := c.Machine(ctx, machineID)
		if err != nil {
			o.log.Warn("screen: signal tunnel", "method", method, "err", err, "after", time.Since(started).Round(time.Millisecond))
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		res, err := call(n.Screen, ctx, req)
		o.log.Info("screen: signal response", "method", method, "err", err, "after", time.Since(started).Round(time.Millisecond))
		if err != nil && (ctx.Err() == nil && tunnelError(err) || errors.Is(ctx.Err(), context.DeadlineExceeded)) {
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
	return o.iceServers.Get(ctx, pc, o.fetchScreenServers(c))
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
	o.iceServers.Warm(pc, o.fetchScreenServers(relayv1connect.NewOwnerControlServiceClient(hc, "http://relay", transport.ClientNoCompression)))
}

func (o *Owner) fetchScreenServers(c relayv1connect.OwnerControlServiceClient) func(context.Context) []*relayv1.IceServer {
	return func(ctx context.Context) []*relayv1.IceServer {
		res, err := c.GetScreenServers(ctx, &relayv1.GetScreenServersRequest{})
		if err != nil {
			// Without them a session has the paths the two ends find by
			// themselves, and no relay to fall back on: say why.
			o.log.Warn("screen: the relay's STUN and TURN servers", "err", err)
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
	moved := d.networkMoved()
	var smu sync.Mutex
	var said string // the status last logged
	// A machine that takes candidates after the offer says so in its state;
	// an older one gets an offer with all of them, as before.
	var candidates func(context.Context, *screenv1.CandidatesRequest) (*screenv1.CandidatesResponse, error)
	o.rep.feeds(func(feeds map[string]*feed) {
		if f := feeds[machineID]; f != nil && f.status.GetScreen().GetCandidatesLater() {
			candidates = screenCall(o, machineID, screenv1connect.ScreenServiceClient.Candidates)
		}
	})
	v, err := screen.Dial(ctx, screen.ViewerConfig{
		Connect: screenCall(o, machineID, screenv1connect.ScreenServiceClient.Connect), Candidates: candidates,
		Name: q.Get("name"), Pointer: q.Get("pointer") != "0", Servers: o.screenServers(ctx), Fresh: o.screenServers,
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
			smu.Lock()
			now := st.Phase + " " + st.Path + " " + st.Local
			changed := now != said
			said = now
			smu.Unlock()
			if changed { // not every two seconds
				o.log.Info("screen: viewer status", "phase", st.Phase, "path", st.Path, "rtt_ms", st.RTT)
			}
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
		o.log.Warn("screen: viewer dial", "err", err)
		if ctx.Err() == nil {
			status(screenStatus{Status: screen.Status{Phase: "failed"}, Error: errorText(err)})
		}
		return
	}
	defer v.Close()
	// Finding a path again, through the tunnel, which may itself be coming
	// back. Asked for with false when the path broke: the session waits a
	// moment for it to come back by itself. With true when this device's
	// network changed: the path may still carry the picture, but a new
	// network has other addresses, and only an offer finds a path from them.
	again := make(chan bool, 1)
	go func() {
		for {
			var now bool
			select {
			case <-ctx.Done():
				return
			case now = <-again:
			}
			for i := 0; i < 5 && ctx.Err() == nil; i++ {
				if !now {
					time.Sleep(1500 * time.Millisecond)
					if v.Connected() {
						break
					}
				}
				now = false
				rctx, done := context.WithTimeout(ctx, 10*time.Second)
				err := v.Restart(rctx)
				done()
				if err == nil {
					break
				}
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			o.log.Info("screen: viewer app closed")
			return
		case <-v.Done():
			o.log.Info("screen: viewer ended", "err", v.Err())
			msg := ""
			if err := v.Err(); err != nil {
				msg = err.Error()
			}
			status(screenStatus{Status: screen.Status{Phase: "failed"}, Error: msg})
			return
		case f := <-input:
			f(v)
		case <-lost:
			select {
			case again <- false:
			default: // one is under way, or waits
			}
		case <-moved:
			o.log.Info("screen: viewer network changed")
			moved = d.networkMoved()
			select {
			case <-again: // a broken path that waited: the new network is the reason
			default:
			}
			select {
			case again <- true:
			default:
			}
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
