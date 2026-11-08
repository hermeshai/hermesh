import assert from "node:assert/strict";
import { test } from "node:test";
import type { Event, EventMsg, SessionInfo, TranscriptItem, UserMessage } from "@hermesh/her-protocol";
import { applyEvents, fromSnapshot } from "./session.ts";
import { attachedBlock, changesFilesAfter, conversation, handedTabBlock, handedTabsBlock, userText } from "./steps.ts";

// The conversation going back (`session_rewound`), as a window follows it.

const info: SessionInfo = { session_id: "s", cwd: "/w", session_file: "/f.jsonl", model: "p/m", thinking: "off", permission_mode: "manual", context_window: 1000, archived: false };
const usage = (input: number) => ({ input, output: 1, cache_read: 0, cache_write: 0, cost: { currency: "USD" as const, input: 0, output: 0, cache_read: 0, cache_write: 0, total: 0 } });
const said = (id: string, text: string): TranscriptItem => ({ type: "message", message_id: id, message: { role: "user", content: [{ type: "text", text }], ts: 1 } });
const answered = (id: string, text: string, input: number): TranscriptItem => ({
  type: "message",
  message_id: id,
  message: { role: "assistant", content: [{ type: "text", text }], provider: "p", api: "a", model: "m", usage: usage(input), stop_reason: "stop", ts: 2 },
} as TranscriptItem);
const ended = (task_id: string): TranscriptItem => ({ type: "task_end", task_id, outcome: "completed", ts: 3 });
const history = (before?: string) =>
  fromSnapshot({
    session: info,
    last_seq: 0,
    transcript: { items: [said("u1", "one"), answered("a1", "answer one", 100), ended("t1"), said("u2", "two"), answered("a2", "answer two", 500), ended("t2")], ...(before && { before }) },
    usage: { per_model: { "p/m": usage(600) } },
  });
const msg = (text: string): UserMessage => ({ content: [{ type: "text", text }], ts: 9 });
let seq = 0;
const play = (msgs: EventMsg[], from: ReturnType<typeof history>) => applyEvents(from, msgs.map((m) => ({ seq: ++seq, ts: 1000 + seq, ...m }) as Event));
const ids = (v: ReturnType<typeof history>) => v.entries.flatMap((e) => (e.type === "message" ? [e.message_id] : e.type === "task_end" ? [e.task_id] : []));

test("the conversation is cut at the message; what was spent stays", () => {
  const before = history();
  assert.equal(before.context, 501);
  const v = play([{ type: "session_rewound", message_id: "u2" }], before);
  assert.deepEqual(ids(v), ["u1", "a1", "t1"]);
  assert.equal(v.context, 101, "measured from the answer now last");
  assert.deepEqual(v.usage, before.usage);
  assert.deepEqual(v.rewound, { message_id: "u2", gone: ["u2"] });
  assert.equal(v.stale, undefined);
  // Back to the first: nothing is left to measure.
  const w = play([{ type: "session_rewound", message_id: "u1" }], before);
  assert.deepEqual(ids(w), []);
  assert.equal(w.context, 0);
  assert.deepEqual(w.rewound?.gone, ["u1", "u2"]);
});

test("a message past what the view holds: everything goes, and it asks for a new snapshot", () => {
  const v = play([{ type: "session_rewound", message_id: "older" }], history("i40"));
  assert.deepEqual(ids(v), []);
  assert.equal(v.stale, true);
  assert.deepEqual(v.rewound?.gone, ["u1", "u2"]);
  // The snapshot it gets has no such mark.
  assert.equal(fromSnapshot({ session: info, last_seq: 9, transcript: { items: [] }, usage: { per_model: {} } }).stale, undefined);
});

