import HermeshSessions
import SwiftUI

/// Every session, grouped as the Mac's sidebar groups them; swipe to
/// archive, filter by grant, project, coding agent or machine.
struct SessionsView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var model = model
        let sections = sessionSections(model.sessions, filter: model.filter, now: model.now)
        List {
            if model.filter.any {
                Section {
                    Button("Show All Sessions") { model.filter = SessionFilter() }
                }
            }
            ForEach(sections) { section in
                Section(section.bucket.title) {
                    ForEach(section.sessions, id: \.sessionID) { s in
                        Button { model.openSession(s.sessionID) } label: { SessionRow(session: s) }
                            .buttonStyle(.plain)
                            .listRowInsets(EdgeInsets())
                            .swipeActions(edge: .trailing) {
                                if s.isArchivable {
                                    Button("Archive", systemImage: "archivebox") { Task { await model.setArchived(s, true) } }.tint(.gray)
                                } else if s.status == .archived {
                                    Button("Unarchive", systemImage: "tray.and.arrow.up") { Task { await model.setArchived(s, false) } }.tint(.gray)
                                }
                            }
                    }
                }
            }
            Section {
                Group {
                    if model.noOlder {
                        Text(model.sessions.isEmpty ? "No sessions yet. Once an agent is connected, its work shows up here." : "No earlier sessions")
                    } else {
                        HStack(spacing: 8) {
                            ProgressView()
                            Text("Loading earlier sessions…")
                        }
                        .task { await model.loadOlder() }
                    }
                }
                .font(.footnote).foregroundStyle(.secondary).frame(maxWidth: .infinity)
                .listRowBackground(Color.clear)
            }
        }
        .navigationTitle("Sessions")
        .rootToolbar()
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) { FilterMenu(filter: $model.filter) }
        }
    }
}

/// Narrows the list to one grant, project, coding agent or machine, and shows archived sessions or not.
struct FilterMenu: View {
    @Environment(AppModel.self) private var model
    @Binding var filter: SessionFilter

    var body: some View {
        Menu {
            Button {
                filter = SessionFilter(archived: filter.archived)
            } label: {
                Label("All", systemImage: filter.narrowed ? "" : "checkmark")
            }
            Section("Grants") {
                ForEach(model.clients.filter { !$0.revoked }, id: \.clientID) { c in pick("grant", c.clientID, c.label) }
            }
            Section("Projects") {
                ForEach(Array(Set(model.sessions.map(\.projectID))).sorted(), id: \.self) { p in pick("project", p, p) }
            }
            Section("Coding Agents") {
                ForEach(Array(Set(model.sessions.map(\.agent))).sorted(), id: \.self) { a in pick("agent", a, agentTitle(a)) }
            }
            Section("Machines") {
                ForEach(model.machines, id: \.machineID) { m in pick("machine", m.machineID, m.machineName) }
            }
            Section {
                Toggle("Show Archived", isOn: $filter.archived)
            }
        } label: {
            Image(systemName: "line.3.horizontal.decrease").foregroundStyle(filter.any ? Color.accentColor : .primary)
        }
        .accessibilityLabel("Filter sessions")
        .accessibilityIdentifier("filter")
    }

    private func pick(_ field: String, _ value: String, _ title: String) -> some View {
        let on: Bool = switch field {
        case "grant": filter.grant == value
        case "project": filter.project == value
        case "agent": filter.agent == value
        default: filter.machine == value
        }
        return Button {
            var f = SessionFilter(archived: filter.archived)
            if !on {
                switch field {
                case "grant": f.grant = value
                case "project": f.project = value
                case "agent": f.agent = value
                default: f.machine = value
                }
            }
            filter = f
        } label: {
            if on { Label(title, systemImage: "checkmark") } else { Text(title) }
        }
    }
}
