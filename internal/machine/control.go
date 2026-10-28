package machine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/internal/peercred"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// The control socket lets `hermeshd project …` change a running machine's
// projects (the machine holds its data directory). Projects are the
// machine's own settings: whoever has a shell as this user can do as much
// already, so only this user may connect.

// ControlSocket is where a machine with this data directory listens.
func ControlSocket(dataDir string) string {
	p := filepath.Join(dataDir, "control.sock")
	if len(p) > 100 {
		p = filepath.Join(os.TempDir(), fmt.Sprintf("hermesh-control-%d.sock", os.Getuid()))
	}
	return p
}

func (n *Machine) serveControl(ctx context.Context) {
	path := ControlSocket(n.cfg.DataDir)
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		n.log.Warn("control socket", "err", err)
		return
	}
	os.Chmod(path, 0o600)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /settings", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		cmd := &identityv1.AdminCommand{}
		if err := protojson.Unmarshal(body, cmd); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		msg, err := n.ApplyLocal(r.Context(), cmd)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		io.WriteString(w, msg)
	})
	mux.HandleFunc("GET /projects", func(w http.ResponseWriter, r *http.Request) {
		res, err := (&machineService{n: n}).projects(r.Context(), &Principal{Kind: PrincipalOwner}, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		b, _ := protojson.Marshal(res)
		w.Write(b)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
		os.Remove(path)
	}()
	srv.Serve(&peercred.SameUserListener{UnixListener: ln.(*net.UnixListener)})
}

// ApplyLocal applies a project change from this machine's own user: adding
// or removing a project, nothing else.
func (n *Machine) ApplyLocal(ctx context.Context, cmd *identityv1.AdminCommand) (string, error) {
	switch cmd.Command.(type) {
	case *identityv1.AdminCommand_AddProject, *identityv1.AdminCommand_RemoveProject:
	default:
		return "", errors.New("only projects can be changed on the machine itself")
	}
	cmd.OwnerId, cmd.TargetMachineId, cmd.CommandId = n.OwnerID(), n.machineID, identity.RandomCode(16)
	cmd.ExpiresAt = timestamppb.New(time.Now().Add(time.Minute))
	return n.applyAdmin(ctx, cmd, "this machine's user")
}

// ControlRequest sends a running machine's control socket a request; ok is
// false when no machine listens there.
func ControlRequest(ctx context.Context, dataDir, method, path string, body []byte) (out []byte, ok bool, err error) {
	sock := ControlSocket(dataDir)
	if _, err := os.Stat(sock); err != nil {
		return nil, false, nil
	}
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}, Timeout: 30 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, method, "http://machine"+path, bytes.NewReader(body))
	res, err := hc.Do(req)
	if err != nil {
		return nil, false, nil // a stale socket: nobody listens
	}
	defer res.Body.Close()
	out, _ = io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		return nil, true, errors.New(strings.TrimSpace(string(out)))
	}
	return out, true, nil
}
