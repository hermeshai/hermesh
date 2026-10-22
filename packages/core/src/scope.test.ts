import { create } from "@bufbuild/protobuf";
import { ClientSchema, ProjectSchema } from "@hermesh/proto/local";
import { describe, expect, test } from "vitest";
import { defaultScope, scopeComplete, scopeOfClient, scopeSpec } from "./scope";

describe("scope", () => {
  test("a star means all", () => {
    const c = create(ClientSchema, { spec: { machineIds: ["*"], projectIds: ["*"], agents: ["*"] } });
    const s = scopeOfClient(c, []);
    expect(s.allProjects).toBe(true);
    expect(s.allAgents).toBe(true);
    expect(scopeSpec(s).agents).toEqual(["*"]);

    const p = create(ProjectSchema, { machineId: "m1", projectId: "app" });
    c.spec!.machineIds = ["m1"];
    c.spec!.projectIds = ["app"];
    c.spec!.agents = ["claude"];
    const n = scopeOfClient(c, [p]);
    expect(n.allProjects).toBe(false);
    expect(n.allAgents).toBe(false);
    expect([...n.projects]).toEqual(["m1/app"]);
    expect([...n.agents]).toEqual(["claude"]);
  });

  test("the spec a scope asks for", () => {
    const s = { ...defaultScope(), projects: new Set(["m2/web", "m1/app"]), agents: new Set(["codex", "claude"]) };
    expect(scopeComplete(s)).toBe(true);
    const spec = scopeSpec(s);
    expect([spec.machineIds, spec.projectIds, spec.agents, spec.ttlDays]).toEqual([
      ["m1", "m2"],
      ["app", "web"],
      ["claude", "codex"],
      30,
    ]);
    expect(scopeSpec({ ...s, allProjects: true }).machineIds).toEqual([]);
    expect(scopeComplete(defaultScope())).toBe(false);
    expect(scopeComplete({ ...s, agents: new Set() })).toBe(false);
    // Every coding agent, including ones installed later: a star, complete without any named.
    expect(scopeSpec({ ...s, allAgents: true }).agents).toEqual(["*"]);
    expect(scopeComplete({ ...s, allAgents: true, agents: new Set() })).toBe(true);
  });
});
