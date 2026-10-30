import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { PairingKind, Workspace } from "@hermesh/proto/identity";
import { LocalService } from "@hermesh/proto/local";
import { expect, test, type ElectronApplication, type Page } from "@playwright/test";
import { execFileSync, spawn } from "node:child_process";
import { createHash } from "node:crypto";
import fs from "node:fs";
import http from "node:http";
import type { AddressInfo } from "node:net";
import os from "node:os";
import path from "node:path";
import { answerMenu, fixture, gitRepo, launch, pickAnswers, snap } from "./app";

// The first run against owner daemons nobody has set up (the fixture's
// fresh ones; each can be set up once): a relay of one's own, the browser
// sign-in with a stand-in center through to the first task, and joining an
// owner that another device already set up.

test.describe.configure({ timeout: 180_000 });

/** The window grew into the main window. */
async function expectMainWindow(app: ElectronApplication, page: Page) {
  await expect(page.getByRole("complementary", { name: "边栏" })).toBeVisible();
  await expect.poll(() => app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0]?.getBounds().width ?? 0)).toBeGreaterThan(900);
}

test("the window opens at its last size, loading in the middle until hermeshd answers", async () => {
  const none = () => path.join(fs.mkdtempSync(path.join(os.tmpdir(), "hmsock-")), "none.sock");
  const width = (app: ElectronApplication) => app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0]?.getBounds().width ?? 0);
  // A first start: the main window's size, as most starts are.
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-"));
  const { app, page } = await launch(none(), { profile });
  await expect(page.getByText("正在连接后台服务…")).toBeVisible();
  expect(await width(app)).toBeGreaterThan(900);
  await snap(page, "start-01-connecting");
  // The explanation only once it has kept not answering.
  await expect(page.getByText("Hermesh 的后台服务没有响应", { exact: false })).toBeHidden();
  await expect(page.getByText("Hermesh 的后台服务没有响应", { exact: false })).toBeVisible({ timeout: 15_000 });
  expect(await width(app)).toBeGreaterThan(900);
  await snap(page, "start-02-not-answering");
  await app.close();

  // The first run makes it compact, and it opens so next time.
  const first = await launch(fixture.fresh[0].socket, { profile });
  await expect(first.page.getByRole("heading", { name: "让 Agent 安全地互相连接" })).toBeVisible();
  await expect.poll(() => width(first.app)).toBe(720);
  await first.app.close();
  const again = await launch(none(), { profile });
  await expect(again.page.getByText("正在连接后台服务…")).toBeVisible();
  expect(await width(again.app)).toBe(720);
  await again.app.close();
});

