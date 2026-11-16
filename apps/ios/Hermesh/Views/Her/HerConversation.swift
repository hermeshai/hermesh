import HermeshHer
import HermeshKit
import HermeshSessions
import QuickLook
import SwiftUI

/// The conversation with Her (the Mac's Conversation.tsx): the owner's
/// messages on the right in the blue bubble, what Her sent on the left in
/// hers, and under it the composer, or an approval Her waits for. Her says
/// only what she sends. Offline, what was read stays readable, and what is
/// written meanwhile waits on this iPhone.
struct HerConversation: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let machine: HerMachine
    let state: HerEnvState
    /// Whether the conversation shows its end, so it follows what comes.
    @State private var atEnd = true
    @State private var scrolling = false
    @State private var position = ScrollPosition(edge: .bottom)
    /// Whether it was scrolled well up from its end, where a button brings it back.
    @State private var far = HerFar()
    /// The top shows, where earlier messages are read.
    @State private var topSeen = false
    @Environment(\.accessibilityReduceMotion) private var still
    /// A file Her sent, brought here for Quick Look to preview and save.
    @State private var preview: URL?
    /// Markdown or code Her sent, brought here to read on a page of the app's.
    @State private var page: HerFileText?
    /// The conversation's connection has been opening again for a while.
    @State private var slow = false
    /// The message a scheduled task's run page brought into view, or one a reply quotes, marked a moment.
    @State private var marked: String?
    /// The message whose text is being selected from, to reply to a part of it (docs/her-reply.md §3.3).
    @State private var selecting: HerSelecting?

    /// No model works there: Her's onboarding, the app speaking for her, until the conversation opens on one.
    private var noModel: Bool { state == .noModel || chat.noModel }

    /// The model the conversation opened on, once it did (however one was connected).
    private var openedModel: String? { state == .ready && chat.convUp && !chat.noModel ? chat.live?.model : nil }

    var body: some View {
        let said = chat.session.map(herChat) ?? []
        let waiting = chat.bot.map { herOutbox(sending: chat.sending, waiting: chat.waiting, botID: $0.id, said: said) } ?? []
        let place = herOnboardingPlace(chat.flow, noModel: noModel, session: chat.session?.sessionID, last: (said + waiting).last?.key)
        let rows = herWithOnboarding(said + waiting, place, at: chat.flow?.at ?? .now)
        let busy = chat.live.map(herBusy) ?? false
        let asking = chat.convUp ? chat.live.flatMap(herPendingApproval) : nil
        ScrollViewReader { proxy in
            ScrollView {
                HerRows(chat: chat, rows: rows, earlier: chat.live?.before != nil, busy: busy, machine: machine, marked: marked,
                        topSeen: $topSeen, preview: $preview, page: $page, selecting: $selecting)
                    .equatable()
            }
            // Earlier messages put above what was first: it stays where it was, unless the end is followed.
            .onChange(of: said.first?.key) { old, _ in
                if let old, !atEnd, said.contains(where: { $0.key == old }) { proxy.scrollTo(old, anchor: .top) }
            }
            // A page read while the top still shows: the next one.
            .onChange(of: chat.live?.before) { if topSeen { Task { await chat.earlier() } } }
            // A run's message, asked for from its page: brought into view and marked a moment.
            .onChange(of: chat.revealRun, initial: true) {
                guard let run = chat.revealRun else { return }
                chat.revealRun = nil
                guard let key = rows.first(where: { $0.source?.runID == run })?.key else { return }
                reveal(key, in: proxy, after: .milliseconds(450))
            }
            // The message a reply quotes, asked for from its quote: the same. One that is not among those read
            // here (earlier history) says so.
            .onChange(of: chat.revealMessage, initial: true) {
                guard let key = chat.revealMessage else { return }
                chat.revealMessage = nil
                guard rows.contains(where: { $0.key == key }) else { return model.show(String(localized: "The message this replies to isn’t here anymore")) }
                reveal(key, in: proxy, after: .zero)
            }
        }
        // As a session's page: open at the end, and stay there as it grows unless the owner scrolled up.
        .scrollPosition($position)
        .defaultScrollAnchor(.bottom, for: .initialOffset)
        .onScrollPhaseChange { _, phase in scrolling = phase == .interacting || phase == .decelerating }
        .onScrollGeometryChange(for: Bool.self) { fromEnd($0) < 80 } action: { _, end in
            if scrolling { atEnd = end }
        }
        // As it grows, and as less of it shows (the keyboard comes up): what was last stays in view.
        .onScrollGeometryChange(for: [CGFloat].self) { [$0.contentSize.height, $0.containerSize.height] } action: { _, _ in
            if atEnd && !scrolling { position.scrollTo(edge: .bottom) }
        }
        .onScrollGeometryChange(for: Bool.self, of: scrolledAway) { _, away in far.away = away }
        .scrollDismissesKeyboard(.interactively)
        .overlay {
            if rows.isEmpty && !busy { HerEmpty(chat: chat, machine: machine, loading: noModel || (chat.session == nil && state == .ready)) }
        }
        .safeAreaInset(edge: .top) {
            if case .offline(let seen) = state {
                HerOfflineLine(name: machine.machineName, seen: seen)
            } else if state == .ready && slow {
                HerConnectingLine(name: machine.machineName)
            }
        }
        // The conversation's connection opening again for more than a moment: a reply may take a while, and the page
        // says why (docs/her-sending.md §5.4).
        .task(id: chat.reconnecting) {
            slow = false
            guard chat.reconnecting else { return }
            try? await Task.sleep(for: .seconds(2))
            if !Task.isCancelled { slow = chat.reconnecting }
        }
        .safeAreaInset(edge: .bottom) {
            HerToEnd(far: far) {
                atEnd = true
                withAnimation(still ? nil : .smooth) { position.scrollTo(edge: .bottom) }
            } over: {
                if let asking {
                    HerApprovalPanel(chat: chat, approval: asking.approval, agent: asking.agent, waiting: asking.waiting).id(asking.approval.callID)
                } else {
                    HerComposer(chat: chat, machine: machine, state: state, noModel: noModel)
                }
            }
        }
        .quickLookPreview($preview)
        .sheet(item: $page) { HerFilePage(file: $0) }
        .sheet(item: $selecting) { from in
            HerSelectText(text: from.text) { picked in chat.reply(herQuote(ref: from.id, own: from.own, whole: from.text, selected: picked)) }
        }
        // Her's onboarding: begun when her says no model works there, or HerStatus says so for a while (it says
        // Her runs a moment before her has listed her models); done once the conversation opens on a model.
        .task(id: state == .noModel) {
            guard state == .noModel else { return }
            try? await Task.sleep(for: .seconds(3))
            if !Task.isCancelled { chat.beginOnboarding() }
        }
        .onChange(of: chat.noModel, initial: true) { if chat.noModel { chat.beginOnboarding() } }
        // A choice in the onboarding is the owner's word: the conversation goes to its end, where Her answers it
        // (a key's field, say), even when the owner had scrolled up to a long list's first choices.
        .onChange(of: chat.flow?.said.filter(\.byOwner).count ?? 0) { _, chose in
            if chose > 0 {
                atEnd = true
                position.scrollTo(edge: .bottom)
            }
        }
        // So is a message sent, or an answer tapped: it shows at the end, wherever the owner had scrolled to.
        .onChange(of: chat.spoken) {
            atEnd = true
            position.scrollTo(edge: .bottom)
        }
        .onChange(of: openedModel) {
            if let m = openedModel { chat.flow?.finish(model: m, session: chat.live?.sessionID, after: said.last?.key, at: .now) }
        }
    }
}

