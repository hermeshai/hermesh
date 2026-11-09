import HermeshSessions
import Markdown
import SwiftUI

/// An agent's reply as GitHub Markdown (swift-markdown), as the Mac app
/// shows it: raw HTML stays text, images show their alt text, and only http
/// and https links open, so a reply never loads anything. Agents write for
/// chat: a single newline breaks the line, and a bare link stops where
/// Chinese text starts.
struct MarkdownText: View {
    let text: String
    var style: MarkdownStyle = .reply

    var body: some View {
        MarkdownBlocks(blocks: parsed(text), style: style)
    }
}

/// A reply the agent is still writing (docs/reply-streaming.md): its
/// finished blocks as they will stay, each parsed once, then the last one
/// with what is still open closed, or without a table that has no delimiter
/// row yet, so nothing shows as raw Markdown on its way.
struct DraftMarkdownText: View {
    let text: String

    var body: some View {
        var blocks = splitBlocks(text)
        let last = closingOpenMarks(withoutOpenTable(blocks.removeLast()))
        return VStack(alignment: .leading, spacing: MarkdownStyle.reply.gap) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { _, b in
                MarkdownBlocks(blocks: parsed(b), style: .reply)
            }
            if !last.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                MarkdownBlocks(blocks: parsed(last), style: .reply)
            }
        }
    }
}

/// The type a reply is set in: an agent's own (the body), Her's in her
/// bubble, or a message between agents (a size down, as on the Mac).
struct MarkdownStyle {
    var font: Font
    var code: Font
    var table: Font
    var gap: CGFloat
    /// What a fenced block is set on.
    var codeGround = Palette.code

    static let reply = MarkdownStyle(font: .body, code: .subheadline.monospaced(), table: .subheadline, gap: 12)
    /// In Her's bubble, as on the Mac: blocks closer, and code on the page's own ground so it stands out from the bubble.
    static let bubble = MarkdownStyle(font: .body, code: .subheadline.monospaced(), table: .subheadline, gap: 9, codeGround: Color(.systemBackground))
    static let small = MarkdownStyle(font: .subheadline, code: .footnote.monospaced(), table: .footnote, gap: 9)
}

// MARK: Blocks

indirect enum MarkdownBlock {
    case paragraph(AttributedString)
    case heading(level: Int, AttributedString)
    case list(ordered: Bool, start: Int, items: [MarkdownItem])
    case code(lang: String, text: String)
    case quote([MarkdownBlock])
    case table(head: [AttributedString], rows: [[AttributedString]])
    case rule
}

struct MarkdownItem {
    /// A task list item's box: checked or not; nil for a plain item.
    let checked: Bool?
    let blocks: [MarkdownBlock]
}

/// Parsed replies, so a row laid out again parses nothing.
@MainActor private var parsedReplies: [String: [MarkdownBlock]] = [:]

@MainActor private func parsed(_ text: String) -> [MarkdownBlock] {
    if let hit = parsedReplies[text] { return hit }
    let blocks = Array(Document(parsing: text).blockChildren.compactMap(block))
    if parsedReplies.count > 400 { parsedReplies.removeAll() }
    parsedReplies[text] = blocks
    return blocks
}

private func block(_ m: Markup) -> MarkdownBlock? {
    switch m {
    case let p as Paragraph: return .paragraph(inlines(p))
    case let h as Heading: return .heading(level: h.level, inlines(h))
    case let l as OrderedList: return .list(ordered: true, start: Int(l.startIndex), items: l.listItems.map(item))
    case let l as UnorderedList: return .list(ordered: false, start: 1, items: l.listItems.map(item))
    case let c as Markdown.CodeBlock: return .code(lang: c.language ?? "", text: c.code.hasSuffix("\n") ? String(c.code.dropLast()) : c.code)
    case let q as BlockQuote: return .quote(Array(q.blockChildren.compactMap(block)))
    case let t as Markdown.Table:
        return .table(head: Array(t.head.cells.map(inlines)), rows: t.body.rows.map { Array($0.cells.map(inlines)) })
    case is ThematicBreak: return .rule
    case let h as HTMLBlock: return .paragraph(AttributedString(h.rawHTML.trimmingCharacters(in: .newlines)))
    default: return nil
    }
}

private func item(_ i: ListItem) -> MarkdownItem {
    MarkdownItem(checked: i.checkbox.map { $0 == .checked }, blocks: Array(i.blockChildren.compactMap(block)))
}

private let web = #/^https?:\/\//#.ignoresCase()
/// Chinese text and punctuation: a bare link ends before them.
private let wide = #/[\x{3000}-\x{303F}\x{3400}-\x{9FFF}\x{FF00}-\x{FFEF}]/#

