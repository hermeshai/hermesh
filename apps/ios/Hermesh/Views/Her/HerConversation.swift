import HermeshHer
import HermeshKit
import HermeshSessions
import QuickLook
import SwiftUI

/// The conversation with Her (the Mac's Conversation.tsx): the owner's
/// messages on the right in the dark bubble, what Her sent on the left in
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
    /// A file Her sent, brought here to preview and save.
    @State private var preview: URL?

    /// No model works there: Her's onboarding, the app speaking for her, until the conversation opens on one.
    private var noModel: Bool { state == .noModel || chat.noModel }

    /// The model the conversation opened on, once it did (however one was connected).
    private var openedModel: String? { state == .ready && chat.convUp && !chat.noModel ? chat.live?.model : nil }

    var body: some View {
        let said = chat.session.map(herChat) ?? []
        let waiting = chat.bot.map { herWaiting(chat.waiting, botID: $0.id, said: said) } ?? []
        let place = herOnboardingPlace(chat.flow, noModel: noModel, session: chat.session?.sessionID, last: (said + waiting).last?.key)
        let rows = herWithOnboarding(said + waiting, place, at: chat.flow?.at ?? .now)
        let busy = chat.live.map(herBusy) ?? false
        let asking = chat.convUp ? chat.live.flatMap(herPendingApproval) : nil
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                ForEach(Array(rows.enumerated()), id: \.element.key) { i, row in
                    HerRow(chat: chat, row: row, previous: i > 0 ? rows[i - 1] : nil, machine: machine, preview: $preview)
                }
                if busy { HerTyping().padding(.top, rows.isEmpty ? 0 : herGap(joined: rows.last?.side == .her)) }
            }
            .padding(.horizontal, 20).padding(.top, 13).padding(.bottom, 21)
        }
        // Nothing drawn under the top: the conversation scrolls under Her's mark and the menu button as it is.
        .scrollEdgeEffectHidden(true, for: .top)
        // As a session's page: open at the end, and stay there as it grows unless the owner scrolled up.
        .scrollPosition($position)
        .defaultScrollAnchor(.bottom, for: .initialOffset)
        .onScrollPhaseChange { _, phase in scrolling = phase == .interacting || phase == .decelerating }
        .onScrollGeometryChange(for: Bool.self) { g in g.visibleRect.maxY > g.contentSize.height - 80 } action: { _, end in
            if scrolling { atEnd = end }
        }
        .onScrollGeometryChange(for: CGFloat.self) { $0.contentSize.height } action: { _, _ in
            if atEnd && !scrolling { position.scrollTo(edge: .bottom) }
        }
        .scrollDismissesKeyboard(.interactively)
        .overlay {
            if rows.isEmpty && !busy { HerEmpty(chat: chat, machine: machine, loading: noModel || (chat.session == nil && state == .ready)) }
        }
        .safeAreaInset(edge: .top) {
            if case .offline(let seen) = state { HerOfflineLine(name: machine.machineName, seen: seen) }
        }
        .safeAreaInset(edge: .bottom) {
            if let asking {
                HerApprovalPanel(chat: chat, approval: asking.approval, agent: asking.agent, waiting: asking.waiting).id(asking.approval.callID)
            } else {
                HerComposer(chat: chat, machine: machine, state: state, noModel: noModel, direct: state == .ready && chat.convUp && waiting.isEmpty)
            }
        }
        .quickLookPreview($preview)
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
        .onChange(of: openedModel) {
            if let m = openedModel { chat.flow?.finish(model: m, session: chat.live?.sessionID, after: said.last?.key, at: .now) }
        }
    }
}

/// Which bubble a message is in: the owner's, Her's, or neither (a line of its own, the onboarding).
private enum HerSide { case owner, her }

private extension HerItem {
    var side: HerSide? {
        switch kind {
        case .user, .waiting: .owner
        case .her, .attachment, .question, .card: .her
        case .failure, .onboarding: nil
        }
    }
}

/// The room above a message: close under one from the same side, as a run of them reads as one; apart where the
/// side changes.
func herGap(joined: Bool) -> CGFloat { joined ? 6 : 21 }

