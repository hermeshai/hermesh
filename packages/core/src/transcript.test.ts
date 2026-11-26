import { describe, expect, it, test } from "vitest";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import {
  ActorKind,
  ActorSchema,
  BackgroundTaskKind,
  BackgroundTaskSchema,
  BackgroundTaskStatus,
  FailureKind,
  QuestionAnswerSchema,
  QuestionItemSchema,
  QuestionOptionSchema,
  QuestionResolution,
  QuestionResolvedSchema,
  RunSchema,
  RunStatus,
  RuntimeRequestKind,
  RuntimeRequestSchema,
  RuntimeRequestStatus,
  SessionProjectionSchema,
  SessionSchema,
  SessionStatus,
  TurnItemKind,
  TurnItemSchema,
  UserInputPayloadSchema,
  WorkspaceAction,
  WorkspaceStepSchema,
} from "@hermesh/proto/session";
import { Workspace } from "@hermesh/proto/identity";
import { groupSteps, replyEnds, stepSummary } from "./steps";
import {
  activeTodoSteps,
  interrupter,
  isInterruption,
  resolutionTitle,
  sessionFocusText,
  sessionFailure,
  sessionLiveReplies,
  sessionQuestion,
  sessionWork,
  sessionTranscript,
  workspaceDetails,
  workspaceFailedRun,
  workspaceTitle,
  type WorkspaceRow,
} from "./transcript";

const at = new Date("2026-11-23T10:00:00Z");
const item = (seq: number, runId: string, kind: TurnItemKind, more: MessageInitShape<typeof TurnItemSchema> = {}) =>
  create(TurnItemSchema, { itemId: `i${seq}`, runId, seq: BigInt(seq), kind, status: "completed", ...more });

