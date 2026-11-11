package machine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"

	"connectrpc.com/connect"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/internal/herhost"
)

// Her on this machine (docs/her-server.md): whether the owner turned it on
// (SetHer, root-signed only), her run by internal/herhost meanwhile, and
// HerService and the console on the owner's tunnels.

const kHer = "her"

// herUnsupported is why this machine can't run her, or "".
func (n *Machine) herUnsupported() string {
	switch {
	case runtime.GOOS == "windows":
		return "windows"
	case n.cfg.HerBin == "":
		return "no her"
	}
	return ""
}

// startHer runs her when Her is on and the machine can.
func (n *Machine) startHer() {
	n.mu.Lock()
	on := n.herOn
	n.mu.Unlock()
	if on && n.herUnsupported() == "" {
		n.her.Start()
		n.startHerLink()
	}
}

// setHer turns Her on or off (AdminCommand.set_her): her starts or stops,
// and her's data stays either way.
func (n *Machine) setHer(on bool) (string, error) {
	v := "0"
	if on {
		v = "1"
	}
	if err := n.st.Set(kHer, []byte(v)); err != nil {
		return "", err
	}
	n.mu.Lock()
	n.herOn = on
	n.mu.Unlock()
	if !on {
		// Her's access to the owner's computers ends with her
		// (docs/her-mesh.md §3.5): the owner's device revokes the grant too.
		n.dropHerGrant("Her was turned off")
		n.her.Off()
		return "Her is off", nil
	}
	if why := n.herUnsupported(); why != "" {
		return "Her is on, but this machine can't run her (" + why + ")", nil
	}
	n.her.Start()
	n.startHerLink()
	return "Her is on", nil
}

// herStatus is HerStatus as hermeshd last saw her: never a request to her.
func (n *Machine) herStatus() *machinev1.HerStatus {
	n.mu.Lock()
	on := n.herOn
	n.mu.Unlock()
	if why := n.herUnsupported(); why != "" {
		return &machinev1.HerStatus{Enabled: on, Unsupported: why}
	}
	st := n.her.Status()
	st.Enabled = on
	st.ClientPublicKey = n.herClientKey()
	n.mu.Lock()
	st.Mesh = n.mesh.grant != nil
	n.mu.Unlock()
	return st
}

// statusFor is the machine's status as p may see it: Her's carries message
// text, and the screen's the name of a device, for the owner's devices only.
func (n *Machine) statusFor(p *Principal) *machinev1.MachineStatus {
	st := n.Status()
	if p == nil || p.Kind != PrincipalOwner {
		st.Her, st.Screen = nil, nil
	}
	return st
}

// herHost is the Her host, on a machine that can run her.
func (n *Machine) herHost() (*herhost.Host, error) {
	if why := n.herUnsupported(); why != "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("this machine can't run Her (%s)", why))
	}
	return n.her, nil
}

// execHer runs the root-signed commands that reach into her.
func (n *Machine) execHer(ctx context.Context, cmd *identityv1.AdminCommand) (string, error) {
	h, err := n.herHost()
	if err != nil {
		return "", err
	}
	switch c := cmd.Command.(type) {
	case *identityv1.AdminCommand_SetHerAssistantMode:
		m := c.SetHerAssistantMode
		if err := h.AssistantMode(ctx, m.BotId, m.PermissionMode); err != nil {
			return "", err
		}
		return "assistant " + m.BotId + " runs in " + m.PermissionMode, nil
	case *identityv1.AdminCommand_SetHerCommandConnector:
		cc := c.SetHerCommandConnector
		if err := h.CommandConnector(ctx, cc); err != nil {
			return "", err
		}
		if cc.Remove {
			return "connector " + cc.Name + " removed", nil
		}
		return "connector " + cc.Name + " runs " + cc.Command, nil
	}
	return "", connect.NewError(connect.CodeInvalidArgument, errors.New("unknown admin command"))
}

// herService is HerService on the tunnel, for the owner's devices.
type herService struct{ n *Machine }

var errOwnerOnly = connect.NewError(connect.CodePermissionDenied, errors.New("owner only"))

func (s *herService) Open(ctx context.Context, req *herv1.OpenRequest, stream *connect.ServerStream[herv1.OpenEvent]) error {
	if principalFrom(ctx).Kind != PrincipalOwner {
		return errOwnerOnly
	}
	return s.n.Local().HerOpen(ctx, req, stream.Send)
}

func (s *herService) Call(ctx context.Context, req *herv1.CallRequest) (*herv1.CallResponse, error) {
	if principalFrom(ctx).Kind != PrincipalOwner {
		return nil, errOwnerOnly
	}
	return s.n.Local().HerCall(ctx, req)
}

func (s *herService) Upload(ctx context.Context, stream *connect.ClientStream[herv1.UploadRequest]) (*herv1.UploadResponse, error) {
	if principalFrom(ctx).Kind != PrincipalOwner {
		return nil, errOwnerOnly
	}
	if !stream.Receive() {
		if err := stream.Err(); err != nil {
			return nil, err
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("an upload starts with `start`"))
	}
	start := stream.Msg().GetStart()
	if start == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("an upload starts with `start`"))
	}
	return s.n.Local().HerUpload(ctx, start, &uploadReader{s: stream})
}

func (s *herService) Download(ctx context.Context, req *herv1.DownloadRequest, stream *connect.ServerStream[herv1.DownloadResponse]) error {
	if principalFrom(ctx).Kind != PrincipalOwner {
		return errOwnerOnly
	}
	return s.n.Local().HerDownload(ctx, req, stream.Send)
}

// uploadReader reads an upload's bytes from its stream.
type uploadReader struct {
	s   *connect.ClientStream[herv1.UploadRequest]
	buf []byte
}

func (r *uploadReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		if !r.s.Receive() {
			if err := r.s.Err(); err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		if r.s.Msg().GetStart() != nil {
			return 0, connect.NewError(connect.CodeInvalidArgument, errors.New("an upload has one `start`"))
		}
		r.buf = r.s.Msg().GetData()
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

// herConsole is the console's HTTP on the tunnel, for the owner's devices.
// It is plain HTTP, past the connect interceptor, so it checks itself.
func (n *Machine) herConsole() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := principalFrom(r.Context()); p == nil || p.Kind != PrincipalOwner {
			http.Error(w, "owner only", http.StatusForbidden)
			return
		}
		n.her.ServeHTTP(w, r)
	})
}
