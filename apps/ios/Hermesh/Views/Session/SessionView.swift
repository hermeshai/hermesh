import HermeshProto
import HermeshSessions
import SwiftUI

// Shared conversation row, ending, scroll-follow, toolbar, and failure components used by
// SessionPage and Her sub-agent records (ProgressPage).

/// The archived note at the top of a conversation, with Unarchive.
struct ArchivedNote: View {
    @Environment(AppModel.self) private var model
    let session: Session

    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: "archivebox")
            Text("Archived: read-only; the worktree is released and the changes stay on the branch.")
                .frame(maxWidth: .infinity, alignment: .leading)
            Button("Unarchive") { Task { await model.setArchived(session, false) } }
                .foregroundStyle(.tint)
        }
        .font(.footnote).foregroundStyle(.secondary)
        .padding(.horizontal, 12).padding(.vertical, 8)
        .background(Palette.fill, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        .padding(.bottom, 3)
        .accessibilityIdentifier("archived-note")
    }
}

/// When the turn before step i started, for the time lines.
func previousTurn(_ steps: [Step], before i: Int) -> Date? {
    for s in steps[..<i].reversed() {
        if case .item(.turn(_, _, let at, _)) = s { return at }
    }
    return nil
}

/// One row of the conversation, as on the Mac: the owner on the right in a
/// blue bubble, the agent's words across the page, its tool calls as quiet
/// grey lines folded until opened.
struct StepView: View {
    @Environment(AppModel.self) private var model
    let step: Step
    let session: Session
    let previousTurn: Date?

    var body: some View {
        switch step {
        case .tools(_, let calls):
            if calls.count == 1 {
                ToolRow(item: calls[0], sessionID: session.sessionID)
            } else {
                ToolGroup(calls: calls, sessionID: session.sessionID)
            }
        case .item(let item):
            row(item)
        }
    }

    /// Who sent a prompt when it isn't the owner: Her, or another of the owner's agents.
    private func sender(_ clientID: String) -> PromptSender? {
        clientID == "owner" ? nil : PromptSender(name: model.senderName(clientID), client: model.clients.first { $0.clientID == clientID })
    }