extension HerConversation {
    /// Brings a row into view and marks it a moment.
    private func reveal(_ key: String, in proxy: ScrollViewProxy, after wait: Duration) {
        atEnd = false
        Task {
            try? await Task.sleep(for: wait)
            withAnimation(still ? nil : .smooth) { proxy.scrollTo(key, anchor: .center) }
            withAnimation(.easeOut(duration: 0.2)) { marked = key }
            try? await Task.sleep(for: .seconds(1.6))
            withAnimation(.easeOut(duration: 0.6)) { if marked == key { marked = nil } }
        }
    }
}

/// Whether the conversation is scrolled well up from its end, kept apart from the conversation's own state: the
/// button back is all that shows it, and all that is drawn again as it changes, while the conversation scrolls.
@Observable private final class HerFar {
    var away = false
}

/// The button back to the end, over what stands at the conversation's bottom.
private struct HerToEnd<Bottom: View>: View {
    let far: HerFar
    let action: () -> Void
    @ViewBuilder let over: Bottom

    var body: some View {
        Group { over }.toEndButton(far.away, action: action)
    }
}

/// The conversation's rows, a view of their own that is the same while they are: the page around them changes far
/// more often than they do (each thing Her does while she works is an event, most of which show nothing here), and a
/// long conversation laid out again each time kept the page from following the finger.
private struct HerRows: View, Equatable {
    let chat: HerChat
    let rows: [HerItem]
    /// Earlier messages can be read at the top.
    let earlier: Bool
    let busy: Bool
    let machine: HerMachine
    let marked: String?
    @Binding var topSeen: Bool
    @Binding var preview: URL?
    @Binding var page: HerFileText?
    @Binding var selecting: HerSelecting?