describe("unified session transcript", () => {
  const proj = create(SessionProjectionSchema, {
    session: create(SessionSchema, { sessionId: "s_1", status: SessionStatus.IDLE }),
    runs: [
      create(RunSchema, {
        runId: "r1",
        ordinal: 1,
        status: RunStatus.COMPLETED,
        startedAt: timestampFromDate(at),
        author: create(ActorSchema, { kind: ActorKind.EXTERNAL_AGENT, id: "c_bot" }),
      }),
      create(RunSchema, { runId: "r2", ordinal: 2, status: RunStatus.INTERRUPTED, errorMessage: "interrupted by owner" }),
    ],
    items: [
      item(1, "r1", TurnItemKind.USER_MESSAGE, { text: "hi" }),
      item(2, "r1", TurnItemKind.REASONING, { text: "thinking" }),
      item(3, "r1", TurnItemKind.COMMAND_EXECUTION, { toolName: "Bash", command: "ls", output: "a\nb" }),
      item(4, "r1", TurnItemKind.COMMAND_EXECUTION, { command: "false", exitCode: 1, status: "failed" }),
      item(5, "r1", TurnItemKind.TOOL_CALL, { toolName: "Read", filePath: "README.md", status: "running" }),
      item(6, "r1", TurnItemKind.FILE_CHANGE, { filePath: "a.txt", diff: "+x" }),
      item(7, "r1", TurnItemKind.CHECKPOINT, { checkpointId: "c1" }),
      item(8, "r1", TurnItemKind.ASSISTANT_MESSAGE, { text: "done" }),
      item(9, "r2", TurnItemKind.USER_MESSAGE, { text: "again" }),
      item(10, "r2", TurnItemKind.ASSISTANT_MESSAGE, { text: "par", streaming: true }),
    ],
  });

  it("makes conversation rows in order with turn asker and leaves out non-rendered metadata items", () => {
    const rows = sessionTranscript(proj);
    expect(rows.map((r) => r.kind)).toEqual(["turn", "user", "tool", "tool", "tool", "tool", "agent", "turn", "user", "agent", "turnEnd"]);
    expect(new Set(rows.map((r) => r.seq)).size).toBe(rows.length);
    const first = rows[0];
    expect(first.kind === "turn" && first.at?.getTime()).toBe(at.getTime());
    expect(first.kind === "turn" && first.asker).toBe("c_bot");
  });

  it("renders inter-agent messages and settled questions", () => {
    const withMailAndQ = create(SessionProjectionSchema, {
      requests: [
        create(RuntimeRequestSchema, {
          requestId: "req1",
          kind: RuntimeRequestKind.USER_INPUT,
          status: RuntimeRequestStatus.RESOLVED,
          questionResolution: QuestionResolution.ANSWERED,
          resolvedBy: create(ActorSchema, { kind: ActorKind.OWNER, id: "owner" }),
          resolvedAnswers: { q1: create(QuestionAnswerSchema, { selectedOptions: ["Red"] }) },
          userInput: create(UserInputPayloadSchema, {
            questions: [create(QuestionItemSchema, { id: "q1", text: "Color?", options: [create(QuestionOptionSchema, { id: "o1", label: "Red" })] })],
          }),
        }),
      ],
      items: [
        item(1, "r1", TurnItemKind.MESSAGE_RECEIVED, { mailName: "helper", mailAddress: "c_helper", text: "check staging", mailDelivered: "queued" }),
        item(2, "r1", TurnItemKind.MESSAGE_SENT, { mailName: "bot", mailAddress: "c_bot", text: "halfway" }),
        item(3, "r1", TurnItemKind.RUNTIME_REQUEST, { requestId: "req1" }),
      ],
    });
    const rows = sessionTranscript(withMailAndQ);
    expect(rows[0]).toMatchObject({ kind: "message", direction: "in", name: "helper", address: "c_helper", text: "check staging", delivered: "queued" });
    expect(rows[1]).toMatchObject({ kind: "message", direction: "out", name: "bot", address: "c_bot", text: "halfway" });
    expect(rows[2]?.kind === "question" && rows[2].resolution?.by).toBe("owner");
  });

  it("tells tool calls as grouped steps", () => {
    const steps = groupSteps(sessionTranscript(proj));
    const tools = steps.find((s) => s.kind === "tools");
    if (tools?.kind !== "tools") throw new Error("no tools");
    expect(tools.calls.map((c) => c.call.kind)).toEqual(["shell", "shell", "read", "edit"]);
    expect(tools.calls[0].call.summary).toBe("ls");
    expect(tools.calls[0].result?.summary).toBe("a\nb");
    expect(tools.calls[1].result?.isError).toBe(true);
    expect(tools.calls[2].call.summary).toBe("README.md");
    expect(tools.calls[2].running).toBe(false);
    expect(tools.calls[3].call.summary).toBe("a.txt");
    expect(tools.calls[3].result?.summary).toBe("+x");
    expect(stepSummary(tools.calls)).not.toBe("");
  });

  it("streams live output on running commands and preserves output blob references", () => {
    const liveProj = create(SessionProjectionSchema, {
      session: create(SessionSchema, { sessionId: "s_live", status: SessionStatus.RUNNING, activeRunId: "r_live" }),
      runs: [create(RunSchema, { runId: "r_live", ordinal: 1, status: RunStatus.RUNNING })],
      items: [
        item(1, "r_live", TurnItemKind.COMMAND_EXECUTION, { command: "go test ./...", status: "running", output: "ok pkg/a\n" }),
        item(2, "r_live", TurnItemKind.COMMAND_EXECUTION, {
          command: "cat big.log",
          input: '{\n  "command": "cat big.log"\n}',
          inputTruncated: true,
          inputBlobSha256: "sha_in_456",
          inputSize: 20480n,
          status: "completed",
          output: "head...",
          outputTruncated: true,
          outputBlobSha256: "sha_big_123",
          outputSize: 32768n,
        }),
      ],
    });
    const rows = sessionTranscript(liveProj);
    expect(rows[1]?.kind === "tool" && rows[1].running).toBe(true);
    expect(rows[1]?.kind === "tool" && rows[1].result?.summary).toBe("ok pkg/a\n");
    expect(rows[1]?.kind === "tool" && rows[1].result?.isError).toBe(false);
    expect(rows[2]?.kind === "tool" && rows[2].call.input).toContain('"command": "cat big.log"');
    expect(rows[2]?.kind === "tool" && rows[2].call.inputTruncated).toBe(true);
    expect(rows[2]?.kind === "tool" && rows[2].call.inputBlobSha256).toBe("sha_in_456");
    expect(rows[2]?.kind === "tool" && rows[2].call.inputSize).toBe(20480n);
    expect(rows[2]?.kind === "tool" && rows[2].result?.blobSha256).toBe("sha_big_123");
    expect(rows[2]?.kind === "tool" && rows[2].result?.size).toBe(32768n);
  });

  it("ends an interrupted run as an interrupted turn and marks live replies", () => {
    const rows = sessionTranscript(proj);
    const end = rows.at(-1);
    expect(end?.kind === "turnEnd" && end.end.failure?.kind).toBe(FailureKind.INTERRUPTED);
    const steps = groupSteps(rows);
    expect(replyEnds(steps, false).size).toBe(2);
    const live = sessionLiveReplies(proj);
    const agent = rows.filter((r) => r.kind === "agent");
    expect(agent.map((r) => live.has(r.seq))).toEqual([false, true]);
  });

  it("reports session failure, questions, proposed plans, and running tools", () => {
    expect(sessionFailure(create(SessionSchema, { status: SessionStatus.CRASHED, error: "boom" }))?.message).toBe("boom");
    expect(sessionFailure(create(SessionSchema, { status: SessionStatus.RUNNING }))).toBeUndefined();

    const q = sessionQuestion(
      create(RuntimeRequestSchema, {
        requestId: "req1",
        kind: RuntimeRequestKind.USER_INPUT,
        userInput: create(UserInputPayloadSchema, {
          questions: [create(QuestionItemSchema, { id: "q1", text: "Color?", options: [create(QuestionOptionSchema, { id: "o1", label: "Red" })], allowOther: true, optional: false })],
        }),
      }),
    );
    expect(q.questionId).toBe("req1");
    expect(q.items[0]).toMatchObject({ id: "q1", text: "Color?", allowOther: true, optional: false });

    const planRows = sessionTranscript(
      create(SessionProjectionSchema, {
        items: [
          item(1, "r1", TurnItemKind.PROPOSED_PLAN, { planTitle: "Unified Session", text: "1. Build\n2. Test" }),
          item(2, "r1", TurnItemKind.PROPOSED_PLAN, { planTitle: "Unified Session", text: "# Unified Session\n\n1. Build\n2. Test" }),
        ],
      }),
    );
    expect(planRows.map((r) => (r.kind === "agent" ? r.text : ""))).toEqual([
      "### Unified Session\n\n1. Build\n2. Test",
      "# Unified Session\n\n1. Build\n2. Test",
    ]);

    // Only the active run counts: r1's tool, reasoning and reply say nothing about r2.
    const workProj = create(SessionProjectionSchema, {
      session: create(SessionSchema, { sessionId: "s_1", status: SessionStatus.RUNNING, activeRunId: "r2" }),
      items: [
        item(1, "r1", TurnItemKind.TOOL_CALL, { toolName: "Read", status: "running" }),
        item(2, "r1", TurnItemKind.REASONING, { streaming: true }),
        item(3, "r1", TurnItemKind.ASSISTANT_MESSAGE, { streaming: true }),
      ],
    });
    expect(sessionWork(workProj)).toBe("working");
    workProj.items.push(item(4, "r2", TurnItemKind.REASONING, { text: "weigh it", streaming: true }));
    expect(sessionWork(workProj)).toBe("thinking");
    workProj.items[3].streaming = false;
    expect(sessionWork(workProj)).toBe("working");
    workProj.items.push(item(5, "r2", TurnItemKind.ASSISTANT_MESSAGE, { text: "On it", streaming: true }));
    expect(sessionWork(workProj)).toBe("replying");
    workProj.items.push(item(6, "r2", TurnItemKind.COMMAND_EXECUTION, { command: "go test ./...", status: "running" }));
    expect(sessionWork(workProj)).toBe("tool");
    workProj.items[5].status = "completed";
    workProj.items[4].streaming = false;
    expect(sessionWork(workProj)).toBe("working");
    workProj.session!.activeRunId = "";
    expect(sessionWork(workProj)).toBe("working");
  });

  test("prompts say who asked, background commands stay running, and continuation runs surface wakeReason", () => {
    const p = create(SessionProjectionSchema, {
      session: create(SessionSchema, { sessionId: "s_bg", status: SessionStatus.IDLE, activeBackgroundCount: 1 }),
      runs: [
        create(RunSchema, { runId: "r1", ordinal: 1, status: RunStatus.COMPLETED, author: create(ActorSchema, { kind: ActorKind.HER, id: "c_her" }) }),
        create(RunSchema, { runId: "r2", ordinal: 2, status: RunStatus.COMPLETED, author: create(ActorSchema, { kind: ActorKind.OWNER, id: "dev_1" }) }),
        create(RunSchema, { runId: "r3", ordinal: 3, status: RunStatus.COMPLETED, author: create(ActorSchema, { kind: ActorKind.SYSTEM, id: "background" }), wakeReason: "pytest -q" }),
      ],
      items: [
        item(1, "r1", TurnItemKind.USER_MESSAGE, { text: "log in, come back" }),
        item(2, "r1", TurnItemKind.COMMAND_EXECUTION, { itemId: "r1:cmd_bg", command: "make e2e", status: "running" }),
        item(3, "r2", TurnItemKind.USER_MESSAGE, { text: "keep the query too" }),
        item(4, "r3", TurnItemKind.USER_MESSAGE, { text: "also docs", actor: create(ActorSchema, { kind: ActorKind.HER, id: "c_her" }) }),
        item(5, "r3", TurnItemKind.ASSISTANT_MESSAGE, { text: "all passed" }),
      ],
      backgroundTasks: [
        create(BackgroundTaskSchema, {
          taskId: "cmd_bg",
          runId: "r1",
          itemId: "r1:cmd_bg",
          kind: BackgroundTaskKind.COMMAND,
          status: BackgroundTaskStatus.RUNNING,
          awaited: true,
        }),
      ],
    });
    const rows = sessionTranscript(p);
    expect(rows.flatMap((r) => (r.kind === "user" ? [r.asker] : []))).toEqual(["c_her", "owner", "c_her"]);
    const bgTool = rows.find((r) => r.kind === "tool");
    expect(bgTool?.kind === "tool" && bgTool.running).toBe(true);
    expect(bgTool?.kind === "tool" && bgTool.result).toBeUndefined();
    expect(rows.flatMap((r) => (r.kind === "turn" ? [r.wakeReason ?? ""] : []))).toEqual(["", "", "pytest -q"]);
  });

  test("how a question was settled and who interrupted", () => {
    const r = (o: object) => create(QuestionResolvedSchema, o);
    expect(resolutionTitle(r({ resolution: QuestionResolution.ANSWERED, by: "owner", answers: [{ selected: ["红"] }, { text: "深一点" }] }))).toBe(
      "你回答：红；深一点",
    );
    expect(resolutionTitle(r({ resolution: QuestionResolution.DECLINED, by: "c1" }), () => "bot")).toBe("bot 跳过了回答");
    expect(resolutionTitle(r({ resolution: QuestionResolution.TIMED_OUT, by: "hermesh" }))).toBe("无人回答，Agent 已自行继续");
    expect(isInterruption({ kind: FailureKind.INTERRUPTED } as never)).toBe(true);
    expect(interrupter({ kind: FailureKind.INTERRUPTED, message: "interrupted by owner" } as never)).toBe("owner");
  });
});

