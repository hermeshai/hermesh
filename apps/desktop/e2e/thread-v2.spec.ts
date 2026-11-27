import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { expect, test } from "@playwright/test";
import type { ElectronApplication, Page } from "@playwright/test";
import type { TrayView } from "../src/shared/bridge";
import { cli, fixture, launch, onWindows, pickFrom, snap } from "./app";

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
    const steps = conversation(page).locator(".tgroup, :not([data-testid=thread-workspace]) > .tool").first();
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

    // Launch a long-running turn (20s, ended by Interrupt below) in Auto Edits mode (2) so steer + queue edit + cancel + interrupt all execute deterministically.
    await pickMode(page, "runtime-mode-2");
    await expect.poll(async () => (await trayView(app)).state).toBe("ok");
    const runningBefore = (await trayView(app)).running;
    await page.getByTestId("thread-composer-input").fill("sleep 20s\nsay slow_turn_done");
    await page.getByTestId("composer-submit-btn").click();

    // While running, Stop sits beside Send; the menu bar counts it as running.
    await expect(page.getByTestId("composer-stop-btn")).toBeVisible({ timeout: 10_000 });
    await expect.poll(async () => (await trayView(app)).running).toBe(runningBefore + 1);

    // On a live-steer provider (MidTurnSteer = true), Enter / primary submit steers directly into the live timeline;
    // the explicit Queue button enqueues follow-ups for the next turn.
    await page.getByTestId("thread-composer-input").fill("Focus on test coverage");
    await page.getByTestId("composer-submit-btn").click();

    // Steered message appears immediately in the live timeline without sitting in the queue.
    await expect(conversation(page)).toContainText("Focus on test coverage");
    await expect(page.getByTestId("queued-runs-strip")).toHaveCount(0);

    // Enqueue a follow-up turn via the explicit Queue button while the active turn is still running, edit it, and cancel it!
    await page.getByTestId("thread-composer-input").fill("Draft follow-up 1");
    await page.getByTestId("composer-queue-btn").click();

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

