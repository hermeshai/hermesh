import HermeshHer
import HermeshProto
import HermeshSessions
import SwiftUI

/// What needs the owner, as a deck (docs/overview-usage.md §4.1): one card
/// in front, whole, handled in place; the edges under it say there are more.
/// The cards page sideways (the system's own paging), and the next one comes
/// up when the one in front is handled. It takes one card's height however
/// many there are.
struct NeedsDeck: View {
    let needs: [Need]
    let team: TeamState
    let step: GuideStep?
    @Binding var front: String?

    var body: some View {
        let index = needs.firstIndex { $0.id == front } ?? 0
        VStack(alignment: .leading, spacing: 8) {
            OverviewHeading(title: Text("Needs You")) {
                if needs.count > 1 {
                    Text(verbatim: "\(index + 1) / \(needs.count)").foregroundStyle(.secondary).monospacedDigit()
                        .accessibilityIdentifier("needs-place")
                }
            }
            .padding(.horizontal, 16)
            VStack(spacing: 0) {
                ZStack(alignment: .bottom) {
                    if needs.count > 2 { edge(inset: 42, drop: 16, opacity: 0.42) }
                    if needs.count > 1 { edge(inset: 28, drop: 8, opacity: 0.72) }
                    ScrollView(.horizontal) {
                        HStack(spacing: 0) {
                            ForEach(needs) { need in
                                NeedCard(need: need, team: team, step: step)
                                    .padding(.horizontal, 16)
                                    .containerRelativeFrame(.horizontal)
                            }
                        }
                        .scrollTargetLayout()
                    }
                    .scrollTargetBehavior(.paging)
                    .scrollPosition(id: $front)
                    .scrollIndicators(.hidden)
                }
                // What shows of the cards further down: a tap brings the next one up.
                if needs.count > 1 {
                    Button {
                        let i = needs.firstIndex { $0.id == front } ?? 0
                        withAnimation(.smooth) { front = needs[(i + 1) % needs.count].id }
                    } label: {
                        Color.clear.frame(height: needs.count > 2 ? 16 : 8).contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .padding(.horizontal, 28)
                    .accessibilityLabel("Next")
                    .accessibilityIdentifier("needs-next")
                }
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("needs")
    }

    /// The edge of a card further down the deck.
    private func edge(inset: CGFloat, drop: CGFloat, opacity: Double) -> some View {
        RoundedRectangle(cornerRadius: 26, style: .continuous).fill(Color(.secondarySystemGroupedBackground))
            .overlay { RoundedRectangle(cornerRadius: 26, style: .continuous).strokeBorder(Color(.separator).opacity(0.5), lineWidth: 0.5) }
            .opacity(opacity)
            .frame(height: 60)
            .padding(.horizontal, inset)
            .offset(y: drop)
            .accessibilityHidden(true)
    }
}

/// One thing that needs the owner: what kind it is, what it says, and what
/// can be done about it, in place.
struct NeedCard: View {
    @Environment(AppModel.self) private var model
    @AppStorage("hideSecondDevice") private var hideSecondDevice = false
    @AppStorage(Guide.key) private var guide = ""
    let need: Need
    let team: TeamState
    let step: GuideStep?
    @State private var sending = false

    var body: some View {
        content
            .padding(.horizontal, 16).padding(.vertical, 14)
            .frame(maxWidth: .infinity, minHeight: 150, maxHeight: .infinity, alignment: .topLeading)
            .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
    }

    @ViewBuilder private var content: some View {
        switch need {
        case .question(let id):
            if let s = team.waiting.first(where: { $0.sessionID == id }) { question(s) }
        case .pairing(let code):
            if let p = model.pairings.first(where: { $0.code == code }) {
                card(label: Text("Pairing"), title: Text(PairingText.title(p)),
                     detail: Text("Verification code \(Text(p.verificationCode).monospaced())")) {
                    AgentLogo(id: p.label, size: 30)
                } actions: {
                    act(Text("Verify"), prominent: true, id: "verify-pairing") { model.sheet = .pairing(p.code) }
                }
            }
        case .failed(let id):
            if let s = team.failed.first(where: { $0.sessionID == id }) {
                card(dot: .red, label: Text("Failed"), trailing: Text(listTime(s.updatedDate, now: model.now)), title: Text(s.displayTitle),
                     detail: Text(model.senderLine(s))) {
                    AgentLogo(id: s.agent, size: 30)
                } actions: {
                    act(Text("View"), id: "session-\(s.displayTitle)") { model.openSession(s.sessionID) }
                }
            }
        case .limit(let agent):
            if let c = agentLimits(model.machines.map { (machineID: $0.machineID, agents: $0.status.agents) }, now: model.now).first(where: { $0.agent == agent }),
               let w = c.least {
                card(dot: .orange, label: Text("Limits"), trailing: Text(verbatim: resetText(w, now: model.now)),
                     title: w.group.isEmpty ? Text("\(agentTitle(agent))’s \(windowName(w.minutes, short: true)) limit is at \(w.left)%")
                         : Text("\(agentTitle(agent))’s \(w.group) \(windowName(w.minutes, short: true)) limit is at \(w.left)%"),
                     detail: Text(verbatim: reportedText(model, c))) {
                    AgentLogo(id: agent, size: 30)
                } actions: {
                    act(Text("View"), id: "limit-view") { withAnimation(.smooth) { model.stats.segment = .limits } }
                }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("limit-\(agent)")
            }
        case .expiring(let id):
            if let c = model.clients.first(where: { $0.grantID == id }) {
                let days = c.daysLeft(now: model.now)
                card(label: Text("Grants"),
                     title: Text(days == 0 ? String(localized: "“\(c.label)”’s grant ends today") : String(localized: "“\(c.label)”’s grant ends in \(days) days")),
                     detail: Text("Then it can no longer use your machines")) {
                    GrantAvatar(client: c, size: 30)
                } actions: {
                    act(Text("Renew…")) { model.sheet = .renew(c.grantID) }
                }
            }
        case .guide:
            if let step {
                card(label: Text("Get Started"), trailing: Text("Step \(step.rawValue + 1) of \(GuideStep.allCases.count)"), title: Text(step.title), detail: nil) {
                    symbol("circle.inset.filled", .accentColor)
                } actions: {
                    act(Text("Continue"), prominent: true, id: "get-started-continue") { guide = Guide.on }
                    act(Text("Hide"), id: "get-started-hide") { guide = Guide.done }
                }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("get-started")
            }
        case .herOff:
            card(label: Text(verbatim: "Her"), title: Text("Her isn’t on"), detail: Text("She runs on a computer of yours and leads these agents for you"), lines: 2) {
                HerAvatar(size: 30, asleep: true)
            } actions: {
                act(Text("Open")) { model.open(.her) }
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("her-off")
        case .notificationsOff:
            card(label: Text("Notifications"), title: Text("Notifications are off"),
                 detail: Text("You’ll see requests and questions only while Hermesh is open."), lines: 2) {
                symbol("bell.slash", .orange)
            } actions: {
                act(Text("Turn On…")) {
                    if let url = URL(string: UIApplication.openNotificationSettingsURLString) { UIApplication.shared.open(url) }
                }
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("notifications-off")
        case .secondDevice:
            card(label: Text("Device"), title: nil,
                 detail: Text("Only this iPhone can approve. If you lose it, you’ll set up again and pair your machines and agents again."), lines: 3) {
                symbol("iphone.gen3", .secondary)
            } actions: {
                act(Text("Add Another…")) { model.sheet = .addDevice }
                act(Text("Hide")) { hideSecondDevice = true }
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("second-device")
        }
    }

    /// A session that waits for an answer: its question, and, when one tap
    /// answers it (quickChoice), the answers; any other opens the session,
    /// where the question's panel is.
    private func question(_ s: Session) -> some View {
        let q = s.pendingQuestion
        let choice = q.flatMap(quickChoice)
        let item = choice ?? q?.items.first
        let title = item.map { $0.text.isEmpty ? $0.header : $0.text } ?? s.displayTitle
        return card(dot: .orange, label: Text(statusTitle(.waitingInput)), title: Text(title), detail: Text(model.senderLine(s)), open: { model.openSession(s.sessionID) },
                    openID: "session-\(s.displayTitle)") {
            AgentLogo(id: s.agent, size: 30)
        } actions: {
            if let choice {
                let answers = ForEach(choice.options.map(\.label), id: \.self) { o in
                    act(Text(verbatim: o), id: "quick-answer-\(o)") {
                        sending = true
                        Task {
                            _ = await model.answer(s, answers: [Answer.with { $0.itemID = choice.id; $0.selected = [o] }], decline: false)
                            sending = false
                        }
                    }
                }
                // Side by side while they fit, else one under the other.
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: 8) { answers }
                    VStack(spacing: 8) { answers }
                }
                .disabled(sending)
            } else {
                act(Text("Answer"), prominent: true) { model.openSession(s.sessionID) }
            }
        }
    }

    /// A card's parts: what kind of thing it is (a dot for a state), what
    /// stands at the end of that line, its picture, title and a line under
    /// it, then what can be done. open: the text is a button that opens it (openID: its identifier).
    private func card<Icon: View, Actions: View>(dot: Color? = nil, label: Text, trailing: Text? = nil, title: Text?, detail: Text?, lines: Int = 1,
                                                 open: (() -> Void)? = nil, openID: String = "", @ViewBuilder icon: () -> Icon, @ViewBuilder actions: () -> Actions) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 6) {
                Circle().fill(dot ?? Color(.tertiaryLabel)).frame(width: 7, height: 7)
                label.fontWeight(.semibold)
                Spacer()
                if let trailing { trailing.monospacedDigit() }
            }
            .font(.caption).foregroundStyle(.secondary)
            let what = HStack(alignment: .top, spacing: 10) {
                icon()
                VStack(alignment: .leading, spacing: 2) {
                    if let title { title.font(.callout.weight(.semibold)).lineLimit(2) }
                    if let detail { detail.font(title == nil ? .subheadline : .footnote).foregroundStyle(title == nil ? .primary : .secondary).lineLimit(lines) }
                }
                .multilineTextAlignment(.leading)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .padding(.top, 10)
            if let open {
                Button(action: open) { what.contentShape(.rect) }.buttonStyle(.plain).accessibilityIdentifier(openID)
            } else {
                what
            }
            Spacer(minLength: 12)
            HStack(spacing: 8) { actions() }.controlSize(.large)
        }
    }

    /// What can be done, as a button as wide as its share of the row; prominent: the one to take.
    @ViewBuilder private func act(_ title: Text, prominent: Bool = false, id: String = "", _ action: @escaping () -> Void) -> some View {
        let label = title.lineLimit(1).frame(maxWidth: .infinity)
        Group {
            if prominent { Button(action: action) { label }.buttonStyle(.glassProminent) } else { Button(action: action) { label }.buttonStyle(.glass) }
        }
        .accessibilityIdentifier(id)
    }

    private func symbol(_ name: String, _ color: Color) -> some View {
        Image(systemName: name).font(.system(size: 16)).foregroundStyle(color)
            .frame(width: 30, height: 30)
            .background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
    }
}
