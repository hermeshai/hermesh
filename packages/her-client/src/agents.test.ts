import assert from "node:assert/strict";
import { test } from "node:test";
import type { AssistantContent, Event, EventMsg, SessionInfo, SessionSnapshot, Usage, UsageTotals } from "@hermesh/her-protocol";
import { applyEvents, fromSnapshot, pendingApproval, runningAgents, statusOf, type SessionView } from "./session.ts";
import { agentBrief, agentTaskOf, conversation, messageBody, reportBody, taskGists } from "./steps.ts";
import { agentTask } from "./mock/agents.ts";

// Sub-agents as the window shows them: what spawn_agent started, their own
// work as agent_event tells it, their reports, and where the work as a whole
// ends.

const info: SessionInfo = { session_id: "s", cwd: "/w", session_file: "/f.jsonl", model: "p/m", thinking: "off", permission_mode: "manual", context_window: 1000, archived: false };
const usage: Usage = { input: 10, output: 5, cache_read: 0, cache_write: 0, cost: { currency: "USD", input: 0, output: 0, cache_read: 0, cache_write: 0, total: 0 } };
const totals: UsageTotals = { per_model: { "p/m": usage } };

let seq = 0;
function play(msgs: EventMsg[], from?: SessionView): SessionView {
  const events = msgs.map((m) => ({ seq: ++seq, ts: 1000 * seq, ...m }) as Event);
  return applyEvents(from ?? fromSnapshot({ session: info, last_seq: 0, transcript: { items: [] }, usage: { per_model: {} } }), events);
}

const user = (id: string, text: string): EventMsg => ({ type: "message_completed", message_id: id, message: { role: "user", content: [{ type: "text", text }], ts: 1000 * (seq + 1) } });
const assistant = (id: string, content: AssistantContent[]): EventMsg => ({
  type: "message_completed",
  message_id: id,
  message: { role: "assistant", content, provider: "p", api: "a", model: "m", usage, stop_reason: "stop", ts: 1 },
});
const spawnCall = (id: string, name: string): AssistantContent => ({ type: "tool_call", id, name: "spawn_agent", arguments: { name, task: `Test ${name}.\nMore detail.` } });
const spawned = (call: string, agent: string, name: string): EventMsg[] => [
  { type: "agent_started", agent_id: agent, name, session_file: `/${agent}.jsonl`, call_id: call },
  {
    type: "tool_completed",
    call_id: call,
    result: { call_id: call, tool_name: "spawn_agent", content: [{ type: "text", text: "started" }], is_error: false, ts: 2, details: { agent_id: agent, name, background: true, reported: false } },
  },
];
const sendCall = (id: string, to: string): AssistantContent => ({ type: "tool_call", id, name: "send_message", arguments: { to, message: "Go on." } });
const sent = (call: string, agent: string, name: string): EventMsg => ({
  type: "tool_completed",
  call_id: call,
  result: { call_id: call, tool_name: "send_message", content: [{ type: "text", text: "sent" }], is_error: false, ts: 2, details: { agent_id: agent, name, background: true, reported: false } },
});
const report = (agent: string, name: string, body: string, status: "completed" | "interrupted" = "completed", no_reply = false): EventMsg => ({
  type: "message_completed",
  message_id: `mail-${agent}-${seq + 1}`,
  message: { role: "user", content: [{ type: "text", text: `<agent_report agent="${name}" status="${status}">\n${body}\n</agent_report>` }], ts: 1000 * (seq + 1) },
  mail: { id: `m-${agent}-${seq + 1}`, from: { id: agent, name }, kind: "report", status, ...(no_reply ? { no_reply } : {}) },
});
const note = (agent: string, name: string, body: string): EventMsg => ({
  type: "message_completed",
  message_id: `note-${agent}-${seq + 1}`,
  message: { role: "user", content: [{ type: "text", text: `<agent_message from="${name}">\n${body}\n</agent_message>` }], ts: 1000 * (seq + 1) },
  mail: { id: `n-${agent}-${seq + 1}`, from: { id: agent, name }, kind: "message", status: "running" },
});
const inner = (agent: string, n: number, msg: EventMsg): EventMsg => ({ type: "agent_event", agent_id: agent, event: { seq: n, ts: 1000 * (seq + 1), ...msg } as Event });
const done = (task: string): EventMsg => ({ type: "task_completed", task_id: task, usage: totals, reason: "done" });

