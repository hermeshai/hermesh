import { create } from "@bufbuild/protobuf";
import { Workspace } from "@hermesh/proto/identity";
import {
  ActorKind,
  AnswerSchema,
  ErrorEventSchema,
  FailureKind,
  FailureSchema,
  QuestionItemSchema,
  QuestionOptionSchema,
  QuestionResolution,
  QuestionResolvedSchema,
  QuestionSchema,
  RunStatus,
  RuntimeRequestKind,
  RuntimeRequestStatus,
  SessionStatus,
  ToolCallSchema,
  ToolResultSchema,
  TurnCompletedSchema,
  TurnItemKind,
  WorkspaceAction,
  type Actor,
  type AttachmentRef,
  type ErrorEvent,
  type Failure,
  type Question,
  type QuestionResolved,
  type Run,
  type RuntimeRequest,
  type Session,
  type SessionProjection,
  type ToolCall,
  type ToolResult,
  type TurnCompleted,
  type TurnItem,
  type WorkspaceStep,
} from "@hermesh/proto/session";
import { t } from "./i18n";
import { dateOf } from "./sessions";
import { activeBackgroundItemIdsOf, activeBackgroundTasksOf } from "./threads";

// A unified session's conversation as the window shows it: its messages, tool
// calls, settled questions, inter-agent mail, time lines before turns, and
// run interruptions/failures.

/** One row of the conversation. */
export type TranscriptItem =
  /** asker: who asked for the turn (a client id, "owner", or ""). wakeReason: when woken by a background task. */
  | { kind: "turn"; seq: bigint; turn: number; at: Date | undefined; asker: string; wakeReason?: string }
  /** asker: who asked for its turn (a client id, or "owner"; "" when the machine didn't say). */
  | { kind: "user"; seq: bigint; text: string; at: Date | undefined; asker: string; attachments?: AttachmentRef[] }
  /** runId: the turn it belongs to, whose files its links open as that turn left them (docs/session-files.md §6.2). */
  | { kind: "agent"; seq: bigint; text: string; at: Date | undefined; runId?: string }
  | { kind: "tool"; seq: bigint; call: ToolCall; result: ToolResult | undefined; resultSeq?: bigint; running: boolean }
  | { kind: "question"; seq: bigint; question: Question; resolution: QuestionResolved | undefined }
  | { kind: "turnEnd"; seq: bigint; end: TurnCompleted }
  | { kind: "error"; seq: bigint; error: ErrorEvent }
  | { kind: "message"; seq: bigint; direction: "in" | "out"; name: string; address: string; text: string; delivered: string }
  /** Where the session works, set up by its first turn: a worktree made, a branch switched to, or the folder kept. */
  | WorkspaceRow;

/** A session's first turn setting its folder up (a WORKSPACE item), as its row shows it. failure: why, as the machine said. */
export type WorkspaceRow = { kind: "workspace"; seq: bigint; state: "running" | "done" | "failed"; step: WorkspaceStep; failure: string };

/** Its words, then the part in code: "New branch from main" `hermesh/s_…`, "Switching to branch" `feature/login`. */
export function workspaceTitle(row: WorkspaceRow): { text: string; code: string } {
  const { baseRef: base, branch, action } = row.step;
  const st = row.state;
  switch (action) {
    case WorkspaceAction.FETCH:
      return { text: t(st === "running" ? "workspace.fetching" : "workspace.fetchFailed"), code: base };
    case WorkspaceAction.CREATE:
      if (st === "running") return { text: t("workspace.creating"), code: base };
      if (st === "done") return { text: t("workspace.created", { base }), code: branch };
      return { text: t("workspace.createFailed"), code: "" };
    case WorkspaceAction.SWITCH:
      return { text: t(st === "running" ? "workspace.switching" : st === "done" ? "workspace.switched" : "workspace.switchFailed"), code: branch };
    default:
      return { text: t("workspace.kept"), code: branch };
  }
}

/** Its details, opened: how, the branch, its base, the folder; or why it failed. */
export function workspaceDetails(row: WorkspaceRow): { label: string; value: string }[] {
  // Stopped while it was under way: no failure of its own.
  if (row.state === "failed") return [{ label: t("workspace.why"), value: row.failure || t("workspace.stopped") }];
  const s = row.step;
  const worktree = s.mode === Workspace.WORKTREE;
  const out = [{ label: t("workspace.where"), value: t(worktree ? "workspace.worktree" : "workspace.inPlace") }];
  if (s.branch) {
    const was = s.action === WorkspaceAction.SWITCH && s.previousBranch;
    out.push({ label: t("workspace.branch"), value: was ? t("workspace.was", { branch: s.branch, previous: s.previousBranch }) : s.branch });
  }
  if (worktree && s.baseRef) out.push({ label: t("workspace.from"), value: s.baseRef });
  if (s.path) out.push({ label: t("workspace.folder"), value: s.path });
  return out;
}

