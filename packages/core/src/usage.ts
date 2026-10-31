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
