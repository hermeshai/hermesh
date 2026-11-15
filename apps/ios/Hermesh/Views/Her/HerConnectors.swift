import HermeshHer
import HermeshKit
import SwiftUI
import UIKit

// Her's connectors on the iPhone (docs/her-connectors-demo.html): the apps and services Her can use, kept in her
// environment's configuration and shared by its assistants. A page lists them (Mine) and offers some (Discover);
// each has a page of its own; one is added by its address, and an account is signed in to in this iPhone's browser,
// the tokens staying on the machine. Her's conversation takes up a change from her next step. They open from Her's
// settings, from the plus beside the composer, and from a connect card once it is done.

// MARK: - Catalog

/// A connector offered on Discover: an address checked against the service's own documentation and reached, and
/// the ways it signs in, the one to use first.
struct HerCatalogEntry: Identifiable {
    let id: String
    let name: String
    let mono: String
    let color: Color
    let url: String
    let about: LocalizedStringResource
    let ways: [HerConnectorProbe.Auth]
    /// Where the service makes a token, for one signed in to with a token.
    var tokenPage: URL?
}

/// Discover's groups. Verified 2026-11-15: GitHub, Notion, Linear and Sentry answer 401 and sign in; Context7 is open.
let herConnectorCatalog: [(group: LocalizedStringResource, entries: [HerCatalogEntry])] = [
    ("Development", [
        HerCatalogEntry(id: "github", name: "GitHub", mono: "GH", color: Color(red: 0.12, green: 0.14, blue: 0.16), url: "https://api.githubcopilot.com/mcp/",
                        about: "Repositories, issues, pull requests and code search", ways: [.token],
                        tokenPage: URL(string: "https://github.com/settings/personal-access-tokens/new")),
        HerCatalogEntry(id: "linear", name: "Linear", mono: "L", color: Color(red: 0.37, green: 0.42, blue: 0.82), url: "https://mcp.linear.app/mcp",
                        about: "Issues, projects and cycles", ways: [.oauth]),
        HerCatalogEntry(id: "sentry", name: "Sentry", mono: "S", color: Color(red: 0.21, green: 0.18, blue: 0.35), url: "https://mcp.sentry.dev/mcp",
                        about: "Errors, performance issues and releases", ways: [.oauth]),
        HerCatalogEntry(id: "context7", name: "Context7", mono: "C7", color: Color(red: 0.06, green: 0.46, blue: 0.43), url: "https://mcp.context7.com/mcp",
                        about: "Up-to-date library documentation for writing code", ways: [.none]),
    ]),
    ("Work", [
        HerCatalogEntry(id: "notion", name: "Notion", mono: "N", color: .black, url: "https://mcp.notion.com/mcp",
                        about: "Pages, databases and comments", ways: [.oauth]),
    ]),
]

func herCatalogEntry(_ name: String) -> HerCatalogEntry? {
    herConnectorCatalog.lazy.flatMap(\.entries).first { $0.id == name }
}

/// A connector's name as the pages show it: the service's own, for one from Discover.
func herConnectorTitle(_ name: String) -> String { herCatalogEntry(name)?.name ?? name }

// MARK: - The pages' state

/// One environment's connectors as a page holds them: read with mcp/list, again whenever her says they changed and
/// after each change made here; what is being checked or signed in to.
@Observable
final class HerConnectorsState {
    let chat: HerChat
    private(set) var list: [HerConnector]?
    private(set) var failed: String?
    var checking: Set<String> = []
    var signingIn: Set<String> = []
    /// Switches turned here, shown at once until the list says so too.
    var switching: [String: Bool] = [:]

    init(chat: HerChat) { self.chat = chat }

    var calls: HerConnectorCalls { HerConnectorCalls { [chat] method, params in try await chat.call(method, params) } }

    var shown: [HerConnector] {
        (list ?? []).map { c in
            var c = c
            if let on = switching[c.name] { c.enabled = on }
            return c
        }
    }

    func look(_ c: HerConnector) -> HerConnectorLook {
        signingIn.contains(c.name) ? .checking : herConnectorLook(c, checking: checking.contains(c.name))
    }

    func load() async {
        do {
            list = try await calls.list()
            failed = nil
            switching = switching.filter { k, v in list?.first { $0.name == k }?.enabled != v }
        } catch {
            failed = error.localizedDescription
        }
    }

