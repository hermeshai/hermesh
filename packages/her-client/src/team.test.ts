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
        { id: "c5", name: "session_send", args: { session_id: "s_ab_12" } },
        { id: "c6", name: "session_start", args: {} },
      ]),
      result("c3", "session_start", { text: doc.replace("s_ab_12", "s_cd_34") }),
      result("c4", "session_start", { error: true, text: '{"ok":false,"exit_code":3,"error":{"kind":"denied"}}' }),
      result("c5", "session_send", { text: doc }),
    ]),
  );
  // One card a session started: none for a call that failed, wrote to a session, or never ran.
  assert.deepEqual(items.map((i) => i.key), ["u1000", "c1", "c2", "c3"]);
  assert.deepEqual(items[2], { kind: "session", key: "c2", started: { session_id: "s_ab_12", title: "Fix the relay test", agent: "claude", machine: "Mac mini" }, ts: 2500 });
  assert.equal((items[3] as Extract<(typeof items)[number], { kind: "session" }>).started.session_id, "s_cd_34");
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
  assert.deepEqual(items.map((i) => i.key), ["c1"]);
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
