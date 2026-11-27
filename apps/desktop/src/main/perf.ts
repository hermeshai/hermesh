import { app, BrowserWindow, webContents, type WebContents } from "electron";
import crypto from "node:crypto";
import fs from "node:fs";
import http from "node:http";
import inspector from "node:inspector";
import path from "node:path";
import { monitorEventLoopDelay, performance } from "node:perf_hooks";
import v8 from "node:v8";
import { gatewayPerfStats } from "./gateway";
import { menuPerfStats } from "./menu";
import { notifyPerfStats } from "./notify";
import { dataDir, perfEndpointPath } from "./paths";
import { trayPerfStats } from "./tray";
import { ours } from "./windows";

export type AnimationSummary = {
  total: number;
  running: number;
  infinite: number;
  compositorOnly: number;
  mainThreadCount: number;
  mainThreadSamples: { target: string; props: string[] }[];
};

export type RendererSnapshot = {
  webContentsId: number;
  osProcessId: number;
  type: string;
  url: string;
  route: string;
  windowVisible: boolean | null;
  windowFocused: boolean | null;
  windowMinimized: boolean | null;
  backgroundThrottled: boolean;
  visibilityState: string | null;
  hidden: boolean | null;
  domNodes: number;
  jsEventListeners: number;
  documents: number;
  frames: number;
  jsHeapUsedMB: number;
  jsHeapTotalMB: number;
  busyPct?: number;
  scriptPct?: number;
  styleLayoutPct?: number;
  layoutsPerSec?: number;
  recalcStylesPerSec?: number;
  animations: AnimationSummary | null;
  cpuProfile?: unknown;
};

export type ProcessPerfRow = {
  pid: number;
  type: string;
  name?: string;
  cpuPct: number;
  idleWakeupsPerSec: number;
  workingSetMB: number;
  privateMB?: number;
};

export type DesktopPerfSnapshot = {
  at: string;
  mainPid: number;
  windowSeconds: number;
  processes: ProcessPerfRow[];
  mainMemoryMB: {
    rss: number;
    heapTotal: number;
    heapUsed: number;
    external: number;
    arrayBuffers: number;
    heapSizeLimit: number;
  };
  activeHandles: number;
  eventLoop: {
    utilizationPct?: number;
    delayP50Ms?: number;
    delayP99Ms?: number;
    delayMaxMs?: number;
  };
  countersBefore: MainCounters;
  countersAfter: MainCounters;
  countersDelta: Record<string, number>;
  gatewayCallsDelta: Record<string, number>;
  renderers: RendererSnapshot[];
  mainCpuProfile?: unknown;
};

type MainCounters = {
  gatewayOpenUpstream: number;
  gatewayBytesUp: number;
  gatewayBytesDown: number;
  gatewayStreamErrors: number;
  gatewayCalls: Record<string, number>;
  watchMessages: number;
  watchReconnects: number;
  worldBroadcasts: number;
  worldListeners: number;
  appMenuBuilds: number;
  appMenuBuildMs: number;
  trayUpdates: number;
  trayIconDraws: number;
  trayIconDrawMs: number;
  dockMenuBuilds: number;
  dockMenuBuildMs: number;
  trayPanelAllocated: boolean;
  trayPanelVisible: boolean;
};

function readMainCounters(): MainCounters {
  const gw = gatewayPerfStats();
  const nt = notifyPerfStats();
  const mn = menuPerfStats();
  const tr = trayPerfStats();
  return {
    gatewayOpenUpstream: gw.openUpstream,
    gatewayBytesUp: gw.bytesUp,
    gatewayBytesDown: gw.bytesDown,
    gatewayStreamErrors: gw.streamErrors,
    gatewayCalls: gw.calls,
    watchMessages: nt.watchMessages,
    watchReconnects: nt.watchReconnects,
    worldBroadcasts: nt.worldBroadcasts,
    worldListeners: nt.worldListeners,
    appMenuBuilds: mn.appMenuBuilds,
    appMenuBuildMs: mn.appMenuBuildMs,
    trayUpdates: tr.trayUpdates,
    trayIconDraws: tr.trayIconDraws,
    trayIconDrawMs: tr.trayIconDrawMs,
    dockMenuBuilds: tr.dockMenuBuilds,
    dockMenuBuildMs: tr.dockMenuBuildMs,
    trayPanelAllocated: tr.panelAllocated,
    trayPanelVisible: tr.panelVisible,
  };
}

