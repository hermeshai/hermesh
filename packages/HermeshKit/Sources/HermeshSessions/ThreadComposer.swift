import Foundation
import HermeshProto
import SwiftProtobuf

// The new-session composer's rules (docs/thread-composer.md), as the Mac has
// them in @hermesh/core's composer.ts: how hard a model thinks, the project a
// session takes on a machine, and why a provider can't be chosen.

/// The workspace each machine's session kernel makes for sessions that name no project.
public let scratchProject = "scratch"

/// Who an actor is, as a session's created_by and a turn's asker say it: "owner" for the owner (or none told), else its client id.
public func actorClientID(_ a: Hermesh_Session_V1_Actor?) -> String {
    guard let a, a.kind != .owner else { return "owner" }
    return a.id
}
/// How hard the selected model thinks: its "reasoning" option, the levels it offers and the one chosen.
public struct Reasoning: Equatable, Sendable {
    public let id: String
    public let choices: [Hermesh_Session_V1_OptionChoice]
    public let value: String
}

public func providerInstance(_ providers: [Hermesh_Session_V1_ProviderInstance], _ id: String) -> Hermesh_Session_V1_ProviderInstance? {
    providers.first { $0.instanceID == id }
}

public func modelEntry(_ providers: [Hermesh_Session_V1_ProviderInstance], _ sel: Hermesh_Session_V1_ModelSelection) -> Hermesh_Session_V1_ModelCatalogEntry? {
    guard let inst = providerInstance(providers, sel.instanceID) else { return nil }
    return inst.models.first { $0.slug == sel.model } ?? inst.models.first(where: \.isDefault) ?? inst.models.first
}

public func reasoningOf(_ providers: [Hermesh_Session_V1_ProviderInstance], _ sel: Hermesh_Session_V1_ModelSelection) -> Reasoning? {
    guard let d = modelEntry(providers, sel)?.optionDescriptors.first(where: { $0.role == "reasoning" && $0.type == .select && !$0.choices.isEmpty }) else { return nil }
    let value: String
    if case .stringValue(let v)? = sel.options[d.id]?.kind {
        value = v
    } else if case .stringValue(let v)? = d.currentValue.kind {
        value = v
    } else {
        value = (d.choices.first(where: \.isDefault) ?? d.choices[0]).id
    }
    return Reasoning(id: d.id, choices: d.choices, value: value)
}

public func withReasoning(_ sel: Hermesh_Session_V1_ModelSelection, id: String, value: String) -> Hermesh_Session_V1_ModelSelection {
    var next = sel
    var v = Hermesh_Session_V1_OptionValue()
    v.kind = .stringValue(value)
    next.options[id] = v
    return next
}

/// Another model: its own default options, with how hard it thinks kept when it offers that level. changed
/// names the level that could not be kept and the one it became.
public func selectModel(
    _ providers: [Hermesh_Session_V1_ProviderInstance], from prev: Hermesh_Session_V1_ModelSelection, instanceID: String, slug: String
) -> (selection: Hermesh_Session_V1_ModelSelection, changed: (from: String, to: String)?) {
    guard let inst = providerInstance(providers, instanceID), let entry = inst.models.first(where: { $0.slug == slug }) else { return (prev, nil) }
    var sel = Hermesh_Session_V1_ModelSelection()
    sel.instanceID = inst.instanceID
    sel.model = entry.slug
    for d in entry.optionDescriptors where d.hasCurrentValue { sel.options[d.id] = d.currentValue }
    guard let before = reasoningOf(providers, prev), let after = reasoningOf(providers, sel), before.value != after.value else { return (sel, nil) }
    if after.choices.contains(where: { $0.id == before.value }) { return (withReasoning(sel, id: after.id, value: before.value), nil) }
    return (sel, (before.value, after.value))
}

/// The selection a provider starts with: its default model and that model's own options.
public func defaultSelection(_ inst: Hermesh_Session_V1_ProviderInstance) -> Hermesh_Session_V1_ModelSelection {
    let slug = (inst.models.first(where: \.isDefault) ?? inst.models.first)?.slug ?? "default"
    return selectModel([inst], from: Hermesh_Session_V1_ModelSelection(), instanceID: inst.instanceID, slug: slug).selection
}

