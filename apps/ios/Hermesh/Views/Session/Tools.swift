import HermeshProto
import HermeshSessions
import SwiftUI
import UIKit

/// Tool calls in a row as one grey line ("Read 3 files · Ran 1 command"),
/// opened to a hairline card with a row for each; folded until the owner
/// opens it, as on the Mac.
struct ToolGroup: View {
    let calls: [ToolItem]
    let sessionID: String

    var body: some View {
        ToolFold(summary: stepSummary(calls), running: calls.contains(where: \.running), ids: calls.map(\.seq)) { seq in
            if let c = calls.first(where: { $0.seq == seq }) { ToolRow(item: c, sessionID: sessionID, inCard: true) }
        }
    }
}

/// Steps in a row as one grey line (`summary`), opened to a hairline card with a row for each (`row`, in the card's
/// size); folded until the owner opens it. A session's tool calls, and a sub-agent's thoughts and calls, are shown so.
struct ToolFold<ID: Hashable & Sendable, Row: View>: View {
    let summary: String
    let running: Bool
    let ids: [ID]
    @ViewBuilder let row: (ID) -> Row
    @State private var open = false

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Button {
                withAnimation(.snappy(duration: 0.25)) { open.toggle() }
            } label: {
                HStack(spacing: 6) {
                    Text(summary).lineLimit(1).shimmer(running)
                    Caret(open: open)
                }
                .font(.subheadline).foregroundStyle(.secondary)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityValue(open ? Text("Expanded") : Text("Collapsed"))
            .accessibilityIdentifier("tool-steps-summary")
            if open { StepsCard(ids: ids, running: running, row: row) }
        }
    }
}

/// What a step in an opened card calls to bring itself (its output, just opened) into view in the card.
extension EnvironmentValues {
    @Entry var revealStep: (() -> Void)?
}

/// An opened group's card: a row per step. Past seven and a half rows it stops growing and scrolls inside (the half row
/// says there is more), laying out only the rows near what is in sight, so a turn of hundreds of steps opens at once.
/// It opens at the newest step and, while the turn runs, stays there as steps come, until the owner scrolls up or opens
/// a step; then a small arrow takes them back. Where there is more above or below, that edge fades into the page.
private struct StepsCard<ID: Hashable & Sendable, Row: View>: View {
    let ids: [ID]
    let running: Bool
    @ViewBuilder let row: (ID) -> Row
    /// How high it grows: seven and a half rows of a step's line (31⅔ points each at the default text size), growing
    /// with the text.
    @ScaledMetric(relativeTo: .footnote) private var cap: CGFloat = 238
    @State private var position = ScrollPosition(edge: .bottom)
    @State private var shape = CardShape()
    @State private var follow = true
    @State private var dragging = false

    /// What of the card's scroll the card shows: its content's height (to the point), whether there is more above or
    /// below what shows, and whether it is at the newest step. Only a change of these draws the card again, not every
    /// point it scrolls (each draw goes over all its steps).
    struct CardShape: Equatable {
        var content: CGFloat = 0
        var moreAbove = false
        var moreBelow = false
        var atEnd = true
    }

