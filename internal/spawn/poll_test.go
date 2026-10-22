package spawn

import (
	"os"
	"testing"
	"time"
)

// Parent ends must use the runtime poller: a blocking read would pin an OS
// thread per agent process.
func TestPipeIsPollable(t *testing.T) {
	for _, parentReads := range []bool{true, false} {
		p, c, err := Pipe(parentReads)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.SetDeadline(time.Now().Add(time.Hour)); err != nil {
			t.Errorf("parentReads=%v: parent end not pollable: %v", parentReads, err)
		}
		p.Close()
		c.Close()
	}
	r, w, _ := os.Pipe()
	t.Logf("os.Pipe deadline: %v", r.SetDeadline(time.Now()))
	r.Close()
	w.Close()
}
