package owner

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/peer"
	"github.com/hermeshai/hermesh/pkg/metrics"
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
	// screens (LocalService.Watch) are told when what they show may have
	// changed, besides sessions.
	screens map[chan struct{}]bool
	usage   map[usageKey]usageHeld // other machines' GetUsage answers
	src     *sources               // nil while stopped
	// stopped: the sources that ran last, stopped (idle, or the app paused):
	// the next start for the same owner begins from what they held and
	// resumes each machine's Watch from its token.
	stopped *sources
	lastUse time.Time // the last list call, or the last watcher leaving
	polls   uint64    // numbers the relay polls, so an older one never overwrites a newer
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
	// listed is closed once the relay was first asked for the lists.
	listed chan struct{}
	// warm: it began from what stopped sources held, so lists answer at
	// once, last known, while the machines catch up.
	warm bool
	// since: on a start from nothing, each machine's clock when this device
	// last applied what it sent (kept on disk): its first Watch asks for
	// the sessions deleted since, which the screens may still show.
	since map[string]*timestamppb.Timestamp

	machines polled[*localv1.Machine] // without this device's own
	clients  polled[*localv1.Client]
	pairings polled[*localv1.PairingDetails]

	feeds map[string]*feed // by machine id
	sent  string           // the machines' state as last sent to watchers

	// rewatch asks the relay's WatchOwner, while it is followed, to start
	// again (after this device's write); the channel sent is closed once
	// the new stream's first message is kept.
	rewatch chan chan struct{}
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
	changed  chan struct{}                 // its sessions changed status: read its state again (a machine without Watch)
	// Where its Watch resumes, and the machine's clock then.
	token  []byte
	heldAt *timestamppb.Timestamp
	// retry: how long after a failed follow it is tried again.
	retry time.Duration
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
	// retryFirst after a failed follow it is tried again, then twice as
	// long each time, up to retryMax.
	retryFirst = 2 * time.Second
	retryMax   = 30 * time.Second
	// screenWait: on a start from nothing, how long a screen's first
	// message waits for the machines once the relay listed them; those
	// still being read come as they are (Machine.syncing).
	screenWait = 2 * time.Second
)

// How followed machines' watches started: afresh, resumed from where they
// were, or afresh because the machine refused the token.
var (
	mWatchFresh   = metrics.Default.Counter(`hermesh_owner_machine_watches_total{start="fresh"}`)
	mWatchResumed = metrics.Default.Counter(`hermesh_owner_machine_watches_total{start="resumed"}`)
	mWatchRefused = metrics.Default.Counter(`hermesh_owner_machine_watches_total{start="refused"}`)
)

