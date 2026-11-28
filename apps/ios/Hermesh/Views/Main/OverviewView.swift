import HermeshHer
import HermeshProto
import HermeshSessions
import SwiftUI
import UserNotifications

/// The overview (docs/overview-usage.md §4), in three views picked at its
/// top, under its title (the bar's middle is the line that says how the
/// connection stands). Live is only now: what needs the owner, as a deck
/// with the most pressing in front; the link map of who may use which
/// environment, with a line for each session that has not finished; then
/// what runs, as live tiles, and the sessions that ended last. A node of the
/// map picked shows the sessions through it instead. All of it is what the
/// device already holds: Live asks for no counts. Usage (what was used over
/// a range, under its filters) and Limits (what is left of each coding
/// agent's account) are the numbers.
struct OverviewView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var phase
    @AppStorage("hideSecondDevice") private var hideSecondDevice = false
    @AppStorage(Guide.key) private var guide = ""
    @State private var notificationsOff = false
    /// The node of the map picked.
    @State private var picked: String?
    /// The card in front of the deck, and where it was among the others.
    @State private var front: String?
    @State private var frontIndex = 0
    /// Moves when the card in front changes: the map shows where it is.
    @State private var spot = 0
    @State private var onScreen = true

    private var liveAwake: Bool {
        onScreen && phase == .active && model.path.isEmpty && model.sheet == nil && !model.accountPresented && (!model.drawerOpen || model.menuBeside)
    }

    var body: some View {
        let team = teamState(model.sessions, requests: 0)
        let step = guide == Guide.later ? GuideStep.next(model) : nil
        let limitCards = agentLimits(model.machines.map { (machineID: $0.machineID, agents: $0.status.agents) }, now: model.now)
        let hasUnseenLow = hasUnseenLowLimits(limitCards, seen: model.stats.seenLowLimits, now: model.now)
        let showLimitDot = model.stats.segment != .limits && hasUnseenLow
        let list = needs(team: team, pairings: model.pairings.map(\.code), expiring: model.expiringClients.map(\.grantID), guide: step != nil,
                         herOff: herEnvironments(model.machines).isEmpty, notificationsOff: notificationsOff, secondDevice: onlyDevice)
        let map = linkMap(senders: model.clients.filter { !$0.revoked }.map { MapSender(clientID: $0.clientID, title: model.senderName($0.clientID)) },
                          environments: model.machines.map { m in
                              MapEnvironment(machineID: m.machineID, title: m.machineName, local: m.local, online: m.online || m.local,
                                             agents: m.status.agents.filter(\.available).map(\.name))
                          },
                          team: team, owner: String(localized: "You"))
        let about = about(list.first { $0.id == front } ?? list.first, team: team)
        @Bindable var stats = model.stats
        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                Picker("Overview", selection: $stats.segment.animation(.smooth)) {
                    Text("Live").tag(StatsModel.Segment.live)
                    Text("Usage").tag(StatsModel.Segment.usage)
                    Text("Limits").tag(StatsModel.Segment.limits)
                }
                .pickerStyle(.segmented)
                .accessibilityIdentifier("overview-views")
                .overlay {
                    if showLimitDot {
                        HStack(spacing: 0) {
                            Color.clear.frame(maxWidth: .infinity)
                            Color.clear.frame(maxWidth: .infinity)
                            HStack(spacing: 4) {
                                Text("Limits").font(.subheadline.weight(.medium)).hidden()
                                Circle()
                                    .fill(.orange)
                                    .frame(width: 7, height: 7)
                                    .accessibilityElement()
                                    .accessibilityLabel(Text("Limits"))
                                    .accessibilityIdentifier("limits-low-dot")
                            }
                            .offset(x: 5.5)
                            .frame(maxWidth: .infinity)
                        }
                        .allowsHitTesting(false)
                    }
                }
                .padding(.horizontal, 16)
                switch stats.segment {
                case .usage: UsagePage().padding(.horizontal, 16)
                case .limits: LimitsPage().padding(.horizontal, 16)
                case .live: live(list: list, team: team, step: step, map: map, about: about)
                }
            }
            .padding(.top, 8).padding(.bottom, 40)
        }
        .background(Color(.systemGroupedBackground))
        .navigationTitle("Overview")
        .navigationBarTitleDisplayMode(.inline)
        .rootToolbar()
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) { NewMenu() }
        }
        // Usage asks only while it shows, again for each state, and when the counts move (AppModel.workChanged).
        .task(id: "\(dayKey(model.now)) \(stats.segment) \(stats.period) \(stats.filter) \(stats.group)") {
            stats.shown = stats.segment == .usage
            if stats.shown { await stats.load(model.api, now: Date()) }
        }
        .onAppear { onScreen = true }
        .onDisappear { onScreen = false; stats.shown = false }
        .refreshable { if stats.segment == .usage { await stats.load(model.api, now: Date()) } }
        .task(id: phase) {
            guard phase == .active, !model.isDemo else { return }
            notificationsOff = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus == .denied
        }
        // The deck keeps its place as what needs the owner comes and goes.
        // What just came to wait on the owner goes in front.
        .onChange(of: list.map(\.id), initial: true) { old, ids in
            if old != ids, let new = arrived(list, before: old) {
                front = new.id
                return
            }
            let at = deckFront(list, was: front, at: frontIndex)
            frontIndex = at
            let now = ids.indices.contains(at) ? ids[at] : nil
            if now != front { front = now }
        }
        .onChange(of: front) { old, new in
            if let i = list.firstIndex(where: { $0.id == new }) { frontIndex = i }
            if old != nil, new != nil { spot += 1 }
        }
        // A node that is gone is not picked.
        .onChange(of: (map.senders + map.environments).map(\.id)) { _, ids in
            if let picked, !ids.contains(picked) { self.picked = nil }
        }
        // The guide's steps were done elsewhere (the Mac, the menus): it is gone for good.
        .onChange(of: guide == Guide.later && step == nil, initial: true) { _, over in
            if over { guide = Guide.done }
        }
        // Tapping Limits (or receiving a new low limit while already viewing it) clears the dot until the window resets.
        .onChange(of: stats.segment == .limits && hasUnseenLow, initial: true) { _, mark in
            if mark { stats.markLowLimitsSeen(limitCards, now: model.now) }
        }
    }

    /// Live: what needs the owner, the map, what runs and what finished (or, a node picked, what goes through it).
    @ViewBuilder private func live(list: [Need], team: TeamState, step: GuideStep?, map: LinkMap, about: (nodes: Set<String>, line: String?)) -> some View {
        UnreachableNotice()
        if model.hasWorld {
            if model.isDemo { demo }
            if !list.isEmpty { NeedsDeck(needs: list, team: team, step: step, front: $front) }
            LinkMapView(map: map, lit: picked.map { map.touched(by: $0, sessions: team.waiting + team.failed + team.running + team.finished) },
                        mine: about.nodes, mineLine: about.line, spot: spot, awake: liveAwake, picked: $picked)
                .padding(.horizontal, 16)
            if let picked, let node = (map.senders + map.environments).first(where: { $0.id == picked }) {
                through(node, team: team)
            } else {
                RunningTiles(sessions: team.running, paused: !liveAwake)
                finished(team)
                if team.running.isEmpty && team.finished.isEmpty && team.waiting.isEmpty && team.failed.isEmpty && !model.machines.isEmpty { first }
            }
        } else { // nothing kept yet: the first launch, or after joining
            PageLoader(text: "Reading your machines…")
        }
    }

    /// Once the first machine joins, while this phone is the only device that
    /// can approve.
    private var onlyDevice: Bool {
        !model.isDemo && !hideSecondDevice && !model.machines.isEmpty && model.devices.count == 1 && model.devices[0].thisDevice
    }

    /// The nodes of the map a need is about, and its session's line.
    private func about(_ need: Need?, team: TeamState) -> (nodes: Set<String>, line: String?) {
        switch need {
        case .question(let id), .failed(let id):
            guard let s = (team.waiting + team.failed).first(where: { $0.sessionID == id }) else { return ([], nil) }
            let a = LinkMap.sender(s.createdBy), b = LinkMap.environment(s.machineID)
            return ([a, b], LinkMap.line(sender: a, environment: b))
        case .expiring(let id):
            return (Set(model.clients.filter { $0.grantID == id }.map { LinkMap.sender($0.clientID) }), nil)
        default:
            return ([], nil)
        }
    }

    private var demo: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Explore Hermesh").font(.title2.bold())
            Text("Try a task, answer the agent’s question, then inspect its changes. Machines, approvals and results here are simulated.").foregroundStyle(.secondary)
            Button("Run Demo Task", systemImage: "play.fill") { model.runDemoTask() }
                .buttonStyle(.borderedProminent).accessibilityIdentifier("run-demo-task")
        }
        .padding(.horizontal, 20)
    }

    /// The sessions that ended last, and the way to all of them.
    @ViewBuilder private func finished(_ team: TeamState) -> some View {
        if !team.finished.isEmpty {
            VStack(alignment: .leading, spacing: 8) {
                OverviewHeading(title: Text("Recently Finished"))
                OverviewCard {
                    ForEach(Array(team.finished.prefix(3).enumerated()), id: \.element.sessionID) { i, s in
                        if i > 0 { Divider().padding(.leading, 16) }
                        Button { model.openSession(s.sessionID) } label: { SessionRow(session: s) }.buttonStyle(.plain)
                    }
                    Divider()
                    Button { model.path.append(.tasks) } label: {
                        HStack {
                            Text("See All").foregroundStyle(.tint)
                            Spacer()
                            Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(.tertiary)
                        }
                        .padding(.horizontal, 16).frame(minHeight: 44).contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("see-all")
                }
            }
            .padding(.horizontal, 16)
        }
    }

    /// Every session through the node picked, in place of what runs and what finished.
    private func through(_ node: LinkMap.Node, team: TeamState) -> some View {
        let list = sessions(through: node.id, team: team)
        return VStack(alignment: .leading, spacing: 8) {
            OverviewHeading(title: Text("Sessions through \(node.title)")) {
                Button("Show All") { withAnimation(.smooth) { picked = nil } }.font(.footnote).accessibilityIdentifier("show-all")
            }
            OverviewCard {
                if list.isEmpty {
                    Text("No sessions yet").foregroundStyle(.secondary)
                        .padding(.horizontal, 16).frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                }
                ForEach(Array(list.enumerated()), id: \.element.sessionID) { i, s in
                    if i > 0 { Divider().padding(.leading, 16) }
                    Button { model.openSession(s.sessionID) } label: { SessionRow(session: s, doing: true) }.buttonStyle(.plain)
                }
            }
        }
        .padding(.horizontal, 16)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("through")
    }

    /// No session yet, from anyone: the way to start one, where the first tile will be.
    private var first: some View {
        Button { model.open(.newSession("", "")) } label: {
            VStack(alignment: .leading, spacing: 3) {
                Label("New Session", systemImage: "plus").font(.callout.weight(.semibold)).foregroundStyle(.tint)
                Text("Sessions from Her, a connected agent or you all show here.").font(.footnote).foregroundStyle(.secondary)
                    .multilineTextAlignment(.leading)
            }
            .padding(.horizontal, 18).padding(.vertical, 16)
            .frame(maxWidth: .infinity, alignment: .leading)
            .overlay { RoundedRectangle(cornerRadius: 26, style: .continuous).strokeBorder(Color(.tertiaryLabel), style: StrokeStyle(lineWidth: 1, dash: [4, 4])) }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .padding(.horizontal, 16)
        .accessibilityIdentifier("first-session")
    }
}

/// A section's name over it, and what stands at its end.
struct OverviewHeading<Trailing: View>: View {
    let title: Text
    @ViewBuilder var trailing: Trailing

    var body: some View {
        HStack {
            title.fontWeight(.semibold).foregroundStyle(.secondary)
            Spacer()
            trailing
        }
        .font(.footnote)
        .padding(.horizontal, 4)
    }
}

extension OverviewHeading where Trailing == EmptyView {
    init(title: Text) {
        self.init(title: title) { EmptyView() }
    }
}

/// The ground of a group of rows.
struct OverviewCard<Content: View>: View {
    @ViewBuilder var content: Content

    var body: some View {
        VStack(spacing: 0) { content }
            .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
    }
}

/// What runs, as tiles that show it: the time the turn has taken, counted
/// by the system, the tool in use, and a bar that moves. One session takes
/// the row.
struct RunningTiles: View {
    @Environment(AppModel.self) private var model
    let sessions: [Session]
    var paused = false

    var body: some View {
        if !sessions.isEmpty {
            VStack(alignment: .leading, spacing: 8) {
                OverviewHeading(title: Text("In Progress")) { Text(verbatim: "\(sessions.count)").foregroundStyle(.secondary).monospacedDigit() }
                LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 10), count: sessions.count == 1 ? 1 : 2), spacing: 10) {
                    ForEach(sessions, id: \.sessionID) { s in
                        Button { model.openSession(s.sessionID) } label: { tile(s, alone: sessions.count == 1) }.buttonStyle(.plain)
                    }
                }
            }
            .padding(.horizontal, 16)
            .id(TeamLists.inProgress)
        }
    }

    private func tile(_ s: Session, alone: Bool) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                AgentLogo(id: s.agent, size: 22, paused: paused)
                Spacer()
                if let start = s.turnStartedDate { Text(start, style: .timer).font(.footnote).foregroundStyle(.secondary).monospacedDigit() }
            }
            ShimmerText(s.displayTitle, paused: paused).font(.subheadline.weight(.semibold)).lineLimit(alone ? 1 : 2)
                .multilineTextAlignment(.leading).padding(.top, 10)
            Text(model.senderLine(s)).font(.caption).foregroundStyle(.secondary).lineLimit(1).padding(.top, 3)
            if !s.progress.currentTool.isEmpty {
                Text(s.progress.currentTool).font(.caption2.monospaced()).foregroundStyle(.secondary).lineLimit(1).padding(.top, 6)
            }
            Spacer(minLength: 12)
            ActivityBar(paused: paused)
        }
        .padding(.horizontal, 14).padding(.top, 12).padding(.bottom, 14)
        .frame(maxWidth: .infinity, minHeight: alone ? nil : 146, alignment: .topLeading)
        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("session-\(s.displayTitle)")
    }
}

