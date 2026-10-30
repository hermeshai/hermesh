import HermeshProto
import HermeshSessions
import SwiftUI

/// Shows the sheet the model asks for.
struct SheetHost: View {
    let kind: SheetKind

    var body: some View {
        switch kind {
        case .account: AccountSheet()
        case .pairing(let code): PairingSheet(code: code)
        case .sessionInfo(let id): SessionInfoSheet(id: id)
        case .renew(let grant): RenewSheet(grantID: grant)
        case .scope(let grant): ScopeSheet(grantID: grant)
        case .connectAgent: ConnectAgentSheet()
        case .addMachine: AddMachineSheet()
        case .enterCode: EnterCodeSheet()
        case .scan: ScanSheet()
        case .addDevice: AddDeviceSheet()
        case .addProject(let machine, let agent): AddProjectSheet(machineID: machine, agent: agent)
        case .search: SearchSheet()
        case .wake(let client, let agent, let wake, let named): WakeSheet(clientID: client, agent: agent, existing: wake, named: named)
        }
    }
}

/// A sheet's close button.
struct CloseButton: ToolbarContent {
    @Environment(\.dismiss) private var dismiss
    var body: some ToolbarContent {
        ToolbarItem(placement: .cancellationAction) {
            Button("Close", systemImage: "xmark") { dismiss() }.accessibilityIdentifier("close")
        }
    }
}

// MARK: Session info

/// Who started the session, where it runs, and what it changed, file by file.
struct SessionInfoSheet: View {
    @Environment(AppModel.self) private var model
    let id: String
    @State private var files: [DiffFile]?
    @State private var failed = false

    var body: some View {
        NavigationStack {
            List {
                if let s = model.session(id) {
                    Section {
                        LabeledContent("Started by", value: model.grantLabel(s))
                        LabeledContent("Machine / project", value: "\(model.machineName(s.machineID)) / \(s.projectID)")
                        LabeledContent("Coding agent", value: agentTitle(s.agent))
                        LabeledContent("Turns", value: "\(s.currentTurn)")
                        if !s.branch.isEmpty { LabeledContent("Branch", value: s.branch) }
                        LabeledContent("Started", value: s.createdDate.formatted(date: .abbreviated, time: .shortened))
                    }
                    Section {
                        if let files {
                            if files.isEmpty { Text("No changes").foregroundStyle(.secondary) }
                            ForEach(files) { f in
                                NavigationLink(value: f.path) {
                                    HStack {
                                        Text(f.path).font(.subheadline.monospaced()).lineLimit(1).truncationMode(.head)
                                        Spacer()
                                        Text("+\(f.insertions)").foregroundStyle(.green)
                                        Text("−\(f.deletions)").foregroundStyle(.red)
                                    }
                                    .font(.subheadline.monospacedDigit())
                                }
                            }
                        } else if failed {
                            Text("The changes can’t be fetched right now.").foregroundStyle(.secondary)
                        } else {
                            InlineLoader(text: "Reading the changes…")
                        }
                    } header: {
                        Text(files.map { $0.count == 1 ? String(localized: "Changes · 1 file") : String(localized: "Changes · \($0.count) files") } ?? String(localized: "Changes"))
                    } footer: {
                        Text("The changes are on the session’s own branch and worktree; your own files are left alone.")
                    }
                }
            }
            .navigationTitle("Session Info")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { CloseButton() }
            .navigationDestination(for: String.self) { path in
                if let f = files?.first(where: { $0.path == path }) { DiffView(file: f) }
            }
            .task {
                do {
                    files = parseUnifiedDiff(try await model.api?.diff(id) ?? "")
                } catch {
                    failed = true
                }
            }
        }
        .presentationDetents([.medium, .large])
    }
}

/// One file's changes, unified: the phone is too narrow for side by side.
struct DiffView: View {
    let file: DiffFile

