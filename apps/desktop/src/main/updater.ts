import { SessionStatus } from "@hermesh/proto/session";
import { app, autoUpdater, BrowserWindow, net, powerMonitor } from "electron";
import { createHash, verify } from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { Readable } from "node:stream";
import { pipeline } from "node:stream/promises";
import enNotes from "../locales/en/releases.json";
import zhNotes from "../locales/zh-CN/releases.json";
import { feedNotes, nextCheckDelay, nextUpdate, releaseCatalog, squirrelFailure, type ReleaseNote, type Update, type UpdateEvent, type UpdateInfo } from "../shared/update";
import { command } from "./ipc";
import { world, worldListeners } from "./notify";
import { platform } from "./platform";
import { showSigner, signerBusy, signerListeners } from "./signer";
import { showUpdates } from "./windows";

// Squirrel.Mac verifies the Developer ID signature and installs on quit. The
// Windows app is unsigned: its feed is signed with the release key, and names
// the package's hash; the app checks both, and Squirrel.Windows installs the
// package from a folder of the app's beside the running version, which the
// next start runs (docs/windows.md §6.2). All checks are quiet; opening the
// update window is always a deliberate user action.
declare const __HERMESH_UPDATE_FEED__: string;
declare const __HERMESH_RELEASE_VERSION__: string;
declare const __HERMESH_RELEASE_PUBKEY__: string;
const feedURL = `${__HERMESH_UPDATE_FEED__ || "https://github.com/hermeshai/hermesh/releases/latest/download"}/RELEASES-${process.platform}-${process.arch}.json`;
// The Windows app installed by its Setup.exe only (not development and test runs).
const supported = app.isPackaged && (platform.token === "mac" || platform.installed);
/** The Windows feed's package: where, its SHA-256, and Squirrel's RELEASES line for it. */
type WindowsPackage = { url: string; sha256: string; releases: string };
let windowsPackage: WindowsPackage | undefined;
let update: Update = { state: supported ? "idle" : "unsupported" };
export const updateListeners = new Set<() => void>();
const bundled = releaseCatalog(enNotes, zhNotes, __HERMESH_RELEASE_VERSION__);
let notes: ReleaseNote[] = bundled;

type Settings = {
  autoDownload: boolean;
  checkedAt: number | null;
  attemptedAt: number | null;
  failures: number;
  snoozedVersion: string;
  snoozedUntil: number;
};
const settingsFile = () => path.join(app.getPath("userData"), "updates.json");
let settings: Settings | undefined;
function prefs(): Settings {
  if (!settings) {
    let saved: Partial<Settings> = {};
    try {
      const value = JSON.parse(fs.readFileSync(settingsFile(), "utf8"));
      if (value && typeof value === "object") saved = value;
    } catch { /* First launch. */ }
    settings = {
      autoDownload: saved.autoDownload !== false,
      checkedAt: typeof saved.checkedAt === "number" ? saved.checkedAt : null,
      attemptedAt: typeof saved.attemptedAt === "number" ? saved.attemptedAt : null,
      failures: typeof saved.failures === "number" ? Math.max(0, saved.failures) : 0,
      snoozedVersion: typeof saved.snoozedVersion === "string" ? saved.snoozedVersion : "",
      snoozedUntil: typeof saved.snoozedUntil === "number" ? saved.snoozedUntil : 0,
    };
  }
  return settings;
}
function save() {
  try {
    fs.mkdirSync(path.dirname(settingsFile()), { recursive: true });
    fs.writeFileSync(settingsFile(), JSON.stringify(prefs()));
  } catch (e) { console.warn("could not save update preferences:", e); }
}

export function setAutoDownload(on: boolean): UpdateInfo {
  prefs().autoDownload = on;
  save();
  if (on && update.state === "available") download();
  else changed();
  return updateInfo();
}

export function snooze() {
  if (update.state !== "ready" && update.state !== "available") return;
  prefs().snoozedVersion = update.version;
  prefs().snoozedUntil = Date.now() + 24 * 60 * 60_000;
  save();
  changed();
}

