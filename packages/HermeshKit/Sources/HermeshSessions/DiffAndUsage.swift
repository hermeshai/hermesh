import Foundation
import HermeshProto

// A session's diff, parsed for display, and the usage heatmap. Ports of
// @hermesh/core's diff.ts and usage.ts.

public struct DiffLine: Equatable, Sendable {
    public enum Kind: Sendable { case context, added, removed, note }
    public let kind: Kind
    /// The line's number before the change; nil for an added line or a note.
    public let old: Int?
    /// The line's number after the change; nil for a removed line or a note.
    public let new: Int?
    public let text: String
}

public struct DiffHunk: Equatable, Sendable {
    public let header: String
    public var lines: [DiffLine]
}

public struct DiffFile: Equatable, Sendable, Identifiable {
    public var path: String
    public var insertions = 0
    public var deletions = 0
    public var binary = false
    public var hunks: [DiffHunk] = []
    public var id: String { path }
}

/// The first number of a hunk range ("-18,4" is 18), or 0.
private func rangeStart(_ range: Substring?) -> Int {
    guard let r = range else { return 0 }
    return Int(r.dropFirst().split(separator: ",").first ?? "") ?? 0
}

/// The files of a `git diff`, with numbered lines.
public func parseUnifiedDiff(_ text: String) -> [DiffFile] {
    var files: [DiffFile] = []
    var old = 0, next = 0
    for raw in text.split(separator: "\n", omittingEmptySubsequences: false) {
        let line = String(raw)
        if line.hasPrefix("diff --git ") {
            files.append(DiffFile(path: line.components(separatedBy: " b/").last ?? line))
        } else if line.hasPrefix("+++ ") || line.hasPrefix("--- ") {
            let p = String(line.dropFirst(4))
            if !files.isEmpty, line.hasPrefix("+++ "), p != "/dev/null" { files[files.count - 1].path = p.hasPrefix("b/") ? String(p.dropFirst(2)) : p }
        } else if line.hasPrefix("Binary files ") {
            if !files.isEmpty { files[files.count - 1].binary = true }
        } else if line.hasPrefix("@@") {
            let nums = line.split(separator: " ").dropFirst().prefix(2)
            old = rangeStart(nums.first)
            next = rangeStart(nums.dropFirst().first)
            if !files.isEmpty { files[files.count - 1].hunks.append(DiffHunk(header: line, lines: [])) }
        } else if !files.isEmpty, !files[files.count - 1].hunks.isEmpty, let first = line.first {
            let f = files.count - 1, h = files[f].hunks.count - 1
            let rest = String(line.dropFirst())
            switch first {
            case "+":
                files[f].hunks[h].lines.append(DiffLine(kind: .added, old: nil, new: next, text: rest))
                files[f].insertions += 1
                next += 1
            case "-":
                files[f].hunks[h].lines.append(DiffLine(kind: .removed, old: old, new: nil, text: rest))
                files[f].deletions += 1
                old += 1
            case " ":
                files[f].hunks[h].lines.append(DiffLine(kind: .context, old: old, new: next, text: rest))
                old += 1
                next += 1
            case "\\":
                files[f].hunks[h].lines.append(DiffLine(kind: .note, old: nil, new: nil, text: line))
            default: break
            }
        }
    }
    return files
}

// MARK: Usage

/// One coding agent's turns on one day, as QueryWork counts them by day and
/// agent from usageSince (bucket 0).
public typealias UsageDay = Hermesh_Local_V1_TurnCount

public struct UsageCell: Equatable, Sendable {
    public let date: Date
    public let key: String
    public let tokens: UInt64
    public var level: Int
}

public struct UsageYear: Equatable, Sendable {
    /// weeks × 7 cells, column by column (Sunday first); nil past today.
    public var cells: [UsageCell?]
    public var weeks: Int
    public var last30: UInt64
    public var today: UInt64
    /// Days with any tokens.
    public var active: Int
    /// Days in a row with tokens, ending today (or yesterday, before today's first turn).
    public var streak: Int
    /// Each coding agent's share of the tokens, largest first.
    public var byAgent: [(agent: String, share: Double)]