    @ViewBuilder private func row(_ item: TranscriptItem) -> some View {
        switch item {
        case .turn(_, _, let at, let wakeReason):
            // A time line before a turn, for the first one, after a pause, or when woken by a background task.
            let timeText = at.flatMap { turnTimeLine($0, previous: previousTurn, now: model.now) }
            let wakeText = wakeReason.isEmpty ? nil : String(localized: "Background wake · \(wakeReason)")
            if let line = (timeText != nil && wakeText != nil) ? "\(timeText!) · \(wakeText!)" : (wakeText ?? timeText) {
                Text(line).font(.caption).foregroundStyle(.tertiary).frame(maxWidth: .infinity)
                    .padding(.top, 8)
                    .accessibilityIdentifier(wakeReason.isEmpty ? "turn-time-line" : "turn-wake-line")
            }
        case .user(_, let text, let at, let asker):
            // Who asked for the turn speaks on the right: the owner in the blue bubble, with nothing over it; another
            // of the owner's agents (Her, a granted one) in a quiet bubble with its face on the corner. Each turn says
            // its own: a session Her started can hold the owner's words too.
            PromptBubble(text: text, sent: at, from: sender(asker.isEmpty ? session.createdBy : asker)) { EmptyView() }
        case .agent(_, let text):
            MarkdownText(text: text)
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
                .contextMenu { CopyMenu(text: text, sent: nil) }
                .padding(.bottom, 8)
        case .message(_, let incoming, let name, let text, let queued):
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 6) {
                    Image(systemName: "envelope").foregroundStyle(.secondary)
                    Text(incoming ? String(localized: "Message from \(name)") : String(localized: "Message to \(name)")).fontWeight(.semibold)
                    if queued {
                        Spacer(minLength: 8)
                        Text("Queued for next turn").foregroundStyle(.secondary)
                    }
                }
                .font(.caption)
                MarkdownText(text: text, style: .small)
            }
            .padding(.horizontal, 12).padding(.vertical, 10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Palette.fill, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        case .question(_, let q, let r):
            if let r {
                VStack(alignment: .leading, spacing: 5) {
                    HStack(alignment: .firstTextBaseline, spacing: 5) {
                        Image(systemName: "questionmark.circle").foregroundStyle(.secondary)
                        Text(asked(q))
                    }
                    Text(resolutionTitle(r, name: { AgentLogo.name(model.clientLabel($0)) })).foregroundStyle(.secondary)
                }
                .font(.footnote)
                .padding(.horizontal, 12).padding(.vertical, 10)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Palette.fill, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
            }
        case .turnEnd(_, let end, _):
            if end.isError, isInterruption(end.hasFailure ? end.failure : nil) {
                interruptNote(end.failure)
            } else if end.isError, !(session.isFailed && sameFailure(session.progress.lastFailure, end.failure)) {
                FailLine(title: String(localized: "Turn \(end.turn) failed: \(failureTitle(end.failure.kind))"), message: end.failure.message)
            }
        case .error(_, let e):
            if e.hasFailure, isInterruption(e.failure) {
                interruptNote(e.failure)
            } else if !(session.isFailed && e.hasFailure && sameFailure(session.progress.lastFailure, e.failure)) {
                FailLine(title: e.hasFailure ? String(localized: "Failed: \(failureTitle(e.failure.kind))") : String(localized: "Failed"), message: e.message)
            }
        case .tool:
            EmptyView()
        }
    }

    /// A turn stopped on purpose: by you, by one of your agents, or by Hermesh. That it can go on, the box under it shows.
    private func interruptNote(_ f: Failure) -> some View {
        let by = interrupter(f)
        let label = model.clients.first { $0.clientID == by }?.label
        let text = by == "owner" ? String(localized: "You interrupted this turn")
            : label.map { String(localized: "\(AgentLogo.name($0)) interrupted this turn") } ?? String(localized: "This turn was interrupted")
        return TurnNote(text: text)
    }

    private func asked(_ q: Question) -> AttributedString {
        var s = AttributedString(String(localized: "Agent asked:"))
        s.inlinePresentationIntent = .stronglyEmphasized
        return s + AttributedString(" " + q.items.map(\.text).joined(separator: " "))
    }
}

/// Who sent work in when it isn't the owner: Her, or another of the owner's agents (its grant, when still known).
struct PromptSender {
    let name: String
    let client: Client?
}

/// What was sent to the agent, on the right, under `sender`; a long press copies it. The owner's is a blue bubble;
/// one another of the owner's agents sent (`from`) is a quiet one with that agent's face on its corner.
struct PromptBubble<Sender: View>: View {
    let text: String
    let sent: Date?
    var from: PromptSender?
    @ViewBuilder let sender: () -> Sender

    var body: some View {
        VStack(alignment: .trailing, spacing: 5) {
            sender()
            Text(text)
                .lineSpacing(3)
                .foregroundStyle(from == nil ? Palette.bubbleText : Color.primary)
                .padding(.horizontal, 14).padding(.vertical, 9)
                .background(from == nil ? Palette.bubble : Palette.fill2, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                .contentShape(.contextMenuPreview, RoundedRectangle(cornerRadius: 18, style: .continuous))
                .contextMenu { CopyMenu(text: text, sent: sent, from: from?.name) }
                .overlay(alignment: .topLeading) { if let from { badge(from).offset(x: -10, y: -10) } }
        }
        .padding(.leading, 48)
        .frame(maxWidth: .infinity, alignment: .trailing)
        .padding(.top, from == nil ? 8 : 14).padding(.bottom, 8)
    }

    /// The sender's face on the bubble's corner: its grant's avatar (Her's cat, an app's logo, an initial), ringed by the page.
    private func badge(_ from: PromptSender) -> some View {
        Group {
            if let c = from.client { GrantAvatar(client: c, size: 20) } else { AgentLogo(id: from.name, size: 20, initialOnly: true) }
        }
        .padding(2)
        .background(Color(UIColor.systemBackground), in: .circle)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text("Sent by \(from.name)"))
        .accessibilityIdentifier("prompt-sender")
    }
}

