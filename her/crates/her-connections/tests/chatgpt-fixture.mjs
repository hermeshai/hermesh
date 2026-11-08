// Real her processes against an isolated OAuth/Responses service. No real credentials.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createHash, generateKeyPairSync, sign } from "node:crypto";
import fs from "node:fs";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import readline from "node:readline";
export const root = path.resolve(import.meta.dirname, "../../..");
export const her = path.join(root, "target/debug/her");
export const sleep = ms => new Promise(r => setTimeout(r, ms));
export async function until(check, label, timeout = 15000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) { const value = await check(); if (value) return value; await sleep(60); }
  throw new Error(`Timed out: ${label}`);
}
export async function fixture() {
  const work = fs.mkdtempSync(path.join(os.tmpdir(), "fxc-"));
  const home = path.join(work, "her");
  fs.mkdirSync(home);
  fs.writeFileSync(path.join(home, "config.toml"), "[models]\nnetwork = false\n[browser]\nenabled = false\n");
  const { privateKey, publicKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
  const jwk = { ...publicKey.export({ format: "jwk" }), kid: "fixture-key", alg: "RS256", use: "sig" };
  const codes = new Map();
  const state = { mode: "ok", responseMode: "text", expires: 3600, tokenDelay: 0, refreshes: 0, revocations: 0, exchanges: 0, responses: [], authorizations: [], errors: [], token: null, refresh: null, rejectNext: false, invalidGrant: false, modelFailure: false };
  let base;
  const json = (res, value, status = 200) => res.writeHead(status, { "content-type": "application/json" }).end(JSON.stringify(value));
  const jwt = claims => {
    const enc = obj => Buffer.from(JSON.stringify(obj)).toString("base64url");
    const data = `${enc({ alg: "RS256", kid: jwk.kid })}.${enc(claims)}`;
    return `${data}.${sign("RSA-SHA256", Buffer.from(data), privateKey).toString("base64url")}`;
  };
  const server = http.createServer(async (req, res) => {
    try {
      let raw = ""; for await (const data of req) raw += data;
      const u = new URL(req.url, base);
      if (u.pathname === "/api/accounts/authorize") {
        if (state.mode === "pending") return res.writeHead(200, { "content-type": "text/plain" }).end("Her test: authorization is waiting. This test tab can be closed.");
        const q = Object.fromEntries(u.searchParams);
        state.authorizations.push(q);
        assert.equal(q.agent_name_hint, q.client_id === "dynamic_agent_client" ? "Hermesh" : undefined); assert.equal(q.code_challenge_method, "S256");
        assert.equal(q.resource, "https://api.openai.com/v1"); assert.ok(q.scope.split(" ").includes("chatgpt.tokens.use.direct"));
        assert.match(q.ext_agent_host_id, /^urn:uuid:/);
        assert.equal(new URL(q.redirect_uri).hostname, "127.0.0.1");
        const code = `code-${codes.size}`; codes.set(code, q);
        const target = new URL(q.redirect_uri); target.searchParams.set("state", q.state); target.searchParams.set("client_id", "fixture-issued-client");
        if (state.mode === "denied") target.searchParams.set("error", "access_denied"); else target.searchParams.set("code", code);
        return res.writeHead(302, { location: target.href }).end();
      }
      if (u.pathname === "/.well-known/openid-configuration") return json(res, { issuer: base, jwks_uri: `${base}/jwks`, revocation_endpoint: `${base}/revoke` });
      if (u.pathname === "/jwks") return json(res, { keys: [jwk] });
      if (u.pathname === "/revoke") { state.revocations++; return json(res, {}, state.mode === "revoke-fail" ? 500 : 200); }
      if (u.pathname === "/api/accounts/oauth/token") {
        const q = Object.fromEntries(new URLSearchParams(raw));
        assert.equal(q.client_id, "fixture-issued-client");
        if (q.grant_type === "refresh_token") {
          state.refreshes++;
          assert.equal(q.refresh_token, state.refresh, "refresh must use the newest rotating credential");
          if (state.invalidGrant) return json(res, { error: "invalid_grant" }, 400);
          await sleep(80); // make overlap from concurrent CLI requests observable
          state.token = `access-refresh-${state.refreshes}`; state.refresh = `refresh-rotated-${state.refreshes}`;
          return json(res, { access_token: state.token, refresh_token: state.refresh, expires_in: 3600, token_type: "Bearer" });
        }
        state.exchanges++;
        if (state.tokenDelay) await sleep(state.tokenDelay);
        const auth = codes.get(q.code); assert.ok(auth);
        assert.equal(q.redirect_uri, auth.redirect_uri);
        assert.equal(createHash("sha256").update(q.code_verifier).digest("base64url"), auth.code_challenge);
        if (state.mode === "exchange-fail") return json(res, { error: "invalid_grant" }, 400);
        const now = Math.floor(Date.now() / 1000);
        const claims = { iss: base, aud: q.client_id, sub: "fixture-account", email: "tester@example.invalid", nonce: auth.nonce, iat: now, exp: now + 3600 };
        if (state.mode === "nonce") claims.nonce = "wrong";
        if (state.mode === "audience") claims.aud = "wrong";
        if (state.mode === "issuer") claims.iss = "https://wrong.invalid";
        if (state.mode === "expired") claims.exp = now - 1;
        if (state.mode === "identity") claims.sub = "different-account";
        let id = jwt(claims);
        if (state.mode === "signature") { const parts = id.split("."); const bytes = Buffer.from(parts[2], "base64url"); bytes[0] ^= 1; id = `${parts[0]}.${parts[1]}.${bytes.toString("base64url")}`; }
        state.token = `access-login-${state.exchanges}`; state.refresh = `refresh-login-${state.exchanges}`;
        return json(res, { access_token: state.token, refresh_token: state.refresh, id_token: id, expires_in: state.expires, token_type: "Bearer", scope: state.mode === "scope" ? "openid" : auth.scope });
      }
      if (u.pathname.startsWith("/v1/")) assert.equal(req.headers.authorization, `Bearer ${state.token}`);
      if (u.pathname === "/v1/models") {
        if (state.modelFailure) return json(res, { error: { code: "unavailable" } }, 503);
        return json(res, { models: [
          { slug: "gpt-test", display_name: "ChatGPT Fixture", visibility: "list", context_window: 128000, input_modalities: ["text", "image"], supported_reasoning_levels: [{ effort: "low" }, { effort: "high" }], default_reasoning_level: "low" },
          { slug: "hidden-model", visibility: "hide" },
        ] });
      }
      if (u.pathname === "/v1/responses") {
        const b = JSON.parse(raw); state.responses.push(b);
        assert.equal(b.store, false); assert.equal(b.stream, true); assert.ok(Array.isArray(b.input));
        for (const key of ["previous_response_id", "temperature", "top_p", "max_output_tokens", "background"]) assert.equal(b[key], undefined, key);
        assert.ok(!b.input.some(i => i.role === "system"));
        if (state.rejectNext) { state.rejectNext = false; return json(res, { error: { code: "invalid_api_key", message: "expired" } }, 401); }
        if (state.responseMode === "quota-http") return json(res, { error: { code: "subscription_sharing_usage_limit_exceeded", message: "plan quota" } }, 429);
        res.writeHead(200, { "content-type": "text/event-stream" });
        const event = data => res.write(`data: ${JSON.stringify(data)}\n\n`);
        if (state.responseMode === "quota-stream" || state.responseMode === "eof") {
          event({ type: "response.output_text.delta", output_index: 0, delta: "partial" });
          if (state.responseMode === "quota-stream") event({ type: "response.failed", response: { error: { code: "subscription_sharing_usage_limit_exceeded", message: "plan quota" } } });
          return res.end();
        }
        const result = b.input.find(i => i.type === "function_call_output");
        let output;
        if (state.responseMode === "tool" && !result) {
          assert.equal(b.tools[0].type, "namespace"); assert.equal(b.tools[0].name, "her");
          const names = b.tools[0].tools.map(t => t.name);
          const tool = names.includes("get_time") ? "get_time" : "read";
          assert.ok(names.includes(tool));
          const call = { type: "function_call", id: "fc_fixture", namespace: "her", call_id: "call_fixture", name: tool, arguments: tool === "get_time" ? "{}" : JSON.stringify({ path: path.join(work, "sample.txt") }), status: "completed" };
          output = [{ type: "reasoning", id: "rs_fixture", encrypted_content: "encrypted-round-one", summary: [] }, call];
          event({ type: "response.output_item.added", output_index: 1, item: { ...call, arguments: "" } });
          event({ type: "response.function_call_arguments.delta", output_index: 1, delta: call.arguments });
        } else {
          if (state.responseMode === "tool") {
            assert.equal(result.call_id, "call_fixture");
            const call = b.input.find(i => i.type === "function_call");
            assert.equal(call.namespace, "her");
            if (call.name === "read") assert.match(JSON.stringify(result.output), /fixture read content/);
            assert.equal(b.input.find(i => i.type === "reasoning").encrypted_content, "encrypted-round-one");
          }
          output = [{ type: "message", role: "assistant", content: [{ type: "output_text", text: "subscription-pong" }] }];
          event({ type: "response.output_text.delta", output_index: 0, delta: "subscription-pong" });
        }
        event({ type: "response.completed", response: { id: "resp_fixture", status: "completed", output, usage: { input_tokens: 100, output_tokens: 10, input_tokens_details: { cached_tokens: 20 }, output_tokens_details: { reasoning_tokens: 2 } } } });
        return res.end();
      }
      res.writeHead(404).end();
    } catch (e) { state.errors.push(e); if (!res.headersSent) json(res, { error: { message: e.message } }, 500); else res.end(); }
  });
  await new Promise(r => server.listen(0, "127.0.0.1", r)); base = `http://127.0.0.1:${server.address().port}`;
  const env = { ...process.env, HER_HOME: home, HER_CREDENTIAL_STORE: "file", HER_TEST_CHATGPT_BASE: base };
  // Never let an inherited provider key affect model selection in the isolated fixture.
  for (const key of Object.keys(env)) if (key.endsWith("_API_KEY") || key === "HER_MODEL") delete env[key];
  const children = [];
  function processFor(args) {
    const child = spawn(her, args, { cwd: work, env, stdio: ["pipe", "pipe", "pipe"] }); children.push(child);
    const result = { out: "", err: "", child, done: null };
    child.stdout.on("data", b => result.out += b); child.stderr.on("data", b => result.err += b);
    result.done = new Promise((resolve, reject) => { child.on("error", reject); child.on("exit", code => resolve({ code, out: result.out, err: result.err })); });
    return result;
  }
  async function cli(args, code = 0) {
    const r = await processFor(args).done; assert.equal(r.code, code, `${args.join(" ")}: ${r.err}`); return r;
  }
  async function authorize(url) { const r = await fetch(url); assert.equal(r.status, 200); }
  async function login() {
    const p = processFor(["models", "login", "chatgpt-api"]);
    const url = await until(() => p.err.match(/http:\/\/127\.0\.0\.1:\d+\/api\/accounts\/authorize\?\S+/)?.[0], "CLI login URL");
    await authorize(url); return p.done;
  }
  async function kernel() {
    const p = processFor(["serve", "--stdio"]); const waiting = new Map(); let seq = 0;
    readline.createInterface({ input: p.child.stdout }).on("line", line => {
      const msg = JSON.parse(line); if (msg.id == null) return;
      const w = waiting.get(msg.id); if (!w) return; waiting.delete(msg.id); clearTimeout(w.timer); msg.error ? w.reject(Object.assign(new Error(msg.error.message), msg.error)) : w.resolve(msg.result);
    });
    const call = (method, params = {}) => new Promise((resolve, reject) => {
      const id = ++seq; const timer = setTimeout(() => { waiting.delete(id); reject(new Error(`RPC timeout: ${method}; ${p.err}`)); }, 30000);
      waiting.set(id, { resolve, reject, timer }); p.child.stdin.write(JSON.stringify({ jsonrpc: "2.0", id, method, params }) + "\n");
    });
    await call("initialize", { protocol_version: 2, client_info: { name: "chatgpt-e2e" } });
    return { call, child: p.child };
  }
  return { work, home, base, env, state, cli, processFor, authorize, login, kernel,
    async close() { for (const c of children) if (c.exitCode === null) c.kill(); server.closeAllConnections(); await new Promise(r => server.close(r)); fs.rmSync(work, { recursive: true, force: true }); },
  };
}
