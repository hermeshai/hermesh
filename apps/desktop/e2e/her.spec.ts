import { expect, test, type Locator, type Page } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fixture, launch, snap, navTo, sessionList, sys, thisComputer } from "./app";

// The Her entry (docs/her-server.md §9), as the owner uses it, against her
// itself with her-mock as its models. Run by TestDesktopHer, not TestDesktop:
// it needs her built. One story, as its steps build on each other: the first
// run on this Mac, Her's settings, a chat, a connector Her asks for on a
// secure card, the console, work-mac added as a second environment, with no
// model until Her's onboarding connects one, and switched between, then
// work-mac offline: what was read stays, after the app restarts too, and what
// is written waits on this Mac. Her's memory shows in her settings, as written,
// and is cleared there. Last, Her is turned off on this Mac, her data kept,
// then erased; removing work-mac, offline, keeps hers.

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

  // Conversations open at the end and virtualize earlier rows; read back through history
  // before checking the first reply, including after restarting the app or Her.
  const readGreeting = async (timeout: number) => {
    const log = page.getByRole("log", { name: "对话" });
    const scroll = page.locator(".cscroll").filter({ has: log });
    await expect.poll(async () => {
      await scroll.hover();
      await page.mouse.wheel(0, -2000);
      return log.textContent();
    }, { timeout }).toContain("你好，主人");
  };

  // The sidebar's Her entry, by her name alone. Her runs nowhere yet: its first run asks where, this Mac or
  // work-mac.
  await expect(page.locator(".side-nav").getByRole("button", { name: /^Her/ })).toHaveText("Her未启用");
  await page.locator(".side-nav").getByRole("button", { name: /^Her/ }).click();
  await expect(page.getByRole("heading", { name: "选择 Her 在哪里运行" })).toBeVisible();
  const choices = page.getByRole("list", { name: "选择 Her 在哪里运行" });
  await expect(choices.getByRole("listitem").filter({ hasText: "work-mac" }).getByRole("button", { name: "开启 Her" })).toBeVisible({ timeout: 60_000 });
  await snap(page, "her-first-run");
  // This Mac: the test's signer signs SetHer, as Touch ID would. Her runs there with the mock's model, and greets.
  await choices.getByRole("listitem").filter({ hasText: thisComputer }).getByRole("button", { name: "开启 Her" }).click();
  await expect(page.getByText(/^你好，我是 .+，在 .+ 上为你做事/)).toBeVisible({ timeout: 120_000 });
  // The page is the conversation with Her alone: its name at the top, no list of assistants, no environments in
  // its ⋯ menu while there is one.
  await expect(page.locator(".her-id")).toContainText("Her");
  expect(await environmentNames(page)).toEqual([]);
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
  // Her's page, beside the conversation, as on the iPhone (docs/her-panel-demo.html): the avatar opens it (no
  // button of its own in the bar, nor Progress, Files or Scheduled Tasks there); the capsule under it is the team's
  // name tag, which opens the team panel (docs/her-team.md §14.3). Both with the hand. Three tabs, Work first: a new
  // assistant remembers nothing yet, has no connectors, and asks before risky steps.
  const avatar = page.getByRole("button", { name: "助手设置", exact: true });
  const tag = page.getByRole("button", { name: "团队：现在在做什么" });
  await expect(avatar.locator(".bav.cat-av")).toBeVisible();
  await expect(page.locator(".her-head").getByRole("button", { name: "助手设置" })).toHaveCount(1);
  for (const el of [avatar, tag]) expect(await el.evaluate((e) => getComputedStyle(e).cursor)).toBe("pointer");
  // Progress, looked at often, is a button of her top; her files and scheduled tasks are on her page.
  await expect(page.locator(".her-head").getByRole("button", { name: /^进展/ })).toHaveCount(1);
  await expect(page.locator(".her-head").getByRole("button", { name: /^(文件|定时任务)/ })).toHaveCount(0);
  await avatar.click();
  const settings = page.getByRole("complementary", { name: "助手设置" });
  await expect(settings.getByRole("tab", { name: "工作" })).toHaveAttribute("aria-selected", "true");
  for (const id of ["her-files", "her-routines"]) await expect(settings.getByTestId(id)).toBeVisible();
  await expect(settings.getByTestId("her-progress")).toHaveCount(0);
  await snap(page, "her-page-work");
  await settings.getByRole("tab", { name: "能力" }).click();
  await expect(settings.getByTestId("her-memory")).toHaveAttribute("aria-label", "记忆，还没有", { timeout: 30_000 });
  // Her's connectors (docs/her-connectors-demo.html): the environment's, as themselves, with none yet.
  await expect(settings.getByTestId("her-connectors")).toHaveText("连接器还没有", { timeout: 30_000 });
  for (const id of ["her-skills", "her-search", "her-browser"]) await expect(settings.getByTestId(id)).toBeVisible();
  await snap(page, "her-page-abilities");
  await settings.getByRole("tab", { name: "设置" }).click();
  await expect(settings).toContainText("询问权限");
  await expect(settings.getByTestId("her-environment")).toContainText("环境");
  await snap(page, "her-page-settings");
  await avatar.click();
  await expect(settings).toBeHidden();
  await tag.click();
  const team = page.getByRole("dialog", { name: "团队" });
  await expect(team).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(team).toBeHidden();
  await expect(tag).toBeFocused();

  // A chat: desktop sends directly without a backend AI-sharing consent modal,
  // and the Model page exposes no user-facing AI sharing review or revoke entry.
  const firstDraft = mockSend("你好，主人");
  await send(firstDraft);
  await expect(page.getByRole("dialog", { name: "与第三方 AI 共享数据？" })).toHaveCount(0);
  await expect(log).toContainText("你好，主人", { timeout: 60_000 });
  // The window is behind (tests open it so): the reply is unread, and notified, by the assistant's name.
  await expect.poll(posted, { timeout: 60_000 }).toContainEqual(expect.objectContaining({ title: expect.stringMatching(/^Her/), body: "你好，主人" }));
  await snap(page, "her-chat");
  await avatar.click();
  await settings.getByRole("tab", { name: "设置" }).click();
  await settings.getByTestId("her-model-page").click();
  const modelsPage = page.getByTestId("her-models-page");
  await expect(modelsPage).toBeVisible({ timeout: 30_000 });
  await expect(modelsPage).not.toContainText("撤回第三方 AI 共享授权");
  await expect(modelsPage).not.toContainText("Revoke Third-Party AI Sharing");
  await expect(modelsPage).not.toContainText("查看第三方 AI 共享授权");
  await modelsPage.getByRole("button", { name: "关闭" }).click();
  await expect(modelsPage).toBeHidden();
  // The owner's bubble is a session page's: the brand blue with white words, as on the iPhone.
  await expect(log.locator(".um .bub").first()).toHaveCSS("background-color", "rgb(61, 91, 255)");
  await expect(log.locator(".um .bub").first()).toHaveCSS("color", "rgb(255, 255, 255)");
  // A message leaves the composer at once and shows once (docs/her-sending.md): it goes by its id through hermeshd,
  // on this conversation's connection, and stays where it is from being sent to her having it.
  await send(mockSend("只收到一次"));
  await expect(composer).toHaveValue("", { timeout: 1_000 });
  await expect(log).toContainText("只收到一次", { timeout: 60_000 });
  await expect(log.locator(".um").filter({ hasText: mockSend("只收到一次") })).toHaveCount(1);
  await expect(log.locator(".um.waiting, [data-testid=her-sending]")).toHaveCount(0);
  await expect(composer).toHaveValue("");
  // A command her takes without it joining the conversation (/context): it is not shown as being sent for long.
  // Before 2026-11-16 it said "正在发送…" for good, with no way to remove it.
  await send("/context");
  await expect(composer).toHaveValue("", { timeout: 1_000 });
  await expect(log.locator("[data-testid=her-sending]")).toHaveCount(0, { timeout: 30_000 });

  // A step that needs the owner's own Chrome, whose extension is not connected: it waits, and a card above the
  // composer says so. Connecting opens the console at its guide; going on without it this time lets the step go on.
  await send("[[mock:yours:https://example.com/]] hi");
  // The site asks first in the owner's own browser, whatever the mode: their sign-ins are real.
  const asked = page.getByRole("region", { name: "等你批准" });
  await expect(asked).toContainText("example.com", { timeout: 60_000 });
  await asked.getByRole("button", { name: /^允许一次/ }).click();
  const chrome = page.getByRole("region", { name: "连接你的 Chrome？" });
  await expect(chrome).toBeVisible({ timeout: 60_000 });
  await snap(page, "her-chrome-wait");
  const guide = app.waitForEvent("window");
  await chrome.getByRole("button", { name: "连接 Chrome…" }).click();
  const consoleAt = await guide;
  await expect.poll(() => consoleAt.url()).toMatch(/#browser\/connect$/);
  await consoleAt.close();
  await chrome.getByRole("button", { name: "这次用内置浏览器" }).click();
  await expect(chrome).toBeHidden({ timeout: 30_000 });

  // Her starts a coding session on this Mac, asked first (docs/her-team.md §14.3): a card where she started it,
  // kept current, which the team panel lists once it is done, and which opens the session.
  await send(`[[mock:mesh:session_start:${JSON.stringify({ project: "owner-mac/demo", prompt: "say 做完了", title: "Her 派的活" })}]] 开工`);
  // Asked first, in words that name the agent and the project.
  const approval = page.getByRole("region", { name: "等你批准" });
  await expect(approval).toContainText("owner-mac/demo", { timeout: 60_000 });
  await approval.getByRole("button", { name: /^允许/ }).click();
  const sessionCard = log.getByRole("button", { name: "Her 派的活" });
  await expect(sessionCard).toBeVisible({ timeout: 60_000 });
  await expect(sessionCard).toContainText("Claude · owner-mac");
  await expect(sessionCard).toContainText("完成", { timeout: 60_000 });
  await snap(page, "her-session-card");
  // The session's news wakes Her, and her-mock tells the owner of it: the card comes down under what she said,
  // where the owner reads now (docs/her-team.md §4.4). One card a session.
  const told = log.locator(".bm").filter({ hasText: "turn_done" });
  await expect(told).toBeVisible({ timeout: 90_000 });
  await expect.poll(async () => ((await sessionCard.boundingBox())?.y ?? 0) - ((b) => (b ? b.y + b.height : Infinity))(await told.boundingBox())).toBeGreaterThanOrEqual(0);
  await expect(sessionCard).toHaveCount(1);
  await tag.click();
  await expect(team.getByRole("region", { name: "刚完成" })).toContainText("Her 派的活");
  await expect(team.getByRole("region", { name: "刚完成" })).toContainText("Her → Claude · demo");
  await page.keyboard.press("Escape");
  await sessionCard.click();
  await expect(page.getByRole("heading", { name: "Her 派的活" })).toBeVisible();
  await navTo(page, /^Her/);
  await expect(sessionCard).toBeVisible();

  // When a coding session crashes and the owner asks Her to continue it, both the session card and the sidebar's
  // task row leave the error state as soon as the resumed turn runs, then settle as done.
  const herSessionId = await page.evaluate(async () => {
    const r = await fetch("app://hermesh/api/hermesh.local.v1.LocalService/ListSessions", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ limit: 50 }) });
    return ((await r.json()).sessions as { sessionId: string; title: string }[]).find((s) => s.title === "Her 派的活")?.sessionId ?? "";
  });
  expect(herSessionId).toMatch(/^s_/);
  await send(`[[mock:mesh:session_send:${JSON.stringify({ session_id: herSessionId, message: "sleep 1s\ncrash" })}]] 跑一下`);
  await expect(approval).toContainText("Her 派的活", { timeout: 60_000 });
  await approval.getByRole("button", { name: /^允许/ }).click();
  const list = await sessionList(page);
  const sideRow = list.getByRole("listitem").filter({ hasText: "Her 派的活" });
  await expect(sessionCard.locator(".hc-fail")).toBeVisible({ timeout: 60_000 });
  await expect(list.getByRole("group", { name: "需要处理" })).toContainText("Her 派的活");
  await expect(sideRow.getByRole("img", { name: "已崩溃" })).toBeVisible();
  await expect(log.locator(".bm").filter({ hasText: 'kind="failure"' })).toBeVisible({ timeout: 90_000 });

  await send(`[[mock:mesh:session_send:${JSON.stringify({ session_id: herSessionId, message: "sleep 5s\nsay 接着做完了" })}]] 继续跑`);
  await expect(approval).toContainText("Her 派的活", { timeout: 60_000 });
  await approval.getByRole("button", { name: /^允许/ }).click();
  await expect(sessionCard.locator(".hc-run")).toBeVisible({ timeout: 60_000 });
  await expect(sessionCard.locator(".busy")).toBeVisible();
  await expect(sessionCard.locator(".hc-fail")).toHaveCount(0);
  await expect(tag).toContainText("1 个在忙");
  await expect(list.getByRole("group", { name: "进行中" })).toContainText("Her 派的活");
  await expect(list.getByRole("group", { name: "需要处理" })).toHaveCount(0);
  await expect(sideRow.getByRole("img", { name: "运行中" })).toBeVisible();

  await expect(sessionCard).toContainText("完成", { timeout: 60_000 });
  await expect(list.getByRole("group", { name: "已结束" })).toContainText("Her 派的活", { timeout: 60_000 });
  await expect(log.locator(".bm").filter({ hasText: "接着做完了" })).toBeVisible({ timeout: 90_000 });

  // Her notes a rule (her-mock's remember), with no approval asked; her settings show it on the memory page,
  // as written (a list, Markdown), and clear it there, after asking.
  await send("[[mock:remember:- 回复一律用中文。(2026-11-11)]] 以后都用中文");
  await expect(log).toContainText("记下了。", { timeout: 60_000 });
  await page.getByRole("button", { name: "助手设置", exact: true }).click();
  await settings.getByRole("tab", { name: "能力" }).click();
  const memoryCard = settings.getByTestId("her-memory");
  await expect(memoryCard).toHaveAttribute("aria-label", "记忆", { timeout: 30_000 });
  await memoryCard.click();
  const memory = page.getByRole("complementary", { name: "记忆" });
  await expect(memory.locator(".mem-doc li")).toHaveText("回复一律用中文。(2026-11-11)");
  await expect(memory).toContainText("这就是它读到的内容");
  await snap(page, "her-memory");
  // Clearing it is at the page's foot, with what it clears, and asks first.
  await memory.getByTestId("her-clear-memory").click();
  const forget = page.getByRole("dialog", { name: "清空「Her」记住的事？" });
  await expect(forget).toContainText("聊天记录保留");
  await forget.getByRole("button", { name: "清空" }).click();
  await expect(memory).toContainText("还没有记下什么", { timeout: 30_000 });
  await memory.getByRole("button", { name: "返回" }).click();
  await expect(memoryCard).toHaveAttribute("aria-label", "记忆，还没有", { timeout: 30_000 });
  await settings.getByRole("button", { name: "关闭" }).click();
  await expect(settings).toBeHidden();

  // A scheduled task Her makes (her-mock's routine): its message names it and opens its run. Once the task is
  // deleted, as one that has done its job often is, the message still opens the run, the task marked as deleted.
  await send("[[mock:routine:拉伸|in 2s|say:该起来活动一下了。]] hi");
  // Asked first, as Her's settings ask before risky steps.
  await expect(approval).toContainText("拉伸", { timeout: 60_000 });
  await approval.getByRole("button", { name: /^允许/ }).click();
  await expect(log.getByRole("button", { name: /已创建定时任务「拉伸」/ })).toBeVisible({ timeout: 60_000 });
  const reminder = log.locator(".bm").filter({ hasText: "该起来活动一下了。" });
  await expect(reminder).toBeVisible({ timeout: 60_000 });
  const fromTask = reminder.getByRole("button", { name: "来自定时任务「拉伸」" });
  await reminder.hover();
  await fromTask.click();
  const routines = page.getByRole("complementary", { name: "定时任务" });
  await expect(routines).toContainText("发给你了", { timeout: 30_000 });
  await expect(routines).not.toContainText("已删除");
  await routines.getByRole("button", { name: "返回" }).click();
  await routines.getByRole("button", { name: "删除" }).click();
  await page.getByRole("dialog", { name: "删除定时任务「拉伸」？" }).getByRole("button", { name: "删除" }).click();
  await expect(routines).toContainText("还没有定时任务", { timeout: 30_000 });
  await reminder.hover();
  await fromTask.click();
  await expect(routines).toContainText("拉伸（已删除）", { timeout: 30_000 });
  await expect(routines).toContainText("发给你了");
  await snap(page, "her-routine-run-deleted");
  await routines.getByRole("button", { name: "关闭" }).click();
  await expect(routines).toBeHidden();

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
  await preview.getByRole("button", { name: sys("在文档中搜索（⌘F）", "在文档中搜索（Ctrl+F）") }).click();
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
  await expect(log.locator(".sc-end.done > span")).toHaveText(/^已连接 Tracker · \d+ 个工具$/, { timeout: 60_000 });
  await expect(card).toHaveCount(0);
  // her-mock hears how it went, and says so.
  await expect(log.locator(".bm .bbub").last()).toContainText(/tracker/i, { timeout: 60_000 });
  await snap(page, "her-card-done");
  // Connected, the card's line opens its page in the console; her settings say it is on, and open the console's
  // connectors.
  const managed = app.waitForEvent("window");
  await log.locator(".sc-end.done").getByRole("button", { name: "管理" }).click();
  const connectorAt = await managed;
  await expect.poll(() => connectorAt.url()).toMatch(/#connectors\/tracker$/);
  await page.getByRole("button", { name: "助手设置", exact: true }).click();
  await settings.getByRole("tab", { name: "能力" }).click();
  const connectorsRow = settings.getByTestId("her-connectors");
  await expect(connectorsRow).toHaveText("连接器1 个开着", { timeout: 30_000 });
  await expect(settings.getByTestId("her-connectors-tray").getByRole("button", { name: "tracker" })).toBeVisible();
  await connectorsRow.click();
  await expect.poll(() => connectorAt.url()).toMatch(/#connectors$/);
  await connectorAt.close();
  await page.getByRole("button", { name: "助手设置", exact: true }).click();
  await expect(settings).toBeHidden();

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

  // Keep the first reply outside the final virtual window when this conversation opens again.
  for (let i = 0; i < 16; i++) {
    const reply = `历史确认 ${i}`;
    await send(mockSend(reply));
    await expect(log.locator(".bm .bbub").last()).toHaveText(reply, { timeout: 30_000 });
  }

  // A second environment: work-mac, from ⋯ › 添加环境…. The page goes there once Her runs on it, and the
  // environment shows beside Her's name, as a menu of both.
  await page.getByRole("button", { name: "更多", exact: true }).click();
  await page.getByRole("menuitem", { name: "添加环境…" }).click();
  const sheet = page.getByRole("dialog", { name: "添加环境" });
  await sheet.getByRole("listitem").filter({ hasText: "work-mac" }).getByRole("button", { name: "开启 Her" }).click();
  await expect(sheet).toBeHidden();
  await expect.poll(async () => (await environmentNames(page)).join("|"), { timeout: 120_000 }).toContain("work-mac");
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
  // keeps its line. The ⋯ menu opens under its button, inside the window.
  const envs = await environments(page);
  const more = page.locator(".her-head").getByRole("button", { name: "更多" });
  const [button, menu] = await Promise.all([more.boundingBox(), page.getByRole("menu", { name: "更多" }).boundingBox()]);
  expect(Math.abs(menu!.x + menu!.width - (button!.x + button!.width))).toBeLessThan(2);
  expect(menu!.x).toBeGreaterThanOrEqual(0);
  await envs.filter({ hasNotText: "work-mac" }).click();
  await readGreeting(30_000);
  await expect(log).not.toContainText("来自 work-mac");
  await expect(log).not.toContainText("已接上");
  await (await environments(page)).filter({ hasText: "work-mac" }).click();
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
  await expect(composer).toHaveValue("", { timeout: 1_000 });
  const waiting = log.locator(".um.waiting").filter({ hasText: "等你回来再说" });
  await expect(waiting).toContainText("等 work-mac 在线后送达", { timeout: 60_000 });
  await expect(log.locator(".um").filter({ hasText: "等你回来再说" })).toHaveCount(1);
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

  // Her turned off on this Mac from her settings (docs/her-server.md §5.6): the sheet says what stops and asks
  // whether her data goes too, keeping it unless chosen. Kept, Settings › Machines says so and Her carries on
  // once turned on again from its switch; there, her data erased, she starts anew, asking for a model.
  await (await environments(page)).filter({ hasNotText: "work-mac" }).click();
  await readGreeting(30_000);
  await page.getByRole("button", { name: "助手设置", exact: true }).click();
  await page.getByRole("complementary", { name: "助手设置" }).getByRole("tab", { name: "设置" }).click();
  await page.getByTestId("her-environment").click();
  await page.getByRole("complementary", { name: "环境" }).getByRole("button", { name: /^在 .+ 上关闭 Her…$/ }).click();
  const off = page.getByRole("dialog", { name: /^在「.+」上关闭 Her？$/ });
  await expect(off).toContainText("Her 会在那里停止运行，也不能再使用你的电脑。");
  await expect(off).toContainText("不清空的话，这些都会保留，下次开启 Her 时接着用。");
  const erase = off.getByRole("switch", { name: "同时清空 Her 的数据" });
  await expect(erase).toHaveAttribute("aria-checked", "false");
  await erase.click();
  await expect(off.getByRole("button", { name: "关闭并清空" })).toBeVisible();
  await snap(page, "her-off-erase");
  await erase.click();
  await snap(page, "her-off");
  await off.getByRole("button", { name: "关闭 Her" }).click();
  await expect(off).toBeHidden({ timeout: 30_000 });
  await expect(page.locator(".toast")).toHaveText(/^已在「.+」上关闭 Her$/);
  // Her runs on work-mac alone now: the page is its, and no menu of environments shows.
  await expect(page.getByText(/^work-mac 离线 · .+在线$/)).toBeVisible({ timeout: 30_000 });
  expect(await environmentNames(page)).toEqual([]);

  const [win] = await Promise.all([app.waitForEvent("window"), page.evaluate(() => window.hermesh?.openSettings("machines"))]);
  await win.waitForLoadState();
  const machines = win.getByRole("main", { name: "机器" });
  await machines.getByRole("list", { name: "机器" }).getByRole("button").filter({ hasNotText: "work-mac" }).first().click();
  const run = machines.getByRole("switch", { name: "在这台机器上运行 Her" });
  await expect(run).toHaveAttribute("aria-checked", "false");
  // Off, her data kept: the row under the switch says so, and offers to erase it.
  const kept = machines.getByText("Her 的数据还在这台机器上，再次开启时接着用。");
  const eraseData = machines.getByRole("button", { name: "清空数据…" });
  await expect(kept).toBeVisible({ timeout: 30_000 });
  await expect(eraseData).toBeVisible();
  await kept.scrollIntoViewIfNeeded();
  await snap(win, "her-data-kept");
  await run.click();
  await expect(run).toHaveAttribute("aria-checked", "true", { timeout: 60_000 });
  await expect(machines).toContainText("关闭时会问你是保留到下次，还是清空。");
  await expect(kept).toBeHidden();
  await expect.poll(async () => (await environmentNames(page)).length, { timeout: 60_000 }).toBe(2);
  await (await environments(page)).filter({ hasNotText: "work-mac" }).click();
  await readGreeting(60_000);

  // Turned off from the switch, her data kept, then erased from the row after asking: turned on again, she
  // starts anew, asking for a model.
  await run.click();
  const offThere = win.getByRole("dialog", { name: /^在「.+」上关闭 Her？$/ });
  await offThere.getByRole("button", { name: "关闭 Her" }).click();
  await expect(offThere).toBeHidden({ timeout: 30_000 });
  await expect(run).toHaveAttribute("aria-checked", "false");
  await eraseData.click({ timeout: 30_000 });
  const sure = win.getByRole("dialog", { name: /^清空「.+」上 Her 的数据？$/ });
  await expect(sure).toContainText(/删除「.+」上 Her 的助手、对话、记忆、定时任务、文件、连接器和模型登录，无法恢复。/);
  await snap(win, "her-data-erase");
  await sure.getByRole("button", { name: "清空", exact: true }).click();
  await expect(sure).toBeHidden({ timeout: 30_000 });
  await expect(win.locator(".toast")).toHaveText(/^已清空「.+」上 Her 的数据$/);
  await expect(kept).toBeHidden({ timeout: 30_000 });
  await expect(run).toHaveAttribute("aria-checked", "false");
  await run.click();
  await expect(run).toHaveAttribute("aria-checked", "true", { timeout: 60_000 });
  await expect.poll(async () => (await environmentNames(page)).length, { timeout: 60_000 }).toBe(2);
  await (await environments(page)).filter({ hasNotText: "work-mac" }).click();
  const fresh = page.getByRole("log", { name: "对话" });
  await expect(fresh.getByText(/^你好，我是 .+，在 .+ 上为你做事。开始之前，我需要接上一个模型/)).toBeVisible({ timeout: 120_000 });
  await expect(fresh).not.toContainText("你好，主人");

  // Turned off and erased at once, on the sheet: the machine says none of her data is left.
  await run.click();
  await offThere.getByRole("switch", { name: "同时清空 Her 的数据" }).click();
  await offThere.getByRole("button", { name: "关闭并清空" }).click();
  await expect(offThere).toBeHidden({ timeout: 30_000 });
  await expect(win.locator(".toast")).toHaveText(/^已在「.+」上关闭 Her，并清空了她的数据$/);
  await expect(run).toHaveAttribute("aria-checked", "false");
  await expect(kept).toBeHidden({ timeout: 30_000 });

  // Removing work-mac, where Her runs: offline, her data can't be erased there, and the dialog says it stays
  // instead of offering to. Cancelled.
  await win.getByRole("button", { name: "返回机器" }).click();
  await machines.getByRole("list", { name: "机器" }).getByRole("button").filter({ hasText: "work-mac" }).click();
  await machines.locator(".set-acts").getByRole("button", { name: "移除…" }).click();
  const removing = win.getByRole("dialog", { name: "移除机器「work-mac」？" });
  await expect(removing).toContainText("「work-mac」离线，Her 的数据会留在上面。");
  await expect(removing.getByRole("switch")).toHaveCount(0);
  await snap(win, "her-remove-offline");
  await removing.getByRole("button", { name: "取消" }).click();
  await expect(removing).toBeHidden();
  await app.close();
});

/** Opens the ⋯ menu of Her's page and returns its environments (docs/her-team.md §14.3: no longer in the capsule). */
async function environments(page: Page): Promise<Locator> {
  const more = page.locator(".her-head").getByRole("button", { name: "更多" });
  if ((await more.getAttribute("aria-expanded")) !== "true") await more.click();
  return page.getByRole("menu", { name: "更多" }).getByRole("menuitemradio");
}

/** The environments' names in the ⋯ menu, which is closed again. */
async function environmentNames(page: Page): Promise<string[]> {
  const names = await (await environments(page)).allInnerTexts();
  await page.keyboard.press("Escape");
  return names;
}
