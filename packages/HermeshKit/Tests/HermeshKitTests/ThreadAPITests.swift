import Foundation
import HermeshProto
import SwiftProtobuf
import Testing
@testable import HermeshKit

private final class MockSessionFFI: LocalFFITransport, @unchecked Sendable {
    var lastUnaryMethod = ""
    var lastStreamMethod = ""
    private var frames: [Data] = []

    init(streamEvents: [Hermesh_Session_V1_SessionEvent] = []) throws {
        frames = try streamEvents.map { try $0.serializedData() }
    }

    func unary(method: String, requestData: Data, timeoutSeconds: Int) throws -> Data {
        lastUnaryMethod = method
        let req = try Hermesh_Session_V1_StartSessionRequest(serializedBytes: requestData)
        var res = Hermesh_Session_V1_StartSessionResponse()
        res.session.sessionID = "s_ffi_1"
        res.session.title = req.title
        return try res.serializedData()
    }

    func openStream(method: String, requestData: Data) -> Int64 {
        lastStreamMethod = method
        return 42
    }

    func nextFrame(streamID: Int64) throws -> Data? {
        guard !frames.isEmpty else { return nil }
        return frames.removeFirst()
    }

    func closeStream(streamID: Int64) {}
}

@Test func sessionServiceFFIUnaryAndStreamRouting() async throws {
    var ev = Hermesh_Session_V1_SessionEvent()
    ev.sessionID = "s_ffi_1"
    ev.seq = 1
    ev.session.sessionID = "s_ffi_1"

    let ffi = try MockSessionFFI(streamEvents: [ev])
    let api = LocalAPI(endpoint: .unix("/tmp/unused.sock"), ffi: ffi)

    var startReq = Hermesh_Session_V1_StartSessionRequest()
    startReq.machineID = "mac-1"
    startReq.title = "FFI Unified Session"
    let started = try await api.startSession(startReq)
    #expect(ffi.lastUnaryMethod == "/hermesh.session.v1.SessionService/StartSession")
    #expect(started.session.sessionID == "s_ffi_1")
    #expect(started.session.title == "FFI Unified Session")

    var received: [Hermesh_Session_V1_SessionEvent] = []
    for try await item in api.subscribeSession(sessionID: "s_ffi_1") {
        received.append(item)
    }
    #expect(ffi.lastStreamMethod == "/hermesh.session.v1.SessionService/SubscribeSession")
    #expect(received.count == 1)
    #expect(received.first?.sessionID == "s_ffi_1")
}