    /// Turns a connector on or off; says how it went.
    func setEnabled(_ c: HerConnector, _ on: Bool, model: AppModel) async {
        switching[c.name] = on
        do {
            try await calls.setEnabled(c, on)
            await load()
            let title = herConnectorTitle(c.name)
            model.show(on ? String(localized: "\(title) is on: Her can use it from her next step") : String(localized: "\(title) is off: Her stops using it from her next step"))
            if on, c.state != .ok { await check(c.name, model: model) }
        } catch {
            switching[c.name] = nil
            model.show(error.localizedDescription)
        }
    }

    /// Connects once and lists the tools again.
    @discardableResult
    func check(_ name: String, model: AppModel) async -> [HerConnectorTool]? {
        checking.insert(name)
        defer { checking.remove(name) }
        do {
            let r = try await calls.check(name)
            await load()
            return r.tools
        } catch {
            model.show(error.localizedDescription)
            return nil
        }
    }

    /// Signs in to a connector added already, in the browser, then checks it.
    func signIn(_ name: String, model: AppModel) async {
        signingIn.insert(name)
        let end = await chat.signIn(connector: name)
        signingIn.remove(name)
        switch end {
        case .connected:
            model.show(String(localized: "Signed in to \(herConnectorTitle(name))"))
            await check(name, model: model)
        case .cancelled: break
        case .failed(let why): model.show(why.isEmpty ? String(localized: "Couldn’t sign in to \(herConnectorTitle(name))") : why)
        }
    }

    func signOut(_ name: String, model: AppModel) async {
        do {
            try await calls.logout(name)
            model.show(String(localized: "Signed out of \(herConnectorTitle(name))"))
            await check(name, model: model)
        } catch { model.show(error.localizedDescription) }
    }

    func remove(_ c: HerConnector, model: AppModel) async -> Bool {
        do {
            try await calls.remove(c)
            await load()
            model.show(String(localized: "Removed \(herConnectorTitle(c.name)), and what was saved for it"))
            return true
        } catch {
            model.show(error.localizedDescription)
            return false
        }
    }

    /// Fixes what the row says is wrong: signs in, or tries again.
    func fix(_ c: HerConnector, model: AppModel) async {
        if look(c) == .needsLogin { await signIn(c.name, model: model) } else { await check(c.name, model: model) }
    }
}

/// The environment's Her these pages are about, with her connections kept while they show.
private struct HerEnvironmentPage<Content: View>: View {
    @Environment(AppModel.self) private var model
    @ViewBuilder let content: (HerChat, HerMachine) -> Content

    var body: some View {
        let envs = herEnvironments(model.machines)
        if let machine = herPick(envs, remembered: model.her.machine) {
            let chat = model.her.chat(machine.machineID, model: model)
            content(chat, machine).herConnections(chat, machine: machine)
        }
    }
}

/// Reads the connectors once the environment's connection is up, and again whenever her says they changed.
private struct LoadsConnectors: ViewModifier {
    let state: HerConnectorsState

    func body(content: Content) -> some View {
        content.task(id: "\(state.chat.envUp) \(state.chat.connectorsChanged)") {
            if state.chat.envUp { await state.load() }
        }
    }
}

// MARK: - Pieces

/// A connector's tile: its logo, else Discover's colours, else its first letters.
struct HerConnectorTile: View {
    let name: String
    var icon: Data?
    var size: CGFloat = 36

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: size * 0.23, style: .continuous)
        Group {
            if let icon, let picture = UIImage(data: icon) {
                Image(uiImage: picture).resizable().scaledToFit().padding(size * 0.12).background(Color.white)
            } else if let e = herCatalogEntry(name) {
                Text(e.mono).font(.system(size: size * (e.mono.count > 1 ? 0.36 : 0.5), weight: .bold)).foregroundStyle(.white)
                    .frame(maxWidth: .infinity, maxHeight: .infinity).background(e.color)
            } else {
                Text(monogram(name)).font(.system(size: size * 0.36, weight: .bold)).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, maxHeight: .infinity).background(Palette.fill2)
            }
        }
        .frame(width: size, height: size)
        .clipShape(shape)
        .overlay(shape.strokeBorder(Color.primary.opacity(0.1), lineWidth: 0.5))
        .accessibilityHidden(true)
    }

    private func monogram(_ name: String) -> String {
        let parts = name.split(whereSeparator: { "-_ ".contains($0) })
        let letters = parts.count > 1 ? "\(parts[0].prefix(1))\(parts[1].prefix(1))" : String(name.prefix(2))
        return letters.uppercased()
    }
}

