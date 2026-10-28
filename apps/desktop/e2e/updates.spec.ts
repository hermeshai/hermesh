import { expect, test, type ElectronApplication, type Page } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { launch, snap } from "./app";
import { installer, restartInstaller, type Hook } from "./install-app";

test.skip(process.platform !== "darwin", "macOS updates");
const clickMenu = (app: ElectronApplication, id: string) => app.evaluate(({ Menu }, id) => Menu.getApplicationMenu()?.getMenuItemById(id)?.click(), id);
const advance = (app: ElectronApplication, ms: number) => app.evaluate(async (_, ms) => { await (globalThis as unknown as Hook).installTest.advance(ms); }, ms);
const requests = (app: ElectronApplication) => app.evaluate(() => (globalThis as unknown as Hook).installTest.feedRequests);
/** Opens the menu bar panel; another app taking focus closes it (the specs run side by side), so it opens it again then. */
async function openTray(app: ElectronApplication) {
  const tray = () => app.evaluate(() => {
    const t = (globalThis as unknown as { hermeshTest: { tray(): { open: boolean; toggle(): void } } }).hermeshTest.tray();
    if (!t.open) t.toggle();
    return t.open;
  });
  await expect.poll(tray).toBe(true);
  await expect.poll(() => app.windows().some((p) => p.url().endsWith("#tray"))).toBe(true);
  return app.windows().find((p) => p.url().endsWith("#tray"))!;
}
async function open(app: ElectronApplication, main: Page) {
  const existing = app.windows().find((p) => p.url().endsWith("#updates"));
  if (existing) { await main.evaluate(() => window.hermesh?.openUpdates()); return existing; }
  const [updatesPage] = await Promise.all([app.waitForEvent("window"), main.evaluate(() => window.hermesh?.openUpdates())]);
  return updatesPage;
}
/**
 * Keeps the work area as it is now, for the window on route to fit and the checks to measure. Other specs' apps
 * come and go from the Dock meanwhile; where the Dock fills the screen's width (CI's small screen) each one resizes
 * it, and the work area's bottom moves by a few points between the window fitting itself and the check.
 */
async function holdWorkArea(app: ElectronApplication, route: string) {
  await app.evaluate(({ BrowserWindow, screen }, route) => {
    const win = BrowserWindow.getAllWindows().find((w) => w.webContents.getURL().endsWith(route))!;
    const display = screen.getDisplayMatching(win.getBounds());
    screen.getDisplayMatching = () => display;
  }, route);
}

