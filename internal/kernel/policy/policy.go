// Package policy implements the Hermesh v2 authority lattice, sandbox path
// containment, shell command risk classification, and tool execution policy.
package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
)

// DefaultThreadRuntimeMode is used when a thread launches without an explicit mode.
const DefaultThreadRuntimeMode = identityv1.RuntimeMode_RUNTIME_MODE_AUTO

// Rank orders RuntimeMode monotonically from narrowest (1) to broadest (4).
func Rank(m identityv1.RuntimeMode) int {
	switch m {
	case identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED:
		return 1
	case identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS:
		return 2
	case identityv1.RuntimeMode_RUNTIME_MODE_AUTO:
		return 3
	case identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS:
		return 4
	default:
		return 0
	}
}

// NormalizeRequested defaults UNSPECIFIED to DefaultThreadRuntimeMode.
func NormalizeRequested(m identityv1.RuntimeMode) identityv1.RuntimeMode {
	if Rank(m) == 0 {
		return DefaultThreadRuntimeMode
	}
	return m
}

// EffectiveCeiling computes the tightest non-unspecified ceiling across machine
// policy and cryptographic Grant ceiling. Returns FULL_ACCESS if unconstrained.
func EffectiveCeiling(machineMax, grantMax identityv1.RuntimeMode) identityv1.RuntimeMode {
	ceiling := identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS
	if Rank(machineMax) > 0 && Rank(machineMax) < Rank(ceiling) {
		ceiling = machineMax
	}
	if Rank(grantMax) > 0 && Rank(grantMax) < Rank(ceiling) {
		ceiling = grantMax
	}
	return ceiling
}

// EffectiveMode intersects machine ceiling, grant ceiling, and requested thread mode.
func EffectiveMode(machineMax, grantMax, requested identityv1.RuntimeMode) identityv1.RuntimeMode {
	req := NormalizeRequested(requested)
	ceil := EffectiveCeiling(machineMax, grantMax)
	if Rank(req) > Rank(ceil) {
		return ceil
	}
	return req
}

// SandboxScope defines filesystem containment boundaries for a thread.
type SandboxScope struct {
	WorkspaceRoot string
	ExtraRoots    []string
}

// CanonicalPath resolves symlinks along the deepest existing ancestor of p so
// non-existent target files inside symlinked directories cannot escape roots.
func CanonicalPath(baseDir, p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	target := p
	if !filepath.IsAbs(target) {
		base := baseDir
		if base == "" {
			wd, err := os.Getwd()
			if err != nil {
				return "", err
			}
			base = wd
		}
		// No lexical cleaning here: "link/.." must be resolved against link's target, as the OS does.
		target = base + string(filepath.Separator) + target
	}
	return resolveLikeOS(target), nil
}

// resolveLikeOS walks absPath one component at a time in the order the kernel
// does: each existing component is resolved through symlinks before a later
// ".." is applied, so "link/../x" cannot be cleaned into the workspace while the
// real write lands next to link's target. Components that do not exist yet are
// kept lexically.
func resolveLikeOS(absPath string) string {
	vol := filepath.VolumeName(absPath)
	parts := strings.FieldsFunc(absPath[len(vol):], func(r rune) bool { return r == '/' || r == filepath.Separator })
	dest := vol + string(filepath.Separator)
	for _, part := range parts {
		switch part {
		case ".":
			continue
		case "..":
			dest = filepath.Dir(dest)
		default:
			dest = filepath.Join(dest, part)
			if resolved, err := filepath.EvalSymlinks(dest); err == nil {
				dest = resolved
			}
		}
	}
	return dest
}

// ContainsPath reports whether targetPath resolves inside WorkspaceRoot or ExtraRoots,
// and whether it touches a sensitive protected path (.git/hooks, .git/config, .hermesh, .env).
func (s SandboxScope) CheckPath(targetPath string) (inside bool, protected bool, resolved string) {
	if s.WorkspaceRoot == "" {
		return false, false, targetPath
	}
	canonRoot, err := CanonicalPath("", s.WorkspaceRoot)
	if err != nil {
		return false, false, targetPath
	}
	canonTarget, err := CanonicalPath(canonRoot, targetPath)
	if err != nil {
		return false, false, targetPath
	}
	roots := []string{canonRoot}
	for _, r := range s.ExtraRoots {
		if cr, err := CanonicalPath(canonRoot, r); err == nil {
			roots = append(roots, cr)
		}
	}
	for _, root := range roots {
		if isWithinRoot(root, canonTarget) {
			inside = true
			break
		}
	}
	protected = isProtectedPath(canonRoot, canonTarget)
	return inside, protected, canonTarget
}

func isWithinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func isProtectedPath(root, target string) bool {
	// Compare case-insensitively: on macOS and Windows ".GIT/hooks" is the real hooks directory.
	root, target = strings.ToLower(root), strings.ToLower(target)
	slash := filepath.ToSlash(target)
	base := filepath.Base(target)
	if base == ".env" || strings.HasPrefix(base, ".env.") && base != ".env.example" {
		return true
	}
	if strings.Contains(slash, "/.git/hooks/") || strings.HasSuffix(slash, "/.git/hooks") ||
		strings.HasSuffix(slash, "/.git/config") || strings.Contains(slash, "/.hermesh/") || strings.HasSuffix(slash, "/.hermesh") {
		return true
	}
	if root != "" {
		rel, err := filepath.Rel(root, target)
		if err == nil {
			relSlash := filepath.ToSlash(rel)
			if strings.HasPrefix(relSlash, ".git/hooks") || relSlash == ".git/config" || strings.HasPrefix(relSlash, ".hermesh") {
				return true
			}
		}
	}
	return false
}

// CommandRisk classifies shell command danger level.
type CommandRisk int

const (
	RiskRoutine  CommandRisk = iota // Safe read, search, lint, build, or unit test commands.
	RiskStandard                    // Package installs, arbitrary scripts, git commits, mutations.
	RiskHigh                        // Destructive, privilege-escalating, or force-push operations.
)

var (
	highRiskPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(^|\s|;|&&|\|\|)\s*sudo(\s|$)`),
		regexp.MustCompile(`(?i)\brm\s+-[a-z]*r[a-z]*f\s+(/|~|\$HOME|\.\.)`),
		regexp.MustCompile(`(?i)\bgit\s+push\b.*(--force|-f\b|--mirror|--delete|\s\+\S)`),
		regexp.MustCompile(`(?i)\bgit\s+(reset\s+--hard|clean\s+-[a-z]*f)`),
		regexp.MustCompile(`(?i)\b(mkfs|dd\s+if=|shutdown|reboot)\b`),
		regexp.MustCompile(`(?i)(curl|wget)\b.*\|\s*(sh|bash|zsh)\b`),
	}
	shellRedirOrSubshell = regexp.MustCompile(`[><` + "`" + `]|\$\(`)
)

var routinePrefixes = []string{
	"ls", "pwd", "cat", "head", "tail", "wc", "rg", "grep", "find", "which", "echo", "stat",
	"git status", "git diff", "git log", "git show", "git branch", "git rev-parse",
	"go test", "go vet", "go build", "gofmt",
	"pnpm test", "pnpm run test", "pnpm typecheck", "pnpm lint",
	"npm test", "npm run test", "npx tsc", "tsc",
	"cargo test", "cargo check", "swift test", "pytest",
}

var readOnlyStagePrefixes = []string{"ls", "cat", "head", "tail", "wc", "rg", "grep", "find", "sort", "uniq", "git diff", "git log", "git status"}

// unsafeFlags lists arguments that turn an otherwise read-only or routine
// command into one that deletes, writes, or runs arbitrary programs.
var unsafeFlags = map[string][]string{
	"find":       {"-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls"},
	"rg":         {"--pre"},
	"go test":    {"-exec", "-toolexec", "-o"},
	"go vet":     {"-vettool", "-toolexec"},
	"go build":   {"-toolexec", "-o"},
	"gofmt":      {"-w"},
	"git branch": {"-d", "-D", "--delete", "-m", "-M", "--move", "-c", "-C", "--copy", "-f", "--force", "-u", "--set-upstream-to", "--unset-upstream", "--edit-description"},
	"git diff":   {"--output", "--ext-diff"},
	"git log":    {"--output", "--ext-diff"},
	"git show":   {"--output", "--ext-diff"},
	"sort":       {"-o", "--output"},
}

// ClassifyCommand evaluates a shell command string conservatively.
func ClassifyCommand(cmd string) CommandRisk {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return RiskRoutine
	}
	for _, re := range highRiskPatterns {
		if re.MatchString(trimmed) {
			return RiskHigh
		}
	}
	if shellRedirOrSubshell.MatchString(trimmed) {
		return RiskStandard
	}
	// Every command in a chain (;, newline, &&, ||, &) must be routine.
	for _, seg := range splitCommandChain(trimmed) {
		if strings.Contains(seg, "|") {
			// Allow simple pipes only if every stage is read-only (ls/rg/grep/head/tail/wc/sort).
			for _, stage := range strings.Split(seg, "|") {
				if !isReadOnlyStage(strings.TrimSpace(stage)) {
					return RiskStandard
				}
			}
			continue
		}
		if !isSafeInvocation(seg, routinePrefixes) {
			return RiskStandard
		}
	}
	return RiskRoutine
}