test("a relay of one's own: welcome, authorize, set up this Mac, add a project, connect later", async () => {
  const fresh = fixture.fresh[0];
  const { app, page } = await launch(fresh.socket);
  await expect(page.getByRole("heading", { name: "让 Agent 安全地互相连接" })).toBeVisible();
  await expect(page.getByRole("button", { name: "立即开始" })).toBeVisible();
  // The official service, and one's own server: nothing to change in between.
  await expect(page.getByRole("button", { name: "hermesh.io", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "更改" })).toHaveCount(0);
  // The promise of an untrusted center.
  await expect(page.getByText("即使服务器被攻破，攻击者也访问不了你的电脑。会话端到端加密，服务器看不到内容。")).toBeVisible();
  await expect.poll(() => app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0]?.getBounds().width)).toBe(720);
  await snap(page, "first-01-welcome");

  await page.getByRole("button", { name: "使用自己的服务器…" }).click();
  await expect(page.getByRole("heading", { name: "连接你自己的服务器" })).toBeVisible();
  await expect(page.getByRole("button", { name: "继续" })).toBeDisabled();

  // A server reached by its IP address is plain HTTP: said before going on.
  // Not for a domain name, or this Mac.
  const plain = page.getByRole("note").filter({ hasText: "未加密（HTTP）" });
  await page.getByLabel("服务器地址").fill("203.0.113.7:8080");
  await expect(plain).toBeVisible();
  await expect(plain).toContainText("抢在你前面认领这台服务器");
  await page.getByLabel("服务器地址").fill("relay.example.com");
  await expect(plain).toHaveCount(0);
  await page.getByLabel("服务器地址").fill("http://127.0.0.1:8080");
  await expect(plain).toHaveCount(0);

  // A network that blocks the server (a company proxy answering 403):
  // said with what to do, before any Touch ID, and the way back still works.
  const proxy = http.createServer((_, res) => res.writeHead(403, { "content-type": "text/html" }).end("<html>Blocked by policy</html>"));
  await new Promise<void>((r) => proxy.listen(0, "127.0.0.1", r));
  const blocked = `127.0.0.1:${(proxy.address() as AddressInfo).port}`;
  await page.getByLabel("服务器地址").fill(`http://${blocked}`);
  await page.getByLabel("认领码").fill(fresh.claimCode);
  await page.getByRole("button", { name: "继续" }).click();
  await page.getByRole("button", { name: "继续" }).click();
  const trouble = page.getByRole("alertdialog", { name: "连不上 Hermesh 服务器" });
  await expect(trouble).toHaveAccessibleDescription(`当前网络可能拦住了 ${blocked}，公司网络和代理常会这样。换个网络（比如手机热点）再试，这台 Mac 上还什么都没改。`);
  await expect(trouble.getByText("403 Forbidden")).toHaveCount(0); // the raw cause, behind 详情
  await trouble.getByRole("button", { name: "详情" }).click();
  await expect(trouble.getByText("403 Forbidden")).toBeVisible();
  await snap(page, "first-03b-unreachable");
  await trouble.getByRole("button", { name: "好" }).click();
  await expect(trouble).toHaveCount(0);
  await expect(page.getByRole("alert")).toHaveCount(0); // nothing left in the footer
  await expect(page.getByRole("button", { name: "继续" })).toBeEnabled();
  proxy.close();
  expect((await daemon(fresh.socket).getStatus({})).initialized).toBe(false);
  await page.getByRole("button", { name: "取消" }).click();

  await page.getByLabel("服务器地址").fill(fresh.relayURL);
  await snap(page, "first-02-relay");
  await page.getByRole("button", { name: "继续" }).click();

  // Nothing is set up before 继续 here; both choices start on.
  await expect(page.getByRole("heading", { name: "让这台 Mac 成为你的可信设备？" })).toBeVisible();
  await expect(page.getByRole("list", { name: "设置步骤" })).toHaveCount(0);
  await expect(page.getByRole("switch", { name: "安全可信地共享本机 agent" })).toBeChecked();
  await expect(page.getByText("不读取 API Key 和 Token")).toBeVisible();
  await expect(page.getByRole("switch", { name: "有事时提醒我" })).toBeChecked();
  await snap(page, "first-03-authorize");
  await page.getByRole("button", { name: "继续" }).click();

  // The signer created the key and approved: this Mac, as its own machine.
  // Opening the page checks the agents (the stand-ins wait for this test): until the first are in, it says it is looking for them.
  const release = (agent: string) => fs.writeFileSync(path.join(fresh.gate!, agent), "");
  const searching = page.getByRole("status").filter({ has: page.getByRole("heading", { name: "正在查找本机上的编码 Agent" }) });
  await expect(searching).toBeVisible({ timeout: 30_000 });
  await expect(searching.locator(".ag-sweep > span")).toHaveCount(15);
  await expect(page.getByRole("button", { name: "稍后设置" })).toBeEnabled();
  await snap(page, "first-04-searching");
  release("codex");
  release("gemini");
  // Every coding agent Hermesh supports: the installed ones with their state, the rest greyed.
  await expect(page.getByRole("heading", { name: "检查和管理可用的 Agent" })).toBeVisible({ timeout: 30_000 });
  await expect(searching).toHaveCount(0);
  await expect(page.getByRole("list", { name: "设置步骤" })).toHaveCount(0); // no way back, so no steps
  const agents = page.getByRole("list", { name: "编码 Agent" });
  const claude = agents.getByRole("listitem", { name: "Claude Code" });
  // Claude Code still being checked shows so while the others are in, not as missing.
  await expect(claude).toContainText("正在检查…");
  await expect(page.getByRole("list", { name: "没有安装的 Agent" }).getByRole("listitem", { name: "Claude Code" })).toHaveCount(0);
  await snap(page, "first-04-checking");
  release("claude");
  await expect(claude).toContainText("2.1.4", { timeout: 30_000 });
  await expect(claude).toContainText("已登录");
  const gemini = agents.getByRole("listitem", { name: "Gemini CLI" });
  await expect(gemini).toContainText("已登录");
  const codex = agents.getByRole("listitem", { name: "Codex" });
  await expect(codex).toContainText("0.48.0");
  await expect(codex).toContainText("还没登录");
  await expect(page.getByRole("list", { name: "没有安装的 Agent" }).getByRole("listitem", { name: "Cursor" })).toBeVisible();
  await expect(agents.getByRole("listitem", { name: "Cursor" })).toHaveCount(0);
  // Each opens its own page, as in settings; Codex's says how to log in. The steps' buttons stay below.
  const agentPage = (name: string) => page.getByRole("region", { name });
  const back = () => page.getByRole("button", { name: "所有 Agent" }).click();
  await codex.getByRole("button", { name: /^Codex/ }).click();
  await expect(agentPage("Codex").getByText("codex login")).toBeVisible();
  await expect(agentPage("Codex").getByRole("button", { name: "在终端运行" })).toBeVisible();
  await expect(page.getByRole("button", { name: "下一步：连接你的 Agent" })).toBeDisabled();
  await snap(page, "first-04-agent");
  // Drawn as a text link, it takes the pointing hand, as links do.
  expect(await page.getByRole("button", { name: "所有 Agent" }).evaluate((el) => getComputedStyle(el).cursor)).toBe("pointer");
  await back();
  await page.waitForTimeout(1500); // the agents around the mark have come in
  await expect(page.getByText("绝不获取 Agent 的任何令牌和密钥，只像你自己在命令行里一样使用它们。")).toBeVisible();
  await snap(page, "first-04-prepare");

  // Claude Code gets its first project: a git repository works in its own worktree per session, unless in place.
  const repo = gitRepo("my-app");
  await pickAnswers(app, repo);
  await claude.getByRole("button", { name: /^Claude Code/ }).click();
  await agentPage("Claude Code").getByRole("button", { name: "添加项目…" }).click();
  const project = agentPage("Claude Code").getByRole("group", { name: "my-app" });
  await expect(project).toContainText(repo);
  await expect(project).toContainText("独立 worktree");
  await answerMenu(app, "inPlace");
  await project.getByRole("button", { name: "更多" }).click();
  await expect(project).toContainText("原地");
  await back();
  await expect(claude).toContainText("1 个项目");
  // It is Claude Code's only; the same folder added to Gemini CLI is the same project, for both.
  await expect(gemini).toContainText("还没有项目");
  await gemini.getByRole("button", { name: /^Gemini CLI/ }).click();
  await agentPage("Gemini CLI").getByRole("button", { name: "添加项目…" }).click();
  await expect(agentPage("Gemini CLI").getByRole("group", { name: "my-app" })).toBeVisible();
  await snap(page, "first-05-project");
  // Taken from Claude Code, it stays Gemini CLI's; the machine knows.
  await back();
  await claude.getByRole("button", { name: /^Claude Code/ }).click();
  await answerMenu(app, "remove");
  await project.getByRole("button", { name: "更多" }).click();
  await expect(project).toHaveCount(0);
  await back();
  await expect(claude).toContainText("还没有项目");
  await expect(gemini).toContainText("1 个项目");
  const mine = (await daemon(fresh.socket).listProjects({})).projects;
  expect(mine.map((p) => [p.projectId, p.agents, p.workspace])).toEqual([["my-app", ["gemini"], Workspace.IN_PLACE]]);

  await page.getByRole("button", { name: "下一步：连接你的 Agent" }).click();
  // One page: the agents as icons (Grok Bot chosen), the message right below; only the name in it differs.
  await expect(page.getByRole("heading", { name: "连接你的 Agent" })).toBeVisible();
  const kinds = page.getByRole("radiogroup", { name: "你的 Agent" });
  await expect(kinds.getByRole("radio", { name: "Grok Bot" })).toBeChecked();
  for (const name of ["Grok Bot", "Muse", "ChatGPT", "Claude", "OpenClaw", "其他 Agent"]) await expect(kinds.getByRole("radio", { name })).toBeEnabled();
  await expect(page.locator(".msgbox")).toContainText('--name "Grok Bot"');
  await kinds.getByRole("radio", { name: "ChatGPT" }).click();
  await expect(page.locator(".msgbox")).toContainText('--name "ChatGPT"');
  await kinds.getByRole("radio", { name: "Muse" }).click();
  await expect(page.locator(".msgbox")).toContainText('--name "Muse"');
  await kinds.getByRole("radio", { name: "其他 Agent" }).click();
  await page.getByLabel("它叫什么").fill("Jarvis");
  await expect(page.locator(".msgbox")).toContainText('--name "Jarvis"');
  await kinds.getByRole("radio", { name: "Grok Bot" }).click();
  await expect(page.getByText("批准后，它拿到你签名的授权，用自己的密钥和你的电脑端到端加密直连，服务器看不到任何内容。")).toBeVisible();
  await snap(page, "first-06-agent");
  await page.getByRole("button", { name: "稍后再连" }).click();
  await expectMainWindow(app, page);
  await app.close();
});

