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
    var basePath = ""
    /// Its blocks laid out as they scroll into view (a file's page), not all at once (a reply).
    var lazy = false

    @Environment(\.sessionFiles) private var files
    @Environment(\.filesRun) private var run

    var body: some View {
        let entry = parsedReply(text, basePath: basePath)
        MarkdownBlocks(blocks: entry.blocks, style: style, lazy: lazy)
            .environment(\.replyImages, entry.images)
            // In a session's reply, which of the paths it writes are files is asked of its machine once.
            .task(id: files == nil ? "" : "\(run)\u{0}\(basePath)\u{0}\(text)") {
                if let files {
                    files.registerGallery(run: run, gallery: entry.images.gallery)
                    files.want(run: run, paths: entry.filePaths)
                }
            }
    }
}

/// A reply the agent is still writing (docs/reply-streaming.md): its
/// finished blocks as they will stay, each parsed once, then the last one
/// with what is still open closed, or without a table that has no delimiter
/// row yet, so nothing shows as raw Markdown on its way.
struct DraftMarkdownText: View, Equatable {
    let text: String
    var animatedFromBlock: Int = .max

    var body: some View {
        var blocks = splitBlocks(unwrapCarousels(text))
        let last = closingOpenMarks(withoutOpenTable(blocks.removeLast()))
        let settledCount = blocks.count
        return VStack(alignment: .leading, spacing: MarkdownStyle.reply.gap) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { i, b in
                MarkdownBlocks(blocks: parsed(b), style: .reply)
                    .modifier(SilkWipeEffect(active: i >= animatedFromBlock))
            }
            if !last.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                MarkdownBlocks(blocks: parsed(last), style: .reply, draft: true, wipeTail: settledCount >= animatedFromBlock || animatedFromBlock < .max)
            }
        }
    }
}

private final class StreamingLatestBox {
    var text = ""
}

/// Releases a live reply one visual row at a time with a left-to-right silk wipe mask,
/// and settles to static `MarkdownText` with zero overhead on completed replies.
struct StreamingMarkdownText: View {
    private let text: String
    let streaming: Bool
    @Environment(\.accessibilityReduceMotion) private var still
    @State private var shownCount: Int = 0
    @State private var animating = false
    @State private var startBlock: Int = .max
    @State private var initialized = false
    @State private var latestBox = StreamingLatestBox()

    init(text: String, streaming: Bool) {
        self.text = unwrapCarousels(text)
        self.streaming = streaming
    }

    var body: some View {
        let _ = { latestBox.text = text }()
        if !streaming && !animating {
            MarkdownText(text: text)
                .onChange(of: streaming) { _, now in
                    if now && !still {
                        shownCount = firstRowCount(of: text)
                        startBlock = 0
                        animating = true
                    }
                }
        } else {
            let visible = streaming && !still ? String(text.prefix(min(shownCount, text.count))) : text
            DraftMarkdownText(text: visible, animatedFromBlock: still ? .max : startBlock)
                .equatable()
                .onAppear {
                    guard !initialized else { return }
                    initialized = true
                    if streaming && !still {
                        shownCount = firstRowCount(of: text)
                        startBlock = 0
                        animating = true
                    } else {
                        shownCount = text.count
                    }
                }
                .onChange(of: streaming) { _, now in
                    if !now {
                        shownCount = text.count
                        Task { @MainActor in
                            try? await Task.sleep(for: .milliseconds(720))
                            if !streaming { animating = false }
                        }
                    }
                }
                .task(id: streaming && !still) {
                    guard streaming, !still else { return }
                    while !Task.isCancelled, streaming {
                        let cur = latestBox.text
                        if shownCount >= cur.count {
                            try? await Task.sleep(for: .milliseconds(50))
                            continue
                        }
                        let idx = cur.index(cur.startIndex, offsetBy: min(shownCount, cur.count))
                        let ready = nextStreamRowEnd(cur, from: idx, flushTail: false)
                        let cadenceMs = cur.count - shownCount > 220 ? 55 : 120
                        try? await Task.sleep(for: .milliseconds(ready != nil ? cadenceMs : 160))
                        guard !Task.isCancelled, streaming else { return }
                        let latest = latestBox.text
                        let curIdx = latest.index(latest.startIndex, offsetBy: min(shownCount, latest.count))
                        if let cut = nextStreamRowEnd(latest, from: curIdx, flushTail: ready == nil) {
                            let nextCount = latest.distance(from: latest.startIndex, to: cut)
                            if nextCount > shownCount { shownCount = nextCount }
                        }
                    }
                }
        }
    }

    private func firstRowCount(of s: String) -> Int {
        guard let cut = nextStreamRowEnd(s, from: s.startIndex, flushTail: false) else { return 0 }
        return s.distance(from: s.startIndex, to: cut)
    }
}

/// One-shot 300% horizontal alpha mask wipe matching `.md-wipe` on the Mac; removes its mask once settled.
private struct SilkWipeEffect: ViewModifier {
    let active: Bool
    @Environment(\.accessibilityReduceMotion) private var still
    @State private var progress: CGFloat = 0
    @State private var settled = false