    var body: some View {
        ScrollView([.vertical, .horizontal]) {
            LazyVStack(alignment: .leading, spacing: 0) {
                if file.binary { Text("A binary file").foregroundStyle(.secondary).padding() }
                ForEach(Array(file.hunks.enumerated()), id: \.offset) { _, h in
                    Text(h.header).foregroundStyle(.secondary).padding(.horizontal, 12).padding(.vertical, 4)
                    ForEach(Array(h.lines.enumerated()), id: \.offset) { _, l in
                        HStack(spacing: 8) {
                            Text(l.new.map(String.init) ?? l.old.map(String.init) ?? "").foregroundStyle(.tertiary).frame(width: 34, alignment: .trailing)
                            Text(prefix(l) + l.text).foregroundStyle(color(l))
                        }
                        .padding(.horizontal, 8).padding(.vertical, 1)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(background(l))
                    }
                }
            }
            .font(.system(size: 12.5, design: .monospaced))
            .textSelection(.enabled)
        }
        .navigationTitle(URL(fileURLWithPath: file.path).lastPathComponent)
        .navigationBarTitleDisplayMode(.inline)
    }

    private func prefix(_ l: DiffLine) -> String {
        switch l.kind {
        case .added: "+"
        case .removed: "-"
        case .context: " "
        case .note: ""
        }
    }

    private func color(_ l: DiffLine) -> Color {
        switch l.kind {
        case .added: .green
        case .removed: .red
        case .note: .secondary
        case .context: .primary
        }
    }

    private func background(_ l: DiffLine) -> Color {
        switch l.kind {
        case .added: .green.opacity(0.1)
        case .removed: .red.opacity(0.08)
        default: .clear
        }
    }
}

// MARK: Scope

/// The coding agents a grant can name; matches machine.AgentNames in hermeshd.
let codingAgentIDs = ["claude", "codex", "gemini", "cursor", "copilot", "opencode", "qwen", "goose", "auggie", "kimi", "kiro", "qoder", "deepseek", "codebuddy", "grok"]

/// A personal agent's scope as the owner chooses it: projects (or all),
/// coding agents and lifetime.
struct ScopeChoice: Equatable {
    var allProjects = false
    /// "machineID/projectID".
    var projects: Set<String> = []
    var agents: Set<String> = ["claude", "codex"]
    var ttlDays = 30

    static let ttlChoices = [7, 30, 90]

    var complete: Bool { (allProjects || !projects.isEmpty) && !agents.isEmpty }

    var spec: GrantSpec {
        var s = GrantSpec()
        s.agents = agents.sorted()
        s.ttlDays = UInt32(ttlDays)
        if !allProjects {
            let pairs = projects.map { $0.split(separator: "/", maxSplits: 1).map(String.init) }
            s.machineIds = Array(Set(pairs.map { $0[0] })).sorted()
            s.projectIds = Array(Set(pairs.map { $0[1] })).sorted()
        }
        return s
    }

    /// A grant's scope; "*" (or nothing) means all.
    init() {}
    init(client: Client, projects all: [Project]) {
        let machines = client.spec.machineIds.filter { $0 != "*" }
        let ids = client.spec.projectIds.filter { $0 != "*" }
        let named = client.spec.agents.filter { $0 != "*" }
        allProjects = machines.isEmpty && ids.isEmpty
        projects = Set(all.filter { (machines.isEmpty || machines.contains($0.machineID)) && (ids.isEmpty || ids.contains($0.projectID)) }.map { "\($0.machineID)/\($0.projectID)" })
        agents = Set(named.isEmpty ? codingAgentIDs : named)
    }
}

/// The scope rows: projects, coding agents and lifetime.
struct ScopeRows: View {
    @Environment(AppModel.self) private var model
    @Binding var scope: ScopeChoice
    let projects: [Project]?
    var showsTTL = true