    var body: some View {
        let first = ids.first
        ScrollView {
            LazyVStack(spacing: 0) {
                // One view a step, its hairline over it (an optional view of its own would have the stack count the
                // views of every step, all of them, to lay out the few in sight).
                ForEach(ids, id: \.self) { id in
                    row(id)
                        .overlay(alignment: .top) { if id != first { Rectangle().fill(Palette.separator).frame(height: 0.5) } }
                        .environment(\.revealStep) {
                            follow = false
                            withAnimation(.snappy(duration: 0.25)) { position.scrollTo(id: id) }
                        }
                }
            }
            .scrollTargetLayout()
            .background { CardScrollsFirst() }
        }
        .scrollPosition($position)
        .scrollBounceBehavior(.basedOnSize)
        // As high as its rows until the cap: a guess of a row a step until the rows say.
        .frame(height: min(shape.content > 0 ? shape.content : CGFloat(ids.count) * 32, cap))
        .onScrollGeometryChange(for: CardShape.self) { g in
            let above = g.contentOffset.y + g.contentInsets.top
            let below = g.contentSize.height - g.containerSize.height - above
            return CardShape(content: g.contentSize.height.rounded(), moreAbove: above > 1, moreBelow: below > 1, atEnd: below < 8)
        } action: { _, now in
            shape = now
            // At the newest step it follows again; scrolled up from it by the owner, it stays where they leave it.
            if now.atEnd { follow = true } else if dragging { follow = false }
        }
        .onScrollPhaseChange { _, phase in
            dragging = phase == .interacting || phase == .decelerating
            // Left above the newest step by the owner's own scroll: it stays there.
            if dragging, !shape.atEnd { follow = false }
        }
        .onChange(of: ids.count) { if running && follow { position.scrollTo(edge: .bottom) } }
        .overlay(alignment: .top) { edge(.top, shown: shape.moreAbove, height: 22) }
        .overlay(alignment: .bottom) { edge(.bottom, shown: shape.moreBelow, height: 28) }
        .overlay(alignment: .bottomTrailing) {
            if running && !follow && shape.moreBelow {
                Button {
                    follow = true
                    withAnimation(.snappy(duration: 0.3)) { position.scrollTo(edge: .bottom) }
                } label: {
                    Image(systemName: "arrow.down").font(.footnote.weight(.semibold)).frame(width: 30, height: 30)
                        .glassEffect(.regular.interactive(), in: .circle)
                        .contentShape(.circle)
                }
                .buttonStyle(.plain)
                .padding(8)
                .transition(.opacity.combined(with: .offset(y: 4)))
                .accessibilityLabel(Text("Back to the Bottom"))
                .accessibilityIdentifier("steps-to-end")
            }
        }
        .animation(.easeOut(duration: 0.18), value: running && !follow && shape.moreBelow)
        .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous).strokeBorder(Palette.separator, lineWidth: 1))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("tool-steps")
    }

    /// An edge fading into the page where there is more past it.
    private func edge(_ at: VerticalEdge, shown: Bool, height: CGFloat) -> some View {
        LinearGradient(colors: [Color(UIColor.systemBackground), Color(UIColor.systemBackground).opacity(0)], startPoint: at == .top ? .top : .bottom, endPoint: at == .top ? .bottom : .top)
            .frame(height: height)
            .opacity(shown ? 1 : 0)
            .animation(.easeOut(duration: 0.15), value: shown)
            .allowsHitTesting(false)
            .accessibilityHidden(true)
    }
}

/// A drag that starts on a card scrolls the card while it can go that way; at its end that way (or when it has nothing
/// to scroll) the drag scrolls the page. Said to UIKit outright: on an iPhone with iOS 27 the page took drags that began
/// on the card. Two gates on the card, decided as the drag starts: one that begins when the card can go the drag's way,
/// which the page's pan waits on; one that begins when it can't, which the card's pan waits on. A drag elsewhere on the
/// page never reaches them. Placed in the card's content, it finds the card's scroll view, then the page's above it.
private struct CardScrollsFirst: UIViewRepresentable {
    func makeUIView(context: Context) -> UIView { Marker() }
    func updateUIView(_ uiView: UIView, context: Context) {}

    private final class Marker: UIView {
        override init(frame: CGRect) {
            super.init(frame: frame)
            isUserInteractionEnabled = false
        }
        required init?(coder: NSCoder) { fatalError() }

        override func didMoveToWindow() {
            super.didMoveToWindow()
            guard window != nil, let card = Self.scrollView(above: self), let page = Self.scrollView(above: card),
                  card.gestureRecognizers?.contains(where: { $0 is Gate }) != true else { return }
            let goes = Gate(card: card) { $0 }
            let stops = Gate(card: card) { !$0 }
            page.panGestureRecognizer.require(toFail: goes)
            card.panGestureRecognizer.require(toFail: stops)
        }

        private static func scrollView(above view: UIView) -> UIScrollView? {
            var v = view.superview
            while let cur = v, !(cur is UIScrollView) { v = cur.superview }
            return v as? UIScrollView
        }
    }

    /// Begins, alongside everything, only when `when` says so of whether the card can go the drag's way.
    private final class Gate: UIPanGestureRecognizer, UIGestureRecognizerDelegate {
        weak var card: UIScrollView?
        let when: (Bool) -> Bool

        init(card: UIScrollView, when: @escaping (Bool) -> Bool) {
            self.card = card
            self.when = when
            super.init(target: nil, action: nil)
            delegate = self
            cancelsTouchesInView = false
            delaysTouchesEnded = false
            card.addGestureRecognizer(self)
        }

        func gestureRecognizer(_ g: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer) -> Bool { true }

