//go:build unix

package paths

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func dataDir() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Hermesh")
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "hermesh")
	}
	return filepath.Join(home, ".local", "share", "hermesh")
}

func cliHome() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "hermesh")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "hermesh")
}

// Endpoints are Unix sockets, whose paths have a short limit: a long data
// directory's go to the temp folder.

// LocalAPI is where the owner role serves LocalService.
func LocalAPI(dataDir string) string {
	p := filepath.Join(dataDir, "run", "hermeshd.sock")
	if len(p) > 100 {
		p = filepath.Join(os.TempDir(), fmt.Sprintf("hermesh-%d", os.Getuid()), "hermeshd.sock")
	}
	return p
}

// Control is where a machine with this data directory takes its own
// user's commands; a long path's is named after the directory, so two
// machines of one user never share it.
func Control(dataDir string) string {
	p := filepath.Join(dataDir, "control.sock")
	if len(p) > 100 {
		abs, _ := filepath.Abs(dataDir)
		h := sha256.Sum256([]byte(abs))
		p = filepath.Join(os.TempDir(), fmt.Sprintf("hermesh-control-%d-%x.sock", os.Getuid(), h[:8]))
	}
	return p
}

// Tools is where the sessions of the machine machineID reach their tools.
func Tools(dataDir, machineID string) string {
	p := filepath.Join(dataDir, "tools.sock")
	if len(p) > 100 {
		p = filepath.Join(os.TempDir(), "hermesh-"+strings.TrimPrefix(machineID, "m_")[:12]+".sock")
	}
	return p
}