test("browser sign-in: the link brings the authorization page, the center's relay links this Mac, the first task opens", async () => {
  const fresh = fixture.fresh[1];
  // A stand-in center: it only says where devices connect.
  const center = http.createServer((req, res) => {
    if (req.url !== "/.well-known/hermesh.json") return res.writeHead(404).end();
    res.writeHead(200, { "content-type": "application/json" }).end(JSON.stringify({ connect_url: fresh.relayURL }));
  });
  await new Promise<void>((r) => center.listen(0, "127.0.0.1", r));
  const centerURL = `http://127.0.0.1:${(center.address() as AddressInfo).port}`;

  const { app, page } = await launch(fresh.socket);
  await app.evaluate(({ shell }) => {
    const opened: string[] = [];
    Object.assign(globalThis, { opened });
    shell.openExternal = async (url: string) => void opened.push(url);
  });
  const link = (url: string) => app.evaluate(({ app }, u) => app.emit("open-url", { preventDefault() {} }, u), url);

  const browser = () => app.evaluate(() => (globalThis as unknown as { opened: string[] }).opened);

  // The service's name opens its site.
  await page.getByRole("button", { name: "hermesh.io", exact: true }).click();
  await expect.poll(browser).toEqual(["https://hermesh.io"]);
  await app.evaluate(() => void (globalThis as unknown as { opened: string[] }).opened.splice(0));

  // A link code while no sign-in is under way is not ours.
  await link(`hermesh://link?code=${fresh.claimCode}`);
  await page.waitForTimeout(500);
  await expect(page.getByRole("heading", { name: "让 Agent 安全地互相连接" })).toBeVisible();
  // An account that already has an owner: join it (cancelled here).
  await link("hermesh://join?owner=o_" + "a".repeat(24));
  await expect(page.getByRole("heading", { name: "让这台 Mac 加入你的 Hermesh？" })).toBeVisible();
  await expect(page.getByText("继续后按一次 Touch ID，再到你已有的设备上批准。")).toBeVisible();
  await page.getByRole("button", { name: "取消" }).click();

  // The stand-in center, as development builds take it.
  await page.evaluate((u) => localStorage.setItem("centerURL", u), centerURL);
  await page.reload();
  await expect(page.getByRole("button", { name: `127.0.0.1:${(center.address() as AddressInfo).port}` })).toBeVisible();
  await page.getByRole("button", { name: "立即开始" }).click();
  await expect(page.getByRole("heading", { name: "请在浏览器里完成登录" })).toBeVisible();
  await snap(page, "first-07-browser");

  // The browser gets only the challenge; the verifier stays in the main process's file (0600).
  await expect.poll(browser).toHaveLength(1);
  const opened = (await browser())[0];
  expect(opened.startsWith(`${centerURL}/app-login?challenge=`)).toBe(true);
  const challenge = new URL(opened).searchParams.get("challenge");
  const file = path.join(await app.evaluate(({ app }) => app.getPath("userData")), "sign-in.json");
  expect(fs.statSync(file).mode & 0o777).toBe(0o600);
  const { verifier } = JSON.parse(fs.readFileSync(file, "utf8"));
  expect(createHash("sha256").update(verifier).digest("base64url")).toBe(challenge);
  expect(await page.content()).not.toContain(verifier);

  await link(`hermesh://link?code=${fresh.claimCode}`);
  await expect(page.getByRole("heading", { name: "让这台 Mac 成为你的可信设备？" })).toBeVisible();
  await page.getByRole("button", { name: "继续" }).click();
  await expect(page.getByRole("heading", { name: "检查和管理可用的 Agent" })).toBeVisible({ timeout: 30_000 });
  expect(fs.existsSync(file)).toBe(false);

  // The first task runs on Gemini CLI (the scripted agent), so the project is its.
  await pickAnswers(app, gitRepo("site"));
  const gemini = page.getByRole("list", { name: "编码 Agent" }).getByRole("listitem", { name: "Gemini CLI" });
  await gemini.getByRole("button", { name: /^Gemini CLI/ }).click({ timeout: 30_000 });
  const geminiPage = page.getByRole("region", { name: "Gemini CLI" });
  await geminiPage.getByRole("button", { name: "添加项目…" }).click();
  await expect(geminiPage.getByRole("group", { name: "site" })).toBeVisible();
  await page.getByRole("button", { name: "下一步：连接你的 Agent" }).click();

  // Muse's message sent, then Grok Bot picked: whichever agent's request arrives takes the page.
  await expect(page.getByRole("heading", { name: "连接你的 Agent" })).toBeVisible();
  await expect(page.getByRole("radio", { name: "Grok Bot" })).toBeChecked();
  await page.getByRole("radio", { name: "Muse" }).click();
  const message = await page.locator(".msgbox").innerText();
  const address = /auth login (\S+) --name "Muse"/.exec(message)?.[1] ?? "";
  expect(address).toMatch(/\/o_\w+$/);
  await snap(page, "first-08-connect");
  await page.getByRole("radio", { name: "Grok Bot" }).click();
  const agent = (name: string) => {
    const env = { ...process.env, HERMESH_HOME: fs.mkdtempSync(path.join(os.tmpdir(), "hmagent-")) };
    const login = spawn(fixture.cli, ["auth", "login", address, "--name", name], { env });
    return { env, exit: new Promise<number | null>((r) => login.on("exit", r)) };
  };
  const muse = agent("Muse");
  await expect(page.getByText("「Muse」能用什么")).toBeVisible({ timeout: 30_000 });
  await expect(page.getByRole("radiogroup", { name: "你的 Agent" })).toBeHidden();

  // Grok Bot asks too: it waits its turn, a click away.
  const grok = agent("Grok Bot");
  const queue = page.locator(".pa-queue");
  await expect(queue).toContainText("还在等你批准：Grok Bot", { timeout: 30_000 });
  await expect(page.getByText("「Muse」能用什么")).toBeVisible();
  await snap(page, "first-09-queue");
  // Connect Another: back to the message, the requests still a click away.
  await queue.getByRole("button", { name: "再连一个" }).click();
  await expect(page.getByRole("radiogroup", { name: "你的 Agent" })).toBeVisible();
  await expect(queue.getByRole("button", { name: "Muse" })).toBeVisible();
  await queue.getByRole("button", { name: "Grok Bot" }).click();
  await expect(page.getByText("「Grok Bot」能用什么")).toBeVisible();
  await queue.getByRole("button", { name: "Muse" }).click();
  await expect(page.getByText("「Muse」能用什么")).toBeVisible();

  // Grok Bot's notification, clicked: its request on this page.
  type Hook = { hermeshTest: { notifications(): { id: string; title: string; click(): void }[] } };
  await expect
    .poll(() => app.evaluate(() => (globalThis as unknown as Hook).hermeshTest.notifications().map((n) => n.title)))
    .toEqual(expect.arrayContaining([expect.stringContaining("Grok Bot")]));
  await app.evaluate(() => (globalThis as unknown as Hook).hermeshTest.notifications().find((n) => n.title.includes("Grok Bot"))?.click());
  await expect(page.getByText("「Grok Bot」能用什么")).toBeVisible();
  const agentsRow = page.getByRole("group", { name: "编码 agent" });
  await agentsRow.getByRole("button", { name: "修改" }).click();
  await agentsRow.getByRole("checkbox", { name: "Gemini CLI" }).click();
  await snap(page, "first-10-scope");
  await page.getByRole("button", { name: "批准" }).click();

  // Approved, the next one comes; once none waits, the first task, with the first agent approved.
  await expect(page.getByRole("status").filter({ hasText: "「Grok Bot」已连接" })).toBeVisible({ timeout: 30_000 });
  await expect(page.getByText("「Muse」能用什么")).toBeVisible();
  await expect(queue).toHaveCount(1); // "Connect Another" only
  await expect(queue).not.toContainText("还在等你批准");
  await page.getByRole("button", { name: "批准" }).click();

  await expect(page.getByRole("heading", { name: "「Grok Bot」已连接" })).toBeVisible({ timeout: 30_000 });
  // A hello that is sure to run: a coding agent it was given, in its project.
  await expect(page.getByText(/^用 Hermesh 在 site 项目里，让 (Claude Code|Codex|Gemini CLI) 跟我打个招呼。$/)).toBeVisible();
  await expect(page.getByRole("button", { name: "进入 Hermesh" })).toBeVisible();
  await snap(page, "first-11-first-task");
  expect(await grok.exit).toBe(0);
  expect(await muse.exit).toBe(0);
  const env = grok.env;

  // Its first session: the window opens on it.
  execFileSync(fixture.cli, ["session", "start", "--project", "site", "--agent", "gemini", "--title", "检查 README", "--prompt", "say 好的"], { env });
  await expectMainWindow(app, page);
  await expect(page.getByText("检查 README").first()).toBeVisible();
  await snap(page, "first-12-main");
  // The overview has no setup checklist.
  await page.getByRole("button", { name: "概览" }).click();
  await expect(page.getByRole("heading", { name: "概览", level: 1 })).toBeVisible();
  await expect(page.getByRole("region", { name: "完成设置" })).toHaveCount(0);
  // Once the first session is done, 正在干活 rests: the mark, what runs (nothing), and what will show there.
  const working = page.getByRole("region", { name: "正在干活" });
  await expect(working.getByRole("status")).toContainText("现在没有会话在跑", { timeout: 30_000 });
  await expect(working.getByRole("status")).toContainText("agent 一开始干活，会话会出现在这里，链路图也会亮起来。");
  await expect(working.locator(".hm-mark")).toBeVisible();
  await snap(page, "first-13-resting");
  // With nothing live, hovering an agent marks what it can reach on the map, which doesn't grow.
  const map = page.getByRole("region", { name: "链路" });
  await page.mouse.move(0, 0);
  const before = (await map.boundingBox())!.height;
  await map.getByRole("button", { name: "Grok Bot", exact: true }).hover();
  await expect(map.locator(".lm-edge.scope").first()).toBeAttached();
  expect((await map.boundingBox())!.height).toBe(before);
  await app.close();
  center.close();
});

