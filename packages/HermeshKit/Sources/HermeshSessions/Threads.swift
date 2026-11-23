import Foundation
import HermeshProto

/// Ordered v2 runtime autonomy modes from strictest to broadest.
public let orderedRuntimeModes: [Hermesh_Identity_V1_RuntimeMode] = [
    .supervised,
    .autoEdits,
    .auto,
    .fullAccess,
]

public func runtimeModeRank(_ mode: Hermesh_Identity_V1_RuntimeMode) -> Int {
    switch mode {
    case .supervised: 1
    case .autoEdits, .unspecified, .UNRECOGNIZED: 2
    case .auto: 3
    case .fullAccess: 4
    }
}

/// Clamps a requested `RuntimeMode` to a principal's cryptographic `Grant` ceiling.
/// An `.unspecified` ceiling defaults to `.fullAccess` for owners or unconstrained principals.
public func clampRuntimeMode(
    _ requested: Hermesh_Identity_V1_RuntimeMode,
    ceiling: Hermesh_Identity_V1_RuntimeMode
) -> Hermesh_Identity_V1_RuntimeMode {
    let req: Hermesh_Identity_V1_RuntimeMode = requested == .unspecified ? .autoEdits : requested
    let cap: Hermesh_Identity_V1_RuntimeMode = ceiling == .unspecified ? .fullAccess : ceiling
    return runtimeModeRank(req) <= runtimeModeRank(cap) ? req : cap
}

public func isRuntimeModeAllowed(
    _ candidate: Hermesh_Identity_V1_RuntimeMode,
    ceiling: Hermesh_Identity_V1_RuntimeMode
) -> Bool {
    let cap: Hermesh_Identity_V1_RuntimeMode = ceiling == .unspecified ? .fullAccess : ceiling
    return runtimeModeRank(candidate) <= runtimeModeRank(cap)
}

public func runtimeModeTitle(_ mode: Hermesh_Identity_V1_RuntimeMode) -> String {
    switch mode {
    case .supervised: String(localized: "Supervised", bundle: .module)
    case .autoEdits, .unspecified, .UNRECOGNIZED: String(localized: "Auto Edits", bundle: .module)
    case .auto: String(localized: "Auto", bundle: .module)
    case .fullAccess: String(localized: "Full Access", bundle: .module)
    }
}

public func interactionModeTitle(_ mode: Hermesh_Thread_V2_InteractionMode) -> String {
    switch mode {
    case .plan: String(localized: "Plan", bundle: .module)
    default: String(localized: "Code", bundle: .module)
    }
}

public func threadStatusTitle(_ status: Hermesh_Thread_V2_ThreadStatus) -> String {
    switch status {
    case .preparing: String(localized: "Preparing", bundle: .module)
    case .running: String(localized: "Running", bundle: .module)
    case .waitingRequest: String(localized: "Waiting for you", bundle: .module)
    case .queued: String(localized: "Queued", bundle: .module)
    case .failed: String(localized: "Failed", bundle: .module)
    case .archived: String(localized: "Archived", bundle: .module)
    default: String(localized: "Idle", bundle: .module)
    }
}

/// Polymorphic primary composer action in `ThreadWorkspaceView`.
public enum ThreadComposerAction: String, Equatable, Sendable {
    case start
    case send
    case stop
    case steer
    case queue
    case queueOffline

    public var title: String {
        switch self {
        case .start: String(localized: "Start Thread", bundle: .module)
        case .send: String(localized: "Send", bundle: .module)
        case .stop: String(localized: "Stop", bundle: .module)
        case .steer: String(localized: "Steer", bundle: .module)
        case .queue: String(localized: "Queue", bundle: .module)
        case .queueOffline: String(localized: "Queue (Offline)", bundle: .module)
        }
    }
}

public func composerPrimaryAction(
    hasThread: Bool,
    status: Hermesh_Thread_V2_ThreadStatus,
    draftNotEmpty: Bool,
    machineOnline: Bool,
    supportsLiveSteer: Bool
) -> ThreadComposerAction {
    if !machineOnline {
        return .queueOffline
    }
    if !hasThread {
        return .start
    }
    let running = status == .preparing || status == .running
    if running {
        if !draftNotEmpty {
            return .stop
        }
        return supportsLiveSteer && status == .running ? .steer : .queue
    }
    if status == .waitingRequest {
        return draftNotEmpty ? .queue : .stop
    }
    if status == .queued {
        return .queue
    }
    return .send
}

