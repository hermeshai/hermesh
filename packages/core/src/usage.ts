import type { MessageInitShape } from "@bufbuild/protobuf";
import { WorkBucket, WorkDimension, type QueryWorkRequestSchema, type QueryWorkResponse } from "@hermesh/proto/local";

// The usage heatmap: a year of days by week (columns) and weekday (rows,
// Sunday first), each day's tokens and a level of 0 (none) to 4, and the
// figures above and below it.

export type UsageCell = { date: Date; key: string; tokens: number; sessions: number; level: number };

export type UsageYear = {
  /** weeks × 7 cells, column by column; null past today. */
  cells: (UsageCell | null)[];
  weeks: number;
  /** The first day shown (a Sunday), YYYY-MM-DD: what QueryWork is asked from. */
  since: string;
  total: number;
  today: number;
  /** Days with any tokens. */
  active: number;
  /** Days in a row with tokens, ending today (or yesterday, before today's first turn). */
  streak: number;
  /** The last 30 days against the 30 before, in percent; null without the 30 before. */
  delta: number | null;
  /** Each coding agent's share of the tokens, largest first. */
  byAgent: { agent: string; share: number }[];
};

export const dayKey = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;

/** The first day a year of weeks ending in today's week starts on. */
export function usageSince(today: Date, weeks = 53): string {
  return dayKey(new Date(today.getFullYear(), today.getMonth(), today.getDate() - today.getDay() - (weeks - 1) * 7));
}

/** The QueryWork question for a year of weeks: from usageSince to today, by day and coding agent. */
export function usageRequest(today: Date, weeks = 53, timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone): MessageInitShape<typeof QueryWorkRequestSchema> {
  const since = usageSince(today, weeks);
  const [y, m, d] = since.split("-").map(Number);
  const days = Math.round((new Date(today.getFullYear(), today.getMonth(), today.getDate()).getTime() - new Date(y, m - 1, d).getTime()) / 86_400_000) + 1;
  return { timeZone, firstDay: since, days, bucket: WorkBucket.DAY, groupBy: [WorkDimension.AGENT] };
}

/** A year of usage from QueryWork's answer to usageRequest: each day's tokens and sessions at work, and each agent's share. */
export function usageYear(res: Pick<QueryWorkResponse, "turns" | "sessions"> | null | undefined, today: Date, weeks = 53): UsageYear {
  const since = usageSince(today, weeks);
  const [y, m, dd] = since.split("-").map(Number);
  const keyOf = (bucket: number) => dayKey(new Date(y, m - 1, dd + bucket));
  const tokens = new Map<string, number>();
  const sessions = new Map<string, number>();
  const agents = new Map<string, number>();
  for (const t of res?.turns ?? []) {
    const n = Number(t.inputTokens) + Number(t.outputTokens);
    const key = keyOf(t.bucket);
    tokens.set(key, (tokens.get(key) ?? 0) + n);
    const agent = t.group?.agent ?? "";
    agents.set(agent, (agents.get(agent) ?? 0) + n);
  }
  // Sessions partition by agent, so a day's add up exactly.
  for (const x of res?.sessions ?? []) sessions.set(keyOf(x.bucket), (sessions.get(keyOf(x.bucket)) ?? 0) + x.sessions);
  const todayKey = dayKey(today);
  const cells: (UsageCell | null)[] = [];
  let past = false;
  for (let i = 0; i < weeks * 7; i++) {
    const date = new Date(y, m - 1, dd + i);
    const key = dayKey(date);
    cells.push(past ? null : { date, key, tokens: tokens.get(key) ?? 0, sessions: sessions.get(key) ?? 0, level: 0 });
    if (key === todayKey) past = true;
  }
  const shown = cells.filter((c): c is UsageCell => c !== null);

  // Levels by the quartiles of the days with tokens, as GitHub's.
  const used = shown.map((c) => c.tokens).filter((n) => n > 0).sort((a, b) => a - b);
  const q = (p: number) => used[Math.min(used.length - 1, Math.floor(p * used.length))];
  const cuts = used.length ? [q(0.25), q(0.5), q(0.75)] : [];
  for (const c of shown) c.level = c.tokens === 0 ? 0 : Math.min(4, 1 + cuts.filter((x) => c.tokens >= x).length);

  const sum = (cs: UsageCell[]) => cs.reduce((n, c) => n + c.tokens, 0);
  const last30 = sum(shown.slice(-30));
  const prev30 = sum(shown.slice(-60, -30));
  let streak = 0;
  for (let i = shown.length - (shown.at(-1)?.tokens ? 1 : 2); i >= 0 && shown[i].tokens > 0; i--) streak++;
  const total = sum(shown);
  return {
    cells,
    weeks,
    since,
    total,
    today: shown.at(-1)?.tokens ?? 0,
    active: used.length,
    streak,
    delta: prev30 > 0 ? Math.round(((last30 - prev30) / prev30) * 100) : null,
    byAgent: [...agents]
      .filter(([, n]) => n > 0)
      .sort((a, b) => b[1] - a[1])
      .map(([agent, n]) => ({ agent, share: n / total })),
  };
}

