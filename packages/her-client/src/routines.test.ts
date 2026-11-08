import assert from "node:assert/strict";
import { test } from "node:test";
import type { MailInfo, RoutineRunInfo, RoutineSummary } from "@hermesh/her-protocol";
import { botChat } from "./botChat.ts";
import { routineDot, routineGroups, routineLine, runLate, runRows, runTook } from "./routines.ts";
import { conversation, reportBody } from "./steps.ts";
import { fromSnapshot, type SessionView } from "./session.ts";

const routine = (over: Partial<RoutineSummary> = {}): RoutineSummary => ({ id: "r_1", name: "喝水", when: "每天 8 点", state: "active", ...over });
const run = (id: string, outcome: RoutineRunInfo["outcome"], over: Partial<RoutineRunInfo> = {}): RoutineRunInfo => ({ id, scheduled: "2026-11-05T01:00:00Z", started: "2026-11-05T01:00:01Z", outcome, ...over });

test("the clock's dot: what failed first, then what runs", () => {
  assert.equal(routineDot(undefined), null);
  assert.equal(routineDot([routine()]), null);
  assert.equal(routineDot([routine({ running: true })]), "running");
  const failed = routine({ last_run: { started: "2026-11-05T01:00:00Z", outcome: "failed" } });
  assert.equal(routineDot([routine({ running: true }), failed]), "failed");
});

test("the list's groups keep their order within", () => {
  const all = [routine({ id: "a", state: "paused" }), routine({ id: "b" }), routine({ id: "c", state: "finished" }), routine({ id: "d" })];
  const { active, paused, finished } = routineGroups(all);
  assert.deepEqual([active.map((r) => r.id), paused.map((r) => r.id), finished.map((r) => r.id)], [["b", "d"], ["a"], ["c"]]);
});

test("a row's second line says what matters most", () => {
  assert.deepEqual(routineLine(routine({ running: true, last_run: { started: "2026-11-05T01:00:00Z" } })), { kind: "running", since: new Date("2026-11-05T01:00:00Z") });
  assert.deepEqual(routineLine(routine({ running: true })), { kind: "running", since: null });
  assert.equal(routineLine(routine({ next_run: "2026-11-05T08:00:00Z", last_run: { started: "2026-11-05T01:00:00Z", outcome: "failed" } })).kind, "failed");
  assert.deepEqual(routineLine(routine({ next_run: "2026-11-05T08:00:00Z" })), { kind: "next", when: "每天 8 点", at: new Date("2026-11-05T08:00:00Z") });
  assert.deepEqual(routineLine(routine({ state: "paused" })), { kind: "when", when: "每天 8 点" });
});

test("three or more quiet runs in a row fold into one row, which can be opened", () => {
  const runs = [run("7", "reported"), run("6", "quiet"), run("5", "quiet"), run("4", "quiet"), run("3", "failed"), run("2", "quiet"), run("1", "quiet")];
  const rows = runRows(runs);
  assert.deepEqual(
    rows.map((r) => (r.kind === "run" ? r.run.id : `fold:${r.runs.map((x) => x.id).join("")}`)),
    ["7", "fold:654", "3", "2", "1"],
  );
  const opened = runRows(runs, new Set(["6"]));
  assert.deepEqual(opened.map((r) => (r.kind === "run" ? r.run.id : "fold")), ["7", "6", "5", "4", "3", "2", "1"]);
});

test("how long a run took and how late it was", () => {
  assert.equal(runTook(run("1", "quiet", { ended: "2026-11-05T01:00:11Z" })), 10_000);
  assert.equal(runTook(run("1", null)), null);
  assert.equal(runTook(run("1", null), new Date("2026-11-05T01:03:01Z")), 180_000, "one still running counts up to now");
  assert.equal(runTook(run("1", "quiet", { ended: "2026-11-05T01:00:11Z" }), new Date("2026-11-05T01:03:01Z")), 10_000, "one that ended stays as it was");
  assert.equal(runTook(run("1", "interrupted"), new Date("2026-11-05T01:03:01Z")), null, "an end nobody wrote down is not still running");
  assert.equal(runTook(run("1", "skipped", { started: undefined }), new Date("2026-11-05T01:03:01Z")), null);
  assert.equal(runLate(run("1", "quiet")), null);
  assert.equal(runLate(run("1", "quiet", { late: true, started: "2026-11-05T03:00:00Z" })), 2 * 3_600_000);
});

/** A bot's conversation with these transcript items. */
function chat(items: unknown[]): SessionView {
  return fromSnapshot(snapshotOf(items));
}

/** A session's snapshot with these transcript items. */
function snapshotOf(items: unknown[]) {
  return {
    session: { session_id: "s", cwd: "/", session_file: "/s.jsonl", model: "m", thinking: "off", permission_mode: "manual", context_window: 1, archived: false },
    last_seq: 0,
    transcript: { items: items as never },
    usage: { per_model: {} },
  } as Parameters<typeof fromSnapshot>[0];
}

const assistant = (ts: number, calls: { id: string; name: string; args: unknown }[]) => ({
  type: "message",
  message_id: `a${ts}`,
  message: { role: "assistant", content: calls.map((c) => ({ type: "tool_call", id: c.id, name: c.name, arguments: c.args })), provider: "p", model: "m", usage: { input: 0, output: 0 }, stop_reason: "tool_use", ts },
});
const result = (call: string, name: string, details?: unknown) => ({
  type: "tool_result",
  call_id: call,
  result: { call_id: call, tool_name: name, content: [{ type: "text", text: "ok" }], is_error: false, ts: 0, details },
});

