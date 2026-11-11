package herhost

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
)

// her's console as plain HTTP (proto/hermesh/her/v1/her.proto's header, and
// packages/her-client's httpTransport). hermeshd's own refusals are non-2xx
// with a short text; her's answers, her errors included, are 200.

const (
	// maxRequest is the largest JSON-RPC request the console may send.
	maxRequest = 16 << 20
	// maxStaged is the largest file the console may upload, such as a
	// skill's archive; staged files are kept stagedFor.
	maxStaged = 50 << 20
	stagedFor = 24 * time.Hour
)

// ServeHTTP serves the console's routes, paths under /her/console/: the
// caller has checked that an owner's device asks.
func (h *Host) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/her/console/events":
		h.events(w, r)
	case "/her/console/rpc":
		h.rpc(w, r)
	case "/her/console/upload":
		h.stage(w, r)
	default:
		h.files(w, r)
	}
}

// files serves the console's own files.
func (h *Host) files(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if st, err := os.Stat(h.cfg.ConsoleDir); h.cfg.ConsoleDir == "" || err != nil || !st.IsDir() {
		http.Error(w, "Her's console is not installed on this machine.", http.StatusNotFound)
		return
	}
	http.StripPrefix("/her/console", http.FileServer(http.Dir(h.cfg.ConsoleDir))).ServeHTTP(w, r)
}

// events is a connection of its own as server-sent events: `opened` with
// its id, then one unnamed event per notification of her's, its data the
// notification's JSON-RPC message. The stream ends with the connection.
func (h *Host) events(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	c, err := h.dial(r.Context(), true)
	if err != nil {
		httpError(w, err)
		return
	}
	id := h.hold(c)
	defer h.release(id, c)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	opened, _ := json.Marshal(map[string]string{"connection_id": id})
	w.Write([]byte("event: opened\ndata: " + string(opened) + "\n\n"))
	flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-c.done:
			return
		case n := <-c.notes:
			if _, err := w.Write([]byte("data: " + string(n.line) + "\n\n")); err != nil {
				return
			}
			flush()
		}
	}
}

// rpc sends one JSON-RPC request: on the connection Her-Connection names,
// or on one of its own when it names none still open. The answer is her's
// response under the request's id.
func (h *Host) rpc(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequest)).Decode(&req); err != nil || len(req.ID) == 0 {
		http.Error(w, "The body is not one JSON-RPC request.", http.StatusBadRequest)
		return
	}
	params, err := check(req.Method, req.Params)
	if err != nil {
		httpError(w, err)
		return
	}
	c := h.held(r.Header.Get("Her-Connection"))
	if c == nil {
		if c, err = h.dial(r.Context(), false); err != nil {
			httpError(w, err)
			return
		}
		defer c.Close()
	}
	res, err := c.call(r.Context(), req.Method, params)
	if err != nil {
		httpError(w, h.lost(r.Context(), err))
		return
	}
	out := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if e := res.Error; e != nil {
		v := map[string]any{"code": e.Code, "message": e.Message}
		if len(e.Data) > 0 {
			v["data"] = json.RawMessage(e.Data)
		}
		out["error"] = v
	} else {
		out["result"] = json.RawMessage(res.Result)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// stage keeps a file the console uploads (the body, named by Her-File-Name)
// in a private folder of her home, for a request that takes a path, such as
// skills/install. It answers with where the file is.
func (h *Host) stage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	name := r.Header.Get("Her-File-Name")
	if !fileName(name) {
		http.Error(w, "Her-File-Name is not a file's name.", http.StatusBadRequest)
		return
	}
	if r.ContentLength > maxStaged {
		http.Error(w, "The file is larger than 50 MiB.", http.StatusRequestEntityTooLarge)
		return
	}
	h.mu.Lock()
	on, home := h.stop != nil, h.home
	h.mu.Unlock()
	switch {
	case !on:
		httpError(w, errOff)
		return
	case home == "":
		http.Error(w, "her is starting", http.StatusServiceUnavailable)
		return
	}
	dir := filepath.Join(home, "uploads", "console")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dir, err := inside(home, dir)
	if err != nil {
		httpError(w, err)
		return
	}
	if old, err := os.ReadDir(dir); err == nil {
		for _, e := range old {
			if i, err := e.Info(); err == nil && time.Since(i.ModTime()) > stagedFor {
				os.RemoveAll(filepath.Join(dir, e.Name()))
			}
		}
	}
	b := make([]byte, 8)
	rand.Read(b)
	path := filepath.Join(dir, hex.EncodeToString(b)+"-"+name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	n, err := io.Copy(f, io.LimitReader(r.Body, maxStaged+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err == nil && n > maxStaged:
		os.Remove(path)
		http.Error(w, "The file is larger than 50 MiB.", http.StatusRequestEntityTooLarge)
		return
	case err != nil:
		os.Remove(path)
		http.Error(w, "The upload failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"path": path})
}

// httpError answers with a refusal of hermeshd's own: a connect error's code
// as HTTP's status, its message as the text.
func httpError(w http.ResponseWriter, err error) {
	status := map[connect.Code]int{
		connect.CodeInvalidArgument:    http.StatusBadRequest,
		connect.CodeFailedPrecondition: http.StatusBadRequest,
		connect.CodePermissionDenied:   http.StatusForbidden,
		connect.CodeNotFound:           http.StatusNotFound,
		connect.CodeUnavailable:        http.StatusServiceUnavailable,
		connect.CodeCanceled:           499,
	}[connect.CodeOf(err)]
	if status == 0 {
		status = http.StatusInternalServerError
	}
	msg := err.Error()
	var ce *connect.Error
	if errors.As(err, &ce) {
		msg = ce.Message()
	}
	http.Error(w, msg, status)
}
