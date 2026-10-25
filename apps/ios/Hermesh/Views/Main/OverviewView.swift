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

/// The first page: what needs the owner, what runs, and a line each for
/// today and usage.
struct OverviewView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 26) {
                UnreachableNotice()
                attention
                working
                VStack(spacing: 10) {
                    today
                    usage
                }
            }
            .padding(.bottom, 40)
        }
        .background(Color(.systemGroupedBackground))
        .navigationTitle("Overview")
        .navigationSubtitle(model.now.formatted(.dateTime.month().day().weekday(.wide)))
        .rootToolbar()
        .refreshable { await model.loadUsage() }
    }

    @ViewBuilder private var attention: some View {
        let questions = model.sessions.filter { $0.status == .waitingInput }.sorted { $0.updatedDate > $1.updatedDate }
        let failed = model.sessions.filter { $0.isFailed && $0.status != .archived }
        let expiring = model.expiringClients
        let count = questions.count + model.pairings.count + expiring.count + failed.count
        if count > 0 {
            Section {
                VStack(spacing: 10) {
                    ForEach(questions, id: \.sessionID) { QuestionCard(session: $0) }
                    ForEach(model.pairings, id: \.code) { PairingCard(pairing: $0) }
                    ForEach(expiring, id: \.grantID) { ExpiringCard(client: $0) }
                    ForEach(failed, id: \.sessionID) { FailedCard(session: $0) }
                }
                .padding(.horizontal, 16)
            } header: {
                SectionTitle(title: "Needs You", count: count)
            }
        }
    }

    @ViewBuilder private var working: some View {
        let running = model.sessions.filter(\.isActive).sorted { $0.createdDate > $1.createdDate }
        Section {
            if running.isEmpty {
                Resting().padding(.horizontal, 16)
            } else {
                VStack(spacing: 0) {
                    ForEach(running, id: \.sessionID) { s in
                        Button { model.openSession(s.sessionID) } label: { SessionRow(session: s) }
                            .buttonStyle(.plain)
                        if s.sessionID != running.last?.sessionID { Divider().padding(.leading, 52) }
                    }
                }
                .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
                .padding(.horizontal, 16)
            }
        } header: {
            SectionTitle(title: "Working Now")
        }
    }

    private var today: some View {
        let t = todayStats(model.sessions, now: model.now)
        return summaryRow("text.bubble") {
            Text("Today **\(t.sessions)** sessions · **\(t.done)** done · \(Text("\(t.failed) failed").foregroundStyle(t.failed > 0 ? .red : .secondary))")
        } action: {
            model.open(.sessions)
        }
    }

    /// Usage in a line; its page has the year of days.
    @ViewBuilder private var usage: some View {
        if let u = model.usage {
            summaryRow("chart.bar") {
                Text("Last 30 days **\(tokensText(u.last30))** tokens · active **\(u.active)** days")
            } action: {
                model.path.append(.usage)
            }
            .accessibilityIdentifier("usage")
        }
    }

    private func summaryRow(_ icon: String, @ViewBuilder text: () -> Text, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 10) {
                Image(systemName: icon).foregroundStyle(.secondary).frame(width: 22)
                text().frame(maxWidth: .infinity, alignment: .leading)
                Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(.tertiary)
            }
            .padding(.horizontal, 18).padding(.vertical, 15)
            .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
        }
        .buttonStyle(.plain)
        .padding(.horizontal, 16)
    }
}

/// Nothing running: the mark at rest, breathing slowly, and what will show here.
struct Resting: View {
    @Environment(\.accessibilityReduceMotion) private var still
    @State private var bright = false

    var body: some View {
        VStack(spacing: 6) {
            Mark(size: 34)
                .opacity(still || bright ? 1 : 0.55)
                .padding(.bottom, 6)
                .onAppear { if !still { withAnimation(.easeInOut(duration: 3.2).repeatForever()) { bright = true } } }
            Text("No sessions are running").font(.subheadline.weight(.semibold))
            Text("When an agent starts working, its sessions show up here.").font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 26).padding(.horizontal, 20)
        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("working-resting")
    }
}