private func inlines(_ m: Markup) -> AttributedString {
    m.children.reduce(into: AttributedString()) { $0 += inline($1) }
}

private func inline(_ m: Markup) -> AttributedString {
    switch m {
    case let t as Markdown.Text: return bareLinks(t.string)
    case is SoftBreak, is LineBreak: return AttributedString("\n")
    case let c as InlineCode:
        var s = AttributedString(c.code)
        s.inlinePresentationIntent = .code
        return s
    case let h as InlineHTML: return AttributedString(h.rawHTML)
    case let i as Markdown.Image: return AttributedString(i.plainText)
    case let l as Markdown.Link:
        let label = l.plainText
        // A bare link stops where Chinese text starts ("见 https://x.io，然后" links https://x.io).
        if l.childCount == 1, l.child(at: 0) is Markdown.Text, label.firstMatch(of: web) != nil || label.hasPrefix("www."),
           let cut = label.firstMatch(of: wide), cut.range.lowerBound > label.startIndex {
            let shown = String(label[..<cut.range.lowerBound])
            return linked(AttributedString(shown), shown.firstMatch(of: web) != nil ? shown : "http://\(shown)")
                + AttributedString(String(label[cut.range.lowerBound...]))
        }
        return linked(inlines(l), l.destination ?? "")
    default:
        var s = inlines(m)
        let intent: InlinePresentationIntent? = switch m {
        case is Emphasis: .emphasized
        case is Strong: .stronglyEmphasized
        case is Strikethrough: .strikethrough
        default: nil
        }
        if let intent {
            for run in s.runs { s[run.range].inlinePresentationIntent = (run.inlinePresentationIntent ?? []).union(intent) }
        }
        return s
    }
}

/// Bare links in text, as GitHub links them ("https://…", "www.…"): up to a
/// space or Chinese text, without the punctuation that ends a sentence.
private func bareLinks(_ text: String) -> AttributedString {
    var out = AttributedString()
    var rest = text[...]
    while let m = rest.firstMatch(of: #/(?:https?:\/\/|www\.)[^\s<>\x{3000}-\x{303F}\x{3400}-\x{9FFF}\x{FF00}-\x{FFEF}]+/#.ignoresCase()) {
        var url = m.output[...]
        while let last = url.last, ".,:;!?'\")*_~".contains(last) { url = url.dropLast() }
        out += AttributedString(String(rest[..<m.range.lowerBound]))
        out += linked(AttributedString(String(url)), url.firstMatch(of: web) != nil ? String(url) : "http://\(url)")
        rest = rest[url.endIndex...]
    }
    return out + AttributedString(String(rest))
}

/// Text that opens its link in the browser when it is http or https; else plain.
private func linked(_ text: AttributedString, _ destination: String) -> AttributedString {
    guard destination.firstMatch(of: web) != nil, let url = URL(string: destination) else { return text }
    var s = text
    s.link = url
    return s
}

// MARK: Views

private struct MarkdownBlocks: View {
    let blocks: [MarkdownBlock]
    let style: MarkdownStyle

    var body: some View {
        VStack(alignment: .leading, spacing: style.gap) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { i, b in
                // A heading after other blocks starts a part: more room above it, as on the Mac.
                BlockView(block: b, style: style).padding(.top, i > 0 && b.isHeading ? 8 : 0)
            }
        }
    }
}

private extension MarkdownBlock {
    var isHeading: Bool { if case .heading = self { true } else { false } }
}

private struct BlockView: View {
    let block: MarkdownBlock
    let style: MarkdownStyle