// A first turn's WORKSPACE item is a row of its own, never in a group of tools; a failed one says why itself, so the
// run's own error is not a row too.
test("the workspace step is a row of its own", () => {
  const step = create(WorkspaceStepSchema, { mode: Workspace.WORKTREE, action: WorkspaceAction.CREATE, branch: "hermesh/s", baseRef: "main", path: "/wt/s" });
  const proj = (status: string, extra: MessageInitShape<typeof TurnItemSchema>[] = []) =>
    create(SessionProjectionSchema, {
      session: { sessionId: "s" },
      runs: [{ runId: "r1", ordinal: 1 }],
      items: [
        { itemId: "u", runId: "r1", seq: 1n, kind: TurnItemKind.USER_MESSAGE, text: "hi" },
        { itemId: "w", runId: "r1", seq: 2n, kind: TurnItemKind.WORKSPACE, status, workspace: step, failure: status === "failed" ? { message: "no main" } : undefined },
        ...extra,
      ],
    });
  const tool = { itemId: "t", runId: "r1", seq: 3n, kind: TurnItemKind.COMMAND_EXECUTION, status: "completed", command: "ls" };
  let steps = groupSteps(sessionTranscript(proj("running", [tool])));
  expect(steps.map((s) => s.kind)).toEqual(["turn", "user", "workspace", "tools"]);
  const running = steps[2] as WorkspaceRow;
  expect(running.state).toBe("running");
  expect(workspaceTitle(running).code).toBe("main");

  steps = groupSteps(sessionTranscript(proj("completed")));
  const done = steps[2] as WorkspaceRow;
  expect(workspaceTitle(done)).toEqual({ text: "从 main 新建了分支", code: "hermesh/s" });
  expect(workspaceDetails(done).map((d) => d.value)).toEqual(["独立 worktree", "hermesh/s", "main", "/wt/s"]);

  const failed = proj("failed", [{ itemId: "e", runId: "r1", seq: 3n, kind: TurnItemKind.ERROR, text: "no main" }]);
  const rows = sessionTranscript(failed);
  expect(rows.some((r) => r.kind === "error")).toBe(false);
  const row = rows.at(-1) as WorkspaceRow;
  expect(row.state).toBe("failed");
  expect(workspaceDetails(row)).toEqual([{ label: "原因", value: "no main" }]);
  expect(workspaceFailedRun(failed)).toBe("r1");

  // A switch says where the folder was.
  const sw = create(WorkspaceStepSchema, { mode: Workspace.IN_PLACE, action: WorkspaceAction.SWITCH, branch: "feat", previousBranch: "main", path: "/repo" });
  const swRow: WorkspaceRow = { kind: "workspace", seq: 1n, state: "done", step: sw, failure: "" };
  expect(workspaceTitle(swRow)).toEqual({ text: "切到了分支", code: "feat" });
  expect(workspaceDetails(swRow).map((d) => d.value)).toEqual(["原地", "feat（原来在 main）", "/repo"]);
});

  test("user messages preserve their attachments in the transcript, including attachment-only prompts", () => {
  const rows = sessionTranscript(
    create(SessionProjectionSchema, {
      runs: [{ runId: "r1", ordinal: 1 }],
      items: [
        item(1, "r1", TurnItemKind.USER_MESSAGE, {
          text: "",
          attachments: [{ blobSha256: "a".repeat(64), name: "shot.png", mimeType: "image/png", sizeBytes: 512n }],
        }),
      ],
    }),
  );
  const user = rows.find((r) => r.kind === "user");
  expect(user?.kind === "user" ? user.attachments?.map((a) => a.name) : []).toEqual(["shot.png"]);
});

