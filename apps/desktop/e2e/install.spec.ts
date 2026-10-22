import { expect, test } from "@playwright/test";
import { snap } from "./app";

import { installer, type Hook } from "./install-app";

test.skip(process.platform !== "darwin", "macOS installation");

// Run the built app, replacing only OS boundaries: the location check,
// native dialogs, relocation and login-item registration. Each instance
// has an isolated home and the fixture's daemon, so no installed copy or
// real login item changes. Packaged app/DMG and Squirrel tests run too.

test("outside Applications, installation precedes windows, links and background registration; Quit exits", async () => {
  const app = await installer();
  await expect.poll(() => app.evaluate(() => (globalThis as unknown as Hook).installTest.dialogs.length)).toBe(1);
  const dialog = await app.evaluate(() => (globalThis as unknown as Hook).installTest.dialogs[0]);
  expect(dialog.message).toBe("要把 Hermesh 移到「应用程序」文件夹吗？");
  expect(dialog.detail).toContain("在后台运行并自动更新");
  expect(dialog.buttons).toEqual(["移到「应用程序」文件夹", "退出"]);
  expect(dialog.defaultId).toBe(0);
  expect(dialog.cancelId).toBe(1);
  await app.evaluate(({ app }) => {
    app.emit("open-url", { preventDefault() {} }, "hermesh://join?owner=o_" + "a".repeat(24));
    app.emit("second-instance", {}, []);
  });
  expect(app.windows()).toHaveLength(0);
  expect(await app.evaluate(() => {
    const s = (globalThis as unknown as Hook).installTest;
    return [s.loginItems.length, s.protocols.length, s.moved];
  })).toEqual([0, 0, 0]);
  await Promise.all([app.waitForEvent("close"), app.evaluate(() => (globalThis as unknown as Hook).installTest.respond(1))]);
});

test("choosing Move hands off to macOS and exits without starting the old copy", async () => {
  const app = await installer();
  await expect.poll(() => app.evaluate(() => (globalThis as unknown as Hook).installTest.dialogs.length)).toBe(1);
  // Keep the process alive long enough to check the handoff, then let it quit.
  await app.evaluate(({ app }) => app.once("before-quit", (e) => e.preventDefault()));
  await app.evaluate(() => (globalThis as unknown as Hook).installTest.respond(0));
  await expect.poll(() => app.evaluate(() => (globalThis as unknown as Hook).installTest.moved)).toBe(1);
  expect(app.windows()).toHaveLength(0);
  expect(await app.evaluate(() => (globalThis as unknown as Hook).installTest.loginItems)).toEqual([]);
});

for (const move of ["cancel", "error"] as const) {
  test(`a ${move === "cancel" ? "cancelled authorization" : "failed copy"} explains installation in Finder, then exits`, async () => {
    const app = await installer(false, move);
    await expect.poll(() => app.evaluate(() => (globalThis as unknown as Hook).installTest.dialogs.length)).toBe(1);
    await app.evaluate(() => (globalThis as unknown as Hook).installTest.respond(0));
    await expect.poll(() => app.evaluate(() => (globalThis as unknown as Hook).installTest.dialogs.length)).toBe(2);
    const failure = await app.evaluate(() => (globalThis as unknown as Hook).installTest.dialogs[1]);
    expect(failure.message).toBe("没能把 Hermesh 移到「应用程序」文件夹");
    expect(failure.detail).toBe("请在访达中把 Hermesh 拖到「应用程序」文件夹，再从那里打开。");
    expect(failure.buttons).toEqual(["好"]);
    expect(app.windows()).toHaveLength(0);
    expect(await app.evaluate(() => (globalThis as unknown as Hook).installTest.loginItems)).toEqual([]);
    await Promise.all([app.waitForEvent("close"), app.evaluate(() => (globalThis as unknown as Hook).installTest.respond(0))]);
  });
}

test("from Applications the app starts normally; update errors explain recovery and fold technical details", async () => {
  const app = await installer(true);
  const page = await app.firstWindow();
  await expect(page.getByRole("list", { name: "会话" })).toContainText("写 hello");
  expect(await app.evaluate(() => (globalThis as unknown as Hook).installTest.dialogs)).toEqual([]);
  expect(await app.evaluate(() => (globalThis as unknown as Hook).installTest.loginItems)).toHaveLength(1);
  expect(await app.evaluate(() => (globalThis as unknown as Hook).installTest.protocols)).toEqual(["hermesh"]);
  const [updates] = await Promise.all([app.waitForEvent("window"), page.evaluate(() => window.hermesh?.openUpdates(true))]);
  const status = updates.getByRole("status", { name: "更新状态" });
  await expect(status).toContainText("已是最新版本");

  for (const [message, text] of [
    ["Cannot update while running on a read-only volume. https://github.com/Squirrel/Squirrel.Mac/issues/182", "Hermesh 要放在「应用程序」文件夹里才能自动更新"],
    ["Could not get code signature for running application", "没能下载更新，Hermesh 稍后会再试"],
  ]) {
    await app.evaluate(({ autoUpdater }, message) => autoUpdater.emit("error", new Error(message)), message);
    await expect(updates.getByText(text, { exact: false })).toBeVisible();
    await expect(updates.getByText(message, { exact: true })).not.toBeVisible();
    const details = updates.locator("summary", { hasText: "技术详情" });
    await expect(updates.locator("details")).not.toHaveAttribute("open");
    await details.click();
    await expect(updates.getByText(message, { exact: true })).toBeVisible();
    await expect(updates.locator("details")).toHaveAttribute("open", "");
    await updates.locator(".upd-raw").scrollIntoViewIfNeeded();
    await snap(updates, "update-error-details");
    await details.click();
    await expect(updates.getByText(message, { exact: true })).not.toBeVisible();
    await details.click();
    await updates.getByRole("button", { name: "重试", exact: true }).click();
    await expect(status).toContainText("已是最新版本");
    await expect(updates.locator(".upd-raw")).toHaveCount(0);
  }
  await app.evaluate(() => { (globalThis as unknown as Hook).installTest.feedFails = true; });
  await updates.evaluate(() => window.hermesh?.checkForUpdates());
  await expect(updates.getByText("没能检查更新，请检查网络连接。", { exact: true })).toBeVisible();
  await expect(updates.locator(".upd-raw")).not.toBeVisible();
  const details = updates.locator("summary", { hasText: "技术详情" });
  await expect(updates.locator("details")).not.toHaveAttribute("open");
  await updates.getByRole("status").scrollIntoViewIfNeeded();
  await snap(updates, "update-error");
  await details.click();
  await expect(updates.locator(".upd-raw")).toContainText("HTTP 503");
  await app.evaluate(() => { (globalThis as unknown as Hook).installTest.feedFails = false; });
  await updates.getByRole("button", { name: "重试", exact: true }).click();
  await expect(status).toContainText("已是最新版本");
});
