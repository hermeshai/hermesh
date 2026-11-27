import { expect, test, type ElectronApplication, type Page } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { once } from "node:events";
import fs from "node:fs";
import http from "node:http";
import type { AddressInfo } from "node:net";
import os from "node:os";
import path from "node:path";
import { answerMenu, cli, fixture, gitRepo, launch, onWindows, pickAnswers, snap, sys, thisComputer } from "./app";

// The settings window: a window of its own (⌘,), laid out as System
// Settings, for what is set up once and changed rarely.

async function openSettings(app: ElectronApplication, page: Page, tab?: string): Promise<Page> {
  const [win] = await Promise.all([app.waitForEvent("window"), page.evaluate((t) => window.hermesh?.openSettings(t), tab)]);
  await win.waitForLoadState();
  return win;
}

async function api<T = Record<string, never>>(page: Page, method: string, body: object = {}): Promise<T> {
  return page.evaluate(
    async ([m, b]) => {
      const r = await fetch(`app://hermesh/api/hermesh.local.v1.LocalService/${m}`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(b) });
      return r.json();
    },
    [method, body] as const,
  );
}

const projects = async (page: Page) => (await api<{ projects?: { path: string; agents?: string[]; workspace?: string }[] }>(page, "ListProjects")).projects ?? [];

const page_ = (settings: Page, name: string) => settings.getByRole("main", { name });
const nav = (settings: Page, name: string) => settings.getByRole("navigation", { name: "设置" }).getByRole("button", { name: new RegExp(`^${name}`) });

test("settings opens on 通用; 授权 lists the grants, one opens its scope, and disconnecting asks first", async () => {
  const { app, page } = await launch();
  const settings = await openSettings(app, page);
  await expect(nav(settings, "通用")).toHaveAttribute("aria-current", "page");
  // Its rows are the main sidebar's: a bare line icon in the text's colour, 18px, on a 32px row; no coloured tiles.
  const side = settings.locator(".set-side");
  await expect(side.locator(".set-tile")).toHaveCount(0);
  for (const row of await side.locator(".nav-i").all()) {
    const looks = await row.evaluate((el) => {
      const ic = el.querySelector(":scope > svg.ic")!;
      return { row: el.getBoundingClientRect().height, ic: ic.getBoundingClientRect().width, colour: getComputedStyle(ic).color === getComputedStyle(el).color };
    });
    expect(looks).toEqual({ row: 32, ic: 18, colour: true });
  }
  const general = page_(settings, "通用");
  await expect(general.getByRole("group", { name: "外观" })).toBeVisible();
  await expect(general.getByRole("combobox", { name: "语言" })).toBeVisible();
  await expect(general.getByRole("switch", { name: "自动下载更新" })).toBeVisible();
  // Starting at login is this Mac's Stay Online now (docs/settings-environments.md §5.4).
  await expect(general).not.toContainText("登录时打开");
  await snap(settings, "settings-general");

  // Notifications, a page of their own.
  await nav(settings, "通知").click();
  const notify = page_(settings, "通知");
  await expect(notify.getByRole("switch", { name: "连接请求" })).toHaveAttribute("aria-checked", "true");
  await expect(notify.getByRole("switch", { name: "新任务" })).toHaveAttribute("aria-checked", "true");
  const turnDone = notify.getByRole("switch", { name: "一轮完成" });
  await expect(turnDone).toHaveAttribute("aria-checked", "false");
  await turnDone.click();
  await expect(turnDone).toHaveAttribute("aria-checked", "true");
  // Kept by the main process, which posts the notifications.
  expect((await page.evaluate(() => window.hermesh?.getPreferences()))?.notifyTurnDone).toBe(true);
  // Named after where the icon is on this platform: the menu bar, the notification area, the system tray.
  const badge = process.platform === "win32" ? "通知区域图标显示圆点" : process.platform === "linux" ? "系统托盘图标显示圆点" : "菜单栏图标显示圆点";
  await expect(notify.getByRole("switch", { name: badge })).toHaveAttribute("aria-checked", "true");
  await snap(settings, "settings-notifications");

  await nav(settings, "授权").click();
  await expect(settings).toHaveTitle("授权");
  const grants = page_(settings, "授权");
  const bot = grants.getByRole("list", { name: "授权" }).getByRole("button", { name: /bot/ });
  // A row says when it was last used, which tells grants apart (the bot started the fixture's sessions).
  await expect(bot).toContainText(/(刚刚|分钟前|小时前)用过/);
  await expect(bot).toContainText("还剩 30 天");
  await snap(settings, "settings-grants");

  // Adding happens in a sheet over the settings window: the first run's connect page.
  await grants.getByRole("button", { name: "连接 agent…" }).click();
  await expect(settings.getByRole("dialog", { name: "连接你的 Agent" })).toBeVisible();
  await settings.keyboard.press("Escape");
  await expect(settings.getByRole("dialog")).toHaveCount(0);

  await bot.click();
  await expect(grants).toContainText("所有项目（包括以后添加的）");
  await expect(grants).toContainText("Gemini CLI");
  await expect(grants).toContainText("还剩 30 天");
  // The rarely needed, folded.
  await expect(grants).not.toContainText(/今天 \d+ 个/);
  await grants.getByRole("button", { name: "更多" }).click();
  await expect(grants).toContainText(/今天 \d+ 个 · 一共 \d+ 个/);
  await snap(settings, "settings-grant");

  await grants.getByRole("button", { name: "断开…" }).click();
  const dialog = settings.getByRole("dialog", { name: "断开「bot」？" });
  await expect(dialog).toContainText("它会立即失去访问");
  await snap(settings, "settings-grants-revoke");
  await dialog.getByRole("button", { name: "取消" }).click();
  await expect(dialog).toBeHidden();

  await grants.getByRole("button", { name: "修改范围…" }).click();
  await expect(settings.getByRole("dialog", { name: "修改「bot」能用什么" })).toBeVisible();
  await settings.keyboard.press("Escape");
  await expect(settings.getByRole("dialog")).toHaveCount(0);

  // ‹ goes back to the list.
  await settings.getByRole("button", { name: "返回授权" }).click();
  await expect(bot).toBeVisible();
  await bot.click();

  // The main window's search, narrowed to this grant.
  await grants.getByRole("button", { name: "更多" }).click();
  await grants.getByRole("button", { name: "在主窗口里查看" }).click();
  const palette = page.getByRole("dialog", { name: "搜索任务" });
  await expect(palette.getByRole("combobox", { name: "授权" })).toHaveValue(/.+/);
  await expect(palette.getByRole("combobox", { name: "授权" }).locator("option:checked")).toHaveText("bot");
  await expect(palette.getByRole("listbox", { name: "搜索结果" }).getByRole("option").first()).toBeVisible();
  await app.close();
});

