import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { PairingKind } from "@hermesh/proto/identity";
import { LocalService } from "@hermesh/proto/local";
import { expect, test, type ElectronApplication, type Page } from "@playwright/test";
import { createHash } from "node:crypto";
import fs from "node:fs";
import http from "node:http";
import type { AddressInfo } from "node:net";
import os from "node:os";
import path from "node:path";
import { fixture, gitRepo, launch, pickAnswers, snap, sys } from "./app";

// The first run against owner daemons nobody has set up (the fixture's fresh ones; each can be
// set up once), as docs/cold-start-prd.md has it: no account needed, signing in on its fourth
// step or later, with the official service in the browser, a server of one's own, or joining a
// Hermesh one has; then the account in Settings, signing out and in again. These computers can't
// run Her (their hermeshd has no her), so the first run goes past Her's step; coldstart.spec.ts
// has a computer that can.

test.describe.configure({ timeout: 180_000 });

/** The window grew into the main window. */
async function expectMainWindow(app: ElectronApplication, page: Page) {
  // A session on a small display may hide the sidebar; its toggle then stays in the top band.
  await expect(page.locator(".side-nav, .side-float")).toBeVisible({ timeout: 30_000 });
  await expect.poll(() => app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0]?.getBounds().width ?? 0)).toBeGreaterThan(900);
}

/** From the welcome to the sign-in step: no project (later), Her's step passed by on these computers. */
async function toSignIn(page: Page) {
  await page.getByRole("button", { name: "开始设置" }).click();
  await expect(page.getByRole("heading", { name: /^(已找到 \d+ 个 Agent 提供方|还没有可用的 Agent 提供方)$/ })).toBeVisible({ timeout: 30_000 });
  await page.getByRole("button", { name: "稍后设置" }).click();
  await expect(page.getByRole("heading", { name: "登录，接入 Hermesh 网络" })).toBeVisible();
}

/** From the signed-in sign-in step into the main window. */
async function finish(app: ElectronApplication, page: Page) {
  await expect(page.getByRole("heading", { name: "已接入 Hermesh 网络" })).toBeVisible({ timeout: 30_000 });
  await page.getByRole("button", { name: "继续" }).click();
  await expect(page.getByRole("heading", { name: "设置完成" })).toBeVisible();
  await page.getByRole("button", { name: "进入 Hermesh" }).click();
  await expectMainWindow(app, page);
}

/** The settings window, on its account page. */
async function accountPage(app: ElectronApplication, page: Page) {
  const [settings] = await Promise.all([app.waitForEvent("window"), page.evaluate(() => window.hermesh?.openSettings("account"))]);
  await settings.waitForLoadState();
  return { settings, main: settings.getByRole("main", { name: "账号与设备" }) };
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
  await expect(first.page.getByRole("heading", { name: "欢迎使用 Hermesh" })).toBeVisible();
  await expect.poll(() => width(first.app)).toBe(720);
  await first.app.close();
  const again = await launch(none(), { profile });
  await expect(again.page.getByText("正在连接后台服务…")).toBeVisible();
  expect(await width(again.app)).toBe(720);
  await again.app.close();
});

