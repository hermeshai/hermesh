package relay

import (
	"maps"
	"slices"
	"sync"

	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
)

// updates is what a machine's WatchUpdates stream has yet to send. Each
// update is whole (the root chain, the revocations, the machine's cert, the
// push targets, its grants), so only the newest of each kind is kept: a
// machine that reads slowly misses none, it skips those a newer one
// replaced.
type updates struct {
	mu      sync.Mutex
	pending map[int32]*relayv1.MachineUpdate // by the update's field number
	// wake is signalled when pending has something to take.
	wake chan struct{}
}

func newUpdates() *updates {
	return &updates{pending: map[int32]*relayv1.MachineUpdate{}, wake: make(chan struct{}, 1)}
}

// put leaves u for the stream, in place of a waiting one of its kind.
func (q *updates) put(u *relayv1.MachineUpdate) {
	m := u.ProtoReflect()
	kind := m.WhichOneof(m.Descriptor().Oneofs().ByName("update"))
	if kind == nil {
		return
	}
	q.mu.Lock()
	q.pending[int32(kind.Number())] = u
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// take returns what waits, the root chain first (what follows is checked
// against it), as a new stream sends them.
func (q *updates) take() []*relayv1.MachineUpdate {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]*relayv1.MachineUpdate, 0, len(q.pending))
	for _, kind := range slices.Sorted(maps.Keys(q.pending)) {
		out = append(out, q.pending[kind])
	}
	clear(q.pending)
	return out
}