/// What a connector is up to, in a word or two, with its colour.
struct HerConnectorStatusLine: View {
    let look: HerConnectorLook

    var body: some View {
        HStack(spacing: 5) {
            if look == .checking { ProgressView().controlSize(.mini) } else { Circle().fill(color).frame(width: 7, height: 7) }
            Text(words).lineLimit(1)
        }
        .font(.footnote).foregroundStyle(.secondary)
    }

    var words: String {
        switch look {
        case .off: String(localized: "Turned off")
        case .checking: String(localized: "Checking…")
        case .ok(let n): String(localized: "Available · \(n) tools")
        case .failed: String(localized: "Couldn’t connect")
        case .needsLogin: String(localized: "Needs sign-in")
        case .unknown: String(localized: "Not checked yet")
        }
    }

    private var color: Color {
        switch look {
        case .ok: .green
        case .failed: .red
        case .needsLogin: .orange
        default: .secondary.opacity(0.5)
        }
    }
}

/// One connector in a list: tile, name, what it is up to, a button for what needs the owner, and its switch.
private struct HerConnectorRow: View {
    @Environment(AppModel.self) private var model
    let state: HerConnectorsState
    let c: HerConnector
    let offline: Bool

    var body: some View {
        let look = state.look(c)
        HStack(spacing: 12) {
            NavigationLink(value: Route.herConnector(c.name)) {
                HStack(spacing: 12) {
                    HerConnectorTile(name: c.name, icon: c.icon)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(herConnectorTitle(c.name)).foregroundStyle(.primary)
                        HerConnectorStatusLine(look: look)
                    }
                    Spacer(minLength: 0)
                }
            }
            if look.needsOwner, !offline, c.enabled {
                Button(look == .needsLogin ? String(localized: "Sign In") : String(localized: "Retry")) { Task { await state.fix(c, model: model) } }
                    .buttonStyle(.bordered).controlSize(.small)
                    .accessibilityIdentifier("connector-fix-\(c.name)")
            }
            Toggle(String(localized: "Let Her Use \(herConnectorTitle(c.name))"), isOn: Binding(get: { c.enabled }, set: { on in Task { await state.setEnabled(c, on, model: model) } }))
                .labelsHidden()
                .disabled(offline || !c.editable)
                .accessibilityIdentifier("connector-switch-\(c.name)")
        }
        .accessibilityIdentifier("connector-\(c.name)")
    }
}

/// Said when the machine can't be reached: what shows is what was read last.
private struct OfflineNote: View {
    let machine: HerMachine

    var body: some View {
        Label("\(machine.machineName) is offline. This is how things were last; changes can be made once it is back.", systemImage: "wifi.slash")
            .font(.footnote).foregroundStyle(.secondary)
    }
}

// MARK: - The connectors page

struct HerConnectorsPage: View {
    var body: some View {
        HerEnvironmentPage { chat, machine in HerConnectorsList(chat: chat, machine: machine) }
            .navigationTitle("Connectors")
    }
}

