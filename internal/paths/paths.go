// Package paths says where Hermesh keeps its things on this computer
// (docs/windows.md §4.1). apps/desktop/src/main/paths.ts mirrors DataDir
// and LocalAPI.
package paths

import "os"

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