        func gestureRecognizerShouldBegin(_ g: UIGestureRecognizer) -> Bool {
            guard let card else { return false }
            let v = velocity(in: card)
            guard abs(v.y) > abs(v.x) else { return false }
            let top = -card.adjustedContentInset.top, bottom = card.contentSize.height - card.bounds.height + card.adjustedContentInset.bottom
            // A finger moving down shows what is above.
            let can = bottom > top + 0.5 && (v.y > 0 ? card.contentOffset.y > top + 0.5 : card.contentOffset.y < bottom - 0.5)
            return when(can)
        }
    }
}

/// One tool call: what it did, and its input and output when opened, read whole
/// then when the conversation holds only their start. Folded until opened,
/// failed ones too; the one running shimmers. A command reads as code.
struct ToolRow: View {
    @Environment(AppModel.self) private var model
    let item: ToolItem
    let sessionID: String
    /// In an opened group's card: a size down, its output on the card's ground.
    var inCard = false
    @State private var open = false
    @State private var wholeIn: ToolOutput?
    @State private var whole: ToolOutput?

    var body: some View {
        let call = item.call
        let denied = call.decision == "deny"
        let failed = denied || item.result?.isError == true
        ToolLine(verb: toolVerb(call, running: item.running), object: toolSummary(toolObject(call), sessionID: sessionID), shell: call.kind == "shell",
                 running: item.running || readingIn || reading, failed: failed, input: inputText, output: output(denied: denied), inCard: inCard, open: $open)
            .task(id: open) {
                if open {
                    async let a: Void = readWholeInput()
                    async let b: Void = readWhole()
                    _ = await (a, b)
                }
            }
    }

    private var partialIn: Bool { partialInput(item.call) }
    private var partial: Bool { item.result.map(partialOutput) ?? false }
    private var inKey: String { "\(sessionID)/in/\(item.call.callID)" }
    private var key: String { "\(sessionID)/\(item.call.callID)" }
    private var doneIn: ToolOutput? { wholeIn ?? ToolOutputs.read[inKey].map { ToolOutput(text: $0, done: true, failed: false) } }
    private var done: ToolOutput? { whole ?? ToolOutputs.read[key].map { ToolOutput(text: $0, done: true, failed: false) } }
    private var readingIn: Bool { open && partialIn && doneIn?.done != true && doneIn?.failed != true }
    private var reading: Bool { open && partial && done?.done != true && done?.failed != true }

    private var inputText: String {
        let c = item.call
        let text = partialIn ? (doneIn.flatMap { $0.text.isEmpty ? nil : $0.text } ?? c.input) : c.input
        if text.isEmpty { return "" }
        let note = doneIn?.failed == true ? String(localized: "… (the rest can’t be read now)")
            : !partialIn && c.inputTruncated ? String(localized: "… (truncated)") : ""
        return note.isEmpty ? text : "\(text)\n\(note)"
    }

    /// What opens under the row: why it was denied, or the output (with a
    /// note when the rest can't be read), or nothing to open.
    private func output(denied: Bool) -> String {
        if denied { return item.call.reason }
        guard let r = item.result else { return "" }
        let text = partial ? (done.flatMap { $0.text.isEmpty ? nil : $0.text } ?? r.summary) : r.summary
        if text.isEmpty { return "" }
        // A machine from before whole outputs kept only their start.
        let note = done?.failed == true ? String(localized: "… (the rest can’t be read now)")
            : !partial && r.truncated ? String(localized: "… (truncated)") : ""
        return note.isEmpty ? text : "\(text)\n\(note)"
    }

    private func readWholeInput() async {
        let sha = item.call.inputBlobSha256
        guard partialIn, !sha.isEmpty, doneIn?.done != true, doneIn?.failed != true, let api = model.api else { return }
        do {
            let data = try await api.downloadBlob(sessionID, sha256: sha)
            let text = String(decoding: data, as: UTF8.self)
            ToolOutputs.keep(inKey, text)
            wholeIn = ToolOutput(text: text, done: true, failed: false)
        } catch {
            if !Task.isCancelled { wholeIn = ToolOutput(text: "", done: false, failed: true) }
        }
    }

    /// The whole output (DownloadBlob), shown once fetched.
    private func readWhole() async {
        guard partial, let r = item.result, !r.blobSha256.isEmpty, done?.done != true, done?.failed != true, let api = model.api else { return }
        do {
            let data = try await api.downloadBlob(sessionID, sha256: r.blobSha256)
            let text = String(decoding: data, as: UTF8.self)
            ToolOutputs.keep(key, text)
            whole = ToolOutput(text: text, done: true, failed: false)
        } catch {
            if !Task.isCancelled { whole = ToolOutput(text: "", done: false, failed: true) }
        }
    }
}

