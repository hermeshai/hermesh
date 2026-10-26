import HermeshProto
import HermeshSessions
import SwiftUI

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
/// them); then the work: what runs, today's sessions hour by hour, each
/// agent's day and the machines.
struct OverviewView: View {
    @Environment(AppModel.self) private var model
    @State private var everyAgent = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 26) {
                UnreachableNotice()
                todo
                working
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
        .refreshable { await model.loadActivity() }
    }

    private enum Todo: Identifiable {
        case pairing(PairingDetails)
        case expiring(Client)
        case sessions(Int)

        var id: String {
            switch self {
            case .pairing(let p): "p" + p.code
            case .expiring(let c): "g" + c.grantID
            case .sessions: "s"
            }
        }
    }

    @ViewBuilder private var todo: some View {
        let waiting = model.sessions.filter(\.needsAttention).count
        let items = model.pairings.map(Todo.pairing) + model.expiringClients.map(Todo.expiring) + (waiting > 0 ? [.sessions(waiting)] : [])
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

    private func expiringRow(_ c: Client) -> some View {
        let days = c.daysLeft(now: model.now)
        return HStack(spacing: 12) {
            GrantAvatar(label: c.label)
            VStack(alignment: .leading, spacing: 2) {
                Text(days == 0 ? String(localized: "“\(c.label)”’s grant ends today") : String(localized: "“\(c.label)”’s grant ends in \(days) days"))
                Text("Then it can no longer use your machines").font(.footnote).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Button("Renew…") { model.sheet = .renew(c.grantID) }.buttonStyle(.glass)
        }
        .padding(.horizontal, 16).padding(.vertical, 11)
    }

    /// What runs and today's sessions hour by hour; then each agent's day
    /// (those that worked first, the others folded), the machines, and the
    /// way to the sessions: the menu.
    private var working: some View {
        let running = model.sessions.filter(\.isActive)
        let hour = Calendar.current.component(.hour, from: model.now)
        let today = model.activity?.date == dayKey(model.now) ? model.activity : nil
        let agents = model.clients.filter { !$0.revoked }
        let runs = { (c: Client) in running.filter { $0.createdBy == c.clientID }.count }
        let worked = { (c: Client) in today?.sessionsByClient[c.clientID] ?? 0 }
        let busy = agents.filter { runs($0) > 0 || worked($0) > 0 }.sorted { (runs($0), worked($0)) > (runs($1), worked($1)) }
        let rest = agents.filter { runs($0) == 0 && worked($0) == 0 }
        let shown = everyAgent ? busy + rest : busy
        return Section {
            VStack(alignment: .leading, spacing: 10) {
                DayCard(running: running.count, today: today, hour: hour)
                if !agents.isEmpty {
                    caption("Agents", detail: "Sessions working each hour")
                    card {
                        ForEach(Array(shown.enumerated()), id: \.element.grantID) { i, c in
                            if i > 0 { Divider().padding(.leading, 50) }
                            agentRow(c, running: runs(c), hours: today?.byClient[c.clientID], hour: hour)
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

    private func agentRow(_ c: Client, running: Int, hours: [Int]?, hour: Int) -> some View {
        Button { model.path.append(.grant(c.grantID)) } label: {
            HStack(alignment: .top, spacing: 10) {
                GrantAvatar(label: c.label, size: 26)
                VStack(alignment: .leading, spacing: 7) {
                    HStack(alignment: .firstTextBaseline) {
                        Text(c.label).lineLimit(1)
                        Spacer()
                        Text(running > 0 ? String(localized: "\(running) running") : String(localized: "Idle"))
                            .font(.subheadline).foregroundStyle(running > 0 ? .primary : .secondary).monospacedDigit()
                    }
                    HourCells(hours: hours ?? Array(repeating: 0, count: 24), hour: hour)
                }
            }
            .padding(.horizontal, 14).padding(.vertical, 10)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("agent-\(c.label)")
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

/// The year of usage, in ink grey: one square per day.
struct UsageView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        ScrollView {
            if let u = model.usage {
                VStack(alignment: .leading, spacing: 14) {
                    heat(u)
                    HStack(alignment: .top) {
                        stat(tokensText(u.last30), "Last 30 days · tokens")
                        stat(String(localized: "\(u.active) days"), "Active days")
                        stat(String(localized: "\(u.streak) days"), "Streak")
                    }
                    if !u.byAgent.isEmpty {
                        HStack(spacing: 14) {
                            ForEach(u.byAgent.prefix(3), id: \.agent) { a in
                                Text("\(agentTitle(a.agent)) \(Int((a.share * 100).rounded()))%").font(.footnote).foregroundStyle(.secondary)
                            }
                        }
                    }
                    let gone = model.unreachableMachines
                    if !gone.isEmpty {
                        Text("Excludes \(listText(gone.map(\.name)))").font(.footnote).foregroundStyle(.secondary)
                    }
                }
                .padding(16)
                .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
                .padding(16)
            }
        }
        .background(Color(.systemGroupedBackground))
        .navigationTitle("Usage")
        .refreshable { await model.loadUsage() }
    }

    private func heat(_ u: UsageYear) -> some View {
        Grid(horizontalSpacing: 3, verticalSpacing: 3) {
            ForEach(0 ..< 7, id: \.self) { row in
                GridRow {
                    ForEach(0 ..< u.weeks, id: \.self) { col in
                        let cell = u.cells[col * 7 + row]
                        RoundedRectangle(cornerRadius: 3, style: .continuous)
                            .fill(cell.map { Color.primary.opacity([0.07, 0.18, 0.32, 0.5, 0.74][$0.level]) } ?? .clear)
                            .aspectRatio(1, contentMode: .fit)
                    }
                }
            }
        }
        .accessibilityLabel(Text("Usage over the last \(u.weeks) weeks"))
    }

    private func stat(_ value: String, _ label: LocalizedStringKey) -> some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(value).font(.title3.weight(.semibold)).monospacedDigit()
            Text(label).font(.caption).foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}
