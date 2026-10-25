import { create } from "@bufbuild/protobuf";
import { ClientSchema, MachineSchema, ProjectRefSchema, ProjectSchema, WatchResponseSchema, type WatchResponse } from "@hermesh/proto/local";
import { SessionStatus } from "@hermesh/proto/session";
import { describe, expect, test } from "vitest";
import { isActive, needsAttention } from "./sessions";
import { applyWatch, emptyWorld, stateKnown, unreachableMachines, withKnownState } from "./world";
import { session } from "./testing";

const msg = (m: Partial<WatchResponse>) => create(WatchResponseSchema, m as never);
const machine = (id: string, name: string, unreachable = "") => create(MachineSchema, { machineId: id, machineName: name, online: unreachable === "", unreachable });

describe("following Watch", () => {
  test("the first message is everything; changes replace in place, add, and remove", () => {
    let w = applyWatch(emptyWorld, msg({ reset: true, machines: [machine("m_b", "vps"), machine("m_a", "mac")], projects: [create(ProjectSchema, { machineId: "m_a", projectId: "demo" })] }));
    expect(w.loaded).toBe(true);
    expect(w.machines.map((m) => m.machineName)).toEqual(["mac", "vps"]); // as the lists sort them
    w = applyWatch(w, msg({ machines: [machine("m_b", "vps", "offline")], clients: [create(ClientSchema, { grantId: "g1", label: "bot" })] }));
    expect(w.machines[1].unreachable).toBe("offline");
    expect(w.clients.map((c) => c.label)).toEqual(["bot"]);
    w = applyWatch(w, msg({ removedProjects: [create(ProjectRefSchema, { machineId: "m_a", projectId: "demo" })], removedGrantIds: ["g1"] }));
    expect(w.projects).toEqual([]);
    expect(w.clients).toEqual([]);
  });

  test("a message that changes nothing keeps the world as it was (nothing renders again)", () => {
    const w = applyWatch(emptyWorld, msg({ reset: true, machines: [machine("m_a", "mac")] }));
    expect(applyWatch(w, msg({}))).toBe(w);
  });

  test("a reset replaces the lists but merges sessions, so older pages loaded stay", () => {
    const old = session("s_old", SessionStatus.IDLE, { updated: -86400 });
    const live = session("s_live", SessionStatus.RUNNING);
    let w = applyWatch(emptyWorld, msg({ reset: true, sessions: [live], machines: [machine("m_a", "mac")] }));
    w = { ...w, sessions: [...w.sessions, old] }; // a page the list scrolled to
    w = applyWatch(w, msg({ reset: true, sessions: [live] }));
    expect(w.machines).toEqual([]);
    expect(w.sessions.map((s) => s.sessionId).sort()).toEqual(["s_live", "s_old"]);
    w = applyWatch(w, msg({ deletedSessionIds: ["s_old"] }));
    expect(w.sessions.map((s) => s.sessionId)).toEqual(["s_live"]);
  });

  test("a session on a machine that cannot be read has no known state", () => {
    const s = session("s_x", SessionStatus.RUNNING);
    s.machineId = "m_b";
    const w = applyWatch(emptyWorld, msg({ reset: true, machines: [machine("m_a", "mac"), machine("m_b", "vps", "offline")] }));
    expect(stateKnown(s, w.machines)).toBe(false);
    expect(unreachableMachines(w.machines).map((u) => [u.machineName, u.error])).toEqual([["vps", "offline"]]);
    s.machineId = "m_a";
    expect(stateKnown(s, w.machines)).toBe(true);
  });

  test("shown with its state unknown: neither in progress nor needing the owner", () => {
    const running = session("s_r", SessionStatus.RUNNING);
    const asking = session("s_q", SessionStatus.WAITING_INPUT);
    const archived = session("s_a", SessionStatus.ARCHIVED);
    for (const s of [running, asking, archived]) s.machineId = "m_b";
    const ok = [machine("m_b", "vps")];
    expect(withKnownState([running], ok)[0]).toBe(running);
    const [r, q, a] = withKnownState([running, asking, archived], [machine("m_b", "vps", "offline")]);
    expect([r.status, q.status]).toEqual([SessionStatus.UNSPECIFIED, SessionStatus.UNSPECIFIED]);
    expect([isActive(r), needsAttention(q)]).toEqual([false, false]);
    expect(a).toBe(archived); // archived stays archived
  });
});
