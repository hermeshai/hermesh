import Foundation
import Testing
import HermeshProto
@testable import HermeshHer

@MainActor private func reviewCall(_ d: HerReviewDemo, _ method: String, _ params: JSON = [:]) async throws -> JSON {
    try await HerConnection.call(d, machineID: "demo-mac", method, params)
}

@MainActor private func approve(_ d: HerReviewDemo, decision: String = "approve") async throws {
    let id = try #require(d.session.live?.approvals.first?.callID)
    _ = try await reviewCall(d, "session/op", ["op": ["type": "approval", "call_id": .string(id), "decision": ["type": .string(decision)]]])
}

@Test @MainActor func reviewDemoRequiresApprovalAndShowsRealCards() async throws {
    let d = HerReviewDemo()
    #expect(herChat(d.session).contains { if case .question = $0.kind { true } else { false } })
    try d.send(HerReviewDemo.delegate, id: "first")
    #expect(d.session.live?.approvals.count == 1)
    #expect(d.codingState == nil)
    try await approve(d)
    #expect(d.codingState == .running)
    #expect(herChat(d.session).contains { if case .session(let s) = $0.kind { s.sessionID == HerReviewDemo.codingID } else { false } })
    #expect(herProgress(d.session).flatMap(\.tasks).count == 1)
    d.complete()
    #expect(d.codingState == .done)
    #expect(d.session.live == nil)
    guard case .agent(let launch)? = herProgress(d.session).first?.tasks.first else { Issue.record("missing progress record"); return }
    #expect(herAgentState(launch) == .done)
    #expect(!herSteps(try #require(launch.agent).view).isEmpty)
    d.stop()
}

@Test @MainActor func reviewDemoDenialAndStaleApprovalCannotStartWork() async throws {
    let d = HerReviewDemo()
    try d.send("a sample task", id: "first")
    let stale = try #require(d.session.live?.approvals.first?.callID)
    try await approve(d, decision: "deny")
    #expect(d.codingState == nil)
    #expect(d.session.live == nil)
    await #expect(throws: HerRPCError.self) {
        try await reviewCall(d, "session/op", ["op": ["type": "approval", "call_id": .string(stale), "decision": ["type": "approve"]]])
    }
    try d.send("again", id: "second")
    #expect(d.session.live?.approvals.count == 1)
    d.stop()
}

@Test @MainActor func reviewDemoFailureRetryAndInterruption() async throws {
    let d = HerReviewDemo()
    try d.send(HerReviewDemo.failure, id: "first")
    try await approve(d)
    d.complete()
    #expect(d.codingState == .failed)
    #expect(d.session.failure != nil)
    try d.send(HerReviewDemo.retry, id: "retry")
    try await approve(d)
    #expect(d.session.failure == nil)
    _ = try await reviewCall(d, "session/op", ["op": ["type": "interrupt"]])
    d.complete()
    #expect(d.codingState == .stopped, "a late completion cannot undo interruption")
    #expect(d.session.live == nil)
    d.stop()
}

@Test @MainActor func reviewDemoIsDisposableAndRefusesExternalOperations() async throws {
    let d = HerReviewDemo()
    try d.send("once", id: "same")
    let seq = d.session.lastSeq
    try d.send("once", id: "same")
    #expect(d.session.lastSeq == seq)
    for method in ["auth/connect", "models/connect", "files/read", "connectors/connect", "shell"] {
        await #expect(throws: HerRPCError.self) { try await reviewCall(d, method) }
    }
    d.stop()
    #expect(throws: HerRPCError.self) { try d.send("late", id: "late") }
    let fresh = HerReviewDemo()
    #expect(fresh.session.lastSeq == 0)
    #expect(fresh.codingState == nil)
    #expect(fresh.session.live == nil)
    fresh.stop()
}

@Test @MainActor func reviewDemoReopensByReplayingTheSameProtocolEvents() async throws {
    let d = HerReviewDemo()
    try d.send(HerReviewDemo.delegate, id: "first")
    try await approve(d)
    d.complete()
    let c = HerConnection(local: d, machineID: "demo-mac")
    var restored: HerSession?
    for try await event in c.open(setup: [("bots/open", ["bot_id": "her"])]) {
        switch event {
        case .opened(let replies):
            if case .success(let r)? = replies.first { restored = HerSession(snapshot: r["snapshot"]) }
        case .notification("session/event", let params): restored?.apply(params["event"])
        default: break
        }
        if restored?.lastSeq == d.session.lastSeq { break }
    }
    #expect(restored == d.session)
    d.stop()
}
