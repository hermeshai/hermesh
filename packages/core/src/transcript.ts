import {
  FailureKind,
  QuestionResolution,
  type ErrorEvent,
  type Failure,
  type Question,
  type QuestionResolved,
  type SessionEvent,
  type ToolCall,
  type ToolResult,
  type TurnCompleted,
} from "@hermesh/proto/session";
import { t } from "./i18n";
import { dateOf } from "./sessions";

// A session's conversation as the window shows it, made from its events.

/** One row of the conversation. */
export type TranscriptItem =
  /** asker: who asked for the turn (a client id; "" from before messages). */
  | { kind: "turn"; seq: bigint; turn: number; at: Date | undefined; asker: string }
  | { kind: "user"; seq: bigint; text: string; at: Date | undefined }
  | { kind: "agent"; seq: bigint; text: string }
  /** A tool call with its result, once there is one; running: the agent is still in it. */
  /** resultSeq: its result's event, when it has one. */
  | { kind: "tool"; seq: bigint; call: ToolCall; result: ToolResult | undefined; resultSeq?: bigint; running: boolean }
  | { kind: "question"; seq: bigint; question: Question; resolution: QuestionResolved | undefined }
  | { kind: "turnEnd"; seq: bigint; end: TurnCompleted }
  | { kind: "error"; seq: bigint; error: ErrorEvent }
  /**
   * A message between the session and one of the owner's agents
   * (docs/agent-messages.md): in, written to the session (delivered: what
   * became of it), or out, written by its coding agent.
   */
  | { kind: "message"; seq: bigint; direction: "in" | "out"; name: string; address: string; text: string; delivered: string };

/**
 * transcript pairs tool calls with their results and questions with their
 * answers; status changes are left out (the header shows the status).
 * running: a turn is in progress, so its last unanswered tool call runs.
 */
export function transcript(events: SessionEvent[], running: boolean): TranscriptItem[] {
  const results = new Map<string, ToolResult>();
  const resultSeqs = new Map<string, bigint>();
  const resolutions = new Map<string, QuestionResolved>();
  let lastCall: bigint | undefined;
  for (const e of events) {
    if (e.kind.case === "toolResult") {
      results.set(e.kind.value.callId, e.kind.value);
      resultSeqs.set(e.kind.value.callId, e.seq);
    }
    if (e.kind.case === "questionResolved") resolutions.set(e.kind.value.questionId, e.kind.value);
    if (e.kind.case === "toolCall") lastCall = e.seq;
  }
  const items: TranscriptItem[] = [];
  for (const e of events) {
    const at = dateOf(e.ts);
    const seq = e.seq;
    switch (e.kind.case) {
      case "turnStarted":
        items.push({ kind: "turn", seq, turn: e.kind.value.turn, at, asker: e.kind.value.asker });
        break;
      case "userMessage":
        items.push({ kind: "user", seq, text: e.kind.value.text, at });
        break;
      case "agentMessage":
        items.push({ kind: "agent", seq, text: e.kind.value.text });
        break;
      case "toolCall": {
        const c = e.kind.value;
        const result = results.get(c.callId);
        items.push({ kind: "tool", seq, call: c, result, resultSeq: resultSeqs.get(c.callId), running: !result && running && seq === lastCall && c.decision !== "deny" });
        break;
      }
      case "questionAsked": {
        const q = e.kind.value.question;
        if (q) items.push({ kind: "question", seq, question: q, resolution: resolutions.get(q.questionId) });
        break;
      }
      case "turnCompleted":
        items.push({ kind: "turnEnd", seq, end: e.kind.value });
        break;
      case "error":
        items.push({ kind: "error", seq, error: e.kind.value });
        break;
      case "messageReceived": {
        const m = e.kind.value;
        items.push({ kind: "message", seq, direction: "in", name: m.fromName, address: m.from, text: m.text, delivered: m.delivered });
        break;
      }
      case "messageSent": {
        const m = e.kind.value;
        items.push({ kind: "message", seq, direction: "out", name: m.toName, address: m.to, text: m.text, delivered: "" });
        break;
      }
    }
  }
  return items;
}

/** When the latest turn started, if the events tell. */
export function turnStartedAt(events: SessionEvent[]): Date | undefined {
  for (let i = events.length - 1; i >= 0; i--) {
    if (events[i].kind.case === "turnStarted") return dateOf(events[i].ts);
  }
  return undefined;
}

/** The turn was stopped on purpose (by the owner or the agent), not by a failure. */
export function isInterruption(f: Failure | undefined): boolean {
  return f?.kind === FailureKind.INTERRUPTED;
}

/**
 * How a question was settled. name: who answered, by the id the machine
 * recorded (the owner is "You").
 */
export function resolutionTitle(r: QuestionResolved, name: (by: string) => string = (by) => by): string {
  const you = r.by === "owner";
  switch (r.resolution) {
    case QuestionResolution.ANSWERED: {
      const answers = r.answers.map((x) => [...x.selected, ...(x.text === "" ? [] : [x.text])].join(", ")).join(t("resolution.separator"));
      return you ? t("resolution.answeredByYou", { answers }) : t("resolution.answeredBy", { name: name(r.by), answers });
    }
    case QuestionResolution.DECLINED:
      return you ? t("resolution.declinedByYou") : t("resolution.declinedBy", { name: name(r.by) });
    case QuestionResolution.TIMED_OUT:
      return t("resolution.timedOut");
    default:
      return t("resolution.withdrawn");
  }
}
