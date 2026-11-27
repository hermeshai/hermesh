import HermeshHer
import HermeshSessions
import SwiftUI

/// The side menu (docs/her-team.md §16), as the Mac's sidebar: search at the top, Her and the overview, then
/// every session as the view lays them out (grouped, each group collapsible, the view menu by the heading), and
/// at the bottom the account, its picture alone. A row opens its session; swipe or long-press it to rename,
/// archive or delete it.
struct DrawerView: View {
    @Environment(AppModel.self) private var model
    /// The size of the menu's buttons: the system's toolbar buttons, and the smallest target the HIG asks for.
    private let button: CGFloat = 44

    /// Where the system stands the bars on the side (iPhone Duo closed), the menu takes the page's place and the
    /// bar is the menu's: Close at its top, where the page has the menu button, then Search and the view menu.
    @Environment(\.barsOnTheSide) private var sideBars

    var body: some View {
        if sideBars {
            NavigationStack {
                menu.toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Close the menu", systemImage: "xmark") { model.drawerOpen = false }
                            .accessibilityIdentifier("menu-close")
                    }
                    ToolbarItem(placement: .topBarTrailing) {
                        Button("Search", systemImage: "magnifyingglass") { model.sheet = .search }
                            .accessibilityIdentifier("search")
                    }
                    ToolbarItem(placement: .topBarTrailing) { ViewMenu(inBar: true) }
                }
            }
        } else {
            menu
        }
    }

    private var menu: some View {
        ZStack(alignment: .bottomLeading) {
            VStack(spacing: 0) {
                // The top stays: search and the menu's own button whatever the list's length.
                top
                List {
                    newSessionPlace.plainRow()
                    herPlace.plainRow()
                    overviewPlace.plainRow()
                    sessionsHeading.plainRow()
                    MenuSessions()
                    Color.clear.frame(height: 90).plainRow() // room under the account's button
                }
                .listStyle(.plain)
                .scrollContentBackground(.hidden)
                .environment(\.defaultMinListRowHeight, 0)
                .scrollIndicators(.hidden)
                .scrollDismissesKeyboard(.immediately)
                .onScrollPhaseChange { _, phase in if phase == .interacting { hideKeyboard() } }
            }
            bottomBar
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Color(.menuBackground))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("drawer")
    }

    private var top: some View {
        HStack {
            Wordmark()
            Spacer()
            if !sideBars {
                Button {
                    model.sheet = .search
                } label: {
                    // The button's size itself: the .glass button style pads its label.
                    Image(systemName: "magnifyingglass").font(.body.weight(.medium)).frame(width: button, height: button)
                        .glassEffect(.regular.interactive(), in: .circle)
                        .contentShape(.circle)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Search")
                .accessibilityIdentifier("search")
            }
        }
        .frame(minHeight: button)
        .padding(.leading, 24).padding(.trailing, 16).padding(.top, 8).padding(.bottom, 14)
    }

    /// New Session, over Her (docs/thread-composer.md §4.6): the composer, on the machine and project used last.
    private var newSessionPlace: some View {
        Button {
            model.open(.newSession("", ""))
        } label: {
            HStack(spacing: 12) {
                Image(systemName: "square.and.pencil").frame(width: 28)
                Text("New")
                Spacer(minLength: 0)
            }
            .font(.body).frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 16).frame(height: 48)
        }
        .buttonStyle(RowStyle(selected: { if case .newSession = model.root { model.path.isEmpty } else { false } }()))
        .padding(.horizontal, 10)
        .accessibilityIdentifier("new-session-place")
    }

    /// Her, first, as on the Mac: a dot while what she sent is unread, and "Off" while she runs nowhere.
    private var herPlace: some View {
        let unread = herUnread(model.machines)
        let off = herEnvironments(model.machines).isEmpty
        return Button {
            model.open(.her)
        } label: {
            HStack(spacing: 12) {
                // The Mac's 16-point icon at 22 points, its 1.25 line grown alike, beside the symbols.
                HerRobot().stroke(style: StrokeStyle(lineWidth: 1.7, lineCap: .round, lineJoin: .round))
                    .frame(width: 22, height: 22).frame(width: 28)
                Text("Her")
                Spacer(minLength: 0)
                if off {
                    Text("Not on").font(.footnote).foregroundStyle(.secondary)
                } else if unread > 0 {
                    Circle().fill(Color.accentColor).frame(width: 9, height: 9).accessibilityHidden(true)
                }
            }
            .font(.body).frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 16).frame(height: 48)
        }
        .buttonStyle(RowStyle(selected: model.root == .her && model.path.isEmpty))
        .padding(.horizontal, 10)
        .accessibilityLabel(off ? Text("Her, not on") : unread > 0 ? Text("Her, \(unread) unread") : Text("Her"))
        .accessibilityIdentifier("her-place")
    }

    /// The overview: an orange dot while something needs the owner. No numbers.
    private var overviewPlace: some View {
        let team = teamState(model.sessions, requests: model.pairings.count)
        let waits = !team.waiting.isEmpty || team.requests > 0
        return Button {
            model.open(.overview)
        } label: {
            HStack(spacing: 12) {
                Image(systemName: "square.grid.2x2").frame(width: 28)
                Text("Overview")
                Spacer(minLength: 0)
                if waits {
                    Circle().fill(.orange).frame(width: 9, height: 9).accessibilityHidden(true)
                }
            }
            .font(.body).frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 16).frame(height: 48)
        }
        .buttonStyle(RowStyle(selected: model.root == .overview && model.path.isEmpty))
        .padding(.horizontal, 10)
        .accessibilityLabel(Text([String(localized: "Overview"), waits ? String(localized: "\(team.waiting.count + team.requests) need you") : "", team.running.isEmpty ? "" : String(localized: "\(team.running.count) working")].filter { !$0.isEmpty }.joined(separator: ", ")))
        .accessibilityIdentifier("overview-place")
    }

    /// "Sessions", and the view menu at its end (in the bar, where the system stands it on the side).
    private var sessionsHeading: some View {
        HStack {
            Text("Sessions").font(.footnote).foregroundStyle(.secondary)
            Spacer()
            if !sideBars { ViewMenu().frame(width: button, height: button) }
        }
        .frame(minHeight: button)
        .padding(.leading, 26).padding(.trailing, 14)
    }

    private var bottomBar: some View {
        Button {
            model.accountPresented = true
        } label: {
            AccountPicture(status: model.status).frame(width: 44, height: 44)
                .padding(4)
                .glassEffect(.regular.interactive(), in: .circle)
                .contentShape(.circle)
        }
        .buttonStyle(.plain)
        .padding(.leading, 16).padding(.bottom, 8)
        .accessibilityLabel("Account and devices")
        .accessibilityIdentifier("account")
    }
}

