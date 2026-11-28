import { test } from "node:test";
import assert from "node:assert/strict";
import { accountFootModel, emailPrefix } from "./accountFoot.ts";

test("emailPrefix extracts the local part before @ and keeps plain handles", () => {
  assert.equal(emailPrefix("yipengfei@gmail.com"), "yipengfei");
  assert.equal(emailPrefix("  alex.chen@company.co.jp "), "alex.chen");
  assert.equal(emailPrefix("plain-handle"), "plain-handle");
  assert.equal(emailPrefix("@leading"), "@leading");
  assert.equal(emailPrefix(""), "");
});

test("accountFootModel: hosted account uses email prefix, https avatar, and relay host", () => {
  const m = accountFootModel(
    {
      initialized: true,
      hosted: true,
      account: "yipengfei@gmail.com",
      accountAvatarUrl: "https://avatars.githubusercontent.com/u/1?v=4",
      relayUrl: "https://relay.hermesh.io",
      relayConnected: true,
    },
    "mac-user",
  );
  assert.deepEqual(m, {
    signedIn: true,
    hosted: true,
    displayName: "yipengfei",
    email: "yipengfei@gmail.com",
    avatarUrl: "https://avatars.githubusercontent.com/u/1?v=4",
    initial: "Y",
    serverHost: "relay.hermesh.io",
    linkState: "connected",
  });
});

test("accountFootModel: non-https avatar URLs are rejected", () => {
  const m = accountFootModel(
    {
      initialized: true,
      hosted: true,
      account: "alex@example.com",
      accountAvatarUrl: "http://insecure.example.com/a.png",
      relayUrl: "https://hermesh.io",
      relayConnected: false,
      relayHealth: { phase: "reconnecting" },
    },
    "alex",
  );
  assert.equal(m.avatarUrl, "");
  assert.equal(m.linkState, "reconnecting");
});

test("accountFootModel: self-hosted server shows local username and keeps server host for hover", () => {
  const m = accountFootModel(
    {
      initialized: true,
      hosted: false,
      account: "ignored@example.com",
      accountAvatarUrl: "https://example.com/a.png",
      relayUrl: "https://relay.internal.example.com:8443",
      relayConnected: true,
    },
    "yipengfei",
  );
  assert.deepEqual(m, {
    signedIn: true,
    hosted: false,
    displayName: "yipengfei",
    email: "",
    avatarUrl: "",
    initial: "Y",
    serverHost: "relay.internal.example.com:8443",
    linkState: "connected",
  });
});

test("accountFootModel: signed-out and uninitialized profiles stay in local mode", () => {
  const uninit = accountFootModel({ initialized: false, relayUrl: "https://hermesh.io" }, "yipengfei");
  assert.equal(uninit.signedIn, false);
  assert.equal(uninit.displayName, "");
  assert.equal(uninit.serverHost, "");
  assert.equal(uninit.linkState, "local");

  const signedOut = accountFootModel({ initialized: true, signedOut: true, hosted: true, account: "a@b.com", relayUrl: "https://hermesh.io" }, "yipengfei");
  assert.equal(signedOut.signedIn, false);
  assert.equal(signedOut.email, "");
  assert.equal(signedOut.serverHost, "");
  assert.equal(signedOut.linkState, "local");
});

test("accountFootModel: machineOnly counts as signed in", () => {
  const m = accountFootModel({ initialized: false, machineOnly: true, hosted: false, relayUrl: "https://relay.example.com", relayConnected: false }, "worker");
  assert.equal(m.signedIn, true);
  assert.equal(m.displayName, "worker");
  assert.equal(m.serverHost, "relay.example.com");
  assert.equal(m.linkState, "offline");
});
