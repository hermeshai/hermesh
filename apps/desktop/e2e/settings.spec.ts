import { expect, test, type ElectronApplication, type Page } from "@playwright/test";
import { codingAgentIds, codingAgentTitle } from "@hermesh/ui/agents";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { answerMenu, gitRepo, launch, pickAnswers, snap } from "./app";

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

const projects = async (page: Page) => (await api<{ projects?: { path: string; agents?: string[] }[] }>(page, "ListProjects")).projects ?? [];

const page_ = (settings: Page, name: string) => settings.getByRole("main", { name });
const nav = (settings: Page, name: string) => settings.getByRole("navigation", { name: "设置" }).getByRole("button", { name: new RegExp(`^${name}`) });

test("settings opens on 通用; 授权 lists the grants, one opens its scope, and disconnecting asks first", async () => {
  const { app, page } = await launch();
  const settings = await openSettings(app, page);
  await expect(nav(settings, "通用")).toHaveAttribute("aria-current", "page");
  const general = page_(settings, "通用");
  await expect(general.getByRole("switch", { name: "连接请求" })).toHaveAttribute("aria-checked", "true");
  const login = general.getByRole("switch", { name: "登录时打开 Hermesh" });
  await expect(login).toHaveAttribute("aria-checked", "false");
  await login.click();
  await expect(login).toHaveAttribute("aria-checked", "true");
  expect((await page.evaluate(() => window.hermesh?.loginItem()))?.on).toBe(true);
  const turnDone = general.getByRole("switch", { name: "一轮完成" });
  await expect(turnDone).toHaveAttribute("aria-checked", "false");
  await turnDone.click();
  await expect(turnDone).toHaveAttribute("aria-checked", "true");
  // Kept by the main process, which posts the notifications.
  expect((await page.evaluate(() => window.hermesh?.getPreferences()))?.notifyTurnDone).toBe(true);
  await snap(settings, "settings-general");

  await nav(settings, "授权").click();
  await expect(settings).toHaveTitle("授权");
  const grants = page_(settings, "授权");
  const bot = grants.getByRole("list", { name: "授权" }).getByRole("button", { name: /bot/ });
  await expect(bot).toContainText("所有项目 · 所有机器");
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
  const palette = page.getByRole("dialog", { name: "搜索会话" });
  await expect(palette.getByRole("combobox", { name: "授权" })).toHaveValue(/.+/);
  await expect(palette.getByRole("combobox", { name: "授权" }).locator("option:checked")).toHaveText("bot");
  await expect(palette.getByRole("listbox", { name: "搜索结果" }).getByRole("option").first()).toBeVisible();
  await app.close();
});

test("settings 机器 opens this Mac: its sharing, its agents each with their projects; rename, pause and resume it", async () => {
  const { app, page } = await launch();
  const settings = await openSettings(app, page, "machines");
  await expect(nav(settings, "机器")).toHaveAttribute("aria-current", "page");
  const panel = page_(settings, "机器");
  const mac = panel.getByRole("list", { name: "机器" }).getByRole("button", { name: /^owner-mac/ });
  await expect(mac).toContainText("这台 Mac");
  await snap(settings, "settings-machines");
  await mac.click();
  await expect(panel.getByRole("switch", { name: "安全可信地共享本机 agent" })).toHaveAttribute("aria-checked", "true");
  await expect(panel).toContainText(/正在运行 \d+ 个/);
  await expect(panel.locator(".set-acts").getByRole("button", { name: "移除…" })).toHaveCount(0); // not this Mac

  // The agents as the first run shows them, each opening to the projects it may work in.
  const agents = panel.getByRole("list", { name: "编码 Agent" });
  const gemini = agents.getByRole("listitem", { name: "Gemini CLI" });
  await expect(gemini).toContainText("已登录", { timeout: 30_000 });
  await expect(agents).not.toContainText("正在检查…", { timeout: 30_000 }); // the first ready one opens once all are in
  const head = gemini.getByRole("button", { name: /^Gemini CLI/ });
  if ((await head.getAttribute("aria-expanded")) !== "true") await head.click();
  await expect(gemini.getByRole("group", { name: "demo" })).toBeVisible();
  await snap(settings, "settings-machine");

  // A folder picked under Gemini CLI is its project; taken from it, it goes.
  const repo = gitRepo(`app-${process.pid}`);
  await pickAnswers(app, repo);
  // Drawn as a text link, it takes the pointing hand, as links do.
  expect(await gemini.getByRole("button", { name: "添加项目…" }).evaluate((el) => getComputedStyle(el).cursor)).toBe("pointer");
  await gemini.getByRole("button", { name: "添加项目…" }).click();
  const project = gemini.getByRole("group", { name: `app-${process.pid}` });
  await expect(project).toContainText(repo);
  await expect(project).toContainText("独立 worktree");
  expect((await projects(settings)).find((p) => p.path === repo)?.agents).toEqual(["gemini"]);
  await answerMenu(app, "remove");
  await project.getByRole("button", { name: "更多" }).click();
  await expect(project).toHaveCount(0);
  expect((await projects(settings)).some((p) => p.path === repo)).toBe(false);

  // A project of an agent not installed here shows after the agents, and can be removed.
  const missing = await panel.getByRole("list", { name: "没有安装的 Agent" }).getByRole("listitem").first().getAttribute("aria-label");
  const id = codingAgentIds.find((x) => codingAgentTitle(x) === missing);
  if (id) {
    const st = await api<{ localMachineId: string }>(settings, "GetStatus");
    await api(settings, "AddProject", { machineId: st.localMachineId, projectId: `left-${process.pid}`, path: gitRepo(`left-${process.pid}`), agents: [id] });
    const left = panel.getByRole("list", { name: "其他项目 · 给没有安装的 agent" }).getByRole("group", { name: `left-${process.pid}` });
    await expect(left).toContainText(missing!, { timeout: 30_000 });
    await expect(agents.getByRole("group", { name: `left-${process.pid}` })).toHaveCount(0);
    await answerMenu(app, "remove");
    await left.getByRole("button", { name: "更多" }).click();
    await expect(left).toHaveCount(0);
  }

  const rename = async (from: string, to: string) => {
    await panel.getByRole("button", { name: "改名…" }).click();
    const dialog = settings.getByRole("dialog", { name: "给这台机器改名" });
    const field = dialog.getByRole("textbox", { name: "机器名" });
    await expect(field).toHaveValue(from);
    await snap(settings, "settings-machines-rename");
    await field.fill(to);
    await field.press("Enter");
    await expect(dialog).toBeHidden();
    await expect(panel.locator(".dhero")).toContainText(to);
  };
  await rename("owner-mac", "studio-mac");
  await rename("studio-mac", "owner-mac");

  const pause = panel.getByRole("switch", { name: "暂停接受新会话" });
  await pause.click();
  await expect(pause).toHaveAttribute("aria-checked", "true");
  await expect(settings.getByRole("status")).toContainText("「owner-mac」暂停接受新会话");
  await snap(settings, "settings-machines-paused");
  await settings.getByRole("button", { name: "返回机器" }).click();
  await expect(mac).toContainText("已暂停");
  await mac.click();
  await pause.click();
  await expect(pause).toHaveAttribute("aria-checked", "false");
  await app.close();
});

