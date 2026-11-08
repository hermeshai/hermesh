import assert from "node:assert/strict";
import { test } from "node:test";
import { httpTransport, RpcClient, RpcError } from "@hermesh/her-client";
import { agoText, errorText, priceText, tokensText } from "./format.ts";
import { i18n } from "./i18n.ts";

// Errors in words: her's own, the connection's, and hermeshd's refusals as it said them.

await i18n.changeLanguage("en");

class FakeEventSource {
  static last: FakeEventSource;
  onmessage: unknown = null;
  onerror: unknown = null;
  private opened: ((e: MessageEvent) => void) | undefined;
  constructor() {
    FakeEventSource.last = this;
  }
  addEventListener(_: string, f: (e: MessageEvent) => void) {
    this.opened = f;
  }
  open() {
    this.opened?.(new MessageEvent("opened", { data: JSON.stringify({ connection_id: "c1" }) }));
  }
  close() {}
}

test("a refusal of hermeshd's reads as hermeshd said it", async () => {
  globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;
  globalThis.fetch = async () => new Response("the owner's devices may not change tools.bash: settings that let Her do more on this machine need the owner's root key\n", { status: 403 });
  const transport = httpTransport("./");
  const rpc = new RpcClient(transport);
  FakeEventSource.last.open();
  const refused = await rpc.request("config/write", { layer: "user", edits: [{ key_path: "tools.bash", value: {} }] }).catch((e: unknown) => e);
  assert.equal(errorText(refused), "the owner's devices may not change tools.bash: settings that let Her do more on this machine need the owner's root key");
  globalThis.fetch = async () => new Response("", { status: 502 });
  assert.equal(errorText(await rpc.request("models/list", {}).catch((e: unknown) => e)), "HTTP 502");
  transport.close();
});

test("her's errors as her said them, a lost connection in the console's words", () => {
  assert.equal(errorText(new RpcError({ code: -32000, message: "no MCP server named x", data: { kind: "invalid_params" } })), "no MCP server named x");
  assert.equal(errorText(new RpcError({ code: -32099, message: "disconnected from her", data: { kind: "disconnected" } })), "The connection to Her was lost");
  assert.equal(errorText("plain"), "plain");
});

test("times, sizes and prices", () => {
  const now = new Date(2026, 10, 8, 18, 30);
  assert.equal(agoText(new Date(now.getTime() - 20_000), now), "just now");
  assert.equal(agoText(new Date(now.getTime() - 5 * 60_000), now), "5 min ago");
  assert.equal(agoText(new Date(2026, 10, 8, 9, 0), now), "9 hr ago");
  assert.equal(agoText(new Date(2026, 10, 7, 22, 0), now), "yesterday");
  assert.equal(tokensText(200_000), "200K");
  assert.equal(tokensText(1_500_000), "1.5M");
  assert.equal(priceText({ input: 1, output: 12.5, currency: "USD" }), "$1.00 / $13");
});
