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

private let breakPunct: Set<Character> = Set("，。、；：！？）》」』】,.!?;:)")

private func isWide(_ scalar: Unicode.Scalar) -> Bool {
    let v = scalar.value
    return (0x2E80 ... 0x9FFF).contains(v) || (0xAC00 ... 0xD7AF).contains(v)
        || (0xF900 ... 0xFAFF).contains(v) || (0xFE30 ... 0xFE6F).contains(v)
        || (0xFF01 ... 0xFF60).contains(v) || v >= 0x20000
}

/// Finds the end index in `text` of the next full visual row starting at `from` (~28 CJK or ~56 ASCII units on iPhone),
/// or `nil` until a newline or full row arrives unless `flushTail` is true.
public func nextStreamRowEnd(_ text: String, from: String.Index, flushTail: Bool, rowUnits: Int = 56) -> String.Index? {
    guard from < text.endIndex else { return nil }
    var lead = from
    while lead < text.endIndex, text[lead] == "\n" || text[lead] == "\r" {
        lead = text.index(after: lead)
    }
    if lead >= text.endIndex { return flushTail ? text.endIndex : nil }

    if let nl = text[lead...].firstIndex(of: "\n") {
        return text.index(after: nl)
    }

    var units = 0
    var inCode = false
    var inLinkURL = false
    var lastGood: String.Index?
    var reached: String.Index?
    var i = lead

    while i < text.endIndex {
        let ch = text[i]
        let next = text.index(after: i)
        if ch == "`" {
            inCode.toggle()
            i = next
            if !inCode, units >= rowUnits - 14 { lastGood = i }
            continue
        }
        if !inCode {
            if inLinkURL {
                i = next
                if ch == ")" {
                    inLinkURL = false
                    if units >= rowUnits - 14 { lastGood = i }
                }
                continue
            }
            if ch == "]", next < text.endIndex, text[next] == "(" {
                inLinkURL = true
                i = text.index(after: next)
                continue
            }
            if ch == "*" || ch == "~" || ch == "[" {
                i = next
                continue
            }
        }

        let wide = ch.unicodeScalars.contains(where: isWide)
        units += wide ? 2 : 1
        i = next

        if !inCode, !inLinkURL {
            if ch == " " || ch == "\t" || breakPunct.contains(ch) {
                lastGood = i
            } else if wide, units >= rowUnits - 14, i == text.endIndex || !breakPunct.contains(text[i]) {
                lastGood = i
            }
        }

        if units >= rowUnits, !inCode, !inLinkURL, reached == nil {
            if ch == "*", i < text.endIndex, text[i] == "*" { continue }
            let prevASCII = ch.isASCII && (ch.isLetter || ch.isNumber || ch == "_")
            let nextASCII = i < text.endIndex && text[i].isASCII && (text[i].isLetter || text[i].isNumber || text[i] == "_")
            if !(prevASCII && nextASCII) {
                if let good = lastGood, text.distance(from: lead, to: good) >= 6, text.distance(from: good, to: i) <= 12 {
                    reached = good
                } else {
                    reached = i
                }
                break
            } else if let good = lastGood, text.distance(from: lead, to: good) >= 6 {
                reached = good
                break
            }
        }
    }

    if var cut = reached {
        while cut < text.endIndex, text[cut] == "*" { cut = text.index(after: cut) }
        if cut < text.endIndex, breakPunct.contains(text[cut]) { cut = text.index(after: cut) }
        return cut
    }

    guard flushTail else { return nil }
    let trimmed = text[lead...].trimmingCharacters(in: .whitespaces)
    if trimmed.isEmpty || trimmed.wholeMatch(of: #/([*`~|#>-]+|\|[^|\n]*)/#) != nil { return nil }
    var end = text.endIndex
    let beforeEnd = text.index(before: end)
    if beforeEnd > lead, text[beforeEnd] == "*", text[text.index(before: beforeEnd)] != "*" {
        end = beforeEnd
    }
    return end > lead ? end : nil
}
