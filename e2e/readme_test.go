package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestReadmeScreens takes the README's screenshots (docs/assets/readme): the
// desktop app in a demo world where Her runs on a Mac mini and looks after
// an open source project: her daily check finds a crash users report, she
// has it reproduced on a PC and asks the owner; on one click she has it
// fixed, checked on two systems, fixed again and checked again, and sends
// the release notes. In Chinese and in English, dark.
// HERMESH_README_OUT=<dir> runs it; the pictures land in <dir>/<lang>/. It
// needs her built (cargo build in her/).
//
//	HERMESH_README_OUT=$PWD/docs/assets/readme go test ./e2e -run TestReadmeScreens -count=1 -v -timeout 40m
func TestReadmeScreens(t *testing.T) {
	out := os.Getenv("HERMESH_README_OUT")
	if testing.Short() || out == "" {
		t.Skip("set HERMESH_README_OUT=<dir> to take the README's screenshots")
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not installed")
	}
	dir, _ := filepath.Abs(filepath.Join("apps", "desktop"))
	app := filepath.Join(t.TempDir(), "app")
	if r := run(t, []string{"HERMESH_DESKTOP_OUT=" + app}, 5*time.Minute, "pnpm", "--dir", dir, "build"); r.code != 0 {
		t.Fatalf("build: %+v", r)
	}
	langs := []string{"zh", "en"}
	if only := os.Getenv("HERMESH_README_LANG"); only != "" {
		langs = []string{only}
	}
	for _, lang := range langs {
		t.Run(lang, func(t *testing.T) {
			f := readmeWorld(t, lang)
			file := filepath.Join(t.TempDir(), "fixture.json")
			b, _ := json.Marshal(f)
			os.WriteFile(file, b, 0o644)
			env := []string{"HERMESH_E2E_FIXTURE=" + file, "HERMESH_DESKTOP_OUT=" + app, "HERMESH_PW_OUT=" + filepath.Join(t.TempDir(), "results"),
				"HERMESH_README_DEMO=" + lang, "HERMESH_SCREENS=" + filepath.Join(out, lang)}
			if r := run(t, env, 10*time.Minute, "pnpm", "--dir", dir, "exec", "playwright", "test", "readme.spec.ts"); r.code != 0 {
				t.Fatalf("playwright:\n%s\n%s", r.stdout, r.stderr)
			}
		})
	}
}

// readmeText is the README's demo in one language: what Her's model says to
// what the owner writes (her-mock's HER_MOCK_SCRIPTS), and what the coding
// agents do with what Her asks of them (HERMESH_FAKE_SCRIPTS). The spec
// (apps/desktop/e2e/readme.spec.ts) writes the same words.
type readmeText struct {
	her    map[string][]any
	agents map[string]string
}

// call is one tool call of a scripted reply.
func call(name string, arguments map[string]any) map[string]any {
	return map[string]any{"name": name, "arguments": arguments}
}