/// Why a provider can't be chosen here, or nil when it can.
public enum ProviderUnusable: String, Sendable { case off, notInstalled, notForProject, signedOut }

public func providerUnusable(_ inst: Hermesh_Session_V1_ProviderInstance, projectAgents: [String]) -> ProviderUnusable? {
    if !inst.enabled { return .off }
    if !inst.installed { return .notInstalled }
    if !projectAgents.isEmpty, ![inst.instanceID, inst.driver, inst.acpFlavor].contains(where: { !$0.isEmpty && projectAgents.contains($0) }) { return .notForProject }
    if !inst.authenticated { return .signedOut }
    return nil
}

/// The providers the model picker offers for the active role: only Her when talking to Her, else only coding agents.
public func providersForRole(_ providers: [Hermesh_Session_V1_ProviderInstance], forHer: Bool) -> [Hermesh_Session_V1_ProviderInstance] {
    providers.filter { ($0.instanceID == herAgent) == forHer }
}

/// The provider a new session starts with when nothing was used on the machine before: the coding agents most
/// people use first, then any ready one.
public func preferredProvider(_ providers: [Hermesh_Session_V1_ProviderInstance]) -> Hermesh_Session_V1_ProviderInstance? {
    let ready = providers.filter { providerUnusable($0, projectAgents: []) == nil }
    return ["claude", "codex", "gemini"].lazy.compactMap { id in ready.first { $0.instanceID == id } }.first ?? ready.first ?? providers.first
}

/// What the search finds, by provider: a model matches by its name, short name or slug, or its provider's name.
public func findModels(_ providers: [Hermesh_Session_V1_ProviderInstance], query: String) -> [(provider: Hermesh_Session_V1_ProviderInstance, models: [Hermesh_Session_V1_ModelCatalogEntry])] {
    let q = query.trimmingCharacters(in: .whitespaces).lowercased()
    guard !q.isEmpty else { return [] }
    return providers.compactMap { inst in
        let models = inst.models.filter { [$0.name, $0.shortName, $0.slug, inst.displayName].contains { $0.lowercased().contains(q) } }
        return models.isEmpty ? nil : (inst, models)
    }
}

/// The project a new session takes on another machine: the same one when that machine has a project of its
/// name, else the one last used there, else the temporary workspace.
public func projectOnMachine(_ projects: [Hermesh_Local_V1_Project], machineID: String, current: String, lastUsed: String? = nil) -> String {
    func has(_ id: String?) -> Bool { guard let id, !id.isEmpty else { return false }; return projects.contains { $0.machineID == machineID && $0.projectID == id } }
    if has(current) { return current }
    if has(lastUsed) { return lastUsed! }
    return scratchProject
}

/// Whether a new session in a project uses its own worktree: never when the project cannot use worktrees
/// (or is the temporary workspace); otherwise what the owner last chose for this project, falling back to
/// the project's own setting.
public func projectWorktree(_ project: Hermesh_Local_V1_Project?, remembered: Bool? = nil) -> Bool {
    guard let project, project.repo || project.workspace == .worktree else { return false }
    return remembered ?? (project.workspace == .worktree)
}

/// The grants a new project on a machine would reach at once, without anyone signing anything: those whose
/// machines include it and whose projects are all ("*") or name it (docs/thread-composer.md §3.2). all: by "*".
public func grantsReaching(_ clients: [Hermesh_Local_V1_Client], machineID: String, projectID: String) -> [(client: Hermesh_Local_V1_Client, all: Bool)] {
    clients
        .filter { !$0.revoked && $0.hasSpec }
        .filter { $0.spec.machineIds.contains("*") || $0.spec.machineIds.contains(machineID) }
        .filter { $0.spec.projectIds.contains("*") || (!projectID.isEmpty && $0.spec.projectIds.contains(projectID)) }
        .map { ($0, $0.spec.projectIds.contains("*")) }
}
