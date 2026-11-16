import HermeshHer
import HermeshKit
import SwiftUI

// A connector's page (docs/her-connectors-v2-demo.html): what it is, what is wrong with it if anything, how Her is
// let in, its switch, its tools, its address, and disconnecting it.

struct HerConnectorPage: View {
    let name: String

    var body: some View {
        HerEnvironmentPage { chat, machine in HerConnectorDetail(chat: chat, machine: machine, name: name) }
            .navigationBarTitleDisplayMode(.inline)
    }
}

private struct HerConnectorDetail: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let machine: HerMachine
    let name: String
    @State private var state: HerConnectorsState
    @State private var removing = false

    init(chat: HerChat, machine: HerMachine, name: String) {
        self.chat = chat
        self.machine = machine
        self.name = name
        _state = State(initialValue: HerConnectorsState(chat: chat))
    }

    var body: some View {
        let offline = !chat.envUp
        Group {
            if let c = state.shown.first(where: { $0.name == name }) {
                page(c, offline: offline)
            } else if state.list != nil {
                ContentUnavailableView("This Connector Is Gone", systemImage: "powerplug", description: Text("It was removed, here or elsewhere."))
            } else if let failed = state.failed {
                ContentUnavailableView("Can’t Read the Connectors", systemImage: "exclamationmark.triangle", description: Text(failed))
            } else {
                ProgressView()
            }
        }
        .modifier(HerLoadsConnectors(state: state))
    }

    private func page(_ c: HerConnector, offline: Bool) -> some View {
        let look = state.look(c)
        let title = herConnectorTitle(c.name)
        return List {
            Section {
                VStack(spacing: 6) {
                    HerConnectorTile(name: c.name, icon: c.icon, command: c.command, size: 84).padding(.bottom, 6)
                    Text(title).font(.title.weight(.bold))
                    Text(herConnectorAbout(c, machine: machine)).font(.callout).foregroundStyle(.secondary).multilineTextAlignment(.center)
                }
                .frame(maxWidth: .infinity).listRowBackground(Color.clear).listRowInsets(EdgeInsets())
            }
            if offline { Section { HerOfflineNote(machine: machine) } }
            if c.enabled, look.needsOwner { Section { trouble(c, look: look, offline: offline) } }
            Section {
                let account = account(c)
                HStack(spacing: 14) {
                    Image(systemName: account.symbol).font(.title3).foregroundStyle(.secondary)
                        .frame(width: 44, height: 44).background(Palette.fill2, in: Circle())
                    VStack(alignment: .leading, spacing: 2) {
                        Text(account.title)
                        if let line = account.line { Text(line).font(.footnote).foregroundStyle(.secondary) }
                    }
                }
                .padding(.vertical, 2)
            } footer: {
                if let kept = kept(c) { Text(kept) }
            }
            Section {
                Toggle(c.command ? String(localized: "Run on \(machine.machineName)") : String(localized: "Let Her Use It"),
                       isOn: Binding(get: { c.enabled }, set: { on in Task { await state.setEnabled(c, on, model: model) } }))
                    .disabled(offline || !c.editable)
                    .accessibilityIdentifier("connector-switch")
            } footer: {
                Text("Off, it keeps its sign-in, and Her stops using it from her next step.")
            }
            Section {
                NavigationLink(value: Route.herConnectorTools(c.name)) {
                    LabeledContent("Tools") { if look == .checking { ProgressView() } else { Text("\(c.tools ?? 0)") } }
                }
                .disabled(!c.enabled)
                .accessibilityIdentifier("connector-tools")
            } footer: {
                Text("Her uses the tools \(title) marks as read-only without asking, and asks you before any other.")
            }
            Section {
                Text(c.summary).font(.footnote.monospaced()).textSelection(.enabled)
                LabeledContent("Last Checked") {
                    HStack(spacing: 10) {
                        if let at = c.checkedAt { Text(at, format: .relative(presentation: .named)) }
                        Button("Check Again") { Task { await state.check(c.name, model: model) } }
                            .buttonStyle(.borderless)
                            .disabled(offline || !c.enabled || state.checking.contains(c.name))
                            .accessibilityIdentifier("connector-check")
                    }
                }
            } header: {
                Text(c.command ? "Command" : "Address")
            }
            if c.editable {
                Section {
                    Button("Disconnect", role: .destructive) { removing = true }
                        .frame(maxWidth: .infinity)
                        .disabled(offline)
                        .accessibilityIdentifier("connector-remove")
                }
            }
        }
        .listSectionSpacing(.compact)
        .accessibilityIdentifier("connector-page")
        .confirmationDialog(String(localized: "Disconnect \(title)?"), isPresented: $removing, titleVisibility: .visible) {
            Button("Disconnect", role: .destructive) {
                Task {
                    if await state.remove(c, model: model) { model.path.removeAll { $0 == .herConnector(c.name) || $0 == .herConnectorTools(c.name) } }
                }
            }
            .accessibilityIdentifier("connector-remove-confirm")
        } message: {
            Text("Her stops using it from her next step, and what was saved for it on \(machine.machineName) is deleted.")
        }
    }

    /// What is wrong, and the one thing that fixes it.
    private func trouble(_ c: HerConnector, look: HerConnectorLook, offline: Bool) -> some View {
        let title = herConnectorTitle(c.name)
        return VStack(alignment: .leading, spacing: 6) {
            Text(look == .needsLogin ? String(localized: "Her can’t use \(title) until you sign in.") : String(localized: "Her can’t reach \(title) now."))
                .font(.headline)
            if case .failed(let why) = look, let why { Text(why).font(.footnote.monospaced()).foregroundStyle(.secondary).textSelection(.enabled) }
            Button(look == .needsLogin ? String(localized: "Sign In in the Browser") : String(localized: "Retry")) { Task { await state.fix(c, model: model) } }
                .buttonStyle(.borderedProminent).buttonBorderShape(.capsule).tint(look == .needsLogin ? .orange : .red)
                .disabled(offline).padding(.top, 4)
                .accessibilityIdentifier("connector-fix")
        }
        .padding(.vertical, 4)
    }

    /// How Her is let in: with the owner's account (by its name, when her knows it), a token, or nothing.
    private func account(_ c: HerConnector) -> (symbol: String, title: String, line: String?) {
        if c.auth == .oauth || c.curated != nil {
            let title = herConnectorTitle(c.name)
            if c.signedIn, let name = c.account { return ("person.fill", name, String(localized: "Signed in with your \(title) account")) }
            return ("person.fill", String(localized: "Your \(title) account"), c.signedIn ? String(localized: "Signed in") : String(localized: "Not signed in"))
        }
        if c.auth == .credential { return ("key.fill", String(localized: "Token"), String(localized: "Saved on \(machine.machineName)")) }
        if c.command { return ("terminal.fill", String(localized: "Runs as you"), machine.machineName) }
        return ("bolt.fill", String(localized: "No sign-in needed"), nil)
    }

    /// Where what lets Her in is kept, for one that has any.
    private func kept(_ c: HerConnector) -> String? {
        if c.auth == .oauth || c.curated != nil { return String(localized: "The sign-in is kept on \(machine.machineName), not on this iPhone or Hermesh’s servers.") }
        if c.auth == .credential { return String(localized: "The configuration only refers to the token; the token itself is kept on \(machine.machineName).") }
        return nil
    }
}

