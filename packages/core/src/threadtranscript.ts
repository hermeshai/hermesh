import { create } from "@bufbuild/protobuf";
import {
  ErrorEventSchema,
  FailureKind,
  FailureSchema,
  QuestionItemSchema,
  QuestionOptionSchema,
  QuestionSchema,
  ToolCallSchema,
  ToolResultSchema,
  TurnCompletedSchema,
  type Failure,
  type Question,
} from "@hermesh/proto/session";
import { RunStatus, TurnItemKind, type Run, type RuntimeRequest, type Thread, type ThreadProjection, type TurnItem } from "@hermesh/proto/thread";
import { dateOf } from "./sessions";
import type { TranscriptItem } from "./transcript";

// A v2 thread's conversation as a session's (transcript.ts), so both draw
// with the same rows: its messages, its tool calls as steps, a time line
// before each run, and how a run that did not finish ended.

/**
 * Each row's seq, unique and in order: an item's is its own ×4, the time
 * line before its run's first item 2 less, the end of its run's last 1 more.
 */
const itemSeq = (it: TurnItem) => it.seq * 4n;

/** The kind of step a tool is, by its name (as a session's machine tells it). */
function toolKind(name: string): string {
  if (name.startsWith("mcp__")) return "mcp";
  switch (name) {
    case "Read":
    case "NotebookRead":
    case "read_file":
    case "view":
      return "read";
    case "Grep":
    case "Glob":
    case "LS":
    case "search":
      return "search";
    case "Edit":
    case "MultiEdit":
    case "Write":
    case "NotebookEdit":
    case "apply_patch":
      return "edit";
    case "Bash":
    case "BashOutput":
    case "KillShell":
    case "shell":
      return "shell";
    case "WebFetch":
    case "WebSearch":
      return "network";
    default:
      return "other";
  }
}

function isRunActive(run: Run | undefined): boolean {
  if (!run) return true;
  return (
    run.status === RunStatus.PREPARING ||
    run.status === RunStatus.RUNNING ||
    run.status === RunStatus.WAITING_REQUEST
  );
}

function toolItem(it: TurnItem, run: Run | undefined): TranscriptItem {
  const seq = itemSeq(it);
  const callId = it.itemId;
  if (it.kind === TurnItemKind.FILE_CHANGE) {
    return {
      kind: "tool",
      seq,
      call: create(ToolCallSchema, { callId, kind: "edit", name: "Edit", summary: it.filePath }),
      result: create(ToolResultSchema, { callId, summary: it.diff }),
      running: false,
    };
  }
  const shell = it.kind === TurnItemKind.COMMAND_EXECUTION;
  const name = it.toolName || (shell ? "Bash" : "");
  const running = it.status === "running" && isRunActive(run);
  return {
    kind: "tool",
    seq,
    call: create(ToolCallSchema, { callId, kind: shell ? "shell" : toolKind(name), name, summary: shell ? it.command : it.filePath }),
    result: running
      ? undefined
      : create(ToolResultSchema, { callId, summary: it.output, isError: it.status === "failed" || it.exitCode !== 0, truncated: it.outputTruncated }),
    running,
  };
}

/**
 * threadTranscript makes a thread's items into a session's conversation
 * rows. Reasoning, plans' to-do lists, requests (the page asks those
 * below), checkpoints and notices have no row of a session's to be.
 */
export function threadTranscript(proj: ThreadProjection | null | undefined): TranscriptItem[] {
  if (!proj) return [];
  const runs = new Map(proj.runs.map((r) => [r.runId, r]));
  const out: TranscriptItem[] = [];
  const items = proj.items;
  items.forEach((it, i) => {
    const run = runs.get(it.runId);
    const seq = itemSeq(it);
    if (run && items[i - 1]?.runId !== it.runId) {
      out.push({ kind: "turn", seq: seq - 2n, turn: run.ordinal, at: dateOf(run.startedAt) ?? dateOf(run.createdAt) ?? dateOf(it.createdAt), asker: "" });
    }
    const at = dateOf(it.createdAt);
    switch (it.kind) {
      case TurnItemKind.USER_MESSAGE:
        out.push({ kind: "user", seq, text: it.text, at });
        break;
      case TurnItemKind.ASSISTANT_MESSAGE:
        out.push({ kind: "agent", seq, text: it.text, at });
        break;
      case TurnItemKind.PROPOSED_PLAN:
        out.push({ kind: "agent", seq, text: formatProposedPlan(it.planTitle, it.text), at });
        break;
      case TurnItemKind.TOOL_CALL:
      case TurnItemKind.COMMAND_EXECUTION:
      case TurnItemKind.FILE_CHANGE:
        out.push(toolItem(it, run));
        break;
      case TurnItemKind.ERROR:
        out.push({ kind: "error", seq, error: create(ErrorEventSchema, { message: it.text, failure: runFailure(it.text) }) });
        break;
    }
    // A run stopped on purpose ends with a line that says so, as a session's interrupted turn does.
    if (run?.status === RunStatus.INTERRUPTED && items[i + 1]?.runId !== it.runId) {
      out.push({
        kind: "turnEnd",
        seq: seq + 1n,
        end: create(TurnCompletedSchema, { turn: run.ordinal, isError: true, failure: create(FailureSchema, { kind: FailureKind.INTERRUPTED }) }),
      });
    }
  });
  return out;
}

function formatProposedPlan(title: string, text: string): string {
  if (!title || text.trimStart().startsWith("#")) return text;
  return `### ${title}\n\n${text}`;
}

const runFailure = (message: string): Failure => create(FailureSchema, { kind: FailureKind.UNSPECIFIED, message });

/**
 * Why the thread stopped, or why the machine rejected what was sent, until the next run starts (which clears it):
 * told once, at the end of the conversation (its row is left out).
 */
export function threadFailure(th: Thread | undefined): Failure | undefined {
  if (!th || th.lastError === "") return undefined;
  return runFailure(th.lastError);
}

/** Whether a tool or command of the thread's run under way is running (its line at work says "Working", not "Thinking"). */
export function threadToolRunning(proj: ThreadProjection | null | undefined): boolean {
  const run = proj?.thread?.activeRunId;
  if (!proj || !run) return false;
  const tools = [TurnItemKind.TOOL_CALL, TurnItemKind.COMMAND_EXECUTION, TurnItemKind.FILE_CHANGE];
  return proj.items.some((it) => it.runId === run && it.status === "running" && tools.includes(it.kind));
}

/** The rows (by seq) of replies still being written. */
export function threadLiveReplies(proj: ThreadProjection | null | undefined): Set<bigint> {
  return new Set((proj?.items ?? []).filter((it) => it.kind === TurnItemKind.ASSISTANT_MESSAGE && it.streaming).map(itemSeq));
}

/** A thread's questions as a session's question, so the same panel asks them. */
export function threadQuestion(req: RuntimeRequest): Question {
  return create(QuestionSchema, {
    questionId: req.requestId,
    askedAt: req.createdAt,
    items: (req.userInput?.questions ?? []).map((q) =>
      create(QuestionItemSchema, {
        id: q.id,
        header: q.header,
        text: q.question,
        options: q.options.map((o) => create(QuestionOptionSchema, { label: o.label, description: o.description })),
        multiSelect: q.multiSelect,
        allowOther: q.allowCustom,
        optional: !q.required,
      }),
    ),
  });
}
