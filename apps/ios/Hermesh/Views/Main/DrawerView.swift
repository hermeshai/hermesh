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
    /// bar is the menu's: Close at its top, where the page has the menu button, then Search.
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
                }
            }
        } else {
            menu
        }
    }

    private var menu: some View {
        ZStack(alignment: .bottom) {
            VStack(spacing: 0) {
                // The top stays: search and the menu's own button whatever the list's length.
                top
                List {
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
            if model.menuBeside {
                // Where the rail's button expands it again. Over the page, it puts the menu away.
                Button {
                    if model.drawerOpen { model.drawerOpen = false } else { model.menuRail = true }
                } label: {
                    Image(systemName: "sidebar.left").font(.body.weight(.medium)).frame(width: button, height: button)
                        .glassEffect(.regular.interactive(), in: .circle)
                        .contentShape(.circle)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Collapse the menu")
                .accessibilityIdentifier("menu-hide")
            }
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
        .padding(.leading, model.menuBeside ? 16 : 24).padding(.trailing, 16).padding(.top, 8).padding(.bottom, 14)
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

    /// The overview: an orange dot while something needs the owner, a ring while anything runs. No numbers.
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
                HStack(spacing: 6) {
                    if waits { Circle().fill(.orange).frame(width: 9, height: 9) }
                    if !team.running.isEmpty { BusyRing() }
                }
                .accessibilityHidden(true)
            }
            .font(.body).frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 16).frame(height: 48)
        }
        .buttonStyle(RowStyle(selected: model.root == .overview && model.path.isEmpty))
        .padding(.horizontal, 10)
        .accessibilityLabel(Text([String(localized: "Overview"), waits ? String(localized: "\(team.waiting.count + team.requests) need you") : "", team.running.isEmpty ? "" : String(localized: "\(team.running.count) working")].filter { !$0.isEmpty }.joined(separator: ", ")))
        .accessibilityIdentifier("overview-place")
    }

    /// "Sessions", and the view menu at its end.
    private var sessionsHeading: some View {
        HStack {
            Text("Sessions").font(.footnote).foregroundStyle(.secondary)
            Spacer()
            ViewMenu().frame(width: button, height: button)
        }
        .padding(.leading, 26).padding(.trailing, 14)
    }

    private var bottomBar: some View {
        HStack {
            Button {
                model.accountPresented = true
            } label: {
                AccountPicture(status: model.status).frame(width: 44, height: 44)
                    .padding(4)
                    .glassEffect(.regular.interactive(), in: .circle)
                    .contentShape(.circle)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Account and devices")
            .accessibilityIdentifier("account")
            Spacer()
        }
        .padding(.horizontal, 16).padding(.bottom, 8).padding(.top, 24)
        // The fade only shows: a row under it (once the list is long) still takes a tap.
        .background(LinearGradient(colors: [Color(.menuBackground).opacity(0), Color(.menuBackground)], startPoint: .top, endPoint: .center).allowsHitTesting(false))
    }
}

/// Every session, as the view lays them out (docs/her-team.md §16): by status, In Progress stays while nothing
/// runs, saying so; with no session at all, what will show here. Older ones load as the end comes into view;
/// archived ones on request.
private struct MenuSessions: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let machines = model.machines.sorted { $0.machineName.localizedStandardCompare($1.machineName) == .orderedAscending }.map(\.machineID)
        let sections = sessionSections(model.sessions, view: model.listView, archived: model.showArchived, now: model.now, machines: machines, alwaysActive: true)
        ForEach(sections) { section in
            let open = !model.collapsed.contains(section.id)
            heading(section, open: open).plainRow()
            if open {
                if section.sessions.isEmpty {
                    Text("None right now").font(.subheadline).foregroundStyle(.tertiary)
                        .padding(.leading, 26).padding(.vertical, 6).frame(maxWidth: .infinity, alignment: .leading)
                        .plainRow()
                        .accessibilityIdentifier("none-now")
                }
                ForEach(section.sessions, id: \.sessionID) { s in SessionListRow(session: s) }
            }
        }
        Group {
            if !model.hasWorld {
                InlineLoader(text: "Reading your sessions…")
            } else if !model.noOlder {
                InlineLoader(text: "Loading earlier sessions…")
                    .task(id: model.olderKey) { await model.loadOlder() }
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
        Button {
            withAnimation(.snappy) {
                if open { model.collapsed.insert(section.id) } else { model.collapsed.remove(section.id) }
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

    private func title(_ s: SessionSection) -> String {
        switch s.grouping {
        case .project: s.key
        case .agent: agentTitle(s.key)
        case .machine: model.machineName(s.key)
        default: SessionBucket(rawValue: s.key)?.title ?? s.key
        }
    }
}

/// A ring that turns while the menu shows, and rests while it does not.
struct BusyRing: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        if model.drawerOpen || model.menuInView {
            ProgressView().controlSize(.mini)
        } else {
            Circle().strokeBorder(.tertiary, lineWidth: 1.5).frame(width: 12, height: 12)
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
