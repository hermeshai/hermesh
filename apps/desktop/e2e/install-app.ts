import { _electron as electron, test, type ElectronApplication } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import { background, fixture } from "./app";

export type Hook = {
  installTest: {
    dialogs: { message: string; detail: string; buttons: string[]; defaultId: number; cancelId: number }[];
    loginItems: unknown[];
    protocols: string[];
    moved: number;
    respond(response: number): void;
    feedFails: boolean;
    latest: string;
    feedRequests: number;
    holdFeed: boolean;
    downloads: number;
    installs: number;
    advance(ms: number): Promise<void>;
    signing(on: boolean): void;
    signerSignals: string[];
    online: boolean;
    badNotes: boolean;
    longNotes: boolean;
    links: string[];
  };
};
const instances: { app: ElectronApplication; home: string; closed: boolean }[] = [];

test.afterEach(async () => {
  for (const { app, home, closed } of instances.splice(0)) {
    if (!closed) await app.close();
    fs.rmSync(home, { recursive: true, force: true });
  }
});

export async function installer(inApplications = false, move: "success" | "cancel" | "error" = "success", options: { autoDownload?: boolean; checkedAt?: number; failures?: number; attemptedAt?: number } = {}) {
  // Keep the daemon socket below macOS's Unix-domain socket path limit.
  const home = fs.mkdtempSync("/tmp/hminst-");
  const data = path.join(home, "Library", "Application Support", "Hermesh");
  fs.mkdirSync(path.join(data, "run"), { recursive: true });
  fs.symlinkSync(fixture.socket, path.join(data, "run", "hermeshd.sock"));
  fs.mkdirSync(path.join(data, "desktop"));
  fs.writeFileSync(path.join(data, "desktop", "preferences.json"), JSON.stringify({ language: "zh-CN" }));
  fs.writeFileSync(path.join(data, "desktop", "updates.json"), JSON.stringify(options));
  const entry = path.join(process.env.HERMESH_DESKTOP_OUT || path.join(import.meta.dirname, "..", "out"), "main", "index.js");
  const bootstrap = path.join(home, "start.cjs");
  fs.writeFileSync(bootstrap, `
    const { app, autoUpdater, dialog, net, shell } = require("electron");
    const config = ${JSON.stringify({ home, entry, inApplications, move })};
    require("node:os").homedir = () => config.home;
    delete process.env.HERMESH_DATA_DIR;
    delete process.env.HERMESH_SOCKET;
    Object.defineProperty(app, "isPackaged", { get: () => true });
    const state = globalThis.installTest = { dialogs: [], loginItems: [], protocols: [], moved: 0, feedFails: false, latest: "0.0.0", feedRequests: 0, holdFeed: false, downloads: 0, installs: 0, signerSignals: [], online: true, badNotes: false, longNotes: false, links: [] };
    net.isOnline = () => state.online;
    shell.openExternal = async (url) => { state.links.push(url); };
    // Advance only long scheduling timers; daemon polling and renderer clocks remain real.
    const realTimeout = global.setTimeout, realClear = global.clearTimeout;
    const realNow = Date.now;
    let now = realNow();
    Date.now = () => now;
    const timers = new Map();
    global.setTimeout = (fn, ms, ...args) => {
      if (!Number.isFinite(ms) || ms < 5000) return realTimeout(fn, ms, ...args);
      const handle = { fake: true, unref() { return this; } };
      timers.set(handle, { at: now + ms, fn, args });
      return handle;
    };
    global.clearTimeout = (handle) => timers.delete(handle) || realClear(handle);
    const realInterval = global.setInterval;
    global.setInterval = (fn, ms, ...args) => ms < 60000 ? realInterval(fn, ms, ...args) : global.setTimeout(function tick() { fn(...args); global.setTimeout(tick, ms); }, ms);
    state.advance = async (ms) => {
      const end = now + ms;
      for (let i = 0; i < 500; i++) {
        const next = [...timers].sort((a,b) => a[1].at - b[1].at)[0];
        if (!next || next[1].at > end) break;
        now = next[1].at;
        timers.delete(next[0]);
        next[1].fn(...next[1].args);
        await new Promise((r) => realTimeout(r, 20));
      }
      now = end;
      await new Promise((r) => realTimeout(r, 20));
    };
    // A signer subprocess boundary, carrying the same busy bit as the real Swift signer.
    const { EventEmitter } = require("node:events");
    const { PassThrough } = require("node:stream");
    const fs = require("node:fs"), cp = require("node:child_process");
    const exists = fs.existsSync, spawn = cp.spawn, execFile = cp.execFile;
    cp.execFile = (bin, ...args) => {
      if (bin !== "launchctl") return execFile(bin, ...args);
      const callback = args.at(-1);
      if (typeof callback === "function") setImmediate(() => callback(null, "", ""));
      return new EventEmitter();
    };
    fs.existsSync = (p) => String(p).endsWith("/HermeshSigner") || exists(p);
    cp.spawn = (bin, ...args) => {
      if (!String(bin).endsWith("/HermeshSigner")) return spawn(bin, ...args);
      const child = new EventEmitter();
      child.stdout = new PassThrough();
      child.kill = (signal) => { state.signerSignals.push(signal); return true; };
      state.signing = (on) => child.stdout.write("HERMESH_SIGNER_BUSY=" + (on ? 1 : 0) + "\\n");
      setImmediate(() => state.signing(false));
      return child;
    };
    app.getVersion = () => "1.0.0";
    app.isInApplicationsFolder = () => config.inApplications;
    dialog.showMessageBox = (options) => {
      state.dialogs.push(options);
      return new Promise((resolve) => { state.respond = (response) => resolve({ response, checkboxChecked: false }); });
    };
    app.moveToApplicationsFolder = () => {
      state.moved++;
      if (config.move === "error") throw new Error("The copy failed");
      if (config.move === "cancel") return false;
      setImmediate(() => app.quit());
      return true;
    };
    app.setLoginItemSettings = (options) => state.loginItems.push(options);
    app.getLoginItemSettings = () => ({ status: "not-registered", wasOpenedAtLogin: false });
    app.setAsDefaultProtocolClient = (scheme) => { state.protocols.push(scheme); return true; };
    autoUpdater.setFeedURL = () => {};
    autoUpdater.checkForUpdates = () => { state.downloads++; autoUpdater.emit("update-available"); };
    autoUpdater.quitAndInstall = () => { state.installs++; };
    const fetch = net.fetch.bind(net);
    net.fetch = async (url, ...args) => {
      if (!String(url).includes("/RELEASES-darwin-")) return fetch(url, ...args);
      state.feedRequests++;
      while (state.holdFeed) await new Promise((r) => realTimeout(r, 10));
      const summary = { improvements: ["Read update highlights in Hermesh."], fixes: [] };
      const zh = { improvements: ["在 Hermesh 中阅读更新摘要。"], fixes: [] };
      if (state.longNotes) {
        summary.improvements = zh.improvements = Array.from({ length: 5 }, (_, i) => (i + 1) + ". " + "Improved update reliability and responsiveness. ".repeat(16));
      }
      return new Response(JSON.stringify({ currentRelease: state.latest, hermeshNotes: state.badNotes ? null : [
        { version: state.latest, summary: { en: summary, "zh-CN": zh } },
        { version: "1.1.0", summary: { en: summary, "zh-CN": zh } }
      ] }), { status: state.feedFails ? 503 : 200 });
    };
    require("node:module").syncBuiltinESMExports();
    import(require("node:url").pathToFileURL(config.entry).href);
  `);
  const app = await electron.launch({ args: ["-r", background, bootstrap] });
  const instance = { app, home, closed: false };
  app.on("close", () => { instance.closed = true; });
  instances.push(instance);
  return app;
}


export async function restartInstaller(app: ElectronApplication) {
  const instance = instances.find((i) => i.app === app)!;
  await app.close();
  const next = await electron.launch({ args: ["-r", background, path.join(instance.home, "start.cjs")] });
  instance.app = next;
  instance.closed = false;
  next.on("close", () => { instance.closed = true; });
  return next;
}
