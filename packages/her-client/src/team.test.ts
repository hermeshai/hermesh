import assert from "node:assert/strict";
import { test } from "node:test";
import { botChat } from "./botChat.ts";
import { fromSnapshot, type SessionView } from "./session.ts";

// Her's cards (docs/her-team.md §4.4, §6.3, §14.3), by the same rules as HermeshKit's ChatTests.

function chat(items: unknown[]): SessionView {
  return fromSnapshot({
    session: { session_id: "s", cwd: "/", session_file: "/s.jsonl", model: "m", thinking: "off", permission_mode: "manual", context_window: 1, archived: false },
    last_seq: 0,
    transcript: { items: items as never },
    usage: { per_model: {} },
  } as Parameters<typeof fromSnapshot>[0]);
}
const user = (ts: number, text: string) => ({ type: "message", message_id: `u${ts}`, message: { role: "user", content: [{ type: "text", text }], ts } });
const assistant = (ts: number, calls: { id: string; name: string; args: unknown }[], duration = 0) => ({
  type: "message",
  message_id: `a${ts}`,
  message: { role: "assistant", content: calls.map((c) => ({ type: "tool_call", id: c.id, name: c.name, arguments: c.args })), provider: "p", model: "m", usage: { input: 0, output: 0 }, stop_reason: "tool_use", ts, duration_ms: duration },
});
const result = (call: string, name: string, o: { details?: unknown; text?: string; error?: boolean } = {}) => ({
  type: "tool_result",
  call_id: call,
  result: { call_id: call, tool_name: name, content: [{ type: "text", text: o.text ?? "ok" }], is_error: o.error ?? false, ts: 0, details: o.details },
});
const ended = (task: string, ts: number) => ({ type: "task_end", task_id: task, outcome: "completed", ts });
const doc = '{"ok":true,"exit_code":0,"error":null,"session":{"session_id":"s_ab_12","title":"Fix the relay test","machine":"Mac mini","agent":"claude"}}';

test("a session Her started is a card where she started it", () => {
  const items = botChat(
    chat([
      user(1000, "fix the relay test"),
      assistant(2000, [{ id: "c1", name: "send_to_user", args: { text: "Starting Claude Code." } }, { id: "c2", name: "call_tool", args: { name: "session_start", arguments: { project: "mini/hermesh" } } }], 500),
      result("c1", "send_to_user"),
      result("c2", "call_tool", { text: doc }),
      assistant(3000, [
        { id: "c3", name: "session_start", args: { project: "mini/hermesh" } },
        { id: "c4", name: "session_start", args: {} },
        { id: "c6", name: "session_start", args: {} },
      ]),
      result("c3", "session_start", { text: doc.replace("s_ab_12", "s_cd_34") }),
      result("c4", "session_start", { error: true, text: '{"ok":false,"exit_code":3,"error":{"kind":"denied"}}' }),
    ]),
  );
  // One card a session started: none for a call that failed or never ran.
  assert.deepEqual(items.map((i) => i.key), ["u1000", "c1", "session:s_ab_12", "session:s_cd_34"]);
  assert.deepEqual(items[2], { kind: "session", key: "session:s_ab_12", started: { session_id: "s_ab_12", title: "Fix the relay test", agent: "claude", machine: "Mac mini" }, ts: 2500 });
  assert.equal((items[3] as Extract<(typeof items)[number], { kind: "session" }>).started.session_id, "s_cd_34");
});

// More asked of a session days later (§4.4): its one card comes down to where she asked, and the earlier one goes.
// Named as it was named before; a call that failed moves nothing, and one that names no session it can be named as
// makes no card.
test("a session's card follows what she asks of it", () => {
  const items = botChat(
    chat([
      user(1000, "fix the relay test"),
      assistant(2000, [{ id: "c1", name: "session_start", args: { project: "mini/hermesh" } }]),
      result("c1", "session_start", { text: doc }),
      ended("t1", 2100),
      user(900_000_000, "also the flaky one"),
      assistant(900_001_000, [
        { id: "c2", name: "send_to_user", args: { text: "Asking it." } },
        { id: "c3", name: "call_tool", args: { name: "session_send", arguments: { session_id: "s_ab_12", message: "and the flaky one" } } },
        { id: "c4", name: "session_send", args: { session_id: "s_ab_12", message: "again" } },
        { id: "c5", name: "session_answer", args: { session_id: "s_zz_99", text: "yes" } },
      ], 300),
      result("c2", "send_to_user"),
      result("c3", "call_tool", { text: '{"ok":true,"message":{"id":"m1","delivered":"queued"}}' }),
      result("c4", "session_send", { error: true, text: '{"ok":false}' }),
      result("c5", "session_answer", { text: '{"ok":true}' }),
      ended("t2", 900_001_400),
    ]),
  );
  // One card, under what she said when she asked.
  assert.deepEqual(items.map((i) => i.key), ["u1000", "u900000000", "c2", "session:s_ab_12"]);
  assert.deepEqual(items[3], { kind: "session", key: "session:s_ab_12", started: { session_id: "s_ab_12", title: "Fix the relay test", agent: "claude", machine: "Mac mini" }, ts: 900_001_300 });
});