/** hermeshd's local API on socket, as another of the owner's devices uses it. */
const daemon = (socket: string) =>
  createClient(LocalService, createConnectTransport({ baseUrl: "http://hermeshd", httpVersion: "1.1", nodeOptions: { socketPath: socket } }));

test("a Mac with no coding agent: an agent's request may use every one, including ones installed later", async () => {
  const fresh = fixture.fresh[8];
  const { app, page } = await launch(fresh.socket);
  await page.getByRole("button", { name: "使用自己的服务器…" }).click();
  await page.getByLabel("服务器地址").fill(fresh.relayURL);
  await page.getByLabel("认领码").fill(fresh.claimCode);
  await page.getByRole("button", { name: "继续" }).click();
  await expect(page.getByRole("heading", { name: "让这台 Mac 成为你的可信设备？" })).toBeVisible();
  await page.getByRole("button", { name: "继续" }).click();

  // None found: Claude Code is among the missing ones; set up later.
  await expect(page.getByRole("heading", { name: "检查和管理可用的 Agent" })).toBeVisible({ timeout: 30_000 });
  await expect(page.getByRole("list", { name: "没有安装的 Agent" }).getByRole("listitem", { name: "Claude Code" })).toBeVisible();
  await expect(page.getByRole("list", { name: "编码 Agent" }).getByRole("listitem")).toHaveCount(0);
  await page.getByRole("button", { name: "稍后设置" }).click();

  await expect(page.getByRole("heading", { name: "连接你的 Agent" })).toBeVisible();
  await page.getByRole("radio", { name: "Muse" }).click();
  const address = /auth login (\S+) --name "Muse"/.exec(await page.locator(".msgbox").innerText())?.[1] ?? "";
  const login = spawn(fixture.cli, ["auth", "login", address, "--name", "Muse"], { env: { ...process.env, HERMESH_HOME: fs.mkdtempSync(path.join(os.tmpdir(), "hmagent-")) } });
  const loggedIn = new Promise<number | null>((r) => login.on("exit", r));
  await expect(page.getByText("「Muse」能用什么")).toBeVisible({ timeout: 30_000 });

  // Not Claude Code and Codex this Mac doesn't have: every coding agent, the only choice there is.
  const agentsRow = page.getByRole("group", { name: "编码 agent" });
  await expect(agentsRow).toContainText("所有编码 agent（包括以后安装的）");
  await agentsRow.getByRole("button", { name: "修改" }).click();
  const all = agentsRow.getByRole("checkbox", { name: "所有编码 agent（包括以后安装的）" });
  await expect(all).toBeChecked();
  await expect(agentsRow.getByRole("checkbox")).toHaveCount(1);
  await snap(page, "first-13-no-agents");
  // Without it nothing is picked: approving waits until it is back.
  await all.click();
  await expect(page.getByText("至少选一个编码 agent，或选“所有编码 agent”。")).toBeVisible();
  await expect(page.getByRole("button", { name: "批准" })).toBeDisabled();
  await all.click();
  await page.getByRole("button", { name: "批准" }).click();

  await expect(page.getByRole("heading", { name: "「Muse」已连接" })).toBeVisible({ timeout: 30_000 });
  expect(await loggedIn).toBe(0);
  const muse = (await daemon(fresh.socket).listClients({})).clients.find((c) => c.label === "Muse");
  expect(muse?.spec?.agents).toEqual(["*"]);
  await app.close();
});

