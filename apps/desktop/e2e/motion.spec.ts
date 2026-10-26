import { expect, test, type Page } from "@playwright/test";
import { launch } from "./app";

// What moves on the main window, and that the page itself does no work for
// it: every animation is one the compositor runs (transform and opacity
// only), a comet rides its session's link exactly as the link is drawn,
// and while nothing happens the only changes to the page are its clocks,
// all ticking in the same frame.

/** The page's changes over ms: [element class, kind] of each, by the batch (task) they came in. */
async function changes(page: Page, ms: number): Promise<string[][]> {
  return page.evaluate(
    (ms) =>
      new Promise<string[][]>((res) => {
        const batches: string[][] = [];
        const mo = new MutationObserver((ms) =>
          batches.push(
            ms.map((m) => {
              const el = m.target instanceof Element ? m.target : m.target.parentElement;
              return `${el?.classList[0] ?? el?.tagName} ${m.type}`;
            }),
          ),
        );
        mo.observe(document.body, { subtree: true, childList: true, attributes: true, characterData: true });
        setTimeout(() => (mo.disconnect(), res(batches)), ms);
      }),
    ms,
  );
}

test("the overview's motion is all the compositor's: comets ride the running session's link, and a question pings", async () => {
  const { app, page } = await launch();
  const map = page.getByRole("region", { name: "链路" });
  // One running session: two comets of a head and five tail dots each.
  await expect(map.locator(".lm-comet")).toHaveCount(2);
  await expect(map.locator(".lm-comet i")).toHaveCount(12);
  await expect(map.locator(".lmap-pings .ping")).toHaveCount(1); // 选颜色 waits on its question
  await expect.poll(() => page.evaluate(() => document.getAnimations().length)).toBeGreaterThan(12);

  const animated = await page.evaluate(() =>
    document.getAnimations().map((a) => ({
      name: a instanceof CSSAnimation ? a.animationName : "script",
      props: [...new Set((a.effect as KeyframeEffect).getKeyframes().flatMap((k) => Object.keys(k)))].filter((p) => !["offset", "computedOffset", "easing", "composite"].includes(p)),
    })),
  );
  for (const a of animated) expect(a.props.every((p) => p === "transform" || p === "opacity"), `${a.name}: ${a.props}`).toBe(true);
  const names = new Set(animated.map((a) => a.name));
  for (const n of ["wc-sweep", "lm-beat", "lm-ring", "lm-breathe", "ping", "stitle-run", "stitle-run-back"]) expect(names, n).toContain(n);

  // Each dot where the link puts it: u of the way along (the document's time over the comet's
  // duration, plus its phase), less its place in the tail; faded in and out at the ends.
  for (let i = 0; i < 4; i++) {
    const off = await page.evaluate(() => {
      const flows = [...document.querySelectorAll<SVGPathElement>(".lmap-back .lm-flow")];
      const t = document.timeline.currentTime as number;
      let where = 0;
      let fade = 0;
      for (const g of document.querySelectorAll<HTMLElement>(".lm-comet")) {
        const p = flows[Number(g.dataset.i)];
        const u = (t / Number(g.dataset.dur) + Number(g.dataset.o)) % 1;
        for (const c of g.querySelectorAll<HTMLElement>("i")) {
          const want = p.getPointAtLength(Math.max(0, u - Number(c.dataset.t) * 0.011) * p.getTotalLength());
          const m = new DOMMatrix(getComputedStyle(c).transform);
          where = Math.max(where, Math.hypot(m.e - want.x, m.f - want.y));
          fade = Math.max(fade, Math.abs(Number(getComputedStyle(c).opacity) - Number(c.dataset.a) * Math.min(1, Math.min(u, 1 - u) * 10)));
        }
      }
      return { where, fade };
    });
    expect(off.where).toBeLessThan(0.15);
    expect(off.fade).toBeLessThan(0.002);
    await page.waitForTimeout(450);
  }
  await app.close();
});

test("while nothing happens, the page changes only its clocks, all in the same frame", async () => {
  const { app, page } = await launch();
  const running = page.getByRole("region", { name: "正在干活" }).getByRole("listitem").filter({ hasText: "跑很久" });
  await expect(running.locator(".wc-time")).toHaveText(/^\d+:\d\d$/);
  // The card ticks while the turn is still queued or preparing; the list
  // row only once it runs. Wait for both clocks.
  const row = page.getByRole("list", { name: "会话" }).getByRole("listitem").filter({ hasText: "跑很久" });
  await expect(row.locator(".stime")).toHaveText(/^\d+:\d\d$/);
  await page.waitForTimeout(1500); // the first lists and the usage are in
  const batches = await changes(page, 3200);
  const all = batches.flat();
  expect(all.length).toBeGreaterThan(0);
  for (const c of all) expect(c).toMatch(/^(wc-time|stime) characterData$/);
  // The card's clock and the list row's tick together.
  for (const b of batches) expect(b.some((c) => c.startsWith("wc-time")) && b.some((c) => c.startsWith("stime")), b.join(", ")).toBe(true);

  // The running session's page: its clocks only (the bar's, the card's, the list's).
  await page.getByRole("list", { name: "会话" }).getByText("跑很久", { exact: true }).click();
  await expect(page.getByRole("region", { name: "这一轮" })).toContainText("正在工作");
  await page.waitForTimeout(1000);
  for (const c of (await changes(page, 2200)).flat()) expect(c).toMatch(/^(el|insp-time|stime) characterData$/);
  await app.close();
});
