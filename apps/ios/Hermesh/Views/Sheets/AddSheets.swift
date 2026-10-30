import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI
import VisionKit

/// Copy and Share for a piece of text, the phone's way of getting a
/// command or a message to where it is needed.
struct CopyShareButtons: View {
    @Environment(AppModel.self) private var model
    let text: String
    var shareTitle: LocalizedStringKey = "Share…"

    var body: some View {
        HStack(spacing: 10) {
            Button {
                UIPasteboard.general.string = text
                model.show(String(localized: "Copied"))
            } label: {
                Label("Copy", systemImage: "doc.on.doc").frame(maxWidth: .infinity)
            }
            .buttonStyle(.glass)
            ShareLink(item: text) {
                Label(shareTitle, systemImage: "square.and.arrow.up").frame(maxWidth: .infinity)
            }
            .buttonStyle(.glassProminent)
        }
        .controlSize(.large)
    }
}

struct MonoBox: View {
    let text: String
    var body: some View {
        Text(text).font(.footnote.monospaced()).textSelection(.enabled)
            .padding(14).frame(maxWidth: .infinity, alignment: .leading)
            .background(.fill.tertiary, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
    }
}

/// The personal agents people connect, the popular ones first.
struct PersonalAgentKind: Identifiable, Hashable {
    let id: String
    let name: String
    let logo: String?
    /// It can connect by MCP (the connector URL) as well as by the CLI.
    let mcp: Bool

    static let all = [
        PersonalAgentKind(id: "claude", name: "Claude", logo: "claude", mcp: true),
        PersonalAgentKind(id: "chatgpt", name: "ChatGPT", logo: "codex", mcp: true),
        PersonalAgentKind(id: "grok", name: "Grok Bot", logo: "grokbot", mcp: false),
        PersonalAgentKind(id: "muse", name: "Muse", logo: "muse", mcp: false),
        PersonalAgentKind(id: "openclaw", name: "OpenClaw", logo: "openclaw", mcp: false),
    ]
}

/// Connect a personal agent: the connector URL for agents that take one,
/// or a message to send it so it installs Hermesh and asks to connect. Its
/// request then shows up here to approve.
struct ConnectAgentSheet: View {
    @Environment(AppModel.self) private var model
    @State private var kind = PersonalAgentKind.all[0]
    @State private var other = ""

