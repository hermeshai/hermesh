import assert from "node:assert/strict";
import { test } from "node:test";
import { FileDoc, type WriteResult } from "./files.ts";

/** A file on a fake disk, saves answered when the test says, timers run by hand. */
function setup(text = "one") {
  const disk = { text, hash: `h:${text}` };
  const writes: { text: string; base: string | null; answer: (r: WriteResult) => void }[] = [];
  const timers: (() => void)[] = [];
  let changes = 0;
  const doc = new FileDoc(disk, {
    write: (t, base) => new Promise((resolve) => writes.push({ text: t, base, answer: resolve })),
    changed: () => void changes++,
    setTimer: (f) => timers.push(f),
    clearTimer: (t) => {
      const i = (t as number) - 1;
      if (timers[i]) timers[i] = () => {};
    },
  });
  const tick = () => timers.splice(0).forEach((f) => f());
  const saved = (i = writes.length - 1) => writes[i].answer({ status: "saved", hash: `h:${writes[i].text}` });
  return { doc, writes, tick, saved, changes: () => changes };
}

const settle = () => new Promise((r) => setTimeout(r, 0));

test("an edit is saved after the pause, on the version it was edited from", async () => {
  const { doc, writes, tick, saved } = setup();
  doc.edit("two");
  assert.equal(doc.state, "dirty");
  assert.equal(writes.length, 0);
  tick();
  assert.equal(doc.state, "saving");
  assert.deepEqual([writes[0].text, writes[0].base], ["two", "h:one"]);
  saved();
  await settle();
  assert.equal(doc.state, "clean");
  assert.deepEqual(doc.disk, { text: "two", hash: "h:two" });
});

test("typing during a save is saved next, on the new base, one save at a time", async () => {
  const { doc, writes, tick, saved } = setup();
  doc.edit("two");
  tick();
  doc.edit("three");
  tick();
  assert.equal(writes.length, 1, "a second save went out while the first was out");
  saved(0);
  await settle();
  assert.equal(writes.length, 2);
  assert.deepEqual([writes[1].text, writes[1].base], ["three", "h:two"]);
  saved(1);
  await settle();
  assert.equal(doc.state, "clean");
  assert.equal(doc.disk.text, "three");
});

test("flush saves at once and waits for it", async () => {
  const { doc, writes, saved } = setup();
  doc.edit("now");
  const done = doc.flush();
  assert.equal(writes.length, 1);
  saved();
  await done;
  assert.equal(doc.state, "clean");
  // Nothing to save: nothing written.
  await doc.flush();
  assert.equal(writes.length, 1);
});

test("a conflict writes nothing more until the person chooses", async () => {
  const { doc, writes, tick } = setup();
  doc.edit("mine");
  tick();
  writes[0].answer({ status: "conflict", hash: "h:theirs" });
  await settle();
  assert.equal(doc.state, "conflict");
  doc.edit("mine, more");
  tick();
  await doc.flush();
  assert.equal(writes.length, 1, "saved on its own during a conflict");
  // Save anyway: whatever is there, with this text.
  const kept = doc.keepMine();
  assert.deepEqual([writes[1].text, writes[1].base], ["mine, more", null]);
  writes[1].answer({ status: "saved", hash: "h:mine, more" });
  await kept;
  assert.equal(doc.state, "clean");
});

test("load the disk's version drops the edits and tells the editor", async () => {
  const { doc, writes, tick } = setup();
  doc.edit("mine");
  tick();
  writes[0].answer({ status: "conflict", hash: "h:theirs" });
  await settle();
  const before = doc.replaced;
  doc.takeDisk({ text: "theirs", hash: "h:theirs" });
  assert.equal(doc.text, "theirs");
  assert.equal(doc.state, "clean");
  assert.equal(doc.replaced, before + 1);
});

test("a change on disk: taken when nothing was edited, a choice when something was", async () => {
  const { doc } = setup();
  doc.diskChanged({ text: "one", hash: "h:one" });
  assert.equal(doc.replaced, 0, "the same version replaced the text");
  doc.diskChanged({ text: "new", hash: "h:new" });
  assert.equal(doc.text, "new");
  assert.equal(doc.replaced, 1);
  doc.edit("edited");
  doc.diskChanged({ text: "newer", hash: "h:newer" });
  assert.equal(doc.state, "conflict");
  assert.equal(doc.text, "edited");
  doc.diskChanged(null);
  assert.equal(doc.state, "deleted");
});

test("a refused save stops saving; a failed one tries again at the next edit", async () => {
  const { doc, writes, tick } = setup();
  doc.edit("x");
  tick();
  writes[0].answer({ status: "refused", message: "her's own data" });
  await settle();
  assert.deepEqual([doc.state, doc.message], ["refused", "her's own data"]);
  doc.edit("y");
  tick();
  assert.equal(writes.length, 1);

  // The write itself failed (no permission): the text is kept, and saved again after the next edit.
  const failing = new FileDoc({ text: "a", hash: "h:a" }, { write: () => Promise.reject(new Error("EACCES")), changed: () => {}, setTimer: (f) => (f(), 0), clearTimer: () => {} });
  failing.edit("b");
  await settle();
  assert.deepEqual([failing.state, failing.message], ["failed", "EACCES"]);
  assert.equal(failing.text, "b");
});
