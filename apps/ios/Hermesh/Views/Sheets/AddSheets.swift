import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI

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
                        MonoBox(text: "https://mcp.hermesh.io")
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

    private var message: String {
        String(localized: """
        Please connect yourself to my computer (Hermesh):
        1. Install: curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh -s -- --cli
        2. Sign in: ~/.local/bin/hermesh auth login \(model.status.connectAddress) --name "\(kind.name)"
        3. Tell me the verification code shown on screen. Once I confirm it on my iPhone, you’re ready.
        Then use hermesh project list to see the projects you can use, hermesh session start --project <project> --prompt "…" to start working, and hermesh --help for every command.
        """)
    }
}

/// Add a machine: the command to run there (send it to the Mac by AirDrop),
/// then approve its request here.
struct AddMachineSheet: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let command = "curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh -s -- \(model.status.connectAddress)"
        NavigationStack {
            List {
                Section {
                } footer: {
                    Text("A Mac mini, a Linux server or a cloud host can each be a machine: agents you grant access use Claude Code and Codex on it.")
                }
                Section {
                    MonoBox(text: command)
                    CopyShareButtons(text: command, shareTitle: "Send…")
                        .listRowBackground(Color.clear).listRowInsets(EdgeInsets())
                } header: {
                    Text("1. Run this in that machine’s terminal")
                } footer: {
                    Text("As the user you code as on that machine. It installs Hermesh, runs it in the background, and shows a verification code.")
                }
                Section {
                    if let p = model.pairings.first(where: { $0.kind == .machine }) {
                        Button(PairingText.title(p)) { model.sheet = .pairing(p.code) }
                    } else {
                        HStack(spacing: 10) {
                            ProgressView()
                            Text("Waiting for that machine to connect…").foregroundStyle(.secondary)
                        }
                    }
                    Button("No connection address? Enter a pairing code…") { model.sheet = .enterCode }
                } header: {
                    Text("2. Approve it here")
                }
            }
            .navigationTitle("Add a Machine")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { CloseButton() }
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
                        Text("When a machine or agent came without your connection address, its terminal shows a pairing code. Its request then opens like any other: compare the verification code.")
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
    @State private var path = ""
    @State private var name = ""
    @State private var inPlace = false
    @State private var baseRef = ""
    @State private var busy = false

    var body: some View {
        NavigationStack {
            List {
                Section {
                    LabeledContent("Machine", value: model.machineName(machineID))
                    TextField("Directory, e.g. ~/code/my-app", text: $path).font(.body.monospaced())
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                        .onChange(of: path) { _, p in
                            let last = URL(fileURLWithPath: (p as NSString).expandingTildeInPath).lastPathComponent
                            if name.isEmpty || name == projectID(String(last.dropLast())) { name = projectID(last) }
                        }
                    TextField("Project name", text: $name).textInputAutocapitalization(.never).autocorrectionDisabled()
                } footer: {
                    Text("A project is a directory agents can work in. Then choose in each grant which projects it can use.")
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
            .toolbar {
                CloseButton()
                ToolbarItem(placement: .confirmationAction) {
                    Button(busy ? "Adding…" : "Add") { add() }.disabled(path.isEmpty || name.isEmpty || busy)
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
        Task {
            defer { busy = false }
            do {
                try await model.api?.addProject(r)
                model.show(String(localized: "Added project “\(name)”"))
                dismiss()
            } catch {
                model.report(error)
            }
        }
    }
}
