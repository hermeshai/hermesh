package owner

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// sessionWatch pushes session changes to the LocalService.WatchSessions
// callers. While anyone watches, it follows SessionService.WatchSessions on
// every online machine over the end-to-end channel (the relay only carries
// the tunnel), and asks the relay every few seconds which machines are
// online, to follow those that come.
type sessionWatch struct {
	o *Owner

	mu       sync.Mutex
	watchers map[chan *localv1.WatchSessionsResponse]bool
	round    *watchRound // nil while nobody watches
}

// watchRound follows the machines from the first watcher until the last
// one leaves.
type watchRound struct {
	cancel context.CancelFunc
	// settled: every machine online at the start is followed or failed;
	// watchers hear of the machines from then on.
	settled  bool
	machines map[string]*followed
	sent     string // the machines' state as last sent
}

// followed is a machine as the relay last listed it, and how following
// its sessions goes.
type followed struct {
	name    string
	online  bool
	running bool   // a follow is under way
	ready   bool   // its machine's watch is in place
	err     string // why the last follow failed
}

const (
	// listMachinesEvery is how often the relay is asked which machines are online.
	listMachinesEvery = 5 * time.Second
	// followTimeout bounds connecting to a machine and its watch starting.
	followTimeout = 15 * time.Second
	// watcherBuffer messages may wait for a slow watcher; then it is ended.
	watcherBuffer = 256
)

// WatchSessions sends session changes from all reachable machines (see
// LocalService.WatchSessions) until ctx is done.
func (o *Owner) WatchSessions(ctx context.Context, send func(*localv1.WatchSessionsResponse) error) error {
	if err := o.requireInit(); err != nil {
		return err
	}
	ch := o.watch.add()
	defer o.watch.remove(ch)
	for {
		select {
		case <-ctx.Done():
			return nil
		case m, ok := <-ch:
			if !ok {
				return connect.NewError(connect.CodeResourceExhausted, errors.New("fell behind on session changes; watch again"))
			}
			if err := send(m); err != nil {
				return err
			}
		}
	}
}

func (w *sessionWatch) add() chan *localv1.WatchSessionsResponse {
	ch := make(chan *localv1.WatchSessionsResponse, watcherBuffer)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.watchers[ch] = true
	switch r := w.round; {
	case r == nil:
		ctx, cancel := context.WithCancel(context.Background())
		w.round = &watchRound{cancel: cancel, machines: map[string]*followed{}}
		go w.run(ctx, w.round)
	case r.settled:
		m, _ := r.state()
		ch <- m
	}
	return ch
}

func (w *sessionWatch) remove(ch chan *localv1.WatchSessionsResponse) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.watchers, ch) // ended already if it fell behind
	if len(w.watchers) == 0 && w.round != nil {
		w.round.cancel()
		w.round = nil
	}
}

// broadcastLocked sends m to every watcher, ending those that fell behind.
func (w *sessionWatch) broadcastLocked(m *localv1.WatchSessionsResponse) {
	for ch := range w.watchers {
		select {
		case ch <- m:
		default:
			close(ch)
			delete(w.watchers, ch)
		}
	}
}

// state is what watchers are told of the machines, and its fingerprint.
func (r *watchRound) state() (*localv1.WatchSessionsResponse, string) {
	m := &localv1.WatchSessionsResponse{MachinesChanged: true}
	var ready []string
	for id, f := range r.machines {
		switch {
		case !f.online:
			m.Unreachable = append(m.Unreachable, &localv1.Unreachable{MachineId: id, MachineName: f.name, Error: "offline"})
		case f.ready:
			ready = append(ready, id)
		case f.err != "":
			m.Unreachable = append(m.Unreachable, &localv1.Unreachable{MachineId: id, MachineName: f.name, Error: f.err})
		}
	}
	slices.SortFunc(m.Unreachable, func(a, b *localv1.Unreachable) int { return strings.Compare(a.MachineName, b.MachineName) })
	slices.Sort(ready)
	fp := strings.Join(ready, ",")
	for _, u := range m.Unreachable {
		fp += "|" + u.MachineId + ":" + u.Error
	}
	return m, fp
}

