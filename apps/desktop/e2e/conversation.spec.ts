import { expect, test, type ElectronApplication, type Page } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { startDaemon, type Daemon } from "../perf/daemon";
import { ids, titles } from "../perf/world";
import { launch } from "./app";

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
  await page.setViewportSize({ width: 1240, height: 860 });
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
    await expect(page.locator(".cbar")).toBeVisible();
    await settle();
    // Following: events every 250 ms and a reply written as drafts, the end always in view.
    for (let i = 0; i < 10; i++) {
      await page.waitForTimeout(300);
      expect(await below(), "the conversation grew past the view").toBeLessThan(80);
    }
    // A reply being written keeps its tables once stored: the same elements. The last row is the reply being
    // written while its key is the event after the last one the daemon stored; its tables are marked then, and
    // looked for again once the next event came (the reply stored).
    const stored = () => daemon.world.events.get(ids.bigLive)!;
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
          if (k && BigInt(k) === stored().at(-1)!.seq + 1n) key = k;
          return key;
        },
        { timeout: 30_000 },
      )
      .not.toBe("");
    await expect(page.locator(`.vrow[data-step-key="${BigInt(key) + 1n}"]`)).toBeAttached({ timeout: 30_000 });
    const kept = await page.locator(`.vrow[data-step-key="${key}"] table`).evaluateAll((els) => els.some((el) => (el as unknown as { __kept?: boolean }).__kept === true));
    expect(kept, "the table was drawn anew when its reply was stored").toBe(true);
    await expect(page.locator(`.vrow[data-step-key="${key}"] .md-table-tools`).first().locator("button")).toHaveCount(2);
    // Up while it grows: it stays where the person went, and the way back shows.
    await wheel(-240, 6);
    await page.waitForTimeout(300);
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
