import { expect, test, type Page } from "@playwright/test";
import { cli, fixture, launch, snap } from "./app";

// The session page: the conversation, the changes, the info panel, and the
// control area where the owner answers the agent or stops it.

async function open(page: Page, title: string) {
  await page.getByRole("list", { name: "会话" }).getByText(title, { exact: true }).click();
  await expect(page.getByRole("heading", { name: title })).toBeVisible();
}

/** The session's title line: just its name (the status is in the list and the bottom bar). */
function titleLine(page: Page, title: string) {
  return page.getByRole("heading", { name: title }).locator("..");
}

async function status(title: string): Promise<string> {
  return JSON.parse(await cli("session", "get", fixture.sessions[title], "-o", "json")).session.status;
}

test("a finished session shows both turns, what the agent did, and that it is done", async () => {
  const { app, page } = await launch();
  await open(page, "写 hello");
  const log = page.getByRole("log", { name: "对话" });
  // A time line before the first turn only (the second came right after), and no names on the messages.
  await expect(log.locator(".tdiv")).toHaveCount(1);
  await expect(log.locator(".tdiv")).toHaveText(/^今天 \d\d:\d\d$/);
  await expect(log).not.toContainText("Gemini CLI");
  await expect(log).toContainText("先写文件");
  // The edit and the command right after it are one step; opened, each in Chinese, without the agent's tool name.
  const step = log.getByRole("button", { name: "编辑了 1 个文件、运行了 1 条命令" });
  await expect(step).toHaveAttribute("aria-expanded", "false");
  // A step is a grey line a size below the agent's words: no icon, only the caret after it.
  await expect(step.locator("svg")).toHaveCount(1);
  await expect(step.locator("svg")).toHaveClass(/chev/);
  const size = (l: typeof step) => l.evaluate((el) => parseFloat(getComputedStyle(el).fontSize));
  expect(await size(step)).toBeLessThan(await size(log.locator(".am").first()));
  // The text keeps to a column of at most 730, so lines stay short enough to read.
  expect(await log.evaluate((el) => el.getBoundingClientRect().width)).toBeLessThanOrEqual(730);
  await step.click();
  await expect(log.getByRole("button", { name: /^已编辑\s*hello\.txt/ }).first()).toBeVisible();
  await expect(log.getByRole("button", { name: /^已运行\s*echo ran > ran\.txt/ })).toBeVisible();
  // A command reads as code, in the mono face and in colour; what other tools acted on stays plain.
  const command = log.getByRole("button", { name: /^已运行\s*echo ran > ran\.txt/ }).locator("code.cmd");
  await expect(command.locator(".hl")).toBeVisible();
  expect(await command.evaluate((el) => getComputedStyle(el).fontFamily)).toMatch(/SF Mono/);
  await expect(log.getByRole("button", { name: /^已编辑\s*hello\.txt/ }).first().locator("code")).toHaveCount(0);
  await expect(log).not.toContainText("Write hello.txt");
  await expect(log).toContainText("改好了");
  // A turn that went well ends without a line of its own.
  await expect(log).not.toContainText("轮完成");
  await expect(page.getByText("这一轮做完了。bot 再发消息时，会在这个会话里接着做。")).toBeVisible();
  await expect(page.getByRole("region", { name: "这一轮" })).toHaveCount(0);
  await snap(page, "session-chat");

  // A tool's output opens under it.
  const shell = log.getByRole("button", { name: /^已运行.*echo ran/ });
  await shell.click();
  await expect(shell).toHaveAttribute("aria-expanded", "true");

  // A turn that writes several times is one reply: only its last words keep room below for when and Copy.
  const early = log.locator(".am").filter({ hasText: "先写文件" });
  const reply = log.locator(".am").filter({ has: page.locator(".md", { hasText: /^好了$/ }) });
  await expect(early.locator(".msg-act")).toHaveCount(0);
  await expect(log.locator(".am .msg-act")).toHaveCount(2);
  await expect(early).toHaveCSS("margin-bottom", "0px");
  await expect(reply).toHaveCSS("margin-bottom", "21px");
  // Copy shows under a message on hover: under the bubble's right edge, under the reply's text on the left.
  for (const [msg, edge] of [[log.locator(".um").first(), "right"], [reply, "left"]] as const) {
    const copy = msg.getByRole("button", { name: "拷贝" });
    await page.mouse.move(0, 0);
    await expect(copy.locator("..")).toHaveCSS("opacity", "0");
    await msg.hover();
    await expect(copy.locator("..")).toHaveCSS("opacity", "1");
    const m = (await msg.boundingBox())!;
    const c = (await copy.boundingBox())!;
    expect(c.y).toBeGreaterThanOrEqual(m.y + m.height);
    expect(Math.abs(edge === "right" ? c.x + c.width - (m.x + m.width) : c.x - m.x)).toBeLessThan(8);
  }
  // Both say when they were sent there too, as time ago: after Copy under the reply.
  for (const msg of [log.locator(".um").first(), reply]) {
    await msg.hover();
    await expect(msg.locator(".msg-act")).toHaveText(/^(刚刚|\d+ 分钟前)$/);
  }
  const copyBox = (await reply.getByRole("button", { name: "拷贝" }).boundingBox())!;
  expect((await reply.locator(".msg-time").boundingBox())!.x).toBeGreaterThan(copyBox.x);
  await app.close();
});

