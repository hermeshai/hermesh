import assert from "node:assert/strict";
import { test } from "node:test";
import { followAgainIn } from "./retry.ts";

test("a broken stream is followed again at once, then after longer waits", () => {
  assert.deepEqual([0, 1, 2, 3, 4, 50].map(followAgainIn), [0, 1000, 2000, 5000, 5000, 5000]);
});
