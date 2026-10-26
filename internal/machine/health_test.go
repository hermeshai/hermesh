package machine

import "testing"

// The user's processes and their limit are read without starting one.
func TestUserProcesses(t *testing.T) {
	pc, err := userProcesses()
	if err != nil {
		t.Fatal(err)
	}
	if pc.used == 0 {
		t.Fatalf("no processes counted: %+v", pc)
	}
	if pc.limit != 0 && pc.used > pc.limit*2 {
		t.Fatalf("more processes than the limit allows: %+v", pc)
	}
}

func TestNearLimit(t *testing.T) {
	for _, c := range []struct {
		used, limit uint64
		near        bool
	}{{2669, 2666, true}, {2400, 2666, true}, {2300, 2666, false}, {5, 0, false}} {
		if got := nearLimit(c.used, c.limit); got != c.near {
			t.Errorf("nearLimit(%d, %d) = %v", c.used, c.limit, got)
		}
	}
}
