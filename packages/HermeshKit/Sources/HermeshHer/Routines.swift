import Foundation

// An assistant's routines (docs/kernel/routine-design.md §8), as the Mac's routines panel shows them (her-client's
// routines.ts): bots/subscribe lists them with each assistant, bots/routine tells one whole, bots/routine_runs its
// runs and bots/routine_run one of them with its steps. New ones and changes go through the chat.

/// A routine's latest run, for its row (her's RoutineRunBrief).
public struct HerRoutineRunBrief: Equatable, Sendable {
    /// When it started, or was due if it was skipped.
    public var started: Date?
    /// nil while it runs: "failed", "reported", "quiet", "interrupted" or "skipped".
    public var outcome: String?
    public var delivered: Date?

    init(_ j: JSON) {
        started = herDate(j["started"])
        outcome = j["outcome"].string
        delivered = herDate(j["delivered"])
    }
}

/// A routine as a list shows it (her's RoutineSummary).
public struct HerRoutine: Equatable, Identifiable, Sendable {
    public var id: String
    public var name: String
    /// The schedule in the owner's words, as the assistant wrote it.
    public var when: String
    /// "active", "paused" or "finished".
    public var state: String
    /// nil when it is paused or finished.
    public var nextRun: Date?
    public var running: Bool
    public var lastRun: HerRoutineRunBrief?

    public init?(_ j: JSON) {
        guard let id = j["id"].string else { return nil }
        self.id = id
        name = j["name"].string ?? ""
        when = j["when"].string ?? ""
        state = j["state"].string ?? "active"
        nextRun = herDate(j["next_run"])
        running = j["running"].bool ?? false
        lastRun = j["last_run"].object == nil ? nil : HerRoutineRunBrief(j["last_run"])
    }
}

/// All about a routine (bots/routine's RoutineDetail).
public struct HerRoutineDetail: Equatable, Sendable {
    /// What each run is told to do.
    public var prompt: String
    /// The next runs; none while it is paused or finished.
    public var upcoming: [Date]

    public init(_ j: JSON) {
        prompt = j["prompt"].string ?? ""
        upcoming = (j["upcoming"].array ?? []).compactMap(herDate)
    }
}

/// One run of a routine (her's RoutineRunInfo).
public struct HerRoutineRun: Equatable, Identifiable, Sendable {
    public var id: String
    public var scheduled: Date?
    /// nil when it was skipped.
    public var started: Date?
    /// nil while it runs.
    public var ended: Date?
    /// nil while it runs: "failed", "reported", "quiet", "interrupted" or "skipped".
    public var outcome: String?
    /// When the assistant next messaged the owner, after a report.
    public var delivered: Date?
    /// It started well after it was due: the computer was off or asleep.
    public var late: Bool
    /// What a quiet run says it checked; why a run was skipped.
    public var note: String?
    /// What it reported, or the error it failed with.
    public var report: String?
    public var tokens: Int

    public init?(_ j: JSON) {
        guard let id = j["id"].string else { return nil }
        self.id = id
        scheduled = herDate(j["scheduled"])
        started = herDate(j["started"])
        ended = herDate(j["ended"])
        outcome = j["outcome"].string
        delivered = herDate(j["delivered"])
        late = j["late"].bool ?? false
        note = j["note"].text
        report = j["report"].text
        tokens = herTokens(totals: j["usage"])
    }

    /// When it is told by: when it started, or when it was due if it never did.
    public var at: Date? { started ?? scheduled }

    /// How long it ran, one still running up to `now`; nil when it never started, or nothing says how long.
    public func took(now: Date) -> TimeInterval? {
        guard let started else { return nil }
        guard let end = ended ?? (outcome == nil ? now : nil) else { return nil }
        return max(0, end.timeIntervalSince(started))
    }

    /// How late it started; nil when it was on time.
    public var lateBy: TimeInterval? {
        guard late, let started, let scheduled else { return nil }
        return started.timeIntervalSince(scheduled)
    }
}

/// What marks the routines' button: a run that failed last, else one running.
public enum HerRoutineDot: Equatable, Sendable { case failed, running }

public func herRoutineDot(_ routines: [HerRoutine]) -> HerRoutineDot? {
    if routines.contains(where: { $0.lastRun?.outcome == "failed" }) { return .failed }
    if routines.contains(where: \.running) { return .running }
    return nil
}

/// The list's groups: what runs on top, then what is paused, then one-offs that are over.
public func herRoutineGroups(_ routines: [HerRoutine]) -> (active: [HerRoutine], paused: [HerRoutine], finished: [HerRoutine]) {
    (routines.filter { $0.state == "active" }, routines.filter { $0.state == "paused" }, routines.filter { $0.state == "finished" })
}

/// A row's second line, the first that applies.
public enum HerRoutineLine: Equatable, Sendable {
    case running(since: Date?)
    case failed(at: Date?)
    case next(when: String, at: Date)
    case when(String)
}

public func herRoutineLine(_ r: HerRoutine) -> HerRoutineLine {
    if r.running { return .running(since: r.lastRun?.started) }
    if r.lastRun?.outcome == "failed" { return .failed(at: r.lastRun?.started) }
    if let next = r.nextRun { return .next(when: r.when, at: next) }
    return .when(r.when)
}

/// One line of a routine's runs: a run, or several quiet ones in a row told as one.
public enum HerRunRow: Equatable, Identifiable, Sendable {
    case run(HerRoutineRun)
    case quiet(key: String, runs: [HerRoutineRun])

    public var id: String {
        switch self {
        case .run(let r): r.id
        case .quiet(let key, _): "quiet:\(key)"
        }
    }
}

/// Fewer than this many quiet runs in a row are shown one by one.
private let foldFrom = 3

/// The runs as rows, newest first: quiet ones in a row fold together, unless `open` has the row's key (its first
/// run's id).
public func herRunRows(_ runs: [HerRoutineRun], open: Set<String> = []) -> [HerRunRow] {
    var rows: [HerRunRow] = []
    var i = 0
    while i < runs.count {
        var end = i
        while end < runs.count, runs[end].outcome == "quiet" { end += 1 }
        if end - i >= foldFrom, !open.contains(runs[i].id) {
            rows.append(.quiet(key: runs[i].id, runs: Array(runs[i..<end])))
            i = end
        } else if end > i {
            rows += runs[i..<end].map(HerRunRow.run)
            i = end
        } else {
            rows.append(.run(runs[i]))
            i += 1
        }
    }
    return rows
}

/// A run's steps (bots/routine_run's transcript), as a sub-agent's record shows its own: what it was told and its
/// last words, its report or what it found, are told above them, so they are left out.
public func herRunSteps(_ transcript: JSON, run: String) -> [HerStep] {
    let all = herSteps(HerSession(id: run, snapshot: ["transcript": ["items": transcript]])).filter {
        if case .said = $0.kind { return false }
        return true
    }
    if case .text? = all.last?.kind { return Array(all.dropLast()) }
    return all
}

/// A time as her writes it (RFC 3339, with or without its fractions of a second).
func herDate(_ j: JSON) -> Date? {
    guard let s = j.string else { return nil }
    let f = ISO8601DateFormatter()
    f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    if let d = f.date(from: s) { return d }
    f.formatOptions = [.withInternetDateTime]
    return f.date(from: s)
}