test("background discovery stays quiet; details, snooze and automatic download share one window", async () => {
  const app = await installer(true, "success", { autoDownload: false });
  const main = await app.firstWindow();
  await expect(main.getByRole("list", { name: "会话" })).toContainText("写 hello");
  expect(await requests(app)).toBe(0);
  await app.evaluate(() => { (globalThis as unknown as Hook).installTest.latest = "1.2.0"; });
  await advance(app, 30_000);
  await expect(main.getByRole("button", { name: /有可用更新/ })).toBeVisible();
  expect(app.windows()).toHaveLength(1);
  const [updates] = await Promise.all([app.waitForEvent("window"), main.getByRole("button", { name: /有可用更新/ }).click()]);
  await expect(updates.getByRole("status")).toHaveText("有可用更新");
  await expect(updates.getByText("在 Hermesh 中阅读更新摘要。").first()).toBeVisible();
  await expect(updates.locator("summary", { hasText: "跨过版本中的更新" })).toBeVisible();
  await updates.locator("summary", { hasText: "跨过版本中的更新" }).click();
  await expect(updates.getByRole("heading", { name: "版本 1.1.0" })).toBeVisible();
  await clickMenu(app, "checkForUpdates");
  await expect.poll(() => requests(app)).toBe(2);
  expect(app.windows().filter((p) => p.url().endsWith("#updates"))).toHaveLength(1);
  await updates.getByRole("button", { name: "稍后", exact: true }).click();
  await expect(main.getByRole("button", { name: /有可用更新/ })).toHaveCount(0);

  const [settings] = await Promise.all([app.waitForEvent("window"), main.evaluate(() => window.hermesh?.openSettings())]);
  await expect(settings.getByRole("main", { name: "通用" })).toBeVisible();
  await expect(settings.getByRole("status", { name: "更新状态" })).toHaveCount(0);
  await expect(settings.getByRole("button", { name: /检查更新|下载更新|重新启动以更新/ })).toHaveCount(0);
  await settings.getByRole("switch", { name: "自动下载更新" }).click();
  await expect.poll(() => app.evaluate(() => (globalThis as unknown as Hook).installTest.downloads)).toBe(1);
  const progress = await open(app, main);
  await expect(progress.getByRole("status")).toContainText("正在下载 1.2.0");
  await expect(progress.getByRole("progressbar")).toBeVisible();
  await clickMenu(app, "checkForUpdates");
  expect(await requests(app)).toBe(2);
  await progress.getByRole("button", { name: "关闭", exact: true }).click();
  await app.evaluate(({ autoUpdater }) => autoUpdater.emit("update-downloaded", {}, "", "1.2.0"));
  // Snoozing applies to this version, including when it finishes downloading.
  await expect(main.getByRole("button", { name: /更新已就绪/ })).toHaveCount(0);
  await clickMenu(app, "showUpdateDetails");
  await expect.poll(() => app.windows().filter((p) => p.url().endsWith("#updates")).length).toBe(1);
  const ready = app.windows().find((p) => p.url().endsWith("#updates"))!;
  await expect(ready.getByRole("status")).toHaveText("更新已就绪");
  await expect(ready.getByText(/退出 Hermesh 时会安装此更新/)).toBeVisible();
  await expect(ready.getByText(/隐藏 24 小时/)).toBeVisible();
  await snap(ready, "updates-ready");
  await ready.emulateMedia({ colorScheme: "dark" });
  await snap(ready, "updates-ready-dark");
  await app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows().find((w) => w.webContents.getURL().endsWith("#updates"))!.setSize(440, 360));
  await expect(ready.getByRole("button", { name: "重新启动以更新" })).toBeInViewport();
  expect(await ready.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await snap(ready, "updates-ready-compact");
  await snap(main, "updates-sidebar-snoozed");
  // The menu bar panel offers the downloaded update, and nothing to check by hand.
  const panel = await openTray(app);
  await expect(panel.getByRole("button", { name: "重新启动以更新到 1.2.0" })).toBeVisible();
  await expect(panel.getByRole("button", { name: /检查更新/ })).toHaveCount(0);
});

test("approval protection covers the window and fast menu command; returning never approves", async () => {
  const app = await installer(true);
  const main = await app.firstWindow();
  await expect(main.getByRole("list", { name: "会话" })).toBeVisible();
  await app.evaluate(() => { (globalThis as unknown as Hook).installTest.latest = "1.2.0"; });
  await advance(app, 30_000);
  await app.evaluate(({ autoUpdater }) => autoUpdater.emit("update-downloaded", {}, "", "1.2.0"));
  await expect(main.getByRole("button", { name: /更新已就绪/ })).toBeVisible();
  await snap(main, "updates-sidebar");
  const updates = await open(app, main);
  await app.evaluate(() => (globalThis as unknown as Hook).installTest.signing(true));
  await expect(updates.getByRole("button", { name: "返回授权" })).toBeVisible();
  await expect(updates.getByRole("button", { name: "重新启动以更新" })).toHaveCount(0);
  await clickMenu(app, "installUpdate");
  expect(await app.evaluate(() => (globalThis as unknown as Hook).installTest.installs)).toBe(0);
  await updates.getByRole("button", { name: "返回授权" }).click();
  expect(await app.evaluate(() => (globalThis as unknown as Hook).installTest.signerSignals)).toEqual(["SIGUSR1"]);
  await snap(updates, "updates-approval");
  await app.evaluate(() => (globalThis as unknown as Hook).installTest.signing(false));
  await expect(updates.getByRole("button", { name: "重新启动以更新" })).toBeVisible();
  await updates.close();
  await clickMenu(app, "installUpdate");
  expect(await app.evaluate(() => (globalThis as unknown as Hook).installTest.installs)).toBe(1);
  expect(app.windows().filter((p) => p.url().endsWith("#updates"))).toHaveLength(0);
});

