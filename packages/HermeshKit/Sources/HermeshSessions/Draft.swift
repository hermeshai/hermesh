import Foundation

// A reply the agent is still writing, cut into blocks that render the same
// alone (docs/reply-streaming.md): the finished ones are parsed once, and
// only the last one changes as the reply grows. A port of @hermesh/core's
// draft.ts, and of what the Mac app asks of remend.

private let fence = #/^ {0,3}(```|~~~)/#

/// Splits Markdown into blocks that parse the same alone: at a blank line
/// followed by an unindented line, outside code fences (an indented line
/// may go on a list item).
public func splitBlocks(_ text: String) -> [String] {
    var blocks: [String] = []
    var cur: [Substring] = []
    var open = ""
    var blank = false
    for line in text.split(separator: "\n", omittingEmptySubsequences: false) {
        let f = line.firstMatch(of: fence).map { String($0.output.1) }
        if open.isEmpty, blank, !line.trimmingCharacters(in: .whitespaces).isEmpty, !(line.first?.isWhitespace ?? false), !cur.isEmpty {
            blocks.append(cur.joined(separator: "\n"))
            cur = []
        }
        if let f { open = open.isEmpty ? f : f == open ? "" : open }
        blank = open.isEmpty && line.trimmingCharacters(in: .whitespaces).isEmpty
        cur.append(line)
    }
    blocks.append(cur.joined(separator: "\n"))
    return blocks
}

private func cells(_ row: Substring) -> [Substring] {
    var r = row.trimmingCharacters(in: .whitespaces)[...]
    if r.hasPrefix("|") { r = r.dropFirst() }
    if r.hasSuffix("|") { r = r.dropLast() }
    return r.split(separator: "|", omittingEmptySubsequences: false)
}

/// The block without a table at its end whose delimiter row (|---|---|) has not come yet.
public func withoutOpenTable(_ block: String) -> String {
    let lines = block.split(separator: "\n", omittingEmptySubsequences: false)
    var start = lines.count
    while start > 0, lines[start - 1].drop(while: { $0 == " " || $0 == "\t" }).hasPrefix("|") { start -= 1 }
    let table = lines[start...]
    if table.isEmpty { return block }
    let rows = Array(table)
    let ready = rows.count >= 2 && cells(rows[1]).count == cells(rows[0]).count
        && cells(rows[1]).allSatisfy { $0.wholeMatch(of: #/\s*:?-+:?\s*/#) != nil }
    return ready ? block : lines[..<start].joined(separator: "\n")
}

/// The last block of a draft with what is still open closed, so nothing
/// shows as raw Markdown on its way: an unfinished link as its text, then
/// inline code, bold, italic and strikethrough. A code fence needs nothing
/// (it runs to the end).
public func closingOpenMarks(_ block: String) -> String {
    var open = ""
    for line in block.split(separator: "\n", omittingEmptySubsequences: false) {
        if let f = line.firstMatch(of: fence).map({ String($0.output.1) }) { open = open.isEmpty ? f : f == open ? "" : open }
    }
    if !open.isEmpty { return block }
    var s = block
    // [text](https://exa… → text; [text… → text
    if let m = s.firstMatch(of: #/\[([^\[\]]*)\]\([^)]*$/#) ?? s.firstMatch(of: #/\[([^\[\]]*)$/#) {
        s.replaceSubrange(m.range, with: m.output.1)
    }
    var code = false
    var stars = 0, singles = 0, tildes = 0
    var i = s.startIndex
    var lineStart = true
    while i < s.endIndex {
        let c = s[i]
        if c == "\\" { i = s.index(after: i); if i < s.endIndex { i = s.index(after: i) }; lineStart = false; continue }
        if c == "`" { code.toggle() }
        if !code {
            if c == "*" {
                var n = 0
                var j = i
                while j < s.endIndex, s[j] == "*" { n += 1; j = s.index(after: j) }
                // "* " at the start of a line is a list item.
                let item = lineStart && n == 1 && j < s.endIndex && s[j] == " "
                if !item { stars += n / 2; singles += n % 2 }
                i = j
                lineStart = false
                continue
            }
            if c == "~", s[s.index(after: i)...].hasPrefix("~") { tildes += 1; i = s.index(i, offsetBy: 2); lineStart = false; continue }
        }
        lineStart = c == "\n" || (lineStart && (c == " " || c == "\t"))
        i = s.index(after: i)
    }
    if code { s += "`" }
    if singles % 2 == 1 { s += "*" }
    if stars % 2 == 1 { s += "**" }
    if tildes % 2 == 1 { s += "~~" }
    return s
}