// MARK: - A connector's tools

struct HerConnectorToolsPage: View {
    let name: String

    var body: some View {
        HerEnvironmentPage { chat, _ in HerConnectorTools(chat: chat, name: name) }
            .navigationTitle("Tools")
            .navigationBarTitleDisplayMode(.inline)
    }
}

/// The tools a connector offered when it last connected: what each does, over its name.
private struct HerConnectorTools: View {
    let chat: HerChat
    let name: String
    @State private var state: HerConnectorsState
    @State private var tools: [HerConnectorTool]?

    init(chat: HerChat, name: String) {
        self.chat = chat
        self.name = name
        _state = State(initialValue: HerConnectorsState(chat: chat))
    }

    var body: some View {
        List {
            if let tools, !tools.isEmpty {
                Section {
                    ForEach(tools) { t in
                        VStack(alignment: .leading, spacing: 3) {
                            if let d = t.description { Text(d).lineLimit(3) }
                            Text(t.name).font(.footnote.monospaced()).foregroundStyle(t.description == nil ? .primary : .secondary)
                        }
                        .padding(.vertical, 2)
                    }
                } footer: {
                    Text("What \(herConnectorTitle(name)) offered when it last connected.")
                }
            } else if tools != nil {
                Section { Text("Once connected, the tools it offers are listed here.").font(.footnote).foregroundStyle(.secondary) }
            } else {
                Section { HStack { Spacer(); ProgressView(); Spacer() } }
            }
        }
        .accessibilityIdentifier("connector-tools-page")
        // The tools again whenever what her says of it changes.
        .task(id: "\(chat.envUp) \(chat.connectorsChanged)") {
            guard chat.envUp, let r = try? await state.calls.tools(name) else { return }
            tools = r.tools
        }
    }
}