private struct HerConnectorsList: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let machine: HerMachine
    @State private var state: HerConnectorsState
    @State private var discover = false
    @State private var adding: HerCatalogEntry?? = nil

    init(chat: HerChat, machine: HerMachine) {
        self.chat = chat
        self.machine = machine
        _state = State(initialValue: HerConnectorsState(chat: chat))
    }

    var body: some View {
        let offline = !chat.envUp
        List {
            Section {
                Picker("Show", selection: $discover) {
                    Text("Mine").tag(false)
                    Text("Discover").tag(true)
                }
                .pickerStyle(.segmented)
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets())
                .accessibilityIdentifier("connectors-tab")
            }
            if offline { Section { OfflineNote(machine: machine) } }
            if discover { discoverSections } else { mine(offline: offline) }
        }
        .listSectionSpacing(.compact)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button { adding = .some(nil) } label: { Image(systemName: "plus") }
                    .disabled(offline)
                    .accessibilityLabel(Text("Add Connector"))
                    .accessibilityIdentifier("connectors-add")
            }
        }
        .modifier(LoadsConnectors(state: state))
        .sheet(item: Binding(get: { adding.map { AddingItem(entry: $0) } }, set: { if $0 == nil { adding = nil } })) { item in
            HerAddConnectorSheet(state: state, machine: machine, entry: item.entry, existing: Set((state.list ?? []).map(\.name)))
        }
    }

    @ViewBuilder private func mine(offline: Bool) -> some View {
        let all = state.shown
        if state.list == nil {
            Section {
                if let failed = state.failed { Text(failed).foregroundStyle(.secondary) } else { HStack { Spacer(); ProgressView(); Spacer() } }
            }
        } else if all.isEmpty {
            Section {
                VStack(spacing: 10) {
                    Image(systemName: "powerplug").font(.largeTitle).foregroundStyle(.secondary)
                    Text("No Connectors Yet").font(.headline)
                    Text("Connect services such as GitHub or Notion, and Her can look things up and get things done there for you.")
                        .font(.subheadline).foregroundStyle(.secondary).multilineTextAlignment(.center)
                    Button("See What’s Available") { discover = true }.buttonStyle(.borderedProminent).padding(.top, 4)
                    Button("Add a Custom Connector…") { adding = .some(nil) }.disabled(offline)
                }
                .frame(maxWidth: .infinity).padding(.vertical, 18)
            } footer: {
                Text("You can also just tell Her: “Connect GitHub for me”.").frame(maxWidth: .infinity)
            }
        } else {
            let fix = all.filter { $0.enabled && state.look($0).needsOwner }
            let rest = all.filter { !fix.contains($0) && !$0.command }
            let commands = all.filter { !fix.contains($0) && $0.command }
            if !fix.isEmpty {
                Section("Needs Your Attention") { ForEach(fix) { HerConnectorRow(state: state, c: $0, offline: offline) } }
            }
            if !rest.isEmpty {
                Section {
                    ForEach(rest) { HerConnectorRow(state: state, c: $0, offline: offline) }
                } header: {
                    Text("Added")
                }
            }
            if !commands.isEmpty {
                Section {
                    ForEach(commands) { HerConnectorRow(state: state, c: $0, offline: offline) }
                } header: {
                    Text("Running on \(machine.machineName)")
                } footer: {
                    Text("Commands run on \(machine.machineName) as you.")
                }
            }
            Section {} footer: {
                Text("Kept on \(machine.machineName) and shared by the assistants there. Turning one on or off takes effect from Her’s next step.")
            }
        }
    }

    @ViewBuilder private var discoverSections: some View {
        ForEach(herConnectorCatalog, id: \.group.key) { group in
            Section(String(localized: group.group)) {
                ForEach(group.entries) { e in
                    let added = state.list?.contains { $0.name == e.id } ?? false
                    HStack(spacing: 12) {
                        HerConnectorTile(name: e.id, size: 40)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(e.name)
                            Text(e.about).font(.footnote).foregroundStyle(.secondary)
                            Text(e.ways.map(herWayName).joined(separator: " · ")).font(.caption).foregroundStyle(.tertiary)
                        }
                        Spacer(minLength: 0)
                        if added {
                            Button { model.path.append(.herConnector(e.id)) } label: { Image(systemName: "checkmark.circle.fill").font(.title2).foregroundStyle(.green) }
                                .accessibilityLabel(Text("\(e.name) Added"))
                        } else {
                            Button { adding = .some(e) } label: { Image(systemName: "plus.circle.fill").font(.title2) }
                                .disabled(!chat.envUp)
                                .accessibilityLabel(Text("Add \(e.name)"))
                                .accessibilityIdentifier("discover-add-\(e.id)")
                        }
                    }
                    .buttonStyle(.borderless)
                }
            }
        }
        Section {
            Button("Add a Custom Connector…") { adding = .some(nil) }.disabled(!chat.envUp)
        } footer: {
            Text("Any MCP server’s address works, such as https://mcp.example.com/mcp.")
        }
    }
}

/// A sheet's item: Discover's entry, or none for an address of the owner's own.
private struct AddingItem: Identifiable {
    let entry: HerCatalogEntry?
    var id: String { entry?.id ?? "" }
}

func herWayName(_ way: HerConnectorProbe.Auth) -> String {
    switch way {
    case .oauth: String(localized: "Sign in with an account")
    case .token: String(localized: "Token or API key")
    case .none: String(localized: "No sign-in needed")
    }
}

// MARK: - A connector's page

struct HerConnectorPage: View {
    let name: String

    var body: some View {
        HerEnvironmentPage { chat, machine in HerConnectorDetail(chat: chat, machine: machine, name: name) }
            .navigationTitle(herConnectorTitle(name))
            .navigationBarTitleDisplayMode(.inline)
    }
}

