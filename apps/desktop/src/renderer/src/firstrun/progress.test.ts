import { test, beforeEach } from "node:test";
import assert from "node:assert/strict";

// The window's storage, as the renderer has it.
const store = new Map<string, string>();
Object.assign(globalThis, {
  localStorage: {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
  },
});
const { firstRunDone, setFirstRunDone, setProgress } = await import("./progress.ts");

beforeEach(() => store.clear());

test("setProgress: a step after the welcome is kept; the welcome and the end keep nothing", () => {
  setProgress({ step: "her" });
  assert.equal(store.get("firstRun"), JSON.stringify({ step: "her" }));
  setProgress({ step: "welcome" });
  assert.equal(store.has("firstRun"), false);
  setProgress({ step: "far" });
  setProgress(null);
  assert.equal(store.has("firstRun"), false);
});

test("firstRunDone: set once finished, forgotten when everything is erased", () => {
  assert.equal(firstRunDone(), false);
  setFirstRunDone(true);
  assert.equal(firstRunDone(), true);
  assert.equal(store.get("firstRunDone"), "1");
  setFirstRunDone(false);
  assert.equal(firstRunDone(), false);
});
