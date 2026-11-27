import assert from "node:assert/strict";
import { describe, it } from "node:test";
import type { SessionFilter } from "@hermesh/core";
import {
  buildSessionExcerpt,
  hasActiveSearchFilter,
  sessionBranch,
  shouldShowActions,
  splitHighlight,
  visibleFilterKeys,
} from "./searchModel.ts";

const filter = (patch: Partial<SessionFilter> = {}): SessionFilter => ({
  grant: "",
  project: "",
  agent: "",
  machine: "",
  archived: false,
  ...patch,
});

describe("searchModel", () => {
  it("splitHighlight splits case-insensitively, preserves casing, and escapes regex metacharacters", () => {
    assert.deepEqual(splitHighlight("", "主题"), []);
    assert.deepEqual(splitHighlight("扩展主题配色", "  "), [{ text: "扩展主题配色", match: false }]);
    assert.deepEqual(splitHighlight("扩展主题配色与主题玻璃", "主题"), [
      { text: "扩展", match: false },
      { text: "主题", match: true },
      { text: "配色与", match: false },
      { text: "主题", match: true },
      { text: "玻璃", match: false },
    ]);
    assert.deepEqual(splitHighlight("Fix C++ (v1.2) [beta]", "c++ (v1.2)"), [
      { text: "Fix ", match: false },
      { text: "C++ (v1.2)", match: true },
      { text: " [beta]", match: false },
    ]);
  });

  it("visibleFilterKeys keeps only essential filters and reveals grant only when deep-linked", () => {
    assert.deepEqual(
      visibleFilterKeys({ projectsCount: 2, agentsCount: 2, machinesCount: 1, filter: filter() }),
      ["project", "agent"],
    );
    assert.deepEqual(
      visibleFilterKeys({ projectsCount: 2, agentsCount: 2, machinesCount: 2, filter: filter() }),
      ["project", "agent", "machine"],
    );
    assert.deepEqual(
      visibleFilterKeys({ projectsCount: 1, agentsCount: 1, machinesCount: 1, filter: filter({ grant: "c_bot" }) }),
      ["project", "agent", "grant"],
    );
  });

  it("shouldShowActions hides generic actions only when query is empty and a task filter is active", () => {
    assert.equal(hasActiveSearchFilter(filter()), false);
    assert.equal(hasActiveSearchFilter(filter({ archived: true })), true);
    assert.equal(shouldShowActions("", filter()), true);
    assert.equal(shouldShowActions("", filter({ project: "hermesh" })), false);
    assert.equal(shouldShowActions("设置", filter({ project: "hermesh" })), true);
  });

  it("sessionBranch resolves gitStatus branch first, then workspaceStrategy, then session.branch", () => {
    assert.equal(
      sessionBranch({ branch: "legacy-b", workspaceStrategy: { branch: "ws-b" }, gitStatus: { branch: "git-b" } }),
      "git-b",
    );
    assert.equal(sessionBranch({ branch: "legacy-b", workspaceStrategy: { branch: "ws-b" } }), "ws-b");
    assert.equal(sessionBranch({ branch: "legacy-b" }), "legacy-b");
  });

  it("buildSessionExcerpt picks waiting question, failure error, running progress, or latest summary", () => {
    assert.deepEqual(
      buildSessionExcerpt({
        waiting: true,
        failed: false,
        active: false,
        progress: {
          lastMessageExcerpt: "previous line",
          pendingQuestion: { items: [{ text: "是否跳过\n已推送分支？" }] },
        },
      }),
      { kind: "waiting", text: "是否跳过 已推送分支？" },
    );

    assert.deepEqual(
      buildSessionExcerpt({
        waiting: false,
        failed: true,
        active: false,
        progress: { lastFailure: { message: "TLS  handshake\ntimeout" } },
      }),
      { kind: "failed", text: "TLS handshake timeout" },
    );

    assert.deepEqual(
      buildSessionExcerpt({
        waiting: false,
        failed: false,
        active: true,
        progress: { currentTool: "pnpm test" },
      }),
      { kind: "running", text: "pnpm test" },
    );

    assert.deepEqual(
      buildSessionExcerpt({
        waiting: false,
        failed: false,
        active: false,
        result: "已完成主题对齐",
      }),
      { kind: "latest", text: "已完成主题对齐" },
    );
  });
});
