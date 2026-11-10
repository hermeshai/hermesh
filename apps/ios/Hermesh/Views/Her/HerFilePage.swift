import SwiftUI

/// How a file Her sent reads on a page of the app's: Markdown as the conversation renders it, or code in the Mac's
/// colours. Quick Look shows the rest (a PDF, a picture, a document) as it looks, but it shows Markdown with its
/// marks, and code in a language it has no type for not at all.
enum HerFileKind: Equatable {
    case markdown
    /// Its language, by a name the highlighter knows it by.
    case code(String)

    /// By the file's name: Markdown by its extension; code when its extension (or its name, as "Dockerfile") names a
    /// language, but not a web page or a table, which Quick Look lays out. Nil for Quick Look's, as for a file over
    /// 1 MiB, which a page here would be slow to lay out.
    init?(name: String, size: Int) {
        guard size <= 1 << 20 else { return nil }
        let ext = (name as NSString).pathExtension.lowercased()
        if ["md", "markdown", "mdown", "mkd", "mkdn"].contains(ext) {
            self = .markdown
            return
        }
        let lang = ext.isEmpty ? name.lowercased() : ext
        guard Highlighter.languages.contains(lang), !["html", "csv", "tsv"].contains(lang) else { return nil }
        self = .code(lang)
    }
}

/// A file Her sent, brought to this iPhone and read, for its page.
struct HerFileText: Identifiable {
    let url: URL
    let text: String
    let kind: HerFileKind
    var id: URL { url }
}

/// A file Her sent, on a page of its own: Markdown as the conversation renders it, or code in numbered lines, each
/// wrapped to the page. Either is laid out as it scrolls into view, so a long file opens at once. Share saves it or
/// sends it on.
struct HerFilePage: View {
    let file: HerFileText

    var body: some View {
        NavigationStack {
            ScrollView {
                switch file.kind {
                case .markdown:
                    MarkdownText(text: file.text, lazy: true).frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal, 20)
                case .code(let lang):
                    HerCodeLines(text: file.text, lang: lang).padding(.horizontal, 13)
                }
            }
            .contentMargins(.top, 13, for: .scrollContent)
            .contentMargins(.bottom, 34, for: .scrollContent)
            .accessibilityIdentifier("her-file-page")
            .navigationTitle(file.url.lastPathComponent)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                CloseButton()
                ToolbarItem(placement: .primaryAction) {
                    ShareLink(item: file.url).accessibilityIdentifier("her-file-share")
                }
            }
        }
    }
}

/// Code in numbered lines, coloured as the Mac colours it once the highlighter has read it: plain until then, and
/// for code too long to colour quickly.
private struct HerCodeLines: View {
    let code: String
    let lines: [Substring]
    let lang: String
    @Environment(\.colorScheme) private var scheme
    @State private var colored: Highlighter.Lines?

    init(text: String, lang: String) {
        // Its lines as the highlighter splits them, without an empty one after the last newline.
        var code = text.replacingOccurrences(of: "\r\n", with: "\n")
        if code.hasSuffix("\n") { code.removeLast() }
        self.code = code
        self.lines = code.split(separator: "\n", omittingEmptySubsequences: false)
        self.lang = lang
    }

    var body: some View {
        let widest = String(repeating: "0", count: String(lines.count).count)
        let colors = colored.flatMap { $0.count == lines.count ? $0 : nil }
        LazyVStack(alignment: .leading, spacing: 0) {
            ForEach(lines.indices, id: \.self) { i in
                HStack(alignment: .firstTextBaseline, spacing: 13) {
                    // The number, right-aligned in a column as wide as the last one's.
                    ZStack(alignment: .trailing) {
                        Text(widest).hidden()
                        Text(String(i + 1)).foregroundStyle(.tertiary)
                    }
                    .accessibilityHidden(true)
                    Text(colors.map { Highlighter.text($0[i], dark: scheme == .dark) } ?? AttributedString(lines[i]))
                        .lineSpacing(2)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .padding(.vertical, 1.5)
            }
        }
        .font(.footnote.monospaced())
        .task { colored = await Highlighter.shared.lines(code, lang: lang) }
    }
}
