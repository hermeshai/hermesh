import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { applyWatch, emptyWorld, filterNotices, herNotices, herSnapshots, noticeDiff, noticeSnapshots, pairingNotice, threadAsSession, withKnownState, type HerSnapshot, type NoticeSnapshot } from "@hermesh/core";
import { LocalService, type Client, type GetStatusResponse, type Machine, type PairingDetails } from "@hermesh/proto/local";
import { HerLatestKind } from "@hermesh/proto/machine";
import type { Session } from "@hermesh/proto/session";
import { ThreadService, type Thread } from "@hermesh/proto/thread";
import { app, nativeTheme, Notification } from "electron";
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

// A session is a session wherever it started: the v2 threads (SubscribeThreads) are sessions here too, for the
// menu bar and the notifications, as in the window's list (docs/thread-composer.md §14).
let watched: Session[] = [];
let threads: Thread[] = [];
function setSessions() {
  world.sessions = withKnownState(threads.length ? [...watched, ...threads.map(threadAsSession)] : watched, world.machines);
}

/** Follows the threads while this watch lasts; a thread list kept from a watch before is replaced by the next snapshot. */
async function followThreads(transport: ReturnType<typeof createConnectTransport>, signal: AbortSignal) {
  const client = createClient(ThreadService, transport);
  try {
    for await (const m of client.subscribeThreads({ includeArchived: true }, { signal })) {
      if (m.snapshot) threads = m.threads;
      else {
        const gone = new Set(m.deletedThreadIds);
        const byId = new Map(threads.filter((t) => !gone.has(t.threadId)).map((t) => [t.threadId, t]));
        for (const t of m.threads) byId.set(t.threadId, t);
        threads = [...byId.values()];
      }
      setSessions();
      noticeSessions();
      changed();
    }
  } catch {
    // The watch ended (hermeshd restarting) or a hermeshd from before threads: the next watch follows them again.
  }
}
export const worldListeners = new Set<() => void>();
const changed = () => worldListeners.forEach((f) => f());

// --- Preferences (the settings window sets them) ---

const defaults: Preferences = { appearance: "system", notifyStarted: true, notifyQuestion: true, notifyFailed: true, notifyTurnDone: false, menuBadge: true, language: "system", stayOnline: true };
let prefs: Preferences | undefined;
/** What the file said before Stay online, until migrateStayOnline reads it. */
let legacy: { keepDaemonOnQuit?: boolean; stayOnline?: boolean } = {};
const prefsFile = () => path.join(app.getPath("userData"), "preferences.json");

export function preferences(): Preferences {
  if (!prefs) {
    try {
      const raw = JSON.parse(fs.readFileSync(prefsFile(), "utf8"));
      legacy = { keepDaemonOnQuit: raw.keepDaemonOnQuit, stayOnline: raw.stayOnline };
      delete raw.keepDaemonOnQuit;
      prefs = { ...defaults, ...raw };
    } catch {
      prefs = { ...defaults };
    }
  }
  return prefs!;
}

/**
 * Stay online replaced open-at-login and keep-the-service-on-quit (docs/settings-environments.md,
 * D10): an owner who had either on stays online; one who had neither, not; a new install is.
 * Then the login item follows it.
 */
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

/** Every window (and the native menus) in the chosen appearance: CSS follows it through prefers-color-scheme. */
export function applyAppearance() {
  nativeTheme.themeSource = preferences().appearance;
}

export function setPreferences(p: Partial<Preferences>): Preferences {
  const before = preferences();
  const next = { ...before };
  for (const k of ["notifyStarted", "notifyQuestion", "notifyFailed", "notifyTurnDone", "menuBadge", "stayOnline"] as const) if (typeof p[k] === "boolean") next[k] = p[k];
  if (p.language === "system" || languages.includes(p.language as Language)) next.language = p.language!;
  if (p.appearance === "system" || p.appearance === "light" || p.appearance === "dark") next.appearance = p.appearance;
  prefs = next;
  legacy.stayOnline = next.stayOnline;
  applyAppearance();
  fs.mkdirSync(path.dirname(prefsFile()), { recursive: true });
  fs.writeFileSync(prefsFile(), JSON.stringify(next));
  if (next.stayOnline !== before.stayOnline) stayOnlineListeners.forEach((f) => f(next.stayOnline));
  changed();
  return next;
}

