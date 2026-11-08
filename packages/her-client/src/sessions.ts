import type { SessionState, SessionSummary } from "@hermesh/her-protocol";
import { isYesterday, sameDay, withinWeek } from "./calendar.ts";
import { baseName, modelName } from "./text.ts";

// What the sidebar and search show about sessions, worked out from the
// sessions alone so it can be checked without a window.

export type ListSession = SessionSummary;

/** The state, also from a kernel without P2: busy is running. */
export function stateOf(s: ListSession): SessionState {
  // A loaded session running a task is running, whatever an older summary says.
  return s.state === "idle" && s.busy ? "running" : s.state;
}

/** Waiting for an approval, or its last task failed. */
export function needsAttention(s: ListSession): boolean {
  const st = stateOf(s);
  return st === "awaiting_approval" || st === "failed";
}

export function isRunning(s: ListSession): boolean {
  return stateOf(s) === "running";
}

/** A task has started and not ended: running, or waiting for an approval. */
export function inTask(s: ListSession): boolean {
  const st = stateOf(s);
  return st === "running" || st === "awaiting_approval";
}

/** The title, else the start of its first message, else its project (empty without one). */
export function displayTitle(s: Pick<ListSession, "title" | "preview" | "no_project" | "cwd">): string {
  return s.title?.trim() || s.preview?.trim().split("\n")[0] || (s.no_project ? "" : baseName(s.cwd));
}

/** The project a session works in, or null when it was started without one. */
export function projectOf(s: ListSession): string | null {
  return s.no_project ? null : s.cwd;
}

/** The projects of these sessions, each once. */
export function projectsOf(list: ListSession[]): string[] {
  return [...new Set(list.flatMap((s) => (s.no_project ? [] : [s.cwd])))];
}

/**
 * Each summary's time, parsed once: a summary is replaced, never changed, when its session moves, and
 * sorting thousands of sessions on every change of one would otherwise parse dates hundreds of thousands of times.
 */
const parsed = new WeakMap<ListSession, number>();

export function updatedAt(s: ListSession): number {
  let t = parsed.get(s);
  if (t === undefined) parsed.set(s, (t = Date.parse(s.updated_at) || 0));
  return t;
}

/** The model's short name for a list row ("deepseek-flash"). */
export function rowModel(s: ListSession): string {
  return s.model ? modelName(s.model) : "";
}

/**
 * A short name for each project directory: its last segment, with the one
 * above it where two projects share a name ("app · client", "app · server").
 */
export function projectNames(cwds: Iterable<string>): Map<string, string> {
  const all = [...new Set(cwds)];
  const byBase = new Map<string, string[]>();
  for (const c of all) {
    const b = baseName(c);
    byBase.set(b, [...(byBase.get(b) ?? []), c]);
  }
  const out = new Map<string, string>();
  for (const [b, list] of byBase) {
    for (const c of list) {
      const parts = c.split(/[\\/]+/).filter(Boolean);
      out.set(c, list.length > 1 && parts.length > 1 ? `${b} · ${parts.at(-2)}` : b);
    }
  }
  return out;
}

/** How the session list groups its sessions. */
export const groupings = ["status", "date", "project"] as const;
export type Grouping = (typeof groupings)[number];

/** How the list is laid out: grouping, which way it sorts by last activity, and whether rows show their metadata line (off until chosen: titles only). */
export interface ListView {
  group: Grouping;
  newestFirst: boolean;
  metadata: boolean;
}

export function listView(v: Partial<ListView> = {}): ListView {
  const group = groupings.includes(v.group as Grouping) ? (v.group as Grouping) : "status";
  return { newestFirst: true, metadata: false, ...v, group };
}

/** A section's key: a state or a day (the page titles them), a project directory ("" for no project), or "archived". */
export type Bucket = "attention" | "active" | "done" | "today" | "yesterday" | "week" | "earlier" | "archived";

const statusOrder: Bucket[] = ["attention", "active", "done"];
const dayOrder: Bucket[] = ["today", "yesterday", "week", "earlier"];

export function statusBucket(s: ListSession): Bucket {
  return needsAttention(s) ? "attention" : isRunning(s) ? "active" : "done";
}

/** The day it was last active; "week" is the six days before today, not the calendar week. */
export function dayBucket(s: ListSession, now: Date): Bucket {
  const d = new Date(updatedAt(s));
  return sameDay(d, now) ? "today" : isYesterday(d, now) ? "yesterday" : withinWeek(d, now) ? "week" : "earlier";
}

