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

// screenMachine returns a tunnel to machineID for signaling: when the relay
// client is already connected, it uses c.Machine (which races cached direct
// addresses against opening a relay tunnel). While the relay client itself is
// still dialing (e.g. cold app launch or proxy delay), it races cached
// direct_addrs against o.relay so Computer Control signaling doesn't wait.
func (o *Owner) screenMachine(ctx context.Context, machineID string) (c *peer.Client, n *peer.Machine, tunnelWait time.Duration, err error) {
	started := time.Now()
	o.mu.Lock()
	probe := o.recheck
	o.mu.Unlock()
	if probe != nil {
		select {
		case <-probe:
		case <-ctx.Done():
			return nil, nil, time.Since(started).Round(time.Millisecond), ctx.Err()
		}
	}
	o.mu.Lock()
	c = o.client
	o.mu.Unlock()
	if c != nil {
		n, err = c.Machine(ctx, machineID)
		return c, n, time.Since(started).Round(time.Millisecond), err
	}
	addrs := o.directAddrs(machineID)
	d := o.dialer()
	if len(addrs) == 0 || d == nil {
		c, err = o.relay(ctx)
		if err != nil {
			return nil, nil, time.Since(started).Round(time.Millisecond), err
		}
		n, err = c.Machine(ctx, machineID)
		return c, n, time.Since(started).Round(time.Millisecond), err
	}
	if cached := o.cachedDirectMachine(machineID); cached != nil {
		return nil, cached, 0, nil
	}
	var relayClient *peer.Client
	dn, sn, err := d.RaceDirect(ctx, machineID, addrs, func(rctx context.Context) (*peer.Machine, error) {
		rc, err := o.relay(rctx)
		if err != nil {
			return nil, err
		}
		relayClient = rc
		return rc.RelayMachine(rctx, machineID)
	})
	elapsed := time.Since(started).Round(time.Millisecond)
	if err != nil {
		return relayClient, nil, elapsed, err
	}
	if dn != nil {
		dn = o.storeDirectMachine(machineID, dn)
		return nil, dn, elapsed, nil
	}
	return relayClient, sn, elapsed, nil
}

