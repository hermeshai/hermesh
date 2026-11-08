import type { ErrorInfo } from "@hermesh/her-protocol";
import { callTarget, runningAgents, type AgentView, type SessionView } from "./session.ts";
import { conversation, detailsOf, userText, type ToolView, type WorkItem } from "./steps.ts";
import { baseName } from "./text.ts";

// A bot's conversation as Bot mode shows it (docs/kernel/bot-design.md §3.4):
// the owner's messages, each message the bot sent with send_to_user (words,
// files or a question, §3.12), what its sub-agents told the owner, which reads
// the same (agent-messaging-design §17), and failures, in the order they were sent.
// What the bot did and wrote for itself does not show. An approval is no part of it:
// it pops up above the composer, as on a session's page.

export const SEND_TO_USER = "send_to_user";
export const MANAGE_ROUTINE = "manage_routine";

/** What the bot did to a routine, which the chat shows as a line (bot-mode-design §6.21). */
export interface RoutineChange {
  action: "create" | "update" | "pause" | "resume" | "delete";
  routine_id: string;
  name: string;
  when: string;
  next_run?: string;
}

/** The run of a routine whose report a message passes on. */
export interface RoutineSource {
  routine_id: string;
  run_id: string;
  name: string;
  /** When the report came (ms). */
  at: number;
}

export type BotItem =
  /** queued: sent while the bot was busy, not in its conversation yet. */
  | { kind: "user"; key: string; text: string; images: { mime_type: string; data: string }[]; files: string[]; skills: string[]; ts: number; queued: boolean }
  /** A message from the bot. */
  | { kind: "bot"; key: string; text: string; ts: number; source?: RoutineSource }
  /** Files and images the bot sent, as one message, with its words if it had any. */
  | { kind: "attachment"; key: string; files: BotFile[]; text?: string; ts: number; source?: RoutineSource }
  /** A question, with answers to tap if they can be listed; open: they can still be tapped. */
  | { kind: "question"; key: string; text: string; options: string[]; open: boolean; ts: number; source?: RoutineSource }
  /** What the bot did to a routine, as a line in the chat. */
  | ({ kind: "routine"; key: string; ts: number } & RoutineChange)
  /** A change the bot made to a deck (deck_edit) or a Word document (docx_edit, docx_review), shown where the owner looks so it can be taken back; `details` is the tool's, validated where it is shown. */
  | { kind: "deck_edit"; key: string; details: unknown; ts: number }
  /** last: no task ran since, so it can be retried. */
  | { kind: "failure"; key: string; error: ErrorInfo; ts: number; last: boolean };

/** A file a bot sent, as its call's result describes it. */
export interface BotFile {
  /** Absolute. */
  path: string;
  name: string;
  size: number;
  /** One that shows as a picture, with its size in pixels when known. */
  image: boolean;
  width?: number;
  height?: number;
}

/** When each message the bot sent was sent: the end of the reply that called send_to_user. */
function sendTimes(v: SessionView): Map<string, number> {
  const at = new Map<string, number>();
  for (const e of v.entries) {
    if (e.type !== "message" || e.message.role !== "assistant") continue;
    const done = e.message.ts + (e.message.duration_ms ?? 0);
    for (const c of e.message.content) if (c.type === "tool_call" && (c.name === SEND_TO_USER || c.name === MANAGE_ROUTINE)) at.set(c.id, done);
  }
  return at;
}

type Sent = { kind: "bot"; text: string } | { kind: "attachment"; files: BotFile[]; text?: string } | { kind: "question"; text: string; options: string[]; open: boolean };

const some = (list: unknown): list is unknown[] => Array.isArray(list) && list.length > 0;

/** The files of an attachment message, from its result's details. */
function filesOf(tool: ToolView): BotFile[] {
  const details = (tool.result as { details?: { files?: unknown } } | undefined)?.details;
  if (!Array.isArray(details?.files)) return [];
  return details.files.flatMap((f: { path?: unknown; size?: unknown; image?: unknown; width?: unknown; height?: unknown }) =>
    typeof f?.path === "string"
      ? [{
          path: f.path,
          name: baseName(f.path),
          size: typeof f.size === "number" ? f.size : 0,
          image: f.image === true,
          ...(typeof f.width === "number" && typeof f.height === "number" ? { width: f.width, height: f.height } : {}),
        }]
      : [],
  );
}

