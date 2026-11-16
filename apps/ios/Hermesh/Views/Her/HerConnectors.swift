import HermeshHer
import HermeshKit
import SwiftUI
import UIKit

// Her's connectors on the iPhone (docs/her-connectors-v2-demo.html): the apps and services Her can use, kept in her
// environment's configuration and shared by its assistants. One page lists the ones connected and, under them, the
// ones offered (HerConnectorsPage); a row has one thing to do: connect, view, sign in, or try again. Connecting is
// one sheet with one button (HerConnectSheet): her asks the address how it signs in, and an account comes before a
// token. Each connector has a page of its own (HerConnectorPage); an account is signed in to in this iPhone's
// browser, the tokens staying on the machine. Her's conversation takes up a change from her next step. They open
// from Her's settings, from the plus beside the composer, and from a connect card once it is done.
//
// Here: what the pages hold of an environment's connectors, and the pieces they share.

// MARK: - The pages' state

/// One environment's connectors as a page holds them: read with mcp/list, again whenever her says they changed and
/// after each change made here; what is being connected, checked or signed in to.
@Observable
final class HerConnectorsState {
    let chat: HerChat
    private(set) var list: [HerConnector]?
    private(set) var failed: String?
    var checking: Set<String> = []
    var signingIn: Set<String> = []
    /// The offered ones connecting where their rows stand.
    var connecting: Set<String> = []
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

