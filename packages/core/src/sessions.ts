import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { FailureKind, GitState, SessionStatus, type Question, type Session, type SessionCursor } from "@hermesh/proto/session";
import { codingAgentTitle } from "./agents";
import { isYesterday, sameDay, withinWeek } from "./calendar";
import { t } from "./i18n";
import { trimWhitespace } from "./text";

// What the main window shows about sessions (the list, search, the
// overview), worked out from the sessions alone so it can be checked
// without a window.

/** A timestamp's date; undefined for an unset timestamp. */
export function dateOf(ts: Timestamp | undefined): Date | undefined {
  return ts && ts.seconds > 0n ? timestampDate(ts) : undefined;
}

/** Before any session, for one without timestamps. */
const distantPast = new Date("0001-01-01T00:00:00Z");

/** The title, or the project for a session from an older machine. */
export function displayTitle(s: Session): string {
  return s.title === "" ? s.projectId : s.title;
}

/** Waiting on the owner, or stopped by a failure. */
export function needsAttention(s: Session): boolean {
  return s.status === SessionStatus.WAITING_INPUT || isFailed(s);
}

/** Crashed, or its last turn failed (a turn stopped on purpose did not fail). */
export function isFailed(s: Session): boolean {
  return (
    s.status === SessionStatus.CRASHED ||
    (s.status === SessionStatus.IDLE &&
      s.progress?.lastFailure !== undefined &&
      s.progress.lastFailure.kind !== FailureKind.INTERRUPTED)
  );
}

export function isActive(s: Session): boolean {
  return (
    s.status === SessionStatus.QUEUED ||
    s.status === SessionStatus.PREPARING ||
    s.status === SessionStatus.RUNNING ||
    (s.status === SessionStatus.IDLE && s.activeBackgroundCount > 0)
  );
}

/** A turn that the owner can interrupt. */
export function isInterruptible(s: Session): boolean {
  return isActive(s) || s.status === SessionStatus.WAITING_INPUT;
}

export type GitMark = "pushed" | "merged";

/**
 * The sidebar row's delivered-code mark: shown only when a settled session's
 * code has been pushed to a remote or merged into the base branch.
 */
export function sessionGitMark(s: Session): GitMark | null {
  if (needsAttention(s) || isActive(s) || s.status === SessionStatus.UNSPECIFIED) return null;
  switch (s.gitStatus?.state) {
    case GitState.PUSHED:
      return "pushed";
    case GitState.MERGED:
      return "merged";
    default:
      return null;
  }
}

export type DiffBlock = "add" | "del" | "empty";

/** GitHub-style 5-square proportional diff bar for +insertions / -deletions. */
export function diffBarBlocks(insertions: number, deletions: number): [DiffBlock, DiffBlock, DiffBlock, DiffBlock, DiffBlock] {
  const ins = Math.max(0, Math.trunc(insertions));
  const del = Math.max(0, Math.trunc(deletions));
  const total = ins + del;
  if (total === 0) return ["empty", "empty", "empty", "empty", "empty"];
  let addCount: number;
  let delCount: number;
  if (total < 5) {
    addCount = ins;
    delCount = del;
  } else {
    addCount = Math.round((ins / total) * 5);
    if (ins > 0 && addCount === 0) addCount = 1;
    if (del > 0 && addCount === 5) addCount = 4;
    delCount = 5 - addCount;
  }
  const out: DiffBlock[] = [];
  for (let i = 0; i < addCount; i++) out.push("add");
  for (let i = 0; i < delCount; i++) out.push("del");
  while (out.length < 5) out.push("empty");
  return out as [DiffBlock, DiffBlock, DiffBlock, DiffBlock, DiffBlock];
}

/**
 * Settled non-archived sessions whose local Git status may have advanced
 * outside Hermesh (e.g. manual commit/push/merge in terminal or IDE).
 */
export function sessionsNeedingGitRefresh(sessions: readonly Session[], selectedId?: string | null, limit = 20): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  const eligible = (s: Session) => {
    if (isActive(s) || s.status === SessionStatus.ARCHIVED || s.status === SessionStatus.UNSPECIFIED) return false;
    const st = s.gitStatus?.state ?? GitState.UNSPECIFIED;
    return st === GitState.UNSPECIFIED || st === GitState.DIRTY || st === GitState.UNPUSHED || st === GitState.PUSHED;
  };
  if (selectedId) {
    const sel = sessions.find((s) => s.sessionId === selectedId);
    if (sel && eligible(sel)) {
      seen.add(sel.sessionId);
      out.push(sel.sessionId);
    }
  }
  for (const s of sessions) {
    if (out.length >= limit) break;
    if (seen.has(s.sessionId)) continue;
    if (eligible(s)) {
      seen.add(s.sessionId);
      out.push(s.sessionId);
    }
  }
  return out;
}

