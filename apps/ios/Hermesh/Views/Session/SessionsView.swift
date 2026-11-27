import HermeshProto
import HermeshSessions
import SwiftUI

/// A session in the menu (docs/her-team.md §16): the coding agent's logo (with a brand-blue orbit while it runs),
/// the title, one mark (an orange dot while it waits for the owner, a red one when it failed); with the metadata, who
/// sent the work to which agent, the project and the time. A tap opens it; long-press for Rename, Archive and Delete
/// (the Mac's ⋯ menu). No swipe: in the menu a swipe left is the menu's own, to close it.
struct SessionListRow: View {
    @Environment(AppModel.self) private var model
    let session: Session
    var isChild = false
    var childFirst = false
    var childLast = false
    var twistShut: Bool?
    var collapsedMark: TreeMark?
    var onTwist: (() -> Void)?
    @State private var renaming = false
    @State private var deleting = false

    var body: some View {
        let base = HStack(spacing: 0) {
            if let twistShut {
                Button {
                    onTwist?()
                } label: {
                    Image(systemName: "chevron.down")
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(.secondary)
                        .rotationEffect(.degrees(twistShut ? -90 : 0))
                        .frame(width: 26, height: 44)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .padding(.leading, 4)
                .accessibilityLabel(twistShut ? Text("Expand") : Text("Collapse"))
                .accessibilityIdentifier("twist-\(session.displayTitle)")
            }
            Button { model.open(.session(session.sessionID)) } label: {
                MenuSessionRow(session: session, metadata: model.listView.metadata, isChild: isChild, hasTwist: twistShut != nil, collapsedMark: collapsedMark)
            }
            .buttonStyle(RowStyle(selected: model.chosenSession == session.sessionID))
            .contentShape(.contextMenuPreview, .capsule)
            .accessibilityAddTraits(model.chosenSession == session.sessionID ? .isSelected : [])
            .contextMenu { SessionActions(session: session, isChild: isChild, renaming: $renaming, deleting: $deleting) }
        }
        .padding(.leading, isChild ? 26 : 0)
        .overlay(alignment: .leading) {
            if isChild {
                Rectangle()
                    .fill(Color.primary.opacity(0.12))
                    .frame(width: 1.5)
                    .padding(.leading, 18)
                    .padding(.top, childFirst ? 6 : 0)
                    .padding(.bottom, childLast ? 6 : 0)
            }
        }
        .sessionDialogs(session, renaming: $renaming, deleting: $deleting)

        Group {
            if twistShut != nil {
                base
                    .accessibilityElement(children: .contain)
                    .accessibilityIdentifier("tree-\(session.displayTitle)")
            } else {
                base
            }
        }
        .listRowInsets(EdgeInsets(top: 0, leading: 10, bottom: 0, trailing: 10))
        .listRowSeparator(.hidden)
        .listRowBackground(Color.clear)
    }
}

/// What a menu row shows: the logo, the title and the mark; the metadata line when the view asks for it.
struct MenuSessionRow: View {
    @Environment(AppModel.self) private var model
    let session: Session
    var metadata = false
    var isChild = false
    var hasTwist = false
    var collapsedMark: TreeMark?

