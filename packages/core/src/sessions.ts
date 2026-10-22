import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { FailureKind, SessionStatus, type Question, type Session, type SessionCursor } from "@hermesh/proto/session";
import { isYesterday, sameDay, withinWeek } from "./calendar";
import { t } from "./i18n";
import { trimWhitespace } from "./text";

// What the main window shows about sessions (the list and its filter, the
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
    (s.status !== SessionStatus.WAITING_INPUT &&
      s.status !== SessionStatus.ARCHIVED &&
      s.progress?.lastFailure !== undefined &&
      s.progress.lastFailure.kind !== FailureKind.INTERRUPTED)
  );
}

export function isActive(s: Session): boolean {
  return (
    s.status === SessionStatus.QUEUED || s.status === SessionStatus.PREPARING || s.status === SessionStatus.RUNNING
  );
}

/** A turn that the owner can interrupt. */
export function isInterruptible(s: Session): boolean {
  return isActive(s) || s.status === SessionStatus.WAITING_INPUT;
}

export function isArchivable(s: Session): boolean {
  return s.status === SessionStatus.IDLE || s.status === SessionStatus.CRASHED;
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
 * watch told of something newer.
 */
export function mergeSessions(list: Session[], changes: Session[]): Session[] {
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

/** Where a session sits in the list, in the list's order. */
export const sessionBuckets = ["attention", "active", "today", "yesterday", "week", "earlier", "archived"] as const;
export type SessionBucket = (typeof sessionBuckets)[number];

/** A section of the list, by its title. */
export function bucketTitle(b: SessionBucket): string {
  return t(`bucket.${b}`);
}

/** Sections that show how many they hold. */
export function bucketCounted(b: SessionBucket): boolean {
  return b === "attention" || b === "active";
}

/** "week" is the six days before today, not the calendar week. */
export function bucketOf(s: Session, now: Date): SessionBucket {
  if (s.status === SessionStatus.ARCHIVED) return "archived";
  if (needsAttention(s)) return "attention";
  if (isActive(s)) return "active";
  const d = updatedDate(s);
  if (sameDay(d, now)) return "today";
  if (isYesterday(d, now)) return "yesterday";
  if (withinWeek(d, now)) return "week";
  return "earlier";
}

/**
 * The session list's filter: by grant (its client id), project, coding
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

export function filterNarrowed(f: SessionFilter): boolean {
  return f.grant !== "" || f.project !== "" || f.agent !== "" || f.machine !== "";
}

export function filterAny(f: SessionFilter): boolean {
  return filterNarrowed(f) || f.archived;
}

export function filterMatches(f: SessionFilter, s: Session): boolean {
  return (
    (f.grant === "" || s.createdBy === f.grant) &&
    (f.project === "" || s.projectId === f.project) &&
    (f.agent === "" || s.agent === f.agent) &&
    (f.machine === "" || s.machineId === f.machine)
  );
}

export interface SessionSection {
  bucket: SessionBucket;
  sessions: Session[];
}

/**
 * The list: what needs the owner first (questions before failures), then
 * what runs (newest first), then the rest by when it last changed.
 */
export function sessionSections(sessions: Session[], filter: SessionFilter, now: Date): SessionSection[] {
  const by = new Map<SessionBucket, Session[]>();
  for (const s of sessions) {
    if (!filterMatches(filter, s)) continue;
    const b = bucketOf(s, now);
    if (b === "archived" && !filter.archived) continue;
    const list = by.get(b);
    if (list) list.push(s);
    else by.set(b, [s]);
  }
  const out: SessionSection[] = [];
  for (const b of sessionBuckets) {
    const list = by.get(b);
    if (!list) continue;
    switch (b) {
      case "attention":
        list.sort((a, c) => {
          const qa = a.status === SessionStatus.WAITING_INPUT;
          const qc = c.status === SessionStatus.WAITING_INPUT;
          return qa !== qc ? (qa ? -1 : 1) : byNewest(updatedDate(a), updatedDate(c));
        });
        break;
      case "active":
        list.sort((a, c) => byNewest(createdDate(a), createdDate(c)));
        break;
      default:
        list.sort((a, c) => byNewest(updatedDate(a), updatedDate(c)));
    }
    out.push({ bucket: b, sessions: list });
  }
  return out;
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