test("joining an owner: its connection address, the code compared there, the two digits typed here; removed later", async () => {
  // The owner's first device (its test signer approves), and this computer joining it.
  const first = daemon(fixture.fresh[2].socket);
  const { connectAddress } = await first.setup({ relayUrl: fixture.fresh[2].relayURL, claimCode: fixture.fresh[2].claimCode, rootLabel: "first-mac" });
  const { app, page } = await launch(fixture.fresh[3].socket);
  await page.getByRole("button", { name: "使用自己的服务器…" }).click();
  await page.getByRole("button", { name: "加入它…" }).click();
  await expect(page.getByRole("heading", { name: "加入你已有的 Hermesh" })).toBeVisible();
  await page.getByLabel("连接地址").fill("relay.example.com");
  await page.getByRole("button", { name: "继续" }).click();
  await expect(page.getByText("连接地址不对：应该像 relay.example.com/o_xxxxxxxx")).toBeVisible();
  await page.getByLabel("连接地址").fill(connectAddress);
  await page.getByRole("button", { name: "继续" }).click();
  await expect(page.getByRole("heading", { name: "让这台 Mac 加入你的 Hermesh？" })).toBeVisible();
  await page.getByRole("button", { name: "继续" }).click();

  // The request shows the same verification code on the device that approves it.
  const code = page.getByLabel("核对码");
  await expect(code).toBeVisible({ timeout: 30_000 });
  await snap(page, "first-12-join-waiting");
  const deviceRequest = async () => (await first.listPairingRequests({})).pairings.find((p) => p.kind === PairingKind.DEVICE);
  await expect.poll(deviceRequest).toBeTruthy();
  const request = (await deviceRequest())!;
  await expect(code).toHaveAttribute("data-code", request.verificationCode);
  const { matchCode } = await first.approveDevicePairing({ code: request.code });

  await expect(page.getByText("「first-mac」批准了。它现在显示着两位数字，输入它们：")).toBeVisible({ timeout: 30_000 });
  const wrong = String((Number(matchCode) + 1) % 100).padStart(2, "0");
  await page.getByLabel("两位数字").fill(wrong);
  await page.getByRole("button", { name: "加入" }).click();
  await expect(page.getByText("数字不对。请输入你另一台设备上显示的两位数字。")).toBeVisible();
  await page.getByLabel("两位数字").fill(matchCode);
  await snap(page, "first-13-join-digits");
  await page.getByRole("button", { name: "加入" }).click();

  // A trusted device now, and (as allowed) a machine too: only this Mac is
  // left to set up; connecting an agent and the first task are the account's.
  await expect(page.getByRole("heading", { name: "检查和管理可用的 Agent" })).toBeVisible({ timeout: 30_000 });
  expect((await first.listDevices({})).devices.map((d) => d.label)).toHaveLength(2);
  await expect(page.getByRole("button", { name: "完成" })).toBeVisible();
  await expect(page.getByRole("button", { name: "下一步：连接你的 Agent" })).toHaveCount(0);
  await page.getByRole("button", { name: "稍后设置" }).click();
  await expectMainWindow(app, page);

  // The first device removes it again: this one says so, and can start over.
  const joined = (await first.listDevices({})).devices.find((d) => !d.thisDevice)!;
  await first.removeDevice({ keyId: joined.keyId });
  await expect(page.getByRole("heading", { name: "这台 Mac 已不在你的 Hermesh 账号里" })).toBeVisible({ timeout: 30_000 });
  await snap(page, "first-14-removed");
  await page.getByRole("button", { name: "重新设置这台 Mac" }).click();
  await expect(page.getByRole("heading", { name: "让 Agent 安全地互相连接" })).toBeVisible();
  await app.close();
});

