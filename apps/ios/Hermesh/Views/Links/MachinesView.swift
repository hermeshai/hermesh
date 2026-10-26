import HermeshProto
import HermeshSessions
import SwiftUI

/// The machines, each opening its page, a green shield on those that are
/// trusted devices too; then the other trusted devices (which approve but
/// run nothing, as this iPhone).
struct MachinesView: View {
    @Environment(AppModel.self) private var model
    @State private var removing: Device?

    var body: some View {
        let trusted = Set(model.devices.map(\.machineID).filter { !$0.isEmpty })
        let others = model.devices.filter { d in d.machineID.isEmpty || !model.machines.contains { $0.machineID == d.machineID } }
        List {
            Section {
                ForEach(model.machines, id: \.machineID) { m in
                    NavigationLink(value: Route.machine(m.machineID)) { MachineRow(machine: m, trusted: trusted.contains(m.machineID)) }
                        .accessibilityIdentifier("machine-\(m.machineName)")
                }
                if model.machines.isEmpty {
                    if model.hasWorld { Text("No machines yet.").foregroundStyle(.secondary) } else { PageLoader(text: "Reading your machines…") }
                }
            } footer: {
                if !trusted.isEmpty || !others.isEmpty {
                    Text("\(Image(systemName: "checkmark.shield.fill")) A trusted device: it can approve requests to connect.")
                }
            }
            if !others.isEmpty {
                Section {
                    ForEach(others, id: \.keyID) { d in
                        HStack(spacing: 12) {
                            Image(systemName: d.platform == "ios" ? "iphone" : "laptopcomputer").frame(width: 28, height: 28).foregroundStyle(.secondary)
                            VStack(alignment: .leading, spacing: 2) {
                                HStack(spacing: 5) {
                                    Text(d.thisDevice ? String(localized: "This iPhone") : d.label)
                                    TrustedMark()
                                }
                                Text("Approves; doesn’t run tasks").font(.subheadline).foregroundStyle(.secondary)
                            }
                        }
                        .accessibilityElement(children: .combine)
                        .accessibilityIdentifier("device-\(d.thisDevice ? "this" : d.label)")
                        .swipeActions {
                            if !d.thisDevice { Button("Remove", role: .destructive) { removing = d } }
                        }
                        .contextMenu {
                            if !d.thisDevice { Button("Remove…", systemImage: "minus.circle", role: .destructive) { removing = d } }
                        }
                    }
                } header: {
                    Text("Other trusted devices")
                } footer: {
                    Text("Every trusted device can approve requests to connect. If one gets lost, remove it from another (swipe left on it).")
                }
            }
            Section {
            } footer: {
                Label("End-to-end encrypted: the server only forwards, and can’t read anything.", systemImage: "lock").frame(maxWidth: .infinity)
            }
        }
        .confirmationDialog(removing.map { String(localized: "Remove “\($0.label)”?") } ?? "", isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }), titleVisibility: .visible) {
            Button("Remove", role: .destructive) { if let d = removing { Task { await model.removeDevice(d) } } }
        } message: {
            Text("It can no longer approve anything at once, and its own machine is removed with it. The machines and agents it approved are reissued by this iPhone and keep working (needs Face ID).")
        }
        .navigationTitle("Machines")
        .rootToolbar()
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button("Add a Machine", systemImage: "plus") { model.sheet = .addMachine }
            }
        }
    }
}

func workspaceText(_ w: Hermesh_Identity_V1_Workspace) -> String {
    switch w {
    case .inPlace: String(localized: "In place")
    default: String(localized: "Separate worktree")
    }
}

/// A machine's "darwin/arm64" as "macOS · arm64".
func platformText(_ platform: String) -> String {
    let parts = platform.split(separator: "/").map(String.init)
    let os = ["darwin": "macOS", "linux": "Linux", "windows": "Windows"][parts.first ?? ""] ?? parts.first ?? ""
    return ([os] + parts.dropFirst()).filter { !$0.isEmpty }.joined(separator: " · ")
}

func machineIcon(_ m: Machine) -> String {
    m.status.platform.contains("linux") ? "server.rack" : m.local ? "laptopcomputer" : "desktopcomputer"
}

