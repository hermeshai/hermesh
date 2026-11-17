import { expect, test, type ElectronApplication, type Page } from "@playwright/test";
import { launch, sessionList } from "./app";

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
  for (const n of ["wc-sweep", "lm-beat", "lm-ring", "lm-breathe", "ping"]) expect(names, n).toContain(n);

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
  const running = page.getByRole("region", { name: "正在运行" }).getByRole("listitem").filter({ hasText: "跑很久" });
  await expect(running.locator(".wc-time")).toHaveText(/^\d+:\d\d$/);
  await page.waitForTimeout(1500); // the first lists and the usage are in
  let batches = await changes(page, 3200);
  expect(batches.flat().length).toBeGreaterThan(0);
  for (const c of batches.flat()) expect(c).toMatch(/^wc-time characterData$/);
  // The sidebar beside it has no clock while its rows are titles alone (docs/her-team.md §16). With their
  // metadata, a running row's clock ticks with the card's.
  await page.getByRole("button", { name: "视图选项" }).click();
  await page.getByRole("menu", { name: "视图选项" }).getByRole("menuitem", { name: /显示/ }).hover();
  await page.getByRole("menu", { name: "显示" }).getByRole("menuitemcheckbox", { name: "元数据" }).click();
  const row = (await sessionList(page)).getByRole("listitem").filter({ hasText: "跑很久" });
  await row.click();
  await expect(row.locator(".stime")).toHaveText(/^\d+:\d\d$/);
  // Opening the session also starts the syntax worker; its reply is not a clock tick.
  await page.locator(".sm2.cmd .hl").first().waitFor({ timeout: 4000 }).catch(() => {});
  await page.waitForTimeout(1500);
  batches = await changes(page, 3200);
  expect(batches.flat().length).toBeGreaterThan(0);
  for (const b of batches) expect(b.some((c) => c.startsWith("stime")), b.join(", ")).toBe(true);

  // The running session's page: its clocks only (the bar's, the card's, the list's).
  await (await sessionList(page)).getByText("跑很久", { exact: true }).click();
  await expect(page.getByRole("region", { name: "这一轮" })).toContainText("正在工作");
  // The command's colours come from a worker, a moment after its text
  // (Code.tsx): they are in, or the worker has given up (2.5 s), before the
  // page is watched.
  await page.locator(".sm2.cmd .hl").first().waitFor({ timeout: 4000 }).catch(() => {});
  await page.waitForTimeout(1000);
  for (const c of (await changes(page, 2200)).flat()) expect(c).toMatch(/^(el|insp-time|stime) characterData$/);
  await app.close();
});

/** Watches for `sel` to start going (`.leaving`): its animations then, whether it takes input, and how long until it is gone. */
async function watchLeaving(page: Page, sel: string) {
  await page.evaluate((sel) => {
    type Seen = { inert: boolean; anims: { name: string; props: string[] }[]; gone: number };
    const w = window as unknown as { leaving?: Seen };
    delete w.leaving;
    let from = 0;
    const mo = new MutationObserver(() => {
      const el = document.querySelector<HTMLElement>(`${sel}.leaving`);
      if (el && !w.leaving) {
        from = performance.now();
        w.leaving = {
          inert: el.inert,
          anims: el.getAnimations().map((a) => ({
            name: (a as CSSAnimation).animationName,
            props: [...new Set((a.effect as KeyframeEffect).getKeyframes().flatMap((k) => Object.keys(k)))].filter((p) => !["offset", "computedOffset", "easing", "composite"].includes(p)),
          })),
          gone: -1,
        };
      } else if (w.leaving && w.leaving.gone < 0 && !document.querySelector(sel)) {
        w.leaving.gone = performance.now() - from;
        mo.disconnect();
      }
    });
    mo.observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ["class"] });
  }, sel);
}

/** What watchLeaving saw, once `sel` is gone. */
async function left(page: Page) {
  type Seen = { inert: boolean; anims: { name: string; props: string[] }[]; gone: number };
  await expect.poll(() => page.evaluate(() => (window as unknown as { leaving?: Seen }).leaving?.gone ?? -1)).toBeGreaterThan(0);
  return page.evaluate(() => (window as unknown as { leaving: Seen }).leaving);
}

test("what comes in goes the way it came, quicker, and takes no input meanwhile: a sheet, a toast and the search", async () => {
  const { app, page } = await launch();
  await expect(await sessionList(page)).toBeVisible();
  const command = (c: string) =>
    (app as ElectronApplication).evaluate((_, c) => (globalThis as unknown as { hermeshTest: { mainWindow(): { webContents: { send(ch: string, c: string): void } } } }).hermeshTest.mainWindow().webContents.send("command", c), c);

  // A sheet's copy brings a toast; it goes on its own after a while.
  await command("addMachine");
  const sheet = page.getByRole("dialog");
  await sheet.getByRole("group", { name: "安装方式" }).getByRole("button", { name: "终端命令" }).click();
  await watchLeaving(page, ".toast");
  await sheet.getByRole("button", { name: "拷贝" }).click();
  await expect(page.locator(".toast")).toHaveText("已拷贝");
  let seen = await left(page);
  expect(seen.inert).toBe(true);
  expect(seen.anims).toEqual([{ name: "toast-out", props: ["opacity", "transform"] }]);
  expect(seen.gone).toBeGreaterThan(100);
  expect(seen.gone).toBeLessThan(1000);

  // The sheet, closed with Esc: it rises a little as it fades, as it came down; the scrim fades with it.
  await watchLeaving(page, ".sheet");
  await page.keyboard.press("Escape");
  seen = await left(page);
  expect(seen.inert).toBe(true);
  expect(seen.anims).toEqual([{ name: "sheet-out", props: ["opacity", "transform"] }]);
  await expect(page.locator(".sheet-scrim")).toHaveCount(0);

  // The search, closed with Esc: it shrinks a little as it fades, as it grew in.
  await page.locator(".side-top").getByRole("button", { name: "搜索" }).click();
  await expect(page.getByRole("dialog", { name: "搜索任务" }).getByRole("textbox")).toBeFocused();
  await watchLeaving(page, ".pal");
  await page.keyboard.press("Escape");
  seen = await left(page);
  expect(seen.inert).toBe(true);
  expect(seen.anims).toEqual([{ name: "pal-out", props: ["opacity", "transform"] }]);
  await expect(page.locator(".scrim")).toHaveCount(0);
  await app.close();
});