/** Told when Stay online changes: the login item follows it (index.ts). */
export const stayOnlineListeners = new Set<(on: boolean) => void>();

// --- Notifications ---

export type Posted = { id: string; title: string; body: string; click(): void };
/** Notifications that can still be clicked, newest last (tests read them). */
export const posted: Posted[] = [];
const shown = new Map<string, Notification>();

function post(id: string, title: string, body: string, open: Command, silent = false) {
  withdraw(id);
  const n = new Notification({ id, title, body, silent });
  const p: Posted = { id, title, body, click: () => command(open) };
  n.on("click", p.click);
  // Kept, so their clicks still arrive; the oldest go.
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

// Sessions as they were at the last look; none before the first. A session
// seen for the first time only tells about its question, so a question
// still waiting when the app starts is notified again (macOS took the old
// notification away when the app quit); failures and finished turns from
// before are no news. A session not seen before is new when it was created
// after the first look (since).
let seen = new Map<string, NoticeSnapshot>();
let since: Date | undefined;

function noticeSessions() {
  // A machine only does not know the owner's agents by name.
  const unnamed = i18n.t(world.status?.machineOnly ? "main:anAgent" : "main:disconnectedGrant");
  const grant = (s: Session) => (s.createdBy === "owner" ? i18n.t("common:sidebar.you") : world.clients.find((c) => c.clientId === s.createdBy)?.label ?? unnamed);
  const look = new Date();
  for (const n of filterNotices(noticeDiff(seen, world.sessions, grant, since), preferences())) {
    post(`session-${n.kind}-${n.sessionId}`, n.title, n.body, `open:${n.sessionId}`, n.kind === "turnDone" || n.kind === "started");
  }
  seen = noticeSnapshots(world.sessions);
  since ??= look;
}

// What Her's assistants send (docs/her-server.md §9.3), from each machine's
// Her as Watch brings it. A reply stays quiet in its visible, focused
// conversation. Clicking a notice opens the Her entry at its environment.
// An environment whose messages were all read, here or on another device,
// takes its notifications back. Nothing before the first look is news.
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

// Every pending request is notified (it cannot be switched off), and
// withdrawn once it is gone: answered here or elsewhere, or expired.
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

/** Wakes the watch loop right away (after starting the daemon). */
export function wakeWatch() {
  pauseWatch = false;
  wakeRetry?.();
  wakeRetry = undefined;
}

/** Pauses the watch loop and clears cached world state (after stopping the daemon). */
export function setWatchPaused(paused: boolean) {
  pauseWatch = paused;
  if (paused) {
    watchAbort?.abort();
    if (world.status !== null) {
      world.status = null;
      watched = [];
      threads = [];
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
      // Where hermeshd is now: a pipe's name changes each time it starts (Windows).
      const transport = createConnectTransport({ baseUrl: "http://hermeshd", httpVersion: "1.1", useBinaryFormat: true, nodeOptions: { socketPath: socketPath() } });
      const local = createClient(LocalService, transport);
      const abort = new AbortController();
      watchAbort = abort;
      void followThreads(transport, abort.signal);
      try {
        for await (const m of local.watch({}, { signal: abort.signal })) {
          if (pauseWatch) break;
          const before = w;
          w = applyWatch(w, m);
          world.status = w.status ?? null;
          world.clients = w.clients;
          world.machines = w.machines;
          watched = w.sessions;
          setSessions();
          world.pairings = world.status?.initialized ? w.pairings : [];
          if (w.sessions !== before.sessions || w.machines !== before.machines) noticeSessions();
          if (w.machines !== before.machines) noticeHer();
          if (w.pairings !== before.pairings) noticePairings();
          if (w !== before) changed();
        }
      } catch (e) {
        if (!pauseWatch && !abort.signal.aborted) console.warn("hermeshd watch:", e);
      } finally {
        abort.abort(); // the threads' stream ends with the watch
        if (watchAbort === abort) watchAbort = undefined;
      }
      // hermeshd restarted, or this watch fell behind: watched again.
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
