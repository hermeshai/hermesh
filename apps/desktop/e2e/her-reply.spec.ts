import { expect, test, type Locator } from "@playwright/test";
import { fixture, launch, snap } from "./app";

// Replying to a message of Her's conversation, or to a part of one (docs/her-reply.md), as the owner does it,
// against her itself with her-mock as its model. Run by TestDesktopHerReply, which needs her built. One story: Her
// is turned on here and says two things; the owner replies to one from its buttons, to words selected in it, and
// to a message of their own. her-mock sends back the prompt it got, so what Her was told shows in her answer.

test("Her: a reply from a message's buttons, from words selected in it, and what it quotes brought into view", async () => {
  test.setTimeout(300_000);
  const { app, page } = await launch(fixture.socket, {});
  const log = page.getByRole("log", { name: "对话" });
  const composer = page.getByRole("textbox", { name: "消息" });
  const send = async (text: string) => {
    await composer.fill(text);
    await composer.press("Enter");
  };
  await page.locator(".side-nav").getByRole("button", { name: /^Her/ }).click();
  const choices = page.getByRole("list", { name: "选择 Her 在哪里运行" });
  await choices.getByRole("listitem").filter({ hasText: "这台 Mac" }).getByRole("button", { name: "开启 Her" }).click({ timeout: 60_000 });
  await expect(page.getByText(/^你好，我是 .+，在 .+ 上为你做事/)).toBeVisible({ timeout: 120_000 });
  await send("[[mock:send:酒店有两家：园区旁的，西湖边的。]] hi");
  await expect(log).toContainText("酒店有两家：园区旁的，西湖边的。", { timeout: 60_000 });
  await send("[[mock:send:周五的票已经锁好。]] hi");
  await expect(log).toContainText("周五的票已经锁好。", { timeout: 60_000 });

  // From a message's buttons: what the next message replies to shows above the composer's text, and goes with Esc.
  const hers = log.locator(".bm").filter({ hasText: "酒店有两家" }).first();
  const bar = page.getByTestId("her-reply-bar");
  // Copied, the check comes in where the copy icon was.
  await hers.hover();
  await hers.getByRole("button", { name: "拷贝" }).click();
  const tick = hers.getByRole("button", { name: "拷贝" }).locator("svg.tick-in");
  await expect(tick).toBeVisible();
  expect(await tick.evaluate((el) => getComputedStyle(el).animationName)).toBe("popin");
  await expect(tick).toHaveCount(0);
  await hers.hover();
  await hers.getByRole("button", { name: "回复" }).click();
  await expect(bar).toContainText("回复 Her");
  await expect(bar).toContainText("酒店有两家：园区旁的，西湖边的。");
  await expect(composer).toBeFocused();
  await snap(page, "her-reply-bar");
  await composer.press("Escape");
  await expect(bar).toBeHidden();
  // Sent, the message shows it at the top of its bubble, its own words under it, and Her is told of it after the
  // owner's words.
  await hers.hover();
  await hers.getByRole("button", { name: "回复" }).click();
  await send("[[mock:send]] 哪家近");
  await expect(bar).toBeHidden();
  const replied = log.locator(".um").filter({ hasText: "哪家近" });
  await expect(replied.getByTestId("her-quote")).toContainText("酒店有两家：园区旁的，西湖边的。");
  await expect(replied.locator(".bub-t")).toHaveText("[[mock:send]] 哪家近");
  const heard = log.locator(".bm").filter({ hasText: "哪家近" });
  await expect(heard).toContainText("Replying to this earlier message of yours, quoted in full:", { timeout: 60_000 });
  await expect(heard.locator("blockquote")).toHaveText("酒店有两家：园区旁的，西湖边的。");

  // From words selected in one message: Reply shows over them, and the message quotes that part.
  const drag = async (from: Locator, chars: [number, number], to?: Locator) => {
    const at = await from.evaluate((el, [a, b]) => {
      const text = document.createTreeWalker(el, NodeFilter.SHOW_TEXT).nextNode()!;
      const range = document.createRange();
      range.setStart(text, a);
      range.setEnd(text, b);
      const box = range.getBoundingClientRect();
      return { x: box.left + 1, y: box.top + box.height / 2, end: box.right };
    }, chars);
    const end = to ? await to.boundingBox().then((b) => ({ x: b!.x + b!.width / 2, y: b!.y + b!.height / 2 })) : { x: at.end, y: at.y };
    await page.mouse.move(at.x, at.y);
    await page.mouse.down();
    await page.mouse.move(end.x, end.y, { steps: 6 });
    await page.mouse.up();
  };
  const pill = page.getByTestId("her-reply-pill");
  await drag(hers.locator(".bbub"), [11, 15]);
  await expect(pill).toBeVisible();
  await snap(page, "her-reply-selected");
  await pill.click();
  await expect(pill).toBeHidden();
  await expect(bar).toContainText("西湖边的");
  await expect(bar).not.toContainText("园区");
  await send("[[mock:send]] 这家多少钱");
  const part = log.locator(".um").filter({ hasText: "这家多少钱" });
  await expect(part.getByTestId("her-quote")).toContainText("西湖边的");
  const heardPart = log.locator(".bm").filter({ hasText: "这家多少钱" });
  await expect(heardPart).toContainText("quoted in part:", { timeout: 60_000 });
  await expect(heardPart.locator("blockquote")).toHaveText("西湖边的");
  await snap(page, "her-reply");
  // Its quote brings the message replied to into view, marked a moment.
  await part.getByTestId("her-quote").click();
  await expect(log.locator(".vrow.rtn-flash")).toContainText("酒店有两家：园区旁的，西湖边的。");
  // Words selected across two messages are no one message's: no Reply.
  await drag(hers.locator(".bbub"), [0, 2], log.locator(".bm").filter({ hasText: "周五的票已经锁好。" }).first().locator(".bbub"));
  await expect(pill).toBeHidden();
  await page.mouse.click(5, 300);

  // A message of the owner's own is quoted the same way, and Her is told whose it was.
  await replied.hover();
  await replied.getByRole("button", { name: "回复" }).click();
  await expect(bar).toContainText("引用自己的消息");
  await bar.getByRole("button", { name: "取消回复" }).click();
  await expect(bar).toBeHidden();
  await replied.hover();
  await replied.getByRole("button", { name: "回复" }).click();
  await send("[[mock:send]] 我是说打车");
  const own = log.locator(".um").filter({ hasText: "我是说打车" });
  await expect(own.getByTestId("her-quote")).toContainText("哪家近");
  await expect(log.locator(".bm").filter({ hasText: "我是说打车" })).toContainText("Replying to this earlier message of mine, quoted in full:", { timeout: 60_000 });
  await app.close();
});
