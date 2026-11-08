import assert from "node:assert/strict";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { test } from "node:test";
import { PROTOCOL_VERSION, type Event } from "@hermesh/her-protocol";
import { botChat } from "./botChat.ts";
import { httpTransport } from "./http.ts";
import { MockServer, type Connection } from "./mock/server.ts";
import { RpcClient, RpcError } from "./rpc.ts";
import { applyEvent, fromSnapshot } from "./session.ts";

// The console's way to her end to end: httpTransport with the platform's own EventSource
// and fetch (node --experimental-eventsource), over real HTTP, to a stand-in for hermeshd's
// console endpoints (proto/hermesh/her/v1/her.proto), with the mock her behind it.

async function body(req: IncomingMessage): Promise<string> {
  let text = "";
  for await (const chunk of req) text += chunk;
  return text;
}

/**
 * hermeshd's console HTTP in front of `her`: each GET of `events` is a connection of its own,
 * initialized, then its notifications as events; a POST to `rpc` goes on the connection
 * `Her-Connection` names, under an id of hermeshd's, and its answer comes back under the
 * request's.
 */
async function consoleSite(her: MockServer) {
  type Open = { conn: Connection; waiting: Map<number, (answer: object) => void>; events: ServerResponse };
  const open = new Map<string, Open>();
  let connections = 0;
  let requests = 0;
  const forward = (c: Open, request: object): Promise<{ id?: unknown }> =>
    new Promise((resolve) => {
      const id = ++requests;
      c.waiting.set(id, resolve);
      c.conn.receive(JSON.stringify({ ...request, jsonrpc: "2.0", id }) + "\n");
    });
  const server = createServer(async (req, res) => {
    if (req.method === "GET" && req.url === "/her/console/events") {
      const id = `conn-${++connections}`;
      const c: Open = { waiting: new Map(), events: res, conn: undefined as unknown as Connection };
      c.conn = her.connect({
        write: (text) => {
          for (const line of text.split("\n").filter(Boolean)) {
            const msg = JSON.parse(line) as { id?: number; method?: string };
            if (msg.method === undefined && typeof msg.id === "number") c.waiting.get(msg.id)?.(msg);
            else res.write(`data: ${line}\n\n`);
          }
        },
      });
      open.set(id, c);
      req.on("close", () => {
        c.conn.close();
        open.delete(id);
      });
      await forward(c, { method: "initialize", params: { protocol_version: PROTOCOL_VERSION, client_info: { name: "hermeshd" } } });
      res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" });
      res.write(`event: opened\ndata: ${JSON.stringify({ connection_id: id })}\n\n`);
      return;
    }
    if (req.method === "POST" && req.url === "/her/console/rpc") {
      const c = open.get(String(req.headers["her-connection"]));
      if (!c) return void res.writeHead(404).end("no such connection");
      const request = JSON.parse(await body(req)) as { id: number };
      const answer = await forward(c, request);
      res.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify({ ...answer, id: request.id }));
      return;
    }
    res.writeHead(404).end();
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  return {
    base: `http://127.0.0.1:${(server.address() as AddressInfo).port}/her/console/`,
    /** Every connection drops, as when the tunnel goes. */
    drop: () => {
      for (const c of open.values()) c.events.end();
    },
    close: () => {
      server.closeAllConnections();
      server.close();
    },
  };
}

test("over HTTP: talking to Her, her's errors, and a new connection after the old one drops", { timeout: 30_000 }, async () => {
  const site = await consoleSite(new MockServer({ speed: 400 }));
  let rpc = new RpcClient(httpTransport(site.base));
  const init = await rpc.request("initialize", { protocol_version: PROTOCOL_VERSION });
  assert.equal(init.server_info.name, "her");
  const { bots } = await rpc.request("bots/subscribe", {});
  const her = bots.find((b) => b.default);
  assert.equal(her?.name, "Her");

  const { session_id, snapshot } = await rpc.request("bots/open", { bot_id: her.id });
  let view = fromSnapshot(snapshot);
  const answered = new Promise<void>((resolve) =>
    rpc.on("session/event", ({ event }) => {
      view = applyEvent(view, event as Event);
      if (event.type === "task_completed") resolve();
    }),
  );
  await rpc.request("session/op", { session_id, op: { type: "prompt", input: [{ type: "text", text: "写一首冬天的诗" }] } });
  await answered;
  const said = botChat(view).at(-1);
  assert.ok(said?.kind === "bot" && said.text.startsWith("**冬**"), JSON.stringify(said));
  await assert.rejects(rpc.request("bots/open", { bot_id: "nobody" }), (e) => e instanceof RpcError && e.kind === "bot_not_found");

  const closed = new Promise<void>((resolve) => rpc.onClose(resolve));
  site.drop();
  await closed;
  const transport = httpTransport(site.base);
  rpc = new RpcClient(transport);
  const again = await rpc.request("bots/subscribe", {});
  assert.deepEqual(
    again.bots.map((b) => b.id),
    bots.map((b) => b.id),
  );
  transport.close();
  site.close();
});
