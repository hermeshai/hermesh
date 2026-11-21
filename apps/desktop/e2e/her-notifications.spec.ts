import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { LocalService } from "@hermesh/proto/local";
import type { BotResult } from "@hermesh/her-protocol";
import { expect, test } from "@playwright/test";
import type { BrowserWindow } from "electron";
import { fixture, launch, openWork, thisComputer } from "./app";

type Hook = { mainWindow(): BrowserWindow; notifications(): { id: string; body: string }[] };
declare const hermeshTest: Hook;
declare const shownNotices: string[];

test("Her notifications follow the visible, focused conversation", async () => {
  test.setTimeout(180_000);
  const { app, page } = await launch();
  try {
    // Record what reaches Electron, even when reading the chat immediately withdraws a notice.
    await app.evaluate(({ Notification }) => {
      const shown: string[] = [];
      Object.assign(globalThis, { shownNotices: shown });
      const show = Notification.prototype.show;
      Notification.prototype.show = function () {
        shown.push(this.body);
        show.call(this);
      };
    });
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
    await expect(replies.filter({ hasText: "foreground synthetic reply" })).toBeVisible({ timeout: 60_000 });
    await expect.poll(focused).toBe(true);
    expect(await app.evaluate(() => shownNotices)).not.toContain("foreground synthetic reply");

    const local = createClient(LocalService, createConnectTransport({
      baseUrl: "http://hermeshd", httpVersion: "1.1", nodeOptions: { socketPath: fixture.socket }, useBinaryFormat: true,
    }));
    const current = async () => (await local.listMachines({})).machines.find((m) => m.her?.latest?.botId);
    await expect.poll(async () => (await current())?.her?.latest?.text).toBe("foreground synthetic reply");
    const machine = (await current())!;
    const botId = machine.her!.latest!.botId;
    const send = async (text: string, bot = botId) => {
      const result = await local.herSend({ machineId: machine.machineId, botId: bot, text });
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
    await app.evaluate(() => {
      const win = hermeshTest.mainWindow();
      win.minimize();
      // The OS would also report a blur for a window it had focused.
      win.webContents.send("window-focus", false);
    });
    await expect.poll(() => app.evaluate(() => hermeshTest.mainWindow().isMinimized())).toBe(true);
    await send("[[mock:send:minimized synthetic reply]] hi");
    await expect.poll(notices).toContain("minimized synthetic reply");

    await setFocus(true);
    await expect(replies.filter({ hasText: "minimized synthetic reply" })).toBeVisible();
    await expect.poll(notices).not.toContain("minimized synthetic reply");
    const created = await local.herCall({ machineId: machine.machineId, call: { request: {
      method: "bots/create", params: Buffer.from(JSON.stringify({ name: "Fixture assistant", avatar: { kind: "mono", hue: "sage" } })),
    } } });
    expect(created.response?.error).toBeUndefined();
    const other = JSON.parse(Buffer.from(created.response!.result).toString("utf8")) as BotResult;
    await expect.poll(focused).toBe(true);
    await expect.poll(async () => (await current())?.her?.unread).toBe(0);
    await send("[[mock:send:other assistant synthetic reply]] hi", other.bot.id);
    await expect.poll(async () => (await current())?.her).toMatchObject({ unread: 1, latest: { botId: other.bot.id, text: "other assistant synthetic reply" } });
    await expect.poll(() => app.evaluate(() => shownNotices)).toContain("other assistant synthetic reply");

    // Keep the prompt unread long enough to exercise the notification path.
    await app.evaluate(() => hermeshTest.mainWindow().webContents.send("window-focus", false));
    await expect(page.locator("html")).toHaveClass(/\bblurred\b/);
    await send("[[mock:ask:synthetic question|yes|no]] hi");
    await expect(replies.filter({ hasText: "synthetic question" })).toBeVisible();
    await expect.poll(() => app.evaluate(() => shownNotices)).toContainEqual(expect.stringContaining("synthetic question"));
  } finally {
    await app.close();
  }
});
