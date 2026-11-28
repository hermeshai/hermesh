import HermeshHer
import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI

// The environments and what each has (docs/settings-environments.md §4.2, §5.9): one page per
// environment, as Settings shows a device, and its pages: name, agent providers, projects, Her
// and storage. The same structure as the Mac's settings; this iPhone is not one of them.

/// A size as Files counts it.
func byteText<T: BinaryInteger>(_ n: T) -> String {
    ByteCountFormatter.string(fromByteCount: Int64(n), countStyle: .file)
}

/// hermeshd there answers method of its admin service: what its status lists; nil for a machine
/// from before the list, taken as yes (docs/version-skew.md §6.1).
func machineAnswers(_ m: Machine, _ method: String) -> Bool? {
    m.status.methods.isEmpty ? nil : m.status.methods.contains(method)
}

/// The environments, each opening its page: where the overview sends the owner when several can't be
/// reached.
struct EnvironmentsView: View {
    var isRoot = true
    @Environment(AppModel.self) private var model
    @Environment(\.barsOnTheSide) private var sideBars
    @Environment(\.menuCoversPage) private var menuCoversPage

    var body: some View {
        let trusted = Set(model.devices.map(\.machineID).filter { !$0.isEmpty })
        List {
            Section {
                EnvironmentRows(trusted: trusted)
            }
        }
        .navigationTitle("Environments")
        .rootToolbar(enabled: isRoot)
        .toolbar {
            if !(isRoot && sideBars && !model.menuBeside) && !menuCoversPage {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Add Environment…", systemImage: "plus") { model.sheet = .addMachine }
                }
            }
        }
    }
}

/// One row per environment, its state on the right.
struct EnvironmentRows: View {
    var trusted: Set<String> = []
    @Environment(AppModel.self) private var model

    var body: some View {
        ForEach(model.machines, id: \.machineID) { m in
            NavigationLink(value: Route.machine(m.machineID)) { MachineRow(machine: m, trusted: trusted.contains(m.machineID)) }
                .accessibilityIdentifier("machine-\(m.machineName)")
        }
        if model.machines.isEmpty {
            if model.hasWorld { Text("No environments yet.").foregroundStyle(.secondary) } else { PageLoader(text: "Reading your environments…") }
        }
    }
}

/// An environment: its icon, name and state; paused, a banner to resume; its name, agent providers,
/// projects, Her and storage, each a page; its system and Hermesh; pausing and removing it.
struct EnvironmentView: View {
    @Environment(AppModel.self) private var model
    let id: String
    @State private var confirmRemove = false
    @State private var help: Help?
    @State private var storage: Hermesh_Machine_V1_ListWorkspacesResponse?

    private enum Help: Identifiable {
        case reconnect, update
        var id: Self { self }
    }

    var body: some View {
        Group {
            if let m = model.machines.first(where: { $0.machineID == id }) {
                content(m)
            } else {
                ContentUnavailableView("This environment is gone", systemImage: "desktopcomputer")
            }
        }
    }

