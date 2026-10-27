package e2e

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/mobile"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// TestMobileCoreProcess is the iPhone app's process in the tests that start
// one (startMobileCore): the owner role as the app runs it (internal/mobile),
// paused and resumed by lines on stdin as the app does when it leaves the
// front and comes back.
func TestMobileCoreProcess(t *testing.T) {
	dir := os.Getenv("HERMESH_TEST_MOBILE_CORE")
	if dir == "" {
		t.Skip("only as a mobile core's process")
	}
	c, err := mobile.Start(dir, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		fmt.Println("error", err)
		os.Exit(1)
	}
	fmt.Println("core", c.Port(), c.Token())
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		switch sc.Text() {
		case "pause":
			c.Pause()
		case "resume":
			c.Resume()
		}
		fmt.Println("did", sc.Text())
	}
	os.Exit(0)
}

// mobileCore is an iPhone app's owner role in a process of its own, and a
// client of its LocalService on the loopback port, as the app's.
type mobileCore struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan string
	client localv1connect.LocalServiceClient
	app    *testApp
	// started: when the core answered with its port (the app's Core.start returned).
	started time.Time
}

// startMobileCore starts the app's core with its data in dir, and plays its
// signer with root.
func startMobileCore(t *testing.T, dir string, root *identity.SoftwareKey) *mobileCore {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "^TestMobileCoreProcess$")
	cmd.Env = append(os.Environ(), "HERMESH_TEST_MOBILE_CORE="+dir)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	logf, err := os.CreateTemp(t.TempDir(), "core-*.log")
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	m := &mobileCore{t: t, cmd: cmd, stdin: stdin, lines: make(chan string, 16)}
	t.Cleanup(func() {
		m.kill()
		if t.Failed() {
			b, _ := os.ReadFile(logf.Name())
			t.Logf("==== mobile core ====\n%s", b)
		}
	})
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			m.lines <- sc.Text()
		}
		close(m.lines)
	}()
	var port, token string
	select {
	case l := <-m.lines:
		f := strings.Fields(l)
		if len(f) != 3 || f[0] != "core" {
			t.Fatalf("mobile core: %q", l)
		}
		port, token = f[1], f[2]
	case <-time.After(30 * time.Second):
		t.Fatal("mobile core did not start")
	}
	m.started = time.Now()
	hc := &http.Client{Transport: &bearer{token: token, next: http.DefaultTransport}}
	m.client = localv1connect.NewLocalServiceClient(hc, "http://127.0.0.1:"+port)
	m.app = &testApp{t: t, client: m.client, root: root, keys: map[string]*identity.SoftwareKey{identity.KeyID(root.Public()): root}}
	ctx, cancel := context.WithCancel(context.Background())
	m.app.cancel = cancel
	t.Cleanup(cancel)
	go m.app.signLoop(ctx)
	return m
}

// do sends the core "pause" or "resume" and waits until it did it.
func (m *mobileCore) do(cmd string) {
	fmt.Fprintln(m.stdin, cmd)
	select {
	case l := <-m.lines:
		if l != "did "+cmd {
			m.t.Fatalf("mobile core: %q", l)
		}
	case <-time.After(60 * time.Second):
		m.t.Fatalf("mobile core: no answer to %s", cmd)
	}
}

func (m *mobileCore) kill() {
	if m.cmd.Process != nil {
		m.cmd.Process.Kill()
		m.cmd.Wait()
	}
}

// cutProxy forwards TCP connections to target until it is cut, as a phone's
// network that goes away: then new connections fail at once, and those made
// before are silent for good (their NAT mappings are gone), even after the
// network is back. A new connection forwards nothing for setup, as TCP's
// handshake and TLS's take before its first byte.
type cutProxy struct {
	mu    sync.Mutex
	cut   bool
	epoch int
}

func newCutProxy(t *testing.T, target string, setup time.Duration) (*cutProxy, string) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	p := &cutProxy{}
	pump := func(dst, src net.Conn, epoch int) {
		b := make([]byte, 32<<10)
		for {
			n, err := src.Read(b)
			if n > 0 && !p.dead(epoch) {
				if _, err := dst.Write(b[:n]); err != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		if !p.dead(epoch) {
			dst.Close()
			src.Close()
		}
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			cut, epoch := p.cut, p.epoch
			p.mu.Unlock()
			if cut {
				c.Close()
				continue
			}
			go func() {
				time.Sleep(setup)
				r, err := net.Dial("tcp", target)
				if err != nil {
					c.Close()
					return
				}
				go pump(r, c, epoch)
				go pump(c, r, epoch)
			}()
		}
	}()
	return p, l.Addr().String()
}