    func body(content: Content) -> some View {
        if !active || still || settled {
            content
        } else {
            content
                .mask {
                    GeometryReader { geo in
                        let w = max(1, geo.size.width)
                        LinearGradient(
                            stops: [
                                .init(color: .black, location: 0),
                                .init(color: .black, location: 0.335),
                                .init(color: .black.opacity(0.72), location: 0.43),
                                .init(color: .black.opacity(0.22), location: 0.56),
                                .init(color: .clear, location: 0.665),
                                .init(color: .clear, location: 1),
                            ],
                            startPoint: .leading,
                            endPoint: .trailing
                        )
                        .frame(width: w * 3, height: geo.size.height)
                        .offset(x: -2 * w * (1 - progress))
                    }
                }
                .onAppear {
                    withAnimation(.timingCurve(0.24, 0.86, 0.28, 1, duration: 0.68)) {
                        progress = 1
                    }
                    Task { @MainActor in
                        try? await Task.sleep(for: .milliseconds(710))
                        settled = true
                    }
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
    /// The sizes of `font` and `table`, for math set beside them.
    var size = UIFont.TextStyle.body
    var tableSize = UIFont.TextStyle.subheadline

    static let reply = MarkdownStyle(font: .body, code: .subheadline.monospaced(), table: .subheadline, gap: 12)
    /// In Her's bubble, as on the Mac: blocks closer, and code on the page's own ground so it stands out from the bubble.
    static let bubble = MarkdownStyle(font: .body, code: .subheadline.monospaced(), table: .subheadline, gap: 9, codeGround: Color(.systemBackground))
    static let small = MarkdownStyle(font: .subheadline, code: .footnote.monospaced(), table: .footnote, gap: 9, size: .subheadline, tableSize: .footnote)
}

// MARK: Blocks

enum CalloutKind: String {
    case note, tip, important, warning, caution

    var color: Color {
        switch self {
        case .note, .important: .accentColor
        case .tip: .green
        case .warning: .orange
        case .caution: .red
        }
    }
}

indirect enum MarkdownBlock {
    case paragraph(AttributedString)
    case images(items: [ReplyImageItem], fromLinks: Bool)
    case heading(level: Int, AttributedString)
    case list(ordered: Bool, start: Int, items: [MarkdownItem])
    case code(lang: String, text: String)
    case quote([MarkdownBlock])
    case callout(CalloutKind, [MarkdownBlock])
    /// Its rows as wide as its head, each column set as its delimiter row says.
    case table(head: [AttributedString], rows: [[AttributedString]], align: [Markdown.Table.ColumnAlignment?])
    case rule
    /// Math set apart from the text: its TeX.
    case math(String)
    /// A Mermaid diagram: its source.
    case diagram(String)
    /// The notes its references point to, in their order, after the reply (HermeshSessions' Footnotes.swift).
    case footnotes([[MarkdownBlock]])
}

struct MarkdownItem {
    /// A task list item's box: checked or not; nil for a plain item.
    let checked: Bool?
    let blocks: [MarkdownBlock]
}

private struct ParsedReply {
    let blocks: [MarkdownBlock]
    let images: ReplyImages
    let filePaths: [String]
}

/// Parsed replies, so a row laid out again parses nothing.
@MainActor private var parsedReplies: [String: ParsedReply] = [:]
@MainActor private var parsedRepliesOrder: [String] = []
@MainActor private var longestWordCache: [String: CGFloat] = [:]
@MainActor private var parsedMemoryHookInstalled = false

@MainActor private func parsed(_ text: String, basePath: String = "") -> [MarkdownBlock] {
    parsedReply(text, basePath: basePath).blocks
}

@MainActor private func parsedReply(_ text: String, basePath: String = "") -> ParsedReply {
    if !parsedMemoryHookInstalled {
        parsedMemoryHookInstalled = true
        NotificationCenter.default.addObserver(forName: UIApplication.didReceiveMemoryWarningNotification, object: nil, queue: .main) { _ in
            MainActor.assumeIsolated {
                parsedReplies.removeAll()
                parsedRepliesOrder.removeAll()
                longestWordCache.removeAll()
                fencedAnalysisCache.removeAll()
            }
        }
    }
    let cacheKey = basePath.isEmpty ? text : "\(basePath)\u{0}\(text)"
    if let hit = parsedReplies[cacheKey] { return hit }
    // Footnotes come out of the source before Markdown reads it, which has none of its own, and show after it.
    let notes = liftFootnotes(unwrapCarousels(text))
    var blocks = markdownBlocks(notes.text, basePath: basePath)
    if !notes.notes.isEmpty { blocks.append(.footnotes(notes.notes.map { markdownBlocks($0, basePath: basePath) })) }
    let entry = ParsedReply(blocks: blocks, images: replyImagesOf(blocks), filePaths: fileRefPaths(blocks))
    if parsedRepliesOrder.count >= 1200 {
        let dropCount = 240
        for old in parsedRepliesOrder.prefix(dropCount) { parsedReplies.removeValue(forKey: old) }
        parsedRepliesOrder.removeFirst(dropCount)
    }
    parsedReplies[cacheKey] = entry
    parsedRepliesOrder.append(cacheKey)
    return entry
}

private func markdownBlocks(_ text: String, basePath: String = "") -> [MarkdownBlock] {
    // Math comes out of the source before Markdown reads it, and goes back in after (HermeshSessions' Math.swift).
    let lifted = liftMath(text)
    let doc = Document(parsing: lifted.text)
    let explicit = collectExplicitImages(doc, basePath: basePath)
    var claimed = explicit
    return convertChildren(Array(doc.blockChildren), claimed: &claimed, topLevel: true, basePath: basePath).flatMap { withMath($0, lifted.spans) }
}

private func replyImagesOf(_ blocks: [MarkdownBlock]) -> ReplyImages {
    var explicit: [String] = []
    var gallery: [ReplyImageItem] = []
    func walk(_ b: MarkdownBlock) {
        switch b {
        case .images(let items, let fromLinks):
            for it in items {
                if !fromLinks, !explicit.contains(where: { sameImagePath($0, it.path) }) { explicit.append(it.path) }
                if !gallery.contains(where: { sameImagePath($0.path, it.path) }) { gallery.append(it) }
            }
        case .list(_, _, let items): items.forEach { $0.blocks.forEach(walk) }
        case .quote(let bs), .callout(_, let bs): bs.forEach(walk)
        case .footnotes(let notes): notes.forEach { $0.forEach(walk) }
        default: break
        }
    }
    blocks.forEach(walk)
    return ReplyImages(explicit: explicit, gallery: gallery)
}

private func collectExplicitImages(_ root: Markup, basePath: String) -> [String] {
    var out: [String] = []
    func walk(_ m: Markup) {
        if let img = m as? Markdown.Image, let raw = sessionImageRef(img.source ?? "") {
            let p = resolveDocPath(basePath, raw)
            if !out.contains(where: { sameImagePath($0, p) }) { out.append(p) }
            return
        }
        for c in m.children { walk(c) }
    }
    walk(root)
    return out
}

private func collectLinkedImages(_ root: Markup, claimed: inout [String], basePath: String) -> [ReplyImageItem] {
    var out: [ReplyImageItem] = []
    func walk(_ m: Markup) {
        if m is InlineCode || m is Markdown.CodeBlock { return }
        if let link = m as? Markdown.Link {
            if let raw = sessionImageRef(link.destination ?? "") {
                let p = resolveDocPath(basePath, raw)
                if !claimed.contains(where: { sameImagePath($0, p) }),
                   !out.contains(where: { sameImagePath($0.path, p) }) {
                    claimed.append(p)
                    out.append(ReplyImageItem(path: p, caption: link.plainText.trimmingCharacters(in: .whitespacesAndNewlines)))
                }
            }
            return
        }
        for c in m.children { walk(c) }
    }
    walk(root)
    return out
}

private let calloutHead = #/^\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\](?:[ \t]+|\n|$)/#.ignoresCase()

private func parseCallout(_ blocks: [MarkdownBlock]) -> (CalloutKind, [MarkdownBlock])? {
    guard case .paragraph(let first)? = blocks.first else { return nil }
    let plain = String(first.characters)
    guard let m = plain.firstMatch(of: calloutHead),
          let kind = CalloutKind(rawValue: m.output.1.lowercased()) else { return nil }
    let dropCount = plain.distance(from: plain.startIndex, to: m.range.upperBound)
    let cutIdx = first.characters.index(first.startIndex, offsetBy: dropCount)
    let rest = trimmed(first[cutIdx ..< first.endIndex])
    var tail = Array(blocks.dropFirst())
    if !rest.characters.isEmpty { tail.insert(.paragraph(rest), at: 0) }
    return (kind, tail)
}

private func appendMarkdownBlock(_ b: MarkdownBlock, to out: inout [MarkdownBlock]) {
    if case .images(let nextItems, false) = b,
       case .images(var prevItems, false)? = out.last {
        for it in nextItems where !prevItems.contains(where: { sameImagePath($0.path, it.path) }) {
            prevItems.append(it)
        }
        out[out.count - 1] = .images(items: prevItems, fromLinks: false)
    } else {
        out.append(b)
    }
}

private func pureImageListItems(_ items: [MarkdownItem]) -> [ReplyImageItem]? {
    var out: [ReplyImageItem] = []
    for it in items {
        guard it.checked == nil, it.blocks.count == 1,
              case .images(let sub, false) = it.blocks[0], !sub.isEmpty else { return nil }
        for img in sub where !out.contains(where: { sameImagePath($0.path, img.path) }) {
            out.append(img)
        }
    }
    return out.count >= 2 ? out : nil
}

private func convertChildren(_ children: [BlockMarkup], claimed: inout [String], topLevel: Bool, basePath: String) -> [MarkdownBlock] {
    var out: [MarkdownBlock] = []
    for m in children {
        switch m {
        case let p as Paragraph:
            for pb in paragraphBlocks(p, basePath: basePath) {
                appendMarkdownBlock(pb, to: &out)
            }
            if topLevel {
                let linked = collectLinkedImages(p, claimed: &claimed, basePath: basePath)
                if !linked.isEmpty { out.append(.images(items: linked, fromLinks: true)) }
            }
        case let l as OrderedList:
            let mapped: [MarkdownItem] = Array(l.listItems).map { item($0, claimed: &claimed, basePath: basePath) }
            let linked = topLevel ? collectLinkedImages(l, claimed: &claimed, basePath: basePath) : []
            if linked.isEmpty, let pure = pureImageListItems(mapped) {
                appendMarkdownBlock(.images(items: pure, fromLinks: false), to: &out)
            } else {
                out.append(.list(ordered: true, start: Int(l.startIndex), items: mapped))
            }
            if !linked.isEmpty { out.append(.images(items: linked, fromLinks: true)) }
        case let l as UnorderedList:
            let mapped: [MarkdownItem] = Array(l.listItems).map { item($0, claimed: &claimed, basePath: basePath) }
            let linked = topLevel ? collectLinkedImages(l, claimed: &claimed, basePath: basePath) : []
            if linked.isEmpty, let pure = pureImageListItems(mapped) {
                appendMarkdownBlock(.images(items: pure, fromLinks: false), to: &out)
            } else {
                out.append(.list(ordered: false, start: 1, items: mapped))
            }
            if !linked.isEmpty { out.append(.images(items: linked, fromLinks: true)) }
        case let q as BlockQuote:
            let inner = convertChildren(Array(q.blockChildren), claimed: &claimed, topLevel: false, basePath: basePath)
            if let (kind, calloutBlocks) = parseCallout(inner), !calloutBlocks.isEmpty {
                out.append(.callout(kind, calloutBlocks))
            } else {
                out.append(.quote(inner))
            }
            if topLevel {
                let linked = collectLinkedImages(q, claimed: &claimed, basePath: basePath)
                if !linked.isEmpty { out.append(.images(items: linked, fromLinks: true)) }
            }
        default:
            if let b = block(m, basePath: basePath) { out.append(b) }
        }
    }
    return out
}

private func paragraphBlocks(_ p: Paragraph, basePath: String) -> [MarkdownBlock] {
    var out: [MarkdownBlock] = []
    var prose = AttributedString()
    var imgBuf: [ReplyImageItem] = []
    func flushProse() {
        let part = trimmed(prose[prose.startIndex ..< prose.endIndex])
        prose = AttributedString()
        if !part.characters.isEmpty { out.append(.paragraph(part)) }
    }
    func flushImgs() {
        if !imgBuf.isEmpty {
            out.append(.images(items: imgBuf, fromLinks: false))
            imgBuf.removeAll()
        }
    }
    for c in p.children {
        if let img = c as? Markdown.Image, let raw = sessionImageRef(img.source ?? "") {
            let path = resolveDocPath(basePath, raw)
            flushProse()
            if !imgBuf.contains(where: { sameImagePath($0.path, path) }) {
                imgBuf.append(ReplyImageItem(path: path, caption: img.plainText.trimmingCharacters(in: .whitespacesAndNewlines)))
            }
            continue
        }
        if !imgBuf.isEmpty {
            if c is SoftBreak || c is LineBreak { continue }
            if let t = c as? Markdown.Text, t.string.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { continue }
        }
        flushImgs()
        prose += inline(c, basePath: basePath)
    }
    flushImgs()
    flushProse()
    return out
}

private func block(_ m: Markup, basePath: String) -> MarkdownBlock? {
    switch m {
    case let p as Paragraph: return .paragraph(inlines(p, basePath: basePath))
    case let h as Heading: return .heading(level: h.level, inlines(h, basePath: basePath))
    case let c as Markdown.CodeBlock: return .code(lang: c.language ?? "", text: c.code.hasSuffix("\n") ? String(c.code.dropLast()) : c.code)
    case let t as Markdown.Table:
        let head = Array(t.head.cells.map { inlines($0, basePath: basePath) })
        // A row short of cells is filled out; cells past the head's are not shown, as GitHub does.
        let rows: [[AttributedString]] = t.body.rows.map { r in (Array(r.cells.map { inlines($0, basePath: basePath) }) + Array(repeating: AttributedString(), count: head.count)).prefix(head.count).map { $0 } }
        return .table(head: head, rows: rows, align: (0 ..< head.count).map { $0 < t.columnAlignments.count ? t.columnAlignments[$0] : nil })
    case is ThematicBreak: return .rule
    case let h as HTMLBlock: return .paragraph(AttributedString(h.rawHTML.trimmingCharacters(in: .newlines)))
    default: return nil
    }
}

/// A block with its math and diagrams in place: the marks `liftMath` left become runs that carry
/// their TeX, math set apart leaves its paragraph for a block of its own, and a fenced block of
/// math or Mermaid becomes one. Code keeps what was written.
private func withMath(_ block: MarkdownBlock, _ spans: [MathSpan]) -> [MarkdownBlock] {
    switch block {
    case .paragraph(let s):
        let text = withMath(s, spans)
        guard text.runs.contains(where: { $0[MathAttribute.self]?.display == true }) else { return [.paragraph(text)] }
        var out: [MarkdownBlock] = []
        var from = text.startIndex
        func flush(_ to: AttributedString.Index) {
            let part = trimmed(text[from..<to])
            if !part.characters.isEmpty { out.append(.paragraph(part)) }
        }
        for run in text.runs {
            guard let span = run[MathAttribute.self], span.display else { continue }
            flush(run.range.lowerBound)
            out.append(.math(span.tex))
            from = run.range.upperBound
        }
        flush(text.endIndex)
        return out
    case .heading(let level, let s): return [.heading(level: level, withMath(s, spans))]
    case .list(let ordered, let start, let items):
        return [.list(ordered: ordered, start: start, items: items.map { MarkdownItem(checked: $0.checked, blocks: $0.blocks.flatMap { withMath($0, spans) }) })]
    case .code(let lang, let text):
        let source = loweringMath(text, spans)
        switch lang.lowercased() {
        case "mermaid": return [.diagram(source)]
        case "math": return [source.count <= maxMathLength ? .math(source) : .code(lang: "latex", text: source)]
        default: return [.code(lang: lang, text: source)]
        }
    case .quote(let blocks): return [.quote(blocks.flatMap { withMath($0, spans) })]
    case .callout(let kind, let blocks): return [.callout(kind, blocks.flatMap { withMath($0, spans) })]
    case .table(let head, let rows, let align): return [.table(head: head.map { withMath($0, spans) }, rows: rows.map { $0.map { withMath($0, spans) } }, align: align)]
    case .images, .rule, .math, .diagram, .footnotes: return [block]
    }
}

private func withMath(_ text: AttributedString, _ spans: [MathSpan]) -> AttributedString {
    let pieces = mathPieces(String(text.characters))
    guard pieces.contains(where: { if case .math = $0 { true } else { false } }) else { return text }
    var out = AttributedString()
    var at = text.startIndex
    for piece in pieces {
        switch piece {
        case .text(let s):
            let to = text.characters.index(at, offsetBy: s.count)
            out += AttributedString(text[at..<to])
            at = to
        case .math(let n):
            // The mark: its two ends and its number.
            let to = text.characters.index(at, offsetBy: String(n).count + 2)
            if spans.indices.contains(n) {
                var math = AttributedString(spans[n].tex)
                math[MathAttribute.self] = spans[n]
                out += math
            }
            at = to
        }
    }
    return out
}

/// Text without the line ends and spaces around it.
private func trimmed(_ text: AttributedSubstring) -> AttributedString {
    var from = text.startIndex
    var to = text.endIndex
    while from < to, text.characters[from].isWhitespace { from = text.characters.index(after: from) }
    while from < to, text.characters[text.characters.index(before: to)].isWhitespace { to = text.characters.index(before: to) }
    return AttributedString(text[from..<to])
}

private func item(_ i: ListItem, claimed: inout [String], basePath: String) -> MarkdownItem {
    MarkdownItem(checked: i.checkbox.map { $0 == .checked }, blocks: convertChildren(Array(i.blockChildren), claimed: &claimed, topLevel: false, basePath: basePath))
}

private let web = #/^https?:\/\//#.ignoresCase()
/// Chinese text and punctuation: a bare link ends before them.
private let wide = #/[\x{3000}-\x{303F}\x{3400}-\x{9FFF}\x{FF00}-\x{FFEF}]/#

private func inlines(_ m: Markup, basePath: String) -> AttributedString {
    m.children.reduce(into: AttributedString()) { $0 += inline($1, basePath: basePath) }
}

private func inline(_ m: Markup, basePath: String) -> AttributedString {
    switch m {
    case let t as Markdown.Text: return bareLinks(t.string)
    case is SoftBreak, is LineBreak: return AttributedString("\n")
    case let c as InlineCode:
        var s = AttributedString(c.code)
        s.inlinePresentationIntent = .code
        // A path the session's machine may say is a file (SessionFiles.swift): a tag once it does.
        if let r = fileOfCode(c.code) {
            s[FileRefAttribute.self] = FileRefMark(path: resolveDocPath(basePath, r.path), line: r.line, endLine: r.endLine, code: true)
        }
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
        // A link to a session's file: a tag in a session's reply (SessionFiles.swift), its words elsewhere.
        if let r = fileOfLink(l.destination ?? "") {
            var s = inlines(l, basePath: basePath)
            let resolved = (l.destination ?? "").lowercased().hasPrefix("file:") ? r.path : resolveDocPath(basePath, r.path)
            s[FileRefAttribute.self] = FileRefMark(path: resolved, line: r.line, endLine: r.endLine, code: false)
            return s
        }
        return linked(inlines(l, basePath: basePath), l.destination ?? "")
    default:
        var s = inlines(m, basePath: basePath)
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
    var lazy = false
    /// Still being written: a diagram waits as its source until it is whole.
    var draft = false
    var wipeTail = false

    var body: some View {
        if lazy || (!draft && blocks.count > 18) {
            LazyVStack(alignment: .leading, spacing: style.gap) { rows }
        } else {
            VStack(alignment: .leading, spacing: style.gap) { rows }
        }
    }

    private var rows: some View {
        ForEach(Array(blocks.enumerated()), id: \.offset) { i, b in
            // A heading after other blocks starts a part: more room above it, as on the Mac.
            BlockView(block: b, style: style, draft: draft)
                .padding(.top, i > 0 && b.isHeading ? 8 : 0)
                .modifier(SilkWipeEffect(active: wipeTail && i == blocks.count - 1))
        }
    }
}

private extension MarkdownBlock {
    var isHeading: Bool { if case .heading = self { true } else { false } }
}

private struct BlockView: View {
    let block: MarkdownBlock
    let style: MarkdownStyle
    var draft = false
    /// Math is drawn at the text's size, so it is drawn again when that changes.
    @Environment(\.dynamicTypeSize) private var typeSize
    @Environment(\.sessionFiles) private var files
    @Environment(\.filesRun) private var run

    var body: some View {
        switch block {
        case .paragraph(let s):
            text(s, style.size).font(style.font).lineSpacing(3).fixedSize(horizontal: false, vertical: true)
        case .images(let items, let fromLinks):
            if files != nil {
                SessionImageStrip(items: items, fromLinks: fromLinks, draft: draft)
            } else if !fromLinks {
                let alt = items.map(\.caption).filter { !$0.isEmpty }.joined(separator: " ")
                if !alt.isEmpty {
                    SwiftUI.Text(verbatim: alt).font(style.font)
                }
            }
        case .heading(let level, let s):
            // A step down each level, as the Mac's 20, 18, 16 and 14 beside its 15: the last three under the text's size.
            let (size, font): (UIFont.TextStyle, Font) = switch level {
            case 1: (.title2, .title2.weight(.semibold))
            case 2: (.title3, .title3.weight(.semibold))
            case 3: (.headline, .headline)
            default: (.subheadline, .subheadline.weight(.semibold))
            }
            text(s, size).font(font).fixedSize(horizontal: false, vertical: true)
                .accessibilityAddTraits(.isHeader)
        case .list(let ordered, let start, let items):
            VStack(alignment: .leading, spacing: 4) {
                ForEach(Array(items.enumerated()), id: \.offset) { i, it in
                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                        marker(ordered ? "\(start + i)." : nil, checked: it.checked).frame(minWidth: 14, alignment: .leading)
                        VStack(alignment: .leading, spacing: 6) {
                            ForEach(Array(it.blocks.enumerated()), id: \.offset) { _, b in BlockView(block: b, style: style, draft: draft) }
                        }
                    }
                }
            }
            .padding(.leading, 4)
        case .code(let lang, let text):
            FencedCode(text: text, lang: lang, font: style.code, ground: style.codeGround)
        case .quote(let blocks):
            MarkdownBlocks(blocks: blocks, style: style, draft: draft)
                .foregroundStyle(.secondary)
                .padding(.leading, 12)
                .overlay(alignment: .leading) { Rectangle().fill(Palette.fill3).frame(width: 2) }
        case .callout(let kind, let blocks):
            MarkdownBlocks(blocks: blocks, style: style, draft: draft)
                .padding(.leading, 14).padding(.trailing, 12).padding(.vertical, 10)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Palette.fill, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                .overlay(alignment: .leading) {
                    UnevenRoundedRectangle(topLeadingRadius: 10, bottomLeadingRadius: 10, style: .continuous)
                        .fill(kind.color).frame(width: 3)
                }
                .overlay { RoundedRectangle(cornerRadius: 10, style: .continuous).strokeBorder(Palette.separator, lineWidth: 0.5) }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("markdown-callout-\(kind.rawValue)")
        case .table(let head, let rows, let align):
            // In a box with its header shaded and its numbers in columns, as on the Mac; as wide as it is, its text
            // wrapping to fit the page, and scrolling sideways once its columns are as narrow as they go.
            let table = TableLayout(columns: head.count, unwrapped: Set(align.indices.filter { align[$0] == .right })) {
                ForEach(Array(head.enumerated()), id: \.offset) { c, s in cell(s, align[c], head: true, last: rows.isEmpty).background(Palette.fill, in: Rectangle()) }
                ForEach(Array(rows.enumerated()), id: \.offset) { i, r in
                    ForEach(Array(r.enumerated()), id: \.offset) { c, s in cell(s, align[c], last: i == rows.count - 1) }
                }
            }
            .font(style.table).monospacedDigit()
            // The box is the page's part the table shows in, as on the Mac: a table scrolled sideways keeps both its edges.
            ViewThatFits(in: .horizontal) {
                table
                ScrollView(.horizontal, showsIndicators: false) { table }
            }
            .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous).strokeBorder(Palette.separator, lineWidth: 0.5))
        case .rule:
            Rectangle().fill(Palette.separator).frame(height: 0.5)
        case .math(let tex):
            MathBlock(tex: tex, style: style)
        case .diagram(let source):
            if draft {
                FencedCode(text: source, lang: "mermaid", font: style.code, ground: style.codeGround)
            } else {
                DiagramBlock(source: source, style: style)
            }
        case .footnotes(let notes):
            // As on the Mac: a size down and muted, under a hairline, each by its number.
            let small = MarkdownStyle(font: .subheadline, code: .footnote.monospaced(), table: .footnote, gap: 6, codeGround: style.codeGround,
                                      size: .subheadline, tableSize: .footnote)
            VStack(alignment: .leading, spacing: 6) {
                ForEach(Array(notes.enumerated()), id: \.offset) { i, blocks in
                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                        Text(verbatim: "\(i + 1).").font(small.font).foregroundStyle(.tertiary).monospacedDigit().frame(minWidth: 14, alignment: .leading)
                        MarkdownBlocks(blocks: blocks, style: small, draft: draft)
                    }
                }
            }
            .foregroundStyle(.secondary)
            .padding(.top, 10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .overlay(alignment: .top) { Rectangle().fill(Palette.separator).frame(height: 0.5) }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("footnotes")
        }
    }

    /// A line of text: inline code in its face, math drawn at the size of the text beside it.
    /// A picture in a line has no words of its own, so a line with math reads out with its TeX.
    @ViewBuilder private func text(_ s: AttributedString, _ size: UIFont.TextStyle) -> some View {
        let line = if let files, !draft, s.runs.contains(where: { $0[FileRefAttribute.self] != nil }) {
            fileText(styled(s), files: files, run: run, size: MathSetter.size(size), code: style.code)
        } else {
            mathText(styled(s), size: MathSetter.size(size), code: style.code)
        }
        if s.runs.contains(where: { $0[MathAttribute.self] != nil }) {
            line.accessibilityLabel(SwiftUI.Text(verbatim: String(s.characters)))
        } else {
            line
        }
    }

    @ViewBuilder private func marker(_ number: String?, checked: Bool?) -> some View {
        if let checked {
            Image(systemName: checked ? "checkmark.square" : "square").foregroundStyle(.secondary).font(style.font)
        } else {
            Text(number ?? "•").font(style.font).foregroundStyle(.tertiary).monospacedDigit()
        }
    }

    /// A cell of a table, filling its place in the row, a hairline under it but in the last row.
    private func cell(_ s: AttributedString, _ align: Markdown.Table.ColumnAlignment?, head: Bool = false, last: Bool) -> some View {
        text(s, style.tableSize).fontWeight(head ? .semibold : nil).lineSpacing(2).fixedSize(horizontal: false, vertical: true)
            .multilineTextAlignment(align == .right ? .trailing : align == .center ? .center : .leading)
            .padding(.horizontal, 12).padding(.vertical, 7)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: align == .right ? .topTrailing : align == .center ? .top : .topLeading)
            .overlay(alignment: .bottom) { if !last { Rectangle().fill(Palette.separator).frame(height: 0.5) } }
            .layoutValue(key: LongestWord.self, value: longestWord(s, head: head) + 24)
    }

