import { expect, test, type ElectronApplication } from "@playwright/test";
import type { BrowserWindow } from "electron";
import type { TrayView } from "../src/shared/bridge";
import { cli, fixture, launch, snap } from "./app";

// The native shell: the app menu, the menu bar panel and notifications, as
// the person uses them. Menu items are clicked through Electron's own menu
// objects (the real items, with their real handlers).

type Item = { label: string; sublabel: string; enabled: boolean; checked: boolean };
type Hook = {
  mainWindow(): BrowserWindow | undefined;
  tray(): { view: TrayView; open: boolean; badge: boolean; toggle(): void };
  notifications(): { id: string; title: string; body: string; click(): void }[];
};
const menuItem = (app: ElectronApplication, id: string) =>
  app.evaluate(({ Menu }, id): Item | null => {
    const i = Menu.getApplicationMenu()?.getMenuItemById(id);
    return i ? { label: i.label, sublabel: i.sublabel, enabled: i.enabled, checked: i.checked } : null;
  }, id);

const clickMenu = (app: ElectronApplication, id: string) =>
  app.evaluate(({ Menu }, id) => {
    Menu.getApplicationMenu()?.getMenuItemById(id)?.click();
  }, id);

const tray = (app: ElectronApplication) =>
  app.evaluate(() => {
    const t = (globalThis as unknown as { hermeshTest: Hook }).hermeshTest.tray();
    return { view: t.view, open: t.open, badge: t.badge };
  });

