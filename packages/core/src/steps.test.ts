import { create } from "@bufbuild/protobuf";
import { ToolCallSchema } from "@hermesh/proto/session";
import { describe, expect, test } from "vitest";
import { groupSteps, stepSummary, toolObject, toolVerb, turnTimeLine } from "./steps";
import type { TranscriptItem } from "./transcript";
import { now } from "./testing";

const call = (kind: string, summary: string, extra: object = {}) => create(ToolCallSchema, { callId: summary, kind, summary, ...extra });
const tool = (seq: number, kind: string, summary: string): TranscriptItem => ({ kind: "tool", seq: BigInt(seq), call: call(kind, summary), result: undefined, running: false });

describe("steps", () => {
  test("consecutive tool calls are one step; anything between them ends it", () => {
    const steps = groupSteps([
      { kind: "user", seq: 1n, text: "做", at: undefined },
      tool(2, "read", "a.ts"),
      tool(3, "read", "b.ts"),
      tool(4, "shell", "go test"),
      { kind: "agent", seq: 5n, text: "好" },
      tool(6, "edit", "a.ts"),
    ]);
    expect(steps.map((s) => s.kind)).toEqual(["user", "tools", "agent", "tools"]);
    expect(steps[1].kind === "tools" && steps[1].calls.length).toBe(3);
    expect(steps[1].kind === "tools" && stepSummary(steps[1].calls)).toBe("读取了 2 个文件、运行了 1 条命令");
  });

  test("a call in Chinese words, without the agent's tool name", () => {
    expect(toolVerb(call("edit", "x"), false)).toBe("已编辑");
    expect(toolVerb(call("shell", "x"), true)).toBe("正在运行");
    expect(toolVerb(call("shell", "x", { decision: "deny" }), false)).toBe("已拒绝");
    expect(toolObject(call("edit", "Write hello.txt", { name: "edit" }))).toBe("hello.txt");
    expect(toolObject(call("shell", "Bash go test ./...", { name: "Bash" }))).toBe("go test ./...");
    expect(toolObject(call("shell", "echo ran > ran.txt"))).toBe("echo ran > ran.txt");
    expect(toolObject(call("other", "", { name: "Task" }))).toBe("Task");
    expect(stepSummary([{ call: call("shell", "x", { decision: "deny" }) }, { call: call("weird", "y") }])).toBe("拒绝了 1 个操作、使用了 1 个工具");
  });

  test("a turn's time line only for the first turn and after half an hour", () => {
    const t = (min: number) => new Date(now.getTime() + min * 60_000);
    expect(turnTimeLine(t(-10), undefined, now)).toMatch(/^今天 \d\d:\d\d$/);
    expect(turnTimeLine(t(-5), t(-10), now)).toBeNull();
    expect(turnTimeLine(t(-5), t(-60), now)).toMatch(/^今天 /);
    expect(turnTimeLine(t(-24 * 60), undefined, now)).toMatch(/^昨天 /);
  });
});
