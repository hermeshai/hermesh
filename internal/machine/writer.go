package machine

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/pkg/metrics"
)

// writeOp is one event, session row, idempotency key and/or turn commit to
// persist. done, if set, receives the commit result.
type writeOp struct {
	l    *live
	ev   *sessionv1.SessionEvent
	row  *sessionRow
	idem *idemKey
	turn *turnRec
	done chan error
}

type idemKey struct{ clientID, key, sessionID string }

type turnRec struct {
	sessionID string
	turn      uint32
	commit    string
}

// writer persists events and session rows in batched transactions (one
// fsync per batch instead of per event) and hands each event to deliver
// only after it is committed, so subscribers never see an event that a
// crash could lose.
type writer struct {
	st      *Store
	ch      chan writeOp
	deliver func(*live, *sessionv1.SessionEvent)
	done    chan struct{}
	log     *slog.Logger
	// stored is told of each session row committed.
	stored func(id string)

	// closeMu orders put against close: writes after close (from session
	// goroutines still finishing at shutdown) are dropped, not a panic.
	closeMu sync.RWMutex
	closed  bool
}

var errWriterClosed = errors.New("the session store is closed")

var (
	mBatch     = metrics.Default.Histogram("hermesh_machine_write_batch_seconds")
	mBatchOps  = metrics.Default.Counter("hermesh_machine_write_ops_total")
	mBatches   = metrics.Default.Counter("hermesh_machine_write_batches_total")
	mWriteWait = metrics.Default.Histogram("hermesh_machine_write_enqueue_seconds")
	// begin covers waiting for the writer connection; commit is the WAL
	// append (plus any automatic checkpoint).
	mBatchBegin  = metrics.Default.Histogram("hermesh_machine_write_begin_seconds")
	mBatchCommit = metrics.Default.Histogram("hermesh_machine_write_commit_seconds")
)

const maxBatch = 2048

func newWriter(st *Store, log *slog.Logger, deliver func(*live, *sessionv1.SessionEvent), stored func(id string)) *writer {
	w := &writer{st: st, ch: make(chan writeOp, 8192), deliver: deliver, stored: stored, done: make(chan struct{}), log: log}
	go w.run()
	return w
}

func (w *writer) put(op writeOp) {
	w.closeMu.RLock()
	defer w.closeMu.RUnlock()
	if w.closed {
		if op.done != nil {
			op.done <- errWriterClosed
		}
		return
	}
	select {
	case w.ch <- op:
	default:
		start := time.Now()
		w.ch <- op // backpressure: the writer is behind
		mWriteWait.Since(start)
	}
}

// sync enqueues op and waits until it, and everything queued before it, is
// committed: many concurrent callers share one transaction.
func (w *writer) sync(op writeOp) error {
	op.done = make(chan error, 1)
	w.put(op)
	return <-op.done
}

func (w *writer) close() {
	w.closeMu.Lock()
	w.closed = true
	close(w.ch)
	w.closeMu.Unlock()
	<-w.done
}

func (w *writer) run() {
	defer close(w.done)
	batch := make([]writeOp, 0, maxBatch)
	for op := range w.ch {
		batch = append(batch[:0], op)
	drain:
		for len(batch) < maxBatch {
			select {
			case op, ok := <-w.ch:
				if !ok {
					break drain
				}
				batch = append(batch, op)
			default:
				break drain
			}
		}
		start := time.Now()
		err := w.st.WriteBatch(batch)
		if err != nil {
			w.log.Error("persist batch", "ops", len(batch), "err", err)
		}
		mBatch.Since(start)
		mBatches.Inc()
		mBatchOps.Add(int64(len(batch)))
		for _, op := range batch {
			if op.ev != nil {
				w.deliver(op.l, op.ev)
			}
			if op.row != nil {
				w.stored(op.row.ID)
			}
			if op.done != nil {
				op.done <- err
			}
		}
	}
}

// snapshotRow copies a session row for persistence (caller holds l.mu).
// The row is stored because it changed, so it is stamped updated now.
func snapshotRow(r *sessionRow) *sessionRow {
	r.UpdatedAt = time.Now()
	c := *r
	c.Progress = proto.Clone(r.Progress).(*sessionv1.SessionProgress)
	return &c
}