    var body: some View {
        Section {
            NavigationLink {
                ProjectPicker(scope: $scope, projects: projects ?? [])
            } label: {
                LabeledContent("Projects", value: projectsText)
            }
            .accessibilityIdentifier("scope-projects")
            NavigationLink {
                AgentPicker(scope: $scope)
            } label: {
                LabeledContent("Coding agents", value: listText(codingAgentIDs.filter(scope.agents.contains).map(agentTitle)))
            }
            if showsTTL {
                Picker("Valid for", selection: $scope.ttlDays) {
                    ForEach(ScopeChoice.ttlChoices, id: \.self) { d in Text("\(d) days").tag(d) }
                }
            }
        } footer: {
            if !scope.complete { Text("Choose at least one project, or All Projects, and one coding agent.") }
        }
    }

    private var projectsText: String {
        if scope.allProjects { return String(localized: "All projects") }
        if scope.projects.isEmpty { return String(localized: "None yet") }
        return listText(scope.projects.sorted().map { $0.split(separator: "/").last.map(String.init) ?? $0 })
    }
}

struct ProjectPicker: View {
    @Environment(AppModel.self) private var model
    @Binding var scope: ScopeChoice
    let projects: [Project]

    var body: some View {
        List {
            Section {
                Toggle("All projects (including ones added later)", isOn: $scope.allProjects)
            }
            if !scope.allProjects {
                ForEach(Dictionary(grouping: projects, by: \.machineID).sorted(by: { $0.key < $1.key }), id: \.key) { machine, ps in
                    Section(model.machineName(machine)) {
                        ForEach(ps, id: \.projectID) { p in
                            let key = "\(p.machineID)/\(p.projectID)"
                            Button {
                                if scope.projects.contains(key) { scope.projects.remove(key) } else { scope.projects.insert(key) }
                            } label: {
                                HStack {
                                    VStack(alignment: .leading) {
                                        Text(p.projectID)
                                        Text(p.path).font(.caption.monospaced()).foregroundStyle(.secondary)
                                    }
                                    Spacer()
                                    if scope.projects.contains(key) { Image(systemName: "checkmark").foregroundStyle(Color.accentColor) }
                                }
                            }
                            .foregroundStyle(.primary)
                        }
                    }
                }
                if projects.isEmpty { Text("No projects yet. Add one on a machine’s page.").foregroundStyle(.secondary) }
            }
        }
        .navigationTitle("Projects")
    }
}

struct AgentPicker: View {
    @Binding var scope: ScopeChoice

    var body: some View {
        List(codingAgentIDs, id: \.self) { a in
            Button {
                if scope.agents.contains(a) { scope.agents.remove(a) } else { scope.agents.insert(a) }
            } label: {
                HStack(spacing: 12) {
                    AgentLogo(id: a, size: 24)
                    Text(agentTitle(a))
                    Spacer()
                    if scope.agents.contains(a) { Image(systemName: "checkmark").foregroundStyle(Color.accentColor) }
                }
            }
            .foregroundStyle(.primary)
        }
        .navigationTitle("Coding Agents")
    }
}

// MARK: Pairing

