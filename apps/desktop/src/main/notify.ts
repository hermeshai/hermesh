import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { applyWatch, emptyWorld, filterNotices, noticeDiff, noticeSnapshots, pairingNotice, withKnownState, type NoticeSnapshot } from "@hermesh/core";
import { LocalService, type Client, type GetStatusResponse, type Machine, type PairingDetails } from "@hermesh/proto/local";
import type { Session } from "@hermesh/proto/session";
import { app, nativeTheme, Notification } from "electron";
import fs from "node:fs";
import path from "node:path";
import type { Command, Preferences } from "../shared/bridge";
import { i18n, languages, type Language } from "../shared/i18n";
import { command } from "./ipc";
import { socketPath } from "./paths";

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

const defaults: Preferences = { appearance: "system", notifyQuestion: true, notifyFailed: true, notifyTurnDone: false, menuBadge: true, language: "system" };
let prefs: Preferences | undefined;
const prefsFile = () => path.join(app.getPath("userData"), "preferences.json");

export function preferences(): Preferences {
  if (!prefs) {
    try {
      prefs = { ...defaults, ...JSON.parse(fs.readFileSync(prefsFile(), "utf8")) };
    } catch {
      prefs = { ...defaults };
    }
  }
  return prefs!;
}

/** Every window (and the native menus) in the chosen appearance: CSS follows it through prefers-color-scheme. */
export function applyAppearance() {
  nativeTheme.themeSource = preferences().appearance;
}

export function setPreferences(p: Partial<Preferences>): Preferences {
  const next = { ...preferences() };
  for (const k of ["notifyQuestion", "notifyFailed", "notifyTurnDone", "menuBadge"] as const) if (typeof p[k] === "boolean") next[k] = p[k];
  if (p.language === "system" || languages.includes(p.language as Language)) next.language = p.language!;
  if (p.appearance === "system" || p.appearance === "light" || p.appearance === "dark") next.appearance = p.appearance;
  prefs = next;
  applyAppearance();
  fs.mkdirSync(path.dirname(prefsFile()), { recursive: true });
  fs.writeFileSync(prefsFile(), JSON.stringify(next));
  changed();
  return next;
}

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
// before are no news.
let seen = new Map<string, NoticeSnapshot>();

function noticeSessions() {
  // A machine only does not know the owner's agents by name.
  const unnamed = i18n.t(world.status?.machineOnly ? "main:anAgent" : "main:disconnectedGrant");
  const grant = (s: Session) => world.clients.find((c) => c.clientId === s.createdBy)?.label ?? unnamed;
  for (const n of filterNotices(noticeDiff(seen, world.sessions, grant), preferences())) {
    post(`session-${n.kind}-${n.sessionId}`, n.title, n.body, `open:${n.sessionId}`, n.kind === "turnDone");
  }
  seen = noticeSnapshots(world.sessions);
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

export function watch() {
  const local = createClient(
    LocalService,
    createConnectTransport({ baseUrl: "http://hermeshd", httpVersion: "1.1", useBinaryFormat: true, nodeOptions: { socketPath: socketPath() } }),
  );
  (async () => {
    for (;;) {
      let w = emptyWorld;
      try {
        for await (const m of local.watch({})) {
          const before = w;
          w = applyWatch(w, m);
          world.status = w.status ?? null;
          world.clients = w.clients;
          world.machines = w.machines;
          world.sessions = withKnownState(w.sessions, w.machines);
          world.pairings = world.status?.initialized ? w.pairings : [];
          if (w.sessions !== before.sessions || w.machines !== before.machines) noticeSessions();
          if (w.pairings !== before.pairings) noticePairings();
          if (w !== before) changed();
        }
      } catch (e) {
        console.warn("hermeshd watch:", e);
      }
      // hermeshd restarted, or this watch fell behind: watched again.
      if (world.status !== null) {
        world.status = null;
        changed();
      }
      await new Promise((r) => setTimeout(r, 1000));
    }
  })();
}
