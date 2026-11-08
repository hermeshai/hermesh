import assert from "node:assert/strict";
import { test } from "node:test";
import { botChat } from "@hermesh/her-client";
import { keptFrom } from "./kept.ts";

const bytes = (v: unknown) => new TextEncoder().encode(JSON.stringify(v));
const none = new Uint8Array();

const usage = { input: 1, output: 1, cache_read: 0, cache_write: 0, cost: { currency: "USD", input: 0, output: 0, cache_read: 0, cache_write: 0, total: 0 } };

/** Her, as bots/subscribe listed the assistants last, busy then. */
const her = { id: "her", name: "Her", default: true, typing: true, agents_working: 2, avatar: { kind: "mark" }, created_at: "2026-11-01T00:00:00Z" };

/** Her's conversation as hermeshd keeps it (internal/owner/her_conversation.go): what the page shows of it, no earlier history. */
const conversation = {
  session: { session_id: "conv-her", archived: false, context_window: 200000, cwd: "/her", model: "mock/mock-1", permission_mode: "manual", session_file: "s.jsonl", thinking: "medium" },
  last_seq: 9,
  transcript: {
    items: [
      { type: "message", message_id: "u1", message: { role: "user", content: [{ type: "text", text: "hello" }], ts: 1000 } },
      {
        type: "message",
        message_id: "a1",
        message: { role: "assistant", api: "x", model: "mock-1", provider: "mock", stop_reason: "tool_use", ts: 2000, usage, content: [{ type: "tool_call", id: "c1", name: "send_to_user", arguments: { text: "hi there" } }] },
      },
      { type: "tool_result", call_id: "c1", result: { call_id: "c1", tool_name: "send_to_user", content: [], is_error: false, ts: 2001 } },
      { type: "task_end", task_id: "t1", outcome: "completed", ts: 2002 },
    ],
  },
  usage: { per_model: {} },
};

test("what hermeshd keeps shows as the conversation was: the owner's words and what Her sent, nothing earlier, Her at work no more", () => {
  const kept = keptFrom({ bot: bytes(her), snapshot: bytes(conversation) });
  assert.ok(kept?.view);
  assert.deepEqual(
    { name: kept.bot.name, id: kept.bot.id, typing: kept.bot.typing, agents: kept.bot.agents_working },
    { name: "Her", id: "her", typing: false, agents: 0 },
  );
  assert.equal(kept.session, "conv-her");
  assert.deepEqual(
    botChat(kept.view).map((i) => `${i.kind}: ${"text" in i ? i.text : ""}`),
    ["user: hello", "bot: hi there"],
  );
  assert.equal(kept.view.before, undefined);
  assert.equal(kept.view.live, null);
});

test("Her kept without a conversation yet, and nothing kept", () => {
  assert.equal(keptFrom({ bot: bytes(her), snapshot: none })?.view, null);
  assert.equal(keptFrom({ bot: none, snapshot: none }), null);
});
