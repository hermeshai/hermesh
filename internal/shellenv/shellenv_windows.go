package shellenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// tempVars name the temp folder in a child's environment.
var tempVars = []string{"TEMP", "TMP"}

func sameName(a, b string) bool { return strings.EqualFold(a, b) }

// Source says where the PATH comes from, for diagnostics.
func Source() string { return "the registry's Environment" }

// resolve builds the PATH a new terminal would get: the machine's, then
// the user's (as Windows builds it), then this process's, then the usual
// install folders. No program is started.
func resolve() (string, string, bool) {
	var parts []string
	problem := ""
	for _, k := range []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`},
		{registry.CURRENT_USER, `Environment`},
	} {
		p, err := registryPath(k.root, k.path)
		if err != nil {
			if problem == "" {
				problem = fmt.Sprintf("reading the PATH from the registry: %v", err)
			}
			continue
		}
		parts = append(parts, filepath.SplitList(p)...)
	}
	parts = append(parts, filepath.SplitList(os.Getenv("PATH"))...)
	home, _ := os.UserHomeDir()
	local, roaming := os.Getenv("LOCALAPPDATA"), os.Getenv("APPDATA")
	for _, d := range [][2]string{{home, `.local\bin`}, {local, `Microsoft\WindowsApps`}, {local, `Microsoft\WinGet\Links`}, {roaming, "npm"}} {
		if d[0] != "" {
			parts = append(parts, filepath.Join(d[0], d[1]))
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		if k := strings.ToLower(filepath.Clean(p)); p != "" && !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	return strings.Join(out, string(filepath.ListSeparator)), problem, false
}

func registryPath(root registry.Key, path string) (string, error) {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	v, typ, err := k.GetStringValue("Path")
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil // no PATH of the user's own
	}
	if err != nil {
		return "", err
	}
	if typ == registry.EXPAND_SZ {
		return registry.ExpandString(v)
	}
	return v, nil
}
