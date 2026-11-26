import { expect, test } from "@playwright/test";
import type { ElectronApplication, Page } from "@playwright/test";
import type { TrayView } from "../src/shared/bridge";
import { launch, onWindows, pickFrom, snap } from "./app";

// The composer's line (docs/thread-composer.md): the project and model menus, and the permission mode menu.
async function pickDemoAndScripted(page: Page) {
  await pickFrom(page.getByRole("button", { name: "项目" }), page.getByTestId("project-item-demo"));
  // The model is in the box, once it is open; choosing one closes the panel.
  await page.getByTestId("thread-composer-input").click();
  await pickFrom(page.getByRole("button", { name: "模型" }), page.getByTestId("picker-provider-scripted").getByTestId("picker-model-scripted-pro"));
  await expect(page.getByTestId("model-traits-popover")).toBeHidden();
}
// What the menu bar counts (main/tray.ts): a session started here counts as any session does.
const trayView = (app: ElectronApplication) =>
  app.evaluate(() => (globalThis as unknown as { hermeshTest: { tray(): { view: TrayView } } }).hermeshTest.tray().view);

// The app menu (main/menu.ts): its Session and View items act on a session started here as on any.
const menuItem = (app: ElectronApplication, id: string) =>
  app.evaluate(({ Menu }, id) => {
    const i = Menu.getApplicationMenu()?.getMenuItemById(id);
    return i ? { enabled: i.enabled } : null;
  }, id);
const clickMenu = (app: ElectronApplication, id: string) =>
  app.evaluate(({ Menu }, id) => {
    Menu.getApplicationMenu()?.getMenuItemById(id)?.click();
  }, id);

// A session started here draws as any session does (session/Chat.tsx): its messages, its replies, its steps.
const conversation = (page: Page) => page.getByRole("log");
const replies = (page: Page) => conversation(page).locator(".am");
/** The permission mode: in the box's ＋ menu. */
async function pickMode(page: Page, testId: string) {
  await page.getByTestId("thread-composer-input").click();
  await page.getByTestId("composer-bar").getByRole("button", { name: "添加附件" }).click();
  await page.getByTestId(testId).click();
}

// End-to-end verification of the Hermesh v2 Thread Workspace in the Desktop Electron app:
// 1. Native thread creation with ProviderInstance + ModelSelection + 4-level RuntimeMode
// 2. Live RuntimeRequest approval gate (DECLINE w/ feedback + ACCEPT_ONCE)
// 3. The conversation and the info cards as a session's page has them
// 4. Plan mode -> structured PLAN_REVIEW card -> 1-click Approve Plan & auto-switch to Code mode
// 5. Mid-turn live STEER & durable follow-up QUEUE management (edit & cancel)

