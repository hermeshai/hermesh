package shellenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// tempVars name the temp folder in a child's environment.
var tempVars = []string{"TEMP", "TMP"}

func sameName(a, b string) bool { return strings.EqualFold(a, b) }

// Source says where the PATH comes from, for diagnostics.
func Source() string { return "the registry's Environment" }

// resolve builds the PATH a new terminal would get: the machine's, then
// the user's (as Windows builds it), then this process's, then the usual
// install folders. No program is started; this process's environment is
// a new terminal's already, so there is no login environment.
func resolve() (string, []string, string, bool) {
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
	return strings.Join(out, string(filepath.ListSeparator)), nil, problem, false
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

var (
	user32             = windows.NewLazySystemDLL("user32.dll")
	findWindow         = user32.NewProc("FindWindowW")
	sendMessageTimeout = user32.NewProc("SendMessageTimeoutW")
)

// OnUserPath puts dir at the end of the user's PATH, or takes it off, as
// the registry holds it (its %VARIABLES% unexpanded), and tells Explorer
// that the environment changed, so the terminals it starts see it at once.
// Explorer only, a second at most each: a broadcast waits on every window
// in turn, seconds where one doesn't answer, and the installer has little
// time.
func OnUserPath(dir string, on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	v, _, err := k.GetStringValue("Path")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	var parts []string
	for _, p := range strings.Split(v, ";") {
		if p != "" && !strings.EqualFold(filepath.Clean(p), filepath.Clean(dir)) {
			parts = append(parts, p)
		}
	}
	if on {
		parts = append(parts, dir)
	}
	next := strings.Join(parts, ";")
	if next == v {
		return nil
	}
	if err := k.SetExpandStringValue("Path", next); err != nil {
		return err
	}
	const wmSettingChange, smtoAbortIfHung = 0x1a, 0x2
	env, _ := windows.UTF16PtrFromString("Environment")
	for _, class := range []string{"Progman", "Shell_TrayWnd"} {
		name, _ := windows.UTF16PtrFromString(class)
		if w, _, _ := findWindow.Call(uintptr(unsafe.Pointer(name)), 0); w != 0 {
			var result uintptr
			sendMessageTimeout.Call(w, wmSettingChange, 0, uintptr(unsafe.Pointer(env)), smtoAbortIfHung, 1000, uintptr(unsafe.Pointer(&result)))
		}
	}
	return nil
}
