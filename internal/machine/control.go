package machine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/internal/localipc"
	"github.com/hermeshai/hermesh/internal/paths"
	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// The control socket lets `hermeshd project …` change a running machine's
// projects, and `hermeshd doctor` and `status` ask it how it is (the
// machine holds its data directory). Projects are the machine's own
// settings: whoever has a shell as this user can do as much already, so
// only this user may connect.

func (n *Machine) serveControl(ctx context.Context) {
	path := paths.Control(n.cfg.DataDir)
	ln, err := localipc.Listen(path)
	if err != nil {
		n.log.Warn("control socket", "err", err)
		return
	}
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
	mux.HandleFunc("GET /check", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(n.Check(r.Context()))
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
		os.Remove(path)
	}()
	srv.Serve(ln)
}

// Check is how this machine is, for `hermeshd doctor` and `status`; its
// coding agents are looked at now.
type Check struct {
	MachineID, Name, OwnerID, RelayURL, Version string
	Paired, Connected                           bool
	Agents                                      map[string]provider.Info
}

func (n *Machine) Check(ctx context.Context) Check {
	return Check{MachineID: n.MachineID(), Name: n.Name(), OwnerID: n.OwnerID(), RelayURL: n.RelayURL(), Version: Version,
		Paired: n.Paired(), Connected: n.Connected(), Agents: n.Discover(ctx)}
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
	sock := paths.Control(dataDir)
	if _, err := os.Stat(sock); err != nil {
		return nil, false, nil
	}
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return localipc.Dial(ctx, sock)
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