/**
 * What a send_to_user call sent the owner. Words and questions show from the call's start: failed,
 * refused and cancelled ones did not reach them. Files show once they were found to be there.
 */
function delivered(item: WorkItem): Sent | null {
  if (item.kind !== "tool" || item.tool.name !== SEND_TO_USER) return null;
  const { state, args } = item.tool;
  if (state === "failed" || state === "denied" || state === "cancelled" || state === "streaming") return null;
  const text = typeof args.text === "string" && args.text.trim() !== "" ? args.text : null;
  // Without a type, what a call carries says what it is.
  if (args.type === "attachment" || (args.type === undefined && some(args.files))) {
    const files = state === "done" ? filesOf(item.tool) : [];
    return files.length > 0 ? { kind: "attachment", files, ...(text !== null ? { text } : {}) } : null;
  }
  if (args.type === "question" || (args.type === undefined && some(args.options))) {
    const options = some(args.options) ? args.options.flatMap((o) => (typeof (o as { label?: unknown })?.label === "string" ? [(o as { label: string }).label.trim()] : [])) : [];
    return text !== null ? { kind: "question", text, options, open: false } : null;
  }
  return text !== null ? { kind: "bot", text } : null;
}

const docEdits = new Set(["deck_edit", "docx_edit", "docx_review"]);

/** The details of a deck_edit that changed the deck, or of a Word change (docx_edit, docx_review), itself or through call_tool. */
function deckEdit(item: WorkItem): Record<string, unknown> | null {
  if (item.kind !== "tool" || item.tool.state !== "done" || !docEdits.has(callTarget(item.tool.name, item.tool.args).name)) return null;
  const d = detailsOf(item.tool.result);
  if (typeof d.docx_id === "string") return typeof d.edit === "number" || typeof d.edit === "string" ? d : null;
  return typeof d.deck_id === "string" && typeof d.edit === "number" ? d : null;
}

/** What a manage_routine call changed; reading a routine changes nothing, and has no details. */
function routineChange(item: WorkItem): RoutineChange | null {
  if (item.kind !== "tool" || item.tool.name !== MANAGE_ROUTINE || item.tool.state !== "done") return null;
  const d = detailsOf(item.tool.result);
  if (typeof d.action !== "string" || typeof d.routine_id !== "string") return null;
  return {
    action: d.action as RoutineChange["action"],
    routine_id: d.routine_id,
    name: String(d.name ?? ""),
    when: String(d.when ?? ""),
    next_run: typeof d.next_run === "string" ? d.next_run : undefined,
  };
}

