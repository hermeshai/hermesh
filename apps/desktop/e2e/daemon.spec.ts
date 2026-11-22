import { expect, test, type ElectronApplication, type Page } from "@playwright/test";
import type { BrowserWindow, Menu } from "electron";
import type { DaemonInfo } from "../src/shared/daemon";
import type { TrayView } from "../src/shared/bridge";
import { cli, fixture, sessionList } from "./app";
import { installer, restartInstaller, type Hook as InstallHook } from "./install-app";

test.skip(process.platform !== "darwin", "macOS managed daemon lifecycle");

type MainHook = InstallHook & {
  hermeshTest: {
    mainWindow(): BrowserWindow | undefined;
    tray(): { view: TrayView; open: boolean; toggle(): void; contextMenu(): Menu; dockMenu(): Menu };
    daemon(): { info: DaemonInfo; start(): Promise<void>; stop(): Promise<boolean> };
  };
};

type MenuSnapshot = { id: string; label: string; enabled: boolean }[];

const menuSnapshot = (app: ElectronApplication, kind: "dock" | "context"): Promise<MenuSnapshot> =>
  app.evaluate((_, kind) => {
    const t = (globalThis as unknown as MainHook).hermeshTest.tray();
    const m = kind === "dock" ? t.dockMenu() : t.contextMenu();
    return m.items.filter((i) => i.type !== "separator").map((i) => ({ id: i.id, label: i.label, enabled: i.enabled }));
  }, kind);

const clickNativeMenu = (app: ElectronApplication, kind: "dock" | "context", id: string) =>
  app.evaluate((_, [kind, id]) => {
    const t = (globalThis as unknown as MainHook).hermeshTest.tray();
    const m = kind === "dock" ? t.dockMenu() : t.contextMenu();
    m.getMenuItemById(id)?.click();
  }, [kind, id] as const);

const dialogs = (app: ElectronApplication) =>
  app.evaluate(() => (globalThis as unknown as MainHook).installTest.dialogs);

const launchctlCalls = (app: ElectronApplication) =>
  app.evaluate(() => (globalThis as unknown as MainHook).installTest.launchctl);

const respond = (app: ElectronApplication, button: number) =>
  app.evaluate((_, n) => (globalThis as unknown as MainHook).installTest.respond(n), button);

async function openTray(app: ElectronApplication): Promise<Page> {
  const open = () =>
    app.evaluate(() => {
      const t = (globalThis as unknown as MainHook).hermeshTest.tray();
      if (!t.open) t.toggle();
      return t.open;
    });
  await expect.poll(open).toBe(true);
  await expect.poll(() => app.windows().some((p) => p.url().endsWith("#tray"))).toBe(true);
  return app.windows().find((p) => p.url().endsWith("#tray"))!;
}

test("opened at login hides the Dock icon until a window opens; closing the window keeps Dock visible with Dock & Tray background service menus", async () => {
  const app = await installer(true, "success", { wasOpenedAtLogin: true });
  await expect.poll(() => app.evaluate(() => (globalThis as unknown as MainHook).hermeshTest.tray().view.state)).toBe("ok");
  expect(app.windows()).toHaveLength(0);
  await expect.poll(() => app.evaluate(({ app }) => app.dock?.isVisible())).toBe(false);

  // Opening the tray panel itself does not show the Dock icon.
  const panel = await openTray(app);
  expect(await app.evaluate(({ app }) => app.dock?.isVisible())).toBe(false);

  // Opening the main window brings the Dock icon in; closing the window frees the renderer but keeps the Dock icon.
  const opened = app.waitForEvent("window", (w) => !w.url().endsWith("#tray"));
  await panel.getByRole("button", { name: "打开 Hermesh" }).click();
  const main = await opened;
  await expect(await sessionList(main)).toContainText("写 hello");
  await expect.poll(() => app.evaluate(({ app }) => app.dock?.isVisible())).toBe(true);

  await app.evaluate(() => (globalThis as unknown as MainHook).hermeshTest.mainWindow()?.close());
  await expect.poll(() => app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows().filter((w) => !w.isDestroyed() && !w.webContents.isDestroyed() && !w.webContents.getURL().endsWith("#tray")).length)).toBe(0);
  expect(await app.evaluate(({ app }) => app.dock?.isVisible())).toBe(true);

  // Both Dock right-click menu and Tray right-click menu expose background service status and controls.
  const dock = await menuSnapshot(app, "dock");
  expect(dock).toContainEqual({ id: "daemonStatus", label: "后台服务：运行中", enabled: false });
  expect(dock).toContainEqual({ id: "stopDaemon", label: "停止后台服务…", enabled: true });
  expect(dock).toContainEqual({ id: "quitKeep", label: "仅退出界面（保持后台运行）", enabled: true });

  const tray = await menuSnapshot(app, "context");
  expect(tray).toContainEqual({ id: "daemonStatus", label: "后台服务：运行中", enabled: false });
  expect(tray).toContainEqual({ id: "stopDaemon", label: "停止后台服务…", enabled: true });
  expect(tray).toContainEqual({ id: "quitKeep", label: "仅退出界面（保持后台运行）", enabled: true });
  expect(tray).toContainEqual({ id: "quit", label: "退出 Hermesh", enabled: true });
});