export function isArchivable(s: Session): boolean {
  return (s.status === SessionStatus.IDLE && s.activeBackgroundCount === 0) || s.status === SessionStatus.CRASHED;
}

export function updatedDate(s: Session): Date {
  return dateOf(s.updatedAt) ?? distantPast;
}

export function createdDate(s: Session): Date {
  return dateOf(s.createdAt) ?? updatedDate(s);
}

/** The question the agent waits on, if any. */
export function pendingQuestion(s: Session): Question | undefined {
  return s.status === SessionStatus.WAITING_INPUT ? s.progress?.pendingQuestion : undefined;
}

/** The turn in progress, or the last one (turn_count counts finished ones). */
export function currentTurn(s: Session): number {
  return (s.progress?.turnCount ?? 0) + (isActive(s) || s.status === SessionStatus.WAITING_INPUT ? 1 : 0);
}

/**
 * Whether a is a later state of its session than b: it has more events, or
 * as many and changed no earlier (a change without an event, such as a new
 * diff stat, only moves updated_at).
 */
export function isLaterState(a: Session, b: Session): boolean {
  const ea = a.progress?.lastEventSeq ?? 0n;
  const eb = b.progress?.lastEventSeq ?? 0n;
  return ea !== eb ? ea > eb : updatedDate(a) >= updatedDate(b);
}

/**
 * The list with changes applied (a page, or what a watch pushed): each
 * session replaces an earlier state of itself, or joins the list. An
 * earlier state than the list's is dropped: a page can arrive after a
 * watch told of something newer. Deleted sessions leave the list.
 */
export function mergeSessions(list: Session[], changes: Session[], deleted: string[] = []): Session[] {
  if (deleted.length > 0) list = list.filter((s) => !deleted.includes(s.sessionId));
  if (changes.length === 0) return list;
  const out = [...list];
  const at = new Map(out.map((s, i) => [s.sessionId, i]));
  for (const c of changes) {
    const i = at.get(c.sessionId);
    if (i === undefined) {
      at.set(c.sessionId, out.length);
      out.push(c);
    } else if (isLaterState(c, out[i])) {
      out[i] = c;
    }
  }
  return out;
}

/** Where a page ends, as a date and id to compare (undefined: the end of the list, past everything). */
function cursorKey(c: SessionCursor | undefined): [number, string] {
  return c ? [dateOf(c.updatedAt)?.getTime() ?? 0, c.sessionId] : [-Infinity, ""];
}

/**
 * Where older pages go on after the first page was listed again (a
 * reconnect, or a machine that came back): from the newer of where the
 * loaded pages ended and where the new first page ends, so nothing in
 * between is skipped. undefined: no more.
 */
export function relistedCursor(loaded: SessionCursor | undefined, first: SessionCursor | undefined): SessionCursor | undefined {
  const [la, li] = cursorKey(loaded);
  const [fa, fi] = cursorKey(first);
  return la > fa || (la === fa && li > fi) ? loaded : first;
}

/** Newest first. */
function byNewest(a: Date, b: Date): number {
  return b.getTime() - a.getTime();
}

/** How the session list groups its sessions. */
export const sessionGroupings = ["sender", "status", "date", "project", "agent", "machine"] as const;
export type SessionGrouping = (typeof sessionGroupings)[number];

/** How the session list sorts each section: workbench default (active by creation, others by last activity), creation time, or last activity. */
export const sessionSorts = ["default", "created", "updated"] as const;
export type SessionSort = (typeof sessionSorts)[number];

/**
 * How the session list is laid out: its grouping, how each section sorts,
 * which direction it goes, and whether each row shows its metadata line
 * (grant, coding agent, project and time).
 */
export interface SessionView {
  group: SessionGrouping;
  sort: SessionSort;
  newestFirst: boolean;
  metadata: boolean;
  /** The work Her sent from a conversation of the owner's with her sits under it (docs/her-conversations.md §3.4). */
  nest: boolean;
}

