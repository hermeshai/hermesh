import assert from "node:assert/strict";
import { test } from "node:test";
import { RpcClient, RpcError, type Transport } from "./rpc.ts";

/** A transport whose other end the test plays. */
function pipe() {
  const sent: string[] = [];
  const listeners = new Set<(text: string) => void>();
  const closers = new Set<() => void>();
  const t: Transport = {
    send: (text) => void sent.push(text),
    onText: (cb) => (listeners.add(cb), () => void listeners.delete(cb)),
    onClose: (cb) => (closers.add(cb), () => void closers.delete(cb)),
  };
  return { t, sent, push: (text: string) => listeners.forEach((f) => f(text)), close: () => closers.forEach((f) => f()) };
}

test("a request goes out as one NDJSON line and resolves with its response", async () => {
  const p = pipe();
  const rpc = new RpcClient(p.t);
  const r = rpc.request("session/list", {});
  assert.equal(p.sent.length, 1);
  assert.ok(p.sent[0].endsWith("\n"));
  const msg = JSON.parse(p.sent[0]);
  assert.deepEqual(msg, { jsonrpc: "2.0", id: 1, method: "session/list", params: {} });
  p.push(`{"jsonrpc":"2.0","id":1,"result":{"sessions":[]}}\n`);
  assert.deepEqual(await r, { sessions: [] });
});

test("lines split across chunks, and several in one chunk, are read whole", async () => {
  const p = pipe();
  const rpc = new RpcClient(p.t);
  const got: number[] = [];
  rpc.on("session/event", (e) => void got.push(e.event.seq));
  const a = rpc.request("session/list", {});
  const line = (seq: number) => JSON.stringify({ jsonrpc: "2.0", method: "session/event", params: { session_id: "s", event: { seq, ts: 0, type: "warning", message: "x" } } }) + "\n";
  const all = line(1) + line(2) + `{"jsonrpc":"2.0","id":1,"result":{"sessions":[]}}\n` + line(3);
  p.push(all.slice(0, 17));
  p.push(all.slice(17, 140));
  p.push(all.slice(140));
  await a;
  assert.deepEqual(got, [1, 2, 3]);
});

test("an error response rejects with its kind", async () => {
  const p = pipe();
  const rpc = new RpcClient(p.t);
  const r = rpc.request("session/attach", { session_id: "nope" });
  p.push(`{"jsonrpc":"2.0","id":1,"error":{"code":-32002,"message":"no session","data":{"kind":"session_not_found"}}}\n`);
  await assert.rejects(r, (e) => e instanceof RpcError && e.kind === "session_not_found" && e.code === -32002);
});

test("pending requests fail when the connection closes, and later ones at once", async () => {
  const p = pipe();
  const rpc = new RpcClient(p.t);
  const r = rpc.request("session/list", {});
  p.close();
  await assert.rejects(r, (e) => e instanceof RpcError && e.kind === "disconnected");
  await assert.rejects(rpc.request("session/list", {}), (e) => e instanceof RpcError && e.kind === "disconnected");
});

test("a listener stops when asked", () => {
  const p = pipe();
  const rpc = new RpcClient(p.t);
  let n = 0;
  const off = rpc.on("sessions/changed", () => void n++);
  const line = `{"jsonrpc":"2.0","method":"sessions/changed","params":{"sessions":[],"deleted":[]}}\n`;
  p.push(line);
  off();
  p.push(line);
  assert.equal(n, 1);
});

test("the server's requests are answered by their handler, its errors as error responses", async () => {
  const p = pipe();
  const rpc = new RpcClient(p.t);
  rpc.handle("docx/serialize", async (params) => {
    if (params.out_path === "/bad") throw new Error("bad state");
    return { bytes: 42 };
  });
  const params = (out: string) => ({ source_path: "/s", state_path: "/t", out_path: out });
  p.push(JSON.stringify({ jsonrpc: "2.0", id: 1, method: "docx/serialize", params: params("/o") }) + "\n");
  p.push(JSON.stringify({ jsonrpc: "2.0", id: 2, method: "docx/serialize", params: params("/bad") }) + "\n");
  p.push(JSON.stringify({ jsonrpc: "2.0", id: 3, method: "nobody/handles" }) + "\n");
  await new Promise((r) => setImmediate(r));
  const replies = p.sent.map((s) => JSON.parse(s)).sort((a, b) => a.id - b.id);
  assert.deepEqual(replies[0], { jsonrpc: "2.0", id: 1, result: { bytes: 42 } });
  assert.equal(replies[1].error.message, "bad state");
  assert.equal(replies[2].error.code, -32601);
});
