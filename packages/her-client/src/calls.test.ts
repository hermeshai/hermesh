import assert from "node:assert/strict";
import { test } from "node:test";
import { PROTOCOL_VERSION } from "@hermesh/her-protocol";
import { openCallTransport, type CallAdapter, type CallResult, type ConnectionEvent, type SetupRequest } from "./calls.ts";
import { RpcClient, RpcError } from "./rpc.ts";

/** hermeshd as the test plays it: each connection's stream, and each call, answered when the test says. */
function hermeshd() {
  const streams: { push(e: ConnectionEvent): void; end(): void; fail(e: Error): void; signal: AbortSignal; setup: SetupRequest[] }[] = [];
  const calls: { connectionId: string; method: string; params: unknown; answer(r: CallResult): void; lose(e: Error): void }[] = [];
  const adapter: CallAdapter = {
    open(signal, setup) {
      const queue: ConnectionEvent[] = [];
      let over: Error | true | null = null;
      let wake = () => {};
      const stop = (why: Error | true) => {
        over ??= why;
        wake();
      };
      signal.addEventListener("abort", () => stop(true));
      streams.push({
        push: (e) => {
          queue.push(e);
          wake();
        },
        end: () => stop(true),
        fail: (e) => stop(e),
        signal,
        setup,
      });
      return (async function* () {
        for (;;) {
          while (queue.length === 0 && over === null) await new Promise<void>((resolve) => (wake = resolve));
          if (queue.length > 0) yield queue.shift()!;
          else if (over === true) return;
          else throw over;
        }
      })();
    },
    call: (connectionId, method, params) =>
      new Promise((answer, lose) => void calls.push({ connectionId, method, params, answer, lose })),
  };
  return { adapter, streams, calls };
}

/** Lets what is under way run: answers, the stream's events, the client's reactions. */
const settle = () => new Promise((resolve) => setImmediate(resolve));

test("initialize is answered by the transport and never reaches hermeshd, before the connection opens or after", async () => {
  const h = hermeshd();
  const rpc = new RpcClient(openCallTransport(h.adapter));
  const init = await rpc.request("initialize", { protocol_version: PROTOCOL_VERSION, client_info: { name: "test" } });
  assert.equal(init.protocol_version, 2);
  assert.equal(init.server_info.name, "her");
  assert.equal(init.capabilities.bots, true);
  // A notification has no way to her through hermeshd.
  rpc.notify("$/cancel_request", { id: 1 });
  h.streams[0].push({ opened: { connectionId: "c1" } });
  await rpc.request("initialize", { protocol_version: PROTOCOL_VERSION });
  await settle();
  assert.equal(h.calls.length, 0);
});

test("requests wait for the connection, go as calls on it, and come back under their own ids in the order they are answered", async () => {
  const h = hermeshd();
  const rpc = new RpcClient(openCallTransport(h.adapter));
  const list = rpc.request("bots/subscribe", {});
  const open = rpc.request("bots/open", { bot_id: "b1" });
  await settle();
  assert.equal(h.calls.length, 0);
  h.streams[0].push({ opened: { connectionId: "c1" } });
  await settle();
  assert.deepEqual(
    h.calls.map((c) => [c.connectionId, c.method, c.params]),
    [
      ["c1", "bots/subscribe", {}],
      ["c1", "bots/open", { bot_id: "b1" }],
    ],
  );
  const order: string[] = [];
  void list.then(() => order.push("bots/subscribe"));
  void open.then(() => order.push("bots/open"));
  h.calls[1].answer({ result: { session_id: "s1" } });
  await settle();
  h.calls[0].answer({ result: { bots: [] } });
  assert.deepEqual(await list, { bots: [] });
  assert.equal((await open).session_id, "s1");
  assert.deepEqual(order, ["bots/open", "bots/subscribe"]);
});

test("her's errors reject with their kind, and a lost call fails only its own request", async () => {
  const h = hermeshd();
  const rpc = new RpcClient(openCallTransport(h.adapter));
  h.streams[0].push({ opened: { connectionId: "c1" } });
  const missing = rpc.request("bots/open", { bot_id: "b9" });
  const lost = rpc.request("models/list", {});
  await settle();
  h.calls[0].answer({ error: { code: -32040, message: "bot b9 not found", data: { kind: "bot_not_found" } } });
  h.calls[1].lose(new Error("socket hang up"));
  await assert.rejects(missing, (e) => e instanceof RpcError && e.kind === "bot_not_found" && e.code === -32040 && e.message === "bot b9 not found");
  await assert.rejects(lost, (e) => e instanceof RpcError && e.kind === "internal_error" && /socket hang up/.test(e.message));
  // The connection is still there.
  const again = rpc.request("models/list", {});
  await settle();
  h.calls[2].answer({ result: { models: [], providers: [] } });
  assert.deepEqual(await again, { models: [], providers: [] });
  assert.equal(rpc.isClosed, false);
});

