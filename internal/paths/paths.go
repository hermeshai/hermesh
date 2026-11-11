// Package paths says where Hermesh keeps its things on this computer
// (docs/windows.md §4.1). apps/desktop/src/main/paths.ts mirrors DataDir
// and LocalAPI.
package paths

import (
	"os"
	"path/filepath"
)

// DataDir is hermeshd's data directory: $HERMESH_DATA_DIR, else this
// system's place for it.
func DataDir() string {
	if v := os.Getenv("HERMESH_DATA_DIR"); v != "" {
		return v
	}
	return dataDir()
}

// CLIHome is where the agents' CLI keeps its key and state: $HERMESH_HOME,
// else this system's place for it.
func CLIHome() string {
	if v := os.Getenv("HERMESH_HOME"); v != "" {
		return v
	}
	return cliHome()
}

// Local is where the machine machineID takes the connections of its own
// user's agents on this computer, so that they need not go through the
// relay to reach it (docs/technical-design.md §4.3): "" when HERMESH_LOCAL
// is off. It depends on nothing but the machine and the user, so an agent
// finds it knowing neither the machine's data directory nor its own home;
// $HERMESH_LOCAL_DIR puts it elsewhere.
func Local(machineID string) string {
	if os.Getenv("HERMESH_LOCAL") == "off" || machineID == "" {
		return ""
	}
	dir := os.Getenv("HERMESH_LOCAL_DIR")
	if dir == "" {
		dir = localDir()
	}
	return filepath.Join(dir, machineID+localExt)
}