test("an agent's name and icon change together: an app's logo, or a picture of the owner's, kept in its grant and shown in the list", async () => {
  const { app, page } = await launch();
  const settings = await openSettings(app, page, "grants");
  const grants = page_(settings, "授权");
  const list = grants.getByRole("list", { name: "授权" });
  await list.getByRole("button", { name: /bot/ }).click();
  const grant = async () => (await api<{ clients?: { label: string; icon?: string; image?: string }[] }>(settings, "ListClients")).clients?.find((c) => /bot/i.test(c.label));

  // Clicking its face opens the sheet; an app picked before the name is typed names it too.
  await grants.locator(".dhero-av").click();
  const dialog = settings.getByRole("dialog", { name: "名字和图标" });
  const name = dialog.getByRole("textbox", { name: "名字" });
  await expect(name).toHaveValue("bot");
  await expect(dialog.getByRole("button", { name: "自动" })).toHaveAttribute("aria-pressed", "true");
  await dialog.getByRole("button", { name: "Grok Bot" }).click();
  await expect(name).toHaveValue("Grok Bot");
  await name.fill("bot of mine");
  await dialog.getByRole("button", { name: "Cursor" }).click();
  await expect(name).toHaveValue("bot of mine");
  await dialog.getByRole("button", { name: "Grok Bot" }).click();
  await snap(settings, "settings-grant-look");
  await dialog.getByRole("button", { name: "存储" }).click();
  await expect(dialog).toBeHidden();
  await expect(grants.locator(".dhero")).toContainText("bot of mine");
  expect(await grant()).toMatchObject({ label: "bot of mine", icon: "app:grokbot" });

  // A picture of the owner's: not square, cut to its middle at 128 px.
  await grants.getByRole("button", { name: "名字和图标…" }).click();
  await expect(dialog.getByRole("button", { name: "Grok Bot" })).toHaveAttribute("aria-pressed", "true");
  const png = await settings.evaluate(async () => {
    const c = document.createElement("canvas");
    c.width = 300;
    c.height = 200;
    const g = c.getContext("2d")!;
    g.fillStyle = "#2a7";
    g.fillRect(0, 0, 300, 200);
    g.fillStyle = "#fc3";
    g.fillRect(100, 50, 100, 100);
    return c.toDataURL("image/png").split(",")[1];
  });
  await dialog.locator("input[type=file]").setInputFiles({ name: "me.png", mimeType: "image/png", buffer: Buffer.from(png, "base64") });
  await expect(dialog.getByRole("button", { name: "你的图片" })).toHaveAttribute("aria-pressed", "true");
  await dialog.getByRole("button", { name: "存储" }).click();
  await expect(dialog).toBeHidden();
  const pic = await grant();
  expect(pic?.icon).toBe("image");
  const kept = Buffer.from(pic!.image!, "base64"); // a PNG: its IHDR says width and height
  expect([kept.toString("latin1", 1, 4), kept.readUInt32BE(16), kept.readUInt32BE(20)]).toEqual(["PNG", 128, 128]);
  await expect(grants.locator(".dhero img.gpic")).toBeVisible();
  await settings.getByRole("button", { name: "返回授权" }).click();
  await expect(list.getByRole("button", { name: /bot of mine/ }).locator("img.gpic")).toBeVisible();
  await snap(settings, "settings-grants-picture");

  // Back as it was, for the other tests: its name, its icon automatic.
  await list.getByRole("button", { name: /bot of mine/ }).click();
  await grants.getByRole("button", { name: "名字和图标…" }).click();
  await name.fill("bot");
  await dialog.getByRole("button", { name: "自动" }).click();
  await dialog.getByRole("button", { name: "存储" }).click();
  await expect(dialog).toBeHidden();
  expect(await grant()).toMatchObject({ label: "bot" });
  expect((await grant())?.icon ?? "").toBe("");
  await app.close();
});

