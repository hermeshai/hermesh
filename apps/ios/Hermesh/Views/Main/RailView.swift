import HermeshHer
import HermeshSessions
import SwiftUI

/// The menu's rail (docs/her-team.md §14.9, as on the Mac): the menu's places as icons, in a column 74 points
/// wide, where the menu stands beside the page and the owner shrank it, or the screen has no room for more. Top
/// to bottom: Expand, Her, Work, Search; All Sessions and the machines, each with its coding agents under it; the
/// connected agents; the account. Each says to VoiceOver what it is and how it stands, as the Mac's says on hover.
struct RailView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        VStack(spacing: 0) {
            ScrollView {
                VStack(spacing: 6) {
                    button("Expand the menu", id: "rail-expand") { model.drawerOpen = true } icon: {
                        Image(systemName: "sidebar.left")
                    }
                    .padding(.bottom, 6)
                    places
                    team
                }
                .padding(.top, 8).padding(.bottom, 12)
                .frame(maxWidth: .infinity)
            }
            .scrollIndicators(.hidden)
            Button {
                model.sheet = .account
            } label: {
                AccountPicture(status: model.status).frame(width: 44, height: 44)
                    .padding(4)
                    .glassEffect(.regular.interactive(), in: .circle)
                    .contentShape(.circle)
            }
            .buttonStyle(.plain)
            .padding(.bottom, 8)
            .accessibilityLabel("Account and devices")
            .accessibilityIdentifier("rail-account")
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Color(.menuBackground))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("rail")
    }

    @ViewBuilder private var places: some View {
        let unread = herUnread(model.machines)
        let off = herEnvironments(model.machines).isEmpty
        let work = teamState(model.sessions, requests: model.pairings.count)
        let waits = work.waiting.count + work.requests
        button(off ? Text("Her, not on") : unread > 0 ? Text("Her, \(unread) unread") : Text("Her"), id: "rail-her", current: model.root == .her) {
            model.open(.her)
        } icon: {
            HerRobot().stroke(style: StrokeStyle(lineWidth: 1.7, lineCap: .round, lineJoin: .round)).frame(width: 22, height: 22)
        } mark: {
            if off {
                Circle().strokeBorder(.tertiary, lineWidth: 1.5).frame(width: 10, height: 10)
            } else if unread > 0 {
                Text("\(unread)").font(.caption2.weight(.semibold)).monospacedDigit().foregroundStyle(.white)
                    .padding(.horizontal, 4).frame(minWidth: 16, minHeight: 16)
                    .background(Color.accentColor, in: .capsule)
            }
        }
        button(Text([String(localized: "Work"), waits > 0 ? String(localized: "\(waits) need you") : "", work.running.isEmpty ? "" : String(localized: "\(work.running.count) working")].filter { !$0.isEmpty }.joined(separator: " · ")),
               id: "rail-work", current: model.root == .overview && model.path.isEmpty) {
            model.open(.overview)
        } icon: {
            Image(systemName: "square.grid.2x2")
        } mark: {
            marks(waiting: waits > 0, failed: !work.failed.isEmpty, running: work.running.count)
        }
        button("Search", id: "rail-search") { model.sheet = .search } icon: { Image(systemName: "magnifyingglass") }
    }

    /// The team (docs/her-team.md §4.5), as the menu lists it.
    @ViewBuilder private var team: some View {
        let sessions = model.sessions.filter { $0.status != .archived }
        let machines = model.machines.sorted { $0.machineName.localizedStandardCompare($1.machineName) == .orderedAscending }
        let clients = model.clients.filter { !$0.revoked && $0.herMachineID.isEmpty }.sorted { $0.label.localizedStandardCompare($1.label) == .orderedAscending }
        let pairings = model.pairings.filter { $0.kind != .device && $0.kind != .machine }
        separator
        route(.sessions, Text("All Sessions"), id: "rail-sessions") { Image(systemName: "list.bullet") }
        ForEach(machines, id: \.machineID) { m in
            route(.machineSessions(m.machineID), Text(verbatim: "\(m.machineName), \(m.online ? (m.her.enabled ? String(localized: "Her is here") : String(localized: "Online")) : String(localized: "Offline"))"), id: "rail-machine-\(m.machineName)") {
                Image(systemName: machineIcon(m)).foregroundStyle(m.online ? .primary : .secondary)
            } mark: {
                if m.online { Circle().fill(.green).frame(width: 8, height: 8) }
            }
            ForEach(sortedAgents(m.status.agents).installed, id: \.name) { a in
                let mine = sessions.filter { $0.machineID == m.machineID && $0.agent == a.name }
                route(.agentSessions(m.machineID, a.name), label(String(localized: "\(agentTitle(a.name)) on \(m.machineName)"), mine), id: "rail-agent-\(m.machineName)-\(a.name)", small: true) {
                    AgentLogo(id: a.name, size: 22)
                } mark: {
                    marks(mine)
                }
            }
        }
        if !clients.isEmpty || !pairings.isEmpty { separator }
        ForEach(pairings, id: \.code) { p in
            button(Text(PairingText.title(p)), id: "rail-pairing") { model.sheet = .pairing(p.code) } icon: {
                AgentLogo(id: p.label, size: 24)
            } mark: {
                Circle().fill(.orange).frame(width: 9, height: 9)
            }
        }
        ForEach(clients, id: \.grantID) { c in
            let mine = sessions.filter { $0.createdBy == c.clientID }
            route(.grantSessions(c.clientID), label(c.label, mine), id: "rail-agent-\(c.label)") {
                GrantAvatar(client: c, size: 24)
            } mark: {
                marks(mine)
            }
        }
    }

    private var separator: some View {
        Rectangle().fill(.separator).frame(width: 28, height: 1).padding(.vertical, 6)
    }

    /// A name and how its sessions stand: "Codex on Mac mini · 1 waiting for you · 1 working".
    private func label(_ name: String, _ sessions: [Session]) -> Text {
        let waiting = sessions.filter { $0.status == .waitingInput }.count
        let running = sessions.filter(\.isActive).count
        return Text([name, waiting > 0 ? String(localized: "\(waiting) waiting for you") : "", running > 0 ? String(localized: "\(running) working") : ""].filter { !$0.isEmpty }.joined(separator: " · "))
    }

    private func marks(_ sessions: [Session]) -> some View {
        marks(waiting: sessions.contains { $0.status == .waitingInput }, failed: sessions.contains(where: \.isFailed), running: sessions.filter(\.isActive).count)
    }

    /// At the icon's corner: a dot while a session waits for the owner (orange) or failed (red), else a ring while any runs.
    @ViewBuilder private func marks(waiting: Bool, failed: Bool, running: Int) -> some View {
        if waiting || failed {
            Circle().fill(waiting ? .orange : .red).frame(width: 9, height: 9)
        } else if running > 0 {
            ProgressView().controlSize(.mini)
        }
    }

    /// A place that opens a page of Work's, current while that page shows.
    private func route(_ route: Route, _ label: Text, id: String, small: Bool = false, @ViewBuilder icon: () -> some View, @ViewBuilder mark: () -> some View = { EmptyView() }) -> some View {
        button(label, id: id, current: model.shows(route), small: small) { model.open(underWork: route) } icon: { icon() } mark: { mark() }
    }

    private func button(_ label: LocalizedStringKey, id: String, action: @escaping () -> Void, @ViewBuilder icon: () -> some View) -> some View {
        button(Text(label), id: id, action: action, icon: icon) { EmptyView() }
    }

    /// A rail button: 48 points across (40 for an agent under its machine), its mark at the top trailing corner;
    /// the current place has the menu's fill and an accent bar at the rail's leading edge.
    private func button(_ label: Text, id: String, current: Bool = false, small: Bool = false, action: @escaping () -> Void, @ViewBuilder icon: () -> some View, @ViewBuilder mark: () -> some View) -> some View {
        let side: CGFloat = small ? 40 : 48
        return Button(action: action) {
            icon().font(.body.weight(.medium)).frame(width: side, height: side)
                .background(current ? AnyShapeStyle(.fill.secondary) : AnyShapeStyle(.clear), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                .overlay(alignment: .topTrailing) { mark().offset(x: 2, y: -2).accessibilityHidden(true) }
                .frame(maxWidth: .infinity, minHeight: 44)
                .overlay(alignment: .leading) {
                    if current { Capsule().fill(Color.accentColor).frame(width: 3, height: 20) }
                }
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(label)
        .accessibilityAddTraits(current ? .isSelected : [])
        .accessibilityIdentifier(id)
    }
}
