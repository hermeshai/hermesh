import assert from "node:assert/strict";
import { test } from "node:test";
import { PROTOCOL_VERSION } from "@hermesh/her-protocol";
import { httpTransport } from "./http.ts";
import { RpcClient, RpcError } from "./rpc.ts";

// httpTransport against a stand-in EventSource and fetch, which the test plays as the
// console's HTTP; http.e2e.test.ts runs it over real HTTP.

/** The browser's EventSource as the test plays it. */
class FakeEventSource {
  static made: FakeEventSource[] = [];
  readonly url: string;
  closed = false;
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  private readonly named = new Map<string, ((e: MessageEvent) => void)[]>();

  constructor(url: string) {
    this.url = url;
    FakeEventSource.made.push(this);
  }

  addEventListener(type: string, f: (e: MessageEvent) => void) {
    this.named.set(type, [...(this.named.get(type) ?? []), f]);
  }

  close() {
    this.closed = true;
  }

  /** An event from the server: unnamed ones are `message`. */
  emit(type: string, data: unknown) {
    const e = new MessageEvent(type, { data: JSON.stringify(data) });
    if (type === "message") this.onmessage?.(e);
    else for (const f of this.named.get(type) ?? []) f(e);
  }

  fail() {
    this.onerror?.(new Event("error"));
  }
}

type Posted = { url: string; method?: string; headers: Record<string, string>; body: { jsonrpc: string; id: number; method: string; params: unknown } };

/** fetch as the test plays it: each POST is kept, and answered by `reply`. */
function fakeFetch(reply: (posted: Posted) => Response) {
  const posted: Posted[] = [];
  globalThis.fetch = async (url, init) => {
    const p: Posted = { url: String(url), method: init?.method, headers: init?.headers as Record<string, string>, body: JSON.parse(String(init?.body)) };
    posted.push(p);
    return reply(p);
  };
  return posted;
}

globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;

const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
const settle = () => new Promise((resolve) => setImmediate(resolve));

test("the connection is the events at baseUrl, and each request a POST to rpc naming it", async () => {
  const posted = fakeFetch((p) => json({ jsonrpc: "2.0", id: p.body.id, result: { bots: [] } }));
  const transport = httpTransport("/her/console/");
  const rpc = new RpcClient(transport);
  const source = FakeEventSource.made.at(-1)!;
  assert.equal(source.url, "/her/console/events");
  await rpc.request("initialize", { protocol_version: PROTOCOL_VERSION });
  source.emit("opened", { connection_id: "c7" });
  assert.deepEqual(await rpc.request("bots/subscribe", {}), { bots: [] });
  assert.equal(posted.length, 1);
  const [p] = posted;
  assert.equal(p.url, "/her/console/rpc");
  assert.equal(p.method, "POST");
  assert.equal(p.headers["Her-Connection"], "c7");
  assert.equal(p.headers["Content-Type"], "application/json");
  assert.deepEqual({ ...p.body, id: typeof p.body.id }, { jsonrpc: "2.0", id: "number", method: "bots/subscribe", params: {} });
  transport.close();
});

test("notifications are the unnamed events; her's errors and refused POSTs fail their request", async () => {
  fakeFetch((p) =>
    p.body.method === "bots/open"
      ? json({ jsonrpc: "2.0", id: p.body.id, error: { code: -32040, message: "bot b9 not found", data: { kind: "bot_not_found" } } })
      : new Response("not for this caller", { status: 403 }),
  );
  const transport = httpTransport("/her/console/");
  const rpc = new RpcClient(transport);
  const source = FakeEventSource.made.at(-1)!;
  const deleted: string[] = [];
  rpc.on("bots/changed", (p) => void deleted.push(...(p.deleted ?? [])));
  source.emit("opened", { connection_id: "c8" });
  source.emit("message", { jsonrpc: "2.0", method: "bots/changed", params: { bots: [], deleted: ["b1"] } });
  await settle();
  assert.deepEqual(deleted, ["b1"]);
  await assert.rejects(rpc.request("bots/open", { bot_id: "b9" }), (e) => e instanceof RpcError && e.kind === "bot_not_found");
  await assert.rejects(rpc.request("session/list", {}), (e) => e instanceof RpcError && /HTTP 403 not for this caller/.test(e.message));
  assert.equal(rpc.isClosed, false);
  transport.close();
});

test("an error on the events ends the connection without the EventSource coming back; close ends it too", async () => {
  fakeFetch(() => json({}));
  const rpc = new RpcClient(httpTransport("/her/console/"));
  const source = FakeEventSource.made.at(-1)!;
  source.emit("opened", { connection_id: "c9" });
  const closed = new Promise<void>((resolve) => rpc.onClose(resolve));
  source.fail();
  await closed;
  await settle();
  assert.ok(source.closed);
  const other = httpTransport("/her/console/");
  const second = FakeEventSource.made.at(-1)!;
  other.close();
  await settle();
  assert.ok(second.closed);
});
