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

    private func content(_ s: Session) -> some View {
        let grant = model.grantLabel(s)
        return ScrollView {
            // A plain stack: a lazy one guesses the height of the rows it has
            // not laid out, and opened at the bottom it left a gap under the
            // last message until those guesses were corrected.
            VStack(alignment: .leading, spacing: 13) {
                if s.status == .archived { archivedNote(s) }
                ForEach(Array(steps.enumerated()), id: \.element.id) { i, step in
                    StepView(step: step, session: s, grant: grant, previousTurn: previousTurn(steps, before: i))
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
        }
        // Open at the end, and stay there as it grows unless the owner scrolled up.
        .defaultScrollAnchor(.bottom, for: .initialOffset)
        .defaultScrollAnchor(atEnd ? .bottom : .top, for: .sizeChanges)
        .onScrollGeometryChange(for: Bool.self) { g in g.visibleRect.maxY > g.contentSize.height - 80 } action: { _, end in atEnd = end }
        .overlay { // in the middle of the screen, not where the scroll ends
            if events.isEmpty && !caughtUp(s) { unread(s) }
        }
        .scrollDismissesKeyboard(.interactively)
        .safeAreaInset(edge: .bottom) {
            if let q = s.pendingQuestion {
                QuestionPanel(session: s, question: q).id(q.questionID)
            } else if s.isInterruptible {
                controlBar(s)
            }
        }
        .toolbar {
            ToolbarItem(placement: .principal) {
                VStack(spacing: 0) {
                    Text(s.displayTitle).font(.subheadline.weight(.semibold)).lineLimit(1)
                    if model.shownSync != nil {
                        SyncLine(small: true) // in place of the line below while what shows is not current
                    } else {
                        Text("\(grant) · \(model.machineName(s.machineID))/\(s.projectID)").font(.caption2).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
            }
            if model.path.isEmpty { ToolbarItem(placement: .topBarLeading) { MenuButton() } }
            ToolbarItemGroup(placement: .topBarTrailing) {
                Button("Session Info", systemImage: "info.circle") { model.sheet = .sessionInfo(s.sessionID) }
                    .accessibilityIdentifier("session-info")
                Menu {
                    Button("Copy Session ID", systemImage: "doc.on.doc") {
                        UIPasteboard.general.string = s.sessionID
                        model.show(String(localized: "Copied the session ID"))
                    }
                    SessionActions(session: s, renaming: $renaming, deleting: $deleting)
                } label: {
                    Image(systemName: "ellipsis")
                }
                .accessibilityLabel("More")
                .accessibilityIdentifier("session-more")
            }
        }
        .sessionDialogs(s, renaming: $renaming, deleting: $deleting)
        .confirmationDialog("Interrupt this turn?", isPresented: $confirmInterrupt, titleVisibility: .visible) {
            Button("Interrupt", role: .destructive) { Task { await model.interrupt(s) } }
                .accessibilityIdentifier("interrupt-confirm")
        } message: {
            Text("The session stays; when \(grant) sends another message, it continues.")
        }
    }

    /// The archived note, with Unarchive.
    private func archivedNote(_ s: Session) -> some View {
        HStack(spacing: 8) {
            Image(systemName: "archivebox")
            Text("Archived: read-only; the worktree is released and the changes stay on the branch.")
                .frame(maxWidth: .infinity, alignment: .leading)
            Button("Unarchive") { Task { await model.setArchived(s, false) } }
                .foregroundStyle(.tint)
        }
        .font(.footnote).foregroundStyle(.secondary)
        .padding(.horizontal, 12).padding(.vertical, 8)
        .background(Palette.fill, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        .padding(.bottom, 3)
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

    /// How the conversation ends: the failure it stopped on, or that the turn
    /// is done (an interrupted turn says so in the conversation).
    @ViewBuilder private func ending(_ s: Session, grant: String) -> some View {
        let failure = s.progress.hasLastFailure ? s.progress.lastFailure : nil
        if s.isFailed, failure != nil || !s.error.isEmpty {
            FailedView(session: s, failure: failure, grant: grant)
        } else if s.status == .idle, !isInterruption(failure), !steps.isEmpty {
            Label("This turn is done. When \(grant) sends another message, it continues in this session.", systemImage: "checkmark")
                .font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center)
                .frame(maxWidth: .infinity)
                .padding(.top, 5)
        }
    }

    /// The bottom of a live turn: the coding agent at work and how long, or
    /// what the session is doing, and Interrupt.
    private func controlBar(_ s: Session) -> some View {
        HStack(spacing: 10) {
            BreathingLogo(agent: s.agent)
            Group {
                if s.status == .running {
                    Text("\(agentTitle(s.agent)) is working").fontWeight(.semibold)
                    if let start = s.turnStartedDate { Text(start, style: .timer).monospacedDigit().foregroundStyle(.secondary) }
                } else {
                    Text(statusTitle(s.status)).fontWeight(.semibold)
                    if phaseText(s) != statusTitle(s.status) { Text(phaseText(s)).foregroundStyle(.secondary) }
                }
            }
            .lineLimit(1)
            Spacer(minLength: 0)
            Button {
                confirmInterrupt = true
            } label: {
                Label("Interrupt", systemImage: "stop.fill")
            }
            .buttonStyle(.glass)
            .accessibilityIdentifier("interrupt")
        }
        .font(.subheadline)
        .padding(.leading, 16).padding(.trailing, 8).padding(.vertical, 8)
        .glassEffect(.regular, in: .capsule)
        .padding(.horizontal, 16).padding(.bottom, 4)
    }

    /// When the turn before step i started, for the time lines.
    private func previousTurn(_ steps: [Step], before i: Int) -> Date? {
        for s in steps[..<i].reversed() {
            if case .item(.turn(_, _, let at)) = s { return at }
        }
        return nil
    }

    /// Shows what is kept of the conversation, then reads the events after
    /// it and follows new ones while the page is shown. Events that come
    /// together are shown together: a long conversation read at once is laid
    /// out a few times, not once per event.
    private func follow() async {
        events = model.conversations.events(id)
        draft = ""
        rebuild(running: model.session(id)?.isActive ?? false)
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

/// One row of the conversation, as on the Mac: the owner on the right in a
/// dark bubble, the agent's words across the page, its tool calls as quiet
/// grey lines folded until opened.
struct StepView: View {
    @Environment(AppModel.self) private var model
    let step: Step
    let session: Session
    let grant: String
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

    @ViewBuilder private func row(_ item: TranscriptItem) -> some View {
        switch item {
        case .turn(_, _, let at):
            // A time line before a turn, for the first one and after a pause.
            if let at, let line = turnTimeLine(at, previous: previousTurn, now: model.now) {
                Text(line).font(.caption).foregroundStyle(.tertiary).frame(maxWidth: .infinity)
                    .padding(.top, 8)
            }
        case .user(_, let text, let at):
            // The one who started the session speaks on the right; no name (the title says who).
            Text(text)
                .lineSpacing(3)
                .foregroundStyle(Palette.bubbleText)
                .padding(.horizontal, 14).padding(.vertical, 9)
                .background(Palette.bubble, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                .contentShape(.contextMenuPreview, RoundedRectangle(cornerRadius: 18, style: .continuous))
                .contextMenu { copy(text, sent: at) }
                .padding(.leading, 48)
                .frame(maxWidth: .infinity, alignment: .trailing)
                .padding(.top, 8).padding(.bottom, 8)
        case .agent(_, let text):
            MarkdownText(text: text)
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
                .contextMenu { copy(text, sent: nil) }
                .padding(.bottom, 8)
        case .message(_, let incoming, let name, let text, let queued):
            // A message between the session and another of the owner's agents: who, and what became of it.
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 6) {
                    Image(systemName: "envelope").foregroundStyle(.secondary)
                    Text(incoming ? String(localized: "Message from \(name)") : String(localized: "Message to \(name)")).fontWeight(.semibold)
                    if queued {
                        Spacer(minLength: 8)
                        Text("Waits for a turn of its own").foregroundStyle(.secondary)
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
                let byOwner = end.failure.message == "interrupted by owner"
                Text(byOwner ? String(localized: "You interrupted this turn. The session stays; when \(grant) sends another message, it continues.")
                    : String(localized: "This turn was interrupted. The session stays; when \(grant) sends another message, it continues."))
                    .font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center)
                    .padding(.horizontal, 12).padding(.vertical, 5)
                    .background(Palette.fill, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                    .frame(maxWidth: .infinity)
            } else if end.isError, !(session.isFailed && sameFailure(session.progress.lastFailure, end.failure)) {
                // The session's current failure is told once, at the end; an earlier one (a later turn went on) in one quiet line.
                FailLine(title: String(localized: "Turn \(end.turn) failed: \(failureTitle(end.failure.kind))"), message: end.failure.message)
            }
            // A turn that went well needs no line: the next message shows it went on.
        case .error(_, let e):
            // The session's current failure is told once, at the end.
            if !(session.isFailed && e.hasFailure && sameFailure(session.progress.lastFailure, e.failure)) {
                FailLine(title: e.hasFailure ? String(localized: "Failed: \(failureTitle(e.failure.kind))") : String(localized: "Failed"), message: e.message)
            }
        case .tool:
            EmptyView()
        }
    }

    /// The long-press menu of a message: Copy, and when the owner sent it.
    @ViewBuilder private func copy(_ text: String, sent: Date?) -> some View {
        Button("Copy", systemImage: "doc.on.doc") { UIPasteboard.general.string = text }
        if let sent { Text("Sent \(agoText(sent, now: model.now))") }
    }

    private func asked(_ q: Question) -> AttributedString {
        var s = AttributedString(String(localized: "Agent asked:"))
        s.inlinePresentationIntent = .stronglyEmphasized
        return s + AttributedString(" " + q.items.map(\.text).joined(separator: " "))
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
    @State private var open = false

    var body: some View {
        let message = failure?.message ?? session.error
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Image(systemName: "exclamationmark.circle").foregroundStyle(.red)
            VStack(alignment: .leading, spacing: 3) {
                Text(failureHeadline(failure?.kind ?? .unspecified, agent: agentTitle(session.agent)))
                    .font(.subheadline.weight(.semibold))
                Group {
                    if !advice.isEmpty { Text(advice) }
                    Text("When \(AgentLogo.name(grant)) sends another message, it picks up from here; the earlier changes are all still there.")
                }
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
                    if session.isArchivable {
                        Button("Archive") { Task { await model.setArchived(session, true) } }
                    }
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
        .accessibilityIdentifier("session-failed")
    }

    /// What to do, when it is the owner's to fix (the advice of the others
    /// only says to send another message, which the card says anyway).
    private var advice: String {
        guard let failure, !adviceResumes.contains(failure.kind) else { return "" }
        return failureAdvice(failure)
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

/// The coding agent at work: its own logo, breathing.
private struct BreathingLogo: View {
    let agent: String
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var dim = false

    var body: some View {
        AgentLogo(id: agent, size: 20)
            .opacity(dim ? 0.45 : 1)
            .onAppear {
                guard !reduceMotion else { return }
                withAnimation(.easeInOut(duration: 1.2).repeatForever(autoreverses: true)) { dim = true }
            }
    }
}
