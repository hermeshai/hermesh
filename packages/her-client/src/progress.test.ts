import assert from "node:assert/strict";
import { test } from "node:test";
import type { AssistantContent, Event, EventMsg, SessionInfo, Usage, UsageTotals } from "@hermesh/her-protocol";
import { agentCalls, agentElapsed, agentPost, agentState, agentTitles, agentTokens, findAgentTask, progressGroups, stepsOf } from "./progress.ts";
import { agentHistoryCursor, applyEvents, fromSnapshot, withAgentHistory, type SessionView } from "./session.ts";

// The Progress page's groups and cards (docs/her-team.md §15): what the owner said, and under it the
// sub-agents and coding sessions the bot started for it, each as it stands now.

const info: SessionInfo = { session_id: "s", cwd: "/w", session_file: "/f.jsonl", model: "p/m", thinking: "off", permission_mode: "manual", context_window: 1000, archived: false };
const usage: Usage = { input: 100, output: 20, cache_read: 0, cache_write: 0, cost: { currency: "USD", input: 0, output: 0, cache_read: 0, cache_write: 0, total: 0 } };
const totals: UsageTotals = { per_model: { "p/m": usage } };

let seq = 0;
function play(msgs: EventMsg[], from?: SessionView): SessionView {
  const events = msgs.map((m) => ({ seq: ++seq, ts: 1000 * seq, ...m }) as Event);
  return applyEvents(from ?? fromSnapshot({ session: info, last_seq: 0, transcript: { items: [] }, usage: { per_model: {} } }), events);
}

const user = (id: string, text: string, ts = 1000 * (seq + 1)): EventMsg => ({ type: "message_completed", message_id: id, message: { role: "user", content: [{ type: "text", text }], ts } });
const assistant = (id: string, content: AssistantContent[]): EventMsg => ({
  type: "message_completed",
  message_id: id,
  message: { role: "assistant", content, provider: "p", api: "a", model: "m", usage, stop_reason: "stop", ts: 1 },
});
const spawn = (id: string, name: string, description: string, task = `Do ${name}.`): AssistantContent => ({ type: "tool_call", id, name: "spawn_agent", arguments: { name, description, task } });
const spawned = (call: string, agent: string, name: string, description: string): EventMsg[] => [
  { type: "agent_started", agent_id: agent, name, description, session_file: `/${agent}.jsonl`, call_id: call },
  {
    type: "tool_completed",
    call_id: call,
    result: { call_id: call, tool_name: "spawn_agent", content: [{ type: "text", text: "started" }], is_error: false, ts: 2, details: { agent_id: agent, name, background: true, reported: false } },
  },
];
const say = (id: string, text: string): [AssistantContent, EventMsg] => [
  { type: "tool_call", id, name: "send_to_user", arguments: { text } },
  { type: "tool_completed", call_id: id, result: { call_id: id, tool_name: "send_to_user", content: [{ type: "text", text: "sent" }], is_error: false, ts: 2 } },
];
const startSession = (id: string, session: string, title: string): [AssistantContent, EventMsg] => [
  { type: "tool_call", id, name: "call_tool", arguments: { name: "session_start", arguments: { prompt: "fix it" } } },
  {
    type: "tool_completed",
    call_id: id,
    result: { call_id: id, tool_name: "call_tool", content: [{ type: "text", text: "{}" }], is_error: false, ts: 2, details: { session: { session_id: session, title, agent: "claude", machine: "MacBook" } } },
  },
];
const inner = (agent: string, n: number, msg: EventMsg): EventMsg => ({ type: "agent_event", agent_id: agent, event: { seq: n, ts: 1000 * (seq + 1), ...msg } as Event });
const done = (task: string): EventMsg => ({ type: "task_completed", task_id: task, usage: totals, reason: "done" });