test("launches a supervised v2 thread, handles live tool approval, and shows it as a session's page", async () => {
  const { app, page } = await launch();
  try {
    // Wide enough for the info cards beside the conversation (a CI screen can be narrower than they need).
    await page.setViewportSize({ width: 1440, height: 800 });
    const openStreams = () =>
      app.evaluate(() => (globalThis as unknown as { hermeshTest: { openUpstream(): number } }).hermeshTest.openUpstream());
    // Unified Watch stream keeps the window's session list and status live.
    await expect.poll(openStreams).toBe(1);

    await page.getByTestId("nav-new-thread").click();
    await expect(page.getByTestId("thread-page")).toBeVisible();

    // Pick project "demo" and select the deterministic Scripted Test Agent + Supervised mode.
    await pickDemoAndScripted(page);
    // RuntimeMode.SUPERVISED == 1
    await pickMode(page, "runtime-mode-1");

    await expect.poll(async () => (await trayView(app)).state).toBe("ok");
    const waitingBefore = (await trayView(app)).attention;
    await page.getByTestId("thread-composer-input").fill("write ui_v2.txt|hello_from_v2_ui\nsay ui_turn_complete");
    await page.getByTestId("composer-submit-btn").click();

    // Live TOOL_APPROVAL banner appears for WriteFile in SUPERVISED mode.
    const approval = page.getByTestId("pending-approval-banner");
    await expect(approval).toBeVisible({ timeout: 15_000 });
    await expect(approval).toContainText("WriteFile");
    await expect(approval).toContainText("ui_v2.txt");
    await snap(page, "thread-approval");
    // Waiting for the owner, it needs attention in the menu bar, as a session asking does.
    await expect.poll(async () => (await trayView(app)).attention).toBe(waitingBefore + 1);

    await page.getByTestId("approve-once-btn").click();
    await expect(approval).toHaveCount(0);
    await expect.poll(async () => (await trayView(app)).attention).toBe(waitingBefore);

    // The conversation as a session's: the message on the right, the reply across, the steps as one folded line.
    await expect(conversation(page).locator(".um .bub")).toContainText("write ui_v2.txt");
    await expect(replies(page)).toContainText("ui_turn_complete");
    const steps = conversation(page).locator(".tgroup, .tool").first();
    await steps.locator("button").first().click();
    await expect(conversation(page)).toContainText("ui_v2.txt");
    await expect(conversation(page)).toContainText("+hello_from_v2_ui");
    // The owner sent it: a soft cool-grey bubble with graphite words, nothing on its corner. A turn that went well ends without a line of its own.
    await expect(conversation(page).locator(".um").first()).not.toHaveClass(/\bfrom\b/);
    expect(await conversation(page).locator(".um .bub").first().evaluate((el) => [getComputedStyle(el).backgroundColor, getComputedStyle(el).color])).toEqual([
      "rgb(231, 235, 242)",
      "rgb(32, 33, 39)",
    ]);
    await expect(conversation(page).getByTestId("prompt-sender")).toHaveCount(0);
    await expect(page.getByText("这一轮做完了", { exact: false })).toHaveCount(0);

    // Its info cards, as a session's: who started it, the agent, this Mac, the project; then the model.
    const env = page.getByRole("complementary", { name: "任务信息" });
    await expect(env).toContainText("你 发起");
    await expect(env).toContainText("这台 Mac");
    await expect(env).toContainText("demo");
    await expect(page.getByRole("complementary", { name: "上下文" })).toContainText("Scripted Pro");
    await snap(page, "thread-session");
    // Nothing else beside it: no diff, checkpoint or runtime tabs.
    await expect(page.getByText("代码变更")).toHaveCount(0);
    await expect(page.getByText("检查点")).toHaveCount(0);
    // The toolbar's button hides and shows them, as on a session's page.
    await page.getByRole("button", { name: "信息面板" }).click();
    await expect(env).toHaveCount(0);
    await page.getByRole("button", { name: "信息面板" }).click();
    await expect(env).toBeVisible();
    // ⌥⌘0 (View › the info), as on a session's page.
    await clickMenu(app, "toggleInspector");
    await expect(env).toHaveCount(0);
    await clickMenu(app, "toggleInspector");
    await expect(env).toBeVisible();

    // Archive (⌘E): read-only, said at the top with Unarchive, and no box, as an archived session is.
    await expect.poll(() => menuItem(app, "archive")).toMatchObject({ enabled: true });
    await clickMenu(app, "archive");
    const note = page.locator(".anote");
    await expect(note).toContainText("已归档");
    await expect(page.getByTestId("thread-composer-input")).toHaveCount(0);
    await expect.poll(() => menuItem(app, "unarchive")).toMatchObject({ enabled: true });
    await note.getByRole("button", { name: "取消归档" }).click();
    await expect(note).toHaveCount(0);
    await expect(page.getByTestId("thread-composer-input")).toBeVisible();

    // Navigating back to Overview closes the session's own subscription: Watch stays.
    await page.locator(".side-nav").getByRole("button", { name: /^概览/ }).click();
    await expect.poll(openStreams).toBe(1);
  } finally {
    await app.close();
  }
});