const letter = "<routine_report routine=\"喝水\" id=\"r_1\" scheduled=\"Thu 2026-11-05 09:00\" ran=\"Thu 2026-11-05 09:00\">\n该喝水了。\n</routine_report>\nThis routine is the user's own request.";
const mail: MailInfo = { id: "m1", from: { id: "run_1", name: "喝水" }, kind: "routine", status: "completed", routine_id: "r_1" };

test("a change to a routine is a line in the chat; reading one is not", () => {
  const items = botChat(
    chat([
      { type: "message", message_id: "u1", message: { role: "user", content: [{ type: "text", text: "每天 8 点提醒我喝水" }], ts: 1 } },
      assistant(2, [{ id: "c0", name: "manage_routine", args: { action: "get", id: "r_1" } }]),
      result("c0", "manage_routine"),
      assistant(3, [{ id: "c1", name: "manage_routine", args: { action: "create" } }]),
      result("c1", "manage_routine", { action: "create", routine_id: "r_1", name: "喝水", when: "每天 8 点", next_run: "2026-11-06T00:00:00Z" }),
      assistant(4, [{ id: "c2", name: "send_to_user", args: { text: "好了。" } }]),
      result("c2", "send_to_user"),
    ]),
  );
  assert.deepEqual(items.map((i) => i.kind), ["user", "routine", "bot"]);
  const line = items[1] as Extract<(typeof items)[number], { kind: "routine" }>;
  assert.deepEqual([line.action, line.routine_id, line.name, line.when, line.next_run], ["create", "r_1", "喝水", "每天 8 点", "2026-11-06T00:00:00Z"]);
});

test("a deck_edit the bot made, through call_tool, shows in the chat, to take back", () => {
  const details = { deck_id: "d1", path: "/a.pptx", edit: 1, revision: 1, slides: [{ index: 0, slide_id: "slide:0:256", status: "changed" }] };
  const items = botChat(
    chat([
      { type: "message", message_id: "u1", message: { role: "user", content: [{ type: "text", text: "加英文" }], ts: 1 } },
      assistant(2, [{ id: "c1", name: "call_tool", args: { name: "deck_edit", arguments: { deck: "/a.pptx", edits: [] } } }]),
      result("c1", "call_tool", details),
      assistant(3, [{ id: "c2", name: "send_to_user", args: { text: "好了。" } }]),
      result("c2", "send_to_user"),
    ]),
  );
  assert.deepEqual(items.map((i) => i.kind), ["user", "deck_edit", "bot"]);
  assert.deepEqual((items[1] as Extract<(typeof items)[number], { kind: "deck_edit" }>).details, details);
});

test("a deck_edit a sub-agent made shows in the bot's chat, once, when it was made", () => {
  const details = { deck_id: "d1", path: "/a.pptx", edit: 2, revision: 2, slides: [{ index: 5, slide_id: "slide:5:261", status: "changed" }] };
  const base = chat([
    { type: "message", message_id: "u1", message: { role: "user", content: [{ type: "text", text: "美化一下第六页" }], ts: 1 } },
    assistant(2, [{ id: "s1", name: "spawn_agent", args: { name: "agent-1", task: "美化第六页" } }]),
    result("s1", "spawn_agent"),
    assistant(9, [{ id: "c9", name: "send_to_user", args: { text: "提案提上去了。" } }]),
    result("c9", "send_to_user"),
  ]);
  const proposed = { ...result("e1", "call_tool", details), result: { ...result("e1", "call_tool", details).result, ts: 5 } };
  const agentTranscript = [assistant(4, [{ id: "e1", name: "call_tool", args: { name: "deck_edit", arguments: { deck: "/a.pptx", edits: [] } } }]), proposed];
  const agent = fromSnapshot(snapshotOf(agentTranscript));
  const v: SessionView = { ...base, agents: [{ id: "g1", name: "agent-1", description: "", call_id: "s1", status: "completed", since: 3, view: agent }] };
  const items = botChat(v);
  assert.deepEqual(items.map((i) => i.kind), ["user", "deck_edit", "bot"]);
  assert.deepEqual((items[1] as Extract<(typeof items)[number], { kind: "deck_edit" }>).details, details);
});

test("a message a routine's report brought knows its run; the next one the owner asked for does not", () => {
  const v = chat([
    { type: "message", message_id: "l1", mail, message: { role: "user", content: [{ type: "text", text: letter }], ts: 1000 } },
    assistant(2000, [{ id: "c1", name: "send_to_user", args: { text: "该喝水了。" } }]),
    result("c1", "send_to_user"),
    { type: "task_end", task_id: "t1", outcome: "completed", reason: "done", ts: 2100 },
    { type: "message", message_id: "u1", message: { role: "user", content: [{ type: "text", text: "好" }], ts: 3000 } },
    assistant(4000, [{ id: "c2", name: "send_to_user", args: { text: "嗯。" } }]),
    result("c2", "send_to_user"),
  ]);
  const bubbles = botChat(v).flatMap((i) => (i.kind === "bot" ? [i] : []));
  assert.deepEqual(bubbles[0].source, { routine_id: "r_1", run_id: "run_1", name: "喝水", at: 1000 });
  assert.equal(bubbles[1].source, undefined);
  // The letter is a step of the conversation, in its own words.
  const report = conversation(v).find((s) => s.kind === "report");
  assert.equal(report?.kind === "report" && report.text, "该喝水了。");
  assert.deepEqual(report?.kind === "report" && report.routine, { id: "r_1", run: "run_1" });
});

test("a letter's own words, either kind", () => {
  assert.equal(reportBody(letter), "该喝水了。");
  assert.equal(reportBody("<agent_report agent=\"a\" status=\"completed\">\n3 files\n</agent_report>"), "3 files");
});
