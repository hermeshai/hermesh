import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { FailureKind } from "@hermesh/proto/session";
import {
  QuestionItemSchema,
  QuestionOptionSchema,
  RunSchema,
  RunStatus,
  RuntimeRequestKind,
  RuntimeRequestSchema,
  ThreadProjectionSchema,
  ThreadSchema,
  ThreadStatus,
  TurnItemKind,
  TurnItemSchema,
  UserInputPayloadSchema,
  type TurnItem,
} from "@hermesh/proto/thread";
import { groupSteps, replyEnds, stepSummary } from "./steps";
import { threadFailure, threadLiveReplies, threadQuestion, threadTranscript } from "./threadtranscript";

const at = new Date("2026-11-23T10:00:00Z");
const item = (seq: number, runId: string, kind: TurnItemKind, more: Partial<TurnItem> = {}) =>
  create(TurnItemSchema, { itemId: `i${seq}`, runId, seq: BigInt(seq), kind, status: "completed", ...more });

describe("a thread drawn as a session's conversation", () => {
  const proj = create(ThreadProjectionSchema, {
    thread: create(ThreadSchema, { threadId: "th_1", status: ThreadStatus.IDLE }),
    runs: [
      create(RunSchema, { runId: "r1", ordinal: 1, status: RunStatus.COMPLETED, startedAt: timestampFromDate(at) }),
      create(RunSchema, { runId: "r2", ordinal: 2, status: RunStatus.INTERRUPTED }),
    ],
    items: [
      item(1, "r1", TurnItemKind.USER_MESSAGE, { text: "hi" }),
      item(2, "r1", TurnItemKind.REASONING, { text: "thinking" }),
      item(3, "r1", TurnItemKind.COMMAND_EXECUTION, { toolName: "Bash", command: "ls", output: "a\nb" }),
      item(4, "r1", TurnItemKind.COMMAND_EXECUTION, { command: "false", exitCode: 1, status: "failed" }),
      item(5, "r1", TurnItemKind.TOOL_CALL, { toolName: "Read" }),
      item(6, "r1", TurnItemKind.FILE_CHANGE, { filePath: "a.txt", diff: "+x" }),
      item(7, "r1", TurnItemKind.CHECKPOINT, { checkpointId: "c1" }),
      item(8, "r1", TurnItemKind.ASSISTANT_MESSAGE, { text: "done" }),
      item(9, "r2", TurnItemKind.USER_MESSAGE, { text: "again" }),
      item(10, "r2", TurnItemKind.ASSISTANT_MESSAGE, { text: "par", streaming: true }),
    ],
  });

  it("makes the rows a session has, in order, leaving out what a session doesn't show", () => {
    const rows = threadTranscript(proj);
    expect(rows.map((r) => r.kind)).toEqual(["turn", "user", "tool", "tool", "tool", "tool", "agent", "turn", "user", "agent", "turnEnd"]);
    expect(new Set(rows.map((r) => r.seq)).size).toBe(rows.length);
    expect(rows.map((r) => r.seq)).toEqual([...rows.map((r) => r.seq)].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0)));
    const first = rows[0];
    expect(first.kind === "turn" && first.at?.getTime()).toBe(at.getTime());
  });

  it("tells tool calls as a session's steps", () => {
    const steps = groupSteps(threadTranscript(proj));
    const tools = steps.find((s) => s.kind === "tools");
    if (tools?.kind !== "tools") throw new Error("no tools");
    expect(tools.calls.map((c) => c.call.kind)).toEqual(["shell", "shell", "read", "edit"]);
    expect(tools.calls[0].call.summary).toBe("ls");
    expect(tools.calls[0].result?.summary).toBe("a\nb");
    expect(tools.calls[1].result?.isError).toBe(true);
    expect(tools.calls[3].call.summary).toBe("a.txt");
    expect(tools.calls[3].result?.summary).toBe("+x");
    expect(stepSummary(tools.calls)).not.toBe("");
  });

  it("ends an interrupted run as a session's interrupted turn, and finds where each reply ends", () => {
    const rows = threadTranscript(proj);
    const end = rows.at(-1);
    expect(end?.kind === "turnEnd" && end.end.failure?.kind).toBe(FailureKind.INTERRUPTED);
    const steps = groupSteps(rows);
    expect(replyEnds(steps, false).size).toBe(2);
  });

  it("marks the reply still being written", () => {
    const live = threadLiveReplies(proj);
    const agent = threadTranscript(proj).filter((r) => r.kind === "agent");
    expect(agent.map((r) => live.has(r.seq))).toEqual([false, true]);
  });

  it("tells a failure, or a message the machine rejected, once, until the next run clears it", () => {
    const failed = create(ThreadSchema, { status: ThreadStatus.FAILED, lastError: "boom" });
    expect(threadFailure(failed)?.message).toBe("boom");
    expect(threadFailure(create(ThreadSchema, { status: ThreadStatus.IDLE, lastError: "rejected" }))?.message).toBe("rejected");
    expect(threadFailure(create(ThreadSchema, { status: ThreadStatus.RUNNING }))).toBeUndefined();
    const rows = threadTranscript(create(ThreadProjectionSchema, { items: [item(1, "", TurnItemKind.ERROR, { text: "boom" })] }));
    expect(rows[0]?.kind === "error" && rows[0].error.failure?.message).toBe("boom");
  });

  it("asks a thread's questions as a session's", () => {
    const q = threadQuestion(
      create(RuntimeRequestSchema, {
        requestId: "req1",
        kind: RuntimeRequestKind.USER_INPUT,
        userInput: create(UserInputPayloadSchema, {
          questions: [create(QuestionItemSchema, { id: "q1", question: "Color?", options: [create(QuestionOptionSchema, { id: "o1", label: "Red" })], allowCustom: true, required: true })],
        }),
      }),
    );
    expect(q.questionId).toBe("req1");
    expect(q.items[0]).toMatchObject({ id: "q1", text: "Color?", allowOther: true, optional: false });
    expect(q.items[0].options[0].label).toBe("Red");
  });
});