test("the page is the conversation alone: no tabs, and no line or link after a finished turn", async () => {
  const { app, page } = await launch();
  await open(page, "写 hello");
  await expect(page.getByRole("tab")).toHaveCount(0);
  const log = page.getByRole("log", { name: "对话" });
  await expect(log).toContainText("改好了");
  await expect(log).not.toContainText("轮完成");
  await expect(log.getByRole("button", { name: "查看改动" })).toHaveCount(0);
  await app.close();
});

// The title as macOS draws a window's: 14px medium, bright in the front
// window and grey while another window has the focus. (The test windows open
// behind and never take the focus, so the test tells the window it got it.)
test("the title is 14px medium, bright in front and grey while the window is behind", async () => {
  const { app, page } = await launch();
  await open(page, "写 hello");
  const title = page.getByRole("heading", { name: "写 hello" });
  expect(await title.evaluate((e) => [getComputedStyle(e).fontSize, getComputedStyle(e).fontWeight])).toEqual(["14px", "500"]);
  const colour = () => title.evaluate((e) => getComputedStyle(e).color);
  const tell = (event: "focus" | "blur") =>
    app.evaluate(({ BrowserWindow }, event) => {
      const win = BrowserWindow.getAllWindows()[0];
      win.isFocused = () => event === "focus";
      win.emit(event);
    }, event);
  const behind = await colour();
  await tell("focus");
  await expect.poll(colour).not.toBe(behind);
  const front = await colour();
  const alpha = (c: string) => Number(c.match(/,\s*([\d.]+)\)$/)?.[1] ?? 1);
  expect(alpha(behind)).toBeLessThan(alpha(front));
  await tell("blur");
  await expect.poll(colour).toBe(behind);
  await app.close();
});