    /// How wide the cell's longest word is in the table's type: a run of letters between spaces, or a single Chinese
    /// character (a line may break after any).
    private func longestWord(_ s: AttributedString, head: Bool) -> CGFloat {
        let plain = String(s.characters)
        let key = "\(style.tableSize.rawValue)\u{0}\(head)\u{0}\(plain)"
        if let hit = longestWordCache[key] { return hit }
        var font = UIFont.preferredFont(forTextStyle: style.tableSize)
        if head, let bold = font.fontDescriptor.withSymbolicTraits(.traitBold) { font = UIFont(descriptor: bold, size: 0) }
        let words = plain.split { c in c.isWhitespace || c.unicodeScalars.contains { (0x3000 ... 0x9FFF).contains($0.value) || (0xFF00 ... 0xFFEF).contains($0.value) } }
        let width = words.map { ceil(String($0).size(withAttributes: [.font: font]).width) }.max() ?? 0
        if longestWordCache.count > 4000 { longestWordCache.removeAll(keepingCapacity: true) }
        longestWordCache[key] = width
        return width
    }

    /// Inline code in the code face, on a grey ground.
    private func styled(_ s: AttributedString) -> AttributedString {
        guard s.runs.contains(where: { $0.inlinePresentationIntent?.contains(.code) == true }) else { return s }
        var out = s
        for run in s.runs where run.inlinePresentationIntent?.contains(.code) == true {
            out[run.range].font = style.code
            out[run.range].backgroundColor = Palette.fill2
        }
        return out
    }
}