func newReplica(o *Owner) *replica {
	return &replica{o: o, watchers: map[chan *localv1.WatchSessionsResponse]bool{}, screens: map[chan struct{}]bool{}, usage: map[usageKey]usageHeld{}}
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
	if held(ctx) && r.src == nil && r.stopped != nil {
		// A screen reading what is held never starts the sources: after a
		// pause, the last tick of a watch the app has left would start
		// them again, to be stopped as idle once the process thaws, under
		// the follows the app's next watch begins.
		s := r.stopped
		r.mu.Unlock()
		return s, nil
	}
	r.lastUse = time.Now()
	s := r.startLocked()
	warm := s.warm
	r.mu.Unlock()
	if warm || held(ctx) {
		return s, nil
	}
	select {
	case <-s.settled:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// heldOnly marks a context whose list calls answer from what the replica
// holds now: without waiting for it to settle, or asking a machine it does
// not follow. A screen's Watch reads so, once its first message is out.
type heldOnly struct{}

func held(ctx context.Context) bool { return ctx.Value(heldOnly{}) != nil }

// firstScreen waits until a screen's first message may go out: at once
// when the sources began from what they held; else once the relay listed
// the machines and they are followed, or screenWait after the listing.
func (r *replica) firstScreen(ctx context.Context) error {
	if err := r.o.requireInit(); err != nil {
		return err
	}
	r.mu.Lock()
	s := r.startLocked()
	r.mu.Unlock()
	if s.warm {
		return nil
	}
	select {
	case <-s.listed:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.settled:
	case <-time.After(screenWait):
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func (r *replica) startLocked() *sources {
	who := r.o.who()
	if r.src != nil && r.src.who != who {
		r.src.cancel() // signed out, or in again: nothing of before holds
		r.src = nil
	}
	if r.src == nil {
		ctx, cancel := context.WithCancel(context.Background())
		s := &sources{ctx: ctx, cancel: cancel, who: who, settled: make(chan struct{}), listed: make(chan struct{}), feeds: map[string]*feed{}, rewatch: make(chan chan struct{})}
		if h := r.stopped; h != nil && h.who == who {
			s.adopt(h)
		} else {
			r.o.heldSince(s) // what deletions to ask the machines for (a start of the app)
		}
		r.stopped = nil
		r.src = s
		go r.run(s)
	}
	return r.src
}

// adopt begins from what stopped sources held: the relay's lists and each
// machine's state, projects and sessions, last known, and where its Watch
// resumes.
func (s *sources) adopt(h *sources) {
	s.warm = true
	s.machines, s.clients, s.pairings = h.machines, h.clients, h.pairings
	s.machines.err, s.clients.err, s.pairings.err = nil, nil, nil // a poll the stop cut short
	for id, f := range h.feeds {
		s.feeds[id] = &feed{name: f.name, online: f.online, local: f.local, status: f.status, projects: f.projects, sessions: f.sessions, more: f.more,
			token: f.token, heldAt: f.heldAt}
	}
}

// stopLocked stops s, keeping what it held for the next start.
func (r *replica) stopLocked(s *sources) {
	s.cancel()
	r.src, r.stopped = nil, s
	r.o.keepHeldAt(s)
}

// pause stops the sources, as the iPhone app does when it leaves the front:
// nothing polls or follows while it is suspended. They start again, from
// what they held, at the next watch or list.
func (r *replica) pause() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.src != nil {
		r.stopLocked(r.src)
	}
}

// restart starts the sources again from what they hold, for the screens
// watching: after the network changed, the connections they had are gone.
func (r *replica) restart() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.src == nil {
		return
	}
	r.stopLocked(r.src)
	if len(r.watchers) > 0 {
		r.startLocked()
	}
}

func (r *replica) run(s *sources) {
	if s.warm { // the machines online when it stopped, from their tokens, while the relay is asked
		r.mu.Lock()
		for id, f := range s.feeds {
			if f.online && !f.running {
				f.running, f.started = true, make(chan struct{})
				go r.follow(s, id, f, f.started)
			}
		}
		r.mu.Unlock()
	}
	r.poll(s.ctx, s, true, true)
	close(s.listed)
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

	go r.followOwner(s)
	idle := time.NewTicker(pollMachinesEvery)
	defer idle.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-idle.C:
			r.mu.Lock()
			if r.src == s && len(r.watchers) == 0 && time.Since(r.lastUse) > sourcesIdle {
				r.stopLocked(s)
			}
			r.mu.Unlock()
		}
	}
}

// followOwner keeps what the relay lists current: as its WatchOwner stream
// pushes it, or, from a relay without WatchOwner, by asking (pollOwner).
// When the stream ends (the connection to the relay dropped), the lists are
// asked for once, which tells whether the relay can be reached, and it is
// watched again.
func (r *replica) followOwner(s *sources) {
	var answer chan struct{} // a refresh waiting for the next first message
	for s.ctx.Err() == nil {
		again, err := r.watchOwner(s, answer)
		if answer = again; again != nil {
			continue // asked to watch again
		}
		if connect.CodeOf(err) == connect.CodeUnimplemented {
			r.pollOwner(s)
			return
		}
		r.poll(s.ctx, s, true, true)
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(pollPairingsEvery):
		}
	}
}

