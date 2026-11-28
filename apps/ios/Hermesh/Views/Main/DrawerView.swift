import HermeshHer
import HermeshSessions
import SwiftUI
import UIKit

/// The side menu (docs/her-team.md §16), as the Mac's sidebar: search at the top, Her and the overview, then
/// every session as the view lays them out (grouped, each group collapsible, the view menu by the heading), and
/// at the bottom the account, its picture alone. A row opens its session; swipe or long-press it to rename,
/// archive or delete it.
struct DrawerView: View {
    @Environment(AppModel.self) private var model
    /// The size of the menu's buttons: the system's toolbar buttons, and the smallest target the HIG asks for.
    private let button: CGFloat = 44

    /// Where the system stands the bars on the side and the menu covers the page (iPhone Duo closed), the vertical
    /// bar is exclusively the sidebar's while open (and completely hidden while closed so nothing bleeds onto root
    /// pages): Collapse (`sidebarMenuUIImage`), Search and View Options at its top, and only `Account` + `+` New
    /// Session as two separate Liquid Glass circles at its bottom.
    @Environment(\.barsOnTheSide) private var sideBars

    private var menuOwnsBar: Bool { sideBars && !model.menuBeside }

    var body: some View {
        if menuOwnsBar {
            NavigationStack {
                menu
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar(model.drawerOpen ? .visible : .hidden, for: .navigationBar, .bottomBar)
                    .toolbar {
                        if model.drawerOpen {
                            duoToolbar
                        }
                    }
            }
        } else {
            menu
        }
    }

