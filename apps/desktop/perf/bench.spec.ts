import { _electron as electron, test, type CDPSession, type ElectronApplication, type Page } from "@playwright/test";
import { execFile } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import { startDaemon, type Daemon } from "./daemon";
import { titles } from "./world";

// The main window's performance against a heavy owner's world (world.ts),
// as docs/desktop-performance.md defines the metrics: startup, how fast
// the window answers, what it costs while it shows live sessions, and its
// memory. Each number is a median over PERF_REPS runs. Results go to
// perf/results/<PERF_LABEL>.json, and the table to the console.

const out = process.env.HERMESH_DESKTOP_OUT || path.join(import.meta.dirname, "..", "out");
const label = process.env.PERF_LABEL ?? "run";
const reps = Number(process.env.PERF_REPS ?? 5);
const window = Number(process.env.PERF_SECONDS ?? 15) * 1000;
// Chromium stops drawing a window that is covered; the benchmark's must draw.
const flags = ["--disable-backgrounding-occluded-windows", "--disable-renderer-backgrounding"];

type Ctx = { app: ElectronApplication; page: Page; cdp: CDPSession };
const results: Record<string, number> = {};
const record = (k: string, v: number) => {
  results[k] = Math.round(v * 10) / 10;
  console.log(`${k.padEnd(44)} ${results[k]}`);
};
const median = (xs: number[]) => [...xs].sort((a, b) => a - b)[Math.floor(xs.length / 2)];
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function launch(main: string, socket: string, profile: string): Promise<Ctx & { launched: number }> {
  const launched = Date.now();
  const app = await electron.launch({ args: [...flags, main], env: { ...process.env, HERMESH_SOCKET: socket, HERMESH_USER_DATA: profile } });
  const page = await app.firstWindow();
  const cdp = await page.context().newCDPSession(page);
  return { app, page, cdp, launched };
}

/**
 * Wall time from launch to the frame after the overview has its data drawn
 * (an observer in the page, there from DOMContentLoaded, so before any data),
 * and how much of it is the renderer's own (from its navigation start).
 */
async function ready(c: Ctx & { launched: number }, selectors: string[]) {
  await c.page.waitForLoadState("domcontentloaded");
  const r = await c.page.evaluate(
    (sels) =>
      new Promise<{ at: number; origin: number }>((res) => {
        const met = () => sels.every((s) => document.querySelector(s));
        const done = () => requestAnimationFrame(() => setTimeout(() => res({ at: performance.now(), origin: performance.timeOrigin })));
        if (met()) return done();
        const mo = new MutationObserver(() => met() && (mo.disconnect(), done()));
        mo.observe(document, { subtree: true, childList: true, attributes: true });
      }),
    selectors,
  );
  return { total: r.origin + r.at - c.launched, renderer: r.at };
}

const overviewReady = [".srow", ".lmap .lm-node.m", ".wcard", ".u-cells .l1"];

type Probe = { app: ElectronApplication; cdp: CDPSession };
async function cpu(c: Probe) {
  return c.app.evaluate(({ app }) => app.getAppMetrics().map((m) => ({ pid: m.pid, type: m.type, cpu: m.cpu.cumulativeCPUUsage ?? 0, wake: m.cpu.idleWakeupsPerSecond })));
}
async function metrics(c: Probe): Promise<Record<string, number>> {
  const { metrics } = (await c.cdp.send("Performance.getMetrics")) as { metrics: { name: string; value: number }[] };
  return Object.fromEntries(metrics.map((m) => [m.name, m.value]));
}

/**
 * A window of steady state: the renderer's main thread busy (all tasks, of
 * which script and style+layout), each process type's CPU and idle wakeups,
 * and long animation frames (over 50 ms).
 */
