import { expect, test } from "@playwright/test";
import { fixture, launch, snap } from "./app";

// The Her entry (docs/her-server.md §9), as the owner uses it, against her
// itself with her-mock as its models. Run by TestDesktopHer, not TestDesktop:
// it needs her built. One story, as its steps build on each other: the first
// run on this Mac, Her's settings, a chat, the console, work-mac added as a
// second environment and switched between, then work-mac offline: what was
// read stays, and what is written waits on this Mac.

type Hook = { notifications(): { id: string; title: string; body: string; click(): void }[] };

/** her-mock answers a message that starts so by sending the owner `reply` (e2e/her_owner_test.go, mockSend). */
const mockSend = (reply: string) => `[[mock:send:${reply}]] hi`;

test("Her: the first run, a chat, the console, a second environment, and one gone offline", async () => {
  test.setTimeout(300_000);
  const { app, page } = await launch();
  const posted = () =>
    app.evaluate(() => (globalThis as unknown as { hermeshTest: Hook }).hermeshTest.notifications().map((n) => ({ id: n.id, title: n.title, body: n.body })));
  const log = page.getByRole("log", { name: "对话" });
  const composer = page.getByRole("textbox", { name: "消息" });
  const send = async (text: string) => {
    await composer.fill(text);
    await composer.press("Enter");
  };

  // The sidebar's Her entry. Her runs nowhere yet: its first run asks where, this Mac or work-mac.
  await page.getByRole("button", { name: /^Her 助手/ }).click();
  await expect(page.getByRole("heading", { name: "选择 Her 在哪里运行" })).toBeVisible();
  const choices = page.getByRole("list", { name: "选择 Her 在哪里运行" });
  await expect(choices.getByRole("listitem").filter({ hasText: "work-mac" }).getByRole("button", { name: "开启 Her" })).toBeVisible({ timeout: 60_000 });
  await snap(page, "her-first-run");
  // This Mac: the test's signer signs SetHer, as Touch ID would. Her runs there with the mock's model, and greets.
  await choices.getByRole("listitem").filter({ hasText: "这台 Mac" }).getByRole("button", { name: "开启 Her" }).click();
  await expect(page.getByText(/^你好，我是 .+，在 .+ 上为你做事/)).toBeVisible({ timeout: 120_000 });
  // The page is the conversation with Her alone: its name at the top, no list of assistants, no menu of
  // environments while there is one.
  await expect(page.locator(".her-id")).toContainText("Her");
  await expect(page.locator(".her-env")).toHaveCount(0);
  await snap(page, "her-greeting");
  // Her's settings, beside the conversation: a new assistant asks before risky steps.
  await page.getByRole("button", { name: "助手设置" }).click();
  await expect(page.getByRole("complementary", { name: "助手设置" })).toContainText("询问权限");
  await page.getByRole("button", { name: "助手设置" }).click();
  await expect(page.getByRole("complementary", { name: "助手设置" })).toBeHidden();

  // A chat: the message, then Her's reply in a bubble of its own.
  await send(mockSend("你好，主人"));
  await expect(log).toContainText("你好，主人", { timeout: 60_000 });
  // The window is behind (tests open it so): the reply is unread, and notified, by the assistant's name.
  await expect.poll(posted, { timeout: 60_000 }).toContainEqual(expect.objectContaining({ title: expect.stringMatching(/^Her/), body: "你好，主人" }));
  await snap(page, "her-chat");

  // The console, from ⋯: a window of its own at this Mac's hermeshd, which is none of the app's pages.
  const opened = app.waitForEvent("window");
  await page.getByRole("button", { name: "更多" }).click();
  await page.getByRole("menuitem", { name: /^管理「.+」…$/ }).click();
  const consolePage = await opened;
  await expect(consolePage.locator("body")).toContainText("the e2e console");
  expect(consolePage.url()).toMatch(/^http:\/\/127\.0\.0\.1:\d+\/her\//);
  expect(await consolePage.evaluate(() => typeof (window as { hermesh?: unknown }).hermesh)).toBe("undefined");
  await consolePage.close();

  // A second environment: work-mac, from ⋯ › 添加环境…. The page goes there once Her runs on it, and the
  // environment shows beside Her's name, as a menu of both.
  await page.getByRole("button", { name: "更多" }).click();
  await page.getByRole("menuitem", { name: "添加环境…" }).click();
  const sheet = page.getByRole("dialog", { name: "添加环境" });
  await sheet.getByRole("listitem").filter({ hasText: "work-mac" }).getByRole("button", { name: "开启 Her" }).click();
  await expect(sheet).toBeHidden();
  const switcher = page.locator(".her-env");
  await expect(switcher).toContainText("work-mac", { timeout: 120_000 });
  await expect(page.getByText(/^你好，我是 .+，在 work-mac 上为你做事/)).toBeVisible({ timeout: 120_000 });
  await send(mockSend("来自 work-mac"));
  await expect(log).toContainText("来自 work-mac", { timeout: 60_000 });

  // Back to this Mac, its own conversation; then to work-mac again.
  await switcher.click();
  await page.getByRole("menuitemradio").filter({ hasNotText: "work-mac" }).click();
  await expect(log).toContainText("你好，主人");
  await expect(log).not.toContainText("来自 work-mac");
  await switcher.click();
  await page.getByRole("menuitemradio").filter({ hasText: "work-mac" }).click();
  await expect(log).toContainText("来自 work-mac");
  await snap(page, "her-two-environments");

  // work-mac goes offline: what was read stays, under a line that says when it was last seen.
  process.kill(-fixture.otherMac, "SIGTERM");
  await expect(page.getByText(/^work-mac 离线 · .+在线$/)).toBeVisible({ timeout: 90_000 });
  await expect(log).toContainText("来自 work-mac");
  await snap(page, "her-offline");
  // Written meanwhile, a message waits on this Mac until work-mac is back; discarded, it goes.
  await send("等你回来再说");
  const waiting = log.locator(".um.waiting").filter({ hasText: "等你回来再说" });
  await expect(waiting).toContainText("等 work-mac 在线后送达");
  await waiting.getByRole("button", { name: "丢弃" }).click();
  await expect(waiting).toBeHidden();
  await app.close();
});
