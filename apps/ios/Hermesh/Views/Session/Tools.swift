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
    @State private var open = false

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Button {
                withAnimation(.snappy(duration: 0.25)) { open.toggle() }
            } label: {
                HStack(spacing: 6) {
                    if calls.contains(where: \.running) { Spinner() }
                    Text(stepSummary(calls)).lineLimit(1)
                    Caret(open: open)
                }
                .font(.subheadline).foregroundStyle(.secondary)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityValue(open ? Text("Expanded") : Text("Collapsed"))
            if open {
                VStack(spacing: 0) {
                    ForEach(Array(calls.enumerated()), id: \.element.seq) { i, c in
                        if i > 0 { Rectangle().fill(Palette.separator).frame(height: 0.5) }
                        ToolRow(item: c, sessionID: sessionID, inCard: true)
                    }
                }
                .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous).strokeBorder(Palette.separator, lineWidth: 1))
            }
        }
    }
}

/// One tool call: what it did, and its output when opened, read whole then
/// when the conversation holds only its start. Folded until opened, failed
/// ones too; the one running spins. A command reads as code.
struct ToolRow: View {
    @Environment(AppModel.self) private var model
    let item: ToolItem
    let sessionID: String
    /// In an opened group's card: a size down, its output on the card's ground.
    var inCard = false
    @State private var open = false
    @State private var whole: ToolOutput?

    var body: some View {
        let call = item.call
        let denied = call.decision == "deny"
        let failed = denied || item.result?.isError == true
        let out = output(denied: denied)
        VStack(alignment: .leading, spacing: 0) {
            Button {
                if !out.isEmpty { withAnimation(.snappy(duration: 0.25)) { open.toggle() } }
            } label: {
                HStack(spacing: 5) {
                    if item.running || reading { Spinner() }
                    Text(toolVerb(call, running: item.running)).foregroundStyle(failed ? AnyShapeStyle(.red) : AnyShapeStyle(.secondary))
                        .layoutPriority(1)
                    let summary = toolSummary(toolObject(call), sessionID: sessionID)
                    if call.kind == "shell" {
                        CodeText(text: summary, lang: "bash").font(inCard ? .caption.monospaced() : .footnote.monospaced()).opacity(0.9)
                    } else {
                        Text(summary).foregroundStyle(.secondary)
                    }
                    if !out.isEmpty { Caret(open: open) }
                }
                .lineLimit(1)
                .font(inCard ? .footnote : .subheadline)
                .padding(.vertical, inCard ? 8 : 2).padding(.horizontal, inCard ? 13 : 0)
                .frame(maxWidth: inCard ? .infinity : nil, alignment: .leading)
                .background(inCard && open ? Palette.fill : .clear)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityValue(out.isEmpty ? Text("") : open ? Text("Expanded") : Text("Collapsed"))
            if open, !out.isEmpty {
                OutputText(text: out)
                    .padding(.horizontal, inCard ? 13 : 10).padding(.top, inCard ? 0 : 8).padding(.bottom, inCard ? 10 : 8)
                    .background(inCard ? Palette.fill : Palette.code, in: RoundedRectangle(cornerRadius: inCard ? 0 : 8, style: .continuous))
                    .padding(.top, inCard ? 0 : 4).padding(.bottom, inCard ? 0 : 6)
                    .accessibilityIdentifier("tool-output")
            }
        }
        .task(id: open) { if open { await readWhole() } }
    }

    private var partial: Bool { item.result.map(partialOutput) ?? false }
    private var key: String { "\(sessionID)/\(item.call.callID)" }
    private var done: ToolOutput? { whole ?? ToolOutputs.read[key].map { ToolOutput(text: $0, done: true, failed: false) } }
    private var reading: Bool { open && partial && done?.done != true && done?.failed != true }

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

    /// The whole output (ReadToolOutput, docs/state-sync.md §3.5), shown as it arrives.
    private func readWhole() async {
        guard partial, let r = item.result, done?.done != true, done?.failed != true, let api = model.api else { return }
        var data = Data()
        do {
            for try await c in api.toolOutput(sessionID, callID: r.callID, seq: item.resultSeq ?? 0) {
                data.append(c.data)
                whole = ToolOutput(text: String(decoding: data, as: UTF8.self), done: false, failed: false)
            }
            let text = String(decoding: data, as: UTF8.self)
            ToolOutputs.keep(key, text)
            whole = ToolOutput(text: text, done: true, failed: false)
        } catch {
            if !Task.isCancelled { whole = ToolOutput(text: "", done: false, failed: true) }
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

/// The small spinner of a step in progress.
struct Spinner: View {
    var body: some View {
        ProgressView().controlSize(.mini).accessibilityLabel("In progress")
    }
}

/// A tool's output: the code face, grey, wrapping, selectable, at most 320
/// high and scrolling inside (UITextView, which lays out a long output fast).
struct OutputText: UIViewRepresentable {
    let text: String
    var maxHeight: CGFloat = 320

    func makeUIView(context: Context) -> UITextView {
        let v = UITextView()
        v.isEditable = false
        v.isSelectable = true
        v.backgroundColor = .clear
        v.textContainerInset = .zero
        v.textContainer.lineFragmentPadding = 0
        v.font = UIFontMetrics(forTextStyle: .caption1).scaledFont(for: .monospacedSystemFont(ofSize: 12, weight: .regular))
        v.adjustsFontForContentSizeCategory = true
        v.textColor = .secondaryLabel
        return v
    }

    func updateUIView(_ v: UITextView, context: Context) {
        if v.text != text { v.text = text }
    }

    func sizeThatFits(_ proposal: ProposedViewSize, uiView: UITextView, context: Context) -> CGSize? {
        let width = proposal.width ?? 320
        // A long output fills the box anyway; it isn't laid out whole to find that out.
        if text.utf8.count > 16_384 { return CGSize(width: width, height: maxHeight) }
        let fit = uiView.sizeThatFits(CGSize(width: width, height: .greatestFiniteMagnitude))
        return CGSize(width: width, height: min(ceil(fit.height), maxHeight))
    }
}
