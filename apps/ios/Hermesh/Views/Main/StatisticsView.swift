import HermeshHer
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

/// Under the days of a range: its first, its middle and today.
struct DayAxis: View {
    let period: WorkPeriod
    @Environment(\.calendar) private var calendar

    var body: some View {
        let first = period.firstDay(Date(), calendar: calendar)
        let mid = calendar.date(byAdding: .day, value: period.days / 2, to: first) ?? first
        HStack {
            Text(first.formatted(.dateTime.month(.abbreviated).day()))
            Spacer()
            Text(mid.formatted(.dateTime.month(.abbreviated).day()))
            Spacer()
            Text("Today").foregroundStyle(Color.accentColor)
        }
        .font(.caption2).foregroundStyle(.tertiary).monospacedDigit()
        .frame(height: 12)
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

/// A session in a list, as the Mac's sidebar and the menu show it: its title
/// (shimmering while it runs), and at its end a dot and a word for a state
/// that is not plain (waiting, failed, preparing…); under it who sent the
/// work to which coding agent, the project, and when. Without its metadata
/// (the list's view menu), the first line alone, as tall as the menu's rows.
/// doing: while it runs, a third line names the tool it is in.
struct SessionRow: View {
    @Environment(AppModel.self) private var model
    let session: Session
    var metadata = true
    var doing = false

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(spacing: 8) {
                Group {
                    if session.isActive && !session.needsAttention { ShimmerText(session.displayTitle, paused: false) } else { Text(session.displayTitle) }
                }
                .foregroundStyle(session.status == .archived ? .secondary : .primary)
                .lineLimit(1).frame(maxWidth: .infinity, alignment: .leading)
                if let state {
                    HStack(spacing: 6) {
                        if session.needsAttention { Circle().fill(session.status == .waitingInput ? .orange : .red).frame(width: 8, height: 8) }
                        Text(state)
                    }
                    .font(.footnote).foregroundStyle(.secondary)
                }
            }
            if metadata {
                HStack(spacing: 8) {
                    Text(model.senderLine(session))
                        .lineLimit(1).frame(maxWidth: .infinity, alignment: .leading)
                    time.monospacedDigit()
                }
                .font(.footnote).foregroundStyle(.secondary)
            }
            if doing, session.isActive, !session.progress.currentTool.isEmpty {
                Text(session.progress.currentTool).font(.caption.monospaced()).foregroundStyle(.secondary).lineLimit(1)
            }
        }
        .padding(.horizontal, 16).padding(.vertical, metadata ? 9 : 0)
        .frame(minHeight: 44)
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("session-\(session.displayTitle)")
    }

    /// The Mac's RowStatus: nothing for a plain running, idle or archived session.
    private var state: String? {
        if session.isFailed && session.status != .crashed && session.status != .waitingInput { return String(localized: "Failed") }
        switch session.status {
        case .waitingInput, .crashed, .preparing, .queued, .unspecified: return statusTitle(session.status)
        default: return nil
        }
    }

    /// How long a running turn has run, else when the session last changed.
    @ViewBuilder private var time: some View {
        if session.status == .running, let start = session.turnStartedDate {
            Text(start, style: .timer)
        } else {
            Text(listTime(session.updatedDate, now: model.now))
        }
    }
}

/// Get Started (docs/onboarding.md §5.2): what the guide has left, on the
/// iPhone that set the owner up and put the guide off. The overview's deck
/// shows the next step; Continue goes back to the guide; done or hidden, it
/// is gone for good.
enum GuideStep: Int, CaseIterable {
    case computer, project, connect, tryIt

    var title: LocalizedStringKey {
        switch self {
        case .computer: "Connect your computer"
        case .project: "Add a project"
        case .connect: "Connect your agent"
        case .tryIt: "Try it"
        }
    }

    @MainActor func done(_ model: AppModel) -> Bool {
        switch self {
        case .computer: !model.machines.isEmpty
        case .project: !model.projects.isEmpty
        case .connect: model.clients.contains { !$0.revoked }
        case .tryIt: model.sessions.contains { s in model.clients.contains { $0.clientID == s.createdBy } && !s.isActive }
        }
    }

    /// The first step not done; nil when the guide has nothing left.
    @MainActor static func next(_ model: AppModel) -> GuideStep? { allCases.first { !$0.done(model) } }
}