    nonisolated static func == (a: HerRows, b: HerRows) -> Bool {
        a.rows == b.rows && a.earlier == b.earlier && a.busy == b.busy && a.marked == b.marked && a.machine == b.machine
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            // Earlier messages (her opens it on its last 100), read once this shows at the top, as on the Mac.
            if earlier {
                ProgressView().frame(maxWidth: .infinity).padding(.bottom, 21)
                    .onScrollVisibilityChange(threshold: 0.1) { seen in
                        topSeen = seen
                        if seen { Task { await chat.earlier() } }
                    }
                    .accessibilityIdentifier("her-earlier")
            }
            ForEach(Array(rows.enumerated()), id: \.element.key) { i, row in
                HerRow(chat: chat, row: row, previous: i > 0 ? rows[i - 1] : nil, machine: machine, preview: $preview, page: $page, selecting: $selecting, marked: marked == row.key)
            }
            if busy { HerTyping().padding(.top, rows.isEmpty ? 0 : herGap(joined: rows.last?.side == .her)) }
        }
        // As wide as the page with nothing in it too: the scroll view is as wide as this, and what shows over it
        // while the conversation is empty (her greeting) was laid out 40 points wide.
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 20).padding(.top, 13).padding(.bottom, 21)
    }
}

/// Which bubble a message is in: the owner's, Her's, or neither (a line of its own, the onboarding).
private enum HerSide { case owner, her }

private extension HerItem {
    var side: HerSide? {
        switch kind {
        case .user, .waiting, .sending: .owner
        case .her, .attachment, .question, .card, .session, .work: .her
        case .failure, .onboarding, .routine: nil
        }
    }
}

/// The room above a message: close under one from the same side, as a run of them reads as one; apart where the
/// side changes.
func herGap(joined: Bool) -> CGFloat { joined ? 6 : 21 }

/// When a row was sent, over it after a pause: the day as it is called now, and the time.
private struct HerTimeLine: View {
    @Environment(AppModel.self) private var model
    let at: Date
    let previous: Date?

    var body: some View {
        if let line = turnTimeLine(at, previous: previous, now: model.now) {
            Text(line).font(.caption).foregroundStyle(.tertiary).frame(maxWidth: .infinity).padding(.top, 6)
        }
    }
}

/// A row of the conversation, with a time line before it after a pause.
private struct HerRow: View {
    let chat: HerChat
    let row: HerItem
    let previous: HerItem?
    let machine: HerMachine
    @Binding var preview: URL?
    @Binding var page: HerFileText?
    @Binding var selecting: HerSelecting?
    /// Brought into view from a scheduled task's run, or from a reply that quotes it: marked a moment.
    var marked = false
    /// One of the owner's files being brought here.
    @State private var opening: String?