struct SectionTitle: View {
    let title: LocalizedStringKey
    var count: Int?

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 6) {
            Text(title).font(.title3.bold())
            if let count { Text("\(count)").font(.body).foregroundStyle(.secondary) }
        }
        .padding(.horizontal, 20)
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

/// A question waiting for the owner, answerable right here when it is a
/// single choice.
struct QuestionCard: View {
    @Environment(AppModel.self) private var model
    let session: Session
    @State private var sending = false

    var body: some View {
        let q = session.pendingQuestion
        let item = q?.items.first
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                AgentLogo(id: session.agent, size: 22)
                Text("\(agentTitle(session.agent)) asks · \(session.displayTitle)").font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
                Spacer(minLength: 4)
                if let q, q.hasExpiresAt {
                    Text(minutesLeft(until: q.expiresAt.date, now: model.now)).font(.footnote).foregroundStyle(.orange)
                }
            }
            Text(item?.text ?? "").font(.body.weight(.medium)).fixedSize(horizontal: false, vertical: true)
            FlowLayout(spacing: 8) {
                if let q, let item, NoticeText.quickAnswerable(q) {
                    ForEach(item.options, id: \.label) { o in
                        Button(o.label) { quickAnswer(item: item, option: o.label) }
                            .buttonStyle(.bordered).buttonBorderShape(.capsule).disabled(sending)
                    }
                }
                Button("Answer…") { model.openSession(session.sessionID) }
                    .buttonStyle(.bordered).buttonBorderShape(.capsule)
            }
        }
        .padding(16)
        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
        .contentShape(.rect)
        .onTapGesture { model.openSession(session.sessionID) }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("question-card")
    }

    private func quickAnswer(item: Hermesh_Session_V1_QuestionItem, option: String) {
        sending = true
        var a = Answer()
        a.itemID = item.id
        a.selected = [option]
        Task {
            _ = await model.answer(session, answers: [a], decline: false)
            sending = false
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

struct FailedCard: View {
    @Environment(AppModel.self) private var model
    let session: Session

    var body: some View {
        Button { model.openSession(session.sessionID) } label: {
            HStack(spacing: 12) {
                Circle().fill(.red).frame(width: 9, height: 9).frame(width: 28)
                VStack(alignment: .leading, spacing: 3) {
                    Text(session.displayTitle).font(.body.weight(.medium)).lineLimit(1)
                    Text(session.progress.hasLastFailure ? failureTitle(session.progress.lastFailure.kind) : String(localized: "Failed"))
                        .font(.footnote).foregroundStyle(.red)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(.tertiary)
            }
            .padding(16)
            .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
        }
        .buttonStyle(.plain)
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

/// Lays children out in rows, wrapping as needed.
struct FlowLayout: Layout {
    var spacing: CGFloat = 8

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? .infinity
        var x: CGFloat = 0, y: CGFloat = 0, line: CGFloat = 0, widest: CGFloat = 0
        for v in subviews {
            let s = v.sizeThatFits(.unspecified)
            if x > 0 && x + s.width > width {
                y += line + spacing
                x = 0
                line = 0
            }
            x += s.width + spacing
            line = max(line, s.height)
            widest = max(widest, x - spacing)
        }
        return CGSize(width: min(widest, width), height: y + line)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var x = bounds.minX, y = bounds.minY, line: CGFloat = 0
        for v in subviews {
            let s = v.sizeThatFits(.unspecified)
            if x > bounds.minX && x + s.width > bounds.maxX {
                y += line + spacing
                x = bounds.minX
                line = 0
            }
            v.place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(s))
            x += s.width + spacing
            line = max(line, s.height)
        }
    }
}