test("adding a trusted device in settings: its join request opens there, also from its notification", async () => {
  const home = daemon(fixture.fresh[6].socket);
  const { ownerId } = await home.setup({ relayUrl: fixture.fresh[6].relayURL, claimCode: fixture.fresh[6].claimCode, rootLabel: "home-mac" });
  const { app, page } = await launch(fixture.fresh[6].socket);
  await expectMainWindow(app, page);
  const [settings] = await Promise.all([app.waitForEvent("window"), page.evaluate(() => window.hermesh?.openSettings("account"))]);
  await settings.waitForLoadState();
  await settings.getByRole("list", { name: "可信设备" }).getByRole("button", { name: "添加设备…" }).click();
  const sheet = settings.getByRole("dialog");
  await expect(sheet.getByRole("heading", { name: "添加可信设备" })).toBeVisible();

  // Another Mac asks to join: its request takes the sheet.
  const { verificationCode } = await daemon(fixture.fresh[7].socket).requestJoin({ relayUrl: fixture.fresh[6].relayURL, ownerId, rootLabel: "second-mac", rootPlatform: "macos" });
  const title = sheet.getByRole("heading", { name: "「second-mac」想成为你的可信设备" });
  await expect(title).toBeVisible({ timeout: 30_000 });
  await expect(sheet).toContainText(verificationCode);
  await snap(settings, "settings-device-request");

  // Its notification, clicked: it stays in settings; the main window opens nothing.
  type Hook = { hermeshTest: { notifications(): { title: string; click(): void }[] } };
  const notice = "「second-mac」想成为你的可信设备";
  await expect.poll(() => app.evaluate(() => (globalThis as unknown as Hook).hermeshTest.notifications().map((n) => n.title))).toContain(notice);
  await app.evaluate((_, t) => (globalThis as unknown as Hook).hermeshTest.notifications().find((n) => n.title === t)?.click(), notice);
  await expect(title).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);

  await sheet.getByRole("button", { name: "加为可信设备" }).click();
  await expect(settings.getByRole("status").filter({ hasText: "在「second-mac」上输入弹出窗口里的两位数字" })).toBeVisible({ timeout: 30_000 });
  await expect(sheet).toBeHidden();
  await app.close();
});