private struct HerConnectorDetail: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let machine: HerMachine
    let name: String
    @State private var state: HerConnectorsState
    @State private var tools: [HerConnectorTool]?
    @State private var checkedAt: Date?
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
        .modifier(LoadsConnectors(state: state))
        // The tools again whenever what her says of it changes.
        .task(id: "\(chat.envUp) \(chat.connectorsChanged) \(state.checking.contains(name))") {
            guard chat.envUp, !state.checking.contains(name), let r = try? await state.calls.tools(name) else { return }
            (tools, checkedAt) = r
        }
    }

    private func page(_ c: HerConnector, offline: Bool) -> some View {
        let look = state.look(c)
        return List {
            Section {
                VStack(spacing: 8) {
                    HerConnectorTile(name: c.name, icon: c.icon, size: 64)
                    Text(herConnectorTitle(c.name)).font(.title2.weight(.semibold))
                    Text(kind(c)).font(.subheadline).foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity).listRowBackground(Color.clear)
            }
            if offline { Section { OfflineNote(machine: machine) } }
            Section {
                Toggle(c.command ? String(localized: "Run on \(machine.machineName)") : String(localized: "Let Her Use It"),
                       isOn: Binding(get: { c.enabled }, set: { on in Task { await state.setEnabled(c, on, model: model) } }))
                    .disabled(offline || !c.editable)
                    .accessibilityIdentifier("connector-switch")
            }
            Section {
                LabeledContent("Status") { HerConnectorStatusLine(look: look) }
                if let account = account(c) { LabeledContent(account.0, value: account.1) }
                LabeledContent(c.command ? String(localized: "Command") : String(localized: "Address")) {
                    Text(c.summary).font(.footnote.monospaced()).lineLimit(2).truncationMode(.middle).textSelection(.enabled)
                }
            }
            if c.enabled, !offline, look.needsOwner {
                Section {
                    if case .failed(let why) = look, let why { Text(why).font(.footnote).foregroundStyle(.secondary).textSelection(.enabled) }
                    Button(look == .needsLogin ? String(localized: "Sign In in the Browser") : String(localized: "Retry")) { Task { await state.fix(c, model: model) } }
                        .accessibilityIdentifier("connector-fix")
                } header: {
                    Text(look == .needsLogin ? String(localized: "Her can’t use \(herConnectorTitle(c.name)) until you sign in.") : String(localized: "Her can’t reach \(herConnectorTitle(c.name)) now."))
                        .textCase(nil)
                }
            }
            Section {
                if let tools, !tools.isEmpty, c.enabled {
                    ForEach(tools) { t in
                        VStack(alignment: .leading, spacing: 2) {
                            Text(t.name).font(.footnote.monospaced())
                            if let d = t.description { Text(d).font(.footnote).foregroundStyle(.secondary).lineLimit(3) }
                        }
                    }
                } else {
                    Text(c.enabled ? "Once connected, the tools it offers are listed here." : "Once it is on, the tools it offers are listed here.")
                        .font(.footnote).foregroundStyle(.secondary)
                }
            } header: {
                HStack {
                    Text("Tools \(tools?.count ?? c.tools ?? 0)")
                    Spacer()
                    Button("Check Again") { Task { await state.check(c.name, model: model) } }
                        .font(.footnote).textCase(nil).disabled(offline || !c.enabled || state.checking.contains(c.name))
                        .accessibilityIdentifier("connector-check")
                }
            } footer: {
                if let checkedAt { Text("Checked \(checkedAt, format: .relative(presentation: .named))") }
            }
        }
        .listSectionSpacing(.compact)
        .accessibilityIdentifier("connector-page")
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Menu {
                    Button("Check Again", systemImage: "arrow.clockwise") { Task { await state.check(c.name, model: model) } }.disabled(offline || !c.enabled)
                    Button(c.command ? String(localized: "Copy Command") : String(localized: "Copy Address"), systemImage: "doc.on.doc") { UIPasteboard.general.string = c.summary }
                    if c.auth == .oauth, c.signedIn {
                        Button("Sign Out", systemImage: "rectangle.portrait.and.arrow.right") { Task { await state.signOut(c.name, model: model) } }.disabled(offline)
                    }
                    if c.editable {
                        Divider()
                        Button("Remove Connector", systemImage: "trash", role: .destructive) { removing = true }.disabled(offline)
                    }
                } label: {
                    Image(systemName: "ellipsis")
                }
                .accessibilityLabel(Text("More"))
                .accessibilityIdentifier("connector-more")
            }
        }
        .confirmationDialog(String(localized: "Remove \(herConnectorTitle(c.name))?"), isPresented: $removing, titleVisibility: .visible) {
            Button("Remove Connector", role: .destructive) {
                Task { if await state.remove(c, model: model) { model.path.removeAll { $0 == .herConnector(c.name) } } }
            }
        } message: {
            Text("Her stops using it from her next step, and what was saved for it on \(machine.machineName) is deleted.")
        }
    }

    private func kind(_ c: HerConnector) -> String {
        if c.curated != nil { return String(localized: "Built in · managed by Her") }
        if c.command { return String(localized: "Command · runs on \(machine.machineName)") }
        return String(localized: "Remote")
    }

    private func account(_ c: HerConnector) -> (String, String)? {
        switch c.auth {
        case .oauth: (String(localized: "Account"), c.signedIn ? String(localized: "Signed in") : String(localized: "Not signed in"))
        case .credential: (String(localized: "Token"), String(localized: "Saved on \(machine.machineName)"))
        case .none: c.curated != nil ? (String(localized: "Account"), c.signedIn ? String(localized: "Signed in") : String(localized: "Not signed in")) : nil
        }
    }
}

