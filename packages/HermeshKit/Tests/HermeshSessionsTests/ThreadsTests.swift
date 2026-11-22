import Foundation
import HermeshProto
import Testing
@testable import HermeshSessions

@Test func runtimeModeLatticeClamping() {
    #expect(clampRuntimeMode(.fullAccess, ceiling: .supervised) == .supervised)
    #expect(clampRuntimeMode(.auto, ceiling: .autoEdits) == .autoEdits)
    #expect(clampRuntimeMode(.supervised, ceiling: .auto) == .supervised)
    #expect(clampRuntimeMode(.fullAccess, ceiling: .unspecified) == .fullAccess)
    #expect(!isRuntimeModeAllowed(.fullAccess, ceiling: .auto))
    #expect(isRuntimeModeAllowed(.autoEdits, ceiling: .auto))
}

@Test func threadEventProjectionReducerAndRollback() {
    var initEv = Hermesh_Thread_V2_ThreadEvent()
    initEv.threadID = "th_mac_1"
    initEv.seq = 1
    initEv.type = "thread.created"
    initEv.thread = .with {
        $0.threadID = "th_mac_1"
        $0.status = .running
        $0.lastSeq = 1
    }
    initEv.item = .with {
        $0.itemID = "it_1"
        $0.runID = "run_1"
        $0.seq = 1
        $0.kind = .assistantMessage
        $0.text = "Hello "
    }

    var state = applyThreadEvent(initEv, to: nil)
    #expect(state.thread.lastSeq == 1)

    // Duplicate seq is ignored; streaming delta appends onto targetItemID.
    state = applyThreadEvent(initEv, to: state)
    #expect(state.items.first?.text == "Hello ")

    var deltaEv = Hermesh_Thread_V2_ThreadEvent()
    deltaEv.threadID = "th_mac_1"
    deltaEv.seq = 2
    deltaEv.type = "item.delta"
    deltaEv.targetItemID = "it_1"
    deltaEv.textDelta = "mesh!"
    state = applyThreadEvent(deltaEv, to: state)
    #expect(state.items.first?.text == "Hello mesh!")
    #expect(state.items.first?.streaming == true)

    // Pending runtime request opens and resolves cleanly.
    var reqEv = Hermesh_Thread_V2_ThreadEvent()
    reqEv.threadID = "th_mac_1"
    reqEv.seq = 3
    reqEv.type = "request.opened"
    reqEv.thread = .with {
        $0.threadID = "th_mac_1"
        $0.status = .waitingRequest
        $0.pendingRequestID = "req_1"
    }
    reqEv.request = .with {
        $0.requestID = "req_1"
        $0.kind = .toolApproval
        $0.status = .pending
    }
    state = applyThreadEvent(reqEv, to: state)
    #expect(pendingRequest(of: state)?.requestID == "req_1")
    #expect(state.thread.status == .waitingRequest)

    var resEv = Hermesh_Thread_V2_ThreadEvent()
    resEv.threadID = "th_mac_1"
    resEv.seq = 4
    resEv.type = "request.resolved"
    resEv.thread = .with {
        $0.threadID = "th_mac_1"
        $0.status = .running
        $0.pendingRequestID = ""
    }
    resEv.request = .with {
        $0.requestID = "req_1"
        $0.kind = .toolApproval
        $0.status = .resolved
    }
    state = applyThreadEvent(resEv, to: state)
    #expect(pendingRequest(of: state) == nil)

    // Queued runs stay sorted by queueOrder and survive rollback of completed higher-ordinal runs.
    state.runs = [
        .with { $0.runID = "run_1"; $0.ordinal = 1; $0.status = .completed },
        .with { $0.runID = "run_2"; $0.ordinal = 2; $0.status = .completed },
        .with { $0.runID = "q_b"; $0.status = .queued; $0.queueOrder = 20 },
        .with { $0.runID = "q_a"; $0.status = .queued; $0.queueOrder = 10 },
    ]
    state.items.append(.with { $0.itemID = "it_2"; $0.runID = "run_2"; $0.seq = 5; $0.text = "later" })
    state.checkpoints = [
        .with { $0.checkpointID = "cp_1"; $0.runOrdinal = 1 },
        .with { $0.checkpointID = "cp_2"; $0.runOrdinal = 2 },
    ]
    #expect(queuedRuns(of: state).map(\.runID) == ["q_a", "q_b"])

    var rbEv = Hermesh_Thread_V2_ThreadEvent()
    rbEv.threadID = "th_mac_1"
    rbEv.seq = 6
    rbEv.type = "thread.rolled_back"
    rbEv.checkpoint = .with {
        $0.checkpointID = "cp_1"
        $0.runOrdinal = 1
    }
    state = applyThreadEvent(rbEv, to: state)
    #expect(state.checkpoints.map(\.checkpointID) == ["cp_1"])
    #expect(state.items.map(\.itemID) == ["it_1"])
    #expect(state.runs.first(where: { $0.runID == "run_2" })?.status == .rolledBack)
    #expect(queuedRuns(of: state).map(\.runID) == ["q_a", "q_b"])
}

@Test func polymorphicComposerActions() {
    #expect(composerPrimaryAction(hasThread: false, status: .idle, draftNotEmpty: false, machineOnline: true, supportsLiveSteer: true) == .start)
    #expect(composerPrimaryAction(hasThread: true, status: .running, draftNotEmpty: false, machineOnline: true, supportsLiveSteer: true) == .stop)
    #expect(composerPrimaryAction(hasThread: true, status: .running, draftNotEmpty: true, machineOnline: true, supportsLiveSteer: true) == .steer)
    #expect(composerPrimaryAction(hasThread: true, status: .running, draftNotEmpty: true, machineOnline: true, supportsLiveSteer: false) == .queue)
    #expect(composerPrimaryAction(hasThread: true, status: .idle, draftNotEmpty: true, machineOnline: false, supportsLiveSteer: true) == .queueOffline)
    #expect(composerPrimaryAction(hasThread: true, status: .idle, draftNotEmpty: true, machineOnline: true, supportsLiveSteer: true) == .send)
}
