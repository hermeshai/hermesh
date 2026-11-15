import { expect, test } from "@playwright/test";
import { fixture, launch, navTo, snap } from "./app";

// Progress (docs/her-team.md §15), as the owner uses it, against her itself with her-mock as its model. Run by
// TestDesktopHerProgress, which needs her built. One story: Her is turned on here; nothing is sent out yet; she
// hands a piece of work to a sub-agent, a post of its own under what needs the owner, with what the owner said at
// its foot; it asks for the owner's approval, works and ends; its record shows what Her asked, what it called and
// what it reported; then a coding session she starts, as a post whose record is the session's own.

test("Progress: what Her sent out as a feed, a sub-agent's record, and a coding session's", async () => {
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

  // Its post, under what needs the owner: the sub-agent's command waits.
  const list = page.getByRole("list", { name: "派出去的活" });
  const post = list.getByRole("article", { name: "写长文" });
  await expect(list.getByRole("listitem", { name: "需要你" }).getByRole("article", { name: "写长文" })).toBeVisible({ timeout: 60_000 });
  await expect(post).toContainText("等你批准");
  // The sidebar says so while Her's conversation is followed.
  await expect(nav.getByRole("button", { name: /^进展/ }).getByRole("img", { name: /在等你/ })).toBeVisible();
  await snap(page, "progress-waiting");

  // What set it off, shown over the page by the post's ⓘ: what the owner said and what Her said back.
  await post.getByRole("button", { name: "来自哪句话" }).click();
  const source = page.getByRole("dialog", { name: "来自你的一句话" });
  await expect(source).toContainText("写一篇长文");
  await expect(source).toContainText("好，这个要一会儿，写好发你。");
  await snap(page, "progress-source");
  await page.keyboard.press("Escape");
  await expect(source).toBeHidden();

  // Review opens its record: what set it off, the work, what Her asked of it, and the question, answered there.
  await post.getByRole("button", { name: "去批准" }).click();
  await expect(page.getByRole("heading", { name: "写长文", level: 2 })).toBeVisible();
  await expect(page.locator(".pt-bar .pf-from")).toContainText("写一篇长文");
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

  // Back in the feed it is among the earlier ones, and nothing needs the owner.
  await page.locator(".pt-back").click();
  await expect(list.getByRole("listitem", { name: "早些时候" }).getByRole("article", { name: "写长文" })).toBeVisible();
  await expect(list.getByRole("listitem", { name: "需要你" })).toHaveCount(0);
  await expect(post.getByRole("button", { name: /^写长文 · 已完成/ })).toBeVisible();

  // A coding session she starts on this Mac, asked first, is a card too; its record is the session's own page.
  await navTo(page, /^Her/);
  await send(`[[mock:mesh:session_start:${JSON.stringify({ project: "owner-mac/demo", prompt: "say 做完了", title: "Her 派的活" })}]] 开工`);
  const approval = page.getByRole("region", { name: "等你批准" });
  await expect(approval).toContainText("owner-mac/demo", { timeout: 60_000 });
  await approval.getByRole("button", { name: /^允许/ }).first().click();
  await expect(log.getByRole("button", { name: "Her 派的活" })).toBeVisible({ timeout: 60_000 });
  await navTo(page, /^进展/);
  const session = list.getByRole("article", { name: "Her 派的活" });
  await expect(session.getByRole("button", { name: /^Her 派的活 · 已完成/ })).toBeVisible({ timeout: 60_000 });
  await session.getByRole("button", { name: "来自哪句话" }).click();
  await expect(page.getByRole("dialog", { name: "来自你的一句话" })).toContainText("开工");
  await page.keyboard.press("Escape");
  await snap(page, "progress-board");
  await session.getByRole("button", { name: /^Her 派的活/ }).click();
  await expect(page.locator(".pt-shead")).toContainText("Her 派的活");
  await expect(page.locator(".pt-shead")).toContainText("Claude · owner-mac");
  await expect(page.getByRole("log", { name: "对话" })).toContainText("做完了", { timeout: 60_000 });
  // From there, the session among all sessions; and from what set it off, back to what Her said.
  await page.getByRole("button", { name: "在全部会话里打开" }).click();
  await expect(page.getByRole("heading", { name: "Her 派的活" })).toBeVisible();
  await navTo(page, /^进展/);
  const done = list.getByRole("article", { name: "写长文" });
  await done.getByRole("button", { name: "来自哪句话" }).click();
  await page.getByRole("dialog", { name: "来自你的一句话" }).getByRole("button", { name: "在对话里看" }).click();
  await expect(log).toContainText("好，这个要一会儿，写好发你。");
  await app.close();
});