/// A step on its line: its verb, what it acted on (a command as code), and what it took and gave when opened (`input`
/// and `output`; nothing to open without either). The one at work shimmers; a failed one is red. In an opened group's
/// card (`inCard`): a size down, its output on the card's ground.
struct ToolLine: View {
    @Environment(\.revealStep) private var reveal
    let verb: String
    let object: String
    let shell: Bool
    let running: Bool
    let failed: Bool
    var input = ""
    let output: String
    var inCard = false
    @Binding var open: Bool

    private var expandable: Bool { !input.isEmpty || !output.isEmpty }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button {
                guard expandable else { return }
                withAnimation(.snappy(duration: 0.25)) { open.toggle() }
                // In a card that scrolls: the output just opened comes into view once it has opened.
                if open, let reveal { DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) { reveal() } }
            } label: {
                HStack(spacing: 5) {
                    Text(verb).foregroundStyle(failed ? AnyShapeStyle(.red) : AnyShapeStyle(.secondary))
                        .shimmer(running && !failed)
                        .layoutPriority(1)
                    if shell {
                        CodeText(text: object, lang: "bash").font(inCard ? .caption.monospaced() : .footnote.monospaced()).opacity(0.9)
                    } else {
                        Text(object).foregroundStyle(.secondary)
                    }
                    if expandable { Caret(open: open) }
                }
                .lineLimit(1)
                .font(inCard ? .footnote : .subheadline)
                .padding(.vertical, inCard ? 8 : 2).padding(.horizontal, inCard ? 13 : 0)
                .frame(maxWidth: inCard ? .infinity : nil, alignment: .leading)
                .background(inCard && open ? Palette.fill : .clear)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityValue(!expandable ? Text("") : open ? Text("Expanded") : Text("Collapsed"))
            if open, expandable {
                // In a card, less high: one output does not fill the card.
                OutputText(input: input, text: output, running: running, maxHeight: inCard ? 160 : 320)
                    .padding(.horizontal, inCard ? 13 : 10).padding(.top, inCard ? 0 : 8).padding(.bottom, inCard ? 10 : 8)
                    .background(inCard ? Palette.fill : Palette.code, in: RoundedRectangle(cornerRadius: inCard ? 0 : 8, style: .continuous))
                    .padding(.top, inCard ? 0 : 4).padding(.bottom, inCard ? 0 : 6)
                    .accessibilityIdentifier("tool-output")
            }
        }
    }
}

/// Where the session works, set up by its first turn (WorkspaceRow): a line of its own, as a step's, with the branch
/// as code; opened, how, the branch, its base and the folder, or why it failed. A step under 400 ms shows by its result.
struct WorkspaceLine: View {
    let row: WorkspaceRow
    @State private var open = false

    var body: some View {
        if row.state == .running {
            Delayed(active: true) { line }
        } else {
            line
        }
    }

    private var line: some View {
        let (text, code) = row.title
        let failed = row.state == .failed
        return VStack(alignment: .leading, spacing: 0) {
            Button {
                guard row.state != .running else { return }
                withAnimation(.snappy(duration: 0.25)) { open.toggle() }
            } label: {
                HStack(spacing: 5) {
                    Image(systemName: failed ? "exclamationmark.triangle" : "arrow.triangle.branch")
                        .font(.footnote).foregroundStyle(failed ? AnyShapeStyle(.red) : AnyShapeStyle(.tertiary))
                    Text(text).foregroundStyle(failed ? AnyShapeStyle(.red) : AnyShapeStyle(.secondary))
                        .shimmer(row.state == .running)
                        .layoutPriority(1)
                    if !code.isEmpty {
                        Text(verbatim: code).font(.footnote.monospaced()).foregroundStyle(.secondary).truncationMode(.middle)
                    }
                    if row.state != .running { Caret(open: open) }
                }
                .lineLimit(1)
                .font(.subheadline)
                .padding(.vertical, 2)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityValue(row.state == .running ? Text("") : open ? Text("Expanded") : Text("Collapsed"))
            .accessibilityIdentifier("thread-workspace")
            if open {
                Grid(alignment: .leading, horizontalSpacing: 12, verticalSpacing: 4) {
                    ForEach(Array(row.details.enumerated()), id: \.offset) { _, d in
                        GridRow {
                            Text(d.label).foregroundStyle(.tertiary)
                            Text(verbatim: d.value).font(.caption.monospaced()).foregroundStyle(.secondary).textSelection(.enabled)
                        }
                    }
                }
                .font(.caption)
                .padding(.horizontal, 12).padding(.vertical, 9)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Palette.fill, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                .padding(.top, 4).padding(.bottom, 6)
                .accessibilityIdentifier("thread-workspace-details")
            }
        }
    }
}

struct ToolOutput {
    let text: String
    let done: Bool
    let failed: Bool
}

/// Whole outputs read lately, so a row opened again shows at once.
@MainActor enum ToolOutputs {
    private(set) static var read: [String: String] = [:]
    private static var order: [String] = []

