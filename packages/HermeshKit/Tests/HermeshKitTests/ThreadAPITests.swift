import Foundation
import HermeshProto
import SwiftProtobuf
import Testing
@testable import HermeshKit

private final class MockThreadFFI: LocalFFITransport, @unchecked Sendable {
    var lastUnaryMethod = ""
    var lastStreamMethod = ""
    private var frames: [Data] = []

    init(streamEvents: [Hermesh_Thread_V2_ThreadEvent] = []) throws {
        frames = try streamEvents.map { try $0.serializedData() }
    }

    func unary(method: String, requestData: Data, timeoutSeconds: Int) throws -> Data {
        lastUnaryMethod = method
        let req = try Hermesh_Thread_V2_LaunchThreadRequest(serializedBytes: requestData)
        var res = Hermesh_Thread_V2_LaunchThreadResponse()
        res.thread.threadID = "th_ffi_1"
        res.thread.title = req.title
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

@Test func threadServiceFFIUnaryAndStreamRouting() async throws {
    var ev = Hermesh_Thread_V2_ThreadEvent()
    ev.threadID = "th_ffi_1"
    ev.seq = 1
    ev.thread.threadID = "th_ffi_1"

    let ffi = try MockThreadFFI(streamEvents: [ev])
    let api = LocalAPI(endpoint: .unix("/tmp/unused.sock"), ffi: ffi)

    var launchReq = Hermesh_Thread_V2_LaunchThreadRequest()
    launchReq.machineID = "mac-1"
    launchReq.title = "FFI v2 Thread"
    let launched = try await api.launchThread(launchReq)
    #expect(ffi.lastUnaryMethod == "/hermesh.thread.v2.ThreadService/LaunchThread")
    #expect(launched.thread.threadID == "th_ffi_1")
    #expect(launched.thread.title == "FFI v2 Thread")

    var received: [Hermesh_Thread_V2_ThreadEvent] = []
    for try await item in api.subscribeThread(threadID: "th_ffi_1") {
        received.append(item)
    }
    #expect(ffi.lastStreamMethod == "/hermesh.thread.v2.ThreadService/SubscribeThread")
    #expect(received.count == 1)
    #expect(received.first?.threadID == "th_ffi_1")
}