function round2(n: number): number {
  return Math.round(n * 100) / 100;
}

function sanitizeURL(raw: string): string {
  try {
    const u = new URL(raw);
    if (u.protocol === "http:" && u.hostname === "127.0.0.1") {
      return `${u.origin}/<redacted>${u.hash}`;
    }
    return raw;
  } catch {
    return raw;
  }
}

async function readPageDiagnostics(wc: WebContents): Promise<{ route: string; visibilityState: string; hidden: boolean; animations: AnimationSummary } | null> {
  if (wc.isDestroyed() || !ours(wc.getURL())) return null;
  const script = `(() => {
    const compositor = new Set(["offset", "computedOffset", "easing", "composite", "transform", "opacity", "translate", "scale", "rotate"]);
    const anims = typeof document.getAnimations === "function" ? document.getAnimations({ subtree: true }) : [];
    let running = 0;
    let infinite = 0;
    let compositorOnly = 0;
    let mainThreadCount = 0;
    const mainThreadSamples = [];
    for (const a of anims) {
      if (a.playState !== "running") continue;
      running++;
      const timing = a.effect && typeof a.effect.getComputedTiming === "function" ? a.effect.getComputedTiming() : null;
      if (timing && timing.iterations === Infinity) infinite++;
      const el = a.effect && "target" in a.effect ? a.effect.target : null;
      const isSvgChild = typeof SVGElement !== "undefined" && el instanceof SVGElement && el.tagName.toLowerCase() !== "svg";
      const kfs = a.effect && typeof a.effect.getKeyframes === "function" ? a.effect.getKeyframes() : [];
      const nonComp = new Set();
      for (const kf of kfs) {
        for (const k of Object.keys(kf)) {
          if (k === "offset" || k === "computedOffset" || k === "easing" || k === "composite") continue;
          if (!compositor.has(k)) nonComp.add(k);
          else if (isSvgChild) nonComp.add("svg:" + k);
        }
      }
      if (nonComp.size === 0) {
        compositorOnly++;
      } else {
        mainThreadCount++;
        if (mainThreadSamples.length < 16) {
          const cls = el && el.classList && el.classList.length ? "." + Array.from(el.classList).slice(0, 2).join(".") : "";
          const target = el && el.tagName ? el.tagName.toLowerCase() + cls : "unknown";
          mainThreadSamples.push({ target, props: Array.from(nonComp) });
        }
      }
    }
    const nav = document.querySelector(".nav-i.sel")?.textContent?.trim() || "";
    const route = location.hash || (document.querySelector(".her") ? "her" : document.querySelector(".lmap") ? "overview" : document.querySelector("[data-testid='thread-composer']") ? "session" : nav || "main");
    return {
      route,
      visibilityState: document.visibilityState,
      hidden: document.hidden,
      animations: { total: anims.length, running, infinite, compositorOnly, mainThreadCount, mainThreadSamples },
    };
  })()`;
  try {
    return await Promise.race([
      wc.executeJavaScript(script, false) as Promise<{ route: string; visibilityState: string; hidden: boolean; animations: AnimationSummary }>,
      new Promise<null>((r) => setTimeout(() => r(null), 1500)),
    ]);
  } catch {
    return null;
  }
}

async function getCDPMetrics(wc: WebContents): Promise<Record<string, number>> {
  const res = (await wc.debugger.sendCommand("Performance.getMetrics")) as { metrics: { name: string; value: number }[] };
  return Object.fromEntries(res.metrics.map((m) => [m.name, m.value]));
}

