import { expect, test, type Locator, type Page } from "@playwright/test";
import { cli, dragFiles, fixture, launch, onMac, onWindows, paste, pickFrom, snap, thisComputer, sessionList } from "./app";

// The session page: the conversation, the changes, the info panel, and the
// control area where the owner answers the agent or stops it.

async function open(page: Page, title: string) {
  await (await sessionList(page)).getByText(title, { exact: true }).click();
  await expect(page.getByRole("heading", { name: title })).toBeVisible();
}

/** The session's title line: its name and where it runs, no status (that is in the list and the bottom bar). */
function titleLine(page: Page, title: string) {
  return page.getByRole("heading", { name: title }).locator("..");
}
/** What the title line says besides the name: where the fixture's sessions run. */
const where = "owner-mac · demo";

async function status(title: string): Promise<string> {
  return JSON.parse(await cli("session", "get", fixture.sessions[title], "-o", "json")).session.status;
}

for (const language of ["zh-Hans", "en"]) {
  test(`a missing worktree base shows the project, ref and recovery advice (${language})`, async () => {
    const title = `缺少基准引用-${language}`;
    const started = JSON.parse(await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", title, "--base", "ios-v0.12.2", "--prompt", "say original task", "-o", "json"));
    const id = started.session.session_id;
    await expect.poll(async () => JSON.parse(await cli("session", "get", id, "-o", "json")).session.status).toBe("crashed");
    const { app, page } = await launch(fixture.socket, { systemLanguages: [language] });
    try {
      await (await sessionList(page)).getByText(title, { exact: true }).click();
      const failure = page.getByRole("alert");
      await expect(failure).toContainText(language === "en" ? "The worktree’s base ref was not found" : "无法创建工作目录：找不到基准引用");
      await expect(failure).toContainText('project "demo"');
      await expect(failure).toContainText('base ref "ios-v0.12.2"');
      await expect(failure).toContainText(language === "en" ? "is on the project’s remote" : "项目的远端仓库里有这个分支或 tag");
      await expect(failure).toContainText(language === "en" ? "in this task to retry the original request" : "再在这个任务里发一条消息，重试原来的请求");
      await expect(failure).not.toContainText(language === "en" ? "Hermesh Settings" : "Hermesh 设置");
      await expect(failure).not.toContainText("git branch");
    } finally {
      await app.close();
    }
  });
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
  // bot sent the work in, not the owner: its prompts are quiet bubbles with bot's face on the corner, no name over
  // them; who sent it shows on hover, before when.
  const prompts = log.locator(".um");
  await expect(prompts.first()).toHaveClass(/\bfrom\b/);
  await expect(prompts.first().getByTestId("prompt-sender")).toHaveAttribute("aria-label", "bot 派的活");
  const accent = await page.evaluate(() => {
    const probe = document.body.appendChild(document.createElement("div"));
    probe.style.background = "var(--accent)";
    const c = getComputedStyle(probe).backgroundColor;
    probe.remove();
    return c;
  });
  expect(await prompts.first().locator(".bub").evaluate((el) => getComputedStyle(el).backgroundColor)).not.toBe(accent);
  await expect(prompts.first().locator(".msg-act")).toHaveCSS("opacity", "0");
  await prompts.first().hover();
  await expect(prompts.first().locator(".msg-act")).toBeVisible();
  await expect(prompts.first().locator(".msg-act")).toContainText(/^bot 派的活 ·/);
  // Where it runs, in the bar beside its title, as a v2 session's.
  await expect(page.getByTestId("session-where")).toHaveText(/ · demo$/);
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
  // Nor does the session: the box under it shows it goes on.
  await expect(page.getByText("这一轮做完了", { exact: false })).toHaveCount(0);
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
    // Hover again if it goes, as it has on a busy Windows runner.
    await expect(async () => {
      await msg.hover();
      await expect(copy.locator("..")).toHaveCSS("opacity", "1", { timeout: 2_000 });
    }).toPass({ timeout: 15_000 });
    const m = (await msg.boundingBox())!;
    const c = (await copy.boundingBox())!;
    expect(c.y).toBeGreaterThanOrEqual(m.y + m.height);
    expect(Math.abs(edge === "right" ? c.x + c.width - (m.x + m.width) : c.x - m.x)).toBeLessThan(8);
  }
  // Both say when they were sent there too, as time ago (the prompt, who sent it first): after Copy under the reply.
  const ago = "(刚刚|\\d+ 分钟前)";
  for (const [msg, said] of [[log.locator(".um").first(), new RegExp(`^bot 派的活 ·${ago}$`)], [reply, new RegExp(`^${ago}$`)]] as const) {
    await msg.hover();
    await expect(msg.locator(".msg-act")).toHaveText(said);
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

test("the conversation and the box under it are one column: a reply's words, its code, steps and the work line start where the box starts, and a message ends where it ends", async () => {
  await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "对齐", "--prompt", "say 一句话\\n\\n```\\necho hi\\n```", "--wait");
  const { app, page } = await launch();
  await page.setViewportSize({ width: 1440, height: 800 });
  const log = page.getByRole("log", { name: "对话" });
  const box = async () => (await page.locator(".th-cmp").boundingBox())!;
  /** How far a thing starts from where the box starts, and ends from where the box ends. */
  const edges = async (l: Locator) => {
    const [b, c] = [(await l.boundingBox())!, await box()];
    return { left: b.x - c.x, right: b.x + b.width - (c.x + c.width) };
  };

  await open(page, "对齐");
  const reply = log.locator(".am");
  expect(Math.abs((await edges(reply.locator(".md p"))).left)).toBeLessThan(0.5);
  for (const edge of Object.values(await edges(reply.locator(".cblock")))) expect(Math.abs(edge)).toBeLessThan(0.5);
  expect(Math.abs((await edges(log.locator(".um .bub"))).right)).toBeLessThan(0.5);
  // Copy, on hover under the reply: its glyph, not its plate, starts on that line.
  await reply.hover();
  expect(Math.abs((await edges(reply.locator(".msg-act").getByRole("button", { name: "拷贝" }).locator(".ic"))).left)).toBeLessThan(1);

  await open(page, "写 hello");
  expect(Math.abs((await edges(log.getByRole("button", { name: "编辑了 1 个文件、运行了 1 条命令" }))).left)).toBeLessThan(0.5);
  expect(Math.abs((await edges(log.getByRole("button", { name: /^已编辑/ }))).left)).toBeLessThan(0.5);

  // A session at work: its line is one more row, as far under the step as steps are from each other.
  await open(page, "跑很久");
  const working = page.getByTestId("thread-working");
  expect(Math.abs((await edges(working.locator(":scope > *").first())).left)).toBeLessThan(0.5);
  const step = (await log.getByRole("button", { name: /sleep 1800/ }).boundingBox())!;
  const mark = (await working.locator(":scope > *").first().boundingBox())!;
  const rowGap = await log.evaluate((el) => parseFloat(getComputedStyle(el).rowGap));
  expect(mark.y - (step.y + step.height)).toBeGreaterThanOrEqual(rowGap);
  expect(mark.y - (step.y + step.height)).toBeLessThan(rowGap + 6);
  await app.close();
});

// The title as macOS draws a window's: 14px medium, bright in the front
// window and grey while another window has the focus. (The test windows open
// behind and never take the focus, so the test tells the window it got it.)
test("the title is 14px medium, bright in front and grey while the window is behind", async () => {
  test.skip(onWindows, "windows-latest moves the focus between windows unreliably: the title greyed in 2 of 3 runs");
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

test("double-clicking the top session title edits it in place without shifting the bar, and syncs with the sidebar", async () => {
  const started = JSON.parse(await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "顶部改名", "--prompt", "say 好", "--wait", "-o", "json"));
  const id = started.session.session_id as string;
  const { app, page } = await launch();
  try {
    // On New Session there is no session yet, so the heading stays part of the window's drag bar.
    await page.getByTestId("nav-new-thread").click();
    expect(await page.getByTestId("thread-title").evaluate((el) => getComputedStyle(el).getPropertyValue("-webkit-app-region"))).not.toBe("no-drag");

    const list = await sessionList(page);
    await open(page, "顶部改名");
    const heading = page.getByTestId("thread-title");
    const whereTag = page.getByTestId("session-where");
    expect(await page.locator(".ctool").evaluate((el) => getComputedStyle(el).getPropertyValue("-webkit-app-region"))).toBe("drag");
    expect(await heading.evaluate((el) => getComputedStyle(el).getPropertyValue("-webkit-app-region"))).toBe("no-drag");

    const hBox = (await heading.boundingBox())!;
    const wBox = (await whereTag.boundingBox())!;

    await heading.dblclick();
    const input = page.getByTestId("thread-title-input");
    await expect(input).toBeFocused();
    await expect(input).toHaveValue("顶部改名");
    expect(await input.evaluate((el: HTMLInputElement) => [el.selectionStart, el.selectionEnd])).toEqual([0, 4]);
    // Only the header turns into an input; the sidebar row stays calm.
    await expect(list.getByRole("textbox")).toHaveCount(0);

    const iBox = (await input.boundingBox())!;
    const wBoxEditing = (await whereTag.boundingBox())!;
    expect((await page.locator(".ctool").boundingBox())!.height).toBe(44);
    expect(Math.abs(iBox.x + 6 - hBox.x)).toBeLessThanOrEqual(1);
    expect(Math.abs(wBoxEditing.x - wBox.x)).toBeLessThanOrEqual(2);
    await snap(page, "header-rename");
    await page.emulateMedia({ colorScheme: "dark" });
    await snap(page, "header-rename-dark");
    await page.emulateMedia({ colorScheme: "light" });

    // Typing a longer title grows the field smoothly and slides the environment label right.
    await input.fill("这是一个更长的会话标题用来验证自适应宽度");
    expect((await input.boundingBox())!.width).toBeGreaterThan(iBox.width + 80);
    expect((await whereTag.boundingBox())!.x).toBeGreaterThan(wBoxEditing.x + 80);

    // Esc cancels and restores the original title.
    await input.press("Escape");
    await expect(input).toHaveCount(0);
    await expect(heading).toHaveText("顶部改名");

    // IME composition Enter does not prematurely commit; plain Enter saves and updates both header and sidebar.
    await heading.dblclick();
    await expect(input).toBeFocused();
    await input.fill("回车保存的新标题");
    await input.evaluate((el) => {
      el.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", keyCode: 229, isComposing: true, bubbles: true, cancelable: true }));
    });
    await expect(input).toBeFocused();
    await input.press("Enter");
    await expect(input).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "回车保存的新标题" })).toBeVisible();
    await expect(list.getByRole("listitem").filter({ hasText: "回车保存的新标题" })).toBeVisible();
    await expect.poll(async () => JSON.parse(await cli("session", "get", id, "-o", "json")).session.title).toBe("回车保存的新标题");

    // Clicking outside commits the edited title as well.
    await page.getByRole("heading", { name: "回车保存的新标题" }).dblclick();
    await expect(input).toBeFocused();
    await input.fill("失焦保存的标题");
    await page.getByRole("log", { name: "对话" }).click();
    await expect(page.getByRole("heading", { name: "失焦保存的标题" })).toBeVisible();
    await expect(list.getByRole("listitem").filter({ hasText: "失焦保存的标题" })).toBeVisible();
  } finally {
    await app.close();
  }
});

