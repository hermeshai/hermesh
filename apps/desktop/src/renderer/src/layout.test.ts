import { test } from "node:test";
import assert from "node:assert/strict";
import { cardRoom, conversationMin, layoutOf, sidebarWidth, type Peek } from "./layout.ts";

const at = (width: number, peek: Peek = null, sidebar = true, inspector = true) => layoutOf({ width, peek, sidebar, inspector });
const both = sidebarWidth + conversationMin + cardRoom; // 1102
const sidebarOnly = sidebarWidth + conversationMin; // 832

test("layoutOf: wide, both sit beside the conversation as the person chose", () => {
  assert.deepEqual(at(1240), { sidebar: "docked", card: "docked" });
  assert.deepEqual(at(both), { sidebar: "docked", card: "docked" });
  assert.deepEqual(at(1240, null, false, false), { sidebar: "hidden", card: "hidden" });
});

test("layoutOf: narrowing, the card gives way first, then the sidebar", () => {
  assert.deepEqual(at(both - 1), { sidebar: "docked", card: "hidden" });
  assert.deepEqual(at(sidebarOnly), { sidebar: "docked", card: "hidden" });
  assert.deepEqual(at(sidebarOnly - 1), { sidebar: "hidden", card: "hidden" });
  assert.deepEqual(at(480), { sidebar: "hidden", card: "hidden" });
});

test("layoutOf: with the sidebar put away, the card keeps its place until the sidebar would give way too", () => {
  assert.deepEqual(at(both - 1, null, false), { sidebar: "hidden", card: "docked" });
  assert.deepEqual(at(sidebarOnly, null, false), { sidebar: "hidden", card: "docked" });
  // The card's room alone (830) is under the sidebar's (832): it never comes back as the sidebar goes.
  assert.deepEqual(at(sidebarOnly - 1, null, false), { sidebar: "hidden", card: "hidden" });
  assert.deepEqual(at(conversationMin + cardRoom), { sidebar: "hidden", card: "hidden" });
});

test("layoutOf: what gave way, asked for, shows over the conversation", () => {
  assert.deepEqual(at(700, "sidebar"), { sidebar: "over", card: "hidden" });
  assert.deepEqual(at(700, "card"), { sidebar: "hidden", card: "over" });
  // A peek also holds against the person's own choice: it is what they asked for now.
  assert.deepEqual(at(700, "sidebar", false), { sidebar: "over", card: "hidden" });
  // Between the sidebar's room and both, the card is over the conversation, the sidebar beside it.
  assert.deepEqual(at(1000, "card"), { sidebar: "docked", card: "over" });
});

test("layoutOf: a peek of what fits beside the conversation changes nothing", () => {
  assert.deepEqual(at(1240, "sidebar", false), { sidebar: "hidden", card: "docked" });
  assert.deepEqual(at(1240, "card", true, false), { sidebar: "docked", card: "hidden" });
});
