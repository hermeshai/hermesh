import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { launch } from "./app";

// The composer's line (docs/thread-composer.md): the project and model menus, and the permission mode menu.
async function pickDemoAndScripted(page: Page) {
  await page.getByRole("button", { name: "项目" }).click();
  await page.getByTestId("project-item-demo").click();
  await page.getByRole("button", { name: "模型" }).click();
  await page.getByTestId("picker-provider-scripted").click();
  await page.getByTestId("picker-model-scripted-pro").click();
  await page.keyboard.press("Escape");
}
async function pickMode(page: Page, testId: string) {
  await page.getByRole("button", { name: "权限模式" }).click();
  await page.getByTestId(testId).click();
}

// End-to-end verification of the Hermesh v2 Thread Workspace in the Desktop Electron app:
// 1. Native thread creation with ProviderInstance + ModelSelection + 4-level RuntimeMode
// 2. Live RuntimeRequest approval gate (DECLINE w/ feedback + ACCEPT_ONCE)
// 3. Unified Git diff inspector & non-destructive Git shadow-tree checkpoint file rewind
// 4. Plan mode -> structured PLAN_REVIEW card -> 1-click Approve Plan & auto-switch to Code mode
// 5. Mid-turn live STEER & durable follow-up QUEUE management (edit & cancel)

test("launches a supervised v2 thread, handles live tool approval, inspects git diff, and rewinds files to checkpoint 0", async () => {
  const { app, page } = await launch();
  try {
    const openStreams = () =>
      app.evaluate(() => (globalThis as unknown as { hermeshTest: { openUpstream(): number } }).hermeshTest.openUpstream());
    // Watch, and the thread list the window keeps for its sidebar (store.tsx useThreads).
    await expect.poll(openStreams).toBe(2);

    await page.getByTestId("nav-new-thread").click();
    await expect(page.getByTestId("thread-page")).toBeVisible();

    // Pick project "demo" and select the deterministic Scripted Test Agent + Supervised mode.
    await pickDemoAndScripted(page);
    // RuntimeMode.SUPERVISED == 1
    await pickMode(page, "runtime-mode-1");

    await page.getByTestId("thread-composer-input").fill("write ui_v2.txt|hello_from_v2_ui\nsay ui_turn_complete");
    await page.getByTestId("composer-submit-btn").click();

    // Live TOOL_APPROVAL banner appears for WriteFile in SUPERVISED mode.
    const approval = page.getByTestId("pending-approval-banner");
    await expect(approval).toBeVisible({ timeout: 15_000 });
    await expect(approval).toContainText("WriteFile");
    await expect(approval).toContainText("ui_v2.txt");

    await page.getByTestId("approve-once-btn").click();
    await expect(approval).toHaveCount(0);

    // Assistant turn completes and timeline records both file change and reply.
    await expect(page.getByTestId("turn-item-file")).toContainText("ui_v2.txt");
    await expect(page.getByTestId("turn-item-assistant")).toContainText("ui_turn_complete");

    // Inspector shows live unified Git diff (+1 -0) and checkpoints #0 and #1.
    const inspector = page.getByTestId("thread-inspector");
    await expect(inspector).toBeVisible();
    await expect(page.getByTestId("insp-tab-diff")).toContainText("(1)");
    await expect(inspector).toContainText("ui_v2.txt");
    await expect(inspector).toContainText("+hello_from_v2_ui");

    await page.getByTestId("insp-tab-checkpoints").click();
    await expect(page.getByTestId("insp-ckpt-0")).toBeVisible();
    await expect(page.getByTestId("insp-ckpt-1")).toBeVisible();

    // Rewind working tree + chat back to pre-turn Checkpoint #0 and verify diff is clean!
    await page.getByTestId("insp-rewind-files-0").click();
    await page.getByTestId("insp-tab-diff").click();
    await expect(page.getByTestId("insp-tab-diff")).toContainText("(0)");

    // Navigating back to Overview closes the thread's own subscription: Watch and the thread list stay.
    await page.locator(".side-nav").getByRole("button", { name: /^概览/ }).click();
    await expect.poll(openStreams).toBe(2);
  } finally {
    await app.close();
  }
});

test("plan mode proposes a structured plan, approves it in one click, and transitions to code mode", async () => {
  const { app, page } = await launch();
  try {
    await page.getByTestId("nav-new-thread").click();
    await pickDemoAndScripted(page);

    // Plan mode, from the permission mode menu.
    await pickMode(page, "mode-plan-btn");
    await expect(page.getByTestId("runtime-mode-chip")).toContainText("规划");

    await page.getByTestId("thread-composer-input").fill("propose-plan V2 Rollout|1. Kernel\n2. Desktop UI\nsay plan_approved_done");
    await page.getByTestId("composer-submit-btn").click();

    await expect(page.getByTestId("turn-item-plan")).toContainText("V2 Rollout", { timeout: 15_000 });
    await expect(page.getByTestId("pending-plan-banner")).toBeVisible();

    // Approve plan -> turn finishes AND interaction mode transitions automatically to Code!
    await page.getByTestId("approve-plan-btn").click();
    await expect(page.getByTestId("turn-item-assistant")).toContainText("plan_approved_done");
    await expect(page.getByTestId("runtime-mode-chip")).not.toContainText("规划");
  } finally {
    await app.close();
  }
});

test("while a turn runs, messages queue; one can steer the turn now, be edited or cancelled, and Stop ends the turn", async () => {
  const { app, page } = await launch();
  try {
    await page.getByTestId("nav-new-thread").click();
    await pickDemoAndScripted(page);

    // Launch a long-running turn (8s) in Auto Edits mode (2) so steer + queue edit + cancel + interrupt all execute deterministically.
    await pickMode(page, "runtime-mode-2");
    await page.getByTestId("thread-composer-input").fill("sleep 8s\nsay slow_turn_done");
    await page.getByTestId("composer-submit-btn").click();

    // While running, Stop sits beside Send.
    await expect(page.getByTestId("composer-stop-btn")).toBeVisible({ timeout: 10_000 });

    // A message sent while running waits for the next turn; the queue's "steer now" puts it into this one.
    await page.getByTestId("thread-composer-input").fill("Focus on test coverage");
    await page.getByTestId("composer-submit-btn").click();
    await expect(page.getByTestId("queued-run-0")).toContainText("Focus on test coverage");
    await page.getByTestId("queue-promote-0").click();

    // Steered message appears immediately in the live timeline.
    await expect(page.getByTestId("thread-timeline")).toContainText("Focus on test coverage");

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

    // Interrupt the running turn cleanly via the Stop button: Stop goes, Send stays.
    await page.getByTestId("composer-stop-btn").click();
    await expect(page.getByTestId("composer-stop-btn")).toHaveCount(0, { timeout: 10_000 });
    await expect(page.getByTestId("composer-submit-btn")).toBeVisible();
  } finally {
    await app.close();
  }
});
