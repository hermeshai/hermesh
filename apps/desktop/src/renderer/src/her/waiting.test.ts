import assert from "node:assert/strict";
import { test } from "node:test";
import type { HerWaiting } from "@hermesh/proto/local";
import { outboxFor, splitAttached, stillSending } from "./waiting.ts";

const waiting = (id: string, bot: string, text: string, at: number, o: { paths?: string[]; error?: string } = {}) =>
  ({ id, machineId: "m1", botId: bot, text, localPaths: o.paths ?? [], createdAt: { seconds: BigInt(at), nanos: 0 }, error: o.error ?? "" }) as unknown as HerWaiting;

test("an assistant's waiting messages, oldest first, with their files by name and the last try's error", () => {
  const list = [
    waiting("w2", "her", "第二句", 200, { paths: ["/Users/me/Desktop/report.pdf"], error: "machine offline" }),
    waiting("w9", "other", "给别人的", 150),
    waiting("w1", "her", "第一句", 100),
  ];
  assert.deepEqual(outboxFor(list, "her", []), [
    { kind: "waiting", key: "w1", id: "w1", text: "第一句", files: [], ts: 100_000, error: "" },
    { kind: "waiting", key: "w2", id: "w2", text: "第二句", files: ["report.pdf"], ts: 200_000, error: "machine offline" },
  ]);
});

test("a message the conversation shows already was delivered: it waits no more, each delivery counting once", () => {
  const list = [waiting("w1", "her", "在吗", 100), waiting("w2", "her", "在吗", 110)];
  // Delivered once, after it was written: one of the two is still waiting.
  assert.deepEqual(outboxFor(list, "her", [{ key: "u1", text: "在吗", ts: 130_000 }]).map((w) => w.key), ["w2"]);
  // The same words said long before were another message.
  assert.deepEqual(outboxFor(list, "her", [{ key: "u1", text: "在吗", ts: 10_000 }]).map((w) => w.key), ["w1", "w2"]);
  assert.deepEqual(outboxFor(list, "her", [{ key: "u1", text: "在吗 ", ts: 105_000 }, { key: "u2", text: "在吗", ts: 115_000 }]), []);
});

test("a message delivered with files names them after its words; the waiting one it was is gone", () => {
  const delivered = "看看这份\n\nAttached files:\n- /home/me/.her/bots/her/files/uploads/report.pdf\n- /home/me/.her/bots/her/files/uploads/a b.png";
  assert.deepEqual(splitAttached(delivered), { text: "看看这份", files: ["/home/me/.her/bots/her/files/uploads/report.pdf", "/home/me/.her/bots/her/files/uploads/a b.png"] });
  assert.deepEqual(splitAttached("Attached files:\n- /x/y.txt"), { text: "", files: ["/x/y.txt"] });
  // Words that only mention it are words.
  assert.deepEqual(splitAttached("I wrote Attached files:\n- here").files, []);
  assert.deepEqual(splitAttached("plain"), { text: "plain", files: [] });
  const list = [waiting("w1", "her", "看看这份", 100, { paths: ["/Users/me/report.pdf"] })];
  assert.deepEqual(outboxFor(list, "her", [{ key: "u1", text: delivered, ts: 101_000 }]), []);
});

test("a message shows once, by its id: in the conversation, the outbox, or as the app sends it (her-sending §5.3)", () => {
  const said = [
    { key: "hw_a", text: "same", ts: 1000 },
    { key: "u7", text: "older her", ts: 2000 },
  ];
  const kept = [waiting("hw_k", "her", "kept", 3)];
  const sending = [
    { id: "hw_a", text: "same", files: [], images: [], ts: 900, refused: null },
    // The same words as one with an id of the app's: another message.
    { id: "hw_b", text: "same", files: [], images: [], ts: 1500, refused: null },
    // her gave the message an id of its own: matched by its words.
    { id: "hw_c", text: "older her", files: [], images: [], ts: 1900, refused: null },
    // In the outbox: the outbox's row shows it.
    { id: "hw_k", text: "kept", files: [], images: [], ts: 2900, refused: null },
    { id: "hw_d", text: "no", files: ["a.txt"], images: [], ts: 4000, refused: "her: no" },
  ];
  const rows = outboxFor(kept, "her", said, sending);
  assert.deepEqual(rows.map((r) => r.key), ["hw_k", "hw_b", "hw_d"]);
  assert.deepEqual(rows[2], { kind: "sending", key: "hw_d", id: "hw_d", text: "no", files: ["a.txt"], images: [], ts: 4000, refused: "her: no" });
});

test("a message being sent goes once the conversation has it, or, taken by her, won't have it (her-sending §5.2)", () => {
  const said = [
    { key: "hw_a", text: "mine", ts: 1000 },
    { key: "u7", text: "older her", ts: 2000 },
  ];
  const m = (id: string, text: string, ts: number, o: { delivered?: boolean; refused?: string } = {}) => ({ id, text, files: [], images: [], ts, refused: o.refused ?? null, delivered: o.delivered });
  const sending = [
    m("hw_a", "mine", 900, { delivered: true }),
    // her gave it an id of its own: matched by its words.
    m("hw_c", "older her", 1900, { delivered: true }),
    // A command her took without it joining the conversation.
    m("hw_x", "/context", 2500, { delivered: true }),
    m("hw_y", "taken, its event to come", 2550, { delivered: true }),
    m("hw_p", "HerSend not answered", 2600),
    m("hw_r", "refused", 2700, { refused: "her: no" }),
  ];
  assert.deepEqual(stillSending(sending, said).map((s) => s.id), ["hw_x", "hw_y", "hw_p", "hw_r"]);
  assert.deepEqual(stillSending(sending, said, (s) => s.id === "hw_x").map((s) => s.id), ["hw_y", "hw_p", "hw_r"]);
  // Only what her took.
  assert.deepEqual(stillSending(sending, said, () => true).map((s) => s.id), ["hw_p", "hw_r"]);
});
