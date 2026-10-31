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
        .confirmationDialog(removing.map { String(localized: "Remove the trusted device “\($0.label)”?") } ?? "", isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }), titleVisibility: .visible) {
            Button("Remove", role: .destructive) { if let d = removing { Task { await model.removeDevice(d) } } }
        } message: {
            Text("This device will immediately lose approval rights and its own machine will be removed. Machines and agents it previously approved will be re-signed by this iPhone so they keep working (requires Face ID).")
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

/// Where each coding agent says how to install it.
let installPage: [String: URL] = [
    "claude": URL(string: "https://github.com/anthropics/claude-code")!,
    "codex": URL(string: "https://github.com/openai/codex")!,
]

/// "v1.2.3" or "1.2.3-dev" as [1, 2, 3]; nil for a build with no version.
func versionParts(_ v: String) -> [Int]? {
    let core = v.trimmingCharacters(in: CharacterSet(charactersIn: "v")).split(separator: "-").first ?? ""
    let parts = core.split(separator: ".").compactMap { Int($0) }
    return parts.count >= 2 ? parts : nil
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

/// One machine: whether it is online, its coding agents (each opening its
/// page: defaults and projects), projects no installed agent may use, its
/// sessions, system and Hermesh version; renaming and removing are in the
/// bar's menu. Offline, one row opens how to bring it back.
struct MachineView: View {
    @Environment(AppModel.self) private var model
    let id: String
    /// Its projects, as the Watch keeps them.
    private var projects: [Project] { model.projects.filter { $0.machineID == id }.sorted { $0.projectID < $1.projectID } }
    @State private var renaming = false
    @State private var newName = ""
    @State private var confirmRemove = false
    @State private var removeProject: Project?
    @State private var checking = false
    @State private var help: Help?
    /// The name under the icon has scrolled under the bar, which shows it then.
    @State private var nameHidden = false

    private enum Help: Identifiable {
        case reconnect, update
        var id: Self { self }
    }

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
        let version = m.status.version.isEmpty ? m.version : m.status.version
        return List {
            Section {
                hero(m)
            }
            if !m.online {
                Section {
                    Button("How to Reconnect", systemImage: "questionmark.circle") { help = .reconnect }
                        .accessibilityIdentifier("machine-offline")
                }
            }
            if m.online {
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
                        Text("No coding agent found on this machine.").foregroundStyle(.secondary)
                    }
                    Button {
                        checking = true
                        Task {
                            await model.recheck(machine: m.machineID)
                            checking = false
                        }
                    } label: {
                        HStack {
                            Text("Check Again")
                            if checking { Spacer(); ProgressView() }
                        }
                    }
                    .disabled(checking)
                    .accessibilityIdentifier("check-again")
                } header: {
                    Text("Coding agents")
                } footer: {
                    if !agents.missing.isEmpty {
                        VStack(alignment: .leading, spacing: 4) {
                            Text("Not installed: \(listText(agents.missing.map(agentTitle))) · install one on \(m.machineName), then Check Again")
                            ForEach(agents.missing.filter { installPage[$0] != nil }, id: \.self) { a in
                                Link(String(localized: "How to install \(agentTitle(a))"), destination: installPage[a]!)
                            }
                        }
                    }
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
                if m.online {
                    Toggle("Accept New Sessions", isOn: Binding(get: { !m.status.paused }, set: { on in Task { await model.setPaused(m, !on) } }))
                        .accessibilityIdentifier("accept-sessions")
                }
                NavigationLink(value: Route.machineSessions(m.machineID)) {
                    LabeledContent("Sessions", value: "\(model.sessions.filter { $0.machineID == m.machineID }.count)")
                }
            } footer: {
                if m.online && m.status.paused { Text("Running sessions go on; no new ones start here.") }
            }
            Section {
                let platform = platformText(m.status.platform)
                if !platform.isEmpty { LabeledContent("Operating System", value: platform) }
                if !version.isEmpty {
                    LabeledContent {
                        HStack(spacing: 10) {
                            Text(version).lineLimit(1).truncationMode(.middle)
                            if isOlder(version) {
                                Button("Update") { help = .update }
                                    .buttonStyle(.glass)
                                    .accessibilityIdentifier("machine-update")
                            }
                        }
                    } label: {
                        Text(verbatim: "Hermesh")
                    }
                }
            }
        }
        .onScrollGeometryChange(for: Bool.self) { $0.contentOffset.y + $0.contentInsets.top > 110 } action: { _, hidden in
            withAnimation(.easeOut(duration: 0.15)) { nameHidden = hidden }
        }
        .navigationTitle(nameHidden ? m.machineName : "")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Menu("More", systemImage: "ellipsis") {
                    Button("Rename…", systemImage: "pencil") {
                        newName = m.machineName
                        renaming = true
                    }
                    .disabled(!m.online)
                    Button("Remove…", systemImage: "trash", role: .destructive) { confirmRemove = true }
                }
                .accessibilityIdentifier("machine-menu")
            }
        }
        .sheet(item: $help) { h in
            switch h {
            case .reconnect:
                CommandSheet(title: "How to Reconnect", message: "Make sure it’s on and online, then run this there:",
                             command: "hermeshd status", details: model.unreachable.first { $0.machineID == m.machineID }?.error ?? "")
            case .update:
                CommandSheet(title: "Update Hermesh", message: "On \(m.machineName), run the install command again:", command: AddMachineSheet.install)
            }
        }
        .refreshable {
            await model.recheck(machine: m.machineID)
        }
        .task(id: m.online) {
            // A machine looks for its coding agents when asked: opened with
            // none listed (just joined), it is asked once.
            guard m.online, m.status.agents.isEmpty, !checking else { return }
            checking = true
            await model.recheck(machine: m.machineID)
            checking = false
        }
        .alert("Rename this machine", isPresented: $renaming) {
            TextField("Machine name", text: $newName).textInputAutocapitalization(.never).autocorrectionDisabled()
            Button("Cancel", role: .cancel) {}
            Button("Rename") { Task { await model.rename(m, to: newName) } }
        } message: {
            Text("Agents find projects as “machine/project” and will see the new name. Use lowercase letters, digits and -.")
        }
        .confirmationDialog(device(m).map { String(localized: "Remove the trusted device “\($0.label)”?") } ?? String(localized: "Remove the machine “\(m.machineName)”?"), isPresented: $confirmRemove, titleVisibility: .visible) {
            Button("Remove", role: .destructive) {
                if let d = device(m) { Task { await model.removeDevice(d) } } else { Task { await model.remove(m) } }
            }
        } message: {
            if device(m) != nil {
                Text("This device will immediately lose approval rights and its own machine will be removed. Machines and agents it previously approved will be re-signed by this iPhone so they keep working (requires Face ID).")
            } else {
                Text("This machine will disconnect immediately and running sessions will stop. You will need to pair it again to reconnect.")
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

    /// Its icon, name (with the shield of a trusted device) and whether it is online.
    private func hero(_ m: Machine) -> some View {
        VStack(spacing: 8) {
            Image(systemName: machineIcon(m)).font(.system(size: 30)).foregroundStyle(m.online ? .secondary : .tertiary)
                .frame(width: 64, height: 64)
                .background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 16, style: .continuous))
            HStack(spacing: 6) {
                Text(m.machineName).font(.title2.bold())
                if device(m) != nil { TrustedMark() }
            }
            HStack(spacing: 6) {
                Circle().fill(m.online ? Color.green : .clear).strokeBorder(m.online ? .clear : Color.secondary, lineWidth: 1.5)
                    .frame(width: 8, height: 8)
                Text(m.online ? String(localized: "Online") : String(localized: "Offline · last online \(m.hasLastSeen ? agoText(m.lastSeen.date, now: model.now) : String(localized: "a long time ago"))"))
            }
            .font(.subheadline).foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity).listRowBackground(Color.clear)
        .accessibilityElement(children: .combine)
    }

    /// The machine's Hermesh is older than this app's. A build of no
    /// release (0.0.0) is never older.
    private func isOlder(_ v: String) -> Bool {
        guard let mine = Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String,
              let a = versionParts(v), a.contains(where: { $0 != 0 }), let b = versionParts(mine) else { return false }
        return a.lexicographicallyPrecedes(b)
    }

    /// The trusted device this machine is, if it is one.
    private func device(_ m: Machine) -> Device? { model.devices.first { $0.machineID == m.machineID } }

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

/// A command to run on a machine, to copy or send there: what it is for,
/// the command, and what went wrong when that is known.
private struct CommandSheet: View {
    let title: LocalizedStringKey
    let message: LocalizedStringKey
    let command: String
    var details = ""
    /// Its content's height: the sheet is as tall as that.
    @State private var height: CGFloat = 300

    var body: some View {
        NavigationStack {
            VStack(alignment: .leading, spacing: 16) {
                Text(message).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                MonoBox(text: command)
                CopyShareButtons(text: command, shareTitle: "Send…")
                if !details.isEmpty {
                    DisclosureGroup("Details") {
                        Text(details).font(.caption.monospaced()).foregroundStyle(.secondary).textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading).padding(.top, 8)
                    }
                    .font(.subheadline)
                }
            }
            .padding(.horizontal, 20).padding(.top, 8).padding(.bottom, 8)
            .fixedSize(horizontal: false, vertical: true)
            .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { height = $0 }
            .frame(maxHeight: .infinity, alignment: .top)
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { CloseButton() }
        }
        .presentationDetents([.height(height + 70)])
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
