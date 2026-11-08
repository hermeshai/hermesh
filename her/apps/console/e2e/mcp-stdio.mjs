// The smallest MCP server over stdio, for a connector started as a command in the end-to-end
// test: it answers initialize, ping and tools/list (one tool), and nothing else.
import { createInterface } from "node:readline";

const answer = (id, result) => process.stdout.write(JSON.stringify({ jsonrpc: "2.0", id, result }) + "\n");
createInterface({ input: process.stdin }).on("line", (line) => {
  const { id, method, params } = JSON.parse(line);
  if (id === undefined) return;
  if (method === "initialize") answer(id, { protocolVersion: params?.protocolVersion ?? "2025-06-18", capabilities: { tools: {} }, serverInfo: { name: "hello", version: "1.0.0" } });
  else if (method === "tools/list") answer(id, { tools: [{ name: "say_hello", description: "Says hello to someone.", inputSchema: { type: "object", properties: { name: { type: "string" } } } }] });
  else if (method === "ping") answer(id, {});
  else process.stdout.write(JSON.stringify({ jsonrpc: "2.0", id, error: { code: -32601, message: `no ${method}` } }) + "\n");
});