/// The green shield of a trusted device.
struct TrustedMark: View {
    var body: some View {
        Image(systemName: "checkmark.shield.fill").font(.footnote).foregroundStyle(.green)
            .accessibilityLabel(Text("Trusted device"))
    }
}

struct MachineRow: View {
    @Environment(AppModel.self) private var model
    let machine: Machine
    var trusted = false

    var body: some View {
        let m = machine
        let running = Int(m.status.runningSessions)
        HStack(spacing: 12) {
            Image(systemName: machineIcon(m)).frame(width: 28, height: 28).foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 5) {
                    Text(m.machineName)
                    if trusted { TrustedMark() }
                }
                Text(m.online ? platformText(m.status.platform) : String(localized: "Last online \(m.hasLastSeen ? agoText(m.lastSeen.date, now: model.now) : String(localized: "a long time ago"))"))
                    .font(.subheadline).foregroundStyle(.secondary)
            }
            Spacer()
            Circle().fill(m.online ? Color.green : .clear).strokeBorder(m.online ? .clear : Color.secondary, lineWidth: 1.5)
                .frame(width: 8, height: 8)
            Group {
                if !m.online {
                    Text("Offline")
                } else if m.status.paused {
                    Text("Paused")
                } else if running > 0 {
                    Text("\(running) running")
                } else {
                    Text("Online")
                }
            }
            .font(.subheadline).foregroundStyle(.secondary).monospacedDigit()
        }
    }
}

