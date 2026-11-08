package owner

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
)

// The console (docs/her-server.md §5.3): served to this device's apps on
// 127.0.0.1, under a token only they are told, as the machine's
// /her/console/…; but for what the page can't do, which this hermeshd does
// for it here: catching a sign-in's redirect (§5.4) and asking this
// device's signer.

type herConsole struct {
	srv   *http.Server
	base  string // http://127.0.0.1:<port>
	token string
	st    *store // the owner's: the port sign-ins come back to

	mu      sync.Mutex
	signins map[string]*herSignin // by id
}

// GetHerConsole is the address of a machine's console here.
func (o *Owner) GetHerConsole(machineID string) (string, error) {
	if machineID == "" || strings.Contains(machineID, "/") {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("no machine %q", machineID))
	}
	o.consoleMu.Lock()
	defer o.consoleMu.Unlock()
	if o.console == nil {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", err
		}
		c := &herConsole{base: "http://" + ln.Addr().String(), token: randomHex(32), st: o.st, signins: map[string]*herSignin{}}
		c.srv = &http.Server{Handler: o.serveConsole(c), ReadHeaderTimeout: 10 * time.Second}
		go c.srv.Serve(ln)
		o.console = c
	}
	return fmt.Sprintf("%s/her/%s/%s/", o.console.base, machineID, o.console.token), nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// serveConsole serves /her/<machine_id>/<token>/….
func (o *Owner) serveConsole(c *herConsole) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, _ := strings.CutPrefix(r.URL.Path, "/her/")
		machineID, rest, _ := strings.Cut(rest, "/")
		token, rest, ok := strings.Cut(rest, "/")
		if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(c.token)) != 1 {
			http.NotFound(w, r)
			return
		}
		switch {
		case rest == "signin" && r.Method == http.MethodPost:
			c.startSignin(w, r)
			return
		case strings.HasPrefix(rest, "signin/") && r.Method == http.MethodGet:
			c.pollSignin(w, r, strings.TrimPrefix(rest, "signin/"))
			return
		case rest == "command-connector" && r.Method == http.MethodPost:
			o.consoleCommandConnector(w, r, machineID)
			return
		}
		h, err := o.herOn(r.Context(), machineID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		r = r.Clone(r.Context())
		r.URL.Path, r.URL.RawPath = "/her/console/"+rest, ""
		h.HerConsole().ServeHTTP(w, r)
	})
}

// closeConsole stops serving the consoles and catching sign-ins.
func (o *Owner) closeConsole() {
	o.consoleMu.Lock()
	defer o.consoleMu.Unlock()
	c := o.console
	if c == nil {
		return
	}
	c.srv.Close()
	c.mu.Lock()
	for _, s := range c.signins {
		s.srv.Close()
	}
	c.mu.Unlock()
	o.console = nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// ---------------------------------------------------------------------------
// Sign-ins started on a machine (§5.4) send the browser back to this device:
// a catcher here takes the redirect, once, and the console collects what it
// brought, to complete the sign-in on the machine (chatgpt/complete,
// mcp/login_complete).
//
//	POST <base>signin {"path": "/auth/callback"}
//	     → {"id": "…", "redirect_uri": "http://127.0.0.1:<port>/auth/callback"}
//	GET  <base>signin/<id>
//	     → {"pending": true}; or what the redirect brought, told once:
//	       {"code", "state", "client_id", "iss"} as present, or
//	       {"error", "error_description"}
//
// The port stays the same from one sign-in to the next while it is free
// (kSigninPort): a provider that registers a client per redirect (an MCP
// server) then registers one.

// signinWait is how long a poll waits for the redirect; signinGiveUp how
// long a catcher waits for the browser (variables for the tests).
var (
	signinWait   = 25 * time.Second
	signinGiveUp = 10 * time.Minute
)

// kSigninPort: the port sign-ins come back to on this device.
const kSigninPort = "her_signin_port"

// herSignin catches one redirect on 127.0.0.1.
type herSignin struct {
	path   string
	port   string
	ln     net.Listener
	srv    *http.Server
	once   sync.Once
	done   chan struct{}     // closed once result is set, the port let go
	result map[string]string // what the redirect brought
}

func (s *herSignin) finish(result map[string]string) {
	s.once.Do(func() {
		s.result = result
		close(s.done)
	})
}

func (s *herSignin) ended() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// ServeHTTP takes the browser's visit to the redirect, keeps what it
// brought, tells the person to go back to Hermesh, and stops listening.
func (s *herSignin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != s.path {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	result := map[string]string{}
	keep := func(keys ...string) {
		for _, k := range keys {
			if q.Has(k) {
				result[k] = q.Get(k)
			}
		}
	}
	switch {
	case q.Get("error") != "":
		keep("error", "error_description")
	case q.Get("code") != "":
		keep("code", "state", "client_id", "iss")
	default:
		result["error"] = "the sign-in came back without a code"
	}
	page := signinPage("en", "You can close this window and go back to Hermesh.")
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Accept-Language"))), "zh") {
		page = signinPage("zh-Hans", "现在可以关闭这个窗口，回到 Hermesh。")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, page)
	s.ln.Close()                            // the port is free for the next sign-in
	go s.srv.Shutdown(context.Background()) // once this answer is out
	s.finish(result)
}

func signinPage(lang, text string) string {
	return `<!doctype html><html lang="` + lang + `"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Hermesh</title>` +
		`<p style="font: 15px -apple-system, system-ui, sans-serif; text-align: center; margin-top: 30vh">` + text + `</p></html>`
}

func (c *herConsole) startSignin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req) != nil || !strings.HasPrefix(req.Path, "/") || strings.ContainsAny(req.Path, "?#") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a sign-in needs the path its redirect goes to, such as /auth/callback"})
		return
	}
	c.mu.Lock() // one sign-in starts at a time: it takes the kept port, or knows who has it
	ln, err := c.listenSigninLocked()
	if err != nil {
		c.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s := &herSignin{path: req.Path, port: portOf(ln), ln: ln, done: make(chan struct{})}
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	id := randomHex(16)
	c.signins[id] = s
	c.mu.Unlock()
	go s.srv.Serve(ln)
	time.AfterFunc(signinGiveUp, func() {
		s.srv.Close()
		s.finish(map[string]string{"error": "no sign-in came back in time"})
	})
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "redirect_uri": "http://127.0.0.1:" + s.port + req.Path})
}