/** The conversation as bubbles and failure cards, oldest first. */
export function botChat(v: SessionView): BotItem[] {
  const times = sendTimes(v);
  const out: BotItem[] = [];
  let since: number | undefined;
  // The run whose report this task passes on.
  let source: RoutineSource | undefined;
  for (const s of conversation(v)) {
    switch (s.kind) {
      case "user":
        out.push({ kind: "user", key: s.key, text: s.text, images: s.images, files: s.files, skills: s.skills, ts: s.ts, queued: false });
        since ??= s.ts;
        source = undefined;
        break;
      case "work":
        for (const item of s.items) {
          const sent = delivered(item);
          if (sent === null) {
            const details = deckEdit(item);
            if (details) out.push({ kind: "deck_edit", key: `deck_edit:${item.key}`, details, ts: times.get(item.key) ?? since ?? Date.now() });
            const change = routineChange(item);
            if (change) out.push({ kind: "routine", key: `routine:${item.key}`, ts: times.get(item.key) ?? since ?? Date.now(), ...change });
            continue;
          }
          const ts = times.get(item.key) ?? since ?? Date.now();
          out.push({ ...sent, key: item.key, ts, source });
          since = ts;
        }
        break;
      case "report":
        if (s.routine) {
          source = { routine_id: s.routine.id, run_id: s.routine.run, name: s.name, at: s.ts };
          since ??= s.ts;
        }
        break;
      case "agent_message":
        // What a sub-agent told the owner is a message like the bot's own.
        if (s.to_user) out.push({ kind: "bot", key: s.key, text: s.text, ts: s.ts });
        break;
      case "end":
        if (s.end.outcome === "failed" && s.end.error) out.push({ kind: "failure", key: s.key, error: s.end.error, ts: s.end.ts, last: false });
        since = undefined;
        source = undefined;
        break;
    }
  }
  // A sub-agent's change is the owner's to take back all the same: the bot sent the agent to make it.
  const editId = (d: unknown) => { const x = d as { deck_id?: string; docx_id?: string; edit: number }; return `${x.deck_id ?? x.docx_id}:${x.edit}`; };
  const shown = new Set(out.flatMap((i) => (i.kind === "deck_edit" ? [editId(i.details)] : [])));
  for (const agent of v.agents) {
    for (const s of conversation(agent.view)) {
      if (s.kind !== "work") continue;
      for (const item of s.items) {
        const details = deckEdit(item);
        if (!details || item.kind !== "tool") continue;
        const id = editId(details);
        if (shown.has(id)) continue;
        shown.add(id);
        out.push({ kind: "deck_edit", key: `deck_edit:${agent.id}:${item.key}`, details, ts: item.tool.result?.ts ?? agent.since });
      }
    }
  }
  const lastFailure = out.findLastIndex((i) => i.kind === "failure");
  if (lastFailure >= 0 && !v.live && v.failure) out[lastFailure] = { ...(out[lastFailure] as Extract<BotItem, { kind: "failure" }>), last: true };
  for (const q of v.queued) {
    const { text, files } = userText(q.message.content);
    const images = q.message.content.flatMap((c) => (c.type === "image" ? [{ mime_type: c.mime_type, data: c.data }] : []));
    out.push({ kind: "user", key: q.id, text, images, files, skills: [], ts: q.message.ts, queued: true });
  }
  // In the order things were sent; the same time keeps the history's order.
  const sorted = out.map((item, i) => ({ item, i })).sort((a, b) => a.item.ts - b.item.ts || a.i - b.i).map((x) => x.item);
  // The latest question can be answered until the owner says anything.
  for (let i = sorted.length - 1; i >= 0; i--) {
    const item = sorted[i];
    if (item.kind === "user") break;
    if (item.kind === "question") {
      sorted[i] = { ...item, open: true };
      break;
    }
  }
  return sorted;
}

/** How many images each row of a message's holds: two or three, never one alone; the rows of two, larger, come first. */
export function imageRows(n: number): number[] {
  if (n <= 1) return n === 1 ? [1] : [];
  const twos = n % 3 === 0 ? 0 : n % 3 === 2 ? 1 : 2;
  return [...Array<number>(twos).fill(2), ...Array<number>((n - twos * 2) / 3).fill(3)];
}

const clamp = (v: number, low: number, high: number) => Math.min(high, Math.max(low, v));

/** The width over height an image shows at: its own, past which it is cut. Alone it has more room to be narrow or wide. */
export function imageRatio(f: Pick<BotFile, "width" | "height">, alone: boolean): number {
  const own = f.width && f.height ? f.width / f.height : 1.6;
  return alone ? clamp(own, 0.5, 4) : clamp(own, 0.75, 2);
}

/** A sub-agent at work, as the busy bubble shows it. */
export interface WorkingAgent {
  id: string;
  /** What the bot called the work. */
  label: string;
}

/** What the busy bubble shows, or null when the bot is not busy: waiting for an approval alone is not busy. */
export function busyBubble(v: SessionView): { typing: boolean; agents: WorkingAgent[] } | null {
  const agents = runningAgents(v).map((a) => ({ id: a.id, label: a.description || a.name }));
  const typing = !!v.live && v.live.approvals.length === 0;
  return typing || agents.length > 0 ? { typing, agents } : null;
}
