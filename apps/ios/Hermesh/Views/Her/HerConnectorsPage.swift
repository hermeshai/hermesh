import HermeshHer
import HermeshKit
import SwiftUI

// The connectors page (docs/her-connectors-v2-demo.html): the connected ones first, then the offered by what they
// are for; the whole of a group on a page of its own; and the two ways in besides Her's settings.

struct HerConnectorsPage: View {
    var body: some View {
        HerEnvironmentPage { chat, machine in HerConnectorsList(chat: chat, machine: machine) }
            .navigationTitle("Connectors")
    }
}

/// How many of a group the page shows before See All.
private let herGroupPreview = 4

private struct HerConnectorsList: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let machine: HerMachine
    @State private var state: HerConnectorsState
    @State private var target: HerConnectTarget?
    @State private var query = ""

    init(chat: HerChat, machine: HerMachine) {
        self.chat = chat
        self.machine = machine
        _state = State(initialValue: HerConnectorsState(chat: chat))
    }

    var body: some View {
        let offline = !chat.envUp
        let searched = query.trimmingCharacters(in: .whitespaces)
        List {
            if offline { Section { HerOfflineNote(machine: machine) } }
            if !searched.isEmpty {
                results(searched, offline: offline)
            } else {
                mine(offline: offline)
                // Offered once the list is read, so that a connected one is never offered again for a moment.
                if state.list != nil || offline { offered(offline: offline) }
            }
        }
        .listSectionSpacing(.compact)
        .searchable(text: $query, placement: .navigationBarDrawer(displayMode: .always), prompt: Text("Search"))
        .autocorrectionDisabled()
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button { target = HerConnectTarget() } label: { Image(systemName: "plus") }
                    .disabled(offline)
                    .accessibilityLabel(Text("Add a Custom Connector"))
                    .accessibilityIdentifier("connectors-add")
            }
        }
        .modifier(HerLoadsConnectors(state: state))
        .modifier(HerStartsConnecting(state: state, machine: machine, target: $target))
    }

    @ViewBuilder private func mine(offline: Bool) -> some View {
        let all = state.shown
        if state.list == nil {
            if let failed = state.failed {
                Section { Text(failed).foregroundStyle(.secondary) }
            } else if !offline {
                Section { HStack { Spacer(); ProgressView(); Spacer() } }
            }
        } else if all.isEmpty {
            Section {
                VStack(spacing: 8) {
                    Text("No Connectors Yet").font(.headline)
                    Text("Connect services such as GitHub or Notion, and Her can look things up and get things done there for you.")
                        .font(.subheadline).foregroundStyle(.secondary).multilineTextAlignment(.center)
                }
                .frame(maxWidth: .infinity).padding(.vertical, 12)
            } footer: {
                Text("You can also just tell Her: “Connect GitHub for me”.").frame(maxWidth: .infinity)
            }
        } else {
            // What needs the owner first; commands, which run on the machine as the owner, last.
            let fix = all.filter { $0.enabled && state.look($0).needsOwner }
            let rest = all.filter { !fix.contains($0) }
            Section {
                ForEach(fix + rest.filter { !$0.command } + rest.filter(\.command)) { HerConnectedRow(state: state, c: $0, machine: machine, offline: offline) }
            } header: {
                GroupHeader(title: "Connected")
            } footer: {
                Text("Kept on \(machine.machineName) and shared by the assistants there. Turning one on or off takes effect from Her’s next step.")
            }
        }
    }

    @ViewBuilder private func offered(offline: Bool) -> some View {
        let added = Set(state.shown.map(\.name))
        ForEach(herConnectorCatalog) { group in
            let open = group.entries.filter { !added.contains($0.id) }
            if !open.isEmpty {
                Section {
                    ForEach(open.prefix(herGroupPreview)) { HerOfferedRow(state: state, e: $0, offline: offline) }
                } header: {
                    GroupHeader(title: group.title, all: group.entries.count > herGroupPreview ? { model.path.append(.herConnectorGroup(group.id)) } : nil, id: group.id)
                }
            }
        }
        Section {
            Button("Add a Custom Connector…") { target = HerConnectTarget() }
                .disabled(offline)
                .accessibilityIdentifier("connectors-custom")
        } footer: {
            Text("Any MCP server’s address works, such as https://mcp.example.com/mcp.")
        }
    }

    /// The offered and the connected whose name or line has the words.
    @ViewBuilder private func results(_ words: String, offline: Bool) -> some View {
        let entries = herConnectorCatalog.flatMap(\.entries).filter {
            $0.name.localizedCaseInsensitiveContains(words) || String(localized: $0.about).localizedCaseInsensitiveContains(words)
        }
        let own = state.shown.filter { herCatalogEntry($0.name) == nil && $0.name.localizedCaseInsensitiveContains(words) }
        if entries.isEmpty && own.isEmpty {
            Section {
                VStack(spacing: 8) {
                    Text("Nothing Called “\(words)” Here").font(.headline)
                    Text("With its MCP address, you can add it yourself.").font(.subheadline).foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity).padding(.vertical, 12)
                Button("Add a Custom Connector…") { target = HerConnectTarget() }.disabled(offline)
            }
        } else {
            Section {
                ForEach(own) { HerConnectedRow(state: state, c: $0, machine: machine, offline: offline) }
                ForEach(entries) { HerEntryRow(state: state, e: $0, machine: machine, offline: offline) }
            }
        }
    }
}

