import assert from "node:assert/strict";
import { test } from "node:test";
import { applyEvents, botChat, fromSnapshot } from "@hermesh/her-client";
import type { Event, SessionSnapshot } from "@hermesh/her-protocol";
import { cardNow, cardOf, cardShown, connectBody, filled, toolCount, type Card } from "./cards.ts";

const usage = { input: 1, output: 1, cache_read: 0, cache_write: 0, cost: { currency: "USD", input: 0, output: 0, cache_read: 0, cache_write: 0, total: 0 } };

/** A connect card for a connector that wants a bearer token, as her makes it (the shared spec, §2). */
const card: Card = {
  id: "c_k3j9x2m4qa",
  kind: "connect",
  title: "Connect Notion",
  asked_by: "owner",
  expires_at: "2026-11-10T09:00:00Z",
  ops: [
    { method: "credentials/set", params: { name: "MCP_NOTION_TOKEN", value: "<value:f1>" } },
    {
      method: "config/write",
      params: { layer: "user", edits: [{ key_path: "mcp_servers.notion", value: { url: "https://mcp.notion.com/mcp", headers: { Authorization: "Bearer ${credential:MCP_NOTION_TOKEN}" } } }] },
    },
  ],
  body: { name: "notion", url: "https://mcp.notion.com/mcp", host: "mcp.notion.com", reconnect: false, sign_in: "none", server_name: "Notion MCP", fields: [{ id: "f1", label: "Authorization", secret: true }] },
};

test("the answer's ops are the card's with each placeholder filled, the card's own left as they were", () => {
  const before = structuredClone(card.ops);
  const ops = filled(card.ops, { f1: "secret-token" });
  assert.deepEqual(ops, [
    { method: "credentials/set", params: { name: "MCP_NOTION_TOKEN", value: "secret-token" } },
    before[1],
  ]);
  assert.deepEqual(card.ops, before, "the card's ops are not changed");
  assert.notEqual(ops[1], card.ops[1], "a copy, nested parts too");
  assert.notEqual((ops[1].params as { edits: unknown[] }).edits, (card.ops[1].params as { edits: unknown[] }).edits);

  // Only a whole string is a value's place; one the owner has no value for stays (her refuses it).
  const odd = [{ method: "x", params: { a: ["<value:f1>", "Bearer <value:f1>", "<value:f2>", 3, null, true] } }];
  assert.deepEqual(filled(odd, { f1: "v" }), [{ method: "x", params: { a: ["v", "Bearer <value:f1>", "<value:f2>", 3, null, true] } }]);
});

test("a card is read from what send_to_user's result carries; one that isn't a card is nothing", () => {
  assert.equal(cardOf(card), card);
  assert.equal(connectBody(card), card.body);
  for (const bad of [null, "c_1", { id: "c_1" }, { ...card, ops: undefined }, { ...card, body: null }]) assert.equal(cardOf(bad), null);
  assert.equal(connectBody({ ...card, kind: "site_login" }), null, "a kind this app doesn't know");
  assert.equal(toolCount("Notion connected: 14 tools (notion_search, notion_fetch, …)"), 14);
  assert.equal(toolCount("Tracker connected: 1 tool (echo)"), 1);
  assert.equal(toolCount("Notion connected"), undefined);
  assert.equal(toolCount(undefined), undefined);
});

/** Her's conversation with the card shown, as a snapshot gives it, with `later` after it in its history. */
function conversation(later: object[] = []): SessionSnapshot {
  return {
    session: { session_id: "conv-her", archived: false, context_window: 200000, cwd: "/her", model: "mock/mock-1", permission_mode: "manual", session_file: "s.jsonl", thinking: "medium" },
    last_seq: 4,
    transcript: {
      items: [
        { type: "message", message_id: "u1", message: { role: "user", content: [{ type: "text", text: "Sum up Notion" }], ts: 1000 } },
        {
          type: "message",
          message_id: "a1",
          message: { role: "assistant", api: "x", model: "mock-1", provider: "mock", stop_reason: "tool_use", ts: 2000, usage, content: [{ type: "tool_call", id: "s1", name: "send_to_user", arguments: { type: "card", card: card.id, text: "I need Notion for this." } }] },
        },
        { type: "tool_result", call_id: "s1", result: { call_id: "s1", tool_name: "send_to_user", content: [{ type: "text", text: "Shown to the owner." }], is_error: false, ts: 2001, details: { card, text: "I need Notion for this." } } },
        { type: "task_end", task_id: "t1", outcome: "completed", ts: 2002 },
        ...later,
      ] as SessionSnapshot["transcript"]["items"],
    },
    usage: { per_model: {} },
  };
}