export function sessionView(v: Partial<SessionView> = {}): SessionView {
  const group: SessionGrouping = v.group && (sessionGroupings as readonly string[]).includes(v.group) ? v.group : "status";
  const sort: SessionSort = v.sort && (sessionSorts as readonly string[]).includes(v.sort) ? v.sort : "default";
  return { group, sort, newestFirst: v.newestFirst ?? true, metadata: v.metadata ?? true, nest: v.nest ?? true };
}

/** The agent of a conversation of the owner's with Her (docs/her-conversations.md). */
export const HER_AGENT = "her";

/** A conversation of the owner's with Her, a session of the her provider. */
export function isHerConversation(s: Session): boolean {
  return s.agent === HER_AGENT || s.modelSelection?.instanceId === HER_AGENT;
}

/** The conversation with Her that this work was sent from, if any. */
export function herParentOf(s: Session): string | undefined {
  return s.lineage?.relationship === HER_AGENT && s.lineage.parentSessionId ? s.lineage.parentSessionId : undefined;
}

/** The key of the project section that holds Her's conversations: they belong to no project. */
export const HER_SECTION = "her";

/**
 * The work under each conversation with Her, newest first, while the view nests it: only work whose
 * conversation is in `sessions` too, and only while neither is archived (an archived one is listed
 * on its own).
 */
export function childrenOf(sessions: readonly Session[], view: Pick<SessionView, "nest">): Map<string, Session[]> {
  const out = new Map<string, Session[]>();
  if (!view.nest) return out;
  const parents = new Set(sessions.filter((s) => isHerConversation(s) && s.status !== SessionStatus.ARCHIVED).map((s) => s.sessionId));
  for (const s of sessions) {
    const parent = herParentOf(s);
    if (!parent || !parents.has(parent) || s.status === SessionStatus.ARCHIVED) continue;
    const list = out.get(parent);
    if (list) list.push(s);
    else out.set(parent, [s]);
  }
  for (const list of out.values()) list.sort((a, b) => byNewest(updatedDate(a), updatedDate(b)));
  return out;
}

/** A conversation and the work under it: the most pressing state of them (needs you, runs, done). */
export function treeStatus(s: Session, children: readonly Session[] = []): SessionBucket {
  const tree = [s, ...children];
  return tree.some(needsAttention) ? "attention" : tree.some(isActive) ? "active" : "done";
}

/** A conversation and the work under it: the last time any of them was active. */
export function treeUpdated(s: Session, children: readonly Session[] = []): Date {
  return new Date(Math.max(updatedDate(s).getTime(), ...children.map((c) => updatedDate(c).getTime())));
}

/**
 * The mark a collapsed conversation's row shows: the most pressing of its own and its work's (an
 * orange dot while any waits for the owner, a red one when any failed), else its own.
 */
export function treeMark(s: Session, children: readonly Session[]): "waiting" | "failed" | undefined {
  const tree = [s, ...children];
  if (tree.some((x) => x.status === SessionStatus.WAITING_INPUT)) return "waiting";
  if (tree.some(isFailed)) return "failed";
  return undefined;
}

/** A conversation with Her is archived with its work, so only once all of it can be. */
export function isTreeArchivable(s: Session, children: readonly Session[]): boolean {
  return isArchivable(s) && children.every((c) => isArchivable(c) || c.status === SessionStatus.ARCHIVED);
}

/** Work under a conversation shows five at once; what runs or waits always shows (docs/her-conversations.md D11). */
export const CHILDREN_INITIAL_LIMIT = 5;

/** The work a conversation's row shows, and how much stays behind "Show more". */
export function sliceChildren(children: readonly Session[], expanded: boolean, selectedId?: string | null): SectionSlice {
  if (expanded || children.length <= CHILDREN_INITIAL_LIMIT) return { visible: [...children], hiddenCount: 0 };
  const keep = new Set(children.filter((c) => c.sessionId === selectedId || needsAttention(c) || isActive(c)).map((c) => c.sessionId));
  for (const c of children) {
    if (keep.size >= CHILDREN_INITIAL_LIMIT) break;
    keep.add(c.sessionId);
  }
  const visible = children.filter((c) => keep.has(c.sessionId));
  return { visible, hiddenCount: children.length - visible.length };
}

/** A section's key: pinned, a state or a day (titled by bucketTitle), who sent the work (a client id, or "owner"), a project, a coding agent, a machine id, or "archived". */
export type SessionBucket = "pinned" | "attention" | "active" | "done" | "today" | "yesterday" | "week" | "earlier" | "archived";