/// A table's cells, a row at a time, laid out as a browser lays out the Mac's: each column as wide as its widest
/// cell when the table fits the page; else each narrowed, the most where there is most to wrap, but none under its
/// floor; and with no width to fit (scrolling sideways), every column at its floor. A row is as tall as its tallest
/// cell, so no cell's lines run into the next row.
private struct TableLayout: Layout {
    let columns: Int
    /// The columns that never wrap (numbers, set to the right): their floor is their whole width.
    let unwrapped: Set<Int>
    /// How narrow a column that wraps may get (the Mac's 104px), unless a word in it is wider, up to `widestWord`.
    static let narrowest: CGFloat = 104
    /// A word wider than this (a hash, a long link) breaks rather than widen its column.
    static let widestWord: CGFloat = 180

    struct Cache {
        var subviewCount = 0
        var most: [CGFloat] = []
        var word: [CGFloat] = []
        var lastWidth: CGFloat? = .nan
        var widths: [CGFloat] = []
        var heights: [CGFloat] = []
    }

    func makeCache(subviews: Subviews) -> Cache { Cache() }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout Cache) -> CGSize {
        let (widths, heights) = measure(proposal.width, subviews, cache: &cache)
        return CGSize(width: widths.reduce(0, +), height: heights.reduce(0, +))
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout Cache) {
        let (widths, heights) = measure(bounds.width, subviews, cache: &cache)
        var y = bounds.minY
        for (r, height) in heights.enumerated() {
            var x = bounds.minX
            for (c, width) in widths.enumerated() {
                subviews[r * columns + c].place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(width: width, height: height))
                x += width
            }
            y += height
        }
    }

    private func measure(_ width: CGFloat?, _ subviews: Subviews, cache: inout Cache) -> (widths: [CGFloat], heights: [CGFloat]) {
        let rows = subviews.count / max(columns, 1)
        if cache.subviewCount != subviews.count || cache.most.count != columns {
            var most = [CGFloat](repeating: 0, count: columns)
            var word = [CGFloat](repeating: 0, count: columns)
            for (i, cell) in subviews.enumerated() {
                most[i % columns] = max(most[i % columns], ceil(cell.sizeThatFits(.unspecified).width))
                word[i % columns] = max(word[i % columns], cell[LongestWord.self])
            }
            cache.subviewCount = subviews.count
            cache.most = most
            cache.word = word
            cache.lastWidth = .nan
        }
        if !cache.widths.isEmpty, cache.lastWidth == width {
            return (cache.widths, cache.heights)
        }
        let most = cache.most
        let word = cache.word
        let least = most.indices.map { unwrapped.contains($0) ? most[$0] : min(most[$0], max(Self.narrowest, min(word[$0], Self.widestWord))) }
        var widths = least
        let room = (width ?? 0) - least.reduce(0, +)
        let give = zip(most, least).map { $0 - $1 }
        let wanted = give.reduce(0, +)
        if room > 0, wanted > 0 {
            // Not rounded: laid out at the width this gives, the table gets these same widths again.
            widths = widths.indices.map { least[$0] + give[$0] * min(1, room / wanted) }
        }
        let heights = (0 ..< rows).map { r in
            (0 ..< columns).map { c in ceil(subviews[r * columns + c].sizeThatFits(ProposedViewSize(width: widths[c], height: nil)).height) }.max() ?? 0
        }
        cache.lastWidth = width
        cache.widths = widths
        cache.heights = heights
        return (widths, heights)
    }
}