const changed = (seq: number, state: string, note?: string) => ({ seq, ts: seq, type: "card_changed", card_id: card.id, state, ...(note && { note }) }) as unknown as Event;
const before = Date.parse("2026-11-09T12:00:00Z");
const after = Date.parse("2026-11-10T09:00:01Z");

test("Her's bubble and the card under it come from the result; the card stands as her says while the conversation is open", () => {
  const v = fromSnapshot(conversation());
  const item = botChat(v).find((i) => i.kind === "card");
  assert.ok(item?.kind === "card");
  assert.equal(item.text, "I need Notion for this.");
  assert.deepEqual(cardOf(item.card), card);

  assert.deepEqual(cardNow(card, v, undefined, before), { state: "open", live: false });
  const working = applyEvents(v, [changed(5, "working")]);
  assert.deepEqual(cardNow(card, working, undefined, before), { state: "working", live: true });
  const refused = applyEvents(working, [changed(6, "retry", "HTTP 401: invalid token")]);
  assert.deepEqual(cardNow(card, refused, undefined, before), { state: "retry", note: "HTTP 401: invalid token", live: true });
  const done = applyEvents(refused, [changed(7, "working"), changed(8, "done", "Notion connected: 14 tools (notion_search, …)")]);
  assert.deepEqual(cardNow(card, done, undefined, after), { state: "done", note: "Notion connected: 14 tools (notion_search, …)", live: true }, "ended: its expiry no longer matters");
});

test("read later, the history ends the card; otherwise it is open until it expires, or as her answered this device", () => {
  const resolved = fromSnapshot(conversation([{ type: "card_resolved", card_id: card.id, kind: "connect", outcome: "declined", note: "The owner declined." }]));
  assert.deepEqual(cardNow(card, resolved, undefined, before), { state: "declined", note: "The owner declined.", live: false });
  assert.deepEqual(cardNow(card, resolved, { state: "sign_in" }, before), { state: "declined", note: "The owner declined.", live: false }, "the history over this device's answer");
  const other = fromSnapshot(conversation([{ type: "card_resolved", card_id: "c_other00000", kind: "connect", outcome: "done" }]));
  assert.deepEqual(cardNow(card, other, undefined, before), { state: "open", live: false }, "another card's end is not this one's");

  const v = fromSnapshot(conversation());
  assert.deepEqual(cardNow(card, v, undefined, after), { state: "expired", live: false });
  assert.deepEqual(cardNow(card, v, { state: "sign_in" }, before), { state: "sign_in", live: false }, "answered here, the conversation opened again");
  assert.deepEqual(cardNow(card, applyEvents(v, [changed(5, "sign_in")]), undefined, after), { state: "expired", live: true }, "her's own expiry may not have come yet");
});

test("what the card shows: its form, the sign-in on the device that answered, or one line once it ended or another device took it", () => {
  const live = (state: string, note?: string) => ({ state, note, live: true }) as Parameters<typeof cardShown>[0];
  const read = (state: string, note?: string) => ({ state, note, live: false }) as Parameters<typeof cardShown>[0];
  assert.deepEqual(cardShown(read("open"), false), { kind: "form", busy: false });
  assert.deepEqual(cardShown(live("retry", "HTTP 401"), true), { kind: "form", note: "HTTP 401", busy: false });
  assert.deepEqual(cardShown(live("working"), true), { kind: "form", busy: true });
  assert.deepEqual(cardShown(live("sign_in"), true), { kind: "signIn" });
  for (const state of ["working", "sign_in"]) assert.deepEqual(cardShown(live(state), false), { kind: "ended", outcome: "elsewhere" }, state);
  assert.deepEqual(cardShown(live("done", "Notion connected: 14 tools"), false), { kind: "ended", outcome: "done", tools: 14 });
  assert.deepEqual(cardShown(live("declined"), true), { kind: "ended", outcome: "declined", note: undefined });
  assert.deepEqual(cardShown(live("declined"), false), { kind: "ended", outcome: "elsewhere" });
  assert.deepEqual(cardShown(read("declined"), false), { kind: "ended", outcome: "declined", note: undefined }, "read later, the outcome itself");
  assert.deepEqual(cardShown(read("failed", "no tools"), false), { kind: "ended", outcome: "failed", note: "no tools" });
  assert.deepEqual(cardShown(read("expired"), false), { kind: "ended", outcome: "expired" });
  assert.deepEqual(cardShown(live("replaced"), true), { kind: "ended", outcome: "replaced" });
});
