import Foundation

// Math in a reply, read as the Mac app reads it (its session/markdown/tex.ts
// and remark-math): \( … \) within a line, \[ … \] within a paragraph, and
// $$ … $$ within a line or as a block between two lines of $$, all outside
// code. Markdown would take the backslashes and read _ and * inside as
// emphasis, so the spans are lifted out of the source before it is parsed
// and put back after.

public struct MathSpan: Hashable, Sendable {
    public let tex: String
    /// Set apart from the text (\[ … \], a $$ block), not in its line.
    public let display: Bool
    /// As it was written, delimiters and all: what shows where math cannot (in code).
    public let raw: String
}

/// TeX longer than this stays text, as on the Mac.
public let maxMathLength = 5000

/// Around a lifted span's number: private-use characters, which Markdown passes through as text.
private let markOpen: Character = "\u{E000}"
private let markClose: Character = "\u{E001}"

/// A piece of text that had math lifted out of it.
public enum MathPiece: Equatable, Sendable {
    case text(String)
    /// A span, by its place in what `liftMath` returned.
    case math(Int)
}

/// The text with each math span replaced by a mark, and the spans the marks stand for.
public func liftMath(_ text: String) -> (text: String, spans: [MathSpan]) {
    guard text.contains("\\(") || text.contains("\\[") || text.contains("$$") else { return (text, []) }
    let c = Array(text)
    var out = ""
    var spans: [MathSpan] = []
    var fence: (mark: Character, count: Int)?
    var lineStart = true
    var i = 0

    func lineEnd(_ from: Int) -> Int { c[from...].firstIndex(where: \.isNewline) ?? c.count }
    func blank(_ from: Int, _ to: Int) -> Bool { c[from..<to].allSatisfy { $0 == " " || $0 == "\t" } }
    func lift(_ tex: String, display: Bool, raw: String) {
        spans.append(MathSpan(tex: tex, display: display, raw: raw))
        out.append(markOpen)
        out += String(spans.count - 1)
        out.append(markClose)
    }
    /// Where the `\)` or `\]` that closes a span opened before `from` starts.
    func closer(_ from: Int, _ close: Character, lines: Bool) -> Int? {
        var j = from
        while j + 1 < c.count, j - from <= maxMathLength {
            if c[j].isNewline {
                // \( … \) stays in its line; \[ … \] stays in its paragraph.
                if !lines || blank(j + 1, lineEnd(j + 1)) { return nil }
            } else if c[j] == "\\" {
                if c[j + 1] == close { return j }
                j += 1
            }
            j += 1
        }
        return nil
    }

    while i < c.count {
        if lineStart {
            lineStart = false
            let end = lineEnd(i)
            var j = i
            while j < end, j - i < 3, c[j] == " " { j += 1 }
            var run = j
            while run < end, c[run] == c[j] { run += 1 }
            let isFence = j < end && (c[j] == "`" || c[j] == "~") && run - j >= 3
            if let open = fence {
                if isFence, c[j] == open.mark, run - j >= open.count, blank(run, end) { fence = nil }
                out += String(c[i..<end])
                i = end
                continue
            }
            if isFence {
                fence = (c[j], run - j)
                out += String(c[i..<end])
                i = end
                continue
            }
            // A block: a line of $$, the math, a line of $$.
            if run - j == 2, c[j] == "$", blank(run, end) {
                var from = end + 1
                while from <= c.count {
                    let to = from < c.count ? lineEnd(from) : c.count
                    let line = String(c[min(from, c.count)..<to]).trimmingCharacters(in: .whitespaces)
                    if line == "$$" {
                        let tex = String(c[min(end + 1, from)..<max(end + 1, from - 1)]).trimmingCharacters(in: .whitespacesAndNewlines)
                        if !tex.isEmpty, tex.count <= maxMathLength {
                            out += String(c[i..<j])
                            lift(tex, display: true, raw: String(c[j..<to]))
                            i = to
                        }
                        break
                    }
                    from = to + 1
                }
                if i >= end { continue }
            }
        }
        let ch = c[i]
        if ch.isNewline {
            out.append(ch)
            i += 1
            lineStart = true
            continue
        }
        if ch == "`" {
            // A code span: up to the next run of as many backticks, in this paragraph.
            var n = i
            while n < c.count, c[n] == "`" { n += 1 }
            var j = n
            var close: Int?
            while j < c.count {
                if c[j].isNewline, blank(j + 1, lineEnd(j + 1)) { break }
                if c[j] == "`" {
                    var k = j
                    while k < c.count, c[k] == "`" { k += 1 }
                    if k - j == n - i { close = k; break }
                    j = k
                } else {
                    j += 1
                }
            }
            let to = close ?? n
            out += String(c[i..<to])
            i = to
            continue
        }
        if ch == "\\", i + 1 < c.count, !c[i + 1].isNewline {
            let next = c[i + 1]
            if next == "(" || next == "[", let at = closer(i + 2, next == "(" ? ")" : "]", lines: next == "[") {
                let tex = String(c[(i + 2)..<at]).trimmingCharacters(in: .whitespacesAndNewlines)
                if !tex.isEmpty {
                    lift(tex, display: next == "[", raw: String(c[i..<(at + 2)]))
                    i = at + 2
                    continue
                }
            }
            // An escape, the backslash's own included: kept as written.
            out.append(ch)
            out.append(next)
            i += 2
            continue
        }
        if ch == "$", i + 1 < c.count, c[i + 1] == "$" {
            var j = i + 2
            var close: Int?
            while j + 1 < c.count, !c[j].isNewline, j - i <= maxMathLength {
                if c[j] == "$", c[j + 1] == "$" { close = j; break }
                j += 1
            }
            if let close {
                let tex = String(c[(i + 2)..<close]).trimmingCharacters(in: .whitespaces)
                if !tex.isEmpty {
                    lift(tex, display: false, raw: String(c[i..<(close + 2)]))
                    i = close + 2
                    continue
                }
            }
            out += "$$"
            i += 2
            continue
        }
        out.append(ch)
        i += 1
    }
    return (out, spans)
}

/// Text that had math lifted out, cut at the marks.
public func mathPieces(_ text: String) -> [MathPiece] {
    var pieces: [MathPiece] = []
    var rest = text[...]
    while let open = rest.firstIndex(of: markOpen), let close = rest[open...].firstIndex(of: markClose) {
        if open > rest.startIndex { pieces.append(.text(String(rest[..<open]))) }
        if let n = Int(rest[rest.index(after: open)..<close]) { pieces.append(.math(n)) }
        rest = rest[rest.index(after: close)...]
    }
    if !rest.isEmpty { pieces.append(.text(String(rest))) }
    return pieces
}

/// The text as it was written, each mark replaced by its span's source.
public func loweringMath(_ text: String, _ spans: [MathSpan]) -> String {
    guard text.contains(markOpen) else { return text }
    return mathPieces(text).map { piece in
        switch piece {
        case .text(let s): s
        case .math(let n): spans.indices.contains(n) ? spans[n].raw : ""
        }
    }.joined()
}
