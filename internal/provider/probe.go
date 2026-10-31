package provider

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/hermeshai/hermesh/internal/shellenv"
)

// Problem is why an agent is not available: not found, or found but unable
// to run, and why (Info.Detail says what was printed).
type Problem int

const (
	ProblemNone Problem = iota
	ProblemNotFound
	// Its desktop app is installed, not the command line tool (Detail: the app).
	ProblemAppWithoutCLI
	// The machine can't start new programs (out of processes).
	ProblemNoProcesses
	ProblemTimedOut
	// What runs it is missing (Detail: e.g. "node").
	ProblemMissingRuntime
	ProblemFailed
)

// SearchPath is the PATH agents are looked for on: the given one, or the
// login shell's when it is "" (read again while reading it fails).
func SearchPath(pathEnv string) string {
	if pathEnv != "" {
		return pathEnv
	}
	return shellenv.Path()
}

// ProbeEnv is the environment for checking an agent: a new terminal's, as
// its sessions get, with the PATH it was looked for on, so a script finds
// its interpreter (node).
func ProbeEnv(pathEnv string) []string {
	return append(shellenv.Without(shellenv.Environ(), "PATH"), "PATH="+SearchPath(pathEnv))
}

// Probe runs bin with args in env and returns what it printed; when it
// fails, why, as a Problem and a short detail.
func Probe(ctx context.Context, env []string, bin string, args ...string) (stdout []byte, problem Problem, detail string) {
	cmd := Command(ctx, bin, args...)
	cmd.Env = env
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if err == nil {
		return out.Bytes(), ProblemNone, ""
	}
	p, d := whyFailed(ctx, bin, err, errOut.String()+"\n"+out.String())
	return out.Bytes(), p, d
}

var (
	noProcesses = regexp.MustCompile(`(?i)resource temporarily unavailable|fork failed|cannot fork|too many processes`)
	// "env: node: No such file or directory", "/usr/bin/env: ‘node’: No such file or directory"
	envMissing = regexp.MustCompile(`env: ['‘]?([\w.+-]+)['’]?: No such file or directory`)
	// "bad interpreter: /usr/local/bin/node: no such file or directory"
	badInterpreter = regexp.MustCompile(`bad interpreter: ([^:\s]+)`)
)

// whyFailed says why running bin failed, from the error and what it printed.
func whyFailed(ctx context.Context, bin string, err error, printed string) (Problem, string) {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return ProblemTimedOut, ""
	case errors.Is(err, syscall.EAGAIN) || noProcesses.MatchString(printed):
		return ProblemNoProcesses, firstLine(printed, err)
	case errors.Is(err, syscall.ENOENT):
		// The script is there (it was found): what is missing is the
		// interpreter its first line names.
		if run := interpreter(bin); run != "" {
			return ProblemMissingRuntime, run
		}
	}
	if m := envMissing.FindStringSubmatch(printed); m != nil {
		return ProblemMissingRuntime, m[1]
	}
	if m := badInterpreter.FindStringSubmatch(printed); m != nil {
		return ProblemMissingRuntime, filepath.Base(m[1])
	}
	return ProblemFailed, firstLine(printed, err)
}

// interpreter is the program a script's "#!" line runs ("node" for
// "#!/usr/bin/env node"), or "".
func interpreter(script string) string {
	f, err := os.Open(script)
	if err != nil {
		return ""
	}
	defer f.Close()
	b := make([]byte, 256)
	n, _ := f.Read(b)
	line, _, _ := strings.Cut(string(b[:n]), "\n")
	words := strings.Fields(strings.TrimPrefix(line, "#!"))
	if !strings.HasPrefix(line, "#!") || len(words) == 0 {
		return ""
	}
	if filepath.Base(words[0]) == "env" && len(words) > 1 {
		return words[len(words)-1]
	}
	return filepath.Base(words[0])
}

// firstLine is the first line printed, or the error when nothing was, cut
// to a length a screen can show.
func firstLine(printed string, err error) string {
	line := ""
	for _, l := range strings.Split(printed, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}
	if line == "" && err != nil {
		line = err.Error()
	}
	if r := []rune(line); len(r) > 200 {
		line = string(r[:200]) + "…"
	}
	return line
}

// Exists: bin is a path to a file (an override may name one that is gone).
func Exists(bin string) bool { return bin != "" && fileExists(bin) }

// AppWithoutCLI is the first of apps (bundle names) installed in
// /Applications or ~/Applications, or "". (Not asked when a test names the
// agent's binary: what this computer has installed stays out of it.)
func AppWithoutCLI(apps ...string) string {
	home, _ := os.UserHomeDir()
	for _, a := range apps {
		for _, dir := range []string{"/Applications", filepath.Join(home, "Applications")} {
			if st, err := os.Stat(filepath.Join(dir, a)); err == nil && st.IsDir() {
				return a
			}
		}
	}
	return ""
}
