import { expect, test } from "@playwright/test";
import { fixture, launch, navTo, snap } from "./app";

// Progress (docs/her-team.md §15), as the owner uses it, against her itself with her-mock as its model. Run by
// TestDesktopHerProgress, which needs her built. One story: Her is turned on here; nothing is sent out yet; she
// hands a piece of work to a sub-agent, whose card shows under what the owner said, asks for the owner's
// approval, works and ends; its record shows what Her asked, what it called and what it reported; then a coding
// session she starts, as a card whose record is the session's own.

test("Progress: what Her sent out as cards, a sub-agent's record, and a coding session's", async () => {
  test.setTimeout(300_000);
  const { app, page } = await launch(fixture.socket, {});
  const log = page.getByRole("log", { name: "对话" });
  const composer = page.getByRole("textbox", { name: "消息" });
  const send = async (text: string) => {
    await composer.fill(text);
    await composer.press("Enter");
  };
  const nav = page.locator(".side-nav");

  // Her runs nowhere yet: Progress is no place to go.
  await expect(nav.getByRole("button", { name: /^Her/ })).toBeVisible();
  await expect(nav.getByRole("button", { name: /^进展/ })).toHaveCount(0);
  await nav.getByRole("button", { name: /^Her/ }).click();
  const choices = page.getByRole("list", { name: "选择 Her 在哪里运行" });
  await choices.getByRole("listitem").filter({ hasText: "这台 Mac" }).getByRole("button", { name: "开启 Her" }).click({ timeout: 60_000 });
  await expect(page.getByText(/^你好，我是 .+，在 .+ 上为你做事/)).toBeVisible({ timeout: 120_000 });

  // Now it is, under Her; she has sent nothing out.
  await navTo(page, /^进展/);
  await expect(page.getByRole("heading", { name: "进展", level: 1 })).toBeVisible();
  await expect(page.getByText("还没有派出去的活")).toBeVisible();
  await snap(page, "progress-empty");

  // She hands a piece of work to a sub-agent (her-mock's relay_slow: "写长文", which runs a command).
  await navTo(page, /^Her/);
  await send("[[mock:relay_slow:3]] 写一篇长文");
  await expect(log).toContainText("好，这个要一会儿，写好发你。", { timeout: 60_000 });

  // The busy bubble names the work: it opens the sub-agent's record, whose back button leads to the board.
  await log.getByRole("button", { name: "写长文" }).click();
  await expect(page.getByRole("heading", { name: "写长文", level: 2 })).toBeVisible();
  await page.locator(".pt-back").click();

  // Its card, under what the owner said and Her's words since; the sub-agent's command waits for the owner.
  const list = page.getByRole("list", { name: "派出去的活" });
  const group = list.getByRole("region", { name: /写一篇长文/ });
  await expect(group).toContainText("好，这个要一会儿，写好发你。");
  const card = group.getByRole("button", { name: /^写长文/ });
  await expect(card).toContainText("子智能体");
  await expect(card).toContainText("等你批准", { timeout: 60_000 });
  // The sidebar says so while Her's conversation is followed.
  await expect(nav.getByRole("button", { name: /^进展/ }).getByRole("img", { name: /在等你/ })).toBeVisible();
  await snap(page, "progress-waiting");

  // The card's page: what set it off, the work, what Her asked of it, and the question, answered there.
  await card.click();
  await expect(page.getByRole("heading", { name: "写长文", level: 2 })).toBeVisible();
  await expect(page.locator(".pt-said")).toContainText("写一篇长文");
  await expect(page.locator(".pt-brief")).toContainText("Write it and report.");
  await expect(page.locator(".pt-pill")).toHaveText("等你批准");
  const asked = page.getByRole("region", { name: "等你批准" });
  await expect(asked).toContainText("sleep 3");
  await snap(page, "progress-record-asking");
  await asked.getByRole("button", { name: /^允许/ }).first().click();

  // It works, then ends: its call as a line that opens to its output, its last words as its report, how long and
  // how many tokens it took.
  const steps = page.getByRole("log", { name: /^过程/ });
  await expect(steps).toContainText("sleep 3", { timeout: 60_000 });
  await expect(page.locator(".pt-pill")).toHaveText("已完成", { timeout: 60_000 });
  await expect(page.locator(".pt-end")).toContainText("交回给 Her 的汇报");
  await expect(page.locator(".pt-facts")).toContainText(/用时 .+ · .+ tokens/);
  await expect(steps.getByRole("button", { name: /已运行/ })).toBeVisible();
  await snap(page, "progress-record-done");

  // Back on the board it is among the ended ones, folded until asked for.
  await page.locator(".pt-back").click();
  await expect(list).toContainText("已结束");
  await expect(card).toBeHidden();
  await group.getByRole("button", { expanded: false }).click();
  await expect(card).toContainText(/用时 .+ · .+ tokens/);

  // A coding session she starts on this Mac, asked first, is a card too; its record is the session's own page.
  await navTo(page, /^Her/);
  await send(`[[mock:mesh:session_start:${JSON.stringify({ project: "owner-mac/demo", prompt: "say 做完了", title: "Her 派的活" })}]] 开工`);
  const approval = page.getByRole("region", { name: "等你批准" });
  await expect(approval).toContainText("owner-mac/demo", { timeout: 60_000 });
  await approval.getByRole("button", { name: /^允许/ }).first().click();
  await expect(log.getByRole("button", { name: "Her 派的活" })).toBeVisible({ timeout: 60_000 });
  await navTo(page, /^进展/);
  const started = list.getByRole("region", { name: /开工/ });
  // It may have ended already: an ended group is folded when the page comes up.
  await expect(started).toBeVisible();
  const folded = started.getByRole("button", { expanded: false });
  if (await folded.count()) await folded.click();
  const session = started.getByRole("button", { name: /^Her 派的活/ });
  await expect(session).toContainText("Claude · owner-mac");
  await expect(session).toContainText("完成", { timeout: 60_000 });
  await snap(page, "progress-board");
  await session.click();
  await expect(page.locator(".pt-shead")).toContainText("Her 派的活");
  await expect(page.getByRole("log", { name: "对话" })).toContainText("做完了", { timeout: 60_000 });
  // From there, the session among all sessions; and back to what Her said.
  await page.getByRole("button", { name: "在全部会话里打开" }).click();
  await expect(page.getByRole("heading", { name: "Her 派的活" })).toBeVisible();
  await navTo(page, /^进展/);
  await page.getByRole("button", { name: "在对话里看" }).first().click();
  await expect(log).toContainText("好，这个要一会儿，写好发你。");
  await app.close();
});
