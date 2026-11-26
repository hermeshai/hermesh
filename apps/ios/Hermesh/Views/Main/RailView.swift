import HermeshHer
import HermeshSessions
import SwiftUI

/// The menu's rail (docs/her-team.md §16, as on the Mac): in a column 74 points wide, where the menu stands beside
/// the page and the owner shrank it, or the screen has no room for more. Top to bottom: Expand, Her, the overview,
/// Search; then one logo for each coding agent with sessions at hand (waiting for the owner, failed, at work):
/// one session opens, more are a menu of them. The account at the foot. Each says to VoiceOver what it is and how
/// it stands, as the Mac's says on hover.
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
                    agents
                }
                .padding(.top, 8).padding(.bottom, 12)
                .frame(maxWidth: .infinity)
            }
            .scrollIndicators(.hidden)
            Button {
                model.accountPresented = true
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
        button(off ? Text("Her, not on") : unread > 0 ? Text("Her, \(unread) unread") : Text("Her"), id: "rail-her", current: model.root == .her && model.path.isEmpty) {
            model.open(.her)
        } icon: {
            HerRobot().stroke(style: StrokeStyle(lineWidth: 1.7, lineCap: .round, lineJoin: .round)).frame(width: 22, height: 22)
        } mark: {
            if off {
                Circle().strokeBorder(.tertiary, lineWidth: 1.5).frame(width: 10, height: 10)
            } else if unread > 0 {
                Circle().fill(Color.accentColor).frame(width: 9, height: 9)
            }
        }
        button(Text([String(localized: "Overview"), waits > 0 ? String(localized: "\(waits) need you") : "", work.running.isEmpty ? "" : String(localized: "\(work.running.count) working")].filter { !$0.isEmpty }.joined(separator: " · ")),
               id: "rail-overview", current: model.root == .overview && model.path.isEmpty) {
            model.open(.overview)
        } icon: {
            Image(systemName: "square.grid.2x2").shimmer(waits == 0 && !work.running.isEmpty)
        } mark: {
            if waits > 0 { Circle().fill(.orange).frame(width: 9, height: 9) }
        }
        button("Search", id: "rail-search") { model.sheet = .search } icon: { Image(systemName: "magnifyingglass") }
    }

    /// The coding agents at work, what waits first: one session opens; more are a menu of them, each with its mark.
    @ViewBuilder private var agents: some View {
        let groups = liveByAgent(teamState(model.sessions, requests: 0))
        if !groups.isEmpty { separator }
        ForEach(groups, id: \.agent) { g in
            let current = g.sessions.contains { $0.sessionID == model.chosenSession }
            if g.sessions.count == 1 {
                button(label(agentTitle(g.agent), g.sessions), id: "rail-agent-\(g.agent)", current: current) {
                    model.open(.session(g.sessions[0].sessionID))
                } icon: {
                    AgentLogo(id: g.agent, size: 24, running: atWork(g.sessions))
                } mark: {
                    marks(g.sessions)
                }
            } else {
                Menu {
                    Section(agentTitle(g.agent)) {
                        ForEach(g.sessions, id: \.sessionID) { s in
                            Button { model.open(.session(s.sessionID)) } label: {
                                Text(s.displayTitle)
                                Text(s.status == .waitingInput ? statusTitle(s.status) : s.isFailed ? String(localized: "Failed") : statusTitle(s.status))
                            }
                        }
                    }
                } label: {
                    tile(current: current) { AgentLogo(id: g.agent, size: 24, running: atWork(g.sessions)) } mark: { marks(g.sessions) }
                }
                .buttonStyle(.plain)
                .accessibilityLabel(label(agentTitle(g.agent), g.sessions))
                .accessibilityAddTraits(current ? .isSelected : [])
                .accessibilityIdentifier("rail-agent-\(g.agent)")
            }
        }
    }

    private var separator: some View {
        Rectangle().fill(.separator).frame(width: 28, height: 1).padding(.vertical, 6)
    }

    /// A name and how its sessions stand: "Codex · 1 waiting for you · 1 failed · 1 working".
    private func label(_ name: String, _ sessions: [Session]) -> Text {
        let waiting = sessions.filter { $0.status == .waitingInput }.count
        let failed = sessions.filter(\.isFailed).count
        let running = sessions.filter(\.isActive).count
        return Text([name, waiting > 0 ? String(localized: "\(waiting) waiting for you") : "", failed > 0 ? String(localized: "\(failed) failed") : "",
                     running > 0 ? String(localized: "\(running) working") : ""].filter { !$0.isEmpty }.joined(separator: " · "))
    }

    /// At the logo's corner: a dot while a session waits for the owner (orange) or failed (red).
    @ViewBuilder private func marks(_ sessions: [Session]) -> some View {
        if sessions.contains(where: { $0.status == .waitingInput }) {
            Circle().fill(.orange).frame(width: 9, height: 9)
        } else if sessions.contains(where: \.isFailed) {
            Circle().fill(.red).frame(width: 9, height: 9)
        }
    }

    /// Without a dot, while any runs: a brand-blue light orbits the logo.
    private func atWork(_ sessions: [Session]) -> Bool {
        !sessions.contains { $0.status == .waitingInput || $0.isFailed } && sessions.contains(where: \.isActive)
    }

    private func button(_ label: LocalizedStringKey, id: String, action: @escaping () -> Void, @ViewBuilder icon: () -> some View) -> some View {
        button(Text(label), id: id, action: action, icon: icon) { EmptyView() }
    }

    /// A rail button: 48 points across, its mark at the top trailing corner; the current place has the menu's
    /// fill and an accent bar at the rail's leading edge.
    private func button(_ label: Text, id: String, current: Bool = false, action: @escaping () -> Void, @ViewBuilder icon: () -> some View, @ViewBuilder mark: () -> some View) -> some View {
        Button(action: action) { tile(current: current, icon: icon, mark: mark) }
            .buttonStyle(.plain)
            .accessibilityLabel(label)
            .accessibilityAddTraits(current ? .isSelected : [])
            .accessibilityIdentifier(id)
    }

    private func tile(current: Bool, @ViewBuilder icon: () -> some View, @ViewBuilder mark: () -> some View) -> some View {
        icon().font(.body.weight(.medium)).frame(width: 48, height: 48)
            .background(current ? AnyShapeStyle(.fill.secondary) : AnyShapeStyle(.clear), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .overlay(alignment: .topTrailing) { mark().offset(x: 2, y: -2).accessibilityHidden(true) }
            .frame(maxWidth: .infinity, minHeight: 44)
            .overlay(alignment: .leading) {
                if current { Capsule().fill(Color.accentColor).frame(width: 3, height: 20) }
            }
            .contentShape(.rect)
    }
}