/** Captures an on-demand performance and memory snapshot of Electron main and all live renderers. */
export async function collectDesktopSnapshot(opts: { seconds?: number; mainProfile?: boolean; rendererProfile?: boolean } = {}): Promise<DesktopPerfSnapshot> {
  const seconds = Math.max(0, Math.min(60, Number.isFinite(opts.seconds) ? Number(opts.seconds) : 0));
  const wantMainProfile = opts.mainProfile !== false && seconds > 0;
  const wantRendererProfile = opts.rendererProfile === true && seconds > 0;

  const countersBefore = readMainCounters();
  const elu0 = performance.eventLoopUtilization();
  const eld = seconds > 0 ? monitorEventLoopDelay({ resolution: 10 }) : undefined;
  eld?.enable();

  // Prime idle wakeup counters before starting the timed window.
  app.getAppMetrics();
  const proc0 = app.getAppMetrics();
  const t0 = Date.now();

  let mainSession: inspector.Session | undefined;
  const postMain = <T = unknown>(method: string, params?: Record<string, unknown>) =>
    new Promise<T>((resolve, reject) => {
      if (!mainSession) return reject(new Error("no session"));
      mainSession.post(method, params ?? {}, (err, res) => (err ? reject(err) : resolve(res as T)));
    });

  if (wantMainProfile) {
    mainSession = new inspector.Session();
    mainSession.connect();
    await postMain("Profiler.enable");
    await postMain("Profiler.setSamplingInterval", { interval: 500 });
    await postMain("Profiler.start");
  }

  type AttachedWC = {
    wc: WebContents;
    attachedByUs: boolean;
    m0?: Record<string, number>;
    profiling: boolean;
  };
  const targets: AttachedWC[] = [];
  for (const wc of webContents.getAllWebContents()) {
    if (wc.isDestroyed()) continue;
    let attachedByUs = false;
    if (!wc.debugger.isAttached()) {
      try {
        wc.debugger.attach("1.3");
        attachedByUs = true;
      } catch {
        targets.push({ wc, attachedByUs: false, profiling: false });
        continue;
      }
    }
    let m0: Record<string, number> | undefined;
    let profiling = false;
    try {
      await wc.debugger.sendCommand("Performance.enable", { timeDomain: "timeTicks" });
      m0 = await getCDPMetrics(wc);
      if (wantRendererProfile) {
        await wc.debugger.sendCommand("Profiler.enable");
        await wc.debugger.sendCommand("Profiler.setSamplingInterval", { interval: 500 });
        await wc.debugger.sendCommand("Profiler.start");
        profiling = true;
      }
    } catch {
      // Ignore transient renderer navigation / teardown.
    }
    targets.push({ wc, attachedByUs, m0, profiling });
  }

  let mainCpuProfile: unknown;
  const renderers: RendererSnapshot[] = [];
  try {
    if (seconds > 0) {
      await new Promise((r) => setTimeout(r, seconds * 1000));
    }
    const dt = Math.max(0.001, (Date.now() - t0) / 1000);
    const proc1 = app.getAppMetrics();
    const countersAfter = readMainCounters();
    const elu = seconds > 0 ? performance.eventLoopUtilization(elu0) : undefined;
    eld?.disable();

    if (mainSession) {
      try {
        const stopped = await postMain<{ profile: unknown }>("Profiler.stop");
        mainCpuProfile = stopped.profile;
      } catch {
        // Ignore profiler stop error.
      }
    }

    for (const item of targets) {
      const { wc, m0, profiling } = item;
      if (wc.isDestroyed()) continue;
      let m1: Record<string, number> = m0 ?? {};
      let cpuProfile: unknown;
      if (wc.debugger.isAttached()) {
        try {
          m1 = await getCDPMetrics(wc);
          if (profiling) {
            const r = (await wc.debugger.sendCommand("Profiler.stop")) as { profile: unknown };
            cpuProfile = r.profile;
          }
        } catch {
          // Renderer closed mid-sample.
        }
      }
      const win = BrowserWindow.fromWebContents(wc);
      const pageDiag = await readPageDiagnostics(wc);
      const snap: RendererSnapshot = {
        webContentsId: wc.id,
        osProcessId: wc.isDestroyed() ? 0 : wc.getOSProcessId(),
        type: wc.isDestroyed() ? "destroyed" : wc.getType(),
        url: wc.isDestroyed() ? "" : sanitizeURL(wc.getURL()),
        route: pageDiag?.route ?? "",
        windowVisible: win && !win.isDestroyed() ? win.isVisible() : null,
        windowFocused: win && !win.isDestroyed() ? win.isFocused() : null,
        windowMinimized: win && !win.isDestroyed() ? win.isMinimized() : null,
        backgroundThrottled: wc.isDestroyed() ? false : wc.backgroundThrottling,
        visibilityState: pageDiag?.visibilityState ?? null,
        hidden: pageDiag?.hidden ?? null,
        domNodes: m1.Nodes ?? 0,
        jsEventListeners: m1.JSEventListeners ?? 0,
        documents: m1.Documents ?? 0,
        frames: m1.Frames ?? 0,
        jsHeapUsedMB: round2((m1.JSHeapUsedSize ?? 0) / 2 ** 20),
        jsHeapTotalMB: round2((m1.JSHeapTotalSize ?? 0) / 2 ** 20),
        animations: pageDiag?.animations ?? null,
      };
      if (seconds > 0 && m0) {
        const rate = (k: string) => ((m1[k] ?? 0) - (m0[k] ?? 0)) / dt;
        snap.busyPct = round2(rate("TaskDuration") * 100);
        snap.scriptPct = round2(rate("ScriptDuration") * 100);
        snap.styleLayoutPct = round2((rate("RecalcStyleDuration") + rate("LayoutDuration")) * 100);
        snap.layoutsPerSec = round2(rate("LayoutCount"));
        snap.recalcStylesPerSec = round2(rate("RecalcStyleCount"));
      }
      if (cpuProfile) snap.cpuProfile = cpuProfile;
      renderers.push(snap);
    }

    const processes: ProcessPerfRow[] = proc1.map((p) => {
      const prev = proc0.find((q) => q.pid === p.pid);
      const cpuPct =
        seconds > 0 && p.cpu.cumulativeCPUUsage !== undefined && prev?.cpu.cumulativeCPUUsage !== undefined
          ? ((p.cpu.cumulativeCPUUsage - prev.cpu.cumulativeCPUUsage) / dt) * 100
          : p.cpu.percentCPUUsage;
      return {
        pid: p.pid,
        type: p.type,
        name: p.name,
        cpuPct: round2(cpuPct),
        idleWakeupsPerSec: round2(p.cpu.idleWakeupsPerSecond),
        workingSetMB: round2((p.memory.workingSetSize ?? 0) / 1024),
        privateMB: p.memory.privateBytes !== undefined ? round2(p.memory.privateBytes / 1024) : undefined,
      };
    });

    const mem = process.memoryUsage();
    const heap = v8.getHeapStatistics();
    const activeHandles = (process as unknown as { _getActiveHandles?: () => unknown[] })._getActiveHandles?.().length ?? 0;

    const numericKeys = [
      "gatewayBytesUp",
      "gatewayBytesDown",
      "gatewayStreamErrors",
      "watchMessages",
      "watchReconnects",
      "worldBroadcasts",
      "appMenuBuilds",
      "appMenuBuildMs",
      "trayUpdates",
      "trayIconDraws",
      "trayIconDrawMs",
      "dockMenuBuilds",
      "dockMenuBuildMs",
    ] as const;
    const countersDelta: Record<string, number> = {};
    for (const k of numericKeys) {
      countersDelta[k] = round2(countersAfter[k] - countersBefore[k]);
    }
    const gatewayCallsDelta: Record<string, number> = {};
    for (const [m, after] of Object.entries(countersAfter.gatewayCalls)) {
      const d = after - (countersBefore.gatewayCalls[m] ?? 0);
      if (d > 0) gatewayCallsDelta[m] = d;
    }

    return {
      at: new Date().toISOString(),
      mainPid: process.pid,
      windowSeconds: seconds > 0 ? round2(dt) : 0,
      processes,
      mainMemoryMB: {
        rss: round2(mem.rss / 2 ** 20),
        heapTotal: round2(mem.heapTotal / 2 ** 20),
        heapUsed: round2(mem.heapUsed / 2 ** 20),
        external: round2(mem.external / 2 ** 20),
        arrayBuffers: round2(mem.arrayBuffers / 2 ** 20),
        heapSizeLimit: round2(heap.heap_size_limit / 2 ** 20),
      },
      activeHandles,
      eventLoop: {
        utilizationPct: elu ? round2(elu.utilization * 100) : undefined,
        delayP50Ms: eld && eld.count > 0 ? round2(eld.percentile(50) / 1e6) : undefined,
        delayP99Ms: eld && eld.count > 0 ? round2(eld.percentile(99) / 1e6) : undefined,
        delayMaxMs: eld && eld.count > 0 ? round2(eld.max / 1e6) : undefined,
      },
      countersBefore,
      countersAfter,
      countersDelta,
      gatewayCallsDelta,
      renderers,
      ...(mainCpuProfile ? { mainCpuProfile } : {}),
    };
  } finally {
    eld?.disable();
    if (mainSession) {
      try {
        mainSession.disconnect();
      } catch {
        // Already disconnected.
      }
    }
    for (const item of targets) {
      if (item.attachedByUs && !item.wc.isDestroyed() && item.wc.debugger.isAttached()) {
        try {
          item.wc.debugger.detach();
        } catch {
          // Already detached.
        }
      }
    }
  }
}