    private func content(_ m: Machine) -> some View {
        let agents = sortedAgents(m.status.agents)
        let ready = agents.installed.filter(\.loggedIn).count
        let unsigned = agents.installed.count - ready
        let projects = model.projects.filter { $0.machineID == m.machineID }.count
        let version = m.status.version.isEmpty ? m.version : m.status.version
        let answers = machineAnswers(m, "ListWorkspaces")
        return List {
            Section { hero(m) }
            if m.online && m.status.paused {
                Section {
                    HStack(spacing: 12) {
                        Image(systemName: "pause.circle.fill").foregroundStyle(.orange)
                        VStack(alignment: .leading, spacing: 2) {
                            Text("Not accepting new tasks")
                            Text("Running tasks go on; new ones start once you resume.").font(.subheadline).foregroundStyle(.secondary)
                        }
                        Spacer(minLength: 0)
                        Button("Resume") { Task { await model.setPaused(m, false) } }.buttonStyle(.bordered)
                            .accessibilityIdentifier("env-resume")
                    }
                    .listRowBackground(Color.orange.opacity(0.12))
                }
            }
            if !m.online {
                Section {
                    Button("How to Reconnect", systemImage: "questionmark.circle") { help = .reconnect }
                        .accessibilityIdentifier("machine-offline")
                }
            }
            Section {
                NavigationLink(value: Route.environmentName(m.machineID)) {
                    LabeledContent("Name", value: m.machineName)
                }
                .disabled(!m.online)
                .accessibilityIdentifier("env-name")
            }
            Section {
                entry("Agent Providers", symbol: "chevron.left.forwardslash.chevron.right", color: .gray, route: .providers(m.machineID), id: "env-providers", off: !m.online && model.providerMachine(m).status.providerMaintenance.isEmpty) {
                    if m.online {
                        Text("\(ready) available") + (unsigned > 0 ? Text(" · \(unsigned)").foregroundStyle(.orange) : Text(verbatim: ""))
                    } else {
                        Text(verbatim: "—")
                    }
                }
                entry("Projects", symbol: "folder.fill", color: .teal, route: .projects(m.machineID), id: "env-projects", off: !m.online) {
                    Text(m.online ? "\(projects)" : "—")
                }
                entry("Her", symbol: "heart.fill", color: .purple, route: .environmentHer(m.machineID), id: "env-her", off: false) {
                    Text(m.her.enabled ? "On" : "Off")
                }
                entry("Storage", symbol: "externaldrive.fill", color: .brown, route: .storage(m.machineID), id: "env-storage", off: !m.online) {
                    if !m.online { Text(verbatim: "—") } else if answers == false { Text("Needs Update") } else if let storage { Text(byteText(storage.workspaces.reduce(0) { $0 + $1.sizeBytes })) } else { Text(verbatim: "") }
                }
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
            if m.online {
                Section {
                    Button(m.status.paused ? "Accept New Tasks" : "Stop Accepting New Tasks") { Task { await model.setPaused(m, !m.status.paused) } }
                        .accessibilityIdentifier("accept-sessions")
                } footer: {
                    Text("Running tasks aren’t affected.")
                }
            }
            Section {
                Button("Remove…", role: .destructive) { confirmRemove = true }
                    .frame(maxWidth: .infinity)
                    .accessibilityIdentifier("env-remove")
            } footer: {
                if device(m) != nil { Text("“\(m.machineName)” is also your trusted device: removing it removes the device too.") }
            }
        }
        .navigationTitle(m.machineName)
        .navigationBarTitleDisplayMode(.inline)
        .task(id: m.online) {
            // A machine looks for its coding agents when asked: opened with none listed (just joined), it is asked once.
            if m.online, m.status.agents.isEmpty { await model.recheck(machine: m.machineID) }
            if m.online, answers != false { storage = try? await model.workspaces(m.machineID, cached: true) }
        }
        .refreshable { await model.recheck(machine: m.machineID) }
        .sheet(item: $help) { h in
            switch h {
            case .reconnect:
                CommandSheet(title: "How to Reconnect", message: "Make sure it’s on and online, then run this there:",
                             command: "hermeshd status", details: model.unreachable.first { $0.machineID == m.machineID }?.error ?? "")
            case .update:
                CommandSheet(title: "Update Hermesh", message: "On \(m.machineName), run the install command again:", command: AddMachineSheet.install)
            }
        }
        // Her running there or her data kept: it can go too, while the machine answers (docs/her-server.md §5.6).
        .confirmationDialog(device(m).map { String(localized: "Remove the trusted device “\($0.label)”?") } ?? String(localized: "Remove the environment “\(m.machineName)”?"), isPresented: $confirmRemove, titleVisibility: .visible) {
            if hasHer(m) && herReachable(m) {
                Button("Remove, Keep Her’s Data", role: .destructive) { remove(m, eraseHer: false) }
                Button("Remove and Erase Her’s Data", role: .destructive) { remove(m, eraseHer: true) }
            } else {
                Button("Remove", role: .destructive) { remove(m, eraseHer: false) }
            }
        } message: {
            let base = device(m) != nil
                ? String(localized: "This device will immediately lose approval rights and its own machine will be removed. Machines and agents it previously approved will be re-signed by this iPhone so they keep working (requires Face ID).")
                : String(localized: "This environment will disconnect immediately and running tasks will stop. You will need to pair it again to reconnect.")
            if !hasHer(m) {
                Text(base)
            } else if herReachable(m) {
                Text(base + "\n\n" + String(localized: "Kept, Her’s data stays on it. Erasing deletes her assistants, conversations, memory, routines, files, connectors and model logins there, and can’t be undone."))
            } else {
                Text(base + "\n\n" + String(localized: "Her’s data stays on it: it’s offline, so it can’t be erased now."))
            }
        }
    }

    /// A row that opens one of its pages: a tile, its name and a value.
    private func entry(_ title: LocalizedStringKey, symbol: String, color: Color, route: Route, id: String, off: Bool, @ViewBuilder value: () -> some View) -> some View {
        NavigationLink(value: route) {
            LabeledContent {
                value().foregroundStyle(.secondary).monospacedDigit()
            } label: {
                Label { Text(title) } icon: { SettingsIcon(symbol: symbol, color: color) }
            }
        }
        .disabled(off)
        .accessibilityIdentifier(id)
    }

    /// Its icon, name, system, and a capsule: online and what runs there, or offline since when.
    private func hero(_ m: Machine) -> some View {
        let running = Int(m.status.runningSessions)
        return VStack(spacing: 8) {
            Image(systemName: machineIcon(m)).font(.system(size: 34)).foregroundStyle(.white)
                .frame(width: 72, height: 72)
                .background(Color.gray.gradient, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            HStack(spacing: 6) {
                Text(m.machineName).font(.title2.bold())
                if device(m) != nil { TrustedMark() }
            }
            Text(platformText(m.status.platform)).font(.subheadline).foregroundStyle(.secondary)
            HStack(spacing: 6) {
                Circle().fill(m.online ? Color.green : .clear).strokeBorder(m.online ? .clear : Color.secondary, lineWidth: 1.5)
                    .frame(width: 7, height: 7)
                if !m.online {
                    Text("Offline · last online \(m.hasLastSeen ? agoText(m.lastSeen.date, now: model.now) : String(localized: "a long time ago"))")
                } else if running > 0 {
                    Text("Online · \(running) tasks running")
                } else {
                    Text("Online")
                }
            }
            .font(.footnote).foregroundStyle(.secondary)
            .padding(.horizontal, 12).padding(.vertical, 5)
            .background(Color(.secondarySystemGroupedBackground), in: .capsule)
            .accessibilityIdentifier("env-state")
        }
        .frame(maxWidth: .infinity).listRowBackground(Color.clear)
        .accessibilityElement(children: .combine)
    }

    /// The machine's Hermesh is older than this app's. A build of no release (0.0.0) is never older.
    private func isOlder(_ v: String) -> Bool {
        guard let mine = Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String,
              let a = versionParts(v), a.contains(where: { $0 != 0 }), let b = versionParts(mine) else { return false }
        return a.lexicographicallyPrecedes(b)
    }

    /// The trusted device this machine is, if it is one.
    private func device(_ m: Machine) -> Device? { model.devices.first { $0.machineID == m.machineID } }

    /// Her runs on it, or her data is still there.
    private func hasHer(_ m: Machine) -> Bool { m.her.enabled || herData(m) }

    /// Removes it; with eraseHer, Her's data there is erased first, with the same approval. A trusted device's
    /// machine goes with the device.
    private func remove(_ m: Machine, eraseHer: Bool) {
        Task {
            if let d = device(m) {
                await model.removeDevice(d, eraseHer: eraseHer)
            } else {
                await model.remove(m, eraseHer: eraseHer)
            }
        }
    }
}

/// An environment's name, as agents find its projects ("name/project").
struct EnvironmentNameView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let id: String
    @State private var name = ""

    var body: some View {
        let m = model.machines.first { $0.machineID == id }
        List {
            Section {
                TextField("Name", text: $name).textInputAutocapitalization(.never).autocorrectionDisabled().font(.body.monospaced())
                    .submitLabel(.done).onSubmit(save)
                    .accessibilityIdentifier("env-name-field")
            } footer: {
                Text("Agents find projects as “name/project” and will see the new name. Use lowercase letters, digits and -.")
            }
        }
        .navigationTitle("Name")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button("Save", systemImage: "checkmark", role: .confirm, action: save).disabled(name.trimmingCharacters(in: .whitespaces).isEmpty || name == m?.machineName)
            }
        }
        .onAppear { name = m?.machineName ?? "" }
    }

    private func save() {
        guard let m = model.machines.first(where: { $0.machineID == id }), !name.isEmpty, name != m.machineName else { return }
        Task {
            await model.rename(m, to: name)
            dismiss()
        }
    }
}

