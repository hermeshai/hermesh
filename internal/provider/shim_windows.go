package provider

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/hermeshai/hermesh/internal/shellenv"
)

// hideWindow keeps a console program from opening a window: hermeshdw has
// no console for it to share.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}

// executable is the program p names: p itself when it ends in .exe, .com
// or .cmd, else p.exe, else p.cmd (an npm shim, which startable follows).
func executable(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".exe", ".com", ".cmd":
		if isFile(p) {
			return p
		}
		return ""
	}
	for _, ext := range []string{".exe", ".cmd"} {
		if isFile(p + ext) {
			return p + ext
		}
	}
	return ""
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// npmTarget finds what an npm shim starts: its last "%dp0%\…", relative
// to the shim's folder.
var npmTarget = regexp.MustCompile(`"%dp0%\\([^"]+)"`)

// startable is what runs bin with args. cmd.exe reads the command line of
// a .cmd or .bat file by rules no quoting of arguments can satisfy, so such
// a file never runs: an npm shim gives way to the program it starts (node
// and its script, or a native program), and anything else is refused.
func startable(bin string, args []string) (string, []string, error) {
	if !strings.ContainsAny(bin, `\/`) {
		if p := LookPath(bin, shellenv.Path()); p != "" {
			bin = p
		}
	}
	switch strings.ToLower(filepath.Ext(bin)) {
	case ".cmd", ".bat":
	default:
		return bin, args, nil
	}
	b, err := os.ReadFile(bin)
	if err != nil {
		return "", nil, err
	}
	m := npmTarget.FindAllStringSubmatch(string(b), -1)
	if len(m) == 0 {
		return "", nil, fmt.Errorf("%s is a batch file, which Hermesh does not run (it follows npm's shims only)", bin)
	}
	dir := filepath.Dir(bin)
	target := filepath.Join(dir, m[len(m)-1][1])
	if strings.EqualFold(filepath.Ext(target), ".exe") {
		return target, args, nil
	}
	node := filepath.Join(dir, "node.exe")
	if !isFile(node) {
		if node = LookPath("node", shellenv.Path()); node == "" {
			return "", nil, fmt.Errorf("%s runs a node script, and node is not on the PATH", bin)
		}
	}
	return node, append([]string{target}, args...), nil
}
