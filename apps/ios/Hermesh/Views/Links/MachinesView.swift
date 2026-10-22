import HermeshProto
import HermeshSessions
import SwiftUI

/// The machines and their projects.
struct MachinesView: View {
    @Environment(AppModel.self) private var model
    @State private var projects: [Project] = []

    var body: some View {
        List {
            Section {
                ForEach(model.machines, id: \.machineID) { m in
                    NavigationLink(value: Route.machine(m.machineID)) { MachineRow(machine: m) }
                        .accessibilityIdentifier("machine-\(m.machineName)")
                }
                if model.machines.isEmpty { Text("No machines yet.").foregroundStyle(.secondary) }
            }
            if !projects.isEmpty {
                Section {
                    ForEach(projects, id: \.self) { p in
                        Label {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(p.projectID)
                                Text("\(model.machineName(p.machineID)) · \(workspaceText(p.workspace))").font(.subheadline).foregroundStyle(.secondary)
                            }
                        } icon: {
                            Image(systemName: "folder")
                        }
                    }
                } header: {
                    Text("Projects")
                } footer: {
                    Text("Agents find a project as “machine/project”.")
                }
            }
            Section {
            } footer: {
                Label("End-to-end encrypted: the server only forwards, and can’t read anything.", systemImage: "lock").frame(maxWidth: .infinity)
            }
        }
        .navigationTitle("Machines")
        .rootToolbar()
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button("Add a Machine", systemImage: "plus") { model.sheet = .addMachine }
            }
        }
        .task { projects = (try? await model.api?.projects())?.projects ?? [] }
    }
}

func workspaceText(_ w: Hermesh_Identity_V1_Workspace) -> String {
    switch w {
    case .inPlace: String(localized: "In place")
    default: String(localized: "Separate worktree")
    }
}

struct MachineRow: View {
    @Environment(AppModel.self) private var model
    let machine: Machine

    var body: some View {
        let running = Int(machine.status.runningSessions)
        HStack(spacing: 12) {
            Image(systemName: machine.status.platform.contains("linux") ? "server.rack" : machine.local ? "laptopcomputer" : "desktopcomputer")
                .frame(width: 28, height: 28).foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 2) {
                Text(machine.machineName)
                Group {
                    if !machine.online {
                        Text("Offline · last online \(machine.hasLastSeen ? agoText(machine.lastSeen.date, now: model.now) : String(localized: "a long time ago"))")
                    } else if machine.status.paused {
                        Text("Paused")
                    } else if running > 0 {
                        Text("Online · \(running) running")
                    } else {
                        Text("Online")
                    }
                }
                .font(.subheadline).foregroundStyle(.secondary)
            }
            Spacer()
        }
    }
}

/// One machine: its coding agents, projects, pausing, renaming and removing.
/// When it is offline, why its sessions can't be shown and what to check.
struct MachineView: View {
    @Environment(AppModel.self) private var model
    let id: String
    @State private var projects: [Project] = []
    @State private var details = false
    @State private var renaming = false
    @State private var newName = ""
    @State private var confirmRemove = false
    @State private var removeProject: Project?

    var body: some View {
        Group {
            if let m = model.machines.first(where: { $0.machineID == id }) {
                content(m)
            } else {
                ContentUnavailableView("This machine is gone", systemImage: "desktopcomputer")
            }
        }
        .task { await load() }
    }

    private func load() async {
        projects = (try? await model.api?.projects(machine: id))?.projects ?? []
    }

