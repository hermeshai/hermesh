package owner

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/peer"
)

// replica is this device's copy of the owner's world (docs/state-sync.md):
// the machines, agents and pairing requests as the relay lists them, and
// each other machine's state, projects and newest sessions, followed end to
// end (the relay only carries the tunnel). The List calls answer from it,
// and the LocalService.WatchSessions callers hear its session changes. Its
// sources run while anyone watches, or within sourcesIdle of a list call.
// This device's own machine is read in process, never copied.
type replica struct {
	o *Owner

	mu       sync.Mutex
	watchers map[chan *localv1.WatchSessionsResponse]bool
	src      *sources  // nil while stopped
	lastUse  time.Time // the last list call, or the last watcher leaving
	polls    uint64    // numbers the relay polls, so an older one never overwrites a newer
}

// sources is one run of the replica's sources, and what they found.
type sources struct {
	ctx    context.Context
	cancel context.CancelFunc
	// who: the owner and sign-in it runs for; another one starts afresh.
	who string
	// settled is closed once the relay was first polled and every machine
	// online then is followed or failed: list calls answer, and watchers
	// hear of the machines, from then on.
	settled   chan struct{}
	isSettled bool

	machines polled[*localv1.Machine] // without this device's own
	clients  polled[*localv1.Client]
	pairings polled[*localv1.PairingDetails]

	feeds map[string]*feed // by machine id
	sent  string           // the machines' state as last sent to watchers
}

// polled is what one relay list last returned.
type polled[T any] struct {
	list []T
	err  error
	poll uint64
}

func (p *polled[T]) set(poll uint64, list []T, err error) bool {
	if poll < p.poll {
		return false // a newer poll got there first
	}
	p.list, p.err, p.poll = list, err, poll
	return true
}

// feed is a machine as the relay last listed it, and how following it goes.
type feed struct {
	name    string
	online  bool
	local   bool          // this device's own: followed in process, its data read there
	running bool          // a follow is under way
	ready   bool          // its snapshot is in and its changes are followed
	err     string        // why the last follow failed
	started chan struct{} // closed once the current follow is ready or failed

	// What another machine showed, kept while it cannot be reached.
	conn     *peer.Machine // while ready
	status   *machinev1.MachineStatus
	projects []*localv1.Project
	sessions map[string]*sessionv1.Session // its newest keepSessions and every unsettled one
	more     bool                          // it has others besides
	changed  chan struct{}                 // its sessions changed status: read its state again
}

const (
	// pollMachinesEvery is how often the relay is asked for the machines
	// and agents, and pollPairingsEvery for the pairing requests.
	pollMachinesEvery = 5 * time.Second
	pollPairingsEvery = 2 * time.Second
	// sourcesIdle: the sources stop this long after the last list call or
	// watcher, so a window that reloads finds them warm.
	sourcesIdle = 60 * time.Second
	// keepSessions of each machine's newest sessions are kept, and their
	// unsettled ones: what a first page of the lists shows.
	keepSessions = 50
	// statusAfter its sessions change status, a machine's state is read
	// again (its counts of sessions); infoEvery its state and projects are
	// in any case, for changes other devices make.
	statusAfter = time.Second
	infoEvery   = 30 * time.Second
	// followTimeout bounds connecting to a machine and its snapshot, and
	// one poll of the relay.
	followTimeout = 15 * time.Second
	// watcherBuffer messages may wait for a slow watcher; then it is ended.
	watcherBuffer = 256
)

func newReplica(o *Owner) *replica {
	return &replica{o: o, watchers: map[chan *localv1.WatchSessionsResponse]bool{}}
}

// who names the owner and sign-in the sources run for.
func (o *Owner) who() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.signedOut {
		return o.ownerID + " signed out"
	}
	return o.ownerID
}

// use is the sources, started if stopped, once they settled.
func (r *replica) use(ctx context.Context) (*sources, error) {
	if err := r.o.requireInit(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.lastUse = time.Now()
	s := r.startLocked()
	r.mu.Unlock()
	select {
	case <-s.settled:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *replica) startLocked() *sources {
	who := r.o.who()
	if r.src != nil && r.src.who != who {
		r.src.cancel() // signed out, or in again: nothing of before holds
		r.src = nil
	}
	if r.src == nil {
		ctx, cancel := context.WithCancel(context.Background())
		r.src = &sources{ctx: ctx, cancel: cancel, who: who, settled: make(chan struct{}), feeds: map[string]*feed{}}
		go r.run(r.src)
	}
	return r.src
}

func (r *replica) run(s *sources) {
	r.poll(s.ctx, s, true, true)
	r.mu.Lock()
	var starting []chan struct{}
	for _, f := range s.feeds {
		if f.running {
			starting = append(starting, f.started)
		}
	}
	r.mu.Unlock()
	for _, c := range starting {
		<-c // each follow ends within followTimeout or is ready
	}
	r.mu.Lock()
	s.isSettled = true
	close(s.settled)
	r.machinesChangedLocked(s)
	r.mu.Unlock()

	machines, pairings := time.NewTicker(pollMachinesEvery), time.NewTicker(pollPairingsEvery)
	defer machines.Stop()
	defer pairings.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-pairings.C:
			r.poll(s.ctx, s, false, true)
		case <-machines.C:
			r.mu.Lock()
			if r.src == s && len(r.watchers) == 0 && time.Since(r.lastUse) > sourcesIdle {
				s.cancel()
				r.src = nil
			}
			r.mu.Unlock()
			r.poll(s.ctx, s, true, false)
		}
	}
}