/// A table cell's longest word, with the cell's padding.
nonisolated private struct LongestWord: LayoutValueKey {
    static let defaultValue: CGFloat = 0
}

private struct FencedAnalysis {
    let eff: EffectiveCodeLang
    let secret: Bool
    let masked: String
    let copyTarget: String
    let runnablePlan: RunnableShellPlan?
    let hasHidden: Bool
}

@MainActor private var fencedAnalysisCache: [String: FencedAnalysis] = [:]

@MainActor private func analyzeFenced(lang: String, text: String) -> FencedAnalysis {
    let key = "\(lang)\u{0}\(text)"
    if let hit = fencedAnalysisCache[key] { return hit }
    let eff = effectiveCodeLang(lang: lang, text: text)
    let secret = hasSecrets(text)
    let masked = secret ? maskSecrets(text) : text
    let copyTarget = eff.shell ? cleanShellCommand(text) : text
    let runnablePlan = inspectRunnableShell(lang: lang, text: text)
    let hasHidden = hiddenChars(text)
    let result = FencedAnalysis(eff: eff, secret: secret, masked: masked, copyTarget: copyTarget, runnablePlan: runnablePlan, hasHidden: hasHidden)
    if fencedAnalysisCache.count > 160 { fencedAnalysisCache.removeAll(keepingCapacity: true) }
    fencedAnalysisCache[key] = result
    return result
}