    private func content(_ m: Machine) -> some View {
        List {
            Section {
            } header: {
                Text(statusLine(m)).textCase(nil).font(.subheadline)
            }
            if !m.online { offline(m) }
            Section("Coding agents") {
                if m.online {
                    ForEach(m.status.agents.filter(\.available), id: \.name) { a in agentRow(a) }
                    if m.status.agents.filter(\.available).isEmpty { Text("No coding agent found on this machine.").foregroundStyle(.secondary) }
                } else {
                    Text("The machine is offline; they show once it connects.").foregroundStyle(.secondary)
                }
            }
            Section {
                ForEach(projects, id: \.projectID) { p in
                    VStack(alignment: .leading, spacing: 2) {
                        Text(p.projectID)
                        Text("\(p.path) · \(workspaceText(p.workspace))").font(.caption.monospaced()).foregroundStyle(.secondary)
                            .lineLimit(1).truncationMode(.middle)
                    }
                    .swipeActions {
                        Button("Remove", role: .destructive) { removeProject = p }
                    }
                }
                Button("Add a Project…", systemImage: "plus") { model.sheet = .addProject(m.machineID) }
                    .disabled(!m.online)
            } header: {
                Text("Projects")
            }
            Section {
                Toggle(isOn: Binding(get: { m.status.paused }, set: { p in Task { await model.setPaused(m, p) } })) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text("Take no new sessions")
                        Text("Running sessions aren’t affected").font(.subheadline).foregroundStyle(.secondary)
                    }
                }
                .disabled(!m.online)
                Button {
                    model.filter = SessionFilter(machine: m.machineID)
                    model.open(.sessions)
                } label: {
                    LabeledContent("Sessions", value: "\(model.sessions.filter { $0.machineID == m.machineID }.count)")
                }
                .foregroundStyle(.primary)
            }
            Section {
                Button("Rename…") {
                    newName = m.machineName
                    renaming = true
                }
                .disabled(!m.online)
                Button("Remove…", role: .destructive) { confirmRemove = true }
            }
        }
        .navigationTitle(m.machineName)
        .refreshable {
            try? await model.api?.checkAgents()
            await load()
        }
        .alert("Rename this machine", isPresented: $renaming) {
            TextField("Machine name", text: $newName).textInputAutocapitalization(.never).autocorrectionDisabled()
            Button("Cancel", role: .cancel) {}
            Button("Rename") { Task { await model.rename(m, to: newName) } }
        } message: {
            Text("Agents find projects as “machine/project” and will see the new name. Use lowercase letters, digits and -.")
        }
        .confirmationDialog("Remove “\(m.machineName)”?", isPresented: $confirmRemove, titleVisibility: .visible) {
            Button("Remove", role: .destructive) { Task { await model.remove(m) } }
        } message: {
            Text("It disconnects at once and running sessions stop. To join again it has to pair again.")
        }
        .confirmationDialog(removeProject.map { String(localized: "Remove project “\($0.projectID)”?") } ?? "", isPresented: Binding(get: { removeProject != nil }, set: { if !$0 { removeProject = nil } }), titleVisibility: .visible) {
            Button("Remove", role: .destructive) {
                if let p = removeProject {
                    Task {
                        try? await model.api?.removeProject(machine: m.machineID, project: p.projectID)
                        await load()
                    }
                }
            }
        } message: {
            Text("The directory itself isn’t deleted; agents can no longer start sessions there.")
        }
    }

    private func statusLine(_ m: Machine) -> String {
        if !m.online { return String(localized: "Offline · last online \(m.hasLastSeen ? agoText(m.lastSeen.date, now: model.now) : String(localized: "a long time ago"))") }
        if m.status.paused { return String(localized: "Takes no new sessions") }
        let r = Int(m.status.runningSessions)
        return r > 0 ? String(localized: "Online · \(r) running") : String(localized: "Online")
    }

    /// Why an offline machine's sessions can't be shown, and what to check.
    private func offline(_ m: Machine) -> some View {
        Section {
            VStack(alignment: .leading, spacing: 8) {
                Label("The machine is off, asleep, or offline", systemImage: "moon.zzz")
                Label {
                    Text("The Hermesh background service isn’t running on it: run \(Text("hermeshd status").monospaced()) there to check")
                } icon: {
                    Image(systemName: "terminal")
                }
                if let u = model.unreachable.first(where: { $0.machineID == m.machineID }), !u.error.isEmpty {
                    DisclosureGroup("Details", isExpanded: $details) {
                        Text(u.error).font(.caption.monospaced()).foregroundStyle(.secondary).textSelection(.enabled)
                    }
                }
            }
            .font(.subheadline)
        } header: {
            Text("Its sessions can’t be shown for now")
        } footer: {
            Text("Once it reconnects, its sessions and usage come back by themselves; there’s nothing for you to do.")
        }
        .accessibilityIdentifier("machine-offline")
    }

    private func agentRow(_ a: AgentStatus) -> some View {
        HStack(spacing: 12) {
            AgentLogo(id: a.name, size: 28)
            VStack(alignment: .leading, spacing: 2) {
                Text(agentTitle(a.name))
                Group {
                    if a.checking {
                        Text("Checking…")
                    } else if a.loggedIn {
                        Text("\(a.version) · signed in")
                    } else {
                        Text("\(a.version) · not signed in").foregroundStyle(.orange)
                    }
                }
                .font(.subheadline).foregroundStyle(.secondary)
            }
        }
    }
}
