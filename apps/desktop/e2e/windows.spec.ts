import { expect, test } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { launch, snap } from "./app";

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

test("the window starts small until hermeshd answers, then grows into the main window", async () => {
  const width = (app: Awaited<ReturnType<typeof launch>>["app"]) => app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0]?.getBounds().width ?? 0);
  const none = await launch(path.join(fs.mkdtempSync(path.join(os.tmpdir(), "hmsock-")), "none"));
  await expect(none.page.getByText("正在连接后台服务…")).toBeVisible();
  expect(await width(none.app)).toBe(360);
  await none.app.close();

  const { app, page } = await launch();
  await expect(page.getByRole("button", { name: "菜单" })).toBeVisible();
  await expect.poll(() => width(app)).toBeGreaterThan(900);
  await app.close();
});
