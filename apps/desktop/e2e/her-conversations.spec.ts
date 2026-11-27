import { expect, test, type Page } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fixture, launch, navTo, sessionList, snap } from "./app";

// Conversations with Her besides her main one (docs/her-conversations.md), as the owner uses them, against her
// itself with her-mock as its model. Run by TestDesktopHerConversations, which needs her built. One story: Her is
// turned on here; a new session goes to Agent the first time; the owner gives it to Her instead (the small cat, then
// ⌘N back and forth), her models first in the model panel; the conversation opens on her own chat, where she asks
// before she starts a coding agent; the session she starts nests under the conversation in the task list, which
// shows the whole tree in progress, folds and unfolds, can't be archived while the child runs, and stops nesting when
// the view says so; the child's news comes back to this conversation, not her main one; and the next new session,
// after the app restarts too, goes to Her again.

type Listed = { sessionId: string; agent: string; title: string; lineage?: { parentSessionId?: string; relationship?: string } };

const listed = (page: Page) =>
  page.evaluate(async () => {
    const r = await fetch("app://hermesh/api/hermesh.local.v1.LocalService/ListSessions", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ limit: 50 }) });
    return (await r.json()).sessions as Listed[];
  });

test("Her conversations: given to Her from a new session, the work she sends nested under it, its news back in it", async () => {
  test.setTimeout(400_000);
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-"));
  let { app, page } = await launch(fixture.socket, { profile });
  await page.setViewportSize({ width: 1440, height: 860 });
  const nav = page.locator(".side-nav");

  // Her on, on this Mac.
  await nav.getByRole("button", { name: /^Her/ }).click();
  const choices = page.getByRole("list", { name: "选择 Her 在哪里运行" });
  await choices.getByRole("listitem").filter({ hasText: "这台 Mac" }).getByRole("button", { name: "开启 Her" }).click({ timeout: 60_000 });
  await expect(page.getByText(/^你好，我是 .+，在 .+ 上为你做事/)).toBeVisible({ timeout: 120_000 });

  // A new session goes to Agent the first time, Her small on the right.
  const newSession = async () => {
    await page.getByTestId("nav-new-thread").click();
    await expect(page.getByTestId("presence")).toBeVisible();
  };
  await newSession();
  const presence = page.getByTestId("presence");
  const agent = presence.getByRole("button", { name: "Agent" });
  const her = presence.getByRole("button", { name: "Her" });
  await expect(agent).toHaveAttribute("aria-pressed", "true");
  await expect(her).toHaveAttribute("title", "交给 Her · Tab");
  await expect(page.getByTestId("thread-page")).toHaveAttribute("data-who", "agent");
  // Tilted provider tiles use alpha-following drop-shadow rather than a rectangular box-shadow around the PNG corners.
  const tileStyles = await agent.locator(".pr-tiles > *").evaluateAll((els) =>
    els.map((el) => {
      const cs = getComputedStyle(el);
      return { boxShadow: cs.boxShadow, filter: cs.filter };
    }),
  );
  expect(tileStyles).toHaveLength(2);
  for (const s of tileStyles) {
    expect(s.boxShadow).toBe("none");
    expect(s.filter).toContain("drop-shadow");
  }
  await expect(page.getByRole("heading", { level: 1 })).toContainText("里做什么？");
  // On Agent, the model panel shows only coding agent providers (no Her).
  await page.getByTestId("model-picker-trigger").click();
  const agentPanel = page.getByTestId("model-traits-popover");
  await expect(agentPanel.locator("[data-testid^=picker-provider-]").first()).toBeVisible();
  await expect(agentPanel.getByTestId("picker-provider-her")).toHaveCount(0);
  await page.keyboard.press("Escape");
  await snap(page, "her-conv-new-agent");

  // The small cat gives it to Her: her question, where she is, her model; ⌘N back to Agent and again to Her.
  await her.click();
  await expect(her).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("要 Her 做什么？");
  await expect(page.getByTestId("launch-her-where")).toHaveText(/^Her 在 /);
  const box = page.getByTestId("thread-launch-card").getByRole("textbox");
  await expect(box).toHaveAttribute("placeholder", "发消息给 Her");
  await page.keyboard.press("Meta+N");
  await expect(agent).toHaveAttribute("aria-pressed", "true");
  // Empty box: Tab toggles, and → / ← pick Her / Agent directionally.
  await page.keyboard.press("Tab");
  await expect(her).toHaveAttribute("aria-pressed", "true");
  await page.keyboard.press("ArrowLeft");
  await expect(agent).toHaveAttribute("aria-pressed", "true");
  await page.keyboard.press("ArrowRight");
  await expect(her).toHaveAttribute("aria-pressed", "true");
  // With text in the box: ← moves the caret without switching, while ⇧Tab switches and keeps the draft.
  await box.fill("保留草稿");
  await page.keyboard.press("ArrowLeft");
  await expect(her).toHaveAttribute("aria-pressed", "true");
  await page.keyboard.press("Shift+Tab");
  await expect(agent).toHaveAttribute("aria-pressed", "true");
  await expect(box).toHaveValue("保留草稿");
  await page.keyboard.press("Shift+Tab");
  await expect(her).toHaveAttribute("aria-pressed", "true");
  await expect(box).toHaveValue("保留草稿");
  await box.fill("");
  await snap(page, "her-conv-new-her");

  // On Her, the model panel shows only Her's models (no coding agent providers), her default marked.
  await page.getByTestId("model-picker-trigger").click();
  const panel = page.getByTestId("model-traits-popover");
  const groups = panel.locator("[data-testid^=picker-provider-]");
  await expect(groups).toHaveCount(1);
  await expect(groups.first()).toHaveAttribute("data-testid", "picker-provider-her");
  await expect(panel.getByTestId("picker-provider-her")).toContainText("默认");
  await page.keyboard.press("Escape");

  // Sent: the conversation opens on Her's chat under the same Her header (avatar + team capsule + Progress) as entering Her.
  await box.fill(`[[mock:mesh:session_start:${JSON.stringify({ project: "owner-mac/demo", prompt: "sleep 25s\nsay 做完了", title: "对话派的活" })}]] 开工`);
  await box.press("Enter");
  await expect(page.getByTestId("her-avatar")).toBeVisible({ timeout: 60_000 });
  await expect(page.getByRole("button", { name: "团队" })).toBeVisible();
  await expect(page.getByTestId("her-progress")).toBeVisible();
  const log = page.getByRole("log", { name: "对话" });
  const approval = page.getByRole("region", { name: "等你批准" });
  await expect(approval).toContainText("owner-mac/demo", { timeout: 60_000 });
  await snap(page, "her-conv-approval");
  await approval.getByRole("button", { name: /^允许/ }).click();
  await expect(log.getByRole("button", { name: "对话派的活" })).toBeVisible({ timeout: 60_000 });

  // In the task list: the conversation, and the work it sent under it.
  let conv = "";
  let child = "";
  await expect
    .poll(async () => {
      const all = await listed(page);
      conv = all.find((s) => s.agent === "her")?.sessionId ?? "";
      child = all.find((s) => s.lineage?.relationship === "her" && s.lineage.parentSessionId === conv)?.sessionId ?? "";
      return !!conv && !!child;
    }, { timeout: 60_000 })
    .toBe(true);
  const list = await sessionList(page);
  const tree = list.locator(".stree").filter({ has: page.locator(`[data-session-id="${conv}"]`) });
  const convRow = tree.locator(`[data-session-id="${conv}"]`);
  const childRow = tree.locator(`.kids [data-session-id="${child}"]`);
  await expect(childRow).toBeVisible({ timeout: 30_000 });
  await expect(childRow).toContainText("对话派的活");
  // The whole tree is in progress while the child works, and the parent row itself shows running orbit + shimmer.
  await expect(list.getByRole("group", { name: "进行中" }).locator(`[data-session-id="${conv}"]`)).toBeVisible();
  await expect(convRow.locator(".slogo.run")).toBeVisible();
  await expect(convRow.locator(".stitle.run")).toBeVisible();
  await snap(page, "her-conv-tree");

  // Folded and unfolded: parent row stays visibly running while folded too.
  await convRow.getByRole("button", { name: "收起" }).click();
  await expect(childRow).toBeHidden();
  await expect(convRow.locator(".slogo.run")).toBeVisible();
  await convRow.getByRole("button", { name: "展开" }).click();
  await expect(childRow).toBeVisible();

  // Overview also keeps the Her conversation under In Progress (never prematurely in Just Finished) while its child runs.
  await navTo(page, /^概览/);
  await expect(page.getByRole("region", { name: "进行中" }).getByRole("button", { name: /对话派的活/ })).toBeVisible();
  await expect(page.getByRole("region", { name: "进行中" }).getByRole("button").filter({ hasText: "你 → Her" })).toBeVisible();

  // Not archived while what it sent still runs; the work under it has no pin of its own.
  await convRow.hover();
  await expect(convRow.getByRole("button", { name: "归档" })).toHaveCount(0);
  await convRow.getByRole("button", { name: "更多" }).click();
  const rowMenu = page.getByRole("menu", { name: "更多" });
  await expect(rowMenu.getByRole("menuitem", { name: "归档" })).toBeDisabled();
  await expect(rowMenu.getByRole("menuitem", { name: "归档" })).toHaveAttribute("title", "还有任务在进行");
  await page.keyboard.press("Escape");
  await childRow.hover();
  await childRow.getByRole("button", { name: "更多" }).click();
  await expect(page.getByRole("menu", { name: "更多" }).getByRole("menuitem", { name: "置顶" })).toHaveCount(0);
  await page.keyboard.press("Escape");

  // By project, Her's conversations are a group of their own, right at the top.
  const view = async (pane: RegExp, menu: string, item: string, role: "menuitemradio" | "menuitemcheckbox") => {
    await page.getByRole("button", { name: "视图选项" }).click();
    await page.getByRole("menu", { name: "视图选项" }).getByRole("menuitem", { name: pane }).hover();
    await page.getByRole("menu", { name: menu }).getByRole(role, { name: item }).click();
  };
  await view(/分组/, "分组", "按项目", "menuitemradio");
  await expect(list.getByRole("group").first()).toHaveAttribute("aria-label", "Her");
  await expect(list.getByRole("group", { name: "Her", exact: true }).locator(`.kids [data-session-id="${child}"]`)).toBeVisible();
  await snap(page, "her-conv-by-project");

  // Nesting off: the child goes back to its own project, and by status the parent still stays in 进行中 (not 已结束).
  await view(/显示/, "显示", "嵌套子任务", "menuitemcheckbox");
  await expect(list.locator(".stree")).toHaveCount(0);
  await expect(list.getByRole("group", { name: "demo", exact: true }).locator(`[data-session-id="${child}"]`)).toBeVisible();
  await view(/分组/, "分组", "按状态", "menuitemradio");
  await expect(list.getByRole("group", { name: "进行中" }).locator(`[data-session-id="${conv}"]`)).toBeVisible();
  await view(/显示/, "显示", "嵌套子任务", "menuitemcheckbox");
  await expect(childRow).toBeVisible();

  // The child's news comes back here, and her main conversation never hears of it.
  await convRow.click();
  await expect(log.locator(".bm").filter({ hasText: "turn_done" })).toBeVisible({ timeout: 120_000 });
  await navTo(page, /^Her/);
  await expect(page.getByText(/^你好，我是 .+，在 .+ 上为你做事/)).toBeVisible();
  await expect(page.getByRole("log", { name: "对话" })).toHaveCount(0);

  // The next new session goes to Her again, and after the app restarts too.
  await newSession();
  await expect(her).toHaveAttribute("aria-pressed", "true");
  await app.close();
  ({ app, page } = await launch(fixture.socket, { profile }));
  await page.getByTestId("nav-new-thread").click();
  await expect(page.getByTestId("presence").getByRole("button", { name: "Her" })).toHaveAttribute("aria-pressed", "true");
  await app.close();
});