let inFlight: Promise<DesktopPerfSnapshot> | null = null;

function perfLogDir(): string {
  const base = !app.isPackaged && process.env.HERMESH_USER_DATA && !process.env.HERMESH_DATA_DIR ? process.env.HERMESH_USER_DATA : dataDir();
  return path.join(base, "logs");
}

function appendRotatedJSONL(file: string, record: unknown): void {
  try {
    fs.mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
    try {
      const st = fs.statSync(file);
      if (st.size > 10 * 1024 * 1024) fs.renameSync(file, `${file}.1`);
    } catch {
      // File does not exist yet.
    }
    fs.appendFileSync(file, JSON.stringify(record) + "\n", { mode: 0o600 });
  } catch {
    // Telemetry logging is best-effort.
  }
}

function startPerfHeartbeat(): () => void {
  const logDir = perfLogDir();
  const logFile = path.join(logDir, "desktop-perf.jsonl");
  let prevCounters = readMainCounters();
  let prevElu = performance.eventLoopUtilization();
  app.getAppMetrics();
  let prevProc = app.getAppMetrics();
  let prevAt = Date.now();
  let lastAutoSpikeAt = 0;
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | undefined;

  const sample = async () => {
    if (stopped) return;
    const now = Date.now();
    const dt = Math.max(0.001, (now - prevAt) / 1000);
    prevAt = now;
    const proc = app.getAppMetrics();
    const counters = readMainCounters();
    const elu = performance.eventLoopUtilization(prevElu);
    prevElu = performance.eventLoopUtilization();

    let mainCpuPct = 0;
    let maxRendererCpuPct = 0;
    const processes: ProcessPerfRow[] = proc.map((p) => {
      const prev = prevProc.find((q) => q.pid === p.pid);
      const cpuPct =
        p.cpu.cumulativeCPUUsage !== undefined && prev?.cpu.cumulativeCPUUsage !== undefined
          ? ((p.cpu.cumulativeCPUUsage - prev.cpu.cumulativeCPUUsage) / dt) * 100
          : p.cpu.percentCPUUsage;
      const rounded = round2(Math.max(0, cpuPct));
      if (p.type === "Browser") mainCpuPct = rounded;
      else if (p.type === "Tab" && rounded > maxRendererCpuPct) maxRendererCpuPct = rounded;
      return {
        pid: p.pid,
        type: p.type,
        name: p.name,
        cpuPct: rounded,
        idleWakeupsPerSec: round2(p.cpu.idleWakeupsPerSecond),
        workingSetMB: round2((p.memory.workingSetSize ?? 0) / 1024),
      };
    });
    prevProc = proc;

    const numericKeys = [
      "gatewayBytesUp",
      "gatewayBytesDown",
      "gatewayStreamErrors",
      "watchMessages",
      "watchReconnects",
      "worldBroadcasts",
      "appMenuBuilds",
      "trayUpdates",
      "trayIconDraws",
      "dockMenuBuilds",
    ] as const;
    const countersDelta: Record<string, number> = {};
    for (const k of numericKeys) {
      const d = round2(counters[k] - prevCounters[k]);
      if (d !== 0) countersDelta[k] = d;
    }
    const gatewayCallsDelta: Record<string, number> = {};
    for (const [m, after] of Object.entries(counters.gatewayCalls)) {
      const d = after - (prevCounters.gatewayCalls[m] ?? 0);
      if (d > 0) gatewayCallsDelta[m] = d;
    }
    prevCounters = counters;

    const windows: {
      id: number;
      visible: boolean;
      focused: boolean;
      minimized: boolean;
      route?: string;
      runningAnims?: number;
      mainThreadAnims?: number;
    }[] = [];
    for (const win of BrowserWindow.getAllWindows()) {
      if (win.isDestroyed()) continue;
      const visible = win.isVisible();
      const minimized = win.isMinimized();
      const diag = visible && !minimized ? await readPageDiagnostics(win.webContents) : null;
      windows.push({
        id: win.id,
        visible,
        focused: win.isFocused(),
        minimized,
        ...(diag
          ? {
              route: diag.route,
              runningAnims: diag.animations.running,
              mainThreadAnims: diag.animations.mainThreadCount,
            }
          : {}),
      });
    }

    const mem = process.memoryUsage();
    const eluPct = round2(elu.utilization * 100);
    appendRotatedJSONL(logFile, {
      at: new Date(now).toISOString(),
      windowSeconds: round2(dt),
      eluPct,
      mainMemoryMB: {
        rss: round2(mem.rss / 2 ** 20),
        heapUsed: round2(mem.heapUsed / 2 ** 20),
        external: round2(mem.external / 2 ** 20),
      },
      processes,
      countersDelta,
      gatewayCallsDelta,
      windows,
    });

    // Capture an automatic 5s V8 CPU profile if sustained CPU spikes occur during real use.
    if (
      dt >= 20 &&
      (mainCpuPct >= 15 || eluPct >= 20 || maxRendererCpuPct >= 25) &&
      now - lastAutoSpikeAt >= 10 * 60_000 &&
      !inFlight
    ) {
      lastAutoSpikeAt = now;
      const run = (inFlight = collectDesktopSnapshot({
        seconds: 5,
        mainProfile: true,
        rendererProfile: maxRendererCpuPct >= 20,
      }).finally(() => {
        inFlight = null;
      }));
      void run
        .then((snap) => {
          const ts = new Date().toISOString().replace(/[:.]/g, "-");
          fs.writeFileSync(path.join(logDir, `auto-desktop-spike-${ts}.json`), JSON.stringify(snap), { mode: 0o600 });
        })
        .catch(() => {});
    }

    if (!stopped) {
      timer = setTimeout(() => void sample(), 60_000);
      timer.unref();
    }
  };

  timer = setTimeout(() => void sample(), 10_000);
  timer.unref();
  return () => {
    stopped = true;
    if (timer) clearTimeout(timer);
  };
}