/// An environment's agent providers: each installed one opening its page, those being checked, Check
/// Again; the ones not installed, with one-tap Hermesh installation or how to install externally.
struct ProvidersView: View {
    @Environment(AppModel.self) private var model
    let id: String
    @State private var checking = false
    @State private var installTarget: String?
    @State private var cancelling = false

    var body: some View {
        let m = model.machines.first { $0.machineID == id }.map(model.providerMachine)
        let agents = sortedAgents(m?.status.agents ?? [])
        let capOf = { (name: String) in m?.status.providerMaintenance.first { $0.provider == name } }
        let latestOf = { (name: String) in m?.status.providerOperations.last { $0.request.provider == name } }
        let orderedMissing = agents.missing.sorted { (capOf($0)?.package.isEmpty == false ? 0 : 1) < (capOf($1)?.package.isEmpty == false ? 0 : 1) }
        List {
            Section {
                ForEach(agents.installed, id: \.name) { a in
                    let cap = capOf(a.name)
                    let latest = latestOf(a.name)
                    let active = providerOperationActive(latest?.phase ?? "")
                    let hasUpdate = !(cap?.managedVersion.isEmpty ?? true) && !(cap?.targetVersion.isEmpty ?? true) && cap?.managedVersion != cap?.targetVersion
                    NavigationLink(value: Route.machineAgent(id, a.name)) {
                        LabeledContent {
                            if active && m?.online == true {
                                HStack(spacing: 6) {
                                    ProgressView()
                                    Text(providerPhaseTitle(latest?.phase ?? ""))
                                }
                                .font(.subheadline)
                            } else {
                                Text(a.loggedIn ? String(localized: "Signed in") : String(localized: "Not signed in")).foregroundStyle(a.loggedIn ? Color.secondary : .orange)
                            }
                        } label: {
                            HStack(spacing: 12) {
                                AgentLogo(id: a.name, size: 30)
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(agentTitle(a.name))
                                    if hasUpdate, let target = cap?.targetVersion {
                                        Text("\(a.version) · \(String(localized: "Update \(target) available"))").font(.caption.monospaced()).foregroundStyle(.secondary)
                                    } else {
                                        Text(a.version).font(.caption.monospaced()).foregroundStyle(.secondary)
                                    }
                                }
                            }
                        }
                    }
                    .accessibilityIdentifier("agent-\(a.name)")
                }
                ForEach(agents.troubled, id: \.name) { a in
                    NavigationLink(value: Route.machineAgent(id, a.name)) {
                        LabeledContent {
                            Text(a.problem == .appWithoutCli ? String(localized: "Needs CLI") : String(localized: "Cannot run")).foregroundStyle(.orange)
                        } label: {
                            HStack(spacing: 12) {
                                AgentLogo(id: a.name, size: 30)
                                Text(agentTitle(a.name))
                            }
                        }
                    }
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
                if agents.installed.isEmpty && agents.troubled.isEmpty && agents.checking.isEmpty {
                    Text("No agent provider found here.").foregroundStyle(.secondary)
                }
                Button {
                    checking = true
                    Task {
                        await model.recheck(machine: id)
                        checking = false
                    }
                } label: {
                    HStack {
                        Text("Check Again")
                        if checking { Spacer(); ProgressView() }
                    }
                }
                .disabled(checking || m?.online != true)
                .accessibilityIdentifier("check-again")
            } footer: {
                Text("Hermesh runs them on the command line as you do, never reading API keys or tokens.")
            }
            if !orderedMissing.isEmpty {
                Section {
                    ForEach(orderedMissing, id: \.self) { a in
                        let cap = capOf(a)
                        let canManage = cap?.package.isEmpty == false
                        let job = latestOf(a)
                        let active = providerOperationActive(job?.phase ?? "")
                        let failed = job?.phase == "failed" || job?.phase == "interrupted"
                        LabeledContent {
                            if active {
                                HStack(spacing: 8) {
                                    if m?.online == true { ProgressView() }
                                    Text(m?.online == true ? providerPhaseTitle(job?.phase ?? "") : "Offline").font(.subheadline)
                                    if m?.online == true, let job, ["preflight", "waiting"].contains(job.phase) {
                                        Button("Cancel") {
                                            cancelling = true
                                            var op = Hermesh_Identity_V1_MaintainProvider()
                                            op.provider = a
                                            op.action = "cancel"
                                            op.requestID = job.request.requestID
                                            Task {
                                                defer { cancelling = false }
                                                await model.maintainProvider(machine: id, operation: op)
                                            }
                                        }
                                        .buttonStyle(.bordered)
                                        .disabled(cancelling)
                                    }
                                }
                            } else if canManage {
                                Button(failed ? String(localized: "Retry") : String(localized: "Install…")) { installTarget = a }
                                    .buttonStyle(.bordered)
                                    .disabled(m?.online != true)
                                    .accessibilityIdentifier("install-\(a)")
                            } else if let url = installPage[a] {
                                Link("How to Install", destination: url).font(.subheadline)
                            }
                        } label: {
                            HStack(spacing: 12) {
                                AgentLogo(id: a, size: 30).grayscale(canManage || active ? 0 : 1).opacity(canManage || active ? 1 : 0.5)
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(agentTitle(a))
                                    if active {
                                        Text(job?.detail.isEmpty == false ? job!.detail : String(localized: "Running installer")).font(.caption).foregroundStyle(.secondary)
                                    } else if failed, let detail = job?.detail, !detail.isEmpty {
                                        Text(detail).font(.caption).foregroundStyle(.orange)
                                    } else if let cap, canManage {
                                        Text("npm · \(cap.package)@\(cap.targetVersion)").font(.caption.monospaced()).foregroundStyle(.secondary)
                                    } else {
                                        Text("Not installed").font(.subheadline).foregroundStyle(.secondary)
                                    }
                                }
                            }
                        }
                    }
                } header: {
                    Text("Not Installed")
                } footer: {
                    if m?.status.providerMaintenance.contains(where: { $0.account == "root" }) == true,
                       m?.status.providerMaintenance.allSatisfy({ $0.package.isEmpty }) == true {
                        Text("Hermesh runs as root in this environment, so remote tool installation is off. Install one in a terminal or reinstall Hermesh for a non-root user (--user), then Check Again.")
                    }
                }
            }
            if let m, !m.status.providerOperations.isEmpty {
                Section {
                    NavigationLink {
                        ProviderHistoryView(machineID: id)
                    } label: {
                        Label("Installation and Update Activity", systemImage: "clock.arrow.circlepath")
                    }
                    .accessibilityIdentifier("provider-history")
                }
            }
        }
        .navigationTitle("Agent Providers")
        .navigationBarTitleDisplayMode(.inline)
        .sheet(isPresented: Binding(get: { installTarget != nil }, set: { if !$0 { installTarget = nil } })) {
            if let target = installTarget {
                ProviderInstallSheet(machineID: id, provider: target)
            }
        }
    }
}