test("without an account: the agents looked for, a project, the sign-in step left for later, the main window", async () => {
  const fresh = fixture.fresh[0];
  const { app, page } = await launch(fresh.socket);
  await expect(page.getByRole("heading", { name: "欢迎使用 Hermesh" })).toBeVisible();
  await snap(page, "first-01-welcome");
  await page.getByRole("button", { name: "开始设置" }).click();

  // Looking for them (the stand-ins wait for this test): every agent provider lit in turn.
  const release = (agent: string) => fs.writeFileSync(path.join(fresh.gate!, agent), "");
  await expect(page.getByRole("heading", { name: "正在查找 Agent 提供方…" })).toBeVisible();
  await expect(page.locator(".fr-sweep > span")).toHaveCount(16);
  await snap(page, "first-02-searching");
  for (const a of ["claude", "codex", "gemini", "antigravity"]) release(a);
  // The installed ones, signed in or not; how many can work now.
  await expect(page.getByRole("heading", { name: "已找到 3 个 Agent 提供方" })).toBeVisible({ timeout: 30_000 });
  const agents = page.getByRole("list", { name: "Agent 提供方" });
  await expect(agents.getByRole("listitem", { name: "Claude Code" })).toContainText("2.1.4");
  await expect(agents.getByRole("listitem", { name: "Claude Code" })).toContainText("已登录");
  await expect(agents.getByRole("listitem", { name: "Gemini CLI" })).toContainText("已登录");
  await expect(agents.getByRole("listitem", { name: "Codex" })).toContainText("还没登录");
  await expect(agents.getByRole("listitem", { name: "Cursor" })).toHaveCount(0); // not installed: not listed
  await expect(page.getByText("绝不获取 Agent 的任何令牌和密钥，只像你自己在命令行里一样使用它们。")).toBeVisible();
  // A project before 继续 (稍后设置 goes on without).
  await expect(page.getByRole("button", { name: "继续" })).toBeDisabled();
  const repo = gitRepo("my-app");
  await pickAnswers(app, repo);
  await page.getByRole("button", { name: "添加项目…" }).click();
  const project = page.getByRole("list", { name: "项目" }).getByRole("listitem", { name: "my-app" });
  await expect(project).toContainText(repo);
  await expect(project).toContainText("独立 worktree");
  await snap(page, "first-03-team");
  await page.getByRole("button", { name: "继续" }).click();

  // Her can't run here: on to signing in, which can wait.
  await expect(page.getByRole("heading", { name: "登录，接入 Hermesh 网络" })).toBeVisible();
  await expect(page.getByText("一张专为 AI Agent 搭的通信网：你的手机、电脑和各家 Agent 两两互通。")).toBeVisible();
  await expect(page.locator(".fr-bubble.up")).toBeVisible({ timeout: 10_000 });
  await snap(page, "first-04-network");
  await page.getByRole("button", { name: "暂不登录" }).click();
  await expect(page.getByRole("heading", { name: "设置完成" })).toBeVisible();
  await expect(page.getByText("这台电脑已准备好。")).toBeVisible();
  await page.getByRole("button", { name: "进入 Hermesh" }).click();
  await expectMainWindow(app, page);
  await expect(page.locator(".side-foot")).toContainText("未登录");
  // The main window opens on the overview: Her can't run here.
  await expect(page.getByRole("heading", { name: "概览", level: 1 })).toBeVisible();
  const status = await daemon(fresh.socket).getStatus({});
  expect([status.initialized, status.localMachineEnabled, status.relayHealth?.phase]).toEqual([false, true, "local"]);

  // What needs an account says so: from the menu, its shortcut, and Settings.
  await page.locator(".side-foot").click();
  await page.getByRole("menuitem", { name: "连接 agent…" }).click();
  await expect(page.getByRole("dialog", { name: "连接 agent需要登录" })).toBeVisible();
  await page.getByRole("dialog").getByRole("button", { name: "取消" }).click();
  const { settings, main } = await accountPage(app, page);
  await expect(main).toContainText("未登录");
  await expect(main).toContainText(sys("登录后，在任何地方都能连到这台 Mac，并接入云端助手和其他电脑。"));
  await expect(main).toContainText("需要登录");
  await expect(main.getByRole("list", { name: "设备" }).getByRole("listitem")).toHaveCount(1);
  await snap(settings, "settings-account-signed-out");
  await main.getByRole("button", { name: "添加环境…" }).click();
  await expect(settings.getByRole("dialog", { name: "添加环境需要登录" })).toBeVisible();
  await app.close();
});

