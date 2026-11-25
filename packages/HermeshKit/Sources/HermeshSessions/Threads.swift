import Foundation
import HermeshProto

/// Ordered runtime autonomy modes from strictest to broadest.
public let orderedRuntimeModes: [Hermesh_Identity_V1_RuntimeMode] = [
    .supervised,
    .autoEdits,
    .auto,
    .fullAccess,
]

public func runtimeModeRank(_ mode: Hermesh_Identity_V1_RuntimeMode) -> Int {
    switch mode {
    case .supervised: 1
    case .autoEdits: 2
    case .auto, .unspecified, .UNRECOGNIZED: 3
    case .fullAccess: 4
    }
}

/// Clamps a requested `RuntimeMode` to a principal's cryptographic `Grant` ceiling.
/// An `.unspecified` ceiling defaults to `.fullAccess` for owners or unconstrained principals.
public func clampRuntimeMode(
    _ requested: Hermesh_Identity_V1_RuntimeMode,
    ceiling: Hermesh_Identity_V1_RuntimeMode
) -> Hermesh_Identity_V1_RuntimeMode {
    let req: Hermesh_Identity_V1_RuntimeMode = requested == .unspecified ? .auto : requested
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
    case .autoEdits: String(localized: "Auto Edits", bundle: .module)
    case .auto, .unspecified, .UNRECOGNIZED: String(localized: "Auto", bundle: .module)
    case .fullAccess: String(localized: "Full Access", bundle: .module)
    }
}

public func interactionModeTitle(_ mode: Hermesh_Session_V1_InteractionMode) -> String {
    switch mode {
    case .plan: String(localized: "Plan", bundle: .module)
    default: String(localized: "Code", bundle: .module)
    }
}

public func sessionStatusTitle(_ status: Hermesh_Session_V1_SessionStatus) -> String {
    switch status {
    case .preparing: String(localized: "Preparing", bundle: .module)
    case .running: String(localized: "Running", bundle: .module)
    case .waitingInput: String(localized: "Waiting for you", bundle: .module)
    case .queued: String(localized: "Queued", bundle: .module)
    case .crashed: String(localized: "Failed", bundle: .module)
    case .archived: String(localized: "Archived", bundle: .module)
    default: String(localized: "Idle", bundle: .module)
    }
}

/// Polymorphic primary composer action in unified session UI.
public enum SessionComposerAction: String, Equatable, Sendable {
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
    hasSession: Bool,
    status: Hermesh_Session_V1_SessionStatus,
    draftNotEmpty: Bool,
    machineOnline: Bool,
    supportsLiveSteer: Bool
) -> SessionComposerAction {
    if !machineOnline {
        return .queueOffline
    }
    if !hasSession {
        return .start
    }
    let running = status == .preparing || status == .running
    if running {
        if !draftNotEmpty {
            return .stop
        }
        return supportsLiveSteer && status == .running ? .steer : .queue
    }
    if status == .waitingInput {
        return draftNotEmpty ? .queue : .stop
    }
    if status == .queued {
        return .queue
    }
    return .send
}

/// Shared localized labels for unified Session UI surfaces.
public enum SessionWorkspaceStrings {
    public static var newSession: String { String(localized: "New Thread", bundle: .module) }
    public static var approveOnce: String { String(localized: "Approve Once", bundle: .module) }
    public static var allowInSession: String { String(localized: "Allow in Thread", bundle: .module) }
    public static var decline: String { String(localized: "Decline", bundle: .module) }
    public static var approvePlan: String { String(localized: "Approve Plan & Code", bundle: .module) }
    public static var checkpoints: String { String(localized: "Checkpoints", bundle: .module) }
    public static var changes: String { String(localized: "Changes", bundle: .module) }
    public static var rewindFiles: String { String(localized: "Rewind Files", bundle: .module) }
}