// The statistics' Usage view (docs/overview-usage.md §6): a range and a
// filter make three questions of QueryWork (the page, the menus' choices and
// the weeks of tokens), and what the screen shows is made here from the
// answers.

export type UsageRange = "today" | "week" | "month";
export const usageRanges: UsageRange[] = ["today", "week", "month"];
const rangeDays: Record<UsageRange, number> = { today: 1, week: 7, month: 30 };

/** Only this agent's, this machine's (or one of its projects') or this sender's work; an empty field: any. projectId needs machineId. */
export type UsageFilter = { agent: string; machineId: string; projectId: string; client: string };
export const noUsageFilter: UsageFilter = { agent: "", machineId: "", projectId: "", client: "" };

type WorkRequest = MessageInitShape<typeof QueryWorkRequestSchema>;
const zone = () => Intl.DateTimeFormat().resolvedOptions().timeZone;
const firstDayOf = (today: Date, days: number) => dayKey(new Date(today.getFullYear(), today.getMonth(), today.getDate() - days + 1));

/** What the page's bars are grouped by: nothing (the total), the agents, or where (environments, or one environment's projects). In the order the control shows them. */
export type UsageGroup = "agent" | "place" | "total";
export const usageGroups: UsageGroup[] = ["total", "agent", "place"];

/**
 * The page's question: the range ending today by hour (Today) or day, under the filter, with the span before it;
 * grouped as the bars are (a place by machine and project, so either can be drawn).
 */
export function usagePageRequest(range: UsageRange, filter: UsageFilter, group: UsageGroup, today: Date, timeZone = zone()): WorkRequest {
  const days = rangeDays[range];
  const req: WorkRequest = { timeZone, firstDay: firstDayOf(today, days), days, bucket: range === "today" ? WorkBucket.HOUR : WorkBucket.DAY, filter, previous: true };
  if (group === "agent") req.groupBy = [WorkDimension.AGENT];
  if (group === "place") req.groupBy = [WorkDimension.MACHINE, WorkDimension.PROJECT];
  return req;
}

/** The menus' question: the range's totals by all four dimensions, not filtered (each menu's lines are added up from it under the other filters). */
export function usageMenusRequest(range: UsageRange, today: Date, timeZone = zone()): WorkRequest {
  const days = rangeDays[range];
  return { timeZone, firstDay: firstDayOf(today, days), days, bucket: WorkBucket.NONE, groupBy: [WorkDimension.CLIENT, WorkDimension.AGENT, WorkDimension.MACHINE, WorkDimension.PROJECT] };
}

/** The weeks' question: usageRequest's days of turns alone, under the filter (usageYear reads the answer). */
export function usageWeeksRequest(filter: UsageFilter, today: Date, weeks = 53, timeZone = zone()): WorkRequest {
  const { firstDay, days, bucket } = usageRequest(today, weeks, timeZone);
  return { timeZone, firstDay, days, bucket, filter, turnsOnly: true };
}

export type UsagePage = {
  /** Each hour of today or day of the range: sessions at work in it and the tokens of the turns that ended in it. */
  buckets: { sessions: number; tokens: number }[];
  /** The bucket now is in: the hour, or the last day. Those after it have not come. */
  now: number;
  /** Sessions at work in the range (each once), and of them those done now. */
  sessions: number;
  done: number;
  failedTurns: number;
  busySeconds: number;
  inputTokens: number;
  outputTokens: number;
  /** Against the span before, in percent; null when nothing was counted in it. */
  sessionsDelta: number | null;
  tokensDelta: number | null;
};