// poll asks the relay for the machines and agents, or the pairing
// requests, or both, and keeps what it answers.
func (r *replica) poll(ctx context.Context, s *sources, machines, pairings bool) {
	if ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, followTimeout)
	defer cancel()
	r.mu.Lock()
	r.polls++
	n := r.polls
	r.mu.Unlock()
	var wg sync.WaitGroup
	if machines {
		wg.Go(func() {
			list, err := r.o.listedMachines(ctx)
			r.mu.Lock()
			defer r.mu.Unlock()
			if s.machines.set(n, list, err) {
				r.machinesListedLocked(s)
			}
		})
		wg.Go(func() {
			list, err := r.o.listClients(ctx)
			r.mu.Lock()
			s.clients.set(n, list, err)
			r.mu.Unlock()
		})
	}
	if pairings {
		wg.Go(func() {
			list, err := r.o.listPairingRequests(ctx)
			r.mu.Lock()
			s.pairings.set(n, list, err)
			r.mu.Unlock()
		})
	}
	wg.Wait()
}

// wrote brings the replica up to date after this device changed something
// (err: unless it failed), so the next list call shows the change: what the
// relay lists, when relay, and the machines' state and projects.
func (o *Owner) wrote(ctx context.Context, err error, relay bool, machineIDs ...string) {
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	if relay {
		wg.Go(func() { o.rep.refresh(ctx) })
	}
	for _, id := range machineIDs {
		wg.Go(func() { o.rep.refreshMachine(ctx, id) })
	}
	wg.Wait()
}

// refresh polls the relay again at once, after this device changed what it
// lists, so the next list call shows the change.
func (r *replica) refresh(ctx context.Context) {
	r.mu.Lock()
	s := r.src
	r.mu.Unlock()
	if s != nil {
		r.poll(ctx, s, true, true)
	}
}

// refreshMachine reads a machine's state and projects again at once, after
// this device changed them.
func (r *replica) refreshMachine(ctx context.Context, id string) {
	r.mu.Lock()
	s := r.src
	var f *feed
	if s != nil {
		f = s.feeds[id]
	}
	if f == nil || !f.ready || f.conn == nil {
		r.mu.Unlock()
		return
	}
	conn := f.conn
	r.mu.Unlock()
	r.readInfo(ctx, s, f, conn, true)
}

// machinesListedLocked follows the machines just listed that are online,
// and forgets those removed.
func (r *replica) machinesListedLocked(s *sources) {
	listed, _ := r.o.withLocal(s.machines.list, s.machines.err)
	seen := map[string]bool{}
	for _, m := range listed {
		seen[m.MachineId] = true
		f := s.feeds[m.MachineId]
		if f == nil {
			f = &feed{}
			s.feeds[m.MachineId] = f
		}
		f.name, f.local = m.MachineName, m.Local
		f.online = m.Online || m.Local // this device's own is followed in process
		if f.online && !f.running && s.ctx.Err() == nil {
			f.running, f.started = true, make(chan struct{})
			go r.follow(s, m.MachineId, f, f.started)
		}
	}
	for id := range s.feeds {
		if !seen[id] {
			delete(s.feeds, id) // removed
		}
	}
	r.machinesChangedLocked(s)
}

// follow follows one machine until its stream ends; started is closed once
// it is ready or failed.
func (r *replica) follow(s *sources, id string, f *feed, started chan struct{}) {
	done := sync.OnceFunc(func() { close(started) })
	ready := func() {
		r.mu.Lock()
		f.ready, f.err = true, ""
		r.machinesChangedLocked(s)
		r.mu.Unlock()
		done()
	}
	var err error
	if f.local {
		err = r.followLocal(s, id, ready)
	} else {
		err = r.followRemote(s, id, f, ready)
	}
	r.mu.Lock()
	f.ready, f.running, f.conn, f.err = false, false, nil, err.Error()
	r.machinesChangedLocked(s)
	r.mu.Unlock()
	done()
}