    @ToolbarContentBuilder private var duoToolbar: some ToolbarContent {
        ToolbarItem(placement: .cancellationAction) {
            Button {
                model.drawerOpen = false
            } label: {
                Image(uiImage: sidebarMenuUIImage)
            }
            .accessibilityLabel("Close the menu")
            .accessibilityIdentifier("menu-close")
        }
        if #available(iOS 27.1, *) {
            ToolbarItemGroup(placement: .topBarTrailing) {
                Button("Search", systemImage: "magnifyingglass") { model.sheet = .search }
                    .accessibilityIdentifier("search")
                ViewMenu(inBar: true)
            }
            .axisBehavior(.verticalPreferred)
            ToolbarItem(placement: .bottomBar) { AccountMenuButton(inBar: true) }
                .axisBehavior(.verticalPreferred)
            ToolbarSpacer(.fixed, placement: .bottomBar)
            ToolbarItem(placement: .bottomBar) { duoNewBarButton }
                .axisBehavior(.verticalPreferred)
        } else {
            ToolbarItem(placement: .topBarTrailing) {
                Button("Search", systemImage: "magnifyingglass") { model.sheet = .search }
                    .accessibilityIdentifier("search")
            }
            ToolbarItem(placement: .topBarTrailing) { ViewMenu(inBar: true) }
            ToolbarItem(placement: .bottomBar) { AccountMenuButton(inBar: true) }
            ToolbarSpacer(.fixed, placement: .bottomBar)
            ToolbarItem(placement: .bottomBar) { duoNewBarButton }
        }
    }

    private var duoNewBarButton: some View {
        let current: Bool = if case .newSession = model.root { model.path.isEmpty } else { false }
        return Button {
            model.open(.newSession("", ""))
        } label: {
            Image(systemName: "plus")
        }
        .accessibilityLabel("New")
        .accessibilityAddTraits(current ? .isSelected : [])
        .accessibilityIdentifier("new-session-place")
    }

    @ViewBuilder private var menu: some View {
        if model.menuBeside {
            sessionList
                .scrollEdgeEffectStyle(.soft, for: .top)
                .safeAreaBar(edge: .top) { top }
                .scrollEdgeEffectStyle(.soft, for: .bottom)
                .safeAreaBar(edge: .bottom) { unfoldedBottomBar }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(Color(.menuBackground))
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("drawer")
        } else {
            ZStack(alignment: .bottomLeading) {
                VStack(spacing: 0) {
                    // The top stays: search and the menu's own button whatever the list's length.
                    top
                    sessionList
                }
                if !menuOwnsBar {
                    AccountMenuButton()
                        .padding(.leading, 16).padding(.bottom, 8)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(Color(.menuBackground))
            .ignoresSafeArea(edges: menuOwnsBar ? .top : [])
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("drawer")
        }
    }

    private var sessionList: some View {
        List {
            if !menuOwnsBar {
                if !model.menuBeside {
                    newSessionPlace.plainRow()
                }
                herPlace.plainRow()
                overviewPlace.plainRow()
                sessionsHeading.plainRow()
            }
            MenuSessions()
            if !menuOwnsBar && !model.menuBeside {
                Color.clear.frame(height: 90).plainRow() // room under the floating account button
            }
        }
        .listStyle(.plain)
        .scrollContentBackground(.hidden)
        .environment(\.defaultMinListRowHeight, 0)
        .scrollIndicators(.hidden)
        .scrollDismissesKeyboard(.immediately)
        .onScrollPhaseChange { _, phase in if phase == .interacting { hideKeyboard() } }
    }

    private var top: some View {
        HStack {
            Wordmark()
            Spacer()
            if !menuOwnsBar {
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
        .padding(.leading, 24).padding(.trailing, 16).padding(.top, menuOwnsBar ? 24 : 8).padding(.bottom, 14)
    }

    /// New Session, over Her (docs/thread-composer.md §4.6): the composer, on the machine and project used last.
    private var newSessionPlace: some View {
        Button {
            model.open(.newSession("", ""))
        } label: {
            HStack(spacing: 12) {
                Image(systemName: "plus").frame(width: 28)
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
                Image(uiImage: herCatUIImage).frame(width: 28)
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
                Image(systemName: overviewSymbol).frame(width: 28)
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

    /// "Sessions", and the view menu at its end (in the vertical bar instead on iPhone Duo closed).
    private var sessionsHeading: some View {
        HStack {
            Text("Sessions").font(.footnote).foregroundStyle(.secondary)
            Spacer()
            ViewMenu().frame(width: button, height: button)
        }
        .frame(minHeight: button)
        .padding(.leading, 26).padding(.trailing, 14)
    }

    private var unfoldedBottomBar: some View {
        let isNew: Bool = if case .newSession = model.root { model.path.isEmpty } else { false }
        return HStack {
            AccountMenuButton()
            Spacer()
            Button {
                model.open(.newSession("", ""))
            } label: {
                Image(systemName: "plus")
                    .font(.title3.weight(.medium))
                    .foregroundStyle(isNew ? Color.accentColor : .primary)
                    .frame(width: 44, height: 44)
                    .padding(4)
                    .glassEffect(.regular.interactive(), in: .circle)
                    .contentShape(.circle)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("New")
            .accessibilityAddTraits(isNew ? .isSelected : [])
            .accessibilityIdentifier("new-session-place")
        }
        .padding(.horizontal, 16)
        .padding(.bottom, 8)
    }
}

/// The account menu at the sidebar's foot (or in iPhone Duo's vertical bar while the sidebar is open): pairing actions
/// as on the Mac, and Settings.
struct AccountMenuButton: View {
    @Environment(AppModel.self) private var model
    var id = "account"
    var inBar = false

    var body: some View {
        Group {
            if inBar {
                menu
            } else {
                menu.buttonStyle(.plain)
            }
        }
        .accessibilityLabel("Account and devices")
        .accessibilityIdentifier(id)
    }

    private var menu: some View {
        let account = model.status.hosted && !model.status.account.isEmpty
        let who = account ? model.status.account : model.status.hosted ? String(localized: "Hermesh service") : String(localized: "Your own server")
        let state = model.status.relayConnected ? String(localized: "Connected") : String(localized: "Not connected")
        return Menu {
            Section {
                NewMenuItems()
            }
            Section {
                Button { model.accountPresented = true } label: {
                    Text("Settings")
                    Text("\(who) · \(state)")
                    Image(systemName: "gearshape")
                }
                .accessibilityIdentifier("account-settings-open")
            }
        } label: {
            if inBar {
                AccountPicture(status: model.status).frame(width: 30, height: 30)
            } else {
                AccountPicture(status: model.status).frame(width: 44, height: 44)
                    .padding(4)
                    .glassEffect(.regular.interactive(), in: .circle)
                    .contentShape(.circle)
            }
        }
    }
}

/// Every session, as the view lays them out (docs/her-team.md §16): by status, In Progress stays while nothing
/// runs, saying so; with no session at all, what will show here. Older ones load as the end comes into view;
/// archived ones on request. Shared by `DrawerView` and iPhone Duo's standalone `SessionsHubView`.
struct MenuSessions: View {
    @Environment(AppModel.self) private var model
    var machineFilter: String? = nil
    var metadataOverride: Bool? = nil
    var onOpen: ((Session) -> Void)? = nil
    @State private var expandedGroup: SessionGrouping?
    @State private var expandedLimits: [String: Int] = [:]
    @State private var expandedTrees: Set<String> = []
    @State private var sectionMemo = MenuSectionsMemo()

    private struct MenuSectionsKey: Equatable {
        let rev: UInt64
        let machineFilter: String?
        let view: SessionListLayout
        let showArchived: Bool
        let dayStart: Date
        let machines: [String]
        let pinnedOrder: [String]
        let activeOrder: [String]
    }

    @MainActor private final class MenuSectionsMemo {
        var key: MenuSectionsKey?
        var sections: [SessionSection] = []
        var allTrees: [String: [Session]] = [:]
    }

    private struct RenderedSection: Identifiable {
        let section: SessionSection
        let open: Bool
        let limit: Int
        let slice: SectionSlice
        var id: String { section.id }
    }

    var body: some View {
        let rev = model.sessionsRevision
        let machines = model.machines.sorted { $0.machineName.localizedStandardCompare($1.machineName) == .orderedAscending }.map(\.machineID)
        let filteredMachines = machineFilter.map { [$0] } ?? machines
        let dayStart = Calendar.current.startOfDay(for: model.now)
        let memoKey = MenuSectionsKey(
            rev: rev,
            machineFilter: machineFilter,
            view: model.listView,
            showArchived: model.showArchived,
            dayStart: dayStart,
            machines: filteredMachines,
            pinnedOrder: model.pinnedOrder,
            activeOrder: model.activeOrder
        )
        let (sections, allTrees): ([SessionSection], [String: [Session]]) = {
            if sectionMemo.key == memoKey {
                return (sectionMemo.sections, sectionMemo.allTrees)
            }
            let baseSessions = machineFilter.map { id in model.sessions.filter { $0.machineID == id } } ?? model.sessions
            let builtSections = sessionSections(baseSessions, view: model.listView, archived: model.showArchived, now: dayStart.addingTimeInterval(43_200), machines: filteredMachines, alwaysActive: true, pinnedOrder: model.pinnedOrder, activeOrder: model.activeOrder)
            let builtTrees = herChildren(model.showArchived ? baseSessions : baseSessions.filter { $0.status != .archived })
            sectionMemo.key = memoKey
            sectionMemo.sections = builtSections
            sectionMemo.allTrees = builtTrees
            return (builtSections, builtTrees)
        }()
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
            SessionListRow(session: s, twistShut: twistShut, collapsedMark: mark, metadataOverride: metadataOverride,
                           onOpen: onOpen.map { fn in { fn(s) } },
                           onTwist: twistShut != nil ? { toggleTree(s.sessionID) } : nil)
        case .child(let k, let first, let last):
            SessionListRow(session: k, isChild: true, childFirst: first, childLast: last, metadataOverride: metadataOverride,
                           onOpen: onOpen.map { fn in { fn(k) } })
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

let overviewSymbol = "gauge.with.dots.needle.bottom.50percent"

/// Unified staggered three-line sidebar toggle icon (two full-width round-cap strokes + shorter bottom stroke) shared
/// across root pages, Duo's slid-out sidebar Collapse button, and wide split-screen expand/collapse controls.
let sidebarMenuUIImage: UIImage = {
    let size: CGFloat = 22
    let format = UIGraphicsImageRendererFormat.default()
    format.opaque = false
    format.scale = 3
    return UIGraphicsImageRenderer(size: CGSize(width: size, height: size), format: format).image { ctx in
        let cg = ctx.cgContext
        cg.setStrokeColor(UIColor.black.cgColor)
        cg.setLineWidth(1.8)
        cg.setLineCap(.round)
        cg.move(to: CGPoint(x: 3.6, y: 6.2))
        cg.addLine(to: CGPoint(x: 18.4, y: 6.2))
        cg.move(to: CGPoint(x: 3.6, y: 11.0))
        cg.addLine(to: CGPoint(x: 18.4, y: 11.0))
        cg.move(to: CGPoint(x: 3.6, y: 15.8))
        cg.addLine(to: CGPoint(x: 12.2, y: 15.8))
        cg.strokePath()
    }.withRenderingMode(.alwaysTemplate)
}()

/// Outline speech-bubble template image for Duo's `Conversations` feature tab so `UITabBar` keeps a clean stroke
/// matching `herCatUIImage` and `overviewSymbol` instead of substituting `bubble.left.fill`.
let conversationsTabUIImage: UIImage = {
    let sym = UIImage(systemName: "bubble.left", withConfiguration: UIImage.SymbolConfiguration(pointSize: 17, weight: .medium))!
    let format = UIGraphicsImageRendererFormat.default()
    format.opaque = false
    format.scale = 3
    return UIGraphicsImageRenderer(size: sym.size, format: format).image { _ in
        sym.draw(in: CGRect(origin: .zero, size: sym.size))
    }.withRenderingMode(.alwaysTemplate)
}()

/// Code-brackets (`</>`) template image for Duo's standalone `Sessions` hub tab (`chevron.left.forwardslash.chevron.right`),
/// rendered at matching optical weight alongside `conversationsTabUIImage`, `herCatUIImage` and `overviewSymbol`.
let sessionsHubTabUIImage: UIImage = {
    let sym = UIImage(systemName: "chevron.left.forwardslash.chevron.right",
                      withConfiguration: UIImage.SymbolConfiguration(pointSize: 16, weight: .medium))!
    let format = UIGraphicsImageRendererFormat.default()
    format.opaque = false
    format.scale = 3
    return UIGraphicsImageRenderer(size: sym.size, format: format).image { _ in
        sym.draw(in: CGRect(origin: .zero, size: sym.size))
    }.withRenderingMode(.alwaysTemplate)
}()

/// Monochrome template cat glyph for Her across the sidebar, collapsed rail and iPhone Duo's native `UITabBar`:
/// rendered as an `.alwaysTemplate` `UIImage` so UIKit's Liquid Glass tab bar and SwiftUI rows tint it identically.
let herCatUIImage: UIImage = {
    let size: CGFloat = 22
    let k = size / 20
    func p(_ x: CGFloat, _ y: CGFloat) -> CGPoint { CGPoint(x: x * k, y: y * k) }
    let format = UIGraphicsImageRendererFormat.default()
    format.opaque = false
    format.scale = 3
    return UIGraphicsImageRenderer(size: CGSize(width: size, height: size), format: format).image { ctx in
        let cg = ctx.cgContext
        let outline = UIBezierPath()
        outline.move(to: p(5.2, 7.2))
        outline.addLine(to: p(4.3, 3.4))
        outline.addLine(to: p(7.9, 5.4))
        outline.addQuadCurve(to: p(12.1, 5.4), controlPoint: p(10.0, 4.85))
        outline.addLine(to: p(15.7, 3.4))
        outline.addLine(to: p(14.8, 7.2))
        outline.addQuadCurve(to: p(16.6, 12.0), controlPoint: p(16.35, 9.1))
        outline.addCurve(to: p(10.0, 16.8), controlPoint1: p(16.6, 15.1), controlPoint2: p(14.0, 16.8))
        outline.addCurve(to: p(3.4, 12.0), controlPoint1: p(6.0, 16.8), controlPoint2: p(3.4, 15.1))
        outline.addQuadCurve(to: p(5.2, 7.2), controlPoint: p(3.65, 9.1))
        outline.close()

        cg.setStrokeColor(UIColor.black.cgColor)
        cg.setLineWidth(1.65 * k)
        cg.setLineCap(.round)
        cg.setLineJoin(.round)
        cg.addPath(outline.cgPath)
        cg.strokePath()

        cg.setFillColor(UIColor.black.cgColor)
        let r = 1.05 * k
        cg.fillEllipse(in: CGRect(x: 7.8 * k - r, y: 11.3 * k - r, width: r * 2, height: r * 2))
        cg.fillEllipse(in: CGRect(x: 12.2 * k - r, y: 11.3 * k - r, width: r * 2, height: r * 2))
    }.withRenderingMode(.alwaysTemplate)
}()
