import { create } from "@bufbuild/protobuf";
import { Workspace } from "@hermesh/proto/identity";
import type { Client, Project } from "@hermesh/proto/local";
import {
  ModelSelectionSchema,
  OptionDescriptorType,
  OptionValueSchema,
  type ModelCatalogEntry,
  type ModelSelection,
  type OptionChoice,
  type ProviderInstance,
} from "@hermesh/proto/session";
import { activeOptionDescriptors, findModelEntry, findProviderInstance } from "./threads";

// The session composer: where a session runs (machine, project), with what
// (provider, model, reasoning effort) and how it may act (permission mode).

/** The workspace each machine's session kernel makes for sessions that name no project. */
export const SCRATCH_PROJECT = "scratch";

/** How hard the selected model thinks: its "reasoning" option, the levels it offers and the one chosen. */
export interface Reasoning {
  id: string;
  choices: OptionChoice[];
  value: string;
}

export function reasoningOf(providers: readonly ProviderInstance[], sel: ModelSelection): Reasoning | undefined {
  const d = activeOptionDescriptors(providers, sel).find((x) => x.role === "reasoning" && x.type === OptionDescriptorType.SELECT && x.choices.length > 0);
  if (!d) return undefined;
  const chosen = sel.options[d.id]?.kind;
  const value =
    chosen?.case === "stringValue" ? chosen.value
    : d.currentValue?.kind.case === "stringValue" ? d.currentValue.kind.value
    : (d.choices.find((c) => c.isDefault) ?? d.choices[0]).id;
  return { id: d.id, choices: d.choices, value };
}

export function withReasoning(sel: ModelSelection, id: string, value: string): ModelSelection {
  return create(ModelSelectionSchema, {
    instanceId: sel.instanceId,
    model: sel.model,
    options: { ...sel.options, [id]: create(OptionValueSchema, { kind: { case: "stringValue", value } }) },
  });
}

/**
 * Another model: its own default options, with how hard it thinks kept when
 * it offers that level (as Her switches models). changed names the level
 * that could not be kept and the one it became.
 */
export function selectModel(
  providers: readonly ProviderInstance[],
  prev: ModelSelection,
  instanceId: string,
  slug: string,
): { sel: ModelSelection; changed?: { from: string; to: string } } {
  const inst = findProviderInstance(providers, instanceId);
  const entry = inst?.models.find((m) => m.slug === slug);
  if (!inst || !entry) return { sel: prev };
  const options: ModelSelection["options"] = {};
  for (const d of entry.optionDescriptors) if (d.currentValue) options[d.id] = d.currentValue;
  const sel = create(ModelSelectionSchema, { instanceId: inst.instanceId, model: entry.slug, options });
  const before = reasoningOf(providers, prev);
  const after = reasoningOf(providers, sel);
  if (!before || !after || before.value === after.value) return { sel };
  if (after.choices.some((c) => c.id === before.value)) return { sel: withReasoning(sel, after.id, before.value) };
  return { sel, changed: { from: before.value, to: after.value } };
}

/** The model's name as the composer shows it. */
export function selectionTitle(providers: readonly ProviderInstance[], sel: ModelSelection): string {
  const m = findModelEntry(providers, sel);
  return m?.shortName || m?.name || sel.model || "";
}

/** Why a provider can't be chosen here, or "" when it can. */
export function providerUnusable(inst: ProviderInstance, projectAgents: readonly string[]): "notInstalled" | "signedOut" | "notForProject" | "off" | "" {
  if (!inst.enabled) return "off";
  if (!inst.installed) return "notInstalled";
  if (projectAgents.length > 0 && ![inst.instanceId, inst.driver, inst.acpFlavor].some((x) => x !== "" && projectAgents.includes(x))) return "notForProject";
  if (!inst.authenticated) return "signedOut";
  return "";
}

/** The providers the model picker offers for the active role: only Her when talking to Her, else only coding agents. */
export function providersForRole(providers: readonly ProviderInstance[], forHer: boolean): ProviderInstance[] {
  return providers.filter((p) => (p.instanceId === "her") === forHer);
}

/** What the search finds, by provider: a model matches by its name, short name or its provider's name. */
export function findModels(providers: readonly ProviderInstance[], query: string): { inst: ProviderInstance; models: ModelCatalogEntry[] }[] {
  const q = query.trim().toLowerCase();
  if (!q) return [];
  return providers
    .map((inst) => ({
      inst,
      models: inst.models.filter((m) => [m.name, m.shortName, m.slug, inst.displayName].some((s) => s.toLowerCase().includes(q))),
    }))
    .filter((g) => g.models.length > 0);
}

/**
 * The project a new session takes on another machine: the same one when that
 * machine has a project of its name, else the one last used there, else the
 * temporary workspace.
 */
export function projectOnMachine(projects: readonly Pick<Project, "machineId" | "projectId">[], machineId: string, current: string, lastUsed?: string): string {
  const has = (id?: string) => !!id && projects.some((p) => p.machineId === machineId && p.projectId === id);
  if (has(current)) return current;
  if (has(lastUsed)) return lastUsed!;
  return SCRATCH_PROJECT;
}

/**
 * Whether a new session in a project uses its own worktree: never when the
 * project cannot use worktrees (or is the temporary workspace); otherwise what
 * the owner last chose for this project, falling back to the project's setting.
 */
export function projectWorktree(project: Pick<Project, "repo" | "workspace"> | undefined, remembered?: boolean): boolean {
  if (!project || (!project.repo && project.workspace !== Workspace.WORKTREE)) return false;
  return remembered ?? project.workspace === Workspace.WORKTREE;
}

/**
 * The grants a new project on a machine would reach at once, without anyone
 * signing anything: those whose machines include it and whose projects are
 * all ("*") or name it. all: by "*".
 */
export function grantsReaching(clients: readonly Client[], machineId: string, projectId: string): { client: Client; all: boolean }[] {
  return clients
    .filter((c) => !c.revoked && c.spec)
    .filter((c) => c.spec!.machineIds.includes("*") || c.spec!.machineIds.includes(machineId))
    .filter((c) => c.spec!.projectIds.includes("*") || (projectId !== "" && c.spec!.projectIds.includes(projectId)))
    .map((c) => ({ client: c, all: c.spec!.projectIds.includes("*") }));
}
