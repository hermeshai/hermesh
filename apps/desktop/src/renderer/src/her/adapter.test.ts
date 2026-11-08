import assert from "node:assert/strict";
import { test } from "node:test";
import { openCallTransport, RpcClient, RpcError } from "@hermesh/her-client";
import type { CallResponse, OpenEvent } from "@hermesh/proto/her";
import { callResult, herAdapter, jsonBytes, type HerLocal } from "./adapter.ts";

const text = (b: Uint8Array) => new TextDecoder().decode(b);

/** hermeshd's LocalService as a test plays it: what each Her call was asked, and a stream the test feeds. */
function hermeshd() {
  const opened: { machineId: string; setup: { method: string; params: string }[]; signal: AbortSignal }[] = [];
  const calls: { machineId: string; connectionId: string; method: string; params: unknown }[] = [];
  const queue: OpenEvent[] = [];
  let wake = () => {};
  let answer: (method: string, params: unknown) => CallResponse = () => ({ response: { result: jsonBytes({}) } }) as CallResponse;
  const local: HerLocal = {
    herOpen(req, o) {
      opened.push({ machineId: req.machineId, setup: req.open.setup.map((r) => ({ method: r.method, params: text(r.params) })), signal: o.signal });
      return (async function* () {
        while (!o.signal.aborted) {
          if (queue.length > 0) yield queue.shift()!;
          else await new Promise<void>((resolve) => {
            wake = resolve;
            o.signal.addEventListener("abort", () => resolve(), { once: true });
          });
        }
      })();
    },
    async herCall(req) {
      const params = JSON.parse(text(req.call.request.params));
      calls.push({ machineId: req.machineId, connectionId: req.call.connectionId, method: req.call.request.method, params });
      return answer(req.call.request.method, params);
    },
  };
  const push = (e: OpenEvent["kind"]) => {
    queue.push({ kind: e } as OpenEvent);
    wake();
  };
  return { local, opened, calls, push, answerWith: (f: typeof answer) => (answer = f) };
}

const settle = () => new Promise((resolve) => setImmediate(resolve));
const response = (result: unknown) => ({ result: jsonBytes(result) }) as never;

test("a conversation opens on its machine with bots/open in the setup, and its snapshot comes before its events", async () => {
  const h = hermeshd();
  const order: string[] = [];
  const rpc = new RpcClient(
    openCallTransport(herAdapter(h.local, "m_mini"), {
      setup: [{ method: "bots/open", params: { bot_id: "her", events: { exclude: ["text_delta"] } } }],
      onOpened: ([r]) => void order.push("result" in r ? `snapshot:${(r.result as { session_id: string }).session_id}` : "error"),
    }),
  );
  rpc.on("session/event", (p) => void order.push(`event:${p.event.seq}`));
  assert.deepEqual(h.opened.map((o) => [o.machineId, o.setup]), [["m_mini", [{ method: "bots/open", params: '{"bot_id":"her","events":{"exclude":["text_delta"]}}' }]]]);
  h.push({ case: "opened", value: { connectionId: "c1", responses: [response({ session_id: "s1" })] } as never });
  h.push({ case: "notification", value: { method: "session/event", params: jsonBytes({ session_id: "s1", event: { seq: 3, ts: 0, type: "idle" } }) } as never });
  await settle();
  await settle();
  assert.deepEqual(order, ["snapshot:s1", "event:3"]);
});

test("a request goes as HerCall on the connection the stream named, its JSON both ways", async () => {
  const h = hermeshd();
  h.answerWith((method) => ({ response: method === "models/list" ? response({ models: [], providers: [{ id: "deepseek" }] }) : response({}) }) as unknown as CallResponse);
  const rpc = new RpcClient(openCallTransport(herAdapter(h.local, "m_cloud")));
  h.push({ case: "opened", value: { connectionId: "c7", responses: [] } as never });
  const models = await rpc.request("models/list", {});
  assert.deepEqual(models, { models: [], providers: [{ id: "deepseek" }] });
  assert.deepEqual(h.calls, [{ machineId: "m_cloud", connectionId: "c7", method: "models/list", params: {} }]);
});

test("her's error comes back with its kind; hermeshd's own refusal fails just that request", async () => {
  const h = hermeshd();
  h.answerWith((method) => {
    if (method === "doc/open") throw new Error("[permission_denied] doc/open is not forwarded");
    return { response: { result: new Uint8Array(), error: { code: -32040, message: "bot b9 not found", data: jsonBytes({ kind: "bot_not_found" }) } } } as unknown as CallResponse;
  });
  const rpc = new RpcClient(openCallTransport(herAdapter(h.local, "m1")));
  h.push({ case: "opened", value: { connectionId: "c1", responses: [] } as never });
  await assert.rejects(rpc.request("bots/read", { bot_id: "b9" }), (e) => e instanceof RpcError && e.kind === "bot_not_found" && e.message === "bot b9 not found");
  await assert.rejects(rpc.request("doc/open", { session_id: "s", path: "/x" } as never), (e) => e instanceof RpcError && e.kind === "internal_error" && /not forwarded/.test(e.message));
  assert.equal(rpc.isClosed, false);
});

test("an answer with nothing in it is an empty result, and one missing is an error", () => {
  assert.deepEqual(callResult({ result: new Uint8Array() } as never), { result: {} });
  assert.deepEqual(callResult({ result: new Uint8Array(), error: { code: 1, message: "m", data: new Uint8Array() } } as never), { error: { code: 1, message: "m" } });
  assert.equal("error" in callResult(undefined), true);
});

test("closing the transport ends the stream", async () => {
  const h = hermeshd();
  const transport = openCallTransport(herAdapter(h.local, "m1"));
  transport.close();
  await settle();
  assert.equal(h.opened[0].signal.aborted, true);
});