// watchOwner keeps what the relay's WatchOwner sends until it ends; answer
// is closed once its first message is kept. again: a refresh asked to
// watch again, and waits on it.
func (r *replica) watchOwner(s *sources, answer chan struct{}) (again chan struct{}, err error) {
	defer func() {
		if answer != nil {
			close(answer) // no first message came: the refresh waits no more
		}
	}()
	ctl, _, err := r.o.control(s.ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	stream, err := ctl.WatchOwner(ctx, &relayv1.WatchOwnerRequest{})
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	asked := make(chan chan struct{}, 1)
	go func() {
		select {
		case <-ctx.Done():
		case a := <-s.rewatch:
			asked <- a
			cancel()
		}
	}()
	defer func() {
		select {
		case again = <-asked:
			err = nil
		default:
		}
	}()
	for stream.Receive() {
		u := stream.Msg()
		var machines []*localv1.Machine
		var mErr error
		if u.Machines != nil {
			machines, mErr = r.o.keepListed(r.o.machinesFrom(u.Machines), nil)
		}
		var clients []*localv1.Client
		if u.Clients != nil {
			clients = r.o.keepClients(r.o.clientsFrom(u.Clients))
			go r.o.syncCards(u.Clients.Clients)
		}
		var pairings []*localv1.PairingDetails
		if u.Pairings != nil {
			pairings = r.o.pairingsFrom(u.Pairings)
		}
		r.mu.Lock()
		r.polls++
		n := r.polls
		if u.Machines != nil && s.machines.set(n, machines, mErr) {
			r.machinesListedLocked(s)
		}
		if u.Clients != nil {
			s.clients.set(n, clients, nil)
		}
		if u.Pairings != nil {
			s.pairings.set(n, pairings, nil)
		}
		r.kickLocked()
		r.mu.Unlock()
		if answer != nil {
			close(answer)
			answer = nil
		}
	}
	return nil, cmp.Or(stream.Err(), errors.New("the relay ended the watch"))
}

// pollOwner asks a relay without WatchOwner for the machines and agents
// every pollMachinesEvery, and the pairing requests every pollPairingsEvery.
func (r *replica) pollOwner(s *sources) {
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
			if errors.Is(ctx.Err(), context.Canceled) {
				return // cut short (the sources stopped, its caller left): it tells nothing
			}
			if err != nil && s.machines.list != nil {
				list = s.machines.list // without the relay, whether they are online is not known: as last listed
			}
			if s.machines.set(n, list, err) {
				r.machinesListedLocked(s)
				r.kickLocked()
			}
		})
		wg.Go(func() {
			list, err := r.o.listClients(ctx)
			r.mu.Lock()
			if s.clients.set(n, list, err) {
				r.kickLocked()
			}
			r.mu.Unlock()
		})
	}
	if pairings {
		wg.Go(func() {
			list, err := r.o.listPairingRequests(ctx)
			r.mu.Lock()
			if s.pairings.set(n, list, err) {
				r.kickLocked()
			}
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
	o.rep.mu.Lock()
	o.rep.kickLocked() // also what is read in process, as this device's own machine
	o.rep.mu.Unlock()
}

// refresh brings what the relay lists up to date at once, after this device
// changed it, so the next list call shows the change: by watching the relay
// again (its first message is everything as it is now), or, while it is
// not watched, by asking it.
func (r *replica) refresh(ctx context.Context) {
	r.mu.Lock()
	s := r.src
	r.mu.Unlock()
	if s == nil {
		return
	}
	done := make(chan struct{})
	select {
	case s.rewatch <- done:
		select {
		case <-done:
		case <-ctx.Done():
		case <-time.After(followTimeout):
		}
		return
	case <-time.After(10 * time.Millisecond): // not watched now
	}
	r.poll(ctx, s, true, true)
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
			f = &feed{heldAt: s.since[m.MachineId]}
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
		f.ready, f.err, f.retry = true, "", 0
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
	if errors.As(err, new(noRelay)) {
		f.err = "" // not the machine's doing: it is being connected to, as the relay is
	}
	f.retry = min(max(2*f.retry, retryFirst), retryMax)
	retry := f.retry
	r.machinesChangedLocked(s)
	r.mu.Unlock()
	done()
	// Again after a while, while the relay lists it online: a relay that
	// pushes its lists (WatchOwner) says nothing while they stay the same.
	select {
	case <-s.ctx.Done():
		return
	case <-time.After(retry):
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.src == s && s.feeds[id] == f && f.online && !f.running {
		f.running, f.started = true, make(chan struct{})
		go r.follow(s, id, f, f.started)
	}
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
			r.broadcastLocked(s, &localv1.WatchSessionsResponse{Sessions: machine.BasicSessions(m.Sessions), DeletedSessionIds: m.DeletedSessionIds})
			r.mu.Unlock()
		}
		return nil
	})
	return cmp.Or(err, errors.New("stopped watching"))
}

