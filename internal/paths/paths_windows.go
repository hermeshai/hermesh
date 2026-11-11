package paths

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Everything is in Local AppData, never Roaming: a domain profile copies
// Roaming to the user's other PCs, and device keys must stay on one.

func dataDir() string { return filepath.Join(localAppData(), "Hermesh") }

func cliHome() string { return filepath.Join(localAppData(), "Hermesh", "cli") }

func localAppData() string {
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		return v
	}
	d, _ := os.UserCacheDir()
	return d
}

// Endpoints are named pipes; each of these files holds one's name
// (package localipc).

// LocalAPI is where the owner role serves LocalService.
func LocalAPI(dataDir string) string { return filepath.Join(dataDir, "run", "hermeshd.pipe") }

// Control is where a machine with this data directory takes its own
// user's commands.
func Control(dataDir string) string { return filepath.Join(dataDir, "run", "control.pipe") }

// Tools is where the sessions of the machine machineID reach their tools.
func Tools(dataDir, machineID string) string { return filepath.Join(dataDir, "run", "tools.pipe") }

// Her is where her would listen; her does not run on Windows yet
// (docs/her-server.md §12).
func Her(dataDir string) string { return filepath.Join(dataDir, "her", "her.sock") }

// Link is where the CLI with this home keeps its relay connection between
// commands (the CLI's link, docs/cli.md).
func Link(cliHome string) string { return filepath.Join(cliHome, "link", "link.pipe") }

// LinkLock is the folder whose lock says the link of this home runs. Not
// in the home: Windows deletes no file a process has open, and the link
// holds its lock while it runs, so a home with it could not be removed
// (the link ends once its home is gone).
func LinkLock(cliHome string) string {
	abs, _ := filepath.Abs(cliHome)
	h := sha256.Sum256([]byte(strings.ToLower(abs)))
	return filepath.Join(localDir(), fmt.Sprintf("link-%x", h[:8]))
}

func localDir() string { return filepath.Join(localAppData(), "Hermesh", "run") }

const localExt = ".pipe"