    var body: some View {
        let running = session.isActive && !session.needsAttention
        let paused = !(model.drawerOpen || model.menuInView)
        let logoSize: CGFloat = isChild ? 19 : 22
        HStack(alignment: .top, spacing: isChild ? 10 : 12) {
            AgentLogo(id: session.agent, size: logoSize, running: running, paused: paused).frame(width: isChild ? 24 : 28, height: logoSize)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 8) {
                    // One running has no trailing mark: its logo has a brand-blue orbit and its title shimmers.
                    Text(session.displayTitle)
                        .font(isChild ? .subheadline : .body)
                        .foregroundStyle(session.status == .archived ? .secondary : .primary)
                        .lineLimit(1)
                        .shimmer(running || session.titleGenerating, paused: paused)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    mark
                }
                if metadata {
                    HStack(spacing: 8) {
                        Text(model.senderLine(session, child: isChild)).lineLimit(1).frame(maxWidth: .infinity, alignment: .leading)
                        time.monospacedDigit()
                    }
                    .font(.footnote).foregroundStyle(.secondary)
                }
            }
        }
        .padding(.leading, hasTwist ? 4 : (isChild ? 12 : 16))
        .padding(.trailing, 16)
        .padding(.vertical, isChild ? (metadata ? 6 : 8) : (metadata ? 8 : 11))
        .frame(minHeight: isChild ? 38 : 44)
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
        .accessibilityValue(state.map { Text($0) } ?? Text(""))
        .accessibilityIdentifier("session-\(session.displayTitle)")
    }

    /// One mark: a shut tree shows its most urgent mark (D12); else orange while waiting, red when failed, nothing while running or done.
    @ViewBuilder private var mark: some View {
        if let collapsedMark {
            Circle().fill(collapsedMark == .waiting ? .orange : .red).frame(width: 9, height: 9).accessibilityHidden(true)
        } else if session.status == .waitingInput || session.isFailed {
            Circle().fill(session.status == .waitingInput ? .orange : .red).frame(width: 9, height: 9).accessibilityHidden(true)
        } else if session.status == .unspecified {
            Text(statusTitle(session.status)).font(.footnote).foregroundStyle(.secondary) // its machine cannot be read now
        }
    }

    /// The mark in words, for VoiceOver.
    private var state: String? {
        if let collapsedMark {
            return collapsedMark == .waiting ? statusTitle(.waitingInput) : String(localized: "Failed")
        }
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
    var isChild = false
    @Binding var renaming: Bool
    @Binding var deleting: Bool

    var body: some View {
        if session.status != .archived && !isChild {
            Button(session.pinned ? "Unpin" : "Pin", systemImage: session.pinned ? "pin.slash" : "pin") {
                Task { await model.setPinned(session, !session.pinned) }
            }
        }
        Button("Rename", systemImage: "pencil") { renaming = true }
        if session.status == .archived {
            Button("Unarchive", systemImage: "tray.and.arrow.up") { Task { await model.setArchived(session, false) } }
        } else {
            let archivable = model.canArchive(session)
            let treeBusy = session.isArchivable && !archivable
            if treeBusy {
                Button {
                    Task { await model.setArchived(session, true) }
                } label: {
                    Text("Archive")
                    Text("Tasks are still running")
                    Image(systemName: "archivebox")
                }
                .disabled(true)
                .accessibilityIdentifier("archive-still-running")
            } else {
                Button("Archive", systemImage: "archivebox") {
                    Task { await model.setArchived(session, true) }
                }
                .disabled(!archivable)
                .accessibilityIdentifier("session-archive")
            }
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
        var parts = [gone]
        if session.isActive { parts.append(String(localized: "Its turn in progress is interrupted first.")) }
        if session.isHerConversation { parts.append(String(localized: "The tasks it sent are kept.")) }
        return parts.joined(separator: " ")
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
                Picker("Sort By", selection: $model.listView.sort) {
                    Text("Default (In Progress by Created)").tag(SessionSort.default)
                    Text("Created").tag(SessionSort.created)
                    Text("Last Active").tag(SessionSort.updated)
                }
                .pickerStyle(.inline)
                Picker("Order", selection: $model.listView.newestFirst) {
                    Text("Newest First").tag(true)
                    Text("Oldest First").tag(false)
                }
                .pickerStyle(.inline)
                if !model.activeOrder.isEmpty || !model.pinnedOrder.isEmpty {
                    Divider()
                    Button("Reset Custom Order") {
                        model.activeOrder = []
                        model.pinnedOrder = []
                    }
                }
            } label: {
                Text("Sort")
                Text(model.listView.newestFirst ? "Newest First" : "Oldest First")
            }
            Menu {
                Toggle("Metadata", isOn: $model.listView.metadata)
                Toggle("Nest subtasks", isOn: $model.listView.nest)
            } label: {
                Text("Show")
                Text(showValue)
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

    private var showValue: LocalizedStringKey {
        switch (model.listView.metadata, model.listView.nest) {
        case (true, true): "Metadata, nested"
        case (true, false): "Metadata"
        case (false, true): "Nested"
        case (false, false): "Title Only"
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