// noRelay is a follow that failed because the relay could not be reached.
type noRelay struct{ error }

func (e noRelay) Unwrap() error { return e.error }

// followRemote follows another machine over the end-to-end channel, until
// its stream ends: its MachineService.Watch, resumed from what the feed
// held, or, on a machine without it, as followSessions does.
func (r *replica) followRemote(s *sources, id string, f *feed, ready func()) error {
	c, err := r.o.relay(s.ctx)
	if err != nil {
		return noRelay{err}
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
	if old, err := r.followWatch(ctx, s, f, n, timer, late, ready); !old {
		return err
	}
	return r.followSessions(ctx, s, f, n, timer, late, ready)
}

// followWatch follows a machine's MachineService.Watch: its first message
// (what changed since the feed's token, or everything) within
// followTimeout, then each change. old: the machine has no Watch.
func (r *replica) followWatch(ctx context.Context, s *sources, f *feed, n *peer.Machine, timer *time.Timer, late func(error) error, ready func()) (old bool, err error) {
	r.mu.Lock()
	req := &machinev1.WatchRequest{ResumeToken: f.token, HeldAt: f.heldAt, Limit: keepSessions, View: sessionv1.SessionView_SESSION_VIEW_BASIC}
	r.mu.Unlock()
	stream, err := n.Info.Watch(ctx, req)
	if err != nil {
		return false, late(err)
	}
	defer stream.Close()
	if !stream.Receive() {
		if connect.CodeOf(stream.Err()) == connect.CodeUnimplemented {
			return true, nil
		}
		return false, late(cmp.Or(stream.Err(), errors.New("stopped at once")))
	}
	if !timer.Stop() {
		return false, late(nil)
	}
	switch first := stream.Msg(); {
	case !first.Reset_:
		mWatchResumed.Inc()
	case len(req.ResumeToken) > 0:
		mWatchRefused.Inc() // the machine started again
	default:
		mWatchFresh.Inc()
	}
	r.machineChanged(s, f, n.MachineID, stream.Msg())
	r.mu.Lock()
	f.conn = n
	r.mu.Unlock()
	ready()
	for stream.Receive() {
		r.machineChanged(s, f, n.MachineID, stream.Msg())
	}
	return false, cmp.Or(stream.Err(), errors.New("stopped watching"))
}

// machineChanged keeps what a machine's Watch sent and passes its session
// changes on.
func (r *replica) machineChanged(s *sources, f *feed, machineID string, m *machinev1.WatchResponse) {
	sessions := machine.BasicSessions(m.Sessions) // a machine that ignores the view
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.src != s {
		return
	}
	f.token, f.heldAt = m.ResumeToken, m.SentAt
	deleted := m.DeletedSessionIds
	if m.Reset_ {
		held := f.sessions
		f.sessions, f.more, f.projects = map[string]*sessionv1.Session{}, m.Next != nil, nil
		for _, x := range sessions {
			f.sessions[x.SessionId] = x
		}
		deleted = append(deleted, goneFrom(held, f.sessions, m.Next)...)
	} else {
		for _, x := range sessions {
			keepLater(f.sessions, x)
		}
		for _, id := range deleted {
			delete(f.sessions, id)
		}
		f.more = trimSessions(f.sessions, keepSessions) || f.more
	}
	for _, p := range m.Projects {
		f.projects = slices.DeleteFunc(f.projects, func(x *localv1.Project) bool { return x.ProjectId == p.ProjectId })
		f.projects = append(f.projects, localProject(machineID, p))
	}
	for _, id := range m.RemovedProjectIds {
		f.projects = slices.DeleteFunc(f.projects, func(x *localv1.Project) bool { return x.ProjectId == id })
	}
	if m.Status != nil {
		f.status = m.Status
	}
	if len(sessions) > 0 || len(deleted) > 0 {
		r.broadcastLocked(s, &localv1.WatchSessionsResponse{Sessions: sessions, DeletedSessionIds: deleted})
	}
	r.kickLocked()
}

// goneFrom is the sessions held that a machine's first page (now, up to
// next) would list and does not: deleted meanwhile.
func goneFrom(held, now map[string]*sessionv1.Session, next *sessionv1.SessionCursor) []string {
	var gone []string
	for id, x := range held {
		if now[id] == nil && (next == nil || !machine.Past(x, next)) {
			gone = append(gone, id)
		}
	}
	return gone
}

func localProject(machineID string, p *machinev1.Project) *localv1.Project {
	return &localv1.Project{MachineId: machineID, ProjectId: p.ProjectId, DefaultBaseRef: p.DefaultBaseRef, Workspace: p.Workspace,
		Description: p.Description, Agents: p.Agents, Path: p.Path}
}

// followSessions follows a machine without MachineService.Watch: its
// SessionService.WatchSessions, and once that is in place its newest
// sessions, state and projects (within followTimeout), then each change;
// its state is read again after its sessions change (keepInfo).
func (r *replica) followSessions(ctx context.Context, s *sources, f *feed, n *peer.Machine, timer *time.Timer, late func(error) error, ready func()) error {
	stream, err := n.Session.WatchSessions(ctx, &sessionv1.WatchSessionsRequest{View: sessionv1.SessionView_SESSION_VIEW_BASIC})
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
	page, err := n.Session.ListSessions(ctx, &sessionv1.ListSessionsRequest{Limit: keepSessions, View: sessionv1.SessionView_SESSION_VIEW_BASIC})
	if err != nil {
		return late(err)
	}
	page.Sessions = machine.BasicSessions(page.Sessions) // a machine from before views sends them whole
	r.mu.Lock()
	held := f.sessions // as last known, before it could not be read
	f.sessions, f.more, f.changed = map[string]*sessionv1.Session{}, page.Next != nil, make(chan struct{}, 1)
	for _, x := range page.Sessions {
		f.sessions[x.SessionId] = x
	}
	// Watchers hear of the snapshot; of the sessions held, one it would list
	// and does not was deleted meanwhile.
	r.broadcastLocked(s, &localv1.WatchSessionsResponse{Sessions: page.Sessions, DeletedSessionIds: goneFrom(held, f.sessions, page.Next)})
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
			list = append(list, localProject(n.MachineID, p))
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.src == s {
		f.status = info.Status
		if projects {
			f.projects = list
		}
		r.kickLocked()
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
	m = &sessionv1.WatchSessionsResponse{Sessions: machine.BasicSessions(m.Sessions), DeletedSessionIds: m.DeletedSessionIds}
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
			keepLater(f.sessions, machine.BasicSession(sess))
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
	r.kickLocked()
	if m, fp := s.state(); fp != s.sent {
		s.sent = fp
		r.broadcastLocked(s, m)
	}
}

// kickLocked tells the screens that what they show may have changed.
func (r *replica) kickLocked() {
	for c := range r.screens {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

// screen is told (on the channel) when what a screen shows may have
// changed, until stop.
func (r *replica) screen() (<-chan struct{}, func()) {
	c := make(chan struct{}, 1)
	r.mu.Lock()
	r.screens[c] = true
	r.mu.Unlock()
	return c, func() {
		r.mu.Lock()
		delete(r.screens, c)
		r.mu.Unlock()
	}
}

// unreachable says, of each machine but this device's own, why its sessions
// and projects cannot be read now; empty while they can, or while it is
// being connected to.
func (r *replica) unreachable(machines []*localv1.Machine) {
	r.feeds(func(feeds map[string]*feed) {
		for _, m := range machines {
			f := feeds[m.MachineId]
			switch {
			case m.Local:
			case !m.Online:
				m.Unreachable = "offline"
			case f != nil && !f.ready && f.err != "":
				m.Unreachable = f.err
			case f == nil || !f.ready:
				m.Syncing = true
			}
		}
	})
}

// held is the sessions kept of the machines that cannot be read now, as
// they last were.
func (r *replica) held() []*sessionv1.Session {
	var out []*sessionv1.Session
	r.feeds(func(feeds map[string]*feed) {
		for _, f := range feeds {
			if !f.ready {
				out = slices.AppendSeq(out, maps.Values(f.sessions))
			}
		}
	})
	return out
}

// usageKey is a machine's GetUsage question.
type usageKey struct{ machineID, since, timeZone string }

// usageHeld is its answer, on what day and after how many turns of the
// machine's sessions it came.
type usageHeld struct {
	res   *sessionv1.GetUsageResponse
	day   string
	turns uint64
}

// usageDay is today in a time zone (UTC when unknown).
func usageDay(tz string) string {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	return time.Now().In(loc).Format(time.DateOnly)
}

// turns adds up the turns of a followed machine's sessions held; known is
// false while it is not followed.
func (r *replica) turns(machineID string) (n uint64, known bool) {
	r.feeds(func(feeds map[string]*feed) {
		f := feeds[machineID]
		if known = f != nil && f.ready; known {
			for _, s := range f.sessions {
				n += uint64(s.GetProgress().GetTurnCount())
			}
		}
	})
	return n, known
}

// heldUsage is a machine's kept answer to req, if still right: asked today,
// and no turn of its sessions ended (or went) since.
func (r *replica) heldUsage(machineID string, req *localv1.GetUsageRequest, day string) *sessionv1.GetUsageResponse {
	turns, known := r.turns(machineID)
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.usage[usageKey{machineID, req.Since, req.TimeZone}]
	if !known || !ok || h.day != day || h.turns != turns {
		return nil
	}
	return h.res
}

func (r *replica) keepUsage(machineID string, req *localv1.GetUsageRequest, day string, turns uint64, res *sessionv1.GetUsageResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usage[usageKey{machineID, req.Since, req.TimeZone}] = usageHeld{res: res, day: day, turns: turns}
}

// kHeldAt keeps, for each machine, its clock when this device last applied
// what its Watch sent: {machine id: RFC 3339 time}, in JSON.
const kHeldAt = "held_at"

// keepHeldAt keeps the machines' held_at of stopped sources, for a start
// from nothing (the app launched again) to ask for what was deleted since.
func (o *Owner) keepHeldAt(s *sources) {
	kept := o.heldAtKept()
	changed := false
	for id, f := range s.feeds {
		if f.heldAt == nil {
			continue
		}
		if v := f.heldAt.AsTime().Format(time.RFC3339Nano); kept[id] != v {
			kept[id], changed = v, true
		}
	}
	if changed {
		b, _ := json.Marshal(kept)
		o.st.set(kHeldAt, b)
	}
}

func (o *Owner) heldAtKept() map[string]string {
	kept := map[string]string{}
	if b, _ := o.st.get(kHeldAt); len(b) > 0 {
		json.Unmarshal(b, &kept)
	}
	return kept
}

// heldSince gives sources started from nothing the machines' kept held_at.
func (o *Owner) heldSince(s *sources) {
	s.since = map[string]*timestamppb.Timestamp{}
	for id, v := range o.heldAtKept() {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			s.since[id] = timestamppb.New(t)
		}
	}
}