// News of a session wakes her (docs/her-mesh.md §6.2): what she then tells the owner has the session's card under
// it, named from the news when nothing else named it (her history holds the last page only). News she tells nothing
// of moves nothing, and the next task's words have no card under them.
test("a session's card follows what she says of its news", () => {
  const news = (id: string, session: string, ts: number) => ({
    type: "message",
    message_id: id,
    mail: { id, kind: "news", from: { id: session, name: "Fix" }, status: "completed" },
    message: { role: "user", ts, content: [{ type: "text", text: `<session_news id="${id}" kind="turn_done" session="${session}" title="Fix \\"the\\" test" agent="codex" project="macbook/hermesh" sent="x">\n<result>\nok\n</result>\n</session_news>\nNews of a coding session you started: its turn ended.` }] },
  });
  const items = botChat(
    chat([
      news("n1", "s_ab_12", 1000),
      assistant(2000, [{ id: "c1", name: "send_to_user", args: { text: "Done." } }], 100),
      result("c1", "send_to_user"),
      assistant(3000, [{ id: "c2", name: "send_to_user", args: { text: "Two files." } }], 100),
      result("c2", "send_to_user"),
      ended("t1", 3200),
      news("n2", "s_cd_34", 4000),
      ended("t2", 4100),
      user(5000, "thanks"),
      assistant(6000, [{ id: "c3", name: "send_to_user", args: { text: "You're welcome." } }]),
      result("c3", "send_to_user"),
    ]),
  );
  // Under the last she said of it.
  assert.deepEqual(items.map((i) => i.key), ["c1", "c2", "session:s_ab_12", "u5000", "c3"]);
  assert.deepEqual(items[2], { kind: "session", key: "session:s_ab_12", started: { session_id: "s_ab_12", title: 'Fix "the" test', agent: "codex", machine: "macbook" }, ts: 3100 });
});

test("the card comes from the result's details first; details without a session id make none", () => {
  const details = { session: { session_id: "s_ef_56", title: "Fix the relay test", agent: "codex", machine: "Mac mini", machine_id: "m_ef" } };
  const items = botChat(
    chat([
      assistant(2000, [{ id: "c1", name: "session_start", args: {} }, { id: "c2", name: "session_start", args: {} }]),
      // A long result is cut: the text is no document, the details still name the session.
      result("c1", "session_start", { details, text: '{"ok":true,"session":{"session_id":"s_ef_56","tit' }),
      result("c2", "session_start", { details: { session: { title: "no id" } }, text: doc }),
    ]),
  );
  assert.deepEqual(items.map((i) => i.key), ["session:s_ef_56"]);
  assert.deepEqual((items[0] as Extract<(typeof items)[number], { kind: "session" }>).started, { session_id: "s_ef_56", title: "Fix the relay test", agent: "codex", machine: "Mac mini" });
});

test("the team's work Her counted is a card", () => {
  const work = {
    days: 1, sessions: 5, done: 3, turns: 9, failed_turns: 1, input_tokens: 1000, output_tokens: 200, buckets: [0, 2, 3],
    by_agent: [{ agent: "claude", sessions: 4, turns: 8 }, { agent: "codex", sessions: 1, turns: 1 }],
    by_client: [{ client: "c_her", name: "", you: true, sessions: 3, turns: 6 }, { client: "owner", sessions: 2, turns: 3 }],
  };
  const items = botChat(
    chat([
      assistant(2000, [
        { id: "c1", name: "call_tool", args: { name: "work_summary", arguments: {} } },
        { id: "c2", name: "work_summary", args: {} },
        { id: "c3", name: "work_summary", args: {} },
      ], 100),
      result("c1", "call_tool", { details: { work } }),
      result("c2", "work_summary", { error: true, text: '{"ok":false}' }),
      result("c3", "work_summary", { details: { work: { by_agent: [] } } }),
    ]),
  );
  assert.deepEqual(items, [{
    kind: "work", key: "c1", ts: 2100,
    work: { days: 1, sessions: 5, done: 3, turns: 9, failed_turns: 1, tokens: 1200, buckets: [0, 2, 3],
      agents: [{ id: "claude", sessions: 4, turns: 8 }, { id: "codex", sessions: 1, turns: 1 }],
      senders: [{ id: "c_her", sessions: 3, turns: 6 }, { id: "owner", sessions: 2, turns: 3 }] },
  }]);
});