func portOf(ln net.Listener) string { return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port) }

// listenSigninLocked listens on the port kept for sign-ins; when something
// else took it, on a new one, kept from then on; while another sign-in of
// this device waits on it, on a new one for this sign-in alone.
func (c *herConsole) listenSigninLocked() (net.Listener, error) {
	kept, _ := c.st.get(kSigninPort)
	if len(kept) > 0 {
		if ln, err := net.Listen("tcp", "127.0.0.1:"+string(kept)); err == nil {
			return ln, nil
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	for _, s := range c.signins {
		if s.port == string(kept) && !s.ended() {
			return ln, nil
		}
	}
	if err := c.st.set(kSigninPort, []byte(portOf(ln))); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func (c *herConsole) pollSignin(w http.ResponseWriter, r *http.Request, id string) {
	c.mu.Lock()
	s := c.signins[id]
	c.mu.Unlock()
	if s != nil {
		select {
		case <-s.done:
		case <-time.After(signinWait):
			writeJSON(w, http.StatusOK, map[string]bool{"pending": true})
			return
		case <-r.Context().Done():
			return
		}
		c.mu.Lock()
		if c.signins[id] != s { // another poll told it
			s = nil
		}
		delete(c.signins, id)
		c.mu.Unlock()
	}
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such sign-in"})
		return
	}
	writeJSON(w, http.StatusOK, s.result)
}

// ---------------------------------------------------------------------------
// The console can't sign: a connector started as a command goes through
// here, which asks this device's signer, as SetHerCommandConnector does.
//
//	POST <base>command-connector {"name", "command", "args", "env", "remove"}
//	     → {} once applied, or a short text saying why not

func (o *Owner) consoleCommandConnector(w http.ResponseWriter, r *http.Request, machineID string) {
	var req struct {
		Name    string            `json:"name"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
		Remove  bool              `json:"remove"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "not a connector: "+err.Error(), http.StatusBadRequest)
		return
	}
	c := &identityv1.SetHerCommandConnector{Name: req.Name, Command: req.Command, Args: req.Args, Env: req.Env, Remove: req.Remove}
	if err := o.SetHerCommandConnector(r.Context(), machineID, c); err != nil {
		code, msg := consoleError(err)
		http.Error(w, msg, code)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

// consoleError is how the console is told why a call failed: an HTTP
// status, and a short text.
func consoleError(err error) (int, string) {
	if errors.Is(err, ErrSigningRejected) {
		return http.StatusForbidden, err.Error()
	}
	ce := new(connect.Error)
	if !errors.As(err, &ce) {
		return http.StatusInternalServerError, err.Error() // as the signer timing out
	}
	switch ce.Code() {
	case connect.CodeInvalidArgument:
		return http.StatusBadRequest, ce.Message()
	case connect.CodePermissionDenied:
		return http.StatusForbidden, ce.Message()
	case connect.CodeNotFound:
		return http.StatusNotFound, ce.Message()
	case connect.CodeFailedPrecondition:
		return http.StatusPreconditionFailed, ce.Message()
	case connect.CodeUnavailable:
		return http.StatusServiceUnavailable, ce.Message()
	case connect.CodeDeadlineExceeded:
		return http.StatusGatewayTimeout, ce.Message()
	}
	return http.StatusInternalServerError, ce.Message()
}