    var body: some View {
        NavigationStack {
            List {
                Section("Your agent") {
                    ScrollView(.horizontal) {
                        HStack(spacing: 8) {
                            ForEach(PersonalAgentKind.all) { k in
                                Button {
                                    kind = k
                                } label: {
                                    HStack(spacing: 8) {
                                        if let l = k.logo { AgentLogo(id: l, size: 26) }
                                        Text(k.name)
                                    }
                                    .padding(.leading, 6).padding(.trailing, 14).frame(height: 40)
                                    .background(.fill.tertiary, in: .capsule)
                                    .overlay(Capsule().strokeBorder(kind == k ? Color.accentColor : .clear, lineWidth: 2))
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }
                    .scrollIndicators(.hidden)
                    .listRowInsets(EdgeInsets(top: 8, leading: 12, bottom: 8, trailing: 12))
                }
                if kind.mcp && model.status.hosted {
                    Section {
                        MonoBox(text: mcpConnectorURL)
                    } header: {
                        Text("Add a connector in \(kind.name)")
                    } footer: {
                        Text("\(kind.name) opens a sign-in and approval page; its request then shows up on this iPhone for you to verify and approve.")
                    }
                }
                Section {
                    MonoBox(text: message)
                    CopyShareButtons(text: message, shareTitle: "Send…")
                        .listRowBackground(Color.clear).listRowInsets(EdgeInsets())
                } header: {
                    Text(kind.mcp && model.status.hosted ? "Or send this to \(kind.name)" : "Send this to \(kind.name)")
                } footer: {
                    Text("Once it runs the commands, its request to connect shows up here.")
                }
            }
            .navigationTitle("Connect Your Agent")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { CloseButton() }
        }
    }

    private var message: String { connectMessage(model.status, agent: kind.name) }
}

/// What to send a personal agent so it connects. On the official service,
/// the line the Mac gives too: the agent's steps are on the console's
/// /connect page and it signs in by a link. On an own server, the steps
/// themselves, with the connection address.
func connectMessage(_ status: StatusResponse, agent: String) -> String {
    if status.hosted, let url = URL(string: status.consoleURL), let scheme = url.scheme, let host = url.host {
        let connect = "\(scheme)://\(host)\(url.port.map { ":\($0)" } ?? "")/connect"
        return String(localized: "Connect to Hermesh: follow \(connect). Your name in Hermesh is “\(agent)”.")
    }
    return String(localized: """
        Please connect yourself to my computer (Hermesh):
        1. Install: curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh -s -- --cli
        2. Sign in: ~/.local/bin/hermesh auth login \(status.connectAddress) --name "\(agent)"
        3. Tell me the verification code shown on screen. Once I confirm it on my iPhone, you’re ready.
        Then use hermesh project list to see the projects you can use, hermesh session start --project <project> --prompt "…" to start working, and hermesh --help for every command.
        """)
}

/// Add a machine: the command to run there (send it to the Mac by AirDrop),
/// then approve its request here.
struct AddMachineSheet: View {
    @Environment(AppModel.self) private var model
    @State private var forThisPhone = false

    static let install = "curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh"
    /// The command with this account's address in it: the machine's request
    /// comes here by itself, to compare its verification code.
    private var pinned: String { Self.install + " -s -- \(model.status.connectAddress)" }

    var body: some View {
        NavigationStack {
            List {
                Section {
                } footer: {
                    Text("A Linux server, a cloud host or a Mac mini can each be a machine.")
                }
                if model.status.hosted {
                    commandSection(Self.install, footer: "As the user you code as. It shows a code.")
                    Section {
                        Button {
                            model.sheet = .scan
                        } label: {
                            Label("Scan the Code", systemImage: "qrcode.viewfinder").frame(maxWidth: .infinity)
                        }
                        .buttonStyle(.glassProminent).controlSize(.large)
                        .listRowBackground(Color.clear).listRowInsets(EdgeInsets())
                        .accessibilityIdentifier("scan")
                        Button("Enter the pairing code instead…") { model.sheet = .enterCode }
                    } header: {
                        Text("2. Scan the code it shows")
                    }
                    Section {
                        if forThisPhone {
                            MonoBox(text: pinned)
                            CopyShareButtons(text: pinned, shareTitle: "Send…")
                                .listRowBackground(Color.clear).listRowInsets(EdgeInsets())
                        } else {
                            Button("Terminal on this iPhone? Copy a Command for This Account") { forThisPhone = true }
                                .font(.footnote)
                        }
                    } footer: {
                        if forThisPhone {
                            Text("With nothing to scan: its request comes here by itself. Compare the verification code.")
                        }
                    }
                } else {
                    commandSection(pinned, footer: "As the user you code as on that machine. It installs Hermesh, runs it in the background, and shows a verification code.")
                    Section {
                        waiting
                        Button("Enter a pairing code instead…") { model.sheet = .enterCode }
                    } header: {
                        Text("2. Approve it here")
                    }
                }
            }
            .navigationTitle("Add a Machine")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { CloseButton() }
        }
    }

    private func commandSection(_ command: String, footer: LocalizedStringKey) -> some View {
        Section {
            MonoBox(text: command)
            CopyShareButtons(text: command, shareTitle: "Send…")
                .listRowBackground(Color.clear).listRowInsets(EdgeInsets())
        } header: {
            Text("1. On that machine, run")
        } footer: {
            Text(footer)
        }
    }

    @ViewBuilder private var waiting: some View {
        if let p = model.pairings.first(where: { $0.kind == .machine }) {
            Button(PairingText.title(p)) { model.sheet = .pairing(p.code) }
        } else {
            HStack(spacing: 10) {
                WaitingMark(size: 22)
                Text("Waiting for that machine to connect…").foregroundStyle(.secondary)
            }
        }
    }
}

/// The camera, reading a Hermesh code: a machine's QR in its terminal, or an
/// agent's link.
struct ScanSheet: View {
    @Environment(AppModel.self) private var model
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Group {
                if DataScannerViewController.isSupported && DataScannerViewController.isAvailable {
                    CodeScanner { text in
                        guard let code = PairingLink.code(scanned: text) else {
                            error = String(localized: "That isn’t a Hermesh code.")
                            return false
                        }
                        Task { await model.openPairingCode(code) }
                        return true
                    }
                    .ignoresSafeArea(edges: .bottom)
                    .overlay(alignment: .bottom) {
                        Text(error ?? String(localized: "Point the camera at the code in the terminal."))
                            .font(.callout).padding(.horizontal, 16).padding(.vertical, 10)
                            .glassEffect().padding(.bottom, 40)
                    }
                } else {
                    ContentUnavailableView {
                        Label("No camera to scan with", systemImage: "camera")
                    } description: {
                        Text("Allow Hermesh to use the camera in Settings, or enter the pairing code.")
                    } actions: {
                        Button("Enter the Pairing Code…") { model.sheet = .enterCode }
                    }
                }
            }
            .navigationTitle("Scan the Code")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { CloseButton() }
        }
    }
}

/// VisionKit's data scanner, for QR codes; `found` says whether to stop.
private struct CodeScanner: UIViewControllerRepresentable {
    let found: (String) -> Bool