test("a work Mac joins as a machine only: no Touch ID, the code, the two digits; its own projects and sessions; removed later", async () => {
  // The owner's trusted device (its test signer approves), and a work Mac
  // joining it from the account's link. The relay stands in for the center:
  // it is where devices connect.
  const home = daemon(fixture.fresh[4].socket);
  const { ownerId, connectAddress } = await home.setup({ relayUrl: fixture.fresh[4].relayURL, claimCode: fixture.fresh[4].claimCode, rootLabel: "home-mac" });
  const { app, page } = await launch(fixture.fresh[5].socket);
  await page.evaluate((u) => localStorage.setItem("centerURL", u), fixture.fresh[4].relayURL);
  await page.reload();
  await app.evaluate(({ app }, u) => app.emit("open-url", { preventDefault() {} }, u), `hermesh://join?owner=${ownerId}`);
  await expect(page.getByRole("heading", { name: "让这台 Mac 加入你的 Hermesh？" })).toBeVisible();

  // Not a trusted device: then sharing its agents is what it is for, and no Touch ID.
  await page.getByRole("switch", { name: "可信设备" }).click();
  await expect(page.getByText("关闭：不在这里生成管理密钥。这台 Mac 只运行任务，不能批准，也看不到你的其他机器。适合公司的 Mac。")).toBeVisible();
  await expect(page.getByRole("switch", { name: "安全可信地共享本机 agent" })).toHaveCount(0);
  await expect(page.getByText("不需要 Touch ID，到你已有的设备上批准即可。")).toBeVisible();
  await snap(page, "machine-01-authorize");
  await page.getByRole("button", { name: "继续" }).click();

  // Its request shows the same code on the trusted device, which approves it as a machine.
  const code = page.getByLabel("核对码");
  await expect(code).toBeVisible({ timeout: 30_000 });
  await expect(page.getByText("打开你已有设备上的 Hermesh，批准这台 Mac 作为执行机器加入。")).toBeVisible();
  await snap(page, "machine-02-waiting");
  const machineRequest = async () => (await home.listPairingRequests({})).pairings.find((p) => p.kind === PairingKind.MACHINE);
  await expect.poll(machineRequest).toBeTruthy();
  const request = (await machineRequest())!;
  await expect(code).toHaveAttribute("data-code", request.verificationCode);
  const { matchCode } = await home.approveMachinePairing({ code: request.code, machineName: "work-mac" });
  expect(matchCode).toMatch(/^\d\d$/);

  // The owner id came from the center: the two digits that device shows.
  await expect(page.getByText("「home-mac」批准了。它现在显示着两位数字，输入它们：")).toBeVisible({ timeout: 30_000 });
  await page.getByLabel("两位数字").fill(matchCode);
  await page.getByRole("button", { name: "加入" }).click();

  // The main window, as on a trusted device: its sessions in the sidebar, and
  // the overview, its links this Mac's own; nothing to approve.
  await expectMainWindow(app, page);
  await expect(page.getByRole("heading", { name: "概览" })).toBeVisible({ timeout: 30_000 });
  const map = page.getByRole("region", { name: "链路" });
  await expect(map).toContainText("这台 Mac");
  // Online: connected to the relay, which the agent's session needs.
  await expect(map).toContainText("接新会话", { timeout: 30_000 });
  await expect(map.getByRole("button", { name: "+ 连接更多" })).toHaveCount(0);
  await expect(page.getByRole("region", { name: "需要你处理" })).toHaveCount(0);

  // Its settings, from the account menu, are this Mac's: its coding agents,
  // their models and projects, and its account; no access to grant, no other
  // machine, nothing to approve.
  await page.getByRole("button", { name: "账号与设置" }).click();
  await expect(page.getByRole("menu", { name: "账号与设置" }).getByRole("menuitem")).toHaveText(["设置…⌘,", "账号…"]);
  const [settings] = await Promise.all([app.waitForEvent("window"), page.getByRole("menuitem", { name: /^设置/ }).click()]);
  await settings.waitForLoadState();
  const nav = settings.getByRole("navigation", { name: "设置" });
  await expect(nav.getByRole("button")).toHaveText(["通用", "这台 Mac", "账号"]);
  await nav.getByRole("button", { name: "这台 Mac" }).click();
  const panel = settings.getByRole("main", { name: "这台 Mac" });
  await expect(panel.getByRole("switch", { name: "安全可信地共享本机 agent" })).toHaveCount(0);
  await expect(panel.getByRole("button", { name: "添加机器…" })).toHaveCount(0);
  await expect(panel.getByRole("button", { name: "移除…" })).toHaveCount(0);
  await snap(settings, "machine-05-settings");
  const agents = panel.getByRole("list", { name: "编码 Agent" });
  const codex = agents.getByRole("listitem", { name: "Codex" });
  await codex.getByRole("button", { name: /^Codex/ }).click({ timeout: 30_000 });
  const model = panel.getByRole("region", { name: "Codex" }).getByRole("group", { name: "模型" });
  await expect(model).toContainText("跟随 Codex（GPT-Fake-Big）", { timeout: 30_000 });
  await answerMenu(app, "gpt-fake-small");
  await model.getByRole("button").click();
  await expect(model).toHaveText("模型GPT-Fake-Small");
  const workMac = daemon(fixture.fresh[5].socket);
  const codexDefaults = async () => (await workMac.listMachines({})).machines[0]?.status?.agents.find((a) => a.name === "codex")?.defaults?.model;
  await expect.poll(codexDefaults).toBe("gpt-fake-small");
  await snap(settings, "machine-06-agent");
  // ‹ goes back to this Mac, which says what is set; Gemini CLI gets a project.
  await settings.getByRole("button", { name: "返回这台 Mac" }).click();
  await expect(codex).toContainText("GPT-Fake-Small");
  await expect(settings.getByRole("button", { name: /^返回/ })).toHaveCount(0);
  await agents.getByRole("listitem", { name: "Gemini CLI" }).getByRole("button", { name: /^Gemini CLI/ }).click();
  await pickAnswers(app, gitRepo("billing"));
  await panel.getByRole("region", { name: "Gemini CLI" }).getByRole("button", { name: "添加项目…" }).click();
  await expect(panel.getByRole("region", { name: "Gemini CLI" }).getByRole("group", { name: "billing" })).toBeVisible({ timeout: 30_000 });
  await expect(map.getByRole("button", { name: "billing" })).toBeVisible({ timeout: 30_000 });
  // Its account: whose it is and where it connects; from here it can leave,
  // or become a trusted device (in the main window).
  await nav.getByRole("button", { name: "账号" }).click();
  const account = settings.getByRole("main", { name: "账号" });
  await expect(account).toContainText("它不能批准，也看不到你的其他机器");
  await expect(account.getByRole("list", { name: "可信设备" })).toHaveCount(0);
  await expect(account.getByRole("button", { name: "离开你的 Hermesh…" })).toBeVisible();
  await snap(settings, "machine-07-account");
  await Promise.all([settings.waitForEvent("close"), account.getByRole("button", { name: "让这台 Mac 也能批准…" }).click()]);
  await expect(page.getByRole("heading", { name: "在你已有的设备上批准" })).toBeVisible({ timeout: 30_000 });
  await page.getByRole("button", { name: "取消" }).click();
  await expect(page.getByRole("heading", { name: "概览" })).toBeVisible({ timeout: 30_000 });

  // An agent of the owner, granted all machines: before it comes, the map
  // has it as one that may start sessions here, faint.
  const env = { ...process.env, HERMESH_HOME: fs.mkdtempSync(path.join(os.tmpdir(), "hmagent-")) };
  const login = spawn(fixture.cli, ["auth", "login", connectAddress, "--name", "Grok Bot"], { env });
  const loggedIn = new Promise<number | null>((r) => login.on("exit", r));
  const clientRequest = async () => (await home.listPairingRequests({})).pairings.find((p) => p.kind === PairingKind.CLIENT);
  await expect.poll(clientRequest, { timeout: 30_000 }).toBeTruthy();
  await home.approveClientPairing({ code: (await clientRequest())!.code, spec: { agents: ["gemini"] } });
  expect(await loggedIn).toBe(0);
  const grok = map.getByRole("button", { name: "Grok Bot", exact: true });
  await expect(grok).toContainText("还没来过", { timeout: 30_000 });
  await expect(map).toContainText("还没来过");
  // Nothing has run here yet: 正在干活 rests, with no prompt to send.
  const working = page.getByRole("region", { name: "正在干活" });
  await expect(working.getByRole("status")).toContainText("现在没有会话在跑");
  await expect(working).not.toContainText("发一句试试");
  await snap(page, "machine-03-window");

  // It runs a session there: the work Mac lists it under the agent's name,
  // the map runs it from the agent to the project, and the agent's card says
  // what it may do here.
  execFileSync(fixture.cli, ["session", "start", "--project", "billing", "--agent", "gemini", "--title", "修账单导出", "--prompt", "shell sleep 1800"], { env });
  const row = page.getByRole("list", { name: "会话" }).getByRole("listitem").filter({ hasText: "修账单导出" });
  await expect(row).toContainText("Grok Bot", { timeout: 30_000 });
  await expect(grok).not.toContainText("还没来过", { timeout: 30_000 });
  await expect(page.getByRole("button", { name: "管理授权…" })).toHaveCount(0);
  // Resting on it shows its card, over the map, which keeps its height.
  const height = (await map.boundingBox())!.height;
  await grok.hover();
  const card = page.getByRole("dialog", { name: "Grok Bot" });
  await expect(card).toBeVisible();
  expect((await map.boundingBox())!.height).toBe(height);
  await page.mouse.move(0, 0);
  await expect(card).toHaveCount(0);
  // Clicked, it stays, and 正在干活 keeps only its sessions.
  await grok.click();
  await expect(card).toContainText("个人 agent · 自己保管密钥");
  await expect(card).toContainText("授权给了它完全权限");
  await expect(card).toContainText("在这里能用所有项目");
  await expect(card).toContainText("编码 agentGemini CLI");
  await expect(card).toContainText(/有效期还剩 \d+ 天/);
  await expect(card).toContainText("在这台 Mac今天 1 个任务 · 最近一次");
  await expect(page.getByRole("status").filter({ hasText: "Grok Bot 发起的会话" })).toBeVisible();
  await snap(page, "machine-08-card");
  await page.mouse.move(0, 0);
  await expect(card).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(card).toHaveCount(0);

  await row.click();
  await expect(page.getByRole("heading", { name: "修账单导出" })).toBeVisible();
  await expect(page.getByRole("log", { name: "对话" })).toContainText("sleep 1800", { timeout: 30_000 });
  await snap(page, "machine-04-session");
  await page.getByRole("button", { name: /打断/ }).click();
  await expect(page.getByRole("status")).toContainText("已打断「修账单导出」");

  // The trusted device removes it: it says so, and can start over.
  const work = (await home.listMachines({})).machines.find((m) => m.machineName === "work-mac")!;
  await home.removeMachine({ machineId: work.machineId });
  await expect(page.getByRole("heading", { name: "这台 Mac 已不在你的 Hermesh 账号里" })).toBeVisible({ timeout: 60_000 });
  await expect(page.getByText("它已从你的机器里移除，或者账号重新开始了。它不再接任务。重新设置即可再次加入你的账号。")).toBeVisible();
  await page.getByRole("button", { name: "重新设置这台 Mac" }).click();
  await expect(page.getByRole("heading", { name: "让 Agent 安全地互相连接" })).toBeVisible();
  await app.close();
});

