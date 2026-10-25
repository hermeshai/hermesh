import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { equals, type DescMessage } from "@bufbuild/protobuf";
import { ConnectError } from "@connectrpc/connect";
import {
  ClientSchema, DeviceSchema, GetStatusResponseSchema, MachineSchema, PairingDetailsSchema, ProjectSchema, UnreachableSchema,
  type Client, type Device, type GetStatusResponse, type Machine, type PairingDetails, type Project, type Unreachable,
} from "@hermesh/proto/local";
import type { Session } from "@hermesh/proto/session";
import { sessionView, type SessionFilter, type SessionView } from "@hermesh/core";
import { local } from "./api";
import { useSessions } from "./useSessions";

// What the window knows: hermeshd's lists, polled like the Mac app did
// (status and lists every few seconds, pairing requests more often), the
// sessions as hermeshd pushes them (useSessions), plus the window's own
// state (selection, list view, open sheet, toast).

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
  | { kind: "confirm"; title: string; message: string; button: string; run: () => Promise<unknown>; done?: string };

export type Toast = { text: string; undo?: () => void; id: number };

type UI = {
  selection: Selection;
  inspector: boolean;
  sidebar: boolean;
  /** How the session list is laid out, and whether it shows archived sessions. */
  view: SessionView;
  archived: boolean;
  searching: boolean;
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

const empty: Data = {
  status: undefined,
  clients: [],
  machines: [],
  projects: [],
  projectsUnreachable: [],
  pairings: [],
  devices: [],
};

const schemas: { [K in keyof Data]: DescMessage } = {
  status: GetStatusResponseSchema,
  clients: ClientSchema,
  machines: MachineSchema,
  projects: ProjectSchema,
  projectsUnreachable: UnreachableSchema,
  pairings: PairingDetailsSchema,
  devices: DeviceSchema,
};

/**
 * d with what a poll answered; d itself when every answer is as before, so
 * nothing renders again for a poll that changed nothing.
 */
function merged(d: Data, answers: Partial<Data>): Data {
  const same = (k: keyof Data, a: unknown, b: unknown): boolean => {
    if (Array.isArray(a) && Array.isArray(b)) return a.length === b.length && a.every((x, i) => same(k, x, b[i]));
    return a === b || (!!a && !!b && equals(schemas[k], a as never, b as never));
  };
  const changed = (Object.keys(answers) as (keyof Data)[]).filter((k) => !same(k, d[k], answers[k]));
  return changed.length ? { ...d, ...Object.fromEntries(changed.map((k) => [k, answers[k]])) } : d;
}

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

type SavedView = Pick<UI, "selection" | "sidebar" | "view" | "archived">;
function savedView(): Partial<SavedView> {
  if (location.hash.startsWith("#settings")) return {};
  try {
    const v: Partial<SavedView> = JSON.parse(localStorage.getItem("main-view") ?? "{}");
    return { selection: v.selection, sidebar: v.sidebar, view: sessionView(v.view), archived: v.archived };
  } catch {
    return {};
  }
}

export function HermeshProvider({ children }: { children: ReactNode }) {
  const [data, setData] = useState<Data>(empty);
  const [ui, setUI] = useState<UI>(() => ({
    selection: { kind: "overview" },
    inspector: saved("inspector", true),
    sidebar: true,
    view: sessionView(),
    archived: false,
    searching: false,
    searchFor: {},
    sheet: null,
    toast: null,
    dismissed: [],
    upgrading: false,
    ...Object.fromEntries(Object.entries(savedView()).filter(([, v]) => v !== undefined)),
  }));
  useEffect(() => {
    if (location.hash.startsWith("#settings")) return;
    try { localStorage.setItem("main-view", JSON.stringify({ selection: ui.selection, sidebar: ui.sidebar, view: ui.view, archived: ui.archived })); }
    catch { /* Main-window context is optional when storage is unavailable. */ }
  }, [ui.selection, ui.sidebar, ui.view, ui.archived]);
  const toastTimer = useRef<ReturnType<typeof setTimeout>>(undefined);

  const refreshStatus = useCallback(async () => {
    try {
      const status = await local.getStatus({});
      setData((d) => merged(d, { status }));
      return status;
    } catch {
      setData((d) => merged(d, { status: null }));
      return null;
    }
  }, []);

  const refreshLists = useCallback(async () => {
    const [m, c, p, d] = await Promise.allSettled([
      local.listMachines({}),
      local.listClients({}),
      local.listProjects({}),
      local.listDevices({}),
    ]);
    setData((x) =>
      merged(x, {
        ...(m.status === "fulfilled" && { machines: m.value.machines }),
        ...(c.status === "fulfilled" && { clients: c.value.clients }),
        ...(p.status === "fulfilled" && { projects: p.value.projects, projectsUnreachable: p.value.unreachable }),
        ...(d.status === "fulfilled" && { devices: d.value.devices }),
      }),
    );
  }, []);

  const refreshPairings = useCallback(async () => {
    try {
      const r = await local.listPairingRequests({});
      setData((d) => merged(d, { pairings: r.pairings }));
    } catch {
      // Kept as they were; the status poll reports an unreachable daemon.
    }
  }, []);

  const refresh = useCallback(async () => {
    const st = await refreshStatus();
    if (st?.initialized) await Promise.all([refreshLists(), refreshPairings()]);
    else if (st?.machineOnly) await refreshLists(); // its own machine, projects and the agents that reached it
  }, [refreshStatus, refreshLists, refreshPairings]);

  useEffect(() => {
    let tick = 0;
    let stopped = false;
    const loop = async () => {
      while (!stopped) {
        if (tick % 3 === 0) await refresh();
        else await refreshPairings();
        tick++;
        await new Promise((r) => setTimeout(r, 1000));
      }
    };
    loop();
    return () => {
      stopped = true;
    };
  }, [refresh, refreshPairings]);

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

  const feed = useSessions(data.status?.initialized === true || data.status?.machineOnly === true);

  const store = useMemo<Store>(
    () => ({
      ...data,
      pairings: data.pairings.filter((p) => !ui.dismissed.includes(p.code)),
      sessions: feed.sessions,
      sessionsUnreachable: feed.unreachable,
      sessionsLoaded: feed.loaded,
      moreSessions: feed.next !== undefined,
      loadOlderSessions: feed.loadOlder,
      ...ui,
      set,
      select,
      refresh,
      run,
      show,
    }),
    [data, feed, ui, set, select, refresh, run, show],
  );
  return <Ctx.Provider value={store}>{children}</Ctx.Provider>;
}

/**
 * The current time, ticking every interval ms (for elapsed times and
 * countdowns). One clock per interval for the whole window, so everything
 * on the same interval changes in the same frame.
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
  let timer: ReturnType<typeof setInterval> | undefined;
  c = {
    subscribe(f) {
      if (subs.size === 0) {
        now = new Date();
        timer = setInterval(() => {
          now = new Date();
          subs.forEach((g) => g());
        }, interval);
      }
      subs.add(f);
      return () => {
        subs.delete(f);
        if (subs.size === 0) clearInterval(timer);
      };
    },
    now: () => now,
  };
  clocks.set(interval, c);
  return c;
}
