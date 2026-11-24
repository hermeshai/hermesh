import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { applyWatch, emptyWorld, filterNotices, herNotices, herSnapshots, noticeDiff, noticeSnapshots, pairingNotice, withKnownState, type HerSnapshot, type NoticeSnapshot } from "@hermesh/core";
import { LocalService, type Client, type GetStatusResponse, type Machine, type PairingDetails } from "@hermesh/proto/local";
import { HerLatestKind } from "@hermesh/proto/machine";
import type { Session } from "@hermesh/proto/session";
import { app, BrowserWindow, nativeTheme, Notification } from "electron";
import fs from "node:fs";
import path from "node:path";
import type { Command, Preferences } from "../shared/bridge";
import { i18n, languages, type Language } from "../shared/i18n";
import { command } from "./ipc";
import { socketPath } from "./paths";
import { focusedHer } from "./windows";

// What reaches the owner while no window may be open: the main process
// follows hermeshd itself (LocalService.Watch: everything once, then each
// change, no polling), posts notifications for what changed, and keeps what
// it saw for the menu bar.

export type World = {
  /** null: hermeshd cannot be reached. */
  status: GetStatusResponse | null;
  sessions: Session[];
  clients: Client[];
  machines: Machine[];
  pairings: PairingDetails[];
};
export const world: World = { status: null, sessions: [], clients: [], machines: [], pairings: [] };

export const worldListeners = new Set<() => void>();
const changed = () => worldListeners.forEach((f) => f());

// --- Preferences (the settings window sets them) ---

const defaults: Preferences = { appearance: "system", notifyStarted: true, notifyQuestion: true, notifyFailed: true, notifyTurnDone: false, menuBadge: true, language: "system", stayOnline: true };
let prefs: Preferences | undefined;
let legacy: { keepDaemonOnQuit?: boolean; stayOnline?: boolean } = {};
const prefsFile = () => path.join(app.getPath("userData"), "preferences.json");

const isAppearance = (a: unknown): a is Preferences["appearance"] => a === "system" || a === "light" || a === "dark" || a === "black";

export function preferences(): Preferences {
  if (!prefs) {
    try {
      const raw = JSON.parse(fs.readFileSync(prefsFile(), "utf8"));
      legacy = { keepDaemonOnQuit: raw.keepDaemonOnQuit, stayOnline: raw.stayOnline };
      delete raw.keepDaemonOnQuit;
      prefs = { ...defaults, ...raw, appearance: isAppearance(raw.appearance) ? raw.appearance : defaults.appearance };
    } catch {
      prefs = { ...defaults };
    }
  }
  return prefs!;
}

export async function migrateStayOnline(loginItem: () => Promise<{ on: boolean }> | { on: boolean }, setLoginItem: (on: boolean) => unknown) {
  preferences();
  if (typeof legacy.stayOnline === "boolean") return;
  if (typeof legacy.keepDaemonOnQuit === "boolean") {
    const on = legacy.keepDaemonOnQuit || (await loginItem()).on;
    setPreferences({ stayOnline: on });
  } else {
    setPreferences({ stayOnline: preferences().stayOnline });
  }
  await setLoginItem(preferences().stayOnline);
}

export function applyAppearance() {
  const appearance = preferences().appearance;
  nativeTheme.themeSource = appearance === "black" ? "dark" : appearance;
}

export function setPreferences(p: Partial<Preferences>): Preferences {
  const before = preferences();
  const next = { ...before };
  for (const k of ["notifyStarted", "notifyQuestion", "notifyFailed", "notifyTurnDone", "menuBadge", "stayOnline"] as const) if (typeof p[k] === "boolean") next[k] = p[k];
  if (p.language === "system" || languages.includes(p.language as Language)) next[kLanguage] = p.language!;
  if (isAppearance(p.appearance)) next.appearance = p.appearance;
  prefs = next;
  legacy.stayOnline = next.stayOnline;
  applyAppearance();
  fs.mkdirSync(path.dirname(prefsFile()), { recursive: true });
  fs.writeFileSync(prefsFile(), JSON.stringify(next));
  for (const w of BrowserWindow.getAllWindows()) if (!w.isDestroyed()) w.webContents.send("preferences", next);
  if (next.stayOnline !== before.stayOnline) stayOnlineListeners.forEach((f) => f(next.stayOnline));
  changed();
  return next;
}
const kLanguage = "language" as const;

export const stayOnlineListeners = new Set<(on: boolean) => void>();

// --- Notifications ---

export type Posted = { id: string; title: string; body: string; click(): void };
export const posted: Posted[] = [];
const shown = new Map<string, Notification>();

function post(id: string, title: string, body: string, open: Command, silent = false) {
  withdraw(id);
  const n = new Notification({ id, title, body, silent });
  const p: Posted = { id, title, body, click: () => command(open) };
  n.on("click", p.click);
  shown.set(id, n);
  posted.push(p);
  if (posted.length > 50) withdraw(posted[0].id);
  n.show();
}