test("activeTodoSteps and sessionFocusText track live turns, background continuations, and auto-collapse", () => {
  const p = create(SessionProjectionSchema, {
    session: create(SessionSchema, { sessionId: "s_todo", status: SessionStatus.RUNNING, activeRunId: "r1" }),
    runs: [
      create(RunSchema, { runId: "r1", ordinal: 1, status: RunStatus.RUNNING, author: create(ActorSchema, { kind: ActorKind.OWNER, id: "dev" }) }),
    ],
    items: [
      item(1, "r1", TurnItemKind.USER_MESSAGE, { text: "optimize halo" }),
      item(2, "r1", TurnItemKind.TODO_LIST, {
        itemId: "r1:todo:todo",
        text: "Inspecting motion constraints",
        todoSteps: [
          { text: "Check motion tests", status: "in_progress" },
          { text: "Rewrite CALayer sprite", status: "pending" },
        ],
      }),
    ],
  });

  // Explicit focus wins while working, but yields to thinking/replying:
  expect(activeTodoSteps(p)).toEqual([
    { text: "Check motion tests", status: "in_progress" },
    { text: "Rewrite CALayer sprite", status: "pending" },
  ]);
  expect(sessionFocusText(p)).toBe("Inspecting motion constraints");

  p.items.push(item(3, "r1", TurnItemKind.REASONING, { text: "weighing...", streaming: true }));
  expect(sessionFocusText(p)).toBeUndefined();
  p.items[2].streaming = false;

  // Without explicit focus, running tool summary wins over in_progress step, and empty summary falls back:
  p.items[1].text = "";
  p.items.push(item(4, "r1", TurnItemKind.COMMAND_EXECUTION, { command: "pnpm bench", text: "Run GPU benchmark", status: "running" }));
  expect(sessionFocusText(p)).toBe("Run GPU benchmark");
  p.items[3].status = "completed";
  expect(sessionFocusText(p)).toBe("Check motion tests");

  // Stays visible while idle waiting on an awaited background task, and carries into SYSTEM continuation run r2:
  p.session!.status = SessionStatus.IDLE;
  p.session!.activeRunId = "";
  p.session!.activeBackgroundCount = 1;
  expect(activeTodoSteps(p)).toHaveLength(2);

  p.runs.push(create(RunSchema, { runId: "r2", ordinal: 2, status: RunStatus.RUNNING, author: create(ActorSchema, { kind: ActorKind.SYSTEM, id: "bg" }), wakeReason: "Run GPU benchmark" }));
  p.session!.status = SessionStatus.RUNNING;
  p.session!.activeRunId = "r2";
  expect(activeTodoSteps(p)).toHaveLength(2);

  // New user turn r3 does not inherit stale incomplete steps from r1:
  p.runs.push(create(RunSchema, { runId: "r3", ordinal: 3, status: RunStatus.RUNNING, author: create(ActorSchema, { kind: ActorKind.OWNER, id: "dev" }) }));
  p.session!.activeRunId = "r3";
  expect(activeTodoSteps(p)).toEqual([]);

  // Auto-collapses when all steps in the active turn are completed/skipped:
  p.items.push(
    item(5, "r3", TurnItemKind.TODO_LIST, {
      itemId: "r3:todo:todo",
      todoSteps: [
        { text: "Step A", status: "done" },
        { text: "Step B", status: "skipped" },
      ],
    }),
  );
  expect(activeTodoSteps(p)).toEqual([]);
});
