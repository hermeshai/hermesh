import assert from "node:assert/strict";
import { test } from "node:test";
import type { Client, Machine } from "@hermesh/proto/local";
import type { Session } from "@hermesh/proto/session";
import { flowStrokeWidth, grantKey, layout, OWNER_ID, sessionKeys, type MapInput } from "./overview/mapLayout.ts";

const client = (clientId: string, label = clientId): Client =>
  ({ clientId, label, revoked: false }) as unknown as Client;

const machine = (machineId: string, local = true, online = true): Machine =>
  ({ machineId, machineName: machineId, local, online }) as unknown as Machine;

type TestSession = Session & { working: boolean };

const session = (sessionId: string, createdBy: string, working: boolean, projectId = "demo", machineId = "mac"): TestSession =>
  ({ sessionId, createdBy, working, projectId, machineId }) as unknown as TestSession;

const isWorking = (s: Session) => (s as TestSession).working;

const baseStore = (): MapInput => ({
  clients: [client("bot", "bot"), client("her", "Her")],
  machines: [machine("mac", true, true)],
  projects: [{ machineId: "mac", projectId: "demo" }],
  status: { machineOnly: false },
});

test("layout: owner sender appears only while unsettled owner sessions exist or while picked, ranked by unsettled count", () => {
  const st = baseStore();
  // With no unsettled owner session, "owner" is absent from the sender column.
  assert.deepEqual(layout(st, [], { isWorking }).grants.map((g) => g.id), ["bot", "her"]);

  // While picked, "owner" stays on the map even after its last session settles.
  assert.deepEqual(layout(st, [], { pick: grantKey(OWNER_ID), isWorking }).grants.map((g) => g.id), [OWNER_ID, "bot", "her"]);

  // One running owner session + three unsettled bot sessions -> bot ranks first, then owner, then her.
  const live = [
    session("b1", "bot", true),
    session("b2", "bot", false),
    session("b3", "bot", false),
    session("o1", OWNER_ID, true),
  ];
  assert.deepEqual(layout(st, live, { isWorking }).grants.map((g) => g.id), ["bot", OWNER_ID, "her"]);

  // Tie in unsettled count: owner leads external grants.
  const tied = [
    session("b1", "bot", true),
    session("o1", OWNER_ID, false),
  ];
  assert.deepEqual(layout(st, tied, { isWorking }).grants.map((g) => g.id), [OWNER_ID, "bot", "her"]);
  assert.deepEqual(sessionKeys(tied[1]), ["g:owner", "m:mac", "p:mac/demo"]);
});

test("layout: unsettled sessions sharing a route merge into one weighted path with atWork if any is active", () => {
  const st: MapInput = {
    ...baseStore(),
    projects: [
      { machineId: "mac", projectId: "demo" },
      { machineId: "mac", projectId: "docs" },
    ],
  };
  const live = [
    session("o1", OWNER_ID, false, "demo"),
    session("o2", OWNER_ID, false, "demo"),
    session("o3", OWNER_ID, true, "demo"),
    // Duplicate sessionId is ignored rather than double-counted.
    session("o3", OWNER_ID, true, "demo"),
    session("b1", "bot", false, "docs"),
  ];

  const L = layout(st, live, { isWorking });
  assert.equal(L.paths.length, 2);
  assert.deepEqual(
    L.paths.map((p) => ({ id: p.id, count: p.count, atWork: p.atWork })),
    [
      { id: "g:owner|m:mac|p:mac/demo", count: 3, atWork: true },
      { id: "g:bot|m:mac|p:mac/docs", count: 1, atWork: false },
    ],
  );

  // Machine-only mode merges along sender → project.
  const onlyLayout = layout({ ...st, status: { machineOnly: true } }, live, { isWorking });
  assert.deepEqual(
    onlyLayout.paths.map((p) => ({ id: p.id, count: p.count, atWork: p.atWork })),
    [
      { id: "g:owner|p:mac/demo", count: 3, atWork: true },
      { id: "g:bot|p:mac/docs", count: 1, atWork: false },
    ],
  );
});

test("flowStrokeWidth: grows logarithmically from 1.3px and caps at 2.8px", () => {
  assert.equal(flowStrokeWidth(1), 1.3);
  assert.equal(flowStrokeWidth(2), 1.85);
  assert.equal(flowStrokeWidth(4), 2.4);
  assert.equal(flowStrokeWidth(16), 2.8);
  assert.equal(flowStrokeWidth(100), 2.8);
});