test("checks debounce, retry with backoff, recover after wake, and preserve freshness across launches", async () => {
  let app = await installer(true);
  let main = await app.firstWindow();
  await app.evaluate(() => { (globalThis as unknown as Hook).installTest.feedFails = true; });
  await advance(app, 29_999);
  expect(await requests(app)).toBe(0);
  await advance(app, 1);
  await expect.poll(() => requests(app)).toBe(1);
  expect((await main.evaluate(() => window.hermesh!.getUpdate())).checkedAt).toBeNull();
  await advance(app, 5 * 60_000 - 1);
  expect(await requests(app)).toBe(1);
  await advance(app, 1);
  await expect.poll(() => requests(app)).toBe(2);
  await advance(app, 30 * 60_000);
  await expect.poll(() => requests(app)).toBe(3);
  await advance(app, 120 * 60_000);
  await expect.poll(() => requests(app)).toBe(4);
  // Manual bypasses backoff; repeated commands join one in-flight check.
  await app.evaluate(() => { const s = (globalThis as unknown as Hook).installTest; s.feedFails = false; s.holdFeed = true; });
  await clickMenu(app, "checkForUpdates");
  await expect.poll(() => requests(app)).toBe(5);
  await clickMenu(app, "checkForUpdates");
  expect(await requests(app)).toBe(5);
  await app.evaluate(() => { (globalThis as unknown as Hook).installTest.holdFeed = false; });
  const updates = await open(app, main);
  await expect(updates.getByRole("status")).toContainText("已是最新版本");
  let success = (await main.evaluate(() => window.hermesh!.getUpdate())).checkedAt;
  expect(success).not.toBeNull();
  await app.evaluate(({ powerMonitor }) => { powerMonitor.emit("resume"); powerMonitor.emit("resume"); });
  await advance(app, 10_000);
  expect(await requests(app)).toBe(5);
  await app.evaluate(() => { (globalThis as unknown as Hook).installTest.online = false; });
  await advance(app, 4 * 60 * 60_000);
  expect(await requests(app)).toBe(5);
  await app.evaluate(() => { (globalThis as unknown as Hook).installTest.online = true; });
  await app.evaluate(({ powerMonitor }) => { powerMonitor.emit("resume"); powerMonitor.emit("resume"); });
  await main.evaluate(() => window.dispatchEvent(new Event("online")));
  await advance(app, 9999);
  expect(await requests(app)).toBe(5);
  await advance(app, 1);
  await expect.poll(() => requests(app)).toBe(6);
  await expect(updates.getByRole("status")).toContainText("已是最新版本");
  success = (await main.evaluate(() => window.hermesh!.getUpdate())).checkedAt;
  // Relaunch retains last success and snooze settings instead of checking immediately.
  app = await restartInstaller(app);
  main = await app.firstWindow();
  await expect(main.getByRole("list", { name: "会话" })).toBeVisible();
  await advance(app, 30_000);
  expect(await requests(app)).toBe(0);
  expect((await main.evaluate(() => window.hermesh!.getUpdate())).checkedAt).toBe(success);
  await clickMenu(app, "checkForUpdates");
  await expect.poll(() => requests(app)).toBe(1);
});