// machinesChangedLocked tells the watchers when the machines' state changed.
func (w *sessionWatch) machinesChangedLocked(r *watchRound) {
	if w.round != r || !r.settled {
		return
	}
	if m, fp := r.state(); fp != r.sent {
		r.sent = fp
		w.broadcastLocked(m)
	}
}

func (w *sessionWatch) run(ctx context.Context, r *watchRound) {
	for {
		machines, err := w.o.machines(ctx)
		var starting sync.WaitGroup
		w.mu.Lock()
		if w.round != r {
			w.mu.Unlock()
			return
		}
		if err == nil {
			listed := map[string]bool{}
			for _, m := range machines {
				listed[m.MachineId] = true
				f := r.machines[m.MachineId]
				if f == nil {
					f = &followed{}
					r.machines[m.MachineId] = f
				}
				f.name, f.online = m.MachineName, m.Online || m.Local // this device's own is followed in process
				if f.online && !f.running {
					f.running = true
					starting.Add(1)
					go w.follow(ctx, r, m.MachineId, sync.OnceFunc(starting.Done))
				}
			}
			for id := range r.machines {
				if !listed[id] {
					delete(r.machines, id) // removed
				}
			}
		} else {
			w.o.log.Debug("watch sessions: machines not listed", "err", err)
		}
		w.machinesChangedLocked(r)
		w.mu.Unlock()
		if !r.settled {
			starting.Wait()
			w.mu.Lock()
			r.settled = true
			w.machinesChangedLocked(r)
			w.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(listMachinesEvery):
		}
	}
}

// follow follows one machine's sessions until its stream ends; started is
// called once it is in place or failed.
func (w *sessionWatch) follow(ctx context.Context, r *watchRound, id string, started func()) {
	set := func(ready bool, err string) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if f := r.machines[id]; f != nil {
			f.ready, f.err = ready, err
			f.running = ready || err == ""
		}
		w.machinesChangedLocked(r)
	}
	err := w.o.followMachine(ctx, id, func() {
		set(true, "")
		started()
	}, func(m *sessionv1.WatchSessionsResponse) {
		w.mu.Lock()
		if w.round == r {
			w.broadcastLocked(&localv1.WatchSessionsResponse{Sessions: m.Sessions, DeletedSessionIds: m.DeletedSessionIds})
		}
		w.mu.Unlock()
	})
	set(false, err.Error())
	started()
}

// followMachine follows a machine's SessionService.WatchSessions: ready is
// called once the machine's watch is in place (within followTimeout), then
// changed with each change, until the stream ends.
func (o *Owner) followMachine(ctx context.Context, id string, ready func(), changed func(*sessionv1.WatchSessionsResponse)) error {
	if l := o.local(); l != nil && l.MachineID() == id {
		first := true
		err := l.WatchSessions(ctx, func(m *sessionv1.WatchSessionsResponse) error {
			if first { // the watch is in place
				first = false
				ready()
			} else {
				changed(m)
			}
			return nil
		})
		return cmp.Or(err, errors.New("stopped watching"))
	}
	c, err := o.relay(ctx)
	if err != nil {
		return err
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := time.AfterFunc(followTimeout, cancel)
	late := func(err error) error {
		if ctx.Err() == nil && sctx.Err() != nil {
			return errors.New("did not answer in time")
		}
		return err
	}
	n, err := c.Machine(sctx, id)
	if err != nil {
		return late(err)
	}
	stream, err := n.Session.WatchSessions(sctx, &sessionv1.WatchSessionsRequest{})
	if err != nil {
		return late(err)
	}
	defer stream.Close()
	if !stream.Receive() {
		if connect.CodeOf(stream.Err()) == connect.CodeUnimplemented {
			return errors.New("its hermeshd is too old to report session changes; update it")
		}
		return late(cmp.Or(stream.Err(), errors.New("stopped at once")))
	}
	if !timer.Stop() {
		return late(nil)
	}
	ready()
	for stream.Receive() {
		changed(stream.Msg())
	}
	return cmp.Or(stream.Err(), errors.New("stopped watching"))
}
