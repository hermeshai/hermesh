import Foundation
import HermeshProto

// What the owner's agents are doing, as Her's name tag, the team panel and
// Work show it (docs/her-team.md §4.2, §4.3): the sessions that wait for
// the owner, those that failed, those at work and those that are done.

public struct TeamState: Equatable {
    /// Waiting for an answer, the latest first.
    public var waiting: [Session]
    /// Requests to connect that wait.
    public var requests: Int
    /// Stopped by a failure, the latest first.
    public var failed: [Session]
    /// At work, the newest first.
    public var running: [Session]
    /// Neither at work nor in need of the owner, the latest first.
    public var finished: [Session]

    /// What the name tag says beside Her's name, the first that holds.
    public enum Tag: Equatable, Sendable {
        /// One session waits for an answer: its coding agent.
        case waiting(agent: String)
        /// Several things wait, or a request to connect.
        case needYou(Int)
        case failed(Int)
        case working(Int)
        case quiet
    }

    public var tag: Tag {
        if waiting.count == 1 && requests == 0 { return .waiting(agent: waiting[0].agent) }
        if waiting.count + requests > 0 { return .needYou(waiting.count + requests) }
        if !failed.isEmpty { return .failed(failed.count) }
        if !running.isEmpty { return .working(running.count) }
        return .quiet
    }

    /// The coding agents at work, each once, in the order of their sessions.
    public var runningAgents: [String] {
        var seen: Set<String> = []
        return running.map(\.agent).filter { seen.insert($0).inserted }
    }
}

/// The team's state from the sessions the device holds (archived ones are no part of it) and the requests to connect.
public func teamState(_ sessions: [Session], requests: Int) -> TeamState {
    let visible = sessions.filter { $0.status != .archived }
    return TeamState(
        waiting: visible.filter { $0.status == .waitingInput }.sorted { $0.updatedDate > $1.updatedDate },
        requests: requests,
        failed: visible.filter(\.isFailed).sorted { $0.updatedDate > $1.updatedDate },
        running: visible.filter(\.isActive).sorted { $0.createdDate > $1.createdDate },
        finished: visible.filter { !$0.isActive && !$0.needsAttention }.sorted { $0.updatedDate > $1.updatedDate })
}

/// A question the owner can answer with one tap where it is listed: one item, with options, one choice, no secret.
public func quickOptions(_ q: Question) -> [String]? {
    guard q.items.count == 1, let item = q.items.first, !item.options.isEmpty, !item.multiSelect, !item.secret else { return nil }
    return item.options.map(\.label)
}
