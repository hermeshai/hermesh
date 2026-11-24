import { expect, test, type ElectronApplication, type Page } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { startDaemon, type Daemon } from "../perf/daemon";
import { conversation, ids, rng, titles } from "../perf/world";
import { launch, sessionList } from "./app";

// A long conversation in the real app (docs/desktop-performance.md §7): the benchmark's stand-in
// for hermeshd (perf/daemon.ts) serves two conversations of 40 turns and about 2,700 events, one
// finished and one that grows while live. Only the rows near what is in sight are drawn; reading,
// following the end, what a row has open and a selection behave as with every row drawn.

test.describe.configure({ mode: "serial" });

let daemon: Daemon;
let app: ElectronApplication;
let page: Page;

test.beforeAll(async () => {
  daemon = await startDaemon(path.join(fs.mkdtempSync(path.join(os.tmpdir(), "hmconv-")), "d.sock"));
  ({ app, page } = await launch(daemon.socket));
  await page.setViewportSize({ width: 1440, height: 860 });
});

test.afterAll(async () => {
  await app?.close();
  await daemon?.close();
});

const scroller = ".cscroll.markdown-chat";
/** How far the view is from the conversation's end. */
const below = () => page.evaluate((sel) => { const el = document.querySelector(sel)!; return Math.round(el.scrollHeight - el.scrollTop - el.clientHeight); }, scroller);
const scrollTop = () => page.evaluate((sel) => document.querySelector(sel)!.scrollTop, scroller);
/** Rows drawn, and rows the conversation holds. */
const drawnRows = () => page.locator(".tr > .vrow").count();
const heldRows = async () => Number(await page.locator(".tr").getAttribute("data-rows"));
const drawn = (key: string) => page.locator(`.vrow[data-step-key="${key}"]`).count().then((n) => n > 0);
/** Until the view stops moving, and a little more for the window to settle. */
const settle = () =>
  page.evaluate(async (sel) => {
    const el = document.querySelector(sel)!;
    let last = -1;
    let same = 0;
    for (let i = 0; i < 120 && same < 4; i++) {
      await new Promise((r) => requestAnimationFrame(r));
      same = el.scrollTop === last ? same + 1 : 0;
      last = el.scrollTop;
    }
    await new Promise((r) => setTimeout(r, 150));
  }, scroller);
/** A point in the conversation's left margin, where the wheel scrolls it. */
async function margin() {
  const r = (await page.locator(scroller).boundingBox())!;
  return { x: r.x + 12, y: r.y + r.height / 2 };
}
async function wheel(dy: number, times = 1) {
  const m = await margin();
  await page.mouse.move(m.x, m.y);
  for (let i = 0; i < times; i++) await page.mouse.wheel(0, dy);
}

async function open(title: string) {
  await sessionList(page);
  await page.locator(".srow", { has: page.locator(".stitle", { hasText: title }) }).first().click();
  await expect(page.locator(".ctitle h2")).toHaveText(title);
  await expect(page.locator(".tr > .vrow").first()).toBeVisible();
}

test("a long conversation opens at its end and draws only the rows near what is in sight", async () => {
  await open(titles.bigIdle);
  await expect(page.locator(".tr .am").last()).toContainText("第 40 轮：");
  await settle();
  expect(await below()).toBeLessThan(80);
  const held = await heldRows();
  const shown = await drawnRows();
  const nodes = await page.locator(".tr *").count();
  expect(held).toBeGreaterThan(300);
  expect(shown, `${shown} of ${held} rows drawn`).toBeLessThan(80);
  expect(nodes, `${nodes} elements in the conversation`).toBeLessThan(2500);
});

