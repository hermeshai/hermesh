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
// is written waits on this Mac. Her's memory shows in her settings, as written,
// and is cleared there.

type Hook = { notifications(): { id: string; title: string; body: string; click(): void }[] };

/** her-mock answers a message that starts so by sending the owner `reply` (e2e/her_owner_test.go, mockSend). */
const mockSend = (reply: string) => `[[mock:send:${reply}]] hi`;

/** her-mock answers a message that starts so by asking to connect `url` as `name` on a secure card, and says how it went once it ended. */
const mockConnect = (name: string, url: string) => `[[mock:connect:${name}|${url}]] hi`;

/** A PDF of one line of Helvetica (a standard font it does not embed) on each of its pages. */
function pdf(lines: string[]): Buffer {
  const pages = lines.map((_, i) => 4 + i * 2);
  const objects = [
    "<< /Type /Catalog /Pages 2 0 R >>",
    `<< /Type /Pages /Kids [${pages.map((n) => `${n} 0 R`).join(" ")}] /Count ${lines.length} >>`,
    "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
    ...lines.flatMap((line, i) => {
      const text = `BT /F1 24 Tf 72 700 Td (${line}) Tj ET`;
      return [
        `<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents ${pages[i] + 1} 0 R >>`,
        `<< /Length ${text.length} >>\nstream\n${text}\nendstream`,
      ];
    }),
  ];
  let out = "%PDF-1.4\n";
  const offsets = objects.map((body, i) => {
    const at = out.length;
    out += `${i + 1} 0 obj\n${body}\nendobj\n`;
    return at;
  });
  const xref = out.length;
  out += `xref\n0 ${objects.length + 1}\n0000000000 65535 f \n${offsets.map((o) => `${String(o).padStart(10, "0")} 00000 n \n`).join("")}`;
  out += `trailer\n<< /Size ${objects.length + 1} /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`;
  return Buffer.from(out, "latin1");
}

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

  // The sidebar's Her entry, by her name alone. Her runs nowhere yet: its first run asks where, this Mac or
  // work-mac.
  await expect(page.locator(".side-nav").getByRole("button", { name: /^Her/ })).toHaveText("Her");
  await page.locator(".side-nav").getByRole("button", { name: /^Her/ }).click();
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
  // As on the iPhone: Her's cat in the middle, larger than a bar's, its name on a capsule over the cat's lower
  // edge, and the greeting clear of both, with no second avatar in the page.
  const cat = await page.locator(".her-id .bav.cat-av").boundingBox();
  const cap = await page.locator(".her-cap").boundingBox();
  const hello = await page.getByText(/^你好，我是 .+，在 .+ 上为你做事/).boundingBox();
  expect(cat!.width).toBe(56);
  expect(cap!.y).toBeLessThan(cat!.y + cat!.height);
  expect(cap!.y + cap!.height).toBeGreaterThan(cat!.y + cat!.height);
  expect(Math.abs(cat!.x + cat!.width / 2 - (cap!.x + cap!.width / 2))).toBeLessThan(1);
  expect(hello!.y).toBeGreaterThan(cap!.y + cap!.height);
  await expect(page.locator(".dbody .bav")).toHaveCount(0);
  await snap(page, "her-greeting");
  // Her's settings, beside the conversation, as on the iPhone: the avatar opens them (no button of their own in the
  // bar), the name too, both with the hand. A new assistant asks before risky steps, and remembers nothing yet.
  const avatar = page.getByRole("button", { name: "助手设置", exact: true });
  await expect(avatar.locator(".bav.cat-av")).toBeVisible();
  await expect(page.locator(".her-head").getByRole("button", { name: "助手设置" })).toHaveCount(1);
  for (const el of [avatar, page.locator(".her-id-n")]) expect(await el.evaluate((e) => getComputedStyle(e).cursor)).toBe("pointer");
  await avatar.click();
  const settings = page.getByRole("complementary", { name: "助手设置" });
  await expect(settings).toContainText("询问权限");
  await expect(settings.getByRole("button", { name: /^记住的事/ })).toHaveText("记住的事还没有");
  await page.locator(".her-id-n").click();
  await expect(settings).toBeHidden();

  // A chat: the message, then Her's reply in a bubble of its own.
  await send(mockSend("你好，主人"));
  await expect(log).toContainText("你好，主人", { timeout: 60_000 });
  // The window is behind (tests open it so): the reply is unread, and notified, by the assistant's name.
  await expect.poll(posted, { timeout: 60_000 }).toContainEqual(expect.objectContaining({ title: expect.stringMatching(/^Her/), body: "你好，主人" }));
  await snap(page, "her-chat");

  // Her notes a rule (her-mock's remember), with no approval asked; her settings show it on the memory page,
  // as written (a list, Markdown), and clear it there, after asking.
  await send("[[mock:remember:- 回复一律用中文。(2026-11-11)]] 以后都用中文");
  await expect(log).toContainText("记下了。", { timeout: 60_000 });
  await page.getByRole("button", { name: "助手设置", exact: true }).click();
  await expect(settings.getByRole("button", { name: /^记住的事/ })).toHaveText("记住的事");
  await settings.getByRole("button", { name: /^记住的事/ }).click();
  const memory = page.getByRole("complementary", { name: "记忆" });
  await expect(memory.locator(".mem-doc li")).toHaveText("回复一律用中文。(2026-11-11)");
  await expect(memory).toContainText("这就是它读到的内容");
  await snap(page, "her-memory");
  await memory.getByRole("button", { name: "返回" }).click();
  await settings.getByRole("button", { name: "清空记忆" }).click();
  const forget = page.getByRole("dialog", { name: "清空「Her」记住的事？" });
  await expect(forget).toContainText("聊天记录保留");
  await forget.getByRole("button", { name: "清空" }).click();
  await expect(settings.getByRole("button", { name: /^记住的事/ })).toHaveText("记住的事还没有", { timeout: 30_000 });
  await settings.getByRole("button", { name: /^记住的事/ }).click();
  await expect(memory).toContainText("还没有记下什么");
  await memory.getByRole("button", { name: "关闭" }).click();
  await expect(memory).toBeHidden();

  // A PDF and a Markdown file, sent to Her with the composer's button and sent back (her-mock's attach): a tile
  // of either, the owner's or Her's, opens it over the window instead of saving it to Downloads. The PDF shows
  // its pages, which can be searched, gone through by their pictures and zoomed; the Markdown reads as written
  // (its single line breaks are not a chat's), or as its source.
  const files = fs.mkdtempSync(path.join(os.tmpdir(), "hmfiles-"));
  fs.writeFileSync(path.join(files, "report.pdf"), pdf(["Hermesh preview page one", "The second page says hello"]));
  fs.writeFileSync(path.join(files, "notes.md"), "# Notes\n\nline one\nline two\n\n- a point\n");
  const errors: string[] = [];
  page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
  const chooser = page.waitForEvent("filechooser");
  await page.getByRole("button", { name: "添加文件" }).click();
  await (await chooser).setFiles([path.join(files, "report.pdf"), path.join(files, "notes.md")]);
  await send("[[mock:attach:uploads/report.pdf,uploads/notes.md]] 发回来");
  const sentBack = log.locator(".batt");
  await expect(sentBack.getByRole("button", { name: "查看 report.pdf" })).toBeVisible({ timeout: 60_000 });
  await sentBack.getByRole("button", { name: "查看 report.pdf" }).click();
  const preview = page.getByRole("dialog", { name: "report.pdf" });
  // Behind it the window goes dim and soft, as behind every dialog.
  expect(await preview.evaluate((el) => getComputedStyle(el, "::backdrop").backdropFilter)).toBe("blur(12px) saturate(1.2)");
  // The see-through sidebar turns opaque under it, so the blur covers its text too.
  await expect(page.locator(".side")).toHaveCSS("background-color", "rgb(244, 244, 247)");
  await expect(preview.locator(".dcp-page")).toContainText("/ 2", { timeout: 30_000 });
  await expect(preview.locator(".pdfViewer .page")).toHaveCount(2);
  await expect(preview.locator(".textLayer").first()).toContainText("Hermesh preview page one", { timeout: 30_000 });
  await preview.getByRole("button", { name: "在文档中搜索（⌘F）" }).click();
  await preview.getByPlaceholder("搜索文档").fill("hello");
  await preview.getByPlaceholder("搜索文档").press("Enter");
  await expect(preview.locator(".dcp-count")).toHaveText("1/1");
  await preview.getByRole("button", { name: "目录与页面" }).click();
  await expect(preview.locator(".dcp-thumb")).toHaveCount(2);
  await preview.locator(".dcp-thumb").nth(1).click();
  await expect(preview.getByRole("textbox", { name: "页码" })).toHaveValue("2");
  const scale = await preview.locator(".dcp-scale").textContent();
  await preview.getByRole("button", { name: "放大" }).click();
  await expect(preview.locator(".dcp-scale")).not.toHaveText(scale ?? "");
  await snap(page, "her-preview-pdf");
  // Esc closes the search first, then the preview.
  await preview.getByPlaceholder("搜索文档").press("Escape");
  await expect(preview.getByPlaceholder("搜索文档")).toBeHidden();
  await preview.locator(".dcp-scroll").press("Escape");
  await expect(preview).toBeHidden();
  // The owner's own Markdown, from where it was uploaded on Her's machine.
  await log.locator(".um").getByRole("button", { name: "查看 notes.md" }).click();
  const notes = page.getByRole("dialog", { name: "notes.md" });
  await expect(notes.getByRole("heading", { name: "Notes" })).toBeVisible({ timeout: 30_000 });
  await expect(notes.locator(".md p").first()).toHaveText(/line one\s+line two/);
  await expect(notes.locator(".md p br")).toHaveCount(0);
  await snap(page, "her-preview-md");
  await notes.getByRole("button", { name: "源码" }).click();
  await expect(notes.locator(".fpv-text")).toContainText("# Notes");
  await notes.getByRole("button", { name: "关闭预览" }).click();
  await expect(notes).toBeHidden();
  expect(errors.filter((e) => /Content Security Policy|pdf/i.test(e))).toEqual([]);

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
  await page.locator(".side-nav").getByRole("button", { name: /^Her/ }).click();
  await expect(page.getByText(/^work-mac 离线 · .+在线$/)).toBeVisible({ timeout: 30_000 });
  await expect(page.getByRole("log", { name: "对话" })).toContainText("来自 work-mac");
  await snap(page, "her-offline-restarted");
  await app.close();
});
