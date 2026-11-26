import Foundation
import HermeshKit
import HermeshProto
import HermeshSessions
import Observation
import SwiftUI
import UIKit

enum InlineRunStatus: Equatable {
    case running
    case done
    case stopped
    case error
}

struct InlineRunState: Equatable {
    var status: InlineRunStatus
    var command: String
    var output: String
    var exitCode: Int32
    var startedAt: Date
    var durationMs: Int64
    var timedOut: Bool
    var truncated: Bool
    var folded: Bool
    var sentToChat: Bool
    var errorMessage: String?
}

struct SessionCodeActions {
    let sessionID: String
    let runStream: (String) -> AsyncThrowingStream<Hermesh_Session_V1_RunCommandEvent, Error>
    let sendOutputToChat: (_ command: String, _ exitCode: Int32, _ output: String, _ stopped: Bool) -> Void
}

extension EnvironmentValues {
    @Entry var sessionCodeActions: SessionCodeActions?
}

/// Keeps inline code-block execution state by `(sessionID, blockText)` outside view rows so scrolling
/// up and down in a long conversation never drops a running or finished terminal drawer.
@MainActor
@Observable
final class CodeBlockRunStore {
    static let shared = CodeBlockRunStore()

    private let maxRuns = 64
    private var runs: [String: InlineRunState] = [:]
    private var order: [String] = []
    @ObservationIgnored private var tasks: [String: (token: UUID, task: Task<Void, Never>)] = [:]

    private func keyOf(_ sessionID: String, _ blockText: String) -> String {
        "\(sessionID)\u{0}\(blockText)"
    }

    func state(sessionID: String, blockText: String) -> InlineRunState? {
        guard !sessionID.isEmpty else { return nil }
        return runs[keyOf(sessionID, blockText)]
    }

    private func setRun(_ key: String, _ next: InlineRunState) {
        if runs[key] == nil {
            if order.count >= maxRuns,
               let evictIdx = order.firstIndex(where: { runs[$0]?.status != .running }) {
                let evicted = order.remove(at: evictIdx)
                runs[evicted] = nil
            }
            order.append(key)
        }
        runs[key] = next
    }

    func start(actions: SessionCodeActions, blockText: String, command: String) {
        let trimmed = command.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !actions.sessionID.isEmpty, !trimmed.isEmpty else { return }
        let key = keyOf(actions.sessionID, blockText)
        tasks.removeValue(forKey: key)?.task.cancel()

        let token = UUID()
        let startedAt = Date.now
        setRun(key, InlineRunState(
            status: .running,
            command: trimmed,
            output: "",
            exitCode: 0,
            startedAt: startedAt,
            durationMs: 0,
            timedOut: false,
            truncated: false,
            folded: false,
            sentToChat: false,
            errorMessage: nil
        ))

        let stream = actions.runStream(trimmed)
        let job = Task { @MainActor [weak self] in
            var acc = ""
            do {
                for try await ev in stream {
                    guard let self, self.tasks[key]?.token == token else { return }
                    if !ev.outputDelta.isEmpty {
                        acc += ev.outputDelta
                    }
                    let folded = self.runs[key]?.folded ?? false
                    let elapsedMs = max(1, Int64(Date.now.timeIntervalSince(startedAt) * 1000))
                    if ev.done {
                        self.tasks[key] = nil
                        self.setRun(key, InlineRunState(
                            status: .done,
                            command: trimmed,
                            output: acc,
                            exitCode: ev.exitCode,
                            startedAt: startedAt,
                            durationMs: ev.durationMs > 0 ? Int64(ev.durationMs) : elapsedMs,
                            timedOut: ev.timedOut,
                            truncated: ev.truncated,
                            folded: folded,
                            sentToChat: false,
                            errorMessage: nil
                        ))
                        return
                    }
                    self.setRun(key, InlineRunState(
                        status: .running,
                        command: trimmed,
                        output: acc,
                        exitCode: 0,
                        startedAt: startedAt,
                        durationMs: elapsedMs,
                        timedOut: false,
                        truncated: ev.truncated,
                        folded: folded,
                        sentToChat: false,
                        errorMessage: nil
                    ))
                }
                guard let self, self.tasks[key]?.token == token else { return }
                self.tasks[key] = nil
                if var cur = self.runs[key], cur.status == .running {
                    cur.status = Task.isCancelled ? .stopped : .done
                    if Task.isCancelled { cur.exitCode = -1 }
                    cur.durationMs = max(1, Int64(Date.now.timeIntervalSince(startedAt) * 1000))
                    self.setRun(key, cur)
                }
            } catch {
                guard let self, self.tasks[key]?.token == token else { return }
                self.tasks[key] = nil
                let folded = self.runs[key]?.folded ?? false
                let elapsedMs = max(1, Int64(Date.now.timeIntervalSince(startedAt) * 1000))
                if Task.isCancelled || error is CancellationError {
                    self.setRun(key, InlineRunState(
                        status: .stopped,
                        command: trimmed,
                        output: acc,
                        exitCode: -1,
                        startedAt: startedAt,
                        durationMs: elapsedMs,
                        timedOut: false,
                        truncated: false,
                        folded: folded,
                        sentToChat: false,
                        errorMessage: nil
                    ))
                } else {
                    let msg = (error as? APIError)?.message ?? error.localizedDescription
                    self.setRun(key, InlineRunState(
                        status: .error,
                        command: trimmed,
                        output: acc,
                        exitCode: -1,
                        startedAt: startedAt,
                        durationMs: elapsedMs,
                        timedOut: false,
                        truncated: false,
                        folded: folded,
                        sentToChat: false,
                        errorMessage: msg
                    ))
                }
            }
        }
        tasks[key] = (token, job)
    }