async function steady(c: Ctx, name: string, ms = window) {
  await c.cdp.send("Performance.enable", { timeDomain: "timeTicks" });
  await c.page.evaluate(() => {
    const w = window as unknown as { __loaf: number[]; __loafObs?: PerformanceObserver; __mut: Map<string, number>; __mutObs?: MutationObserver };
    w.__loaf = [];
    w.__loafObs = new PerformanceObserver((l) => l.getEntries().forEach((e) => w.__loaf.push(e.duration)));
    w.__loafObs.observe({ type: "long-animation-frame" });
    // DOM changes, by the element they were in (its tag and first class).
    w.__mut = new Map();
    w.__mutObs = new MutationObserver((ms) =>
      ms.forEach((m) => {
        const el = m.target instanceof Element ? m.target : m.target.parentElement;
        const k = `${m.type} ${el?.tagName.toLowerCase()}.${el?.classList[0] ?? ""}${m.attributeName ? ` [${m.attributeName}]` : ""}`;
        w.__mut.set(k, (w.__mut.get(k) ?? 0) + 1);
      }),
    );
    w.__mutObs.observe(document.body, { subtree: true, childList: true, attributes: true, characterData: true });
  });
  // PERF_PROFILE=<window>[,…]: a CPU profile of the renderer over that window, to results/.
  const profile = (process.env.PERF_PROFILE ?? "").split(",").includes(name);
  if (profile) {
    await c.cdp.send("Profiler.enable");
    await c.cdp.send("Profiler.setSamplingInterval", { interval: 200 });
    await c.cdp.send("Profiler.start");
  }
  // PERF_TRACE=<window>[,…]: the renderer main thread's work by kind over that window, to results/.
  const trace = (process.env.PERF_TRACE ?? "").split(",").includes(name);
  const events: { name: string; ph: string; dur?: number; tid: number; pid: number; args?: { name?: string } }[] = [];
  if (trace) {
    c.cdp.on("Tracing.dataCollected", (e) => events.push(...(e.value as unknown as typeof events)));
    await c.cdp.send("Tracing.start", { categories: "devtools.timeline,disabled-by-default-devtools.timeline,blink,cc,viz", transferMode: "ReportEvents" });
  }
  await cpu(c); // resets the wakeup counters
  const m0 = await metrics(c);
  const p0 = await cpu(c);
  const t0 = Date.now();
  await sleep(ms);
  const m1 = await metrics(c);
  const p1 = await cpu(c);
  const dt = (Date.now() - t0) / 1000;
  if (trace) {
    const done = new Promise<void>((r) => c.cdp.once("Tracing.tracingComplete", () => r()));
    await c.cdp.send("Tracing.end");
    await done;
    const main = events.find((e) => e.name === "thread_name" && e.args?.name === "CrRendererMain");
    const by = new Map<string, [number, number]>();
    for (const e of events) {
      if (!main || e.tid !== main.tid || e.pid !== main.pid || e.ph !== "X" || !e.dur) continue;
      const [ms, n] = by.get(e.name) ?? [0, 0];
      by.set(e.name, [ms + e.dur / 1000, n + 1]);
    }
    const lines = [...by].sort((a, b) => b[1][0] - a[1][0]).slice(0, 40).map(([k, [ms, n]]) => `${ms.toFixed(1).padStart(9)} ms ${String(n).padStart(6)}×  ${k}`);
    fs.mkdirSync(path.join(import.meta.dirname, "results"), { recursive: true });
    fs.writeFileSync(path.join(import.meta.dirname, "results", `${label}-${name}.trace.txt`), lines.join("\n") + "\n");
  }
  if (profile) {
    const { profile: p } = (await c.cdp.send("Profiler.stop")) as { profile: unknown };
    fs.mkdirSync(path.join(import.meta.dirname, "results"), { recursive: true });
    fs.writeFileSync(path.join(import.meta.dirname, "results", `${label}-${name}.cpuprofile`), JSON.stringify(p));
  }
  const [loaf, mut] = await c.page.evaluate(() => {
    const w = window as unknown as { __loaf: number[]; __loafObs: PerformanceObserver; __mut: Map<string, number>; __mutObs: MutationObserver };
    w.__loafObs.disconnect();
    w.__mutObs.disconnect();
    return [w.__loaf, [...w.__mut].sort((a, b) => b[1] - a[1])] as const;
  });
  if (process.env.PERF_MUTATIONS) console.log(name, mut.slice(0, 12));
  const d = (k: string) => ((m1[k] ?? 0) - (m0[k] ?? 0)) / dt;
  record(`${name}.renderer.busy%`, d("TaskDuration") * 100);
  record(`${name}.renderer.script%`, d("ScriptDuration") * 100);
  record(`${name}.renderer.styleLayout%`, (d("RecalcStyleDuration") + d("LayoutDuration")) * 100);
  record(`${name}.renderer.layouts/s`, d("LayoutCount"));
  const by = (type: string, f: "cpu" | "wake") => {
    const pids = p1.filter((p) => p.type === type);
    if (f === "wake") return pids.reduce((n, p) => n + p.wake, 0);
    return pids.reduce((n, p) => n + p.cpu - (p0.find((q) => q.pid === p.pid)?.cpu ?? p.cpu), 0) / dt;
  };
  const total = p1.reduce((n, p) => n + p.cpu - (p0.find((q) => q.pid === p.pid)?.cpu ?? p.cpu), 0) / dt;
  record(`${name}.cpu.renderer%`, by("Tab", "cpu") * 100);
  record(`${name}.cpu.gpu%`, by("GPU", "cpu") * 100);
  record(`${name}.cpu.main%`, by("Browser", "cpu") * 100);
  record(`${name}.cpu.total%`, total * 100);
  record(`${name}.wakeups.renderer/s`, by("Tab", "wake"));
  record(`${name}.mutations/s`, mut.reduce((n, [, v]) => n + v, 0) / dt);
  record(`${name}.loaf>50ms`, loaf.length);
  record(`${name}.loaf.max`, Math.max(0, ...loaf));
}

