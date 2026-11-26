import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { Workspace } from "@hermesh/proto/identity";
import { ClientSchema, GrantSpecSchema, ProjectSchema } from "@hermesh/proto/local";
import {
  ModelCatalogEntrySchema,
  ModelSelectionSchema,
  OptionChoiceSchema,
  OptionDescriptorSchema,
  OptionDescriptorType,
  OptionValueSchema,
  ProviderInstanceSchema,
} from "@hermesh/proto/session";
import {
  findModels,
  grantsReaching,
  projectOnMachine,
  projectWorktree,
  providerUnusable,
  reasoningOf,
  selectModel,
  withReasoning,
} from "./composer";
import { projectIdFromFolder } from "./signin";

const effort = (ids: string[], def: string) =>
  create(OptionDescriptorSchema, {
    id: "effort",
    role: "reasoning",
    type: OptionDescriptorType.SELECT,
    choices: ids.map((id) => create(OptionChoiceSchema, { id, label: id, isDefault: id === def })),
    currentValue: create(OptionValueSchema, { kind: { case: "stringValue", value: def } }),
  });
const claude = create(ProviderInstanceSchema, {
  instanceId: "claude",
  driver: "claude",
  displayName: "Claude Code",
  enabled: true,
  installed: true,
  authenticated: true,
  models: [
    create(ModelCatalogEntrySchema, { slug: "opus", name: "Claude Opus", shortName: "Opus", optionDescriptors: [effort(["low", "medium", "high", "max"], "high")] }),
    create(ModelCatalogEntrySchema, { slug: "sonnet", name: "Claude Sonnet", shortName: "Sonnet", isDefault: true, optionDescriptors: [effort(["low", "medium", "high"], "medium")] }),
    create(ModelCatalogEntrySchema, { slug: "haiku", name: "Claude Haiku", shortName: "Haiku" }),
  ],
});
const gemini = create(ProviderInstanceSchema, {
  instanceId: "gemini",
  driver: "acp",
  acpFlavor: "gemini",
  displayName: "Gemini CLI",
  enabled: true,
  installed: true,
  authenticated: false,
  models: [create(ModelCatalogEntrySchema, { slug: "gemini-3-pro", name: "Gemini 3 Pro", isDefault: true })],
});
const providers = [claude, gemini];
const sel = (model: string, level?: string) =>
  create(ModelSelectionSchema, { instanceId: "claude", model, options: level ? { effort: create(OptionValueSchema, { kind: { case: "stringValue", value: level } }) } : {} });

describe("the unified session composer", () => {
  it("reads and sets how hard the model thinks", () => {
    expect(reasoningOf(providers, sel("opus"))).toMatchObject({ id: "effort", value: "high" });
    expect(reasoningOf(providers, sel("opus", "low"))?.value).toBe("low");
    expect(reasoningOf(providers, sel("haiku"))).toBeUndefined();
    expect(reasoningOf(providers, withReasoning(sel("sonnet"), "effort", "high"))?.value).toBe("high");
  });

  it("keeps the thinking level on another model when it has it, else says what it became", () => {
    expect(reasoningOf(providers, selectModel(providers, sel("opus", "low"), "claude", "sonnet").sel)?.value).toBe("low");
    const r = selectModel(providers, sel("opus", "max"), "claude", "sonnet");
    expect(r.changed).toEqual({ from: "max", to: "medium" });
    expect(reasoningOf(providers, r.sel)?.value).toBe("medium");
    expect(selectModel(providers, sel("opus", "max"), "claude", "haiku").changed).toBeUndefined();
  });

  it("finds models across providers by name or provider", () => {
    expect(findModels(providers, "son").map((g) => [g.inst.instanceId, g.models.map((m) => m.slug)])).toEqual([["claude", ["sonnet"]]]);
    expect(findModels(providers, "gemini").map((g) => g.inst.instanceId)).toEqual(["gemini"]);
    expect(findModels(providers, " ")).toEqual([]);
  });

  it("says why a provider can't be chosen", () => {
    expect(providerUnusable(claude, [])).toBe("");
    expect(providerUnusable(gemini, [])).toBe("signedOut");
    expect(providerUnusable(gemini, ["claude"])).toBe("notForProject");
    expect(providerUnusable(gemini, ["gemini"])).toBe("signedOut");
    expect(providerUnusable(create(ProviderInstanceSchema, { instanceId: "codex", enabled: true }), [])).toBe("notInstalled");
  });

  it("keeps the project on another machine when it has one of that name", () => {
    const projects = [
      create(ProjectSchema, { machineId: "a", projectId: "hermesh" }),
      create(ProjectSchema, { machineId: "b", projectId: "hermesh" }),
      create(ProjectSchema, { machineId: "b", projectId: "relay" }),
    ];
    expect(projectOnMachine(projects, "b", "hermesh")).toBe("hermesh");
    expect(projectOnMachine(projects, "b", "website", "relay")).toBe("relay");
    expect(projectOnMachine(projects, "c", "hermesh", "relay")).toBe("scratch");
  });

  it("names the grants a new project reaches without a signature", () => {
    const grant = (clientId: string, machineIds: string[], projectIds: string[], revoked = false) =>
      create(ClientSchema, { clientId, label: clientId, revoked, spec: create(GrantSpecSchema, { machineIds, projectIds }) });
    const clients = [grant("her", ["*"], ["*"]), grant("grok", ["devbox"], ["relay"]), grant("gpt", ["mac"], ["relay"]), grant("old", ["*"], ["*"], true)];
    expect(grantsReaching(clients, "devbox", "relay").map((g) => [g.client.clientId, g.all])).toEqual([["her", true], ["grok", false]]);
    expect(grantsReaching(clients, "devbox", "app").map((g) => g.client.clientId)).toEqual(["her"]);
  });

  it("never names a project after the temporary workspace", () => {
    expect(projectIdFromFolder("scratch", new Set())).toBe("scratch-2");
  });

  it("picks a project's working copy from what was remembered for it, falling back to its setting", () => {
    const repoWorktree = create(ProjectSchema, { repo: true, workspace: Workspace.WORKTREE });
    const repoInPlace = create(ProjectSchema, { repo: true, workspace: Workspace.IN_PLACE });
    const plainFolder = create(ProjectSchema, { repo: false, workspace: Workspace.IN_PLACE });

    expect(projectWorktree(repoWorktree)).toBe(true);
    expect(projectWorktree(repoWorktree, false)).toBe(false);
    expect(projectWorktree(repoInPlace)).toBe(false);
    expect(projectWorktree(repoInPlace, true)).toBe(true);
    expect(projectWorktree(plainFolder, true)).toBe(false);
    expect(projectWorktree(undefined, true)).toBe(false);
  });
});