test("on an ACP provider without native mid-turn steer, Steer Now promotes the chosen queued item, interrupts the turn, and runs it first", async () => {
  await cli(
    "session",
    "start",
    "--project",
    "demo",
    "--agent",
    "gemini",
    "--model",
    "gemini-3-pro",
    "--runtime-mode",
    "auto_edits",
    "--title",
    "ACP 引导测试",
    "--prompt",
    "read README.md\nsleep 20s",
  );
  const { app, page } = await launch();
  try {
    await page.locator(".side").getByText("ACP 引导测试").click();
    await expect(page.getByTestId("composer-stop-btn")).toBeVisible({ timeout: 10_000 });
    await expect(conversation(page).locator(".tgroup, .tool").first()).toBeVisible({ timeout: 10_000 });

    await page.getByTestId("thread-composer-input").fill("say acp_queued_second");
    await page.getByTestId("composer-submit-btn").click();
    await page.getByTestId("thread-composer-input").fill("say acp_steered_first");
    await page.getByTestId("composer-submit-btn").click();

    await expect(page.getByTestId("queued-run-0")).toContainText("acp_queued_second");
    await expect(page.getByTestId("queued-run-1")).toContainText("acp_steered_first");
    await page.getByTestId("queue-promote-1").click();

    await expect(replies(page).first()).toContainText("acp_steered_first", { timeout: 15_000 });
    await expect(replies(page).nth(1)).toContainText("acp_queued_second", { timeout: 15_000 });
    await expect(page.getByTestId("queued-runs-strip")).toHaveCount(0);
    await expect(page.getByTestId("composer-stop-btn")).toHaveCount(0, { timeout: 10_000 });

    // Also verify the direct composer Steer button (1-click interrupt & send on ACP without queueing first):
    await page.getByTestId("thread-composer-input").fill("sleep 20s");
    await page.getByTestId("composer-submit-btn").click();
    await expect(page.getByTestId("composer-stop-btn")).toBeVisible({ timeout: 10_000 });

    await page.getByTestId("thread-composer-input").fill("say acp_direct_steered");
    await expect(page.getByTestId("composer-steer-btn")).toBeVisible();
    await page.getByTestId("composer-steer-btn").click();
    await expect(replies(page).nth(2)).toContainText("acp_direct_steered", { timeout: 15_000 });
    await expect(page.getByTestId("composer-stop-btn")).toHaveCount(0, { timeout: 10_000 });
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
    const wakeLine = conversation(page).getByTestId("thread-wake-line");
    await expect(wakeLine).toContainText("pytest -q 已完成");
    await expect(bgLine).toHaveCount(0);
    await expect(page.getByTestId("toolbar-background-badge")).toHaveCount(0);
    await expect(page.getByTestId("toolbar-background-btn")).toBeVisible();
    await expect(page.getByText("这一轮做完了", { exact: false })).toHaveCount(0);
    await expect(bgPanel).toContainText("全部完成");

    // Clicking the finished task row opens its execution detail in-place; back returns to the list;
    // clicking the info toolbar button switches back to the session's info cards, and clicking the
    // inline "<task> 已完成 ›" wake line reopens that task's execution detail directly.
    await bgPanel.locator(".srow", { hasText: "pytest -q" }).click();
    await expect(bgPanel).toContainText("12 passed in 3s");
    await expect(bgPanel).toContainText("完成");
    await page.getByTestId("background-back").click();
    await expect(bgPanel).toContainText("全部完成");
    await page.getByRole("button", { name: "信息面板" }).click();
    await expect(bgPanel).toHaveCount(0);
    await expect(page.getByRole("complementary", { name: "任务信息" })).toBeVisible();
    await wakeLine.click();
    await expect(bgPanel).toBeVisible();
    await expect(bgPanel).toContainText("12 passed in 3s");
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

test("sniffs shell blocks, masks secrets, runs multiline scripts in an inline terminal drawer, and sends results to chat on demand", async () => {
  const { app, page } = await launch();
  try {
    await page.setViewportSize({ width: 1440, height: 820 });
    await page.getByTestId("nav-new-thread").click();
    await pickDemoAndScripted(page);
    await pickMode(page, "runtime-mode-4");

    // 1. Agent outputs a multi-line bash script (with shebang), a destructive rm -rf script, and a secret-bearing curl block.
    const replyMd = [
      "say Here are three blocks:",
      "say ```bash",
      "say #!/usr/bin/env bash",
      "say echo e2e_inline_line1",
      "say echo e2e_inline_line2",
      "say ```",
      "say ```bash",
      "say # clean temp dir",
      "say rm -rf ./tmp_e2e_nonexistent",
      "say echo cleaned_ok",
      "say ```",
      "say ```bash",
      'say curl -H "Authorization: Bearer sk-proj-1234567890abcdefghij" https://example.com',
      "say ```",
    ].join("\n");
    const input = page.getByTestId("thread-composer-input");
    await input.fill(replyMd);
    await page.getByTestId("composer-submit-btn").click();

    const blocks = conversation(page).locator(".cblock");
    await expect(blocks).toHaveCount(3, { timeout: 15_000 });
    const replyCountBefore = await replies(page).count();

    // Multi-line bash script with shebang exposes the Run button and keeps original script visible when clicked!
    const multiBlock = blocks.nth(0);
    await expect(multiBlock.getByTestId("codeblock-run-btn")).toBeVisible();
    await multiBlock.getByTestId("codeblock-run-btn").click();

    // Attached bottom terminal drawer appears inside the same CodeBlock, showing exit 0 + multi-line output, WITHOUT waking Agent!
    const drawer = multiBlock.getByTestId("codeblock-terminal-drawer");
    await expect(drawer).toBeVisible({ timeout: 10_000 });
    await expect(drawer).toContainText("退出码 0", { timeout: 10_000 });
    await expect(multiBlock.getByTestId("codeblock-term-body")).toContainText("e2e_inline_line1");
    await expect(multiBlock.getByTestId("codeblock-term-body")).toContainText("e2e_inline_line2");
    await expect(multiBlock.locator("pre").first()).toContainText("#!/usr/bin/env bash");
    expect(await replies(page).count()).toBe(replyCountBefore);

    // Folding and unfolding via the status bar toggle works cleanly.
    await multiBlock.getByTestId("codeblock-term-toggle").click();
    await expect(multiBlock.getByTestId("codeblock-term-body")).toHaveCount(0);
    await multiBlock.getByTestId("codeblock-term-toggle").click();
    await expect(multiBlock.getByTestId("codeblock-term-body")).toBeVisible();

    // Destructive `rm -rf` script shows inline confirmation bar before running.
    const rmBlock = blocks.nth(1);
    await rmBlock.getByTestId("codeblock-run-btn").click();
    await expect(rmBlock.getByTestId("codeblock-confirm-bar")).toBeVisible();
    await rmBlock.getByTestId("codeblock-confirm-run").click();
    await expect(rmBlock.getByTestId("codeblock-terminal-drawer")).toContainText("退出码 0", { timeout: 10_000 });
    await expect(rmBlock.getByTestId("codeblock-term-body")).toContainText("cleaned_ok");

    // Secret-bearing block masks the token by default and hides Run until revealed.
    const secretBlock = blocks.nth(2);
    await expect(secretBlock.locator("pre")).toContainText("••••••••");
    await expect(secretBlock.getByTestId("codeblock-run-btn")).toHaveCount(0);
    await secretBlock.getByTestId("codeblock-secret-toggle").click();
    await expect(secretBlock.getByTestId("codeblock-run-btn")).toBeVisible();

    // 2. Clicking "发入对话" sends the inline run result into the session and continues the conversation!
    const sendBtn = multiBlock.getByTestId("codeblock-send-to-chat");
    await expect(sendBtn).toContainText("发入对话");
    await sendBtn.click();
    await expect(sendBtn).toContainText("已发入对话");
    await expect(replies(page).last()).toContainText("Acknowledged: 运行结果（退出码 0）：", { timeout: 15_000 });

    // 3. Direct `! <cmd>` shortcut in the composer still runs with ANSI colours and wakes the agent.
    const ansiCmd = onWindows
      ? '! Write-Output "$([char]27)[32mansi_green_ok$([char]27)[0m"'
      : "! printf '\\033[32mansi_green_ok\\033[0m\\n'";
    await input.fill(ansiCmd);
    await expect(page.getByTestId("composer-shell-badge")).toBeVisible();
    await input.press("Enter");
    await expect(replies(page).last()).toContainText("Exit code: 0", { timeout: 15_000 });
  } finally {
    await app.close();
  }
});

test("remembers unsent composer text and attachments independently across new session, existing sessions, overview, and app restarts", async () => {
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-drafts-"));
  let { app, page } = await launch(fixture.socket, { profile });
  try {
    await page.setViewportSize({ width: 1280, height: 800 });

    // Create two distinct sessions: alpha and beta.
    await page.getByTestId("nav-new-thread").click();
    await pickDemoAndScripted(page);
    const input = () => page.getByTestId("thread-composer-input");
    const tiles = () => page.getByTestId("thread-composer").locator(".cmp-att .tile");

    await input().fill("say session_alpha_ready");
    await input().press("Enter");
    await expect(replies(page).first()).toContainText("session_alpha_ready", { timeout: 20_000 });

    await page.getByTestId("nav-new-thread").click();
    await expect(input()).toHaveValue("");
    await input().fill("say session_beta_ready");
    await input().press("Enter");
    await expect(replies(page).first()).toContainText("session_beta_ready", { timeout: 20_000 });

    const alphaRow = () => page.locator(".side-sessions [role=listitem]").filter({ hasText: "session_alpha_ready" });
    const betaRow = () => page.locator(".side-sessions [role=listitem]").filter({ hasText: "session_beta_ready" });
    await expect(alphaRow()).toBeVisible();
    await expect(betaRow()).toBeVisible();

    // Write a draft in Session Beta.
    await input().fill("say beta_unsent_followup");

    // Switch to Session Alpha: its composer is clean, and gets its own multi-line draft + image attachment.
    await alphaRow().click();
    await expect(input()).toHaveValue("");
    await expect(tiles()).toHaveCount(0);

    const pngBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==";
    const uploaded = page.waitForResponse((r) => r.url().endsWith("/UploadBlob"));
    await page.locator('.cmp input[type="file"]').setInputFiles({
      name: "alpha-draft.png",
      mimeType: "image/png",
      buffer: Buffer.from(pngBase64, "base64"),
    });
    await (await uploaded).finished();
    await input().fill("alpha draft line 1\nalpha draft line 2");
    await expect(tiles()).toHaveCount(1);
    await expect
      .poll(() => tiles().first().locator("img").evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0))
      .toBe(true);

    // Switch to New Session: starts empty, and keeps its own draft across project switches and project-group +.
    await page.getByTestId("nav-new-thread").click();
    await expect(input()).toHaveValue("");
    await expect(tiles()).toHaveCount(0);
    await input().fill("new session staging idea");
    await pickFrom(page.getByRole("button", { name: "项目" }), page.getByTestId("project-item-scratch"));
    await expect(input()).toHaveValue("new session staging idea");

    await page.getByRole("button", { name: "视图选项" }).click();
    await page.getByRole("menu", { name: "视图选项" }).getByRole("menuitem", { name: /分组/ }).hover();
    await page.getByRole("menu", { name: "分组" }).getByRole("menuitemradio", { name: "按项目" }).click();
    await page.keyboard.press("Escape");
    const demoGroup = page.locator(".side-sessions").getByRole("group", { name: "demo" });
    await demoGroup.hover();
    await demoGroup.getByRole("button", { name: "在 demo 里新建会话" }).click();
    await expect(input()).toHaveValue("new session staging idea");

    // Leave SessionPage completely (Overview) and verify all three drafts survive unmounting.
    await page.locator(".side-nav").getByRole("button", { name: /^概览/ }).click();
    await alphaRow().click();
    await expect(input()).toHaveValue("alpha draft line 1\nalpha draft line 2");
    await expect(tiles()).toHaveCount(1);
    await betaRow().click();
    await expect(input()).toHaveValue("say beta_unsent_followup");
    await expect(tiles()).toHaveCount(0);

    // Restart the app with the same profile: all three drafts and Alpha's image preview restore from storage.
    await app.close();
    ({ app, page } = await launch(fixture.socket, { profile }));
    await page.setViewportSize({ width: 1280, height: 800 });

    await alphaRow().click();
    await expect(input()).toHaveValue("alpha draft line 1\nalpha draft line 2");
    await expect(tiles()).toHaveCount(1);
    await expect
      .poll(() => tiles().first().locator("img").evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0))
      .toBe(true);

    await page.getByTestId("nav-new-thread").click();
    await expect(input()).toHaveValue("new session staging idea");

    // Sending in Beta clears only Beta's draft; Alpha and New Session keep theirs.
    await betaRow().click();
    await expect(input()).toHaveValue("say beta_unsent_followup");
    await input().press("Enter");
    await expect(input()).toHaveValue("");
    await expect(replies(page).last()).toContainText("beta_unsent_followup", { timeout: 20_000 });

    await alphaRow().click();
    await expect(input()).toHaveValue("alpha draft line 1\nalpha draft line 2");
    await expect(tiles()).toHaveCount(1);

    await page.getByTestId("nav-new-thread").click();
    await expect(input()).toHaveValue("new session staging idea");
  } finally {
    await app.close();
  }
});
