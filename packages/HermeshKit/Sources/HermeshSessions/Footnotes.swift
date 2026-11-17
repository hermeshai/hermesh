import Foundation

// Footnotes in a reply, read as the Mac app reads them (remark-gfm): `[^label]`
// refers to a note written as `[^label]: …` on a line of its own, with the
// lines indented under it. The phone's Markdown has no footnotes, so the notes
// come out of the source before it is parsed: each reference becomes its
// number in superscript, numbered in the order the notes are first referred to,
// and the notes, in that order, show after the reply. As on the Mac, a note
// nothing refers to is left out, a reference to no note stays as written, and
// code keeps what was written.

/// The text with its notes taken out and its references numbered, and the notes' text in that order.
public func liftFootnotes(_ text: String) -> (text: String, notes: [String]) {
    guard text.contains("[^") else { return (text, []) }
    var lines = text.components(separatedBy: "\n")
    var inCode = codeLines(lines)

    // The notes, by label: the first written for a label counts. Their lines leave the text.
    var defined: [String: String] = [:]
    var i = 0
    while i < lines.count {
        guard !inCode[i], let m = lines[i].firstMatch(of: #/^ {0,3}\[\^([^\]\s]+)\]:[ \t]?(.*)$/#) else { i += 1; continue }
        var body = [String(m.output.2)]
        var end = i + 1
        // The lines indented under it, and blank lines between them.
        while end < lines.count {
            if let rest = indented(lines[end]) {
                body.append(rest)
            } else if lines[end].allSatisfy(\.isWhitespace), end + 1 < lines.count, indented(lines[end + 1]) != nil {
                body.append("")
            } else {
                break
            }
            end += 1
        }
        let label = String(m.output.1).lowercased()
        if defined[label] == nil { defined[label] = body.joined(separator: "\n") }
        lines.removeSubrange(i ..< end)
        inCode.removeSubrange(i ..< end)
    }
    guard !defined.isEmpty else { return (text, []) }

    var order: [String] = []
    let out = lines.indices.map { n in
        inCode[n] ? lines[n] : outsideCodeSpans(lines[n]) { part in
            part.replacing(#/\[\^([^\]\s]+)\]/#) { m in
                let label = String(m.output.1).lowercased()
                guard defined[label] != nil else { return String(m.output.0) }
                if !order.contains(label) { order.append(label) }
                return superscript(order.firstIndex(of: label)! + 1)
            }
        }
    }
    return (out.joined(separator: "\n"), order.map { defined[$0]! })
}

/// A number in superscript digits, as a reference to its note shows.
private func superscript(_ n: Int) -> String {
    let digits = Array("⁰¹²³⁴⁵⁶⁷⁸⁹")
    return String(String(n).map { digits[$0.wholeNumberValue!] })
}

/// A line indented under a note (four spaces or a tab), without its indent; nil for one that is not.
private func indented(_ line: String) -> String? {
    if line.hasPrefix("    ") { return String(line.dropFirst(4)) }
    if line.hasPrefix("\t") { return String(line.dropFirst()) }
    return nil
}

/// Which lines are in a fenced block, its fences with them.
private func codeLines(_ lines: [String]) -> [Bool] {
    var fence: (mark: Character, count: Int)?
    return lines.map { line in
        let body = line.drop { $0 == " " }
        guard line.count - body.count <= 3, let mark = body.first, mark == "`" || mark == "~" else { return fence != nil }
        let run = body.prefix { $0 == mark }.count
        if let open = fence {
            if mark == open.mark, run >= open.count, body.dropFirst(run).allSatisfy(\.isWhitespace) { fence = nil }
            return true
        }
        if run >= 3 { fence = (mark, run); return true }
        return false
    }
}

/// The line with `change` made to what is outside its code spans (a run of backticks up to the next as long).
private func outsideCodeSpans(_ line: String, _ change: (String) -> String) -> String {
    var out = ""
    var rest = line[...]
    while let open = rest.firstIndex(of: "`") {
        let run = rest[open...].prefix { $0 == "`" }
        let ticks = String(run)
        let after = rest[run.endIndex...]
        // The closing run: as many backticks, not part of a longer run.
        var close = after.startIndex
        var found: Range<Substring.Index>?
        while let r = after[close...].range(of: ticks) {
            let longer = (r.upperBound < after.endIndex && after[r.upperBound] == "`") || (r.lowerBound > after.startIndex && after[after.index(before: r.lowerBound)] == "`")
            if !longer { found = r; break }
            close = after[r.upperBound...].firstIndex { $0 != "`" } ?? after.endIndex
        }
        guard let found else { break }
        out += change(String(rest[..<open])) + String(rest[open ..< found.upperBound])
        rest = rest[found.upperBound...]
    }
    return out + change(String(rest))
}