/** The run whose first turn could not set its folder up: its row says so, so the run's own error is not a row too (the session's failure card still says what to do). */
export function workspaceFailedRun(proj: SessionProjection | null | undefined): string | undefined {
  return proj?.items.findLast((it) => it.kind === TurnItemKind.WORKSPACE && it.status === "failed")?.runId;
}

const itemSeq = (it: TurnItem) => it.seq * 4n;

/** Who an actor is, as a session's created_by and a turn's asker say it: "owner" for the owner (or none told), else its client id. */
export function actorClientId(a: Actor | undefined): string {
  return !a || a.kind === ActorKind.OWNER ? "owner" : a.id;
}

function toolKind(name: string): string {
  if (name.startsWith("mcp__") || name === "message_send") return "mcp";
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
    case "WriteFile":
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

function toolItem(it: TurnItem, run: Run | undefined, activeBgItems: ReadonlySet<string>): TranscriptItem {
  const seq = itemSeq(it);
  const callId = it.itemId;
  if (it.kind === TurnItemKind.FILE_CHANGE) {
    return {
      kind: "tool",
      seq,
      call: create(ToolCallSchema, {
        callId,
        kind: "edit",
        name: "Edit",
        summary: it.filePath,
        input: it.input,
        inputTruncated: it.inputTruncated,
        inputBlobSha256: it.inputBlobSha256,
        inputSize: it.inputSize,
      }),
      result: create(ToolResultSchema, { callId, summary: it.diff }),
      running: false,
    };
  }
  const shell = it.kind === TurnItemKind.COMMAND_EXECUTION;
  const name = it.toolName || (shell ? "Bash" : "");
  const running = it.status === "running" && (isRunActive(run) || activeBgItems.has(it.itemId));
  return {
    kind: "tool",
    seq,
    call: create(ToolCallSchema, {
      callId,
      kind: shell ? "shell" : toolKind(name),
      name,
      summary: shell ? it.command : it.filePath,
      input: it.input,
      inputTruncated: it.inputTruncated,
      inputBlobSha256: it.inputBlobSha256,
      inputSize: it.inputSize,
    }),
    result: running && it.output === ""
      ? undefined
      : create(ToolResultSchema, {
          callId,
          summary: it.output,
          isError: !running && (it.status === "failed" || it.exitCode !== 0),
          truncated: it.outputTruncated,
          preview: running,
          size: it.outputSize,
          blobSha256: it.outputBlobSha256,
        }),
    running,
  };
}

function actorIdString(run: Run | undefined): string {
  if (!run?.author) return "";
  return run.author.kind === ActorKind.OWNER ? "owner" : run.author.id;
}

/**
 * sessionTranscript converts a unified SessionProjection into conversation rows.
 */
export function sessionTranscript(proj: SessionProjection | null | undefined): TranscriptItem[] {
  if (!proj) return [];
  const runs = new Map(proj.runs.map((r) => [r.runId, r]));
  const reqs = new Map(proj.requests.map((r) => [r.requestId, r]));
  const activeBgItems = activeBackgroundItemIdsOf(proj);
  const setupFailed = workspaceFailedRun(proj);
  const out: TranscriptItem[] = [];
  const items = proj.items;
  items.forEach((it, i) => {
    const run = runs.get(it.runId);
    const seq = itemSeq(it);
    if (run && items[i - 1]?.runId !== it.runId) {
      const wakeReason = run.author?.kind === ActorKind.SYSTEM && run.wakeReason ? run.wakeReason : undefined;
      out.push({
        kind: "turn",
        seq: seq - 2n,
        turn: run.ordinal,
        at: dateOf(run.startedAt) ?? dateOf(run.createdAt) ?? dateOf(it.createdAt),
        asker: actorIdString(run),
        ...(wakeReason ? { wakeReason } : {}),
      });
    }
    const at = dateOf(it.createdAt);
    switch (it.kind) {
      case TurnItemKind.USER_MESSAGE:
        out.push({
          kind: "user",
          seq,
          text: it.text,
          at,
          asker: it.actor ? actorClientId(it.actor) : run ? actorClientId(run.author) : "",
          attachments: it.attachments,
        });
        break;
      case TurnItemKind.ASSISTANT_MESSAGE:
        out.push({ kind: "agent", seq, text: it.text, at, runId: it.runId });
        break;
      case TurnItemKind.PROPOSED_PLAN:
        out.push({ kind: "agent", seq, text: formatProposedPlan(it.planTitle, it.text), at, runId: it.runId });
        break;
      case TurnItemKind.TOOL_CALL:
      case TurnItemKind.COMMAND_EXECUTION:
      case TurnItemKind.FILE_CHANGE:
        out.push(toolItem(it, run, activeBgItems));
        break;
      case TurnItemKind.RUNTIME_REQUEST: {
        const req = reqs.get(it.requestId);
        if (req && req.kind === RuntimeRequestKind.USER_INPUT && req.status !== RuntimeRequestStatus.PENDING) {
          const by = req.resolvedBy?.kind === ActorKind.OWNER ? "owner" : (req.resolvedBy?.id ?? "");
          const answers = Object.entries(req.resolvedAnswers).map(([itemId, a]) =>
            create(AnswerSchema, { itemId, selected: a.selectedOptions, text: a.customText }),
          );
          out.push({
            kind: "question",
            seq,
            question: sessionQuestion(req),
            resolution: create(QuestionResolvedSchema, {
              questionId: req.requestId,
              resolution: req.questionResolution,
              by,
              answers,
            }),
          });
        }
        break;
      }
      case TurnItemKind.MESSAGE_RECEIVED:
        out.push({
          kind: "message",
          seq,
          direction: "in",
          name: it.mailName,
          address: it.mailAddress,
          text: it.text,
          delivered: it.mailDelivered,
        });
        break;
      case TurnItemKind.MESSAGE_SENT:
        out.push({
          kind: "message",
          seq,
          direction: "out",
          name: it.mailName,
          address: it.mailAddress,
          text: it.text,
          delivered: "",
        });
        break;
      case TurnItemKind.WORKSPACE:
        if (it.workspace) {
          const state = it.status === "completed" ? "done" : it.status === "running" ? "running" : "failed";
          out.push({ kind: "workspace", seq, state, step: it.workspace, failure: it.failure?.message || it.text });
        }
        break;
      case TurnItemKind.ERROR:
        if (it.runId === setupFailed) break;
        out.push({
          kind: "error",
          seq,
          error: create(ErrorEventSchema, { message: it.text, failure: it.failure ?? runFailure(it.text) }),
        });
        break;
    }
    if (run?.status === RunStatus.INTERRUPTED && items[i + 1]?.runId !== it.runId) {
      out.push({
        kind: "turnEnd",
        seq: seq + 1n,
        end: create(TurnCompletedSchema, {
          turn: run.ordinal,
          isError: true,
          failure: create(FailureSchema, { kind: FailureKind.INTERRUPTED, message: run.errorMessage }),
        }),
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

export function sessionFailure(s: Session | undefined): Failure | undefined {
  if (!s) return undefined;
  if (s.progress?.lastFailure && s.progress.lastFailure.kind !== FailureKind.INTERRUPTED) {
    return s.progress.lastFailure;
  }
  if (s.error !== "") return runFailure(s.error);
  return undefined;
}

/** What the session's active run does now: runs a tool, thinks, writes its reply, or anything else (working). */
export type SessionWork = "tool" | "thinking" | "replying" | "working";

export function sessionWork(proj: SessionProjection | null | undefined): SessionWork {
  const run = proj?.session?.activeRunId;
  if (!proj || !run) return "working";
  const now = proj.items.filter((it) => it.runId === run);
  const tools = [TurnItemKind.TOOL_CALL, TurnItemKind.COMMAND_EXECUTION, TurnItemKind.FILE_CHANGE];
  if (now.some((it) => it.status === "running" && tools.includes(it.kind))) return "tool";
  if (now.some((it) => it.kind === TurnItemKind.REASONING && it.streaming)) return "thinking";
  if (now.some((it) => it.kind === TurnItemKind.ASSISTANT_MESSAGE && it.streaming)) return "replying";
  return "working";
}

export type TodoStepStatus = "pending" | "in_progress" | "completed" | "skipped" | "failed";

export interface TodoStepView {
  text: string;
  status: TodoStepStatus;
}

export function normalizeTodoStepStatus(raw: string): TodoStepStatus {
  switch (raw.trim().toLowerCase()) {
    case "completed":
    case "complete":
    case "done":
      return "completed";
    case "in_progress":
    case "inprogress":
    case "active":
    case "running":
      return "in_progress";
    case "skipped":
    case "cancelled":
    case "canceled":
      return "skipped";
    case "failed":
    case "error":
      return "failed";
    default:
      return "pending";
  }
}

/** Run IDs belonging to the current turn and any consecutive system continuation wakes from background tasks. */
function currentTurnChainRunIds(proj: SessionProjection): Set<string> {
  const out = new Set<string>();
  const activeId = proj.session?.activeRunId ?? "";
  let idx = activeId !== "" ? proj.runs.findIndex((r) => r.runId === activeId) : -1;
  if (idx < 0) {
    for (let i = proj.runs.length - 1; i >= 0; i--) {
      const st = proj.runs[i].status;
      if (st !== RunStatus.QUEUED && st !== RunStatus.ROLLED_BACK) {
        idx = i;
        break;
      }
    }
  }
  if (idx < 0) {
    if (activeId !== "") out.add(activeId);
    return out;
  }
  while (idx >= 0) {
    const r = proj.runs[idx];
    out.add(r.runId);
    if (r.author?.kind !== ActorKind.SYSTEM) break;
    idx--;
  }
  return out;
}

function isSessionChecklistLive(proj: SessionProjection): boolean {
  const s = proj.session;
  if (!s) return false;
  if (
    s.status === SessionStatus.PREPARING ||
    s.status === SessionStatus.RUNNING ||
    s.status === SessionStatus.WAITING_INPUT
  ) {
    return true;
  }
  if (s.status === SessionStatus.IDLE) {
    return s.activeBackgroundCount > 0 || activeBackgroundTasksOf(proj).some((t) => t.awaited);
  }
  return false;
}

/**
 * Steps for the floating live checklist above the composer while the session is working.
 * Automatically returns [] once the turn settles or all steps are completed/skipped.
 */
export function activeTodoSteps(proj: SessionProjection | null | undefined): TodoStepView[] {
  if (!proj || !isSessionChecklistLive(proj)) return [];
  const chain = currentTurnChainRunIds(proj);
  if (chain.size === 0) return [];
  const latest = proj.items.findLast((it) => it.kind === TurnItemKind.TODO_LIST && chain.has(it.runId));
  if (!latest || latest.todoSteps.length === 0) return [];
  const steps: TodoStepView[] = [];
  for (const s of latest.todoSteps) {
    const text = s.text.trim();
    if (!text) continue;
    steps.push({ text, status: normalizeTodoStepStatus(s.status) });
  }
  if (steps.length === 0 || steps.every((s) => s.status === "completed" || s.status === "skipped")) {
    return [];
  }
  return steps;
}

/**
 * Native live focus phrase for the bottom working line when the model is not actively streaming
 * thoughts or reply prose; undefined falls back to the localized generic work label.
 */
export function sessionFocusText(proj: SessionProjection | null | undefined): string | undefined {
  const s = proj?.session;
  if (!proj || !s || s.status !== SessionStatus.RUNNING || !s.activeRunId) return undefined;
  const work = sessionWork(proj);
  if (work === "thinking" || work === "replying") return undefined;

  const chain = currentTurnChainRunIds(proj);
  const latestTodo = proj.items.findLast((it) => it.kind === TurnItemKind.TODO_LIST && chain.has(it.runId));
  const explicitFocus = latestTodo?.text.trim();
  if (explicitFocus) return explicitFocus;

  const runningTool = proj.items.findLast(
    (it) =>
      it.runId === s.activeRunId &&
      it.status === "running" &&
      (it.kind === TurnItemKind.COMMAND_EXECUTION || it.kind === TurnItemKind.TOOL_CALL) &&
      it.text.trim() !== "",
  );
  if (runningTool) return runningTool.text.trim();

  const inProgressStep = activeTodoSteps(proj).find((st) => st.status === "in_progress");
  return inProgressStep?.text;
}

export function sessionLiveReplies(proj: SessionProjection | null | undefined): Set<bigint> {
  return new Set((proj?.items ?? []).filter((it) => it.kind === TurnItemKind.ASSISTANT_MESSAGE && it.streaming).map(itemSeq));
}

export function sessionQuestion(req: RuntimeRequest): Question {
  return create(QuestionSchema, {
    questionId: req.requestId,
    askedAt: req.createdAt,
    expiresAt: req.expiresAt,
    items: (req.userInput?.questions ?? []).map((q) =>
      create(QuestionItemSchema, {
        id: q.id,
        header: q.header,
        text: q.text,
        options: q.options.map((o) => create(QuestionOptionSchema, { id: o.id, label: o.label, description: o.description })),
        multiSelect: q.multiSelect,
        allowOther: q.allowOther,
        optional: q.optional,
        secret: q.secret,
      }),
    ),
  });
}

/** The turn was stopped on purpose (by the owner or the agent), not by a failure. */
export function isInterruption(f: Failure | undefined): boolean {
  return f?.kind === FailureKind.INTERRUPTED;
}

/** Who stopped an interrupted turn, as the machine recorded it: "owner" or an agent's client id. */
export function interrupter(f: Failure | undefined): string | undefined {
  const prefix = "interrupted by ";
  return isInterruption(f) && f!.message.startsWith(prefix) ? f!.message.slice(prefix.length) : undefined;
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