// readmeWorld is this Mac (macbook-pro), a Mac mini (mac-mini), a PC
// (windows-pc) and a server (linux-server), each with a clone of the open
// source project tidepool and scripted Claude Code, Codex and Gemini CLI, and
// Grok Bot connected. The Mac mini can run Her, with her-mock playing the
// demo's script as her model; Her is on for none yet. HerHome is the Mac
// mini's.
func readmeWorld(t *testing.T, lang string) desktopFixture {
	ctx := context.Background()
	text := readmeTexts[lang]
	write := func(name string, v any) string {
		file := filepath.Join(t.TempDir(), name)
		b, _ := json.Marshal(v)
		os.WriteFile(file, b, 0o644)
		return file
	}
	her, mockBin := herBinaries(t)
	p := start(t, "her-mock", []string{"HER_MOCK_SCRIPTS=" + write("her.json", text.her)}, mockBin, "127.0.0.1:0")
	eventually(t, 20*time.Second, "her-mock up", func() bool { return strings.Contains(p.output(), "cost = ") })
	_, models, _ := strings.Cut(p.output(), "\n")
	base := regexp.MustCompile(`base_url = "(http://[^/"]+)`).FindStringSubmatch(models)[1]
	home := herHome(t, models)

	agents := []string{"HERMESH_FAKE_PROVIDERS=1", "HERMESH_FAKE_AGENTS=gemini", "HERMESH_FAKE_SCRIPTS=" + write("agents.json", text.agents)}
	spec := &localv1.GrantSpec{Agents: []string{"claude", "codex", "gemini"}}
	w := newOwnerMac(t, agents, spec)
	st, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.app.client.RenameMachine(ctx, &localv1.RenameMachineRequest{MachineId: st.LocalMachineId, MachineName: "macbook-pro"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.app.client.RemoveProject(ctx, &localv1.RemoveProjectRequest{MachineId: st.LocalMachineId, ProjectId: "demo"}); err != nil {
		t.Fatal(err)
	}
	repo := func() string { return gitRepo(t, map[string]string{"README.md": "# tidepool\n"}) }
	if _, err := w.app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: st.LocalMachineId, ProjectId: "tidepool", Path: repo()}); err != nil {
		t.Fatal(err)
	}
	// The test's own agent is Grok Bot, connected and idle.
	clients, err := w.app.client.ListClients(ctx, &localv1.ListClientsRequest{})
	if err != nil || len(clients.Clients) != 1 {
		t.Fatalf("clients: %v %v", clients, err)
	}
	if _, err := w.app.client.RenameClient(ctx, &localv1.RenameClientRequest{GrantId: clients.Clients[0].GrantId, Label: "Grok Bot"}); err != nil {
		t.Fatal(err)
	}
	herVars := herEnv(her, herConsole(t), home, base)
	if lang == "zh" {
		herVars = append(herVars, "HER_LANG=zh")
	}
	for _, name := range []string{"mac-mini", "windows-pc", "linux-server"} {
		env := agents
		if name == "mac-mini" {
			env = append(herVars, agents...)
		}
		mac, _ := w.otherMac(name, env)
		daemon := w.logs[len(w.logs)-1]
		t.Cleanup(daemon.stop) // stopped as a service is, so that it stops her
		joined := w.joinTyped(mac, st, name)
		if _, err := mac.AddProject(ctx, &localv1.AddProjectRequest{MachineId: joined.LocalMachineId, ProjectId: "tidepool", Path: repo()}); err != nil {
			t.Fatal(err)
		}
	}
	return desktopFixture{Socket: w.sock, Sessions: map[string]string{}, CLI: filepath.Join(w.bin, "hermesh"), CLIEnv: w.cliEnv, HerHome: home, owner: w}
}

// herStarts is Her's call that starts a coding agent.
func herStarts(project, agent, title, prompt string) map[string]any {
	return call("call_tool", map[string]any{"name": "session_start", "arguments": map[string]any{"project": project, "agent": agent, "title": title, "prompt": prompt}})
}

// herSays is Her's message to the owner; last ends her turn.
func herSays(text string, last bool) map[string]any {
	return call("send_to_user", map[string]any{"text": text, "end_turn": last})
}

// herTells is Her's message to a session she started, named by its title.
func herTells(title, message string) map[string]any {
	return call("call_tool", map[string]any{"name": "session_send", "arguments": map[string]any{"session_id": "{{session:" + title + "}}", "message": message}})
}

// wrote is a coding agent's file of n lines.
func wrote(path string, n int) string {
	return "write " + path + " " + strings.Repeat(`// …\n`, n-1) + "// …"
}

