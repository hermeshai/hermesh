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

/// Connect a personal agent, as a card like the request it leads to: which
/// agent, then the connector URL for agents that take one or a message to
/// send it, to copy or send. Its request then shows up here to approve.
struct ConnectAgentSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var kind = PersonalAgentKind.all[0]
    @State private var byConnector = true

    var body: some View {
        let connector = mcpConnectorAddress(model.status)
        let canConnector = kind.mcp && !connector.isEmpty
        let useConnector = canConnector && byConnector
        let text = useConnector ? connector : connectMessage(model.status, agent: kind.name)
        VStack(spacing: 0) {
            Text("Connect Your Agent").font(.title2.bold())
            HStack(spacing: 0) {
                ForEach(PersonalAgentKind.all) { k in
                    Button {
                        kind = k
                    } label: {
                        VStack(spacing: 6) {
                            AgentLogo(id: k.logo ?? k.name, size: 48)
                                .padding(4)
                                .overlay(RoundedRectangle(cornerRadius: 48 * 0.26 + 4, style: .continuous)
                                    .strokeBorder(kind == k ? Color.accentColor : .clear, lineWidth: 2))
                            Text(k.name).font(.caption).fontWeight(kind == k ? .semibold : .regular)
                                .foregroundStyle(kind == k ? .primary : .secondary).lineLimit(1)
                        }
                        .frame(maxWidth: .infinity).contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(k.name)
                    .accessibilityAddTraits(kind == k ? .isSelected : [])
                    .accessibilityIdentifier("agent-\(k.id)")
                }
            }
            .padding(.top, 22)
            if canConnector {
                Picker("Way", selection: $byConnector) {
                    Text("Connector").tag(true)
                    Text("Message").tag(false)
                }
                .pickerStyle(.segmented).labelsHidden()
                .padding(.top, 22)
            }
            Text(useConnector ? "Add this connector in \(kind.name)" : "Send this to \(kind.name)")
                .font(.subheadline).foregroundStyle(.secondary).padding(.top, 22)
            // An own server's message is its steps, long: the box scrolls past a few lines.
            ScrollView {
                Text(text).font(useConnector ? .footnote.monospaced() : .callout).textSelection(.enabled)
                    .padding(14).frame(maxWidth: .infinity, alignment: .leading)
            }
            .scrollBounceBehavior(.basedOnSize)
            .frame(maxHeight: 220)
            .background(.fill.tertiary, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
                .padding(.top, 10)
                .accessibilityIdentifier("connect-text")
            HStack(spacing: 8) {
                WaitingMark(size: 16)
                Text(useConnector ? "Once connected, its request shows up here." : "Once it runs it, its request shows up here.")
            }
            .font(.footnote).foregroundStyle(.secondary)
            .padding(.top, 14)
            CardButton(title: "Copy", systemImage: "doc.on.doc") {
                UIPasteboard.general.string = text
                model.show(String(localized: "Copied"))
            }
            .padding(.top, 22)
            ShareLink(item: text) { Text("Send…") }.padding(.top, 14)
        }
        .padding(.horizontal, 26).padding(.top, 44).padding(.bottom, 6)
        .overlay(alignment: .topTrailing) { CardCloseButton { dismiss() }.padding(16) }
        .fittedSheet()
    }
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

/// Add a machine: its system, then how: the app (a Mac or a Windows PC,
/// which then signs in to this account or joins this server) or the command
/// to run there (Linux has only this; send it to the Mac by AirDrop),
/// install.sh's or install.ps1's in PowerShell. Then approve its request here.
struct AddMachineSheet: View {
    enum System: Hashable { case mac, windows, linux }

    @Environment(AppModel.self) private var model
    @State private var forThisPhone = false
    @State private var system = System.mac
    @State private var app = true

    static let install = "curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh"
    static let installScript = "https://github.com/hermeshai/hermesh/releases/latest/download/install.ps1"
    /// Where that Mac or PC gets the app: the site picks the build for it.
    static let download = "hermesh.io/download"
    private var windows: Bool { system == .windows }
    private var viaApp: Bool { app && system != .linux }
    private var install: String { windows ? "irm \(Self.installScript) | iex" : Self.install }
    /// The command with this account's address in it: the machine's request
    /// comes here by itself, to compare its verification code.
    private var pinned: String {
        windows ? "& ([scriptblock]::Create((irm \(Self.installScript)))) \(model.status.connectAddress)" : Self.install + " -s -- \(model.status.connectAddress)"
    }

    var body: some View {
        NavigationStack {
            List {
                Section {
                    VStack(spacing: 10) {
                        Picker("That machine’s system", selection: $system) {
                            Text("Mac").tag(System.mac)
                            Text("Windows").tag(System.windows)
                            Text("Linux").tag(System.linux)
                        }
                        Picker("How to install", selection: Binding(get: { viaApp }, set: { app = $0 })) {
                            Text("Install the App").tag(true)
                            Text("Terminal Command").tag(false)
                        }
                        .disabled(system == .linux)
                    }
                    .pickerStyle(.segmented)
                    .listRowBackground(Color.clear).listRowInsets(EdgeInsets())
                } footer: {
                    Text("Agents use Claude Code and Codex on it.")
                }
                if viaApp {
                    Section {
                        MonoBox(text: Self.download)
                        CopyShareButtons(text: "https://\(Self.download)", shareTitle: "Send…")
                            .listRowBackground(Color.clear).listRowInsets(EdgeInsets())
                    } header: {
                        windows ? Text("1. On that PC, open") : Text("1. On that Mac, open")
                    }
                    if model.status.hosted {
                        Section {
                            waiting
                            Button("Enter a pairing code instead…") { model.sheet = .enterCode }
                        } header: {
                            Text("2. Install it, open it, and sign in with the same account")
                        }
                    } else {
                        Section {
                            MonoBox(text: model.status.connectAddress)
                            CopyShareButtons(text: model.status.connectAddress, shareTitle: "Send…")
                                .listRowBackground(Color.clear).listRowInsets(EdgeInsets())
                        } header: {
                            Text("2. Install it, open it, and join")
                        }
                        Section {
                            waiting
                            Button("Enter a pairing code instead…") { model.sheet = .enterCode }
                        }
                    }
                } else if model.status.hosted {
                    commandSection(install, footer: windows ? "In PowerShell, as the user you code as. It shows a code." : "As the user you code as. It shows a code.")
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
                    commandSection(pinned, footer: windows
                        ? "In PowerShell, as the user you code as on that PC (Windows 10 or later). It installs Hermesh, runs it in the background, and shows a verification code."
                        : "As the user you code as on that machine. It installs Hermesh, runs it in the background, and shows a verification code.")
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
        // The app joins as a machine only, or as a trusted device (which is a machine too).
        if let p = model.pairings.first(where: { $0.kind == .machine || $0.kind == .device }) {
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
    /// The folder chosen, as the person writes it ("~/code/app").
    @State private var path = ""
    /// What its machine said of it: nil while asked, or from a machine from
    /// before ListFolders.
    @State private var folder: Hermesh_Machine_V1_ListFoldersResponse?
    @State private var inPlace = false
    /// Where new sessions' branches start; empty: the branch checked out then.
    @State private var baseRef = ""
    @State private var busy = false
    @State private var repos: [Hermesh_Machine_V1_Repo]?
    @State private var reposError: String?
    /// The folders browsed over this form, one screen each.
    @State private var browsing: [String] = []

    private var machine: Machine? { model.machines.first { $0.machineID == machineID } }
    private var onMac: Bool { machine?.status.platform.hasPrefix("darwin") ?? false }
    /// The project the chosen folder already is.
    private var existing: Project? {
        guard let full = folder?.fullPath, !full.isEmpty else { return nil }
        return model.projects.first { $0.machineID == machineID && $0.path == full }
    }
    private var alreadyForAgent: Bool { existing.map { $0.agents.isEmpty || $0.agents.contains(agent) } ?? false }
    /// What agents call the project: the folder's name, made to fit a
    /// project's, numbered when another project of this machine has it.
    private var name: String {
        if let existing { return existing.projectID }
        let names = Set(model.projects.filter { $0.machineID == machineID }.map(\.projectID))
        let base = projectID(folderName(path))
        var n = base, i = 2
        while names.contains(n) {
            n = "\(base)-\(i)"
            i += 1
        }
        return n
    }
    /// Whether the folder is a git repository; nil when its machine couldn't say.
    private var isRepo: Bool? { folder.map(\.repo) ?? (repos?.contains { $0.path == path } == true ? true : nil) }
    private var canAdd: Bool {
        !path.isEmpty && !alreadyForAgent && (machine?.online ?? false)
    }

    var body: some View {
        NavigationStack(path: $browsing) {
            List {
                if !(machine?.online ?? false) {
                    Section {
                        Label("Choose a Folder", systemImage: "folder").foregroundStyle(.secondary)
                    } footer: {
                        Text("\(model.machineName(machineID)) is offline. Once it’s back, you can browse its folders and add a project.")
                    }
                } else if path.isEmpty {
                    recent
                } else {
                    chosen
                    if isRepo != false && existing == nil { workspace }
                }
            }
            .navigationTitle("Add a Project")
            .navigationSubtitle("\(model.machineName(machineID)) · \(agentTitle(agent))")
            .navigationBarTitleDisplayMode(.inline)
            .navigationDestination(for: String.self) { p in
                FolderBrowser(machineID: machineID, path: p, browsing: $browsing) { choose($0, listing: $1) }
            }
            .task {
                do {
                    repos = try await model.api?.repos(machine: machineID)
                } catch {
                    reposError = error.localizedDescription
                }
            }
            .toolbar {
                CloseButton()
                ToolbarItem(placement: .confirmationAction) {
                    if busy {
                        ProgressView()
                    } else {
                        Button("Add", systemImage: "checkmark", role: .confirm) { add() }.disabled(!canAdd)
                            .accessibilityIdentifier("add-project-confirm")
                    }
                }
            }
        }
    }

    /// Before a folder is chosen: the machine's newest repositories, one tap
    /// each, and its folders to browse.
    private var recent: some View {
        Section {
            if let repos {
                ForEach(repos.prefix(5), id: \.path) { r in
                    Button { choose(r.path) } label: {
                        FolderLabel(name: folderName(r.path), detail: ([r.path, r.branch] + (r.hasLastCommit ? [agoText(r.lastCommit.date, now: model.now)] : [])).filter { !$0.isEmpty }.joined(separator: " · "), repo: true)
                    }
                    .accessibilityIdentifier("repo-\(r.path)")
                }
            } else if reposError == nil {
                HStack(spacing: 10) {
                    ProgressView()
                    Text("Looking for repositories…").foregroundStyle(.secondary)
                }
            }
            NavigationLink(value: "~") {
                Label("Browse Other Folders…", systemImage: "folder")
            }
            .accessibilityIdentifier("browse-folders")
        } header: {
            if repos?.isEmpty == false { Text("Recent Repositories") }
        } footer: {
            VStack(alignment: .leading, spacing: 6) {
                if let reposError { Text("Couldn’t list its repositories: \(reposError)") }
                Text("A project is a directory agents can work in. Then choose in each grant which projects it can use.")
                if onMac && repos != nil { Text("Repositories in folders macOS protects, such as Documents, aren’t listed: browse to them.") }
            }
        }
    }

    private var chosen: some View {
        Section {
            NavigationLink(value: parentFolder(path)) {
                FolderLabel(name: folderName(path), detail: path, repo: isRepo == true, large: true)
            }
            .accessibilityIdentifier("chosen-folder")
        } header: {
            Text("Folder")
        } footer: {
            if let existing {
                if alreadyForAgent {
                    Text("“\(existing.projectID)” is already a project for \(agentTitle(agent)).")
                } else {
                    Text("Already the project “\(existing.projectID)”, for \(existing.agents.map(agentTitle).formatted()). Adding it lets \(agentTitle(agent)) use it too, working as it does now.")
                }
            } else {
                VStack(alignment: .leading, spacing: 6) {
                    let base = projectID(folderName(path))
                    if name != base {
                        Text("Agents call it “\(name)”: another project here is called “\(base)”.")
                    } else if name != folderName(path) {
                        Text("Agents call it “\(name)”.")
                    }
                    if isRepo == false { Text("Not a git repository: agents change the files in this folder directly.") }
                }
            }
        }
    }

    private var workspace: some View {
        Section("How Agents Work") {
            choice(false, "Separate worktree", "Each session gets its own branch; the files you’re changing are left alone")
            choice(true, "In place", "Agents change this folder directly, as if you ran \(agentTitle(agent)) here")
            if !inPlace, let folder, folder.repo {
                Picker("New Branches From", selection: $baseRef) {
                    Text("Current branch (\(folder.branch))").tag("")
                    ForEach(folder.branches, id: \.self) { Text($0).tag($0) }
                }
                .pickerStyle(.menu)
                .accessibilityIdentifier("base-branch")
            }
        }
    }

    private func choice(_ value: Bool, _ title: LocalizedStringKey, _ detail: LocalizedStringKey) -> some View {
        Button { inPlace = value } label: {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text(title).foregroundStyle(.primary)
                    Text(detail).font(.footnote).foregroundStyle(.secondary)
                }
                Spacer()
                if inPlace == value { Image(systemName: "checkmark").fontWeight(.semibold).foregroundStyle(.tint) }
            }
            .contentShape(Rectangle())
        }
        .accessibilityAddTraits(inPlace == value ? .isSelected : [])
        .accessibilityIdentifier(value ? "workspace-in-place" : "workspace-worktree")
    }

    /// Takes a folder, with what its machine said of it, or asks.
    private func choose(_ p: String, listing: Hermesh_Machine_V1_ListFoldersResponse? = nil) {
        path = listing?.path ?? p
        folder = nil
        inPlace = false
        baseRef = ""
        browsing = []
        if let listing { return describe(listing) }
        Task {
            if let l = try? await model.api?.folders(machine: machineID, path: p), path == l.path { describe(l) }
        }
    }

    private func describe(_ listing: Hermesh_Machine_V1_ListFoldersResponse) {
        folder = listing
        inPlace = !listing.repo
    }

    private func projectID(_ folder: String) -> String {
        let s = folder.lowercased().map { c in c.isLetter && c.isASCII || c.isNumber && c.isASCII || "._-".contains(c) ? String(c) : "-" }.joined()
        let t = s.drop { "-._".contains($0) }
        return t.isEmpty ? "project" : String(t.prefix(60))
    }

    private func add() {
        let name = name
        busy = true
        Task {
            defer { busy = false }
            do {
                if let existing {
                    try await model.saveProject(existing, agents: existing.agents + [agent], workspace: existing.workspace)
                    model.show(String(localized: "“\(existing.projectID)” is now for \(agentTitle(agent)) too"))
                } else {
                    var r = Hermesh_Local_V1_AddProjectRequest()
                    r.machineID = machineID
                    r.projectID = name
                    r.path = folder?.fullPath.isEmpty == false ? folder!.fullPath : path
                    r.workspace = inPlace ? .inPlace : .worktree
                    r.defaultBaseRef = inPlace ? "" : baseRef
                    r.agents = [agent]
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
