import assert from "node:assert/strict";
import { test } from "node:test";
import { Code, ConnectError } from "@connectrpc/connect";
import type { Machine } from "@hermesh/proto/local";
import { herAnswers, machineAnswers, needsUpdate, workspaceKeptError } from "./skew.ts";

// A machine older than this app (docs/version-skew.md §6.1, §6.3).

test("unimplemented and an unknown method of her's say the machine needs an update; nothing else does", () => {
  assert.equal(needsUpdate(new ConnectError("her's \"bots/memory\" is not open to the owner's devices", Code.Unimplemented)), true);
  assert.equal(needsUpdate({ code: -32601, kind: "method_not_found", message: "unknown method 'bots/memory'" }), true);
  assert.equal(needsUpdate(new ConnectError("no", Code.PermissionDenied)), false);
  assert.equal(needsUpdate({ kind: "invalid_params" }), false);
  assert.equal(needsUpdate(new Error("x")), false);
  assert.equal(needsUpdate(null), false);
});

test("what a machine lists is what it answers; nothing listed is unknown", () => {
  const m = (her?: { methods: string[] }) => ({ machineId: "m", her }) as unknown as Machine;
  assert.equal(herAnswers(m(), "bots/memory"), undefined);
  assert.equal(herAnswers(m({ methods: [] }), "bots/memory"), undefined);
  const listed = m({ methods: ["bots/subscribe", "bots/memory"] });
  assert.equal(herAnswers(listed, "bots/memory"), true);
  assert.equal(herAnswers(listed, "bots/routine"), false);
});

test("hermeshd's own methods: what its status lists, unknown for one from before the list", () => {
  const m = (methods?: string[]) => ({ machineId: "m", status: methods && { methods } }) as unknown as Machine;
  assert.equal(machineAnswers(m(), "ListWorkspaces"), undefined);
  assert.equal(machineAnswers(m([]), "ListWorkspaces"), undefined);
  assert.equal(machineAnswers(m(["TestAgent", "ListWorkspaces"]), "ListWorkspaces"), true);
  assert.equal(machineAnswers(m(["TestAgent"]), "ListWorkspaces"), false);
});

test("older hermeshd workspace kept errors extract clean relative paths without git status markers", () => {
  assert.deepEqual(workspaceKeptError("workspace kept: save or move local files before releasing it (!! apps/ios/build/)"), { path: "apps/ios/build/" });
  assert.deepEqual(workspaceKeptError("workspace kept: save or move local changes before releasing it (src/app.ts)"), { path: "src/app.ts" });
  assert.deepEqual(workspaceKeptError("workspace kept: cannot verify this session's checkout"), { path: "" });
  assert.equal(workspaceKeptError("permission denied"), undefined);
});