test("while a turn runs, messages queue; one can steer the turn now, be edited or cancelled, and the app menu's Interrupt ends the turn", async () => {
  const { app, page } = await launch();
  try {
    await page.getByTestId("nav-new-thread").click();
    await pickDemoAndScripted(page);

    // Launch a long-running turn (8s) in Auto Edits mode (2) so steer + queue edit + cancel + interrupt all execute deterministically.
    await pickMode(page, "runtime-mode-2");
    await expect.poll(async () => (await trayView(app)).state).toBe("ok");
    const runningBefore = (await trayView(app)).running;
    await page.getByTestId("thread-composer-input").fill("sleep 8s\nsay slow_turn_done");
    await page.getByTestId("composer-submit-btn").click();

    // While running, Stop sits beside Send; the menu bar counts it as running.
    await expect(page.getByTestId("composer-stop-btn")).toBeVisible({ timeout: 10_000 });
    await expect.poll(async () => (await trayView(app)).running).toBe(runningBefore + 1);

    // A message sent while running waits for the next turn; the queue's "steer now" puts it into this one.
    await page.getByTestId("thread-composer-input").fill("Focus on test coverage");
    await page.getByTestId("composer-submit-btn").click();
    await expect(page.getByTestId("queued-run-0")).toContainText("Focus on test coverage");
    await page.getByTestId("queue-promote-0").click();

    // Steered message appears immediately in the live timeline.
    await expect(conversation(page)).toContainText("Focus on test coverage");

    // Enqueue a follow-up turn while the active turn is still running, edit it, and cancel it!
    await page.getByTestId("thread-composer-input").fill("Draft follow-up 1");
    await page.getByTestId("composer-submit-btn").click();

    const strip = page.getByTestId("queued-runs-strip");
    await expect(strip).toBeVisible();
    await expect(page.getByTestId("queued-run-0")).toContainText("Draft follow-up 1");

    await page.getByTestId("queue-edit-0").click();
    await page.getByTestId("queue-edit-input").fill("Edited follow-up 1");
    await page.getByTestId("queue-edit-save").click();
    await expect(page.getByTestId("queued-run-0")).toContainText("Edited follow-up 1");

    await page.getByTestId("queue-cancel-0").click();
    await expect(strip).toHaveCount(0);

    // Interrupt the running turn from the app menu (⌘.), as a session's: Stop goes, Send stays.
    await expect.poll(() => menuItem(app, "interrupt")).toMatchObject({ enabled: true });
    await clickMenu(app, "interrupt");
    await expect(page.getByTestId("composer-stop-btn")).toHaveCount(0, { timeout: 10_000 });
    await expect(page.getByTestId("composer-submit-btn")).toBeVisible();
  } finally {
    await app.close();
  }
});