func (p *cutProxy) dead(epoch int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return epoch != p.epoch
}

func (p *cutProxy) setCut(cut bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cut && !p.cut {
		p.epoch++
	}
	p.cut = cut
}

// joinMobile makes the core at dir one of w's trusted devices, approved on
// w's Mac, as the iPhone joins; it reaches the relay at relayURL.
func joinMobile(t *testing.T, w *ownerMac, relayURL, dir string, root *identity.SoftwareKey) *mobileCore {
	t.Helper()
	ctx := context.Background()
	st, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	m := startMobileCore(t, dir, root)
	join, err := m.client.RequestJoin(ctx, &localv1.RequestJoinRequest{RelayUrl: relayURL, OwnerId: st.OwnerId,
		RootPublicKey: rootPub(root), RootLabel: "test-iphone", RootPlatform: "ios"})
	if err != nil {
		t.Fatalf("request join: %v", err)
	}
	var approval *localv1.ApproveDevicePairingResponse
	eventually(t, 30*time.Second, "the iPhone's request approved on the Mac", func() bool {
		approval, err = w.app.client.ApproveDevicePairing(ctx, &localv1.ApproveDevicePairingRequest{Code: join.Code})
		return err == nil
	})
	eventually(t, 60*time.Second, "the approval reaches the iPhone", func() bool {
		r, err := m.client.WaitJoin(ctx, &localv1.WaitJoinRequest{Code: join.Code})
		return err == nil && r.Done
	})
	if _, err := m.client.ConfirmJoin(ctx, &localv1.ConfirmJoinRequest{Code: join.Code, Accept: true, MatchCode: approval.MatchCode}); err != nil {
		t.Fatal(err)
	}
	return m
}