/// A request to connect: who asks, the verification code to compare, and
/// (for an agent) what it may use. Approving signs with Face ID at once:
/// this sheet is the confirmation.
struct PairingSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let code: String
    @State private var scope = ScopeChoice()
    @State private var machineName = ""
    @State private var busy = false
    @State private var done: String?
    /// The machine just approved: its id, name and the digits to type on it.
    @State private var joining: (id: String, name: String, digits: String)?
    /// The agent just approved: its client id and name, for the first task.
    @State private var connected: (id: String, name: String)?

    var body: some View {
        NavigationStack {
            Group {
                if let joining {
                    MachineJoining(machineID: joining.id, name: joining.name, digits: joining.digits) { dismiss() }
                } else if let connected {
                    FirstTask(clientID: connected.id, name: connected.name) { dismiss() }
                } else if let done {
                    VStack(spacing: 14) {
                        Image(systemName: "checkmark.circle.fill").font(.system(size: 56)).foregroundStyle(.green)
                        Text(done).font(.title3.bold()).multilineTextAlignment(.center)
                        Button("Done") { dismiss() }.buttonStyle(.glassProminent).controlSize(.large).padding(.top, 8)
                    }
                    .padding(32).frame(maxHeight: .infinity)
                } else if let p = pairing {
                    form(p)
                } else {
                    ContentUnavailableView("This request is gone", systemImage: "checkmark.circle", description: Text("It may have been approved elsewhere, or it expired."))
                }
            }
            .toolbar { CloseButton() }
        }
        .presentationDetents([.large])
        .task {
            if let p = pairing { machineName = p.label }
        }
    }

    private var pairing: PairingDetails? { model.pairings.first { $0.code == code } }

    private func form(_ p: PairingDetails) -> some View {
        List {
            Section {
                VStack(spacing: 10) {
                    AgentLogo(id: p.label, size: 58)
                    Text(PairingText.title(p)).font(.title2.bold()).multilineTextAlignment(.center)
                    Text(selfReport(p)).font(.subheadline).foregroundStyle(.secondary).multilineTextAlignment(.center)
                    if !p.confirmed {
                        CodeTiles(code: p.verificationCode, caption: "Does it match what the other side shows?").padding(.top, 8)
                    }
                }
                .frame(maxWidth: .infinity)
                .listRowBackground(Color.clear)
            }
            if p.confirmed && p.kind == .machine {
                Section {
                    Label {
                        VStack(alignment: .leading, spacing: 2) {
                            Text("The pairing code matches this machine’s key")
                            Text(p.code).font(.footnote.monospaced()).foregroundStyle(.secondary)
                        }
                    } icon: {
                        Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
                    }
                    .accessibilityIdentifier("code-matches")
                }
            }
            switch p.kind {
            case .client:
                ScopeRows(scope: $scope, projects: model.projects)
                Section {
                } footer: {
                    VStack(alignment: .leading, spacing: 6) {
                        Text("In these projects it can do anything you can. You can change or disconnect it at any time.")
                        if !p.custodian.isEmpty {
                            let host = URL(string: p.custodian)?.host ?? p.custodian
                            Text("“\(p.label)” connects through \(host): \(host) keeps this agent’s key, can act for it within this scope, and sees the sessions it reaches.")
                        }
                    }
                }
            case .machine:
                Section {
                    TextField("Machine name", text: $machineName).textInputAutocapitalization(.never).autocorrectionDisabled()
                        .accessibilityIdentifier("machine-name")
                } header: {
                    Text("Name this machine")
                } footer: {
                    Text("Agents find projects as “machine/project”. Once approved, add projects to it on its page and choose which grants may use them.")
                }
            default:
                Section {
                } footer: {
                    Text("It will approve machines, personal agents and new devices, just like this iPhone. Approve only if that device is in your hands; if it gets lost, remove it from any other device.")
                }
            }
            Section {
                HStack(spacing: 10) {
                    Button {
                        model.dismissedPairings.insert(p.code)
                        model.show(p.confirmed ? String(localized: "Put off.") : String(localized: "Put off. Don’t approve unless the codes match."))
                        dismiss()
                    } label: {
                        Text("Deny").frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.glass)
                    Button {
                        approve(p)
                    } label: {
                        Label("Approve", systemImage: "faceid").frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.glassProminent)
                    .disabled(busy || (p.kind == .client && !scope.complete) || (p.kind == .machine && machineName.isEmpty))
                    .accessibilityIdentifier("approve")
                }
                .controlSize(.large)
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets())
            }
        }
    }

    private func selfReport(_ p: PairingDetails) -> String {
        let who = [p.hostname, p.platform].filter { !$0.isEmpty }.joined(separator: " · ")
        if who.isEmpty { return p.confirmed ? "" : String(localized: "Compare the code with what it shows.") }
        return String(localized: "It says it is: \(who)")
    }

    private func approve(_ p: PairingDetails) {
        busy = true
        Task {
            defer { busy = false }
            switch p.kind {
            case .client:
                var spec = scope.spec
                spec.label = p.label
                if await model.approveClient(p, spec: spec) { connected = (p.subjectID, p.label) }
            case .machine:
                if let res = await model.approveMachine(p, name: machineName) {
                    joining = (res.machineID.isEmpty ? p.subjectID : res.machineID, machineName, res.matchCode)
                }
            default:
                if await model.approveDevice(p) != nil { done = String(localized: "“\(p.label)” is a trusted device now") }
            }
        }
    }
}