export interface Section {
  grouping: Grouping | "archived";
  key: string;
  sessions: ListSession[];
}

/** Sections that show how many they hold: what needs the owner and what runs. */
export function sectionCounted(sec: Section): boolean {
  return sec.grouping === "status" && (sec.key === "attention" || sec.key === "active");
}

/**
 * The list as the view lays it out, each section sorted by last activity.
 * States keep their order (needs you, running, done); days follow the sort;
 * projects go by name, those without one last. Archived sessions, when shown, are a section of their
 * own at the end.
 */
export function sections(list: ListSession[], view: ListView, o: { archived: boolean; now: Date }): Section[] {
  const by = new Map<string, ListSession[]>();
  const archived: ListSession[] = [];
  for (const s of list) {
    if (s.archived) {
      if (o.archived) archived.push(s);
      continue;
    }
    const k = view.group === "status" ? statusBucket(s) : view.group === "date" ? dayBucket(s, o.now) : (projectOf(s) ?? "");
    const l = by.get(k);
    if (l) l.push(s);
    else by.set(k, [s]);
  }
  const names = projectNames(by.keys());
  // Sessions without a project come after the projects.
  const order =
    view.group === "status" ? statusOrder
    : view.group === "date" ? (view.newestFirst ? dayOrder : [...dayOrder].reverse())
    : [...by.keys()].sort((a, b) => +(a === "") - +(b === "") || (names.get(a) ?? a).localeCompare(names.get(b) ?? b) || a.localeCompare(b));
  const sorted = (l: ListSession[]) => l.sort((a, b) => (view.newestFirst ? 1 : -1) * (updatedAt(b) - updatedAt(a)));
  const out: Section[] = order.filter((k) => by.has(k)).map((k) => ({ grouping: view.group, key: k, sessions: sorted(by.get(k)!) }));
  if (archived.length) out.push({ grouping: "archived", key: "archived", sessions: sorted(archived) });
  return out;
}

/** The search palette's filter: a project directory and a model (empty matches all), and whether archived sessions show. */
export interface SearchFilter {
  project: string;
  model: string;
  archived: boolean;
}

export function searchFilter(f: Partial<SearchFilter> = {}): SearchFilter {
  return { project: "", model: "", archived: false, ...f };
}

/** Sessions matching a search: those needing the owner first, then running ones, then by when they last changed. */
export function searchSessions(list: ListSession[], query: string, f: SearchFilter): ListSession[] {
  const q = query.trim().toLowerCase();
  const rank = (s: ListSession) => (needsAttention(s) ? 0 : isRunning(s) ? 1 : 2);
  return list
    .filter(
      (s) =>
        (f.archived || !s.archived) &&
        (f.project === "" || projectOf(s) === f.project) &&
        (f.model === "" || s.model === f.model) &&
        (q === "" || [displayTitle(s), s.preview ?? "", s.cwd, s.model ?? ""].join(" ").toLowerCase().includes(q)),
    )
    .sort((a, b) => rank(a) - rank(b) || updatedAt(b) - updatedAt(a));
}

/** The list with changes applied: each session replaces its earlier state or joins; deleted ones leave. */
export function mergeSessions(list: ListSession[], changed: ListSession[], deleted: string[] = []): ListSession[] {
  let out = deleted.length ? list.filter((s) => !deleted.includes(s.session_id)) : list;
  if (!changed.length) return out;
  out = [...out];
  // A change or two, as her sends them: looked up directly; many at once: through an index.
  const at = changed.length > 8 ? new Map(out.map((s, i) => [s.session_id, i])) : null;
  for (const c of changed) {
    const i = at ? at.get(c.session_id) : out.findIndex((s) => s.session_id === c.session_id);
    if (i === undefined || i < 0) {
      at?.set(c.session_id, out.length);
      out.push(c);
    } else out[i] = c;
  }
  return out;
}

/** The projects sessions were run in, the most recently used first. */
export function recentProjects(list: ListSession[], limit = 6): string[] {
  const latest = new Map<string, number>();
  for (const s of list) if (!s.no_project) latest.set(s.cwd, Math.max(latest.get(s.cwd) ?? 0, updatedAt(s)));
  return [...latest].sort((a, b) => b[1] - a[1]).slice(0, limit).map(([cwd]) => cwd);
}