/// The long-press menu of a message: Copy, who sent it when it isn't the owner, and when.
struct CopyMenu: View {
    @Environment(AppModel.self) private var model
    let text: String
    let sent: Date?
    var from: String?

    var body: some View {
        Button("Copy", systemImage: "doc.on.doc") { UIPasteboard.general.string = text }
        if let from { Text("Sent by \(from)") }
        if let sent { Text("Sent \(agoText(sent, now: model.now))") }
    }
}

/// A session page's bar, on either kind of session (SessionView, ThreadPage): the menu button on a root page; in the
/// middle its title over where it runs, in a glass capsule that opens Session Info; ⋯ with its actions.
struct SessionToolbar: ToolbarContent {
    @Environment(AppModel.self) private var model
    let session: Session
    /// The sync line in place of where it runs while what shows is not current (a page with no box to say it over).
    var sync = false
    @Binding var renaming: Bool
    @Binding var deleting: Bool
    /// A thread's background tasks, when it has any: a button beside ⋯ while one runs, and always an entry in ⋯.
    var backgroundTasks: BackgroundTasks?

    struct BackgroundTasks {
        let running: Bool
        let open: () -> Void
    }

    var body: some ToolbarContent {
        ToolbarItem(placement: .principal) {
            Button { model.sheet = .sessionInfo(session.sessionID) } label: {
                VStack(spacing: 1) {
                    Text(session.displayTitle).font(.subheadline.weight(.semibold)).foregroundStyle(.primary)
                    if sync && model.shownSync != nil {
                        SyncLine(small: true)
                    } else {
                        Text(verbatim: "\(model.machineName(session.machineID))/\(session.projectID)").font(.caption2).foregroundStyle(.secondary)
                    }
                }
                .lineLimit(1)
                .padding(.horizontal, 18)
                .frame(minHeight: 44)
                .contentShape(.capsule)
                // Tinted with the page's own colour: the title reads over the conversation that scrolls under it (the
                // page draws no band of blur there), and the capsule looks the same whatever is under it.
                .glassEffect(.regular.tint(Color(.systemBackground).opacity(0.72)).interactive(), in: .capsule)
            }
            .buttonStyle(.plain)
            .accessibilityHint(Text("Session Info"))
            .accessibilityIdentifier("session-info")
        }
        .sharedBackgroundVisibility(.hidden)
        if model.path.isEmpty { MenuToolbarItem() }
        if let tasks = backgroundTasks, tasks.running {
            ToolbarItem(placement: .topBarTrailing) {
                Button("Background Tasks", systemImage: "terminal", action: tasks.open)
                    .accessibilityIdentifier("thread-background-tasks")
            }
        }
        ToolbarItem(placement: .topBarTrailing) {
            Menu {
                if let tasks = backgroundTasks {
                    Button("Background Tasks", systemImage: "terminal", action: tasks.open)
                }
                Button("Copy Session ID", systemImage: "doc.on.doc") {
                    UIPasteboard.general.string = session.sessionID
                    model.show(String(localized: "Copied the session ID"))
                }
                SessionActions(session: session, renaming: $renaming, deleting: $deleting)
            } label: {
                Image(systemName: "ellipsis")
            }
            .accessibilityLabel("More")
            .accessibilityIdentifier("session-more")
        }
    }
}

/// A quiet note in the middle of the conversation: a turn stopped on purpose, and such.
struct TurnNote: View {
    let text: String

