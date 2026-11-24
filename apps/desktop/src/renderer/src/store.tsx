import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { ConnectError } from "@connectrpc/connect";
import type { Client, Device, GetStatusResponse, Machine, PairingDetails, Project, Unreachable, Wake } from "@hermesh/proto/local";
import type { Session } from "@hermesh/proto/session";
import { sessionView, unreachableMachines, withKnownState, type SessionFilter, type SessionView } from "@hermesh/core";
import type { Peek } from "./layout";
import { notNow, setNotNow, startPage } from "./start";
import { useWorld } from "./useWorld";
import { i18n } from "../../shared/i18n";
import { needsUpdate } from "./skew";
import { noteRelay } from "./connection";

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

export type Selection =
  | { kind: "overview" }
  | { kind: "newSession"; machineId?: string; projectId?: string }
  | { kind: "session"; id: string }
  | { kind: "her"; machine?: string }
  | { kind: "progress"; machine?: string; task?: ProgressTaskRef };

export type ProgressTaskRef = { agent: string; fromHer?: boolean };

export type Sheet =
  | { kind: "newGrant" }
  | { kind: "addMachine" }
  | { kind: "addProject"; machineId?: string; agent?: string; then?: (projectId: string, worktree: boolean) => void }
  | { kind: "addDevice" }
  | { kind: "enterCode" }
  | { kind: "pairing"; code: string; details?: PairingDetails }
  | { kind: "scope"; clientId: string; renew: boolean }
  | { kind: "wake"; clientId: string; agent: string; wake?: Wake; code?: string; name?: string }
  | { kind: "confirm"; title: string; message: string; button: string; run: (eraseHer: boolean) => Promise<unknown>; done?: string; herData?: { name: string; offline: boolean }; safe?: boolean }
  | { kind: "erase" }
  | { kind: "herOff"; machineId: string; machineName: string };

export type Toast = { text: string; undo?: () => void; failed?: boolean; id: number };

export const defaultView = () => sessionView({ metadata: false });

type UI = {
  selection: Selection;
  inspector: boolean;
  sidebar: boolean;
  peek: Peek;
  view: SessionView;
  viewChosen: boolean;
  archived: boolean;
  collapsed: string[];
  searching: boolean;
  renaming: string | null;
  searchFor: Partial<SessionFilter>;
  sheet: Sheet | null;
  toast: Toast | null;
  dismissed: string[];
  frontRequest: string | null;
  upgrading: boolean;
  herAgents: { waits: number; running: number } | null;
};

export type Store = Data &
  UI & {
    width: number;
    sessions: Session[];
    workVersion: number;
    sessionsUnreachable: Unreachable[];
    sessionsLoaded: boolean;
    moreSessions: boolean;
    loadOlderSessions: () => void;
    set: (patch: Partial<UI>) => void;
    select: (id: string) => void;
    refresh: () => Promise<void>;
    run: (action: () => Promise<unknown>, done?: string) => Promise<boolean>;
    show: (text: string, undo?: () => void) => void;
  };

const Ctx = createContext<Store | null>(null);

export function useStore(): Store {
  const s = useContext(Ctx);
  if (!s) throw new Error("useStore outside HermeshProvider");
  return s;
}

export function errorText(e: unknown): string {
  if (needsUpdate(e)) return i18n.t("common:needsUpdate.text");
  if (declinedToSign(e)) return i18n.t("common:signing.declined");
  if (signerUnavailable(e)) return i18n.t("common:signing.unavailable");
  if (e instanceof ConnectError) return e.rawMessage;
  return e instanceof Error ? e.message : String(e);
}

function declinedToSign(e: unknown): boolean {
  return (e instanceof ConnectError ? e.rawMessage : e instanceof Error ? e.message : String(e)).includes("the owner declined to sign");
}

