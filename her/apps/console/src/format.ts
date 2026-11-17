import { RpcError } from "@hermesh/her-client";
import type { ModelPrice } from "@hermesh/her-protocol";
import { i18n } from "./i18n.ts";
import { SignInTimedOut } from "./signin.ts";

// Times, sizes and prices as the pages say them, and her's errors in words.

const t = i18n.getFixedT(null, "console");

const sameDay = (a: Date, b: Date) => a.toDateString() === b.toDateString();

/** "just now", "5 min ago", "3 hr ago", "Yesterday", else the date. */
export function agoText(d: Date, now: Date): string {
  const s = (now.getTime() - d.getTime()) / 1000;
  if (s < 60) return t("time.justNow");
  if (s < 3600) return t("time.minutesAgo", { count: Math.trunc(s / 60) });
  if (sameDay(d, now)) return t("time.hoursAgo", { count: Math.trunc(s / 3600) });
  if (sameDay(d, new Date(now.getTime() - 86_400_000))) return t("time.yesterday");
  return d.toLocaleDateString(i18n.language, { month: "short", day: "numeric" });
}

/** "200K", "1.5M": a context window. */
export function tokensText(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1).replace(/\.0$/, "")}M`;
  if (n >= 1000) return `${Math.round(n / 1000)}K`;
  return String(n);
}

/** "0 B", "820 KB", "4.2 MB", "127 MB": a size on disk, in the units Finder uses (1 KB = 1000 bytes). */
export function bytesText(n: number): string {
  if (n < 1000) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1000;
  let i = 0;
  while (v >= 999.5 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v < 9.95 ? v.toFixed(1).replace(/\.0$/, "") : Math.round(v)} ${units[i]}`;
}

/** "$0.15 / $0.60": input and output prices per million tokens. */
export function priceText(cost: ModelPrice): string {
  const sign = cost.currency === "CNY" ? "¥" : "$";
  const n = (v: number) => sign + (v >= 10 ? v.toFixed(0) : v.toFixed(2));
  return `${n(cost.input)} / ${n(cost.output)}`;
}

/**
 * What went wrong, in words: her's own message, the connection's state, or hermeshd's refusal
 * as it said it (the transport reports one as "config/write: HTTP 403 <its text>").
 */
export function errorText(e: unknown): string {
  if (e instanceof RpcError && e.kind === "disconnected") return t("error.disconnected");
  if (e instanceof SignInTimedOut) return t("error.signinTimedOut");
  const message = e instanceof Error ? e.message : String(e);
  const refused = /^[\w/-]+: (HTTP \d{3})(?: ([\s\S]+))?$/.exec(message);
  return refused ? (refused[2] ?? refused[1]) : message;
}
