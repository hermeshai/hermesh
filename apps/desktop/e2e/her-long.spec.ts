import { expect, test } from "@playwright/test";
import { fixture, launch, snap } from "./app";

// A long conversation with Her, read from its end to its first message (docs/desktop-performance.md §7). Run by
// TestDesktopHerLong, which needs her built: Her on this Mac has had 240 messages in English, short and long, a
// paragraph or many, with headings, lists, code and tables. The app opens on the last page of them; the owner
// scrolls up on the trackpad, the pointer over the messages (code blocks too), and the earlier pages come in and
// their rows are drawn as they come into sight. What is on screen moves exactly as far as each stroke,
// never jumps and is never blank, until the first message.

/** How far one stroke on the trackpad scrolls: in small steps, a frame apart, as a trackpad sends them. */
const STROKE = 480;
const STEPS = 12;

test("Her: a long conversation, scrolled up to its first message, moves only as far as it is scrolled", async () => {
  test.setTimeout(600_000);
  const { app, page } = await launch(fixture.socket, { systemLanguages: ["en"] });
  await page.locator(".side-nav").getByRole("button", { name: /^Her/ }).click();
  const log = page.getByRole("log", { name: "Conversation" });
  await expect(log).toContainText("Reply 240:", { timeout: 120_000 });
  await snap(page, "her-long-end");

  // Every frame, whether any message is in sight: rows not drawn are a space, which must never fill the view.
  await page.evaluate(() => {
    const scroller = document.querySelector<HTMLElement>(".cscroll")!;
    const lab = ((window as unknown as { lab: { blank: number; frames: number } }).lab = { blank: 0, frames: 0 });
    const tick = () => {
      const top = scroller.getBoundingClientRect().top;
      const bottom = top + scroller.clientHeight;
      const seen = [...scroller.querySelectorAll(".vrow")].some((row) => {
        const r = row.getBoundingClientRect();
        return r.bottom > top && r.top < bottom;
      });
      lab.frames++;
      if (!seen) lab.blank++;
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  });

  // The pointer in the middle of the conversation, where code blocks pass under it.
  const box = (await page.locator(".cscroll").boundingBox())!;
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  /** The first row in sight and where it is, and how far the conversation is scrolled. */
  const sight = (key?: string) =>
    page.evaluate((key) => {
      const scroller = document.querySelector<HTMLElement>(".cscroll")!;
      const top = scroller.getBoundingClientRect().top;
      const row = key
        ? scroller.querySelector<HTMLElement>(`.vrow[data-step-key="${CSS.escape(key)}"]`)
        : [...scroller.querySelectorAll<HTMLElement>(".vrow")].find((r) => r.getBoundingClientRect().bottom > top + 40);
      return { key: row?.dataset.stepKey, y: row ? row.getBoundingClientRect().top - top : null, scrolled: scroller.scrollTop, earlier: !!scroller.querySelector(".tdiv.earlier") };
    }, key);

  const jumps: string[] = [];
  let steps = 0;
  for (;;) {
    const before = await sight();
    if (before.scrolled === 0 && !before.earlier) break;
    expect(steps++, "the first message is reached").toBeLessThan(600);
    for (let i = 0; i < STEPS; i++) {
      await page.mouse.wheel(0, -STROKE / STEPS);
      await page.waitForTimeout(16);
    }
    await page.waitForTimeout(100);
    const after = await sight(before.key);
    // Scrolled a whole stroke up: the row in sight came down as far, whatever was read or drawn meanwhile.
    if (before.scrolled > STROKE + 1 && (after.y === null || Math.abs(after.y - before.y! - STROKE) > 2))
      jumps.push(`step ${steps}: ${before.key} moved ${after.y === null ? "off the page" : Math.round(after.y - before.y!)}px`);
  }
  await expect(log.locator(".vrow").first()).toContainText("Msg 1:");
  await snap(page, "her-long-start");
  const { blank, frames } = await page.evaluate(() => (window as unknown as { lab: { blank: number; frames: number } }).lab);
  expect(jumps, "every stroke moves the conversation as far").toEqual([]);
  expect(frames).toBeGreaterThan(steps);
  expect(blank, "no frame without a message in sight").toBe(0);
  await app.close();
});
