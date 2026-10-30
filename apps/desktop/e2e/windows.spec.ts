import { expect, test } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fixture, launch, snap } from "./app";

// The app on Windows (docs/windows.md §5), against the real daemon there:
// a PC's words and Ctrl shortcuts, the menu button where macOS has the
// menu bar, the notification area's icon, and staying there once the
// window closes. The rest of the suite runs on macOS.
test.skip(process.platform !== "win32", "Windows only");

type Hook = { tray(): { view: { state: string } }; mainWindow(): { close(): void } | undefined };

test("a PC's words and Ctrl shortcuts, the menu button, the notification area, and staying there without a window", async () => {
  const { app, page } = await launch();
  await expect(page.locator("html")).toHaveAttribute("data-platform", "win");
  const map = page.getByRole("region", { name: "链路" });
  await expect(map.getByRole("button", { name: "这台电脑", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /概览/ })).toContainText("Ctrl+1");
  await expect(page.getByRole("button", { name: "菜单" })).toBeVisible();
  await expect.poll(() => app.evaluate(() => (globalThis as unknown as { hermeshTest: Hook }).hermeshTest.tray().view.state)).toBe("ok");
  await snap(page, "windows");
  await app.evaluate(() => (globalThis as unknown as { hermeshTest: Hook }).hermeshTest.mainWindow()?.close());
  await expect.poll(() => app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows().length)).toBe(0);
  // Still there: its notification area's icon still follows the daemon.
  expect(await app.evaluate(() => (globalThis as unknown as { hermeshTest: Hook }).hermeshTest.tray().view.state)).toBe("ok");
  await app.close();
});

test("the window opens at the size it had last time, loading in the middle until hermeshd answers", async () => {
  const width = (app: Awaited<ReturnType<typeof launch>>["app"]) => app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0]?.getBounds().width ?? 0);
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-"));
  const first = await launch(undefined, { profile });
  await expect(first.page.getByRole("button", { name: "菜单" })).toBeVisible();
  await expect.poll(() => width(first.app)).toBeGreaterThan(900);
  await first.app.close();

  const { app, page } = await launch(path.join(fs.mkdtempSync(path.join(os.tmpdir(), "hmsock-")), "none"), { profile });
  await expect(page.getByText("正在连接后台服务…")).toBeVisible();
  expect(await width(app)).toBeGreaterThan(900);
  await app.close();
});

test("an older signer set up here: Needs You offers the app's, with one UAC prompt", async () => {
  const { page, app } = await launch(undefined, { env: { HERMESH_TEST_SIGNER_DIR: fixture.oldSigner! } });
  const needs = page.getByRole("region", { name: "需要你处理" });
  await expect(needs).toContainText("Hermesh 签名器有新版本");
  await expect(needs).toContainText("这台电脑装的是 0.0.1。");
  await expect(needs.getByRole("button", { name: "更新…" })).toBeVisible();
  await snap(page, "windows-signer-update");
  await app.close();

  // Up to date: nothing to offer.
  const current = await launch();
  await expect(current.page.getByRole("region", { name: "链路" })).toBeVisible();
  await expect(current.page.getByText("Hermesh 签名器有新版本")).toHaveCount(0);
  await current.app.close();
});
