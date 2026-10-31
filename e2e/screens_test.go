package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestScreens takes the screenshots of the website's download page
// (hermesh-cloud web/console/public/download): the desktop app in a demo
// world, in Chinese and in English, dark. HERMESH_SCREENS_OUT=<dir> runs it;
// the pictures land in <dir>/<lang>/.
func TestScreens(t *testing.T) {
	out := os.Getenv("HERMESH_SCREENS_OUT")
	if testing.Short() || out == "" {
		t.Skip("set HERMESH_SCREENS_OUT=<dir> to take the download page's screenshots")
	}
	dir, _ := filepath.Abs(filepath.Join("apps", "desktop"))
	app := filepath.Join(t.TempDir(), "app")
	if r := run(t, []string{"HERMESH_DESKTOP_OUT=" + app}, 5*time.Minute, "pnpm", "--dir", dir, "build"); r.code != 0 {
		t.Fatalf("build: %+v", r)
	}
	for _, lang := range []string{"zh", "en"} {
		t.Run(lang, func(t *testing.T) {
			f := demoWorld(t, demoScripts[lang])
			file := filepath.Join(t.TempDir(), "fixture.json")
			b, _ := json.Marshal(f)
			os.WriteFile(file, b, 0o644)
			env := []string{"HERMESH_E2E_FIXTURE=" + file, "HERMESH_DESKTOP_OUT=" + app, "HERMESH_PW_OUT=" + filepath.Join(t.TempDir(), "results"),
				"HERMESH_SCREENS_DEMO=" + lang, "HERMESH_SCREENS=" + filepath.Join(out, lang)}
			if r := run(t, env, 10*time.Minute, "pnpm", "--dir", dir, "exec", "playwright", "test", "screens.spec.ts"); r.code != 0 {
				t.Fatalf("playwright:\n%s\n%s", r.stdout, r.stderr)
			}
		})
	}
}

// demoText is a demo world's words in one language.
type demoText struct {
	sessions []demoSession
}

// demoSession is a session an agent starts: its title, who (an agent's
// name), where, with which coding agent, what the agent asks, and the
// script the scripted provider plays for it (internal/provider/fake,
// HERMESH_FAKE_SCRIPTS). wait: it ends before the next starts.
type demoSession struct {
	title, prompt, by, project, agent, script string
	wait                                      bool
}

// demoWorld is a Mac (MacBook Pro) with two projects and a Mac mini with
// one (mac-mini), Grok Bot and Muse connected, and sessions they started with Claude
// Code and Codex: done, running and waiting for an answer.
func demoWorld(t *testing.T, text demoText) desktopFixture {
	ctx := context.Background()
	scripts := map[string]string{}
	for _, s := range text.sessions {
		scripts[s.prompt] = s.script
	}
	file := filepath.Join(t.TempDir(), "scripts.json")
	b, _ := json.Marshal(scripts)
	os.WriteFile(file, b, 0o644)
	env := []string{"HERMESH_FAKE_PROVIDERS=1", "HERMESH_FAKE_SCRIPTS=" + file}
	grant := &localv1.GrantSpec{Agents: []string{"claude", "codex"}}
	w := newOwnerMac(t, env, grant)
	home, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.app.client.RenameMachine(ctx, &localv1.RenameMachineRequest{MachineId: home.LocalMachineId, MachineName: "MacBook Pro"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.app.client.RemoveProject(ctx, &localv1.RemoveProjectRequest{MachineId: home.LocalMachineId, ProjectId: "demo"}); err != nil {
		t.Fatal(err)
	}
	for id, files := range map[string]map[string]string{
		"web-app":      {"README.md": "# web-app\n", "package.json": "{\"name\": \"web-app\"}\n"},
		"payments-api": {"README.md": "# payments-api\n", "go.mod": "module acme/payments\n"},
	} {
		if _, err := w.app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: home.LocalMachineId, ProjectId: id, Path: gitRepo(t, files)}); err != nil {
			t.Fatal(err)
		}
	}
	mini, _ := w.otherMac("mac-mini", env)
	st := w.joinTyped(mini, home, "mac-mini")
	if _, err := mini.AddProject(ctx, &localv1.AddProjectRequest{MachineId: st.LocalMachineId, ProjectId: "ios-app", Path: gitRepo(t, map[string]string{"README.md": "# ios-app\n"})}); err != nil {
		t.Fatal(err)
	}

	// The test's agent is Grok Bot; Muse joins it.
	clients, err := w.app.client.ListClients(ctx, &localv1.ListClientsRequest{})
	if err != nil || len(clients.Clients) != 1 {
		t.Fatalf("clients: %v %v", clients, err)
	}
	if _, err := w.app.client.RenameClient(ctx, &localv1.RenameClientRequest{GrantId: clients.Clients[0].GrantId, Label: "Grok Bot"}); err != nil {
		t.Fatal(err)
	}
	agents := map[string][]string{"Grok Bot": w.cliEnv, "Muse": w.addAgent("Muse", grant)}

	f := desktopFixture{Socket: w.sock, Sessions: map[string]string{}, CLI: filepath.Join(w.bin, "hermesh"), CLIEnv: w.cliEnv, owner: w}
	by := map[string][]string{} // each session's agent, who sees it
	for _, s := range text.sessions {
		args := []string{"session", "start", "-o", "json", "--project", s.project, "--agent", s.agent, "--title", s.title, "--prompt", s.prompt}
		if s.wait {
			args = append(args, "--wait")
		}
		var id string
		eventually(t, 2*time.Minute, "session "+s.title, func() bool {
			id = firstSession(run(t, agents[s.by], 2*time.Minute, f.CLI, args...).stdout).SessionID
			return id != ""
		})
		f.Sessions[s.title] = id
		by[id] = agents[s.by]
	}
	for title, id := range f.Sessions {
		var s sessionJSON
		for deadline := time.Now().Add(time.Minute); ; time.Sleep(300 * time.Millisecond) {
			if s = firstSession(run(t, by[id], 30*time.Second, f.CLI, "session", "get", id, "-o", "json").stdout); s.Status == "idle" || s.Status == "running" || s.Status == "waiting_input" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("session %s: %+v\n%s", title, s, run(t, by[id], 30*time.Second, f.CLI, "session", "events", id).stdout)
			}
		}
	}
	return f
}

