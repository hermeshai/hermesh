import assert from "node:assert/strict";
import { test } from "node:test";
import { localIdentity, remoteIdentity } from "./identity.ts";

// The same cases as her/crates/her-mcp/src/identity.rs: the two must agree.
test("the same service under other spellings has one identity", () => {
  const one = remoteIdentity("https://mcp.hermesh.io/mcp");
  assert.equal(one, "https://mcp.hermesh.io/mcp");
  assert.equal(remoteIdentity("HTTPS://MCP.Hermesh.io:443/mcp/"), one);
  assert.equal(remoteIdentity("https://mcp.hermesh.io/mcp#x"), one);
  assert.notEqual(remoteIdentity("https://mcp.hermesh.io/mcp?team=b"), one);
  assert.notEqual(remoteIdentity("https://mcp.hermesh.io/sse"), one);
  assert.equal(remoteIdentity("http://127.0.0.1:8080/mcp/"), "http://127.0.0.1:8080/mcp");
  assert.equal(localIdentity("npx", ["-y", "@acme/server"]), localIdentity("npx", ["@acme/server"]));
  assert.notEqual(localIdentity("npx", ["@acme/server"]), localIdentity("npx", ["@acme/other"]));
});