export function updateInfo(): UpdateInfo {
  const p = prefs();
  return { version: app.getVersion(), autoDownload: p.autoDownload, checkedAt: p.checkedAt,
    attemptedAt: p.attemptedAt, snoozedVersion: p.snoozedVersion, snoozedUntil: p.snoozedUntil,
    approvalPending: signerBusy() || world.pairings.length > 0, notes, update };
}
function apply(e: UpdateEvent): Update {
  update = nextUpdate(update, e);
  changed();
  return update;
}
function changed() {
  updateListeners.forEach((f) => f());
  const info = updateInfo();
  for (const w of BrowserWindow.getAllWindows()) w.webContents.send("update", info);
}

let timer: NodeJS.Timeout | undefined;
function delay() {
  const p = prefs();
  return nextCheckDelay(p.checkedAt, p.attemptedAt, p.failures, Date.now());
}
function schedule(minimum = 0) {
  clearTimeout(timer);
  if (!supported || ["unsupported", "downloading", "ready"].includes(update.state)) return;
  timer = setTimeout(() => net.isOnline() ? check() : schedule(5 * 60_000), Math.max(minimum, delay()));
}
/** A wake or online event catches up only if the last successful check is stale. */
export function catchUp() {
  if (update.state !== "checking") schedule(10_000);
}
function failed(reason: "network" | "location" | "download", message: string) {
  prefs().attemptedAt = Date.now();
  prefs().failures++;
  save();
  apply({ type: "error", reason, message });
  schedule();
}

export function startUpdater() {
  let pending = updateInfo().approvalPending;
  worldListeners.add(() => {
    restartStaleDaemon();
    const next = updateInfo().approvalPending;
    if (pending !== next) { pending = next; changed(); }
  });
  signerListeners.add(changed);
  if (!supported) return;
  // Windows: set to the downloaded package's folder (startDownload).
  if (platform.token === "mac") {
    try { autoUpdater.setFeedURL({ url: feedURL, serverType: "json" }); }
    catch (e) { console.warn("updates off:", e); update = { state: "unsupported" }; return; }
  }
  autoUpdater.on("update-available", () => apply({ type: "download" }));
  autoUpdater.on("update-downloaded", (_e, _notes, name) => {
    prefs().failures = 0;
    save();
    apply({ type: "downloaded", version: name || ("version" in update ? update.version : "") });
    clearTimeout(timer);
  });
  autoUpdater.on("update-not-available", () => { prefs().failures = 0; save(); apply({ type: "not-available" }); schedule(); });
  autoUpdater.on("error", (e) => {
    console.warn("update:", e);
    if (update.state !== "ready") failed(squirrelFailure(e.message), e.message);
  });
  schedule(30_000);
  powerMonitor.on("resume", catchUp);
  let online = net.isOnline();
  setInterval(() => {
    const next = net.isOnline();
    if (next && !online) catchUp();
    online = next;
  }, 60_000);
}

/** Manual checks bypass freshness and backoff; concurrent calls share the active operation. */
export async function check() {
  if (["unsupported", "checking", "downloading", "ready"].includes(update.state)) return;
  clearTimeout(timer);
  prefs().attemptedAt = Date.now();
  apply({ type: "check" });
  try {
    const res = await net.fetch(feedURL, { cache: "no-store", signal: AbortSignal.timeout(30_000) });
    if (!res.ok && res.status !== 404) throw new Error(`${feedURL}: HTTP ${res.status}`);
    const body = res.status === 404 ? "" : await res.text();
    if (body && platform.token === "win") await verifyFeed(body);
    const feed = body ? JSON.parse(body) : { currentRelease: "" };
    if (typeof feed.currentRelease !== "string") throw new Error("Invalid update feed: currentRelease is missing");
    if (platform.token === "win") windowsPackage = feed.package;
    const remoteNotes = feedNotes(feed.hermeshNotes);
    notes = [...remoteNotes, ...bundled.filter((n) => !remoteNotes.some((r) => r.version === n.version))];
    prefs().checkedAt = Date.now();
    const next = apply({ type: "feed", latest: feed.currentRelease, current: app.getVersion(), autoDownload: prefs().autoDownload });
    if (next.state !== "downloading") prefs().failures = 0;
    save();
    if (next.state === "downloading") startDownload();
    else schedule();
  } catch (e) {
    console.warn("update check:", e);
    failed("network", e instanceof Error ? e.message : String(e));
  }
}
function startDownload() {
  const fail = (e: unknown) => failed("download", e instanceof Error ? e.message : String(e));
  if (platform.token === "mac") {
    try { autoUpdater.checkForUpdates(); }
    catch (e) { fail(e); }
    return;
  }
  void downloadPackage().then((dir) => {
    autoUpdater.setFeedURL({ url: dir });
    autoUpdater.checkForUpdates();
  }).catch(fail);
}