// splitCommandChain splits on the shell list operators ;, newline, &&, || and a
// background &, keeping single | pipes inside a segment. Empty segments are dropped.
func splitCommandChain(cmd string) []string {
	var segs []string
	start := 0
	for i := 0; i < len(cmd); i++ {
		switch c := cmd[i]; {
		case c == ';' || c == '\n' || c == '&':
			segs = append(segs, cmd[start:i])
			if c == '&' && i+1 < len(cmd) && cmd[i+1] == '&' {
				i++
			}
			start = i + 1
		case c == '|' && i+1 < len(cmd) && cmd[i+1] == '|':
			segs = append(segs, cmd[start:i])
			i++
			start = i + 1
		}
	}
	segs = append(segs, cmd[start:])
	out := segs[:0]
	for _, s := range segs {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func isReadOnlyStage(s string) bool {
	return isSafeInvocation(s, readOnlyStagePrefixes)
}

// isSafeInvocation reports whether cmd starts with one of prefixes and carries
// none of that prefix's unsafe arguments.
func isSafeInvocation(cmd string, prefixes []string) bool {
	cmd = strings.TrimSpace(cmd)
	for _, p := range prefixes {
		if cmd == p || strings.HasPrefix(cmd, p+" ") || strings.HasPrefix(cmd, p+"\t") {
			for _, tok := range strings.Fields(cmd[len(p):]) {
				for _, f := range unsafeFlags[p] {
					if tok == f || strings.HasPrefix(tok, f+"=") {
						return false
					}
				}
			}
			return true
		}
	}
	return false
}

// ToolEvalInput describes a tool invocation requested by an agent runtime.
type ToolEvalInput struct {
	Category        string // "read", "file_write", "command", "mcp", "network"
	ToolName        string
	Command         string
	TargetPath      string
	EffectiveMode   identityv1.RuntimeMode
	GrantCeiling    identityv1.RuntimeMode // non-zero if thread is constrained by an external grant
	InteractionMode threadv2.InteractionMode
	Sandbox         SandboxScope
	AllowedRuleKeys map[string]bool
}

// ToolDecision is the outcome of policy evaluation.
type ToolDecision struct {
	Allow            bool
	NeedsApproval    bool
	DenyImmediately  bool
	OutsideWorkspace bool
	RuleKey          string
	Reason           string
}

// exactRuleCommands run arbitrary code from their arguments, so a thread rule
// for them must match the whole command line.
var exactRuleCommands = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true,
	"python": true, "python2": true, "python3": true, "node": true, "deno": true, "bun": true,
	"ruby": true, "perl": true, "php": true, "lua": true, "osascript": true, "pwsh": true, "powershell": true,
	"env": true, "xargs": true, "eval": true, "exec": true, "sudo": true, "doas": true, "su": true,
	"nohup": true, "timeout": true, "time": true, "nice": true, "command": true, "builtin": true,
}

// RuleKeyFor builds a stable approval rule key for "accept_for_thread".
func RuleKeyFor(category, toolName, command, resolvedPath string) string {
	switch category {
	case "command":
		fields := strings.Fields(strings.TrimSpace(command))
		// Interpreters, wrappers and chained/redirected commands get an exact-match key:
		// "python -c" or "sudo ls" as a two-word family would allow arbitrary code.
		if len(fields) > 0 && (exactRuleCommands[filepath.Base(fields[0])] ||
			len(splitCommandChain(command)) > 1 || strings.Contains(command, "|") || shellRedirOrSubshell.MatchString(command)) {
			return "cmd=" + strings.Join(fields, " ")
		}
		if len(fields) >= 2 {
			return "cmd:" + fields[0] + " " + fields[1]
		}
		if len(fields) == 1 {
			return "cmd:" + fields[0]
		}
		return "cmd:*"
	case "file_write":
		if resolvedPath != "" {
			return "dir:" + filepath.Dir(resolvedPath)
		}
		return "write:*"
	default:
		return "tool:" + category + ":" + toolName
	}
}

// EvaluateTool determines whether a tool call may run automatically, must pause
// for a durable RuntimeRequest approval, or must be rejected immediately (Plan mode).
func EvaluateTool(in ToolEvalInput) ToolDecision {
	if in.Category == "read" {
		if in.TargetPath != "" && in.Sandbox.WorkspaceRoot != "" {
			inside, _, resolved := in.Sandbox.CheckPath(in.TargetPath)
			if !inside && Rank(in.EffectiveMode) < Rank(identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS) {
				key := RuleKeyFor("read", in.ToolName, "", resolved)
				if in.AllowedRuleKeys[key] {
					return ToolDecision{Allow: true, RuleKey: key}
				}
				return ToolDecision{
					NeedsApproval:    true,
					OutsideWorkspace: true,
					RuleKey:          key,
					Reason:           fmt.Sprintf("Reading path outside workspace (%s)", resolved),
				}
			}
		}
		return ToolDecision{Allow: true}
	}

	// Plan mode forbids mutations and side-effecting commands until the plan is approved.
	if in.InteractionMode == threadv2.InteractionMode_INTERACTION_MODE_PLAN {
		if in.Category == "command" && ClassifyCommand(in.Command) == RiskRoutine && isReadOnlyStage(strings.TrimSpace(in.Command)) {
			return ToolDecision{Allow: true}
		}
		return ToolDecision{
			DenyImmediately: true,
			Reason:          "Thread is in Plan mode; file modifications and mutating commands are disabled until the plan is approved.",
		}
	}

	var inside, protected bool
	resolvedPath := in.TargetPath
	if in.TargetPath != "" && in.Sandbox.WorkspaceRoot != "" {
		inside, protected, resolvedPath = in.Sandbox.CheckPath(in.TargetPath)
	} else {
		inside = true
	}
	ruleKey := RuleKeyFor(in.Category, in.ToolName, in.Command, resolvedPath)
	if in.Category == "file_write" && protected {
		// A protected file is approved on its own; a directory rule never covers it.
		ruleKey = "file:" + resolvedPath
	}
	// A thread rule never pre-approves a high-risk command (e.g. "cmd:git push" -> "git push --force").
	ruleApplies := !(in.Category == "command" && ClassifyCommand(in.Command) == RiskHigh)
	if ruleApplies && in.AllowedRuleKeys[ruleKey] {
		return ToolDecision{Allow: true, RuleKey: ruleKey, OutsideWorkspace: !inside}
	}

	// Even in FULL_ACCESS, if a cryptographic Grant ceiling restricts workspace scope,
	// writes outside the workspace still pause for owner approval.
	mode := NormalizeRequested(in.EffectiveMode)
	if mode == identityv1.RuntimeMode_RUNTIME_MODE_FULL_ACCESS {
		if Rank(in.GrantCeiling) > 0 && in.TargetPath != "" && !inside {
			return ToolDecision{
				NeedsApproval:    true,
				OutsideWorkspace: true,
				RuleKey:          ruleKey,
				Reason:           "Grant restricts writes outside the project workspace",
			}
		}
		return ToolDecision{Allow: true, RuleKey: ruleKey, OutsideWorkspace: !inside}
	}

	switch in.Category {
	case "file_write":
		if !inside {
			return ToolDecision{
				NeedsApproval:    true,
				OutsideWorkspace: true,
				RuleKey:          ruleKey,
				Reason:           fmt.Sprintf("File write outside workspace (%s)", resolvedPath),
			}
		}
		if protected {
			return ToolDecision{
				NeedsApproval: true,
				RuleKey:       ruleKey,
				Reason:        fmt.Sprintf("Protected path requires approval (%s)", filepath.Base(resolvedPath)),
			}
		}
		if Rank(mode) >= Rank(identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS) {
			return ToolDecision{Allow: true, RuleKey: ruleKey}
		}
		return ToolDecision{
			NeedsApproval: true,
			RuleKey:       ruleKey,
			Reason:        "Supervised mode requires approval before editing files",
		}

	case "command":
		risk := ClassifyCommand(in.Command)
		if mode == identityv1.RuntimeMode_RUNTIME_MODE_AUTO && risk == RiskRoutine {
			return ToolDecision{Allow: true, RuleKey: ruleKey}
		}
		reason := "Command execution requires approval"
		if risk == RiskHigh {
			reason = "High-risk or destructive command requires explicit approval"
		}
		return ToolDecision{
			NeedsApproval: true,
			RuleKey:       ruleKey,
			Reason:        reason,
		}

	default:
		return ToolDecision{
			NeedsApproval: true,
			RuleKey:       ruleKey,
			Reason:        fmt.Sprintf("External %s tool %q requires approval", in.Category, in.ToolName),
		}
	}
}