test("only an agent that asked to be woken has a wake group: its link opens the setup, a pasted block split, a wrong key caught, then it wakes for every name, shows its rings, and stops", async () => {
  // The bot's routine: a webhook that takes one key.
  const calls: { type?: string; as?: string; raw: string }[] = [];
  const server = http.createServer((req, res) => {
    let raw = "";
    req.on("data", (c) => (raw += c));
    req.on("end", () => {
      calls.push({ ...JSON.parse(raw || "{}"), raw });
      res.statusCode = req.headers.authorization === "Bearer crsr_test" ? 200 : 401;
      res.end();
    });
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}/hook`;
  const { app, page } = await launch();
  // The bot asks to be woken (message wake); its owner opens the link it gives, as macOS hands the app a hermesh:// link.
  const askedLink = async (...as: string[]) => {
    const link: string = JSON.parse(await cli(...as, "message", "wake", "-o", "json")).wake.link;
    expect(link).toMatch(/^hermesh:\/\/wake\?code=/);
    await app.evaluate(({ app }, l) => app.emit("open-url", { preventDefault() {} }, l), link);
  };
  try {
    const settings = await openSettings(app, page);
    const grants = page_(settings, "授权");
    const openBot = async () => {
      await nav(settings, "通用").click();
      await nav(settings, "授权").click();
      await grants.getByRole("list", { name: "授权" }).getByRole("button", { name: /bot/ }).click();
      await expect(grants.getByText("能用什么")).toBeVisible();
    };
    await openBot();
    const wake = grants.getByRole("list", { name: "新消息提醒" });
    // Nothing to set up from here: not every agent can be woken.
    await expect(wake).toHaveCount(0);
    await expect(grants.getByRole("button", { name: "设置…" })).toHaveCount(0);

    await askedLink();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("第 1 步，共 2 步");
    await expect(dialog.getByRole("heading", { name: "叫醒 bot" })).toBeVisible();
    await expect(dialog).toContainText("bot 已经建好一个只由 Webhook 触发的定时任务");
    await snap(page, "main-wake-link");
    // Grok's Webhook section pasted whole: the URL and key go to their boxes.
    await dialog.getByLabel("地址（POST to）").fill(`POST to\n${url}\nkey\nwrong`);
    await expect(dialog.getByLabel("地址（POST to）")).toHaveValue(url);
    await expect(dialog.getByLabel("密钥（key）")).toHaveValue("wrong");
    // A wrong key: the first ring says so, and nothing is kept.
    await dialog.getByRole("button", { name: "叫醒一次试试" }).click();
    await expect(dialog).toContainText("没叫醒");
    await expect(dialog).toContainText("对方回复 401：密钥不对");
    await dialog.getByRole("button", { name: "返回修改" }).click();
    await dialog.getByLabel("密钥（key）").fill("crsr_test");
    await dialog.getByRole("button", { name: "叫醒一次试试" }).click();
    await expect(dialog).toContainText("设置好了");
    await expect(dialog).toContainText("以后有新消息，Hermesh 都会这样叫醒它");
    expect(calls.map((c) => c.type)).toEqual(["hermesh.check", "hermesh.check"]);
    await snap(page, "main-wake-ok");
    await dialog.getByRole("button", { name: "完成" }).click();

    await openBot();
    const row = wake.getByRole("button", { name: /叫醒 bot/ });
    await expect(row).toContainText("已开");
    await expect(row).toContainText("叫醒过");
    await snap(settings, "settings-wake-listed");

    // A message for a bot of it without a doorbell of its own wakes it, naming
    // the bot and nothing more.
    await cli("message", "send", "--to", "bot/Tech Demos", "--message", "the review is ready");
    await expect.poll(() => calls.find((c) => c.type === "hermesh.message")?.as, { timeout: 30_000 }).toBe("tech-demos");
    expect(calls.map((c) => c.raw).join()).not.toContain("the review is ready");

    // Its rings, newest first; then it stops, and the group goes with it.
    await row.click();
    const sheet = settings.getByRole("dialog");
    await expect(sheet.getByRole("heading", { name: "叫醒 bot" })).toBeVisible();
    const recent = sheet.getByRole("list", { name: "最近几次" });
    await expect(recent.getByRole("listitem").first()).toContainText("tech-demos 有新消息 · 收到了");
    await expect(recent).toContainText("试一次 · 收到了");
    await snap(settings, "settings-wake-manage");
    await sheet.getByRole("button", { name: "停止叫醒…" }).click();
    await settings.getByRole("dialog", { name: "不再叫醒 bot？" }).getByRole("button", { name: "停止叫醒" }).click();
    await expect(wake).toHaveCount(0);

    // One bot of several sharing the connection asks for itself: its link names it.
    await askedLink("--as", "Research");
    await expect(dialog.getByRole("heading", { name: "叫醒 research" })).toBeVisible();
    await dialog.getByLabel("地址（POST to）").fill(`${url} crsr_test`);
    await dialog.getByRole("button", { name: "叫醒一次试试" }).click();
    await expect(dialog).toContainText("设置好了");
    await dialog.getByRole("button", { name: "完成" }).click();
    await openBot();
    await expect(wake.getByRole("button", { name: /叫醒 research/ })).toBeVisible();
    await expect(wake.getByRole("button")).toHaveCount(1);
  } finally {
    await app.close();
    server.close();
  }
});

const pipePrefix = "\\\\.\\pipe\\";

// The Windows fixture points to a file holding the daemon's pipe name. Read it
// for each request because the name changes when hermeshd restarts.
const daemonEndpoint = () => (onWindows && !fixture.socket.startsWith(pipePrefix) ? fs.readFileSync(fixture.socket, "utf8").trim() : fixture.socket);

async function listenDaemonProxy(proxy: http.Server): Promise<string> {
  // Keep Unix socket paths short even when TMPDIR is deeply nested.
  const socket = onWindows ? `${pipePrefix}hms-${randomUUID()}` : path.join(fs.mkdtempSync("/tmp/hms-"), "d.sock");
  await once(proxy.listen(socket), "listening");
  return socket;
}

test("an app just updated finds the previous hermeshd, which keeps no doorbells; once that is restarted, the agent's page has them", async () => {
  // The daemon through a proxy: first as the previous version (no
  // ListWakes), then, its connections dropped as a restart drops them, as
  // itself.
  let previous = true;
  const proxy = http.createServer((req, res) => {
    if (previous && req.url?.endsWith("/ListWakes")) {
      res.writeHead(404, { "content-type": "application/json" });
      res.end(JSON.stringify({ code: "unimplemented", message: "not in this version" }));
      return;
    }
    const up = http.request({ socketPath: daemonEndpoint(), path: req.url, method: req.method, headers: req.headers }, (r) => {
      res.writeHead(r.statusCode ?? 502, r.headers);
      r.pipe(res);
    });
    up.on("error", () => res.destroy());
    res.on("close", () => up.destroy());
    req.pipe(up);
  });
  const { app, page } = await launch(await listenDaemonProxy(proxy));
  // The bot has a doorbell (kept though nothing answers its webhook).
  const bot = (await api<{ clients: { clientId: string; label: string }[] }>(page, "ListClients")).clients.find((c) => c.label === "bot")!.clientId;
  await api(page, "SetWake", { clientId: bot, url: "http://127.0.0.1:9/hook", keep: true });
  try {
    const settings = await openSettings(app, page);
    await nav(settings, "授权").click();
    const grants = page_(settings, "授权");
    await grants.getByRole("list", { name: "授权" }).getByRole("button", { name: /bot/ }).click();
    await expect(grants.getByRole("button", { name: "修改范围…" })).toBeVisible();
    await expect(grants.getByRole("list", { name: "新消息提醒" })).toHaveCount(0);

    previous = false;
    proxy.closeAllConnections();
    await expect(grants.getByRole("list", { name: "新消息提醒" }).getByRole("button", { name: /叫醒 bot/ })).toBeVisible();
  } finally {
    await api(page, "ClearWake", { clientId: bot }).catch(() => {});
    await app.close();
    proxy.close();
  }
});

test("settings 环境: this Mac's page names it, keeps it online, opens its providers and projects, and pauses it", async () => {
  const { app, page } = await launch();
  const settings = await openSettings(app, page, "env");
  await expect(nav(settings, thisComputer)).toHaveAttribute("aria-current", "page");
  const panel = page_(settings, thisComputer);
  await expect(panel.locator(".set-hero")).toContainText("owner-mac");
  await expect(panel.locator(".set-hero")).toContainText(/Hermesh \d/);
  await expect(panel.locator(".set-pill")).toContainText("在线");
  // Remove is for other environments; this Mac stops being one instead.
  await expect(panel.getByRole("button", { name: "移除…" })).toHaveCount(0);
  await expect(panel.getByRole("button", { name: sys("停用这台 Mac…", "停用这台电脑…") })).toBeVisible();
  await snap(settings, "settings-env");

  // The name, changed in place, as hermeshd keeps it.
  const name = panel.getByRole("textbox", { name: "名称" });
  await expect(name).toHaveValue("owner-mac");
  await name.fill("我的电脑");
  await expect(panel).toContainText("名字里没有字母或数字");
  await name.press("Escape");
  await expect(name).toHaveValue("owner-mac");
  await name.fill("办公室 studio-mac");
  await expect(panel).toContainText("将保存为「studio-mac」");
  await name.press("Enter");
  await expect(panel.locator(".set-hero b")).toHaveText("studio-mac");
  await name.fill("owner-mac");
  await name.press("Enter");
  await expect(panel.locator(".set-hero b")).toHaveText("owner-mac");

  // Stay online: on, the login item with it; off asks first and says what follows.
  const online = panel.getByRole("switch", { name: "保持在线" });
  await expect(online).toHaveAttribute("aria-checked", "true");
  await expect(panel).toContainText("开机后自动在后台运行");
  expect((await page.evaluate(() => window.hermesh?.loginItem()))?.on).toBe(true);
  await online.click();
  const off = settings.getByRole("dialog", { name: "要关闭「保持在线」吗？" });
  await expect(off).toBeVisible();
  await off.getByRole("button", { name: "关闭" }).click();
  await expect(online).toHaveAttribute("aria-checked", "false");
  await expect(panel).toContainText("只有 Hermesh 打开时");
  expect((await page.evaluate(() => window.hermesh?.getPreferences()))?.stayOnline).toBe(false);
  expect((await page.evaluate(() => window.hermesh?.loginItem()))?.on).toBe(false);
  await online.click();
  await expect(online).toHaveAttribute("aria-checked", "true");
  expect((await page.evaluate(() => window.hermesh?.loginItem()))?.on).toBe(true);

  // Agent providers: the installed ones, each opening its page; the others not installed.
  const entries = panel.getByRole("list", { name: "owner-mac" });
  await expect(entries.getByRole("listitem", { name: "Agent 提供方" })).toContainText(/\d+ 个可用/, { timeout: 30_000 });
  await entries.getByRole("listitem", { name: "Agent 提供方" }).click();
  const providers = page_(settings, "Agent 提供方");
  const gemini = providers.getByRole("list", { name: "Agent 提供方" }).getByRole("listitem", { name: "Gemini CLI" });
  await expect(gemini).toContainText("已登录", { timeout: 30_000 });
  await expect(providers.getByRole("list", { name: "未安装" }).getByRole("listitem").first()).toContainText("安装后会自动出现在这里");
  await snap(settings, "settings-providers");
  await gemini.click();
  await expect(page_(settings, "Gemini CLI").getByRole("region", { name: "Gemini CLI" })).toContainText("owner-mac");
  // No projects of its own: projects are the environment's.
  await expect(page_(settings, "Gemini CLI")).not.toContainText("添加项目");
  await settings.getByRole("button", { name: "返回Agent 提供方" }).click();
  await settings.getByRole("button", { name: `返回${thisComputer}` }).click();
  await expect(entries).toBeVisible();

  // Projects: for every provider. + adds a picked folder, its way of working a pop-up, − removes it, asking first.
  await entries.getByRole("listitem", { name: "项目" }).click();
  const list = page_(settings, "项目").getByRole("listbox", { name: "项目" });
  await expect(list.getByRole("option", { name: "demo" })).toBeVisible();
  const repo = gitRepo(`app-${process.pid}`);
  await pickAnswers(app, repo);
  await page_(settings, "项目").getByRole("button", { name: "添加项目…" }).click();
  const added = list.getByRole("option", { name: `app-${process.pid}` });
  await expect(added).toContainText(repo);
  await expect(added.getByRole("combobox")).toHaveValue("worktree");
  expect((await projects(settings)).find((p) => p.path === repo)?.agents ?? []).toEqual([]);
  await added.getByRole("combobox").selectOption("inPlace");
  await expect.poll(async () => (await projects(settings)).find((p) => p.path === repo)?.workspace).toBe("WORKSPACE_IN_PLACE");
  await snap(settings, "settings-projects");
  // Added, it is the one selected, as in Login Items.
  await expect(added).toHaveAttribute("aria-selected", "true");
  await page_(settings, "项目").getByRole("button", { name: "移除", exact: true }).click();
  const removing = settings.getByRole("dialog", { name: `移除项目「app-${process.pid}」？` });
  await expect(removing).toContainText("目录本身不会被删除");
  await removing.getByRole("button", { name: "移除" }).click();
  await expect(added).toHaveCount(0);
  expect((await projects(settings)).some((p) => p.path === repo)).toBe(false);
  expect(fs.existsSync(repo)).toBe(true);
  await settings.getByRole("button", { name: `返回${thisComputer}` }).click();

  // Pausing: a banner says so, and the sidebar.
  await panel.getByRole("button", { name: "暂停接受新任务" }).click();
  await expect(settings.getByRole("status").filter({ hasText: "「owner-mac」暂停接受新任务" })).toBeVisible();
  await expect(panel.locator(".set-banner")).toContainText("已暂停接受新任务");
  await expect(nav(settings, thisComputer)).toContainText("已暂停");
  await snap(settings, "settings-env-paused");
  await panel.locator(".set-banner").getByRole("button", { name: "恢复" }).click();
  await expect(panel.locator(".set-banner")).toHaveCount(0);
  await expect(nav(settings, thisComputer)).not.toContainText("已暂停");

  // Its sessions' permission limit (docs/thread-composer.md §5.4): picked from a menu, kept on the machine.
  const limit = panel.getByTestId("machine-max-mode");
  await expect(limit).toHaveText("不限制");
  await answerMenu(app, "2"); // RuntimeMode.AUTO_EDITS
  await limit.click();
  await expect(settings.getByRole("status")).toContainText("已把 owner-mac 的会话权限上限设为「自动接受编辑」");
  await expect(limit).toHaveText("自动接受编辑");
  const machines = await api<{ machines: { local?: boolean; status?: { maxRuntimeMode?: string } }[] }>(settings, "ListMachines");
  expect(machines.machines.find((m) => m.local)?.status?.maxRuntimeMode).toBe("RUNTIME_MODE_AUTO_EDITS");
  await answerMenu(app, "0");
  await limit.click();
  await expect(limit).toHaveText("不限制");

  // Stopping this Mac as an environment asks first.
  await panel.getByRole("button", { name: sys("停用这台 Mac…", "停用这台电脑…") }).click();
  const disable = settings.getByRole("dialog", { name: sys("要停用这台 Mac 吗？", "要停用这台电脑吗？") });
  await expect(disable).toContainText("这里的任务会停下");
  await disable.getByRole("button", { name: "取消" }).click();
  await expect(disable).toBeHidden();

  // An old link to the machine lands here; + offers adding another computer.
  await page.evaluate(() => window.hermesh?.openSettings("machines"));
  await expect(nav(settings, thisComputer)).toHaveAttribute("aria-current", "page");
  await app.close();
});

test("settings 环境: Codex's page sets the model, effort and speed its sessions run with, its own until set, for Hermesh only", async () => {
  test.skip(onWindows, "the fixture's Codex is a shell script");
  const { app, page } = await launch();
  const settings = await openSettings(app, page, "env");
  const panel = page_(settings, thisComputer);
  await panel.getByRole("list", { name: "owner-mac" }).getByRole("listitem", { name: "Agent 提供方" }).click();
  const codex = page_(settings, "Agent 提供方").getByRole("list", { name: "Agent 提供方" }).getByRole("listitem", { name: "Codex" });
  await expect(codex).toContainText("还没登录", { timeout: 30_000 });
  await codex.click();
  const row = (name: string) => page_(settings, "Codex").getByRole("region", { name: "Codex" }).getByRole("group", { name });
  // Each follows Codex, saying what that is.
  await expect(row("模型")).toContainText("跟随 Codex（GPT-Fake-Big）");
  await expect(row("推理强度")).toContainText("跟随 Codex（高）");
  await expect(row("速度")).toContainText("跟随 Codex（标准）");
  await snap(settings, "settings-agent");

  // Each row's button opens the choices for that row alone, Codex's own first.
  const pick = async (name: string, id: string) => {
    await app.evaluate(({ ipcMain }, id) => {
      ipcMain.removeHandler("popup-menu");
      ipcMain.handle("popup-menu", (_e, items: { label?: string; checked?: boolean }[]) => {
        (globalThis as unknown as { shown: unknown }).shown = items.map((i) => (i.checked ? "✓ " : "") + i.label);
        return id;
      });
    }, id);
    await row(name).getByRole("button").click();
    return app.evaluate(() => (globalThis as unknown as { shown: unknown }).shown);
  };
  expect(await pick("模型", "gpt-fake-small")).toEqual(["✓ 跟随 Codex（GPT-Fake-Big）", "GPT-Fake-Big", "GPT-Fake-Small"]);
  await expect(row("模型")).toHaveText("模型GPT-Fake-Small");
  expect(await pick("推理强度", "low")).toEqual(["✓ 跟随 Codex（高）", "低", "高"]);
  expect(await pick("速度", "2")).toEqual(["✓ 跟随 Codex（标准）", "标准", "快速"]);
  await expect(row("推理强度")).toHaveText("推理强度低");
  await expect(row("速度")).toHaveText("速度快速");
  await snap(settings, "settings-agent-set");
  const codexStatus = async () => {
    const r = await api<{ machines?: { status?: { agents?: { name: string; defaults?: object }[] } }[] }>(settings, "ListMachines");
    return r.machines?.[0]?.status?.agents?.find((a) => a.name === "codex");
  };
  expect((await codexStatus())?.defaults).toEqual({ agent: "codex", model: "gpt-fake-small", effort: "low", speed: "AGENT_SPEED_FAST" });

  // Back to Codex's own, one by one.
  expect(await pick("模型", "")).toContain("✓ GPT-Fake-Small");
  await pick("推理强度", "");
  await pick("速度", "0");
  await expect(row("模型")).toContainText("跟随 Codex（GPT-Fake-Big）");
  await expect(row("速度")).toContainText("跟随 Codex（标准）");
  expect((await codexStatus())?.defaults).toEqual({ agent: "codex" });
  await app.close();
});

test("settings 账号与设备 shows the account, the devices and, folded, the addresses", async () => {
  const { app, page } = await launch();
  const status = await page.evaluate(() =>
    fetch("app://hermesh/api/hermesh.local.v1.LocalService/GetStatus", { method: "POST", headers: { "content-type": "application/json" }, body: "{}" }).then((r) => r.json()),
  );
  const settings = await openSettings(app, page, "account");
  const panel = page_(settings, "账号与设备");
  const devices = panel.getByRole("list", { name: "设备" });
  // This computer first, by its own name (docs/cold-start-prd.md §7.2).
  await expect(devices.getByRole("listitem").first()).toContainText(thisComputer);
  await expect(devices.getByRole("listitem").first()).not.toContainText("—");
  await expect(devices.getByRole("button", { name: "移除…" })).toHaveCount(0);
  await expect(panel).not.toContainText(status.ownerId);
  await snap(settings, "settings-account");

  await panel.getByRole("button", { name: "添加设备…" }).click();
  await expect(settings.getByRole("dialog", { name: "添加设备" })).toBeVisible();
  await settings.keyboard.press("Escape");
  await expect(settings.getByRole("dialog")).toHaveCount(0);

  await panel.getByRole("button", { name: "高级…" }).click();
  const advanced = settings.getByRole("dialog", { name: "高级" });
  await expect(advanced).toContainText(status.ownerId);
  await expect(advanced).toContainText("已连接");
  await expect(advanced).toContainText(`hermeshd pair ${status.connectAddress}`);
  await expect(advanced).toContainText(`hermesh auth login ${status.connectAddress}`);
  await expect(advanced.getByRole("button", { name: "全部清空…" })).toBeVisible();
  await advanced.getByRole("button", { name: "拷贝身份 ID" }).click();
  await expect(settings.getByRole("status")).toContainText("已拷贝");
  expect(await app.evaluate(({ clipboard }) => clipboard.readText())).toBe(status.ownerId);
  // This server's MCP address, with its key in it.
  expect(status.mcpUrl).toMatch(/\/mcp\/[0-9a-z]{26}$/);
  await advanced.getByRole("button", { name: "拷贝MCP 地址" }).click();
  await expect.poll(() => app.evaluate(({ clipboard }) => clipboard.readText())).toBe(status.mcpUrl);
  await snap(settings, "settings-account-advanced");
  await advanced.getByRole("button", { name: "完成" }).click();
  await expect(advanced).toBeHidden();

  // Signing out asks first, and says what stays (signing out itself: onboarding.spec.ts); cancelled here.
  await panel.getByRole("button", { name: "退出登录…" }).click();
  const dialog = settings.getByRole("dialog", { name: "要退出登录吗？" });
  await expect(dialog).toContainText("项目、任务和 Her 都会保留");
  await dialog.getByRole("button", { name: "取消" }).click();
  await expect(dialog).toBeHidden();
  await expect(panel).toContainText("已连接");

  // Another page opens in the same window; the old 可信设备 link lands here too.
  await page.evaluate(() => window.hermesh?.openSettings("general"));
  await expect(nav(settings, "通用")).toHaveAttribute("aria-current", "page");
  await page.evaluate(() => window.hermesh?.openSettings("devices"));
  await expect(nav(settings, "账号与设备")).toHaveAttribute("aria-current", "page");
  expect(app.windows()).toHaveLength(2);
  await Promise.all([settings.waitForEvent("close"), settings.keyboard.down("Escape")]); // closes the window
  expect(app.windows()).toHaveLength(1);
  await app.close();
});

test("通用's 外观 follows the system (remembering the chosen light and dark pair), previews on hover, and keeps glass vs solid styles across launches", async () => {
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-"));
  let { app, page } = await launch(undefined, { profile, systemColors: true });
  const dark = (p: Page) => p.evaluate(() => matchMedia("(prefers-color-scheme: dark)").matches);
  const lightStyle = (p: Page) => p.evaluate(() => document.documentElement.dataset.lightStyle);
  const darkStyle = (p: Page) => p.evaluate(() => document.documentElement.dataset.darkStyle);
  const vibrant = (p: Page) => p.evaluate(() => document.documentElement.classList.contains("vibrant"));
  const hasMaterial = await page.evaluate(() => !!window.hermesh?.material);
  const source = () => app.evaluate(({ nativeTheme }) => nativeTheme.themeSource);
  let settings = await openSettings(app, page, "general");
  const appearance = () => page_(settings, "通用").getByRole("group", { name: "外观" });
  await expect(appearance().getByRole("button")).toHaveText(["跟随系统", "浅色", "柔白", "深色", "暗黑"]);
  await expect(appearance().getByRole("button", { name: "跟随系统" })).toHaveAttribute("aria-pressed", "true");
  expect(await source()).toBe("system");

  // Hovering previews across both windows without committing, and leaving restores the saved choice.
  await appearance().getByRole("button", { name: "柔白" }).hover();
  await expect.poll(() => lightStyle(page)).toBe("soft");
  await expect.poll(() => vibrant(page)).toBe(false);
  await expect(settings.locator(".set-main")).toHaveCSS("background-color", "rgb(244, 245, 247)");
  await expect(appearance().getByRole("button", { name: "跟随系统" })).toHaveAttribute("aria-pressed", "true");
  await settings.mouse.move(4, 4);
  await expect.poll(source).toBe("system");

  await appearance().getByRole("button", { name: "深色" }).click();
  await expect(appearance().getByRole("button", { name: "深色" })).toHaveAttribute("aria-pressed", "true");
  await expect.poll(() => dark(page)).toBe(true);
  await expect.poll(() => dark(settings)).toBe(true);
  await expect.poll(() => darkStyle(page)).toBe("dark");
  await expect.poll(() => vibrant(page)).toBe(hasMaterial);
  await expect(settings.locator(".set-main")).toHaveCSS("background-color", "rgb(30, 30, 33)");
  await snap(settings, "settings-appearance-dark");

  await appearance().getByRole("button", { name: "暗黑" }).click();
  await expect(appearance().getByRole("button", { name: "暗黑" })).toHaveAttribute("aria-pressed", "true");
  await expect.poll(() => dark(page)).toBe(true);
  await expect.poll(() => darkStyle(page)).toBe("black");
  await expect.poll(() => darkStyle(settings)).toBe("black");
  await expect.poll(() => vibrant(page)).toBe(false);
  await expect.poll(() => vibrant(settings)).toBe(false);
  await expect(settings.locator(".set-main")).toHaveCSS("background-color", "rgb(7, 7, 9)");
  await expect(page.locator(".side")).toHaveCSS("background-color", "rgb(17, 17, 20)");

  // Kept for next time, from the very first frame (including 暗黑 with solid sidebar).
  await app.close();
  ({ app, page } = await launch(undefined, { profile, systemColors: true }));
  expect(await source()).toBe("dark");
  expect(await dark(page)).toBe(true);
  expect(await darkStyle(page)).toBe("black");
  expect(await vibrant(page)).toBe(false);
  await expect(page.locator(".side")).toHaveCSS("background-color", "rgb(17, 17, 20)");
  settings = await openSettings(app, page, "general");
  await expect(appearance().getByRole("button", { name: "暗黑" })).toHaveAttribute("aria-pressed", "true");
  await expect(settings.locator(".set-main")).toHaveCSS("background-color", "rgb(7, 7, 9)");

  // FinxWork soft paper light ("柔白"): opaque #eef0f4 sidebar, #f4f5f7 ground, persisted across restarts.
  await appearance().getByRole("button", { name: "柔白" }).click();
  await expect(appearance().getByRole("button", { name: "柔白" })).toHaveAttribute("aria-pressed", "true");
  await expect.poll(() => dark(page)).toBe(false);
  await expect.poll(() => lightStyle(page)).toBe("soft");
  await expect.poll(() => vibrant(page)).toBe(false);
  await expect.poll(() => vibrant(settings)).toBe(false);
  await expect(settings.locator(".set-main")).toHaveCSS("background-color", "rgb(244, 245, 247)");
  await expect(page.locator(".side")).toHaveCSS("background-color", "rgb(238, 240, 244)");

  await app.close();
  ({ app, page } = await launch(undefined, { profile, systemColors: true }));
  expect(await source()).toBe("light");
  expect(await dark(page)).toBe(false);
  expect(await lightStyle(page)).toBe("soft");
  expect(await vibrant(page)).toBe(false);
  await expect(page.locator(".side")).toHaveCSS("background-color", "rgb(238, 240, 244)");
  settings = await openSettings(app, page, "general");
  await expect(appearance().getByRole("button", { name: "柔白" })).toHaveAttribute("aria-pressed", "true");
  await expect(settings.locator(".set-main")).toHaveCSS("background-color", "rgb(244, 245, 247)");

  // Crisp light ("浅色") restores sidebar vibrancy, and "跟随系统" remembers the chosen pair.
  await appearance().getByRole("button", { name: "浅色" }).click();
  await expect.poll(() => lightStyle(page)).toBe("light");
  await expect.poll(() => vibrant(page)).toBe(hasMaterial);
  await expect(settings.locator(".set-main")).toHaveCSS("background-color", "rgb(255, 255, 255)");

  await appearance().getByRole("button", { name: "柔白" }).click();
  await appearance().getByRole("button", { name: "跟随系统" }).click();
  await expect.poll(source).toBe("system");
  await expect(settings.locator(".sys-win.day.m-soft")).toBeVisible();
  await expect(settings.locator(".sys-win.night.m-black")).toBeVisible();
  await app.close();
});

test("nothing signed: a signer older than the app offers its update, else the failure says nothing was signed", async () => {
  // hermeshd as it answers when its signer didn't sign a rename: as an older signer does with a request it can't show.
  const proxy = http.createServer((req, res) => {
    if (req.url?.endsWith("/RenameMachine")) {
      res.writeHead(500, { "content-type": "application/json" });
      res.end(JSON.stringify({ code: "unknown", message: "the owner declined to sign" }));
      return;
    }
    const up = http.request({ socketPath: daemonEndpoint(), path: req.url, method: req.method, headers: req.headers }, (r) => {
      res.writeHead(r.statusCode ?? 502, r.headers);
      r.pipe(res);
    });
    up.on("error", () => res.destroy());
    res.on("close", () => up.destroy());
    req.pipe(up);
  });
  const { app, page } = await launch(await listenDaemonProxy(proxy));
  // The signer set up here (Windows: a copy in Program Files), older than the app or not; updating it is counted.
  const signer = (version?: string) =>
    app.evaluate(({ ipcMain }, v) => {
      const g = globalThis as unknown as { updates: number };
      g.updates = 0;
      ipcMain.removeHandler("signer-update");
      ipcMain.handle("signer-update", () => ({ value: v }));
      ipcMain.removeHandler("update-signer");
      ipcMain.handle("update-signer", () => ({ value: void g.updates++ }));
    }, version);
  try {
    const settings = await openSettings(app, page, "env");
    const panel = page_(settings, thisComputer);
    const rename = async () => {
      const field = panel.getByRole("textbox", { name: "名称" });
      await field.fill("studio-mac");
      await field.press("Enter");
    };

    await signer("0.0.1");
    await rename();
    const update = settings.getByRole("dialog", { name: "更新 Hermesh 签名器" });
    await expect(update).toContainText("这台电脑的签名器（0.0.1）比 Hermesh 旧");
    await snap(settings, "settings-signer-older");
    await update.getByRole("button", { name: "更新签名器" }).click();
    await expect(settings.getByRole("status")).toContainText("签名器已更新，请再试一次");
    await expect(update).toBeHidden();
    expect(await app.evaluate(() => (globalThis as unknown as { updates: number }).updates)).toBe(1);

    await signer(undefined);
    await rename();
    const toast = settings.getByRole("status");
    await expect(toast).toContainText("没有签名：请求在签名器里被拒绝了，或者没有完成确认。");
    await expect(toast).not.toContainText("declined");
    await expect(settings.getByRole("dialog")).toHaveCount(0);
    await expect(panel.locator(".set-hero b")).toHaveText("owner-mac");
    // What failed to save goes back to the name it has.
    await expect(panel.getByRole("textbox", { name: "名称" })).toHaveValue("owner-mac");
  } finally {
    await app.close();
    proxy.close();
  }
});

test("storage: a project's worktrees in a table, one with local changes kept saying why, an idle one released and its task resumed; auto-release on", async () => {
  const { app, page } = await launch();
  try {
    const title = "工作区恢复验证";
    const kept = "有改动的任务";
    const started = JSON.parse(await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", title, "--prompt", "write saved.txt first turn", "--wait", "-o", "json")).session;
    const other = JSON.parse(await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", kept, "--prompt", "say ready", "--wait", "-o", "json")).session;
    const { localMachineId } = await api<{ localMachineId: string }>(page, "GetStatus");
    const { workspaces } = await api<{ workspaces: { sessionId: string; path: string }[] }>(page, "ListWorkspaces", { machineId: localMachineId });
    const dir = workspaces.find((w) => w.sessionId === started.session_id)!.path;
    const otherDir = workspaces.find((w) => w.sessionId === other.session_id)!.path;
    const draft = path.join(otherDir, "unsaved.txt");
    fs.writeFileSync(draft, "keep me");

    const settings = await openSettings(app, page, `env/${localMachineId}/storage`);
    const panel = page_(settings, "储存空间");
    await expect(panel).toContainText(/已使用 \d+(\.\d)? (B|KB|MB|GB)/);
    await expect(panel.getByRole("listitem", { name: "demo" })).toContainText(/\d+ 个工作目录/);
    await expect(panel).toContainText("释放空闲的工作目录");
    await snap(settings, "settings-storage");

    await panel.getByRole("listitem", { name: "demo" }).getByRole("button", { name: "显示详细信息" }).click();
    const sheet = settings.getByRole("dialog", { name: "demo" });
    const keptRow = sheet.getByRole("option", { name: kept });
    await expect(keptRow).toHaveAttribute("aria-disabled", "true");
    await expect(keptRow).toContainText("有未提交的改动");
    const row = sheet.getByRole("option", { name: title });
    await row.click();
    await expect(row).toHaveAttribute("aria-selected", "true");
    await snap(settings, "settings-storage-sheet");
    await sheet.getByRole("button", { name: /^释放 \d/ }).click();
    const confirm = settings.getByRole("dialog", { name: "要释放 1 个工作目录吗？" });
    await confirm.getByRole("button", { name: "释放" }).click();
    await expect(settings.getByRole("status").filter({ hasText: `已释放「${title}」的本地工作区。` })).toBeVisible();
    await expect(row).toHaveCount(0);
    expect(fs.existsSync(dir)).toBe(false);
    expect(fs.readFileSync(draft, "utf8")).toBe("keep me");
    await sheet.getByRole("button", { name: "完成" }).click();

    // Auto-release, a setting of this environment.
    await panel.getByRole("button", { name: "开启…" }).click();
    await settings.getByRole("dialog", { name: "要自动释放空闲的工作目录吗？" }).getByRole("button", { name: "开启" }).click();
    await expect(panel.getByLabel("自动释放空闲的工作目录")).toContainText("已开启");
    expect((await api<{ autoRelease?: boolean }>(page, "ListWorkspaces", { machineId: localMachineId, cached: true })).autoRelease).toBe(true);
    await panel.getByLabel("自动释放空闲的工作目录").getByRole("button", { name: "关闭" }).click();
    await expect(panel.getByRole("button", { name: "开启…" })).toBeVisible();

    // The released task goes on where it was.
    await cli("session", "send", started.session_id, "--message", "write resumed.txt second turn", "--wait");
    expect(fs.readFileSync(path.join(dir, "saved.txt"), "utf8")).toContain("first turn");
    expect(fs.readFileSync(path.join(dir, "resumed.txt"), "utf8")).toContain("second turn");
  } finally {
    await app.close();
  }
});