test("reading up into rows not drawn before, what is read stays where it is and no frame is blank", async () => {
  test.setTimeout(150_000);
  // Halfway up first (a jump, as with the scrollbar: the next test sees to that one).
  await wheel(-300);
  await page.evaluate((sel) => { const el = document.querySelector(sel)!; el.scrollTop = Math.round(el.scrollHeight * 0.55); }, scroller);
  await settle();
  // From there with the wheel: frames whose middle shows the space that stands in for rows not drawn.
  await page.evaluate((sel) => {
    const w = window as unknown as { __blank: number; __frames: number; __watch: boolean };
    w.__blank = 0;
    w.__frames = 0;
    w.__watch = true;
    const el = document.querySelector(sel)!;
    const tick = () => {
      if (!w.__watch) return;
      const r = el.getBoundingClientRect();
      w.__frames++;
      if (document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)?.closest(".vspace")) w.__blank++;
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  }, scroller);
  let worst = 0;
  for (let i = 0; i < 30; i++) {
    await wheel(-300);
    await settle();
    const before = await page.evaluate((sel) => {
      const r = document.querySelector(sel)!.getBoundingClientRect();
      const row = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)?.closest<HTMLElement>(".vrow");
      return row ? { key: row.dataset.stepKey!, top: row.getBoundingClientRect().top } : null;
    }, scroller);
    if (!before) continue;
    await page.waitForTimeout(400);
    const after = await page.evaluate((key) => document.querySelector(`.vrow[data-step-key="${key}"]`)?.getBoundingClientRect().top ?? null, before.key);
    expect(after, "the row being read was taken away").not.toBeNull();
    worst = Math.max(worst, Math.abs(after! - before.top));
  }
  const { blank, frames } = await page.evaluate(() => {
    const w = window as unknown as { __blank: number; __frames: number; __watch: boolean };
    w.__watch = false;
    return { blank: w.__blank, frames: w.__frames };
  });
  expect(worst, `what was read moved ${worst}px with nothing touched`).toBeLessThanOrEqual(2);
  expect(frames).toBeGreaterThan(30);
  expect(blank, `${blank} of ${frames} frames showed nothing in the middle`).toBe(0);
});

test("a group of calls opened stays open after its row was not drawn", async () => {
  // A closed group of calls among the rows drawn, brought to the middle of the view.
  const key = await page.evaluate(() => {
    const group = document.querySelector<HTMLElement>(".vrow .tgroup:not(.open)");
    group?.scrollIntoView({ block: "center" });
    return group?.closest<HTMLElement>(".vrow")!.dataset.stepKey ?? null;
  });
  expect(key, "no closed group of calls drawn").not.toBeNull();
  await settle();
  await page.locator(`.vrow[data-step-key="${key}"] .tg-h`).click();
  await expect(page.locator(`.vrow[data-step-key="${key}"] .tgroup.open`)).toBeVisible();
  // The focused row stays drawn wherever the view goes: the button lets go of the focus first.
  await page.locator(`.vrow[data-step-key="${key}"] .tg-h`).blur();
  const there = await scrollTop();
  await page.evaluate(([sel, top]) => { const el = document.querySelector(sel as string)!; el.scrollTop = (top as number) + el.clientHeight * 6; }, [scroller, there] as const);
  await settle();
  expect(await drawn(key!), "the group's row is still drawn six screens away").toBe(false);
  await page.evaluate(([sel, top]) => { document.querySelector(sel as string)!.scrollTop = top as number; }, [scroller, there] as const);
  await settle();
  await expect(page.locator(`.vrow[data-step-key="${key}"] .tgroup.open`)).toBeVisible();
});

