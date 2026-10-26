import HermeshProto
import HermeshSessions
import SwiftUI

/// The projects agent may work in: those for it, and those for every agent.
func projectsFor(_ projects: [Project], _ agent: String) -> [Project] {
    projects.filter { $0.agents.isEmpty || $0.agents.contains(agent) }
}

/// A machine's coding agents sorted out: installed (signed in first, then
/// Claude Code and Codex, then by name), still being checked, not installed.
func sortedAgents(_ agents: [AgentStatus]) -> (installed: [AgentStatus], checking: [AgentStatus], missing: [String]) {
    let first = ["claude", "codex"]
    let rank = { (a: AgentStatus) in first.firstIndex(of: a.name) ?? first.count }
    let installed = agents.filter(\.available).sorted {
        ($0.loggedIn ? 0 : 1, rank($0), agentTitle($0.name)) < ($1.loggedIn ? 0 : 1, rank($1), agentTitle($1.name))
    }
    let checking = agents.filter { $0.checking && !$0.available }
    let missing = agents.filter { !$0.available && !$0.checking }.map(\.name)
    return (installed, checking, missing)
}

func effortName(_ e: String) -> String {
    switch e {
    case "low": String(localized: "Low")
    case "medium": String(localized: "Medium")
    case "high": String(localized: "High")
    case "xhigh": String(localized: "Extra High")
    case "max": String(localized: "Max")
    case "ultra": String(localized: "Ultra")
    default: e
    }
}

func speedName(_ s: Hermesh_Identity_V1_AgentSpeed) -> String {
    s == .fast ? String(localized: "Fast") : String(localized: "Standard")
}

func modelName(_ a: AgentStatus, _ id: String) -> String {
    let n = a.models.first { $0.id == id }?.name ?? ""
    return n.isEmpty ? id : n
}

/// What is set for an agent's sessions on a machine, for its row: "" while
/// it follows its own configuration.
func defaultsText(_ a: AgentStatus) -> String {
    let d = a.defaults
    return [d.model.isEmpty ? "" : modelName(a, d.model), d.effort.isEmpty ? "" : effortName(d.effort), d.speed == .unspecified ? "" : speedName(d.speed)]
        .filter { !$0.isEmpty }.joined(separator: " · ")
}

/// A coding agent on a machine, opened from the machine's page: signing in
/// when it isn't, the model, reasoning effort and (Codex) speed its sessions
/// run with when the agent starting one names no model, and the projects it
/// may work in, each with how agents work there.
struct MachineAgentView: View {
    @Environment(AppModel.self) private var model
    let machineID: String
    let agent: String
    @State private var projects: [Project] = []

    var body: some View {
        Group {
            if let m = model.machines.first(where: { $0.machineID == machineID }), let a = m.status.agents.first(where: { $0.name == agent && $0.available }) {
                content(m, a)
            } else {
                ContentUnavailableView("This coding agent isn’t on this machine", systemImage: "questionmark.circle")
            }
        }
        .navigationTitle(agentTitle(agent))
        .navigationBarTitleDisplayMode(.inline)
        .task { await load() }
        .onChange(of: model.sheet) { if model.sheet == nil { Task { await load() } } }
    }

    private func load() async {
        projects = (try? await model.api?.projects(machine: machineID))?.projects ?? []
    }

    private func content(_ m: Machine, _ a: AgentStatus) -> some View {
        let title = agentTitle(agent)
        let mine = projectsFor(projects, agent)
        let editable = m.online
        return List {
            Section {
                VStack(spacing: 8) {
                    AgentLogo(id: agent, size: 58)
                    Text(title).font(.title2.bold())
                    Text("\(m.machineName) · \(a.version) · \(Text(a.loggedIn ? String(localized: "Signed in") : String(localized: "Not signed in")).foregroundStyle(a.loggedIn ? Color.secondary : .orange))")
                        .font(.subheadline).foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity).listRowBackground(Color.clear)
            }
            if !a.loggedIn {
                Section {
                    Label("Sign in to \(title) first so Hermesh can use it.", systemImage: "exclamationmark.triangle").foregroundStyle(.orange)
                    if let command = ["claude": "claude", "codex": "codex login"][agent] {
                        Text("On \(m.machineName), run \(Text(command).monospaced()) in Terminal").font(.subheadline).foregroundStyle(.secondary)
                    }
                }
            }
            if !a.models.isEmpty {
                defaults(m, a, title: title).disabled(!editable)
            }
            Section {
                ForEach(mine, id: \.projectID) { p in
                    projectRow(p, title: title, editable: editable)
                }
                Button("Add a Project…", systemImage: "plus") { model.sheet = .addProject(machineID, agent) }
                    .disabled(!editable)
                    .accessibilityIdentifier("add-project")
            } header: {
                Text("Projects")
            } footer: {
                if mine.isEmpty { Text("No projects yet") }
            }
        }
        .refreshable {
            try? await model.api?.checkAgents()
            await load()
        }
    }

