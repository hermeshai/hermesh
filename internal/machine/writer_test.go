package machine

import (
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// A batch the store refuses is written again: while it is refused nothing
// is delivered, and once it is stored its events are, with no error for
// whoever waited. A batch refused every time is delivered all the same,
// and its waiter gets the error.
func TestWriterTriesARefusedBatchAgain(t *testing.T) {
	saved := writeRetries
	writeRetries = []time.Duration{20 * time.Millisecond, 20 * time.Millisecond, 20 * time.Millisecond}
	t.Cleanup(func() { writeRetries = saved })

	for _, tc := range []struct {
		name     string
		refusals int // how many writes fail before one is taken
		tries    int
		stored   bool
	}{
		{"refused twice", 2, 3, true},
		{"refused every time", 100, 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			tries, delivered := 0, 0
			earlyDelivery := false
			w := &writer{ch: make(chan writeOp, 8), done: make(chan struct{}), log: slog.New(slog.NewTextHandler(io.Discard, nil)),
				stored: func(string) {},
				deliver: func(*live, *sessionv1.SessionEvent) {
					mu.Lock()
					delivered++
					mu.Unlock()
				},
			}
			w.write = func([]writeOp) error {
				mu.Lock()
				defer mu.Unlock()
				tries++
				if delivered > 0 {
					earlyDelivery = true
				}
				if tries <= tc.refusals {
					return errors.New("disk I/O error")
				}
				return nil
			}
			go w.run()
			err := w.sync(writeOp{l: &live{}, ev: &sessionv1.SessionEvent{Seq: 1}})
			w.close()
			if tc.stored != (err == nil) {
				t.Fatalf("the waiter got %v, stored: %v", err, tc.stored)
			}
			if tries != tc.tries {
				t.Errorf("%d writes, want %d", tries, tc.tries)
			}
			if earlyDelivery {
				t.Error("an event was delivered while its batch was still being tried")
			}
			if delivered != 1 {
				t.Errorf("delivered %d times, want once", delivered)
			}
		})
	}
}
