package policy

import (
	"os"
	"path/filepath"
	"testing"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// Regression: destructive commands classified as routine -> auto-run in AUTO (the default mode)
// and even allowed in PLAN mode.
func TestDestructiveCommandsAreNotRoutine(t *testing.T) {
	cases := []string{
		"find . -delete",
		`find . -name '*.go' -exec rm -f {} \;`,
		"ls & rm -rf src",
		"rg --pre 'sh -c \"rm -rf src\"' x",
		"git branch -D main",
		"go test -exec 'rm -rf src' ./...",
	}
	for _, c := range cases {
		risk := ClassifyCommand(c)
		auto := EvaluateTool(ToolEvalInput{Category: "command", Command: c, EffectiveMode: identityv1.RuntimeMode_RUNTIME_MODE_AUTO})
		plan := EvaluateTool(ToolEvalInput{Category: "command", Command: c, EffectiveMode: identityv1.RuntimeMode_RUNTIME_MODE_AUTO,
			InteractionMode: sessionv1.InteractionMode_INTERACTION_MODE_PLAN})
		t.Logf("%-55q risk=%d AUTO.allow=%v PLAN.allow=%v", c, risk, auto.Allow, plan.Allow)
		if risk == RiskRoutine || auto.Allow {
			t.Errorf("%q classified RiskRoutine (auto-allowed in AUTO)", c)
		}
		if plan.Allow {
			t.Errorf("%q allowed in Plan mode", c)
		}
	}
	for _, c := range []string{
		"find . -name '*.go'", "rg -n foo", "git branch", "git branch --list", "ls && git status",
		"git log --oneline | head -5", "go test ./...", "go build ./...", "ls &", "grep -r x . || true",
	} {
		want := RiskRoutine
		if c == "grep -r x . || true" {
			want = RiskStandard // "true" is not on the routine list
		}
		if got := ClassifyCommand(c); got != want {
			t.Errorf("ClassifyCommand(%q) = %d, want %d", c, got, want)
		}
	}
	if ClassifyCommand("git push origin +main:main") != RiskHigh {
		t.Errorf("+refspec force push must be high risk")
	}
}

// Regression: ACCEPT_FOR_THREAD rule key is only the first two words, and the allow-rule
// check runs before the high-risk check.
func TestThreadRuleKeysDoNotOverreach(t *testing.T) {
	approved := RuleKeyFor("command", "Bash", "git push origin feature", "")
	rules := map[string]bool{approved: true}
	for _, c := range []string{"git push --force origin main", "git push -f origin main"} {
		d := EvaluateTool(ToolEvalInput{Category: "command", Command: c,
			EffectiveMode: identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED, AllowedRuleKeys: rules})
		t.Logf("approved key=%q; %q -> allow=%v", approved, c, d.Allow)
		if d.Allow {
			t.Errorf("high-risk %q auto-allowed by earlier approval of 'git push origin feature'", c)
		}
	}
	approved = RuleKeyFor("command", "Bash", "python -c 'print(1)'", "")
	d := EvaluateTool(ToolEvalInput{Category: "command", Command: "python -c 'import shutil;shutil.rmtree(\"/\")'",
		EffectiveMode: identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED, AllowedRuleKeys: map[string]bool{approved: true}})
	if d.Allow {
		t.Errorf("key %q auto-allows arbitrary python -c", approved)
	}
	// The npm install family rule still works for ordinary commands.
	approved = RuleKeyFor("command", "Bash", "npm install leftpad", "")
	d = EvaluateTool(ToolEvalInput{Category: "command", Command: "npm install rightpad",
		EffectiveMode: identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED, AllowedRuleKeys: map[string]bool{approved: true}})
	if !d.Allow {
		t.Errorf("rule %q should still allow npm install rightpad", approved)
	}
}

// Regression: approving one write in the workspace root "for thread" yields rule dir:<root>,
// which is checked before the protected-path check -> .env / .git/config become writable.
func TestDirRuleDoesNotCoverProtectedFiles(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	sb := SandboxScope{WorkspaceRoot: root}
	first := EvaluateTool(ToolEvalInput{Category: "file_write", TargetPath: filepath.Join(root, "README.md"),
		EffectiveMode: identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED, Sandbox: sb})
	rules := map[string]bool{first.RuleKey: true}
	envDec := EvaluateTool(ToolEvalInput{Category: "file_write", TargetPath: filepath.Join(root, ".env"),
		EffectiveMode: identityv1.RuntimeMode_RUNTIME_MODE_SUPERVISED, Sandbox: sb, AllowedRuleKeys: rules})
	t.Logf("rule=%q .env allow=%v", first.RuleKey, envDec.Allow)
	if envDec.Allow {
		t.Errorf("protected .env write auto-allowed by README approval rule %q", first.RuleKey)
	}
	// Same with .git: approving .git/description opens .git/config.
	_ = os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	first = EvaluateTool(ToolEvalInput{Category: "file_write", TargetPath: filepath.Join(root, ".git", "description"),
		EffectiveMode: identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS, Sandbox: sb})
	cfgDec := EvaluateTool(ToolEvalInput{Category: "file_write", TargetPath: filepath.Join(root, ".git", "config"),
		EffectiveMode: identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS, Sandbox: sb, AllowedRuleKeys: map[string]bool{"dir:" + filepath.Join(root, ".git"): true}})
	t.Logf(".git/description needsApproval=%v; .git/config allow with dir rule=%v", first.NeedsApproval, cfgDec.Allow)
	if cfgDec.Allow {
		t.Errorf(".git/config auto-allowed by dir:.git rule")
	}
}

// Regression: lexical Clean of "link/../x" before symlink resolution -> policy says inside,
// the OS write actually lands outside the workspace.
func TestSymlinkDotDotCannotEscapeWorkspace(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.MkdirAll(filepath.Join(outside, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "sub"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "link") + "/../pwned.txt" // not cleaned, as an agent would send it
	inside, _, resolved := SandboxScope{WorkspaceRoot: root}.CheckPath(target)
	dec := EvaluateTool(ToolEvalInput{Category: "file_write", TargetPath: target,
		EffectiveMode: identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS, Sandbox: SandboxScope{WorkspaceRoot: root}})
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, statErr := os.Stat(filepath.Join(outside, "pwned.txt"))
	t.Logf("policy inside=%v resolved=%s allow=%v; file landed outside=%v", inside, resolved, dec.Allow, statErr == nil)
	if inside && statErr == nil {
		t.Errorf("policy treats %s as inside workspace, but the write landed in %s", target, outside)
	}
}

// Regression: on case-insensitive filesystems (macOS default APFS), ".GIT/hooks" is the real
// hooks dir, but the protected check is case-sensitive.
func TestProtectedPathsAreCaseInsensitive(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".GIT", "HOOKS")); err != nil {
		t.Skip("filesystem is case-sensitive")
	}
	target := filepath.Join(root, ".GIT", "hooks", "pre-commit")
	_, protected, _ := SandboxScope{WorkspaceRoot: root}.CheckPath(target)
	dec := EvaluateTool(ToolEvalInput{Category: "file_write", TargetPath: target,
		EffectiveMode: identityv1.RuntimeMode_RUNTIME_MODE_AUTO_EDITS, Sandbox: SandboxScope{WorkspaceRoot: root}})
	_ = os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755)
	_, err := os.Stat(filepath.Join(root, ".git", "hooks", "pre-commit"))
	t.Logf("protected=%v allow=%v realHookWritten=%v", protected, dec.Allow, err == nil)
	if dec.Allow && err == nil {
		t.Errorf("write to .GIT/hooks/pre-commit auto-allowed and lands in .git/hooks")
	}
}
