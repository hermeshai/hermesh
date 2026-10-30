import JavaScriptCore
import SwiftUI

/// Code in GitHub's colours, light or dark with the app: the Mac app's
/// Shiki, run in JavaScriptCore (scripts/highlighter). Plain until its
/// language has loaded, each one the first time it is shown, and plain for
/// a language Shiki doesn't know.
struct CodeText: View {
    let text: String
    let lang: String
    @Environment(\.colorScheme) private var scheme
    /// The text last coloured and its lines; while a draft grows, what was
    /// coloured stays and the rest follows plain until its turn.
    @State private var colored: (text: String, lines: Highlighter.Lines)?

    init(text: String, lang: String) {
        self.text = text
        self.lang = lang
        _colored = State(initialValue: Highlighter.shared.cached(text, lang: lang).map { (text, $0) })
    }

    var body: some View {
        Text(attributed)
            .task(id: "\(lang)\u{0}\(text)") {
                if colored?.text == text { return }
                try? await Task.sleep(for: .milliseconds(80)) // a draft grows many times a second
                if Task.isCancelled { return }
                if let lines = await Highlighter.shared.lines(text, lang: lang), !Task.isCancelled { colored = (text, lines) }
            }
    }

    private var attributed: AttributedString {
        guard let colored, text.hasPrefix(colored.text) else { return AttributedString(text) }
        var out = AttributedString()
        for (i, line) in colored.lines.enumerated() {
            if i > 0 { out += AttributedString("\n") }
            for t in line {
                var run = AttributedString(t.text)
                if let c = scheme == .dark ? t.dark : t.light { run.foregroundColor = c }
                if t.style & 1 != 0 { run.inlinePresentationIntent = .emphasized }
                if t.style & 2 != 0 { run.inlinePresentationIntent = (run.inlinePresentationIntent ?? []).union(.stronglyEmphasized) }
                out += run
            }
        }
        out += AttributedString(text.dropFirst(colored.text.count))
        return out
    }
}

/// Shiki in one JavaScriptCore context, used from one queue; what it
/// coloured lately is kept, so a row shown again is coloured at once.
nonisolated final class Highlighter: @unchecked Sendable {
    static let shared = Highlighter()

    struct Token {
        let text: String
        let light: Color?
        let dark: Color?
        /// Shiki's font style: 1 italic, 2 bold.
        let style: Int
    }

    typealias Lines = [[Token]]

    private final class Box {
        let lines: Lines
        init(_ lines: Lines) { self.lines = lines }
    }

    private let queue = DispatchQueue(label: "hermesh.highlighter", qos: .userInitiated)
    private let cache = NSCache<NSString, Box>()
    /// On the queue: Shiki's highlight function, once loaded; nil if it can't be.
    private var highlight: JSValue?
    private var loaded = false

    private init() { cache.countLimit = 200 }

    func cached(_ code: String, lang: String) -> Lines? { cache.object(forKey: key(code, lang))?.lines }

    func lines(_ code: String, lang: String) async -> Lines? {
        if let hit = cached(code, lang: lang) { return hit }
        return await withCheckedContinuation { done in
            queue.async {
                let lines = self.run(code, lang: lang)
                if let lines { self.cache.setObject(Box(lines), forKey: self.key(code, lang)) }
                done.resume(returning: lines)
            }
        }
    }

    private func key(_ code: String, _ lang: String) -> NSString { "\(lang)\u{0}\(code)" as NSString }

    private func run(_ code: String, lang: String) -> Lines? {
        if !loaded {
            loaded = true
            highlight = Self.load()
        }
        guard let highlight, let json = highlight.call(withArguments: [code, lang]), json.isString,
              let raw = try? JSONSerialization.jsonObject(with: Data(json.toString().utf8)) as? [[[Any]]] else { return nil }
        return raw.map { line in
            line.map { t in
                Token(text: t[0] as? String ?? "", light: Self.color(t[1] as? String), dark: Self.color(t[2] as? String), style: t[3] as? Int ?? 0)
            }
        }
    }

    /// A Shiki colour, "#1F2328" or "#1F232880"; nil for anything else.
    private static func color(_ hex: String?) -> Color? {
        guard let hex, hex.hasPrefix("#"), hex.count == 7 || hex.count == 9, let v = UInt64(hex.dropFirst(), radix: 16) else { return nil }
        let rgba = hex.count == 7 ? v << 8 | 0xff : v
        return Color(.sRGB, red: Double(rgba >> 24 & 0xff) / 255, green: Double(rgba >> 16 & 0xff) / 255,
                     blue: Double(rgba >> 8 & 0xff) / 255, opacity: Double(rgba & 0xff) / 255)
    }

    /// The bundled script, with readLanguage reading each grammar from Highlighter/langs.
    private static func load() -> JSValue? {
        guard let dir = Bundle.main.url(forResource: "Highlighter", withExtension: nil),
              let script = try? String(contentsOf: dir.appendingPathComponent("highlighter.js"), encoding: .utf8),
              let context = JSContext() else { return nil }
        let read: @convention(block) (String) -> String? = { id in
            try? String(contentsOf: dir.appendingPathComponent("langs/\(id).json"), encoding: .utf8)
        }
        context.setObject(read, forKeyedSubscript: "readLanguage" as NSString)
        context.evaluateScript(script)
        let f = context.objectForKeyedSubscript("Highlighter")?.objectForKeyedSubscript("highlight")
        return f?.isObject == true ? f : nil
    }
}
