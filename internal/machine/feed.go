package machine

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// MachineService.Watch (docs/state-sync.md §3.3): what a watcher may see of
// this machine, then each change, resumable after a reconnect.

// changeLog numbers the changes to this machine's sessions and projects
// since hermeshd started, keeping each one's last, so a watcher that held
// what it was sent up to change n gets only what changed after n. A token
// from another start is refused: the watcher starts again.
type changeLog struct {
	mu       sync.Mutex
	start    [8]byte           // random per start
	n        uint64            // changes so far
	sessions map[string]uint64 // each session's last change (deleted ones too)
	projects map[string]uint64 // each project's last change (removed ones too)
	kicks    map[chan struct{}]bool
}

func newChangeLog() *changeLog {
	c := &changeLog{sessions: map[string]uint64{}, projects: map[string]uint64{}, kicks: map[chan struct{}]bool{}}
	rand.Read(c.start[:])
	return c
}

func (c *changeLog) add(m map[string]uint64, id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	m[id] = c.n
	for k := range c.kicks {
		select {
		case k <- struct{}{}:
		default:
		}
	}
}

func (c *changeLog) session(id string) { c.add(c.sessions, id) }
func (c *changeLog) project(id string) { c.add(c.projects, id) }

// token names change n of this start.
func (c *changeLog) token(n uint64) []byte {
	return binary.BigEndian.AppendUint64(slices.Clone(c.start[:]), n)
}

// resume is the change a token names, if it is of this start.
func (c *changeLog) resume(token []byte) (uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(token) != 16 || [8]byte(token[:8]) != c.start {
		return 0, false
	}
	n := binary.BigEndian.Uint64(token[8:])
	return n, n <= c.n
}

// since is the sessions and projects changed after change n, and the
// change it is at now.
func (c *changeLog) since(n uint64) (sessions, projects []string, now uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, at := range c.sessions {
		if at > n {
			sessions = append(sessions, id)
		}
	}
	for id, at := range c.projects {
		if at > n {
			projects = append(projects, id)
		}
	}
	return sessions, projects, c.n
}

// changes is the machine's change log, made on first use.
func (s *Sessions) changes() *changeLog {
	s.changesOnce.Do(func() { s.changeLog = newChangeLog() })
	return s.changeLog
}

// statusEvery: how often a watch compares the machine's state with what it
// last sent (counts of sessions, pausing, agents checked); in process only.
const statusEvery = time.Second

// Watch sends what p may see of this machine, then each change (see
// MachineService.Watch), until ctx is done.
func (n *Machine) Watch(ctx context.Context, p *Principal, req *machinev1.WatchRequest, send func(*machinev1.WatchResponse) error) error {
	c := n.sessions.changes()
	kick := make(chan struct{}, 1)
	c.mu.Lock()
	c.kicks[kick] = true
	at := c.n
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.kicks, kick)
		c.mu.Unlock()
	}()

	first := &machinev1.WatchResponse{}
	if from, ok := c.resume(req.ResumeToken); ok {
		var err error
		if first, at, err = n.changesSince(ctx, p, req.View, from); err != nil {
			return err
		}
	} else {
		limit := req.Limit
		if limit == 0 {
			limit = 50
		}
		list, next, err := n.sessions.List(p, &sessionv1.ListSessionsRequest{Limit: limit, View: req.View})
		if err != nil {
			return err
		}
		projects, err := (&machineService{n: n}).projects(ctx, p, false)
		if err != nil {
			return err
		}
		first = &machinev1.WatchResponse{Reset_: true, Sessions: list, Next: next, Projects: projects.Projects}
		if req.HeldAt != nil { // a watcher from before this start: what went meanwhile
			first.DeletedSessionIds = n.sessions.deletedSince(p, req.HeldAt.AsTime())
		}
	}
	status := n.Status()
	first.Status = status
	if err := n.sendWatch(c, at, first, send); err != nil {
		return err
	}
	tick := time.NewTicker(statusEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-kick:
		case <-tick.C:
		}
		m, now, err := n.changesSince(ctx, p, req.View, at)
		if err != nil {
			return err
		}
		if st := n.Status(); !proto.Equal(st, status) {
			status, m.Status = st, st
		}
		if now != at || m.Status != nil {
			at = now
			if err := n.sendWatch(c, at, m, send); err != nil {
				return err
			}
		}
		select { // changes that follow come in the next message
		case <-ctx.Done():
			return nil
		case <-time.After(watchEvery):
		}
	}
}

func (n *Machine) sendWatch(c *changeLog, at uint64, m *machinev1.WatchResponse, send func(*machinev1.WatchResponse) error) error {
	m.ResumeToken, m.SentAt = c.token(at), timestamppb.Now()
	return send(m)
}

// changesSince is what p may see of the sessions and projects changed after
// change from, and the change it is at.
func (n *Machine) changesSince(ctx context.Context, p *Principal, view sessionv1.SessionView, from uint64) (*machinev1.WatchResponse, uint64, error) {
	sessions, projects, now := n.sessions.changes().since(from)
	m := &machinev1.WatchResponse{}
	for _, id := range sessions {
		if s, err := n.sessions.Get(p, id); err == nil {
			if view == sessionv1.SessionView_SESSION_VIEW_BASIC {
				s = BasicSession(s)
			}
			m.Sessions = append(m.Sessions, s)
		} else if n.sessions.deletedFor(p, id) {
			m.DeletedSessionIds = append(m.DeletedSessionIds, id)
		}
	}
	slices.SortFunc(m.Sessions, NewestFirst)
	if len(projects) > 0 {
		all, err := (&machineService{n: n}).projects(ctx, p, false)
		if err != nil {
			return nil, 0, err
		}
		by := map[string]*machinev1.Project{}
		for _, pr := range all.Projects {
			by[pr.ProjectId] = pr
		}
		for _, id := range projects {
			if pr := by[id]; pr != nil {
				m.Projects = append(m.Projects, pr)
			} else {
				m.RemovedProjectIds = append(m.RemovedProjectIds, id)
			}
		}
	}
	return m, now, nil
}

// deletedSince is the sessions p could read that were deleted at or after t.
func (s *Sessions) deletedSince(p *Principal, t time.Time) []string {
	rows, err := s.st.DeletedSessionsSince(t)
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range rows {
		if s.canRead(p, r) {
			out = append(out, r.ID)
		}
	}
	return out
}
