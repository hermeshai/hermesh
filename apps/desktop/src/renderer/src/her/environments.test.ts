import assert from "node:assert/strict";
import { test } from "node:test";
import type { Machine } from "@hermesh/proto/local";
import { environments, envState, herChoices, newServer, pickEnvironment, unreadCount } from "./environments.ts";

type Her = NonNullable<Machine["her"]>;

/** A machine as Watch lists it: online unless said, Her as given (none when undefined). */
function machine(id: string, name: string, o: { local?: boolean; online?: boolean; her?: Partial<Her>; platform?: string; seen?: number } = {}): Machine {
  return {
    machineId: id,
    machineName: name,
    local: o.local ?? false,
    online: o.online ?? true,
    lastSeen: o.seen === undefined ? undefined : { seconds: BigInt(o.seen), nanos: 0 },
    status: o.platform ? { platform: o.platform } : undefined,
    her: o.her ? ({ enabled: false, running: false, version: "", modelReady: false, unsupported: "", unread: 0, error: "", ...o.her } as Her) : undefined,
  } as Machine;
}

const ready = { enabled: true, running: true, modelReady: true };

test("the environments are the machines Her is on for, this Mac first, then by name", () => {
  const list = [
    machine("m3", "cloud", { her: ready }),
    machine("m4", "work-mac", { her: { enabled: false } }),
    machine("m1", "this", { local: true, her: ready }),
    machine("m2", "Mac mini", { online: false, her: ready }),
    machine("m5", "no-her"),
  ];
  assert.deepEqual(environments(list).map((m) => m.machineId), ["m1", "m3", "m2"]);
});

test("the remembered environment is shown while it is one; else the first", () => {
  const envs = environments([machine("m1", "a", { her: ready }), machine("m2", "b", { her: ready })]);
  assert.equal(pickEnvironment(envs, "m2")?.machineId, "m2");
  assert.equal(pickEnvironment(envs, "m9")?.machineId, "m1");
  assert.equal(pickEnvironment(envs, undefined)?.machineId, "m1");
  assert.equal(pickEnvironment([], "m1"), undefined);
});

test("the sidebar's count is every environment's unread, as Watch brings it", () => {
  assert.equal(unreadCount([machine("m1", "a", { her: { ...ready, unread: 2 } }), machine("m2", "b", { her: { ...ready, unread: 3 } }), machine("m3", "c", { her: { enabled: false, unread: 9 } })]), 5);
});

test("an environment's state: offline with when it was seen, starting, failed, no model, ready; this Mac is never offline", () => {
  assert.deepEqual(envState(machine("m", "a", { online: false, seen: 1_800_000_000, her: ready })), { kind: "offline", seen: new Date(1_800_000_000_000) });
  assert.deepEqual(envState(machine("m", "a", { local: true, online: false, her: ready })), { kind: "ready" });
  assert.deepEqual(envState(machine("m", "a", { her: { enabled: true } })), { kind: "starting" });
  assert.deepEqual(envState(machine("m", "a", { her: { enabled: true, error: "her exited: status 1" } })), { kind: "failed", error: "her exited: status 1" });
  assert.deepEqual(envState(machine("m", "a", { her: { enabled: true, running: true } })), { kind: "noModel" });
});

test("the first run offers this Mac first, then machines that can run Her, then those that can't and why", () => {
  const choices = herChoices([
    machine("w", "gaming-pc", { platform: "windows/amd64" }),
    machine("o", "old-mac", { online: false, platform: "darwin/arm64" }),
    machine("c", "cloud", { platform: "linux/amd64" }),
    machine("l", "this", { local: true, platform: "darwin/arm64" }),
    machine("e", "already", { platform: "linux/amd64", her: ready }),
    machine("n", "nas", { platform: "linux/arm", her: { unsupported: "no her" } }),
  ]);
  assert.deepEqual(
    choices.map((c) => [c.machine.machineId, c.why, c.detail]),
    [
      ["l", null, undefined],
      ["c", null, undefined],
      ["w", "windows", undefined],
      ["n", "unsupported", "no her"],
      ["o", "offline", undefined],
    ],
  );
  // A PC running the app: its own machine can't run Her either.
  assert.equal(herChoices([machine("l", "this", { local: true, platform: "windows/amd64" })])[0].why, "windows");
  // Her says it can't run there, whatever the platform says.
  assert.equal(herChoices([machine("x", "box", { her: { unsupported: "windows" } })])[0].why, "windows");
});

test("a new server is offered once it is paired, online and able to run Her; never a machine there before", () => {
  const before = new Set(["l", "c"]);
  const there = [machine("l", "this", { local: true, platform: "darwin/arm64" }), machine("c", "cloud", { platform: "linux/amd64" })];
  assert.equal(newServer(there, before), undefined);
  assert.equal(newServer([...there, machine("n", "her-1", { online: false, platform: "linux/amd64" })], before), undefined);
  assert.equal(newServer([...there, machine("n", "her-1", { platform: "linux/amd64", her: { unsupported: "no her" } })], before), undefined);
  assert.equal(newServer([...there, machine("n", "her-1", { platform: "linux/amd64", her: {} })], before)?.machineId, "n");
});