/// A row of the conversation, with a time line before it after a pause.
private struct HerRow: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let row: HerItem
    let previous: HerItem?
    let machine: HerMachine
    @Binding var preview: URL?

    var body: some View {
        let line = row.kind == .onboarding ? nil : turnTimeLine(date(row.ts), previous: previous.map { date($0.ts) }, now: model.now)
        VStack(alignment: .leading, spacing: 8) {
            if let line {
                Text(line).font(.caption).foregroundStyle(.tertiary).frame(maxWidth: .infinity).padding(.top, 6)
            }
            content
        }
        .padding(.top, previous.map { herGap(joined: line == nil && row.side != nil && row.side == $0.side) } ?? 0)
    }

    private func date(_ ms: Double) -> Date { Date(timeIntervalSince1970: ms / 1000) }

    @ViewBuilder private var content: some View {
        switch row.kind {
        case .user(let text, let images, let files, let queued):
            HerBubble(text: text, images: images, files: files).opacity(queued ? 0.62 : 1)
        case .waiting(let id, let text, let files, let error):
            VStack(alignment: .trailing, spacing: 5) {
                HerBubble(text: text, images: [], files: files).opacity(0.62)
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
        case .her(let text):
            HerWords(text: text)
        case .attachment(let files, let text):
            VStack(alignment: .leading, spacing: 6) {
                if let text { HerWords(text: text) }
                HerFiles(chat: chat, files: files, preview: $preview)
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
        case .onboarding:
            if let flow = chat.flow { HerOnboardingView(chat: chat, flow: flow, machine: machine) }
        }
    }
}

/// The owner's message: its pictures and files above its words, in the dark bubble on the right.
struct HerBubble: View {
    let text: String
    let images: [HerImage]
    let files: [String]

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
                    ForEach(files, id: \.self) { HerChip(name: $0) }
                }
            }
            if !text.isEmpty {
                Text(text)
                    .lineSpacing(3)
                    .foregroundStyle(Palette.bubbleText)
                    .padding(.horizontal, 14).padding(.vertical, 9)
                    .background(Palette.bubble, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                    .contentShape(.contextMenuPreview, RoundedRectangle(cornerRadius: 18, style: .continuous))
                    .contextMenu { Button("Copy", systemImage: "doc.on.doc") { UIPasteboard.general.string = text } }
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
        MarkdownText(text: text)
            .padding(.horizontal, 14).padding(.vertical, 9)
            .herReply()
            .contextMenu { Button("Copy", systemImage: "doc.on.doc") { UIPasteboard.general.string = text } }
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

    /// On the left, short of the right edge as the owner's bubble is of the left.
    func herSide() -> some View { padding(.trailing, 48).frame(maxWidth: .infinity, alignment: .leading) }
}

/// A file named in a message, as a small chip.
struct HerChip: View {
    let name: String
    var action: (() -> Void)?

    var body: some View {
        HStack(spacing: 5) {
            Image(systemName: "doc").foregroundStyle(.secondary)
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
/// brought here to preview and save when tapped.
private struct HerFiles: View {
    let chat: HerChat
    let files: [HerFile]
    @Binding var preview: URL?
    @State private var opening: String?

    var body: some View {
        let pictures = files.filter { $0.image && !chat.unreadable($0.path) }
        let others = files.filter { f in !pictures.contains(f) }
        VStack(alignment: .leading, spacing: 8) {
            ForEach(pictures, id: \.path) { f in
                Button { open(f) } label: {
                    Group {
                        if let picture = chat.pictures[f.path] ?? nil {
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
                .task(id: f.path) { await chat.loadPicture(f.path) }
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
                .frame(maxWidth: 300)
                .herReply()
            }
        }
    }

    private func open(_ f: HerFile) {
        guard opening == nil else { return }
        opening = f.path
        Task {
            preview = await chat.download(f)
            opening = nil
        }
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
            MarkdownText(text: text).padding(.horizontal, 14).padding(.vertical, 9)
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
        .contextMenu { Button("Copy", systemImage: "doc.on.doc") { UIPasteboard.general.string = text } }
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
