import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { ConnectError } from "@connectrpc/connect";
import type { Client, Device, GetStatusResponse, Machine, PairingDetails, Project, Unreachable, Wake } from "@hermesh/proto/local";
import type { Session } from "@hermesh/proto/session";
import { sessionView, unreachableMachines, withKnownState, type SessionFilter, type SessionView } from "@hermesh/core";
import { useWorld } from "./useWorld";

// What the window knows: the owner's world as hermeshd pushes it
// (useWorld: no polling), plus the window's own state (selection, list
// view, open sheet, toast).

export type Data = {
  /** undefined until the first answer; null when hermeshd cannot be reached. */
  status: GetStatusResponse | null | undefined;
  clients: Client[];
  machines: Machine[];
  projects: Project[];
  projectsUnreachable: Unreachable[];
  pairings: PairingDetails[];
  devices: Device[];
};

export type Selection = { kind: "overview" } | { kind: "session"; id: string };

export type Sheet =
  | { kind: "newGrant" }
  | { kind: "addMachine" }
  | { kind: "addProject"; machineId?: string; agent?: string }
  | { kind: "addDevice" }
  | { kind: "enterCode" }
  /** details: from LookupPairing, until the poll lists the request. */
  | { kind: "pairing"; code: string; details?: PairingDetails }
  | { kind: "scope"; clientId: string; renew: boolean }
  /** An agent's doorbell (docs/agent-names.md §7): set one up, or, with wake, see and change it. */
  | { kind: "wake"; clientId: string; agent: string; wake?: Wake }
  | { kind: "confirm"; title: string; message: string; button: string; run: () => Promise<unknown>; done?: string };

export type Toast = { text: string; undo?: () => void; id: number };

type UI = {
  selection: Selection;
  inspector: boolean;
  sidebar: boolean;
  /** How the session list is laid out, and whether it shows archived sessions. */
  view: SessionView;
  archived: boolean;
  /** The list's collapsed groups, as "grouping:key" (each grouping keeps its own). */
  collapsed: string[];
  searching: boolean;
  /** The session whose title is being edited in the list. */
  renaming: string | null;
  /** What the search palette opens narrowed to ("Show in Main Window" in the settings). */
  searchFor: Partial<SessionFilter>;
  sheet: Sheet | null;
  toast: Toast | null;
  /** Pairing codes the owner put off ("Deny"): left out of pairings until opened again. */
  dismissed: string[];
  /** A machine only asked to become a trusted device: the join pages show. */
  upgrading: boolean;
};

export type Store = Data &
  UI & {
    /** The sessions loaded so far: the newest page, older pages the list asked for, and every change since. */
    sessions: Session[];
    sessionsUnreachable: Unreachable[];
    /** The first page of sessions arrived. */
    sessionsLoaded: boolean;
    /** Older sessions than those loaded exist. */
    moreSessions: boolean;
    loadOlderSessions: () => void;
    set: (patch: Partial<UI>) => void;
    select: (id: string) => void;
    refresh: () => Promise<void>;
    /** Runs a change against hermeshd, then refreshes; failures become a toast. Resolves to whether it worked. */
    run: (action: () => Promise<unknown>, done?: string) => Promise<boolean>;
    show: (text: string, undo?: () => void) => void;
  };

const Ctx = createContext<Store | null>(null);

export function useStore(): Store {
  const s = useContext(Ctx);
  if (!s) throw new Error("useStore outside HermeshProvider");
  return s;
}

/** Message of an error from hermeshd, for the person. */
export function errorText(e: unknown): string {
  if (e instanceof ConnectError) return e.rawMessage;
  return e instanceof Error ? e.message : String(e);
}

function saved(key: string, fallback: boolean): boolean {
  try {
    const v = localStorage.getItem(key);
    return v === null ? fallback : v === "1";
  } catch {
    return fallback;
  }
}

export function save(key: string, v: boolean) {
  try {
    localStorage.setItem(key, v ? "1" : "0");
  } catch {
    // Preferences are a convenience; without storage they just don't stick.
  }
}

type SavedView = Pick<UI, "selection" | "sidebar" | "view" | "archived" | "collapsed">;
function savedView(): Partial<SavedView> {
  if (location.hash.startsWith("#settings")) return {};
  try {
    const v: Partial<SavedView> = JSON.parse(localStorage.getItem("main-view") ?? "{}");
    return { selection: v.selection, sidebar: v.sidebar, view: sessionView(v.view), archived: v.archived, collapsed: Array.isArray(v.collapsed) ? v.collapsed : undefined };
  } catch {
    return {};
  }
}