test("About and Help lead to updates and readable history", async () => {
  const app = await installer(true);
  const main = await app.firstWindow();
  const [about] = await Promise.all([app.waitForEvent("window"), clickMenu(app, "about")]);
  await expect(about.getByRole("main", { name: "关于 Hermesh" })).toBeVisible();
  const [updates] = await Promise.all([app.waitForEvent("window"), about.getByRole("button", { name: "检查更新…", exact: true }).click()]);
  await expect(updates.getByRole("status")).toContainText("已是最新版本");
  const [history] = await Promise.all([app.waitForEvent("window"), clickMenu(app, "whatsNew")]);
  await expect(history.getByRole("heading", { name: "版本 0.1.0-rc7" })).toBeVisible();
  await expect(history.getByText("通过签名磁盘映像安装 Hermesh，可直接拖入「应用程序」。")).toBeVisible();
  await snap(history, "updates-history");
  await history.close();
  // The menu bar panel offers no check of its own while Hermesh is up to date.
  const panel = await openTray(app);
  await expect(panel.getByRole("button", { name: "打开 Hermesh" })).toBeVisible();
  await expect(panel.getByRole("button", { name: /检查更新|重新启动以更新/ })).toHaveCount(0);
  await main.evaluate(() => window.hermesh?.trayAction("settings")); // Sender restrictions reject main-window tray actions.
  await new Promise((r) => setTimeout(r, 500));
  expect(app.windows().some((p) => p.url().includes("#settings"))).toBe(false);
  expect(await requests(app)).toBe(1);
});

test("release history fits short content and keeps its actions visible with long notes", async () => {
  const app = await installer(true, "success", { autoDownload: false });
  const main = await app.firstWindow();
  const [history] = await Promise.all([app.waitForEvent("window"), clickMenu(app, "whatsNew")]);
  await expect(history.getByRole("heading", { name: "版本 0.1.0-rc7" })).toBeVisible();
  const layout = () => history.evaluate(() => {
    const scroll = document.querySelector<HTMLElement>(".update-scroll")!;
    const link = scroll.querySelector<HTMLElement>(".update-links")!.getBoundingClientRect();
    const button = document.querySelector<HTMLElement>(".update-actions .btn")!.getBoundingClientRect();
    return { height: innerHeight, gap: button.top - link.bottom, alignment: Math.abs(button.right - link.right), scrolls: scroll.scrollHeight > scroll.clientHeight };
  });
  await snap(history, "history-short");
  await expect.poll(async () => (await layout()).gap).toBeGreaterThanOrEqual(34);
  await expect.poll(async () => (await layout()).gap).toBeLessThanOrEqual(42);
  expect((await layout()).alignment).toBeLessThanOrEqual(1);
  expect((await layout()).scrolls).toBe(false);
  const shortHeight = (await layout()).height;
  await history.emulateMedia({ colorScheme: "dark" });
  await snap(history, "history-short-dark");

  await holdWorkArea(app, "#whats-new");
  await app.evaluate(() => { const s = (globalThis as unknown as Hook).installTest; s.latest = "1.2.0"; s.longNotes = true; });
  await main.evaluate(() => window.hermesh?.checkForUpdates());
  await expect.poll(async () => (await layout()).height).toBeGreaterThan(shortHeight);
  await expect.poll(async () => (await layout()).scrolls).toBe(true);
  await expect(history.getByRole("button", { name: "完成", exact: true })).toBeInViewport();
  const frame = await app.evaluate(({ BrowserWindow, screen }) => {
    const win = BrowserWindow.getAllWindows().find((w) => w.webContents.getURL().endsWith("#whats-new"))!;
    const bounds = win.getBounds();
    return { bounds, area: screen.getDisplayMatching(bounds).workArea };
  });
  // macOS can round native window bounds by one point.
  expect(frame.bounds.height).toBeLessThanOrEqual(frame.area.height - 42 + 1);
  await history.locator(".update-scroll").evaluate((e) => { e.scrollTop = e.scrollHeight; });
  await expect(history.getByRole("button", { name: "完整更新历史" })).toBeInViewport();
  await snap(history, "history-long");
  await history.getByRole("button", { name: "完成", exact: true }).click();
  await expect.poll(() => app.windows().some((p) => p.url().endsWith("#whats-new"))).toBe(false);
});