// within is how long cond took to hold, or -1 when it did not in timeout.
func within(timeout time.Duration, cond func() bool) time.Duration {
	began := time.Now()
	for time.Since(began) < timeout {
		if cond() {
			return time.Since(began)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return -1
}

// TestMobileNetwork measures, in round trips to a relay 150 ms away (the
// Mac as far on the other side, as from a phone on a mobile network to an
// owner's Mac at home through a hosted center), how long the iPhone's core
// takes to answer the app: its status at launch, the first world it shows,
// the world complete (the Mac followed), a long conversation opened, the
// world again after a minute in the background while the phone's network
// went away (the connections it had are dead after that) and a session was
// deleted on the Mac, and after a few seconds in the background (they
// still work). A new connection from the phone costs TCP's and TLS's
// round trips before its first byte.
func TestMobileNetwork(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	const oneWay = 150 * time.Millisecond
	rtt := 2 * oneWay
	w := newFarOwnerMac(t, oneWay, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	var script strings.Builder
	for i := range 120 {
		fmt.Fprintf(&script, "say step %d: looking at the next part of the change, which is long enough to read like a real reply\n", i)
		fmt.Fprintf(&script, "shell go test ./internal/part%d\n", i)
		if i%10 == 0 {
			script.WriteString("output 20000\n")
		}
	}
	long, r := w.startSession(2*time.Minute, "--project", "demo", "--prompt", script.String(), "--wait")
	if long.Status != "idle" {
		t.Fatalf("long session: %+v", r)
	}
	var short []string
	for i := range 11 {
		s, r := w.startSession(time.Minute, "--project", "demo", "--prompt", fmt.Sprintf("say short %d", i), "--wait")
		if s.Status != "idle" {
			t.Fatalf("session: %+v", r)
		}
		short = append(short, s.SessionID)
	}
	sessions := 12

	ctx := context.Background()
	st, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	network, phoneRelay := newCutProxy(t, strings.TrimPrefix(st.RelayUrl, "http://"), 2*rtt)
	dir := t.TempDir()
	root, _ := identity.GenerateKey()
	joinMobile(t, w, "http://"+phoneRelay, dir, root).kill()

	// Launch: the app starts its core and asks for the status, then watches.
	m := startMobileCore(t, dir, root)
	st, err = m.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil || !st.Initialized {
		t.Fatalf("status: %v %v", st, err)
	}
	status := time.Since(m.started)
	front, leave := context.WithCancel(ctx)
	scr := watchScreenOf(front, t, m.client)
	eventually(t, 60*time.Second, "the first world", scr.has(func(*screen) bool { return true }))
	first := time.Since(m.started)
	complete := func(s *screen) bool {
		if len(s.sessions) < sessions {
			return false
		}
		for _, x := range s.machines {
			if !x.Online || x.Unreachable != "" || x.Syncing || x.Status == nil {
				return false
			}
		}
		return len(s.machines) == 1
	}
	eventually(t, 60*time.Second, "the world complete", scr.has(complete))
	whole := time.Since(m.started)

	// A long conversation opened: everything up to its last event.
	var last uint64
	scr.mu.Lock()
	last = scr.sessions[long.SessionID].GetProgress().GetLastEventSeq()
	scr.mu.Unlock()
	began := time.Now()
	sctx, stop := context.WithCancel(ctx)
	stream, err := m.client.StreamSessionEvents(sctx, &localv1.StreamSessionEventsRequest{SessionId: long.SessionID, Follow: true, Partial: true, View: sessionv1.EventView_EVENT_VIEW_BASIC})
	if err != nil {
		t.Fatal(err)
	}
	var events, bytes int
	for stream.Receive() {
		events++
		bytes += proto.Size(stream.Msg())
		if stream.Msg().Seq >= last {
			break
		}
	}
	opened := time.Since(began)
	stop()

	// The app leaves the front; the phone's network goes away for a minute,
	// while a session is deleted on the Mac; then the app comes back.
	leave()
	m.do("pause")
	// Paused, it listens no more (as Apple asks of a suspended app, whose
	// listening socket iOS may take back without a word): calls are refused.
	pctx, pcancel := context.WithTimeout(ctx, 2*time.Second)
	if _, err := m.client.GetStatus(pctx, &localv1.GetStatusRequest{}); err == nil {
		t.Error("LocalService answered while paused")
	}
	pcancel()
	time.Sleep(3 * time.Second) // iOS suspends the app a few seconds after it left the front
	m.cmd.Process.Signal(syscall.SIGSTOP)
	network.setCut(true)
	gone := short[0]
	if _, err := w.app.client.DeleteSession(ctx, &localv1.DeleteSessionRequest{SessionId: gone}); err != nil {
		t.Fatal(err)
	}
	sessions--
	away, r := w.startSession(time.Minute, "--project", "demo", "--prompt", "say while the phone was away", "--wait")
	if away.Status != "idle" {
		t.Fatalf("session: %+v", r)
	}
	sessions++
	time.Sleep(65 * time.Second)
	network.setCut(false)
	m.cmd.Process.Signal(syscall.SIGCONT)
	m.do("resume")
	back := time.Now()
	fresh := func(after int, show string) func(s *screen) bool {
		return func(s *screen) bool { return s.resets > after && s.sessions[show] != nil && complete(s) }
	}
	scr.mu.Lock()
	resets := scr.resets
	scr.shownUnreachable = nil
	scr.mu.Unlock()
	front, leave = context.WithCancel(ctx)
	scr.follow(front, t, m.client)
	if within(90*time.Second, scr.has(fresh(resets, away.SessionID))) < 0 {
		m.cmd.Process.Signal(syscall.SIGQUIT) // its goroutines, into its log
		time.Sleep(time.Second)
		t.Fatal("the world after the background did not come")
	}
	resumed := time.Since(back)
	forgotten := within(max(0, 10*time.Second-time.Since(back)), scr.has(func(s *screen) bool { return s.sessions[gone] == nil }))
	if forgotten >= 0 {
		forgotten = time.Since(back)
	}
	// The Mac stayed online: it was never shown as unreachable on the way.
	scr.mu.Lock()
	if len(scr.shownUnreachable) > 0 {
		t.Errorf("after the background, shown unreachable: %v", scr.shownUnreachable)
	}
	scr.mu.Unlock()

	// Back and forth quickly: the app leaves the front while it is still
	// reading the world again, a few times, and stays in front at last. It
	// shows the world, never the Mac unreachable.
	leave()
	m.do("pause")
	scr.mu.Lock()
	scr.shownUnreachable = nil
	scr.mu.Unlock()
	for range 3 {
		m.do("resume")
		quick, gone := context.WithCancel(ctx)
		scr.follow(quick, t, m.client)
		time.Sleep(rtt / 2)
		gone()
		m.do("pause")
	}
	m.do("resume")
	scr.mu.Lock()
	resets = scr.resets
	scr.mu.Unlock()
	front, leave = context.WithCancel(ctx)
	scr.follow(front, t, m.client)
	eventually(t, 30*time.Second, "the world after quick switches", scr.has(fresh(resets, away.SessionID)))
	time.Sleep(time.Second)
	scr.mu.Lock()
	if len(scr.shownUnreachable) > 0 {
		t.Errorf("switching back and forth, shown unreachable: %v", scr.shownUnreachable)
	}
	scr.mu.Unlock()
	leave()

	// A short trip to the background (another app answered, and back), while
	// a session starts on the Mac: the connections it had still work.
	m.do("pause")
	time.Sleep(3 * time.Second)
	m.cmd.Process.Signal(syscall.SIGSTOP)
	brief, r := w.startSession(time.Minute, "--project", "demo", "--prompt", "say while the phone was away briefly", "--wait")
	if brief.Status != "idle" {
		t.Fatalf("session: %+v", r)
	}
	sessions++
	time.Sleep(2 * time.Second)
	m.cmd.Process.Signal(syscall.SIGCONT)
	m.do("resume")
	back = time.Now()
	scr.mu.Lock()
	resets = scr.resets
	scr.shownUnreachable = nil
	scr.mu.Unlock()
	front, leave = context.WithCancel(ctx)
	scr.follow(front, t, m.client)
	briefly := within(30*time.Second, scr.has(fresh(resets, brief.SessionID)))
	time.Sleep(time.Second)
	scr.mu.Lock()
	if len(scr.shownUnreachable) > 0 {
		t.Errorf("after a short background, shown unreachable: %v", scr.shownUnreachable)
	}
	scr.mu.Unlock()
	leave()

	// The app is closed from the switcher (it left the front first), a
	// session is deleted on the Mac, and the app launched again: the screen
	// still holds what it showed (the app keeps it), and loses the deleted one.
	leave2, stop2 := context.WithCancel(ctx)
	scr.follow(leave2, t, m.client)
	stop2()
	m.do("pause")
	m.kill()
	gone2 := short[1]
	if _, err := w.app.client.DeleteSession(ctx, &localv1.DeleteSessionRequest{SessionId: gone2}); err != nil {
		t.Fatal(err)
	}
	sessions--
	m = startMobileCore(t, dir, root)
	launched := time.Now()
	scr.mu.Lock()
	resets = scr.resets
	scr.mu.Unlock()
	scr.follow(ctx, t, m.client)
	relaunched := within(60*time.Second, scr.has(func(s *screen) bool { return fresh(resets, away.SessionID)(s) && s.sessions[gone2] == nil }))
	if relaunched >= 0 {
		relaunched = time.Since(launched)
	}

	round := func(d time.Duration) string {
		if d < 0 {
			return "never"
		}
		return fmt.Sprintf("%5.1f round trips (%v)", float64(d)/float64(rtt), d.Round(10*time.Millisecond))
	}
	t.Logf("status at launch             %s", round(status))
	t.Logf("first world                  %s", round(first))
	t.Logf("world complete               %s", round(whole))
	t.Logf("long conversation opened     %s, %d events, %d KB", round(opened), events, bytes>>10)
	t.Logf("world after background       %s", round(resumed))
	t.Logf("deleted meanwhile, forgotten %s", round(forgotten))
	t.Logf("world after a short pause    %s", round(briefly))
	t.Logf("relaunched, deleted gone     %s", round(relaunched))
	// The floor: TCP, TLS and the WebSocket upgrade to the relay (3), the
	// end-to-end handshake and one call through it to the Mac, as far
	// again behind it (2 each); after a short pause, a ping on the relay
	// connection kept (1) and the call.
	for _, c := range []struct {
		what string
		took time.Duration
		max  float64
	}{
		{"status at launch", status, 0.2},
		{"world complete", whole, 9},
		{"long conversation opened", opened, 3},
		{"world after background", resumed, 8.5},
		{"deleted session forgotten after the background", forgotten, 8.5},
		{"world after a short pause", briefly, 3.5},
		{"deleted session forgotten after a relaunch", relaunched, 10},
	} {
		if c.took < 0 || float64(c.took) > c.max*float64(rtt) {
			t.Errorf("%s: %s, want at most %.1f round trips", c.what, round(c.took), c.max)
		}
	}
}
