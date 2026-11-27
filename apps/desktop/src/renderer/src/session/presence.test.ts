import assert from "node:assert/strict";
import test from "node:test";
import { nextWhoForKey, type Who } from "./presenceKeys.ts";

const key = (
  k: string,
  mods: { metaKey?: boolean; ctrlKey?: boolean; shiftKey?: boolean; altKey?: boolean; isComposing?: boolean; keyCode?: number } = {},
) => ({
  key: k,
  metaKey: false,
  ctrlKey: false,
  shiftKey: false,
  altKey: false,
  ...mods,
});

const ctx = (
  who: Who,
  over: Partial<{ promptEmpty: boolean; inComposer: boolean; inOtherEditable: boolean; overlayOpen: boolean }> = {},
) => ({
  who,
  promptEmpty: true,
  inComposer: true,
  inOtherEditable: false,
  overlayOpen: false,
  ...over,
});

test("empty composer: Tab toggles and Left/Right select Agent or Her directionally", () => {
  assert.equal(nextWhoForKey(key("Tab"), ctx("agent")), "her");
  assert.equal(nextWhoForKey(key("Tab"), ctx("her")), "agent");
  assert.equal(nextWhoForKey(key("ArrowRight"), ctx("agent")), "her");
  assert.equal(nextWhoForKey(key("ArrowRight"), ctx("her")), "her");
  assert.equal(nextWhoForKey(key("ArrowLeft"), ctx("her")), "agent");
  assert.equal(nextWhoForKey(key("ArrowLeft"), ctx("agent")), "agent");
});

test("non-empty composer: Left/Right and bare Tab leave editing alone, while Shift+Tab and Cmd/Ctrl+N switch", () => {
  const writing = ctx("agent", { promptEmpty: false, inComposer: true });
  assert.equal(nextWhoForKey(key("ArrowLeft"), writing), null);
  assert.equal(nextWhoForKey(key("ArrowRight"), writing), null);
  assert.equal(nextWhoForKey(key("Tab"), writing), null);
  assert.equal(nextWhoForKey(key("Tab", { shiftKey: true }), writing), "her");
  assert.equal(nextWhoForKey(key("n", { metaKey: true }), writing), "her");
  assert.equal(nextWhoForKey(key("N", { ctrlKey: true }), ctx("her", { promptEmpty: false })), "agent");
});

test("IME composition, open menus/dialogs/sheets, and other text fields never trigger role switching", () => {
  assert.equal(nextWhoForKey(key("Tab", { isComposing: true }), ctx("agent")), null);
  assert.equal(nextWhoForKey(key("ArrowRight", { keyCode: 229 }), ctx("agent")), null);
  assert.equal(nextWhoForKey(key("Tab"), ctx("agent", { overlayOpen: true })), null);
  assert.equal(nextWhoForKey(key("n", { metaKey: true }), ctx("agent", { overlayOpen: true })), null);
  assert.equal(nextWhoForKey(key("ArrowLeft"), ctx("her", { inComposer: false, inOtherEditable: true })), null);
  assert.equal(nextWhoForKey(key("Tab", { shiftKey: true }), ctx("her", { inComposer: false, inOtherEditable: true })), null);
});