test("a restart restores the selected session, layout and unfinished answer", async () => {
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmview-"));
  let instance = await launch(undefined, { profile });
  try {
    const list = instance.page.getByRole("list", { name: "会话" });
    await list.getByRole("listitem").filter({ hasText: "选颜色" }).click();
    await instance.page.getByRole("radio", { name: /蓝/ }).click();
    await instance.page.getByRole("textbox", { name: "你的回答" }).fill("Keep this unfinished answer");
    await instance.page.getByRole("button", { name: "信息面板" }).click();
    await instance.app.close();
    instance = await launch(undefined, { profile });
    await expect(instance.page.getByRole("heading", { name: "选颜色" })).toBeVisible();
    await expect(instance.page.getByRole("textbox", { name: "你的回答" })).toHaveValue("Keep this unfinished answer");
    await expect(instance.page.getByRole("radio", { name: /蓝/ })).toHaveAttribute("aria-checked", "true");
    await expect(instance.page.getByRole("button", { name: "信息面板" })).toHaveAttribute("aria-pressed", "false");
  } finally {
    await instance.app.close();
    fs.rmSync(profile, { recursive: true, force: true });
  }
});


test("snooze survives relaunch, a later version reminds again, and missing notes never block download", async () => {
  let app = await installer(true, "success", { autoDownload: false });
  let main = await app.firstWindow();
  await app.evaluate(() => { const s = (globalThis as unknown as Hook).installTest; s.latest = "1.2.0"; s.badNotes = true; });
  await advance(app, 30_000);
  const updates = await open(app, main);
  await expect(updates.getByText(/暂时无法在这里显示更新摘要/)).toBeVisible();
  await updates.getByRole("button", { name: "在浏览器中查看更新说明" }).click();
  expect(await app.evaluate(() => (globalThis as unknown as Hook).installTest.links)).toEqual(["https://github.com/hermeshai/hermesh/releases/tag/v1.2.0"]);
  await updates.getByRole("button", { name: "稍后", exact: true }).click();
  app = await restartInstaller(app);
  main = await app.firstWindow();
  await app.evaluate(() => { (globalThis as unknown as Hook).installTest.latest = "1.2.0"; });
  const again = await open(app, main);
  await expect(again.getByRole("status")).toHaveText("有可用更新");
  await expect(main.getByRole("button", { name: /有可用更新/ })).toHaveCount(0);
  await app.evaluate(() => { const s = (globalThis as unknown as Hook).installTest; s.latest = "1.3.0"; s.badNotes = true; });
  await again.evaluate(() => window.hermesh?.checkForUpdates());
  await expect(main.getByRole("button", { name: /有可用更新/ })).toBeVisible();
  await again.getByRole("button", { name: "下载更新" }).click();
  await expect(again.getByRole("status")).toContainText("正在下载 1.3.0");
  await app.evaluate(({ autoUpdater }) => autoUpdater.emit("update-downloaded", {}, "", "1.3.0"));
  await expect(again.getByRole("button", { name: "重新启动以更新" })).toBeVisible();
  await expect(main.getByRole("button", { name: /更新已就绪/ })).toBeVisible();
});