/// Every session, as the view lays them out (docs/her-team.md §16): by status, In Progress stays while nothing
/// runs, saying so; with no session at all, what will show here. Older ones load as the end comes into view;
/// archived ones on request.
private struct MenuSessions: View {
    @Environment(AppModel.self) private var model
    @State private var expandedGroup: SessionGrouping?
    @State private var expandedLimits: [String: Int] = [:]
    @State private var expandedTrees: Set<String> = []

    private struct RenderedSection: Identifiable {
        let section: SessionSection
        let open: Bool
        let limit: Int
        let slice: SectionSlice
        var id: String { section.id }
    }

    var body: some View {
        let machines = model.machines.sorted { $0.machineName.localizedStandardCompare($1.machineName) == .orderedAscending }.map(\.machineID)
        let sections = sessionSections(model.sessions, view: model.listView, archived: model.showArchived, now: model.now, machines: machines, alwaysActive: true, pinnedOrder: model.pinnedOrder, activeOrder: model.activeOrder)
        let allTrees = herChildren(model.showArchived ? model.sessions : model.sessions.filter { $0.status != .archived })
        let trees = model.listView.nest ? allTrees : [:]
        let limits = expandedGroup == model.listView.group ? expandedLimits : [:]
        let rendered = sections.map { section in
            let open = !model.collapsed.contains(section.id)
            let limit = limits[section.id] ?? sectionInitialLimit
            let slice = sliceSectionSessions(section.sessions, grouping: section.grouping, limit: limit, selectedID: model.chosenSession, trees: allTrees)
            return RenderedSection(section: section, open: open, limit: limit, slice: slice)
        }
        let anyTruncated = rendered.contains { $0.slice.hiddenCount > 0 }
        ForEach(rendered) { item in
            let section = item.section
            heading(section, open: item.open).plainRow()
            if item.open {
                if section.sessions.isEmpty {
                    Text("None right now").font(.subheadline).foregroundStyle(.tertiary)
                        .padding(.leading, 26).padding(.vertical, 6).frame(maxWidth: .infinity, alignment: .leading)
                        .plainRow()
                        .accessibilityIdentifier("none-now")
                }
                let rows = sectionRows(item.slice.visible, trees: trees, allTrees: allTrees)
                if section.key == SessionBucket.pinned.rawValue || (section.grouping == .status && section.key == SessionBucket.active.rawValue) {
                    ForEach(rows) { r in rowView(r) }
                        .onMove { from, to in moveFlatRows(in: section, rows: rows, from: from, to: to) }
                } else {
                    ForEach(rows) { r in rowView(r) }
                }
                if item.slice.hiddenCount > 0 {
                    showMoreButton(section, limit: item.limit, hiddenCount: item.slice.hiddenCount)
                }
            }
        }
        Group {
            if !model.hasWorld {
                InlineLoader(text: "Reading your sessions…")
            } else if !model.noOlder {
                if anyTruncated {
                    if model.loadingOlder {
                        InlineLoader(text: "Loading earlier sessions…")
                    } else {
                        Button("Load earlier tasks") { Task { await model.loadOlder() } }
                            .font(.footnote)
                            .foregroundStyle(.tint)
                            .padding(.top, 14)
                            .accessibilityIdentifier("load-older")
                    }
                } else {
                    InlineLoader(text: "Loading earlier sessions…")
                        .task(id: model.olderKey) { await model.loadOlder() }
                }
            } else if sections.isEmpty {
                VStack(spacing: 4) {
                    Text("No sessions yet").font(.callout.weight(.semibold)).foregroundStyle(.secondary)
                    Text("Give Her something to do. The sessions she sends to agents show up here.")
                        .font(.subheadline).foregroundStyle(.tertiary)
                }
                .multilineTextAlignment(.center).padding(.horizontal, 34).padding(.top, 40)
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("no-sessions")
            } else {
                HStack(spacing: 4) {
                    Text("No earlier sessions ·")
                    Button(model.showArchived ? "Hide Archived" : "Show Archived") { model.showArchived.toggle() }
                        .foregroundStyle(.tint)
                        .accessibilityIdentifier("toggle-archived")
                }
                .font(.footnote).foregroundStyle(.secondary)
                .padding(.top, 14)
            }
        }
        .frame(maxWidth: .infinity)
        .plainRow()
    }