/// Deterministic client-side event-sourced reducer matching Go `kernel.Engine` and `@hermesh/core`.
public func applySessionEvent(
    _ event: Hermesh_Session_V1_SessionEvent,
    to current: Hermesh_Session_V1_SessionProjection?
) -> Hermesh_Session_V1_SessionProjection {
    var next = current ?? Hermesh_Session_V1_SessionProjection()
    let prevLastSeq = next.session.progress.lastEventSeq
    if event.hasSession {
        next.session = event.session
    }
    let maxSeq = max(prevLastSeq, max(next.session.progress.lastEventSeq, event.seq))
    if next.session.progress.lastEventSeq != maxSeq {
        next.session.progress.lastEventSeq = maxSeq
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
        next.items.sort(by: { (a: Hermesh_Session_V1_TurnItem, b: Hermesh_Session_V1_TurnItem) -> Bool in
            a.seq < b.seq
        })
    }

    if event.hasBackgroundTask {
        if let idx = next.backgroundTasks.firstIndex(where: { $0.taskID == event.backgroundTask.taskID }) {
            next.backgroundTasks[idx] = event.backgroundTask
        } else {
            next.backgroundTasks.append(event.backgroundTask)
        }
    }

    if event.type == "session.rolled_back" && event.hasCheckpoint {
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
        next.backgroundTasks.removeAll { removedRunIDs.contains($0.runID) }
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

/// Runs waiting for a turn, in their order. One whose message is in the conversation already (a session kept on this
/// device for its offline machine shows its first message) is that bubble, not the queue's; a machine writes a queued
/// run's message only once it starts.
public func queuedRuns(of projection: Hermesh_Session_V1_SessionProjection?) -> [Hermesh_Session_V1_Run] {
    guard let projection else { return [] }
    let shown = Set(projection.items.filter { $0.kind == .userMessage }.map(\.runID))
    return projection.runs
        .filter { $0.status == .queued && !shown.contains($0.runID) }
        .sorted(by: { (a: Hermesh_Session_V1_Run, b: Hermesh_Session_V1_Run) -> Bool in
            a.queueOrder < b.queueOrder
        })
}

public func pendingRequest(of projection: Hermesh_Session_V1_SessionProjection?) -> Hermesh_Session_V1_RuntimeRequest? {
    guard let projection else { return nil }
    let pid = projection.session.pendingRequestID
    if !pid.isEmpty, let found = projection.requests.first(where: { $0.requestID == pid && $0.status == .pending }) {
        return found
    }
    return projection.requests.first(where: { $0.status == .pending })
}

public func activeBackgroundTasks(of projection: Hermesh_Session_V1_SessionProjection?) -> [Hermesh_Session_V1_BackgroundTask] {
    guard let projection else { return [] }
    return projection.backgroundTasks
        .filter { $0.status == .running }
        .sorted { a, b in
            if a.awaited != b.awaited { return a.awaited && !b.awaited }
            return a.startedAt.seconds < b.startedAt.seconds
        }
}

public func backgroundTasks(of projection: Hermesh_Session_V1_SessionProjection?) -> [Hermesh_Session_V1_BackgroundTask] {
    guard let projection else { return [] }
    return projection.backgroundTasks.sorted { a, b in
        let ar = a.status == .running
        let br = b.status == .running
        if ar != br { return ar }
        if ar {
            if a.awaited != b.awaited { return a.awaited && !b.awaited }
            return a.startedAt.seconds < b.startedAt.seconds
        }
        let at = a.hasCompletedAt ? a.completedAt.seconds : a.startedAt.seconds
        let bt = b.hasCompletedAt ? b.completedAt.seconds : b.startedAt.seconds
        return at > bt
    }
}

public func activeBackgroundItemIDs(of projection: Hermesh_Session_V1_SessionProjection?) -> Set<String> {
    guard let projection else { return [] }
    var out = Set<String>()
    for bt in projection.backgroundTasks where bt.status == .running && !bt.itemID.isEmpty {
        out.insert(bt.itemID)
    }
    return out
}