test("setup requests go with the open, and their answers come before any notification: a snapshot is never behind its events", async () => {
  const h = hermeshd();
  const got: string[] = [];
  let id: string | null = null;
  const rpc = new RpcClient(
    openCallTransport(h.adapter, {
      setup: [{ method: "bots/open", params: { bot_id: "b1" } }],
      onOpened: ([r]) => {
        id = "result" in r ? (r.result as { session_id: string }).session_id : null;
        got.push(`opened:${id}`);
      },
    }),
  );
  // Events of the session the snapshot names: dropped by a page that does not know it yet.
  rpc.on("session/event", (p) => void (p.session_id === id && got.push(`event:${p.event.seq}`)));
  assert.deepEqual(h.streams[0].setup, [{ method: "bots/open", params: { bot_id: "b1" } }]);
  h.streams[0].push({ opened: { connectionId: "c1", responses: [{ result: { session_id: "s1" } }] } });
  h.streams[0].push({ notification: { method: "session/event", params: { session_id: "s1", event: { seq: 8, ts: 0, type: "idle" } } } });
  await settle();
  assert.deepEqual(got, ["opened:s1", "event:8"]);
  // The connection is the same one for calls.
  const history = rpc.request("session/history", { session_id: "s1", before: "c8" });
  await settle();
  assert.equal(h.calls[0].connectionId, "c1");
  h.calls[0].answer({ result: { items: [], before: null } });
  await history;
});

test("without setup, onOpened hears of the connection with no answers; a failed setup request is its own answer", async () => {
  const h = hermeshd();
  const seen: CallResult[][] = [];
  openCallTransport(h.adapter, { onOpened: (r) => void seen.push(r) });
  assert.deepEqual(h.streams[0].setup, []);
  h.streams[0].push({ opened: { connectionId: "c1" } });
  const second = openCallTransport(h.adapter, { setup: [{ method: "bots/subscribe", params: {} }, { method: "models/list", params: {} }], onOpened: (r) => void seen.push(r) });
  h.streams[1].push({ opened: { connectionId: "c2", responses: [{ result: { bots: [] } }, { error: { code: -32050, message: "no model", data: { kind: "no_model" } } }] } });
  await settle();
  assert.deepEqual(seen, [[], [{ result: { bots: [] } }, { error: { code: -32050, message: "no model", data: { kind: "no_model" } } }]]);
  second.close();
});

test("the stream's notifications reach the client's listeners, in order", async () => {
  const h = hermeshd();
  const rpc = new RpcClient(openCallTransport(h.adapter));
  const got: string[] = [];
  rpc.on("bots/changed", (p) => void got.push(`bots/changed:${p.deleted?.join(",")}`));
  rpc.on("session/event", (p) => void got.push(`session/event:${p.session_id}:${p.event.seq}`));
  h.streams[0].push({ opened: { connectionId: "c1" } });
  h.streams[0].push({ notification: { method: "bots/changed", params: { bots: [], deleted: ["b1"] } } });
  h.streams[0].push({ notification: { method: "session/event", params: { session_id: "s1", event: { seq: 7, ts: 0, type: "idle" } } } });
  await settle();
  assert.deepEqual(got, ["bots/changed:b1", "session/event:s1:7"]);
});

test("when the stream ends, what waits fails, a late answer goes nowhere, and the owner reconnects on a new connection", async () => {
  const h = hermeshd();
  const first = openCallTransport(h.adapter);
  const lines: string[] = [];
  first.onText((text) => void lines.push(text));
  let rpc = new RpcClient(first);
  h.streams[0].push({ opened: { connectionId: "c1" } });
  const pending = rpc.request("bots/subscribe", {});
  await settle();
  assert.equal(h.calls.length, 1);
  const closed = new Promise<void>((resolve) => rpc.onClose(resolve));
  h.streams[0].end();
  await closed;
  await assert.rejects(pending, (e) => e instanceof RpcError && e.kind === "disconnected");
  h.calls[0].answer({ result: { bots: [] } });
  await settle();
  assert.deepEqual(lines, []);
  // The owner opens a new transport: a new connection, on which the next calls go.
  rpc = new RpcClient(openCallTransport(h.adapter));
  assert.equal(h.streams.length, 2);
  h.streams[1].push({ opened: { connectionId: "c2" } });
  const again = rpc.request("bots/subscribe", {});
  await settle();
  assert.equal(h.calls[1].connectionId, "c2");
  h.calls[1].answer({ result: { bots: [] } });
  assert.deepEqual(await again, { bots: [] });
});

test("a stream that fails before it opens ends the connection, and requests waiting for it are never sent", async () => {
  const h = hermeshd();
  const rpc = new RpcClient(openCallTransport(h.adapter));
  const waiting = rpc.request("bots/subscribe", {});
  h.streams[0].fail(new Error("PermissionDenied"));
  await assert.rejects(waiting, (e) => e instanceof RpcError && e.kind === "disconnected");
  await settle();
  assert.equal(h.calls.length, 0);
  assert.ok(rpc.isClosed);
});

test("close ends the stream and tells of the end once, also to whoever asks later", async () => {
  const h = hermeshd();
  const transport = openCallTransport(h.adapter);
  let ends = 0;
  transport.onClose(() => void ends++);
  transport.close();
  transport.close();
  assert.ok(h.streams[0].signal.aborted);
  await settle();
  assert.equal(ends, 1);
  const late = new Promise<void>((resolve) => transport.onClose(resolve));
  await late;
});