// screenCall is a call of ScreenService on a machine, through its tunnel.
func screenCall[Req, Res any](o *Owner, machineID string, call func(screenv1connect.ScreenServiceClient, context.Context, *Req) (*Res, error)) func(context.Context, *Req) (*Res, error) {
	var recoveryMu sync.Mutex
	var recovered *Req // the viewer retries the same Connect request
	var pingRecovered *Req
	return func(ctx context.Context, req *Req) (*Res, error) {
		started := time.Now()
		method := fmt.Sprintf("%T", req)
		c, n, tunnelAfter, err := o.screenMachine(ctx, machineID)
		if err != nil {
			o.log.Warn("screen: signal tunnel", "method", method, "err", err, "after", tunnelAfter)
			if c != nil {
				if _, ok := any(req).(*screenv1.ConnectRequest); ok {
					recoveryMu.Lock()
					reset := recovered != req && c.RecoverTunnel(err)
					if reset {
						recovered = req
					}
					recoveryMu.Unlock()
					if reset {
						o.log.Info("screen: signal relay recovery", "method", method, "after", time.Since(started).Round(time.Millisecond))
					}
				}
			}
			var ce *connect.Error
			if errors.As(err, &ce) {
				return nil, ce
			}
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		_, isConnect := any(req).(*screenv1.ConnectRequest)
		if isConnect {
			o.log.Info("screen: signal request", "method", method, "direct", n.Path() != "relay", "path", n.Path(), "tunnel_after", tunnelAfter)
		}
		callCtx := ctx
		var callCancel context.CancelFunc
		var stop func() bool
		if isConnect {
			if dl, ok := ctx.Deadline(); ok && time.Until(dl) > 2500*time.Millisecond {
				callCtx, callCancel = context.WithTimeout(ctx, 2500*time.Millisecond)
			}
		} else if dl, ok := ctx.Deadline(); ok && dl.Sub(started) >= 4*time.Second && time.Until(dl) < 4*time.Second {
			callCtx, callCancel = context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
			stop = context.AfterFunc(ctx, func() {
				if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
					callCancel()
				}
			})
		}
		if callCancel != nil {
			defer func() {
				if stop != nil {
					stop()
				}
				callCancel()
			}()
		}
		res, err := call(n.Screen, callCtx, req)
		rpcAfter := time.Since(started).Round(time.Millisecond)
		o.log.Info("screen: signal response", "method", method, "err", err, "after", rpcAfter,
			"direct", n.Path() != "relay", "path", n.Path(), "tunnel_after", tunnelAfter, "rpc_after", rpcAfter)
		if err != nil && (callCtx.Err() == nil && tunnelError(err) || errors.Is(callCtx.Err(), context.DeadlineExceeded)) {
			if c != nil {
				c.ForgetMachine(n)
				recoveryMu.Lock()
				checkPing := pingRecovered != req
				if checkPing {
					pingRecovered = req
				}
				recoveryMu.Unlock()
				if checkPing {
					pctx, pcancel := context.WithTimeout(ctx, 500*time.Millisecond)
					perr := c.Ping(pctx)
					pcancel()
					if perr != nil {
						c.Drop()
						o.log.Info("screen: signal relay dropped", "method", method, "err", perr)
					}
				}
			} else {
				o.forgetDirectMachine(machineID, n)
			}
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
	if list, fresh := o.iceServers.Cached(); list != nil {
		if !fresh {
			go func() {
				rctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if c, pc, err := o.control(rctx); err == nil {
					o.iceServers.Warm(pc, o.fetchScreenServers(c))
				}
			}()
		}
		return list
	}
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
	moved, _ := d.nextNetworkMove()
	var smu sync.Mutex
	var said string // the status last logged
	var restarting atomic.Bool
	reportStatus := func(st screen.Status) {
		smu.Lock()
		now := st.Phase + " " + st.Path + " " + st.Local
		changed := now != said
		said = now
		smu.Unlock()
		if changed { // not every two seconds
			o.log.Info("screen: viewer status", "phase", st.Phase, "path", st.Path, "rtt_ms", st.RTT)
		}
		status(screenStatus{Status: st})
	}
	// A machine that takes candidates after the offer says so in its state;
	// an older one gets an offer with all of them, as before.
	var candidates func(context.Context, *screenv1.CandidatesRequest) (*screenv1.CandidatesResponse, error)
	o.rep.feeds(func(feeds map[string]*feed) {
		if f := feeds[machineID]; f != nil && f.status.GetScreen().GetCandidatesLater() {
			candidates = screenCall(o, machineID, screenv1connect.ScreenServiceClient.Candidates)
		}
	})
	v, err := screen.Dial(ctx, screen.ViewerConfig{
		Log:     o.log,
		Connect: screenCall(o, machineID, screenv1connect.ScreenServiceClient.Connect), Candidates: candidates,
		Name: q.Get("name"), Pointer: q.Get("pointer") != "0", Servers: o.screenServers(ctx), Fresh: o.screenServers, NoRelay: o.iceServers.Doubt,
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
			reportStatus(st)
			if st.Phase == "reconnecting" && !restarting.Load() {
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
	wake := make(chan struct{}, 1)
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
					woke := false
					timer := time.NewTimer(1500 * time.Millisecond)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-v.Done():
						timer.Stop()
						return
					case <-wake:
						timer.Stop()
						woke = true
					case <-timer.C:
					}
					if !woke && i == 0 && v.Connected() {
						break
					}
				} else if i == 0 {
					// Coalesce bursty OS interface events (e.g. Wi-Fi <-> 5G transitions)
					// so we issue one clean ICE restart after the new path settles.
					settle := time.NewTimer(250 * time.Millisecond)
				coalesceLoop:
					for {
						select {
						case <-ctx.Done():
							settle.Stop()
							return
						case <-v.Done():
							settle.Stop()
							return
						case <-wake:
							if !settle.Stop() {
								select {
								case <-settle.C:
								default:
								}
							}
							settle.Reset(250 * time.Millisecond)
						case <-settle.C:
							break coalesceLoop
						}
					}
				}
				select {
				case <-again:
				default:
				}
				now = false
				rctx, done := context.WithTimeout(ctx, 10*time.Second)
				restarting.Store(true)
				err := v.Restart(rctx)
				restarting.Store(false)
				done()
				if err == nil {
					break
				}
				// After a failed offer, the old transport may still report
				// connected. Its pending negotiation must still be retried.
				o.log.Warn("screen: viewer restart", "attempt", i+1, "err", err)
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
			var forced bool
			moved, forced = d.nextNetworkMove()
			if !forced && v.LocalPathHealthy() {
				o.log.Info("screen: viewer path unchanged, keeping active connection")
				continue
			}
			o.log.Info("screen: viewer network changed", "forced", forced)
			reportStatus(screen.Status{Phase: "reconnecting"})
			select {
			case <-again: // a broken path that waited: the new network is the reason
			default:
			}
			select {
			case wake <- struct{}{}:
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