    var body: some View {
        Text(text)
            .font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center)
            .padding(.horizontal, 12).padding(.vertical, 5)
            .background(Palette.fill, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .frame(maxWidth: .infinity)
    }
}

/// Under a conversation whose last turn went well: a quiet line that says so.
struct DoneLine: View {
    let text: String

    var body: some View {
        Label(text, systemImage: "checkmark")
            .font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center)
            .frame(maxWidth: .infinity)
            .padding(.top, 5)
    }
}

/// The bottom of a live turn, a glass capsule over the conversation: who is at work (`lead`), what it does and for
/// how long (`words`), and how to stop it.
struct LiveBar<Lead: View, Words: View>: View {
    let stop: LocalizedStringKey
    let stopID: String
    var disabled = false
    let action: () -> Void
    @ViewBuilder let lead: () -> Lead
    @ViewBuilder let words: () -> Words

    var body: some View {
        HStack(spacing: 10) {
            lead()
            Group { words() }.lineLimit(1)
            Spacer(minLength: 0)
            Button(action: action) {
                Label(stop, systemImage: "stop.fill")
            }
            .buttonStyle(.glass)
            .disabled(disabled)
            .accessibilityIdentifier(stopID)
        }
        .font(.subheadline)
        .padding(.leading, 16).padding(.trailing, 8).padding(.vertical, 8)
        .glassEffect(.regular, in: .capsule)
        .padding(.horizontal, 16).padding(.bottom, 4)
    }
}

/// The UIScrollView backing a conversation ScrollView, once mounted.
final class ConversationScrollBox {
    weak var view: UIScrollView?
}

/// Finds the UIScrollView hosting the conversation content.
struct ConversationScrollFinder: UIViewRepresentable {
    let box: ConversationScrollBox

    func makeUIView(context: Context) -> UIView { Finder(box: box) }
    func updateUIView(_ uiView: UIView, context: Context) {}

    private final class Finder: UIView {
        let box: ConversationScrollBox
        init(box: ConversationScrollBox) {
            self.box = box
            super.init(frame: .zero)
            isUserInteractionEnabled = false
        }
        required init?(coder: NSCoder) { fatalError() }
        override func didMoveToWindow() {
            super.didMoveToWindow()
            var v = superview
            while let view = v, !(view is UIScrollView) { v = view.superview }
            box.view = v as? UIScrollView
        }
    }
}

private struct ScrollLayout: Equatable {
    let contentHeight: CGFloat
    let containerHeight: CGFloat
    let topInset: CGFloat
    let bottomInset: CGFloat
}

extension View {
    func followsEnd<Bottom: View>(
        id: String,
        ready: Bool,
        atEnd: Binding<Bool>,
        position: Binding<ScrollPosition>,
        scrollBox: ConversationScrollBox,
        @ViewBuilder bottom: @escaping () -> Bottom
    ) -> some View {
        modifier(FollowsEnd(id: id, ready: ready, atEnd: atEnd, position: position, scrollBox: scrollBox, bottom: bottom))
    }
}

private struct FollowsEnd<Bottom: View>: ViewModifier {
    let id: String
    let ready: Bool
    @Binding var atEnd: Bool
    @Binding var position: ScrollPosition
    let scrollBox: ConversationScrollBox
    let bottom: () -> Bottom
    @State private var away = false
    @State private var scrolling = false
    @State private var positioned = false
    @State private var layout = ScrollLayout(contentHeight: 0, containerHeight: 0, topInset: 0, bottomInset: 0)
    @State private var alignTask: Task<Void, Never>?
    @Environment(\.accessibilityReduceMotion) private var still

    private let endThreshold: CGFloat = 24

    private var userInteracting: Bool {
        if scrolling { return true }
        guard let sv = scrollBox.view else { return false }
        return sv.isTracking || sv.isDragging || sv.isDecelerating
    }

