import type { ToolCall } from "@hermesh/proto/session";
import { hourMinute, isYesterday, monthDay, sameDay, weekday, withinWeek } from "./calendar";
import { t } from "./i18n";
import type { TranscriptItem } from "./transcript";

// How the conversation shows what the agent did: consecutive tool calls as
// one step ("Read 3 files · Ran 2 commands"), each call in a few words
// ("Edited hello.txt"), and a time line only after a pause.

type ToolItem = Extract<TranscriptItem, { kind: "tool" }>;

/** A conversation row, with consecutive tool calls gathered into one group. */
export type Step = Exclude<TranscriptItem, ToolItem> | { kind: "tools"; seq: bigint; calls: ToolItem[] };

export function groupSteps(items: TranscriptItem[]): Step[] {
  const out: Step[] = [];
  for (const it of items) {
    const last = out.at(-1);
    if (it.kind !== "tool") out.push(it);
    else if (last?.kind === "tools") last.calls.push(it);
    else out.push({ kind: "tools", seq: it.seq, calls: [it] });
  }
  return out;
}

/** The kinds of tool call with words of their own; any other is "other". */
const kinds = ["read", "search", "edit", "shell", "network", "mcp"] as const;
type VerbKind = (typeof kinds)[number] | "other";

function verbKind(call: ToolCall): VerbKind {
  return (kinds as readonly string[]).includes(call.kind) ? (call.kind as VerbKind) : "other";
}

/** The verb for a call: "Edited", "Running"; a denied call is "Denied". */
export function toolVerb(call: ToolCall, running: boolean): string {
  if (call.decision === "deny") return t("steps.deny.done");
  const k = verbKind(call);
  return running ? t(`steps.${k}.doing`) : t(`steps.${k}.done`);
}

/**
 * What a call acted on, without the agent's own tool name in front ("Write
 * hello.txt" is "hello.txt": the verb says it is an edit).
 */
export function toolObject(call: ToolCall): string {
  const s = call.summary.trim();
  if (s === "") return call.name;
  const m = /^([A-Za-z][\w-]*)\s+(.+)$/s.exec(s);
  if (m && (m[1].toLowerCase() === call.name.toLowerCase() || toolWords.has(m[1].toLowerCase()))) return m[2];
  return s;
}
const toolWords = new Set(["read", "write", "edit", "multiedit", "bash", "grep", "glob", "search", "fetch", "webfetch", "websearch", "ls", "list", "view", "create"]);

/** A group's line: "Read 3 files · Ran 2 commands", in the order the kinds first appear. */
export function stepSummary(calls: { call: ToolCall }[]): string {
  const counts = new Map<VerbKind | "deny", number>();
  for (const { call } of calls) {
    const k = call.decision === "deny" ? "deny" : verbKind(call);
    counts.set(k, (counts.get(k) ?? 0) + 1);
  }
  return [...counts].map(([k, count]) => t(`steps.${k}.count`, { count })).join(t("steps.separator"));
}

/** A turn's time line, shown only for the first turn and after a pause of half an hour: "Today 18:22", "Yesterday 09:05", "Fri 14:30", "Oct 3 14:30". */
export function turnTimeLine(at: Date, previous: Date | undefined, now: Date): string | null {
  if (previous && at.getTime() - previous.getTime() < 30 * 60_000) return null;
  const day = sameDay(at, now) ? t("time.today") : isYesterday(at, now) ? t("time.yesterday") : withinWeek(at, now) ? weekday(at) : monthDay(at);
  return `${day} ${hourMinute(at)}`;
}
