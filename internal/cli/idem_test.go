package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestIdempotencyKeysDiffer: CLI processes started at the same moment (an
// agent's batch of `session start`) each make their own key. Seeded from
// the clock, two in the same tick of Windows' coarse clock made the same
// one, and the machine took the second start for a retry of the first.
func TestIdempotencyKeysDiffer(t *testing.T) {
	if os.Getenv("HERMESH_TEST_IDEM_KEY") != "" {
		fmt.Println(newIdempotencyKey())
		return
	}
	const n = 32
	cmds := make([]*exec.Cmd, n)
	outs := make([]*strings.Builder, n)
	for i := range cmds {
		cmds[i] = exec.Command(os.Args[0], "-test.run=^TestIdempotencyKeysDiffer$")
		cmds[i].Env = append(os.Environ(), "HERMESH_TEST_IDEM_KEY=1")
		outs[i] = &strings.Builder{}
		cmds[i].Stdout = outs[i]
	}
	for _, c := range cmds {
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]int{}
	for i, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("process %d: %v", i, err)
		}
		key, _, _ := strings.Cut(outs[i].String(), "\n") // the test framework's PASS comes after
		if j, ok := seen[key]; ok {
			t.Fatalf("processes %d and %d made the same key %s", j, i, key)
		}
		seen[key] = i
	}
}
