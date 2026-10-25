import type { Client, Device, GetStatusResponse, Machine, PairingDetails, Project, Unreachable, WatchResponse } from "@hermesh/proto/local";
import { SessionStatus, type Session, type SessionCursor } from "@hermesh/proto/session";
import { mergeSessions, relistedCursor } from "./sessions";

/**
 * What a screen knows of the owner's world, as hermeshd's LocalService.Watch
 * pushes it (docs/state-sync.md): no screen polls. Sessions also hold the
 * older pages a list loaded.
 */
export type World = {
  /** undefined until the first message. */
  status?: GetStatusResponse;
  machines: Machine[];
  clients: Client[];
  devices: Device[];
  projects: Project[];
  pairings: PairingDetails[];
  sessions: Session[];
  /** The first message arrived. */
  loaded: boolean;
  /** Where older sessions start; undefined when there are none. */
  next?: SessionCursor;
};

export const emptyWorld: World = { machines: [], clients: [], devices: [], projects: [], pairings: [], sessions: [], loaded: false };

/** Replaces each item that changed (by key) where it was, adds new ones, and drops those removed; list itself when nothing changed. */
function upsert<T>(list: T[], changed: T[], removed: string[], key: (x: T) => string): T[] {
  if (changed.length === 0 && removed.length === 0) return list;
  const gone = new Set(removed);
  const out = list.filter((x) => !gone.has(key(x)));
  const at = new Map(out.map((x, i) => [key(x), i]));
  for (const c of changed) {
    const i = at.get(key(c));
    if (i === undefined) {
      at.set(key(c), out.length);
      out.push(c);
    } else out[i] = c;
  }
  return out;
}

const byName = (a: Machine, b: Machine) => a.machineName.localeCompare(b.machineName);
const byLabel = (a: Client, b: Client) => a.label.localeCompare(b.label);
const projectKey = (p: { machineId: string; projectId: string }) => `${p.machineId}/${p.projectId}`;

/** The world with a Watch message applied (w itself when it changed nothing). */
export function applyWatch(w: World, m: WatchResponse): World {
  const base: World = m.reset ? { ...emptyWorld, sessions: w.sessions, next: w.next, loaded: w.loaded, status: w.status } : w;
  const next: World = {
    ...base,
    status: m.status ?? base.status,
    machines: upsert(base.machines, m.machines, m.removedMachineIds, (x) => x.machineId),
    clients: upsert(base.clients, m.clients, m.removedGrantIds, (x) => x.grantId),
    devices: upsert(base.devices, m.devices, m.removedDeviceKeyIds, (x) => x.keyId),
    projects: upsert(base.projects, m.projects, m.removedProjects.map(projectKey), projectKey),
    pairings: upsert(base.pairings, m.pairings, m.removedPairingCodes, (x) => x.code),
    sessions: mergeSessions(base.sessions, m.sessions, m.deletedSessionIds),
    loaded: true,
  };
  if (m.reset) next.next = w.loaded ? relistedCursor(w.next, m.next) : m.next;
  if (next.machines !== base.machines) next.machines = [...next.machines].sort(byName);
  if (next.clients !== base.clients) next.clients = [...next.clients].sort(byLabel);
  const same = (Object.keys(next) as (keyof World)[]).every((k) => next[k] === w[k]);
  return same ? w : next;
}

/** The machines whose sessions and projects cannot be read now. */
export function unreachableMachines(machines: Machine[]): Unreachable[] {
  return machines.filter((m) => m.unreachable !== "").map((m) => ({ $typeName: "hermesh.local.v1.Unreachable", machineId: m.machineId, machineName: m.machineName, error: m.unreachable }) as Unreachable);
}

/**
 * Whether a session's state is known now: not when its machine cannot be
 * read (it may have finished, or asked something, meanwhile). Such a
 * session counts neither as in progress nor as needing the owner, and can
 * be neither interrupted, answered nor archived until it can be read again.
 */
export function stateKnown(s: Session, machines: Machine[]): boolean {
  return (machines.find((m) => m.machineId === s.machineId)?.unreachable ?? "") === "";
}

/**
 * The sessions as screens show them: one whose state is not known now
 * (stateKnown) has an unspecified status, which every check treats as
 * neither in progress, waiting nor failed, and which reads "State unknown".
 */
export function withKnownState(sessions: Session[], machines: Machine[]): Session[] {
  if (!machines.some((m) => m.unreachable !== "")) return sessions;
  return sessions.map((s) => (s.status === SessionStatus.ARCHIVED || stateKnown(s, machines) ? s : { ...s, status: SessionStatus.UNSPECIFIED }));
}