/// After a machine is approved: the two digits to type on it, when it asked
/// for them, until it connects; then that it joined, and its page.
struct MachineJoining: View {
    @Environment(AppModel.self) private var model
    let machineID: String
    let name: String
    let digits: String
    let close: () -> Void
    @State private var long = false

    private var joined: Bool { model.machines.contains { $0.machineID == machineID && $0.online } }

    var body: some View {
        VStack(spacing: 16) {
            Spacer()
            if joined {
                Image(systemName: "checkmark.circle.fill").font(.system(size: 60)).foregroundStyle(.green)
                Text("“\(name)” joined").font(.title2.bold()).multilineTextAlignment(.center)
                    .accessibilityIdentifier("machine-joined")
                Text("Add projects to it on its page.").foregroundStyle(.secondary)
                Spacer()
                Button {
                    close()
                    model.path.append(.machine(machineID))
                } label: {
                    Text("Open \(name)").frame(maxWidth: .infinity)
                }
                .buttonStyle(.glassProminent).controlSize(.large)
                .accessibilityIdentifier("open-machine")
                Button("Done") { close() }
            } else {
                if digits.isEmpty {
                    WaitingMark(size: 64)
                } else {
                    Text("Type these digits on “\(name)”").font(.title2.bold()).multilineTextAlignment(.center)
                    HStack(spacing: 14) {
                        ForEach(Array(digits), id: \.self) { d in
                            Text(String(d)).font(.system(size: 48, weight: .semibold, design: .monospaced))
                                .frame(width: 76, height: 94).background(.fill.tertiary, in: RoundedRectangle(cornerRadius: 22, style: .continuous))
                        }
                    }
                    .accessibilityElement(children: .combine)
                    .accessibilityIdentifier("match-digits")
                    Text("They prove the approval came from your own device.").font(.subheadline).foregroundStyle(.secondary)
                }
                HStack(spacing: 8) {
                    WaitingMark(size: 20)
                    Text("Waiting for “\(name)” to connect…").foregroundStyle(.secondary)
                }
                .padding(.top, 8)
                Spacer()
                if long {
                    Button("It didn’t join? Remove it", role: .destructive) {
                        Task {
                            if let m = model.machines.first(where: { $0.machineID == machineID }) { await model.remove(m) }
                            close()
                        }
                    }
                }
                Button("Done") { close() }
            }
        }
        .padding(28)
        .task {
            try? await Task.sleep(for: .seconds(120))
            long = true
        }
    }
}

/// What to send an agent just connected: one short task that shows the whole
/// way works.
func firstTaskPrompt(_ model: AppModel) -> String {
    let agent = model.machines.flatMap { $0.status.agents.filter { $0.available && $0.loggedIn }.map(\.name) }.first.map(agentTitle) ?? String(localized: "a coding agent")
    if let project = model.projects.first?.projectID {
        return String(localized: "Use Hermesh to have \(agent) say hello to me in the \(project) project.")
    }
    return String(localized: "Use Hermesh to have \(agent) say hello to me.")
}

/// After an agent is approved: one thing to send it that shows the whole way
/// works (as the Mac's first task), then its first session as soon as it
/// starts.
struct FirstTask: View {
    @Environment(AppModel.self) private var model
    let clientID: String
    let name: String
    let close: () -> Void