test("the session card floats over the conversation with what a glance needs, and the toolbar hides it", async () => {
  const { app, page } = await launch();
  await open(page, "写 hello");
  const card = page.getByRole("complementary", { name: "会话信息" });
  const toggle = page.getByRole("button", { name: "信息面板", exact: true });
  await expect(card).toBeVisible();
  await expect(toggle).toHaveAttribute("aria-pressed", "true");
  // Its environment, one row each, under a title: who started it, with what, on which machine, in which project, on which branch
  // (a worktree session's own). No state, turns or changes: the list and the conversation say those.
  await expect(card.getByRole("heading", { name: "环境" })).toBeVisible();
  const branch = `hermesh/${fixture.sessions["写 hello"]}`;
  await expect(card.getByRole("listitem")).toHaveText([/bot 发起$/, "Gemini CLI 执行", "这台 Mac", "demo", branch]);
  await expect(card).not.toContainText("已完成");
  await expect(card).not.toContainText("轮");
  await expect(card).not.toContainText("tokens");
  // Every row the same height, its icon the same size.
  const rows = card.getByRole("listitem");
  const heights = await rows.evaluateAll((els) => els.map((el) => el.getBoundingClientRect().height));
  expect(new Set(heights).size).toBe(1);
  const icons = await rows.evaluateAll((els) => els.map((el) => el.querySelector("svg, img, .av")!.getBoundingClientRect().width));
  expect(new Set(icons)).toEqual(new Set([16]));
  // A long value is cut short; hovered, it shows whole (the project: its folder).
  await expect(card.getByRole("button", { name: branch })).toHaveAttribute("title", branch);
  await expect(card.getByRole("button", { name: "demo" })).toHaveAttribute("title", /^\/.+/);

  // The project and the branch open a menu of what to do with them; this Mac's project can open its folder too.
  await card.getByRole("button", { name: "demo" }).click();
  const menu = page.getByRole("menu", { name: "项目" });
  await expect(menu.getByRole("menuitem")).toHaveText(["拷贝项目路径", "在访达中打开", "在终端中打开"]);
  // Right under the project, from its left edge, inside the window.
  const anchor = (await card.getByRole("button", { name: "demo" }).boundingBox())!;
  const box = (await menu.boundingBox())!;
  expect(Math.abs(box.x - anchor.x)).toBeLessThan(2);
  expect(box.y - (anchor.y + anchor.height)).toBeGreaterThanOrEqual(0);
  expect(box.y - (anchor.y + anchor.height)).toBeLessThan(8);
  expect(box.x + box.width).toBeLessThanOrEqual(page.viewportSize()?.width ?? (await page.evaluate(() => innerWidth)));
  await menu.getByRole("menuitem", { name: "拷贝项目路径" }).click();
  await expect(menu).toHaveCount(0);
  await expect(page.getByRole("status")).toContainText("已拷贝");
  expect(await app.evaluate(({ clipboard }) => clipboard.readText())).toBe(await card.getByRole("button", { name: "demo" }).getAttribute("title"));
  await card.getByRole("button", { name: branch }).click();
  const branchMenu = page.getByRole("menu", { name: "分支" });
  await expect(branchMenu.getByRole("menuitem")).toHaveText(["拷贝分支名称"]);
  await page.keyboard.press("Escape");
  await expect(branchMenu).toHaveCount(0);
  await card.getByRole("button", { name: branch }).click();
  await page.getByRole("menuitem", { name: "拷贝分支名称" }).click();
  await expect.poll(() => app.evaluate(({ clipboard }) => clipboard.readText())).toBe(branch);
  // The machine's settings are a click away, on its own page.
  const [settings] = await Promise.all([app.waitForEvent("window"), card.getByRole("button", { name: "机器设置" }).click()]);
  await settings.waitForLoadState();
  expect(settings.url()).toMatch(/#settings\/machines\/.+/);
  await settings.close();
  // It floats over the conversation, which keeps clear of it.
  const c = (await card.boundingBox())!;
  const log = (await page.getByRole("log", { name: "对话" }).boundingBox())!;
  expect(log.x + log.width).toBeLessThanOrEqual(c.x);
  await snap(page, "session-info");

  // The toggle is a toolbar button the size of the sidebar's, highlighted only while the info shows.
  const left = (await page.getByRole("button", { name: "隐藏边栏" }).boundingBox())!;
  const right = (await toggle.boundingBox())!;
  expect([right.width, right.height]).toEqual([left.width, left.height]);
  const fill = () => toggle.evaluate((el) => getComputedStyle(el).backgroundColor);
  expect(await fill()).not.toBe("rgba(0, 0, 0, 0)");
  await toggle.click();
  await expect(card).toHaveCount(0);
  await expect(toggle).toHaveAttribute("aria-pressed", "false");
  await page.mouse.move(0, 0);
  await expect.poll(fill).toBe("rgba(0, 0, 0, 0)");
  await snap(page, "session-chat");
  // In the dark, the owner's messages are a quiet raised grey with light text, not a white block.
  await page.emulateMedia({ colorScheme: "dark" });
  const bubble = page.getByRole("log", { name: "对话" }).locator(".um .bub").first();
  const colors = () => bubble.evaluate((el) => [getComputedStyle(el).backgroundColor, getComputedStyle(el).color]);
  await expect.poll(async () => (await colors())[0]).toMatch(/^rgba\(255, 255, 255, 0\.\d+\)$/);
  expect((await colors())[1]).toMatch(/^rgba\(255, 255, 255, 0\.8\d*\)$/);
  await snap(page, "session-chat-dark");
  await page.emulateMedia({ colorScheme: "light" });
  await toggle.click();
  await expect(card).toBeVisible();
  await app.close();
});

test("failed sessions tell what went wrong and what to do; an archived one says it is read-only", async () => {
  const { app, page } = await launch();
  await open(page, "崩溃");
  await expect(titleLine(page, "崩溃")).toHaveText("崩溃");
  // Said once, in a quiet card where the reply would be: what happened to the coding agent, who goes on, the agent's words folded under 详情, and 归档.
  const failed = page.getByRole("alert");
  await expect(failed).toContainText("Gemini CLI 意外退出了");
  await expect(failed).not.toContainText("已崩溃"); // the sidebar says so
  await expect(failed.locator(".qf-ic")).toHaveCSS("color", /^rgb\((215, 51, 63|255, 102, 112)\)$/); // red only in its mark
  await expect(failed.locator(".qf-title")).not.toHaveCSS("color", /^rgb\((215, 51, 63|255, 102, 112)\)$/);
  await expect(failed).toContainText("bot 再发消息就会从这里接着做，之前的改动都还在。");
  await expect(failed).not.toContainText("exit status");
  await failed.getByRole("button", { name: /详情/ }).click();
  await expect(failed).toContainText("agent process exited unexpectedly");
  await expect(failed.getByRole("button", { name: "归档" })).toBeVisible();
  await snap(page, "session-crashed");

  await open(page, "限流");
  await expect(titleLine(page, "限流")).toHaveText("限流");
  const log = page.getByRole("log", { name: "对话" });
  await expect(log).not.toContainText("第 1 轮失败"); // not said twice
  await expect(page.getByRole("alert")).toContainText("Gemini CLI 被限流了");
  await expect(log).not.toContainText("出错了"); // nor as a line above the card
  await expect(page.getByRole("alert")).toContainText("bot 再发消息就会从这里接着做");
  await snap(page, "session-failed");

  await page.getByRole("button", { name: "显示已归档" }).click();
  await open(page, "归档的");
  await expect(page.getByText("已归档：只读，worktree 已释放，改动还在分支上。")).toBeVisible();
  await expect(page.getByRole("button", { name: "取消归档" }).last()).toBeVisible();
  await snap(page, "session-archived");
  await app.close();
});

test("a question waits in the control area; the owner picks an option by key and answers", async () => {
  const { app, page } = await launch();
  await open(page, "选颜色");
  const ctl = page.getByRole("region", { name: "这一轮" });
  await expect(ctl).toContainText("Gemini CLI 问你");
  await expect(ctl).toContainText("选哪个颜色");
  await expect(ctl).toContainText("等你回答");
  await expect(ctl).toContainText("还剩");
  const answer = ctl.getByRole("button", { name: /^回答/ });
  await expect(answer).toBeDisabled();
  await snap(page, "session-question");

  await page.keyboard.press("1");
  await expect(ctl.getByRole("radio", { name: /红/ })).toHaveAttribute("aria-checked", "true");
  await expect(ctl.getByRole("radio", { name: /蓝/ })).toHaveAttribute("aria-checked", "false");
  await page.keyboard.press("2");
  await expect(ctl.getByRole("radio", { name: /蓝/ })).toHaveAttribute("aria-checked", "true");
  await page.keyboard.press("1");
  // The form's second field (free text) is one the agent does not require.
  await expect(ctl).toContainText("可选");
  await expect(answer).toBeEnabled();
  await snap(page, "session-question-chosen");
  await page.keyboard.press("Enter");

  await expect(page.getByRole("status")).toContainText("已回答，Gemini CLI 继续工作");
  await expect(ctl.getByText("选哪个颜色")).toHaveCount(0);
  await expect.poll(() => status("选颜色")).not.toBe("waiting_input");
  const log = page.getByRole("log", { name: "对话" });
  await expect(log).toContainText("你回答：红");
  await expect(log).toContainText("answer: v-红");
  await app.close();
});

test("a session worked on in place shows the branch its folder is on, or that it is no git repository; another machine's can only be copied", async () => {
  const { app, page } = await launch();
  const card = page.getByRole("complementary", { name: "会话信息" });
  await open(page, "就地");
  await expect(card.getByRole("listitem")).toHaveText([/bot 发起$/, "Gemini CLI 执行", "这台 Mac", "live", "feature/card"]);
  await open(page, "不在仓库里");
  await expect(card.getByRole("listitem")).toHaveText([/bot 发起$/, "Gemini CLI 执行", "这台 Mac", "notes", "不是 Git 仓库"]);
  // No branch, nothing to pick.
  await expect(card.getByRole("button", { name: "不是 Git 仓库" })).toHaveCount(0);
  // Another machine's project and branch can be copied, not opened here.
  await open(page, "在另一台 Mac 上");
  await expect(card.getByRole("listitem").nth(2)).toHaveText("work-mac");
  await card.getByRole("button", { name: "billing" }).click();
  await expect(page.getByRole("menu", { name: "项目" }).getByRole("menuitem")).toHaveText(["拷贝项目路径"]);
  await app.close();
});

test("a running session shows what it does, and 打断 stops the turn", async () => {
  const { app, page } = await launch();
  await open(page, "跑很久");
  await expect(titleLine(page, "跑很久")).toHaveText("跑很久");
  const ctl = page.getByRole("region", { name: "这一轮" });
  await expect(ctl).toContainText("Gemini CLI 正在工作");
  // A live one: the card is its environment alone; the bar below says what runs.
  const card = page.getByRole("complementary", { name: "会话信息" });
  await expect(card).toContainText("Gemini CLI");
  await expect(card).not.toContainText("运行中");
  // The agent that works is shown by its own logo, not the Hermesh mark.
  await expect(ctl.locator(".live-logo img")).toBeVisible();
  await expect(ctl.locator(".hm-mark")).toHaveCount(0);
  // What it is doing is the running tool call in the conversation, not repeated in the bar.
  await expect(ctl).not.toContainText("sleep 1800");
  await expect(ctl).toContainText(/\d+:\d\d/);
  await expect(page.getByRole("log", { name: "对话" })).toContainText("sleep 1800");
  await expect(page.getByRole("log", { name: "对话" }).getByRole("img", { name: "进行中" })).toBeVisible();
  await snap(page, "session-running");

  await ctl.getByRole("button", { name: /打断/ }).click();
  await expect(page.getByRole("status")).toContainText("已打断「跑很久」");
  await expect(ctl).toHaveCount(0);
  await expect.poll(() => status("跑很久")).not.toBe("running");
  await expect(page.getByRole("log", { name: "对话" })).toContainText("你打断了这一轮。会话还在，bot 再发消息就会接着做。");
  await expect(titleLine(page, "跑很久")).toHaveText("跑很久");
  await expect(page.getByText("这一轮做完了。", { exact: false })).toHaveCount(0);
  await snap(page, "session-interrupted");
  await app.close();
});

test("tool details stay folded until opened, a failed one and a running one too", async () => {
  await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "命令失败", "--prompt", "shell ls no-such-file\nshell sleep 1800");
  const { app, page } = await launch();
  await open(page, "命令失败");
  const log = page.getByRole("log", { name: "对话" });
  const step = log.getByRole("button", { name: /运行了 2 条命令/ });
  await expect(step.getByRole("img", { name: "进行中" })).toBeVisible();
  await expect(step).toHaveAttribute("aria-expanded", "false");
  const failed = log.getByRole("button", { name: /^已运行\s*no-such-file/ });
  await expect(failed).toBeHidden();
  // Opened, each command shows; the failed one is red, its output still folded.
  await step.click();
  await expect(failed.locator("..")).toHaveClass(/\berr\b/);
  await expect(failed).toHaveAttribute("aria-expanded", "false");
  await expect(log.getByText("No such file or directory")).toBeHidden();
  await failed.click();
  await expect(failed).toHaveAttribute("aria-expanded", "true");
  await expect(log.getByText("No such file or directory")).toBeVisible();
  await page.getByRole("region", { name: "这一轮" }).getByRole("button", { name: /打断/ }).click();
  await expect(page.getByRole("status")).toContainText("已打断「命令失败」");
  await app.close();
});