test("each thing the owner said heads the work the bot sent out for it; what sent nothing out, or a coding session only, is no group", () => {
  const [told, toldDone] = say("c9", "分两路去办。");
  const [start, started] = startSession("c3", "S1", "修测试");
  const v = play([
    { type: "task_started", task_id: "t1" },
    user("u1", "准备周报"),
    assistant("a1", [told, spawn("c1", "data", "整理数据"), spawn("c2", "rivals", "看竞品")]),
    toldDone,
    ...spawned("c1", "ag1", "data", "整理数据"),
    ...spawned("c2", "ag2", "rivals", "看竞品"),
    done("t1"),
    { type: "task_started", task_id: "t2" },
    user("u2", "今天天气怎么样"),
    assistant("a2", [say("c8", "晴。")[0]]),
    say("c8", "晴。")[1],
    done("t2"),
    { type: "task_started", task_id: "t3" },
    user("u3", "把 relay 的测试修一下"),
    assistant("a3", [start]),
    started,
    done("t3"),
  ]);
  const groups = progressGroups(v);
  assert.deepEqual(groups.map((g) => [g.parent.kind === "user" ? g.parent.text : g.parent.kind, g.said, g.tasks.map((t) => t.kind)]), [
    ["准备周报", "分两路去办。", ["agent", "agent"]],
  ]);
  assert.deepEqual([...agentTitles(groups[0].tasks).values()], ["整理数据", "看竞品"]);
  assert.equal(findAgentTask(groups, "ag2")?.task.key, "c2");
  assert.equal(findAgentTask(groups, "c1")?.group.key, "u1");
  // The same view gives the same steps: they are worked out once.
  assert.equal(stepsOf(v), stepsOf(v));
});

test("a post says how its agent stands: starting, at work on a call, asking, done with its report's start, failed", () => {
  let v = play([{ type: "task_started", task_id: "t1" }, user("u1", "go"), assistant("a1", [spawn("c1", "data", "整理数据")])]);
  const launch = () => {
    const t = progressGroups(v)[0].tasks[0];
    assert.ok(t.kind === "agent");
    return t.launch;
  };
  assert.equal(agentState(launch()), "starting");
  assert.deepEqual(agentPost(launch()), { words: null, now: null, calls: 0, files: [] });

  v = play(
    [
      ...spawned("c1", "ag1", "data", "整理数据"),
      inner("ag1", 1, { type: "task_started", task_id: "x" }),
      inner("ag1", 2, user("x1", "<task>\nDo data.\n</task>", 10_000)),
      inner("ag1", 3, assistant("x2", [{ type: "thinking", text: "先看表头。\n再算。" }, { type: "tool_call", id: "k1", name: "read", arguments: { path: "/w/a.csv" } }])),
      inner("ag1", 4, { type: "tool_started", call_id: "k1", name: "read", args: { path: "/w/a.csv" }, category: "read", title: "read a.csv" }),
      inner("ag1", 5, { type: "turn_completed", task_id: "x", turn: 1, usage }),
    ],
    v,
  );
  assert.equal(agentState(launch()), "running");
  // Its latest thought, and the call it is on.
  const post = agentPost(launch());
  assert.ok(post.now?.title === "read a.csv" && post.now.state === "pending");
  assert.equal(post.words, "先看表头。");
  // Its running task's turns count already.
  assert.equal(agentTokens(launch().agent!), 120);
  assert.equal(agentElapsed(launch().agent!, launch().agent!.since + 4200), 4200);
  assert.equal(agentCalls(launch().agent!), 1);

  v = play([inner("ag1", 6, { type: "approval_requested", call_id: "k1", name: "read", args: {}, summary: "读取 a.csv\n在项目外" })], v);
  assert.equal(agentState(launch()), "waiting");
  assert.equal(agentPost(launch()).words, "读取 a.csv");

  v = play(
    [
      inner("ag1", 7, { type: "approval_resolved", call_id: "k1", decision: { type: "approve" } }),
      inner("ag1", 8, { type: "tool_completed", call_id: "k1", result: { call_id: "k1", tool_name: "read", content: [{ type: "text", text: "wau" }], is_error: false, ts: 3 } }),
      inner("ag1", 9, assistant("x3", [{ type: "text", text: "**周活** 多了 12%。\n表在附件里。" }])),
      inner("ag1", 10, { type: "task_completed", task_id: "x", usage: totals, reason: "done", ts: 40_000 } as EventMsg),
      { type: "agent_status", agent_id: "ag1", name: "data", status: "completed", summary: "**周活** 多了 12%。", usage: totals, elapsed_ms: 30_000 },
    ],
    v,
  );
  assert.equal(agentState(launch()), "done");
  assert.deepEqual(agentPost(launch()), { words: "周活 多了 12%。", now: null, calls: 1, files: [] });
  assert.equal(agentElapsed(launch().agent!, 0), 30_000);
  assert.equal(agentTokens(launch().agent!), 120);

  v = play([{ type: "agent_status", agent_id: "ag1", name: "data", status: "failed", error: { kind: "provider", message: "模型没有回应" } as never }], v);
  assert.equal(agentState(launch()), "failed");
  assert.equal(agentPost(launch()).words, "模型没有回应");
});

