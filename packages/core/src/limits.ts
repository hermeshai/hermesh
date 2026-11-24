import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { Machine } from "@hermesh/proto/local";

// Rate limits (docs/overview-usage.md §7.4): what each coding agent last said
// of its account's limits, from whichever machine said it last. The agents
// do not name the account, so it is one card an agent.

/** A window as the page shows it: how long, how much is left and of its time, and when it starts again. */
export type LimitShown = { minutes: number; left: number; timeLeft: number; resetsAt: Date };

export type AgentLimitsCard = {
  agent: string;
  /** The machine that said it last, and when; "" and null when no machine has it. */
  machineId: string;
  observedAt: Date | null;
  plan: string;
  /** Not yet reset, the shortest first; empty when none was said or all have reset since. */
  windows: LimitShown[];
  /** It said some, and every one of them has reset since: what is left now is not known. */
  stale: boolean;
};

/** Left of a window at or below this is a thing that needs the owner. */
export const limitLow = 20;

/**
 * A card for each coding agent available on a machine, from what the machines say: the one with the least left
 * first, then those that said nothing, in the order of first seen.
 */
export function agentLimits(machines: Pick<Machine, "machineId" | "status">[], now: Date): AgentLimitsCard[] {
  const cards = new Map<string, AgentLimitsCard>();
  for (const m of machines) {
    for (const a of m.status?.agents ?? []) {
      if (!a.available && !a.limits) continue;
      const at = a.limits?.observedAt ? timestampDate(a.limits.observedAt) : null;
      const had = cards.get(a.name);
      if (had && (!at || (had.observedAt && had.observedAt >= at))) continue;
      const said = a.limits?.windows ?? [];
      const windows = said
        .map((w) => ({ minutes: w.minutes, used: w.usedPercent, resetsAt: w.resetsAt ? timestampDate(w.resetsAt) : new Date(0) }))
        .filter((w) => w.resetsAt > now)
        .map((w) => ({
          minutes: w.minutes,
          left: Math.max(0, 100 - w.used),
          timeLeft: Math.min(100, Math.max(0, Math.round(((w.resetsAt.getTime() - now.getTime()) / (w.minutes * 60_000)) * 100))),
          resetsAt: w.resetsAt,
        }))
        .sort((x, y) => x.minutes - y.minutes);
      cards.set(a.name, { agent: a.name, machineId: at ? m.machineId : "", observedAt: at, plan: a.limits?.plan ?? "", windows, stale: said.length > 0 && windows.length === 0 });
    }
  }
  const least = (c: AgentLimitsCard) => (c.windows.length ? Math.min(...c.windows.map((w) => w.left)) : 101);
  return [...cards.values()].sort((a, b) => least(a) - least(b));
}

/** The window with the least left of each card, where it is at or below limitLow: what needs the owner. */
export function lowLimits(cards: AgentLimitsCard[]): { card: AgentLimitsCard; window: LimitShown }[] {
  return cards.flatMap((card) => {
    const w = card.windows.reduce<LimitShown | null>((m, x) => (!m || x.left < m.left ? x : m), null);
    return w && w.left <= limitLow ? [{ card, window: w }] : [];
  });
}
