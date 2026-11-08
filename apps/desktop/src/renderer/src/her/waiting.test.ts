import assert from "node:assert/strict";
import { test } from "node:test";
import type { HerWaiting } from "@hermesh/proto/local";
import { splitAttached, waitingFor } from "./waiting.ts";

const waiting = (id: string, bot: string, text: string, at: number, o: { paths?: string[]; error?: string } = {}) =>
  ({ id, machineId: "m1", botId: bot, text, localPaths: o.paths ?? [], createdAt: { seconds: BigInt(at), nanos: 0 }, error: o.error ?? "" }) as unknown as HerWaiting;

test("an assistant's waiting messages, oldest first, with their files by name and the last try's error", () => {
  const list = [
    waiting("w2", "her", "第二句", 200, { paths: ["/Users/me/Desktop/report.pdf"], error: "machine offline" }),
    waiting("w9", "other", "给别人的", 150),
    waiting("w1", "her", "第一句", 100),
  ];
  assert.deepEqual(waitingFor(list, "her", []), [
    { kind: "waiting", key: "waiting:w1", id: "w1", text: "第一句", files: [], ts: 100_000, error: "" },
    { kind: "waiting", key: "waiting:w2", id: "w2", text: "第二句", files: ["report.pdf"], ts: 200_000, error: "machine offline" },
  ]);
});

test("a message the conversation shows already was delivered: it waits no more, each delivery counting once", () => {
  const list = [waiting("w1", "her", "在吗", 100), waiting("w2", "her", "在吗", 110)];
  // Delivered once, after it was written: one of the two is still waiting.
  assert.deepEqual(waitingFor(list, "her", [{ text: "在吗", ts: 130_000 }]).map((w) => w.key), ["waiting:w2"]);
  // The same words said long before were another message.
  assert.deepEqual(waitingFor(list, "her", [{ text: "在吗", ts: 10_000 }]).map((w) => w.key), ["waiting:w1", "waiting:w2"]);
  assert.deepEqual(waitingFor(list, "her", [{ text: "在吗 ", ts: 105_000 }, { text: "在吗", ts: 115_000 }]), []);
});

test("a message delivered with files names them after its words; the waiting one it was is gone", () => {
  const delivered = "看看这份\n\nAttached files:\n- /home/me/.her/bots/her/files/uploads/report.pdf\n- /home/me/.her/bots/her/files/uploads/a b.png";
  assert.deepEqual(splitAttached(delivered), { text: "看看这份", files: ["/home/me/.her/bots/her/files/uploads/report.pdf", "/home/me/.her/bots/her/files/uploads/a b.png"] });
  assert.deepEqual(splitAttached("Attached files:\n- /x/y.txt"), { text: "", files: ["/x/y.txt"] });
  // Words that only mention it are words.
  assert.deepEqual(splitAttached("I wrote Attached files:\n- here").files, []);
  assert.deepEqual(splitAttached("plain"), { text: "plain", files: [] });
  const list = [waiting("w1", "her", "看看这份", 100, { paths: ["/Users/me/report.pdf"] })];
  assert.deepEqual(waitingFor(list, "her", [{ text: delivered, ts: 101_000 }]), []);
});