    static func keep(_ key: String, _ text: String) {
        order.removeAll { $0 == key }
        order.append(key)
        read[key] = text
        if order.count > 20 { read[order.removeFirst()] = nil }
    }
}

/// The small caret after a foldable line: pointing right while folded, down once open.
struct Caret: View {
    let open: Bool

    var body: some View {
        Image(systemName: "chevron.down").font(.caption2.weight(.semibold)).foregroundStyle(.tertiary)
            .rotationEffect(.degrees(open ? 0 : -90))
            .accessibilityHidden(true)
    }
}

/// A tool's input and output: the code face, grey (with ANSI SGR & unified diff colours when present),
/// wrapping, selectable, at most 320 high and scrolling inside (UITextView, which lays out a long output fast).
struct OutputText: UIViewRepresentable {
    var input = ""
    let text: String
    var running = false
    var maxHeight: CGFloat = 320

    final class Coordinator {
        var lastInput = ""
        var lastText = ""
        var lastRunning = false
        var lastDark: Bool?
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> UITextView {
        let v = UITextView()
        v.isEditable = false
        v.isSelectable = true
        v.backgroundColor = .clear
        v.textContainerInset = .zero
        v.textContainer.lineFragmentPadding = 0
        v.adjustsFontForContentSizeCategory = true
        return v
    }

    func updateUIView(_ v: UITextView, context: Context) {
        let dark = v.traitCollection.userInterfaceStyle == .dark
        let c = context.coordinator
        guard c.lastInput != input || c.lastText != text || c.lastRunning != running || c.lastDark != dark else { return }
        c.lastInput = input
        c.lastText = text
        c.lastRunning = running
        c.lastDark = dark
        if input.isEmpty {
            v.attributedText = TerminalRendering.nsAttributedString(text, dark: dark)
            return
        }
        let labelFont = UIFontMetrics(forTextStyle: .caption2).scaledFont(for: .systemFont(ofSize: 11, weight: .medium))
        let emptyFont = UIFontMetrics(forTextStyle: .caption1).scaledFont(for: .systemFont(ofSize: 12, weight: .regular))
        let headingPara = NSMutableParagraphStyle()
        headingPara.paragraphSpacing = 2
        let sectionHeadingPara = NSMutableParagraphStyle()
        sectionHeadingPara.paragraphSpacingBefore = 8
        sectionHeadingPara.paragraphSpacing = 2

        let full = NSMutableAttributedString()
        full.append(NSAttributedString(string: String(localized: "Input") + "\n", attributes: [
            .font: labelFont, .foregroundColor: UIColor.tertiaryLabel, .paragraphStyle: headingPara,
        ]))
        full.append(TerminalRendering.nsAttributedString(input, dark: dark))
        full.append(NSAttributedString(string: "\n" + String(localized: "Output") + "\n", attributes: [
            .font: labelFont, .foregroundColor: UIColor.tertiaryLabel, .paragraphStyle: sectionHeadingPara,
        ]))
        if !text.isEmpty {
            full.append(TerminalRendering.nsAttributedString(text, dark: dark))
        } else {
            let emptyWord = running ? String(localized: "Waiting for output…") : String(localized: "(no output)")
            full.append(NSAttributedString(string: emptyWord, attributes: [
                .font: emptyFont, .foregroundColor: UIColor.tertiaryLabel,
            ]))
        }
        v.attributedText = full
    }

    func sizeThatFits(_ proposal: ProposedViewSize, uiView: UITextView, context: Context) -> CGSize? {
        let width = proposal.width ?? 320
        // A long output fills the box anyway; it isn't laid out whole to find that out.
        if input.utf8.count + text.utf8.count > 16_384 { return CGSize(width: width, height: maxHeight) }
        let fit = uiView.sizeThatFits(CGSize(width: width, height: .greatestFiniteMagnitude))
        return CGSize(width: width, height: min(ceil(fit.height), maxHeight))
    }
}
