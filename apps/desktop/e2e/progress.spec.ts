import { expect, test } from "@playwright/test";
import { fixture, launch, navTo, snap } from "./app";

// Progress (docs/her-team.md §15), as the owner uses it, against her itself with her-mock as its model. Run by
// TestDesktopHerProgress, which needs her built. One story: Her is turned on here; nothing is sent out yet; she
// hands a piece of work to a sub-agent, a post of its own under what needs the owner, with what the owner said at
// its foot; it asks for the owner's approval, works and ends; its record shows what Her asked, what it called and
// what it reported. Her busy bubble is hers alone: gone once she answered, it says the call she is on. A coding
// session she starts is no post: its own page holds it.

test("Progress: what Her sent out to her sub-agents as a feed, and a sub-agent's record", async () => {
  test.setTimeout(300_000);
  const { app, page } = await launch(fixture.socket, {});
  const log = page.getByRole("log", { name: "对话" });
  const composer = page.getByRole("textbox", { name: "消息" });
  const send = async (text: string) => {
    await composer.fill(text);
    await composer.press("Enter");
  };
  const nav = page.locator(".side-nav");
  // Progress is a page of Her's: the first card of her page, beside the conversation, opens it.
  const herPage = page.getByRole("complementary", { name: "助手设置" });
  const card = herPage.getByTestId("her-progress");
  const openHerPage = async () => {
    await navTo(page, /^Her/);
    if (!(await herPage.isVisible())) await page.getByTestId("her-avatar").click();
    await herPage.getByRole("tab", { name: "工作" }).click();
  };
  const progress = async () => {
    await openHerPage();
    await card.click();
  };

  // The sidebar has no place for Progress: it is Her's.
  await expect(nav.getByRole("button", { name: /^Her/ })).toBeVisible();
  await expect(nav.getByRole("button", { name: /^进展/ })).toHaveCount(0);
  await nav.getByRole("button", { name: /^Her/ }).click();
  const choices = page.getByRole("list", { name: "选择 Her 在哪里运行" });
  await choices.getByRole("listitem").filter({ hasText: "这台 Mac" }).getByRole("button", { name: "开启 Her" }).click({ timeout: 60_000 });
  await expect(page.getByText(/^你好，我是 .+，在 .+ 上为你做事/)).toBeVisible({ timeout: 120_000 });

  // Now her top bar has it; she has sent nothing out. Her row stays current there, and the back button leads to her.
  await progress();
  await expect(page.getByRole("heading", { name: "进展", level: 1 })).toBeVisible();
  await expect(page.getByText("还没有派出去的活")).toBeVisible();
  await expect(nav.getByRole("button", { name: /^Her/ })).toHaveClass(/\bsel\b/);
  await snap(page, "progress-empty");

  // She hands a piece of work to a sub-agent (her-mock's relay_slow: "写长文", which runs a command).
  await navTo(page, /^Her/);
  await send("[[mock:relay_slow:3]] 写一篇长文");
  await expect(log).toContainText("好，这个要一会儿，写好发你。", { timeout: 60_000 });

  // She has answered: no dots while her sub-agent works, whose work is Progress's.
  await expect(log.locator(".bm.working")).toHaveCount(0, { timeout: 30_000 });

  // Its post, under what needs the owner: the sub-agent's command waits.
  await progress();
  const list = page.getByRole("list", { name: "派出去的活" });
  const post = list.getByRole("article", { name: "写长文" });
  await expect(list.getByRole("listitem", { name: "需要你" }).getByRole("article", { name: "写长文" })).toBeVisible({ timeout: 60_000 });
  await expect(post).toContainText("等你批准");
  // Her's top says so: her avatar is marked while a piece of her work waits, and on her page Progress says it.
  await page.locator(".pt-back").click();
  await expect(page.getByTestId("her-avatar")).toHaveAttribute("aria-label", "助手设置 · 有事在等你");
  await openHerPage();
  await expect(card).toHaveAttribute("aria-label", "进展，1 个在等你");
  await progress();
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

  // Another, allowed: it works on in the background, and her top bar says one is at work, with no dots on her page.
  await navTo(page, /^Her/);
  await send("[[mock:relay_slow:60]] 再写一篇");
  const asking = page.getByRole("region", { name: "等你批准" });
  await expect(asking).toContainText("sleep 60", { timeout: 60_000 });
  await asking.getByRole("button", { name: /^允许/ }).first().click();
  await openHerPage();
  await expect(card).toHaveAttribute("aria-label", "进展，1 个进行中", { timeout: 60_000 });
  await expect(log.locator(".bm.working")).toHaveCount(0);
  await progress();
  await expect(list.getByRole("listitem", { name: "现在" }).getByRole("article", { name: "写长文" })).toBeVisible();

  // While Her herself is on a call, her bubble says what it does, in the words Progress says a step in.
  await navTo(page, /^Her/);
  await send("[[mock:bash:sleep 8]] 看看");
  await expect(asking).toContainText("sleep 8", { timeout: 60_000 });
  await asking.getByRole("button", { name: /^允许/ }).first().click();
  await expect(log.locator(".bm.working")).toContainText("正在运行 sleep 8", { timeout: 30_000 });
  await snap(page, "busy-step");

  // A coding session she starts on this Mac, asked first, is a card in her conversation, and no post of Progress.
  await send(`[[mock:mesh:session_start:${JSON.stringify({ project: "owner-mac/demo", prompt: "say 做完了", title: "Her 派的活" })}]] 开工`);
  await expect(asking).toContainText("owner-mac/demo", { timeout: 60_000 });
  await asking.getByRole("button", { name: /^允许/ }).first().click();
  await expect(log.getByRole("button", { name: "Her 派的活" })).toBeVisible({ timeout: 60_000 });
  await progress();
  await expect(list.getByRole("article", { name: "写长文" })).toHaveCount(2);
  await expect(list.getByRole("article", { name: "Her 派的活" })).toHaveCount(0);
  await snap(page, "progress-board");
  // From what set it off, back to what Her said.
  const done = list.getByRole("listitem", { name: "早些时候" }).getByRole("article", { name: "写长文" });
  await done.getByRole("button", { name: "来自哪句话" }).click();
  await page.getByRole("dialog", { name: "来自你的一句话" }).getByRole("button", { name: "在对话里看" }).click();
  await expect(log).toContainText("好，这个要一会儿，写好发你。");
  await app.close();
});