/** hermeshd's ErrSignerUnavailable: the signer kept exiting before the request could be confirmed. */
function signerUnavailable(e: unknown): boolean {
  return (e instanceof ConnectError ? e.rawMessage : e instanceof Error ? e.message : String(e)).includes("the signer exited unexpectedly before confirming");
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

type SavedView = Pick<UI, "selection" | "sidebar" | "view" | "viewChosen" | "archived" | "collapsed">;
function savedView(): Partial<SavedView> {
  if (location.hash.startsWith("#settings")) return {};
  try {
    const v = JSON.parse(localStorage.getItem("main-view") ?? "{}") as Omit<Partial<SavedView>, "selection"> & {
      selection?: { kind?: string; id?: string; machineId?: string; projectId?: string; machine?: string; task?: ProgressTaskRef };
    };
    const chosen = v.viewChosen === true;
    let selection: Selection | undefined;
    if (v.selection?.kind === "list") {
      selection = undefined;
    } else if (v.selection?.kind === "thread" && v.selection.id) {
      selection = { kind: "session", id: v.selection.id };
    } else if (v.selection?.kind === "newThread") {
      selection = { kind: "newSession", machineId: v.selection.machineId, projectId: v.selection.projectId };
    } else {
      selection = v.selection as Selection | undefined;
    }
    return { selection, sidebar: v.sidebar, view: chosen ? sessionView(v.view) : undefined, viewChosen: chosen, archived: v.archived, collapsed: Array.isArray(v.collapsed) ? v.collapsed : undefined };
  } catch {
    return {};
  }
}

export function HermeshProvider({ children }: { children: ReactNode }) {
  const world = useWorld();
  const unreachable = useMemo(() => unreachableMachines(world.machines), [world.machines]);
  const status = world.down ? null : world.loaded ? world.status : undefined;
  const relayUp = status === undefined || status?.relayConnected === true;
  useEffect(() => noteRelay(relayUp), [relayUp]);
  const data = useMemo<Data>(
    () => ({ status, clients: world.clients, machines: world.machines, projects: world.projects, projectsUnreachable: unreachable, pairings: world.pairings, devices: world.devices }),
    [status, world.clients, world.machines, world.projects, unreachable, world.pairings, world.devices],
  );
  const sessions = useMemo(
    () => withKnownState(world.sessions, world.machines),
    [world.sessions, world.machines],
  );
  const [ui, setUI] = useState<UI>(() => ({
    selection: { kind: "overview" },
    inspector: saved("inspector", true),
    sidebar: true,
    peek: null,
    view: defaultView(),
    viewChosen: false,
    archived: false,
    collapsed: [],
    searching: false,
    renaming: null,
    searchFor: {},
    sheet: null,
    toast: null,
    dismissed: [],
    frontRequest: null,
    upgrading: false,
    herAgents: null,
    ...Object.fromEntries(Object.entries(savedView()).filter(([, v]) => v !== undefined)),
  }));
  const opened = useRef(ui.selection);
  const started = useRef(false);
  useEffect(() => {
    const s = world.status;
    if (started.current || location.hash.startsWith("#settings") || !world.loaded || !s || !(s.initialized || s.machineOnly)) return;
    started.current = true;
    const kind = startPage({ machines: world.machines, machineOnly: s.machineOnly, notNow: notNow(), onSession: opened.current.kind === "session" });
    if (kind) setUI((u) => (u.selection === opened.current ? { ...u, selection: { kind } } : u));
  }, [world.loaded, world.status, world.machines]);
  useEffect(() => {
    if (world.machines.some((m) => m.her?.enabled)) setNotNow(false);
  }, [world.machines]);
  useEffect(() => {
    if (location.hash.startsWith("#settings")) return;
    try { localStorage.setItem("main-view", JSON.stringify({ selection: ui.selection, sidebar: ui.sidebar, view: ui.view, viewChosen: ui.viewChosen, archived: ui.archived, collapsed: ui.collapsed })); }
    catch { /* Main-window context is optional when storage is unavailable. */ }
  }, [ui.selection, ui.sidebar, ui.view, ui.viewChosen, ui.archived, ui.collapsed]);
  const [width, setWidth] = useState(() => window.innerWidth);
  useEffect(() => {
    const resize = () => setWidth(window.innerWidth);
    window.addEventListener("resize", resize);
    return () => window.removeEventListener("resize", resize);
  }, []);
  const toastTimer = useRef<ReturnType<typeof setTimeout>>(undefined);

  const refresh = world.refreshStatus;

  const set = useCallback((patch: Partial<UI>) => {
    if (patch.inspector !== undefined) save("inspector", patch.inspector);
    setUI((u) => ({ ...u, ...patch }));
  }, []);

  const show = useCallback((text: string, undo?: () => void, failed?: boolean) => {
    clearTimeout(toastTimer.current);
    const id = Date.now();
    setUI((u) => ({ ...u, toast: { text, undo, failed, id } }));
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
        const signer = declinedToSign(e) ? await window.hermesh?.signerUpdate().catch(() => undefined) : undefined;
        if (signer) {
          setUI((u) => ({
            ...u,
            sheet: {
              kind: "confirm",
              title: i18n.t("common:signing.title"),
              message: i18n.t("common:signing.body", { version: signer }),
              button: i18n.t("common:signing.button"),
              done: i18n.t("common:signing.updated"),
              safe: true,
              run: async () => window.hermesh?.updateSigner(),
            },
          }));
        } else show(errorText(e), undefined, true);
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
      workVersion: world.workVersion,
      ...ui,
      width,
      set,
      select,
      refresh,
      run,
      show,
    }),
    [data, sessions, world.loaded, world.status, world.next, world.loadOlder, world.workVersion, ui, width, set, select, refresh, run, show],
  );
  return <Ctx.Provider value={store}>{children}</Ctx.Provider>;
}

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
