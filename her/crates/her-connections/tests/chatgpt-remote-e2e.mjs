#!/usr/bin/env node
// cargo build -p her-cli && node crates/her-connections/tests/chatgpt-remote-e2e.mjs
// A ChatGPT sign-in that starts and ends on a machine without a browser (docs/her-server.md §5.4),
// through the kernel's RPC alone: the browser goes back to the client's device, which completes it.
import assert from "node:assert/strict";
import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import { fixture, until } from "./chatgpt-fixture.mjs";
const f = await fixture();
// The client's device: the browser lands on its loopback port, as it would on the app's.
const device = http.createServer((req, res) => { device.caught = Object.fromEntries(new URL(req.url, "http://device").searchParams); res.end("caught"); });
await new Promise(r => device.listen(0, "127.0.0.1", r));
const redirect = `http://127.0.0.1:${device.address().port}/callback`;
const profile = () => JSON.parse(fs.readFileSync(path.join(f.home, "connections/chatgpt/profile.json")));
try {
  const k = await f.kernel();
  // The browser signs in and is sent to the device; what it brings is what the client completes with.
  const browse = async url => { assert.equal(await (await fetch(url)).text(), "caught"); return device.caught; };

  console.log("Only an address on the client's own device is taken");
  for (const elsewhere of ["https://evil.example/callback", "http://192.168.1.20:8080/callback", "http://127.0.0.1/callback"]) {
    await assert.rejects(k.call("chatgpt/login", { redirect_uri: elsewhere }), e => e.code === -32602, elsewhere);
  }

  console.log("Asked again, the same sign-in; any other waits its turn");
  let started = await k.call("chatgpt/login", { redirect_uri: redirect });
  assert.equal(started.state, "pending");
  const authorize = new URL(started.authorize_url);
  assert.equal(authorize.searchParams.get("redirect_uri"), redirect);
  assert.equal(authorize.searchParams.get("client_id"), "dynamic_agent_client");
  assert.equal((await k.call("chatgpt/login", { redirect_uri: redirect })).login_id, started.login_id);
  await assert.rejects(k.call("chatgpt/login", { redirect_uri: "http://127.0.0.1:1/other" }), /cancel it first/);
  await assert.rejects(k.call("chatgpt/login"), /cancel it first/);

  console.log("A state that is not the sign-in's changes nothing; the right one connects, keeping the client ChatGPT named");
  let caught = await browse(started.authorize_url);
  assert.equal(caught.client_id, "fixture-issued-client");
  await assert.rejects(k.call("chatgpt/complete", { login_id: started.login_id, code: caught.code, state: "wrong" }), e => e.data?.kind === "invalid_state");
  assert.equal((await k.call("chatgpt/status")).state, "pending");
  let done = await k.call("chatgpt/complete", { login_id: started.login_id, code: caught.code, state: caught.state, client_id: caught.client_id });
  assert.equal(done.state, "connected", done.message);
  assert.equal(done.account, "tester@example.invalid");
  assert.equal(done.connection.state, "ready");
  assert.equal(profile().client_id, "fixture-issued-client");
  const provider = done.connection.provider_id;
  assert.ok((await k.call("models/list")).models.some(m => m.id === `${provider}/gpt-test` && m.available));
  await assert.rejects(k.call("chatgpt/complete", { login_id: "another", code: caught.code, state: caught.state }), e => e.data?.kind === "not_found");

  console.log("Signed out and in again: the kept client is used, so the redirect's client_id may be left out");
  await k.call("chatgpt/logout");
  started = await k.call("chatgpt/login", { redirect_uri: redirect });
  assert.equal(new URL(started.authorize_url).searchParams.get("client_id"), "fixture-issued-client");
  caught = await browse(started.authorize_url);
  done = await k.call("chatgpt/complete", { login_id: started.login_id, code: caught.code, state: caught.state });
  assert.equal(done.state, "connected", done.message);
  assert.equal(done.connection.provider_id, provider, "the same account keeps its provider");

  console.log("Cancelled, a remote sign-in ends as one on this machine does");
  await k.call("chatgpt/logout");
  started = await k.call("chatgpt/login", { redirect_uri: redirect });
  await k.call("chatgpt/cancel", { login_id: started.login_id });
  await until(async () => (await k.call("chatgpt/status")).state === "cancelled", "remote cancellation");
  caught = await browse(started.authorize_url);
  const late = await k.call("chatgpt/complete", { login_id: started.login_id, code: caught.code, state: caught.state });
  assert.equal(late.state, "cancelled");
  assert.equal(late.connection, null);

  const publicData = JSON.stringify(await k.call("chatgpt/status")) + JSON.stringify(await k.call("models/list"));
  assert.ok(!/access-login-|refresh-login-|id_token|code_verifier/.test(publicData));
  assert.deepEqual(f.state.errors.map(e => e.message), []);
  console.log("PASS: remote ChatGPT sign-in through the kernel RPC: loopback-only redirects, state check, issued client, cancel");
} finally { device.closeAllConnections(); device.close(); await f.close(); }