/// A fenced block, as on the Mac: keeps the original script or code intact on top,
/// offers prompt-stripped Copy, secret masking, and 1-click workspace execution with
/// a destructive-command confirmation bar and an attached bottom terminal output drawer.
struct FencedCode: View {
    @Environment(\.sessionCodeActions) private var actions
    let text: String
    let lang: String
    let font: Font
    var ground = Palette.code
    /// Just copied: the button shows a check a moment.
    @State private var copied = false
    @State private var revealed = false
    @State private var confirming = false

    var body: some View {
        let info = analyzeFenced(lang: lang, text: text)
        let eff = info.eff
        let secret = info.secret
        let shownText = (secret && !revealed) ? info.masked : text
        let copyTarget = info.copyTarget
        let plan = (!secret || revealed) ? info.runnablePlan : nil
        let runState = CodeBlockRunStore.shared.state(sessionID: actions?.sessionID ?? "", blockText: text)
        let isRunning = runState?.status == .running

        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 5) {
                if !eff.lang.isEmpty {
                    if eff.shell { Image(systemName: "terminal") }
                    Text(verbatim: eff.lang).font(.caption.monospaced()).lineLimit(1)
                }
                if info.hasHidden {
                    Label("Hidden characters", systemImage: "exclamationmark.triangle.fill")
                        .font(.caption2.weight(.medium))
                        .foregroundStyle(.orange)
                        .lineLimit(1)
                }
                Spacer(minLength: 6)
                if secret {
                    Button {
                        revealed.toggle()
                    } label: {
                        Image(systemName: revealed ? "eye.slash" : "eye")
                            .padding(.horizontal, 8).frame(maxHeight: .infinity).contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(Text(revealed ? "Hide Secrets" : "Reveal Masked Secrets"))
                    .accessibilityIdentifier("code-secret-toggle")
                }
                if let actions, isRunning {
                    Button {
                        CodeBlockRunStore.shared.stop(sessionID: actions.sessionID, blockText: text)
                    } label: {
                        Label("Stop", systemImage: "stop.fill")
                            .foregroundStyle(.red)
                            .padding(.horizontal, 8).frame(maxHeight: .infinity).contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("code-stop")
                } else if let actions, let plan {
                    Button {
                        triggerRun(actions: actions, plan: plan)
                    } label: {
                        if runState != nil {
                            Label("Rerun", systemImage: "arrow.clockwise")
                                .padding(.horizontal, 8).frame(maxHeight: .infinity).contentShape(.rect)
                        } else if plan.destructive {
                            Label("Run…", systemImage: "play.fill")
                                .foregroundStyle(.orange)
                                .padding(.horizontal, 8).frame(maxHeight: .infinity).contentShape(.rect)
                        } else {
                            Label("Run", systemImage: "play.fill")
                                .foregroundStyle(.green)
                                .padding(.horizontal, 8).frame(maxHeight: .infinity).contentShape(.rect)
                        }
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(Text(runState != nil ? "Rerun" : "Run"))
                    .accessibilityIdentifier("code-run")
                }
                Button {
                    UIPasteboard.general.string = copyTarget
                    copied = true
                } label: {
                    Label("Copy", systemImage: copied ? "checkmark" : "doc.on.doc")
                        .padding(.horizontal, 10).frame(maxHeight: .infinity).contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(copied ? Text("Copied") : Text(eff.shell ? "Copy Command" : "Copy Code"))
                .accessibilityIdentifier("code-copy")
            }
            .font(.caption).foregroundStyle(.secondary)
            .padding(.leading, 12)
            .frame(height: 30)
            .overlay(alignment: .bottom) { Rectangle().fill(Palette.separator).frame(height: 0.5) }

            ScrollView(.horizontal, showsIndicators: false) {
                Group {
                    if eff.lang.isEmpty { SwiftUI.Text(shownText) } else { CodeText(text: shownText, lang: eff.lang) }
                }
                .font(font).lineSpacing(4)
                .fixedSize()
                .padding(.horizontal, 12).padding(.vertical, 10)
            }
            .textSelection(.enabled)

            if confirming, let actions, let plan {
                Rectangle().fill(Palette.separator).frame(height: 0.5)
                VStack(alignment: .leading, spacing: 8) {
                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                        Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                        Text("This script contains destructive operations. Run it in the current workspace?")
                            .foregroundStyle(.primary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    HStack(spacing: 12) {
                        Spacer(minLength: 0)
                        Button("Cancel") {
                            confirming = false
                        }
                        .foregroundStyle(.secondary)
                        .accessibilityIdentifier("code-confirm-cancel")
                        Button("Run Anyway") {
                            confirming = false
                            CodeBlockRunStore.shared.start(actions: actions, blockText: text, command: plan.command)
                        }
                        .fontWeight(.semibold)
                        .foregroundStyle(.orange)
                        .accessibilityIdentifier("code-confirm-run")
                    }
                }
                .font(.caption)
                .padding(.horizontal, 12).padding(.vertical, 9)
                .background(Color.orange.opacity(0.08))
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("code-confirm-bar")
            }

            if let runState, let actions {
                InlineTerminalDrawer(run: runState, actions: actions, blockText: text)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
        .background(ground, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 10, style: .continuous)
                .strokeBorder(isRunning ? Color.accentColor.opacity(0.45) : Palette.separator, lineWidth: isRunning ? 1 : 0.5)
        )
        .task(id: copied) {
            guard copied else { return }
            try? await Task.sleep(for: .seconds(1.5))
            copied = false
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("code-block")
    }

    private func triggerRun(actions: SessionCodeActions, plan: RunnableShellPlan) {
        if plan.destructive {
            confirming = true
            return
        }
        confirming = false
        CodeBlockRunStore.shared.start(actions: actions, blockText: text, command: plan.command)
    }
}