// MARK: - Adding a connector

/// Adds a connector by its address, or Discover's with its address set (connector-add-demo, cut to a phone): the name
/// and address; her checks the address; how it signs in, the way her found set; for an account, the sign-in in the
/// browser before it is written; then the tools it offers.
struct HerAddConnectorSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let state: HerConnectorsState
    let machine: HerMachine
    let entry: HerCatalogEntry?
    let existing: Set<String>

    private enum Step: Equatable {
        case address, checking, settings, signingIn, done([HerConnectorTool]), failed(String)
    }

    @State private var step: Step = .address
    @State private var name = ""
    @State private var url = ""
    @State private var way: HerConnectorProbe.Auth = .none
    @State private var token = ""
    @State private var probe: HerConnectorProbe?
    @State private var work: Task<Void, Never>?

    var body: some View {
        NavigationStack {
            Form { content }
                .navigationTitle(title)
                .navigationBarTitleDisplayMode(.inline)
                .toolbar { toolbar }
        }
        .interactiveDismissDisabled(step == .signingIn || step == .checking)
        .onAppear {
            guard let entry, step == .address else { return }
            name = entry.id
            url = entry.url
            way = entry.ways[0]
            step = .settings
        }
        .onDisappear { work?.cancel() }
    }

    private var title: String {
        switch step {
        case .done: String(localized: "Added")
        default: entry.map { String(localized: "Add \($0.name)") } ?? String(localized: "Add Connector")
        }
    }

    private var finalName: String { name.trimmingCharacters(in: .whitespaces).isEmpty ? herSuggestedConnectorName(url) : name.trimmingCharacters(in: .whitespaces) }

    /// Why the address step can't go on, if it can't.
    private var addressProblem: String? {
        let u = url.trimmingCharacters(in: .whitespacesAndNewlines)
        if u.isEmpty { return "" }
        guard let parsed = URL(string: u), let scheme = parsed.scheme, parsed.host() != nil else {
            return String(localized: "Connectors that run as a command are added on the Mac. Here, enter an address that starts with https://.")
        }
        if !["http", "https"].contains(scheme) { return String(localized: "Only http and https addresses work.") }
        if !herValidConnectorName(finalName) { return String(localized: "A name is 1 to 32 letters, digits, - or _.") }
        if existing.contains(finalName) { return String(localized: "There is a connector named \(finalName) already.") }
        return nil
    }

    @ViewBuilder private var content: some View {
        switch step {
        case .address:
            Section {
                TextField(herSuggestedConnectorName(url).isEmpty ? String(localized: "Name") : herSuggestedConnectorName(url), text: $name)
                    .autocorrectionDisabled().textInputAutocapitalization(.never)
                    .accessibilityIdentifier("add-connector-name")
                TextField("https://mcp.example.com/mcp", text: $url, axis: .vertical)
                    .font(.body.monospaced()).keyboardType(.URL).autocorrectionDisabled().textInputAutocapitalization(.never)
                    .accessibilityIdentifier("add-connector-url")
            } footer: {
                if let problem = addressProblem, !problem.isEmpty { Text(problem).foregroundStyle(.red) } else {
                    Text("An MCP server’s address. \(machine.machineName) connects to it, as Her will.")
                }
            }
        case .checking:
            Section { summary }
            Section {
                HStack(spacing: 10) {
                    ProgressView()
                    Text("Checking the address from \(machine.machineName)…")
                }
            }
        case .settings:
            Section { summary }
            Section {
                Picker("Sign in with", selection: $way) {
                    ForEach(ways, id: \.self) { w in
                        Text(herWayName(w) + (detected == w ? " · " + String(localized: "Detected") : "")).tag(w)
                    }
                }
                .accessibilityIdentifier("add-connector-way")
                if way == .token {
                    SecureField("Token", text: $token)
                        .textContentType(.oneTimeCode).autocorrectionDisabled().textInputAutocapitalization(.never)
                        .accessibilityIdentifier("add-connector-token")
                    if let page = entry?.tokenPage { Link(String(localized: "Get a token from \(entry?.name ?? "")"), destination: page).font(.footnote) }
                }
            } header: {
                Text("Sign-In")
            } footer: {
                Text(wayNote)
            }
            Section {} footer: { Text("Only add connectors you trust: Her does what their tools say.") }
        case .signingIn:
            Section {
                VStack(spacing: 12) {
                    HerConnectorTile(name: finalName, icon: probe?.icon, size: 56)
                    Text("Sign in to \(herConnectorTitle(finalName)) in the browser").font(.headline).multilineTextAlignment(.center)
                    Text("You come back here once you’re done.").font(.subheadline).foregroundStyle(.secondary)
                    ProgressView().padding(.top, 4)
                }
                .frame(maxWidth: .infinity).padding(.vertical, 20)
            }
        case .done(let tools):
            Section {
                VStack(spacing: 10) {
                    Image(systemName: "checkmark.circle.fill").font(.system(size: 52)).foregroundStyle(.green)
                    Text("Added \(herConnectorTitle(finalName))").font(.title3.weight(.semibold))
                    Text("\(tools.count) tools · Her can use it now").foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity).padding(.vertical, 16)
                .accessibilityIdentifier("add-connector-done")
            }
            if !tools.isEmpty {
                Section("Tools") {
                    ForEach(tools.prefix(5)) { Text($0.name).font(.footnote.monospaced()) }
                    if tools.count > 5 { Text("and \(tools.count - 5) more").font(.footnote).foregroundStyle(.secondary) }
                }
            }
        case .failed(let why):
            Section { summary }
            Section {
                Label(why, systemImage: "exclamationmark.triangle").foregroundStyle(.red).textSelection(.enabled)
                    .accessibilityIdentifier("add-connector-failed")
            } footer: {
                Text("Check the address and try again, or add it anyway and fix it later.")
            }
        }
    }

    private var summary: some View {
        HStack(spacing: 12) {
            HerConnectorTile(name: finalName, icon: probe?.icon)
            VStack(alignment: .leading, spacing: 2) {
                Text(herConnectorTitle(finalName))
                Text(url).font(.footnote.monospaced()).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
            }
        }
    }

    private var detected: HerConnectorProbe.Auth? { probe?.auth }

    private var ways: [HerConnectorProbe.Auth] {
        if let entry { return entry.ways }
        return probe?.ownClient == true ? [.token, .none] : [.oauth, .token, .none]
    }

    private var wayNote: String {
        switch way {
        case .oauth: String(localized: "You sign in with your \(herConnectorTitle(finalName)) account in this iPhone’s browser. The sign-in is kept on \(machine.machineName), not on this iPhone or Hermesh’s servers.")
        case .token: String(localized: "Sent as Authorization: Bearer. It is kept on \(machine.machineName); the configuration only refers to it.")
        case .none: probe?.requiresAuth == true ? String(localized: "This server asked for a sign-in: without one it may refuse Her.") : String(localized: "Anyone who can reach this address can use it.")
        }
    }

    @ToolbarContentBuilder private var toolbar: some ToolbarContent {
        ToolbarItem(placement: .cancellationAction) {
            if case .done = step {} else {
                Button("Cancel") { work?.cancel(); dismiss() }
            }
        }
        ToolbarItem(placement: .confirmationAction) {
            switch step {
            case .address:
                Button("Connect") { connect() }.disabled(addressProblem != nil).accessibilityIdentifier("add-connector-connect")
            case .settings:
                Button(way == .oauth ? String(localized: "Add and Sign In") : String(localized: "Add")) { add() }
                    .disabled(way == .token && token.trimmingCharacters(in: .whitespaces).isEmpty)
                    .accessibilityIdentifier("add-connector-add")
            case .done:
                Button("Done") { dismiss() }.accessibilityIdentifier("add-connector-close")
            case .failed:
                Menu("Next") {
                    Button("Edit the Address") { step = .address }
                    Button("Try Again") { connect() }
                    Button("Add Anyway") { way = .none; step = .settings }
                }
            default:
                EmptyView()
            }
        }
    }

    private func connect() {
        name = finalName
        url = url.trimmingCharacters(in: .whitespacesAndNewlines)
        step = .checking
        work = Task {
            do {
                let p = try await state.calls.probe(name: name, url: url)
                guard !Task.isCancelled else { return }
                probe = p
                if let error = p.error { step = .failed(error); return }
                way = p.ownClient && p.auth == .oauth ? .token : p.auth
                step = .settings
            } catch {
                if !Task.isCancelled { step = .failed(error.localizedDescription) }
            }
        }
    }

    private func add() {
        let signIn: HerConnectorSignIn = switch way {
        case .oauth: .oauth
        case .token: .token(token.trimmingCharacters(in: .whitespaces))
        case .none: .none
        }
        token = ""
        if way == .oauth { step = .signingIn }
        let (name, url, chat) = (name, url, state.chat)
        work = Task {
            do {
                let tools = try await state.calls.add(name: name, url: url, signIn: signIn) { server in
                    await chat.signIn(connector: name, server: server)
                }
                await state.load()
                guard !Task.isCancelled else { return }
                if let tools { step = .done(tools) } else { step = .settings }
            } catch {
                if !Task.isCancelled { step = .failed(error.localizedDescription) }
            }
        }
    }
}

