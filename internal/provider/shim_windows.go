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

// fileExists: a program named without its extension exists too.
func fileExists(p string) bool { return executable(p) != "" }

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// npmTarget finds what an npm shim starts: its last "%dp0%\…", relative
// to the shim's folder.
var npmTarget = regexp.MustCompile(`"%dp0%\\([^"]+)"`)

// voltaRun is all a Volta shim does: Volta runs the program the shim is
// named for. Volta 2.0.2's format is the only one besides npm's we have
// tested; pnpm's and yarn's shims are their own, refused as batch files.
var voltaRun = regexp.MustCompile(`(?m)^volta run %~n0 %\*\r?$`)

// lookVolta finds Volta where the shim's "volta" is found: on the PATH.
var lookVolta = func() string { return LookPath("volta", shellenv.Path()) }

// startable is what runs bin with args. cmd.exe reads the command line of
// a .cmd or .bat file by rules no quoting of arguments can satisfy, so such
// a file never runs: an npm shim gives way to the program it starts (node
// and its script, or a native program), a Volta shim to Volta itself, which
// resolves the program the shim is named for, and anything else is refused.
func startable(bin string, args []string) (string, []string, error) {
	if !strings.ContainsAny(bin, `\/`) {
		if p := LookPath(bin, shellenv.Path()); p != "" {
			bin = p
		}
	} else if p := executable(bin); p != "" {
		bin = p // named without its extension
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
	if voltaRun.Match(b) {
		v := lookVolta()
		if v == "" {
			return "", nil, fmt.Errorf("%s is a Volta shim, and volta is not on the PATH", bin)
		}
		return v, append([]string{"run", strings.TrimSuffix(filepath.Base(bin), filepath.Ext(bin))}, args...), nil
	}
	m := npmTarget.FindAllStringSubmatch(string(b), -1)
	if len(m) == 0 {
		return "", nil, fmt.Errorf("%s is a batch file, which Hermesh does not run (it follows npm's and Volta's shims only)", bin)
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
