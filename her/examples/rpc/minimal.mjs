#!/usr/bin/env node
// Drives her over its JSON-RPC protocol: start a session, send a task, answer its approvals,
// print the reply. Needs nothing but Node and her.
//
//     node minimal.mjs "fix the failing test"
//     node minimal.mjs "fix the failing test" /path/to/her

import { spawn } from "node:child_process";
import { createInterface } from "node:readline";

const prompt = process.argv[2] ?? "say hello";
const program = process.argv[3] ?? "her";

const server = spawn(program, ["serve", "--stdio"], { stdio: ["pipe", "pipe", "inherit"] });
const lines = createInterface({ input: server.stdout });

const waiting = new Map();
let nextId = 0;
const call = (method, params) =>
  new Promise((resolve, reject) => {
    const id = ++nextId;
    waiting.set(id, { resolve, reject });
    server.stdin.write(JSON.stringify({ jsonrpc: "2.0", id, method, params }) + "\n");
  });

let session;
const reply = [];
let finished;
const done = new Promise((resolve, reject) => (finished = { resolve, reject }));

lines.on("line", (line) => {
  const message = JSON.parse(line);
  if (message.id !== undefined && waiting.has(message.id)) {
    const { resolve, reject } = waiting.get(message.id);
    waiting.delete(message.id);
    message.error ? reject(new Error(message.error.message)) : resolve(message.result);
    return;
  }
  if (message.method !== "session/event") return;
  const event = message.params.event;
  if (event.type === "message_delta" && event.delta.type === "text") {
    reply.push(event.delta.text);
  } else if (event.type === "approval_requested") {
    // A real client would ask someone. This one says yes to everything.
    console.error(`approving: ${event.summary}`);
    const op = { type: "approval", call_id: event.call_id, decision: { type: "approve" } };
    call("session/op", { session_id: message.params.session_id, op }).catch(finished.reject);
  } else if (event.type === "task_failed") {
    finished.reject(new Error(event.error.message));
  } else if (event.type === "idle") {
    finished.resolve();
  }
});

try {
  await call("initialize", { protocol_version: 2, client_info: { name: "minimal.mjs" } });
  session = (await call("session/new", { cwd: process.cwd() })).session_id;
  const input = [{ type: "text", text: prompt }];
  await call("session/op", { session_id: session, op: { type: "prompt", input } });
  await done;
  console.log(reply.join(""));
  await call("session/close", { session_id: session });
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
server.stdin.end();
