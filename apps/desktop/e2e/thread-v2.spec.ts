import { expect, test } from "@playwright/test";
import type { ElectronApplication, Page } from "@playwright/test";
import type { TrayView } from "../src/shared/bridge";
import { launch, pickFrom, snap } from "./app";

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

    // 1. Foreground reply finishes, while background task pill + toolbar badge + Inspector card appear,
    //    and the conversation's end says it waits on the background task.
    await expect(replies(page)).toContainText("Foreground reply done, running pytest in background", { timeout: 10_000 });
    const bgStrip = page.getByTestId("background-tasks-strip");
    await expect(bgStrip).toBeVisible();
    await expect(bgStrip).toContainText("pytest -q");
    await expect(page.getByTestId("toolbar-background-badge")).toBeVisible();
    await expect(page.locator(".dbody .livebar")).toHaveCount(0);
    await expect(page.getByTestId("thread-working")).toContainText("正在等待 1 个后台任务完成后继续汇报");
    await expect(page.getByTestId("inspector-background-tasks")).toContainText("pytest -q");

    // 2. When the 3s background task finishes, the system Continuation Run streams the final report
    //    under a quiet wake divider, clears the active background strip, and the waiting line goes: a turn that
    //    went well ends without a line of its own.
    await expect(replies(page).last()).toContainText("All 12 background tests passed!", { timeout: 15_000 });
    await expect(page.getByTestId("turn-wake-line")).toContainText("后台唤醒 · pytest -q");
    await expect(bgStrip).toHaveCount(0);
    await expect(page.getByTestId("thread-working")).toHaveCount(0);
    await expect(page.getByText("这一轮做完了", { exact: false })).toHaveCount(0);

    // 3. Long-lived daemon (`IsDaemon=true` / `bg-daemon`): does NOT keep the conversation's end waiting,
    //    shows live output in Inspector when clicking "查看", and stops cleanly when clicking "停止"!
    await page
      .getByTestId("thread-composer-input")
      .fill("bg-daemon npm run dev|ready on http://localhost:3000\nsay Dev server started");
    await page.getByTestId("composer-submit-btn").click();

    await expect(replies(page).last()).toContainText("Dev server started", { timeout: 10_000 });
    await expect(page.getByTestId("thread-working")).toHaveCount(0);
    await expect(bgStrip).toContainText("常驻进程运行中");

    await page.getByTestId("background-strip-view").click();
    await expect(page.getByTestId("inspector-background-tasks")).toContainText("ready on http://localhost:3000");

    await page.getByTestId("background-strip-stop").click();
    await expect(bgStrip).toHaveCount(0, { timeout: 10_000 });
    await expect(page.getByTestId("inspector-background-tasks")).toContainText("已停止");
  } finally {
    await app.close();
  }
});