    func body(content: Content) -> some View {
        content
            .scrollPosition($position)
            .defaultScrollAnchor(.bottom, for: .initialOffset)
            .onChange(of: id) { _, _ in
                reset()
            }
            .onChange(of: ready, initial: true) { _, isReady in
                if isReady && !positioned {
                    scheduleInitialAlign()
                }
            }
            .onScrollPhaseChange { oldPhase, newPhase, context in
                let active = newPhase == .interacting || newPhase == .decelerating
                let wasActive = oldPhase == .interacting || oldPhase == .decelerating
                scrolling = active
                guard active || wasActive else { return }
                let gap = fromEnd(context.geometry)
                if gap > endThreshold {
                    detachFromEnd()
                } else if !active {
                    atEnd = true
                }
            }
            .onScrollGeometryChange(for: Bool.self) { fromEnd($0) <= endThreshold } action: { _, near in
                guard userInteracting else { return }
                if !near {
                    detachFromEnd()
                } else {
                    atEnd = true
                }
            }
            .onScrollGeometryChange(for: ScrollLayout.self) { g in
                ScrollLayout(
                    contentHeight: g.contentSize.height,
                    containerHeight: g.containerSize.height,
                    topInset: g.contentInsets.top,
                    bottomInset: g.contentInsets.bottom
                )
            } action: { _, next in
                layout = next
                guard atEnd, !userInteracting else { return }
                if ready {
                    if !positioned {
                        scheduleInitialAlign()
                    } else {
                        alignToBottom()
                    }
                } else if next.contentHeight > 0 {
                    alignToBottom()
                }
            }
            .onScrollGeometryChange(for: Bool.self, of: scrolledAway) { _, far in away = far }
            .onDisappear {
                alignTask?.cancel()
                alignTask = nil
            }
            .safeAreaInset(edge: .bottom) {
                VStack(spacing: 0) { bottom() }
                    .frame(maxWidth: .infinity)
                    .toEndButton(away && !atEnd) {
                        atEnd = true
                        positioned = true
                        alignTask?.cancel()
                        alignTask = nil
                        withAnimation(still ? nil : .smooth) { position.scrollTo(edge: .bottom) }
                    }
            }
    }

    private func reset() {
        alignTask?.cancel()
        alignTask = nil
        atEnd = true
        away = false
        scrolling = false
        positioned = false
        if ready { scheduleInitialAlign() }
    }

    private func detachFromEnd() {
        atEnd = false
        positioned = true
        alignTask?.cancel()
        alignTask = nil
    }

    private func scheduleInitialAlign() {
        guard atEnd, !userInteracting else { return }
        alignTask?.cancel()
        alignTask = Task { @MainActor in
            alignToBottom()
            for delay in [0, 32, 100] {
                if delay > 0 {
                    try? await Task.sleep(for: .milliseconds(delay))
                } else {
                    await Task.yield()
                }
                guard !Task.isCancelled, atEnd, !userInteracting else { return }
                alignToBottom()
            }
            if !Task.isCancelled, ready, layout.containerHeight > 0 {
                positioned = true
            }
        }
    }

    private func alignToBottom() {
        guard atEnd, !userInteracting else { return }
        if let sv = scrollBox.view {
            sv.layoutIfNeeded()
            guard !sv.isTracking, !sv.isDragging, !sv.isDecelerating else { return }
            let content = max(sv.contentSize.height, layout.contentHeight)
            let targetY = max(-sv.adjustedContentInset.top, content + sv.adjustedContentInset.bottom - sv.bounds.height)
            if sv.bounds.height > 0, abs(sv.contentOffset.y - targetY) > 1 {
                sv.setContentOffset(CGPoint(x: sv.contentOffset.x, y: targetY), animated: false)
            }
        } else {
            position.scrollTo(edge: .bottom)
        }
    }
}

private func sameFailure(_ a: Failure, _ b: Failure) -> Bool { a.kind == b.kind && a.message == b.message }