test("a session's conversation appears whole as its page opens, not in pieces", async () => {
  const { app, page } = await launch();
  // Each time the conversation changes, how many rows it has.
  await page.evaluate(() => {
    const w = window as unknown as { __rows: number[] };
    w.__rows = [];
    new MutationObserver(() => {
      const n = document.querySelector(".tr")?.childElementCount ?? 0;
      if (n && n !== w.__rows.at(-1)) w.__rows.push(n);
    }).observe(document.body, { subtree: true, childList: true });
  });
  await open(page, "写 hello");
  const log = page.getByRole("log", { name: "对话" });
  await expect(log).toContainText("改好了");
  await page.waitForTimeout(300);
  const rows = await page.evaluate(() => (window as unknown as { __rows: number[] }).__rows);
  expect(rows).toEqual([await log.locator(":scope > *").count()]);
  await app.close();
});

test("an agent's Markdown shows as GitHub shows it, without HTML, images or unsafe links", async () => {
  const reply = [
    "## 结论",
    "见 https://hermesh.io/p/abc，然后再说。",
    "第二行",
    "",
    "| 项 | 值 |",
    "|---|---|",
    "| 轮 | 1 |",
    "",
    "- 外层",
    "  - 里层",
    "",
    "```go",
    'func main() { fmt.Println("hi") }',
    "```",
    "",
    "```",
    "没写语言",
    "```",
    "",
    "~~旧的~~ [危险](javascript:alert(1)) ![图](https://example.com/a.png) <b>原样</b>",
  ].join("\\n");
  await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "排版", "--prompt", `say ${reply}`, "--wait");
  const { app, page } = await launch();
  await open(page, "排版");
  const md = page.getByRole("log", { name: "对话" }).locator(".md");
  await expect(md.getByRole("heading", { name: "结论" })).toBeVisible();
  // A bare link ends where the Chinese starts; a single newline breaks the line.
  await expect(md.getByRole("link")).toHaveCount(1);
  await expect(md.getByRole("link")).toHaveText("https://hermesh.io/p/abc");
  await expect(md.getByRole("link")).toHaveAttribute("href", "https://hermesh.io/p/abc");
  await expect(md.locator("p", { hasText: "然后再说" }).locator("br")).toHaveCount(1);
  await expect(md.getByRole("table")).toBeVisible();
  await expect(md.getByRole("columnheader", { name: "值" })).toBeVisible();
  await expect(md.getByRole("cell", { name: "1", exact: true })).toBeVisible();
  await expect(md.locator("li li")).toHaveText("里层");
  await expect(md.locator("del")).toHaveText("旧的");
  // A block that names its language is in GitHub's colours, light or dark with the app; one that doesn't stays plain.
  const go = md.locator("pre code").first();
  await expect(go).toHaveText('func main() { fmt.Println("hi") }');
  await expect(go.locator(".hl")).toBeVisible();
  expect(await go.locator(".hl span span").evaluateAll((els) => new Set(els.map((el) => getComputedStyle(el).color)).size)).toBeGreaterThan(2);
  const themed = await go.getByText("func", { exact: true }).evaluate((el) => {
    const probe = document.createElement("span");
    probe.style.color = getComputedStyle(el).getPropertyValue(matchMedia("(prefers-color-scheme: dark)").matches ? "--shiki-dark" : "--shiki-light");
    document.body.append(probe);
    const want = getComputedStyle(probe).color;
    probe.remove();
    return getComputedStyle(el).color === want;
  });
  expect(themed).toBe(true);
  await expect(md.locator("pre code").nth(1)).toHaveText("没写语言");
  await expect(md.locator("pre code").nth(1).locator(".hl")).toHaveCount(0);
  // Not a link, no image loaded, and HTML stays text.
  await expect(md).toContainText("危险");
  await expect(md.locator("img")).toHaveCount(0);
  await expect(md).toContainText("图");
  await expect(md).toContainText("<b>原样</b>");
  // The agent's words are 15 on 21.
  expect(await md.evaluate((el) => [getComputedStyle(el).fontSize, getComputedStyle(el).lineHeight])).toEqual(["15px", "21px"]);
  await snap(page, "session-markdown");
  await app.close();
});