/** A section of the list, by its title. */
export function bucketTitle(b: SessionBucket): string {
  return t(`bucket.${b}`);
}

const statusOrder: SessionBucket[] = ["attention", "active", "done"];
const dayOrder: SessionBucket[] = ["today", "yesterday", "week", "earlier"];

/** Needs the owner, runs, or is done. */
export function statusOf(s: Session): SessionBucket {
  return needsAttention(s) ? "attention" : isActive(s) ? "active" : "done";
}

/** When a session's current state began for activity sorting without intra-turn heartbeat jitter. */
export function activityDate(s: Session): Date {
  return isActive(s) && !needsAttention(s) ? (dateOf(s.progress?.turnStartedAt) ?? createdDate(s)) : updatedDate(s);
}

/** The day it was last active; "week" is the six days before today, not the calendar week. */
export function dayOf(s: Session, now: Date, at?: Date): SessionBucket {
  const d = at ?? updatedDate(s);
  return sameDay(d, now) ? "today" : isYesterday(d, now) ? "yesterday" : withinWeek(d, now) ? "week" : "earlier";
}

/** Places `movedId` at insertion slot `slot` (0..currentIds.length) among `currentIds`. */
export function reorderIds(currentIds: readonly string[], movedId: string, slot: number): string[] {
  const orig = currentIds.indexOf(movedId);
  const without = currentIds.filter((id) => id !== movedId);
  const idx = orig !== -1 && orig < slot ? slot - 1 : slot;
  without.splice(Math.max(0, Math.min(without.length, idx)), 0, movedId);
  return without;
}

export interface SessionSection {
  grouping: SessionGrouping | "pinned" | "archived";
  key: string;
  sessions: Session[];
}

function sortWithManualOrder(
  list: Session[],
  timeOf: (s: Session) => number,
  newestFirst: boolean,
  manualOrder?: readonly string[],
  manualApplies: (s: Session) => boolean = () => true,
): Session[] {
  const dir = newestFirst ? 1 : -1;
  const cmpTime = (ta: number, tb: number) => dir * (tb - ta);
  if (!manualOrder || manualOrder.length === 0) {
    return [...list].sort((a, b) => cmpTime(timeOf(a), timeOf(b)));
  }
  const present = new Map<string, Session>();
  for (const s of list) {
    if (manualApplies(s)) present.set(s.sessionId, s);
  }
  const orderedIds = manualOrder.filter((id) => present.has(id));
  if (orderedIds.length === 0) {
    return [...list].sort((a, b) => cmpTime(timeOf(a), timeOf(b)));
  }
  const slotTimes = orderedIds.map((id) => timeOf(present.get(id)!)).sort((ta, tb) => cmpTime(ta, tb));
  const assigned = new Map<string, [number, number]>();
  orderedIds.forEach((id, rank) => assigned.set(id, [slotTimes[rank], rank]));
  return [...list].sort((a, b) => {
    const ma = assigned.get(a.sessionId);
    const mb = assigned.get(b.sessionId);
    const ta = ma ? ma[0] : timeOf(a);
    const tb = mb ? mb[0] : timeOf(b);
    if (ta !== tb) return cmpTime(ta, tb);
    if (ma && mb) return ma[1] - mb[1];
    return 0;
  });
}

/**
 * The list as the view lays it out. Pinned sessions sit in their own section
 * at the top (whatever the grouping); active sessions sort by creation time
 * (plus any manual drag order) so concurrent tool heartbeats never jump rows,
 * while sessions needing attention or done sort by when they settled.
 */