/// A bar that says something is at work: a run of it going across, moved by
/// the render server (an offset, animated once).
private struct ActivityBar: View {
    var paused = false
    @Environment(\.accessibilityReduceMotion) private var still
    @State private var across = false

    var body: some View {
        GeometryReader { geo in
            Capsule().fill(Color.live)
                .frame(width: geo.size.width * 0.38)
                .offset(x: (still || paused) ? geo.size.width * 0.31 : across ? geo.size.width : -geo.size.width * 0.38)
        }
        .frame(height: 3)
        .background(Color(.tertiarySystemFill), in: Capsule())
        .clipShape(Capsule())
        .onAppear {
            guard !still, !paused else { return }
            withAnimation(.easeInOut(duration: 1.7).repeatForever(autoreverses: false)) { across = true }
        }
        .onChange(of: paused) { _, isPaused in
            guard !still else { return }
            if isPaused {
                var tx = Transaction()
                tx.disablesAnimations = true
                withTransaction(tx) { across = false }
            } else {
                across = false
                withAnimation(.easeInOut(duration: 1.7).repeatForever(autoreverses: false)) { across = true }
            }
        }
    }
}

extension Color {
    /// What is at work on the overview: the map's lines and dots, a tile's bar.
    static let live = Color(UIColor.live)
}

