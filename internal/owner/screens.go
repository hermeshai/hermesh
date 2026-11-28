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
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/pkg/metrics"
)

var (
	mWatchDiff        = metrics.Default.Histogram("hermesh_owner_watch_diff_seconds")
	mWatchWakeSession = metrics.Default.Counter(`hermesh_owner_watch_wakes_total{trigger="session"}`)
	mWatchWakeKick    = metrics.Default.Counter(`hermesh_owner_watch_wakes_total{trigger="kick"}`)
	mWatchWakeTick    = metrics.Default.Counter(`hermesh_owner_watch_wakes_total{trigger="tick"}`)
	mWatchSent        = metrics.Default.Counter("hermesh_owner_watch_sent_total")
	mWatchNoop        = metrics.Default.Counter("hermesh_owner_watch_noop_total")
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
// only, a standalone local machine, or only the status (not set up without a
// local machine, or signed out).
func screenMode(st *localv1.GetStatusResponse) string {
	switch {
	case st.Initialized && !st.SignedOut:
		return "owner"
	case !st.Initialized && st.MachineOnly:
		return "machine"
	case st.LocalMachineEnabled && (!st.Initialized || st.SignedOut):
		return "standalone"
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
	case "machine", "standalone":
		var m *machine.Local
		if mode == "machine" {
			var err error
			m, _, err = o.onlyMachine()
			if err != nil {
				return err
			}
		} else {
			m = o.local()
			if m == nil {
				return connect.NewError(connect.CodeUnavailable, errors.New("this computer's machine is not running"))
			}
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
	first := w.diff(ctx, l, o, mode, true)
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
		full := true
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
			m, ok = drainSessionChanges(m, changes)
			if !ok {
				return connect.NewError(connect.CodeResourceExhausted, errors.New("fell behind; watch again"))
			}
			mWatchWakeSession.Inc()
			if m.MachinesChanged {
				t0 := time.Now()
				out = w.diff(ctx, l, o, mode, true)
				mWatchDiff.Since(t0)
			} else {
				out = &localv1.WatchResponse{}
			}
			out.Sessions, out.DeletedSessionIds = m.Sessions, m.DeletedSessionIds
		case <-kick:
			mWatchWakeKick.Inc()
		case <-tick.C:
			mWatchWakeTick.Inc()
			full = false
		}
		if out == nil {
			if l.o() != o || screenMode(o.Status()) != mode {
				return errNewRound
			}
			t0 := time.Now()
			out = w.diff(ctx, l, o, mode, full)
			mWatchDiff.Since(t0)
		}
		if !watchResponseEmpty(out) {
			mWatchSent.Inc()
			if err := send(out); err != nil {
				return err
			}
		} else {
			mWatchNoop.Inc()
		}
	}
}

// drainSessionChanges coalesces any additional session updates already queued
// in ch without blocking, preserving latest-state-wins ordering across updates
// and deletions while adding zero latency when ch has no backlog.
func drainSessionChanges(first *localv1.WatchSessionsResponse, ch <-chan *localv1.WatchSessionsResponse) (*localv1.WatchSessionsResponse, bool) {
	var next *localv1.WatchSessionsResponse
	var ok bool
	select {
	case next, ok = <-ch:
		if !ok {
			return nil, false
		}
	default:
		return first, true
	}

	merged := &localv1.WatchSessionsResponse{
		MachinesChanged: first.GetMachinesChanged(),
	}
	sessIdx := make(map[string]int, len(first.GetSessions())+len(next.GetSessions()))
	deleted := make(map[string]int, len(first.GetDeletedSessionIds())+len(next.GetDeletedSessionIds()))

	applyBatch := func(b *localv1.WatchSessionsResponse) {
		if b.GetMachinesChanged() {
			merged.MachinesChanged = true
		}
		for _, id := range b.GetDeletedSessionIds() {
			if id == "" {
				continue
			}
			if idx, exists := sessIdx[id]; exists {
				merged.Sessions[idx] = nil
				delete(sessIdx, id)
			}
			if _, exists := deleted[id]; !exists {
				deleted[id] = len(merged.DeletedSessionIds)
				merged.DeletedSessionIds = append(merged.DeletedSessionIds, id)
			}
		}
		for _, s := range b.GetSessions() {
			if s == nil || s.SessionId == "" {
				continue
			}
			if dIdx, wasDel := deleted[s.SessionId]; wasDel {
				merged.DeletedSessionIds[dIdx] = ""
				delete(deleted, s.SessionId)
			}
			if idx, exists := sessIdx[s.SessionId]; exists {
				merged.Sessions[idx] = s
			} else {
				sessIdx[s.SessionId] = len(merged.Sessions)
				merged.Sessions = append(merged.Sessions, s)
			}
		}
	}

	applyBatch(first)
	applyBatch(next)
	for {
		select {
		case more, open := <-ch:
			if !open {
				return nil, false
			}
			applyBatch(more)
		default:
			if len(merged.Sessions) > len(sessIdx) {
				compact := merged.Sessions[:0]
				for _, s := range merged.Sessions {
					if s != nil {
						compact = append(compact, s)
					}
				}
				merged.Sessions = compact
			}
			if len(merged.DeletedSessionIds) > len(deleted) {
				compact := merged.DeletedSessionIds[:0]
				for _, id := range merged.DeletedSessionIds {
					if id != "" {
						compact = append(compact, id)
					}
				}
				merged.DeletedSessionIds = compact
			}
			return merged, true
		}
	}
}

func watchResponseEmpty(r *localv1.WatchResponse) bool {
	return r == nil || (!r.Reset_ &&
		r.Status == nil &&
		r.WorkVersion == 0 &&
		r.HerKeptVersion == 0 &&
		r.Next == nil &&
		len(r.Sessions) == 0 &&
		len(r.DeletedSessionIds) == 0 &&
		len(r.Machines) == 0 &&
		len(r.RemovedMachineIds) == 0 &&
		len(r.Clients) == 0 &&
		len(r.RemovedGrantIds) == 0 &&
		len(r.Devices) == 0 &&
		len(r.RemovedDeviceKeyIds) == 0 &&
		len(r.Projects) == 0 &&
		len(r.RemovedProjects) == 0 &&
		len(r.Pairings) == 0 &&
		len(r.RemovedPairingCodes) == 0)
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
	herKept  uint64
}

// diff is what changed since the screen was last sent (everything, the
// first time). A list that could not be read is left as it was. When full is
// false (periodic screenTick without a replica/write kick), owner-mode screens
// skip re-cloning and diffing replica-only lists whose mutations always emit
// kick (Clients, and Pairings when no active pairing awaits expiry).
func (w *shown) diff(ctx context.Context, l *localService, o *Owner, mode string, full bool) *localv1.WatchResponse {
	out := &localv1.WatchResponse{}
	st := o.Status()
	// Suppress minor relay ping RTT micro-jitter when connection health is
	// otherwise unchanged, matching Machine.Tunnel RTT suppression below.
	if oh, nh := w.status.GetRelayHealth(), st.GetRelayHealth(); oh != nil && nh != nil &&
		oh.GetPhase() == nh.GetPhase() && oh.GetRetryCount() == nh.GetRetryCount() && oh.GetLastError() == nh.GetLastError() {
		oldMs, newMs := oh.GetRttMs(), nh.GetRttMs()
		if oldMs > 0 && newMs > 0 {
			var delta uint32
			if newMs > oldMs {
				delta = newMs - oldMs
			} else {
				delta = oldMs - newMs
			}
			if delta < max(uint32(20), oldMs/4) {
				nh.RttMs = oldMs
			}
		}
	}
	if !proto.Equal(st, w.status) {
		w.status, out.Status = st, st
	}
	if mode == "status" {
		return out
	}
	if v := o.screenWorkVersion(mode); v != w.work {
		w.work, out.WorkVersion = v, v
	}
	if v := o.herKeptVersion.Load(); v != w.herKept {
		w.herKept, out.HerKeptVersion = v, v
	}
	var listedMachines []*localv1.Machine
	var haveMachines bool
	if r, err := l.ListMachines(ctx, &localv1.ListMachinesRequest{}); err == nil {
		listedMachines, haveMachines = r.Machines, true
		if mode == "owner" {
			o.rep.unreachable(r.Machines)
		}
		// Suppress minor QUIC keepalive SRTT micro-jitter on unchanged ready tunnels
		// so idle screens receive zero spurious Watch wakeups.
		for _, m := range r.Machines {
			old := w.machines.by[m.GetMachineId()]
			ot, nt := old.GetTunnel(), m.GetTunnel()
			if ot == nil || nt == nil || ot.GetPhase() != nt.GetPhase() || ot.GetPath() != nt.GetPath() {
				continue
			}
			oldMs, newMs := ot.GetRttMs(), nt.GetRttMs()
			if oldMs > 0 && newMs > 0 {
				var delta uint32
				if newMs > oldMs {
					delta = newMs - oldMs
				} else {
					delta = oldMs - newMs
				}
				if delta < max(uint32(20), oldMs/4) {
					nt.RttMs = oldMs
				}
			}
		}
		out.Machines, out.RemovedMachineIds = w.machines.diff(r.Machines, func(m *localv1.Machine) string { return m.MachineId })
	}
	if full || mode != "owner" {
		if r, err := l.ListClients(ctx, &localv1.ListClientsRequest{}); err == nil {
			out.Clients, out.RemovedGrantIds = w.clients.diff(r.Clients, func(c *localv1.Client) string { return c.GrantId })
		}
	}
	var projRes *localv1.ListProjectsResponse
	var projErr error
	if haveMachines && mode != "machine" {
		projRes, projErr = o.listProjectsForMachines(ctx, listedMachines, "")
	} else {
		projRes, projErr = l.ListProjects(ctx, &localv1.ListProjectsRequest{})
	}
	if projErr == nil {
		var removed []string
		out.Projects, removed = w.projects.diff(projRes.Projects, projectKey)
		for _, k := range removed {
			m, p, _ := strings.Cut(k, "/") // a machine id has no slash
			out.RemovedProjects = append(out.RemovedProjects, &localv1.ProjectRef{MachineId: m, ProjectId: p})
		}
	}
	if mode == "owner" {
		if haveMachines && o.requireInit() == nil {
			out.Devices, out.RemovedDeviceKeyIds = w.devices.diff(o.devicesForMachines(listedMachines), func(d *localv1.Device) string { return d.KeyId })
		} else if r, err := l.ListDevices(ctx, &localv1.ListDevicesRequest{}); err == nil {
			out.Devices, out.RemovedDeviceKeyIds = w.devices.diff(r.Devices, func(d *localv1.Device) string { return d.KeyId })
		}
		if full || len(w.pairings.by) > 0 {
			if r, err := l.ListPairingRequests(ctx, &localv1.ListPairingRequestsRequest{}); err == nil {
				out.Pairings, out.RemovedPairingCodes = w.pairings.diff(r.Pairings, func(p *localv1.PairingDetails) string { return p.Code })
			}
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