    func stop(sessionID: String, blockText: String) {
        let key = keyOf(sessionID, blockText)
        guard let active = tasks.removeValue(forKey: key) else { return }
        active.task.cancel()
        if var cur = runs[key], cur.status == .running {
            cur.status = .stopped
            cur.exitCode = -1
            cur.durationMs = max(1, Int64(Date.now.timeIntervalSince(cur.startedAt) * 1000))
            setRun(key, cur)
        }
    }

    func toggleFold(sessionID: String, blockText: String) {
        let key = keyOf(sessionID, blockText)
        guard var cur = runs[key] else { return }
        cur.folded.toggle()
        setRun(key, cur)
    }

    func markSent(sessionID: String, blockText: String) {
        let key = keyOf(sessionID, blockText)
        guard var cur = runs[key] else { return }
        cur.sentToChat = true
        setRun(key, cur)
    }
}

// MARK: - Terminal Output Rendering (ANSI SGR + Unified Diff)

enum TerminalRendering {
    static let drawerBackground = Color(UIColor { traits in
        traits.userInterfaceStyle == .dark
            ? UIColor(red: 0x11 / 255, green: 0x12 / 255, blue: 0x15 / 255, alpha: 1)
            : UIColor(red: 0xED / 255, green: 0xEF / 255, blue: 0xF4 / 255, alpha: 1)
    })

    static func formatDurationMs(_ ms: Int64) -> String {
        let sec = max(0, Double(ms) / 1000)
        return sec < 10 ? String(format: "%.1fs", sec) : String(format: "%.0fs", sec)
    }