// MARK: - From the plus beside the composer

/// The connectors with their switches, out of the plus: one turned off for now, in two taps; managing and adding
/// them go on from here.
struct HerConnectorsSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let chat: HerChat
    let machine: HerMachine
    @State private var state: HerConnectorsState
    @State private var adding = false

    init(chat: HerChat, machine: HerMachine) {
        self.chat = chat
        self.machine = machine
        _state = State(initialValue: HerConnectorsState(chat: chat))
    }

    var body: some View {
        let offline = !chat.envUp
        NavigationStack {
            List {
                if offline { Section { OfflineNote(machine: machine) } }
                Section {
                    if let list = state.list, list.isEmpty {
                        Text("No connectors yet.").foregroundStyle(.secondary)
                    } else if state.list == nil {
                        HStack { Spacer(); ProgressView(); Spacer() }
                    }
                    ForEach(state.shown) { c in
                        Toggle(isOn: Binding(get: { c.enabled }, set: { on in Task { await state.setEnabled(c, on, model: model) } })) {
                            HStack(spacing: 12) {
                                HerConnectorTile(name: c.name, icon: c.icon, size: 30)
                                VStack(alignment: .leading, spacing: 1) {
                                    Text(herConnectorTitle(c.name))
                                    HerConnectorStatusLine(look: state.look(c))
                                }
                            }
                        }
                        .disabled(offline || !c.editable)
                        .accessibilityIdentifier("connector-switch-\(c.name)")
                    }
                } footer: {
                    Text("Her stops using a connector turned off from her next step.")
                }
                Section {
                    Button("Manage Connectors") {
                        dismiss()
                        model.path.append(.herConnectors)
                    }
                    .accessibilityIdentifier("connectors-manage")
                    Button("Add Connector…") { adding = true }.disabled(offline)
                }
            }
            .navigationTitle("Connectors")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
        }
        .presentationDetents([.medium, .large])
        .modifier(LoadsConnectors(state: state))
        .sheet(isPresented: $adding) {
            HerAddConnectorSheet(state: state, machine: machine, entry: nil, existing: Set((state.list ?? []).map(\.name)))
        }
        .herConnections(chat, machine: machine)
    }
}

/// Her settings' row: how many connectors are on, or how many need the owner.
struct HerConnectorsRow: View {
    let chat: HerChat
    @State private var state: HerConnectorsState

    init(chat: HerChat) {
        self.chat = chat
        _state = State(initialValue: HerConnectorsState(chat: chat))
    }

    var body: some View {
        NavigationLink(value: Route.herConnectors) {
            LabeledContent("Connectors") {
                if let list = state.list {
                    let attention = list.filter { $0.enabled && herConnectorLook($0).needsOwner }.count
                    if attention > 0 {
                        Text("\(attention) need attention").foregroundStyle(.orange)
                    } else if list.isEmpty {
                        Text("None Yet")
                    } else {
                        Text("\(list.filter(\.enabled).count) on")
                    }
                }
            }
        }
        .modifier(LoadsConnectors(state: state))
        .accessibilityIdentifier("her-connectors")
    }
}
