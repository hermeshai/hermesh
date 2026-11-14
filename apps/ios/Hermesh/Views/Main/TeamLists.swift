import HermeshProto
import HermeshSessions
import SwiftUI

/// What the owner's agents are doing, as lists (docs/her-team.md §4.3,
/// §4.6): what needs the owner (each question a card, answered in place when
/// one tap can; then requests to connect and sessions that failed), what
/// runs, and the sessions that ended last. Her's team panel and Work both
/// show it.
struct TeamLists: View {
    @Environment(AppModel.self) private var model
    let team: TeamState
    /// Requests to connect among what needs the owner: the panel lists them, Work has them above.
    var pairings: [PairingDetails] = []
    /// How many ended sessions, and what they are called.
    var finished = 3
    var finishedTitle: LocalizedStringKey = "Recently Finished"
    /// The rows' ground.
    var fill = AnyShapeStyle(Color(.secondarySystemGroupedBackground))

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            if !team.waiting.isEmpty || !pairings.isEmpty || !team.failed.isEmpty {
                caption("Needs You")
                ForEach(team.waiting, id: \.sessionID) { s in QuestionCard(session: s) }
                if !pairings.isEmpty || !team.failed.isEmpty {
                    card {
                        ForEach(Array(pairings.enumerated()), id: \.element.code) { i, p in
                            if i > 0 { divider }
                            pairingRow(p)
                        }
                        ForEach(Array(team.failed.enumerated()), id: \.element.sessionID) { i, s in
                            if i > 0 || !pairings.isEmpty { divider }
                            sessionRow(s)
                        }
                    }
                }
            }
            if !team.running.isEmpty {
                caption("In Progress").id(TeamLists.inProgress)
                sessions(team.running)
            }
            if finished > 0, !team.finished.isEmpty {
                caption(finishedTitle)
                sessions(Array(team.finished.prefix(finished)))
            }
        }
    }

    /// Where a page scrolls to show what runs.
    static let inProgress = "team-in-progress"

    private func sessions(_ list: [Session]) -> some View {
        card {
            ForEach(Array(list.enumerated()), id: \.element.sessionID) { i, s in
                if i > 0 { divider }
                sessionRow(s)
            }
        }
    }

    private func sessionRow(_ s: Session) -> some View {
        Button { model.openSession(s.sessionID) } label: { SessionRow(session: s, doing: true) }
            .buttonStyle(.plain)
    }

    private func pairingRow(_ p: PairingDetails) -> some View {
        Button { model.sheet = .pairing(p.code) } label: {
            HStack(spacing: 8) {
                Text(PairingText.title(p)).lineLimit(2).multilineTextAlignment(.leading).frame(maxWidth: .infinity, alignment: .leading)
                Circle().fill(.orange).frame(width: 8, height: 8)
            }
            .padding(.horizontal, 16).padding(.vertical, 9).frame(minHeight: 44)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("team-pairing")
    }

    private var divider: some View { Divider().padding(.leading, 16) }

    private func caption(_ title: LocalizedStringKey) -> some View {
        Text(title).font(.caption).foregroundStyle(.secondary).padding(.horizontal, 4).padding(.top, 8)
    }

    private func card(@ViewBuilder _ content: () -> some View) -> some View {
        VStack(spacing: 0, content: content)
            .background(fill, in: RoundedRectangle(cornerRadius: 26, style: .continuous))
    }
}

/// A session that waits for an answer: its question, who sent the work to
/// which agent, and, when one tap answers it (quickChoice), the answers.
/// Any other question opens the session, where the question's panel is.
struct QuestionCard: View {
    @Environment(AppModel.self) private var model
    let session: Session
    @State private var sending = false

    var body: some View {
        let question = session.pendingQuestion
        let choice = question.flatMap(quickChoice)
        let item = choice ?? question?.items.first
        VStack(alignment: .leading, spacing: 12) {
            Button { model.openSession(session.sessionID) } label: {
                HStack(alignment: .top, spacing: 10) {
                    AgentLogo(id: session.agent, size: 28)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(item.map { $0.text.isEmpty ? $0.header : $0.text } ?? session.displayTitle)
                            .font(.callout.weight(.semibold)).multilineTextAlignment(.leading)
                        Text(model.senderLine(session)).font(.footnote).foregroundStyle(.secondary).lineLimit(1)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    Circle().fill(.orange).frame(width: 9, height: 9).padding(.top, 6)
                }
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("session-\(session.displayTitle)")
            if let choice {
                let options = choice.options.map(\.label)
                // Side by side while they fit, else one under the other.
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: 8) { answers(options, item: choice.id, wide: true) }
                    VStack(spacing: 8) { answers(options, item: choice.id, wide: true) }
                }
                .disabled(sending)
            }
        }
        .padding(14)
        .background(Color.orange.opacity(0.14), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
    }

    private func answers(_ options: [String], item: String, wide: Bool) -> some View {
        ForEach(options, id: \.self) { o in
            Button {
                sending = true
                Task {
                    _ = await model.answer(session, answers: [Answer.with { $0.itemID = item; $0.selected = [o] }], decline: false)
                    sending = false
                }
            } label: {
                Text(o).lineLimit(1).frame(maxWidth: wide ? .infinity : nil)
            }
            .buttonStyle(.glass)
            .controlSize(.large)
            .accessibilityIdentifier("quick-answer-\(o)")
        }
    }
}
