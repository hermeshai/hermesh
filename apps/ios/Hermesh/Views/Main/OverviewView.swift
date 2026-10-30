import HermeshProto
import HermeshSessions
import SwiftUI
import UserNotifications

/// What a pairing request is, in a line.
enum PairingText {
    static func title(_ p: PairingDetails) -> String {
        switch p.kind {
        case .device: String(localized: "“\(p.label)” wants to become your trusted device")
        case .machine: String(localized: "A machine calling itself “\(p.label)” wants to join")
        default: String(localized: "“\(p.label)” wants to connect to your computer")
        }
    }
}

/// The first page: what waits for the owner beyond the sessions (requests
/// to connect, grants that end) and how many sessions wait (the menu lists
/// them); then the work: what runs, today's sessions hour by hour, who sent
/// work to which coding agent, each coding agent's day and the machines.
struct OverviewView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var phase
    @State private var everyAgent = false
    @State private var picked: FlowPick?
    @State private var notificationsOff = false
    @AppStorage("hideSecondDevice") private var hideSecondDevice = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 26) {
                UnreachableNotice()
                if model.hasWorld {
                    GetStarted()
                    todo
                    working
                } else { // nothing kept yet: the first launch, or after joining
                    PageLoader(text: "Reading your machines…")
                }
            }
            .padding(.bottom, 40)
        }
        .background(Color(.systemGroupedBackground))
        .navigationTitle("Overview")
        .rootToolbar()
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) { NewMenu() }
        }
        .task(id: dayKey(model.now)) { await model.loadActivity() }
        .onAppear { model.overviewShown = true }
        .onDisappear { model.overviewShown = false }
        .refreshable { await model.loadActivity() }
        .task(id: phase) {
            guard phase == .active else { return }
            notificationsOff = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus == .denied
        }
    }

    private enum Todo: Identifiable {
        case pairing(PairingDetails)
        case expiring(Client)
        case sessions(Int)
        case notificationsOff
        case secondDevice

        var id: String {
            switch self {
            case .pairing(let p): "p" + p.code
            case .expiring(let c): "g" + c.grantID
            case .sessions: "s"
            case .notificationsOff: "n"
            case .secondDevice: "d"
            }
        }
    }

    /// Once the first machine joins, while this phone is the only device that
    /// can approve.
    private var onlyDevice: Bool {
        !hideSecondDevice && !model.machines.isEmpty && model.devices.count == 1 && model.devices[0].thisDevice
    }

    @ViewBuilder private var todo: some View {
        let waiting = model.sessions.filter(\.needsAttention).count
        let items = model.pairings.map(Todo.pairing) + model.expiringClients.map(Todo.expiring) + (waiting > 0 ? [.sessions(waiting)] : [])
            + (notificationsOff ? [.notificationsOff] : []) + (onlyDevice ? [.secondDevice] : [])
        if !items.isEmpty {
            Section {
                card {
                    ForEach(Array(items.enumerated()), id: \.element.id) { i, item in
                        if i > 0 { Divider().padding(.leading, 56) }
                        switch item {
                        case .pairing(let p): pairingRow(p)
                        case .expiring(let c): expiringRow(c)
                        case .sessions(let n):
                            row(open: { model.drawerOpen = true }) {
                                Circle().fill(.orange).frame(width: 9, height: 9).frame(width: 28)
                                Text("\(n) sessions need you").frame(maxWidth: .infinity, alignment: .leading)
                            }
                            .accessibilityIdentifier("sessions-waiting")
                        case .notificationsOff: notificationsRow
                        case .secondDevice: secondDeviceRow
                        }
                    }
                }
                .padding(.horizontal, 16)
            } header: {
                SectionTitle(title: "To Do")
            }
        }
    }

    private func pairingRow(_ p: PairingDetails) -> some View {
        HStack(spacing: 12) {
            AgentLogo(id: p.label, size: 28)
            VStack(alignment: .leading, spacing: 2) {
                Text(PairingText.title(p))
                Text("Verification code \(Text(p.verificationCode).monospaced())").font(.footnote).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Button("Verify") { model.sheet = .pairing(p.code) }
                .buttonStyle(.glassProminent)
                .accessibilityIdentifier("verify-pairing")
        }
        .padding(.horizontal, 16).padding(.vertical, 11)
    }

    private var notificationsRow: some View {
        HStack(spacing: 12) {
            Image(systemName: "bell.slash").foregroundStyle(.orange).frame(width: 28)
            VStack(alignment: .leading, spacing: 2) {
                Text("Notifications are off")
                Text("You’ll see requests and questions only while Hermesh is open.").font(.footnote).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Button("Turn On…") {
                if let url = URL(string: UIApplication.openNotificationSettingsURLString) { UIApplication.shared.open(url) }
            }
            .buttonStyle(.glass)
        }
        .padding(.horizontal, 16).padding(.vertical, 11)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("notifications-off")
    }

    private var secondDeviceRow: some View {
        HStack(spacing: 12) {
            Image(systemName: "iphone.gen3").foregroundStyle(.secondary).frame(width: 28)
            Text("Only this iPhone can approve. If you lose it, you’ll set up again and pair your machines and agents again.")
                .font(.subheadline).frame(maxWidth: .infinity, alignment: .leading)
            VStack(spacing: 6) {
                Button("Add Another…") { model.sheet = .addDevice }.buttonStyle(.glass)
                Button("Hide") { hideSecondDevice = true }.font(.footnote)
            }
        }
        .padding(.horizontal, 16).padding(.vertical, 11)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("second-device")
    }

    private func expiringRow(_ c: Client) -> some View {
        let days = c.daysLeft(now: model.now)
        return HStack(spacing: 12) {
            GrantAvatar(client: c)
            VStack(alignment: .leading, spacing: 2) {
                Text(days == 0 ? String(localized: "“\(c.label)”’s grant ends today") : String(localized: "“\(c.label)”’s grant ends in \(days) days"))
                Text("Then it can no longer use your machines").font(.footnote).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Button("Renew…") { model.sheet = .renew(c.grantID) }.buttonStyle(.glass)
        }
        .padding(.horizontal, 16).padding(.vertical, 11)
    }

    /// What runs and today's sessions hour by hour; then who sent the day's
    /// work to which coding agent, each coding agent's day (those that worked
    /// first, the others folded), the machines, and the way to the sessions:
    /// the menu.
    private var working: some View {
        let running = model.sessions.filter(\.isActive)
        let hour = Calendar.current.component(.hour, from: model.now)
        let today = model.activity?.date == dayKey(model.now) ? model.activity : nil
        let flow = WorkFlow(today?.pairs ?? [])
        let agents = codingAgents(running: running, today: today)
        let runs = { (a: String) in running.filter { $0.agent == a }.count }
        let turns = { (a: String) in today?.turns(agent: a) ?? 0 }
        let worked = { (a: String) in today?.sessions(agent: a) ?? 0 }
        let busy = agents.filter { runs($0) > 0 || worked($0) > 0 }.sorted { (runs($0), turns($0), worked($0)) > (runs($1), turns($1), worked($1)) }
        let rest = agents.filter { runs($0) == 0 && worked($0) == 0 }
        let shown = everyAgent ? busy + rest : busy
        let idleClients = model.clients.filter { c in
            !c.revoked && !(today?.pairs.contains { $0.createdBy == c.clientID } ?? false) && !running.contains { $0.createdBy == c.clientID }
        }
        return Section {
            VStack(alignment: .leading, spacing: 10) {
                DayCard(running: running.count, today: today, hour: hour)
                if let today, !flow.bands.isEmpty {
                    caption("Where Today’s Work Came From and Went", detail: "\(today.turns) turns · \(tokensText(today.tokens)) tokens")
                    FlowCard(flow: flow, picked: $picked, clientName: { model.clientLabel($0) }, idle: idleClients.map { AgentLogo.name($0.label) })
                }
                if !agents.isEmpty {
                    caption("Agents", detail: "Sessions working each hour")
                    card {
                        ForEach(Array(shown.enumerated()), id: \.element) { i, a in
                            if i > 0 { Divider().padding(.leading, 50) }
                            agentRow(a, running: runs(a), turns: turns(a), flow: flow, today: today, hours: today?.byAgent[a], hour: hour)
                        }
                        if !everyAgent && !rest.isEmpty {
                            if !shown.isEmpty { Divider().padding(.leading, 50) }
                            Button {
                                withAnimation(.smooth) { everyAgent = true }
                            } label: {
                                Text("\(rest.count) more agents haven’t worked today")
                                    .font(.subheadline).foregroundStyle(.secondary)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                                    .padding(.leading, 50).padding(.trailing, 16).padding(.vertical, 12)
                                    .contentShape(.rect)
                            }
                            .buttonStyle(.plain)
                            .accessibilityIdentifier("more-agents")
                        }
                    }
                }
                if !model.machines.isEmpty {
                    caption("Machines")
                    card {
                        ForEach(Array(model.machines.enumerated()), id: \.element.machineID) { i, m in
                            if i > 0 { Divider().padding(.leading, 50) }
                            machineRow(m, running: running.filter { $0.machineID == m.machineID }.count)
                        }
                    }
                }
                card {
                    row(open: { model.drawerOpen = true }) {
                        Text("View All Sessions").frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
                .padding(.top, 6)
                .accessibilityIdentifier("all-sessions")
            }
            .padding(.horizontal, 16)
        } header: {
            SectionTitle(title: "Working Now")
        }
    }

    /// The coding agents ready on any machine, and any that ran today or runs now.
    private func codingAgents(running: [Session], today: DayActivity?) -> [String] {
        var ids = Set(model.machines.flatMap { $0.status.agents.filter(\.available).map(\.name) })
        ids.formUnion(running.map(\.agent))
        ids.formUnion(today?.pairs.map(\.agent) ?? [])
        ids.remove("")
        return ids.sorted()
    }

    /// A coding agent's day: what it runs, on how many machines, its turns
    /// (or, with a client picked in the flow, the turns that client sent
    /// it), and its sessions hour by hour.
    private func agentRow(_ a: String, running: Int, turns: Int, flow: WorkFlow, today: DayActivity?, hours: [Int]?, hour: Int) -> some View {
        let machines = model.machines.filter { $0.status.agents.contains { $0.name == a && $0.available } }.count
        var detail = machines > 0 ? (turns > 0 ? String(localized: "\(machines) machines · \(turns) turns") : String(localized: "\(machines) machines"))
            : (turns > 0 ? String(localized: "\(turns) turns") : "")
        var dim = false
        switch picked {
        case .client(let c):
            let shown = Set(flow.clients.map(\.id))
            let sent = today?.pairs.filter { $0.agent == a && (c.isEmpty ? !shown.contains($0.createdBy) : $0.createdBy == c) }
            let n = sent?.reduce(0) { $0 + $1.turns } ?? 0
            let name = c.isEmpty ? String(localized: "The others") : AgentLogo.name(model.clientLabel(c))
            detail = String(localized: "\(name) sent \(n) turns")
            dim = sent?.isEmpty ?? true
        case .agent(let x):
            dim = x.isEmpty ? flow.agents.contains { $0.id == a } : x != a
        case nil:
            break
        }
        return HStack(alignment: .top, spacing: 10) {
            AgentLogo(id: a, size: 26).padding(.top, 1)
            VStack(alignment: .leading, spacing: 6) {
                HStack(alignment: .firstTextBaseline) {
                    Text(agentTitle(a)).lineLimit(1)
                    Spacer()
                    Text(running > 0 ? String(localized: "\(running) running") : String(localized: "Idle"))
                        .font(.subheadline).foregroundStyle(running > 0 ? .primary : .secondary).monospacedDigit()
                }
                if !detail.isEmpty {
                    Text(detail).font(.footnote).foregroundStyle(.secondary).monospacedDigit().lineLimit(1)
                }
                HourCells(hours: hours ?? Array(repeating: 0, count: 24), hour: hour)
            }
        }
        .padding(.horizontal, 14).padding(.vertical, 10)
        .opacity(dim ? 0.35 : 1)
        .animation(.smooth, value: picked)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("coding-agent-\(a)")
    }

    private func machineRow(_ m: Machine, running: Int) -> some View {
        Button { model.path.append(.machine(m.machineID)) } label: {
            HStack(spacing: 10) {
                Image(systemName: m.status.platform.contains("linux") ? "server.rack" : m.local ? "laptopcomputer" : "desktopcomputer")
                    .frame(width: 26).foregroundStyle(m.online ? .secondary : .tertiary)
                Text(m.machineName).lineLimit(1).foregroundStyle(m.online ? .primary : .secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                Circle().fill(m.online ? Color.green : .clear).strokeBorder(m.online ? .clear : Color.secondary, lineWidth: 1.5)
                    .frame(width: 8, height: 8)
                Group {
                    if !m.online {
                        Text(m.hasLastSeen ? String(localized: "Offline · \(agoText(m.lastSeen.date, now: model.now))") : String(localized: "Offline"))
                    } else if m.status.paused {
                        Text("Paused")
                    } else if running > 0 {
                        Text("\(running) running")
                    } else {
                        Text("Online")
                    }
                }
                .font(.subheadline).foregroundStyle(.secondary).monospacedDigit()
            }
            .padding(.horizontal, 14).frame(minHeight: 48)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("machine-row-\(m.machineName)")
    }

    private func caption(_ title: LocalizedStringKey, detail: LocalizedStringKey? = nil) -> some View {
        HStack {
            Text(title)
            Spacer()
            if let detail { Text(detail) }
        }
        .font(.caption).foregroundStyle(.secondary)
        .padding(.horizontal, 4).padding(.top, 8)
    }

    private func card(@ViewBuilder _ content: () -> some View) -> some View {
        VStack(spacing: 0, content: content)
            .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
    }

    private func row(open: @escaping () -> Void, @ViewBuilder _ content: () -> some View) -> some View {
        Button(action: open) {
            HStack(spacing: 12) {
                content()
                Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(.tertiary)
            }
            .padding(.horizontal, 16).padding(.vertical, 13)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
    }
}

/// What runs now, and today's sessions hour by hour: past hours in ink
/// grey, this one in the accent, the hours to come faint.
struct DayCard: View {
    let running: Int
    let today: DayActivity?
    let hour: Int

    var body: some View {
        let hours = today?.hours ?? Array(repeating: 0, count: 24)
        let top = max(hours.max() ?? 0, 1)
        VStack(alignment: .leading, spacing: 14) {
            HStack(alignment: .lastTextBaseline) {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    Text("\(running)").font(.system(size: 56, weight: .bold)).monospacedDigit()
                    Text("sessions running").foregroundStyle(.secondary)
                }
                Spacer()
                if let today {
                    VStack(alignment: .trailing, spacing: 2) {
                        Text("Active today \(Text("\(today.sessions)").bold().foregroundStyle(.primary))")
                        Text("Done \(Text("\(today.done)").bold().foregroundStyle(.primary))")
                    }
                    .font(.footnote).foregroundStyle(.secondary).monospacedDigit()
                }
            }
            HStack(alignment: .bottom, spacing: 3) {
                ForEach(0 ..< 24, id: \.self) { i in
                    RoundedRectangle(cornerRadius: 3, style: .continuous)
                        .fill(i > hour ? AnyShapeStyle(.quaternary) : i == hour ? AnyShapeStyle(Color.accentColor) : AnyShapeStyle(Color.primary.opacity(0.32)))
                        .frame(height: i > hour ? 3 : max(3, CGFloat(hours[i]) / CGFloat(top) * 56))
                }
            }
            .frame(height: 56, alignment: .bottom)
            .accessibilityHidden(true)
            HourAxis(hour: hour)
        }
        .padding(.horizontal, 18).padding(.top, 18).padding(.bottom, 12)
        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("working-today")
    }
}

/// What is picked in the flow: a client or a coding agent ("" for the
/// others together on that side).
enum FlowPick: Hashable {
    case client(String)
    case agent(String)
}

/// Who sent the day's work to which coding agent: the clients on the left,
/// the agents on the right, their logos at the bands' ends, each band as
/// thick as its turns; the turns once, under each agent. Tapping a side
/// picks it, its bands in the accent and the rest faint; tapping it again
/// lets go. Under it, the clients that sent nothing.
struct FlowCard: View {
    let flow: WorkFlow
    @Binding var picked: FlowPick?
    let clientName: (String) -> String
    let idle: [String]

    private let rowHeight: CGFloat = 52

    var body: some View {
        let rows = CGFloat(max(flow.clients.count, flow.agents.count, 1))
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text("Sent the work")
                Spacer()
                Text("Did the work")
            }
            .font(.caption2).foregroundStyle(.secondary)
            GeometryReader { g in
                let layout = FlowLayout(flow: flow, width: g.size.width, height: rows * rowHeight)
                ZStack(alignment: .topLeading) {
                    Canvas { ctx, _ in
                        for (b, band) in zip(flow.bands, layout.bands) {
                            var p = Path()
                            p.move(to: CGPoint(x: layout.left, y: band.from))
                            p.addCurve(to: CGPoint(x: layout.right, y: band.to),
                                       control1: CGPoint(x: (layout.left + layout.right) / 2, y: band.from),
                                       control2: CGPoint(x: (layout.left + layout.right) / 2, y: band.to))
                            ctx.stroke(p, with: .color(color(b)), lineWidth: band.width)
                        }
                    }
                    ForEach(Array(flow.clients.enumerated()), id: \.element.id) { i, n in
                        node(n, pick: .client(n.id), name: n.id.isEmpty ? String(localized: "\(n.count) more") : AgentLogo.name(clientName(n.id)), logo: n.id.isEmpty ? nil : clientName(n.id), trailing: true)
                            .frame(width: layout.nodeWidth, height: 44)
                            .position(x: layout.nodeWidth / 2, y: layout.clientY[i])
                    }
                    ForEach(Array(flow.agents.enumerated()), id: \.element.id) { i, n in
                        node(n, pick: .agent(n.id), name: n.id.isEmpty ? String(localized: "\(n.count) more") : agentTitle(n.id), logo: n.id.isEmpty ? nil : n.id)
                            .frame(width: layout.nodeWidth, height: 44)
                            .position(x: g.size.width - layout.nodeWidth / 2, y: layout.agentY[i])
                    }
                }
            }
            .frame(height: rows * rowHeight)
            if !idle.isEmpty {
                Text("\(listText(idle)) sent no work today").font(.caption).foregroundStyle(.secondary).lineLimit(2)
            }
        }
        .padding(.horizontal, 18).padding(.vertical, 14)
        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
    }

    /// A client (its logo trailing, next to its bands) or an agent (its
    /// logo leading, its turns under its name).
    private func node(_ n: WorkFlow.Node, pick: FlowPick, name: String, logo: String?, trailing: Bool = false) -> some View {
        let on = picked == pick
        return Button {
            withAnimation(.smooth) { picked = on ? nil : pick }
        } label: {
            HStack(spacing: 8) {
                if trailing { Spacer(minLength: 0) }
                if !trailing { mark(logo) }
                VStack(alignment: trailing ? .trailing : .leading, spacing: 1) {
                    Text(name).font(.subheadline.weight(on ? .semibold : .regular)).foregroundStyle(on ? Color.accentColor : .primary).lineLimit(1)
                    if !trailing { Text("\(n.turns) turns").font(.caption).foregroundStyle(.secondary).monospacedDigit() }
                }
                if trailing { mark(logo) }
                if !trailing { Spacer(minLength: 0) }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .opacity(linked(pick) ? 1 : 0.35)
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(on ? .isSelected : [])
        .accessibilityIdentifier(pick.identifier(name))
    }

    @ViewBuilder private func mark(_ logo: String?) -> some View {
        if let logo {
            AgentLogo(id: logo, size: 22)
        } else {
            Image(systemName: "ellipsis").font(.caption.weight(.semibold)).foregroundStyle(.secondary).frame(width: 22, height: 22)
        }
    }

    /// Whether nothing is picked, or this is picked or sends work to (or gets work from) what is.
    private func linked(_ p: FlowPick) -> Bool {
        guard let picked, picked != p else { return true }
        return flow.bands.contains { touches($0, picked) && touches($0, p) }
    }

    private func touches(_ b: WorkFlow.Band, _ p: FlowPick) -> Bool {
        switch p {
        case .client(let c): b.client == c
        case .agent(let a): b.agent == a
        }
    }

    private func color(_ b: WorkFlow.Band) -> Color {
        guard let picked else { return Color.primary.opacity(0.16) }
        return touches(b, picked) ? Color.accentColor.opacity(0.55) : Color.primary.opacity(0.05)
    }
}

extension FlowPick {
    func identifier(_ name: String) -> String {
        switch self {
        case .client: "flow-client-\(name)"
        case .agent(let a): "flow-agent-\(a.isEmpty ? "more" : a)"
        }
    }
}

/// Where the flow's nodes sit and its bands run: nodes spread down each
/// side; each band leaves its client and reaches its agent stacked with
/// the node's other bands, in the order of the other side, as thick as its
/// turns (the busiest node 16 points; a band without a finished turn still
/// shows, thin).
struct FlowLayout {
    struct BandLine { var from: CGFloat, to: CGFloat, width: CGFloat }

    let nodeWidth: CGFloat
    let left: CGFloat
    let right: CGFloat
    var clientY: [CGFloat] = []
    var agentY: [CGFloat] = []
    var bands: [BandLine] = []

    init(flow: WorkFlow, width: CGFloat, height: CGFloat) {
        nodeWidth = (width - max(56, width * 0.22)) / 2
        left = nodeWidth + 2
        right = width - nodeWidth - 2
        func spread(_ n: Int) -> [CGFloat] { (0 ..< n).map { (CGFloat($0) + 0.5) * height / CGFloat(n) } }
        clientY = spread(flow.clients.count)
        agentY = spread(flow.agents.count)
        let top = CGFloat(max((flow.clients + flow.agents).map(\.turns).max() ?? 1, 1))
        let k = 16 / top
        let widths = flow.bands.map { max(1.5, CGFloat($0.turns) * k) }
        let ci = { (id: String) in flow.clients.firstIndex { $0.id == id } ?? 0 }
        let ai = { (id: String) in flow.agents.firstIndex { $0.id == id } ?? 0 }
        var from = Array(repeating: CGFloat(0), count: flow.bands.count)
        var to = from
        for (i, n) in flow.clients.enumerated() {
            let mine = flow.bands.indices.filter { flow.bands[$0].client == n.id }.sorted { ai(flow.bands[$0].agent) < ai(flow.bands[$1].agent) }
            var y = clientY[i] - mine.reduce(0) { $0 + widths[$1] } / 2
            for b in mine { from[b] = y + widths[b] / 2; y += widths[b] }
        }
        for (i, n) in flow.agents.enumerated() {
            let mine = flow.bands.indices.filter { flow.bands[$0].agent == n.id }.sorted { ci(flow.bands[$0].client) < ci(flow.bands[$1].client) }
            var y = agentY[i] - mine.reduce(0) { $0 + widths[$1] } / 2
            for b in mine { to[b] = y + widths[b] / 2; y += widths[b] }
        }
        bands = flow.bands.indices.map { BandLine(from: from[$0], to: to[$0], width: widths[$0]) }
    }
}

/// Under the hours: midnight, every six hours and now, where they don't
/// crowd each other.
struct HourAxis: View {
    let hour: Int

    var body: some View {
        GeometryReader { g in
            let w = g.size.width
            let now = w * (CGFloat(hour) + 0.5) / 24
            ForEach([0, 6, 12, 18, 24], id: \.self) { mark in
                let x = w * CGFloat(mark) / 24
                if abs(x - now) > 30 {
                    Text(mark == 0 || mark == 24 ? String(localized: "\(mark) h") : "\(mark)")
                        .fixedSize()
                        .position(x: min(max(x, 14), w - 14), y: 6)
                }
            }
            Text("Now").fixedSize().foregroundStyle(Color.accentColor)
                .position(x: min(max(now, 14), w - 14), y: 6)
        }
        .frame(height: 12)
        .font(.caption2).foregroundStyle(.tertiary).monospacedDigit()
    }
}

/// An agent's day in 24 cells: how many of its sessions worked each hour,
/// in ink grey (this hour in the accent), the hours to come faint.
struct HourCells: View {
    let hours: [Int]
    let hour: Int

    var body: some View {
        HStack(spacing: 2) {
            ForEach(0 ..< 24, id: \.self) { i in
                RoundedRectangle(cornerRadius: 2, style: .continuous)
                    .fill(fill(i))
                    .frame(height: 8)
            }
        }
        .accessibilityHidden(true)
    }

    private func fill(_ i: Int) -> Color {
        if i > hour { return Color.primary.opacity(0.04) }
        let v = i < hours.count ? hours[i] : 0
        if v == 0 { return Color.primary.opacity(0.08) }
        return (i == hour ? Color.accentColor : Color.primary).opacity(min(1, 0.25 + Double(v) / 6))
    }
}

struct SectionTitle: View {
    let title: LocalizedStringKey

    var body: some View {
        Text(title).font(.title3.bold()).padding(.horizontal, 20)
    }
}

/// A machine whose sessions couldn't be listed: which one, what it means,
/// when it was last seen, and that it catches up by itself. The details
/// are on the machine's page.
struct UnreachableNotice: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let gone = model.unreachableMachines
        if !gone.isEmpty {
            Button {
                if gone.count == 1, let m = model.machines.first(where: { $0.machineName == gone[0].name }) {
                    model.path.append(.machine(m.machineID))
                } else {
                    model.open(.machines)
                }
            } label: {
                HStack(spacing: 12) {
                    Image(systemName: "wifi.slash").foregroundStyle(.secondary)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(line1(gone)).font(.subheadline)
                        Text(line2(gone)).font(.footnote).foregroundStyle(.secondary)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(.tertiary)
                }
                .padding(.horizontal, 20)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("unreachable")
        }
    }

    private func line1(_ gone: [(name: String, lastSeen: Date?, online: Bool)]) -> AttributedString {
        if gone.count > 1 { return AttributedString(localized: "**\(gone.count) machines can’t be reached**; their sessions can’t be shown for now") }
        let g = gone[0]
        return g.online ? AttributedString(localized: "**\(g.name) isn’t responding**; its sessions may be missing")
            : AttributedString(localized: "**\(g.name) is offline**; its sessions can’t be shown for now")
    }

    private func line2(_ gone: [(name: String, lastSeen: Date?, online: Bool)]) -> String {
        if gone.count > 1 { return listText(gone.map(\.name)) }
        let g = gone[0]
        if g.online { return String(localized: "Retrying") }
        if let seen = g.lastSeen { return String(localized: "Last online \(agoText(seen, now: model.now)) · they’ll appear once it reconnects") }
        return String(localized: "They’ll appear once it reconnects")
    }
}

/// A session in a list: its state, title, what it is doing or who started it, and when.
/// Without its metadata (the list's view menu), its state and title alone.
struct SessionRow: View {
    @Environment(AppModel.self) private var model
    let session: Session
    var metadata = true

    var body: some View {
        HStack(alignment: .center, spacing: 12) {
            StatusDot(session: session)
            VStack(alignment: .leading, spacing: 2) {
                Text(session.displayTitle).lineLimit(1)
                if metadata { second.font(.subheadline).lineLimit(1) }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            if metadata { trailing.font(.subheadline).foregroundStyle(.secondary).monospacedDigit() }
        }
        .padding(.horizontal, 16).padding(.vertical, 11)
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("session-\(session.displayTitle)")
    }

    @ViewBuilder private var second: some View {
        let grant = model.grantLabel(session)
        if session.isActive {
            HStack(spacing: 6) {
                ProgressView().controlSize(.mini)
                Text(phaseText(session)).foregroundStyle(.secondary)
            }
        } else if session.status == .waitingInput {
            Text("Waiting for you · \(grant)").foregroundStyle(.orange)
        } else if session.isFailed {
            Text("\(session.progress.hasLastFailure ? failureTitle(session.progress.lastFailure.kind) : String(localized: "Failed")) · \(grant)").foregroundStyle(.red)
        } else {
            Text("\(grant) · \(session.projectID)").foregroundStyle(.secondary)
        }
    }

    @ViewBuilder private var trailing: some View {
        if session.isActive, let start = session.turnStartedDate {
            Text(start, style: .timer)
        } else {
            Text(listTime(session.updatedDate, now: model.now))
        }
    }
}

/// Get Started (docs/onboarding.md §5.2): what the guide has left, on the
/// iPhone that set the owner up and put the guide off. Continue goes back
/// to it; done or hidden, it is gone for good.
struct GetStarted: View {
    @Environment(AppModel.self) private var model
    @AppStorage(Guide.key) private var guide = ""

    private enum Step: Int, CaseIterable {
        case computer, project, connect, tryIt
    }

    private func done(_ s: Step) -> Bool {
        switch s {
        case .computer: !model.machines.isEmpty
        case .project: !model.projects.isEmpty
        case .connect: model.clients.contains { !$0.revoked }
        case .tryIt: model.sessions.contains { s in model.clients.contains { $0.clientID == s.createdBy } && !s.isActive }
        }
    }

    private func title(_ s: Step) -> LocalizedStringKey {
        switch s {
        case .computer: "Connect your computer"
        case .project: "Add a project"
        case .connect: "Connect your agent"
        case .tryIt: "Try it"
        }
    }

    var body: some View {
        if guide == Guide.later {
            if let next = Step.allCases.first(where: { !done($0) }) {
                VStack(alignment: .leading, spacing: 10) {
                    HStack {
                        SectionTitle(title: "Get Started").accessibilityIdentifier("get-started")
                        Spacer()
                        Menu {
                            Button("Hide", systemImage: "eye.slash") { guide = Guide.done }
                        } label: {
                            Image(systemName: "ellipsis").frame(width: 32, height: 32)
                        }
                        .padding(.trailing, 20)
                        .accessibilityIdentifier("get-started-menu")
                    }
                    VStack(spacing: 0) {
                        ForEach(Step.allCases, id: \.self) { s in
                            if s.rawValue > 0 { Divider().padding(.leading, 52) }
                            HStack(spacing: 12) {
                                Image(systemName: done(s) ? "checkmark.circle.fill" : s == next ? "circle.inset.filled" : "circle")
                                    .foregroundStyle(done(s) ? AnyShapeStyle(.green) : s == next ? AnyShapeStyle(.tint) : AnyShapeStyle(.tertiary))
                                    .frame(width: 24)
                                Text(title(s)).foregroundStyle(s == next || done(s) ? .primary : .secondary)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                                if s == next {
                                    Button("Continue") { guide = Guide.on }.buttonStyle(.glassProminent)
                                        .accessibilityIdentifier("get-started-continue")
                                }
                            }
                            .padding(.horizontal, 16).padding(.vertical, 11)
                            .accessibilityElement(children: .contain)
                            .accessibilityIdentifier("get-started-\(s.rawValue)")
                        }
                    }
                    .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
                    .padding(.horizontal, 16)
                }
            } else {
                // Done from elsewhere (the Mac, the menus): gone for good.
                Color.clear.frame(height: 0).onAppear { guide = Guide.done }
            }
        }
    }
}