test("the session title bar floats over the conversation as liquid glass, resizing its lens filter and keeping controls clickable", async () => {
  const paragraphs = Array.from({ length: 18 }, (_, i) => `### 第 ${i + 1} 节：液态玻璃验证\n\n这里是一段包含 \`inline_code_${i}\` 与彩色块的内容，用来在标题栏下方滚动并折射显示。`).join("\n\n");
  await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "液态玻璃标题栏", "--prompt", `say ${paragraphs}`, "--wait");
  const { app, page } = await launch();
  try {
    await page.setViewportSize({ width: 1280, height: 720 });
    await page.getByTestId("nav-new-thread").click();
    await expect(page.locator(".ctool.glassbar")).toHaveCount(0);

    await open(page, "液态玻璃标题栏");
    const bar = page.locator(".ctool.glassbar");
    await expect(bar).toBeVisible();
    const barBox = (await bar.boundingBox())!;
    expect(barBox.y).toBe(0);
    expect(barBox.height).toBe(44);

    const filterInfo = await bar.evaluate((el) => {
      const bf = getComputedStyle(el).backdropFilter || el.style.backdropFilter;
      const filter = el.querySelector("svg.glass-defs filter");
      const mapHref = el.querySelector("svg.glass-defs feImage")?.getAttribute("href") ?? "";
      const scales = [...el.querySelectorAll("feDisplacementMap")].map((n) => Number(n.getAttribute("scale")));
      const bg = getComputedStyle(el).backgroundColor;
      return {
        position: getComputedStyle(el).position,
        bf,
        filterId: filter?.id ?? "",
        w: Number(filter?.getAttribute("width") ?? 0),
        h: Number(filter?.getAttribute("height") ?? 0),
        mapSvg: mapHref.startsWith("data:image/svg+xml;utf8,") ? decodeURIComponent(mapHref.slice("data:image/svg+xml;utf8,".length)) : "",
        scales,
        bg,
      };
    });
    expect(filterInfo.position).toBe("absolute");
    expect(filterInfo.filterId).toMatch(/^lg/);
    expect(filterInfo.bf).toContain(`#${filterInfo.filterId}`);
    expect(filterInfo.w).toBe(Math.round(barBox.width));
    expect(filterInfo.h).toBe(44);
    expect(filterInfo.mapSvg).toContain(`offset="${(44 - 14) / 44}" stop-color="rgb(128,128,128)"`);
    expect(filterInfo.mapSvg).toContain('offset="1" stop-color="rgb(128,128,0)"');
    expect(filterInfo.scales).toEqual([22, 25, 28]);
    expect(filterInfo.bg).toMatch(/^rgba\(/);

    const scroller = page.locator(".spage .cscroll");
    expect((await scroller.boundingBox())!.y).toBe(0);
    const card = page.getByRole("complementary", { name: "任务信息" });
    await expect(card).toBeVisible();
    expect((await card.boundingBox())!.y).toBeGreaterThanOrEqual(44 + 20);

    await scroller.evaluate((el) => {
      el.scrollTop = 180;
    });
    await expect.poll(async () =>
      scroller.evaluate((el) => {
        const body = el.querySelector(".dbody")!.getBoundingClientRect();
        return body.top < 0 && body.bottom > 44;
      }),
    ).toBe(true);
    await snap(page, "session-liquid-glass-light");
    await page.emulateMedia({ colorScheme: "dark" });
    await snap(page, "session-liquid-glass-dark");
    await page.emulateMedia({ colorScheme: "light" });

    if (onMac) {
      await page.getByRole("button", { name: "隐藏边栏" }).click();
      await expect.poll(async () => bar.evaluate((el) => Number(el.querySelector("svg.glass-defs filter")?.getAttribute("width") ?? 0))).toBe(1280);
      await page.getByRole("button", { name: "显示边栏" }).click();
      await expect(page.getByRole("complementary", { name: "边栏" })).toBeVisible();
    }
  } finally {
    await app.close();
  }
});

