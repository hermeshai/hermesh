import { create } from "@bufbuild/protobuf";
import { GrantSpecSchema, type Client, type GrantSpec, type Project } from "@hermesh/proto/local";

/**
 * A personal agent's scope, as edited in the approval window and the
 * agent's page: which projects (machine/project), coding agents and
 * lifetime.
 */
export interface ScopeChoice {
  allProjects: boolean;
  /** "machineID/projectID". */
  projects: ReadonlySet<string>;
  /** Every coding agent, including ones installed later. */
  allAgents: boolean;
  agents: ReadonlySet<string>;
  ttlDays: number;
}

/** The lifetimes the owner picks from, in days. */
export const ttlChoices = [7, 30, 90] as const;

/** A new grant's scope. */
export function defaultScope(): ScopeChoice {
  return { allProjects: false, projects: new Set(), allAgents: false, agents: new Set(["claude", "codex"]), ttlDays: 30 };
}

/** A grant's scope; "*" (or nothing) means all, as a relay may store it. */
export function scopeOfClient(client: Client, projects: Project[]): ScopeChoice {
  const machines = (client.spec?.machineIds ?? []).filter((m) => m !== "*");
  const ids = (client.spec?.projectIds ?? []).filter((p) => p !== "*");
  const named = (client.spec?.agents ?? []).filter((a) => a !== "*");
  return {
    allProjects: machines.length === 0 && ids.length === 0,
    projects: new Set(
      projects
        .filter(
          (p) =>
            (machines.length === 0 || machines.includes(p.machineId)) &&
            (ids.length === 0 || ids.includes(p.projectId)),
        )
        .map((p) => `${p.machineId}/${p.projectId}`),
    ),
    allAgents: named.length === 0,
    agents: new Set(named),
    ttlDays: 30,
  };
}

/** The grant spec a scope asks for, without its label. */
export function scopeSpec(scope: ScopeChoice): GrantSpec {
  const spec = create(GrantSpecSchema, { agents: scope.allAgents ? ["*"] : [...scope.agents].sort(), ttlDays: scope.ttlDays });
  if (!scope.allProjects) {
    const pairs = [...scope.projects].map((key) => {
      const i = key.indexOf("/");
      return [key.slice(0, i), key.slice(i + 1)];
    });
    spec.machineIds = [...new Set(pairs.map((p) => p[0]))].sort();
    spec.projectIds = [...new Set(pairs.map((p) => p[1]))].sort();
  }
  return spec;
}

/** At least one project (or all) and one coding agent (or all). */
export function scopeComplete(scope: ScopeChoice): boolean {
  return (scope.allProjects || scope.projects.size > 0) && (scope.allAgents || scope.agents.size > 0);
}