/// An environment's projects, for every agent provider there: each opening its page; swiped, removed.
struct ProjectsView: View {
    @Environment(AppModel.self) private var model
    let id: String
    @State private var removing: Project?

    var body: some View {
        let m = model.machines.first { $0.machineID == id }
        let projects = model.projects.filter { $0.machineID == id }.sorted { $0.projectID < $1.projectID }
        List {
            Section {
                ForEach(projects, id: \.projectID) { p in
                    NavigationLink(value: Route.project(id, p.projectID)) {
                        LabeledContent {
                            Text(workspaceText(p.workspace)).font(.subheadline)
                        } label: {
                            HStack(spacing: 12) {
                                SettingsIcon(symbol: "folder.fill", color: .teal)
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(p.projectID)
                                    Text(p.path).font(.caption.monospaced()).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
                                }
                            }
                        }
                    }
                    .swipeActions { Button("Remove", role: .destructive) { removing = p } }
                    .accessibilityIdentifier("project-\(p.projectID)")
                }
                if projects.isEmpty { Text("No projects yet").foregroundStyle(.secondary) }
            } footer: {
                Text("Every signed-in agent provider here can work in these projects; to limit an agent, change its scope in Access.")
            }
            Section {
                Button("Add Project…") { model.sheet = .addProject(id) }
                    .disabled(m?.online != true)
                    .accessibilityIdentifier("add-project")
            }
        }
        .navigationTitle("Projects")
        .navigationBarTitleDisplayMode(.inline)
        .confirmationDialog(removing.map { String(localized: "Remove project “\($0.projectID)”?") } ?? "", isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }), titleVisibility: .visible) {
            Button("Remove", role: .destructive) {
                if let p = removing {
                    Task { do { try await model.removeProject(machine: id, project: p.projectID) } catch { model.report(error) } }
                }
            }
        } message: {
            Text("The directory itself isn’t deleted; agents can no longer start sessions there.")
        }
    }
}