test("what waited comes back, also when the next task starts in the same frame", () => {
  const v = play(
    [
      { type: "task_interrupted", task_id: "t3", returned_input: [], usage: { per_model: {} } },
      { type: "session_rewound", message_id: "u2", returned_input: [msg("later")] },
      { type: "task_started", task_id: "t4" },
    ],
    play([{ type: "task_started", task_id: "t3" }], history()),
  );
  assert.deepEqual(v.returned?.map((m) => m.content), [[{ type: "text", text: "later" }]]);
  assert.equal(v.live?.task_id, "t4");
  // The line that said the last task was stopped went with the rest.
  assert.deepEqual(ids(v), ["u1", "a1", "t1"]);
});

test("sub-agents started in what is taken back leave; a past failure goes", () => {
  const started = (agent_id: string): EventMsg => ({ type: "agent_started", agent_id, name: agent_id, description: "", session_file: "/a", call_id: `c-${agent_id}` });
  let v = play([started("a1"), started("a2"), { type: "task_failed", task_id: "t3", error: { kind: "auth", message: "no", retryable: false }, usage: { per_model: {} } }], history());
  assert.ok(v.failure);
  v = play([{ type: "session_rewound", message_id: "u2", removed_agents: ["a2"] }], v);
  assert.deepEqual(v.agents.map((a) => a.id), ["a1"]);
  assert.equal(v.failure, undefined);
});

test("a refusal is noted for the window that sent it, and is no notice", () => {
  const v = play([{ type: "warning", message: "cannot resend: that message is not in the conversation", code: "resend_rejected" }], history());
  assert.ok(v.refused);
  assert.equal(v.notices.length, 0);
  assert.deepEqual(ids(v), ids(history()), "nothing changed");
});

test("whether what came after a message may have changed files", () => {
  const call = (name: string, args: Record<string, unknown>) => ({ type: "message", message_id: `m-${name}`, message: { role: "assistant", content: [{ type: "tool_call", id: `c-${name}`, name, arguments: args }], provider: "p", api: "a", model: "m", usage: usage(1), stop_reason: "tool_use", ts: 2 } }) as TranscriptItem;
  const after = (...items: TranscriptItem[]) => fromSnapshot({ session: info, last_seq: 0, transcript: { items: [said("u1", "one"), ...items] }, usage: { per_model: {} } });
  assert.equal(changesFilesAfter(after(answered("a", "just words", 1)), "u1"), false);
  assert.equal(changesFilesAfter(after(call("read", { path: "a" }), call("grep", { pattern: "a" })), "u1"), false);
  for (const [name, args] of [["write", { path: "a" }], ["edit", { path: "a" }], ["bash", { command: "ls" }], ["spawn_agent", { task: "x" }], ["send_message", { to: "x" }]] as const) {
    assert.equal(changesFilesAfter(after(call(name, args)), "u1"), true, name);
  }
  // Only what came after it counts.
  const v = fromSnapshot({ session: info, last_seq: 0, transcript: { items: [call("write", { path: "a" }), said("u1", "one"), answered("a", "words", 1)] }, usage: { per_model: {} } });
  assert.equal(changesFilesAfter(v, "u1"), false);
  assert.equal(changesFilesAfter(v, "nothing-like-it"), false);
});

test("tabs a message hands over are not the owner's words; the block stays as it was", () => {
  const one = handedTabBlock('Inbox "work"', "https://mail.a.io/");
  const many = handedTabsBlock([{ url: "https://a.io", title: "A", current: true }, { url: "https://b.io", title: "B", current: false }]);
  const { text, files, tabs } = userText([{ type: "text", text: "read this" }, attachedBlock(["/w/a"]), one, many]);
  assert.equal(text, "read this");
  assert.deepEqual(files, ["/w/a"]);
  assert.deepEqual(tabs.map((t) => t.titles), [['Inbox "work"'], ["A", "B"]]);
  assert.deepEqual(tabs.map((t) => ({ type: "text", text: t.text })), [one, many]);
  const v = fromSnapshot({ session: info, last_seq: 0, transcript: { items: [{ type: "message", message_id: "u", message: { role: "user", content: [{ type: "text", text: "read this" }, one], ts: 1 } }] }, usage: { per_model: {} } });
  const step = conversation(v)[0];
  assert.ok(step.kind === "user" && step.text === "read this" && step.tabs.length === 1);
});
