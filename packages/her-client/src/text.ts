import type { Cost, Usage, UsageTotals } from "@hermesh/her-protocol";
import { pad2 } from "./calendar.ts";

// Numbers as the window shows them. Words are the app's (its language);
// these are only digits and units.

/**
 * Whole seconds from one time to another, as wall-clock seconds passed:
 * elapsed clocks counted this way all change together on each second
 * boundary, whatever the milliseconds of their start.
 */
export function elapsedSeconds(from: number, now: number): number {
  return Math.floor(now / 1000) - Math.floor(from / 1000);
}

/** How long something has been running: "4:12", "1:02:03". */
export function clockText(seconds: number): string {
  const s = Math.max(0, Math.trunc(seconds));
  const m = Math.trunc(s / 60);
  return m >= 60 ? `${Math.trunc(m / 60)}:${pad2(m % 60)}:${pad2(s % 60)}` : `${m}:${pad2(s % 60)}`;
}

/** 412K, 1.8M. */
export function tokensText(n: number): string {
  if (n >= 1_000_000) return `${(n / 1e6).toFixed(1).replace(/\.0$/, "")}M`;
  if (n >= 10_000) return `${Math.round(n / 1000)}K`;
  if (n >= 1000) return `${(n / 1000).toFixed(1).replace(/\.0$/, "")}K`;
  return `${n}`;
}

/** ¥0.42, $1.20; small amounts keep enough digits to show they are not nothing. */
export function costText(c: Pick<Cost, "currency" | "total">): string {
  const symbol = c.currency === "CNY" ? "¥" : "$";
  const digits = c.total > 0 && c.total < 0.01 ? 4 : 2;
  return `${symbol}${c.total.toFixed(digits)}`;
}

export function emptyUsage(): Usage {
  return { input: 0, output: 0, cache_read: 0, cache_write: 0, cost: { currency: "USD", input: 0, output: 0, cache_read: 0, cache_write: 0, total: 0 } };
}

/** Every token of a response: what it read (cached or not) and wrote. */
export function usageTokens(u: Usage): number {
  return u.input + u.output + u.cache_read + u.cache_write;
}

/** Totals over models: tokens summed; cost summed per currency, the larger first. */
export function totals(t: UsageTotals): { tokens: number; costs: Pick<Cost, "currency" | "total">[] } {
  let tokens = 0;
  const costs = new Map<Cost["currency"], number>();
  for (const u of Object.values(t.per_model)) {
    tokens += usageTokens(u);
    costs.set(u.cost.currency, (costs.get(u.cost.currency) ?? 0) + u.cost.total);
  }
  return { tokens, costs: [...costs].map(([currency, total]) => ({ currency, total })).sort((a, b) => b.total - a.total) };
}

/** a + b, by model. */
export function addTotals(a: UsageTotals, b: UsageTotals): UsageTotals {
  const per_model: Record<string, Usage> = { ...a.per_model };
  for (const [model, u] of Object.entries(b.per_model)) {
    const x = per_model[model] ?? emptyUsage();
    per_model[model] = {
      input: x.input + u.input,
      output: x.output + u.output,
      cache_read: x.cache_read + u.cache_read,
      cache_write: x.cache_write + u.cache_write,
      reasoning: x.reasoning === undefined && u.reasoning === undefined ? undefined : (x.reasoning ?? 0) + (u.reasoning ?? 0),
      cost: {
        currency: u.cost.currency,
        input: x.cost.input + u.cost.input,
        output: x.cost.output + u.cost.output,
        cache_read: x.cost.cache_read + u.cost.cache_read,
        cache_write: x.cost.cache_write + u.cost.cache_write,
        total: x.cost.total + u.cost.total,
      },
    };
  }
  return { per_model };
}

/** The last path segment: the project's name for its directory. */
export function baseName(path: string): string {
  const parts = path.split(/[\\/]+/).filter(Boolean);
  return parts.at(-1) ?? path;
}

/** A path with the home directory as "~". */
export function homePath(path: string, home: string): string {
  return home && (path === home || path.startsWith(home + "/")) ? "~" + path.slice(home.length) : path;
}

/** The model's own name in a reference: "deepseek/deepseek-flash" → "deepseek-flash". */
export function modelName(ref: string): string {
  const i = ref.indexOf("/");
  return i < 0 ? ref : ref.slice(i + 1);
}
