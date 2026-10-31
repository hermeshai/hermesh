package owner

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// LocalService.Watch: what the screens show, pushed (docs/state-sync.md
// §3.4). The lists come from the same calls as ListMachines and the rest,
// which answer from the replica (or, on a machine only, from its own
// machine); a screen gets what changed in them whenever the replica
// changes, and what is read in process (the status, this device's own
// machine) is compared every screenTick. Sessions come as their changes.

// screenTick: how often what is read in process is compared.
const screenTick = time.Second

// errNewRound: the profile, or what it is (set up, signed out, a machine
// only), changed; the screen gets everything again.
var errNewRound = errors.New("new round")

func (l *localService) Watch(ctx context.Context, _ *localv1.WatchRequest, stream *connect.ServerStream[localv1.WatchResponse]) error {
	for {
		if err := l.watchRound(ctx, stream.Send); !errors.Is(err, errNewRound) {
			return err
		}
	}
}

// screenMode is what a round of Watch shows: an owner's world, a machine
// only, or only the status (not set up, or signed out).
func screenMode(st *localv1.GetStatusResponse) string {
	switch {
	case st.Initialized && !st.SignedOut:
		return "owner"
	case !st.Initialized && st.MachineOnly:
		return "machine"
	}
	return "status"
}

func (l *localService) watchRound(ctx context.Context, send func(*localv1.WatchResponse) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	o := l.o()
	mode := screenMode(o.Status())

	// Session changes, from before the first page is read so none is missed.
	var changes <-chan *localv1.WatchSessionsResponse // nil: none
	ended := make(chan error, 1)
	switch mode {
	case "owner":
		ch := o.rep.add()
		defer o.rep.remove(ch)
		changes = ch
	case "machine":
		m, _, err := o.onlyMachine()
		if err != nil {
			return err
		}
		ready, ch := make(chan struct{}), make(chan *localv1.WatchSessionsResponse, watcherBuffer)
		changes = ch
		go func() {
			first := true
			ended <- m.WatchSessions(ctx, func(c *sessionv1.WatchSessionsResponse) error {
				if first { // the watch is in place
					first = false
					close(ready)
					return nil
				}
				select {
				case ch <- &localv1.WatchSessionsResponse{Sessions: c.Sessions, DeletedSessionIds: c.DeletedSessionIds}:
					return nil
				default:
					return connect.NewError(connect.CodeResourceExhausted, errors.New("fell behind on session changes; watch again"))
				}
			})
		}()
		select {
		case <-ready:
		case err := <-ended:
			return err
		}
	}
	kick, stop := o.rep.screen()
	defer stop()
	if mode == "owner" {
		if err := o.rep.firstScreen(ctx); err != nil {
			return err
		}
	}
	// What a screen is sent is what the replica holds: nothing waits on the
	// network, here or at each tick (heldOnly).
	ctx = context.WithValue(ctx, heldOnly{}, true)

	var w shown
	first := w.diff(ctx, l, o, mode)
	first.Reset_ = true
	if mode != "status" {
		page, err := l.ListSessions(ctx, &localv1.ListSessionsRequest{Limit: keepSessions, View: sessionv1.SessionView_SESSION_VIEW_BASIC})
		if err != nil {
			return err
		}
		first.Sessions, first.Next = page.Sessions, page.Next
		if mode == "owner" {
			listed := map[string]bool{}
			for _, s := range page.Sessions {
				listed[s.SessionId] = true
			}
			for _, s := range o.rep.held() { // of machines that cannot be read now
				if !listed[s.SessionId] {
					first.Sessions = append(first.Sessions, s)
				}
			}
		}
	}
	if err := send(first); err != nil {
		return err
	}
	tick := time.NewTicker(screenTick)
	defer tick.Stop()
	for {
		var out *localv1.WatchResponse
		select {
		case <-ctx.Done():
			return nil
		case err := <-ended:
			if ctx.Err() != nil {
				return nil
			}
			return err
		case m, ok := <-changes:
			if !ok {
				return connect.NewError(connect.CodeResourceExhausted, errors.New("fell behind; watch again"))
			}
			if m.MachinesChanged {
				out = w.diff(ctx, l, o, mode)
			} else {
				out = &localv1.WatchResponse{}
			}
			out.Sessions, out.DeletedSessionIds = m.Sessions, m.DeletedSessionIds
		case <-kick:
		case <-tick.C:
		}
		if out == nil {
			if l.o() != o || screenMode(o.Status()) != mode {
				return errNewRound
			}
			out = w.diff(ctx, l, o, mode)
		}
		if !proto.Equal(out, &localv1.WatchResponse{}) {
			if err := send(out); err != nil {
				return err
			}
		}
	}
}