    func makeUIViewController(context: Context) -> DataScannerViewController {
        let v = DataScannerViewController(recognizedDataTypes: [.barcode(symbologies: [.qr])], qualityLevel: .balanced, isHighlightingEnabled: true)
        v.delegate = context.coordinator
        try? v.startScanning()
        return v
    }

    func updateUIViewController(_ v: DataScannerViewController, context: Context) {}

    func makeCoordinator() -> Coordinator { Coordinator(found: found) }

    final class Coordinator: NSObject, DataScannerViewControllerDelegate {
        let found: (String) -> Bool
        private var done = false
        init(found: @escaping (String) -> Bool) { self.found = found }

        func dataScanner(_ v: DataScannerViewController, didAdd items: [RecognizedItem], allItems: [RecognizedItem]) {
            for case .barcode(let b) in items {
                guard !done, let s = b.payloadStringValue else { continue }
                if found(s) {
                    done = true
                    v.stopScanning()
                }
            }
        }
    }
}

/// A pairing code from a terminal, for a machine or agent that came without
/// the connection address.
struct EnterCodeSheet: View {
    @Environment(AppModel.self) private var model
    @State private var code = ""
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            List {
                Section {
                    TextField("Pairing code", text: $code).font(.body.monospaced()).textInputAutocapitalization(.characters).autocorrectionDisabled()
                        .accessibilityIdentifier("pairing-code")
                } footer: {
                    if let error { Text(error).foregroundStyle(.red) } else {
                        Text("The code a machine or agent shows in its terminal. Its request then opens here, to approve.")
                    }
                }
            }
            .navigationTitle("Enter a Pairing Code")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                CloseButton()
                ToolbarItem(placement: .confirmationAction) {
                    Button(busy ? "Finding…" : "Find") { find() }.disabled(code.isEmpty || busy)
                }
            }
        }
        .presentationDetents([.medium])
    }

    private func find() {
        busy = true
        error = nil
        Task {
            defer { busy = false }
            do {
                guard let p = try await model.api?.lookupPairing(code.trimmingCharacters(in: .whitespaces)) else { return }
                if !model.pairings.contains(where: { $0.code == p.code }) { model.pairings.append(p) }
                model.sheet = .pairing(p.code)
            } catch {
                self.error = (error as? APIError)?.message ?? error.localizedDescription
            }
        }
    }
}

