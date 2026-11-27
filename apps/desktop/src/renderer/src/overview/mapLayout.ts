import type { Client, Machine } from "@hermesh/proto/local";
import type { Session } from "@hermesh/proto/session";

export const OWNER_ID = "owner";

export const W = 820;
const GAP = 64;
const TOP = 13;
const MAX = 6;
// The projects' column shows four (more while more have a session at work), the rest in a line under them.
const PROJECTS = 4;
const COL = { g: 24, m: 448, p: 670 };
const WIDTH = { g: 164, m: 168, p: 134 };
const HEIGHT = { g: 42, m: 46, p: 34 };
export const HUB = { x: 318, s: 36 };
// A machine only's map: grants, this Mac (the hub), projects.
const ONE = { g: { x: 24, w: 196 }, p: { x: 640, w: 150 }, hub: 410 };

export type Kind = "g" | "m" | "p";
export type Spot = { key: string; kind: Kind; x: number; y: number; w: number; h: number };
export type GrantNode = Spot & { kind: "g"; id: string; client?: Client };
export type MachineNode = Spot & { kind: "m"; machine: Machine };
export type ProjectNode = Spot & { kind: "p"; machineId: string; projectId: string; online: boolean };
export type ExtraItem = { key: string; label: string };
export type Extra = Spot & ExtraItem;
export type Path = { id: string; keys: string[]; d: string; count: number; atWork: boolean };
export type MoreKey = "moreGrants" | "moreMachines" | "moreProjects";

export type MapInput = {
  clients: Client[];
  machines: Machine[];
  projects: { machineId: string; projectId: string }[];
  status?: { machineOnly?: boolean } | null;
};

export const grantKey = (id: string) => `g:${id}`;
export const machineKey = (id: string) => `m:${id}`;
export const projectKey = (m: string, p: string) => `p:${m}/${p}`;

/** The keys of the nodes a session runs through: its sender, machine and project. */
export function sessionKeys(s: Pick<Session, "createdBy" | "machineId" | "projectId">): string[] {
  return [grantKey(s.createdBy), machineKey(s.machineId), projectKey(s.machineId, s.projectId)];
}

/** Logarithmic stroke width for merged session routes: 1.3px for 1 session, capped at 2.8px. */
export function flowStrokeWidth(count: number): number {
  return Math.min(2.8, +(1.3 + 0.55 * Math.log2(Math.max(1, count))).toFixed(2));
}

const curve = (x1: number, y1: number, x2: number, y2: number) => {
  const k = (x2 - x1) * 0.55;
  return `C${x1 + k} ${y1} ${x2 - k} ${y2} ${x2} ${y2}`;
};

export type LayoutOptions = {
  pick?: string;
  isWorking?: (s: Session) => boolean;
  moreLabel?: (more: MoreKey, count: number) => string;
};