test("the official service in the browser: the link brings the authorization, this Mac signs in on the sign-in step", async () => {
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
  // The stand-in center, as development builds take it.
  await page.evaluate((u) => localStorage.setItem("centerURL", u), centerURL);
  await toSignIn(page);

  // An account that already has an owner: joining it (cancelled here).
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(page.getByRole("heading", { name: "请在浏览器里完成登录" })).toBeVisible();
  await link("hermesh://join?owner=o_" + "a".repeat(24));
  await expect(page.getByText(sys("让这台 Mac 加入你的 Hermesh？"))).toBeVisible();
  await expect(page.getByText(sys("继续后按一次 Touch ID，再到你已有的设备上批准。", "继续后用一次系统密码，再到你已有的设备上批准。"))).toBeVisible();
  await page.getByRole("button", { name: "取消" }).click();
  await expect(page.getByRole("heading", { name: "登录，接入 Hermesh 网络" })).toBeVisible();

  // The browser gets only the challenge; the verifier stays in the main process's file (0600).
  await app.evaluate(() => void (globalThis as unknown as { opened: string[] }).opened.splice(0));
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(page.getByRole("heading", { name: "请在浏览器里完成登录" })).toBeVisible();
  await snap(page, "first-05-browser");
  await expect.poll(browser).toHaveLength(1);
  const opened = (await browser())[0];
  expect(opened.startsWith(`${centerURL}/app-login?challenge=`)).toBe(true);
  const challenge = new URL(opened).searchParams.get("challenge");
  const file = path.join(await app.evaluate(({ app }) => app.getPath("userData")), "sign-in.json");
  expect(fs.statSync(file).mode & 0o777).toBe(0o600);
  const { verifier } = JSON.parse(fs.readFileSync(file, "utf8"));
  expect(createHash("sha256").update(verifier).digest("base64url")).toBe(challenge);
  expect(await page.content()).not.toContain(verifier);

  // The link: one question, one Touch ID (the test signer), this Mac's machine kept.
  const before = (await daemon(fresh.socket).getStatus({})).localMachineId;
  await link(`hermesh://link?code=${fresh.claimCode}`);
  await expect(page.getByText(sys("让这台 Mac 成为你的可信设备？"))).toBeVisible();
  await expect(page.getByText(sys("以后有设备或 agent 要连接这台 Mac，都要你在这里按 Touch ID 批准。", "以后有设备或 agent 要连接这台电脑，都要你在这里用系统密码批准。"))).toBeVisible();
  await expect(page.getByRole("switch")).toHaveCount(0);
  await snap(page, "first-06-authorize");
  await page.getByRole("button", { name: "继续" }).click();
  await expect(page.getByRole("heading", { name: "已接入 Hermesh 网络" })).toBeVisible({ timeout: 30_000 });
  expect(fs.existsSync(file)).toBe(false);
  const status = await daemon(fresh.socket).getStatus({});
  expect([status.initialized, status.localMachineId]).toEqual([true, before]);
  await snap(page, "first-07-linked");
  await finish(app, page);
  await expect(page.locator(".side-foot")).toContainText("已登录");
  await app.close();
  center.close();
});

test("a server of one's own: its warnings, signing in on the sign-in step, then out and in again in Settings", async () => {
  const fresh = fixture.fresh[4];
  const { app, page } = await launch(fresh.socket);
  await toSignIn(page);
  await page.getByRole("button", { name: "使用自己的服务器…" }).click();
  await expect(page.getByText("连接你自己的服务器")).toBeVisible();
  await expect(page.getByRole("button", { name: "继续" })).toBeDisabled();

  // A server reached by its IP address is plain HTTP: said before going on. Not for a domain name, or this Mac.
  const plain = page.getByRole("note").filter({ hasText: "未加密（HTTP）" });
  await page.getByLabel("服务器地址").fill("203.0.113.7:8080");
  await expect(plain).toBeVisible();
  await page.getByLabel("服务器地址").fill("relay.example.com");
  await expect(plain).toHaveCount(0);

  // A network that blocks the server (a company proxy answering 403): said, with the raw cause behind 详情.
  const proxy = http.createServer((_, res) => res.writeHead(403, { "content-type": "text/html" }).end("<html>Blocked by policy</html>"));
  await new Promise<void>((r) => proxy.listen(0, "127.0.0.1", r));
  const blocked = `127.0.0.1:${(proxy.address() as AddressInfo).port}`;
  await page.getByLabel("服务器地址").fill(`http://${blocked}`);
  await page.getByLabel("认领码").fill(fresh.claimCode);
  await page.getByRole("button", { name: "继续" }).click();
  await page.getByRole("button", { name: "继续" }).click();
  const trouble = page.getByRole("alertdialog", { name: "连不上 Hermesh 服务器" });
  await expect(trouble).toHaveAccessibleDescription(sys(`当前网络可能拦住了 ${blocked}，公司网络和代理常会这样。换个网络（比如手机热点）再试，这台 Mac 上还什么都没改。`));
  await trouble.getByRole("button", { name: "详情" }).click();
  await expect(trouble.getByText("403 Forbidden")).toBeVisible();
  await trouble.getByRole("button", { name: "好" }).click();
  proxy.close();
  expect((await daemon(fresh.socket).getStatus({})).initialized).toBe(false);
  await page.getByRole("button", { name: "取消" }).click();

  await page.getByRole("button", { name: "使用自己的服务器…" }).click();
  await page.getByLabel("服务器地址").fill(fresh.relayURL);
  await page.getByLabel("认领码").fill(fresh.claimCode);
  await page.getByRole("button", { name: "继续" }).click();
  await page.getByRole("button", { name: "继续" }).click();
  await finish(app, page);

  // Settings: signed in, the devices, what the network adds.
  const { settings, main } = await accountPage(app, page);
  await expect(main).toContainText(`自己的服务器 · ${new URL(fresh.relayURL).host}`);
  await expect(main).toContainText("已连接");
  await expect(main).not.toContainText("需要登录");
  await expect(main.getByRole("button", { name: "高级…" })).toBeVisible();
  await snap(settings, "settings-account-signed-in");

  // Signing out keeps this Mac: its projects, sessions and Her; the others reach it no more.
  await main.getByRole("button", { name: "退出登录…" }).click();
  const ask = settings.getByRole("dialog", { name: "要退出登录吗？" });
  await expect(ask).toContainText(sys("这台 Mac 上的项目、任务和 Her 都会保留。其他设备和 agent 将无法连接这台 Mac。"));
  await ask.getByRole("button", { name: "退出登录" }).click();
  await expect(main).toContainText("未登录", { timeout: 30_000 });
  let status = await daemon(fresh.socket).getStatus({});
  expect([status.signedOut, status.localMachineEnabled, status.relayHealth?.phase]).toEqual([true, true, "local"]);
  await expect(page.locator(".side-foot")).toContainText("未登录");

  // Signing in again: a server of one's own only checks it; no Touch ID, the same machine.
  await main.getByRole("button", { name: "登录", exact: true }).click();
  await expect(main).toContainText("已连接", { timeout: 30_000 });
  status = await daemon(fresh.socket).getStatus({});
  expect([status.signedOut, status.initialized]).toEqual([false, true]);
  await expect(page.locator(".side-foot")).toContainText("已登录");
  await app.close();
});

