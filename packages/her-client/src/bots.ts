import type { BotSummary, LastMessage } from "@hermesh/her-protocol";

// What Bot mode's list shows about bots (docs/desktop/bot-mode-design.md §6.2),
// worked out from the bots alone so it can be checked without a window.

/** Each row's time, parsed once (a row is replaced, never changed): sorting runs on every change of any bot. */
const parsed = new WeakMap<BotSummary, number>();

/** When the bot last had a message, else when it was made: what the list sorts by. */
export function botTime(b: BotSummary): number {
  let t = parsed.get(b);
  if (t === undefined) parsed.set(b, (t = Date.parse(b.last?.ts ?? b.created_at) || 0));
  return t;
}

/** Pinned bots first, then the latest message first. Being busy moves nothing: rows would jump. */
export function sortBots(list: BotSummary[]): BotSummary[] {
  return [...list].sort((a, b) => +!!b.pinned - +!!a.pinned || botTime(b) - botTime(a) || a.id.localeCompare(b.id));
}

/** The list with bots/changed applied: each bot replaces its earlier row or joins; deleted ones leave. */
export function mergeBots(list: BotSummary[], changed: BotSummary[], deleted: string[] = []): BotSummary[] {
  const out = list.filter((b) => !deleted.includes(b.id) && !changed.some((c) => c.id === b.id));
  return [...out, ...changed.filter((c) => !deleted.includes(c.id))];
}

/** Sub-agents at work, or answering: what its row and title bar say it does. */
export function botWorking(b: BotSummary): boolean {
  return !!b.typing || (b.agents_working ?? 0) > 0;
}

/** The row's second line, the first that applies: an approval waits, it is busy, its last message. */
export type BotLine =
  | { kind: "waiting"; text: string }
  | { kind: "agents"; count: number }
  | { kind: "typing" }
  | { kind: "last"; from: LastMessage["from"]; text: string; images: number; files: string[] }
  | { kind: "empty" };

export function botLine(b: BotSummary): BotLine {
  if (b.waiting) return { kind: "waiting", text: b.waiting };
  if ((b.agents_working ?? 0) > 0) return { kind: "agents", count: b.agents_working ?? 0 };
  if (b.typing) return { kind: "typing" };
  if (!b.last) return { kind: "empty" };
  const { text, files } = lastText(b.last);
  return { kind: "last", from: b.last.from, text, images: b.last.images ?? 0, files };
}

/** The marker the composer puts before the files a message points at (attachedBlock). */
const attached = "Attached files:\n";

/**
 * A last message's line: the owner's own words without the blocks the composer adds, or the
 * first line of the bot's message without Markdown marks; the files it points at by name.
 */
export function lastText(last: LastMessage): { text: string; files: string[] } {
  let text = last.text;
  let files: string[] = last.file ? [last.file] : [];
  if (last.from === "user") {
    const at = text.indexOf(attached);
    if (at >= 0) {
      files = text.slice(at + attached.length).split("\n").filter(Boolean).map((p) => p.split(/[\\/]/).at(-1) ?? p);
      text = text.slice(0, at);
    }
  }
  const line = text.split("\n").map((l) => l.trim()).find((l) => l !== "") ?? "";
  return { text: last.from === "bot" ? plain(line) : line, files };
}

/** A Markdown line as plain text: no heading, list, quote or emphasis marks, links as their text. */
export function plain(line: string): string {
  return line
    .replace(/^(#{1,6}\s+|>\s*|[-*+]\s+|\d+[.)]\s+)/, "")
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/(\*\*|__|\*|_|~~|`)(.+?)\1/g, "$2")
    .trim();
}

/** What a bot's avatar shows when it is a letter: the first character of its name. */
export function initialOf(name: string): string {
  return Array.from(name.trim())[0] ?? "";
}

/** The bots with something the owner has not seen, and with an approval waiting. */
export function attention(list: BotSummary[]): { unread: boolean; waiting: boolean } {
  return { unread: list.some((b) => (b.unread ?? 0) > 0), waiting: list.some((b) => !!b.waiting) };
}

/** Bots matching a search (⌘K in Bot mode): by name or by what was last said, in the list's order. */
export function searchBots(list: BotSummary[], query: string): BotSummary[] {
  const q = query.trim().toLowerCase();
  if (q === "") return sortBots(list);
  return sortBots(list).filter((b) => b.name.toLowerCase().includes(q) || (b.last ? lastText(b.last).text.toLowerCase().includes(q) : false));
}
