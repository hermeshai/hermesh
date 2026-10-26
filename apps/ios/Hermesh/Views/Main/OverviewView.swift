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

/// The first page: what needs the owner beyond the sessions (pairings,
/// grants that end), and how many sessions wait; then the work: today's
/// sessions, the agents and the machines. The sessions themselves are in
/// the menu.
struct OverviewView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 26) {
                UnreachableNotice()
                attention
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
    }

    @ViewBuilder private var attention: some View {
        let questions = model.sessions.filter { $0.status == .waitingInput }.count
        let failed = model.sessions.filter { $0.isFailed && $0.status != .archived }.count
        let expiring = model.expiringClients
        let count = model.pairings.count + expiring.count + questions + failed
        if count > 0 {
            Section {
                VStack(spacing: 10) {
                    ForEach(model.pairings, id: \.code) { PairingCard(pairing: $0) }
                    ForEach(expiring, id: \.grantID) { ExpiringCard(client: $0) }
                    if questions + failed > 0 {
                        card {
                            row(open: { model.drawerOpen = true }) {
                                Image(systemName: "bubble.left.and.text.bubble.right").foregroundStyle(.secondary).frame(width: 28)
                                VStack(alignment: .leading, spacing: 2) {
                                    Text("Sessions waiting for you")
                                    Text(waitingText(questions: questions, failed: failed)).font(.subheadline).foregroundStyle(.secondary)
                                }
                                .frame(maxWidth: .infinity, alignment: .leading)
                                Text("\(questions + failed)").foregroundStyle(.secondary).monospacedDigit()
                            }
                        }
                        .accessibilityIdentifier("sessions-waiting")
                    }
                }
                .padding(.horizontal, 16)
            } header: {
                SectionTitle(title: "Needs You")
            }
        }
    }

    private func waitingText(questions: Int, failed: Int) -> String {
        [questions > 0 ? String(localized: "\(questions) asking") : nil, failed > 0 ? String(localized: "\(failed) failed") : nil]
            .compactMap { $0 }.joined(separator: " · ")
    }

    /// Today's sessions in a line, who is working (the agents), where (the
    /// machines), and the way to the sessions: the menu.
    private var working: some View {
        let t = todayStats(model.sessions, now: model.now)
        let running = model.sessions.filter(\.isActive)
        let agents = model.clients.filter { !$0.revoked }
        return Section {
            VStack(alignment: .leading, spacing: 10) {
                Text("Today **\(t.sessions)** sessions · **\(running.count)** running · **\(t.done)** done")
                    .font(.subheadline).foregroundStyle(.secondary)
                    .padding(.horizontal, 4)
                    .accessibilityIdentifier("working-today")
                if !agents.isEmpty {
                    card {
                        ForEach(agents, id: \.grantID) { c in
                            let n = running.filter { $0.createdBy == c.clientID }.count
                            row(open: { model.path.append(.grant(c.grantID)) }) {
                                GrantAvatar(label: c.label)
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(c.label)
                                    Text(n > 0 ? String(localized: "\(n) running") : String(localized: "Idle")).font(.subheadline).foregroundStyle(.secondary)
                                }
                                .frame(maxWidth: .infinity, alignment: .leading)
                            }
                            if c.grantID != agents.last?.grantID { Divider().padding(.leading, 56) }
                        }
                    }
                }
                if !model.machines.isEmpty {
                    card {
                        ForEach(model.machines, id: \.machineID) { m in
                            row(open: { model.path.append(.machine(m.machineID)) }) {
                                MachineRow(machine: m, running: running.filter { $0.machineID == m.machineID }.count)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                            }
                            if m.machineID != model.machines.last?.machineID { Divider().padding(.leading, 56) }
                        }
                    }
                }
                card {
                    row(open: { model.drawerOpen = true }) {
                        Text("View All Sessions").frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
                .accessibilityIdentifier("all-sessions")
            }
            .padding(.horizontal, 16)
        } header: {
            SectionTitle(title: "Working Now")
        }
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
            .padding(.horizontal, 16).padding(.vertical, 11)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
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

struct PairingCard: View {
    @Environment(AppModel.self) private var model
    let pairing: PairingDetails

    var body: some View {
        HStack(spacing: 12) {
            AgentLogo(id: pairing.label, size: 28)
            VStack(alignment: .leading, spacing: 3) {
                Text(PairingText.title(pairing)).font(.body.weight(.medium))
                Text("Verification code \(Text(pairing.verificationCode).monospaced())").font(.footnote).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Button("Verify") { model.sheet = .pairing(pairing.code) }
                .buttonStyle(.glassProminent)
                .accessibilityIdentifier("verify-pairing")
        }
        .padding(16)
        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
    }
}

struct ExpiringCard: View {
    @Environment(AppModel.self) private var model
    let client: Client

    var body: some View {
        let days = client.daysLeft(now: model.now)
        HStack(spacing: 12) {
            GrantAvatar(label: client.label)
            VStack(alignment: .leading, spacing: 3) {
                Text(days == 0 ? String(localized: "“\(client.label)”’s grant ends today") : String(localized: "“\(client.label)”’s grant ends in \(days) days"))
                    .font(.body.weight(.medium))
                Text("Then it can no longer use your machines, and running sessions are interrupted").font(.footnote).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Button("Renew…") { model.sheet = .renew(client.grantID) }.buttonStyle(.glass)
        }
        .padding(16)
        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
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