test("the session card floats over the conversation with what a glance needs, and the toolbar hides it", async () => {
  const { app, page } = await launch();
  // The card docks only when there is room beside the conversation, regardless of the runner's display size.
  await page.setViewportSize({ width: 1440, height: 800 });
  await open(page, "写 hello");
  const card = page.getByRole("complementary", { name: "任务信息" });
  const toggle = page.getByRole("button", { name: "信息面板", exact: true });
  await expect(card).toBeVisible();
  await expect(toggle).toHaveAttribute("aria-pressed", "true");
  // Its environment, one row each, under a title: who started it, with what, on which machine, in which project, on which branch
  // (a worktree session's own). No state, turns or changes: the list and the conversation say those.
  await expect(card.getByRole("heading", { name: "环境" })).toBeVisible();
  const branch = `hermesh/${fixture.sessions["写 hello"]}`;
  await expect(card.getByRole("listitem")).toHaveText([/bot 发起$/, "Gemini CLI 执行", thisComputer, "demo", branch]);
  await expect(card).not.toContainText("已完成");
  await expect(card).not.toContainText("轮");
  await expect(card).not.toContainText("tokens");
  // Every row the same height, its icon the same size.
  const rows = card.getByRole("listitem");
  const heights = await rows.evaluateAll((els) => els.map((el) => el.getBoundingClientRect().height));
  expect(new Set(heights).size).toBe(1);
  const icons = await rows.evaluateAll((els) => els.map((el) => el.querySelector("svg, img, .av")!.getBoundingClientRect().width));
  expect(new Set(icons)).toEqual(new Set([16]));
  // Under it, the session's context: the model it runs on, and its reasoning effort, which the agent
  // doesn't tell, so it follows the agent's own. The same width and rows, 8 apart.
  const context = page.getByRole("complementary", { name: "上下文" });
  await expect(context.getByRole("heading", { name: "上下文" })).toBeVisible();
  await expect(context.getByRole("listitem")).toHaveText(["gemini-3-pro", "推理强度 跟随 Gemini CLI"]);
  const env = (await card.boundingBox())!;
  const below = (await context.boundingBox())!;
  expect([below.x, below.width]).toEqual([env.x, env.width]);
  expect(Math.round(below.y - (env.y + env.height))).toBe(8);
  const contextRows = await context.getByRole("listitem").evaluateAll((els) => els.map((el) => el.getBoundingClientRect().height));
  expect(new Set([...heights, ...contextRows]).size).toBe(1);
  // A long value is cut short; hovered, it shows whole (the project: its folder).
  await expect(card.getByRole("button", { name: branch })).toHaveAttribute("title", branch);
  await expect(card.getByRole("button", { name: "demo" })).toHaveAttribute("title", onWindows ? /^[A-Z]:\\.+/ : /^\/.+/);

  // The project and the branch open a menu of what to do with them; this Mac's project can open its folder too.
  await card.getByRole("button", { name: "demo" }).click();
  const menu = page.getByRole("menu", { name: "项目" });
  await expect(menu.getByRole("menuitem")).toHaveText(["拷贝项目路径", onWindows ? "在文件资源管理器中打开" : onMac ? "在访达中打开" : "在文件管理器中打开", "在终端中打开"]);
  // Right under the project, from its left edge, inside the window.
  await menu.evaluate((el) => Promise.all(el.getAnimations().map((x) => x.finished))); // it rises into place
  const anchor =(await card.getByRole("button", { name: "demo" }).boundingBox())!;
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
  const [settings] = await Promise.all([app.waitForEvent("window"), card.getByRole("button", { name: "环境设置" }).click()]);
  await settings.waitForLoadState();
  expect(settings.url()).toMatch(/#settings\/env\/.+/);
  await settings.close();
  // It floats over the conversation, which keeps clear of it.
  const c = (await card.boundingBox())!;
  const log = (await page.getByRole("log", { name: "对话" }).boundingBox())!;
  expect(log.x + log.width).toBeLessThanOrEqual(c.x);
  await snap(page, "session-info");

  // The toggle is a toolbar button the size of the list's view options, highlighted only while the info shows.
  const left = (await page.getByRole("button", { name: "视图选项" }).boundingBox())!;
  const right = (await toggle.boundingBox())!;
  expect([right.width, right.height]).toEqual([left.width, left.height]);
  const fill = () => toggle.evaluate((el) => getComputedStyle(el).backgroundColor);
  expect(await fill()).not.toBe("rgba(0, 0, 0, 0)");
  await toggle.click();
  await expect(card).toHaveCount(0);
  await expect(context).toHaveCount(0);
  await expect(toggle).toHaveAttribute("aria-pressed", "false");
  await page.mouse.move(0, 0);
  await expect.poll(fill).toBe("rgba(0, 0, 0, 0)");
  await snap(page, "session-chat");
  // bot sent the work in: its messages are a quiet bubble with the reading surface's words (#202127 / #dededb) and a text cursor (the owner's soft cool-grey bubble is thread-v2.spec.ts's).
  const bubble = page.getByRole("log", { name: "对话" }).locator(".um .bub").first();
  const prose = page.getByRole("log", { name: "对话" }).locator(".am .md").first();
  const colors = () => bubble.evaluate((el) => [getComputedStyle(el).backgroundColor, getComputedStyle(el).color, getComputedStyle(el).cursor]);
  expect(await colors()).toEqual(["rgba(32, 33, 39, 0.07)", "rgb(32, 33, 39)", "text"]);
  expect(await prose.evaluate((el) => [getComputedStyle(el).color, getComputedStyle(el).cursor])).toEqual(["rgb(32, 33, 39)", "text"]);
  await page.emulateMedia({ colorScheme: "dark" });
  await expect.poll(colors).toEqual(["rgba(255, 255, 255, 0.075)", "rgb(222, 222, 219)", "text"]);
  await expect.poll(() => prose.evaluate((el) => getComputedStyle(el).color)).toBe("rgb(222, 222, 219)");
  await snap(page, "session-chat-dark");
  await page.emulateMedia({ colorScheme: "light" });
  await toggle.click();
  await expect(card).toBeVisible();
  await app.close();
});

test("failed sessions tell what went wrong and what to do; an archived one says it is read-only", async () => {
  const { app, page } = await launch();
  await open(page, "崩溃");
  await expect(titleLine(page, "崩溃")).toHaveText("崩溃" + where);
  // Said once, in a quiet card where the reply would be: what happened to the coding agent, who goes on, the agent's words folded under 详情, and 归档.
  const failed = page.getByRole("alert");
  await expect(failed).toContainText("Gemini CLI 意外退出了");
  await expect(failed).not.toContainText("已崩溃"); // the sidebar says so
  await expect(failed.locator(".qf-ic")).toHaveCSS("color", /^rgb\((215, 51, 63|222, 85, 85|255, 102, 112)\)$/); // red only in its mark
  await expect(failed.locator(".qf-title")).not.toHaveCSS("color", /^rgb\((215, 51, 63|222, 85, 85|255, 102, 112)\)$/);
  await expect(failed).toContainText("bot 再发消息就会从这里接着做，之前的改动都还在。");
  await expect(failed).not.toContainText("exit status");
  await failed.getByRole("button", { name: /详情/ }).click();
  await expect(failed).toContainText("agent process exited unexpectedly");
  await expect(failed.getByRole("button", { name: "归档" })).toBeVisible();
  await snap(page, "session-crashed");

  await open(page, "限流");
  await expect(titleLine(page, "限流")).toHaveText("限流" + where);
  const log = page.getByRole("log", { name: "对话" });
  await expect(log).not.toContainText("第 1 轮失败"); // not said twice
  await expect(page.getByRole("alert")).toContainText("Gemini CLI 触发了限流");
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
  const ctl = page.getByTestId("pending-question-banner");
  await expect(ctl).toContainText("Gemini CLI 问你");
  await expect(ctl).toContainText("选哪个颜色");
  await expect(ctl).toContainText("还剩");
  await expect(ctl.getByTestId("question-step")).toHaveText("1 / 2");
  const answer = ctl.getByRole("button", { name: /^回答/ });
  await expect(answer).toBeDisabled();
  await snap(page, "session-question");

  await page.keyboard.press("1");
  await expect(ctl.getByRole("radio", { name: /红/ })).toHaveAttribute("aria-checked", "true");
  await expect(ctl.getByRole("radio", { name: /蓝/ })).toHaveAttribute("aria-checked", "false");
  await page.keyboard.press("2");
  await expect(ctl.getByRole("radio", { name: /蓝/ })).toHaveAttribute("aria-checked", "true");
  await page.keyboard.press("1");
  // The form's second field (free text) is optional and reachable via Right/Left without hijacking focus.
  await expect(answer).toBeEnabled();
  await page.keyboard.press("ArrowRight");
  await expect(ctl.getByTestId("question-step")).toHaveText("2 / 2");
  await expect(ctl).toContainText("可选");
  await page.keyboard.press("ArrowLeft");
  await expect(ctl.getByTestId("question-step")).toHaveText("1 / 2");
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

test("a multi-question decision presents one item at a time, auto-advances, and lets the owner revisit earlier answers", async () => {
  const title = "多选拍板";
  const started = JSON.parse(await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", title, "--prompt", "ask3", "-o", "json"));
  const id = started.session.session_id as string;
  await expect.poll(async () => JSON.parse(await cli("session", "get", id, "-o", "json")).session.status).toBe("waiting_input");

  const { app, page } = await launch();
  try {
    await open(page, title);
    const ctl = page.getByTestId("pending-question-banner");
    await expect(ctl.getByTestId("question-step")).toHaveText("1 / 3");
    await expect(ctl).toContainText("推进节奏选哪种？");
    await expect(ctl).not.toContainText("The agent has 3 questions.");
    await expect(ctl).not.toContainText("输出如何处理？");
    await expect(ctl.locator(".qx-rec").first()).toHaveText("推荐");

    // Pick item 1 -> auto-advances to 2 / 3.
    await page.keyboard.press("1");
    await expect(ctl.getByTestId("question-step")).toHaveText("2 / 3");
    await expect(ctl).toContainText("输出如何处理？");

    // Pick item 2 -> auto-advances to 3 / 3.
    await page.keyboard.press("2");
    await expect(ctl.getByTestId("question-step")).toHaveText("3 / 3");
    await expect(ctl).toContainText("终端程序怎么选？");

    // Pick item 3 -> stays on 3 / 3 with Answer enabled so the owner can review/revise.
    await page.keyboard.press("1");
    await expect(ctl.getByTestId("question-step")).toHaveText("3 / 3");
    const answer = ctl.getByRole("button", { name: /^回答/ });
    await expect(answer).toBeEnabled();

    // Step back to 2 / 3 to revise the choice, then press Enter to submit all 3 answers.
    await ctl.getByRole("button", { name: "上一个" }).click();
    await expect(ctl.getByTestId("question-step")).toHaveText("2 / 3");
    await ctl.getByRole("radio", { name: /自动喂给模型/ }).click();
    await page.keyboard.press("Enter");

    await expect.poll(async () => JSON.parse(await cli("session", "get", id, "-o", "json")).session.status).toBe("idle");
    const log = page.getByRole("log", { name: "对话" });
    await expect(log).toContainText("answers3: q0=(Recommended) 一步到位|q0_other=<nil>|q1=自动喂给模型|q2=auto");
  } finally {
    await app.close();
  }
});

async function expectImageLoaded(img: Locator, timeout = 30_000): Promise<void> {
  await expect(img).toBeVisible({ timeout });
  await expect
    .poll(() => img.evaluate((el) => el instanceof HTMLImageElement && el.complete && el.naturalWidth > 0), { timeout })
    .toBe(true);
}

test("a screenshot pasted into the box is attached and goes with the message; cells copied from a spreadsheet paste as their words", async () => {
  const { app, page } = await launch();
  try {
    await page.getByTestId("nav-new-thread").click();
    await pickFrom(page.getByRole("button", { name: "项目" }), page.getByTestId("project-item-demo"));
    const input = page.getByTestId("thread-composer-input");
    await input.click();
    await pickFrom(page.getByRole("button", { name: "模型" }), page.getByTestId("picker-provider-scripted").getByTestId("picker-model-scripted-pro"));
    const tiles = page.getByTestId("thread-composer").locator(".cmp-att .tile");
    // A spreadsheet copies its cells' words and a picture of them: the words go in the box, nothing is attached.
    await input.focus();
    await paste(app, page, { text: "收入\t123\n成本\t45", picture: true });
    await expect(input).toHaveValue("收入\t123\n成本\t45");
    await expect(tiles).toHaveCount(0);
    // A screenshot: uploaded to the machine and attached, as a file from ＋ is; the words stay as they are.
    await input.fill("images");
    let uploaded = page.waitForResponse((r) => r.url().endsWith("/UploadBlob"));
    await paste(app, page, { picture: true });
    await (await uploaded).finished();
    await expect(tiles).toHaveCount(1);
    await expect(tiles.first()).toHaveAttribute("title", "image.png");
    await expectImageLoaded(tiles.first().locator("img"));
    await expect(input).toHaveValue("images");
    // The same picture pasted again is attached once.
    uploaded = page.waitForResponse((r) => r.url().endsWith("/UploadBlob"));
    await paste(app, page, { picture: true });
    await (await uploaded).finished();
    await expect(tiles).toHaveCount(1);
    await snap(page, "session-pasted-picture");
    // Sent, the user's own bubble shows the picture thumbnail, and the agent has it.
    await input.press("Enter");
    const log = page.getByRole("log");
    await expectImageLoaded(log.locator(".um").first().locator(".um-att .tile.img img"));
    await expect(log.locator(".am").first()).toContainText("images: image.png", { timeout: 30_000 });

    // An attachment-only message (no text) sends and shows its thumbnail without an empty text bubble.
    await page.getByTestId("thread-composer-input").focus();
    uploaded = page.waitForResponse((r) => r.url().endsWith("/UploadBlob"));
    await paste(app, page, { picture: true });
    await (await uploaded).finished();
    await page.getByTestId("composer-submit-btn").click();
    const onlyPic = log.locator(".um").nth(1);
    await expectImageLoaded(onlyPic.locator(".um-att .tile.img img"));
    await expect(onlyPic.locator(".bub")).toHaveCount(0);
    await snap(page, "session-sent-picture");
  } finally {
    await app.close();
  }
});

test("files dragged over a session's window are attached to its box when let go, and go with the next message", async () => {
  const { app, page } = await launch();
  try {
    await page.getByTestId("nav-new-thread").click();
    await pickFrom(page.getByRole("button", { name: "项目" }), page.getByTestId("project-item-demo"));
    const input = page.getByTestId("thread-composer-input");
    await input.click();
    await pickFrom(page.getByRole("button", { name: "模型" }), page.getByTestId("picker-provider-scripted").getByTestId("picker-model-scripted-pro"));
    await input.fill("say started");
    await input.press("Enter");
    const replies = page.getByRole("log").locator(".am");
    await expect(replies.first()).toContainText("started", { timeout: 30_000 });

    // Over the window, the box says it takes them; let go, they are uploaded and attached.
    const shot = await app.evaluate(({ nativeImage }) => {
      const w = 32;
      const h = 32;
      const buf = Buffer.alloc(w * h * 4);
      for (let y = 0; y < h; y++) {
        for (let x = 0; x < w; x++) {
          const i = (y * w + x) * 4;
          buf[i + 0] = Math.round((x / (w - 1)) * 220 + 35); // B
          buf[i + 1] = Math.round(((w - 1 - x) / (w - 1)) * 120 + 80); // G
          buf[i + 2] = Math.round((y / (h - 1)) * 220 + 35); // R
          buf[i + 3] = 0xff; // A
        }
      }
      return nativeImage.createFromBitmap(buf, { width: w, height: h }).toPNG().toString("base64");
    });
    const box = page.getByTestId("thread-composer");
    const drag = await dragFiles(page, [
      { name: "shot.png", type: "image/png", base64: shot },
      { name: "notes.md", type: "text/markdown", base64: Buffer.from("# Notes\n").toString("base64") },
    ]);
    await expect(box).toHaveClass(/\bdropping\b/);
    await expect(box.locator(".cmp-drop")).toContainText("松手添加附件");
    await snap(page, "session-dropping");
    await drag.drop();
    await expect(box).not.toHaveClass(/\bdropping\b/);
    const tiles = box.locator(".cmp-att .tile");
    await expect(tiles).toHaveCount(2);
    await expect(tiles.nth(0)).toHaveAttribute("title", "shot.png");
    await expectImageLoaded(tiles.nth(0).locator("img"));
    await expect(tiles.nth(1)).toHaveAttribute("title", "notes.md");
    await expect(input).toBeFocused();
    // The next message takes them to the agent and shows both the picture thumbnail and file tile on the user's bubble.
    await input.fill("inspect");
    await input.press("Enter");
    await expect(replies.last()).toContainText("attachments=[shot.png,notes.md]", { timeout: 30_000 });
    const sentBubble = page.getByRole("log").locator(".um").last();
    await expectImageLoaded(sentBubble.locator('.um-att .tile.img[title="shot.png"] img'));
    await expect(sentBubble.locator('.um-att .tile.file[title="notes.md"]')).toBeVisible();
    await expect(tiles).toHaveCount(0);

    // Reloading clears the in-memory blob cache; reopening the session downloads the thumbnail via DownloadBlob.
    await page.reload();
    const downloaded = page.waitForResponse((r) => r.url().endsWith("/DownloadBlob"));
    await open(page, "say started");
    await (await downloaded).finished();
    const reloadedBubble = page.getByRole("log").locator(".um").last();
    await expectImageLoaded(reloadedBubble.locator('.um-att .tile.img[title="shot.png"] img'));
    await expect(reloadedBubble.locator('.um-att .tile.file[title="notes.md"]')).toBeVisible();
    await snap(page, "session-sent-attachments");
  } finally {
    await app.close();
  }
});

test("a session worked on in place shows the branch its folder is on, or that it is no git repository; another machine's can only be copied", async () => {
  const { app, page } = await launch();
  await page.setViewportSize({ width: 1440, height: 800 });
  const card = page.getByRole("complementary", { name: "任务信息" });
  await open(page, "就地");
  await expect(card.getByRole("listitem")).toHaveText([/bot 发起$/, "Gemini CLI 执行", thisComputer, "live", "feature/card"]);
  await open(page, "不在仓库里");
  await expect(card.getByRole("listitem")).toHaveText([/bot 发起$/, "Gemini CLI 执行", thisComputer, "notes", "不是 Git 仓库"]);
  // No branch, nothing to pick.
  await expect(card.getByRole("button", { name: "不是 Git 仓库" })).toHaveCount(0);
  // Another machine's project and branch can be copied, not opened here.
  await open(page, "在另一台 Mac 上");
  await expect(card.getByRole("listitem").nth(2)).toHaveText("work-mac");
  await card.getByRole("button", { name: "billing" }).click();
  await expect(page.getByRole("menu", { name: "项目" }).getByRole("menuitem")).toHaveText(["拷贝项目路径"]);
  await app.close();
});

test("a running session shows what it does, and 停止 stops the turn", async () => {
  const { app, page } = await launch();
  await page.setViewportSize({ width: 1440, height: 800 });
  await open(page, "跑很久");
  await expect(titleLine(page, "跑很久")).toHaveText("跑很久" + where);
  const working = page.getByTestId("thread-working");
  await expect(working).toContainText("处理中");
  // A live one: the card is its environment alone; the work line below says what runs.
  const card = page.getByRole("complementary", { name: "任务信息" });
  await expect(card).toContainText("Gemini CLI");
  await expect(card).not.toContainText("运行中");
  // Neither its model nor its effort known (the agent tells neither): no context card.
  await expect(page.getByRole("complementary", { name: "上下文" })).toHaveCount(0);
  // What it is doing is the running tool call in the conversation, not repeated in the work line.
  await expect(working).not.toContainText("sleep 1800");
  await expect(working).toContainText(/\d+:\d\d/);
  await expect(page.getByRole("log", { name: "对话" })).toContainText("sleep 1800");
  const runningStep = page.getByRole("log", { name: "对话" }).getByRole("button", { name: /sleep 1800/ });
  await expect(runningStep).toHaveAttribute("aria-busy", "true");
  await expect(runningStep).toHaveClass(/\bshimmer\b/);
  await snap(page, "session-running");

  await page.getByTestId("composer-stop-btn").click();
  await expect(page.getByRole("status")).toContainText("已打断「跑很久」");
  await expect(working).toHaveCount(0);
  await expect.poll(() => status("跑很久")).not.toBe("running");
  // Who stopped it, and no more: the box under it shows it goes on.
  await expect(page.getByRole("log", { name: "对话" }).locator(".tnote")).toHaveText("你打断了这一轮");
  await expect(titleLine(page, "跑很久")).toHaveText("跑很久" + where);
  await expect(page.getByText("这一轮做完了。", { exact: false })).toHaveCount(0);
  await snap(page, "session-interrupted");
  await app.close();
});

test("a permission mode chosen while a turn runs holds for that turn's next step", async () => {
  const id = JSON.parse(await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "中途改权限", "--prompt", "sleep 30s\nwrite mid_mode.txt 改了\nsay 写好了", "-o", "json")).session.session_id as string;
  const { app, page } = await launch();
  await open(page, "中途改权限");
  await expect(page.getByTestId("thread-working")).toBeVisible();
  // While it runs, from 完全权限 (the mode the CLI started it in, which never asks) to 受控审批.
  await page.getByTestId("thread-composer").getByRole("button", { name: "添加附件" }).click();
  const plus = page.getByTestId("composer-plus-menu");
  await expect(plus.getByRole("menuitemradio", { checked: true })).toContainText("完全权限");
  await plus.getByRole("menuitemradio", { name: /受控审批/ }).click();
  // The write the turn comes to next waits for the owner, as 受控审批 says.
  const ask = page.getByTestId("pending-approval-banner");
  await expect(ask).toContainText("mid_mode.txt", { timeout: 40_000 });
  await page.getByTestId("approve-once-btn").click();
  await expect(page.getByRole("log", { name: "对话" }).locator(".am").last()).toContainText("写好了", { timeout: 30_000 });
  await expect.poll(async () => JSON.parse(await cli("session", "get", id, "-o", "json")).session.status).toBe("idle");
  await app.close();
});

test("a turn of many steps opens them in a card that stops growing and scrolls inside, at the newest, following it while the turn runs", async () => {
  const title = "很多步";
  const steps = Array.from({ length: 60 }, (_, i) => `shell echo step-${i}`);
  const late = ["shell echo late-0", "shell echo late-1", "shell echo late-2"];
  await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", title, "--prompt", [...steps, "shell sleep 10", ...late, "shell sleep 12", "say 都做完了"].join("\n"), "-o", "json");
  const { app, page } = await launch();
  try {
    await (await sessionList(page)).getByText(title, { exact: true }).click();
    const log = page.getByRole("log", { name: "对话" });
    const head = log.locator(".tg-h").first();
    await expect(head).toContainText("运行了 61 条命令", { timeout: 60_000 });
    await head.click();
    const card = log.locator(".tg-b");
    const scroller = card.locator(".tg-s");
    const back = card.getByRole("button", { name: "回到底部" });
    // Ten and a half rows high at most, scrolling inside, and only the rows near what is in sight drawn.
    const rowHeight = await card.locator(".tg-l > .vrow").last().evaluate((el) => el.getBoundingClientRect().height);
    expect(((await card.boundingBox())!.height - 2) / rowHeight).toBeCloseTo(10.5, 0);
    expect(await scroller.evaluate((el) => el.scrollHeight > el.clientHeight)).toBe(true);
    expect(await card.locator(".tool").count()).toBeLessThan(61);
    // At the newest step, the one at work, with more above it fading; following it, so no arrow.
    const working = card.locator(".tool", { hasText: "sleep 10" });
    await expect(working).toBeInViewport();
    await expect(card).toHaveClass(/\bmore-up\b/);
    await expect(card).not.toHaveClass(/\bmore-down\b/);
    await expect(back).toBeHidden();
    await snap(page, "session-many-steps-running");
    // Scrolled up by the owner: it stays there, fading below, and offers the way back.
    await scroller.hover();
    await page.mouse.wheel(0, -500);
    await expect(card).toHaveClass(/\baway\b/);
    await expect(back).toBeVisible();
    await expect(working).not.toBeInViewport();
    await expect(card).toHaveClass(/\bmore-down\b/);
    // Steps come while it is scrolled up: it stays where the owner left it, and still offers the way back.
    const at = await scroller.evaluate((el) => el.scrollTop);
    await expect(head).toContainText("运行了 65 条命令", { timeout: 60_000 });
    expect(await scroller.evaluate((el) => el.scrollTop)).toBe(at);
    await expect(back).toBeVisible();
    await back.click();
    const newest = card.locator(".tool", { hasText: "sleep 12" });
    await expect(newest).toBeInViewport();
    await expect(back).toBeHidden();
    // The turn ends: no arrow while nothing runs.
    await expect(log).toContainText("都做完了", { timeout: 60_000 });
    await expect(back).toHaveCount(0);
    // A step opened in the card shows its output there, in view and a size down (180 at most).
    await card.locator(".tool .th[aria-expanded]").last().click();
    const out = card.locator(".tool.open .out");
    await expect(out).toBeInViewport();
    expect((await out.boundingBox())!.height).toBeLessThanOrEqual(180);
    // At the top: nothing above fades, more below does.
    await scroller.evaluate((el) => el.scrollTo({ top: 0 }));
    await expect(card).not.toHaveClass(/\bmore-up\b/);
    await expect(card).toHaveClass(/\bmore-down\b/);
    await expect(card.locator(".tool").first()).toContainText("echo step-0");
    await snap(page, "session-many-steps");
    // The scroll bar held a moment, then dragged to the top: the card goes where it is dragged and stays there (where
    // scroll bars only show while scrolling there is nothing to hold, and nothing to check).
    await card.locator(".tool.open .th").click();
    await scroller.evaluate((el) => el.scrollTo({ top: el.scrollHeight }));
    await expect(card).not.toHaveClass(/\bmore-down\b/);
    const box = (await scroller.boundingBox())!;
    await scroller.evaluate((el) => {
      (window as unknown as { __scrolls: number }).__scrolls = 0;
      el.addEventListener("scroll", () => (window as unknown as { __scrolls: number }).__scrolls++);
    });
    await page.mouse.move(box.x + box.width - 5, box.y + box.height - 14);
    await page.mouse.down();
    await page.waitForTimeout(1300);
    await page.mouse.move(box.x + box.width - 5, box.y + 4, { steps: 12 });
    await page.waitForTimeout(600);
    const dragged = await page.evaluate(() => (window as unknown as { __scrolls: number }).__scrolls);
    if (dragged > 0) {
      // Well up from the end (rows measured as they are drawn can move it a little), and still there a moment later.
      const [top, max] = await scroller.evaluate((el) => [el.scrollTop, el.scrollHeight - el.clientHeight]);
      expect(top).toBeLessThan(max / 3);
      await page.waitForTimeout(600);
      expect(await scroller.evaluate((el) => el.scrollTop)).toBe(top);
    }
    await page.mouse.up();
    test.info().annotations.push({ type: "scroll bar dragged", description: String(dragged > 0) });
  } finally {
    await app.close();
  }
});

test("a session its agent stops as it starts says the agent interrupted it, not that it failed", async () => {
  const title = "刚开始就打断";
  const id = JSON.parse(await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", title, "--prompt", "sleep 1800s\nsay 不会说", "-o", "json")).session.session_id;
  await cli("session", "interrupt", id);
  await expect.poll(async () => JSON.parse(await cli("session", "get", id, "-o", "json")).session.status).toBe("idle");
  const { app, page } = await launch();
  try {
    await (await sessionList(page)).getByText(title, { exact: true }).click();
    const log = page.getByRole("log", { name: "对话" });
    await expect(log.locator(".tnote")).toHaveText("bot 打断了这一轮");
    await expect(log).not.toContainText("出错了");
    await expect(page.getByRole("alert")).toHaveCount(0);
  } finally {
    await app.close();
  }
});

test("tool details stay folded until opened, a failed one and a running one too", async () => {
  await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "命令失败", "--prompt", "shell ls no-such-file\nshell sleep 1800");
  const { app, page } = await launch();
  await open(page, "命令失败");
  const log = page.getByRole("log", { name: "对话" });
  const step = log.getByRole("button", { name: /运行了 2 条命令/ });
  await expect(step).toHaveAttribute("aria-busy", "true");
  await expect(step).toHaveClass(/\bshimmer\b/);
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
  await page.getByTestId("composer-stop-btn").click();
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
  expect(rows, `the conversation's rows as it changed: ${rows}`).toEqual([await log.locator(":scope > *").count()]);
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
  // A block that names its language is coloured, light or dark with the app; one that doesn't stays plain.
  // Each has a bar with its language and Copy.
  const go = md.locator("pre code").first();
  await expect(go).toHaveText('func main() { fmt.Println("hi") }');
  const bar = md.locator(".cblock").first().locator(".cb-h");
  await expect(bar.locator(".cb-lang")).toHaveText("go");
  await bar.getByRole("button", { name: "拷贝" }).click();
  await expect.poll(() => app.evaluate(({ clipboard }) => clipboard.readText())).toBe('func main() { fmt.Println("hi") }');
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
  // The agent's words are 14.5 on 1.625, in Google Sans Flex, with finxwork's heading scale, link colour and code surface.
  expect(await md.evaluate((el) => [getComputedStyle(el).fontSize, getComputedStyle(el).lineHeight])).toEqual(["14.5px", "23.5625px"]);
  expect(await md.evaluate((el) => getComputedStyle(el).fontFamily)).toMatch(/^"Google Sans Flex"/);
  expect(await md.getByRole("heading", { name: "结论" }).evaluate((el) => [getComputedStyle(el).fontSize, getComputedStyle(el).lineHeight])).toEqual(["18px", "28px"]);
  await expect(md.getByRole("link")).toHaveCSS("color", "rgb(40, 95, 184)");
  await expect(md.locator(".cblock").first()).toHaveCSS("background-color", "rgb(235, 237, 242)");
  expect(await page.evaluate(() => document.fonts.check('14px "Google Sans Flex"') && document.fonts.check('13px "Google Sans Code"'))).toBe(true);
  await snap(page, "session-markdown");
  await app.close();
});

test("formulas, diagrams, callouts, folds, footnotes and tables show as the reply means them", async () => {
  const reply = [
    "公式 \\(x_i^2 + \\frac{a}{b}\\)，金额 $100 和 $200 保持原文。颜色 `#1F6BFF`。",
    "",
    "$$",
    "E = mc^2",
    "$$",
    "",
    "> [!NOTE]",
    "> 这是提示。",
    "",
    ':::details{title="展开计算说明"}',
    "折叠起来的内容。",
    ":::",
    "",
    "引用一处来源[^1]。",
    "",
    "[^1]: 来源说明",
    "",
    "| 指标 | 本期 | 上期 |",
    "|:---|---:|---:|",
    "| 收入 | 123.45 | 98.76 |",
    "",
    "```mermaid",
    "flowchart LR",
    ' A["读取 profiles/<id><br/>本机"] <--> B{检查}',
    " B --> C[报告]",
    "```",
    "",
    "```sh",
    "echo ​hi",
    "```",
  ].join("\\n");
  await cli("session", "start", "--project", "demo", "--agent", "gemini", "--title", "富文本", "--prompt", `say ${reply}`, "--wait");
  const { app, page } = await launch();
  await open(page, "富文本");
  const md = page.getByRole("log", { name: "对话" }).locator(".md");
  // Formulas render (a subscript smaller than its base); prices stay words.
  await expect(md.locator(".katex")).toHaveCount(2);
  await expect(md.locator(".katex-display")).toHaveCount(1);
  await expect(md).toContainText("$100 和 $200");
  const [sub, base] = await md.locator(".katex").first().evaluate((k) => [parseFloat(getComputedStyle(k.querySelector(".msupsub .mord")!).fontSize), parseFloat(getComputedStyle(k).fontSize)]);
  expect(sub).toBeLessThan(base * 0.8);
  await expect(md.locator("code .md-color")).toHaveCount(1);
  await expect(md.locator(".md-callout[data-kind=note]")).toContainText("这是提示。");
  // A fold opens and shuts.
  const fold = md.locator("details.md-details");
  await expect(fold.locator("summary")).toHaveText("展开计算说明");
  await expect(fold.getByText("折叠起来的内容。")).toBeHidden();
  await fold.locator("summary").click();
  await expect(fold.getByText("折叠起来的内容。")).toBeVisible();
  // A footnote's mark goes to its note.
  await md.locator("[data-footnote-ref]").click();
  expect(await page.evaluate(() => document.activeElement?.closest(".footnotes") !== null)).toBe(true);
  // A diagram draws as a picture (preserving <id> placeholders and <br/> breaks), its unescaped source a click away.
  const diagram = md.locator(".md-diagram");
  await expect.poll(() => diagram.locator(".md-diagram-svg").evaluate((el) => !!el.shadowRoot?.querySelector("svg")), { timeout: 20_000 }).toBe(true);
  expect(await diagram.locator(".md-diagram-svg").evaluate((el) => el.shadowRoot?.textContent ?? "")).toContain("profiles/<id>");
  await diagram.getByRole("button", { name: "源码" }).click();
  await expect(diagram.locator(".cblock code")).toContainText("profiles/<id>");
  await expect(diagram.locator(".cblock code")).not.toContainText("#lt;");
  await diagram.getByRole("button", { name: "图形" }).click();
  await expect(diagram.locator(".md-diagram-svg")).toBeVisible();
  // A table's tools show under the pointer: Copy gives a sheet tabs, Expand shows it large until Escape.
  const table = md.locator(".md-table");
  const tools = table.locator(".md-table-tools");
  await expect.poll(() => tools.evaluate((el) => getComputedStyle(el).opacity)).toBe("0");
  await table.locator("table").hover();
  await expect.poll(() => tools.evaluate((el) => getComputedStyle(el).opacity)).toBe("1");
  await tools.getByRole("button", { name: "拷贝表格" }).click();
  // Windows' clipboard ends its lines with CRLF.
  await expect.poll(() => app.evaluate(({ clipboard }) => clipboard.readText()).then((text) => text.replace(/\r\n/g, "\n"))).toBe("指标\t本期\t上期\n收入\t123.45\t98.76");
  await tools.getByRole("button", { name: "展开" }).click();
  await expect(page.locator("dialog[open]")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.locator("dialog[open]")).toHaveCount(0);
  // A shell's block holding a character that does not show says so.
  const sh = md.locator(".cblock").filter({ hasText: "echo" });
  await expect(sh.locator(".cb-lang")).toHaveText("sh");
  await expect(sh.locator(".cb-warn")).toHaveText("含隐藏字符");
  await snap(page, "session-markdown-rich");
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
  const stop = page.getByTestId("composer-stop-btn");
  await expect(agent.locator(".md")).toHaveText("准备好了");
  await cli("session", "send", streaming.session.session_id, "--message", `stream ${reply}\nsleep 4s`);
  // Part of the reply shows while the turn runs, then all of it, still running.
  await expect(agent.last()).toContainText("最后一段说明");
  expect(await page.evaluate(() => (window as unknown as { __part: boolean }).__part)).toBe(true);
  await expect(stop).toBeVisible();
  await expect(log.getByRole("table")).toBeVisible();
  await expect(log.locator("pre code")).toHaveText("func main() {}");
  await log.locator(".cblock").evaluate((el) => ((el as unknown as { __kept: boolean }).__kept = true));
  await snap(page, "session-streaming");
  // The stored message replaces it when the turn ends: the same, not a second one, and its code block not drawn anew.
  await expect(stop).toHaveCount(0);
  await expect(agent).toHaveCount(2);
  expect(await log.locator(".cblock").evaluate((el) => (el as unknown as { __kept?: boolean }).__kept === true)).toBe(true);
  await expect(agent.last().locator("strong")).toHaveText("这一段很长很长的加粗文字会跨过好几次刷新");
  expect(await page.evaluate(() => (window as unknown as { __raw: string[] }).__raw)).toEqual([]);

  // Interrupted mid-reply: what was shown stays, as a message like any other.
  await open(page, "流式打断");
  await expect(agent).toHaveText("被打断的回复写到一半");
  await page.getByTestId("composer-stop-btn").click();
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
  test.skip(onWindows, "it stops the other machine by its process group");
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
  await expect(page.getByText(/^这个任务的内容保存在那个环境里，连上后会自动显示。.*还在线。$/)).toBeVisible();
  await snap(page, "session-machine-offline");
  // The overview says it as the iPhone does: offline, when it was last seen, no raw error.
  await page.getByRole("button", { name: /^概览/ }).click();
  const warn = page.locator(".ov-warn");
  await expect(warn).toContainText("work-mac 离线了，它上面的任务暂时看不到");
  await expect(warn).toContainText("还在线 · 连上后会自动补上");
  await app.close();
});