/// A project: its folder, how agents work in it, and removing it.
struct ProjectView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let machineID: String
    let projectID: String
    @State private var removing = false

    var body: some View {
        Group {
            if let p = model.projects.first(where: { $0.machineID == machineID && $0.projectID == projectID }) {
                content(p)
            } else {
                ContentUnavailableView("This project is gone", systemImage: "folder")
            }
        }
        .navigationTitle(projectID)
        .navigationBarTitleDisplayMode(.inline)
    }

    private func content(_ p: Project) -> some View {
        let online = model.machines.first { $0.machineID == machineID }?.online == true
        return List {
            Section {
                LabeledContent("Path") { Text(p.path).font(.subheadline.monospaced()).lineLimit(2).truncationMode(.middle) }
            }
            Section("How Agents Work") {
                choice(p, .worktree, "Separate Worktree: Each Session on Its Own Branch").disabled(!online)
                choice(p, .inPlace, "In Place: Change This Folder Directly").disabled(!online)
            }
            Section {
                Button("Remove Project…", role: .destructive) { removing = true }
                    .accessibilityIdentifier("project-remove")
            } footer: {
                Text("The directory itself isn’t deleted; agents can no longer start sessions there.")
            }
        }
        .confirmationDialog("Remove project “\(p.projectID)”?", isPresented: $removing, titleVisibility: .visible) {
            Button("Remove", role: .destructive) {
                Task {
                    do {
                        try await model.removeProject(machine: machineID, project: p.projectID)
                        dismiss()
                    } catch { model.report(error) }
                }
            }
        }
    }

    private func choice(_ p: Project, _ w: Hermesh_Identity_V1_Workspace, _ title: LocalizedStringKey) -> some View {
        let on = (p.workspace == .inPlace) == (w == .inPlace)
        return Button {
            guard !on else { return }
            Task { do { try await model.saveProject(p, workspace: w) } catch { model.report(error) } }
        } label: {
            HStack {
                Text(title).foregroundStyle(.primary)
                Spacer()
                if on { Image(systemName: "checkmark").foregroundStyle(.tint) }
            }
        }
        .accessibilityIdentifier(w == .inPlace ? "workspace-in-place" : "workspace-worktree")
    }
}