test("tracks post-turn background tasks without premature completion, streams automatic continuation wakeup, and stops daemons from the UI", async () => {
  const { app, page } = await launch();
  try {
    // Wide enough for the info cards beside the conversation (a CI screen can be narrower than they need).
    await page.setViewportSize({ width: 1440, height: 800 });
    await page.getByTestId("nav-new-thread").click();
    await pickDemoAndScripted(page);
    // RuntimeMode.FULL_ACCESS == 4 so shell commands run without approval pause.
    await pickMode(page, "runtime-mode-4");

    await page
      .getByTestId("thread-composer-input")
      .fill("bg-shell 3s|pytest -q|12 passed in 3s|All 12 background tests passed!\nsay Foreground reply done, running pytest in background");
    await page.getByTestId("composer-submit-btn").click();

    // 1. Foreground reply finishes with no floating strip above the composer: instead, the toolbar layers
    //    button shows the running badge and the conversation's end shows a link that opens the independent
    //    right-side background panel in place of the info cards.
    await expect(replies(page)).toContainText("Foreground reply done, running pytest in background", { timeout: 10_000 });
    await expect(page.getByTestId("background-tasks-strip")).toHaveCount(0);
    await expect(page.getByTestId("toolbar-background-badge")).toHaveText("1");
    await expect(page.locator(".dbody .livebar")).toHaveCount(0);
    const bgLine = page.getByTestId("chat-background-line");
    await expect(bgLine).toContainText("有 1 条后台任务正在执行");
    await expect(page.getByRole("complementary", { name: "任务信息" })).toBeVisible();
    await expect(page.getByTestId("background-panel")).toHaveCount(0);

    await bgLine.click();
    const bgPanel = page.getByTestId("background-panel");
    await expect(bgPanel).toBeVisible();
    await expect(page.getByRole("complementary", { name: "任务信息" })).toHaveCount(0);
    await expect(bgPanel).toContainText("1 个执行中");
    await expect(bgPanel).toContainText("pytest -q");

    // 2. When the 3s background task finishes, the system Continuation Run streams the final report
    //    seamlessly without any special wake divider, clears the active badge and end-of-conversation line,
    //    and updates the panel to "全部完成" while keeping the toolbar button so finished tasks can still be inspected.
    await expect(replies(page).last()).toContainText("All 12 background tests passed!", { timeout: 15_000 });
    await expect(page.getByText("后台唤醒", { exact: false })).toHaveCount(0);
    await expect(bgLine).toHaveCount(0);
    await expect(page.getByTestId("toolbar-background-badge")).toHaveCount(0);
    await expect(page.getByTestId("toolbar-background-btn")).toBeVisible();
    await expect(page.getByText("这一轮做完了", { exact: false })).toHaveCount(0);
    await expect(bgPanel).toContainText("全部完成");

    // Clicking the finished task row opens its execution detail in-place; back returns to the list;
    // clicking the info toolbar button switches back to the session's info cards.
    await bgPanel.locator(".srow", { hasText: "pytest -q" }).click();
    await expect(bgPanel).toContainText("12 passed in 3s");
    await expect(bgPanel).toContainText("完成");
    await page.getByTestId("background-back").click();
    await expect(bgPanel).toContainText("全部完成");
    await page.getByRole("button", { name: "信息面板" }).click();
    await expect(bgPanel).toHaveCount(0);
    await expect(page.getByRole("complementary", { name: "任务信息" })).toBeVisible();

    // 3. Long-lived daemon (`IsDaemon=true` / `bg-daemon`): opens in the independent right panel,
    //    shows live output in its detail view, and stops cleanly from its stop button!
    await page
      .getByTestId("thread-composer-input")
      .fill("bg-daemon npm run dev|ready on http://localhost:3000\nsay Dev server started");
    await page.getByTestId("composer-submit-btn").click();

    await expect(replies(page).last()).toContainText("Dev server started", { timeout: 10_000 });
    await expect(page.getByTestId("toolbar-background-badge")).toHaveText("1");
    await expect(bgLine).toContainText("有 1 条后台任务正在执行");

    await page.getByTestId("toolbar-background-btn").click();
    await expect(bgPanel).toContainText("1 个执行中 · 1 个已完成");
    await bgPanel.locator(".srow", { hasText: "npm run dev" }).click();
    await expect(bgPanel).toContainText("常驻服务");
    await expect(bgPanel).toContainText("ready on http://localhost:3000");

    await bgPanel.getByRole("button", { name: "停止" }).click();
    await expect(page.getByTestId("toolbar-background-badge")).toHaveCount(0, { timeout: 10_000 });
    await expect(bgLine).toHaveCount(0);
    await expect(bgPanel).toContainText("已停止");
    await page.getByTestId("background-back").click();
    await expect(bgPanel).toContainText("1 个已完成 · 1 个已停止");
  } finally {
    await app.close();
  }
});

test("sending a message while scrolled up in a session automatically scrolls back to the bottom", async () => {
  const { app, page } = await launch();
  try {
    await page.setViewportSize({ width: 1280, height: 720 });
    await page.getByTestId("nav-new-thread").click();
    await pickDemoAndScripted(page);
    await pickMode(page, "runtime-mode-4");

    const lines = Array.from({ length: 45 }, (_, i) => `say line_${i + 1}_padding_for_long_scroll_history`).join("\n");
    const input = page.getByTestId("thread-composer-input");
    await input.fill(`${lines}\nsay initial_long_turn_done`);
    await page.getByTestId("composer-submit-btn").click();
    await expect(replies(page).last()).toContainText("initial_long_turn_done", { timeout: 20_000 });

    const scroller = page.locator(".cscroll.markdown-chat");
    const below = () => scroller.evaluate((el) => Math.round(el.scrollHeight - el.scrollTop - el.clientHeight));
    await expect.poll(below).toBeLessThan(80);

    // Scroll well up so the "to bottom" button appears and the view is detached from the bottom.
    const box = (await scroller.boundingBox())!;
    await page.mouse.move(box.x + 24, box.y + box.height / 2);
    for (let i = 0; i < 8; i++) await page.mouse.wheel(0, -300);
    await expect(page.locator(".tobottom.on")).toBeVisible();
    expect(await below()).toBeGreaterThan(240);

    // Typing in the composer keeps the scrolled-up position until Send.
    await input.fill("say followup_from_scrolled_up");
    await expect(page.locator(".tobottom.on")).toBeVisible();

    // Sending immediately re-attaches to the bottom and keeps the new message + reply in view.
    await input.press("Enter");
    await expect(page.locator(".tobottom.on")).toHaveCount(0);
    await expect.poll(below).toBeLessThan(80);
    await expect(replies(page).last()).toContainText("followup_from_scrolled_up", { timeout: 15_000 });
    await expect.poll(below).toBeLessThan(80);
  } finally {
    await app.close();
  }
});

