import { chromium, expect, test, type Browser, type Page } from "@playwright/test";
import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import path from "node:path";

// Updating a packaged, Developer ID–signed Hermesh.app (run by
// TestDesktopUpdate): HERMESH_UPDATE_APP is version HERMESH_UPDATE_FROM,
// built to read its feed from a local server that offers
// HERMESH_UPDATE_TO. The app runs as people run it (see packaged.spec.ts),
// with its data directory (HERMESH_UPDATE_DATA) pointing at the fixture's
// daemon; TestDesktopUpdate also sets that for apps launchd starts, so the
// app Squirrel opens after installing finds it too.
const bundle = process.env.HERMESH_UPDATE_APP;
const from = process.env.HERMESH_UPDATE_FROM ?? "";
const to = process.env.HERMESH_UPDATE_TO ?? "";
const data = process.env.HERMESH_UPDATE_DATA ?? "";
const exe = bundle ? path.join(bundle, "Contents", "MacOS", "Hermesh") : "";
// Opened by launchd, the app runs from the resolved path (/private/var/… for /var/…).
const exes = bundle ? [exe, path.join(fs.realpathSync(bundle), "Contents", "MacOS", "Hermesh")] : [];

const freePort = () =>
  new Promise<number>((resolve) => {
    const s = net.createServer().listen(0, "127.0.0.1", () => {
      const p = (s.address() as net.AddressInfo).port;
      s.close(() => resolve(p));
    });
  });

const processes = () =>
  execFileSync("ps", ["-axo", "pid=,ppid=,command="], { encoding: "utf8" })
    .split("\n")
    .map((l) => l.trim().split(/\s+/))
    .filter((p) => p.length >= 3)
    .map(([pid, ppid, ...cmd]) => ({ pid: Number(pid), ppid: Number(ppid), command: cmd.join(" ") }));
/** The app's main process, however it was started. */
const running = () => processes().find((p) => exes.includes(p.command));
const signers = (parent: number) => processes().filter((p) => p.ppid === parent && p.command.includes("Hermesh Signer.app/Contents/MacOS/HermeshSigner"));
const bundleVersion = () => execFileSync("plutil", ["-extract", "CFBundleShortVersionString", "raw", path.join(bundle!, "Contents", "Info.plist")], { encoding: "utf8" }).trim();

/** Starts the app with a debugging port and returns its main window's page. */
async function start(): Promise<{ child: ChildProcess; browser: Browser; main: Page }> {
  // In Chinese, as the tests read the app; a setting, as the update's restart passes no arguments.
  const prefs = path.join(data, "desktop", "preferences.json");
  fs.mkdirSync(path.dirname(prefs), { recursive: true });
  fs.writeFileSync(prefs, JSON.stringify({ ...(fs.existsSync(prefs) ? JSON.parse(fs.readFileSync(prefs, "utf8")) : {}), language: "zh-CN" }));
  const port = await freePort();
  const child = spawn(exe, [`--remote-debugging-port=${port}`], { env: { ...process.env, HERMESH_DATA_DIR: data }, stdio: "ignore" });
  let browser: Browser | undefined;
  for (let i = 0; !browser && i < 60; i++) {
    browser = await chromium.connectOverCDP(`http://127.0.0.1:${port}`).catch(() => undefined);
    if (!browser) await new Promise((r) => setTimeout(r, 500));
  }
  expect(browser, "the app's debugging port").toBeTruthy();
  await expect.poll(() => browser!.contexts().flatMap((c) => c.pages()).length).toBeGreaterThan(0);
  const main = browser!.contexts().flatMap((c) => c.pages())[0];
  await expect(main.getByRole("list", { name: "会话" })).toContainText("写 hello");
  return { child, browser: browser!, main };
}

/** Opens the standalone update window through the same bridge as the native menu. */
async function openUpdates(browser: Browser, main: Page): Promise<Page> {
  await main.evaluate(() => window.hermesh?.openUpdates(true));
  let updates: Page | undefined;
  await expect.poll(() => (updates = browser.contexts().flatMap((c) => c.pages()).find((p) => p.url().includes("#updates")))).toBeTruthy();
  await expect(updates!.getByRole("main", { name: "更新" })).toBeVisible();
  return updates!;
}