    private var prompt: String { firstTaskPrompt(model) }

    private var first: Session? {
        model.sessions.filter { $0.createdBy == clientID }.min { $0.createdAt.date < $1.createdAt.date }
    }

    var body: some View {
        List {
            Section {
                VStack(spacing: 10) {
                    Image(systemName: "checkmark.circle.fill").font(.system(size: 56)).foregroundStyle(.green)
                    Text("“\(name)” is connected").font(.title2.bold()).multilineTextAlignment(.center)
                        .accessibilityIdentifier("client-connected")
                    Text("Try it in \(name): send it this:").foregroundStyle(.secondary).multilineTextAlignment(.center)
                }
                .frame(maxWidth: .infinity)
                .listRowBackground(Color.clear)
            }
            Section {
                MonoBox(text: prompt)
                CopyShareButtons(text: prompt, shareTitle: "Send…")
                    .listRowBackground(Color.clear).listRowInsets(EdgeInsets())
            } footer: {
                HStack(spacing: 8) {
                    WaitingMark(size: 18)
                    Text("As soon as it starts working, its session opens here.")
                }
                .padding(.top, 8)
            }
            Section {
                Button("Done", action: close).frame(maxWidth: .infinity)
                    .listRowBackground(Color.clear)
            }
        }
        .onChange(of: first?.sessionID) { _, id in
            if let id {
                close()
                model.openSession(id)
            }
        }
    }
}

// MARK: Grants

/// Renews a grant: the same scope for longer, signed again.
struct RenewSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let grantID: String
    @State private var days = 30
    @State private var busy = false

    var body: some View {
        NavigationStack {
            if let c = model.grant(id: grantID) {
                List {
                    Section {
                        Picker("Valid for", selection: $days) {
                            ForEach(ScopeChoice.ttlChoices, id: \.self) { d in Text("\(d) days").tag(d) }
                        }
                        .pickerStyle(.segmented)
                    } footer: {
                        Text("Its projects and coding agents stay the same. Once you confirm with Face ID, “\(c.label)” works with the new expiry and the old grant stops working.")
                    }
                }
                .navigationTitle("Renew “\(c.label)”")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    CloseButton()
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Renew") {
                            busy = true
                            var spec = c.spec
                            spec.ttlDays = UInt32(days)
                            Task {
                                if await model.update(c, spec: spec, done: String(localized: "Renewed “\(c.label)” for \(days) days")) { dismiss() }
                                busy = false
                            }
                        }
                        .disabled(busy)
                        .accessibilityIdentifier("renew-confirm")
                    }
                }
            }
        }
        .presentationDetents([.medium])
    }
}

/// Changes what a grant may use.
struct ScopeSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let grantID: String
    @State private var scope = ScopeChoice()
    @State private var busy = false

    var body: some View {
        NavigationStack {
            if let c = model.grant(id: grantID) {
                List {
                    ScopeRows(scope: $scope, projects: model.projects, showsTTL: false)
                    Section {
                    } footer: {
                        Text("Once you confirm with Face ID, “\(c.label)” works with the new settings and the old ones stop working at once.")
                    }
                }
                .navigationTitle("What “\(c.label)” Can Use")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    CloseButton()
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Save") {
                            busy = true
                            var spec = scope.spec
                            spec.label = c.label
                            spec.ttlDays = UInt32(max(1, c.daysLeft(now: Date())))
                            spec.taskVisibility = c.spec.taskVisibility
                            spec.maxConcurrentSessions = c.spec.maxConcurrentSessions
                            Task {
                                if await model.update(c, spec: spec, done: String(localized: "“\(c.label)” works with the new settings")) { dismiss() }
                                busy = false
                            }
                        }
                        .disabled(busy || !scope.complete)
                    }
                }
                .task(id: model.projects.isEmpty) { // again once the projects came (the first Watch after a launch)
                    scope = ScopeChoice(client: c, projects: model.projects)
                }
            }
        }
    }
}