extension UIColor {
    static let live = UIColor { $0.userInterfaceStyle == .dark ? UIColor(red: 0.655, green: 0.694, blue: 0.78, alpha: 1) : UIColor(red: 0.373, green: 0.42, blue: 0.522, alpha: 1) }
}

/// Every session, the newest day first: where See All under Recently
/// Finished leads. A second view of the menu's list, not a second store:
/// older ones load as its end comes into view.
struct TasksView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let sections = sessionSections(model.sessions, view: SessionListLayout(group: .date), archived: false, now: model.now, pinnedOrder: model.pinnedOrder, activeOrder: model.activeOrder)
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 8) {
                ForEach(sections) { section in
                    OverviewHeading(title: Text(verbatim: SessionBucket(rawValue: section.key)?.title ?? section.key)).padding(.top, 14)
                    OverviewCard {
                        ForEach(Array(section.sessions.enumerated()), id: \.element.sessionID) { i, s in
                            if i > 0 { Divider().padding(.leading, 16) }
                            Button { model.openSession(s.sessionID) } label: { SessionRow(session: s, doing: true) }.buttonStyle(.plain)
                        }
                    }
                }
                Group {
                    if !model.hasWorld {
                        InlineLoader(text: "Reading your sessions…")
                    } else if !model.noOlder {
                        InlineLoader(text: "Loading earlier sessions…").task(id: model.olderKey) { await model.loadOlder() }
                    } else if sections.isEmpty {
                        Text("No sessions yet").foregroundStyle(.secondary)
                    }
                }
                .font(.subheadline).frame(maxWidth: .infinity).padding(.top, 16)
            }
            .padding(.horizontal, 16).padding(.bottom, 40)
        }
        .background(Color(.systemGroupedBackground))
        .navigationTitle("Sessions")
        .navigationBarTitleDisplayMode(.inline)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("tasks")
    }
}