    static func uiColor(for ansi: TerminalAnsiColor, dark: Bool) -> UIColor {
        switch ansi {
        case .gray:
            return .secondaryLabel
        case .red:
            return dark ? UIColor(red: 0xF8 / 255, green: 0x71 / 255, blue: 0x71 / 255, alpha: 1)
                : UIColor(red: 0xDC / 255, green: 0x26 / 255, blue: 0x26 / 255, alpha: 1)
        case .green:
            return dark ? UIColor(red: 0x4A / 255, green: 0xDE / 255, blue: 0x80 / 255, alpha: 1)
                : UIColor(red: 0x16 / 255, green: 0xA3 / 255, blue: 0x4A / 255, alpha: 1)
        case .yellow:
            return dark ? UIColor(red: 0xFB / 255, green: 0xBF / 255, blue: 0x24 / 255, alpha: 1)
                : UIColor(red: 0xCA / 255, green: 0x8A / 255, blue: 0x04 / 255, alpha: 1)
        case .blue:
            return dark ? UIColor(red: 0x60 / 255, green: 0xA5 / 255, blue: 0xFA / 255, alpha: 1)
                : UIColor(red: 0x25 / 255, green: 0x63 / 255, blue: 0xEB / 255, alpha: 1)
        case .magenta:
            return dark ? UIColor(red: 0xC0 / 255, green: 0x84 / 255, blue: 0xFC / 255, alpha: 1)
                : UIColor(red: 0x93 / 255, green: 0x33 / 255, blue: 0xEA / 255, alpha: 1)
        case .cyan:
            return dark ? UIColor(red: 0x22 / 255, green: 0xD3 / 255, blue: 0xEE / 255, alpha: 1)
                : UIColor(red: 0x08 / 255, green: 0x91 / 255, blue: 0xB2 / 255, alpha: 1)
        case .fg:
            return .label
        case .rgb(let r, let g, let b):
            return UIColor(red: CGFloat(r) / 255, green: CGFloat(g) / 255, blue: CGFloat(b) / 255, alpha: 1)
        }
    }

    static func nsAttributedString(_ raw: String, dark: Bool, defaultColor: UIColor = .secondaryLabel) -> NSAttributedString {
        let baseFont = UIFontMetrics(forTextStyle: .caption1).scaledFont(for: .monospacedSystemFont(ofSize: 12, weight: .regular))
        let boldFont = UIFontMetrics(forTextStyle: .caption1).scaledFont(for: .monospacedSystemFont(ofSize: 12, weight: .semibold))
        let para = NSMutableParagraphStyle()
        para.lineSpacing = 2.5

        if raw.utf8.count > 32_768 {
            return NSAttributedString(
                string: stripTerminalEscapes(raw),
                attributes: [.font: baseFont, .foregroundColor: defaultColor, .paragraphStyle: para]
            )
        }

        let lines = parseTerminalLines(raw)
        let out = NSMutableAttributedString()
        for (i, line) in lines.enumerated() {
            if i > 0 {
                out.append(NSAttributedString(string: "\n", attributes: [.font: baseFont, .paragraphStyle: para]))
            }
            var diffFg: UIColor?
            var diffBg: UIColor?
            if let diff = line.diff {
                switch diff {
                case .add:
                    diffFg = uiColor(for: .green, dark: dark)
                    diffBg = uiColor(for: .green, dark: dark).withAlphaComponent(dark ? 0.12 : 0.08)
                case .del:
                    diffFg = uiColor(for: .red, dark: dark)
                    diffBg = uiColor(for: .red, dark: dark).withAlphaComponent(dark ? 0.12 : 0.08)
                case .hunk:
                    diffFg = uiColor(for: .cyan, dark: dark)
                case .meta:
                    diffFg = .tertiaryLabel
                }
            }
            for sp in line.spans {
                var attrs: [NSAttributedString.Key: Any] = [
                    .font: sp.style?.bold == true ? boldFont : baseFont,
                    .foregroundColor: diffFg ?? defaultColor,
                    .paragraphStyle: para,
                ]
                if let diffBg { attrs[.backgroundColor] = diffBg }
                if let st = sp.style {
                    var fg = st.color.map { uiColor(for: $0, dark: dark) } ?? (diffFg ?? defaultColor)
                    if st.dim { fg = fg.withAlphaComponent(0.6) }
                    attrs[.foregroundColor] = fg
                    if st.underline { attrs[.underlineStyle] = NSUnderlineStyle.single.rawValue }
                    if st.italic, let italicDesc = (attrs[.font] as? UIFont)?.fontDescriptor.withSymbolicTraits(.traitItalic) {
                        attrs[.font] = UIFont(descriptor: italicDesc, size: 0)
                    }
                }
                out.append(NSAttributedString(string: sp.text, attributes: attrs))
            }
        }
        return out
    }
}