/** Lays out the columns around the hub's height, the busiest first, at most MAX a column. */
export function layout(st: MapInput, live: Session[], opts: LayoutOptions = {}) {
  const pick = opts.pick ?? "";
  const isWorking = opts.isWorking ?? (() => false);
  const moreLabel = opts.moreLabel ?? ((more: MoreKey, count: number) => `${more}:${count}`);
  const only = st.status?.machineOnly === true;
  const count = (k: (s: Session) => string) => {
    const n = new Map<string, number>();
    const seen = new Set<string>();
    for (const s of live) {
      if (s.sessionId) {
        if (seen.has(s.sessionId)) continue;
        seen.add(s.sessionId);
      }
      n.set(k(s), (n.get(k(s)) ?? 0) + 1);
    }
    return n;
  };
  const byGrant = count((s) => s.createdBy);
  const byProject = count((s) => projectKey(s.machineId, s.projectId));

  const senders: { id: string; client?: Client }[] = [];
  if ((byGrant.get(OWNER_ID) ?? 0) > 0 || pick === grantKey(OWNER_ID)) {
    senders.push({ id: OWNER_ID });
  }
  for (const c of st.clients) {
    if (!c.revoked && c.clientId !== OWNER_ID) senders.push({ id: c.clientId, client: c });
  }
  senders.sort((a, b) => (byGrant.get(b.id) ?? 0) - (byGrant.get(a.id) ?? 0));

  const machines = only ? [] : [...st.machines].sort((a, b) => Number(b.local) - Number(a.local) || Number(b.online) - Number(a.online));
  const projects = new Map<string, { machineId: string; projectId: string }>();
  for (const s of live) projects.set(projectKey(s.machineId, s.projectId), { machineId: s.machineId, projectId: s.projectId });
  for (const p of st.projects) projects.set(projectKey(p.machineId, p.projectId), { machineId: p.machineId, projectId: p.projectId });
  const machineRank = new Map(machines.map((m, i) => [m.machineId, i]));
  const projectList = [...projects.entries()].sort(
    ([ka, a], [kb, b]) =>
      Number((byProject.get(kb) ?? 0) > 0) - Number((byProject.get(ka) ?? 0) > 0) ||
      (machineRank.get(a.machineId) ?? 99) - (machineRank.get(b.machineId) ?? 99),
  );

  const extras: Extra[] = [];
  // A side's nodes, and a line for those it has no room for. Adding a grant or an environment is rare, so the map draws
  // no place for one: the sidebar's account menu and the File menu have them.
  const column = <T,>(items: T[], kind: Kind, more: MoreKey, max = MAX - 1) => {
    const shown = items.length > max + 1 ? items.slice(0, max) : items;
    const rest = items.length - shown.length;
    const tail: ExtraItem[] = [];
    if (rest > 0) tail.push({ key: `more:${kind}`, label: moreLabel(more, rest) });
    return { shown, tail };
  };
  const g = column(senders, "g", "moreGrants");
  const m = column(machines, "m", "moreMachines");
  const busy = projectList.filter(([key]) => (byProject.get(key) ?? 0) > 0).length;
  const p = column(projectList, "p", "moreProjects", Math.min(MAX - 1, Math.max(PROJECTS, busy)));

  const rows = Math.max(g.shown.length + g.tail.length, m.shown.length + m.tail.length, p.shown.length, 1);
  const H = Math.max(190, rows * GAP + TOP + 44);
  // The hub with its label (about 127 tall, the icon on top) is what sits in the middle.
  const cy = TOP + (H - TOP) / 2 - 19;
  const y = (i: number, n: number) => cy + (i - (n - 1) / 2) * GAP;
  const col = (kind: Kind) => (only && kind !== "m" ? ONE[kind] : { x: COL[kind], w: WIDTH[kind] });
  const spot = (kind: Kind, i: number, n: number) => ({ kind, x: col(kind).x, y: y(i, n), w: col(kind).w, h: HEIGHT[kind] });

  const gn = g.shown.length + g.tail.length;
  const grants: GrantNode[] = g.shown.map((x, i) => ({ ...spot("g", i, gn), kind: "g", key: grantKey(x.id), id: x.id, client: x.client }));
  g.tail.forEach((t, i) => extras.push({ ...spot("g", g.shown.length + i, gn), ...t }));
  const mn = m.shown.length + m.tail.length;
  const machineNodes: MachineNode[] = m.shown.map((x, i) => ({ ...spot("m", i, mn), kind: "m", key: machineKey(x.machineId), machine: x }));
  m.tail.forEach((t, i) => extras.push({ ...spot("m", m.shown.length + i, mn), ...t }));
  const pn = p.shown.length;
  const online = new Map(st.machines.map((x) => [x.machineId, x.online]));
  const projectNodes: ProjectNode[] = p.shown.map(([key, x], i) => ({ ...spot("p", i, pn), kind: "p", key, ...x, online: online.get(x.machineId) ?? false }));
  // How many more there are takes no row: a line close under the last.
  p.tail.forEach((t) => extras.push({ ...spot("p", pn - 1, pn), y: y(pn - 1, pn) + HEIGHT.p / 2 + 16, h: 20, ...t }));

  const hx = only ? ONE.hub : HUB.x;
  const hub = { x: hx, y: cy, l: hx - HUB.s - 4, r: hx + HUB.s + 4 };
  const at = new Map<string, Spot>([...grants, ...machineNodes, ...projectNodes].map((n) => [n.key, n]));
  const edges: { keys: string[]; d: string; off: boolean }[] = [];
  for (const n of grants) edges.push({ keys: [n.key], off: Boolean(only && n.client && !n.client.lastSeen), d: `M${n.x + n.w} ${n.y} ${curve(n.x + n.w, n.y, hub.l, hub.y)}` });
  for (const n of machineNodes) edges.push({ keys: [n.key], off: !n.machine.online, d: `M${hub.r} ${hub.y} ${curve(hub.r, hub.y, n.x, n.y)}` });
  for (const n of projectNodes) {
    if (only) {
      edges.push({ keys: [n.key], off: !n.online, d: `M${hub.r} ${hub.y} ${curve(hub.r, hub.y, n.x, n.y)}` });
      continue;
    }
    const mm = at.get(machineKey(n.machineId));
    if (mm) edges.push({ keys: [mm.key, n.key], off: !n.online, d: `M${mm.x + mm.w} ${mm.y} ${curve(mm.x + mm.w, mm.y, n.x, n.y)}` });
  }

  const paths: Path[] = [];
  const byRoute = new Map<string, Path>();
  const seenSessions = new Set<string>();
  for (const s of live) {
    if (s.sessionId) {
      if (seenSessions.has(s.sessionId)) continue;
      seenSessions.add(s.sessionId);
    }
    const a = at.get(grantKey(s.createdBy));
    const b = at.get(machineKey(s.machineId));
    const c = at.get(projectKey(s.machineId, s.projectId));
    let keys: string[];
    let d: string;
    if (only) {
      if (!a || !c) continue;
      keys = [a.key, c.key];
      d = `M${a.x + a.w} ${a.y} ${curve(a.x + a.w, a.y, hub.l, hub.y)} L${hub.r} ${hub.y} ${curve(hub.r, hub.y, c.x, c.y)}`;
    } else {
      if (!a || !b || !c) continue;
      keys = [a.key, b.key, c.key];
      d =
        `M${a.x + a.w} ${a.y} ${curve(a.x + a.w, a.y, hub.l, hub.y)} L${hub.r} ${hub.y} ` +
        `${curve(hub.r, hub.y, b.x, b.y)} L${b.x + b.w} ${b.y} ${curve(b.x + b.w, b.y, c.x, c.y)}`;
    }
    const id = keys.join("|");
    const existing = byRoute.get(id);
    if (existing) {
      existing.count += 1;
      existing.atWork = existing.atWork || isWorking(s);
    } else {
      const route: Path = { id, keys, d, count: 1, atWork: isWorking(s) };
      byRoute.set(id, route);
      paths.push(route);
    }
  }
  return { H, hub, grants, machines: machineNodes, projects: projectNodes, extras, edges, paths, at };
}