test("a card of calls scrolled inside is where it was left after its row was not drawn", async () => {
  // The group the test before left open, in the middle of the view; two of its calls opened, so the card has more
  // than it shows and scrolls inside.
  const key = await page.evaluate(() => {
    const group = document.querySelector<HTMLElement>(".vrow .tgroup.open");
    group?.scrollIntoView({ block: "center" });
    return group?.closest<HTMLElement>(".vrow")!.dataset.stepKey ?? null;
  });
  expect(key, "no open group of calls drawn").not.toBeNull();
  await settle();
  const card = page.locator(`.vrow[data-step-key="${key}"] .tg-s`);
  for (const i of [0, 1]) await page.locator(`.vrow[data-step-key="${key}"] .tool .th[aria-expanded]`).nth(i).click();
  await expect.poll(() => card.evaluate((el) => el.scrollHeight - el.clientHeight)).toBeGreaterThan(40);
  // Scrolled by the person to its top (it opened at its newest call).
  await card.evaluate((el) => el.scrollTo({ top: el.scrollHeight }));
  await card.hover();
  await page.mouse.wheel(0, -2000);
  await expect.poll(() => card.evaluate((el) => el.scrollTop)).toBe(0);
  // The focused row stays drawn wherever the view goes: the call clicked last lets go of the focus first.
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await page.mouse.move(5, 5);
  const there = await scrollTop();
  await page.evaluate(([sel, top]) => { const el = document.querySelector(sel as string)!; el.scrollTop = (top as number) + el.clientHeight * 6; }, [scroller, there] as const);
  await settle();
  expect(await drawn(key!), "the group's row is still drawn six screens away").toBe(false);
  await page.evaluate(([sel, top]) => { document.querySelector(sel as string)!.scrollTop = top as number; }, [scroller, there] as const);
  await settle();
  await expect(card).toBeVisible();
  expect(await card.evaluate((el) => el.scrollTop), "the card is back at its newest call, not where it was left").toBe(0);
});

test("a selection over several screens stays whole as the view moves on", async () => {
  const picked = await page.evaluate((sel) => {
    const r = document.querySelector(sel)!.getBoundingClientRect();
    const node = [...document.querySelectorAll<HTMLElement>(".vrow .md p, .vrow .bub")].find((p) => { const b = p.getBoundingClientRect(); return b.top > r.top && b.bottom < r.bottom - 100; })!;
    getSelection()!.setBaseAndExtent(node, 0, node, node.childNodes.length);
    return { key: node.closest<HTMLElement>(".vrow")!.dataset.stepKey!, text: node.textContent!.trim().slice(0, 12) };
  }, scroller);
  await page.waitForTimeout(200);
  await wheel(300, 12);
  await settle();
  expect(await drawn(picked.key), "the row where the selection starts was taken away").toBe(true);
  const end = await page.evaluate((sel) => {
    const r = document.querySelector(sel)!.getBoundingClientRect();
    const node = [...document.querySelectorAll<HTMLElement>(".vrow .md p, .vrow .bub")].reverse().find((p) => { const b = p.getBoundingClientRect(); return b.bottom < r.bottom && b.top > r.top; })!;
    const s = getSelection()!;
    s.setBaseAndExtent(s.anchorNode!, s.anchorOffset, node, node.childNodes.length);
    return node.textContent!.trim().slice(-12);
  }, scroller);
  await page.waitForTimeout(200);
  const selected = await page.evaluate(() => getSelection()!.toString());
  expect(selected).toContain(picked.text);
  expect(selected).toContain(end);
  expect(selected.length, "characters selected over several screens").toBeGreaterThan(1000);
  await page.evaluate(() => getSelection()!.removeAllRanges());
  await page.waitForTimeout(300);
  await page.evaluate((sel) => document.querySelector(sel)!.dispatchEvent(new Event("scroll")), scroller);
  await settle();
  expect(await drawn(picked.key), "the selection's row stays drawn after the selection went").toBe(false);
});

test("the conversation's top and its end each show rows within two frames", async () => {
  for (const to of ["top", "end"]) {
    const shown = await page.evaluate(async ([sel, where]) => {
      const el = document.querySelector(sel as string)!;
      el.scrollTop = where === "top" ? 0 : el.scrollHeight;
      for (let i = 0; i < 2; i++) await new Promise((r) => requestAnimationFrame(r));
      const r = el.getBoundingClientRect();
      return !!document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)?.closest(".vrow, .tr > :not(.vspace)");
    }, [scroller, to] as const);
    expect(shown, `nothing in sight two frames after going to the ${to}`).toBe(true);
    await settle();
  }
  // Read to its start, the conversation still draws a window of it.
  expect(await page.locator(".tr *").count()).toBeLessThan(2500);
});