test("a sub-agent's own session follows its agent_events, and its status its agent_status", () => {
  let v = play([{ type: "task_started", task_id: "t1" }, user("u1", "go"), assistant("a1", [spawnCall("c1", "math")]), ...spawned("c1", "ag1", "math")]);
  assert.deepEqual(runningAgents(v).map((a) => a.name), ["math"]);
  v = play(
    [
      inner("ag1", 1, { type: "task_started", task_id: "t1" }),
      inner("ag1", 2, user("x1", agentTask("math", "Test math.\nMore detail."))),
      inner("ag1", 3, assistant("x2", [{ type: "tool_call", id: "k1", name: "bash", arguments: { command: "python3 -m unittest" } }])),
      inner("ag1", 4, { type: "tool_started", call_id: "k1", name: "bash", args: { command: "python3 -m unittest" }, category: "execute", title: "python3 -m unittest" }),
      inner("ag1", 5, { type: "tool_running", call_id: "k1" }),
    ],
    v,
  );
  const a = v.agents[0];
  // Its work is its own conversation, not the main session's.
  assert.equal(v.entries.filter((e) => e.type === "message" && e.message.role === "user").length, 1);
  assert.deepEqual(conversation(a.view).map((s) => s.kind), ["user", "work"]);
  const work = conversation(a.view)[1];
  const step = work.kind === "work" ? work.items[0] : undefined;
  assert.ok(step?.kind === "tool" && step.tool.state === "running" && step.tool.title === "python3 -m unittest");
  // What her asked comes from the spawn call; the brief in its first message says the same.
  assert.equal(agentTaskOf(v, a), "Test math.\nMore detail.");
  const first = conversation(a.view)[0];
  assert.ok(first.kind === "user" && agentBrief(first.text) === "Test math.\nMore detail.");

  v = play([{ type: "agent_status", agent_id: "ag1", name: "math", status: "completed", summary: "**31** tests pass", usage: totals, elapsed_ms: 34_000 }], v);
  assert.equal(v.agents[0].status, "completed");
  assert.equal(v.agents[0].elapsed_ms, 34_000);
  assert.equal(runningAgents(v).length, 0);
});

test("spawn calls show as one card of agents; reports as reports, not as the owner's messages", () => {
  const v = play([
    { type: "task_started", task_id: "t1" },
    user("u1", "test the three modules"),
    assistant("a1", [{ type: "text", text: "Three agents." }, spawnCall("c1", "math"), spawnCall("c2", "text")]),
    ...spawned("c1", "ag1", "math"),
    ...spawned("c2", "ag2", "text"),
    assistant("a2", [{ type: "text", text: "They work." }]),
    done("t1"),
    { type: "task_started", task_id: "t2", trigger: "mail" },
    report("ag2", "text", "**34** tests pass"),
  ]);
  const steps = conversation(v);
  assert.deepEqual(steps.map((s) => s.kind), ["user", "text", "agents", "text", "report"]);
  const card = steps[2];
  assert.ok(card.kind === "agents");
  assert.deepEqual(card.launches.map((l) => [l.name, l.agent?.id]), [["math", "ag1"], ["text", "ag2"]]);
  const r = steps[4];
  assert.ok(r.kind === "report" && r.name === "text" && r.text === "**34** tests pass" && r.agent?.id === "ag2");
});

test("an agent goes by its label: the call's while it starts, agent_started's after; older calls have none", () => {
  const labelled: AssistantContent = { type: "tool_call", id: "c1", name: "spawn_agent", arguments: { description: "为 math 写测试", task: "Test math.", name: "math" } };
  let v = play([{ type: "task_started", task_id: "t1" }, user("u1", "go"), assistant("a1", [labelled, spawnCall("c2", "text")])]);
  const launches = () => {
    const card = conversation(v).find((x) => x.kind === "agents");
    assert.ok(card?.kind === "agents");
    return card.launches.map((l) => [l.description, l.agent?.id]);
  };
  assert.deepEqual(launches(), [["为 math 写测试", undefined], ["", undefined]]);
  v = play([{ type: "agent_started", agent_id: "ag1", name: "math", description: "为 math 写测试", session_file: "/ag1.jsonl", call_id: "c1" }], v);
  assert.equal(v.agents[0].description, "为 math 写测试");
  assert.equal(v.agents[0].view.info.title, "为 math 写测试");
  assert.deepEqual(launches(), [["为 math 写测试", "ag1"], ["", undefined]]);
});