/// An environment's Her: running there or not; where she is off with her data kept, erasing it.
struct EnvironmentHerView: View {
    @Environment(AppModel.self) private var model
    let id: String
    @State private var erasing = false

    var body: some View {
        let m = model.machines.first { $0.machineID == id }
        List {
            if let m {
                Section {
                    LabeledContent("Status") { Text(m.her.enabled ? "On" : "Off").foregroundStyle(m.her.enabled ? .green : .secondary) }
                    if m.her.enabled {
                        Button("Open Her") { model.open(.her) }
                    }
                } footer: {
                    Text(m.her.enabled ? "Her’s assistants, conversations and settings are kept in this environment." : "Turn Her on here from Her’s page: Turn On Her on Another Machine…")
                }
                if !m.her.enabled && herData(m) && herReachable(m) {
                    Section {
                        Button("Erase Her’s Data…", role: .destructive) { erasing = true }
                            .accessibilityIdentifier("machine-erase-her")
                    } footer: {
                        Text("Her is off. Her’s data is still on this machine; turned on again, she carries on.")
                    }
                }
            }
        }
        .navigationTitle("Her")
        .navigationBarTitleDisplayMode(.inline)
        .confirmationDialog("Erase Her’s data on “\(m?.machineName ?? "")”?", isPresented: $erasing, titleVisibility: .visible) {
            Button("Erase", role: .destructive) {
                guard let m else { return }
                Task { if await model.eraseHer(m) { model.show(String(localized: "Her’s data on \(m.machineName) is erased")) } }
            }
        } message: {
            Text("Her’s assistants, conversations, memory, routines, files, connectors and model logins on \(m?.machineName ?? "") are deleted. This can’t be undone.")
        }
    }
}