test("a Mac with no coding agent: the agents not installed, and on without them", async () => {
  const { app, page } = await launch(fixture.fresh[8].socket);
  await page.getByRole("button", { name: "开始设置" }).click();
  await expect(page.getByRole("heading", { name: "还没有可用的 Agent 提供方" })).toBeVisible({ timeout: 30_000 });
  await expect(page.getByText("安装并登录 Claude Code 或 Codex 后，Hermesh 会自己发现。")).toBeVisible();
  await expect(page.getByRole("list", { name: "Agent 提供方" }).getByRole("listitem", { name: "Claude Code" })).toContainText("没有安装");
  const others = page.getByRole("button", { name: "其他提供方（3）" });
  const folded = page.getByRole("listitem", { name: /^(Gemini CLI|DeepSeek Harness|Augment)$/ });
  await expect(folded).toHaveCount(0);
  await others.click();
  await expect(folded).toHaveCount(3);
  await others.click();
  await expect(folded).toHaveCount(0);
  await expect(page.getByRole("button", { name: "继续" })).toBeDisabled();
  // No agent provider to work: a project is not what it waits for.
  await expect(page.getByText("添加一个项目后即可继续", { exact: false })).toHaveCount(0);
  await snap(page, "first-08-no-agents");
  await page.getByRole("button", { name: "稍后设置" }).click();
  await expect(page.getByRole("heading", { name: "登录，接入 Hermesh 网络" })).toBeVisible();
  await app.close();
});