test("quitting with active local tasks and pending approval explains every impact; Cancel aborts and Keep Running leaves daemon alive", async () => {
  const app = await installer(true);
  const main = await app.firstWindow();
  await expect(await sessionList(main)).toContainText("跑很久");
  await expect.poll(() => app.evaluate(() => (globalThis as unknown as MainHook).hermeshTest.tray().view.running)).toBeGreaterThanOrEqual(1);
  // Simulate an active Touch ID signing prompt as well.
  await app.evaluate(() => (globalThis as unknown as MainHook).installTest.signing(true));

  // Trigger standard Cmd+Q / app.quit(): must intercept and show detailed impact warning.
  await app.evaluate(({ app }) => { setImmediate(() => app.quit()); });
  await expect.poll(async () => (await dialogs(app)).length).toBe(1);
  const d = (await dialogs(app))[0];
  expect(d.message).toBe("要退出 Hermesh 并停止后台服务吗？");
  expect(d.detail).toContain("1 个正在这台 Mac 上运行的任务会被中断");
  expect(d.detail).toContain("跑很久");
  expect(d.detail).toContain("1 个正在等待回答的本机任务");
  expect(d.detail).toContain("待处理的连接或签名审批");
  expect(d.detail).toContain("暂时无法连接这台 Mac");
  expect(d.buttons).toEqual(["停止服务并退出", "保持后台运行", "取消"]);
  expect(d.defaultId).toBe(2); // Cancel is the safe default when active work would be disrupted.
  expect(d.cancelId).toBe(2);

  // 1) Cancel keeps both app and daemon running.
  await respond(app, 2);
  await new Promise((r) => setTimeout(r, 200));
  expect((await launchctlCalls(app)).filter(([v]) => v === "kill")).toEqual([]);
  expect(await app.evaluate(() => (globalThis as unknown as MainHook).hermeshTest.daemon().info.state)).toBe("running");

  // 2) Choosing "保持后台运行" exits Electron without stopping hermeshd.
  await app.evaluate(({ app }) => { setImmediate(() => app.quit()); });
  await expect.poll(async () => (await dialogs(app)).length).toBe(2);
  const closed = app.waitForEvent("close");
  await respond(app, 1);
  await closed;
});

test("quitting with active tasks and choosing Stop Service & Quit sends launchctl kill SIGTERM before exiting", async () => {
  const app = await installer(true);
  const main = await app.firstWindow();
  await expect(await sessionList(main)).toContainText("跑很久");
  await expect.poll(() => app.evaluate(() => (globalThis as unknown as MainHook).hermeshTest.tray().view.running)).toBeGreaterThanOrEqual(1);

  await app.evaluate(({ app }) => { setImmediate(() => app.quit()); });
  await expect.poll(async () => (await dialogs(app)).length).toBe(1);
  expect((await dialogs(app))[0].detail).toContain("跑很久");

  // Observe launchctl kill before the process exits.
  const killSeen = app.evaluate(
    () =>
      new Promise<string[][]>((resolve) => {
        const check = () => {
          const calls = (globalThis as unknown as MainHook).installTest.launchctl;
          if (calls.some(([v, sig]) => v === "kill" && sig === "SIGTERM")) resolve(calls);
          else setTimeout(check, 20);
        };
        check();
      }),
  );
  const closed = app.waitForEvent("close");
  await respond(app, 0);
  const calls = await killSeen;
  expect(calls).toContainEqual(["kill", "SIGTERM", expect.stringMatching(/^gui\/\d+\/com\.hermesh\.app\.daemon$/)]);
  await closed;
});