/** The Windows feed, signed by the release key (.sig: openssl's DER signature), or an error. */
async function verifyFeed(body: string) {
  const res = await net.fetch(`${feedURL}.sig`, { cache: "no-store", signal: AbortSignal.timeout(30_000) });
  if (!res.ok) throw new Error(`${feedURL}.sig: HTTP ${res.status}`);
  if (!verify("sha256", Buffer.from(body), __HERMESH_RELEASE_PUBKEY__, Buffer.from(await res.arrayBuffer()))) {
    throw new Error("The update feed is not signed by the release key");
  }
}

/**
 * Downloads the Windows feed's package into a folder of its own, with its
 * RELEASES, as Squirrel reads an update from a folder; its hash must be
 * the feed's.
 */
async function downloadPackage(): Promise<string> {
  const p = windowsPackage;
  const name = p?.releases.split(/\s+/)[1];
  if (!p || !name || !/^[\w.-]+\.nupkg$/.test(name)) throw new Error("Invalid update feed: package is missing");
  const dir = path.join(app.getPath("userData"), "update");
  fs.rmSync(dir, { recursive: true, force: true });
  fs.mkdirSync(dir, { recursive: true });
  const res = await net.fetch(p.url, { cache: "no-store" });
  if (!res.ok || !res.body) throw new Error(`${p.url}: HTTP ${res.status}`);
  const hash = createHash("sha256");
  const body = Readable.fromWeb(res.body as import("node:stream/web").ReadableStream);
  body.on("data", (chunk: Buffer) => hash.update(chunk));
  await pipeline(body, fs.createWriteStream(path.join(dir, name)));
  if (hash.digest("hex") !== p.sha256) throw new Error(`${p.url}: not the package the feed names`);
  fs.writeFileSync(path.join(dir, "RELEASES"), p.releases);
  return dir;
}
export function download() {
  if (update.state !== "available") return;
  clearTimeout(timer);
  apply({ type: "download" });
  startDownload();
}
export function openUpdates(manual = false) {
  showUpdates();
  if (manual || update.state === "idle") void check();
}
export function returnToApproval() {
  if (signerBusy()) showSigner();
  else if (world.pairings[0]) command(`pairing:${world.pairings[0].code}`);
}
/** Recheck the live guard even when Restart to Update came from a native menu. */
export function install() {
  if (update.state !== "ready") return;
  if (updateInfo().approvalPending) { showUpdates(); changed(); return; }
  autoUpdater.quitAndInstall();
}

// --- hermeshd after an update ---

// hermeshd is a LaunchAgent and keeps running while the app is replaced,
// so a new app may find the previous version's daemon: restart it, once,
// to run the binary now in the bundle. Not during a turn on this Mac: the
// restart would end it (the sessions themselves survive), so it waits for
// them to finish. It looks again 30 s after first seeing the old version,
// once the app has heard about the sessions too.
let daemonRestart: "no" | "soon" | "done" = "no";

function staleDaemon(): string | undefined {
  if (signerBusy() || world.pairings.length > 0) return undefined;
  const st = world.status;
  if (!st?.version || st.version === app.getVersion()) return undefined;
  const busy = world.sessions.some(
    (s) =>
      s.machineId === st.localMachineId &&
      [SessionStatus.QUEUED, SessionStatus.PREPARING, SessionStatus.RUNNING, SessionStatus.WAITING_INPUT].includes(s.status),
  );
  return busy ? undefined : st.version;
}

function restartStaleDaemon() {
  if (daemonRestart !== "no" || !staleDaemon()) return;
  daemonRestart = "soon";
  setTimeout(() => {
    const v = staleDaemon();
    if (v) platform.restartDaemon(`hermeshd ${v} with Hermesh ${app.getVersion()}`);
    daemonRestart = v ? "done" : "no";
  }, 30_000);
}