test("the app finds, downloads and installs a new version, and restarts into it when asked", async () => {
  test.skip(!bundle, "set HERMESH_UPDATE_APP (TestDesktopUpdate)");
  test.setTimeout(5 * 60_000);
  expect(bundleVersion()).toBe(from);
  // 自动下载更新 off, so each step shows: available, downloading, ready.
  fs.mkdirSync(path.join(data, "desktop"), { recursive: true });
  fs.writeFileSync(path.join(data, "desktop", "updates.json"), JSON.stringify({ autoDownload: false }));

  const a = await start();
  const updates = await openUpdates(a.browser, a.main);
  const status = updates.getByRole("status", { name: "更新状态" });
  await expect(updates.getByText(`${from} → ${to}`)).toBeVisible();
  await expect(status).toHaveText("有可用更新");
  await expect(updates.getByText("从 Hermesh 菜单或侧栏的轻量提醒查看更新。")).toBeVisible();
  // Nothing is downloaded until asked: the main window has no restart button yet.
  await expect(a.main.getByRole("button", { name: "重新启动以更新" })).toHaveCount(0);

  await updates.getByRole("button", { name: "下载更新" }).click();
  // The test server holds the download for a few seconds.
  await expect(status).toContainText(`正在下载 ${to}…`);
  await expect(status).toHaveText("更新已就绪", { timeout: 60_000 });
  await expect(updates.getByRole("button", { name: "重新启动以更新" })).toBeVisible();
  // It waits for the owner: still the old version, still running.
  expect(bundleVersion()).toBe(from);
  expect(a.child.exitCode).toBeNull();

  // Preserve an unfinished answer and selection while the daemon keeps its active sessions.
  await a.main.getByRole("list", { name: "会话" }).getByRole("listitem").filter({ hasText: "选颜色" }).click();
  await a.main.getByRole("textbox", { name: "你的回答" }).fill("Draft survives the signed update");
  await a.main.getByRole("radio", { name: /蓝/ }).click();
  await updates.close();
  await a.main.getByRole("button", { name: /更新已就绪/ }).click();
  let ready: Page | undefined;
  await expect.poll(() => (ready = a.browser.contexts().flatMap((c) => c.pages()).find((p) => p.url().includes("#updates")))).toBeTruthy();
  await expect(ready!.getByText(/退出 Hermesh 时会安装此更新/)).toBeVisible();
  await ready!.getByRole("button", { name: "重新启动以更新" }).click();
  await expect.poll(() => a.child.exitCode !== null || a.child.signalCode !== null, { timeout: 30_000 }).toBe(true);
  await expect.poll(bundleVersion, { message: "Squirrel installed the new version", timeout: 60_000 }).toBe(to);
  // Squirrel opens the new version, which starts its own signer.
  let b: ReturnType<typeof running>;
  await expect.poll(() => (b = running()) !== undefined, { message: "the new version runs", timeout: 60_000 }).toBe(true);
  await expect.poll(() => signers(b!.pid).length, { message: "with its bundled signer" }).toBe(1);
  process.kill(b!.pid);
  await expect.poll(() => running() === undefined, { timeout: 15_000 }).toBe(true);

  // Started again: the new version, and nothing newer to get.
  const again = await start();
  try {
    await expect(again.main.getByRole("heading", { name: "选颜色" })).toBeVisible();
    await expect(again.main.getByRole("textbox", { name: "你的回答" })).toHaveValue("Draft survives the signed update");
    await expect(again.main.getByRole("radio", { name: /蓝/ })).toHaveAttribute("aria-checked", "true");
    const s = await openUpdates(again.browser, again.main);
    await expect(s.getByText(`版本 ${to}`, { exact: true })).toBeVisible();
    await expect(s.getByRole("status", { name: "更新状态" })).toContainText("已是最新版本");
    await expect(again.main.getByRole("button", { name: "重新启动以更新" })).toHaveCount(0);
  } finally {
    again.child.kill();
  }
});