// shown is what a screen was last sent, but its sessions.
type shown struct {
	status   *localv1.GetStatusResponse
	machines kept[*localv1.Machine]
	clients  kept[*localv1.Client]
	devices  kept[*localv1.Device]
	projects kept[*localv1.Project]
	pairings kept[*localv1.PairingDetails]
	work     uint64
}

// diff is what changed since the screen was last sent (everything, the
// first time). A list that could not be read is left as it was.
func (w *shown) diff(ctx context.Context, l *localService, o *Owner, mode string) *localv1.WatchResponse {
	out := &localv1.WatchResponse{}
	if st := o.Status(); !proto.Equal(st, w.status) {
		w.status, out.Status = st, st
	}
	if mode == "status" {
		return out
	}
	if v := o.screenWorkVersion(mode); v != w.work {
		w.work, out.WorkVersion = v, v
	}
	if r, err := l.ListMachines(ctx, &localv1.ListMachinesRequest{}); err == nil {
		if mode == "owner" {
			o.rep.unreachable(r.Machines)
		}
		out.Machines, out.RemovedMachineIds = w.machines.diff(r.Machines, func(m *localv1.Machine) string { return m.MachineId })
	}
	if r, err := l.ListClients(ctx, &localv1.ListClientsRequest{}); err == nil {
		out.Clients, out.RemovedGrantIds = w.clients.diff(r.Clients, func(c *localv1.Client) string { return c.GrantId })
	}
	if r, err := l.ListProjects(ctx, &localv1.ListProjectsRequest{}); err == nil {
		var removed []string
		out.Projects, removed = w.projects.diff(r.Projects, projectKey)
		for _, k := range removed {
			m, p, _ := strings.Cut(k, "/") // a machine id has no slash
			out.RemovedProjects = append(out.RemovedProjects, &localv1.ProjectRef{MachineId: m, ProjectId: p})
		}
	}
	if mode == "owner" {
		if r, err := l.ListDevices(ctx, &localv1.ListDevicesRequest{}); err == nil {
			out.Devices, out.RemovedDeviceKeyIds = w.devices.diff(r.Devices, func(d *localv1.Device) string { return d.KeyId })
		}
		if r, err := l.ListPairingRequests(ctx, &localv1.ListPairingRequestsRequest{}); err == nil {
			out.Pairings, out.RemovedPairingCodes = w.pairings.diff(r.Pairings, func(p *localv1.PairingDetails) string { return p.Code })
		}
	}
	return out
}

func projectKey(p *localv1.Project) string { return p.MachineId + "/" + p.ProjectId }

// kept is a list as last sent, by key.
type kept[T proto.Message] struct{ by map[string]T }

// diff keeps now, and returns what is new or changed in it, and the keys
// that are gone.
func (k *kept[T]) diff(now []T, key func(T) string) (changed []T, removed []string) {
	next := make(map[string]T, len(now))
	for _, x := range now {
		id := key(x)
		next[id] = x
		if old, ok := k.by[id]; !ok || !proto.Equal(old, x) {
			changed = append(changed, x)
		}
	}
	for id := range k.by {
		if _, ok := next[id]; !ok {
			removed = append(removed, id)
		}
	}
	k.by = next
	return changed, removed
}