test("settings 账号与设备 shows the identity, the trusted devices and, folded, the addresses", async () => {
  const { app, page } = await launch();
  const status = await page.evaluate(() =>
    fetch("app://hermesh/api/hermesh.local.v1.LocalService/GetStatus", { method: "POST", headers: { "content-type": "application/json" }, body: "{}" }).then((r) => r.json()),
  );
  const settings = await openSettings(app, page, "account");
  const panel = page_(settings, "账号与设备");
  const devices = panel.getByRole("list", { name: "可信设备" });
  await expect(devices).toContainText("test-mac");
  await expect(devices).toContainText("这台 Mac");
  await expect(devices.getByRole("button", { name: "移除…" })).toHaveCount(0);
  await expect(panel).not.toContainText(status.ownerId);
  await snap(settings, "settings-account");

  await devices.getByRole("button", { name: "添加设备…" }).click();
  await expect(settings.getByRole("dialog", { name: "添加可信设备" })).toBeVisible();
  await settings.keyboard.press("Escape");
  await expect(settings.getByRole("dialog")).toHaveCount(0);

  await panel.getByRole("button", { name: "高级" }).click();
  await expect(panel).toContainText(status.ownerId);
  await expect(panel).toContainText("已连接");
  await expect(panel).toContainText(`hermeshd pair ${status.connectAddress}`);
  await expect(panel).toContainText(`hermesh auth login ${status.connectAddress}`);
  await panel.getByRole("button", { name: "拷贝身份 ID" }).click();
  await expect(settings.getByRole("status")).toContainText("已拷贝");
  expect(await app.evaluate(({ clipboard }) => clipboard.readText())).toBe(status.ownerId);
  await snap(settings, "settings-account-advanced");

  // Still a trusted device: signing out only explains how.
  await panel.getByRole("button", { name: "退出登录…" }).click();
  const dialog = settings.getByRole("dialog", { name: "让这台 Mac 退出登录？" });
  await expect(dialog).toContainText("先在你的另一台可信设备上");
  await dialog.getByRole("button", { name: "好" }).click();
  await expect(dialog).toBeHidden();

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

test("通用's 外观 follows the system, or keeps every window light or dark, also next time", async () => {
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-"));
  let { app, page } = await launch(undefined, { profile, systemColors: true });
  const dark = (p: Page) => p.evaluate(() => matchMedia("(prefers-color-scheme: dark)").matches);
  const source = () => app.evaluate(({ nativeTheme }) => nativeTheme.themeSource);
  let settings = await openSettings(app, page, "general");
  const appearance = () => page_(settings, "通用").getByRole("group", { name: "外观" });
  await expect(appearance().getByRole("button")).toHaveText(["跟随系统", "浅色", "深色"]);
  await expect(appearance().getByRole("button", { name: "跟随系统" })).toHaveAttribute("aria-pressed", "true");
  expect(await source()).toBe("system");

  await appearance().getByRole("button", { name: "深色" }).click();
  await expect(appearance().getByRole("button", { name: "深色" })).toHaveAttribute("aria-pressed", "true");
  await expect.poll(() => dark(page)).toBe(true);
  await expect.poll(() => dark(settings)).toBe(true);
  await snap(settings, "settings-appearance-dark");
  await appearance().getByRole("button", { name: "浅色" }).click();
  await expect.poll(() => dark(page)).toBe(false);
  await expect.poll(() => dark(settings)).toBe(false);

  // Kept for next time, from the start.
  await app.close();
  ({ app, page } = await launch(undefined, { profile, systemColors: true }));
  expect(await source()).toBe("light");
  expect(await dark(page)).toBe(false);
  settings = await openSettings(app, page, "general");
  await expect(appearance().getByRole("button", { name: "浅色" })).toHaveAttribute("aria-pressed", "true");
  await appearance().getByRole("button", { name: "跟随系统" }).click();
  await expect.poll(source).toBe("system");
  await app.close();
});