test("an agent is starting until its call has a result; after that, only the session says how it is", () => {
  let v = play([{ type: "task_started", task_id: "t1" }, user("u1", "go"), assistant("a1", [spawnCall("c1", "math")])]);
  const launch = () => {
    const card = conversation(v).find((x) => x.kind === "agents");
    assert.ok(card?.kind === "agents");
    return card.launches[0];
  };
  assert.equal(launch().starting, true);
  v = play(spawned("c1", "ag1", "math"), v);
  assert.equal(launch().starting, false);
  assert.equal(launch().agent?.status, "running");
  // A session that no longer knows the agent leaves its status unknown rather than running.
  v = { ...v, agents: [] };
  assert.equal(launch().starting, false);
  assert.equal(launch().agent, undefined);
});

test("the work ends where the last report is answered: one end line for all of it", () => {
  let v = play([
    { type: "task_started", task_id: "t1" },
    user("u1", "test the modules"),
    assistant("a1", [spawnCall("c1", "math"), spawnCall("c2", "text")]),
    ...spawned("c1", "ag1", "math"),
    ...spawned("c2", "ag2", "text"),
    assistant("a2", [{ type: "text", text: "They work." }]),
    done("t1"),
  ]);
  // While they work, no end line: the background line says it.
  assert.ok(!conversation(v).some((s) => s.kind === "end"));

  v = play(
    [
      { type: "agent_status", agent_id: "ag2", name: "text", status: "completed", elapsed_ms: 1000 },
      { type: "task_started", task_id: "t2", trigger: "mail" },
      report("ag2", "text", "done"),
      assistant("a3", [{ type: "text", text: "text is done; waiting for math." }]),
      done("t2"),
    ],
    v,
  );
  assert.ok(!conversation(v).some((s) => s.kind === "end"), "math still works");

  v = play(
    [
      { type: "agent_status", agent_id: "ag1", name: "math", status: "completed", elapsed_ms: 2000 },
      { type: "task_started", task_id: "t3", trigger: "mail" },
      report("ag1", "math", "done"),
      assistant("a4", [{ type: "text", text: "All done." }]),
      done("t3"),
    ],
    v,
  );
  const ends = conversation(v).filter((s) => s.kind === "end");
  assert.equal(ends.length, 1);
  const end = ends[0];
  assert.ok(end.kind === "end" && end.chain);
  assert.equal(end.chain.agents, 2);
  assert.equal(end.chain.usage?.per_model["p/m"].input, 30, "three tasks' usage");
});

test("stopped agents hold the work open: their reports wait until her is asked to go on", () => {
  let v = play([
    { type: "task_started", task_id: "t1" },
    user("u1", "test the modules"),
    assistant("a1", [spawnCall("c1", "math"), spawnCall("c2", "text")]),
    ...spawned("c1", "ag1", "math"),
    ...spawned("c2", "ag2", "text"),
    assistant("a2", [{ type: "text", text: "They work." }]),
    done("t1"),
    { type: "agent_status", agent_id: "ag1", name: "math", status: "interrupted", elapsed_ms: 1000 },
    { type: "agent_status", agent_id: "ag2", name: "text", status: "interrupted", elapsed_ms: 1000 },
  ]);
  assert.ok(!conversation(v).some((s) => s.kind === "end"), "stopped is not done");

  v = play(
    [
      { type: "task_started", task_id: "t2" },
      user("u2", "go on"),
      report("ag1", "math", "half way", "interrupted"),
      report("ag2", "text", "(It had not replied yet.)", "interrupted", true),
      assistant("a3", [sendCall("c3", "math"), sendCall("c4", "text")]),
      sent("c3", "ag1", "math"),
      sent("c4", "ag2", "text"),
      { type: "agent_status", agent_id: "ag1", name: "math", status: "running" },
      { type: "agent_status", agent_id: "ag2", name: "text", status: "running" },
      assistant("a4", [{ type: "text", text: "They go on." }]),
      done("t2"),
    ],
    v,
  );
  assert.ok(!conversation(v).some((s) => s.kind === "end"), "they work again");

  v = play(
    [
      { type: "agent_status", agent_id: "ag1", name: "math", status: "completed", elapsed_ms: 2000 },
      { type: "agent_status", agent_id: "ag2", name: "text", status: "completed", elapsed_ms: 2000 },
      { type: "task_started", task_id: "t3", trigger: "mail" },
      report("ag1", "math", "done"),
      report("ag2", "text", "done"),
      assistant("a5", [{ type: "text", text: "All done." }]),
      done("t3"),
    ],
    v,
  );
  const ends = conversation(v).filter((s) => s.kind === "end");
  assert.equal(ends.length, 1);
  assert.ok(ends[0].kind === "end" && ends[0].chain?.agents === 2);
  const reports = conversation(v).flatMap((s) => (s.kind === "report" ? [s] : []));
  assert.deepEqual(reports.map((s) => s.status), ["interrupted", "interrupted", "completed", "completed"]);
  // One had not replied when stopped: its report says so only to the model, and the step knows.
  assert.deepEqual(reports.map((s) => s.no_reply), [false, true, false, false]);
});

