import assert from "node:assert/strict";
import { test } from "node:test";
import type { Event, EventMsg, QueuedInput, SessionInfo, UserMessage } from "@hermesh/her-protocol";
import { applyEvents, fromSnapshot } from "./session.ts";

// Input sent while a task runs: it waits in view, as the kernel's queue says, until it joins
// the history under its id; a stopped or failed task hands it back.

const info: SessionInfo = { session_id: "s", cwd: "/w", session_file: "/f.jsonl", model: "p/m", thinking: "off", permission_mode: "manual", context_window: 1000, archived: false };
const start = () => fromSnapshot({ session: info, last_seq: 0, transcript: { items: [] }, usage: { per_model: {} } });
const msg = (text: string, ts: number): UserMessage => ({ content: [{ type: "text", text }], ts });
const q = (id: string, text: string, mode: QueuedInput["mode"]): QueuedInput => ({ id, message: msg(text, 1), mode });
let seq = 0;
const play = (msgs: EventMsg[], from = start()) => applyEvents(from, msgs.map((m) => ({ seq: ++seq, ts: seq, ...m }) as Event));

test("the queue is what the kernel last said", () => {
  let v = play([
    { type: "task_started", task_id: "t" },
    { type: "queue_changed", queued: [q("a", "now", "steer"), q("b", "later", "follow_up")] },
  ]);
  assert.deepEqual(v.queued.map((x) => x.id), ["a", "b"]);
  // "now" joins: it leaves the queue, then shows up under the same id.
  v = play([
    { type: "queue_changed", queued: [q("b", "later", "follow_up")] },
    { type: "message_completed", message_id: "a", message: { role: "user", ...msg("now", 1) } },
  ], v);
  assert.deepEqual(v.queued.map((x) => x.id), ["b"]);
  const last = v.entries.at(-1);
  assert.equal(last?.type === "message" ? last.message_id : undefined, "a");
});

test("a withdrawal is noted for the window that asked", () => {
  const v = play([
    { type: "task_started", task_id: "t" },
    { type: "queue_changed", queued: [q("a", "oops", "steer")] },
    { type: "input_withdrawn", id: "a" },
    { type: "queue_changed", queued: [] },
  ]);
  assert.equal(v.withdrawn, "a");
  assert.equal(v.queued.length, 0);
});

test("a failed task hands what waited back, as a stopped one does", () => {
  let v = play([
    { type: "task_started", task_id: "t" },
    { type: "queue_changed", queued: [q("b", "later", "follow_up")] },
    { type: "queue_changed", queued: [] },
    { type: "task_failed", task_id: "t", error: { kind: "network", message: "down", retryable: true }, usage: { per_model: {} }, returned_input: [msg("later", 1)] },
  ]);
  assert.deepEqual(v.returned?.map((m) => m.content), [[{ type: "text", text: "later" }]]);
  assert.equal(v.queued.length, 0);
  v = play([{ type: "task_started", task_id: "t2" }, { type: "task_interrupted", task_id: "t2", returned_input: [msg("again", 2)], usage: { per_model: {} } }], v);
  assert.equal(v.returned?.length, 1);
});

test("a snapshot carries the queue", () => {
  const v = fromSnapshot({ session: info, last_seq: 3, transcript: { items: [] }, usage: { per_model: {} }, queued: [q("x", "x", "follow_up")] });
  assert.equal(v.queued[0].id, "x");
});
