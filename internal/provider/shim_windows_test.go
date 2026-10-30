package provider

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// npm's shims give way to the program they start; other batch files are
// refused.
func TestStartable(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	native := write("claude.cmd", "@ECHO off\r\nGOTO start\r\n:find_dp0\r\nSET dp0=%~dp0\r\nEXIT /b\r\n:start\r\nSETLOCAL\r\nCALL :find_dp0\r\n\"%dp0%\\node_modules\\@anthropic-ai\\claude-code\\bin\\claude.exe\"   %*\r\n")
	script := write("codex.cmd", "@ECHO off\r\n:start\r\nSETLOCAL\r\nCALL :find_dp0\r\n\r\nIF EXIST \"%dp0%\\node.exe\" (\r\n  SET \"_prog=%dp0%\\node.exe\"\r\n) ELSE (\r\n  SET \"_prog=node\"\r\n)\r\n\r\nendLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & \"%_prog%\"  \"%dp0%\\node_modules\\@openai\\codex\\bin\\codex.js\" %*\r\n")
	node := write("node.exe", "")
	other := write("tool.bat", "@echo %*\r\n")

	bin, args, err := startable(native, []string{"-p"})
	if want := filepath.Join(dir, `node_modules\@anthropic-ai\claude-code\bin\claude.exe`); err != nil || bin != want || !slices.Equal(args, []string{"-p"}) {
		t.Fatalf("native: %s %q %v, want %s", bin, args, err, want)
	}
	bin, args, err = startable(script, []string{"app-server"})
	if want := []string{filepath.Join(dir, `node_modules\@openai\codex\bin\codex.js`), "app-server"}; err != nil || bin != node || !slices.Equal(args, want) {
		t.Fatalf("script: %s %q %v", bin, args, err)
	}
	if _, _, err := startable(other, nil); err == nil {
		t.Fatal("ran a batch file")
	}
	if got := executable(filepath.Join(dir, "claude")); got != native {
		t.Fatalf("executable: %q", got)
	}
}