test("a post names the files its agent wrote, once each", () => {
  const done = (id: string, error = false) => ({ type: "tool_completed", call_id: id, result: { call_id: id, tool_name: "edit", content: [{ type: "text", text: "ok" }], is_error: error, ts: 3 } }) as EventMsg;
  const v = play([
    { type: "task_started", task_id: "t1" },
    user("u1", "go"),
    assistant("a1", [spawn("c1", "w", "写周报")]),
    ...spawned("c1", "ag1", "w", "写周报"),
    inner("ag1", 1, { type: "task_started", task_id: "x" }),
    inner("ag1", 2, user("x1", "<task>\nWrite it.\n</task>", 10_000)),
    inner("ag1", 3, assistant("x2", [
      { type: "tool_call", id: "k1", name: "write", arguments: { path: "out/周报.md" } },
      { type: "tool_call", id: "k2", name: "edit", arguments: { path: "out/周报.md" } },
      { type: "tool_call", id: "k3", name: "edit", arguments: { path: "b.txt" } },
      { type: "tool_call", id: "k4", name: "read", arguments: { path: "c.txt" } },
    ])),
    inner("ag1", 4, done("k1")),
    inner("ag1", 5, done("k2")),
    inner("ag1", 6, done("k3", true)),
    inner("ag1", 7, done("k4")),
  ]);
  const t = progressGroups(v)[0].tasks[0];
  assert.ok(t.kind === "agent");
  // What failed wrote nothing; a read is no file of its.
  const post = agentPost(t.launch);
  assert.deepEqual([post.files, post.calls], [["out/周报.md"], 4]);
});

test("agents whose spawn calls are on an earlier page of the history are a group of their own, first", () => {
  const agent = { agent_id: "ag0", name: "old", description: "旧的活", call_id: "c0", session_file: "/ag0.jsonl", status: "completed" as const, since: 5000, transcript: { items: [], before: "ag0/i4" } };
  const v = play(
    [{ type: "task_started", task_id: "t1" }, user("u1", "go"), assistant("a1", [spawn("c1", "data", "整理数据")]), ...spawned("c1", "ag1", "data", "整理数据")],
    fromSnapshot({ session: info, last_seq: 0, transcript: { items: [], before: "i7" }, usage: { per_model: {} }, agents: [agent] }),
  );
  const groups = progressGroups(v);
  assert.deepEqual(groups.map((g) => [g.parent.kind, g.tasks.map((t) => t.key)]), [["earlier", ["c0"]], ["user", ["c1"]]]);

  // Its earlier history is read with its own cursor, and put before what the view has of it.
  const old = v.agents[0];
  assert.equal(agentHistoryCursor(old), "ag0/i4");
  assert.equal(agentHistoryCursor({ ...old, view: { ...old.view, before: "i4" } }), undefined, "an older her's cursor is the main session's kind: not read");
  const item = { type: "message" as const, message_id: "m0", message: { role: "user" as const, content: [{ type: "text" as const, text: "old task" }], ts: 1 } };
  const more = withAgentHistory(v, "ag0", { items: [item] });
  assert.equal(more.agents[0].view.entries.length, 1);
  assert.equal(more.agents[0].view.before, undefined);
  assert.equal(more.agents[1], v.agents[1], "another agent stays the same object");
});

test("a routine's run heads what the bot starts when its report wakes it", () => {
  const v = play([
    { type: "task_started", task_id: "t1" },
    user("u1", "hi"),
    done("t1"),
    { type: "task_started", task_id: "t2", trigger: "mail" } as EventMsg,
    {
      type: "message_completed",
      message_id: "r1",
      message: { role: "user", content: [{ type: "text", text: '<routine_report routine="晨报">\n该发晨报了\n</routine_report>' }], ts: 9000 },
      mail: { id: "m1", from: { id: "run1", name: "晨报" }, kind: "routine", status: "completed", routine_id: "rt1" },
    },
    assistant("a1", [spawn("c1", "news", "收集新闻")]),
    ...spawned("c1", "ag1", "news", "收集新闻"),
  ]);
  const groups = progressGroups(v);
  assert.equal(groups.length, 1);
  assert.deepEqual(groups[0].parent, { kind: "routine", name: "晨报", ts: 9000 });
});