/// One machine: its coding agents (each opening its page: defaults and
/// projects), projects no installed agent may use, pausing, renaming and
/// removing. When it is offline, why its sessions can't be shown and what
/// to check.
struct MachineView: View {
    @Environment(AppModel.self) private var model
    let id: String
    /// Its projects, as the Watch keeps them.
    private var projects: [Project] { model.projects.filter { $0.machineID == id }.sorted { $0.projectID < $1.projectID } }
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
    }

    private func content(_ m: Machine) -> some View {
        let agents = sortedAgents(m.status.agents)
        let others = projects.filter { p in !agents.installed.contains { !projectsFor([p], $0.name).isEmpty } }
        return List {
            Section {
                VStack(spacing: 8) {
                    Image(systemName: machineIcon(m)).font(.system(size: 30)).foregroundStyle(.secondary)
                        .frame(width: 64, height: 64)
                        .background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 16, style: .continuous))
                    Text(m.machineName).font(.title2.bold())
                    if device(m) != nil {
                        Label("Trusted device", systemImage: "checkmark.shield.fill").font(.footnote.weight(.medium)).foregroundStyle(.green)
                    }
                    Text(heroLine(m)).font(.subheadline).foregroundStyle(.secondary).multilineTextAlignment(.center)
                }
                .frame(maxWidth: .infinity).listRowBackground(Color.clear)
            }
            if !m.online { offline(m) }
            Section {
                ForEach(agents.installed, id: \.name) { a in
                    NavigationLink(value: Route.machineAgent(m.machineID, a.name)) { agentRow(a) }
                        .accessibilityIdentifier("agent-\(a.name)")
                }
                ForEach(agents.checking, id: \.name) { a in
                    HStack(spacing: 12) {
                        AgentLogo(id: a.name, size: 30)
                        Text(agentTitle(a.name))
                        Spacer()
                        ProgressView()
                    }
                }
                if agents.installed.isEmpty && agents.checking.isEmpty {
                    Text(m.online ? "No coding agent found on this machine." : "The machine is offline; they show once it connects.").foregroundStyle(.secondary)
                }
            } header: {
                Text("Coding agents")
            } footer: {
                if !agents.missing.isEmpty {
                    Text("Not installed: \(listText(agents.missing.map(agentTitle))) · Hermesh finds them once they are")
                }
            }
            if !others.isEmpty {
                Section {
                    ForEach(others, id: \.projectID) { p in
                        ProjectLine(project: p, detail: ([workspaceText(p.workspace)] + p.agents.map(agentTitle)).joined(separator: " · "))
                            .swipeActions { Button("Remove", role: .destructive) { removeProject = p } }
                    }
                } header: {
                    Text("Other projects · for agents not installed")
                }
            }
            Section {
                Toggle(isOn: Binding(get: { m.status.paused }, set: { p in Task { await model.setPaused(m, p) } })) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text("Take no new sessions")
                        Text("Running sessions aren’t affected").font(.subheadline).foregroundStyle(.secondary)
                    }
                }
                .disabled(!m.online)
                NavigationLink(value: Route.machineSessions(m.machineID)) {
                    LabeledContent("Sessions", value: "\(model.sessions.filter { $0.machineID == m.machineID }.count)")
                }
            } header: {
                Text("Sessions")
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
        .navigationBarTitleDisplayMode(.inline)
        .refreshable {
            try? await model.api?.checkAgents()
        }
        .alert("Rename this machine", isPresented: $renaming) {
            TextField("Machine name", text: $newName).textInputAutocapitalization(.never).autocorrectionDisabled()
            Button("Cancel", role: .cancel) {}
            Button("Rename") { Task { await model.rename(m, to: newName) } }
        } message: {
            Text("Agents find projects as “machine/project” and will see the new name. Use lowercase letters, digits and -.")
        }
        .confirmationDialog("Remove “\(m.machineName)”?", isPresented: $confirmRemove, titleVisibility: .visible) {
            Button("Remove", role: .destructive) {
                if let d = device(m) { Task { await model.removeDevice(d) } } else { Task { await model.remove(m) } }
            }
        } message: {
            if device(m) != nil {
                Text("It can no longer approve anything at once, and its own machine is removed with it. The machines and agents it approved are reissued by this iPhone and keep working (needs Face ID).")
            } else {
                Text("It disconnects at once and running sessions stop. To join again it has to pair again.")
            }
        }
        .confirmationDialog(removeProject.map { String(localized: "Remove project “\($0.projectID)”?") } ?? "", isPresented: Binding(get: { removeProject != nil }, set: { if !$0 { removeProject = nil } }), titleVisibility: .visible) {
            Button("Remove", role: .destructive) {
                if let p = removeProject {
                    Task {
                        do {
                            try await model.removeProject(machine: m.machineID, project: p.projectID)
                        } catch { model.report(error) } // the Watch brings the change
                    }
                }
            }
        } message: {
            Text("The directory itself isn’t deleted; agents can no longer start sessions there.")
        }
    }

    /// The trusted device this machine is, if it is one.
    private func device(_ m: Machine) -> Device? { model.devices.first { $0.machineID == m.machineID } }

    private func heroLine(_ m: Machine) -> String {
        var parts = [m.online ? String(localized: "Online") : String(localized: "Offline · last online \(m.hasLastSeen ? agoText(m.lastSeen.date, now: model.now) : String(localized: "a long time ago"))")]
        if m.online && m.status.paused { parts.append(String(localized: "Takes no new sessions")) }
        parts.append(platformText(m.status.platform))
        let version = m.status.version.isEmpty ? m.version : m.status.version
        if !version.isEmpty { parts.append("Hermesh \(version)") }
        return parts.filter { !$0.isEmpty }.joined(separator: " · ")
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

    /// An installed agent: whether it is signed in, how many projects it may
    /// work in, and its defaults when set.
    private func agentRow(_ a: AgentStatus) -> some View {
        let n = projectsFor(projects, a.name).count
        let rest = [n > 0 ? String(localized: "\(n) projects") : String(localized: "No projects yet"), defaultsText(a)].filter { !$0.isEmpty }
        return HStack(spacing: 12) {
            AgentLogo(id: a.name, size: 30)
            VStack(alignment: .leading, spacing: 2) {
                Text(agentTitle(a.name))
                Text("\(Text(a.loggedIn ? String(localized: "Signed in") : String(localized: "Not signed in")).foregroundStyle(a.loggedIn ? Color.secondary : .orange)) · \(rest.joined(separator: " · "))")
                    .font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
            }
        }
    }
}

/// A project in a list: its name, how agents work in it and its directory.
struct ProjectLine: View {
    let project: Project
    let detail: String

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: "folder").foregroundStyle(.secondary).frame(width: 30)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(project.projectID)
                    Text(detail).font(.subheadline).foregroundStyle(.secondary)
                }
                .lineLimit(1)
                Text(project.path).font(.caption.monospaced()).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
            }
        }
    }
}
