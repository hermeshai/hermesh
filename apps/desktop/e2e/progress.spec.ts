import { expect, test } from "@playwright/test";
import { fixture, launch, navTo, snap } from "./app";

// Progress (docs/her-team.md §15), as the owner uses it, against her itself with her-mock as its model. Run by
// TestDesktopHerProgress, which needs her built. One story: Her is turned on here; nothing is sent out yet; she
// hands a piece of work to a sub-agent, a post of its own under what needs the owner, with what the owner said at
// its foot; it asks for the owner's approval, works and ends; its record shows what Her asked, what it called and
// what it reported. Her busy line says who is at work, never what: Her while she answers, her sub-agents at work by
// name, one opening its record, several a menu of them. A coding session she starts is no post: its own page holds
// it.

test("Progress: what Her sent out to her sub-agents as a feed, and a sub-agent's record", async () => {
  test.setTimeout(300_000);
  const { app, page } = await launch(fixture.socket, {});
  // The info card docks beside the record only when the window has room for it.
  await page.setViewportSize({ width: 1440, height: 800 });
  const log = page.getByRole("log", { name: "对话" });
  const composer = page.getByRole("textbox", { name: "消息" });
  const send = async (text: string) => {
    await composer.fill(text);
    await composer.press("Enter");
  };
  const nav = page.locator(".side-nav");
  // Progress is a page of Her's: a button of her top, as often as it is looked at, opens it.
  const button = page.locator(".her-head").getByTestId("her-progress");
  const progress = async () => {
    await navTo(page, /^Her/);
    await button.click();
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

  // She has answered, and her sub-agent waits for the owner: no dots, the question is Progress's.
  await expect(log.locator(".bm.working")).toHaveCount(0, { timeout: 30_000 });

  // Its post, under what needs the owner: the sub-agent's command waits.
  await progress();
  const list = page.getByRole("list", { name: "派出去的活" });
  const post = list.getByRole("article", { name: "写长文" });
  await expect(list.getByRole("listitem", { name: "需要你" }).getByRole("article", { name: "写长文" })).toBeVisible({ timeout: 60_000 });
  await expect(post).toContainText("等你批准");
  // Her's top says so: Progress is marked while a piece of her work waits; her avatar is not, which is a scheduled
  // task's.
  await page.locator(".pt-back").click();
  await expect(button).toHaveAttribute("aria-label", "进展 · 1 个在等你");
  await expect(page.getByTestId("her-avatar")).toHaveAttribute("aria-label", "助手设置");
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

  // Review opens its record, read as a session is: what Her asked of it as what was sent to it, the info card with
  // how it stands and what set it off, and the question, answered in the control bar at the bottom.
  await post.getByRole("button", { name: "去批准" }).click();
  await expect(page.getByRole("heading", { name: "写长文", level: 2 })).toBeVisible();
  const steps = page.getByRole("log", { name: /^过程/ });
  await expect(steps.locator(".um .bub")).toContainText("Write it and report.");
  const card = page.getByRole("complementary", { name: "任务信息" });
  await expect(card).toBeVisible();
  await expect(card).toContainText("等你批准");
  await expect(card.getByRole("button", { name: "来自哪句话" })).toContainText("写一篇长文");
  const bar = page.getByRole("region", { name: "这一轮" });
  await expect(bar).toContainText("等你批准");
  const asked = bar.getByRole("region", { name: "等你批准" });
  await expect(asked).toContainText("sleep 3");
  await snap(page, "progress-record-asking");
  await asked.getByRole("button", { name: /^允许/ }).first().click();

  // It works, then ends: its call as a grey line that opens to its output, its last words across the page and a
  // line that says they went back to Her; the card says how long and how many tokens it took. The bar is gone.
  await expect(card).toContainText("已完成", { timeout: 60_000 });
  await expect(bar).toHaveCount(0);
  await expect(steps).toContainText("sleep 3");
  await expect(page.getByText(/^已办完，最后这段话已交回给 /)).toBeVisible();
  await expect(card).toContainText(/用时 .+/);
  await expect(card).toContainText(/.+ tokens/);
  const call = steps.getByRole("button", { name: /已运行/ });
  await expect(call).toHaveAttribute("aria-expanded", "false");
  await call.click();
  await expect(call).toHaveAttribute("aria-expanded", "true");
  // The info card goes as a session's does, with its button.
  await page.getByRole("button", { name: "信息面板", exact: true }).click();
  await expect(card).toHaveCount(0);
  await page.getByRole("button", { name: "信息面板", exact: true }).click();
  await expect(card).toBeVisible();
  await snap(page, "progress-record-done");

  // Back in the feed it is among the earlier ones, and nothing needs the owner.
  await page.locator(".pt-back").click();
  await expect(list.getByRole("listitem", { name: "早些时候" }).getByRole("article", { name: "写长文" })).toBeVisible();
  await expect(list.getByRole("listitem", { name: "需要你" })).toHaveCount(0);
  await expect(post.getByRole("button", { name: /^写长文 · 已完成/ })).toBeVisible();

  // Another, allowed: it works on in the background; Progress says one is at work, and her conversation's busy line
  // stays, naming it, never what it does; it opens its record, whose way back is to her conversation.
  await navTo(page, /^Her/);
  await send("[[mock:relay_slow:60]] 再写一篇");
  const asking = page.getByRole("region", { name: "等你批准" });
  await expect(asking).toContainText("sleep 60", { timeout: 60_000 });
  await asking.getByRole("button", { name: /^允许/ }).first().click();
  await expect(button).toHaveAttribute("aria-label", "进展 · 1 个进行中", { timeout: 60_000 });
  const busy = log.locator(".bm.working");
  const one = busy.getByRole("button", { name: "子智能体 · 写长文，正在工作" });
  await expect(one).toHaveText("写长文", { timeout: 30_000 });
  await snap(page, "busy-agent");
  await one.click();
  await expect(page.getByRole("heading", { name: "写长文", level: 2 })).toBeVisible();
  await page.locator(".pt-back").click();
  await expect(composer).toBeVisible();

  // A third while it works: the two take turns in the line, which ends with their number and a chevron; it opens a
  // menu of them, each opening its record, and Progress.
  await send("[[mock:relay_slow:60]] 第三篇");
  await expect(asking).toContainText("sleep 60", { timeout: 60_000 });
  await asking.getByRole("button", { name: /^允许/ }).first().click();
  await expect(button).toHaveAttribute("aria-label", "进展 · 2 个进行中", { timeout: 60_000 });
  const two = busy.getByRole("button", { name: /^2 个子智能体正在工作/ });
  await expect(two.locator(".wb-pop")).toHaveText("2", { timeout: 30_000 });
  await two.click();
  const menu = page.getByRole("menu", { name: /^2 个子智能体正在工作/ });
  await expect(menu.getByRole("menuitem")).toHaveText(["写长文", "写长文", "在「进展」中查看全部"]);
  await snap(page, "busy-menu");
  await menu.getByRole("menuitem").first().click();
  await expect(page.getByRole("heading", { name: "写长文", level: 2 })).toBeVisible();
  await page.locator(".pt-back").click();
  await two.click();
  await menu.getByRole("menuitem", { name: "在「进展」中查看全部" }).click();
  await expect(list.getByRole("listitem", { name: "现在" }).getByRole("article", { name: "写长文" })).toHaveCount(2);

  // While Her herself answers, her top capsule says "正在输入…" and the busy bubble stays clean (never mixing her status with sub-agents, nor naming her call).
  await navTo(page, /^Her/);
  await send("[[mock:bash:sleep 8]] 看看");
  await expect(asking).toContainText("sleep 8", { timeout: 60_000 });
  await asking.getByRole("button", { name: /^允许/ }).first().click();
  await expect(page.locator(".her-cap")).toContainText("正在输入…", { timeout: 30_000 });
  await expect(busy).not.toContainText("正在输入…");
  await expect(busy).not.toContainText("sleep 8");
  await snap(page, "busy-her");

  // A coding session she starts on this Mac, asked first, is a card in her conversation, and no post of Progress.
  await send(`[[mock:mesh:session_start:${JSON.stringify({ project: "owner-mac/demo", prompt: "say 做完了", title: "Her 派的活" })}]] 开工`);
  await expect(asking).toContainText("owner-mac/demo", { timeout: 60_000 });
  await asking.getByRole("button", { name: /^允许/ }).first().click();
  await expect(log.getByRole("button", { name: "Her 派的活" })).toBeVisible({ timeout: 60_000 });
  await progress();
  await expect(list.getByRole("article", { name: "写长文" })).toHaveCount(3);
  await expect(list.getByRole("article", { name: "Her 派的活" })).toHaveCount(0);
  await snap(page, "progress-board");
  // From what set it off, back to what Her said.
  const done = list.getByRole("listitem", { name: "早些时候" }).getByRole("article", { name: "写长文" });
  await done.getByRole("button", { name: "来自哪句话" }).click();
  await page.getByRole("dialog", { name: "来自你的一句话" }).getByRole("button", { name: "在对话里看" }).click();
  await expect(log).toContainText("好，这个要一会儿，写好发你。");
  await app.close();
});
