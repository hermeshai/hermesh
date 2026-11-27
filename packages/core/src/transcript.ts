import { create } from "@bufbuild/protobuf";
import { Workspace } from "@hermesh/proto/identity";
import {
  ActorKind,
  ApprovalDecision,
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
  type QuestionItem,
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

function isRedundantQuestionToolCall(it: TurnItem): boolean {
  if (it.kind !== TurnItemKind.TOOL_CALL || it.status === "failed" || it.exitCode !== 0) return false;
  let norm = it.toolName.trim().toLowerCase();
  if (norm.startsWith("run ")) norm = norm.slice(4).trim();
  return norm === "ask_question" || norm === "askuserquestion" || norm === "request_user_input";
}

function settledResolutionOf(req: RuntimeRequest): QuestionResolved {
  const by = req.resolvedBy && req.resolvedBy.kind !== ActorKind.UNSPECIFIED ? actorClientId(req.resolvedBy) : "";
  let resolution = req.questionResolution;
  if (resolution === QuestionResolution.UNSPECIFIED) {
    if (req.status === RuntimeRequestStatus.EXPIRED) resolution = QuestionResolution.TIMED_OUT;
    else if (req.status === RuntimeRequestStatus.DISMISSED) resolution = QuestionResolution.CANCELLED;
    else resolution = req.resolvedDecision === ApprovalDecision.DECLINE ? QuestionResolution.DECLINED : QuestionResolution.ANSWERED;
  }
  const orderedIds = (req.userInput?.questions ?? []).map((q) => q.id);
  const seen = new Set<string>();
  const answers = [];
  if (resolution === QuestionResolution.ANSWERED) {
    for (const qid of orderedIds) {
      const a = req.resolvedAnswers[qid];
      if (a) {
        seen.add(qid);
        answers.push(create(AnswerSchema, { itemId: qid, selected: a.selectedOptions, text: a.customText }));
      }
    }
    for (const [itemId, a] of Object.entries(req.resolvedAnswers)) {
      if (!seen.has(itemId)) {
        answers.push(create(AnswerSchema, { itemId, selected: a.selectedOptions, text: a.customText }));
      }
    }
  }
  return create(QuestionResolvedSchema, {
    questionId: req.requestId,
    resolution,
    by,
    answers,
  });
}

/**
 * sessionTranscript converts a unified SessionProjection into conversation rows.
 */
export function sessionTranscript(proj: SessionProjection | null | undefined): TranscriptItem[] {
  if (!proj) return [];
  const runs = new Map(proj.runs.map((r) => [r.runId, r]));
  const reqs = new Map(proj.requests.map((r) => [r.requestId, r]));
  const questionRuns = new Set<string>();
  for (const r of proj.requests) {
    if (r.kind === RuntimeRequestKind.USER_INPUT && r.runId) questionRuns.add(r.runId);
  }
  for (const it of proj.items) {
    if (it.kind === TurnItemKind.RUNTIME_REQUEST && reqs.get(it.requestId)?.kind === RuntimeRequestKind.USER_INPUT && it.runId) {
      questionRuns.add(it.runId);
    }
  }
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
        if (isMetaProgressToolItem(it) || (questionRuns.has(it.runId) && isRedundantQuestionToolCall(it))) break;
        out.push(toolItem(it, run, activeBgItems));
        break;
      case TurnItemKind.COMMAND_EXECUTION:
      case TurnItemKind.FILE_CHANGE:
        out.push(toolItem(it, run, activeBgItems));
        break;
      case TurnItemKind.RUNTIME_REQUEST: {
        const req = reqs.get(it.requestId);
        if (req && req.kind === RuntimeRequestKind.USER_INPUT && req.status !== RuntimeRequestStatus.PENDING) {
          out.push({
            kind: "question",
            seq,
            question: sessionQuestion(req),
            resolution: settledResolutionOf(req),
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
  if (now.some((it) => it.status === "running" && tools.includes(it.kind) && !isMetaProgressToolItem(it))) return "tool";
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

function normalizeMetaProgressToolName(rawName: string): string {
  let norm = rawName.trim().toLowerCase();
  for (const pfx of ["run ", "call ", "tool: ", "tool "]) {
    if (norm.startsWith(pfx)) {
      norm = norm.slice(pfx.length).trim();
      break;
    }
  }
  const dbl = norm.lastIndexOf("__");
  const slash = norm.lastIndexOf("/");
  const dot = norm.lastIndexOf(".");
  if (dbl >= 0) norm = norm.slice(dbl + 2);
  else if (slash >= 0) norm = norm.slice(slash + 1);
  else if (dot >= 0) norm = norm.slice(dot + 1);

  for (const sfx of ["update_focus", "update_steps", "todowrite", "todo_write", "update_plan"]) {
    if (norm === sfx || (norm.startsWith("mcp_") && norm.endsWith(`_${sfx}`))) return sfx;
  }
  if (["todoread", "taskcreate", "taskupdate", "tasklist", "taskget"].includes(norm)) return norm;
  return "";
}

function isMetaProgressToolItem(it: TurnItem): boolean {
  return it.kind === TurnItemKind.TOOL_CALL && it.command === "" && normalizeMetaProgressToolName(it.toolName) !== "";
}

function reduceChainProgress(proj: SessionProjection, chain: Set<string>): { steps: TodoStepView[]; focus: string } {
  let steps: TodoStepView[] = [];
  let focus = "";
  for (const it of proj.items) {
    if (!chain.has(it.runId)) continue;
    if (it.kind === TurnItemKind.TODO_LIST) {
      if (it.todoSteps.length > 0) {
        const parsed: TodoStepView[] = [];
        for (const s of it.todoSteps) {
          const text = s.text.trim();
          if (text) parsed.push({ text, status: normalizeTodoStepStatus(s.status) });
        }
        steps = parsed;
      }
      focus = it.text.trim();
      continue;
    }
    if (it.kind !== TurnItemKind.TOOL_CALL || it.command !== "") continue;
    const meta = normalizeMetaProgressToolName(it.toolName);
    if (!meta || !it.input) continue;
    try {
      const obj = JSON.parse(it.input) as Record<string, unknown>;
      if (meta === "update_focus") {
        if (typeof obj.focus === "string" && obj.focus.trim()) focus = obj.focus.trim();
        continue;
      }
      const rawList = Array.isArray(obj.steps)
        ? obj.steps
        : Array.isArray(obj.todos)
          ? obj.todos
          : Array.isArray(obj.plan)
            ? obj.plan
            : Array.isArray(obj.items)
              ? obj.items
              : null;
      if (!rawList) continue;
      let nextFocus = typeof obj.focus === "string" ? obj.focus.trim() : typeof obj.explanation === "string" ? obj.explanation.trim() : "";
      const parsed: TodoStepView[] = [];
      for (const entry of rawList) {
        if (!entry || typeof entry !== "object") continue;
        const rec = entry as Record<string, unknown>;
        const rawText = [rec.title, rec.content, rec.step, rec.text].find((v) => typeof v === "string" && v.trim() !== "") as string | undefined;
        if (!rawText) continue;
        const status = normalizeTodoStepStatus(typeof rec.status === "string" ? rec.status : "");
        parsed.push({ text: rawText.trim(), status });
        if (!nextFocus && status === "in_progress" && typeof rec.activeForm === "string" && rec.activeForm.trim()) {
          nextFocus = rec.activeForm.trim();
        }
      }
      steps = parsed;
      focus = nextFocus;
    } catch {
      // Ignore malformed legacy tool input.
    }
  }
  return { steps, focus };
}

/**
 * Steps for the floating live checklist above the composer while the session is working.
 * Automatically returns [] once the turn settles or all steps are completed/skipped.
 */
export function activeTodoSteps(proj: SessionProjection | null | undefined): TodoStepView[] {
  if (!proj || !isSessionChecklistLive(proj)) return [];
  const chain = currentTurnChainRunIds(proj);
  if (chain.size === 0) return [];
  const { steps } = reduceChainProgress(proj, chain);
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
  const { focus: explicitFocus } = reduceChainProgress(proj, chain);
  if (explicitFocus) return explicitFocus;

  const runningTool = proj.items.findLast(
    (it) =>
      it.runId === s.activeRunId &&
      it.status === "running" &&
      (it.kind === TurnItemKind.COMMAND_EXECUTION || (it.kind === TurnItemKind.TOOL_CALL && !isMetaProgressToolItem(it))) &&
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

const recPrefixRe = /^\s*(?:\(Recommended\)|（推荐）)\s*/i;
const genericQuestionTextRe = /^the agent has (?:a|\d+) questions?\.?$/i;

export type ParsedOptionLabel = { recommended: boolean; text: string };

export function parseOptionLabel(label: string): ParsedOptionLabel {
  const m = recPrefixRe.exec(label);
  return m ? { recommended: true, text: label.slice(m[0].length) } : { recommended: false, text: label };
}

export type SettledQuestionStatus = "answered" | "skipped" | "declined" | "timedOut" | "withdrawn";

export type SettledQuestionRow = {
  prompt: string;
  choices: ParsedOptionLabel[];
  status: SettledQuestionStatus;
  statusText: string;
};

function settledItemPrompt(it: Pick<QuestionItem, "header" | "text">): string {
  const header = it.header.trim();
  const text = it.text.trim();
  if (header && (!text || genericQuestionTextRe.test(text))) return header;
  if (header && header !== text) return `${header} · ${text}`;
  return text || header;
}

export function settledQuestionRows(
  question: Question,
  r: QuestionResolved,
  name: (by: string) => string = (by) => by,
): SettledQuestionRow[] {
  const you = r.by === "" || r.by === "owner";
  const items = question.items;
  if (r.resolution !== QuestionResolution.ANSWERED) {
    let status: SettledQuestionStatus = "withdrawn";
    let note = t("resolution.withdrawn");
    if (r.resolution === QuestionResolution.DECLINED) {
      status = "declined";
      note = you ? t("resolution.declinedByYou") : t("resolution.declinedBy", { name: name(r.by) });
    } else if (r.resolution === QuestionResolution.TIMED_OUT) {
      status = "timedOut";
      note = t("resolution.timedOut");
    }
    const prompts = items.map(settledItemPrompt).filter(Boolean);
    if (prompts.length === 0) return [{ prompt: "", choices: [], status, statusText: note }];
    return prompts.map((prompt, idx) => ({
      prompt,
      choices: [],
      status,
      statusText: idx === prompts.length - 1 ? note : "",
    }));
  }

  if (items.length === 0) {
    const choices = r.answers.flatMap((a) => [
      ...a.selected.map(parseOptionLabel),
      ...(a.text.trim() === "" ? [] : [{ recommended: false, text: a.text.trim() }]),
    ]);
    return [{ prompt: "", choices, status: choices.length === 0 ? "skipped" : "answered", statusText: choices.length === 0 ? t("resolution.skipped") : "" }];
  }

  return items.map((it) => {
    const prompt = settledItemPrompt(it);
    const matched = r.answers.find((a) => a.itemId === it.id) ?? (items.length === 1 && r.answers.length === 1 && r.answers[0]!.itemId === "" ? r.answers[0] : undefined);
    const choices: ParsedOptionLabel[] = (matched?.selected ?? []).map(parseOptionLabel);
    const custom = (matched?.text ?? "").trim();
    if (custom !== "") choices.push({ recommended: false, text: custom });
    if (choices.length === 0) {
      return { prompt, choices: [], status: "skipped", statusText: t("resolution.skipped") };
    }
    return { prompt, choices, status: "answered", statusText: "" };
  });
}

/**
 * How a question was settled. name: who answered, by the id the machine
 * recorded (the owner is "You").
 */
export function resolutionTitle(r: QuestionResolved, name: (by: string) => string = (by) => by): string {
  const you = r.by === "owner";
  switch (r.resolution) {
    case QuestionResolution.ANSWERED: {
      const answers = r.answers
        .map((x) => [...x.selected.map((s) => parseOptionLabel(s).text), ...(x.text.trim() === "" ? [] : [x.text.trim()])].join(", "))
        .filter(Boolean)
        .join(t("resolution.separator"));
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
