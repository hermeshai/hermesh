import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { LocalService } from "@hermesh/proto/local";
import { expect, test } from "@playwright/test";
import type { BrowserWindow } from "electron";
import { allowFixtureAISharing, fixture, launch, openWork, thisComputer } from "./app";

type Hook = { mainWindow(): BrowserWindow; notifications(): { id: string; body: string }[] };
declare const hermeshTest: Hook;

test("Her notifications follow the visible, focused conversation", async () => {
  test.setTimeout(180_000);
  const { app, page } = await launch();
  try {
    const focused = () => app.evaluate(() => hermeshTest.mainWindow().isFocused());
    const notices = () => app.evaluate(() => hermeshTest.notifications().filter((n) => n.id.startsWith("her-")).map((n) => n.body));
    // The runner keeps windows in the background. Drive Electron's focus signal for this window only.
    const setFocus = (on: boolean) => app.evaluate((_electron, on) => {
      const win = hermeshTest.mainWindow();
      if (on && win.isMinimized()) win.restore();
      win.isFocused = () => on;
      win.webContents.send("window-focus", on);
    }, on);
    await setFocus(true);

    const her = page.locator(".side-nav, .rail-nav").getByRole("button", { name: /^Her/ });
    await her.click();
    await page.getByRole("list", { name: "选择 Her 在哪里运行" }).getByRole("listitem")
      .filter({ hasText: thisComputer }).getByRole("button", { name: "开启 Her" }).click({ timeout: 60_000 });
    await expect(page.getByText(/^你好，我是 .+，在 .+ 上为你做事/)).toBeVisible({ timeout: 120_000 });
    const composer = page.getByRole("textbox", { name: "消息" });
    const replies = page.getByRole("log", { name: "对话" }).locator(".bm");
    await composer.fill("[[mock:send:foreground synthetic reply]] hi");
    await composer.press("Enter");
    await allowFixtureAISharing(page);
    await expect(replies.filter({ hasText: "foreground synthetic reply" })).toBeVisible({ timeout: 60_000 });
    await expect.poll(focused).toBe(true);
    expect(await notices()).not.toContain("foreground synthetic reply");

    const local = createClient(LocalService, createConnectTransport({
      baseUrl: "http://hermeshd", httpVersion: "1.1", nodeOptions: { socketPath: fixture.socket }, useBinaryFormat: true,
    }));
    const current = async () => (await local.listMachines({})).machines.find((m) => m.her?.latest?.botId);
    await expect.poll(async () => (await current())?.her?.latest?.text).toBe("foreground synthetic reply");
    const machine = (await current())!;
    const send = async (text: string) => {
      const result = await local.herSend({ machineId: machine.machineId, botId: machine.her!.latest!.botId, text });
      expect(result.delivered).toBe(true);
    };

    await openWork(app, page);
    await expect(composer).toHaveCount(0);
    await expect.poll(focused).toBe(true);
    await send("[[mock:send:other page synthetic reply]] hi");
    await expect.poll(notices).toContain("other page synthetic reply");

    await her.click();
    await expect(replies.filter({ hasText: "other page synthetic reply" })).toBeVisible();
    await setFocus(true);
    await expect.poll(notices).not.toContain("other page synthetic reply");
    await setFocus(false);
    await expect.poll(focused).toBe(false);
    await send("[[mock:send:blurred synthetic reply]] hi");
    await expect.poll(notices).toContain("blurred synthetic reply");

    await setFocus(true);
    await expect.poll(notices).not.toContain("blurred synthetic reply");
    await app.evaluate(() => hermeshTest.mainWindow().minimize());
    await expect.poll(() => app.evaluate(() => hermeshTest.mainWindow().isMinimized())).toBe(true);
    await send("[[mock:send:minimized synthetic reply]] hi");
    await expect.poll(notices).toContain("minimized synthetic reply");

    await setFocus(true);
    await expect(replies.filter({ hasText: "minimized synthetic reply" })).toBeVisible();
    await expect.poll(notices).not.toContain("minimized synthetic reply");
    await send("[[mock:ask:synthetic question|yes|no]] hi");
    await expect.poll(notices).toContainEqual(expect.stringContaining("synthetic question"));
  } finally {
    await app.close();
  }
});