private let adviceResumes: Set<FailureKind> = [.rateLimit, .providerError, .network, .contextOverflow, .resumeRejected, .agentCrashed, .stalled]

struct FailedView: View {
    @Environment(AppModel.self) private var model
    let session: Session
    let failure: Failure?
    let grant: String

    var body: some View {
        FailedCard(headline: failureHeadline(failure?.kind ?? .unspecified, agent: agentTitle(session.agent)),
                   hints: [advice, String(localized: "When \(AgentLogo.name(grant)) sends another message, it picks up from here; the earlier changes are all still there.")],
                   message: failure?.message ?? session.error) {
            if session.isArchivable {
                Button("Archive") { Task { await model.setArchived(session, true) } }
            }
        }
        .accessibilityIdentifier("session-failed")
    }

    private var advice: String {
        guard let failure, !adviceResumes.contains(failure.kind) else { return "" }
        return failureAdvice(failure)
    }
}

struct FailedCard<Actions: View>: View {
    let headline: String
    let hints: [String]
    let message: String
    @ViewBuilder let actions: () -> Actions
    @State private var open = false

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Image(systemName: "exclamationmark.circle").foregroundStyle(.red)
            VStack(alignment: .leading, spacing: 3) {
                Text(headline)
                    .font(.subheadline.weight(.semibold))
                ForEach(hints.filter { !$0.isEmpty }, id: \.self) { Text($0) }
                    .font(.footnote).foregroundStyle(.secondary)
                HStack(spacing: 14) {
                    if !message.isEmpty {
                        Button { withAnimation(.snappy(duration: 0.25)) { open.toggle() } } label: {
                            HStack(spacing: 3) {
                                Text("Details")
                                Image(systemName: "chevron.right").font(.caption2.weight(.semibold)).rotationEffect(.degrees(open ? 90 : 0))
                            }
                        }
                        .accessibilityIdentifier("failure-details")
                    }
                    actions()
                }
                .font(.footnote).foregroundStyle(.secondary).buttonStyle(.plain)
                .padding(.top, 6)
                if open { RawMessage(text: message).padding(.top, 4) }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.horizontal, 14).padding(.vertical, 12)
        .background(Palette.fill, in: RoundedRectangle(cornerRadius: 13, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 13, style: .continuous).strokeBorder(Palette.separator, lineWidth: 0.5))
        .padding(.top, 6)
        .accessibilityElement(children: .contain)
    }
}

struct FailLine: View {
    let title: String
    let message: String
    @State private var open = false

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Button {
                if !message.isEmpty { withAnimation(.snappy(duration: 0.25)) { open.toggle() } }
            } label: {
                HStack(spacing: 7) {
                    Image(systemName: "exclamationmark.circle")
                    Text(title).multilineTextAlignment(.leading)
                    if !message.isEmpty {
                        Image(systemName: "chevron.right").font(.caption2.weight(.semibold)).rotationEffect(.degrees(open ? 90 : 0))
                    }
                }
                .font(.footnote).foregroundStyle(.red)
            }
            .buttonStyle(.plain)
            if open { RawMessage(text: message).padding(.leading, 21) }
        }
    }
}

private struct RawMessage: View {
    let text: String

    var body: some View {
        Text(text)
            .font(.caption.monospaced()).foregroundStyle(.secondary)
            .textSelection(.enabled)
            .padding(.horizontal, 10).padding(.vertical, 8)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Palette.code, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 8, style: .continuous).strokeBorder(Palette.separator, lineWidth: 0.5))
    }
}

struct Breathing<Content: View>: View {
    @ViewBuilder let content: () -> Content
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var dim = false

    var body: some View {
        content()
            .opacity(dim ? 0.45 : 1)
            .onAppear {
                guard !reduceMotion else { return }
                withAnimation(.easeInOut(duration: 1.2).repeatForever(autoreverses: true)) { dim = true }
            }
    }
}
