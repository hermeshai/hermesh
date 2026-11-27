import assert from "node:assert/strict";
import { test } from "node:test";
import type { RuntimeMode } from "@hermesh/proto/identity";
import { forgetWorktree, remember, remembered, rememberedWorktree, rememberWorktree, type ChoiceStore } from "./choiceMemory.ts";

function memStore(): ChoiceStore {
  const map = new Map<string, string>();
  return {
    getItem: (k) => map.get(k) ?? null,
    setItem: (k, v) => void map.set(k, v),
  };
}

test("working copy choices are remembered per machine and project independently", () => {
  const s = memStore();
  assert.equal(rememberedWorktree("mac-1", "hermesh", s), undefined);

  rememberWorktree("mac-1", "hermesh", false, s);
  rememberWorktree("mac-1", "website", true, s);
  rememberWorktree("devbox", "hermesh", true, s);
  rememberWorktree("mac-1", "scratch", false, s);

  assert.equal(rememberedWorktree("mac-1", "hermesh", s), false);
  assert.equal(rememberedWorktree("mac-1", "website", s), true);
  assert.equal(rememberedWorktree("devbox", "hermesh", s), true);
  assert.equal(rememberedWorktree("mac-1", "scratch", s), undefined);
});

test("remembering a started session preserves other projects' working copy choices", () => {
  const s = memStore();
  rememberWorktree("mac-1", "hermesh", false, s);

  remember(
    "mac-1",
    {
      projectId: "website",
      sel: { instanceId: "claude", model: "opus", options: {} },
      mode: 3 as RuntimeMode,
      worktree: true,
    },
    s,
  );

  assert.equal(remembered(s).machineId, "mac-1");
  assert.equal(remembered(s).byMachine["mac-1"]?.projectId, "website");
  assert.equal(rememberedWorktree("mac-1", "hermesh", s), false);
  assert.equal(rememberedWorktree("mac-1", "website", s), true);

  forgetWorktree("mac-1", "hermesh", s);
  assert.equal(rememberedWorktree("mac-1", "hermesh", s), undefined);
  assert.equal(rememberedWorktree("mac-1", "website", s), true);
});