test("erasing everything: what it erases, the server's address typed, Touch ID, then this Mac's first run says what was done", async () => {
  const fresh = fixture.fresh[9];
  const mac = daemon(fresh.socket);
  await mac.setup({ relayUrl: fresh.relayURL, claimCode: fresh.claimCode, rootLabel: "erase-mac", localMachineName: "erase-mac" });
  const host = new URL(fresh.relayURL).host;
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-"));
  const { app, page } = await launch(fresh.socket, { profile });
  await expectMainWindow(app, page);
  const [settings] = await Promise.all([app.waitForEvent("window"), page.evaluate(() => window.hermesh?.openSettings("account"))]);
  await settings.waitForLoadState();
  await settings.getByRole("main", { name: "账号与设备" }).getByRole("button", { name: "全部清空…" }).click();

  // What it erases, by name (this Mac's machine is this Mac), and what stays.
  const sheet = settings.getByRole("dialog", { name: "全部清空 Hermesh？" });
  await expect(sheet).toContainText("erase-mac 这台 Mac");
  await expect(sheet).toContainText(`自己的服务器 · ${host}`);
  await expect(sheet).not.toContainText("机器");
  await expect(sheet).toContainText("有未提交改动的 worktree：挪到一边保留");
  // Escape doesn't close it; the button waits for the server's address.
  await settings.keyboard.press("Escape");
  await expect(sheet).toBeVisible();
  const erase = sheet.getByRole("button", { name: "全部清空", exact: true });
  await expect(erase).toBeDisabled();
  await sheet.getByLabel(/清空后不能撤销/).fill(host.slice(0, -1));
  await expect(erase).toBeDisabled();
  await sheet.getByLabel(/清空后不能撤销/).fill(` ${host.toUpperCase()} `);
  await snap(settings, "settings-erase");
  // The test signer approves (Touch ID); this Mac is the only one, so it erases itself at once.
  await Promise.all([settings.waitForEvent("close"), erase.click()]);

  // The first run, saying once what was done.
  await expect(page.getByRole("heading", { name: "让 Agent 安全地互相连接" })).toBeVisible({ timeout: 30_000 });
  const note = page.getByRole("status").filter({ hasText: "Hermesh 已全部清空" });
  await expect(note).toBeVisible();
  await snap(page, "first-erased");
  const status = await mac.getStatus({});
  expect(status.initialized).toBe(false);
  expect(status.erased?.eraseId).toBeTruthy();
  await note.getByRole("button", { name: "关闭" }).click();
  await expect(note).toBeHidden();
  await app.close();

  // Closed, it stays closed.
  const again = await launch(fresh.socket, { profile });
  await expect(again.page.getByRole("heading", { name: "让 Agent 安全地互相连接" })).toBeVisible();
  await expect(again.page.getByText("Hermesh 已全部清空")).toHaveCount(0);
  await again.app.close();
});

test("removing another trusted device's machine in 设置 › 机器 removes the device", async () => {
  // This Mac's owner, and a PC that joins it as a trusted device with its own machine.
  const first = daemon(fixture.fresh[10].socket);
  await first.setup({ relayUrl: fixture.fresh[10].relayURL, claimCode: fixture.fresh[10].claimCode, rootLabel: "first-mac" });
  const { ownerId } = await first.getStatus({});
  const pc = daemon(fixture.fresh[11].socket);
  const join = await pc.requestJoin({ relayUrl: fixture.fresh[10].relayURL, ownerId, rootLabel: "work-pc", rootPlatform: "windows", localMachineName: "work-pc" });
  await expect.poll(async () => (await first.listPairingRequests({})).pairings.some((p) => p.kind === PairingKind.DEVICE)).toBe(true);
  const { matchCode } = await first.approveDevicePairing({ code: join.code });
  await expect.poll(async () => (await pc.waitJoin({ code: join.code })).done, { timeout: 30_000 }).toBe(true);
  await pc.confirmJoin({ code: join.code, accept: true, matchCode });
  await expect.poll(async () => (await first.listDevices({})).devices.find((d) => d.label === "work-pc")?.machineId ?? "", { timeout: 30_000 }).not.toBe("");

  const { app, page } = await launch(fixture.fresh[10].socket);
  const [settings] = await Promise.all([app.waitForEvent("window"), page.evaluate(() => window.hermesh?.openSettings("machines"))]);
  await settings.getByRole("list", { name: "机器" }).getByRole("button", { name: /^work-pc/ }).click();
  await settings.locator(".set-acts").getByRole("button", { name: "移除…" }).click();
  // The device, not only its machine: it would stay trusted otherwise.
  const dialog = settings.getByRole("dialog", { name: "移除「work-pc」？" });
  await expect(dialog).toContainText("它立刻不能再批准任何事，它自己的执行机器也一并移除。");
  await dialog.getByRole("button", { name: "移除" }).click();
  await expect.poll(async () => (await first.listDevices({})).devices.map((d) => d.label), { timeout: 30_000 }).toEqual(["first-mac"]);
  await expect.poll(async () => (await pc.getStatus({})).removed, { timeout: 30_000 }).toBe(true);
  await app.close();
});
