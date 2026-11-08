#!/usr/bin/env node
// cargo build -p her-cli && node crates/her-connections/tests/chatgpt-e2e.mjs
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fixture, until } from "./chatgpt-fixture.mjs";
const f = await fixture();
try {
  const k = await f.kernel(); // stays alive while independent CLI processes change credentials
  console.log("OAuth denial, JWT verification, scope, and persistent issued client ID");
  for (const mode of ["denied", "exchange-fail", "signature", "nonce", "audience", "issuer", "expired", "scope"]) {
    f.state.mode = mode;
    const r = await f.login(); assert.notEqual(r.code, 0, mode);
    assert.match(r.err, mode === "denied" ? /not authorized/ : mode === "exchange-fail" ? /Sign in.*again/ : mode === "scope" ? /Authorize ChatGPT plan usage/ : /identity verification failed/, mode);
    const profile = JSON.parse(fs.readFileSync(path.join(f.home, "connections/chatgpt/profile.json")));
    assert.equal(profile.client_id, "fixture-issued-client"); assert.equal(profile.enabled, false);
    assert.equal((await k.call("chatgpt/status")).connection, null);
  }
  assert.equal(f.state.authorizations[0].client_id, "dynamic_agent_client");
  assert.ok(f.state.authorizations.slice(1).every(a => a.client_id === "fixture-issued-client"));
  assert.equal(new Set(f.state.authorizations.map(a => a.ext_agent_host_id)).size, 1);
  console.log("Desktop RPC login: bad callback state, concurrent login, cancel and closed listener");
  let started = await k.call("chatgpt/login");
  const auth = new URL(started.authorize_url), callback = new URL(auth.searchParams.get("redirect_uri"));
  callback.searchParams.set("state", "wrong");
  assert.equal((await fetch(callback)).status, 400);
  assert.equal((await k.call("chatgpt/status")).state, "pending");
  assert.notEqual((await f.processFor(["models", "login", "chatgpt-api"]).done).code, 0);
  await k.call("chatgpt/cancel", { login_id: started.login_id });
  await until(async () => (await k.call("chatgpt/status")).state === "cancelled", "cancelled");
  await assert.rejects(fetch(callback));
  console.log("Cancellation during token exchange never activates a connection");
  f.state.mode = "ok"; f.state.tokenDelay = 400;
  started = await k.call("chatgpt/login");
  const exchanges = f.state.exchanges;
  await f.authorize(started.authorize_url);
  await until(() => f.state.exchanges > exchanges, "token exchange started");
  await k.call("chatgpt/cancel", { login_id: started.login_id });
  await until(async () => (await k.call("chatgpt/status")).state === "cancelled", "exchange cancellation");
  assert.equal((await k.call("chatgpt/status")).connection, null);
  assert.equal(f.state.revocations, 1);
  f.state.revocations = 0; f.state.tokenDelay = 0;
  console.log("CLI sign-in, shared desktop catalog, tools and encrypted reasoning replay");
  f.state.mode = "ok"; f.state.expires = 1;
  const logged = await f.login(); assert.equal(logged.code, 0, logged.err);
  const status = await k.call("chatgpt/status"); assert.equal(status.connection.state, "ready");
  assert.equal(status.account, "tester@example.invalid"); assert.equal(status.connection.models.length, 1);
  const model = `${status.connection.provider_id}/gpt-test`;
  assert.ok((await k.call("models/list")).models.some(m => m.id === model && m.available));
  f.state.responseMode = "tool";
  // Both processes see the expired token, but only one may rotate it.
  const chats = await Promise.all([f.cli(["chat", "hello", "--model", model, "--tool-test", "--thinking", "high", "--system", "custom system"]), f.cli(["chat", "hello", "--model", model, "--tool-test"])]);
  assert.equal(f.state.refreshes, 1); for (const c of chats) assert.match(c.out, /subscription-pong/);
  assert.ok(f.state.responses.some(r => r.instructions === "custom system" && r.reasoning.effort === "high"));
  fs.writeFileSync(path.join(f.work, "sample.txt"), "fixture read content");
  const task = await f.cli(["exec", "read the fixture", "--model", model, "--no-save", "--output", "json"]);
  assert.match(task.out, /subscription-pong/);
  console.log("One refresh after 401; quota and truncated streams fail without fallback");
  f.state.responseMode = "text"; f.state.rejectNext = true;
  await f.cli(["chat", "hello", "--model", model]); assert.equal(f.state.refreshes, 2);
  for (const mode of ["quota-http", "quota-stream", "eof"]) {
    f.state.responseMode = mode; const before = f.state.responses.length;
    const r = await f.cli(["chat", "hello", "--model", model], 1);
    assert.match(r.err, mode === "eof" ? /without response.completed/ : /plan usage limit/i);
    assert.equal(f.state.responses.length, before + 1, "permanent errors cannot retry or use an API key");
  }
  console.log("Refresh revocation, reauthentication, model-list failure recovery, and logout");
  f.state.responseMode = "text"; f.state.rejectNext = true; f.state.invalidGrant = true;
  await f.cli(["chat", "hello", "--model", model], 1);
  assert.equal((await k.call("chatgpt/status")).connection.state, "needs_login");
  f.state.invalidGrant = false; f.state.expires = 3600; f.state.modelFailure = true;
  const failedCatalog = await f.login(); assert.notEqual(failedCatalog.code, 0);
  f.state.modelFailure = false;
  await k.call("connections/models", { connection_id: "chatgpt-api" });
  assert.equal((await k.call("chatgpt/status")).connection.state, "ready");
  const publicData = JSON.stringify(await k.call("chatgpt/status")) + JSON.stringify(await k.call("connections/list")) + JSON.stringify(await k.call("models/list"));
  assert.ok(!/access-login-|access-refresh-|refresh-login-|refresh-rotated-|id_token|code_verifier/.test(publicData));
  for (const name of ["profile.json", "tokens.toml"]) assert.equal(fs.statSync(path.join(f.home, "connections/chatgpt", name)).mode & 0o777, 0o600);
  const end = await k.call("chatgpt/logout"); assert.equal(end.connection, null); assert.equal(f.state.revocations, 1);
  assert.ok(!(await k.call("models/list")).models.some(m => m.id === model));
  const after = JSON.parse((await f.cli(["models", "status", "chatgpt-api"])).out); assert.equal(after.connection, null);
  console.log("Sign-out retains registration; explicit removal allows a different account");
  let profile = JSON.parse(fs.readFileSync(path.join(f.home, "connections/chatgpt/profile.json")));
  assert.equal(profile.subject, "fixture-account"); assert.equal(profile.client_id, "fixture-issued-client");
  const host = profile.installation;
  assert.equal((await f.login()).code, 0);
  assert.equal((await k.call("chatgpt/status")).connection.provider_id, status.connection.provider_id);
  assert.equal(f.state.authorizations.at(-1).client_id, "fixture-issued-client");
  await assert.rejects(k.call("connections/remove", { connection_id: "chatgpt-api" }), /Disconnect/);
  f.state.mode = "revoke-fail";
  const localLogout = await k.call("chatgpt/logout"); assert.match(localLogout.message, /Remote revocation failed/);
  assert.equal(localLogout.connection, null);
  await k.call("connections/remove", { connection_id: "chatgpt-api" });
  profile = JSON.parse(fs.readFileSync(path.join(f.home, "connections/chatgpt/profile.json")));
  assert.equal(profile.client_id, null); assert.equal(profile.installation, host);
  f.state.mode = "identity";
  assert.equal((await f.login()).code, 0);
  assert.equal(f.state.authorizations.at(-1).client_id, "dynamic_agent_client");
  assert.notEqual((await k.call("chatgpt/status")).connection.provider_id, status.connection.provider_id);
  await f.cli(["models", "logout", "chatgpt-api"]);
  await f.cli(["models", "forget", "chatgpt-api"]);
  assert.deepEqual(f.state.errors.map(e => e.message), []);
  console.log("PASS: real CLI + kernel RPC, OAuth/JWT, cross-process rotation, stateless tool loop, failure paths, logout");
} finally { await f.close(); }