/** How freely the main thread produces frames: rAF rate and frames over 1.5 vsync. */
async function frames(c: Ctx, name: string, ms = 3000) {
  const f = await c.page.evaluate(
    (ms) =>
      new Promise<{ fps: number; slow: number; p99: number }>((r) => {
        const t: number[] = [];
        const step = (x: number) => {
          t.push(x);
          if (x - t[0] < ms) requestAnimationFrame(step);
          else {
            const iv = t.slice(1).map((v, i) => v - t[i]).sort((a, b) => a - b);
            const vsync = iv[Math.floor(iv.length / 2)];
            r({ fps: (iv.length * 1000) / (t.at(-1)! - t[0]), slow: iv.filter((v) => v > vsync * 1.5).length, p99: iv[Math.floor(iv.length * 0.99)] });
          }
        };
        requestAnimationFrame(step);
      }),
    ms,
  );
  record(`${name}.fps`, f.fps);
  record(`${name}.slowFrames`, f.slow);
  record(`${name}.frame.p99`, f.p99);
}

/**
 * Click (a real pointer) on target, then the time from its pointerdown to
 * the frame after done (selectors, each with text in it if given) appears.
 */
type Done = { sel: string; text?: string };
async function timed(page: Page, click: () => Promise<void>, ...done: Done[]): Promise<number> {
  await page.evaluate((done) => {
    const met = () =>
      done.every((d) => {
        const el = document.querySelector(d.sel);
        return !!el && (!d.text || (el.textContent ?? "").includes(d.text));
      });
    (window as unknown as { __t: Promise<number> }).__t = new Promise((res) =>
      document.addEventListener(
        "pointerdown",
        (e) => {
          const t0 = e.timeStamp;
          const finish = () => requestAnimationFrame(() => setTimeout(() => res(performance.now() - t0)));
          // After this event's own handlers.
          setTimeout(() => {
            if (met()) return finish();
            const mo = new MutationObserver(() => met() && (mo.disconnect(), finish()));
            mo.observe(document.body, { childList: true, subtree: true, characterData: true });
          });
        },
        { once: true, capture: true },
      ),
    );
  }, done);
  await click();
  return page.evaluate(() => (window as unknown as { __t: Promise<number> }).__t);
}

const row = (page: Page, title: string) => page.locator(".srow", { has: page.locator(".stitle", { hasText: title }) }).first();
const toOverview = (page: Page) => page.locator(".nav-i", { hasText: "概览" }).click();

/** Event Timing (what Chromium measures from input to the next frame) for everything since observe(). */
async function observeEvents(page: Page) {
  await page.evaluate(() => {
    const w = window as unknown as { __ev: { name: string; id: number; d: number }[] };
    w.__ev = [];
    new PerformanceObserver((l) =>
      l.getEntries().forEach((e) => {
        const x = e as PerformanceEventTiming & { interactionId: number };
        if (x.interactionId) w.__ev.push({ name: x.name, id: x.interactionId, d: x.duration });
      }),
    ).observe({ type: "event", durationThreshold: 16, buffered: false } as PerformanceObserverInit);
  });
}
async function eventDurations(page: Page): Promise<number[]> {
  await sleep(300); // entries arrive after their frame
  return page.evaluate(() => {
    const w = window as unknown as { __ev: { name: string; id: number; d: number }[] };
    const by = new Map<number, number>();
    for (const e of w.__ev) by.set(e.id, Math.max(by.get(e.id) ?? 0, e.d));
    w.__ev = [];
    return [...by.values()];
  });
}

async function memory(c: Ctx, name: string) {
  await c.cdp.send("HeapProfiler.collectGarbage");
  await sleep(500);
  await c.cdp.send("HeapProfiler.collectGarbage");
  const heap = (await c.cdp.send("Runtime.getHeapUsage")) as { usedSize: number };
  const dom = (await c.cdp.send("Memory.getDOMCounters")) as { nodes: number; jsEventListeners: number };
  record(`${name}.heap.MB`, heap.usedSize / 2 ** 20);
  record(`${name}.domNodes`, dom.nodes);
  record(`${name}.listeners`, dom.jsEventListeners);
  await footprint(c.app, name);
}

