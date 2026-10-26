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
    @State private var open: Set<UInt64> = []
    @State private var confirmInterrupt = false
    @State private var renaming = false
    @State private var deleting = false

    var body: some View {
        Group {
            if let s = model.session(id) {
                content(s)
                    .onChange(of: s.isActive) { _, running in rebuild(running: running) }
            } else {
                PageLoader(text: "Reading the session…").frame(maxHeight: .infinity)
            }
        }
        .navigationBarTitleDisplayMode(.inline)
        .task(id: id) { await follow() }
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
            LazyVStack(alignment: .leading, spacing: 14) {
                if s.status == .archived {
                    Label("Archived: read-only; the worktree is released and the changes stay on the branch.", systemImage: "archivebox")
                        .font(.footnote).foregroundStyle(.secondary)
                }
                if events.isEmpty && !caughtUp(s) {
                    PageLoader(text: "Reading the conversation…").padding(.top, 60)
                }
                ForEach(Array(steps.enumerated()), id: \.element.id) { i, step in
                    StepView(step: step, session: s, grant: grant, previousTurn: previousTurn(steps, before: i), open: $open)
                }
                if !draft.isEmpty {
                    Text(agentMarkdown(draft)).frame(maxWidth: .infinity, alignment: .leading)
                        .accessibilityIdentifier("session-draft")
                }
                ending(s, grant: grant)
            }
            .padding(.horizontal, 18)
            .padding(.vertical, 12)
        }
        .defaultScrollAnchor(.bottom)
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
                    Text("\(grant) · \(model.machineName(s.machineID))/\(s.projectID)").font(.caption2).foregroundStyle(.secondary).lineLimit(1)
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

    @ViewBuilder private func ending(_ s: Session, grant: String) -> some View {
        let failure = s.progress.hasLastFailure ? s.progress.lastFailure : nil
        if s.isFailed {
            VStack(alignment: .leading, spacing: 4) {
                Text(failure.map { failureTitle($0.kind) } ?? String(localized: "Failed")).foregroundStyle(.red)
                let detail = [failure?.message ?? s.error, failure.map(failureAdvice) ?? ""].filter { !$0.isEmpty }.joined(separator: " ")
                if !detail.isEmpty { Text(detail).font(.subheadline).foregroundStyle(.secondary) }
            }
            .accessibilityIdentifier("session-failed")
        } else if s.status == .idle, !events.isEmpty {
            let interrupted = isInterruption(failure)
            Label(interrupted ? String(localized: "This turn was interrupted. The session stays; when \(grant) sends another message, it continues.")
                : String(localized: "This turn is done. When \(grant) sends another message, it continues in this session."),
                  systemImage: interrupted ? "pause.circle" : "checkmark")
                .font(.footnote).foregroundStyle(.secondary).frame(maxWidth: .infinity)
                .padding(.top, 6)
        }
    }

    private func controlBar(_ s: Session) -> some View {
        HStack(spacing: 12) {
            Circle().fill(.green).frame(width: 9, height: 9)
            VStack(alignment: .leading, spacing: 1) {
                Text("\(agentTitle(s.agent)) is working").font(.subheadline.weight(.semibold))
                HStack(spacing: 4) {
                    Text("Turn \(s.currentTurn)")
                    if let start = s.turnStartedDate { Text("·"); Text(start, style: .timer).monospacedDigit() }
                }
                .font(.caption).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Button {
                confirmInterrupt = true
            } label: {
                Label("Interrupt", systemImage: "stop.fill")
            }
            .buttonStyle(.glass)
            .accessibilityIdentifier("interrupt")
        }
        .padding(.leading, 20).padding(.trailing, 8).padding(.vertical, 8)
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

/// One row of the conversation.
struct StepView: View {
    @Environment(AppModel.self) private var model
    let step: Step
    let session: Session
    let grant: String
    let previousTurn: Date?
    @Binding var open: Set<UInt64>

    var body: some View {
        switch step {
        case .tools(let seq, let calls):
            tools(seq, calls)
        case .item(let item):
            switch item {
            case .turn(_, _, let at):
                if let at, let line = turnTimeLine(at, previous: previousTurn, now: model.now) {
                    Text(line).font(.caption).foregroundStyle(.secondary).frame(maxWidth: .infinity)
                }
            case .user(_, let text):
                VStack(alignment: .leading, spacing: 5) {
                    HStack(spacing: 6) {
                        GrantAvatar(label: grant, size: 18)
                        Text(grant).font(.caption).foregroundStyle(.secondary)
                    }
                    Text(text).textSelection(.enabled)
                }
                .padding(.horizontal, 15).padding(.vertical, 12)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 22, style: .continuous))
            case .agent(_, let text):
                Text(agentMarkdown(text)).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading)
            case .message(_, let incoming, let name, let text, let queued):
                // A message between the session and another of the owner's agents.
                VStack(alignment: .leading, spacing: 5) {
                    HStack(spacing: 6) {
                        Image(systemName: "envelope").font(.caption)
                        Text(incoming ? String(localized: "Message from \(name)") : String(localized: "Message to \(name)")).font(.caption.weight(.semibold))
                        if queued {
                            Spacer()
                            Text("Waits for a turn of its own").font(.caption).foregroundStyle(.secondary)
                        }
                    }
                    Text(agentMarkdown(text)).textSelection(.enabled)
                }
                .padding(.horizontal, 13).padding(.vertical, 10)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            case .question(_, let q, let r):
                // Unanswered, it is the panel at the bottom.
                if let r {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("The agent asked: \(q.items.map(\.text).joined(separator: " "))").font(.subheadline)
                        Text(resolutionTitle(r, name: { _ in grant })).font(.subheadline).foregroundStyle(.secondary)
                    }
                }
            case .turnEnd(_, let end, _):
                let files = end.hasDiffStat ? Int(end.diffStat.filesChanged) : 0
                HStack {
                    VStack { Divider() }
                    Text(files == 0 ? String(localized: "Turn \(end.turn) done · no file changes") : String(localized: "Turn \(end.turn) done · \(files) files"))
                        .font(.caption).foregroundStyle(.secondary).fixedSize()
                    VStack { Divider() }
                }
            case .error(_, let e):
                Text(e.hasFailure ? failureTitle(e.failure.kind) + (e.message.isEmpty ? "" : ": \(e.message)") : e.message)
                    .font(.subheadline).foregroundStyle(.red)
            case .tool:
                EmptyView()
            }
        }
    }

    private func tools(_ seq: UInt64, _ calls: [ToolItem]) -> some View {
        let expanded = open.contains(seq)
        let live = calls.last?.running == true
        return VStack(alignment: .leading, spacing: 6) {
            Button {
                withAnimation(.snappy) { if expanded { open.remove(seq) } else { open.insert(seq) } }
            } label: {
                HStack(spacing: 8) {
                    if live { ProgressView().controlSize(.mini) } else { Image(systemName: "wrench.and.screwdriver").font(.caption) }
                    Text(live ? "\(toolVerb(calls.last!.call, running: true)) \(toolSummary(toolObject(calls.last!.call), sessionID: session.sessionID))" : stepSummary(calls))
                        .lineLimit(1)
                    Image(systemName: "chevron.right").font(.caption2.weight(.semibold)).rotationEffect(.degrees(expanded ? 90 : 0))
                }
                .font(.subheadline).foregroundStyle(.secondary)
            }
            .buttonStyle(.plain)
            if expanded {
                VStack(alignment: .leading, spacing: 4) {
                    ForEach(calls, id: \.seq) { c in
                        Text("\(toolVerb(c.call, running: c.running)) \(toolSummary(toolObject(c.call), sessionID: session.sessionID))")
                            .font(.caption.monospaced()).foregroundStyle(c.result?.isError == true ? .red : .secondary).lineLimit(2)
                    }
                }
                .padding(.leading, 22)
            }
        }
    }
}

/// An agent's words: inline Markdown, its line breaks kept.
private func agentMarkdown(_ s: String) -> AttributedString {
    (try? AttributedString(markdown: s, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace))) ?? AttributedString(s)
}