test("going back to a long conversation draws its window, not all of it", async () => {
  // Another session first: the list's first short one.
  const other = (await page.locator(".srow .stitle").allTextContents()).find((t) => t !== titles.bigIdle && t !== titles.bigLive)!;
  await open(other);
  await page.evaluate(() => {
    const w = window as unknown as { __most: number };
    w.__most = 0;
    new MutationObserver(() => (w.__most = Math.max(w.__most, document.querySelectorAll(".tr > .vrow").length))).observe(document.body, { childList: true, subtree: true });
  });
  await open(titles.bigIdle);
  await page.waitForTimeout(800);
  const most = await page.evaluate(() => (window as unknown as { __most: number }).__most);
  expect(most, `${most} rows drawn on going back, of ${await heldRows()}`).toBeLessThan(80);
});

test("a growing conversation follows its end; scrolled up it stays put until the button takes it back; a finished reply keeps its table", async () => {
  test.setTimeout(150_000);
  daemon.live(true);
  try {
    await open(titles.bigLive);
    await expect(page.getByTestId("thread-composer")).toBeVisible();
    await settle();
    // Following: events every 250 ms and a reply written as drafts, the end always in view.
    for (let i = 0; i < 10; i++) {
      await page.waitForTimeout(300);
      expect(await below(), "the conversation grew past the view").toBeLessThan(80);
    }
    // A reply being written keeps its tables once stored: the same elements. The last row is the reply being
    // written while the last item in the projection is still streaming; its tables are marked then, and
    // looked for again once the next item came (the reply stored).
    const lastItem = () => daemon.world.projections.get(ids.bigLive)!.items.at(-1)!;
    let key = "";
    await expect
      .poll(
        async () => {
          const k = await page.evaluate(() => {
            const row = document.querySelector<HTMLElement>(".tr > .vrow:last-child");
            if (!row?.querySelector("table")) return "";
            row.querySelectorAll("table").forEach((t) => ((t as unknown as { __kept: boolean }).__kept = true));
            return row.dataset.stepKey!;
          });
          const it = lastItem();
          if (k && it.streaming && BigInt(k) === it.seq * 4n) key = k;
          return key;
        },
        { timeout: 30_000 },
      )
      .not.toBe("");
    await expect(page.locator(`.vrow[data-step-key="${BigInt(key) + 4n}"]`)).toBeAttached({ timeout: 30_000 });
    const kept = await page.locator(`.vrow[data-step-key="${key}"] table`).evaluateAll((els) => els.some((el) => (el as unknown as { __kept?: boolean }).__kept === true));
    expect(kept, "the table was drawn anew when its reply was stored").toBe(true);
    await expect(page.locator(`.vrow[data-step-key="${key}"] .md-table-tools`).first().locator("button")).toHaveCount(2);
    // Up while it grows: it stays where the person went, and the way back shows.
    await wheel(-240, 6);
    await settle(); // a slow runner may scroll by the last tick later
    const away = await scrollTop();
    await page.waitForTimeout(3000);
    expect(await below()).toBeGreaterThan(240);
    expect(Math.abs((await scrollTop()) - away), "what the person read moved").toBeLessThan(80);
    await expect(page.locator(".tobottom.on")).toBeVisible();
    await page.locator(".tobottom").click();
    await expect.poll(below).toBeLessThan(80);
    await page.waitForTimeout(2000);
    expect(await below(), "back at the end, it follows again").toBeLessThan(80);
    await expect(page.locator(".tobottom.on")).toHaveCount(0);
  } finally {
    daemon.live(false);
  }
});

