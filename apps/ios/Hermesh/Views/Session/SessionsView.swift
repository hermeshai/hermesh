import HermeshProto
import HermeshSessions
import SwiftUI

/// A session in the menu (docs/her-team.md §16): the coding agent's logo, the title, one mark (an orange dot
/// while it waits for the owner, a red one when it failed, a ring while it runs); with the metadata, who sent the
/// work to which agent, the project and the time. A tap opens it; long-press for Rename, Archive and Delete (the
/// Mac's ⋯ menu). No swipe: in the menu a swipe left is the menu's own, to close it.
struct SessionListRow: View {
    @Environment(AppModel.self) private var model
    let session: Session
    @State private var renaming = false
    @State private var deleting = false

    var body: some View {
        Button { model.open(.session(session.sessionID)) } label: { MenuSessionRow(session: session, metadata: model.listView.metadata) }
            .buttonStyle(RowStyle(selected: model.chosenSession == session.sessionID))
            .accessibilityAddTraits(model.chosenSession == session.sessionID ? .isSelected : [])
            .listRowInsets(EdgeInsets(top: 0, leading: 10, bottom: 0, trailing: 10))
            .listRowSeparator(.hidden)
            .listRowBackground(Color.clear)
            .contextMenu { SessionActions(session: session, renaming: $renaming, deleting: $deleting) }
            .sessionDialogs(session, renaming: $renaming, deleting: $deleting)
    }
}

/// What a menu row shows: the logo, the title and the mark; the metadata line when the view asks for it.
struct MenuSessionRow: View {
    @Environment(AppModel.self) private var model
    let session: Session
    var metadata = false

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            AgentLogo(id: session.agent, size: 22).frame(width: 28, height: 22)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 8) {
                    // One running has no mark: its title shimmers, while the menu shows.
                    Text(session.displayTitle)
                        .foregroundStyle(session.status == .archived ? .secondary : .primary)
                        .lineLimit(1)
                        .shimmer((session.isActive && !session.needsAttention) || session.titleGenerating, paused: !(model.drawerOpen || model.menuInView))
                        .frame(maxWidth: .infinity, alignment: .leading)
                    mark
                }
                if metadata {
                    HStack(spacing: 8) {
                        Text(model.senderLine(session)).lineLimit(1).frame(maxWidth: .infinity, alignment: .leading)
                        time.monospacedDigit()
                    }
                    .font(.footnote).foregroundStyle(.secondary)
                }
            }
        }
        .padding(.horizontal, 16).padding(.vertical, metadata ? 8 : 11)
        .frame(minHeight: 44)
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
        .accessibilityValue(state.map { Text($0) } ?? Text(""))
        .accessibilityIdentifier("session-\(session.displayTitle)")
    }

    /// One mark: orange, it waits for the owner; red, it failed; nothing, it runs (its title shimmers) or is done.
    @ViewBuilder private var mark: some View {
        if session.status == .waitingInput || session.isFailed {
            Circle().fill(session.status == .waitingInput ? .orange : .red).frame(width: 9, height: 9).accessibilityHidden(true)
        } else if session.status == .unspecified {
            Text(statusTitle(session.status)).font(.footnote).foregroundStyle(.secondary) // its machine cannot be read now
        }
    }

    /// The mark in words, for VoiceOver.
    private var state: String? {
        if session.isFailed && session.status != .crashed && session.status != .waitingInput { return String(localized: "Failed") }
        switch session.status {
        case .waitingInput, .crashed, .preparing, .queued, .running: return statusTitle(session.status)
        default: return nil
        }
    }

    /// How long a running turn has run, else when the session last changed.
    @ViewBuilder private var time: some View {
        if session.status == .running, let start = session.turnStartedDate {
            Text(start, style: .timer)
        } else {
            Text(listTime(session.updatedDate, now: model.now))
        }
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
        Button("Regenerate Title", systemImage: "arrow.trianglehead.2.clockwise") { Task { await model.regenerateTitle(session) } }
            .disabled(session.titleGenerating)
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
    /// In the system's bar: its symbol alone, the bar drawing the glass as for its other buttons.
    var inBar = false

    var body: some View {
        // The bar draws its buttons' glass and colour: the plain style only for the menu's own glass.
        Group {
            if inBar { menu } else { menu.buttonStyle(.plain) }
        }
        .accessibilityLabel("View Options")
        .accessibilityIdentifier("view-options")
    }

    private var menu: some View {
        @Bindable var model = model
        return Menu {
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
            if inBar {
                Image(systemName: "arrow.up.arrow.down")
            } else {
                // The menu's own glass, as its Search beside it, a size smaller: in the text's colour, not the tint.
                Image(systemName: "arrow.up.arrow.down").font(.subheadline.weight(.semibold)).foregroundStyle(.primary)
                    .frame(width: 36, height: 36)
                    .glassEffect(.regular.interactive(), in: .circle)
                    .contentShape(.circle)
            }
        }
    }

    private func groupName(_ g: SessionGrouping) -> LocalizedStringKey {
        switch g {
        case .status: "By Status"
        case .date: "By Date"
        case .project: "By Project"
        case .agent: "By Agent"
        case .machine: "By Machine"
        }
    }
}