test("sniffs shell blocks, masks secrets, runs codeblock and ! shell shortcuts in the session worktree with ANSI output, and wakes the agent", async () => {
  const { app, page } = await launch();
  try {
    await page.setViewportSize({ width: 1440, height: 820 });
    await page.getByTestId("nav-new-thread").click();
    await pickDemoAndScripted(page);
    await pickMode(page, "runtime-mode-4");

    // 1. Agent outputs an untagged CLI block (`$ echo ...`) and a bash block with an API key.
    const replyMd = [
      "say Here are two commands:",
      "say ```",
      "say $ echo e2e_codeblock_run_ok",
      "say ```",
      "say ```bash",
      'say curl -H "Authorization: Bearer sk-proj-1234567890abcdefghij" https://example.com',
      "say ```",
    ].join("\n");
    const input = page.getByTestId("thread-composer-input");
    await input.fill(replyMd);
    await page.getByTestId("composer-submit-btn").click();

    const blocks = conversation(page).locator(".cblock");
    await expect(blocks).toHaveCount(2, { timeout: 15_000 });

    // Untagged `$ echo ...` fence is auto-sniffed as `bash` and exposes the 1-click Run button.
    const firstBlock = blocks.nth(0);
    await expect(firstBlock.locator(".cb-lang")).toContainText("bash");
    await expect(firstBlock.getByTestId("codeblock-run-btn")).toBeVisible();

    // Secret-bearing block masks the token by default and hides Run until revealed.
    const secretBlock = blocks.nth(1);
    await expect(secretBlock.locator("pre")).toContainText("••••••••");
    await expect(secretBlock.locator("pre")).not.toContainText("1234567890abcdefghij");
    await expect(secretBlock.getByTestId("codeblock-run-btn")).toHaveCount(0);

    await secretBlock.getByTestId("codeblock-secret-toggle").click();
    await expect(secretBlock.locator("pre")).toContainText("sk-proj-1234567890abcdefghij");
    await expect(secretBlock.getByTestId("codeblock-run-btn")).toBeVisible();

    // 2. Click Run on the first block: strips `$ `, runs in the session worktree, and wakes the agent.
    await firstBlock.getByTestId("codeblock-run-btn").click();
    await expect(replies(page).last()).toContainText("Reviewed echo e2e_codeblock_run_ok", { timeout: 15_000 });
    await expect(replies(page).last()).toContainText("Exit code: 0");

    // 3. Type a `! <cmd>` shell shortcut in the composer: shows shell badge, renders ANSI colours, and Inspector reflects worktree path.
    const ansiCmd = onWindows
      ? '! Write-Output "$([char]27)[32mansi_green_ok$([char]27)[0m"'
      : "! printf '\\033[32mansi_green_ok\\033[0m\\n'";
    await input.fill(ansiCmd);
    await expect(page.getByTestId("composer-shell-badge")).toBeVisible();
    await input.press("Enter");

    await expect(replies(page).last()).toContainText("Exit code: 0", { timeout: 15_000 });

    // Expand the latest tool step group/row and verify `.ansi-green` span rendered inside `.term-out`.
    const lastTool = conversation(page).locator(".tgroup, .tool").last();
    await lastTool.locator("button").first().click();
    if ((await lastTool.locator(".tool:not(.open) .th").count()) > 0) {
      await lastTool.locator(".tool:not(.open) .th").last().click();
    }
    await expect(conversation(page).locator(".term-out .ansi-green").last()).toContainText("ansi_green_ok");

    // Inspector's project button points to the isolated worktrees/<sessionId> path, not just the base repo.
    const envCard = page.getByRole("complementary", { name: "任务信息" });
    const projBtnTitle = await envCard.locator(".insp-pick").first().getAttribute("title");
    expect(projBtnTitle ?? "").toContain("worktrees");
  } finally {
    await app.close();
  }
});