/// A project on a machine: a directory there, typed (the phone can't browse
/// that machine's files).
struct AddProjectSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let machineID: String
    /// The coding agent it is for; a directory already a project gets it too.
    let agent: String
    @State private var path = ""
    @State private var name = ""
    @State private var inPlace = false
    @State private var baseRef = ""
    @State private var busy = false
    @State private var taken: String?
    @State private var autoName = ""
    @State private var repos: [Hermesh_Machine_V1_Repo]?

    private var onMac: Bool { model.machines.first { $0.machineID == machineID }?.status.platform.hasPrefix("darwin") ?? false }

    var body: some View {
        NavigationStack {
            List {
                Section {
                    LabeledContent("Machine", value: model.machineName(machineID))
                    LabeledContent("Coding agent", value: agentTitle(agent))
                    TextField("Directory, e.g. ~/code/my-app", text: $path).font(.body.monospaced())
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                        .onChange(of: path) { _, p in
                            // The name follows the directory until edited.
                            let last = URL(fileURLWithPath: (p as NSString).expandingTildeInPath).lastPathComponent
                            if name.isEmpty || name == autoName {
                                name = projectID(last)
                                autoName = name
                            }
                        }
                    TextField("Project name", text: $name).textInputAutocapitalization(.never).autocorrectionDisabled()
                } footer: {
                    Text("A project is a directory agents can work in. Then choose in each grant which projects it can use.")
                }
                if let repos, !repos.isEmpty {
                    Section {
                        ForEach(repos.prefix(8), id: \.path) { r in
                            Button {
                                path = r.path
                                inPlace = false
                            } label: {
                                HStack {
                                    VStack(alignment: .leading, spacing: 2) {
                                        Text(URL(fileURLWithPath: r.path).lastPathComponent).foregroundStyle(.primary)
                                        Text(([r.path, r.branch] + (r.hasLastCommit ? [agoText(r.lastCommit.date, now: model.now)] : [])).filter { !$0.isEmpty }.joined(separator: " · "))
                                            .font(.footnote).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
                                    }
                                    Spacer()
                                    if path == r.path { Image(systemName: "checkmark").foregroundStyle(.tint) }
                                }
                            }
                            .accessibilityIdentifier("repo-\(r.path)")
                        }
                    } header: {
                        Text("On this machine")
                    } footer: {
                        if onMac { Text("Folders macOS protects, such as Desktop and Documents, aren’t searched: type those above.") }
                    }
                }
                Section {
                    Picker("How agents work", selection: $inPlace) {
                        Text("Separate worktree").tag(false)
                        Text("In place").tag(true)
                    }
                    .pickerStyle(.segmented)
                    if !inPlace { TextField("Base branch (optional), e.g. main", text: $baseRef).textInputAutocapitalization(.never).autocorrectionDisabled() }
                } footer: {
                    Text(inPlace ? "Agents change the files in this directory directly, as if you ran Claude Code there; no separate branch and no change history."
                        : "For a git repository: each session gets its own branch and worktree, and your own files are left alone.")
                }
            }
            .navigationTitle("Add a Project")
            .navigationBarTitleDisplayMode(.inline)
            .task { repos = try? await model.api?.repos(machine: machineID) }
            .alert("Something went wrong", isPresented: Binding(get: { taken != nil }, set: { if !$0 { taken = nil } })) {
                Button("OK", role: .cancel) {}
            } message: {
                Text(taken ?? "")
            }
            .toolbar {
                CloseButton()
                ToolbarItem(placement: .confirmationAction) {
                    Button(busy ? "Adding…" : "Add") { add() }.disabled(path.isEmpty || name.isEmpty || busy)
                        .accessibilityIdentifier("add-project-confirm")
                }
            }
        }
    }

    private func projectID(_ folder: String) -> String {
        let s = folder.lowercased().map { c in c.isLetter && c.isASCII || c.isNumber && c.isASCII || "._-".contains(c) ? String(c) : "-" }.joined()
        let t = s.drop { "-._".contains($0) }
        return t.isEmpty ? "project" : String(t.prefix(60))
    }

    private func add() {
        busy = true
        var r = Hermesh_Local_V1_AddProjectRequest()
        r.machineID = machineID
        r.projectID = name
        r.path = path
        r.workspace = inPlace ? .inPlace : .worktree
        r.defaultBaseRef = inPlace ? "" : baseRef
        r.agents = [agent]
        Task {
            defer { busy = false }
            do {
                let projects = try await model.api?.projects(machine: machineID).projects ?? []
                let trimmed = { (p: String) in p.count > 1 && p.hasSuffix("/") ? String(p.dropLast()) : p }
                if let same = projects.first(where: { trimmed($0.path) == trimmed(path) }) {
                    if !same.agents.isEmpty && !same.agents.contains(agent) {
                        try await model.saveProject(same, agents: same.agents + [agent], workspace: same.workspace)
                        model.show(String(localized: "“\(same.projectID)” is now for \(agentTitle(agent)) too"))
                    } else {
                        model.show(String(localized: "“\(same.projectID)” is already for \(agentTitle(agent))"))
                    }
                } else if let taken = projects.first(where: { $0.projectID == name }) {
                    // Adding it would replace that project (it may be this
                    // directory, written another way): a name of its own.
                    self.taken = String(localized: "A project named “\(name)” is already on this machine, at \(taken.path). Choose another name.")
                    return
                } else {
                    try await model.addProject(r)
                    model.show(String(localized: "Added project “\(name)”"))
                }
                dismiss()
            } catch {
                model.report(error)
            }
        }
    }
}

/// Another trusted device, so this iPhone is not the only one that can
/// approve: on it, Hermesh joins this account, and its request comes here.
struct AddDeviceSheet: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        NavigationStack {
            List {
                Section {
                } footer: {
                    Text("Another iPhone or a Mac can approve too. If you lose this iPhone, it keeps your machines and agents working.")
                }
                Section {
                    if model.status.hosted {
                        Text("Install Hermesh on it and sign in with the same account.")
                    } else {
                        Text("Install Hermesh on it, choose Use Your Own Server…, and type this connection address:")
                        MonoBox(text: model.status.connectAddress)
                        CopyShareButtons(text: model.status.connectAddress, shareTitle: "Send…")
                            .listRowBackground(Color.clear).listRowInsets(EdgeInsets())
                    }
                } header: {
                    Text("1. On the other device")
                }
                Section {
                    if let p = model.pairings.first(where: { $0.kind == .device }) {
                        Button(PairingText.title(p)) { model.sheet = .pairing(p.code) }
                    } else {
                        HStack(spacing: 10) {
                            WaitingMark(size: 22)
                            Text("Waiting for its request…").foregroundStyle(.secondary)
                        }
                    }
                } header: {
                    Text("2. Approve it here")
                } footer: {
                    Text("Compare the code it shows, then type the two digits this iPhone shows on it.")
                }
            }
            .navigationTitle("Add a Trusted Device")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { CloseButton() }
        }
    }
}
