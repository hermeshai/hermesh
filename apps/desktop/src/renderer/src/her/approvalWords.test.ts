import assert from "node:assert/strict";
import { test } from "node:test";
import { meshProject, summaryParts } from "./approvalWords.ts";

test("a mesh rule names its project; any other rule, and none, names nothing", () => {
  assert.equal(meshProject({ decision: "allow", prefix: ["mesh", "homemini/hermesh"] }), "homemini/hermesh");
  assert.equal(meshProject({ decision: "allow", prefix: ["git", "status"] }), undefined);
  assert.equal(meshProject({ decision: "allow", prefix: ["mesh"] }), undefined);
  assert.equal(meshProject(null), undefined);
  assert.equal(meshProject(undefined), undefined);
});

test("a summary of several lines is its first line, then the rest as written", () => {
  assert.deepEqual(summaryParts("Start Codex in homemini/hermesh\n\nFix the flaky relay test.\n  Keep the diff small.\n"), {
    title: "Start Codex in homemini/hermesh",
    body: "Fix the flaky relay test.\n  Keep the diff small.",
  });
  assert.equal(summaryParts("git status"), null);
  assert.equal(summaryParts("Start Codex in homemini/hermesh\n\n"), null);
  assert.equal(summaryParts(""), null);
});
