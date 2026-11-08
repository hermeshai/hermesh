import assert from "node:assert/strict";
import { test } from "node:test";
import type { SessionInfo, TranscriptItem } from "@hermesh/her-protocol";
import { conversation } from "./steps.ts";
import { fromSnapshot, latestPage, withHistory } from "./session.ts";

// A session attaches with its latest items; earlier pages go before them, oldest first.

const info: SessionInfo = { session_id: "s", cwd: "/w", session_file: "/f.jsonl", model: "p/m", thinking: "off", permission_mode: "manual", context_window: 1000, archived: false };
const user = (id: string, text: string, ts: number): TranscriptItem => ({ type: "message", message_id: id, message: { role: "user", content: [{ type: "text", text }], ts } });

test("earlier pages go before the snapshot's items, until there is none before", () => {
  let v = fromSnapshot({ session: info, last_seq: 0, transcript: { items: [user("c", "third", 3)], before: "2" }, usage: { per_model: {} } });
  assert.equal(v.before, "2");
  v = withHistory(v, { items: [user("b", "second", 2)], before: "1" });
  assert.equal(v.before, "1");
  v = withHistory(v, { items: [user("a", "first", 1)], before: null });
  assert.equal(v.before, undefined);
  assert.deepEqual(conversation(v).map((x) => x.key), ["a", "b", "c"]);
});

test("a view cut to its latest entries still says there is more before them", () => {
  const all = withHistory(fromSnapshot({ session: info, last_seq: 0, transcript: { items: [user("c", "third", 3)], before: "2" }, usage: { per_model: {} } }), { items: [user("a", "first", 1), user("b", "second", 2)], before: null });
  assert.equal(all.before, undefined);
  const cut = latestPage(all, 1);
  assert.deepEqual(conversation(cut).map((x) => x.key), ["c"]);
  assert.ok(cut.before);
  assert.equal(latestPage(all, 3), all);
});
