import assert from "node:assert/strict";
import { test } from "node:test";
import { keys, platformKeys } from "./keys.ts";

test("shortcuts read as each system shows them", () => {
  assert.equal(keys("Shift+CmdOrCtrl+N", "mac"), "⇧⌘N");
  assert.equal(keys("Shift+CmdOrCtrl+N", "win"), "Ctrl+Shift+N");
  assert.equal(keys("CmdOrCtrl+,", "mac"), "⌘,");
  assert.equal(keys("CmdOrCtrl+,", "win"), "Ctrl+,");
  assert.equal(keys("Alt+CmdOrCtrl+0", "win"), "Ctrl+Alt+0");
  assert.equal(keys(platformKeys.mac.toggleSidebar, "mac"), "⌃⌘S");
  assert.equal(keys(platformKeys.mac.toggleInspector, "mac"), "⌥⌘0");
  assert.equal(keys(platformKeys.win.toggleSidebar, "win"), "Ctrl+B");
  assert.equal(keys(platformKeys.win.toggleInspector, "win"), "Ctrl+Shift+I");
});

test("no Windows shortcut is a Ctrl+Alt one (AltGr types characters with it)", () => {
  for (const k of Object.values(platformKeys.win)) assert.ok(!(k.includes("Ctrl") && k.includes("Alt")), k);
});