test("a reply shows as the agent writes it, never as raw Markdown, and stays when the turn is interrupted", async () => {
  // Bold text and a table header long enough to stay open over several 100 ms updates.
  const reply = ["## 计划", "先看 **这一段很长很长的加粗文字会跨过好几次刷新** 的文件。", "", "| 文件名称写得很长很长 | 改动说明也写得很长很长 |", "|---|---|", "| a.go | 新增 |", "", "```go", "func main() {}", "```", "", "最后一段说明。"].join("\\n");
  // The reply streams in a second turn, sent once the page is open, so the page sees it grow however slow the machine.
  const streaming = JSON.parse(await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "流式", "--prompt", "say 准备好了", "--wait", "-o", "json"));
  await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "流式打断", "--prompt", "stream 被打断的回复写到一半\nsleep 1800s");
  const { app, page } = await launch();
  // Every state of the agent's words while the reply grows (the prompt shows it raw), checked for Markdown that shows raw,
  // and whether one showed part of it only.
  await page.evaluate(() => {
    const w = window as unknown as { __raw: string[]; __part: boolean };
    w.__raw = [];
    w.__part = false;
    new MutationObserver(() => {
      const text = [...document.querySelectorAll("[role=log] .am")].map((el) => (el as HTMLElement).innerText).join("\n");
      if (/\||\*\*|```/.test(text)) w.__raw.push(text);
      if (text.includes("计划") && !text.includes("最后一段说明")) w.__part = true;
    }).observe(document.body, { subtree: true, childList: true, characterData: true });
  });
  await open(page, "流式");
  const log = page.getByRole("log", { name: "对话" });
  const agent = log.locator(".am");
  const ctl = page.getByRole("region", { name: "这一轮" });
  await expect(agent.locator(".md")).toHaveText("准备好了");
  await cli("session", "send", streaming.session.session_id, "--message", `stream ${reply}\nsleep 4s`);
  // Part of the reply shows while the turn runs, then all of it, still running.
  await expect(agent.last()).toContainText("最后一段说明");
  expect(await page.evaluate(() => (window as unknown as { __part: boolean }).__part)).toBe(true);
  await expect(ctl).toBeVisible();
  await expect(log.getByRole("table")).toBeVisible();
  await expect(log.locator("pre code")).toHaveText("func main() {}");
  await snap(page, "session-streaming");
  // The stored message replaces it when the turn ends: the same, not a second one.
  await expect(ctl).toHaveCount(0);
  await expect(agent).toHaveCount(2);
  await expect(agent.last().locator("strong")).toHaveText("这一段很长很长的加粗文字会跨过好几次刷新");
  expect(await page.evaluate(() => (window as unknown as { __raw: string[] }).__raw)).toEqual([]);

  // Interrupted mid-reply: what was shown stays, as a message like any other.
  await open(page, "流式打断");
  await expect(agent).toHaveText("被打断的回复写到一半");
  await page.getByRole("region", { name: "这一轮" }).getByRole("button", { name: /打断/ }).click();
  await expect(log).toContainText("你打断了这一轮");
  // A stored message (only those have a copy button), after the turn's end.
  await expect(agent.locator(".md")).toHaveText("被打断的回复写到一半");
  await expect(agent.locator(".msg-act")).toHaveCount(1);
  await app.close();
});

test("a message an agent writes to the session shows as a card that says who wrote it", async () => {
  const started = JSON.parse(await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "消息", "--prompt", "say 好", "--wait", "-o", "json"));
  await cli("message", "send", "--to", started.session.session_id, "--message", "say 收到", "--wait");
  const { app, page } = await launch();
  await open(page, "消息");
  const card = page.getByRole("log", { name: "对话" }).locator(".msg.in");
  await expect(card).toContainText(/来自 .+ 的消息/);
  await expect(card).toContainText("say 收到");
  await snap(page, "session-message");
  await app.close();
});

test("a conversation on a machine gone offline says why: one read before shows with a line under it, one never read says so instead of a blank page", async () => {
  const { app, page } = await launch();
  await open(page, "在另一台 Mac 上");
  const log = page.getByRole("log", { name: "对话" });
  await expect(log).toContainText("在那边");
  await open(page, "写 hello");
  process.kill(-fixture.otherMac, "SIGTERM");
  // Read before: what was read shows, and a line says it is what was last read.
  await open(page, "在另一台 Mac 上");
  await expect(log).toContainText("在那边");
  await expect(page.getByText("work-mac 离线了，这里是上次读到的内容。", { exact: true })).toBeVisible({ timeout: 60_000 });
  await snap(page, "session-machine-offline-kept");
  // Never read: the page says why it is empty, not a blank page or a wait for good.
  await open(page, "也在另一台 Mac 上");
  await expect(page.getByText("work-mac 离线了", { exact: true })).toBeVisible();
  await expect(page.getByText(/^这个会话的内容保存在那台机器上，连上后会自动显示。.*还在线。$/)).toBeVisible();
  await snap(page, "session-machine-offline");
  // The overview says it as the iPhone does: offline, when it was last seen, no raw error.
  await page.getByRole("button", { name: /^概览/ }).click();
  const warn = page.locator(".ov-warn");
  await expect(warn).toContainText("work-mac 离线了，它上面的会话暂时看不到");
  await expect(warn).toContainText("还在线 · 连上后会自动补上");
  await app.close();
});