    /// The model, reasoning effort and (Codex) speed, one menu each: the
    /// agent's own configuration unless set here.
    private func defaults(_ m: Machine, _ a: AgentStatus, title: String) -> some View {
        let d = a.defaults
        let own = a.own
        let same = { (v: String) in v.isEmpty ? String(localized: "Same as \(title)") : String(localized: "Same as \(title) (\(v))") }
        let effortsOf = { (id: String) in a.models.first { $0.id == id }?.efforts }
        let efforts = effortsOf(d.model.isEmpty ? own.model : d.model) ?? a.models[0].efforts
        let save = { (next: Hermesh_Identity_V1_AgentDefaults) in Task { await model.setAgentDefaults(m, next) } }
        return Section {
            Picker("Model", selection: Binding(get: { d.model }, set: { v in
                var next = d
                next.agent = agent
                next.model = v
                // An effort the newly picked model lacks goes back to the agent's own.
                if !d.effort.isEmpty, let ok = effortsOf(v.isEmpty ? own.model : v), !ok.contains(d.effort) { next.effort = "" }
                save(next)
            })) {
                Text(same(own.model.isEmpty ? "" : modelName(a, own.model))).tag("")
                ForEach(a.models, id: \.id) { x in Text(x.name.isEmpty ? x.id : x.name).tag(x.id) }
            }
            .accessibilityIdentifier("default-model")
            Picker("Reasoning Effort", selection: Binding(get: { d.effort }, set: { v in
                var next = d
                next.agent = agent
                next.effort = v
                save(next)
            })) {
                Text(same(own.effort.isEmpty ? "" : effortName(own.effort))).tag("")
                ForEach(efforts, id: \.self) { e in Text(effortName(e)).tag(e) }
            }
            .accessibilityIdentifier("default-effort")
            if agent == "codex" {
                Picker("Speed", selection: Binding(get: { d.speed }, set: { v in
                    var next = d
                    next.agent = agent
                    next.speed = v
                    save(next)
                })) {
                    Text(same(own.speed == .unspecified ? "" : speedName(own.speed))).tag(Hermesh_Identity_V1_AgentSpeed.unspecified)
                    Text("Standard").tag(Hermesh_Identity_V1_AgentSpeed.standard)
                    Text("Fast").tag(Hermesh_Identity_V1_AgentSpeed.fast)
                }
            }
        } header: {
            Text("Default Model")
        } footer: {
            Text("Sessions an agent starts here without naming a model use these. \(title)’s own settings stay as they are.")
        }
    }

    /// A project of this agent, with a menu (and swipe) to change how agents
    /// work in it, or stop using it with this agent: taken from the last
    /// agent it is for, the project goes.
    private func projectRow(_ p: Project, title: String, editable: Bool) -> some View {
        let stop = {
            // One for every agent stays for every other the machine knows of.
            let rest = (p.agents.isEmpty ? (model.machines.first { $0.machineID == machineID }?.status.agents ?? []).map(\.name) : p.agents).filter { $0 != agent }
            run {
                if rest.isEmpty {
                    try await model.removeProject(machine: machineID, project: p.projectID)
                } else {
                    try await model.saveProject(p, agents: rest, workspace: p.workspace)
                }
            }
        }
        return HStack {
            ProjectLine(project: p, detail: workspaceText(p.workspace))
            Spacer(minLength: 0)
            Menu {
                Picker("How agents work", selection: Binding(get: { p.workspace == .inPlace ? Hermesh_Identity_V1_Workspace.inPlace : .worktree }, set: { w in
                    run { try await model.saveProject(p, agents: p.agents, workspace: w) }
                })) {
                    Text("Separate Worktree: Each Session on Its Own Branch").tag(Hermesh_Identity_V1_Workspace.worktree)
                    Text("In Place: Change This Folder Directly").tag(Hermesh_Identity_V1_Workspace.inPlace)
                }
                Divider()
                Button("Stop Using with \(title)", systemImage: "minus.circle", role: .destructive) { stop() }
            } label: {
                Image(systemName: "ellipsis").frame(width: 44, height: 44).contentShape(.rect)
            }
            .disabled(!editable)
            .accessibilityLabel("More")
            .accessibilityIdentifier("project-menu-\(p.projectID)")
        }
        .swipeActions {
            Button("Stop Using", role: .destructive) { stop() }.disabled(!editable)
        }
    }

    private func run(_ work: @escaping () async throws -> Void) {
        Task {
            do {
                try await work()
            } catch {
                model.report(error)
            }
            await load()
        }
    }
}