test("update windows fit their content with balanced action spacing and scroll long notes", async () => {
  const app = await installer(true, "success", { autoDownload: false });
  const main = await app.firstWindow();
  const updates = await open(app, main);
  const status = updates.getByRole("status", { name: "更新状态" });
  const layout = () => updates.evaluate(() => {
    const scroll = document.querySelector<HTMLElement>(".update-scroll")!;
    const links = document.querySelector<HTMLElement>(".update-links")!.getBoundingClientRect();
    const button = document.querySelector<HTMLElement>(".update-actions .btn:last-child")!.getBoundingClientRect();
    return { height: innerHeight, gap: button.top - links.bottom, alignment: Math.abs(button.right - links.right), scrolls: scroll.scrollHeight > scroll.clientHeight };
  });
  const compact = async (maximum: number) => {
    await expect.poll(async () => (await layout()).height).toBeLessThan(maximum);
    await expect.poll(async () => (await layout()).gap).toBeGreaterThanOrEqual(34);
    await expect.poll(async () => (await layout()).gap).toBeLessThanOrEqual(42);
    const metrics = await layout();
    expect(metrics.gap).toBeGreaterThanOrEqual(34);
    expect(metrics.alignment).toBeLessThanOrEqual(1);
    expect(metrics.scrolls).toBe(false);
    await expect(updates.locator(".update-actions .btn").last()).toBeInViewport();
  };
  await expect(status).toContainText("已是最新版本");
  await compact(340);
  await snap(updates, "updates-current-balanced");
  await app.evaluate(({ autoUpdater }) => autoUpdater.emit("error", new Error("The update download failed.")));
  await expect(status).toHaveText("暂时无法更新");
  await compact(380);
  await snap(updates, "updates-error-balanced");
  await updates.getByRole("button", { name: "重试", exact: true }).click();
  await expect(status).toContainText("已是最新版本");

  await app.evaluate(() => { (globalThis as unknown as Hook).installTest.latest = "1.2.0"; });
  await updates.evaluate(() => window.hermesh?.checkForUpdates());
  await expect(status).toHaveText("有可用更新");
  await compact(460);
  const shortHeight = (await layout()).height;
  await holdWorkArea(app, "#updates");
  await app.evaluate(() => { (globalThis as unknown as Hook).installTest.longNotes = true; });
  await updates.evaluate(() => window.hermesh?.checkForUpdates());
  await expect.poll(async () => (await layout()).height).toBeGreaterThan(shortHeight);
  await expect.poll(async () => (await layout()).scrolls).toBe(true);
  const frame = await app.evaluate(({ BrowserWindow, screen }) => {
    const win = BrowserWindow.getAllWindows().find((w) => w.webContents.getURL().endsWith("#updates"))!;
    const bounds = win.getBounds();
    return { bounds, area: screen.getDisplayMatching(bounds).workArea };
  });
  // macOS can round native window bounds by one point.
  expect(frame.bounds.height).toBeLessThanOrEqual(frame.area.height - 42 + 1);
  expect(frame.bounds.y + frame.bounds.height).toBeLessThanOrEqual(frame.area.y + frame.area.height - 21 + 1);
  await expect(updates.getByRole("button", { name: "下载更新", exact: true })).toBeInViewport();
  await snap(updates, "updates-long-notes-balanced");

  await app.evaluate(() => { (globalThis as unknown as Hook).installTest.longNotes = false; });
  await updates.evaluate(() => window.hermesh?.checkForUpdates());
  await expect(status).toHaveText("有可用更新");
  await compact(460);
  await updates.getByRole("button", { name: "下载更新", exact: true }).click();
  await expect(status).toContainText("正在下载 1.2.0");
  await compact(500);
  await app.evaluate(({ autoUpdater }) => autoUpdater.emit("update-downloaded", {}, "", "1.2.0"));
  await expect(status).toHaveText("更新已就绪");
  await compact(560);
  await snap(updates, "updates-ready-balanced");
  await updates.emulateMedia({ colorScheme: "dark" });
  await snap(updates, "updates-ready-balanced-dark");
  const before = (await layout()).height;
  await main.evaluate(() => window.hermesh?.updateWindowHeight(2000));
  await updates.evaluate(() => window.hermesh?.updateWindowHeight(Number.NaN));
  expect((await layout()).height).toBe(before);
});
