import type { Transport } from "../rpc.ts";
import { MockServer } from "./server.ts";

export { MockServer, type Connection, type MockOptions, type Peer } from "./server.ts";
export { parseStress, type StressOptions } from "./stress.ts";

/** A transport to a mock server in the same process: a page previewed in a browser, or a test. */
export function mockTransport(server: MockServer = new MockServer()): Transport & { server: MockServer } {
  const listeners = new Set<(text: string) => void>();
  const conn = server.connect({ write: (text) => listeners.forEach((f) => f(text)) });
  return {
    server,
    send: (text) => queueMicrotask(() => conn.receive(text)),
    onText: (cb) => {
      listeners.add(cb);
      return () => void listeners.delete(cb);
    },
    onClose: () => () => {},
  };
}
