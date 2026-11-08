import assert from "node:assert/strict";
import { test } from "node:test";
import { detect, suggestName } from "./detect.ts";

// What the add sheet makes of its address box, and the name it offers.

test("a remote address: its host, whether a token would go in clear text, and the older transport", () => {
  assert.deepEqual(detect(" https://mcp.example.com/mcp "), { kind: "remote", url: "https://mcp.example.com/mcp", host: "mcp.example.com", insecure: false, sse: false, headers: [] });
  assert.equal((detect("http://tools.example.org/sse") as { insecure: boolean }).insecure, true);
  assert.equal((detect("http://tools.example.org/sse/") as { sse: boolean }).sse, true);
  assert.equal((detect("http://127.0.0.1:8080/mcp") as { insecure: boolean }).insecure, false);
  assert.deepEqual(detect("https://exa mple.com"), { kind: "bad", why: "url" });
  assert.deepEqual(detect("ftp://example.com"), { kind: "bad", why: "scheme" });
  assert.equal(detect("   "), null);
});

test("a command line: its words, and the variables before it, secrets locked", () => {
  assert.deepEqual(detect("GITHUB_TOKEN=ghp_1 LOG=debug npx -y @modelcontextprotocol/server-github"), {
    kind: "local",
    argv: ["npx", "-y", "@modelcontextprotocol/server-github"],
    env: [
      { key: "GITHUB_TOKEN", value: "ghp_1", locked: true },
      { key: "LOG", value: "debug", locked: false },
    ],
  });
  assert.deepEqual(detect("A=1 B=2"), { kind: "bad", why: "onlyVars" });
});

test("JSON as other apps write it: one server of a table, its name and headers or variables", () => {
  const remote = detect('{"mcpServers": {"linear": {"url": "https://mcp.linear.app/mcp", "headers": {"Authorization": "Bearer x"}}}}');
  assert.deepEqual(remote, { kind: "remote", url: "https://mcp.linear.app/mcp", host: "mcp.linear.app", insecure: false, sse: false, json: true, name: "linear", headers: [{ key: "Authorization", value: "Bearer x", locked: true }] });
  const local = detect('{"command": "uvx", "args": ["mcp-server-fetch"], "env": {"API_KEY": "k"}}');
  assert.deepEqual(local, { kind: "local", argv: ["uvx", "mcp-server-fetch"], env: [{ key: "API_KEY", value: "k", locked: true }], json: true, name: undefined });
  assert.deepEqual(detect("{nope"), { kind: "bad", why: "json" });
  assert.deepEqual(detect('{"servers": {"x": {"type": "stdio"}}}'), { kind: "bad", why: "jsonServer" });
});

test("the name offered: the host's telling part, the package's, or none for an address by number", () => {
  assert.equal(suggestName(detect("https://mcp.linear.app/mcp")), "linear");
  assert.equal(suggestName(detect("https://api.githubcopilot.com/mcp/")), "githubcopilot");
  assert.equal(suggestName(detect("npx -y @modelcontextprotocol/server-github")), "github");
  assert.equal(suggestName(detect("uvx mcp-server-fetch")), "fetch");
  assert.equal(suggestName(detect("npx -y @acme/mcp@1.2.0")), "acme");
  assert.equal(suggestName(detect("node ./tools/weather-mcp.js")), "weather");
  assert.equal(suggestName(detect('{"mcpServers": {"My Tools": {"url": "https://x.example.com/mcp"}}}')), "My Tools");
  assert.equal(suggestName(detect("http://127.0.0.1:8080/mcp")), "");
  assert.equal(suggestName(detect("http://[::1]:8080/mcp")), "");
  assert.equal(suggestName(detect("ftp://x")), "");
});