export function sessionSections(
  sessions: Session[],
  view: SessionView,
  o: { archived: boolean; now: Date; machines?: string[]; her?: string[]; alwaysActive?: boolean; pinnedOrder?: readonly string[]; activeOrder?: readonly string[] },
): SessionSection[] {
  const by = new Map<string, Session[]>();
  const pinned: Session[] = [];
  const archived: Session[] = [];
  // Nested, the work under a conversation with Her goes where the conversation goes, and the
  // conversation is placed by all of it (docs/her-conversations.md D8, D9).
  const children = childrenOf(sessions, view);
  const nested = new Set([...children.values()].flat().map((c) => c.sessionId));
  const kidsOf = (s: Session) => children.get(s.sessionId) ?? [];
  for (const s of sessions) {
    if (s.status === SessionStatus.ARCHIVED) {
      if (o.archived) archived.push(s);
      continue;
    }
    if (nested.has(s.sessionId)) continue;
    if (s.pinned) {
      pinned.push(s);
      continue;
    }
    const k = {
      sender: () => s.createdBy,
      status: () => treeStatus(s, kidsOf(s)),
      date: () => dayOf(s, o.now, treeUpdated(s, kidsOf(s))),
      project: () => (isHerConversation(s) ? HER_SECTION : s.projectId),
      agent: () => s.agent,
      machine: () => s.machineId,
    }[view.group]();
    const list = by.get(k);
    if (list) list.push(s);
    else by.set(k, [s]);
  }
  const keys = [...by.keys()];
  const at = (k: string) => {
    const i = o.machines?.indexOf(k) ?? -1;
    return i < 0 ? Infinity : i;
  };
  // Her 0, the agents 1, the owner 2; the agents among themselves by their latest session.
  const senderRank = (k: string) => (o.her?.includes(k) ? 0 : k === "owner" ? 2 : 1);
  const latest = (k: string) => Math.max(...by.get(k)!.map((s) => updatedDate(s).getTime()));
  const order =
    view.group === "sender" ? keys.sort((a, b) => senderRank(a) - senderRank(b) || latest(b) - latest(a) || a.localeCompare(b))
    : view.group === "status" ? statusOrder
    : view.group === "date" ? (view.newestFirst ? dayOrder : [...dayOrder].reverse())
    : view.group === "project" ? keys.sort((a, b) => Number(b === HER_SECTION) - Number(a === HER_SECTION) || a.localeCompare(b))
    : view.group === "agent" ? keys.sort((a, b) => Number(b === HER_AGENT) - Number(a === HER_AGENT) || codingAgentTitle(a).localeCompare(codingAgentTitle(b)))
    : keys.sort((a, b) => at(a) - at(b) || a.localeCompare(b));

  const timeFor = (s: Session): number => {
    if (view.sort === "created") return createdDate(s).getTime();
    const kids = kidsOf(s);
    // A conversation with work under it moves with its latest activity, whatever is running.
    if (kids.length > 0) return treeUpdated(s, kids).getTime();
    if (view.sort === "updated") return activityDate(s).getTime();
    return isActive(s) && !needsAttention(s) ? createdDate(s).getTime() : updatedDate(s).getTime();
  };
  const sorted = (list: Session[], isActBucket = false) =>
    sortWithManualOrder(
      list,
      timeFor,
      view.newestFirst,
      o.activeOrder,
      (s) => isActBucket || (isActive(s) && !needsAttention(s)),
    );

  // By status, the running section stays while nothing runs, empty, so the sidebar always shows it (docs/her-team.md §16).
  const keep = o.alwaysActive && view.group === "status" && (by.size > 0 || pinned.length > 0);
  const out: SessionSection[] = [];
  if (pinned.length) {
    const pinTime = (s: Session) => (view.sort === "updated" ? activityDate(s).getTime() : createdDate(s).getTime());
    out.push({ grouping: "pinned", key: "pinned", sessions: sortWithManualOrder(pinned, pinTime, view.newestFirst, o.pinnedOrder) });
  }
  for (const k of order) {
    if (by.has(k) || (keep && k === "active")) {
      out.push({ grouping: view.group, key: k, sessions: sorted(by.get(k) ?? [], view.group === "status" && k === "active") });
    }
  }
  if (archived.length) out.push({ grouping: "archived", key: "archived", sessions: sorted(archived) });
  return out;
}

export const SECTION_INITIAL_LIMIT = 5;
export const SECTION_EXPAND_STEP = 10;

export function isCappedGrouping(grouping: SessionSection["grouping"]): boolean {
  return grouping === "project" || grouping === "agent" || grouping === "machine" || grouping === "sender";
}

export interface SectionSlice {
  visible: Session[];
  hiddenCount: number;
}

/**
 * The rows a sidebar section shows at once, and how many stay folded behind
 * "Show more". Status, date and archived sections never fold; entity sections
 * (project, agent, machine, sender) fold past `limit` unless folding would hide
 * at most one row, while keeping anything active, waiting, failed or selected
 * in view in its place.
 */
