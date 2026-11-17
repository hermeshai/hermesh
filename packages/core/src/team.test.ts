import { create } from "@bufbuild/protobuf";
import { FailureKind, FailureSchema, QuestionItemSchema, QuestionSchema, SessionStatus, type Session } from "@hermesh/proto/session";
import { describe, expect, test } from "vitest";
import { sessionSections, sessionView } from "./sessions";
import { liveByAgent, quickChoice, teamState } from "./team";
import { ask, now, progressOf, session } from "./testing";

const ids = (list: Session[]) => list.map((s) => s.sessionId);

/** A session whose last turn failed. */
function failed(id: string, updated: number): Session {
  const s = session(id, SessionStatus.IDLE, { updated });
  progressOf(s).lastFailure = create(FailureSchema, { kind: FailureKind.CONTEXT_OVERFLOW });
  return s;
}

// The same table as HermeshKit's SessionsTests (docs/her-team.md §4.2), so both apps say the same.
describe("the team's state", () => {
  const waiting = (id: string, updated: number, agent = "codex") => {
    const s = session(id, SessionStatus.WAITING_INPUT, { updated, agent });
    ask(s);
    return s;
  };
  const running = (id: string, created: number, agent = "claude") => session(id, SessionStatus.RUNNING, { created, updated: created + 5, agent });

  test("what waits, what failed, what runs and what is done, each in its order; archived ones are no part of it", () => {
    const t = teamState(
      [
        waiting("w1", -50), waiting("w2", -10), failed("f1", -30), running("r1", -400), running("r2", -100),
        session("d1", SessionStatus.IDLE, { updated: -20 }), session("d2", SessionStatus.IDLE, { updated: -5 }), session("gone", SessionStatus.ARCHIVED),
      ],
      1,
    );
    expect([ids(t.waiting), ids(t.failed), ids(t.running), ids(t.finished)]).toEqual([["w2", "w1"], ["f1"], ["r2", "r1"], ["d2", "d1"]]);
    expect(t.requests).toBe(1);
  });

  test("the sessions at hand by coding agent: what waits, then what failed, then what runs; finished and archived ones none of it", () => {
    const t = teamState(
      [
        running("r1", -400, "claude"), running("r2", -100, "gemini"), waiting("w1", -50, "codex"), waiting("w2", -10, "claude"),
        failed("f1", -30), session("d1", SessionStatus.IDLE, { updated: -20, agent: "cursor" }), session("gone", SessionStatus.ARCHIVED, { agent: "kimi" }),
      ],
      0,
    );
    // failed() makes a "claude" session (the fixture's default agent).
    expect(liveByAgent(t).map((g) => [g.agent, ids(g.sessions)])).toEqual([["claude", ["w2", "f1", "r1"]], ["codex", ["w1"]], ["gemini", ["r2"]]]);
    expect(liveByAgent(teamState([session("d", SessionStatus.IDLE)], 0))).toEqual([]);
  });

  test("the name tag says the first that holds", () => {
    const tag = (sessions: Session[], requests = 0) => teamState(sessions, requests).tag;
    expect(tag([waiting("w", -10), running("r", -100), failed("f", -30)])).toEqual({ kind: "waiting", agent: "codex" });
    expect(tag([waiting("w", -10), waiting("x", -20)])).toEqual({ kind: "needYou", count: 2 });
    // A request to connect is something that waits, not a session's question.
    expect(tag([waiting("w", -10)], 1)).toEqual({ kind: "needYou", count: 2 });
    expect(tag([], 1)).toEqual({ kind: "needYou", count: 1 });
    expect(tag([failed("f", -30), running("r", -100)])).toEqual({ kind: "failed", count: 1 });
    expect(tag([running("r", -100), running("q", -50)])).toEqual({ kind: "working", count: 2 });
    expect(tag([session("d", SessionStatus.IDLE)])).toEqual({ kind: "quiet" });
  });

  test("the coding agents at work, each once", () => {
    const t = teamState([running("a", -300, "claude"), running("b", -200, "codex"), running("c", -100, "claude")], 0);
    expect(t.runningAgents).toEqual(["claude", "codex"]);
  });
});

describe("a question one click answers", () => {
  const question = (...items: Parameters<typeof create<typeof QuestionItemSchema>>[1][]) => create(QuestionSchema, { questionId: "q", items: items.map((i) => create(QuestionItemSchema, i)) });
  const options = [{ label: "Red" }, { label: "Blue" }];

  test("a single choice, with any other items optional", () => {
    expect(quickChoice(question({ id: "color", options }))?.id).toBe("color");
    // An optional note before the choice does not take the answer.
    expect(quickChoice(question({ id: "note", optional: true }, { id: "color", options }))?.id).toBe("color");
    // A single optional choice keeps its buttons.
    expect(quickChoice(question({ id: "color", options, optional: true }))?.id).toBe("color");
  });

  test("not free text, several choices, a secret, or two required items", () => {
    expect(quickChoice(question({ id: "text" }))).toBeUndefined();
    expect(quickChoice(question({ id: "color", options, multiSelect: true }))).toBeUndefined();
    expect(quickChoice(question({ id: "color", options, secret: true }))).toBeUndefined();
    expect(quickChoice(question({ id: "color", options }, { id: "size", options }))).toBeUndefined();
  });
});

describe("the list by who sent the work", () => {
  test("Her first, then the agents by their latest session, the owner last", () => {
    const all = [
      session("mine", SessionStatus.IDLE, { by: "owner", updated: -1 }),
      session("g1", SessionStatus.IDLE, { by: "c_grok", updated: -300 }),
      session("t1", SessionStatus.IDLE, { by: "c_chat", updated: -50 }),
      session("h1", SessionStatus.IDLE, { by: "c_her", updated: -900 }),
      session("g2", SessionStatus.RUNNING, { by: "c_grok", updated: -200 }),
      session("old", SessionStatus.ARCHIVED, { by: "c_grok" }),
    ];
    const secs = sessionSections(all, sessionView({ group: "sender" }), { archived: false, now, her: ["c_her"] });
    expect(secs.map((s) => [s.grouping, s.key])).toEqual([["sender", "c_her"], ["sender", "c_chat"], ["sender", "c_grok"], ["sender", "owner"]]);
    expect(ids(secs[2].sessions)).toEqual(["g2", "g1"]);
    // Without Her, the agents and the owner alone.
    expect(sessionSections(all, sessionView({ group: "sender" }), { archived: false, now }).map((s) => s.key)).toEqual(["c_chat", "c_grok", "c_her", "owner"]);
  });
});
