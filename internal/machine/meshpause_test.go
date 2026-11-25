package machine

import "testing"

// A machine whose device signed out keeps its mesh paused across restarts,
// until it is resumed (docs/cold-start-tech.md §2.1).
func TestMeshPausedPersists(t *testing.T) {
	dir := t.TempDir()
	open := func() *Machine {
		n, err := Open(Config{DataDir: dir, Discoverers: map[string]AgentDiscoverer{}, AllowUnpaired: true})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	n := open()
	if n.MeshPaused() {
		t.Fatal("a new machine starts paused")
	}
	if err := n.SetMeshPaused(true); err != nil {
		t.Fatal(err)
	}
	n.Close()
	n = open()
	if !n.MeshPaused() {
		t.Fatal("paused did not last a restart")
	}
	if err := n.SetMeshPaused(false); err != nil {
		t.Fatal(err)
	}
	n.Close()
	n = open()
	defer n.Close()
	if n.MeshPaused() {
		t.Fatal("resumed did not last a restart")
	}
}
