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
  // A step is a grey line in the body's size: no icon, only the caret after it.
  await expect(step.locator("svg")).toHaveCount(1);
  await expect(step.locator("svg")).toHaveClass(/chev/);
  const size = (l: typeof step) => l.evaluate((el) => getComputedStyle(el).fontSize);
  expect(await size(step)).toBe(await size(log.locator(".am").first()));
  // The text keeps to a column of at most 700, so lines stay short enough to read.
  expect(await log.evaluate((el) => el.getBoundingClientRect().width)).toBeLessThanOrEqual(700);
  await step.click();
  await expect(log.getByRole("button", { name: /^已编辑\s*hello\.txt/ }).first()).toBeVisible();
  await expect(log.getByRole("button", { name: /^已运行\s*echo ran > ran\.txt/ })).toBeVisible();
  await expect(log).not.toContainText("Write hello.txt");
  await expect(log).toContainText("第 1 轮完成 · 2 个文件 +2 −0");
  await expect(log).toContainText("改好了");
  await expect(page.getByText("这一轮做完了。bot 再发消息时，会在这个会话里接着做。")).toBeVisible();
  await expect(page.getByRole("region", { name: "这一轮" })).toHaveCount(0);
  await snap(page, "session-chat");

  // A tool's output opens under it.
  const shell = log.getByRole("button", { name: /^已运行.*echo ran/ });
  await shell.click();
  await expect(shell).toHaveAttribute("aria-expanded", "true");
  await app.close();
});

test("the page is the conversation alone: no tabs, and a finished turn says what it changed without a link", async () => {
  const { app, page } = await launch();
  await open(page, "写 hello");
  await expect(page.getByRole("tab")).toHaveCount(0);
  const log = page.getByRole("log", { name: "对话" });
  await expect(log).toContainText("第 1 轮完成 · 2 个文件 +2 −0");
  await expect(log).toContainText("第 2 轮完成 · 1 个文件 +1 −1");
  await expect(log.getByRole("button", { name: "查看改动" })).toHaveCount(0);
  await app.close();
});

test("the session card floats over the conversation with what a glance needs, and the toolbar hides it", async () => {
  const { app, page } = await launch();
  await open(page, "写 hello");
  const card = page.getByRole("complementary", { name: "会话信息" });
  const toggle = page.getByRole("button", { name: "信息面板", exact: true });
  await expect(card).toBeVisible();
  await expect(toggle).toHaveAttribute("aria-pressed", "true");
  // A finished session: no state (the list says it), straight to who, with what, where; then what it has done.
  await expect(card).not.toContainText("已完成");
  await expect(card).not.toContainText("刚刚");
  await expect(card).toContainText("bot 发起");
  await expect(card).toContainText("Gemini CLI");
  await expect(card).toContainText("这台 Mac / demo");
  await expect(card).toContainText("2 轮");
  await expect(card).toContainText("2 个文件 +2 −0");
  await expect(card).toContainText("250 tokens");
  // Not what a glance doesn't need.
  await expect(card).not.toContainText(fixture.sessions["写 hello"]);
  await expect(card).not.toContainText("worktree");
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
  // Said once, quietly: what, what to do, the agent's words folded under 详情, and 归档.
  const failed = page.getByRole("alert");
  await expect(failed).toContainText("已崩溃：agent 进程意外退出");
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
  await expect(page.getByRole("alert")).toContainText("出错了：限流");
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

test("a running session shows what it does, and 打断 stops the turn", async () => {
  const { app, page } = await launch();
  await open(page, "跑很久");
  await expect(titleLine(page, "跑很久")).toHaveText("跑很久");
  const ctl = page.getByRole("region", { name: "这一轮" });
  await expect(ctl).toContainText("Gemini CLI 正在工作");
  // A live one: the card leads with its state and the turn it is on.
  const card = page.getByRole("complementary", { name: "会话信息" });
  await expect(card).toContainText("运行中");
  await expect(card).toContainText("第 1 轮");
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
  await expect(log).toContainText("第 2 轮完成");
  await page.waitForTimeout(300);
  const rows = await page.evaluate(() => (window as unknown as { __rows: number[] }).__rows);
  expect(rows).toEqual([await log.locator(":scope > *").count()]);
  await app.close();
});