/// Attached bottom terminal output drawer inside `FencedCode`:
/// - Left status toggle (`▾ ● Exit 0 · 0.1s` / `退出码 0 · 0.1s`) folds/unfolds the output body;
/// - Right actions hold only `↑ 发入对话` (`Send to Chat`) and an icon-only Copy Output button.
struct InlineTerminalDrawer: View {
    let run: InlineRunState
    let actions: SessionCodeActions
    let blockText: String
    @State private var copiedOutput = false

    private var isRunning: Bool { run.status == .running }
    private var canSend: Bool { run.status == .done || run.status == .stopped }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Rectangle().fill(Palette.separator).frame(height: 0.5)
            HStack(spacing: 6) {
                Button {
                    withAnimation(.snappy(duration: 0.2)) {
                        CodeBlockRunStore.shared.toggleFold(sessionID: actions.sessionID, blockText: blockText)
                    }
                } label: {
                    HStack(spacing: 6) {
                        Caret(open: !run.folded)
                        statusDot
                        statusLabelView
                    }
                    .padding(.vertical, 6)
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(Text(run.folded ? "Expand Output" : "Collapse Output"))
                .accessibilityValue(statusSummary(at: .now))
                .accessibilityIdentifier("code-term-toggle")

                Spacer(minLength: 6)

                if canSend {
                    Button {
                        CodeBlockRunStore.shared.markSent(sessionID: actions.sessionID, blockText: blockText)
                        actions.sendOutputToChat(run.command, run.exitCode, run.output, run.status == .stopped)
                    } label: {
                        HStack(spacing: 3) {
                            Image(systemName: run.sentToChat ? "checkmark" : "arrow.up")
                                .font(.caption2.weight(.semibold))
                            Text(run.sentToChat ? "Sent to chat" : "Send to Chat")
                                .lineLimit(1)
                        }
                        .font(.caption.weight(.medium))
                        .foregroundStyle(run.sentToChat ? Color.green : Color.accentColor)
                        .padding(.horizontal, 8).padding(.vertical, 4)
                        .background(
                            (run.sentToChat ? Color.green : Color.accentColor).opacity(0.12),
                            in: Capsule()
                        )
                    }
                    .buttonStyle(.plain)
                    .disabled(run.sentToChat)
                    .accessibilityIdentifier("code-send-to-chat")
                }

                Button {
                    let plain = stripTerminalEscapes(run.output.isEmpty ? (run.errorMessage ?? "") : run.output)
                    UIPasteboard.general.string = plain
                    copiedOutput = true
                } label: {
                    Image(systemName: copiedOutput ? "checkmark" : "doc.on.doc")
                        .font(.caption)
                        .foregroundStyle(copiedOutput ? Color.green : .secondary)
                        .frame(width: 28, height: 28)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(copiedOutput ? Text("Copied") : Text("Copy Output"))
                .accessibilityIdentifier("code-copy-output")
            }
            .padding(.leading, 12).padding(.trailing, 6)
            .frame(minHeight: 32)

            if !run.folded {
                Rectangle().fill(Palette.separator).frame(height: 0.5)
                Group {
                    if run.output.isEmpty {
                        Text(isRunning ? String(localized: "Waiting for output…") : (run.errorMessage ?? String(localized: "(no output)")))
                            .font(.caption.monospaced())
                            .foregroundStyle(.tertiary)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.horizontal, 12).padding(.vertical, 9)
                    } else {
                        TerminalOutputView(text: run.output, following: isRunning, maxHeight: 220)
                            .padding(.horizontal, 12).padding(.vertical, 8)
                    }
                }
                .accessibilityIdentifier("code-term-body")
            }
        }
        .background(TerminalRendering.drawerBackground)
        .task(id: copiedOutput) {
            guard copiedOutput else { return }
            try? await Task.sleep(for: .seconds(1.5))
            copiedOutput = false
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("code-term-drawer")
    }

