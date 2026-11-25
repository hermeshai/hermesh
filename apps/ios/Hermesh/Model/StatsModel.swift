import Foundation
import HermeshProto
import HermeshSessions
import Observation

/// Which of the overview's views shows (Live, Usage, Limits), and what Usage
/// asks for (docs/overview-usage.md §6.7): the range and the filter, and the
/// answers to their three questions (the page, the menus' choices, the weeks
/// of tokens) by question, so going back to a state shows its last answer at
/// once and the new one replaces it. Asked only while Usage shows, and again
/// when the counts move.
@MainActor @Observable
final class StatsModel {
    enum Segment: String { case live, usage, limits }

    var segment: Segment = .live
    var period: WorkPeriod = .week {
        didSet { if !isDemo { UserDefaults.standard.set(period.rawValue, forKey: "workPeriod") } }
    }
    var filter = UsageFilter()
    /// What the bars are in parts of (§6.9): the agents to start with.
    var group: UsageGroup = .agent
    /// Low-limit windows already seen by the owner, mapped to the epoch seconds when each resets.
    private(set) var seenLowLimits: [String: TimeInterval] = [:]
    private(set) var answers: [String: Hermesh_Local_V1_QueryWorkResponse] = [:]
    /// The page's last answer shown, while a new state's is asked.
    private(set) var lastPage: Hermesh_Local_V1_QueryWorkResponse?
    @ObservationIgnored private var asked: [String] = []
    @ObservationIgnored var shown = false
    @ObservationIgnored private let isDemo: Bool

    /// At most this many answers are kept.
    static let kept = 16
    private static let seenLimitsKey = "seenLowLimits"

    init(isDemo: Bool) {
        self.isDemo = isDemo
        if !isDemo {
            if let p = UserDefaults.standard.string(forKey: "workPeriod").flatMap(WorkPeriod.init) { period = p }
            if let saved = UserDefaults.standard.dictionary(forKey: Self.seenLimitsKey) as? [String: TimeInterval] { seenLowLimits = saved }
        }
    }

    func markLowLimitsSeen(_ cards: [AgentLimitsCard], now: Date) {
        let next = HermeshSessions.markLowLimitsSeen(cards, seen: seenLowLimits, now: now)
        guard next != seenLowLimits else { return }
        seenLowLimits = next
        if !isDemo { UserDefaults.standard.set(next, forKey: Self.seenLimitsKey) }
    }

    func pageRequest(_ now: Date) -> Hermesh_Local_V1_QueryWorkRequest { usagePageRequest(period, filter: filter, group: group, now: now) }
    func menusRequest(_ now: Date) -> Hermesh_Local_V1_QueryWorkRequest { usageMenusRequest(period, now: now) }
    func weeksRequest(_ now: Date) -> Hermesh_Local_V1_QueryWorkRequest { usageWeeksRequest(filter: filter, now: now) }

    func answer(_ r: Hermesh_Local_V1_QueryWorkRequest) -> Hermesh_Local_V1_QueryWorkResponse? { answers[Self.key(r)] }

    /// The page's answer for now, or the last one shown while it is asked.
    func page(_ now: Date) -> Hermesh_Local_V1_QueryWorkResponse? { answer(pageRequest(now)) ?? lastPage }

    /// Asks the three questions of the state shown. A state left meanwhile keeps nothing of what came too late.
    func load(_ api: AppAPI?, now: Date) async {
        guard let api else { return }
        for r in [pageRequest(now), menusRequest(now), weeksRequest(now)] {
            guard let res = try? await api.queryWork(r), !Task.isCancelled else { return }
            let k = Self.key(r)
            answers[k] = res
            asked.removeAll { $0 == k }
            asked.append(k)
            while asked.count > Self.kept { answers[asked.removeFirst()] = nil }
            if k == Self.key(pageRequest(now)) { lastPage = res }
        }
    }

    private static func key(_ r: Hermesh_Local_V1_QueryWorkRequest) -> String { (try? r.jsonString()) ?? "" }
}