/// Shared localized labels for v2 Thread UI surfaces.
public enum ThreadWorkspaceStrings {
    public static var newThread: String { String(localized: "New Thread", bundle: .module) }
    public static var approveOnce: String { String(localized: "Approve Once", bundle: .module) }
    public static var allowInThread: String { String(localized: "Allow in Thread", bundle: .module) }
    public static var decline: String { String(localized: "Decline", bundle: .module) }
    public static var approvePlan: String { String(localized: "Approve Plan & Code", bundle: .module) }
    public static var checkpoints: String { String(localized: "Checkpoints", bundle: .module) }
    public static var changes: String { String(localized: "Changes", bundle: .module) }
    public static var rewindFiles: String { String(localized: "Rewind Files", bundle: .module) }
}

/// Deterministic client-side event-sourced reducer matching Go `kernel.Engine` and `@hermesh/core`.
public func applyThreadEvent(
    _ event: Hermesh_Thread_V2_ThreadEvent,
    to current: Hermesh_Thread_V2_ThreadProjection?
) -> Hermesh_Thread_V2_ThreadProjection {
    var next = current ?? Hermesh_Thread_V2_ThreadProjection()
    let prevLastSeq = next.thread.lastSeq
    if event.hasThread {
        next.thread = event.thread
    }
    let maxSeq = max(prevLastSeq, max(next.thread.lastSeq, event.seq))
    if next.thread.lastSeq != maxSeq {
        next.thread.lastSeq = maxSeq
    }

    if event.hasRun {
        if let idx = next.runs.firstIndex(where: { $0.runID == event.run.runID }) {
            next.runs[idx] = event.run
        } else {
            next.runs.append(event.run)
        }
    }

    if event.type == "item.delta" && !event.targetItemID.isEmpty && !event.textDelta.isEmpty && !event.hasItem {
        if event.seq == 0 || event.seq != prevLastSeq,
           let idx = next.items.firstIndex(where: { $0.itemID == event.targetItemID }) {
            next.items[idx].text += event.textDelta
            next.items[idx].streaming = true
        }
    } else if event.hasItem {
        if let idx = next.items.firstIndex(where: { $0.itemID == event.item.itemID }) {
            next.items[idx] = event.item
        } else {
            next.items.append(event.item)
        }
        next.items.sort(by: { (a: Hermesh_Thread_V2_TurnItem, b: Hermesh_Thread_V2_TurnItem) -> Bool in
            a.seq < b.seq
        })
    }

    if event.type == "thread.rolled_back" && event.hasCheckpoint {
        let maxOrd = event.checkpoint.runOrdinal
        let removedRunIDs = Set(
            next.runs
                .filter { $0.ordinal > maxOrd && $0.status != .queued }
                .map(\.runID)
        )
        for i in next.runs.indices where removedRunIDs.contains(next.runs[i].runID) {
            next.runs[i].status = .rolledBack
        }
        next.items.removeAll { removedRunIDs.contains($0.runID) }
        next.checkpoints.removeAll { $0.runOrdinal > maxOrd }
    }

    if event.hasRequest {
        if let idx = next.requests.firstIndex(where: { $0.requestID == event.request.requestID }) {
            next.requests[idx] = event.request
        } else {
            next.requests.append(event.request)
        }
    }

    if event.hasCheckpoint {
        if let idx = next.checkpoints.firstIndex(where: { $0.checkpointID == event.checkpoint.checkpointID }) {
            next.checkpoints[idx] = event.checkpoint
        } else {
            next.checkpoints.append(event.checkpoint)
        }
    }

    return next
}

public func queuedRuns(of projection: Hermesh_Thread_V2_ThreadProjection?) -> [Hermesh_Thread_V2_Run] {
    guard let projection else { return [] }
    return projection.runs
        .filter { $0.status == .queued }
        .sorted(by: { (a: Hermesh_Thread_V2_Run, b: Hermesh_Thread_V2_Run) -> Bool in
            a.queueOrder < b.queueOrder
        })
}

public func pendingRequest(of projection: Hermesh_Thread_V2_ThreadProjection?) -> Hermesh_Thread_V2_RuntimeRequest? {
    guard let projection else { return nil }
    let pid = projection.thread.pendingRequestID
    if !pid.isEmpty, let found = projection.requests.first(where: { $0.requestID == pid && $0.status == .pending }) {
        return found
    }
    return projection.requests.first(where: { $0.status == .pending })
}