    var body: some View {
        let at = date(row.ts), before = previous.map { date($0.ts) }
        // Whether a time line shows is the pause before the row, whatever the time is now; what it says turns on the
        // clock, which HerTimeLine reads: a row reading it would be laid out again at each tick, every row at once.
        let timed = row.kind != .onboarding && turnTimeLine(at, previous: before, now: at) != nil
        VStack(alignment: .leading, spacing: 8) {
            if timed { HerTimeLine(at: at, previous: before) }
            VStack(alignment: .leading, spacing: 2) {
                if let source = row.source { HerRunSource(source: source) }
                content.environment(\.herReplying, replying)
            }
            .padding(marked ? 8 : 0)
            .background(marked ? Color.yellow.opacity(0.22) : .clear, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .padding(marked ? -8 : 0)
        }
        .padding(.top, previous.map { herGap(joined: !timed && row.side != nil && row.side == $0.side) } ?? 0)
    }

    private func date(_ ms: Double) -> Date { Date(timeIntervalSince1970: ms / 1000) }

    /// One of the owner's files, from where it was uploaded on Her's machine.
    private func open(_ path: String) {
        guard opening == nil else { return }
        opening = path
        Task {
            await openHerFile(chat: chat, path: path, preview: $preview, page: $page)
            opening = nil
        }
    }

    /// How this row's message is replied to (docs/her-reply.md §3.1): one of the conversation that has words.
    private var replying: HerReplying? {
        let said: (words: String?, own: Bool)
        switch row.kind {
        case .user(let text, _, _, _): said = (text, true)
        case .her(let text), .question(let text, _, _): said = (text, false)
        case .attachment(_, let text), .card(_, let text): said = (text, false)
        default: return nil
        }
        guard let words = said.words, !words.isEmpty else { return nil }
        let key = row.key, own = said.own
        return HerReplying(
            reply: { chat.reply(herQuote(ref: key, own: own, whole: words)) },
            select: { selecting = HerSelecting(id: key, own: own, text: herPlainText(words)) })
    }

    /// What the owner's message replies to, at the top of its bubble: a tap brings that message into view.
    private var quote: HerQuote? {
        row.reply.map { reply in HerQuote(reply: reply, name: chat.bot?.name ?? "Her") { chat.revealMessage = reply.ref } }
    }

    @ViewBuilder private var content: some View {
        switch row.kind {
        case .user(let text, let images, let files, let queued):
            HerBubble(text: text, images: images, files: files, opening: opening, open: open, quote: quote).opacity(queued ? 0.62 : 1)
        case .waiting(let id, let text, let files, let error):
            VStack(alignment: .trailing, spacing: 5) {
                HerBubble(text: text, images: [], files: files, quote: quote).opacity(0.62)
                // One her keeps refusing holds back those after it, so it can be discarded.
                HStack(spacing: 8) {
                    if error.isEmpty || error == "offline" {
                        Text("Delivered when \(machine.machineName) is back")
                    } else {
                        Label("Not delivered: \(error)", systemImage: "exclamationmark.triangle").lineLimit(2)
                    }
                    Button("Discard") { Task { await chat.discard(id) } }
                        .fontWeight(.semibold).foregroundStyle(.secondary)
                        .accessibilityIdentifier("her-discard")
                }
                .font(.caption).foregroundStyle(.tertiary)
            }
            .frame(maxWidth: .infinity, alignment: .trailing)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("her-waiting")
        case .sending(let id, let text, let images, let files, let refused):
            VStack(alignment: .trailing, spacing: 5) {
                HerBubble(text: text, images: images, files: files, quote: quote).opacity(refused == nil ? 0.62 : 1)
                HStack(spacing: 8) {
                    if let refused {
                        Label("Not delivered: \(refused)", systemImage: "exclamationmark.triangle").lineLimit(2)
                        Button("Try Again") { chat.retry(id) }
                            .fontWeight(.semibold).foregroundStyle(.secondary)
                            .accessibilityIdentifier("her-retry")
                        Button("Edit") { chat.edit(id) }
                            .fontWeight(.semibold).foregroundStyle(.secondary)
                            .accessibilityIdentifier("her-edit")
                    } else {
                        Text("Sending…")
                    }
                }
                .font(.caption).foregroundStyle(.tertiary)
            }
            .frame(maxWidth: .infinity, alignment: .trailing)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("her-sending")
        case .her(let text):
            HerWords(text: text)
        case .attachment(let files, let text):
            VStack(alignment: .leading, spacing: 6) {
                if let text { HerWords(text: text) }
                HerFiles(chat: chat, files: files, preview: $preview, page: $page)
            }
        case .question(let text, let options, let open):
            HerQuestion(chat: chat, text: text, options: options, open: open)
        case .card(let raw, let text):
            VStack(alignment: .leading, spacing: 6) {
                if let text { HerWords(text: text) }
                HerSecureCard(chat: chat, card: HerCard(raw), machine: machine)
            }
        case .failure(let f, _):
            FailLine(title: String(localized: "This task didn’t finish"), message: f.message)
        case .session(let started):
            HerSessionCard(started: started)
        case .work(let work):
            HerWorkCard(work: work)
        case .routine(let change):
            HerRoutineLine(change: change)
        case .onboarding:
            if let flow = chat.flow { HerOnboardingView(chat: chat, flow: flow, machine: machine) }
        }
    }
}

/// What Her did to a scheduled task, as a line of its own in the middle that opens it (the list, for one deleted).
private struct HerRoutineLine: View {
    @Environment(AppModel.self) private var model
    let change: HerRoutineChange

    var body: some View {
        Button {
            model.path.append(change.action == "delete" ? .routines : .routine(change.routineID))
        } label: {
            HStack(spacing: 6) {
                Image(systemName: "clock")
                Text(words).multilineTextAlignment(.leading)
                Image(systemName: "chevron.right").font(.caption2.weight(.semibold)).foregroundStyle(.tertiary)
            }
            .font(.footnote).foregroundStyle(.secondary)
            .padding(.horizontal, 12).padding(.vertical, 7)
            .background(Color(.secondarySystemBackground), in: .capsule)
            .frame(minHeight: 44)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .frame(maxWidth: .infinity)
        .accessibilityIdentifier("her-routine-line")
    }

    private var words: String {
        let time = change.nextRun.map { momentText($0, now: model.now) } ?? change.when
        let name = change.name
        return switch change.action {
        case "create": String(localized: "Created the scheduled task “\(name)” · \(time)")
        case "update": String(localized: "Changed the scheduled task “\(name)” · \(time)")
        case "pause": String(localized: "Paused the scheduled task “\(name)”")
        case "resume": String(localized: "Resumed the scheduled task “\(name)” · \(time)")
        case "delete": String(localized: "Deleted the scheduled task “\(name)”")
        default: name
        }
    }
}

/// Over a message Her passed on from a scheduled task's run: the task's name, opening the run.
private struct HerRunSource: View {
    @Environment(AppModel.self) private var model
    let source: HerRoutineSource

