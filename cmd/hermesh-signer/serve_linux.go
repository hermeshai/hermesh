package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	"github.com/hermeshai/hermesh/internal/localipc"
	"github.com/hermeshai/hermesh/internal/peercred"
	"github.com/hermeshai/hermesh/internal/preview"
	"github.com/hermeshai/hermesh/internal/signer"
)

// keysFor are a user's root keys; ownerFor how that user is asked, in the
// process pid of their desktop session, in language.
type (
	keysFor  func(uid int) fileKeys
	ownerFor func(uid, pid int, language string) signer.Owner
)

// request is what an app asks on the signer's socket, a line of JSON: watch
// this hermeshd's signing requests, and ask me in my language.
type request struct {
	Socket   string `json:"socket"`
	Language string `json:"language"`
}

// reply is the signer's answer, a line of JSON: ok, or a refusal the app
// words itself (Error) with what went wrong (Message).
type reply struct {
	OK      bool   `json:"ok,omitempty"`
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
}

// The signer's refusals, by name.
const (
	// The socket named is not the asking user's own.
	refuseNotYours = "not_yours"
	// The user's sudo needs no authentication: any program of theirs could
	// become root and take the keys, so no key here is apart from them.
	refuseOpenSudo = "open_sudo"
	// No polkit to ask the user with.
	refuseNoPolkit = "no_polkit"
)

// server watches each user's hermeshd that asked, one watch per socket.
type server struct {
	keys    keysFor
	owners  ownerFor
	log     *slog.Logger
	mu      sync.Mutex
	watches map[string]*watch
	// openSudo reports whether a user's sudo asks them nothing.
	openSudo func(name string) bool
	// polkit reports whether polkit can ask anyone here.
	polkit func() bool
}

type watch struct {
	uid    int
	owner  *latestOwner
	cancel context.CancelFunc
}

func newServer(keys keysFor, owners ownerFor, log *slog.Logger) *server {
	return &server{keys: keys, owners: owners, log: log, watches: map[string]*watch{}, openSudo: sudoAsksNothing, polkit: polkitHere}
}

func (s *server) serve(ctx context.Context, ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.answer(ctx, c)
	}
}

// answer takes one app's request.
func (s *server) answer(ctx context.Context, c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return
	}
	uid, err := peercred.PeerUID(uc)
	if err != nil {
		return
	}
	pid := peercred.PeerProcess(uc).PID
	var req request
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err == nil {
		err = json.Unmarshal(line, &req)
	}
	var r reply
	if err != nil {
		r = reply{Error: "bad_request", Message: err.Error()}
	} else {
		r = s.take(ctx, uid, pid, req)
	}
	b, _ := json.Marshal(r)
	c.Write(append(b, '\n'))
}

// take starts watching req.Socket for uid, or tells the watch there who to
// ask now (the app may have started again), or refuses.
func (s *server) take(ctx context.Context, uid, pid int, req request) reply {
	if err := owned(req.Socket, uid); err != nil {
		return reply{Error: refuseNotYours, Message: err.Error()}
	}
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil && s.openSudo(u.Username) {
		return reply{Error: refuseOpenSudo, Message: u.Username + " can run anything as root without being asked (sudo NOPASSWD)"}
	}
	if !s.polkit() {
		return reply{Error: refuseNoPolkit, Message: "polkit is not installed"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if w := s.watches[req.Socket]; w != nil && w.uid == uid {
		w.owner.set(s.owners(uid, pid, req.Language))
		return reply{OK: true}
	}
	keys := s.keys(uid)
	if err := os.MkdirAll(keys.dir, 0o700); err != nil {
		return reply{Error: "keys", Message: err.Error()}
	}
	wctx, cancel := context.WithCancel(ctx)
	w := &watch{uid: uid, owner: &latestOwner{}, cancel: cancel}
	w.owner.set(s.owners(uid, pid, req.Language))
	s.watches[req.Socket] = w
	s.log.Info("watching", "uid", uid, "socket", req.Socket)
	go s.follow(wctx, req.Socket, uid)
	go signer.Run(wctx, client(req.Socket), keys, w.owner)
	return reply{OK: true}
}

// follow ends the watch once the socket is gone or no longer uid's (its
// hermeshd uninstalled, or another user's at that path).
func (s *server) follow(ctx context.Context, socket string, uid int) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if owned(socket, uid) != nil {
			s.mu.Lock()
			if w := s.watches[socket]; w != nil && w.uid == uid {
				w.cancel()
				delete(s.watches, socket)
			}
			s.mu.Unlock()
			s.log.Info("stopped watching", "uid", uid, "socket", socket)
			return
		}
	}
}

// owned: path is a socket of uid's, not a link to another one.
func owned(path string, uid int) error {
	if !strings.HasPrefix(path, "/") {
		return errors.New("the socket's path is not absolute")
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if st.Mode()&os.ModeSocket == 0 || !ok || int(sys.Uid) != uid {
		return errors.New(path + " is not a socket of the user who asked")
	}
	return nil
}

// client is a client of hermeshd's local API at socket.
func client(socket string) localv1connect.LocalServiceClient {
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return localipc.Dial(ctx, socket)
	}}}
	return localv1connect.NewLocalServiceClient(hc, "http://local")
}

// latestOwner passes each ask to the owner of the app that asked last.
type latestOwner struct {
	mu sync.Mutex
	o  signer.Owner
}

func (l *latestOwner) set(o signer.Owner) { l.mu.Lock(); l.o = o; l.mu.Unlock() }
func (l *latestOwner) get() signer.Owner  { l.mu.Lock(); defer l.mu.Unlock(); return l.o }

func (l *latestOwner) Approve(ctx context.Context, cards []preview.Card) (bool, error) {
	return l.get().Approve(ctx, cards)
}
func (l *latestOwner) Failed(err error) { l.get().Failed(err) }
func (l *latestOwner) Show()            { l.get().Show() }

func (l *latestOwner) Language() string {
	if o, ok := l.get().(interface{ Language() string }); ok {
		return o.Language()
	}
	return "en"
}

func (l *latestOwner) Matched(m preview.Match) {
	if o, ok := l.get().(interface{ Matched(preview.Match) }); ok {
		o.Matched(m)
	}
}

// sudoAsksNothing: name's sudo runs anything as root without asking them.
func sudoAsksNothing(name string) bool {
	out, err := exec.Command("sudo", "-n", "-l", "-U", name).Output()
	return err == nil && strings.Contains(string(out), "NOPASSWD: ALL")
}

// polkitHere reports whether polkit can ask anyone here (test builds: always).
var polkitHere = havePolkit

// havePolkit: pkcheck is there, and the signer's polkit action.
func havePolkit() bool {
	if _, err := exec.LookPath("pkcheck"); err != nil {
		return false
	}
	_, err := os.Stat("/usr/share/polkit-1/actions/io.hermesh.signer.policy")
	return err == nil
}
