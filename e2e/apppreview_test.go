package e2e

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestAppPreview sets up sessions in every state the app's sessions page
// shows, with this computer's real Claude Code and Codex, then keeps them
// up for HERMESH_APP_PREVIEW seconds so a dev build of the desktop app can
// be looked at against them:
//
//	HERMESH_APP_PREVIEW=900 go test ./e2e -run TestAppPreview -v -timeout 30m
//	HERMESH_SOCKET=<socket it prints> pnpm --dir apps/desktop dev
func TestAppPreview(t *testing.T) {
	secs, _ := strconv.Atoi(os.Getenv("HERMESH_APP_PREVIEW"))
	if secs <= 0 {
		t.Skip("set HERMESH_APP_PREVIEW=<seconds>")
	}
	env := []string{"PATH=" + os.Getenv("PATH") + ":/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin"}
	w := newOwnerMac(t, env, &localv1.GrantSpec{Agents: []string{"claude", "codex"}})
	os.WriteFile(filepath.Join(w.repo, "calc.js"), []byte("function add(a, b) {\n  return a - b\n}\n\nmodule.exports = { add }\n"), 0o644)
	gitOut(t, w.repo, "add", ".")
	gitOut(t, w.repo, "commit", "-qm", "calc")

	session := func(code int, args ...string) {
		t.Helper()
		r := w.cli(8*time.Minute, append([]string{"session", "start", "--project", "demo"}, args...)...)
		if r.code != code {
			t.Fatalf("start %v: %+v", args, r)
		}
	}
	haiku := []string{"--agent", "claude", "--model", "haiku"}
	session(0, append(haiku, "--wait", "--prompt", "修复 calc.js 里 add 函数的 bug（现在返回的是两数之差），再写一个 calc.test.js 用 node 的 assert 验证，然后用 node 运行它。")...)
	session(0, "--agent", "codex", "--wait", "--title", "说明项目用途", "--prompt", "读一下 README.md 和 calc.js，用两三句话说明这个项目是做什么的。不要修改任何文件。")
	session(8, append(haiku, "--wait", "--prompt", "Ask me which color I prefer, Red or Blue, with your tool for asking the user a question, and wait for my answer. Then reply with only the chosen color.")...)
	session(0, append(haiku, "--prompt", "用 shell 运行 `sleep 900`，结束后回复 done。")...)
	r := w.cli(8*time.Minute, append([]string{"session", "start", "--project", "demo", "--wait", "-o", "json"}, append(haiku, "--prompt", "Reply with just: hello")...)...)
	if id := firstSession(r.stdout).SessionID; id == "" {
		t.Fatalf("hello: %+v", r)
	} else if a := w.cli(30*time.Second, "session", "archive", id); a.code != 0 {
		t.Fatalf("archive: %+v", a)
	}

	t.Logf("sessions ready; open the app with:\nHERMESH_SOCKET=%s pnpm --dir apps/desktop dev", w.sock)
	time.Sleep(time.Duration(secs) * time.Second)
	if t.Failed() || strings.Contains(w.owner.output(), "panic") {
		t.Log(w.owner.output())
	}
}
