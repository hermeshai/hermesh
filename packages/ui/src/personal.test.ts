import assert from "node:assert/strict";
import { test } from "node:test";
import { kindNamed } from "./personal.ts";

test("a grant named for an app is that app", () => {
  assert.equal(kindNamed("Muse")?.id, "muse");
  assert.equal(kindNamed("Grok Bot")?.id, "grok");
  assert.equal(kindNamed("其他 Agent"), undefined);
  assert.equal(kindNamed("Jarvis"), undefined);
});

test("a hosted connection is its app's only from the app's own domain", () => {
  assert.equal(kindNamed("Muse (agent.meta.ai)")?.id, "muse");
  assert.equal(kindNamed("Muse (meta.ai)")?.id, "muse");
  assert.equal(kindNamed("ChatGPT (chatgpt.com)")?.id, "chatgpt");
  assert.equal(kindNamed("Claude (claude.ai)")?.id, "claude");
  assert.equal(kindNamed("Muse (meta.ai.example.com)"), undefined);
  assert.equal(kindNamed("Muse (evilmeta.ai)"), undefined);
  assert.equal(kindNamed("Claude (chatgpt.com)"), undefined);
  assert.equal(kindNamed("Grok Bot (anysphere.cursor-mcp)")?.id, "grok");
  assert.equal(kindNamed("Grok Bot (www.cursor.com)")?.id, "grok");
  assert.equal(kindNamed("Grok Bot (grok.example)"), undefined);
  assert.equal(kindNamed("OpenClaw (openclaw.ai)"), undefined); // no hosted connector known
});