    /// A group's heading: its title and ▾ (▸ when collapsed), a row of its own; a tap collapses or opens it.
    private func heading(_ section: SessionSection, open: Bool) -> some View {
        HStack(spacing: 0) {
            headingToggle(section, open: open)
            // A project's group: a new session in it, on the machine of its latest session there.
            if section.grouping == .project {
                Button {
                    let latest = section.sessions.max { $0.updatedDate < $1.updatedDate }
                    model.open(.newSession(latest?.machineID ?? "", section.key))
                } label: {
                    Image(systemName: "plus").font(.footnote.weight(.semibold)).foregroundStyle(.secondary)
                        .frame(width: 44, height: 32).contentShape(.rect)
                }
                .buttonStyle(.plain)
                .padding(.top, 10).padding(.trailing, 10)
                .accessibilityLabel(Text("New Session in \(title(section))"))
                .accessibilityIdentifier("new-in-\(section.key)")
            }
        }
    }

    private func headingToggle(_ section: SessionSection, open: Bool) -> some View {
        Button {
            withAnimation(.snappy) {
                if open {
                    if expandedGroup == model.listView.group {
                        expandedLimits.removeValue(forKey: section.id)
                    }
                    model.collapsed.insert(section.id)
                } else {
                    model.collapsed.remove(section.id)
                }
            }
        } label: {
            HStack(spacing: 6) {
                Text(title(section))
                Image(systemName: "chevron.down").font(.caption2.weight(.semibold)).foregroundStyle(.tertiary)
                    .rotationEffect(.degrees(open ? 0 : -90))
            }
            .font(.footnote).foregroundStyle(.secondary)
            .padding(.horizontal, 26).padding(.top, 14).padding(.bottom, 4)
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(.isHeader)
        .accessibilityValue(open ? Text("Expanded") : Text("Collapsed"))
        .accessibilityIdentifier("group-\(title(section))")
    }

    private func showMoreButton(_ section: SessionSection, limit: Int, hiddenCount: Int) -> some View {
        Button {
            withAnimation(.snappy) {
                var next = expandedGroup == model.listView.group ? expandedLimits : [:]
                next[section.id] = limit + sectionExpandStep
                expandedGroup = model.listView.group
                expandedLimits = next
            }
            if hiddenCount <= sectionExpandStep && !model.noOlder {
                Task { await model.loadOlder() }
            }
        } label: {
            Text("Show more (\(hiddenCount))")
                .font(.footnote)
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.leading, 56).padding(.trailing, 16).padding(.vertical, 8)
                .frame(minHeight: 36)
                .contentShape(.rect)
        }
        .buttonStyle(RowStyle(selected: false))
        .listRowInsets(EdgeInsets(top: 0, leading: 10, bottom: 0, trailing: 10))
        .listRowSeparator(.hidden)
        .listRowBackground(Color.clear)
        .accessibilityIdentifier("show-more-\(section.key)")
    }