/// A group's name over its rows, with See All when it has more than show.
private struct GroupHeader: View {
    let title: LocalizedStringResource
    var all: (() -> Void)?
    var id = ""

    var body: some View {
        HStack(alignment: .firstTextBaseline) {
            Text(title).font(.title3.weight(.bold)).foregroundStyle(.primary)
            Spacer()
            if let all {
                Button(action: all) {
                    HStack(spacing: 2) {
                        Text("See All")
                        Image(systemName: "chevron.right").font(.caption.weight(.semibold))
                    }
                }
                .font(.subheadline).foregroundStyle(.secondary)
                .accessibilityIdentifier("connectors-all-\(id)")
            }
        }
        .textCase(nil)
        .padding(.bottom, 2)
    }
}

// MARK: - The whole of a group

struct HerConnectorGroupPage: View {
    let id: String

    var body: some View {
        if let group = herConnectorCatalog.first(where: { $0.id == id }) {
            HerEnvironmentPage { chat, machine in HerConnectorGroupList(chat: chat, machine: machine, group: group) }
                .navigationTitle(Text(group.title))
                .navigationBarTitleDisplayMode(.inline)
        }
    }
}

private struct HerConnectorGroupList: View {
    let chat: HerChat
    let machine: HerMachine
    let group: HerCatalogGroup
    @State private var state: HerConnectorsState
    @State private var target: HerConnectTarget?

    init(chat: HerChat, machine: HerMachine, group: HerCatalogGroup) {
        self.chat = chat
        self.machine = machine
        self.group = group
        _state = State(initialValue: HerConnectorsState(chat: chat))
    }

    var body: some View {
        let offline = !chat.envUp
        List {
            if offline { Section { HerOfflineNote(machine: machine) } }
            if state.list != nil || offline {
                Section { ForEach(group.entries) { HerEntryRow(state: state, e: $0, machine: machine, offline: offline) } }
            } else {
                Section { HStack { Spacer(); ProgressView(); Spacer() } }
            }
        }
        .accessibilityIdentifier("connectors-group")
        .modifier(HerLoadsConnectors(state: state))
        .modifier(HerStartsConnecting(state: state, machine: machine, target: $target))
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
                if offline { Section { HerOfflineNote(machine: machine) } }
                Section {
                    if let list = state.list, list.isEmpty {
                        Text("No connectors yet.").foregroundStyle(.secondary)
                    } else if state.list == nil {
                        HStack { Spacer(); ProgressView(); Spacer() }
                    }
                    ForEach(state.shown) { c in
                        Toggle(isOn: Binding(get: { c.enabled }, set: { on in Task { await state.setEnabled(c, on, model: model) } })) {
                            HStack(spacing: 12) {
                                HerConnectorTile(name: c.name, icon: c.icon, command: c.command, size: 30)
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
                    Button("Add a Custom Connector…") { adding = true }.disabled(offline)
                }
            }
            .navigationTitle("Connectors")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
        }
        .presentationDetents([.medium, .large])
        .modifier(HerLoadsConnectors(state: state))
        .sheet(isPresented: $adding) {
            HerConnectSheet(state: state, machine: machine, entry: nil)
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
        .modifier(HerLoadsConnectors(state: state))
        .accessibilityIdentifier("her-connectors")
    }
}