    var body: some View {
        switch block {
        case .paragraph(let s):
            Text(styled(s)).font(style.font).lineSpacing(3).fixedSize(horizontal: false, vertical: true)
        case .heading(let level, let s):
            Text(styled(s)).font(level == 1 ? .title3.weight(.semibold) : .headline).fixedSize(horizontal: false, vertical: true)
                .accessibilityAddTraits(.isHeader)
        case .list(let ordered, let start, let items):
            VStack(alignment: .leading, spacing: 4) {
                ForEach(Array(items.enumerated()), id: \.offset) { i, it in
                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                        marker(ordered ? "\(start + i)." : nil, checked: it.checked).frame(minWidth: 14, alignment: .leading)
                        VStack(alignment: .leading, spacing: 6) {
                            ForEach(Array(it.blocks.enumerated()), id: \.offset) { _, b in BlockView(block: b, style: style) }
                        }
                    }
                }
            }
            .padding(.leading, 4)
        case .code(let lang, let text):
            FencedCode(text: text, lang: lang, font: style.code, ground: style.codeGround)
        case .quote(let blocks):
            MarkdownBlocks(blocks: blocks, style: style)
                .foregroundStyle(.secondary)
                .padding(.leading, 12)
                .overlay(alignment: .leading) { Rectangle().fill(Palette.fill3).frame(width: 2) }
        case .table(let head, let rows):
            // In a box with its header shaded and its numbers in columns, as on the Mac; as wide as it is, and
            // scrolling sideways once wider than the page.
            let table = Grid(alignment: .topLeading, horizontalSpacing: 0, verticalSpacing: 0) {
                GridRow { ForEach(Array(head.enumerated()), id: \.offset) { _, c in cell(c, last: rows.isEmpty).fontWeight(.semibold).background(Palette.fill) } }
                ForEach(Array(rows.enumerated()), id: \.offset) { i, r in
                    GridRow { ForEach(Array(r.enumerated()), id: \.offset) { _, c in cell(c, last: i == rows.count - 1) } }
                }
            }
            .font(style.table).monospacedDigit()
            .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous).strokeBorder(Palette.separator, lineWidth: 0.5))
            ViewThatFits(in: .horizontal) {
                table
                ScrollView(.horizontal, showsIndicators: false) { table }
            }
        case .rule:
            Rectangle().fill(Palette.separator).frame(height: 0.5)
        }
    }

    @ViewBuilder private func marker(_ number: String?, checked: Bool?) -> some View {
        if let checked {
            Image(systemName: checked ? "checkmark.square" : "square").foregroundStyle(.secondary).font(style.font)
        } else {
            Text(number ?? "•").font(style.font).foregroundStyle(.tertiary).monospacedDigit()
        }
    }

    /// A cell of a table, a hairline under it but in the last row.
    private func cell(_ s: AttributedString, last: Bool) -> some View {
        Text(styled(s)).lineSpacing(2).fixedSize(horizontal: false, vertical: true)
            .padding(.horizontal, 12).padding(.vertical, 7)
            .frame(maxWidth: 320, maxHeight: .infinity, alignment: .topLeading)
            .overlay(alignment: .bottom) { if !last { Rectangle().fill(Palette.separator).frame(height: 0.5) } }
    }

    /// Inline code in the code face, on a grey ground.
    private func styled(_ s: AttributedString) -> AttributedString {
        var out = s
        for run in s.runs where run.inlinePresentationIntent?.contains(.code) == true {
            out[run.range].font = style.code
            out[run.range].backgroundColor = Palette.fill2
        }
        return out
    }
}

/// A fenced block, as on the Mac: a bar with its language (a shell's with a
/// terminal mark) and Copy, then the code, coloured when it names its
/// language, scrolling sideways rather than wrapping.
struct FencedCode: View {
    let text: String
    let lang: String
    let font: Font
    var ground = Palette.code
    /// Just copied: the button shows a check a moment.
    @State private var copied = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 5) {
                if !lang.isEmpty {
                    if Self.shells.contains(lang.lowercased()) { Image(systemName: "terminal") }
                    Text(lang).font(.caption.monospaced())
                }
                Spacer(minLength: 8)
                Button {
                    UIPasteboard.general.string = text
                    copied = true
                } label: {
                    Label("Copy", systemImage: copied ? "checkmark" : "doc.on.doc")
                        .padding(.horizontal, 10).frame(maxHeight: .infinity).contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(copied ? Text("Copied") : Text("Copy Code"))
                .accessibilityIdentifier("code-copy")
            }
            .font(.caption).foregroundStyle(.secondary)
            .padding(.leading, 12)
            .frame(height: 30)
            .overlay(alignment: .bottom) { Rectangle().fill(Palette.separator).frame(height: 0.5) }
            ScrollView(.horizontal, showsIndicators: false) {
                Group {
                    if lang.isEmpty { SwiftUI.Text(text) } else { CodeText(text: text, lang: lang) }
                }
                .font(font).lineSpacing(4)
                .fixedSize()
                .padding(.horizontal, 12).padding(.vertical, 10)
            }
            .textSelection(.enabled)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(ground, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous).strokeBorder(Palette.separator, lineWidth: 0.5))
        .task(id: copied) {
            guard copied else { return }
            try? await Task.sleep(for: .seconds(1.5))
            copied = false
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("code-block")
    }

    /// Languages of commands to run, not code to read (the Mac's list).
    static let shells: Set<String> = ["bash", "sh", "zsh", "shell", "shellscript", "console", "terminal", "fish", "ksh", "csh", "tcsh",
                                      "powershell", "pwsh", "ps1", "cmd", "bat", "batch"]
}