    private enum SectionRow: Identifiable {
        case session(Session, twistShut: Bool?, mark: TreeMark?)
        case child(Session, first: Bool, last: Bool)
        case moreKids(parentID: String, hiddenCount: Int)

        var id: String {
            switch self {
            case .session(let s, _, _): s.sessionID
            case .child(let k, _, _): k.sessionID
            case .moreKids(let parentID, _): "more-kids-\(parentID)"
            }
        }

        var isTopLevel: Bool {
            if case .session = self { return true }
            return false
        }
    }

    /// Flattens each tree into separate List rows so every parent and child row owns its own context menu,
    /// while keeping children non-draggable and moving whole trees together (docs/her-conversations.md D13).
    private func sectionRows(_ visible: [Session], trees: [String: [Session]], allTrees: [String: [Session]]) -> [SectionRow] {
        var out: [SectionRow] = []
        for s in visible {
            let kids = trees[s.sessionID] ?? []
            if kids.isEmpty {
                let flatKids = allTrees[s.sessionID] ?? []
                out.append(.session(s, twistShut: nil, mark: flatKids.isEmpty ? nil : treeMark(s, children: flatKids)))
                continue
            }
            let shut = model.collapsedTrees.contains(s.sessionID)
            let mark = shut ? treeMark(s, children: kids) : nil
            out.append(.session(s, twistShut: shut, mark: mark))
            guard !shut else { continue }
            let shown = sliceChildren(kids, expanded: expandedTrees.contains(s.sessionID), selectedID: model.chosenSession)
            let hasMore = shown.hiddenCount > 0
            for (idx, k) in shown.visible.enumerated() {
                out.append(.child(k, first: idx == 0, last: !hasMore && idx == shown.visible.count - 1))
            }
            if hasMore {
                out.append(.moreKids(parentID: s.sessionID, hiddenCount: shown.hiddenCount))
            }
        }
        return out
    }

