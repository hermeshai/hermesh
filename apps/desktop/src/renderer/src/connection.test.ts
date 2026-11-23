import { test } from "node:test";
import assert from "node:assert/strict";
import { connectionNotice, retrying, type ConnectionFacts } from "./connection.ts";

const up: ConnectionFacts = { noNetwork: false, relayUp: true, relayDownFor: 0, machineLocal: false, machineOnline: true, machineUnreachable: false, linkUp: true, linkDownFor: 0 };
/** A page's connection down for `ms`, on a machine online, with the relay up. */
const down = (ms: number, more: Partial<ConnectionFacts> = {}) => connectionNotice({ ...up, linkUp: false, linkDownFor: ms, ...more });

test("connectionNotice: nothing while the page's own connection is up, whatever else is down", () => {
  assert.equal(connectionNotice(up), null);
  assert.equal(connectionNotice({ ...up, noNetwork: true, relayUp: false, relayDownFor: 60_000, machineOnline: false }), null);
});

test("connectionNotice: a short wait goes unsaid, then connecting", () => {
  assert.equal(down(0), null);
  assert.equal(down(900), null);
  assert.equal(down(1000), "connecting");
  assert.equal(down(14_000), "connecting");
});

test("connectionNotice: a machine online that doesn't answer, after a while, or at once when hermeshd says so", () => {
  assert.equal(down(15_000), "notResponding");
  assert.equal(down(0, { machineUnreachable: true }), "notResponding");
});

test("connectionNotice: a machine offline at once; no network before anything", () => {
  assert.equal(down(0, { machineOnline: false }), "offline");
  assert.equal(down(0, { noNetwork: true, machineOnline: false }), "noNetwork");
});

test("connectionNotice: while the relay is down, the relay is what is said, after a while", () => {
  assert.equal(down(3000, { relayUp: false, relayDownFor: 3000, machineOnline: false }), "connecting");
  assert.equal(down(30_000, { relayUp: false, relayDownFor: 9000 }), "connecting");
  assert.equal(down(30_000, { relayUp: false, relayDownFor: 10_000 }), "cantReachHermesh");
});

test("connectionNotice: this Mac's own machine needs no relay", () => {
  assert.equal(down(60_000, { machineLocal: true, relayUp: false, relayDownFor: 60_000, machineOnline: false }), "connecting");
  assert.equal(down(500, { machineLocal: true }), null);
});

test("retrying: what shimmers", () => {
  assert.deepEqual((["noNetwork", "connecting", "cantReachHermesh", "offline", "notResponding"] as const).filter(retrying), ["connecting", "cantReachHermesh", "notResponding"]);
});
