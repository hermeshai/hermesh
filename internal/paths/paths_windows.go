package paths

import (
	"os"
	"path/filepath"
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