test("joining an owner: its connection address, the code compared there, the two digits typed here; removed later, set up again from the start", async () => {
  // The owner's first device (its test signer approves), and this computer joining it.
  const first = daemon(fixture.fresh[2].socket);
  const { connectAddress } = await first.setup({ relayUrl: fixture.fresh[2].relayURL, claimCode: fixture.fresh[2].claimCode, rootLabel: "first-mac" });
  const { app, page } = await launch(fixture.fresh[3].socket);
  await toSignIn(page);
  await page.getByRole("button", { name: "使用自己的服务器…" }).click();
  await page.getByRole("button", { name: "已在别的设备上用？加入它…" }).click();
  await expect(page.getByText("加入你已有的 Hermesh")).toBeVisible();
  await page.getByLabel("连接地址").fill("relay.example.com");
  await page.getByRole("button", { name: "继续" }).click();
  await expect(page.getByText("连接地址不对：应该像 relay.example.com/o_xxxxxxxx")).toBeVisible();
  await page.getByLabel("连接地址").fill(connectAddress);
  await page.getByRole("button", { name: "继续" }).click();
  await expect(page.getByText(sys("让这台 Mac 加入你的 Hermesh？"))).toBeVisible();
  await page.getByRole("button", { name: "继续" }).click();

  // The request shows the same verification code on the device that approves it.
  const code = page.getByLabel("核对码");
  await expect(code).toBeVisible({ timeout: 30_000 });
  await snap(page, "first-09-join-waiting");
  const deviceRequest = async () => (await first.listPairingRequests({})).pairings.find((p) => p.kind === PairingKind.DEVICE);
  await expect.poll(deviceRequest).toBeTruthy();
  const request = (await deviceRequest())!;
  await expect(code).toHaveAttribute("data-code", request.verificationCode);
  const { matchCode } = await first.approveDevicePairing({ code: request.code });

  await expect(page.getByText("输入已有设备上的两位数字")).toBeVisible({ timeout: 30_000 });
  const wrong = String((Number(matchCode) + 1) % 100).padStart(2, "0");
  await page.getByLabel("两位数字").fill(wrong);
  await page.getByRole("button", { name: "加入" }).click();
  await expect(page.getByText("数字不匹配。请输入你另一台设备上显示的两位数字。")).toBeVisible();
  await page.getByLabel("两位数字").fill(matchCode);
  await snap(page, "first-10-join-digits");
  await page.getByRole("button", { name: "加入" }).click();
  await finish(app, page);
  expect((await first.listDevices({})).devices.map((d) => d.label)).toHaveLength(2);

  // The first device removes it again: this one says so; set up again, from the welcome.
  const joined = (await first.listDevices({})).devices.find((d) => !d.thisDevice)!;
  await first.removeDevice({ keyId: joined.keyId });
  await expect(page.getByRole("heading", { name: sys("这台 Mac 已不在你的 Hermesh 账号里") })).toBeVisible({ timeout: 30_000 });
  await snap(page, "first-11-removed");
  await page.getByRole("button", { name: sys("重新设置这台 Mac") }).click();
  await expect(page.getByRole("heading", { name: "欢迎使用 Hermesh" })).toBeVisible({ timeout: 30_000 });
  await app.close();
});

test("adding a device in settings: its join request opens there, also from its notification", async () => {
  const home = daemon(fixture.fresh[6].socket);
  const { ownerId } = await home.setup({ relayUrl: fixture.fresh[6].relayURL, claimCode: fixture.fresh[6].claimCode, rootLabel: "home-mac" });
  const { app, page } = await launch(fixture.fresh[6].socket);
  await expectMainWindow(app, page);
  const { settings, main } = await accountPage(app, page);
  await main.getByRole("button", { name: "添加设备…" }).click();
  const sheet = settings.getByRole("dialog");
  await expect(sheet.getByRole("heading", { name: "添加设备" })).toBeVisible();
  await expect(sheet).toContainText("在那台设备上安装 Hermesh");
  await expect(sheet).toContainText("点「使用自己的服务器…」，再点「加入它…」，填入这个连接地址：");
  await snap(settings, "settings-add-device");

  // Another Mac asks to join: its request takes the sheet.
  const { verificationCode } = await daemon(fixture.fresh[7].socket).requestJoin({ relayUrl: fixture.fresh[6].relayURL, ownerId, rootLabel: "second-mac", rootPlatform: "macos" });
  const title = sheet.getByRole("heading", { name: "「second-mac」想成为你的可信设备" });
  await expect(title).toBeVisible({ timeout: 30_000 });
  await expect(sheet).toContainText(verificationCode);

  // Its notification, clicked: it stays in settings; the main window opens nothing.
  type Hook = { hermeshTest: { notifications(): { title: string; click(): void }[] } };
  const notice = "「second-mac」想成为你的可信设备";
  await expect.poll(() => app.evaluate(() => (globalThis as unknown as Hook).hermeshTest.notifications().map((n) => n.title))).toContain(notice);
  await app.evaluate((_, t) => (globalThis as unknown as Hook).hermeshTest.notifications().find((n) => n.title === t)?.click(), notice);
  await expect(title).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);

  await sheet.getByRole("button", { name: "加为可信设备" }).click();
  await expect(settings.getByRole("status").filter({ hasText: sys("在「second-mac」上输入弹出窗口里的两位数字", "在「second-mac」上输入通知里的两位数字") })).toBeVisible({ timeout: 30_000 });
  await expect(sheet).toBeHidden();
  await app.close();
});

