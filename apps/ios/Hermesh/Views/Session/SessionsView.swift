import HermeshProto
import HermeshSessions
import SwiftUI

/// Every session, laid out as the Mac's sidebar: grouped, sorted by last
/// activity, with or without each row's metadata (the view menu), each
/// group collapsible; archived sessions on request. Swipe or long-press a
/// row to rename, archive or delete it.
struct SessionsView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let machines = model.machines.sorted { $0.machineName.localizedStandardCompare($1.machineName) == .orderedAscending }.map(\.machineID)
        let sections = sessionSections(model.sessions, view: model.listView, archived: model.showArchived, now: model.now, machines: machines)
        List {
            ForEach(sections) { section in
                let open = !model.collapsed.contains(section.id)
                Section {
                    if open {
                        ForEach(section.sessions, id: \.sessionID) { s in SessionListRow(session: s) }
                    }
                } header: {
                    Button {
                        withAnimation(.snappy) {
                            if open { model.collapsed.insert(section.id) } else { model.collapsed.remove(section.id) }
                        }
                    } label: {
                        HStack(spacing: 6) {
                            Text(title(section))
                            if section.counted { Text("\(section.sessions.count)").foregroundStyle(.tertiary) }
                            Image(systemName: "chevron.down").font(.caption2.weight(.semibold)).foregroundStyle(.tertiary)
                                .rotationEffect(.degrees(open ? 0 : -90))
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityAddTraits(.isHeader)
                    .accessibilityValue(open ? Text("Expanded") : Text("Collapsed"))
                    .accessibilityIdentifier("group-\(title(section))")
                }
            }
            Section {
                Group {
                    if !model.hasWorld {
                        PageLoader(text: "Reading your sessions…")
                    } else if model.noOlder {
                        if model.sessions.isEmpty {
                            Text("No sessions yet. Once an agent is connected, its work shows up here.")
                        } else {
                            HStack(spacing: 4) {
                                Text("No earlier sessions ·")
                                Button(model.showArchived ? "Hide Archived" : "Show Archived") { model.showArchived.toggle() }
                                    .foregroundStyle(.tint)
                                    .accessibilityIdentifier("toggle-archived")
                            }
                        }
                    } else {
                        InlineLoader(text: "Loading earlier sessions…")
                            .task(id: model.olderKey) { await model.loadOlder() }
                    }
                }
                .font(.footnote).foregroundStyle(.secondary).frame(maxWidth: .infinity)
                .listRowBackground(Color.clear)
            }
        }
        .navigationTitle("Sessions")
        .rootToolbar()
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) { ViewMenu() }
        }
    }

    private func title(_ s: SessionSection) -> String {
        switch s.grouping {
        case .agent: agentTitle(s.key)
        case .machine: model.machineName(s.key)
        default: SessionBucket(rawValue: s.key)?.title ?? s.key
        }
    }
}

/// The sessions of one grant or one machine, newest first: where Grants and
/// Machines lead. Its own page; the full list stays as the owner laid it out.
struct ScopedSessionsView: View {
    @Environment(AppModel.self) private var model
    let filter: SessionFilter
    let title: String