test("tasks that open alike show the sentence that tells them apart", () => {
  const brief = "Write unit tests for the calc package.";
  assert.deepEqual(taskGists([`${brief}\n\nOnly mathutil: read calc/mathutil.py.`, `${brief}\n\nOnly textutil: read calc/textutil.py.`]), [
    "Only mathutil: read calc/mathutil.py.",
    "Only textutil: read calc/textutil.py.",
  ]);
  assert.deepEqual(taskGists(["Test math.\nMore detail.", "Test text.\nMore detail."]), ["Test math.", "Test text."]);
  assert.deepEqual(taskGists(["Same.", "Same."]), ["Same.", "Same."]);
  assert.deepEqual(taskGists(["Alone.\nMore."]), ["Alone."]);
  // One paragraph that differs only in a later sentence; Markdown marks go.
  const open = "You write tests for ONE module. Work only in /p (the cwd).";
  assert.deepEqual(taskGists([`${open} Touch nothing but tests/test_math.py.\n\n1. Read it.`, `${open} Touch nothing but tests/test_text.py.\n\n1. Read it.`]), [
    "Touch nothing but tests/test_math.py.",
    "Touch nothing but tests/test_text.py.",
  ]);
  assert.deepEqual(taskGists(["Task: test `calc/a.py` (read it first). Then run it.", "Task: test `calc/b.py` (read it first). Then run it."]), [
    "Task: test calc/a.py (read it first).",
    "Task: test calc/b.py (read it first).",
  ]);
  assert.deepEqual(taskGists(["在根目录为一个 Python 包写单元测试。只做 mathutil 部分。", "在根目录为一个 Python 包写单元测试。只做 textutil 部分。"]), ["只做 mathutil 部分。", "只做 textutil 部分。"]);
  // List marks go too; underscores in names stay.
  assert.deepEqual(taskGists(["- Add __init__.py to a.", "- Add __init__.py to b."]), ["Add __init__.py to a.", "Add __init__.py to b."]);
});

test("what a sub-agent sends on the way is a step of its own, and its round of work goes on", () => {
  let v = play([
    { type: "task_started", task_id: "t1" },
    user("u1", "sum the sales"),
    assistant("a1", [spawnCall("c1", "sales")]),
    ...spawned("c1", "ag1", "sales"),
    assistant("a2", [{ type: "text", text: "It works." }]),
    done("t1"),
    { type: "task_started", task_id: "t2", trigger: "mail" },
    note("ag1", "sales", "March is missing."),
    assistant("a3", [{ type: "text", text: "Heard." }]),
    done("t2"),
  ]);
  let steps = conversation(v);
  assert.deepEqual(steps.map((s) => s.kind), ["user", "agents", "text", "agent_message", "text"], "no end: the agent still works");
  const m = steps[3];
  assert.ok(m.kind === "agent_message" && m.name === "sales" && m.text === "March is missing." && m.agent?.id === "ag1");
  assert.deepEqual(runningAgents(v).map((a) => a.name), ["sales"]);

  v = play(
    [
      { type: "agent_status", agent_id: "ag1", name: "sales", status: "completed" },
      { type: "task_started", task_id: "t3", trigger: "mail" },
      report("ag1", "sales", "Summed."),
      assistant("a4", [{ type: "text", text: "Done." }]),
      done("t3"),
    ],
    v,
  );
  steps = conversation(v);
  assert.deepEqual(steps.map((s) => s.kind), ["user", "agents", "text", "agent_message", "text", "report", "text", "end"]);
  const end = steps.at(-1);
  assert.ok(end?.kind === "end" && end.chain?.agents === 1, "the report ended the piece of work");
});