test("erasing everything: what it erases, the server's address typed, Touch ID, then the first run says what was done", async () => {
  const fresh = fixture.fresh[9];
  const mac = daemon(fresh.socket);
  await mac.setup({ relayUrl: fresh.relayURL, claimCode: fresh.claimCode, rootLabel: "erase-mac", localMachineName: "erase-mac" });
  const host = new URL(fresh.relayURL).host;
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-"));
  const { app, page } = await launch(fresh.socket, { profile });
  await expectMainWindow(app, page);
  const { settings, main } = await accountPage(app, page);
  await main.getByRole("button", { name: "高级…" }).click();
  await settings.getByRole("dialog", { name: "高级" }).getByRole("button", { name: "全部清空…" }).click();

  // What it erases, by name (this Mac's machine is this Mac), and what stays.
  const sheet = settings.getByRole("dialog", { name: "全部清空 Hermesh？" });
  await expect(sheet).toContainText(sys("erase-mac 这台 Mac"));
  await expect(sheet).toContainText(`自己的服务器 · ${host}`);
  await expect(sheet).toContainText("有未提交改动的 worktree：挪到一边保留");
  // Escape doesn't close it; the button waits for the server's address.
  await settings.keyboard.press("Escape");
  await expect(sheet).toBeVisible();
  const erase = sheet.getByRole("button", { name: "全部清空", exact: true });
  await expect(erase).toBeDisabled();
  await sheet.getByLabel(/清空后不能撤销/).fill(` ${host.toUpperCase()} `);
  // The test signer approves (Touch ID); this Mac is the only one, so it erases itself at once.
  await Promise.all([settings.waitForEvent("close"), erase.click()]);

  // The first run, saying once what was done.
  await expect(page.getByRole("heading", { name: "欢迎使用 Hermesh" })).toBeVisible({ timeout: 30_000 });
  const note = page.getByRole("status").filter({ hasText: "Hermesh 已全部清空" });
  await expect(note).toBeVisible();
  await snap(page, "first-erased");
  expect((await mac.getStatus({})).erased?.eraseId).toBeTruthy();
  await note.getByRole("button", { name: "关闭" }).click();
  await expect(note).toBeHidden();
  await app.close();

  // Closed, it stays closed; the first run is still to be done.
  const again = await launch(fresh.socket, { profile });
  await expect(again.page.getByRole("heading", { name: "欢迎使用 Hermesh" })).toBeVisible();
  await expect(again.page.getByText("Hermesh 已全部清空")).toHaveCount(0);
  await again.app.close();
});

test("a trusted device's environment says so, and removing it removes the device, as in 账号与设备 (docs/settings-environments.md D13)", async () => {
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
  const [settings] = await Promise.all([app.waitForEvent("window"), page.evaluate(() => window.hermesh?.openSettings("env"))]);
  await settings.getByRole("navigation", { name: "设置" }).getByRole("button", { name: /^work-pc/ }).click();
  const env = settings.getByRole("main", { name: "work-pc" });
  await expect(env.locator(".set-hero")).toContainText("可信设备");
  await snap(settings, "settings-machine-trusted-device");
  await env.getByRole("button", { name: "移除…" }).click();
  const dialog = settings.getByRole("dialog", { name: "移除可信设备「work-pc」？" });
  await expect(dialog).toContainText("该设备将立即失去批准权限，其关联的执行环境也会一并移除。");
  await dialog.getByRole("button", { name: "移除" }).click();
  await expect.poll(async () => (await first.listDevices({})).devices.map((d) => d.label), { timeout: 30_000 }).toEqual(["first-mac"]);
  await expect.poll(async () => (await pc.getStatus({})).removed, { timeout: 30_000 }).toBe(true);
  await app.close();
});

/** hermeshd's local API on socket, as another of the owner's devices uses it. */
const daemon = (socket: string) =>
  createClient(LocalService, createConnectTransport({ baseUrl: "http://hermeshd", httpVersion: "1.1", nodeOptions: { socketPath: socket } }));
