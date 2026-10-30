import assert from "node:assert/strict";
import { test } from "node:test";
import { agentName, kindNamed } from "./personal.ts";

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

test("Grok Bot, which signs in as Cursor, is drawn as Cursor; the picker doesn't offer it", async () => {
  assert.equal(kindNamed("Cursor (www.cursor.com)")?.logo, "cursor");
  assert.equal(kindNamed("Cursor (cursor.com.example)"), undefined);
  assert.equal((await import("./personal.ts")).personalAgents.some((k) => k.id === "cursor"), false);
});

test("a hosted app is shown by its own name once its domain vouches for it", () => {
  assert.equal(agentName("Claude (claude.ai)"), "Claude");
  assert.equal(agentName("Cursor (www.cursor.com)"), "Cursor");
  assert.equal(agentName("Claude (chatgpt.com)"), "Claude (chatgpt.com)");
  assert.equal(agentName("Jarvis (jarvis.example)"), "Jarvis (jarvis.example)");
  assert.equal(agentName("千问办公"), "千问办公");
});

test("the icon the owner picks wins over the one the name suggests", async () => {
  const { grantLogo, grantPicture, iconApps } = await import("./personal.ts");
  assert.equal(grantLogo({ label: "Cursor (cursor.com)" }), "cursor");
  assert.equal(grantLogo({ label: "Cursor (cursor.com)", icon: "app:grokbot" }), "grokbot");
  assert.equal(grantLogo({ label: "Claude", icon: "letter" }), undefined);
  assert.equal(grantLogo({ label: "Claude", icon: "image", image: new Uint8Array([0x89]) }), undefined);
  assert.equal(grantPicture({ label: "x", icon: "image", image: new Uint8Array([0x89, 0x50]) }), "data:image/png;base64,iVA=");
  assert.equal(grantPicture({ label: "x", icon: "image", image: new Uint8Array([0xff, 0xd8]) }), "data:image/jpeg;base64,/9g=");
  assert.equal(grantPicture({ label: "x", image: new Uint8Array([0x89]) }), undefined);
  assert.ok(iconApps.some((a) => a.logo === "grokbot") && iconApps.some((a) => a.logo === "cursor"));
});