test("stopping the background service from the Dock explains impact, shows clean Stopped UI everywhere, and starting recovers", async () => {
  const app = await installer(true);
  const main = await app.firstWindow();
  await expect(await sessionList(main)).toContainText("写 hello");

  const [settings] = await Promise.all([app.waitForEvent("window"), main.evaluate(() => window.hermesh?.openSettings("env"))]);
  await settings.waitForLoadState();
  const env = settings.getByRole("main", { name: "这台 Mac" });
  const pill = env.locator(".set-pill");
  await expect(pill).toContainText("在线");
  // Starting and stopping is the menus' (docs/settings-environments.md §5.4): no row for it in settings.
  await expect(settings.getByRole("button", { name: "停止…" })).toHaveCount(0);

  // 1) Stop from the Dock -> Cancel leaves it running.
  await clickNativeMenu(app, "dock", "stopDaemon");
  await expect.poll(async () => (await dialogs(app)).length).toBe(1);
  const d1 = (await dialogs(app))[0];
  expect(d1.message).toBe("要停止 Hermesh 后台服务吗？");
  expect(d1.detail).toContain("跑很久");
  expect(d1.detail).toContain("暂时无法连接这台 Mac");
  expect(d1.buttons).toEqual(["停止后台服务", "取消"]);
  expect(d1.defaultId).toBe(1);
  await respond(app, 1);
  await expect(pill).toContainText("在线");
  expect((await launchctlCalls(app)).filter(([v]) => v === "kill")).toEqual([]);

  // 2) Confirmed: every surface says Stopped, the environment's pill and the sidebar too.
  await clickNativeMenu(app, "dock", "stopDaemon");
  await expect.poll(async () => (await dialogs(app)).length).toBe(2);
  await respond(app, 0);

  await expect.poll(async () => (await launchctlCalls(app)).filter(([v]) => v === "kill").length).toBe(1);
  await expect(main.getByText("后台服务已停止")).toBeVisible();
  await expect(main.getByRole("button", { name: "启动后台服务" })).toBeVisible();
  await expect(main.getByText("后台服务没有响应")).toHaveCount(0);
  await expect(pill).toContainText("后台服务已停止");
  await expect(settings.getByRole("navigation", { name: "设置" }).getByRole("button", { name: /^这台 Mac/ })).toContainText("已停止");

  const panel = await openTray(app);
  await expect(panel.getByRole("status")).toHaveText("已停止");
  await expect(panel.getByText("后台服务已停止，其他设备暂时无法连接这台 Mac")).toBeVisible();
  await expect(panel.getByRole("button", { name: "启动后台服务" })).toBeVisible();

  expect(await menuSnapshot(app, "dock")).toContainEqual({ id: "startDaemon", label: "启动后台服务", enabled: true });
  expect(await menuSnapshot(app, "context")).toContainEqual({ id: "startDaemon", label: "启动后台服务", enabled: true });

  // 3) Start again from the pill -> recovers everything cleanly.
  const kickstartsBefore = (await launchctlCalls(app)).filter(([v]) => v === "kickstart").length;
  await pill.getByRole("button", { name: "启动" }).click();
  await expect.poll(async () => (await launchctlCalls(app)).filter(([v]) => v === "kickstart").length).toBeGreaterThan(kickstartsBefore);
  await expect(await sessionList(main)).toContainText("写 hello");
  await expect(pill).toContainText("在线");
  await expect.poll(() => app.evaluate(() => (globalThis as unknown as MainHook).hermeshTest.tray().view.state)).toBe("ok");
  expect(await menuSnapshot(app, "dock")).toContainEqual({ id: "stopDaemon", label: "停止后台服务…", enabled: true });
});