/** macOS's footprint (Activity Monitor's 内存) of every process of the app, in MB. */
async function footprint(app: ElectronApplication, name: string) {
  const procs = await app.evaluate(({ app }) => app.getAppMetrics().map((m) => ({ pid: m.pid, type: m.type })));
  const args = procs.flatMap((p) => ["-p", String(p.pid)]);
  const { stdout } = await promisify(execFile)("footprint", ["-f", "bytes", ...args]);
  const bytes = new Map([...stdout.matchAll(/\[(\d+)\].*?Footprint: (\d+) B/g)].map((m) => [Number(m[1]), Number(m[2])]));
  const sum = (type?: string) => procs.filter((p) => !type || p.type === type).reduce((n, p) => n + (bytes.get(p.pid) ?? 0), 0) / 2 ** 20;
  record(`${name}.footprint.total.MB`, sum());
  record(`${name}.footprint.renderer.MB`, sum("Tab"));
  record(`${name}.footprint.main.MB`, sum("Browser"));
  record(`${name}.footprint.gpu.MB`, sum("GPU"));
}

/** Scrolls the sidebar to its end until every page of sessions is loaded. */
async function loadAll(page: Page) {
  for (let i = 0; i < 40; i++) {
    if (await page.locator(".side-list .endrow", { hasText: "没有更早的会话了" }).count()) return;
    await page.locator(".side-list").evaluate((el) => (el.scrollTop = el.scrollHeight));
    await sleep(150);
  }
  throw new Error("the sidebar never loaded every session");
}