    @ViewBuilder private func rowView(_ r: SectionRow) -> some View {
        switch r {
        case .session(let s, let twistShut, let mark):
            SessionListRow(session: s, twistShut: twistShut, collapsedMark: mark, onTwist: twistShut != nil ? { toggleTree(s.sessionID) } : nil)
        case .child(let k, let first, let last):
            SessionListRow(session: k, isChild: true, childFirst: first, childLast: last)
                .moveDisabled(true)
        case .moreKids(let parentID, let hiddenCount):
            Button {
                withAnimation(.snappy) { _ = expandedTrees.insert(parentID) }
            } label: {
                Text("Show more (\(hiddenCount))")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.leading, 40).padding(.trailing, 16).padding(.vertical, 6)
                    .frame(minHeight: 34)
                    .contentShape(.rect)
            }
            .buttonStyle(RowStyle(selected: false))
            .padding(.leading, 26)
            .overlay(alignment: .leading) {
                Rectangle()
                    .fill(Color.primary.opacity(0.12))
                    .frame(width: 1.5)
                    .padding(.leading, 18)
                    .padding(.bottom, 6)
            }
            .listRowInsets(EdgeInsets(top: 0, leading: 10, bottom: 0, trailing: 10))
            .listRowSeparator(.hidden)
            .listRowBackground(Color.clear)
            .moveDisabled(true)
            .accessibilityIdentifier("show-more-kids-\(parentID)")
        }
    }

    private func moveFlatRows(in section: SessionSection, rows: [SectionRow], from: IndexSet, to: Int) {
        let topFrom = IndexSet(from.compactMap { i -> Int? in
            guard rows[i].isTopLevel else { return nil }
            return rows.prefix(i).filter(\.isTopLevel).count
        })
        guard !topFrom.isEmpty else { return }
        let topTo = rows.prefix(to).filter(\.isTopLevel).count
        model.moveSessions(in: section, from: topFrom, to: topTo)
    }

    private func toggleTree(_ id: String) {
        withAnimation(.snappy) {
            if model.collapsedTrees.contains(id) {
                model.collapsedTrees.remove(id)
            } else {
                model.collapsedTrees.insert(id)
                expandedTrees.remove(id)
            }
        }
    }

    private func title(_ s: SessionSection) -> String {
        switch s.grouping {
        case .project: s.key == herSection ? "Her" : s.key == scratchProject ? String(localized: "Temporary Workspace") : s.key
        case .agent: agentTitle(s.key)
        case .machine: model.machineName(s.key)
        default: SessionBucket(rawValue: s.key)?.title ?? s.key
        }
    }
}

extension View {
    /// A row of the menu's list drawn as the view itself: no insets, separator or background of the list's.
    func plainRow() -> some View {
        listRowInsets(EdgeInsets()).listRowSeparator(.hidden).listRowBackground(Color.clear)
    }
}

/// A row of the menu and of the session lists: highlighted while selected,
/// and under the finger from the moment it touches down.
struct RowStyle: ButtonStyle {
    let selected: Bool

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .background(selected || configuration.isPressed ? AnyShapeStyle(.fill.secondary) : AnyShapeStyle(.clear), in: .capsule)
            .contentShape(.capsule)
    }
}

/// The Mac's Her icon (icons.tsx `her`): a robot's head with its antenna,
/// ears and eyes, drawn on the same 16-point grid.
nonisolated struct HerRobot: Shape {
    func path(in rect: CGRect) -> Path {
        let k = min(rect.width, rect.height) / 16
        func p(_ x: CGFloat, _ y: CGFloat) -> CGPoint { CGPoint(x: rect.minX + x * k, y: rect.minY + y * k) }
        var path = Path()
        path.addRoundedRect(in: CGRect(origin: p(2.9, 4.6), size: CGSize(width: 10.2 * k, height: 8.2 * k)), cornerSize: CGSize(width: 2.3 * k, height: 2.3 * k))
        for (x, y0, y1) in [(8, 2, 4.6), (1.3, 7.9, 9.6), (14.7, 7.9, 9.6), (6.1, 7.8, 9.5), (9.9, 7.8, 9.5)] as [(CGFloat, CGFloat, CGFloat)] {
            path.move(to: p(x, y0))
            path.addLine(to: p(x, y1))
        }
        return path
    }
}