    var body: some View {
        let list = model.sessions.filter { filter.matches($0) && $0.status != .archived }.sorted { $0.updatedDate > $1.updatedDate }
        List {
            if list.isEmpty {
                Text("No sessions").foregroundStyle(.secondary).frame(maxWidth: .infinity).listRowBackground(Color.clear)
            }
            ForEach(list, id: \.sessionID) { s in SessionListRow(session: s) }
        }
        .navigationTitle(title)
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// A session in a list: opens it; swipe for Archive and Delete, long-press
/// for Rename, Archive and Delete (the Mac's ⋯ menu).
struct SessionListRow: View {
    @Environment(AppModel.self) private var model
    let session: Session
    @State private var renaming = false
    @State private var deleting = false

    var body: some View {
        Button { model.openSession(session.sessionID) } label: { SessionRow(session: session, metadata: model.listView.metadata) }
            .buttonStyle(.plain)
            .listRowInsets(EdgeInsets())
            .swipeActions(edge: .trailing) {
                // Not role: .destructive, which takes the row away before the confirmation shows.
                Button("Delete", systemImage: "trash") { deleting = true }.tint(.red)
                if session.isArchivable {
                    Button("Archive", systemImage: "archivebox") { Task { await model.setArchived(session, true) } }.tint(.gray)
                } else if session.status == .archived {
                    Button("Unarchive", systemImage: "tray.and.arrow.up") { Task { await model.setArchived(session, false) } }.tint(.gray)
                }
            }
            .contextMenu { SessionActions(session: session, renaming: $renaming, deleting: $deleting) }
            .sessionDialogs(session, renaming: $renaming, deleting: $deleting)
    }
}

/// Rename, Archive (or Unarchive), and Delete after a divider: a row's
/// long-press menu and the session page's ⋯ menu.
struct SessionActions: View {
    @Environment(AppModel.self) private var model
    let session: Session
    @Binding var renaming: Bool
    @Binding var deleting: Bool

    var body: some View {
        Button("Rename", systemImage: "pencil") { renaming = true }
        if session.status == .archived {
            Button("Unarchive", systemImage: "tray.and.arrow.up") { Task { await model.setArchived(session, false) } }
        } else {
            Button("Archive", systemImage: "archivebox") { Task { await model.setArchived(session, true) } }
                .disabled(!session.isArchivable)
        }
        Divider()
        Button("Delete…", systemImage: "trash", role: .destructive) { deleting = true }
    }
}

extension View {
    /// The rename field and the delete confirmation SessionActions asks for.
    func sessionDialogs(_ s: Session, renaming: Binding<Bool>, deleting: Binding<Bool>) -> some View {
        modifier(SessionDialogs(session: s, renaming: renaming, deleting: deleting))
    }
}

private struct SessionDialogs: ViewModifier {
    @Environment(AppModel.self) private var model
    let session: Session
    @Binding var renaming: Bool
    @Binding var deleting: Bool
    @State private var title = ""

    func body(content: Content) -> some View {
        content
            .alert("Rename Session", isPresented: $renaming) {
                TextField("Session title", text: $title)
                    .accessibilityIdentifier("rename-field")
                Button("Cancel", role: .cancel) {}
                Button("Rename") { Task { await model.rename(session, to: title) } }
                    .accessibilityIdentifier("rename-confirm")
            }
            .onChange(of: renaming) { _, on in if on { title = session.displayTitle } }
            .confirmationDialog(Text("Delete “\(session.displayTitle)”?"), isPresented: $deleting, titleVisibility: .visible) {
                Button("Delete", role: .destructive) { Task { await model.delete(session) } }
                    .accessibilityIdentifier("delete-confirm")
            } message: {
                Text(deleteMessage)
            }
    }

    private var deleteMessage: String {
        let gone = session.branch.isEmpty
            ? String(localized: "Its record and conversation are deleted for good.")
            : String(localized: "Its record, conversation and worktree are deleted for good. The agent’s changes stay on the branch \(session.branch).")
        return session.isActive ? gone + " " + String(localized: "Its turn in progress is interrupted first.") : gone
    }
}

/// The list's view menu: Group, Sort and Show, each a submenu saying what it is set to.
struct ViewMenu: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var model = model
        Menu {
            Menu {
                Picker("Group", selection: $model.listView.group) {
                    ForEach(SessionGrouping.allCases, id: \.self) { g in Text(groupName(g)).tag(g) }
                }
            } label: {
                Text("Group")
                Text(groupName(model.listView.group))
            }
            Menu {
                Picker("Sort By", selection: .constant(0)) { Text("Last Active").tag(0) }
                Picker("Order", selection: $model.listView.newestFirst) {
                    Text("Newest First").tag(true)
                    Text("Oldest First").tag(false)
                }
            } label: {
                Text("Sort")
                Text(model.listView.newestFirst ? "Newest First" : "Oldest First")
            }
            Menu {
                Toggle("Metadata", isOn: $model.listView.metadata)
            } label: {
                Text("Show")
                Text(model.listView.metadata ? "Metadata" : "Title Only")
            }
        } label: {
            Image(systemName: "line.3.horizontal.decrease")
        }
        .accessibilityLabel("View Options")
        .accessibilityIdentifier("view-options")
    }

    private func groupName(_ g: SessionGrouping) -> LocalizedStringKey {
        switch g {
        case .status: "By Status"
        case .date: "By Date"
        case .agent: "By Agent"
        case .machine: "By Machine"
        }
    }
}
