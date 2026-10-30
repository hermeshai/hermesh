package flock

import "testing"

// One data directory, one daemon: a second lock fails until the first is
// released.
func TestAcquire(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir); err == nil {
		t.Fatal("locked twice")
	}
	l.Release()
	l, err = Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
}