function withdraw(id: string) {
  shown.get(id)?.close();
  shown.delete(id);
  const i = posted.findIndex((x) => x.id === id);
  if (i >= 0) posted.splice(i, 1);
}

let seen = new Map<string, NoticeSnapshot>();
let since: Date | undefined;

function noticeSessions() {
  const unnamed = i18n.t(world.status?.machineOnly ? "main:anAgent" : "main:disconnectedGrant");
  const grant = (s: Session) => (s.createdBy === "owner" ? i18n.t("common:sidebar.you") : world.clients.find((c) => c.clientId === s.createdBy)?.label ?? unnamed);
  const look = new Date();
  for (const n of filterNotices(noticeDiff(seen, world.sessions, grant, since), preferences())) {
    post(`session-${n.kind}-${n.sessionId}`, n.title, n.body, `open:${n.sessionId}`, n.kind === "turnDone" || n.kind === "started");
  }
  seen = noticeSnapshots(world.sessions);
  since ??= look;
}

let herSeen: Map<string, HerSnapshot> | undefined;

function noticeHer() {
  if (herSeen) {
    const looking = focusedHer();
    for (const n of herNotices(herSeen, world.machines)) {
      if (n.kind === HerLatestKind.MESSAGE && looking?.machineId === n.machineId && looking.botId === n.botId) continue;
      post(`her-${n.machineId}-${n.botId}`, n.title, n.body, `her:${n.machineId}`);
    }
    for (const m of world.machines) {
      if (m.her?.enabled && m.her.unread === 0) for (const p of posted.filter((x) => x.id.startsWith(`her-${m.machineId}-`))) withdraw(p.id);
    }
  }
  herSeen = herSnapshots(world.machines);
}

const pairingId = (code: string) => `pairing-${code}`;
let notifiedPairings = new Set<string>();

function noticePairings() {
  const now = new Set(world.pairings.map((p) => p.code));
  for (const p of world.pairings) {
    if (notifiedPairings.has(p.code)) continue;
    const n = pairingNotice(p);
    post(pairingId(p.code), n.title, n.body, `pairing:${p.code}`);
  }
  for (const code of notifiedPairings) if (!now.has(code)) withdraw(pairingId(code));
  notifiedPairings = now;
}

// --- Following hermeshd ---

let wakeRetry: (() => void) | undefined;
let watchAbort: AbortController | undefined;
let pauseWatch = false;

export function wakeWatch() {
  pauseWatch = false;
  wakeRetry?.();
  wakeRetry = undefined;
}

export function setWatchPaused(paused: boolean) {
  pauseWatch = paused;
  if (paused) {
    watchAbort?.abort();
    if (world.status !== null) {
      world.status = null;
      world.sessions = [];
      world.clients = [];
      world.machines = [];
      world.pairings = [];
      changed();
    }
  } else {
    wakeWatch();
  }
}

export function watch() {
  (async () => {
    for (;;) {
      if (pauseWatch) {
        await new Promise<void>((r) => {
          const timer = setTimeout(r, 1000);
          wakeRetry = () => {
            clearTimeout(timer);
            r();
          };
        });
        wakeRetry = undefined;
        continue;
      }
      let w = emptyWorld;
      const transport = createConnectTransport({ baseUrl: "http://hermeshd", httpVersion: "1.1", useBinaryFormat: true, nodeOptions: { socketPath: socketPath() } });
      const local = createClient(LocalService, transport);
      const abort = new AbortController();
      watchAbort = abort;
      try {
        for await (const m of local.watch({}, { signal: abort.signal })) {
          if (pauseWatch) break;
          const before = w;
          w = applyWatch(w, m);
          world.status = w.status ?? null;
          world.clients = w.clients;
          world.machines = w.machines;
          world.sessions = withKnownState(w.sessions, world.machines);
          world.pairings = world.status?.initialized ? w.pairings : [];
          if (w.sessions !== before.sessions || w.machines !== before.machines) noticeSessions();
          if (w.machines !== before.machines) noticeHer();
          if (w.pairings !== before.pairings) noticePairings();
          if (w !== before) changed();
        }
      } catch (e) {
        if (!pauseWatch && !abort.signal.aborted) console.warn("hermeshd watch:", e);
      } finally {
        abort.abort();
        if (watchAbort === abort) watchAbort = undefined;
      }
      if (world.status !== null) {
        world.status = null;
        changed();
      }
      if (pauseWatch) continue;
      await new Promise<void>((r) => {
        const timer = setTimeout(r, 1000);
        wakeRetry = () => {
          clearTimeout(timer);
          r();
        };
      });
      wakeRetry = undefined;
    }
  })();
}