/// Why a worktree stays.
func blockText(_ w: Hermesh_Machine_V1_Workspace) -> String {
    switch w.block {
    case .busy: String(localized: "Task in progress")
    case .changes: String(localized: "Uncommitted changes")
    case .localFiles: String(localized: "Files its branch can’t restore: \(w.blockPath)")
    default: String(localized: "Can’t confirm this is the task’s worktree")
    }
}

/// An environment's storage, as iPhone Storage shows it: what its tasks' worktrees take, by project;
/// recommendations; each project opening its worktrees, the idle ones released with a swipe.
struct StorageView: View {
    @Environment(AppModel.self) private var model
    let id: String
    @State private var res: Hermesh_Machine_V1_ListWorkspacesResponse?
    @State private var failed: Error?
    @State private var asking: Ask?

    private enum Ask: Identifiable {
        case auto, free
        var id: Self { self }
    }

    var body: some View {
        let m = model.machines.first { $0.machineID == id }
        List {
            if let m, machineAnswers(m, "ListWorkspaces") == false || (failed as? APIError)?.code == "unimplemented" {
                NeedsUpdateView(machineName: m.machineName)
            } else if let res {
                content(res, name: m?.machineName ?? "")
            } else if let failed {
                Text((failed as? APIError)?.message ?? failed.localizedDescription).foregroundStyle(.secondary)
            } else {
                PageLoader(text: "Measuring…")
            }
        }
        .navigationTitle("Storage")
        .navigationBarTitleDisplayMode(.inline)
        .task { await load(fresh: true) }
        .refreshable { await load(fresh: true) }
        .confirmationDialog(asking == .auto ? String(localized: "Release idle worktrees automatically?") : String(localized: "Release \(idle.count) idle worktrees?"),
                            isPresented: Binding(get: { asking != nil }, set: { if !$0 { asking = nil } }), titleVisibility: .visible) {
            if asking == .auto {
                Button("Turn On") { Task { await model.setWorkspacePolicy(id, autoRelease: true); await load(fresh: false) } }
            } else {
                Button("Release", role: .destructive) { Task { await model.releaseWorkspaces(id, idle); await load(fresh: false) } }
            }
        } message: {
            Text(asking == .auto
                ? String(localized: "Worktrees idle for 7 days with no local changes are released. Branches stay; turn it off here any time.")
                : String(localized: "\(byteText(idle.reduce(0) { $0 + $1.sizeBytes })) will be freed. Branches stay; each task’s next message restores its worktree."))
        }
    }

    private var idle: [Hermesh_Machine_V1_Workspace] { res?.workspaces.filter { $0.block == .unspecified } ?? [] }

    private func load(fresh: Bool) async {
        do {
            res = try await model.workspaces(id, cached: !fresh)
            failed = nil
        } catch {
            failed = error
        }
    }