    public static func == (a: UsageYear, b: UsageYear) -> Bool {
        a.cells == b.cells && a.today == b.today && a.active == b.active && a.streak == b.streak && a.byAgent.map(\.agent) == b.byAgent.map(\.agent)
    }
}

public func dayKey(_ d: Date, calendar: Calendar = .current) -> String {
    let c = calendar.dateComponents([.year, .month, .day], from: d)
    return String(format: "%04d-%02d-%02d", c.year ?? 0, c.month ?? 0, c.day ?? 0)
}

/// The first day a year of weeks ending in today's week starts on (what to ask QueryWork from).
public func usageSince(_ today: Date, weeks: Int = 53, calendar: Calendar = .current) -> Date {
    let start = calendar.startOfDay(for: today)
    let weekday = calendar.component(.weekday, from: start) - 1 // Sunday is 0
    return calendar.date(byAdding: .day, value: -weekday - (weeks - 1) * 7, to: start) ?? start
}

/// The QueryWork question for weeks of usage: from usageSince to today, by
/// day and coding agent.
public func usageRequest(_ today: Date, weeks: Int = 53, calendar: Calendar = .current) -> Hermesh_Local_V1_QueryWorkRequest {
    let since = usageSince(today, weeks: weeks, calendar: calendar)
    var r = Hermesh_Local_V1_QueryWorkRequest()
    r.timeZone = calendar.timeZone.identifier
    r.firstDay = dayKey(since, calendar: calendar)
    r.days = UInt32((calendar.dateComponents([.day], from: since, to: calendar.startOfDay(for: today)).day ?? 0) + 1)
    r.bucket = .day
    r.groupBy = [.agent]
    return r
}

public func usageYear(_ days: [UsageDay], today: Date, weeks: Int = 53, calendar: Calendar = .current) -> UsageYear {
    let since = usageSince(today, weeks: weeks, calendar: calendar)
    var tokens: [String: UInt64] = [:], agents: [String: UInt64] = [:]
    for d in days {
        let n = d.inputTokens + d.outputTokens
        let date = calendar.date(byAdding: .day, value: Int(d.bucket), to: since) ?? since
        tokens[dayKey(date, calendar: calendar), default: 0] += n
        agents[d.group.agent, default: 0] += n
    }
    let todayKey = dayKey(today, calendar: calendar)
    var cells: [UsageCell?] = []
    var past = false
    for i in 0 ..< weeks * 7 {
        let date = calendar.date(byAdding: .day, value: i, to: since) ?? since
        let key = dayKey(date, calendar: calendar)
        cells.append(past ? nil : UsageCell(date: date, key: key, tokens: tokens[key] ?? 0, level: 0))
        if key == todayKey { past = true }
    }
    // Levels by the quartiles of the days with tokens, as GitHub's.
    let used = cells.compactMap { $0?.tokens }.filter { $0 > 0 }.sorted()
    let cuts = used.isEmpty ? [] : [0.25, 0.5, 0.75].map { used[min(used.count - 1, Int($0 * Double(used.count)))] }
    for i in cells.indices {
        guard let c = cells[i] else { continue }
        cells[i]!.level = c.tokens == 0 ? 0 : min(4, 1 + cuts.filter { c.tokens >= $0 }.count)
    }
    let shown = cells.compactMap { $0 }
    var streak = 0
    var i = shown.count - ((shown.last?.tokens ?? 0) > 0 ? 1 : 2)
    while i >= 0 && shown[i].tokens > 0 {
        streak += 1
        i -= 1
    }
    let total = shown.reduce(0) { $0 + $1.tokens }
    return UsageYear(cells: cells, weeks: weeks, last30: shown.suffix(30).reduce(0) { $0 + $1.tokens }, today: shown.last?.tokens ?? 0,
                     active: used.count, streak: streak,
                     byAgent: agents.filter { $0.value > 0 }.sorted { $0.value > $1.value }.map { ($0.key, total > 0 ? Double($0.value) / Double(total) : 0) })
}
