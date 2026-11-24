import HermeshProto
import HermeshSessions
import SwiftUI

/// One session: its conversation, newest at the bottom; while a turn is
/// live, a control bar (or the agent's question) floats over the bottom.
struct SessionView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var scenePhase
    let id: String
    @State private var events: [SessionEvent] = []
    /// The conversation's rows, made again only when its events change.
    @State private var steps: [Step] = []
    @State private var stepsRunning = false
    /// The reply the agent is still writing; a stored event ends it.
    @State private var draft = ""
    /// Whether the conversation shows its end, so it follows what comes.
    @State private var atEnd = true
    @State private var position = ScrollPosition(edge: .bottom)
    @State private var scrollBox = ConversationScrollBox()
    /// Whether `follow()` has loaded the cached history for the current session `id`.
    @State private var initialized = false
    @State private var confirmInterrupt = false
    @State private var renaming = false
    @State private var deleting = false
    /// The conversation has taken long enough to say why it isn't here.
    @State private var slow = false

    var body: some View {
        Group {
            if let s = model.session(id) {
                content(s)
                    .onChange(of: s.isActive) { _, running in rebuild(running: running) }
                    // The agent asks while the owner looks.
                    .onChange(of: s.pendingQuestion?.questionID) { _, asked in if asked != nil { Haptic.asks.play() } }
                    // A turn ends while the owner looks: done, or failed; one stopped on purpose plays none.
                    .onChange(of: s.status) { was, now in
                        guard [.queued, .preparing, .running, .waitingInput].contains(was), now == .idle || now == .crashed,
                              let s = model.session(id) else { return }
                        if s.isFailed { Haptic.error.play() } else if !s.progress.hasLastFailure { Haptic.success.play() }
                    }
            } else {
                PageLoader(text: "Reading the session…", bare: true)
            }
        }
        .navigationBarTitleDisplayMode(.inline)
        .task(id: id) { await follow() }
        .task(id: id) {
            slow = false
            try? await Task.sleep(for: .seconds(15))
            if !Task.isCancelled { slow = true }
        }
        .onDisappear { model.conversations.keep(id, events) }
        .onChange(of: scenePhase) { _, phase in if phase == .background { model.conversations.keep(id, events) } }
    }

    /// The rows, from the events (and whether a turn runs, which tells a
    /// tool call that is still going).
    private func rebuild(running: Bool) {
        stepsRunning = running
        steps = groupSteps(transcript(events, running: running))
    }

    /// Whether the conversation holds every event the session has had.
    private func caughtUp(_ s: Session) -> Bool { (events.last?.seq ?? 0) >= s.progress.lastEventSeq }

    /// Whether the initial history for this session is loaded and ready for first-time bottom positioning.
    private func historyReady(_ s: Session) -> Bool {
        guard initialized else { return false }
        if caughtUp(s) { return true }
        return !events.isEmpty && unreadable(s) != nil
    }

    private func content(_ s: Session) -> some View {
        let grant = model.grantLabel(s)
        return ScrollView {
            // A plain stack: a lazy one guesses the height of the rows it has
            // not laid out, and opened at the bottom it left a gap under the
            // last message until those guesses were corrected.
            VStack(alignment: .leading, spacing: 13) {
                if s.status == .archived { ArchivedNote(session: s) }
                ForEach(Array(steps.enumerated()), id: \.element.id) { i, step in
                    StepView(step: step, session: s, previousTurn: previousTurn(steps, before: i))
                }
                if !draft.isEmpty {
                    DraftMarkdownText(text: draft).frame(maxWidth: .infinity, alignment: .leading)
                        .accessibilityElement(children: .combine)
                        .accessibilityIdentifier("session-draft")
                }
                ending(s, grant: grant)
                if !events.isEmpty { stale(s) }
            }
            .padding(.horizontal, 20)
            .padding(.top, 13).padding(.bottom, 21)
            .background(ConversationScrollFinder(box: scrollBox))
        }
        .followsEnd(id: id, ready: historyReady(s), atEnd: $atEnd, position: $position, scrollBox: scrollBox) {
            if let q = s.pendingQuestion {
                QuestionPanel(session: s, question: q).id(q.questionID)
            } else if s.isInterruptible {
                controlBar(s)
            }
        }
        .overlay { // in the middle of the screen, not where the scroll ends
            if events.isEmpty && !caughtUp(s) { unread(s) }
        }
        .scrollDismissesKeyboard(.interactively)
        .toolbar { SessionToolbar(session: s, sync: true, renaming: $renaming, deleting: $deleting) }
        .sessionDialogs(s, renaming: $renaming, deleting: $deleting)
        .confirmationDialog("Interrupt this turn?", isPresented: $confirmInterrupt, titleVisibility: .visible) {
            Button("Interrupt", role: .destructive) { Task { await model.interrupt(s) } }
                .accessibilityIdentifier("interrupt-confirm")
        } message: {
            Text("The session stays; when \(grant) sends another message, it continues.")
        }
    }

    /// Why the conversation can't be read now: it is kept on the machine
    /// the session runs on, and this iPhone can't reach Hermesh (or, a
    /// while after the page opened, still hasn't), or that
    /// machine is offline, or (a while after the page opened, still behind)
    /// it doesn't answer. follow() keeps trying meanwhile.
    private enum Unreadable { case notConnected, offline(Machine), noAnswer }

    private func unreadable(_ s: Session) -> Unreadable? {
        // While this iPhone isn't current itself, the machine is never the one to blame.
        switch model.shownSync {
        case .offline: return .notConnected
        case .connecting: return slow ? .notConnected : nil
        default: break
        }
        if let m = model.machines.first(where: { $0.machineID == s.machineID }), !m.online { return .offline(m) }
        return slow && !caughtUp(s) ? .noAnswer : nil
    }

    /// In place of a conversation not read yet: why, or the wait.
    @ViewBuilder private func unread(_ s: Session) -> some View {
        let name = model.machineName(s.machineID)
        switch unreadable(s) {
        case .notConnected:
            ContentUnavailableView {
                Label("Can’t reach Hermesh", systemImage: "wifi.slash")
            } description: {
                Text("This conversation is kept on \(name); it shows here once this iPhone reconnects.")
            }
            .accessibilityIdentifier("session-unread")
        case .offline(let m):
            ContentUnavailableView {
                Label("\(name) is offline", systemImage: "desktopcomputer")
            } description: {
                if m.hasLastSeen {
                    Text("This conversation is kept on that machine; it shows here once the machine reconnects. Last online \(agoText(m.lastSeen.date, now: model.now)).")
                } else {
                    Text("This conversation is kept on that machine; it shows here once the machine reconnects.")
                }
            }
            .accessibilityIdentifier("session-unread")
        case .noAnswer:
            ContentUnavailableView {
                Label("\(name) isn’t responding", systemImage: "desktopcomputer.trianglebadge.exclamationmark")
            } description: {
                Text("This conversation is kept on that machine. Retrying; it shows here once the machine answers.")
            }
            .accessibilityIdentifier("session-unread")
        case nil:
            PageLoader(text: "Reading the conversation…", bare: true)
        }
    }

    /// Under a conversation kept from before that can't be brought up to
    /// date now: a quiet line that says why it may be old.
    @ViewBuilder private func stale(_ s: Session) -> some View {
        let name = model.machineName(s.machineID)
        if let why = unreadable(s) {
            Group {
                switch why {
                case .notConnected: Label("Can’t reach Hermesh; this is what was last read here.", systemImage: "wifi.slash")
                case .offline: Label("\(name) is offline; this is what was last read from it.", systemImage: "desktopcomputer")
                case .noAnswer: Label("\(name) isn’t responding; this is what was last read from it.", systemImage: "desktopcomputer.trianglebadge.exclamationmark")
                }
            }
            .font(.footnote).foregroundStyle(.secondary)
            .frame(maxWidth: .infinity)
            .accessibilityIdentifier("session-stale")
        }
    }

    /// How the conversation ends: the failure it stopped on. A turn that went well needs no line: the box under it
    /// shows it can go on (an interrupted turn says so in the conversation).
    @ViewBuilder private func ending(_ s: Session, grant: String) -> some View {
        let failure = s.progress.hasLastFailure ? s.progress.lastFailure : nil
        if s.isFailed, failure != nil || !s.error.isEmpty {
            FailedView(session: s, failure: failure, grant: grant)
        }
    }

    /// The bottom of a live turn: the coding agent at work and how long, or
    /// what the session is doing, and Interrupt.
    private func controlBar(_ s: Session) -> some View {
        LiveBar(stop: "Interrupt", stopID: "interrupt", action: { confirmInterrupt = true }) {
            Breathing { AgentLogo(id: s.agent, size: 20) }
        } words: {
            if s.status == .running {
                Text("\(agentTitle(s.agent)) is working").fontWeight(.semibold)
                if let start = s.turnStartedDate { Text(start, style: .timer).monospacedDigit().foregroundStyle(.secondary) }
            } else {
                Text(statusTitle(s.status)).fontWeight(.semibold)
                if phaseText(s) != statusTitle(s.status) { Text(phaseText(s)).foregroundStyle(.secondary) }
            }
        }
    }

    /// Shows what is kept of the conversation, then reads the events after
    /// it and follows new ones while the page is shown. Events that come
    /// together are shown together: a long conversation read at once is laid
    /// out a few times, not once per event.
    private func follow() async {
        initialized = false
        atEnd = true
        events = model.conversations.events(id)
        draft = ""
        rebuild(running: model.session(id)?.isActive ?? false)
        initialized = true
        guard let api = model.api else { return }
        var pending: [SessionEvent] = []
        var flush: Task<Void, Never>?
        func show() {
            flush = nil
            guard !pending.isEmpty else { return }
            events.append(contentsOf: pending)
            pending = []
            draft = ""
            rebuild(running: model.session(id)?.isActive ?? stepsRunning)
        }
        while !Task.isCancelled {
            do {
                for try await e in api.events(id, after: (pending.last ?? events.last)?.seq ?? 0) {
                    if case .agentDraft(let d) = e.kind {
                        show() // a draft follows the events before it
                        draft = d.whole ? d.text : draft + d.text
                        continue
                    }
                    if let last = pending.last ?? events.last, e.seq <= last.seq { continue }
                    pending.append(e)
                    if flush == nil {
                        flush = Task {
                            try? await Task.sleep(for: .milliseconds(50))
                            show()
                        }
                    }
                }
            } catch {}
            flush?.cancel()
            show()
            try? await Task.sleep(for: .seconds(1))
        }
        flush?.cancel()
        show()
    }
}

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
            // A message between the session and another of the owner's agents: who, and what became of it.
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
            // Unanswered, it is the panel at the bottom; a settled one stays here.
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
                // The session's current failure is told once, at the end; an earlier one (a later turn went on) in one quiet line.
                FailLine(title: String(localized: "Turn \(end.turn) failed: \(failureTitle(end.failure.kind))"), message: end.failure.message)
            }
            // A turn that went well needs no line: the next message shows it went on.
        case .error(_, let e):
            // Stopped before its turn began (queued or starting): an interruption, not a failure.
            if e.hasFailure, isInterruption(e.failure) {
                interruptNote(e.failure)
            } else if !(session.isFailed && e.hasFailure && sameFailure(session.progress.lastFailure, e.failure)) {
                // The session's current failure is told once, at the end.
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
                .glassEffect(.regular.interactive(), in: .capsule)
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
    /// A conversation's scrolling, as a session's page and a sub-agent's record have it: once history is loaded and
    /// laid out, it positions at the latest content at the bottom and stays there as updates arrive unless the owner
    /// actively scrolls up; `bottom` stands over its bottom edge, and scrolled well up, a button over that takes them
    /// back down.
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
    /// Whether it was scrolled well up from its end, where a button brings it back.
    @State private var away = false
    /// The owner is scrolling (dragging, or the list still moving from it).
    @State private var scrolling = false
    /// Whether initial bottom positioning after history loading and layout has settled.
    @State private var positioned = false
    @State private var layout = ScrollLayout(contentHeight: 0, containerHeight: 0, topInset: 0, bottomInset: 0)
    @State private var alignTask: Task<Void, Never>?
    @Environment(\.accessibilityReduceMotion) private var still

    /// Distance from the bottom edge that still counts as staying at the end.
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
                    // Keep the bottom in view while multi-batch history is still arriving unless the owner scrolled up.
                    alignToBottom()
                }
            }
            .onScrollGeometryChange(for: Bool.self, of: scrolledAway) { _, far in away = far }
            .onDisappear {
                alignTask?.cancel()
                alignTask = nil
            }
            .safeAreaInset(edge: .bottom) {
                // A stack, there even with nothing in it, for the button back to the end to stand on.
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

    /// Waits for history layout passes (text wrapping, Markdown, safeAreaInset) to settle,
    /// then positions to the latest content at the bottom once without overriding user scroll.
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
            // SwiftUI knows content grew (a row, the line saying it works) before the scroll view's contentSize says so:
            // aligned to the older size, the end stayed under the box, the more so with the keyboard up.
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

/// Failures whose advice already says to send another message.
private let adviceResumes: Set<FailureKind> = [.rateLimit, .providerError, .network, .contextOverflow, .resumeRejected, .agentCrashed, .stalled]

/// A session that stopped on a failure, where the agent's reply would be: a
/// quiet card telling what happened to the coding agent, what to do when it
/// is the owner's to fix, and that the agent driving the session goes on
/// from here; then Details (the agent's own words, folded) and Archive.
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

    /// What to do, when it is the owner's to fix (the advice of the others
    /// only says to send another message, which the card says anyway).
    private var advice: String {
        guard let failure, !adviceResumes.contains(failure.kind) else { return "" }
        return failureAdvice(failure)
    }
}

/// Where a conversation stopped on a failure: a quiet card with what happened, what to do (`hints`), then Details
/// (`message`, folded) and `actions`.
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

/// An earlier failure in the conversation: one red line, its words folded.
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

/// A failure in the agent's own words: the code face on the code ground.
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

/// Who is at work, breathing: a coding agent's logo, Her's mark.
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