    @ViewBuilder private func content(_ res: Hermesh_Machine_V1_ListWorkspacesResponse, name: String) -> some View {
        let ws = res.workspaces
        let projects = Array(Set(ws.map(\.projectID))).sorted { a, b in size(ws, a) > size(ws, b) }
        let colors: [Color] = [.indigo, .teal, .brown, .purple, .gray]
        let total = ws.reduce(0) { $0 + $1.sizeBytes }
        Section {
            VStack(alignment: .leading, spacing: 10) {
                HStack {
                    Text("Task Worktrees").font(.headline)
                    Spacer()
                    Text("\(byteText(total)) used").foregroundStyle(.secondary)
                }
                GeometryReader { g in
                    HStack(spacing: 2) {
                        ForEach(Array(projects.enumerated()), id: \.element) { i, p in
                            colors[i % colors.count].opacity(0.75).frame(width: max(4, g.size.width * CGFloat(size(ws, p)) / CGFloat(max(total, 1)) - 2))
                        }
                    }
                }
                .frame(height: 22).clipShape(RoundedRectangle(cornerRadius: 6))
                HStack(spacing: 14) {
                    ForEach(Array(projects.enumerated()), id: \.element) { i, p in
                        HStack(spacing: 5) {
                            Circle().fill(colors[i % colors.count].opacity(0.75)).frame(width: 8, height: 8)
                            Text(p)
                        }
                    }
                }
                .font(.caption).foregroundStyle(.secondary)
            }
            .padding(.vertical, 6)
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("storage-summary")
        }
        Section("Recommendations") {
            recommendation("Release Idle Worktrees Automatically", detail: "Worktrees idle for 7 days with no local changes are released. Branches stay; the next message restores them.", symbol: "clock.fill", color: .indigo) {
                if res.autoRelease {
                    Label("On", systemImage: "checkmark").foregroundStyle(.secondary).labelStyle(.titleAndIcon)
                } else {
                    Button("Turn On") { asking = .auto }.buttonStyle(.bordered).accessibilityIdentifier("storage-auto")
                }
            }
            recommendation("Release Idle Worktrees", detail: idle.isEmpty ? "Nothing to release." : "\(idle.count) tasks are idle: \(byteText(idle.reduce(0) { $0 + $1.sizeBytes })) can be released.", symbol: "sparkles", color: .teal) {
                if idle.isEmpty {
                    Label("Done", systemImage: "checkmark").foregroundStyle(.secondary)
                } else {
                    Button("Release") { asking = .free }.buttonStyle(.bordered).accessibilityIdentifier("storage-free")
                }
            }
        }
        Section {
            ForEach(projects, id: \.self) { p in
                NavigationLink(value: Route.storageProject(id, p)) {
                    LabeledContent {
                        Text(byteText(size(ws, p))).monospacedDigit()
                    } label: {
                        HStack(spacing: 12) {
                            SettingsIcon(symbol: "folder.fill", color: .teal)
                            VStack(alignment: .leading, spacing: 2) {
                                Text(p)
                                Text("\(ws.filter { $0.projectID == p }.count) worktrees").font(.subheadline).foregroundStyle(.secondary)
                            }
                        }
                    }
                }
                .accessibilityIdentifier("storage-project-\(p)")
            }
            if projects.isEmpty { Text("No task worktrees take up space here.").foregroundStyle(.secondary) }
        } footer: {
            Text("Releasing deletes the worktrees on “\(name)” only. Branches stay, and a task’s next message restores its worktree.")
        }
    }

    private func size(_ ws: [Hermesh_Machine_V1_Workspace], _ p: String) -> UInt64 {
        ws.filter { $0.projectID == p }.reduce(0) { $0 + $1.sizeBytes }
    }

    private func recommendation(_ title: LocalizedStringKey, detail: LocalizedStringKey, symbol: String, color: Color, @ViewBuilder action: () -> some View) -> some View {
        HStack(spacing: 12) {
            SettingsIcon(symbol: symbol, color: color)
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                Text(detail).font(.subheadline).foregroundStyle(.secondary)
            }
            Spacer(minLength: 0)
            action()
        }
    }
}

/// A project's worktrees in an environment: the idle ones released with a swipe; the others say why
/// they stay.
struct StorageProjectView: View {
    @Environment(AppModel.self) private var model
    let machineID: String
    let projectID: String
    @State private var ws: [Hermesh_Machine_V1_Workspace] = []

    var body: some View {
        List {
            Section {
                ForEach(ws, id: \.sessionID) { w in
                    LabeledContent {
                        Text(byteText(w.sizeBytes)).monospacedDigit()
                    } label: {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(w.title.isEmpty ? w.sessionID : w.title).foregroundStyle(w.block == .unspecified ? .primary : .secondary)
                            if w.block == .unspecified {
                                Text(w.hasLastActive ? agoText(w.lastActive.date, now: model.now) : "").font(.subheadline).foregroundStyle(.secondary)
                            } else {
                                Text(blockText(w)).font(.subheadline).foregroundStyle(.orange)
                            }
                        }
                    }
                    .swipeActions {
                        if w.block == .unspecified {
                            Button("Release") { Task { await model.releaseWorkspaces(machineID, [w]); await load() } }.tint(.red)
                        }
                    }
                    .accessibilityIdentifier("worktree-\(w.title)")
                }
                if ws.isEmpty { Text("All released.").foregroundStyle(.secondary) }
            } footer: {
                Text("Swipe left on an idle worktree to release it. Ones with local changes or work under way can’t be.")
            }
        }
        .navigationTitle(projectID)
        .navigationBarTitleDisplayMode(.inline)
        .task { await load() }
    }

    private func load() async {
        ws = ((try? await model.workspaces(machineID, cached: true))?.workspaces ?? []).filter { $0.projectID == projectID }
    }
}