// followLocal follows this device's own machine in process; its data is
// read there, so only its changes are passed on.
func (r *replica) followLocal(s *sources, id string, ready func()) error {
	l := r.o.local()
	if l == nil || l.MachineID() != id {
		return errors.New("not running")
	}
	first := true
	err := l.WatchSessions(s.ctx, func(m *sessionv1.WatchSessionsResponse) error {
		if first { // the watch is in place
			first = false
			ready()
		} else {
			r.mu.Lock()
			r.broadcastLocked(s, &localv1.WatchSessionsResponse{Sessions: m.Sessions, DeletedSessionIds: m.DeletedSessionIds})
			r.mu.Unlock()
		}
		return nil
	})
	return cmp.Or(err, errors.New("stopped watching"))
}

// followRemote follows another machine's SessionService.WatchSessions over
// the end-to-end channel: once its watch is in place, its newest sessions,
// state and projects are read (within followTimeout), then each change is
// kept and passed on, until the stream ends.
func (r *replica) followRemote(s *sources, id string, f *feed, ready func()) error {
	c, err := r.o.relay(s.ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	timer := time.AfterFunc(followTimeout, cancel)
	late := func(err error) error {
		if s.ctx.Err() == nil && ctx.Err() != nil {
			return errors.New("did not answer in time")
		}
		return err
	}
	n, err := c.Machine(ctx, id)
	if err != nil {
		return late(err)
	}
	stream, err := n.Session.WatchSessions(ctx, &sessionv1.WatchSessionsRequest{})
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
	// The watch is in place before the snapshot, so no change falls
	// between them; one the snapshot already holds is not taken back.
	page, err := n.Session.ListSessions(ctx, &sessionv1.ListSessionsRequest{Limit: keepSessions})
	if err != nil {
		return late(err)
	}
	r.mu.Lock()
	f.sessions, f.more, f.changed = map[string]*sessionv1.Session{}, page.Next != nil, make(chan struct{}, 1)
	for _, x := range page.Sessions {
		f.sessions[x.SessionId] = x
	}
	r.mu.Unlock()
	if err := r.readInfo(ctx, s, f, n, true); err != nil {
		return late(err)
	}
	if !timer.Stop() {
		return late(nil)
	}
	r.mu.Lock()
	f.conn = n
	changed := f.changed
	r.mu.Unlock()
	ready()
	go r.keepInfo(ctx, s, f, n, changed)
	for stream.Receive() {
		r.sessionsChanged(s, f, stream.Msg())
	}
	return cmp.Or(stream.Err(), errors.New("stopped watching"))
}

// readInfo reads a machine's state, and its projects if asked, end to end.
func (r *replica) readInfo(ctx context.Context, s *sources, f *feed, n *peer.Machine, projects bool) error {
	info, err := n.Info.GetInfo(ctx, &machinev1.GetInfoRequest{})
	if err != nil {
		return err
	}
	var list []*localv1.Project
	if projects {
		res, err := n.Info.ListProjects(ctx, &machinev1.ListProjectsRequest{})
		if err != nil {
			return err
		}
		for _, p := range res.Projects {
			list = append(list, &localv1.Project{MachineId: n.MachineID, ProjectId: p.ProjectId, DefaultBaseRef: p.DefaultBaseRef, Workspace: p.Workspace,
				Description: p.Description, Agents: p.Agents, Path: p.Path})
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.src == s {
		f.status = info.Status
		if projects {
			f.projects = list
		}
	}
	return nil
}

// keepInfo reads a followed machine's state again statusAfter its sessions
// change status (changed), and its state and projects every infoEvery.
func (r *replica) keepInfo(ctx context.Context, s *sources, f *feed, n *peer.Machine, changed chan struct{}) {
	every := time.NewTicker(infoEvery)
	defer every.Stop()
	for {
		projects := false
		select {
		case <-ctx.Done():
			return
		case <-changed:
			select {
			case <-ctx.Done():
				return
			case <-time.After(statusAfter): // the changes after it come along
			}
			select {
			case <-changed:
			default:
			}
		case <-every.C:
			projects = true
		}
		if err := r.readInfo(ctx, s, f, n, projects); err != nil {
			r.o.log.Debug("machine state not read", "machine", n.MachineID, "err", err)
		}
	}
}

// sessionsChanged keeps a followed machine's changed sessions and passes
// them on.
func (r *replica) sessionsChanged(s *sources, f *feed, m *sessionv1.WatchSessionsResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.src != s {
		return
	}
	statusChanged := len(m.DeletedSessionIds) > 0
	for _, x := range m.Sessions {
		old := f.sessions[x.SessionId]
		statusChanged = statusChanged || old == nil || old.Status != x.Status
		keepLater(f.sessions, x)
	}
	for _, id := range m.DeletedSessionIds {
		delete(f.sessions, id)
	}
	f.more = trimSessions(f.sessions, keepSessions) || f.more
	if statusChanged {
		select {
		case f.changed <- struct{}{}:
		default:
		}
	}
	r.broadcastLocked(s, &localv1.WatchSessionsResponse{Sessions: m.Sessions, DeletedSessionIds: m.DeletedSessionIds})
}

// sessionWritten keeps what this device's change to a session returned
// (nil and deleted: it deleted it), so the next list shows it before the
// machine's watch tells of it.
func (r *replica) sessionWritten(id string, sess *sessionv1.Session, deleted bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.src == nil {
		return
	}
	for mid, f := range r.src.feeds {
		if f.sessions == nil || !ownsSession(mid, id) {
			continue
		}
		if deleted {
			delete(f.sessions, id)
		} else if sess != nil {
			keepLater(f.sessions, sess)
		}
	}
}

// keepLater keeps s unless an equal or later state of it is kept: later by
// its events, then by its time (as isLaterState decides in the apps).
func keepLater(sessions map[string]*sessionv1.Session, s *sessionv1.Session) {
	old := sessions[s.SessionId]
	if old != nil {
		eo, en := old.GetProgress().GetLastEventSeq(), s.GetProgress().GetLastEventSeq()
		if eo > en || eo == en && old.UpdatedAt.AsTime().After(s.UpdatedAt.AsTime()) {
			return
		}
	}
	sessions[s.SessionId] = s
}

// trimSessions keeps the newest n sessions and every unsettled one, and
// says whether it dropped any.
func trimSessions(sessions map[string]*sessionv1.Session, n int) bool {
	if len(sessions) <= n {
		return false
	}
	all := slices.SortedFunc(maps.Values(sessions), machine.NewestFirst)
	dropped := false
	for _, s := range all[n:] {
		if !machine.Unsettled(s) {
			delete(sessions, s.SessionId)
			dropped = true
		}
	}
	return dropped
}

// ownsSession says whether a session's id names the machine (by its id's prefix).
func ownsSession(machineID, sessionID string) bool {
	parts := strings.SplitN(sessionID, "_", 3)
	return len(parts) == 3 && parts[0] == "s" && strings.HasPrefix(strings.TrimPrefix(machineID, "m_"), parts[1])
}

// feeds runs fn with the followed machines (none while the sources are
// stopped), under the replica's lock.
func (r *replica) feeds(fn func(map[string]*feed)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.src != nil {
		fn(r.src.feeds)
	} else {
		fn(nil)
	}
}

// listed is a copy of a relay list, as last polled.
func listed[T proto.Message](p *polled[T]) ([]T, error) {
	out := make([]T, 0, len(p.list))
	for _, m := range p.list {
		out = append(out, proto.Clone(m).(T))
	}
	return out, p.err
}

// ---------------------------------------------------------------------------
// LocalService.WatchSessions.

// WatchSessions sends session changes from all reachable machines (see
// LocalService.WatchSessions) until ctx is done.
func (o *Owner) WatchSessions(ctx context.Context, send func(*localv1.WatchSessionsResponse) error) error {
	if err := o.requireInit(); err != nil {
		return err
	}
	ch := o.rep.add()
	defer o.rep.remove(ch)
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

func (r *replica) add() chan *localv1.WatchSessionsResponse {
	ch := make(chan *localv1.WatchSessionsResponse, watcherBuffer)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.watchers[ch] = true
	if s := r.startLocked(); s.isSettled {
		m, _ := s.state()
		ch <- m
	}
	return ch
}

func (r *replica) remove(ch chan *localv1.WatchSessionsResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.watchers, ch) // ended already if it fell behind
	r.lastUse = time.Now()
}

// broadcastLocked sends m to every watcher, ending those that fell behind.
func (r *replica) broadcastLocked(s *sources, m *localv1.WatchSessionsResponse) {
	if r.src != s {
		return
	}
	for ch := range r.watchers {
		select {
		case ch <- m:
		default:
			close(ch)
			delete(r.watchers, ch)
		}
	}
}

// state is what watchers are told of the machines, and its fingerprint.
func (s *sources) state() (*localv1.WatchSessionsResponse, string) {
	m := &localv1.WatchSessionsResponse{MachinesChanged: true}
	var ready []string
	for id, f := range s.feeds {
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
func (r *replica) machinesChangedLocked(s *sources) {
	if r.src != s || !s.isSettled {
		return
	}
	if m, fp := s.state(); fp != s.sent {
		s.sent = fp
		r.broadcastLocked(s, m)
	}
}