export function sliceSectionSessions(
  sessions: readonly Session[],
  grouping: SessionSection["grouping"],
  limit = SECTION_INITIAL_LIMIT,
  selectedId?: string | null,
): SectionSlice {
  const cap = Math.max(0, limit);
  if (!isCappedGrouping(grouping) || sessions.length <= cap + 1) {
    return { visible: [...sessions], hiddenCount: 0 };
  }
  const head = sessions.slice(0, cap);
  const pinned = sessions.slice(cap).filter((s) => s.sessionId === selectedId || needsAttention(s) || isActive(s));
  const hiddenCount = sessions.length - head.length - pinned.length;
  if (hiddenCount <= 1) {
    return { visible: [...sessions], hiddenCount: 0 };
  }
  return { visible: [...head, ...pinned], hiddenCount };
}

/**
 * The search palette's filter: by grant (its client id), project, coding
 * agent or machine (empty matches all), and whether archived sessions show.
 */
export interface SessionFilter {
  grant: string;
  project: string;
  agent: string;
  machine: string;
  archived: boolean;
}

export function sessionFilter(f: Partial<SessionFilter> = {}): SessionFilter {
  return { grant: "", project: "", agent: "", machine: "", archived: false, ...f };
}

export function filterMatches(f: SessionFilter, s: Session): boolean {
  return (
    (f.grant === "" || s.createdBy === f.grant) &&
    (f.project === "" || s.projectId === f.project) &&
    (f.agent === "" || s.agent === f.agent) &&
    (f.machine === "" || s.machineId === f.machine)
  );
}

/**
 * Sessions matching a search, those needing the owner first, then running
 * ones, then by when they last changed. text gives each session's
 * searchable words (title, project, grant, agent, machine).
 */
export function searchSessions(
  sessions: Session[],
  query: string,
  filter: SessionFilter,
  text: (s: Session) => string,
): Session[] {
  const q = trimWhitespace(query).toLowerCase();
  const rank = (s: Session) => (needsAttention(s) ? 0 : isActive(s) ? 1 : 2);
  return sessions
    .filter(
      (s) =>
        (filter.archived || s.status !== SessionStatus.ARCHIVED) &&
        filterMatches(filter, s) &&
        (q === "" || text(s).toLowerCase().includes(q)),
    )
    .sort((a, c) => rank(a) - rank(c) || byNewest(updatedDate(a), updatedDate(c)));
}

/** The overview's "Today": what the sessions that ran today did. */
export interface TodayStats {
  sessions: number;
  done: number;
  failed: number;
  files: number;
  insertions: number;
  deletions: number;
  tokens: bigint;
  cost: number;
  /** Sessions started in each of the last hours, oldest first. */
  hours: { hour: number; count: number }[];
  byAgent: { agent: string; count: number }[];
}

export function todayStats(all: Session[], now: Date, hours = 9): TodayStats {
  const today = all.filter((s) => sameDay(updatedDate(s), now) || sameDay(createdDate(s), now) || isActive(s));
  const t: TodayStats = {
    sessions: today.length,
    done: today.filter(
      (s) => !isFailed(s) && (s.status === SessionStatus.IDLE || s.status === SessionStatus.ARCHIVED),
    ).length,
    failed: today.filter(isFailed).length,
    files: 0,
    insertions: 0,
    deletions: 0,
    tokens: 0n,
    cost: 0,
    hours: [],
    byAgent: [],
  };
  for (const s of today) {
    const d = s.progress?.diffStat;
    const u = s.progress?.usage;
    t.files += d?.filesChanged ?? 0;
    t.insertions += d?.insertions ?? 0;
    t.deletions += d?.deletions ?? 0;
    t.tokens += (u?.inputTokens ?? 0n) + (u?.outputTokens ?? 0n);
    t.cost += u?.costUsd ?? 0;
  }
  const hour = now.getHours();
  for (let i = 0; i < hours; i++) {
    const h = hour - (hours - 1) + i;
    if (h < 0) {
      t.hours.push({ hour: h + 24, count: 0 });
      continue;
    }
    const count = today.filter((s) => {
      const c = createdDate(s);
      return sameDay(c, now) && c.getHours() === h;
    }).length;
    t.hours.push({ hour: h, count });
  }
  const counts = new Map<string, number>();
  for (const s of today) counts.set(s.agent, (counts.get(s.agent) ?? 0) + 1);
  t.byAgent = [...counts]
    .map(([agent, count]) => ({ agent, count }))
    .sort((a, b) => b.count - a.count || (a.agent < b.agent ? -1 : a.agent > b.agent ? 1 : 0));
  return t;
}