test("the app menu drives the main window", async () => {
  const { app, page } = await launch();
  const list = page.getByRole("list", { name: "会话" });
  const row = (title: string) => list.getByRole("listitem").filter({ hasText: title });
  await row("写 hello").click();
  await expect(page.getByRole("heading", { name: "写 hello" })).toBeVisible();
  await expect.poll(() => menuItem(app, "overview")).toMatchObject({ checked: false, enabled: true });

  // 显示 → 概览 (⌘1)
  await clickMenu(app, "overview");
  await expect(page.getByRole("heading", { name: "概览", level: 1 })).toBeVisible();
  await expect.poll(() => menuItem(app, "overview")).toMatchObject({ checked: true });

  // ⌘K: the search palette
  await clickMenu(app, "search");
  await expect(page.getByRole("dialog", { name: "搜索会话" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog", { name: "搜索会话" })).toHaveCount(0);

  // 显示 → 隐藏边栏 / 显示边栏 (⌃⌘S)
  await clickMenu(app, "toggleSidebar");
  await expect(page.getByRole("button", { name: "显示边栏" })).toBeVisible();
  await expect.poll(() => menuItem(app, "toggleSidebar")).toMatchObject({ label: "显示边栏" });
  await clickMenu(app, "toggleSidebar");
  await expect(page.getByRole("button", { name: "隐藏边栏" })).toBeVisible();
  await expect.poll(() => menuItem(app, "toggleSidebar")).toMatchObject({ label: "隐藏边栏" });

  // A session waiting on a question: 回答问题 yes, 归档 no.
  await row("选颜色").click();
  await expect.poll(() => menuItem(app, "answer")).toMatchObject({ enabled: true });
  expect(await menuItem(app, "archive")).toMatchObject({ enabled: false });
  expect(await menuItem(app, "interrupt")).toMatchObject({ enabled: true });
  expect(await menuItem(app, "filterGrant")).toMatchObject({ label: "只看「bot」的会话", enabled: true });
  expect(await menuItem(app, "filterProject")).toMatchObject({ label: "只看「demo」的会话", enabled: true });
  await clickMenu(app, "answer");
  await expect(page.getByRole("group", { name: "问题" }).locator(":focus")).toHaveCount(1);

  // The conversation is the only view: no 对话 / 改动 items.
  expect(await menuItem(app, "chatTab")).toBeNull();
  expect(await menuItem(app, "diffTab")).toBeNull();
  expect(await menuItem(app, "viewDiff")).toBeNull();

  // A finished session: 归档 (⌘E), then 编辑 → 撤销 (⌘Z) brings it back.
  await row("写 hello").click();
  await expect.poll(() => menuItem(app, "archive")).toMatchObject({ enabled: true });
  expect(await menuItem(app, "answer")).toMatchObject({ enabled: false });
  await clickMenu(app, "archive");
  await expect(page.getByRole("status")).toContainText("已归档「写 hello」");
  await expect(row("写 hello")).toHaveCount(0);
  await expect.poll(() => menuItem(app, "unarchive")).toMatchObject({ enabled: true });
  await snap(page, "menu-archived");
  await clickMenu(app, "undo");
  await expect(row("写 hello")).toHaveCount(1);
  await expect.poll(() => menuItem(app, "archive")).toMatchObject({ enabled: true });
  await app.close();
});

test("the menu bar panel counts what needs the owner and what runs; a count opens the first one", async () => {
  const { app, page } = await launch();
  const pairings = await page.evaluate(() =>
    fetch("app://hermesh/api/hermesh.local.v1.LocalService/ListPairingRequests", { method: "POST", headers: { "content-type": "application/json" }, body: "{}" })
      .then((r) => r.json())
      .then((r: { pairings?: unknown[] }) => r.pairings?.length ?? 0),
  );
  // A question and two failures, and every pairing request; one running.
  await expect.poll(async () => (await tray(app)).view).toMatchObject({ state: "ok", online: 1, attention: 3 + pairings, running: 1 });
  expect((await tray(app)).badge).toBe(true);

  // The icon opens the panel under it.
  const [win] = await Promise.all([
    app.waitForEvent("window", (w) => w.url().endsWith("#tray")),
    app.evaluate(() => (globalThis as unknown as { hermeshTest: Hook }).hermeshTest.tray().toggle()),
  ]);
  // Another app taking focus closes it, as a menu bar panel does (the specs run side by side): opened again then.
  const toggle = () => app.evaluate(() => (globalThis as unknown as { hermeshTest: Hook }).hermeshTest.tray().toggle());
  await expect.poll(async () => (await tray(app)).open || (await toggle(), false)).toBe(true);
  await expect(win.getByRole("status")).toHaveText("正常");
  await expect(win.getByText("你的 agent 能用 1 台机器")).toBeVisible();
  await expect(win.getByRole("button", { name: `需要处理 ${3 + pairings}` })).toBeVisible();
  await expect(win.getByRole("button", { name: "进行中 1" })).toBeVisible();
  await expect(win.getByRole("button", { name: "设置" })).toBeVisible();
  await expect(win.getByRole("button", { name: "退出 Hermesh" })).toBeVisible();
  await snap(win, "tray-panel");

  // 需要处理 opens the first thing waiting: a pairing request, else the question.
  await win.getByRole("button", { name: /^需要处理/ }).click();
  await expect.poll(async () => (await tray(app)).open).toBe(false);
  if (pairings) await expect(page.getByRole("dialog")).toBeVisible();
  else await expect(page.getByRole("heading", { name: "选颜色" })).toBeVisible();
  await page.keyboard.press("Escape");

  // 进行中 opens the running one.
  await app.evaluate(() => (globalThis as unknown as { hermeshTest: Hook }).hermeshTest.tray().toggle());
  await win.getByRole("button", { name: "进行中 1" }).click();
  await expect(page.getByRole("heading", { name: "跑很久" })).toBeVisible();

  // 通用's switch takes the dot off the icon.
  await page.evaluate(() => window.hermesh?.setPreferences({ menuBadge: false }));
  await expect.poll(async () => (await tray(app)).badge).toBe(false);
  expect(await page.evaluate(() => window.hermesh?.getPreferences())).toMatchObject({ menuBadge: false, notifyQuestion: true, notifyTurnDone: false });
  await app.close();
});

test("a question notifies, and the notification opens its session", async () => {
  const { app, page } = await launch();
  // The main process has had its first look: what was there before is no news.
  await expect.poll(async () => (await tray(app)).view.attention).toBeGreaterThan(0);
  expect(await app.evaluate(() => (globalThis as unknown as { hermeshTest: Hook }).hermeshTest.notifications().length)).toBe(0);

  // The agent asks something new. (In the archived session, brought back for
  // this and archived again after: a new session would change the counts
  // other tests rely on.)
  const id = fixture.sessions["归档的"];
  await cli("session", "unarchive", id);
  try {
    await cli("session", "send", id, "--message", "ask 要继续吗|是,否");
    const posted = () =>
      app.evaluate(() => (globalThis as unknown as { hermeshTest: Hook }).hermeshTest.notifications().map((n) => ({ id: n.id, title: n.title, body: n.body })));
    await expect.poll(posted, { timeout: 30_000 }).toContainEqual({ id: `session-question-${id}`, title: "bot 在等你回答", body: "归档的：要继续吗" });
    // Clicking it opens the session (the person clicks the banner).
    await app.evaluate((_e, id) => {
      const hook = (globalThis as unknown as { hermeshTest: Hook }).hermeshTest;
      hook.notifications().find((n) => n.id === `session-question-${id}`)?.click();
    }, id);
    await expect(page.getByRole("heading", { name: "归档的" })).toBeVisible();
    await expect(page.getByRole("group", { name: "问题" })).toContainText("要继续吗");
  } finally {
    await cli("session", "interrupt", id).catch(() => "");
    await expect.poll(() => cli("session", "archive", id).then(() => true, () => false)).toBe(true);
  }
  await app.close();
});

test("closing the window keeps Hermesh in the menu bar, without a renderer", async () => {
  const { app, page } = await launch();
  await expect(page.getByRole("list", { name: "会话" })).toContainText("写 hello");
  const memory = async () => {
    const metrics = await app.evaluate(({ app }) => app.getAppMetrics().map((m) => ({ type: m.type, kb: m.memory.workingSetSize })));
    const mb = (kb: number) => Math.round(kb / 1024);
    return `${mb(metrics.reduce((n, m) => n + m.kb, 0))} MB (${metrics.map((m) => `${m.type} ${mb(m.kb)}`).join(", ")})`;
  };
  console.log(`memory with the window open: ${await memory()}`);
  await app.evaluate(() => (globalThis as unknown as { hermeshTest: Hook }).hermeshTest.mainWindow()?.close());
  await expect.poll(() => app.evaluate(({ app }) => app.getAppMetrics().filter((m) => m.type === "Tab").length)).toBe(0);
  await expect.poll(() => app.evaluate(({ app }) => app.dock?.isVisible())).toBe(false);

  // Idle: let the closed renderer's memory go back.
  await new Promise((r) => setTimeout(r, 5000));
  console.log(`idle memory with the window closed: ${await memory()}`);

  // The menu bar panel opens it again; the panel itself doesn't bring the Dock icon back.
  const [panel] = await Promise.all([
    app.waitForEvent("window", (w) => w.url().endsWith("#tray")),
    app.evaluate(() => (globalThis as unknown as { hermeshTest: Hook }).hermeshTest.tray().toggle()),
  ]);
  expect(await app.evaluate(({ app }) => app.dock?.isVisible())).toBe(false);
  const opened = app.waitForEvent("window", (w) => !w.url().endsWith("#tray"));
  await panel.getByRole("button", { name: "打开 Hermesh" }).click();
  const again = await opened;
  await expect(again.getByRole("list", { name: "会话" })).toContainText("写 hello");
  await expect.poll(() => app.evaluate(({ app }) => app.dock?.isVisible())).toBe(true);
  await app.close();
});
