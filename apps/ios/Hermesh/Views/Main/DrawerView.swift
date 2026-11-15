import HermeshHer
import HermeshSessions
import SwiftUI

/// The side menu, a map of the team (docs/her-team.md §4.5): search at the
/// top, Her and Work, then the machines with their coding agents and the
/// connected agents, and at the bottom the account, its picture alone. It
/// lists no session: Work does.
struct DrawerView: View {
    @Environment(AppModel.self) private var model
    /// The size of the menu's buttons: the system's toolbar buttons, and the
    /// smallest target the HIG asks for.
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
            ScrollView {
                VStack(alignment: .leading, spacing: 2) {
                    HStack {
                        if model.menuBeside {
                            // Where the page's button shows the menu again once it is hidden.
                            Button {
                                model.menuHidden = true
                            } label: {
                                Image(systemName: "sidebar.left").font(.body.weight(.medium)).frame(width: button, height: button)
                                    .glassEffect(.regular.interactive(), in: .circle)
                                    .contentShape(.circle)
                            }
                            .buttonStyle(.plain)
                            .accessibilityLabel("Hide the menu")
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
                    .padding(.leading, model.menuBeside ? 16 : 24).padding(.trailing, 16).padding(.bottom, 14)
                    places
                    lists
                }
                .padding(.top, 8)
                .padding(.bottom, 110)
            }
            .scrollIndicators(.hidden)
            bottomBar
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Color(.menuBackground))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("drawer")
    }

    private var places: some View {
        VStack(spacing: 2) {
            herPlace
            workPlace
        }
    }