    var body: some View {
        Button {
            model.path.append(.routineRun(source.routineID, source.runID))
        } label: {
            HStack(spacing: 5) {
                Image(systemName: "clock")
                Text(source.name).lineLimit(1)
                Image(systemName: "chevron.right").font(.caption2.weight(.semibold)).foregroundStyle(.tertiary)
            }
            .font(.footnote).foregroundStyle(.secondary)
            .frame(minHeight: 32)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(Text("From the scheduled task “\(source.name)”"))
        .accessibilityIdentifier("her-routine-source")
    }
}

/// The owner's message: its pictures and files above its words, in the blue bubble on the right. With `open`, a
/// file is tapped to read it, as one Her sent; `opening` is the one being brought here.
struct HerBubble: View {
    let text: String
    let images: [HerImage]
    let files: [String]
    var opening: String?
    var open: ((String) -> Void)?
    /// What it replies to.
    var quote: HerQuote?

    var body: some View {
        VStack(alignment: .trailing, spacing: 6) {
            if !images.isEmpty || !files.isEmpty {
                HerWrap(spacing: 6) {
                    ForEach(Array(images.enumerated()), id: \.offset) { _, im in
                        if let picture = Data(base64Encoded: im.data).flatMap(UIImage.init(data:)) {
                            Image(uiImage: picture).resizable().scaledToFill().frame(width: 72, height: 72)
                                .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
                        }
                    }
                    ForEach(files, id: \.self) { f in
                        if let open {
                            Button { open(f) } label: { HerChip(name: baseName(f), busy: opening == f) }
                                .buttonStyle(.plain)
                                .accessibilityIdentifier("her-sent-\(baseName(f))")
                        } else {
                            HerChip(name: baseName(f))
                        }
                    }
                }
            }
            if !text.isEmpty || quote != nil {
                VStack(alignment: .leading, spacing: 7) {
                    if let quote { HerQuoteCard(quote: quote).padding(.horizontal, -5).padding(.top, -1) }
                    if !text.isEmpty { Text(text).lineSpacing(3) }
                }
                .foregroundStyle(Palette.bubbleText)
                .padding(.horizontal, 14).padding(.vertical, 9)
                .background(Palette.bubble, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                .contentShape(.contextMenuPreview, RoundedRectangle(cornerRadius: 18, style: .continuous))
                .herMessageActions(copy: text)
            }
        }
        .padding(.leading, 48)
        .frame(maxWidth: .infinity, alignment: .trailing)
    }
}

/// Her's words, in Markdown, in her bubble on the left.
struct HerWords: View {
    let text: String

    var body: some View {
        MarkdownText(text: text, style: .bubble)
            .herFill()
            .padding(.horizontal, 14).padding(.vertical, 9)
            .herReply()
            .herMessageActions(copy: text)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("her-reply")
            .herSide()
    }
}

extension View {
    /// Her's bubble, as the Mac draws hers: a shade off the page, with a hairline.
    func herReply() -> some View {
        let shape = RoundedRectangle(cornerRadius: 18, style: .continuous)
        return background(Palette.reply, in: shape)
            .overlay(shape.strokeBorder(Palette.separator, lineWidth: 0.5))
            .clipShape(shape)
            .contentShape(.contextMenuPreview, shape)
    }

    /// Her's side: from the left, to 20 short of the page's right margin, where all she sends ends (a messenger's
    /// proportion, most of the page).
    func herSide() -> some View { padding(.trailing, 20).frame(maxWidth: .infinity, alignment: .leading) }