test("turning on Stay Online opens Hermesh at login and skips the dialog on a normal Quit, while explicit Quit & Stop still confirms", async () => {
  let app = await installer(true);
  let main = await app.firstWindow();
  await expect(await sessionList(main)).toContainText("写 hello");

  const [settings] = await Promise.all([app.waitForEvent("window"), main.evaluate(() => window.hermesh?.openSettings("env"))]);
  await settings.waitForLoadState();
  const sw = settings.getByRole("switch", { name: "保持在线" });
  await expect(sw).toHaveAttribute("aria-checked", "false");
  await expect(settings.getByRole("main", { name: "这台 Mac" })).toContainText("只有 Hermesh 打开时");
  await sw.click();
  await expect(sw).toHaveAttribute("aria-checked", "true");
  // The login item follows it.
  await expect.poll(() => app.evaluate(() => (globalThis as unknown as MainHook).installTest.loginItems)).toContainEqual({ type: "mainAppService", openAtLogin: true });

  // Menus adapt to offer explicit "退出并停止后台服务…".
  await expect.poll(() => menuSnapshot(app, "context")).toContainEqual({ id: "quitStop", label: "退出并停止后台服务…", enabled: true });
  await expect.poll(() => menuSnapshot(app, "dock")).toContainEqual({ id: "quitStop", label: "退出并停止后台服务…", enabled: true });

  // Normal Quit exits immediately with 0 dialogs and without stopping hermeshd.
  await Promise.all([app.waitForEvent("close"), app.evaluate(({ app }) => { setImmediate(() => app.quit()); })]);

  // Reopen the same profile: setting persists, and explicit Quit & Stop shows impact dialog and stops daemon.
  app = await restartInstaller(app);
  main = await app.firstWindow();
  await expect(await sessionList(main)).toContainText("写 hello");
  expect((await main.evaluate(() => window.hermesh?.getPreferences()))?.stayOnline).toBe(true);

  await clickNativeMenu(app, "context", "quitStop");
  await expect.poll(async () => (await dialogs(app)).length).toBe(1);
  expect((await dialogs(app))[0].message).toBe("要退出 Hermesh 并停止后台服务吗？");
  const closed = app.waitForEvent("close");
  await respond(app, 0);
  await closed;
});

test("an older app's choice carries over: keeping the service on quit becomes Stay Online, its login item with it", async () => {
  const app = await installer(true, "success", { prefs: { language: "zh-CN", keepDaemonOnQuit: true } });
  const main = await app.firstWindow();
  await expect(await sessionList(main)).toContainText("写 hello");
  await expect.poll(async () => (await main.evaluate(() => window.hermesh?.getPreferences()))?.stayOnline).toBe(true);
  await expect.poll(() => app.evaluate(() => (globalThis as unknown as MainHook).installTest.loginItems)).toContainEqual({ type: "mainAppService", openAtLogin: true });
  await Promise.all([app.waitForEvent("close"), app.evaluate(({ app }) => { setImmediate(() => app.quit()); })]);
});

test("relaunch for language change never prompts or stops the background service", async () => {
  const app = await installer(true);
  const main = await app.firstWindow();
  await expect(await sessionList(main)).toContainText("写 hello");
  // Prevent spawning a real second Electron process during relaunch.
  await app.evaluate(({ app }) => {
    (globalThis as unknown as { relaunched?: number }).relaunched = 0;
    app.relaunch = () => { (globalThis as unknown as { relaunched?: number }).relaunched!++; };
  });
  const closed = app.waitForEvent("close");
  await main.evaluate(() => window.hermesh?.relaunch());
  await closed;
});

test("with Stay Online off, quitting when idle stops the service without asking", async () => {
  // Interrupt the fixture's two active sessions so this Mac is completely idle.
  await cli("session", "interrupt", fixture.sessions["跑很久"]);
  await cli("session", "interrupt", fixture.sessions["选颜色"]);

  const app = await installer(true);
  const main = await app.firstWindow();
  await expect(await sessionList(main)).toContainText("写 hello");
  await expect.poll(() => app.evaluate(() => (globalThis as unknown as MainHook).hermeshTest.tray().view.running)).toBe(0);

  // The owner chose "only while Hermesh is open": nothing to ask while nothing runs.
  const killSeen = app.evaluate(
    () =>
      new Promise<boolean>((resolve) => {
        const check = () => ((globalThis as unknown as MainHook).installTest.launchctl.some(([v]) => v === "kill") ? resolve(true) : setTimeout(check, 20));
        check();
      }),
  );
  const closed = app.waitForEvent("close");
  await app.evaluate(({ app }) => { setImmediate(() => app.quit()); });
  expect(await killSeen).toBe(true);
  await closed;
});
