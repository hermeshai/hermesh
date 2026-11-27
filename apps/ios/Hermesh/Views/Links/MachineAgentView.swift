import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI

/// A machine's coding agents sorted out: installed (signed in first, then
/// Claude Code and Codex, then by name), troubled (e.g. desktop app without CLI),
/// still being checked, not installed (excluding retired Gemini CLI).
func sortedAgents(_ agents: [AgentStatus]) -> (installed: [AgentStatus], troubled: [AgentStatus], checking: [AgentStatus], missing: [String]) {
    let first = ["claude", "codex"]
    let rank = { (a: AgentStatus) in first.firstIndex(of: a.name) ?? first.count }
    let installed = agents.filter(\.available).sorted {
        ($0.loggedIn ? 0 : 1, rank($0), agentTitle($0.name)) < ($1.loggedIn ? 0 : 1, rank($1), agentTitle($1.name))
    }
    let troubled = agents.filter { !$0.available && !$0.checking && $0.problem != .unspecified && $0.problem != .notFound }
    let checking = agents.filter { $0.checking && !$0.available }
    let listed = Set((installed + troubled + checking).map(\.name))
    let missing = codingAgentIDs.filter { $0 != "gemini" && !listed.contains($0) }
    return (installed, troubled, checking, missing)
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

/// An agent provider in an environment, opened from its Agent Providers page: signing in when it isn't,
/// its default model, CLI installation & updates, and availability test.
struct MachineAgentView: View {
    @Environment(AppModel.self) private var model
    let machineID: String
    let agent: String
    @State private var testing = false
    @State private var test: Result<Hermesh_Machine_V1_TestAgentResponse, Error>?
    @State private var showInstallSheet = false

    var body: some View {
        Group {
            if let m = model.machines.first(where: { $0.machineID == machineID }).map(model.providerMachine),
               let a = m.status.agents.first(where: { $0.name == agent && ($0.available || ($0.problem != .unspecified && $0.problem != .notFound)) }) {
                content(m, a)
            } else {
                ContentUnavailableView("This coding agent isn’t on this machine", systemImage: "questionmark.circle")
            }
        }
        .navigationTitle(agentTitle(agent))
        .navigationBarTitleDisplayMode(.inline)
        .sheet(isPresented: $showInstallSheet) {
            ProviderInstallSheet(machineID: machineID, provider: agent)
        }
    }

    private func content(_ m: Machine, _ a: AgentStatus) -> some View {
        let title = agentTitle(agent)
        let editable = m.online
        let cap = m.status.providerMaintenance.first { $0.provider == agent }
        let latest = m.status.providerOperations.last { $0.request.provider == agent }
        let activeOp = providerOperationActive(latest?.phase ?? "")
        return List {
            Section {
                VStack(spacing: 8) {
                    AgentLogo(id: agent, size: 58)
                    Text(title).font(.title2.bold())
                    if a.available {
                        Text("\(m.machineName) · \(a.version) · \(Text(a.loggedIn ? String(localized: "Signed in") : String(localized: "Not signed in")).foregroundStyle(a.loggedIn ? Color.secondary : .orange))")
                            .font(.subheadline).foregroundStyle(.secondary)
                            .accessibilityIdentifier("installed-version-\(a.version)")
                    } else {
                        Text("\(m.machineName) · \(Text(a.problem == .appWithoutCli ? String(localized: "Needs CLI") : String(localized: "Cannot run")).foregroundStyle(.orange))")
                            .font(.subheadline).foregroundStyle(.secondary)
                    }
                }
                .frame(maxWidth: .infinity).listRowBackground(Color.clear)
            }
            if !a.available {
                Section {
                    if a.problem == .appWithoutCli {
                        Text("Found the desktop app, but its command-line tool isn’t installed yet.").font(.subheadline)
                        if let cap, !cap.package.isEmpty && cap.managedVersion.isEmpty {
                            Button("Install Command-Line Tool…") { showInstallSheet = true }
                                .disabled(!m.online || activeOp)
                                .accessibilityIdentifier("install-\(agent)")
                        } else if let url = installPage[agent] {
                            Link("How to Install", destination: url)
                        }
                    } else if !a.detail.isEmpty {
                        Text(a.detail).font(.subheadline).foregroundStyle(.secondary)
                    }
                    Button("Check Again") { Task { await model.recheck(machine: machineID) } }
                        .disabled(!m.online)
                }
            }
            if a.available && !a.loggedIn {
                Section {
                    Label("Sign in to \(title) first so Hermesh can use it.", systemImage: "exclamationmark.triangle").foregroundStyle(.orange)
                    if let command = [
                        "claude": "claude",
                        "codex": "codex login",
                        "antigravity": "hermesh agent login antigravity",
                        "copilot": "copilot",
                        "opencode": "opencode auth login",
                        "qoder": "qodercli",
                        "qwen": "qwen",
                        "kimi": "kimi",
                        "codebuddy": "codebuddy",
                        "grok": "grok",
                        "auggie": "auggie login",
                    ][agent] {
                        Text("On \(m.machineName), run \(Text(command).monospaced()) in Terminal").font(.subheadline).foregroundStyle(.secondary)
                        Text("On a server without a browser, it shows a link: open it on this iPhone.").font(.subheadline).foregroundStyle(.secondary)
                    }
                }
            }
            if a.available {
                if !a.models.isEmpty {
                    defaults(m, a, title: title).disabled(!editable)
                } else if a.loggedIn && (a.modelsState == "unsupported" || a.modelsState == "error") {
                    Section {
                        Text(a.modelsState == "unsupported"
                            ? String(localized: "\(title) offers no choice of model; its sessions use its own settings.")
                            : String(localized: "Couldn’t read the models \(title) offers; the next check tries again. Its sessions use its own settings."))
                            .font(.subheadline).foregroundStyle(.secondary)
                    } header: {
                        Text("Default Model")
                    }
                }
                ProviderMaintenanceSection(
                    machine: m,
                    agent: agent,
                    info: a,
                    testing: testing,
                    testResult: test.map(testResultView),
                    runTest: {
                        testing = true
                        test = nil
                        Task {
                            defer { testing = false }
                            do {
                                guard let api = model.api else { return }
                                test = .success(try await api.testAgent(machine: machineID, agent: agent))
                            } catch {
                                test = .failure(error)
                            }
                        }
                    }
                )
            }
        }
        .refreshable {
            await model.recheck(machine: machineID)
        }
    }

    private func testResultView(_ r: Result<Hermesh_Machine_V1_TestAgentResponse, Error>) -> AnyView {
        AnyView(
            Group {
                switch r {
                case .success(let t) where t.ok:
                    Label {
                        Text("Replied in \(String(format: "%.1f", Double(t.millis) / 1000)) s: “\(t.reply)”")
                    } icon: {
                        Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
                    }
                case .success(let t):
                    Label {
                        Text(t.reply.isEmpty ? String(localized: "It didn’t reply.") : t.reply)
                    } icon: {
                        Image(systemName: "xmark.circle.fill").foregroundStyle(.red)
                    }
                case .failure(let e):
                    Label {
                        Text((e as? APIError)?.message ?? e.localizedDescription)
                    } icon: {
                        Image(systemName: "xmark.circle.fill").foregroundStyle(.red)
                    }
                }
            }
            .font(.subheadline)
            .accessibilityIdentifier("test-result")
        )
    }

    /// The model, reasoning effort and (Codex) speed, one menu each: the
    /// agent's own configuration unless set here.
    private func defaults(_ m: Machine, _ a: AgentStatus, title: String) -> some View {
        let d = a.defaults
        let own = a.own
        let same = { (v: String) in v.isEmpty ? String(localized: "Same as \(title)") : String(localized: "Same as \(title) (\(v))") }
        let effortsOf = { (id: String) in a.models.first { $0.id == id }?.efforts }
        let efforts = effortsOf(d.model.isEmpty ? own.model : d.model) ?? a.models.first { !$0.efforts.isEmpty }?.efforts ?? []
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
            // An agent whose models take no reasoning effort has no picker for it.
            if a.models.contains(where: { !$0.efforts.isEmpty }) {
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
            }
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
                .accessibilityIdentifier("default-speed")
            }
        } header: {
            Text("Default Model")
        } footer: {
            Text("Sessions an agent starts here without naming a model use these. \(title)’s own settings stay as they are.")
        }
    }
}