test("what a peer sends shows in the agent's own conversation", () => {
  const v = play([
    { type: "task_started", task_id: "t1" },
    user("u1", "go"),
    assistant("a1", [spawnCall("c1", "caller")]),
    ...spawned("c1", "ag1", "caller"),
    inner("ag1", 1, { type: "task_started", task_id: "t1" }),
    inner("ag1", 2, user("x1", agentTask("caller", "Write the caller."))),
    inner("ag1", 3, note("ag2", "api", "The API is in api.rs.")),
  ]);
  const own = conversation(v.agents[0].view);
  const m = own.at(-1);
  assert.ok(m?.kind === "agent_message" && m.name === "api" && m.text === "The API is in api.rs.");
  assert.equal(v.agents[0].status, "running");
});

test("a message's own words come without the wrapper", () => {
  assert.equal(messageBody('<agent_message from="a">\nMarch is missing.\n\nGoing on.\n</agent_message>'), "March is missing.\n\nGoing on.");
  assert.equal(messageBody("plain"), "plain");
});

test("a report's own words come without the wrapper and the note after it", () => {
  assert.equal(reportBody('<agent_report agent="a" status="interrupted">\nhalf way\n</agent_report>\nIt keeps its work and context: send_message lets it continue where it stopped.'), "half way");
  assert.equal(reportBody("plain"), "plain");
  assert.equal(agentBrief("no task tags"), "no task tags");
});

test("what a sub-agent asks waits for the owner while the session itself is idle", () => {
  let v = play([{ type: "task_started", task_id: "t1" }, user("u1", "go"), assistant("a1", [spawnCall("c1", "math")]), ...spawned("c1", "ag1", "math"), done("t1")]);
  assert.equal(statusOf(v), "idle");
  assert.equal(pendingApproval(v), null);
  const asked = { call_id: "k1", name: "bash", args: { command: "rm -rf build" }, summary: "rm -rf build" };
  v = play(
    [
      inner("ag1", 1, { type: "task_started", task_id: "t1" }),
      inner("ag1", 2, { type: "tool_started", call_id: "k1", name: "bash", args: asked.args, category: "execute", title: "rm -rf build" }),
      inner("ag1", 3, { type: "approval_requested", ...asked }),
    ],
    v,
  );
  assert.equal(statusOf(v), "awaiting_approval");
  assert.equal(v.live, null);
  const waiting = pendingApproval(v);
  assert.equal(waiting?.approval.summary, "rm -rf build");
  assert.equal(waiting?.agent?.id, "ag1");
  // Its own work says so too.
  assert.equal(v.agents[0].view.live?.tools[0].phase, "awaiting_approval");

  v = play([inner("ag1", 4, { type: "approval_resolved", call_id: "k1", decision: { type: "approve" } })], v);
  assert.equal(pendingApproval(v), null);
  assert.equal(statusOf(v), "idle");
});

test("a snapshot taken while a sub-agent asks brings the question back", () => {
  const usage = { per_model: {} };
  const v = fromSnapshot({
    session: { session_id: "s", cwd: "/w", session_file: "/f", model: "m", thinking: "default", permission_mode: "manual", context_window: 1, archived: false },
    last_seq: 3,
    transcript: { items: [] },
    usage,
    agents: [
      {
        agent_id: "ag1",
        name: "math",
        description: "测数学",
        call_id: "c1",
        session_file: "/a",
        status: "running",
        since: 1,
        transcript: { items: [] },
        active_task: {
          task_id: "t1",
          turn: 0,
          tools: [{ call_id: "k1", name: "bash", category: "execute", title: "rm x", args: { command: "rm x" }, running: false }],
          approvals: [{ call_id: "k1", name: "bash", args: { command: "rm x" }, summary: "rm x" }],
        },
      },
    ],
  } as unknown as SessionSnapshot);
  assert.equal(statusOf(v), "awaiting_approval");
  assert.equal(pendingApproval(v)?.agent?.description, "测数学");
  assert.equal(v.agents[0].view.live?.tools[0].phase, "awaiting_approval");
});
