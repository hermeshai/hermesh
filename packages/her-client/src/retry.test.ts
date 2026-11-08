import assert from "node:assert/strict";
import { test } from "node:test";
import type { Event, EventMsg, SessionInfo } from "@hermesh/her-protocol";
import { applyEvents, fromSnapshot, type SessionView } from "./session.ts";

// A model request retried after part of the reply arrived: the part is dropped while the
// retry waits, and the next attempt streams a message of its own.

const info: SessionInfo = { session_id: "s", cwd: "/w", session_file: "/f.jsonl", model: "p/m", thinking: "off", permission_mode: "manual", context_window: 1000, archived: false };

let seq = 0;
function play(msgs: EventMsg[], from?: SessionView): SessionView {
  const events = msgs.map((m) => ({ seq: ++seq, ts: 1000 * seq, ...m }) as Event);
  return applyEvents(from ?? fromSnapshot({ session: info, last_seq: 0, transcript: { items: [] }, usage: { per_model: {} } }), events);
}

test("a retry drops the reply streamed so far, and the next attempt streams anew", () => {
  let v = play([
    { type: "task_started", task_id: "t1" },
    { type: "message_started", message_id: "m1", role: "assistant" },
    { type: "message_delta", message_id: "m1", delta: { type: "text", index: 0, text: "half a rep" } },
  ]);
  assert.equal(v.live?.streaming?.message_id, "m1");
  v = play([{ type: "stream_retry", attempt: 1, delay_ms: 500, error: { kind: "network", message: "stream interrupted", retryable: true } }], v);
  assert.equal(v.live?.streaming, undefined);
  assert.equal(v.live?.retry?.attempt, 1);
  v = play(
    [
      { type: "message_started", message_id: "m2", role: "assistant" },
      { type: "message_delta", message_id: "m2", delta: { type: "text", index: 0, text: "The whole reply" } },
    ],
    v,
  );
  assert.equal(v.live?.retry, undefined);
  assert.equal(v.live?.streaming?.message_id, "m2");
});
