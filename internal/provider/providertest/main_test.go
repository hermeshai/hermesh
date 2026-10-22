package providertest

import (
	"fmt"
	"os"
	"testing"

	"github.com/hermeshai/hermesh/internal/provider/codex/codextest"
)

// Real Codex runs (HERMESH_REAL_CLI) must not write threads or trusted
// projects into the user's own Codex state.
func TestMain(m *testing.M) {
	cleanup, err := codextest.IsolateHome()
	if err != nil {
		fmt.Fprintln(os.Stderr, "isolating CODEX_HOME:", err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}