/** What the page shows from the answer to usagePageRequest. */
export function usagePage(res: QueryWorkResponse | null | undefined, range: UsageRange, now: Date): UsagePage {
  const hourly = range === "today";
  const buckets = Array.from({ length: hourly ? 24 : rangeDays[range] }, () => ({ sessions: 0, tokens: 0 }));
  for (const x of res?.sessions ?? []) if (buckets[x.bucket]) buckets[x.bucket].sessions += x.sessions;
  for (const x of res?.turns ?? []) if (buckets[x.bucket]) buckets[x.bucket].tokens += Number(x.inputTokens) + Number(x.outputTokens);
  const sum = <T,>(list: T[] | undefined, f: (x: T) => number | bigint) => (list ?? []).reduce((n, x) => n + Number(f(x)), 0);
  const sessions = sum(res?.rangeSessions, (x) => x.sessions);
  const inputTokens = sum(res?.rangeTurns, (x) => x.inputTokens);
  const outputTokens = sum(res?.rangeTurns, (x) => x.outputTokens);
  const delta = (cur: number, prev: number) => (prev > 0 ? Math.round(((cur - prev) / prev) * 100) : null);
  return {
    buckets,
    now: hourly ? now.getHours() : buckets.length - 1,
    sessions,
    done: sum(res?.rangeSessions, (x) => x.done),
    failedTurns: sum(res?.rangeTurns, (x) => x.failedTurns),
    busySeconds: sum(res?.rangeTurns, (x) => x.busySeconds),
    inputTokens,
    outputTokens,
    sessionsDelta: delta(sessions, sum(res?.previousSessions, (x) => x.sessions)),
    tokensDelta: delta(inputTokens + outputTokens, sum(res?.previousTurns, (x) => Number(x.inputTokens) + Number(x.outputTokens))),
  };
}

/** A part of the page's bars: what it is, its colour (0 to 5; -1 for Other), its whole in the range and its share of each bucket. */
export type UsagePart = { id: string; color: number; sessions: number; tokens: number; buckets: { sessions: number; tokens: number }[] };
/** What the bars are in parts of: agents, machines, or one machine's projects (its id in machineId). */
export type UsageParts = { by: "agent" | "machine" | "project"; machineId: string; parts: UsagePart[] };

/** The colours a page's parts are told apart by; past them the least busy are one part, Other. */
export const usagePartColors = 6;

/**
 * The parts of the page's bars from the answer to its grouped question: the agents; or the machines, or (one
 * machine chosen, or only one that worked) its projects. The busiest first. A part's colour follows it, not its
 * rank: its place in order (the coding agents' own list, the machines as the app lists them, projects by name)
 * when all of those drawn fall within the colours, else its place among them in that order. More parts than
 * colours: the least busy are added up as Other (id "", colour -1).
 */
export function usageParts(
  res: Pick<QueryWorkResponse, "sessions" | "turns" | "rangeSessions" | "rangeTurns"> | null | undefined,
  range: UsageRange,
  group: UsageGroup,
  filter: UsageFilter,
  order: { agents: readonly string[]; machines: readonly string[] },
): UsageParts | null {
  if (group === "total") return null;
  const n = range === "today" ? 24 : rangeDays[range];
  const machineIds = new Set([...(res?.rangeSessions ?? []), ...(res?.rangeTurns ?? [])].map((x) => x.group?.machineId ?? ""));
  const machineId = group === "place" ? filter.machineId || (machineIds.size === 1 ? [...machineIds][0] : "") : "";
  const by: UsageParts["by"] = group === "agent" ? "agent" : machineId ? "project" : "machine";
  const keyOf = (g: { agent: string; machineId: string; projectId: string } | undefined) =>
    by === "agent" ? (g?.agent ?? "") : by === "machine" ? (g?.machineId ?? "") : g?.machineId === machineId ? g.projectId : null;
  const all = new Map<string, UsagePart>();
  const part = (id: string) => {
    let p = all.get(id);
    if (!p) all.set(id, (p = { id, color: 0, sessions: 0, tokens: 0, buckets: Array.from({ length: n }, () => ({ sessions: 0, tokens: 0 })) }));
    return p;
  };
  const each = <T extends { group?: { agent: string; machineId: string; projectId: string } }>(list: T[] | undefined, add: (p: UsagePart, x: T) => void) => {
    for (const x of list ?? []) {
      const id = keyOf(x.group);
      // A group with no name is the answer to another question (the last one, shown while this is asked): no part.
      if (id) add(part(id), x);
    }
  };
  const tok = (x: { inputTokens: bigint; outputTokens: bigint }) => Number(x.inputTokens) + Number(x.outputTokens);
  each(res?.rangeSessions, (p, x) => (p.sessions += x.sessions));
  each(res?.rangeTurns, (p, x) => (p.tokens += tok(x)));
  each(res?.sessions, (p, x) => p.buckets[x.bucket] && (p.buckets[x.bucket].sessions += x.sessions));
  each(res?.turns, (p, x) => p.buckets[x.bucket] && (p.buckets[x.bucket].tokens += tok(x)));

  const parts = [...all.values()].filter((p) => p.sessions || p.tokens).sort((a, b) => b.sessions - a.sessions || b.tokens - a.tokens || a.id.localeCompare(b.id));
  const shown = parts.length > usagePartColors ? parts.slice(0, usagePartColors - 1) : parts;
  const known = by === "agent" ? order.agents : by === "machine" ? order.machines : shown.map((p) => p.id).sort();
  const place = (id: string) => (known.indexOf(id) < 0 ? Infinity : known.indexOf(id));
  const inOrder = [...shown].sort((a, b) => place(a.id) - place(b.id) || a.id.localeCompare(b.id));
  const fits = shown.every((p) => place(p.id) < usagePartColors);
  for (const p of shown) p.color = fits ? place(p.id) : inOrder.indexOf(p);
  if (shown.length < parts.length) {
    const other = { id: "", color: -1, sessions: 0, tokens: 0, buckets: Array.from({ length: n }, () => ({ sessions: 0, tokens: 0 })) };
    for (const p of parts.slice(shown.length)) {
      other.sessions += p.sessions;
      other.tokens += p.tokens;
      p.buckets.forEach((b, i) => ((other.buckets[i].sessions += b.sessions), (other.buckets[i].tokens += b.tokens)));
    }
    shown.push(other);
  }
  return { by, machineId, parts: shown };
}