/** Starts a local-user-only diagnostic endpoint so `hermeshd perf-snapshot` can inspect the running desktop app. */
export function startPerfServer(): () => void {
  const stopHeartbeat = startPerfHeartbeat();
  const endpoint = perfEndpointPath();
  let listenAddr = endpoint;
  let markerFile: string | undefined;
  try {
    fs.mkdirSync(path.dirname(endpoint), { recursive: true, mode: 0o700 });
    if (process.platform === "win32") {
      listenAddr = `\\\\.\\pipe\\hermesh-desktop-${crypto.randomBytes(16).toString("hex")}`;
      markerFile = endpoint;
    } else {
      try {
        fs.unlinkSync(endpoint);
      } catch {
        // Not present yet.
      }
    }
  } catch (e) {
    console.warn("perf server dir:", e);
    return stopHeartbeat;
  }

  const srv = http.createServer(async (req, res) => {
    try {
      const u = new URL(req.url ?? "/", "http://desktop-perf");
      if (req.method !== "GET" || u.pathname !== "/snapshot") {
        res.writeHead(404);
        res.end("not found");
        return;
      }
      const seconds = Number(u.searchParams.get("seconds") ?? "0");
      const mainProfile = u.searchParams.get("main_profile") !== "0";
      const rendererProfile = u.searchParams.get("renderer_profile") === "1";
      const run = (inFlight ??= collectDesktopSnapshot({ seconds, mainProfile, rendererProfile }).finally(() => {
        inFlight = null;
      }));
      const snap = await run;
      const body = JSON.stringify(snap);
      res.writeHead(200, { "content-type": "application/json" });
      res.end(body);
    } catch (e) {
      res.writeHead(500, { "content-type": "text/plain" });
      res.end(e instanceof Error ? e.message : String(e));
    }
  });

  srv.on("error", (e) => console.warn("perf server:", e));
  srv.listen(listenAddr, () => {
    try {
      if (markerFile) {
        fs.writeFileSync(markerFile, listenAddr, { mode: 0o600 });
      } else {
        fs.chmodSync(listenAddr, 0o600);
      }
    } catch (e) {
      console.warn("perf server permissions:", e);
    }
  });

  return () => {
    stopHeartbeat();
    srv.close();
    try {
      fs.unlinkSync(markerFile ?? listenAddr);
    } catch {
      // Already cleaned up.
    }
  };
}
