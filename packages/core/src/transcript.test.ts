import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import {
  FailureKind,
  QuestionResolution,
  QuestionResolvedSchema,
  SessionEventSchema,
  type SessionEvent,
} from "@hermesh/proto/session";
import { describe, expect, test } from "vitest";
import { isInterruption, resolutionTitle, transcript, turnStartedAt } from "./transcript";
import { at } from "./testing";

let seq = 0n;
function ev(seconds: number, kind: MessageInitShape<typeof SessionEventSchema>["kind"]): SessionEvent {
  return create(SessionEventSchema, { seq: ++seq, ts: timestampFromDate(at(seconds)), kind });
}

const conversation = () => [
  ev(0, { case: "status", value: { status: 3 } }),
  ev(0, { case: "turnStarted", value: { turn: 1 } }),
  ev(1, { case: "userMessage", value: { text: "写 hello" } }),
  ev(2, { case: "toolCall", value: { callId: "c1", kind: "edit", summary: "hello.txt" } }),
  ev(3, { case: "toolResult", value: { callId: "c1", summary: "wrote hello.txt" } }),
  ev(4, { case: "questionAsked", value: { question: { questionId: "q1", items: [{ id: "i", text: "哪个？" }] } } }),
  ev(5, { case: "questionResolved", value: { questionId: "q1", resolution: QuestionResolution.ANSWERED, by: "owner", answers: [{ itemId: "i", selected: ["红"] }] } }),
  ev(6, { case: "toolCall", value: { callId: "c2", kind: "shell", summary: "sleep 1800" } }),
];

describe("transcript", () => {
  test("messages between the session and the owner's agents, and who asked for each turn", () => {
    const items = transcript(
      [
        ev(0, { case: "turnStarted", value: { turn: 1, asker: "c_bot" } }),
        ev(1, { case: "messageReceived", value: { messageId: "msg_1", from: "c_helper", fromName: "helper", text: "check staging", delivered: "queued" } }),
        ev(2, { case: "messageSent", value: { messageId: "msg_2", to: "c_bot", toName: "bot", text: "halfway" } }),
      ],
      false,
    );
    expect(items[0].kind === "turn" && items[0].asker).toBe("c_bot");
    expect(items[1]).toMatchObject({ kind: "message", direction: "in", name: "helper", address: "c_helper", text: "check staging", delivered: "queued" });
    expect(items[2]).toMatchObject({ kind: "message", direction: "out", name: "bot", address: "c_bot", text: "halfway" });
  });
  test("pairs calls with results and questions with answers, without status changes", () => {
    const items = transcript(conversation(), true);
    expect(items.map((i) => i.kind)).toEqual(["turn", "user", "tool", "question", "tool"]);
    const [, , done, q, last] = items;
    expect(done.kind === "tool" && done.result?.summary).toBe("wrote hello.txt");
    expect(done.kind === "tool" && done.running).toBe(false);
    expect(last.kind === "tool" && last.running).toBe(true);
    expect(q.kind === "question" && q.resolution?.by).toBe("owner");
  });

  test("nothing runs once the turn is over", () => {
    const items = transcript(conversation(), false);
    expect(items.some((i) => i.kind === "tool" && i.running)).toBe(false);
  });

  test("when the latest turn started", () => {
    const events = [ev(0, { case: "turnStarted", value: { turn: 2 } }), ev(65, { case: "turnCompleted", value: { turn: 2 } })];
    expect(turnStartedAt(events)).toEqual(at(0));
    expect(turnStartedAt([])).toBeUndefined();
  });

  test("how a question was settled", () => {
    const r = (o: object) => create(QuestionResolvedSchema, o);
    expect(resolutionTitle(r({ resolution: QuestionResolution.ANSWERED, by: "owner", answers: [{ selected: ["红"] }, { text: "深一点" }] }))).toBe(
      "你回答：红；深一点",
    );
    expect(resolutionTitle(r({ resolution: QuestionResolution.DECLINED, by: "c1" }), () => "bot")).toBe("bot 跳过了回答");
    expect(resolutionTitle(r({ resolution: QuestionResolution.TIMED_OUT, by: "hermesh" }))).toBe("无人回答，Agent 已自行继续");
  });

  test("an interruption is not a failure", () => {
    expect(isInterruption({ kind: FailureKind.INTERRUPTED } as never)).toBe(true);
    expect(isInterruption({ kind: FailureKind.RATE_LIMIT } as never)).toBe(false);
    expect(isInterruption(undefined)).toBe(false);
  });
});
