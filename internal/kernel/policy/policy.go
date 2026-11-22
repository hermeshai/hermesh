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
		if baseDir == "" {
			var err error
			target, err = filepath.Abs(target)
			if err != nil {
				return "", err
			}
		} else {
			target = filepath.Join(baseDir, target)
		}
	}
	target = filepath.Clean(target)
	return evalDeepestExisting(target)
}

func evalDeepestExisting(cleanPath string) (string, error) {
	cur := cleanPath
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return cleanPath, nil
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
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
	slash := filepath.ToSlash(target)
	base := strings.ToLower(filepath.Base(target))
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
		regexp.MustCompile(`(?i)\bgit\s+push\b.*(--force|-f\b)`),
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
	// Split chained commands on && or ; and require every segment to be routine.
	segments := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ';' || r == '\n'
	})
	for _, seg := range segments {
		for _, part := range strings.Split(seg, "&&") {
			if strings.Contains(part, "|") {
				// Allow simple pipes only if every stage is read-only (ls/rg/grep/head/tail/wc/sort).
				for _, stage := range strings.Split(part, "|") {
					if !isReadOnlyStage(strings.TrimSpace(stage)) {
						return RiskStandard
					}
				}
				continue
			}
			if !matchesPrefixList(strings.TrimSpace(part), routinePrefixes) {
				return RiskStandard
			}
		}
	}
	return RiskRoutine
}

func isReadOnlyStage(s string) bool {
	return matchesPrefixList(s, []string{"ls", "cat", "head", "tail", "wc", "rg", "grep", "find", "sort", "uniq", "git diff", "git log", "git status"})
}

func matchesPrefixList(cmd string, prefixes []string) bool {
	for _, p := range prefixes {
		if cmd == p || strings.HasPrefix(cmd, p+" ") || strings.HasPrefix(cmd, p+"\t") {
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

// RuleKeyFor builds a stable approval rule key for "accept_for_thread".
func RuleKeyFor(category, toolName, command, resolvedPath string) string {
	switch category {
	case "command":
		fields := strings.Fields(strings.TrimSpace(command))
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
	if in.AllowedRuleKeys[ruleKey] {
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
