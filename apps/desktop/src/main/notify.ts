import { createClient, type Client as RpcClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { filterNotices, mergeSessions, noticeDiff, noticeSnapshots, pairingNotice, type NoticeSnapshot } from "@hermesh/core";
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
// follows hermeshd itself (sessions as hermeshd pushes them; status and
// lists every 3 s, pairing requests every 2 s), posts notifications for
// what changed, and keeps what it saw for the menu bar.

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

// Sessions as they were at the last look; none before the first, so what
// was already there when the app started is no news.
let seen: Map<string, NoticeSnapshot> | undefined;

function noticeSessions() {
  // A machine only does not know the owner's agents by name.
  const unnamed = i18n.t(world.status?.machineOnly ? "main:anAgent" : "main:disconnectedGrant");
  const grant = (s: Session) => world.clients.find((c) => c.clientId === s.createdBy)?.label ?? unnamed;
  if (seen) {
    for (const n of filterNotices(noticeDiff(seen, world.sessions, grant), preferences())) {
      post(`session-${n.kind}-${n.sessionId}`, n.title, n.body, `open:${n.sessionId}`, n.kind === "turnDone");
    }
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
  every(3000, async () => {
    try {
      world.status = await local.getStatus({});
    } catch {
      world.status = null;
    }
    if (world.status?.initialized || world.status?.machineOnly) {
      const [c, m] = await Promise.allSettled([local.listClients({}), local.listMachines({})]);
      if (c.status === "fulfilled") world.clients = c.value.clients;
      if (m.status === "fulfilled") world.machines = m.value.machines;
    }
    changed();
  });
  followSessions(local);
  every(2000, async () => {
    if (!world.status?.initialized) return;
    try {
      world.pairings = (await local.listPairingRequests({})).pairings;
    } catch {
      return; // kept as they were; the status poll tells when hermeshd is gone
    }
    noticePairings();
    changed();
  });
}

// Sessions as hermeshd pushes them (WatchSessions): when the watch starts
// and whenever machines come or go, the first page again, which carries
// every session that needs the owner or runs (all the menu bar shows and
// notices start from); then each change as it happens.
async function followSessions(local: RpcClient<typeof LocalService>) {
  for (;;) {
    if (world.status?.initialized || world.status?.machineOnly) {
      try {
        for await (const m of local.watchSessions({})) {
          if (m.machinesChanged) world.sessions = (await local.listSessions({ limit: 1 })).sessions;
          world.sessions = mergeSessions(world.sessions, m.sessions, m.deletedSessionIds);
          noticeSessions();
          changed();
        }
      } catch (e) {
        console.warn("hermeshd watch:", e);
      }
    }
    await new Promise((r) => setTimeout(r, 1000));
  }
}

function every(ms: number, f: () => Promise<void>) {
  const loop = () =>
    f()
      .catch((e) => console.warn("hermeshd poll:", e))
      .finally(() => setTimeout(loop, ms));
  loop();
}
