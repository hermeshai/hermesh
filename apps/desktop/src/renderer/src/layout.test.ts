import { test } from "node:test";
import assert from "node:assert/strict";
import { cardRoom, conversationMin, layoutOf, listWidth, railWidth, sidebarWidth, type Peek } from "./layout.ts";

const at = (width: number, peek: Peek = null, sidebar = true, inspector = true) => layoutOf({ width, peek, sidebar, inspector });
const both = sidebarWidth + conversationMin + cardRoom; // 1102
const sidebarOnly = sidebarWidth + conversationMin; // 832

test("layoutOf: wide, both sit beside the conversation as the person chose", () => {
  assert.deepEqual(at(1240), { sidebar: "docked", card: "docked", list: "hidden" });
  assert.deepEqual(at(both), { sidebar: "docked", card: "docked", list: "hidden" });
  assert.deepEqual(at(1240, null, false, false), { sidebar: "rail", card: "hidden", list: "hidden" });
});

test("layoutOf: narrowing, the card gives way first, then the sidebar shrinks to its rail; it is never hidden", () => {
  assert.deepEqual(at(both - 1), { sidebar: "docked", card: "hidden", list: "hidden" });
  assert.deepEqual(at(sidebarOnly), { sidebar: "docked", card: "hidden", list: "hidden" });
  assert.deepEqual(at(sidebarOnly - 1), { sidebar: "rail", card: "hidden", list: "hidden" });
  assert.deepEqual(at(480), { sidebar: "rail", card: "hidden", list: "hidden" });
});

test("layoutOf: with the sidebar shrunk to its rail, the card keeps its place beside the rail", () => {
  assert.deepEqual(at(both - 1, null, false), { sidebar: "rail", card: "docked", list: "hidden" });
  assert.deepEqual(at(railWidth + conversationMin + cardRoom, null, false), { sidebar: "rail", card: "docked", list: "hidden" });
  assert.deepEqual(at(railWidth + conversationMin + cardRoom - 1, null, false), { sidebar: "rail", card: "hidden", list: "hidden" });
});

test("layoutOf: what gave way, asked for, shows over the conversation", () => {
  assert.deepEqual(at(700, "sidebar"), { sidebar: "over", card: "hidden", list: "hidden" });
  assert.deepEqual(at(700, "card"), { sidebar: "rail", card: "over", list: "hidden" });
  // A peek also holds against the person's own choice: it is what they asked for now.
  assert.deepEqual(at(700, "sidebar", false), { sidebar: "over", card: "hidden", list: "hidden" });
  // Between the sidebar's room and both, the card is over the conversation, the sidebar beside it.
  assert.deepEqual(at(1000, "card"), { sidebar: "docked", card: "over", list: "hidden" });
});

test("layoutOf: a peek of what fits beside the conversation changes nothing", () => {
  assert.deepEqual(at(1240, "sidebar", false), { sidebar: "rail", card: "docked", list: "hidden" });
  assert.deepEqual(at(1240, "card", true, false), { sidebar: "docked", card: "hidden", list: "hidden" });
});

test("layoutOf: beside the session list's column, the sidebar stays as the person chose; the card, then the sidebar, then the list give way", () => {
  const list = (width: number, o: { kind?: "session" | "list"; peek?: Peek; sidebar?: boolean } = {}) =>
    layoutOf({ width, peek: o.peek ?? null, sidebar: o.sidebar ?? true, inspector: true, selection: (o.kind ?? "session") === "session" ? { kind: "session", id: "s" } : { kind: "list" } });
  assert.deepEqual(list(both + listWidth), { sidebar: "docked", card: "docked", list: "docked" });
  assert.deepEqual(list(both + listWidth - 1), { sidebar: "docked", card: "hidden", list: "docked" });
  assert.deepEqual(list(sidebarOnly + listWidth - 1), { sidebar: "rail", card: "hidden", list: "docked" });
  // Shrunk to its rail by the person, it stays so, and the card has the room the sidebar left.
  assert.deepEqual(list(1240, { sidebar: false }), { sidebar: "rail", card: "docked", list: "docked" });
  // Asked for where there is no room, the sidebar shows over the sessions.
  assert.deepEqual(list(1000, { peek: "sidebar" }), { sidebar: "over", card: "hidden", list: "docked" });
  // Then the list, beside a session; alone, it is the page.
  assert.deepEqual(list(railWidth + listWidth + conversationMin), { sidebar: "rail", card: "hidden", list: "docked" });
  assert.deepEqual(list(railWidth + listWidth + conversationMin - 1), { sidebar: "rail", card: "hidden", list: "hidden" });
  assert.deepEqual(list(480, { kind: "list" }), { sidebar: "rail", card: "hidden", list: "docked" });
  // Work and Her have no list.
  assert.deepEqual(layoutOf({ width: both, peek: null, sidebar: true, inspector: true, selection: { kind: "overview" } }), { sidebar: "docked", card: "docked", list: "hidden" });
});