test("benchmark", async () => {
  const socket = path.join("/tmp", `hmperf-${process.pid}.sock`);
  const daemon: Daemon = await startDaemon(socket);
  const main = path.join(out, "main", "index.js");
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "hmperf-"));
  try {
    // The floor: an empty window of the same kind.
    {
      const starts: number[] = [];
      const own: number[] = [];
      const profile = path.join(tmp, "floor");
      for (let i = 0; i <= reps; i++) {
        const c = await launch(path.join(import.meta.dirname, "floor", "main.mjs"), socket, profile);
        const t = await ready(c, [".srow"]);
        if (i > 0) {
          // The first run makes the profile.
          starts.push(t.total);
          own.push(t.renderer);
        }
        if (i === reps) {
          await sleep(3000);
          await steady(c, "floor.idle", 10_000);
          await memory(c, "floor");
          // The least that anything moving costs: one small compositor animation.
          await c.page.evaluate(() => document.querySelector(".srow")!.animate([{ transform: "scale(1)" }, { transform: "scale(2)" }], { duration: 2000, iterations: Infinity }));
          await sleep(1000);
          await steady(c, "floor.anim", 10_000);
        }
        await c.app.close();
      }
      record("floor.start.ms", median(starts));
      record("floor.start.renderer.ms", median(own));
    }

    // PERF_AB=<out A>,<out B>: startup of two builds, launched in turn (so
    // both see the same machine), and the median of B − A over the pairs.
    if (process.env.PERF_AB) {
      const outs = process.env.PERF_AB.split(",");
      const t: number[][] = outs.map(() => []);
      for (let i = 0; i <= reps * 3; i++) {
        for (const [k, o] of outs.entries()) {
          const c = await launch(path.join(o, "main", "index.js"), socket, path.join(tmp, `ab${k}`));
          const r = await ready(c, overviewReady);
          if (i > 0) t[k].push(r.renderer);
          await c.app.close();
        }
      }
      record("ab.A.start.renderer.ms", median(t[0]));
      record("ab.B.start.renderer.ms", median(t[1]));
      record("ab.B-A.ms", median(t[1].map((v, i) => v - t[0][i])));
      return;
    }

    // Startup, with a profile that exists (the first launch makes it).
    const profile = path.join(tmp, "app");
    {
      const total: number[] = [];
      const renderer: number[] = [];
      for (let i = 0; i <= reps * 2; i++) {
        const c = await launch(main, socket, profile);
        const t = await ready(c, overviewReady);
        if (i > 0) {
          total.push(t.total);
          renderer.push(t.renderer);
        }
        await c.app.close();
      }
      record("start.ms", median(total));
      record("start.renderer.ms", median(renderer));
    }

    if (process.env.PERF_ONLY === "start") return;
    const c = await launch(main, socket, profile);
    const { page } = c;
    await ready(c, overviewReady);
    await sleep(2000);
    await memory(c, "fresh");

    // Every session in the list (a heavy user scrolled back), then quiet.
    const t0 = Date.now();
    await loadAll(page);
    record("loadAll.ms", Date.now() - t0);
    await toOverview(page);
    await sleep(2000);
    await memory(c, "loaded");

    // Steady states: the overview while nothing changes, then while the machines work.
    await steady(c, "overview.quiet");
    daemon.live(true);
    await sleep(2000);
    await steady(c, "overview.live");
    await frames(c, "overview.live");

    // The long running conversation while it grows.
    await timed(page, () => row(page, titles.bigLive).click(), { sel: ".cbar" });
    await sleep(3000);
    await steady(c, "session.live");
    await frames(c, "session.live");
    daemon.live(false);

    // An idle session's page: only the sidebar's clocks change.
    await row(page, titles.bigIdle).click();
    await page.locator(".livebar").first().waitFor();
    await sleep(2000);
    await steady(c, "session.quiet");

    // Interactions, each from its click to the frame showing the result.
    await observeEvents(page);
    const open: number[] = [];
    const back: number[] = [];
    const sw: number[] = [];
    for (let i = 0; i < reps; i++) {
      await toOverview(page);
      await page.locator(".wcard").first().waitFor();
      await sleep(500);
      open.push(await timed(page, () => row(page, titles.bigIdle).click(), { sel: ".tr > :last-child", text: `第 40 轮：` }));
      await sleep(500);
      back.push(await timed(page, () => toOverview(page), { sel: ".lmap .lm-node.m" }));
      await sleep(500);
      await row(page, titles.smallA).click();
      await page.locator(`.ctitle h2:text-is("${titles.smallA}")`).first().waitFor();
      await page.locator(".tr .am").first().waitFor();
      await sleep(300);
      sw.push(await timed(page, () => row(page, titles.smallB).click(), { sel: ".ctitle h2", text: titles.smallB }, { sel: ".tr .am" }));
      await sleep(300);
    }
    record("open.long.ms", median(open));
    record("back.overview.ms", median(back));
    record("switch.session.ms", median(sw));
    const clicks = await eventDurations(page);
    record("inp.clicks.p50", median(clicks.length ? clicks : [0]));
    record("inp.clicks.max", Math.max(0, ...clicks));

    // Scrolling the long conversation.
    await row(page, titles.bigIdle).click();
    await page.locator(".tr > :last-child:has-text('第 40 轮：')").first().waitFor();
    await page.mouse.move(500, 400);
    const scroll = (async () => {
      for (let i = 0; i < 60; i++) {
        await page.mouse.wheel(0, i < 30 ? -400 : 400);
        await sleep(50);
      }
    })();
    await frames(c, "scroll.long", 2500);
    await scroll;

    // The search palette: opening it and typing.
    await toOverview(page);
    await sleep(500);
    await eventDurations(page);
    const pal: number[] = [];
    for (let i = 0; i < reps; i++) {
      pal.push(await timed(page, () => page.locator(".nav-i", { hasText: "搜索" }).click(), { sel: ".pal input" }));
      await page.keyboard.type("hermesh", { delay: 80 });
      await page.keyboard.press("Escape");
      await sleep(300);
    }
    record("palette.open.ms", median(pal));
    const keys = await eventDurations(page);
    record("inp.typing.p50", median(keys.length ? keys : [0]));
    record("inp.typing.max", Math.max(0, ...keys));

    // Hovering the map's nodes (each lights its links).
    const nodes = page.locator(".lmap .lm-node:not(.ghost):not(.more)");
    for (let i = 0; i < (await nodes.count()); i++) {
      await nodes.nth(i).hover();
      await sleep(120);
    }
    await page.mouse.move(5, 5);
    const hovers = await eventDurations(page);
    record("inp.hover.max", Math.max(0, ...hovers));

    // Memory after visiting many sessions and coming back.
    for (let i = 0; i < 20; i++) {
      await row(page, `#${120 + i}`).click();
      await page.locator(".tr .am").first().waitFor();
    }
    await row(page, titles.bigIdle).click();
    await page.locator(".tr > :last-child:has-text('第 40 轮：')").first().waitFor();
    await toOverview(page);
    await sleep(2000);
    await memory(c, "visited");
    record("leak.heap.MB", results["visited.heap.MB"] - results["loaded.heap.MB"]);
    record("leak.domNodes", results["visited.domNodes"] - results["loaded.domNodes"]);

    await c.app.close();
  } finally {
    await daemon.close();
    fs.rmSync(tmp, { recursive: true, force: true });
    const dir = path.join(import.meta.dirname, "results");
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(path.join(dir, `${label}.json`), JSON.stringify({ label, at: new Date().toISOString(), reps, results }, null, 2));
  }
});
