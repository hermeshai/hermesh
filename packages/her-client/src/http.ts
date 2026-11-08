import { openCallTransport, type CallError, type ConnectionEvent } from "./calls.ts";
import type { Transport } from "./rpc.ts";

// her through a console's HTTP, as hermeshd serves it (proto/hermesh/her/v1/her.proto): a
// connection is the server-sent events at `events`, first `opened` with its id, then one
// unnamed event per notification, its data the notification's JSON-RPC message. Each
// request is a POST to `rpc`, its body the JSON-RPC request and `Her-Connection` naming the
// connection; the answer is the JSON-RPC response.

/** A transport to her at `baseUrl`, the console's address with its last "/" ("/her/console/"). */
export function httpTransport(baseUrl: string): Transport & { close(): void } {
  let next = 1;
  return openCallTransport({
    open: (signal) => events(`${baseUrl}events`, signal),
    async call(connectionId, method, params) {
      const response = await fetch(`${baseUrl}rpc`, {
        method: "POST",
        headers: { "Content-Type": "application/json", "Her-Connection": connectionId },
        body: JSON.stringify({ jsonrpc: "2.0", id: next++, method, params }),
      });
      if (!response.ok) throw new Error(`${method}: HTTP ${response.status} ${await response.text()}`.trim());
      const reply = (await response.json()) as { result?: unknown } | { error: CallError };
      return "error" in reply ? { error: reply.error } : { result: reply.result };
    },
  });
}

/**
 * The events of an EventSource as a stream. It ends at the first error: an EventSource that
 * reconnects by itself would be on another connection, without what this one had opened.
 */
async function* events(url: string, signal: AbortSignal): AsyncGenerator<ConnectionEvent> {
  const source = new EventSource(url);
  const queue: (ConnectionEvent | null)[] = [];
  let wake = () => {};
  const push = (e: ConnectionEvent | null) => {
    queue.push(e);
    wake();
  };
  source.addEventListener("opened", (m) => push({ opened: { connectionId: JSON.parse(m.data).connection_id } }));
  source.onmessage = (m) => {
    const msg = JSON.parse(m.data) as { method: string; params?: unknown };
    push({ notification: { method: msg.method, params: msg.params } });
  };
  source.onerror = () => push(null);
  signal.addEventListener("abort", () => push(null));
  try {
    for (;;) {
      while (queue.length === 0) await new Promise<void>((resolve) => (wake = resolve));
      const e = queue.shift()!;
      if (e === null) return;
      yield e;
    }
  } finally {
    source.close();
  }
}
