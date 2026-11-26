import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { chooseMacTerminal, parseRunningBundleIds } from "./terminal.ts";

describe("chooseMacTerminal", () => {
  it("parses running bundle IDs from lsappinfo output", () => {
    const sample = `
      1) "Ghostty" ASN:0x0-0x123:
          bundleID="com.mitchellh.ghostty"
      2) "Finder" ASN:0x0-0x124:
          bundleID="com.apple.finder"
          bundleID=[ NULL ]
    `;
    const ids = parseRunningBundleIds(sample);
    assert.deepEqual([...ids], ["com.mitchellh.ghostty", "com.apple.finder"]);
  });

  it("prefers a currently running terminal even when a higher-priority terminal is also installed", () => {
    const installed = new Set([
      "/Applications/Ghostty.app",
      "/Applications/iTerm.app",
      "/System/Applications/Utilities/Terminal.app",
    ]);
    const running = new Set(["com.googlecode.iterm2"]);
    assert.equal(
      chooseMacTerminal("/Users/alice", running, (p) => installed.has(p)),
      "/Applications/iTerm.app",
    );
  });

  it("falls back to installed priority order when no terminal is running", () => {
    const installed = new Set([
      "/Users/alice/Applications/Warp.app",
      "/System/Applications/Utilities/Terminal.app",
    ]);
    assert.equal(
      chooseMacTerminal("/Users/alice", new Set(), (p) => installed.has(p)),
      "/Users/alice/Applications/Warp.app",
    );
  });

  it("falls back to system Terminal.app when no third-party terminal is installed", () => {
    assert.equal(
      chooseMacTerminal("/Users/alice", new Set(), (p) => p === "/System/Applications/Utilities/Terminal.app"),
      "/System/Applications/Utilities/Terminal.app",
    );
  });
});