    /// Her, first, as on the Mac: the Mac's robot, what she sent that is unread, and "Off" while she runs nowhere.
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
                    Text("\(unread)").font(.caption.weight(.semibold)).monospacedDigit().foregroundStyle(.white)
                        .padding(.horizontal, 6).frame(minWidth: 20, minHeight: 20)
                        .background(Color.accentColor, in: .capsule)
                }
            }
            .font(.body).frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 16).frame(height: 48)
        }
        .buttonStyle(RowStyle(selected: model.root == .her))
        .padding(.horizontal, 10)
        .accessibilityLabel(off ? Text("Her, not on") : unread > 0 ? Text("Her, \(unread) unread") : Text("Her"))
        .accessibilityIdentifier("her-place")
    }

    /// Work: marked while something needs the owner, with how many sessions run.
    private var workPlace: some View {
        let team = teamState(model.sessions, requests: model.pairings.count)
        return Button {
            model.open(.overview)
        } label: {
            HStack(spacing: 12) {
                Image(systemName: "square.grid.2x2").frame(width: 28)
                Text("Work")
                Spacer(minLength: 0)
                marks(waiting: !team.waiting.isEmpty || team.requests > 0, failed: !team.failed.isEmpty, running: team.running.count, counted: true)
            }
            .font(.body).frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 16).frame(height: 48)
        }
        .buttonStyle(RowStyle(selected: model.root == .overview && model.path.isEmpty))
        .padding(.horizontal, 10)
        .accessibilityIdentifier("work-place")
    }

    /// What a row's sessions are up to: a dot while one waits for the owner (orange) or failed (red), a ring
    /// while any runs, with their number where the row counts them.
    private func marks(waiting: Bool, failed: Bool, running: Int, counted: Bool = false) -> some View {
        HStack(spacing: 6) {
            if waiting || failed { Circle().fill(waiting ? .orange : .red).frame(width: 9, height: 9) }
            if running > 0 {
                // Turning only while the menu shows.
                if model.drawerOpen || model.menuInView {
                    ProgressView().controlSize(.mini)
                } else {
                    Circle().strokeBorder(.tertiary, lineWidth: 1.5).frame(width: 12, height: 12)
                }
                if counted { Text("\(running)").font(.footnote).monospacedDigit().foregroundStyle(.secondary) }
            }
        }
        .accessibilityHidden(true)
    }

    /// The team (docs/her-team.md §4.5): the machines, each with its coding agents, and the connected agents.
    /// A row opens its sessions; a heading opens the page that manages them.
    @ViewBuilder private var lists: some View {
        let sessions = model.sessions.filter { $0.status != .archived }
        let machines = model.machines.sorted { $0.machineName.localizedStandardCompare($1.machineName) == .orderedAscending }
        heading("Machines", opens: .machines)
        ForEach(machines, id: \.machineID) { m in
            machineRow(m)
            ForEach(sortedAgents(m.status.agents).installed, id: \.name) { a in
                agentRow(a.name, on: m, sessions: sessions.filter { $0.machineID == m.machineID && $0.agent == a.name })
            }
        }
        heading("Connected Agents", opens: .grants)
        ForEach(model.pairings.filter { $0.kind != .device && $0.kind != .machine }, id: \.code) { p in
            Button {
                model.drawerOpen = false
                model.sheet = .pairing(p.code)
            } label: {
                row { AgentLogo(id: p.label, size: 24) } title: { Text(PairingText.title(p)) } trailing: { Circle().fill(.orange).frame(width: 9, height: 9) }
            }
            .buttonStyle(RowStyle(selected: false))
            .padding(.horizontal, 10)
        }
        ForEach(model.clients.filter { !$0.revoked && $0.herMachineID.isEmpty }.sorted { $0.label.localizedStandardCompare($1.label) == .orderedAscending }, id: \.grantID) { c in
            let mine = sessions.filter { $0.createdBy == c.clientID }
            item(.grantSessions(c.clientID), id: "menu-agent-\(c.label)") {
                row { GrantAvatar(client: c, size: 24) } title: { Text(c.label) } trailing: { marks(mine) }
            }
        }
    }

    private func heading(_ title: LocalizedStringKey, opens root: Root) -> some View {
        Button {
            model.open(root)
        } label: {
            HStack(spacing: 4) {
                Text(title)
                Image(systemName: "chevron.right").font(.caption2.weight(.semibold)).foregroundStyle(.tertiary)
            }
            .font(.footnote).foregroundStyle(model.root == root ? .primary : .secondary)
            .padding(.horizontal, 26).padding(.top, 20).padding(.bottom, 6)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier(root == .machines ? "menu-machines" : "menu-grants")
    }

    private func marks(_ sessions: [Session]) -> some View {
        marks(waiting: sessions.contains { $0.status == .waitingInput }, failed: sessions.contains(where: \.isFailed), running: sessions.filter(\.isActive).count)
    }

    private func machineRow(_ m: Machine) -> some View {
        let note: String? = !m.online ? (m.hasLastSeen ? String(localized: "Offline · \(agoText(m.lastSeen.date, now: model.now))") : String(localized: "Offline"))
            : m.her.enabled ? String(localized: "Her is here") : nil
        return item(.machineSessions(m.machineID), id: "menu-machine-\(m.machineName)") {
            row {
                Image(systemName: machineIcon(m)).foregroundStyle(m.online ? .primary : .secondary)
            } title: {
                VStack(alignment: .leading, spacing: 1) {
                    Text(m.machineName).foregroundStyle(m.online ? .primary : .secondary)
                    if let note { Text(note).font(.caption).foregroundStyle(.secondary) }
                }
            } trailing: {
                if m.online { Circle().fill(.green).frame(width: 8, height: 8) }
            }
        }
    }

    private func agentRow(_ agent: String, on m: Machine, sessions: [Session]) -> some View {
        item(.agentSessions(m.machineID, agent), id: "menu-agent-\(m.machineName)-\(agent)") {
            row { AgentLogo(id: agent, size: 22) } title: { Text(agentTitle(agent)).font(.callout) } trailing: { marks(sessions) }
                .padding(.leading, 28)
        }
    }

    /// A row that opens a page of Work's.
    private func item(_ route: Route, id: String, @ViewBuilder label: () -> some View) -> some View {
        Button { model.open(underWork: route) } label: { label() }
            .buttonStyle(RowStyle(selected: model.root == .overview && model.path.first == route))
            .padding(.horizontal, 10)
            .accessibilityIdentifier(id)
    }

    /// A row's layout: its picture in the places' column, its name, and its marks at the end.
    private func row(@ViewBuilder icon: () -> some View, @ViewBuilder title: () -> some View, @ViewBuilder trailing: () -> some View) -> some View {
        HStack(spacing: 12) {
            icon().frame(width: 28)
            title().lineLimit(1).frame(maxWidth: .infinity, alignment: .leading)
            trailing()
        }
        .padding(.horizontal, 16).frame(minHeight: 44)
    }

    private var bottomBar: some View {
        HStack {
            Button {
                model.sheet = .account
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
private nonisolated struct HerRobot: Shape {
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