// The demo: Her looks after tidepool, an open source project. Her script is
// keyed by words of what wakes her: the owner's, her daily check's report,
// a session's result or failure, the owner's answer. Each ends with a line
// of her own that nobody sees, for a wake that brings nothing new.
var readmeTexts = map[string]readmeText{
	"zh": {
		her: map[string][]any{
			"每天早上巡检一次": {
				[]any{call("manage_routine", map[string]any{"action": "create", "name": "每日巡检", "schedule": "0 9 * * *", "when": "每天 9:00",
					"prompt": "巡检 tidepool 的 GitHub 和 X：新 issue、新 PR、提到项目的帖子。"})},
				[]any{herSays("好，每天 9:00 巡检一次。有事我来找你。", true)},
				"已转告。",
			},
			"巡检 tidepool 的 GitHub 和 X": {
				"GitHub：3 个新 issue 报的像是一回事，Windows 上启动就崩溃（#218、#220、#221）。1 个新 PR（#217，改文档）。\nX：9 条帖子说 tidepool 在 Windows 上打不开。",
			},
			"像是一回事": {
				[]any{call("tool_search", map[string]any{"server": "mesh"})},
				[]any{herSays("今天巡检有一件事：Windows 上有人一启动就崩溃。我先让 Windows 电脑上的 Codex 复现一下。", false),
					herStarts("windows-pc/tidepool", "codex", "复现 #218", "在 Windows 上复现 issue #218：启动崩溃。只复现和定位，不要修。")},
				"已派出。",
			},
			"复现成功": {
				[]any{call("send_to_user", map[string]any{"type": "question",
					"text":    "复现了：读配置时路径分隔符写死了。GitHub 上 #218、#220、#221 三个 issue，还有 X 上 9 个人，说的都是它。现在修吗？",
					"options": []any{map[string]any{"label": "现在修"}, map[string]any{"label": "先不修"}}})},
				"已转告。",
			},
			"现在修": {
				[]any{herSays("好，交给 Mac mini 上的 Claude Code。", false),
					herStarts("mac-mini/tidepool", "claude", "修复 #218", "修复 issue #218：Windows 上启动崩溃，原因是读配置时路径分隔符写死成了 /。补测试。")},
				"已派出。",
			},
			"第一版修好了": {
				[]any{herSays("修了一版。Windows 和 Linux 上同时验证。", false),
					herStarts("windows-pc/tidepool", "codex", "Windows 验证", "在 Windows 上验证 #218 的修复分支：启动、读配置、跑全部测试。"),
					herStarts("linux-server/tidepool", "gemini", "Linux 验证", "在 Linux 上验证 #218 的修复分支：启动、读配置、跑全部测试。")},
				"已派出。",
			},
			"Linux 上通过": {"等 Windows。"},
			"盘符开头的路径": {
				[]any{herSays("Windows 上还是崩：配置放在 D 盘时出错。我把日志转给 Claude 了。", false),
					herTells("修复 #218", "Windows 验证没过：盘符开头的路径被当成了相对路径，配置在 D 盘时仍然崩溃。请接着修。")},
				"已派出。",
			},
			"第二版修好了": {
				[]any{herSays("又修了一版。Windows 上再验一次。", false),
					herTells("Windows 验证", "修复分支更新了，请在 Windows 上再验证一次。")},
				"已派出。",
			},
			"Windows 上通过": {
				[]any{call("send_to_user", map[string]any{"type": "attachment", "end_turn": true, "files": []any{map[string]any{"path": "发布说明.md"}},
					"text": "修好了，PR 是 #222，Windows、macOS、Linux 都验证过了。\n\n- #218、#220、#221 下我都回复了，说了原因和修复所在的 PR\n- 发布说明写好了，在附件里\n- 给 X 上那 9 个人的回复也写好了，等你点头再发\n\nPR 我没有合并，等你看。"})},
				"已转告。",
			},
		},
		agents: map[string]string{
			"在 Windows 上复现 issue #218：启动崩溃。只复现和定位，不要修。": lines(
				`sleep 5s`,
				`say 我照 issue 里的步骤跑一遍。`,
				`run tidepool.exe start|panic: open C:/Users/alex/AppData/Roaming/tidepool/config.toml: The system cannot find the path specified.\n\ngoroutine 1 [running]:\nmain.loadConfig(...)\n	internal/config/path.go:31`,
				`run rg -n '"/"' internal/config|internal/config/path.go:27:	return dir + "/" + name`,
				`tokens 18400 920`,
				`say 复现成功。原因在 internal/config/path.go 第 27 行：配置路径用 "/" 手工拼接，在 Windows 上拼出来的路径不存在，启动时直接 panic。macOS 和 Linux 不受影响。`),
			"修复 issue #218：Windows 上启动崩溃，原因是读配置时路径分隔符写死成了 /。补测试。": lines(
				`sleep 6s`,
				wrote("internal/config/path.go", 48),
				wrote("internal/config/path_test.go", 36),
				`run go test ./...|ok  	tidepool/internal/config	0.214s\nok  	tidepool/cmd/tidepool	0.482s`,
				`tokens 36200 2400`,
				`say 第一版修好了：路径改用 filepath.Join 拼接，并补了测试。这台 Mac 上 41 个测试全部通过，还没有在 Windows 上跑过。`),
			"在 Windows 上验证 #218 的修复分支：启动、读配置、跑全部测试。": lines(
				`sleep 12s`,
				`run go test ./...|ok  	tidepool/internal/config	0.391s\nok  	tidepool/cmd/tidepool	0.840s`,
				`run tidepool.exe start --config D:/work/tidepool.toml|panic: open C:/Users/alex/D:/work/tidepool.toml: The filename, directory name, or volume label syntax is incorrect.`,
				`tokens 22800 1100`,
				`fail 默认位置的配置能启动了，但配置放在 D 盘时仍然崩溃：盘符开头的路径被当成了相对路径。`),
			"在 Linux 上验证 #218 的修复分支：启动、读配置、跑全部测试。": lines(
				`sleep 7s`,
				`run go test ./...|ok  	tidepool/internal/config	0.188s\nok  	tidepool/cmd/tidepool	0.402s`,
				`run ./tidepool start|tidepool 0.9.3 listening on :7070`,
				`tokens 19600 800`,
				`say Linux 上通过：41 个测试全过，启动正常。`),
			"Windows 验证没过：盘符开头的路径被当成了相对路径，配置在 D 盘时仍然崩溃。请接着修。": lines(
				`sleep 6s`,
				wrote("internal/config/path.go", 57),
				wrote("internal/config/path_windows_test.go", 44),
				`run go test ./...|ok  	tidepool/internal/config	0.231s\nok  	tidepool/cmd/tidepool	0.479s`,
				`run gh pr create --fill|https://github.com/acme/tidepool/pull/222`,
				`tokens 28900 1900`,
				`say 第二版修好了：盘符路径和 UNC 路径都按绝对路径处理，并补了一个只在 Windows 上跑的测试。PR 是 #222。`),
			"修复分支更新了，请在 Windows 上再验证一次。": lines(
				`sleep 7s`,
				`run go test ./...|ok  	tidepool/internal/config	0.402s\nok  	tidepool/cmd/tidepool	0.851s`,
				`run tidepool.exe start --config D:/work/tidepool.toml|tidepool 0.9.3 listening on :7070`,
				`tokens 20100 760`,
				`say Windows 上通过：42 个测试全过，配置在 C 盘和 D 盘都能正常启动。`),
		},
	},
	"en": {
		her: map[string][]any{
			"check them every morning": {
				[]any{call("manage_routine", map[string]any{"action": "create", "name": "Daily check", "schedule": "0 9 * * *", "when": "Every day at 9:00 AM",
					"prompt": "Go through tidepool's GitHub and X: new issues, new PRs, posts that mention the project."})},
				[]any{herSays("Will do: every morning at 9. I'll come to you when there's something.", true)},
				"Passed on.",
			},
			"Go through tidepool's GitHub and X": {
				"GitHub: 3 new issues that look like one problem, a crash at startup on Windows (#218, #220, #221). 1 new PR (#217, docs).\nX: 9 posts say tidepool won't open on Windows.",
			},
			"look like one problem": {
				[]any{call("tool_search", map[string]any{"server": "mesh"})},
				[]any{herSays("One thing from today's check: people on Windows crash at startup. I'm having Codex on the Windows PC reproduce it first.", false),
					herStarts("windows-pc/tidepool", "codex", "Reproduce #218", "Reproduce issue #218 on Windows: the crash at startup. Reproduce and locate it only; fix nothing.")},
				"Started.",
			},
			"Reproduced it": {
				[]any{call("send_to_user", map[string]any{"type": "question",
					"text":    "Reproduced: the config path has a hard-coded separator. Issues #218, #220 and #221, and 9 people on X, are all about it. Fix it now?",
					"options": []any{map[string]any{"label": "Fix it now"}, map[string]any{"label": "Not now"}}})},
				"Passed on.",
			},
			"Fix it now": {
				[]any{herSays("On it: Claude Code on the Mac mini takes the fix.", false),
					herStarts("mac-mini/tidepool", "claude", "Fix #218", "Fix issue #218: the crash at startup on Windows, from a config path joined with a hard-coded /. Add tests.")},
				"Started.",
			},
			"A first fix is in": {
				[]any{herSays("A first fix is in. Checking it on Windows and Linux at once.", false),
					herStarts("windows-pc/tidepool", "codex", "Check on Windows", "Check the fix branch for #218 on Windows: start it, read the config, run every test."),
					herStarts("linux-server/tidepool", "gemini", "Check on Linux", "Check the fix branch for #218 on Linux: start it, read the config, run every test.")},
				"Started.",
			},
			"Passes on Linux": {"Waiting for Windows."},
			"taken for a relative one": {
				[]any{herSays("Windows still crashes, with the config on drive D. I passed the log to Claude.", false),
					herTells("Fix #218", "The Windows check failed: a path that starts with a drive letter is taken for a relative one, so a config on drive D still crashes. Please go on.")},
				"Started.",
			},
			"A second fix is in": {
				[]any{herSays("A second fix is in. Checking on Windows again.", false),
					herTells("Check on Windows", "The fix branch has changed. Please check it on Windows once more.")},
				"Started.",
			},
			"Passes on Windows": {
				[]any{call("send_to_user", map[string]any{"type": "attachment", "end_turn": true, "files": []any{map[string]any{"path": "release-notes.md"}},
					"text": "Fixed, in PR #222, and checked on Windows, macOS and Linux.\n\n- I answered #218, #220 and #221 with the cause and the PR that fixes it\n- The release notes are written: see the file\n- Replies to the 9 people on X are written too, and wait for your go-ahead\n\nI haven't merged the PR. That's yours."})},
				"Passed on.",
			},
		},
		agents: map[string]string{
			"Reproduce issue #218 on Windows: the crash at startup. Reproduce and locate it only; fix nothing.": lines(
				`sleep 5s`,
				`say Following the steps in the issue.`,
				`run tidepool.exe start|panic: open C:/Users/alex/AppData/Roaming/tidepool/config.toml: The system cannot find the path specified.\n\ngoroutine 1 [running]:\nmain.loadConfig(...)\n	internal/config/path.go:31`,
				`run rg -n '"/"' internal/config|internal/config/path.go:27:	return dir + "/" + name`,
				`tokens 18400 920`,
				`say Reproduced it. The cause is line 27 of internal/config/path.go: the config path is joined by hand with "/", which gives a path that doesn't exist on Windows, and startup panics. macOS and Linux are not affected.`),
			"Fix issue #218: the crash at startup on Windows, from a config path joined with a hard-coded /. Add tests.": lines(
				`sleep 6s`,
				wrote("internal/config/path.go", 48),
				wrote("internal/config/path_test.go", 36),
				`run go test ./...|ok  	tidepool/internal/config	0.214s\nok  	tidepool/cmd/tidepool	0.482s`,
				`tokens 36200 2400`,
				`say A first fix is in: the path is joined with filepath.Join, with tests. All 41 tests pass on this Mac; it hasn't run on Windows yet.`),
			"Check the fix branch for #218 on Windows: start it, read the config, run every test.": lines(
				`sleep 12s`,
				`run go test ./...|ok  	tidepool/internal/config	0.391s\nok  	tidepool/cmd/tidepool	0.840s`,
				`run tidepool.exe start --config D:/work/tidepool.toml|panic: open C:/Users/alex/D:/work/tidepool.toml: The filename, directory name, or volume label syntax is incorrect.`,
				`tokens 22800 1100`,
				`fail It starts with the config in its default place, but still crashes with one on drive D: a path that starts with a drive letter is taken for a relative one.`),
			"Check the fix branch for #218 on Linux: start it, read the config, run every test.": lines(
				`sleep 7s`,
				`run go test ./...|ok  	tidepool/internal/config	0.188s\nok  	tidepool/cmd/tidepool	0.402s`,
				`run ./tidepool start|tidepool 0.9.3 listening on :7070`,
				`tokens 19600 800`,
				`say Passes on Linux: all 41 tests, and it starts.`),
			"The Windows check failed: a path that starts with a drive letter is taken for a relative one, so a config on drive D still crashes. Please go on.": lines(
				`sleep 6s`,
				wrote("internal/config/path.go", 57),
				wrote("internal/config/path_windows_test.go", 44),
				`run go test ./...|ok  	tidepool/internal/config	0.231s\nok  	tidepool/cmd/tidepool	0.479s`,
				`run gh pr create --fill|https://github.com/acme/tidepool/pull/222`,
				`tokens 28900 1900`,
				`say A second fix is in: drive-letter and UNC paths count as absolute, with a test that runs on Windows only. The PR is #222.`),
			"The fix branch has changed. Please check it on Windows once more.": lines(
				`sleep 7s`,
				`run go test ./...|ok  	tidepool/internal/config	0.402s\nok  	tidepool/cmd/tidepool	0.851s`,
				`run tidepool.exe start --config D:/work/tidepool.toml|tidepool 0.9.3 listening on :7070`,
				`tokens 20100 760`,
				`say Passes on Windows: all 42 tests, and it starts with the config on drive C and on drive D.`),
		},
	},
}