    /// Words that wrap take the whole of Her's side, so her bubbles of more than a line all end on one edge rather
    /// than at each one's longest line; words that fit on a line keep a bubble as wide as they are.
    func herFill() -> some View {
        ViewThatFits(in: .horizontal) {
            self
            frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

/// A coding session Her started or works on, where she last did (docs/her-team.md §4.4): the agent, the title, where it
/// runs, and how it stands now, kept current from the sessions the app holds; it opens the session's page. One the
/// app holds no more (deleted, or older than what it keeps) shows as the call named it, without a state. It
/// answers no question: news of her own sessions goes to Her first.
struct HerSessionCard: View {
    @Environment(AppModel.self) private var model
    let started: HerStarted

    var body: some View {
        let s = model.session(started.sessionID)
        let agent = s?.agent ?? started.agent
        Button { model.openSession(started.sessionID) } label: {
            HStack(alignment: .top, spacing: 10) {
                AgentLogo(id: agent, size: 30)
                VStack(alignment: .leading, spacing: 2) {
                    Text(s?.displayTitle ?? started.title).font(.callout.weight(.semibold)).lineLimit(1)
                    Text(verbatim: "\(agentShortTitle(agent)) · \(s.map { model.machineName($0.machineID) } ?? started.machine)")
                        .font(.footnote).foregroundStyle(.secondary).lineLimit(1)
                    if let s { state(s) }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                if let s { mark(s).frame(height: 20) }
            }
            .padding(12)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .disabled(s == nil)
        .herReply()
        .herSide()
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("her-session-\(s?.displayTitle ?? started.title)")
    }

    /// The third line: what it does now, that it waits, how it failed, or what it changed.
    @ViewBuilder private func state(_ s: Session) -> some View {
        if s.status == .waitingInput {
            Text("Waiting for an answer").font(.footnote).foregroundStyle(.orange)
        } else if s.isFailed {
            Text("Failed").font(.footnote).foregroundStyle(.red)
        } else if s.isActive {
            HStack(spacing: 6) {
                if !s.progress.currentTool.isEmpty { Text(s.progress.currentTool).font(.caption.monospaced()).lineLimit(1) }
                if let start = s.turnStartedDate { Text(start, style: .timer).font(.caption).monospacedDigit() }
            }
            .foregroundStyle(.secondary)
        } else if s.status == .idle {
            let d = s.progress.diffStat
            if d.filesChanged > 0 {
                HStack(spacing: 6) {
                    Text("\(d.filesChanged) files")
                    Text(verbatim: "+\(d.insertions)").foregroundStyle(.green)
                    Text(verbatim: "−\(d.deletions)").foregroundStyle(.red)
                }
                .font(.footnote).foregroundStyle(.secondary).monospacedDigit()
            } else {
                Text("Done").font(.footnote).foregroundStyle(.secondary)
            }
        }
    }

    @ViewBuilder private func mark(_ s: Session) -> some View {
        if s.status == .waitingInput {
            Circle().fill(.orange).frame(width: 9, height: 9)
        } else if s.isFailed {
            Circle().fill(.red).frame(width: 9, height: 9)
        } else if s.isActive {
            ProgressView().controlSize(.small)
        } else if s.status == .idle {
            Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
        }
    }
}

/// The team's work Her counted for the owner (docs/her-team.md §6.3), as Work's card draws it, smaller: the sessions
/// at work, what came of them, a bar for each hour or day, and the coding agents and who sent the work. It opens Work.
struct HerWorkCard: View {
    @Environment(AppModel.self) private var model
    let work: HerWork

    var body: some View {
        let top = max(work.buckets.max() ?? 0, 1)
        Button { model.open(.overview) } label: {
            VStack(alignment: .leading, spacing: 10) {
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    Text("\(work.sessions)").font(.system(size: 34, weight: .bold)).monospacedDigit()
                    Text(work.days == 1 ? "sessions today" : "sessions in \(work.days) days").foregroundStyle(.secondary)
                    Spacer()
                    Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(.tertiary)
                }
                Text("Done \(Text("\(work.done)").bold().foregroundStyle(.primary)) · \(work.turns) turns · \(tokensText(UInt64(work.tokens))) tokens")
                    .font(.footnote).foregroundStyle(.secondary).monospacedDigit()
                if !work.buckets.isEmpty {
                    HStack(alignment: .bottom, spacing: 2) {
                        ForEach(work.buckets.indices, id: \.self) { i in
                            RoundedRectangle(cornerRadius: 2, style: .continuous)
                                .fill(Color.primary.opacity(work.buckets[i] > 0 ? 0.32 : 0.08))
                                .frame(height: max(3, CGFloat(work.buckets[i]) / CGFloat(top) * 32))
                        }
                    }
                    .frame(height: 32, alignment: .bottom)
                    .accessibilityHidden(true)
                }
                groups(work.agents) { AgentLogo(id: $0.id, size: 18) } name: { agentShortTitle($0.id) }
                groups(work.senders) { g in sender(g.id) } name: { model.senderName($0.id) }
            }
            .padding(12)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .herReply()
        .herSide()
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("her-work")
    }

    /// The three busiest of a grouping, each with its picture and sessions.
    private func groups(_ gs: [HerWork.Group], @ViewBuilder picture: @escaping (HerWork.Group) -> some View, name: @escaping (HerWork.Group) -> String) -> some View {
        HStack(spacing: 12) {
            ForEach(gs.prefix(3), id: \.id) { g in
                HStack(spacing: 5) {
                    picture(g)
                    Text(verbatim: "\(name(g)) \(g.sessions)").font(.caption).foregroundStyle(.secondary).lineLimit(1).monospacedDigit()
                }
            }
        }
    }

    @ViewBuilder private func sender(_ id: String) -> some View {
        if let c = model.clients.first(where: { $0.clientID == id }) {
            GrantAvatar(client: c, size: 18)
        } else {
            Image(systemName: "person.crop.circle.fill").font(.system(size: 16)).foregroundStyle(.secondary).frame(width: 18, height: 18)
        }
    }
}

/// A file named in a message, as a small chip.
struct HerChip: View {
    let name: String
    /// Being brought here to read.
    var busy = false
    var action: (() -> Void)?

    var body: some View {
        HStack(spacing: 5) {
            if busy { ProgressView().controlSize(.mini) } else { Image(systemName: "doc").foregroundStyle(.secondary) }
            Text(name).lineLimit(1).truncationMode(.middle)
            if let action {
                Button(action: action) { Image(systemName: "xmark.circle.fill").foregroundStyle(.tertiary) }
                    .buttonStyle(.plain)
                    .accessibilityLabel(Text("Remove \(name)"))
            }
        }
        .font(.footnote)
        .padding(.horizontal, 10).padding(.vertical, 7)
        .background(Palette.fill2, in: .capsule)
        .frame(maxWidth: 220)
    }
}

/// What Her sent as files: her pictures, read from the machine, then her other files in one list in her bubble, each
/// brought here when tapped, to read on a page of the app's (Markdown, code) or for Quick Look to preview and save.
private struct HerFiles: View {
    let chat: HerChat
    let files: [HerFile]
    @Binding var preview: URL?
    @Binding var page: HerFileText?
    @State private var opening: String?

    var body: some View {
        let pictures = files.filter { $0.image && !chat.unreadable($0) }
        let others = files.filter { f in !pictures.contains(f) }
        VStack(alignment: .leading, spacing: 8) {
            ForEach(pictures, id: \.path) { f in
                Button { open(f) } label: {
                    Group {
                        if let picture = chat.pictures[f.source] ?? nil {
                            Image(uiImage: picture).resizable().scaledToFit()
                        } else {
                            Palette.fill.frame(height: 180).overlay { ProgressView() }
                        }
                    }
                    .frame(maxWidth: 300, maxHeight: 360, alignment: .leading)
                    .clipShape(RoundedRectangle(cornerRadius: 18, style: .continuous))
                }
                .buttonStyle(.plain)
                .accessibilityLabel(f.name)
                .task(id: f.source) { await chat.loadPicture(f) }
            }
            if !others.isEmpty {
                VStack(spacing: 0) {
                    ForEach(Array(others.enumerated()), id: \.element.path) { i, f in
                        if i > 0 { Divider().padding(.leading, 56) }
                        Button { open(f) } label: {
                            HStack(spacing: 10) {
                                Image(systemName: "doc.text").font(.title3).foregroundStyle(.secondary)
                                    .frame(width: 34, height: 40)
                                    .background(Palette.fill2, in: RoundedRectangle(cornerRadius: 7, style: .continuous))
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(f.name).font(.subheadline.weight(.medium)).lineLimit(1).truncationMode(.middle)
                                    Text(ByteCountFormatter.string(fromByteCount: Int64(f.size), countStyle: .file)).font(.caption).foregroundStyle(.secondary)
                                }
                                Spacer(minLength: 0)
                                if opening == f.path { ProgressView() }
                            }
                            .padding(.horizontal, 12).padding(.vertical, 9)
                            .contentShape(.rect)
                        }
                        .buttonStyle(HerChoiceStyle())
                        .accessibilityIdentifier("her-file-\(f.name)")
                    }
                }
                .herReply()
                .herSide()
            }
        }
    }

    private func open(_ f: HerFile) {
        guard opening == nil else { return }
        opening = f.path
        Task {
            await openHerFile(chat: chat, path: f.path, snapshot: f.snapshot, preview: $preview, page: $page)
            opening = nil
        }
    }
}

/// A file on Her's machine, brought here when tapped: Markdown and code to read on a page of the app's, the rest (a
/// PDF, a document) for Quick Look to preview and save. `snapshot`: the copy her kept of a file she sent, brought in
/// its place.
@MainActor func openHerFile(chat: HerChat, path: String, snapshot: String? = nil, sha256: String? = nil, preview: Binding<URL?>, page: Binding<HerFileText?>) async {
    guard let url = await chat.download(path, snapshot: snapshot, sha256: sha256) else { return }
    let size = (try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? .max
    if let kind = HerFileKind(name: url.lastPathComponent, size: size), let text = try? String(contentsOf: url, encoding: .utf8) {
        page.wrappedValue = HerFileText(url: url, text: text, kind: kind)
    } else {
        preview.wrappedValue = url
    }
}

/// A question in her bubble, with its answers under it as rows to tap if they can be listed (the Mac's). A tapped
/// answer is the owner's message like any other; once they said anything, the answers fold away.
private struct HerQuestion: View {
    let chat: HerChat
    let text: String
    let options: [String]
    let open: Bool
    @State private var sending = false

    var body: some View {
        let asking = open && !options.isEmpty
        VStack(alignment: .leading, spacing: 0) {
            MarkdownText(text: text, style: .bubble).herFill().padding(.horizontal, 14).padding(.vertical, 9)
            if asking {
                VStack(spacing: 0) {
                    ForEach(options, id: \.self) { label in
                        Divider()
                        Button {
                            sending = true
                            Task { if !(await chat.say(label)) { sending = false } }
                        } label: {
                            Text(label).frame(maxWidth: .infinity, alignment: .leading)
                                .padding(.horizontal, 14).padding(.vertical, 11)
                                .contentShape(.rect)
                        }
                        .buttonStyle(HerChoiceStyle())
                    }
                }
                .disabled(sending || !chat.convUp)
                .transition(.opacity)
            }
        }
        .frame(maxWidth: asking ? .infinity : nil, alignment: .leading)
        .herReply()
        .herMessageActions(copy: text)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("her-question")
        .herSide()
        .animation(.easeOut(duration: 0.25), value: open)
    }
}

/// A quiet answer to tap, as the Mac's.
struct HerPill: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.subheadline.weight(.semibold))
            .padding(.horizontal, 14).padding(.vertical, 8)
            .background(configuration.isPressed ? Palette.fill2 : Color.clear, in: .capsule)
            .overlay(Capsule().strokeBorder(Palette.fill3))
            .contentShape(.capsule)
    }
}

/// The last thing while Her is at work: three dots, in her bubble.
struct HerTyping: View {
    @Environment(\.accessibilityReduceMotion) private var still

    var body: some View {
        TimelineView(.animation(paused: still)) { t in
            let phase = t.date.timeIntervalSinceReferenceDate * 2.4
            HStack(spacing: 4) {
                ForEach(0 ..< 3) { i in
                    Circle().fill(.secondary).frame(width: 7, height: 7)
                        .opacity(still ? 0.6 : 0.35 + 0.5 * max(0, sin(phase - Double(i) * 0.7)))
                }
            }
        }
        .padding(.horizontal, 14).padding(.vertical, 11)
        .background(Palette.reply, in: .capsule)
        .overlay(Capsule().strokeBorder(Palette.separator, lineWidth: 0.5))
        .accessibilityElement()
        .accessibilityLabel(Text("Typing…"))
        .accessibilityIdentifier("her-typing")
    }
}

/// A new conversation, or one that can't be read now: Her's mark, her name, and a line; or the wait.
private struct HerEmpty: View {
    let chat: HerChat
    let machine: HerMachine
    let loading: Bool

    var body: some View {
        if loading {
            PageLoader(text: "Reading the conversation…", bare: true)
        } else {
            VStack(spacing: 10) {
                Mark(size: 56)
                Text(chat.bot?.name ?? "Her").font(.title3.weight(.semibold))
                Group {
                    if herReachable(machine) {
                        Text("Hi, I’m \(chat.bot?.name ?? "Her"). I work on \(machine.machineName). Tell me what you need, several things at once if you like.")
                    } else {
                        Text("This conversation is kept on \(machine.machineName). It shows once the machine is back.")
                    }
                }
                .font(.subheadline).foregroundStyle(.secondary).multilineTextAlignment(.center)
            }
            .padding(.horizontal, 40)
        }
    }
}

/// An environment that can't be reached: what was read stays, under when the machine was last seen.
private struct HerOfflineLine: View {
    @Environment(AppModel.self) private var model
    let name: String
    let seen: Date?

    var body: some View {
        HStack(spacing: 7) {
            Circle().fill(.tertiary).frame(width: 7, height: 7)
            if let seen {
                Text("\(name) offline · seen \(agoText(seen, now: model.now))")
            } else {
                Text("\(name) is offline")
            }
        }
        .font(.footnote).foregroundStyle(.secondary)
        .padding(.horizontal, 12).padding(.vertical, 7)
        .background(.bar, in: .capsule)
        .frame(maxWidth: .infinity)
        .padding(.top, 4)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("her-offline")
    }
}

/// The conversation's connection opening again, under the top, while the machine answers.
private struct HerConnectingLine: View {
    let name: String

    var body: some View {
        HStack(spacing: 7) {
            ProgressView().controlSize(.mini)
            Text("Connecting to \(name)…")
        }
        .font(.footnote).foregroundStyle(.secondary)
        .padding(.horizontal, 12).padding(.vertical, 7)
        .background(.bar, in: .capsule)
        .frame(maxWidth: .infinity)
        .padding(.top, 4)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("her-connecting")
    }
}

/// Views in rows as wide as they fit, the next row under: answers to tap, files.
struct HerWrap: Layout {
    var spacing: CGFloat

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let rows = rows(subviews, width: proposal.width ?? .infinity)
        return CGSize(width: rows.map(\.width).max() ?? 0, height: rows.reduce(0) { $0 + $1.height } + spacing * CGFloat(max(0, rows.count - 1)))
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var y = bounds.minY
        for row in rows(subviews, width: bounds.width) {
            var x = bounds.minX
            for i in row.items {
                let size = subviews[i].sizeThatFits(.unspecified)
                subviews[i].place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(size))
                x += size.width + spacing
            }
            y += row.height + spacing
        }
    }

    private func rows(_ subviews: Subviews, width: CGFloat) -> [(items: [Int], width: CGFloat, height: CGFloat)] {
        var rows: [(items: [Int], width: CGFloat, height: CGFloat)] = []
        for (i, v) in subviews.enumerated() {
            let size = v.sizeThatFits(.unspecified)
            if let last = rows.last, last.width + spacing + size.width <= width {
                rows[rows.count - 1] = (last.items + [i], last.width + spacing + size.width, max(last.height, size.height))
            } else {
                rows.append(([i], size.width, size.height))
            }
        }
        return rows
    }
}