test("a conversation whose replies hold tall diagrams: rows that grow at its end, and going back to it, leave nothing in sight blank", async () => {
  test.setTimeout(90_000);
  // Five turns, the replies of the second and third ending in a diagram taller than the view: a diagram is drawn at
  // the height of its "rendering" note first and at its own a moment later, while the view follows the end. Those
  // two rows (8 and 12) sit before the last step of the window (16), which they must not fall out of.
  await (await sessionList(page)).locator(".srow").first().waitFor();
  const shown = (await page.locator(".srow .stitle").allTextContents()).filter((t) => t !== titles.bigIdle && t !== titles.bigLive);
  const [other, title] = [shown[0], shown.at(-1)!];
  const s = [...daemon.world.sessions.values()].find((x) => x.title === title)!;
  // Each time with diagrams not drawn before, which take a moment to draw.
  const diagrams = (round: number) => {
    const proj = conversation(s, rng(5), 5, 4, Date.now() - 3_600_000, true);
    const chain = ["flowchart TD", ...Array.from({ length: 28 }, (_, i) => `  n${i}[步骤 ${round}.${i}] --> n${i + 1}[步骤 ${round}.${i + 1}]`)].join("\n");
    for (const it of proj.items.filter((x) => /^第 [23] 轮：我先看了一下/.test(x.text))) it.text += `\n\n\`\`\`mermaid\n${chain}\n\`\`\`\n`;
    s.progress!.lastEventSeq = proj.items.at(-1)!.seq;
    daemon.world.projections.set(s.sessionId, proj);
  };
  diagrams(0);
  await open(title);
  await expect(page.locator(".md-diagram-svg").first()).toBeAttached({ timeout: 30_000 });
  // At its end, the third turn's reply (row 12) grows by more than two views and the view goes to the new end at
  // once, as following the end does. The scroll comes before the row's new size is reported (a frame runs its
  // scrolls before its size observers): the window is placed by the sizes on the page, not those reported last.
  await settle();
  await expect(page.locator(".tr > .vrow", { hasText: "第 3 轮：我先看了一下" })).toHaveCount(1);
  const grown = await page.evaluate(async (sel) => {
    const el = document.querySelector(sel)!;
    const row = [...document.querySelectorAll<HTMLElement>(".tr > .vrow")].find((r) => r.textContent!.includes("第 3 轮：我先看了一下"))!;
    row.style.paddingBottom = `${3 * el.clientHeight}px`;
    el.scrollTop = el.scrollHeight;
    const out: { blank: boolean; drawn: boolean }[] = [];
    for (let i = 0; i < 30; i++) {
      await new Promise((r) => requestAnimationFrame(r));
      const r = el.getBoundingClientRect();
      out.push({
        blank: [0.1, 0.5].some((at) => document.elementFromPoint(r.left + r.width / 2, r.top + r.height * at)?.closest(".vspace")),
        drawn: row.isConnected,
      });
    }
    row.style.paddingBottom = "";
    return out;
  }, scroller);
  expect(grown.filter((f) => f.blank).length, "frames with a space in sight as a row grew").toBe(0);
  expect(grown.every((f) => f.drawn), "the reply that grew stopped being drawn").toBe(true);
  expect(await below()).toBeLessThan(80);

  // Each time back, every frame from the click on: whether the top or the middle of the view shows a space for rows
  // not drawn, and the first row drawn. How the rows' sizes and the scrolls meet varies, so it goes back a few times.
  for (let round = 1; round <= 3; round++) {
    await open(other);
    diagrams(round);
    await page.evaluate((sel) => {
      const w = window as unknown as { __frames: [string, string][] };
      w.__frames = [];
      const t0 = performance.now();
      const tick = () => {
        const el = document.querySelector(sel);
        const first = document.querySelector<HTMLElement>(".tr > .vrow")?.dataset.stepKey;
        if (el && first) {
          const r = el.getBoundingClientRect();
          const blank = [0.1, 0.5].some((at) => document.elementFromPoint(r.left + r.width / 2, r.top + r.height * at)?.closest(".vspace"));
          w.__frames.push([blank ? "blank" : "row", first]);
        }
        if (performance.now() - t0 < 3000) requestAnimationFrame(tick);
      };
      requestAnimationFrame(tick);
    }, scroller);
    await open(title);
    await page.waitForTimeout(3500);
    const frames = await page.evaluate(() => (window as unknown as { __frames: [string, string][] }).__frames);
    const blank = frames.filter(([what]) => what === "blank").length;
    const moves = frames.filter((f, i) => i > 0 && f[1] !== frames[i - 1][1]).length;
    expect(frames.length).toBeGreaterThan(60);
    expect(blank, `back ${round}: ${blank} of ${frames.length} frames with a space in sight`).toBe(0);
    expect(moves, `back ${round}: the window moved ${moves} times`).toBeLessThan(6);
    expect(await below()).toBeLessThan(80);
  }
});