    @ViewBuilder private var statusDot: some View {
        let color: Color = switch run.status {
        case .running: .accentColor
        case .stopped: .secondary
        case .error: .red
        case .done: (run.timedOut || run.exitCode != 0) ? .red : .green
        }
        Circle()
            .fill(color)
            .frame(width: 6, height: 6)
            .opacity(isRunning ? 0.85 : 1)
    }

    @ViewBuilder private var statusLabelView: some View {
        if isRunning {
            TimelineView(.periodic(from: .now, by: 0.1)) { ctx in
                Text(verbatim: statusSummary(at: ctx.date))
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
        } else {
            Text(verbatim: statusSummary(at: .now))
                .font(.caption.monospacedDigit())
                .foregroundStyle(statusForeground)
                .lineLimit(1)
        }
    }

    private var statusForeground: Color {
        switch run.status {
        case .running, .stopped:
            return .secondary
        case .error:
            return .red
        case .done:
            if run.timedOut || run.exitCode != 0 { return .red }
            return .green
        }
    }

    private func statusSummary(at now: Date) -> String {
        let ms = isRunning ? max(0, Int64(now.timeIntervalSince(run.startedAt) * 1000)) : run.durationMs
        let dur = TerminalRendering.formatDurationMs(ms)
        switch run.status {
        case .running:
            return String(localized: "Running · \(dur)")
        case .stopped:
            return String(localized: "Stopped · \(dur)")
        case .error:
            return run.errorMessage ?? String(localized: "Failed to run")
        case .done:
            if run.timedOut {
                return String(localized: "Timed out · \(dur)")
            }
            if run.exitCode == 0 {
                return String(localized: "Exit 0 · \(dur)")
            }
            return String(localized: "Exit \(Int(run.exitCode)) · \(dur)")
        }
    }
}

/// Selectable, ANSI/Diff-colored monospaced terminal output view that caps height at `maxHeight`
/// and auto-scrolls to the newest line while `following` is true unless the user scrolled up.
private struct TerminalOutputView: UIViewRepresentable {
    let text: String
    let following: Bool
    var maxHeight: CGFloat = 220

    final class Coordinator: NSObject, UITextViewDelegate {
        var stickToBottom = true
        var lastRendered = ""
        var lastDark: Bool?

        func scrollViewDidScroll(_ scrollView: UIScrollView) {
            guard scrollView.isTracking || scrollView.isDragging || scrollView.isDecelerating else { return }
            let maxOffset = scrollView.contentSize.height - scrollView.bounds.height
            stickToBottom = scrollView.contentOffset.y >= maxOffset - 32
        }
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
        v.delegate = context.coordinator
        return v
    }

    func updateUIView(_ v: UITextView, context: Context) {
        let dark = v.traitCollection.userInterfaceStyle == .dark
        let c = context.coordinator
        guard c.lastRendered != text || c.lastDark != dark else { return }
        c.lastRendered = text
        c.lastDark = dark
        v.attributedText = TerminalRendering.nsAttributedString(text, dark: dark, defaultColor: .label)
        if following && c.stickToBottom, v.text.utf16.count > 0 {
            let end = NSRange(location: max(0, v.text.utf16.count - 1), length: 1)
            v.scrollRangeToVisible(end)
        }
    }

    func sizeThatFits(_ proposal: ProposedViewSize, uiView: UITextView, context: Context) -> CGSize? {
        let width = proposal.width ?? 300
        if text.utf8.count > 16_384 { return CGSize(width: width, height: maxHeight) }
        let fit = uiView.sizeThatFits(CGSize(width: width, height: .greatestFiniteMagnitude))
        return CGSize(width: width, height: min(max(ceil(fit.height), 18), maxHeight))
    }
}
