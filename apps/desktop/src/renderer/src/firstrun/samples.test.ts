import { test } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { actStarts, frameMs, messageMs, messages, meshWires, show, showWires } from "./samples.ts";

// The first run's examples as docs/cold-start-prd.md §5.5.2 and §5.6.2 have them.

const locales = path.join(import.meta.dirname, "..", "..", "..", "locales");
const words = (lang: string) => JSON.parse(fs.readFileSync(path.join(locales, lang, "firstrun.json"), "utf8"));

test("Her's show: 10 frames 2.1s apart, three acts starting at frames 0, 4 and 8", () => {
  assert.equal(show.length, 10);
  assert.equal(frameMs, 2100);
  assert.deepEqual(actStarts, [0, 4, 8]);
  assert.deepEqual(
    show.map((f) => f.act),
    [0, 0, 0, 0, 1, 1, 1, 1, 2, 2],
  );
  // The question is asked, then answered by the pointer; the PR and notes come at the end.
  assert.deepEqual(
    show.map((f) => f.ask ?? 0),
    [0, 0, 1, 2, 0, 0, 0, 0, 0, 0],
  );
  assert.deepEqual(
    show.map((f) => !!f.out),
    [false, false, false, false, false, false, false, false, true, true],
  );
  // Codex fails on Windows once, and passes when verified again.
  assert.deepEqual(show[6].cards.cx, ["verifyWin", "fail"]);
  assert.deepEqual(show[7].cards.cx, ["reverify", "run"]);
});

test("Her's show: every wire it lights exists, and every word it says is in both languages", () => {
  for (const f of show) for (const [card] of f.go ?? []) assert.ok(showWires[card], card);
  for (const lang of ["en", "zh-CN"]) {
    const s = words(lang).herStep.show;
    for (const f of show) {
      assert.ok(s[f.say], `${lang} herStep.show.${f.say}`);
      for (const [task] of Object.values(f.cards)) assert.ok(s.task[task!], `${lang} herStep.show.task.${task}`);
    }
  }
});

test("the connection map: 11 wires, each of the three senders reaching every agent, and Her the other two", () => {
  assert.equal(Object.keys(meshWires).length, 11);
  for (const from of ["cl", "her", "you"]) for (const to of ["cc", "cx", "gm"]) assert.ok(meshWires[`${from}-${to}`], `${from}-${to}`);
  assert.ok(meshWires["her-cl"]);
  assert.ok(meshWires["her-you"]);
});

test("the connection map: 7 messages 2.4s apart, along wires that exist, in both languages", () => {
  assert.equal(messages.length, 7);
  assert.equal(messageMs, 2400);
  // The phone reaches Codex directly, and Codex answers it (the map's point: all reach all).
  assert.deepEqual(messages[0].wires, [["you-cx", false]]);
  assert.deepEqual(messages[1].wires, [["you-cx", true]]);
  for (const m of messages) for (const [w] of m.wires) assert.ok(meshWires[w], w);
  for (const lang of ["en", "zh-CN"]) {
    const n = words(lang).net;
    for (const m of messages) {
      assert.ok(n[m.say], `${lang} net.${m.say}`);
      assert.ok(n[m.to], `${lang} net.${m.to}`);
    }
  }
});