    /// Connects an offered service that needs no sign-in where its row stands. What her found instead, when it
    /// could not be reached or wants a sign-in after all: its sheet goes on from there.
    func connectOpen(_ e: HerCatalogEntry, model: AppModel) async -> HerConnectorProbe? {
        connecting.insert(e.id)
        defer { connecting.remove(e.id) }
        do {
            let found = try await calls.probe(name: e.id, url: e.url)
            guard found.error == nil, HerConnectWay(found) == .open else { return found }
            _ = try await calls.add(name: e.id, url: e.url, signIn: .none)
            await load()
            Haptic.success.play()
            model.show(String(localized: "\(e.name) is connected: Her can use it from her next step"))
        } catch {
            model.show(error.localizedDescription)
        }
        return nil
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
struct HerEnvironmentPage<Content: View>: View {
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
struct HerLoadsConnectors: ViewModifier {
    let state: HerConnectorsState

    func body(content: Content) -> some View {
        content.task(id: "\(state.chat.envUp) \(state.chat.connectorsChanged)") {
            if state.chat.envUp { await state.load() }
        }
    }
}

// MARK: - Starting to connect

/// What a connect sheet opens for: an offered entry, with what her found of it when its row asked already; or, with
/// none, an address of the owner's own.
struct HerConnectTarget: Identifiable {
    let id = UUID()
    var entry: HerCatalogEntry?
    var found: HerConnectorProbe?
}

/// What a row calls to connect its entry.
struct HerConnectAction {
    var start: (HerCatalogEntry) -> Void = { _ in }
}

private struct HerConnectKey: EnvironmentKey {
    static let defaultValue = HerConnectAction()
}

extension EnvironmentValues {
    var herConnect: HerConnectAction {
        get { self[HerConnectKey.self] }
        set { self[HerConnectKey.self] = newValue }
    }
}

/// Starts connecting the offered entries of a page: one that needs no sign-in where its row stands, the others in
/// the sheet this presents.
struct HerStartsConnecting: ViewModifier {
    @Environment(AppModel.self) private var model
    let state: HerConnectorsState
    let machine: HerMachine
    @Binding var target: HerConnectTarget?

    func body(content: Content) -> some View {
        content
            .environment(\.herConnect, HerConnectAction { e in
                guard e.open else { target = HerConnectTarget(entry: e); return }
                Task {
                    if let found = await state.connectOpen(e, model: model) { target = HerConnectTarget(entry: e, found: found) }
                }
            })
            .sheet(item: $target) { t in
                HerConnectSheet(state: state, machine: machine, entry: t.entry, found: t.found)
            }
    }
}

// MARK: - Pieces

/// A connector's tile: the service's icon for one the page offers, else the logo her found, else its first letters.
struct HerConnectorTile: View {
    let name: String
    var icon: Data?
    /// It runs as a command on the machine.
    var command = false
    var size: CGFloat = 44

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: size * 0.23, style: .continuous)
        Group {
            if let e = herCatalogEntry(name) {
                Image("Connectors/\(e.id)").resizable().interpolation(.high).scaledToFit().padding(e.fills ? 0 : size * 0.14).background(Color.white)
            } else if let icon, let picture = UIImage(data: icon) {
                Image(uiImage: picture).resizable().scaledToFit().padding(size * 0.12).background(Color.white)
            } else if command {
                Image(systemName: "terminal").font(.system(size: size * 0.42, weight: .semibold)).foregroundStyle(.green)
                    .frame(maxWidth: .infinity, maxHeight: .infinity).background(Color(white: 0.09))
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

/// What a connected one is, in a line: the service's own line, where a command runs, or the address's host.
func herConnectorAbout(_ c: HerConnector, machine: HerMachine) -> String {
    if let e = herCatalogEntry(c.name) { return String(localized: e.about) }
    if c.curated != nil { return String(localized: "Built in · managed by Her") }
    if c.command { return String(localized: "Runs on \(machine.machineName)") }
    return herHost(c.summary)
}

/// Said when the machine can't be reached: what shows is what was read last.
struct HerOfflineNote: View {
    let machine: HerMachine

    var body: some View {
        Label("\(machine.machineName) is offline. This is how things were last; changes can be made once it is back.", systemImage: "wifi.slash")
            .font(.footnote).foregroundStyle(.secondary)
    }
}

// MARK: - Rows

/// One line of a connectors list: the tile, a name over a line about it, and the one thing to do with it. A tap on
/// the rest of the line does `open`.
private struct HerConnectorLine<Trailing: View>: View {
    let id: String
    let tile: HerConnectorTile
    let title: String
    let detail: Text
    let open: () -> Void
    @ViewBuilder let trailing: Trailing

    var body: some View {
        HStack(spacing: 12) {
            Button(action: open) {
                HStack(spacing: 12) {
                    tile
                    VStack(alignment: .leading, spacing: 2) {
                        Text(title).foregroundStyle(.primary).lineLimit(1)
                        detail.font(.footnote).lineLimit(1)
                    }
                    Spacer(minLength: 0)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("connector-\(id)")
            trailing.buttonStyle(.borderless)
        }
        .padding(.vertical, 3)
    }
}

/// A connected one in a list: what it is, or what is wrong with it, and View, Sign In or Retry.
struct HerConnectedRow: View {
    @Environment(AppModel.self) private var model
    let state: HerConnectorsState
    let c: HerConnector
    let machine: HerMachine
    let offline: Bool

    var body: some View {
        let look = state.look(c)
        HerConnectorLine(id: c.name, tile: HerConnectorTile(name: c.name, icon: c.icon, command: c.command), title: herConnectorTitle(c.name),
                         detail: detail(look), open: { model.path.append(.herConnector(c.name)) }) {
            if look == .checking {
                ProgressView()
            } else if look.needsOwner, c.enabled {
                Button(look == .needsLogin ? String(localized: "Sign In") : String(localized: "Retry")) { Task { await state.fix(c, model: model) } }
                    .tint(.orange).disabled(offline)
                    .accessibilityIdentifier("connector-fix-\(c.name)")
            } else {
                Button("View") { model.path.append(.herConnector(c.name)) }
                    .accessibilityIdentifier("connector-view-\(c.name)")
            }
        }
    }

    private func detail(_ look: HerConnectorLook) -> Text {
        let words = HerConnectorStatusLine(look: look).words
        return switch look {
        case .needsLogin: Text(words).foregroundStyle(.orange)
        case .failed: Text(words).foregroundStyle(.red)
        case .off, .checking: Text(words).foregroundStyle(.secondary)
        case .ok, .unknown: Text(herConnectorAbout(c, machine: machine)).foregroundStyle(.secondary)
        }
    }
}

/// An offered one in a list: what it is for, and Connect.
struct HerOfferedRow: View {
    @Environment(\.herConnect) private var connect
    let state: HerConnectorsState
    let e: HerCatalogEntry
    let offline: Bool

    var body: some View {
        HerConnectorLine(id: e.id, tile: HerConnectorTile(name: e.id), title: e.name, detail: Text(e.about).foregroundStyle(.secondary),
                         open: { if !offline, !state.connecting.contains(e.id) { connect.start(e) } }) {
            if state.connecting.contains(e.id) {
                ProgressView()
            } else {
                Button("Connect") { connect.start(e) }
                    .disabled(offline)
                    .accessibilityIdentifier("connector-connect-\(e.id)")
            }
        }
    }
}

/// An entry's row wherever the whole of a group shows: connected, or offered.
struct HerEntryRow: View {
    let state: HerConnectorsState
    let e: HerCatalogEntry
    let machine: HerMachine
    let offline: Bool

    var body: some View {
        if let c = state.shown.first(where: { $0.name == e.id }) {
            HerConnectedRow(state: state, c: c, machine: machine, offline: offline)
        } else {
            HerOfferedRow(state: state, e: e, offline: offline)
        }
    }
}
