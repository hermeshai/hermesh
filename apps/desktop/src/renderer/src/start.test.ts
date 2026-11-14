import { test } from "node:test";
import assert from "node:assert/strict";
import { startPage } from "./start.ts";

const on = { her: { enabled: true } };
const off = { her: { enabled: false } };

test("startPage: Her when she runs on a machine, whatever else holds", () => {
  assert.equal(startPage({ machines: [off, on], machineOnly: false, notNow: true, onSession: false }), "her");
});

test("startPage: her first run while she runs nowhere, until the owner says Not Now", () => {
  assert.equal(startPage({ machines: [off], machineOnly: false, notNow: false, onSession: false }), "her");
  assert.equal(startPage({ machines: [off], machineOnly: false, notNow: true, onSession: false }), "overview");
});

test("startPage: Work with no machine yet, and on a machine only", () => {
  assert.equal(startPage({ machines: [], machineOnly: false, notNow: false, onSession: false }), "overview");
  assert.equal(startPage({ machines: [on], machineOnly: true, notNow: false, onSession: false }), "overview");
});

test("startPage: a window that quit on a session opens on it again", () => {
  assert.equal(startPage({ machines: [on], machineOnly: false, notNow: false, onSession: true }), null);
  assert.equal(startPage({ machines: [], machineOnly: false, notNow: false, onSession: true }), null);
});