export function HermeshProvider({ children }: { children: ReactNode }) {
  const world = useWorld();
  // Each part changes only when its own list does: a session's change leaves them all.
  const unreachable = useMemo(() => unreachableMachines(world.machines), [world.machines]);
  const status = world.down ? null : world.loaded ? world.status : undefined;
  const data = useMemo<Data>(
    () => ({ status, clients: world.clients, machines: world.machines, projects: world.projects, projectsUnreachable: unreachable, pairings: world.pairings, devices: world.devices }),
    [status, world.clients, world.machines, world.projects, unreachable, world.pairings, world.devices],
  );
  const sessions = useMemo(() => withKnownState(world.sessions, world.machines), [world.sessions, world.machines]);
  const [ui, setUI] = useState<UI>(() => ({
    selection: { kind: "overview" },
    inspector: saved("inspector", true),
    sidebar: true,
    view: sessionView(),
    archived: false,
    collapsed: [],
    searching: false,
    renaming: null,
    searchFor: {},
    sheet: null,
    toast: null,
    dismissed: [],
    upgrading: false,
    ...Object.fromEntries(Object.entries(savedView()).filter(([, v]) => v !== undefined)),
  }));
  useEffect(() => {
    if (location.hash.startsWith("#settings")) return;
    try { localStorage.setItem("main-view", JSON.stringify({ selection: ui.selection, sidebar: ui.sidebar, view: ui.view, archived: ui.archived, collapsed: ui.collapsed })); }
    catch { /* Main-window context is optional when storage is unavailable. */ }
  }, [ui.selection, ui.sidebar, ui.view, ui.archived, ui.collapsed]);
  const toastTimer = useRef<ReturnType<typeof setTimeout>>(undefined);

  // Watch brings every change; a step that waits on one (signing in) reads the status at once.
  const refresh = world.refreshStatus;

  const set = useCallback((patch: Partial<UI>) => {
    if (patch.inspector !== undefined) save("inspector", patch.inspector);
    setUI((u) => ({ ...u, ...patch }));
  }, []);

  const show = useCallback((text: string, undo?: () => void) => {
    clearTimeout(toastTimer.current);
    const id = Date.now();
    setUI((u) => ({ ...u, toast: { text, undo, id } }));
    toastTimer.current = setTimeout(() => setUI((u) => (u.toast?.id === id ? { ...u, toast: null } : u)), 5200);
  }, []);

  const run = useCallback(
    async (action: () => Promise<unknown>, done?: string) => {
      try {
        await action();
        if (done) show(done);
        await refresh();
        return true;
      } catch (e) {
        show(errorText(e));
        return false;
      }
    },
    [refresh, show],
  );

  const select = useCallback((id: string) => setUI((u) => ({ ...u, selection: { kind: "session", id } })), []);

  const store = useMemo<Store>(
    () => ({
      ...data,
      pairings: data.pairings.filter((p) => !ui.dismissed.includes(p.code)),
      sessions,
      sessionsUnreachable: data.projectsUnreachable,
      sessionsLoaded: world.loaded && world.status !== undefined && (world.status.initialized || world.status.machineOnly),
      moreSessions: world.next !== undefined,
      loadOlderSessions: world.loadOlder,
      ...ui,
      set,
      select,
      refresh,
      run,
      show,
    }),
    [data, sessions, world.loaded, world.status, world.next, world.loadOlder, ui, set, select, refresh, run, show],
  );
  return <Ctx.Provider value={store}>{children}</Ctx.Provider>;
}

/**
 * The current time, ticking every interval ms (for elapsed times and
 * countdowns). One clock per interval for the whole window, so everything
 * on the same interval changes in the same frame. Ticks fall halfway
 * between wall-clock multiples of interval, so a clock counting whole
 * seconds (elapsedSeconds) moves by exactly one on each, however late a
 * tick comes.
 */
export function useNow(interval = 1000): Date {
  const c = clock(interval);
  return useSyncExternalStore(c.subscribe, c.now);
}

const clocks = new Map<number, { subscribe: (f: () => void) => () => void; now: () => Date }>();

function clock(interval: number) {
  let c = clocks.get(interval);
  if (c) return c;
  const subs = new Set<() => void>();
  let now = new Date();
  let timer: ReturnType<typeof setTimeout> | undefined;
  c = {
    subscribe(f) {
      if (subs.size === 0) {
        now = new Date();
        const next = () => {
          timer = setTimeout(() => {
            now = new Date();
            subs.forEach((g) => g());
            next();
          }, interval - ((Date.now() + interval / 2) % interval));
        };
        next();
      }
      subs.add(f);
      return () => {
        subs.delete(f);
        if (subs.size === 0) clearTimeout(timer);
      };
    },
    now: () => now,
  };
  clocks.set(interval, c);
  return c;
}
