import { test } from "node:test";
import assert from "node:assert/strict";
import { computerUseView, parseDesktopStatus, type DesktopStatus } from "./status.ts";

const status = (s: Partial<DesktopStatus>): DesktopStatus => ({
  enabled: true,
  program: "/Applications/Hermesh.app/Contents/Helpers/agent-desktop",
  accessibility: "denied",
  screen_recording: "granted",
  ...s,
});

test("each permission, under its list's name on that macOS", () => {
  assert.deepEqual(computerUseView(status({}), 27), {
    kind: "permissions",
    enabled: true,
    rows: [
      { kind: "accessibility", list: "deviceControl", granted: false },
      { kind: "screen", list: "screenAudio", granted: true },
    ],
  });
  const sonoma = computerUseView(status({ accessibility: "granted", screen_recording: "unknown" }), 14);
  assert.deepEqual(sonoma.kind === "permissions" && sonoma.rows, [
    { kind: "accessibility", list: "accessibility", granted: true },
    { kind: "screen", list: "screen", granted: false },
  ]);
  const tahoe = computerUseView(status({}), 26);
  assert.deepEqual(tahoe.kind === "permissions" && tahoe.rows.map((r) => r.list), ["accessibility", "screenAudio"]);
});

test("the tools off still show what macOS allows", () => {
  const off = computerUseView(status({ enabled: false }), 27);
  assert.equal(off.kind === "permissions" && off.enabled, false);
});

test("nothing to show off macOS or without an answer; a missing agent-desktop says so", () => {
  assert.deepEqual(computerUseView(null, 27), { kind: "none" });
  assert.deepEqual(computerUseView(status({ program: null, accessibility: "unsupported", screen_recording: "unsupported" }), 0), { kind: "none" });
  assert.deepEqual(computerUseView(status({ program: undefined, accessibility: "unknown", screen_recording: "unknown" }), 27), { kind: "missing" });
});

test("desktop/status as hermeshd hands it over", () => {
  const json = (v: unknown) => new TextEncoder().encode(JSON.stringify(v));
  const s = status({});
  assert.deepEqual(parseDesktopStatus(json(s), false), s);
  assert.equal(parseDesktopStatus(json(s), true), null, "her's error");
  assert.equal(parseDesktopStatus(undefined, false), null);
  assert.equal(parseDesktopStatus(new TextEncoder().encode("{"), false), null);
  assert.equal(parseDesktopStatus(json({ enabled: true }), false), null, "an older her's answer to something else");
});
