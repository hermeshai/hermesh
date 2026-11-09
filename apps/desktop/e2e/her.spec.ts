import { expect, test } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fixture, launch, snap } from "./app";

// The Her entry (docs/her-server.md §9), as the owner uses it, against her
// itself with her-mock as its models. Run by TestDesktopHer, not TestDesktop:
// it needs her built. One story, as its steps build on each other: the first
// run on this Mac, Her's settings, a chat, a connector Her asks for on a
// secure card, the console, work-mac added as a second environment, with no
// model until Her's onboarding connects one, and switched between, then
// work-mac offline: what was read stays, after the app restarts too, and what
// is written waits on this Mac.

type Hook = { notifications(): { id: string; title: string; body: string; click(): void }[] };

/** her-mock answers a message that starts so by sending the owner `reply` (e2e/her_owner_test.go, mockSend). */
const mockSend = (reply: string) => `[[mock:send:${reply}]] hi`;

/** her-mock answers a message that starts so by asking to connect `url` as `name` on a secure card, and says how it went once it ended. */
const mockConnect = (name: string, url: string) => `[[mock:connect:${name}|${url}]] hi`;

test("Her: the first run, a chat, a secure card, the console, a second environment, and one gone offline", async () => {
  test.setTimeout(300_000);
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-"));
  let { app, page } = await launch(fixture.socket, { profile });
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
  await page.getByRole("button", { name: "助手设置", exact: true }).click();
  await expect(page.getByRole("complementary", { name: "助手设置" })).toContainText("询问权限");
  await page.getByRole("button", { name: "助手设置", exact: true }).click();
  await expect(page.getByRole("complementary", { name: "助手设置" })).toBeHidden();

  // A chat: the message, then Her's reply in a bubble of its own.
  await send(mockSend("你好，主人"));
  await expect(log).toContainText("你好，主人", { timeout: 60_000 });
  // The window is behind (tests open it so): the reply is unread, and notified, by the assistant's name.
  await expect.poll(posted, { timeout: 60_000 }).toContainEqual(expect.objectContaining({ title: expect.stringMatching(/^Her/), body: "你好，主人" }));
  await snap(page, "her-chat");

  // A connector Her asks for on a secure card (docs/her-secure-cards.md §4): the fixture's service, which signs in
  // with OAuth. Under Her's words, the card says it is an MCP connection, what Her wants to connect to, its address
  // large, what that lets Her do and to connect only what one trusts; nothing changes until the owner answers, and
  // the composer stays open meanwhile. Authorizing adds it, and its sign-in
  // opens in the browser (here, the stand-in records it), coming back to this Mac's hermeshd, and opens again when
  // asked. The service approves at once: the card folds into one line with the connector's tools.
  await app.evaluate(({ shell }) => {
    const pages: string[] = [];
    Object.assign(globalThis, { pages });
    shell.openExternal = async (url: string) => void pages.push(url);
  });
  const pages = () => app.evaluate(() => (globalThis as unknown as { pages: string[] }).pages);
  const service = new URL(fixture.connector ?? "");
  await send(mockConnect("tracker", service.href));
  const card = log.getByRole("group", { name: "MCP 连接" });
  await expect(card.locator(".sc-h")).toContainText("MCP 连接", { timeout: 60_000 });
  await expect(card).toContainText("Her 想连接 Tracker");
  await expect(card.locator(".sc-t b")).toContainText(service.hostname);
  await expect(card).toContainText("连接后，Her 可以使用它提供的工具，用到时会把相关内容发给它。只连接你信任的服务。");
  await expect(card).not.toContainText("不是你发起的");
  await expect(card.getByRole("button", { name: "拒绝" })).toBeVisible();
  await expect(composer).toBeEnabled();
  await snap(page, "her-card");
  await card.getByRole("button", { name: "授权连接" }).click();
  await expect(card).toContainText("等你在浏览器里登录并授权…", { timeout: 30_000 });
  await expect.poll(pages, { timeout: 30_000 }).toHaveLength(1);
  const [authorize] = await pages();
  const back = new URL(authorize).searchParams.get("redirect_uri") ?? "";
  expect(back).toMatch(/^http:\/\/127\.0\.0\.1:\d+\/callback$/);
  await card.getByRole("button", { name: "重新打开登录页" }).click();
  await expect.poll(pages).toEqual([authorize, authorize]);
  // The owner's browser: the service approves and redirects to this Mac's catcher.
  const landed = await fetch(authorize);
  expect(landed.ok).toBe(true);
  expect(landed.url.startsWith(`${back}?`)).toBe(true);
  await expect(log.locator(".sc-end.done")).toHaveText(/^已连接 Tracker · \d+ 个工具$/, { timeout: 60_000 });
  await expect(card).toHaveCount(0);
  // her-mock hears how it went, and says so.
  await expect(log.locator(".bm .bbub").last()).toContainText(/tracker/i, { timeout: 60_000 });
  await snap(page, "her-card-done");

  // The console, from ⋯: a window of its own at this Mac's hermeshd, which is none of the app's pages.
  const opened = app.waitForEvent("window");
  await page.getByRole("button", { name: "更多", exact: true }).click();
  await page.getByRole("menuitem", { name: /^管理「.+」…$/ }).click();
  const consolePage = await opened;
  await consolePage.waitForLoadState("domcontentloaded");
  expect(consolePage.url()).toMatch(/^http:\/\/127\.0\.0\.1:\d+\/her\//);
  // The test's console page, or the real one (HERMESH_HER_CONSOLE): served, whichever it is.
  expect(await consolePage.evaluate(async () => (await fetch(location.href)).status)).toBe(200);
  expect(await consolePage.evaluate(() => typeof (window as { hermesh?: unknown }).hermesh)).toBe("undefined");
  await consolePage.close();

  // A second environment: work-mac, from ⋯ › 添加环境…. The page goes there once Her runs on it, and the
  // environment shows beside Her's name, as a menu of both.
  await page.getByRole("button", { name: "更多", exact: true }).click();
  await page.getByRole("menuitem", { name: "添加环境…" }).click();
  const sheet = page.getByRole("dialog", { name: "添加环境" });
  await sheet.getByRole("listitem").filter({ hasText: "work-mac" }).getByRole("button", { name: "开启 Her" }).click();
  await expect(sheet).toBeHidden();
  const switcher = page.locator(".her-env");
  await expect(switcher).toContainText("work-mac", { timeout: 120_000 });
  // work-mac has no model yet: Her speaks first, the app writing for it, with the ways to connect one in a
  // list, each saying what it takes. The composer waits until then.
  await expect(log.getByText(/^你好，我是 .+，在 work-mac 上为你做事。开始之前，我需要接上一个模型/)).toBeVisible({ timeout: 120_000 });
  const ways = log.getByRole("group", { name: "接入方式" });
  await expect(ways.getByRole("button")).toHaveText([/^用 ChatGPT 登录/, /^接入 Token Plan/, /^填写 API Key/]);
  await expect(ways.getByRole("button", { name: "接入 Token Plan" })).toHaveAccessibleDescription("已订阅的模型套餐，如百炼、火山方舟、智谱 GLM");
  await expect(log).toContainText("Key 和登录都只留在 work-mac 上");
  await expect(composer).toBeDisabled();
  await expect(composer).toHaveAttribute("placeholder", "接上模型后就能回复你");
  await snap(page, "her-onboarding");

  // ChatGPT: her's sign-in page opens in the browser (here, the stand-in records it), coming back to this
  // Mac's hermeshd, and opens again when asked. Another way stops it, and Her offers the ways again.
  await app.evaluate(({ shell }) => {
    const opened: string[] = [];
    Object.assign(globalThis, { opened });
    shell.openExternal = async (url: string) => void opened.push(url);
  });
  const browser = () => app.evaluate(() => (globalThis as unknown as { opened: string[] }).opened);
  await log.getByRole("button", { name: "用 ChatGPT 登录" }).click();
  await expect(log.locator(".um").filter({ hasText: "用 ChatGPT 登录" })).toBeVisible();
  await expect(log).toContainText("我在浏览器里打开了 ChatGPT 的登录页，登录完回到这里就好。");
  await expect.poll(browser, { timeout: 60_000 }).toHaveLength(1);
  const [signInPage] = await browser();
  expect(signInPage).toMatch(/^https:\/\//);
  expect(new URL(signInPage).searchParams.get("redirect_uri")).toMatch(/^http:\/\/127\.0\.0\.1:\d+\/auth\/callback$/);
  await log.getByRole("button", { name: "重新打开登录页" }).click();
  await expect.poll(browser).toEqual([signInPage, signInPage]);
  await log.getByRole("button", { name: "换一种方式" }).click();
  await expect(log).toContainText("登录已取消。可以再试一次，或者换一种方式。", { timeout: 30_000 });
  await expect(log.getByRole("button", { name: "重新打开登录页" })).toHaveCount(0);

  // An API key: which provider (the best known first, at most five, the rest in the console), then the key,
  // in a field of its own. A key the provider refuses (her-mock, in DeepSeek's place, refuses this one) is
  // said in Her's words, and asked for again.
  await log.getByRole("button", { name: "填写 API Key" }).click();
  await expect(log).toContainText("用哪家的？");
  const providers = log.getByRole("group", { name: "服务商" });
  await expect(providers.getByRole("button")).toHaveCount(6);
  await expect(providers.getByRole("button").last()).toHaveAccessibleName("其他服务…");
  await providers.getByRole("button", { name: "DeepSeek", exact: true }).click();
  await expect(providers.getByRole("button", { name: "DeepSeek", exact: true })).toHaveAttribute("aria-current", "true");
  await expect(log).toContainText("把 DeepSeek 的 API Key 贴在下面。它只存进 work-mac 的凭据库，不会出现在对话里，也不会发给模型。");
  await log.getByRole("button", { name: "去 DeepSeek 获取 Key" }).click();
  await expect.poll(browser).toContain("https://platform.deepseek.com/api_keys");
  const key = () => log.getByLabel("DeepSeek 的 API Key");
  await expect(key()).toHaveAttribute("type", "password");
  await expect(key()).toHaveAttribute("autocomplete", "off");
  await key().fill("sk-rejected");
  await key().press("Enter");
  await expect(log).toContainText("这个 Key 没通过 DeepSeek 的检查：Authentication Fails, Your api key is invalid。再贴一次？", { timeout: 30_000 });
  await expect(key()).toHaveValue("");

  // Another way, Her asks which at once; then a Token Plan: which plan (each with a line telling it from the
  // others), then its key, the page to get one being the plan's own. her-mock stands in for Tencent Cloud's
  // plan, whose key only a reply can check.
  await log.getByRole("button", { name: "换一种方式" }).click();
  await expect(log).toContainText("好，换哪种方式？");
  await expect(key()).toHaveCount(0);
  await log.getByRole("group", { name: "接入方式" }).getByRole("button", { name: "接入 Token Plan" }).click();
  await expect(log).toContainText("接入哪个套餐？");
  const plans = log.getByRole("group", { name: "Token Plan" });
  await expect(plans.getByRole("button")).toHaveCount(6);
  await expect(plans.getByRole("button").first()).toHaveAccessibleName("百炼 Token Plan");
  await expect(plans.getByRole("button", { name: "火山方舟 Coding Plan" })).toHaveAccessibleDescription("Coding Plan 专属密钥");
  await plans.getByRole("button", { name: "腾讯云 Token Plan" }).click();
  await expect(log).toContainText("把 腾讯云 Token Plan 的 API Key 贴在下面。");
  // Picked, the conversation goes to its end: the key's field shows above the composer, whatever the
  // click scrolled to first.
  const planKey = log.getByLabel("腾讯云 Token Plan 的 API Key");
  const bottom = async () => ((b) => b.y + b.height)((await planKey.boundingBox())!);
  await expect.poll(async () => (await bottom()) <= (await page.locator(".cmp-col").boundingBox())!.y).toBe(true);
  await snap(page, "her-onboarding-plan");
  await log.getByRole("button", { name: "去 腾讯云 Token Plan 获取 Key" }).click();
  await expect.poll(browser).toContain("https://console.cloud.tencent.com/tokenhub");
  // One it takes: Her says which model it talks with, the onboarding folds into one line above, and the
  // composer opens. No key is in any of it.
  await planKey.fill("sk-e2e-plan-key");
  await log.getByRole("button", { name: "保存" }).click();
  await expect(log.getByText(/^好了，我会用 .+ 和你聊，想换可以去助手设置。有什么要我做的？$/)).toBeVisible({ timeout: 60_000 });
  await expect(log).toContainText("已接上 腾讯云 Token Plan");
  await expect(log).not.toContainText("接入哪个套餐？");
  await expect(composer).toBeEnabled();
  await expect(composer).toHaveAttribute("placeholder", /^发消息给 /);
  for (const secret of ["sk-e2e-plan-key", "sk-rejected"]) {
    await expect(page.locator("body")).not.toContainText(secret);
  }
  await expect(composer).toHaveValue("");
  await snap(page, "her-onboarding-done");
  await send(mockSend("来自 work-mac"));
  await expect(log).toContainText("来自 work-mac", { timeout: 60_000 });

  // Back to this Mac, its own conversation, with no onboarding of its own; then to work-mac again, which
  // keeps its line. The menu opens under the switcher, inside the window.
  await switcher.click();
  const [button, menu] = await Promise.all([switcher.boundingBox(), page.getByRole("menu").boundingBox()]);
  expect(Math.abs(menu!.x - button!.x)).toBeLessThan(2);
  expect(menu!.x + menu!.width).toBeLessThanOrEqual(page.viewportSize()?.width ?? (await page.evaluate(() => innerWidth)));
  await page.getByRole("menuitemradio").filter({ hasNotText: "work-mac" }).click();
  await expect(log).toContainText("你好，主人");
  await expect(log).not.toContainText("来自 work-mac");
  await expect(log).not.toContainText("已接上");
  await switcher.click();
  await page.getByRole("menuitemradio").filter({ hasText: "work-mac" }).click();
  await expect(log).toContainText("来自 work-mac");
  await expect(log).toContainText("已接上 腾讯云 Token Plan");
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

  // The app restarts while work-mac is away: what was said there still shows, as this Mac's hermeshd
  // keeps it, under the same line.
  await app.close();
  ({ app, page } = await launch(fixture.socket, { profile }));
  await page.getByRole("button", { name: /^Her 助手/ }).click();
  await expect(page.getByText(/^work-mac 离线 · .+在线$/)).toBeVisible({ timeout: 30_000 });
  await expect(page.getByRole("log", { name: "对话" })).toContainText("来自 work-mac");
  await snap(page, "her-offline-restarted");
  await app.close();
});