// demoScripts are the demo world's sessions, in each language.
var demoScripts = map[string]demoText{
	"zh": {sessions: []demoSession{
		{title: "每周依赖安全检查", prompt: "每周例行：检查 payments-api 的依赖有没有已知漏洞，能升级的也列一下。", by: "Grok Bot", project: "payments-api", agent: "codex", wait: true, script: lines(
			`say 我来跑一遍依赖审计。`,
			`run govulncheck ./...|No vulnerabilities found.`,
			`run go list -m -u all|acme/payments\ngithub.com/stripe/stripe-go/v82 v82.1.0 [v82.3.0]\ngolang.org/x/crypto v0.43.0 [v0.44.0]`,
			`tokens 21840 960`,
			`say 没有发现漏洞。有 2 个依赖可以升级（stripe-go 82.3、x/crypto 0.44），都是小版本，需要的话我来升。`)},
		{title: "写 v2.3 的发布说明", prompt: "根据 v2.2 以来合并的 PR，写一下 v2.3 的发布说明，放进 CHANGELOG.md。", by: "Muse", project: "web-app", agent: "claude", wait: true, script: lines(
			`say 我先看一下 v2.2 以来合并的 PR。`,
			`run gh pr list --state merged --search "merged:>2026-10-17"|#141  结账页支持 Apple Pay\n#138  订单列表按日期筛选\n#136  修复移动端菜单遮挡`,
			`write CHANGELOG.md ## v2.3\n\n- 结账页支持 Apple Pay\n- 订单列表可以按日期筛选\n- 修复：移动端菜单不再遮挡内容`,
			`tokens 30420 1880`,
			`say 写好了，放在 CHANGELOG.md 的最上面：三条更新，按用户能感受到的顺序排。`)},
		{title: "审查 PR #88：结账改用新支付接口", prompt: "#88 改了结账流程，帮我仔细审一遍，有问题直接评论在 PR 上。", by: "Muse", project: "payments-api", agent: "codex", wait: true, script: lines(
			`say 先看 PR #88 改了什么。`,
			`run gh pr diff 88 --stat| src/checkout/total.go    | 42 ++++++++++------\n src/checkout/payment.go  | 67 ++++++++++++++++++------\n src/checkout/stock.go    | 18 ++++++--\n 3 files changed, 98 insertions(+), 29 deletions(-)`,
			`run go test ./...|ok  	acme/payments/checkout	0.412s\nok  	acme/payments/stock	0.188s`,
			`run gh pr review 88 --comment --body-file review.md|Reviewed pull request #88`,
			`tokens 61500 2480`,
			`say 审完了，有 2 个问题，都已评论在 PR 上：\n\n1. **合计会少一分钱**：total.go 先对每件商品四舍五入再求和，多件商品时总价可能少 1 分。建议先求和再取整。\n2. **支付失败不回补库存**：payment.go 的失败分支没有调用 stock.Release，库存会一直被锁住。\n\n其余改动没问题，测试全部通过。`)},
		{title: "修复登录按钮点击无反应 #142", prompt: "有人报了 #142：点「登录」没反应。帮我修一下，修好后开个 PR。", by: "Grok Bot", project: "web-app", agent: "claude", wait: true, script: lines(
			`say 我先读一下 issue #142 和登录表单。`,
			`run gh issue view 142|#142 点击「登录」没有任何反应\n输入邮箱后直接点「登录」，页面没有反应，控制台也没有报错。`,
			`run rg -n "onSubmit" src/login|src/login/form.tsx:18:  const onSubmit = async (e: FormEvent) => {`,
			`write src/login/form.tsx const onSubmit = async (e: FormEvent) => {\n  e.preventDefault();\n  if (!isEmail(email)) {\n    setError("请输入正确的邮箱");\n    return;\n  }\n  await signIn(email);\n};`,
			`run pnpm test src/login| ✓ src/login/form.test.tsx (6)\n ✓ src/login/session.test.ts (18)\n\n Test Files  2 passed (2)\n      Tests  24 passed (24)`,
			`run gh pr create --fill|https://github.com/acme/web-app/pull/143`,
			`tokens 48210 3120`,
			`say 修好了。原因是提交前没有校验邮箱：格式不对时 onSubmit 直接返回，也没有任何提示。\n\n- 提交前先校验邮箱，不对就在输入框下面提示\n- 补了测试：空邮箱和格式错误都会提示\n- 24 个测试全部通过\n\nPR 在这里：#143`)},
		{title: "升级到 React 19", prompt: "把 web-app 升级到 React 19，再跑一遍测试。", by: "Muse", project: "web-app", agent: "claude", script: lines(
			`say 我先看看哪些依赖还不支持 React 19。`,
			`run pnpm outdated react react-dom|react      18.3.1  19.2.0\nreact-dom  18.3.1  19.2.0`,
			`run pnpm why react --depth 1|@acme/charts 3.1.0 (peer react@^18)\nreact-beautiful-dnd 13.1.1 (peer react@^16 || ^17 || ^18)\nreact-helmet 6.1.0 (peer react@>=16)`,
			`tokens 18800 740`,
			`ask 升级方式|有 3 个依赖还没声明支持 React 19，怎么处理？|先升级 React（强制安装）,等它们都支持了再升`)},
		{title: "给设置页加深色模式", prompt: "给 iOS app 的设置页加上深色模式，记得跑 UI 测试。", by: "Grok Bot", project: "mac-mini/ios-app", agent: "claude", script: lines(
			`say 我先看看现在的颜色是在哪里定义的。`,
			`run rg -l "Color(" Sources/Settings|Sources/Settings/SettingsView.swift\nSources/Settings/Theme.swift`,
			`write Sources/Settings/Theme.swift extension Color {\n  static let card = Color("Card")\n  static let ink = Color("Ink")\n}`,
			`say 颜色已经改成资源目录里的动态颜色，正在跑 UI 测试。`,
			`tool-sleep 3h`)},
	}},
	"en": {sessions: []demoSession{
		{title: "Weekly dependency audit", prompt: "Weekly check: any known vulnerabilities in payments-api's dependencies? List the ones that can be upgraded too.", by: "Grok Bot", project: "payments-api", agent: "codex", wait: true, script: lines(
			`say Running the dependency audit.`,
			`run govulncheck ./...|No vulnerabilities found.`,
			`run go list -m -u all|acme/payments\ngithub.com/stripe/stripe-go/v82 v82.1.0 [v82.3.0]\ngolang.org/x/crypto v0.43.0 [v0.44.0]`,
			`tokens 21840 960`,
			`say No vulnerabilities. Two dependencies have minor updates (stripe-go 82.3, x/crypto 0.44); I can upgrade them if you like.`)},
		{title: "Release notes for v2.3", prompt: "Write the v2.3 release notes from the PRs merged since v2.2, at the top of CHANGELOG.md.", by: "Muse", project: "web-app", agent: "claude", wait: true, script: lines(
			`say Looking at the PRs merged since v2.2.`,
			`run gh pr list --state merged --search "merged:>2026-10-17"|#141  Apple Pay at checkout\n#138  Filter orders by date\n#136  Fix the mobile menu covering content`,
			`write CHANGELOG.md ## v2.3\n\n- Pay with Apple Pay at checkout\n- Filter the order list by date\n- Fixed: the mobile menu no longer covers the page`,
			`tokens 30420 1880`,
			`say Done, at the top of CHANGELOG.md: three entries, in the order people will notice them.`)},
		{title: "Review PR #88: new payments API", prompt: "#88 changes checkout. Review it closely and comment on the PR if anything's wrong.", by: "Muse", project: "payments-api", agent: "codex", wait: true, script: lines(
			`say Reading what PR #88 changes.`,
			`run gh pr diff 88 --stat| src/checkout/total.go    | 42 ++++++++++------\n src/checkout/payment.go  | 67 ++++++++++++++++++------\n src/checkout/stock.go    | 18 ++++++--\n 3 files changed, 98 insertions(+), 29 deletions(-)`,
			`run go test ./...|ok  	acme/payments/checkout	0.412s\nok  	acme/payments/stock	0.188s`,
			`run gh pr review 88 --comment --body-file review.md|Reviewed pull request #88`,
			`tokens 61500 2480`,
			`say Reviewed: two problems, both commented on the PR.\n\n1. **The total can come out a cent short**: total.go rounds each item before adding them up. Add first, then round.\n2. **A failed payment keeps the stock locked**: the failure path in payment.go never calls stock.Release.\n\nThe rest looks good, and the tests pass.`)},
		{title: "Fix Sign in doing nothing #142", prompt: "Someone reported #142: nothing happens on Sign in. Fix it and open a PR.", by: "Grok Bot", project: "web-app", agent: "claude", wait: true, script: lines(
			`say Reading issue #142 and the sign-in form.`,
			`run gh issue view 142|#142 Nothing happens on Sign in\nEnter an email and click Sign in: nothing happens, and the console shows no error.`,
			`run rg -n "onSubmit" src/login|src/login/form.tsx:18:  const onSubmit = async (e: FormEvent) => {`,
			`write src/login/form.tsx const onSubmit = async (e: FormEvent) => {\n  e.preventDefault();\n  if (!isEmail(email)) {\n    setError("Enter a valid email");\n    return;\n  }\n  await signIn(email);\n};`,
			`run pnpm test src/login| ✓ src/login/form.test.tsx (6)\n ✓ src/login/session.test.ts (18)\n\n Test Files  2 passed (2)\n      Tests  24 passed (24)`,
			`run gh pr create --fill|https://github.com/acme/web-app/pull/143`,
			`tokens 48210 3120`,
			`say Fixed. The email wasn't checked before submitting: with a bad one, onSubmit returned without a word.\n\n- The email is checked first, with a message under the field\n- A test for an empty and a malformed email\n- All 24 tests pass\n\nThe PR: #143`)},
		{title: "Upgrade to React 19", prompt: "Upgrade web-app to React 19 and run the tests.", by: "Muse", project: "web-app", agent: "claude", script: lines(
			`say Checking which dependencies don't support React 19 yet.`,
			`run pnpm outdated react react-dom|react      18.3.1  19.2.0\nreact-dom  18.3.1  19.2.0`,
			`run pnpm why react --depth 1|@acme/charts 3.1.0 (peer react@^18)\nreact-beautiful-dnd 13.1.1 (peer react@^16 || ^17 || ^18)\nreact-helmet 6.1.0 (peer react@>=16)`,
			`tokens 18800 740`,
			`ask Upgrade|Three dependencies don't declare React 19 support yet. How should I go on?|Upgrade React now (forced),Wait until they all support it`)},
		{title: "Dark mode for Settings", prompt: "Add dark mode to the iOS app's Settings screen, and run the UI tests.", by: "Grok Bot", project: "mac-mini/ios-app", agent: "claude", script: lines(
			`say Looking at where the colors are defined now.`,
			`run rg -l "Color(" Sources/Settings|Sources/Settings/SettingsView.swift\nSources/Settings/Theme.swift`,
			`write Sources/Settings/Theme.swift extension Color {\n  static let card = Color("Card")\n  static let ink = Color("Ink")\n}`,
			`say The colors now come from the asset catalog and follow dark mode; running the UI tests.`,
			`tool-sleep 3h`)},
	}},
}

func lines(l ...string) string { return strings.Join(l, "\n") }