/** A choice of a filter's menu: what it is, and its sessions and tokens in the range under the other filters. */
export type UsageChoice = { id: string; sessions: number; tokens: number };
export type UsageChoices = {
  agents: UsageChoice[];
  /** Each machine with its projects (a project's id is its machine's own). */
  machines: (UsageChoice & { projects: UsageChoice[] })[];
  clients: UsageChoice[];
};

/**
 * Each menu's choices from the answer to usageMenusRequest: what worked in the range under the other filters (so
 * a choice never leads to nothing), the busiest first; the one chosen stays listed when it has no work.
 */
export function usageChoices(res: Pick<QueryWorkResponse, "rangeSessions" | "rangeTurns"> | null | undefined, filter: UsageFilter): UsageChoices {
  type Group = { client: string; agent: string; machineId: string; projectId: string };
  const agent = (g: Group) => !filter.agent || g.agent === filter.agent;
  const place = (g: Group) => (!filter.machineId || g.machineId === filter.machineId) && (!filter.projectId || g.projectId === filter.projectId);
  const client = (g: Group) => !filter.client || g.client === filter.client;
  const add = (to: Map<string, UsageChoice>, id: string, sessions: number, tokens: number) => {
    const c = to.get(id) ?? { id, sessions: 0, tokens: 0 };
    to.set(id, { id, sessions: c.sessions + sessions, tokens: c.tokens + tokens });
  };
  const agents = new Map<string, UsageChoice>();
  const machines = new Map<string, UsageChoice>();
  const projects = new Map<string, Map<string, UsageChoice>>();
  const clients = new Map<string, UsageChoice>();
  const each = (g: Group | undefined, sessions: number, tokens: number) => {
    if (!g) return;
    if (place(g) && client(g)) add(agents, g.agent, sessions, tokens);
    if (agent(g) && place(g)) add(clients, g.client, sessions, tokens);
    if (agent(g) && client(g)) {
      add(machines, g.machineId, sessions, tokens);
      if (!projects.has(g.machineId)) projects.set(g.machineId, new Map());
      add(projects.get(g.machineId)!, g.projectId, sessions, tokens);
    }
  };
  for (const x of res?.rangeSessions ?? []) each(x.group, x.sessions, 0);
  for (const x of res?.rangeTurns ?? []) each(x.group, 0, Number(x.inputTokens) + Number(x.outputTokens));
  const list = (m: Map<string, UsageChoice> | undefined, chosen: string) => {
    const out = [...(m?.values() ?? [])].sort((a, b) => b.sessions - a.sessions || b.tokens - a.tokens || a.id.localeCompare(b.id));
    return chosen && !out.some((c) => c.id === chosen) ? [...out, { id: chosen, sessions: 0, tokens: 0 }] : out;
  };
  return {
    agents: list(agents, filter.agent),
    machines: list(machines, filter.machineId).map((m) => ({ ...m, projects: list(projects.get(m.id), m.id === filter.machineId ? filter.projectId : "") })),
    clients: list(clients, filter.client),
  };
}
