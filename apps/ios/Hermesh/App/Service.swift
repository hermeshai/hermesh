import Foundation
import HermeshKit
import HermeshProto

// LocalService's messages by shorter names, and its calls typed.

typealias StatusResponse = Hermesh_Local_V1_GetStatusResponse
typealias Machine = Hermesh_Local_V1_Machine
typealias Client = Hermesh_Local_V1_Client
typealias Device = Hermesh_Local_V1_Device
typealias Project = Hermesh_Local_V1_Project
typealias PairingDetails = Hermesh_Local_V1_PairingDetails
typealias GrantSpec = Hermesh_Local_V1_GrantSpec
typealias Unreachable = Hermesh_Local_V1_Unreachable
typealias MachineAck = Hermesh_Local_V1_MachineAck
typealias AgentStatus = Hermesh_Machine_V1_AgentStatus
typealias Answer = Hermesh_Session_V1_Answer
typealias Wake = Hermesh_Local_V1_Wake
typealias WakeCheck = Hermesh_Local_V1_WakeCheck
typealias WakeRing = Hermesh_Local_V1_WakeRing

/// How long a call that waits for Face ID may take.
let signingTimeout = 180

extension LocalAPI {
    func status() async throws -> StatusResponse { try await call("GetStatus", Hermesh_Local_V1_GetStatusRequest()) }

    func listSessions(limit: UInt32 = 50, before: Hermesh_Session_V1_SessionCursor? = nil) async throws -> Hermesh_Local_V1_ListSessionsResponse {
        var r = Hermesh_Local_V1_ListSessionsRequest()
        r.limit = limit
        r.view = .basic
        if let before { r.before = before }
        return try await call("ListSessions", r)
    }

    /// The owner's world, then each change (docs/state-sync.md): nothing is polled.
    func watch() -> AsyncThrowingStream<Hermesh_Local_V1_WatchResponse, Error> {
        stream("Watch", Hermesh_Local_V1_WatchRequest())
    }

    func events(_ sessionID: String, after: UInt64 = 0) -> AsyncThrowingStream<Hermesh_Session_V1_SessionEvent, Error> {
        var r = Hermesh_Local_V1_StreamSessionEventsRequest()
        r.sessionID = sessionID
        r.afterSeq = after
        r.follow = true
        r.partial = true // the reply as the agent writes it (docs/reply-streaming.md)
        r.view = .basic // a long tool output as its first lines, which is what the conversation shows
        return stream("StreamSessionEvents", r)
    }

    func answer(_ sessionID: String, question: String, answers: [Answer], decline: Bool) async throws -> Hermesh_Session_V1_Session {
        var r = Hermesh_Local_V1_AnswerQuestionRequest()
        r.sessionID = sessionID
        r.questionID = question
        r.answers = answers
        r.decline = decline
        let res: Hermesh_Local_V1_AnswerQuestionResponse = try await call("AnswerQuestion", r)
        return res.session
    }

    func interrupt(_ sessionID: String) async throws -> Hermesh_Session_V1_Session {
        var r = Hermesh_Local_V1_InterruptSessionRequest()
        r.sessionID = sessionID
        let res: Hermesh_Local_V1_SessionResponse = try await call("InterruptSession", r)
        return res.session
    }

    func archive(_ sessionID: String, _ archived: Bool) async throws -> Hermesh_Session_V1_Session {
        let res: Hermesh_Local_V1_SessionResponse
        if archived {
            var r = Hermesh_Local_V1_ArchiveSessionRequest()
            r.sessionID = sessionID
            res = try await call("ArchiveSession", r)
        } else {
            var r = Hermesh_Local_V1_UnarchiveSessionRequest()
            r.sessionID = sessionID
            res = try await call("UnarchiveSession", r)
        }
        return res.session
    }

    func delete(_ sessionID: String) async throws {
        var r = Hermesh_Local_V1_DeleteSessionRequest()
        r.sessionID = sessionID
        let _: Hermesh_Local_V1_DeleteSessionResponse = try await call("DeleteSession", r)
    }

    func rename(_ sessionID: String, to title: String) async throws -> Hermesh_Session_V1_Session {
        var r = Hermesh_Local_V1_RenameSessionRequest()
        r.sessionID = sessionID
        r.title = title
        let res: Hermesh_Local_V1_SessionResponse = try await call("RenameSession", r)
        return res.session
    }

    func diff(_ sessionID: String) async throws -> String {
        var r = Hermesh_Local_V1_GetDiffRequest()
        r.sessionID = sessionID
        let res: Hermesh_Session_V1_GetDiffResponse = try await call("GetDiff", r, timeout: 30)
        return res.diff
    }

    func usage(since: String) async throws -> Hermesh_Local_V1_GetUsageResponse {
        var r = Hermesh_Local_V1_GetUsageRequest()
        r.since = since
        r.timeZone = TimeZone.current.identifier
        return try await call("GetUsage", r, timeout: 30)
    }

    func activity(date: String) async throws -> Hermesh_Local_V1_GetActivityResponse {
        var r = Hermesh_Session_V1_GetActivityRequest()
        r.date = date
        r.timeZone = TimeZone.current.identifier
        return try await call("GetActivity", r, timeout: 30)
    }

    func machines() async throws -> [Machine] {
        let r: Hermesh_Local_V1_ListMachinesResponse = try await call("ListMachines", Hermesh_Local_V1_ListMachinesRequest())
        return r.machines
    }

    func clients() async throws -> [Client] {
        let r: Hermesh_Local_V1_ListClientsResponse = try await call("ListClients", Hermesh_Local_V1_ListClientsRequest())
        return r.clients
    }

    func devices() async throws -> [Device] {
        let r: Hermesh_Local_V1_ListDevicesResponse = try await call("ListDevices", Hermesh_Local_V1_ListDevicesRequest())
        return r.devices
    }

    func projects(machine: String = "") async throws -> Hermesh_Local_V1_ListProjectsResponse {
        var r = Hermesh_Local_V1_ListProjectsRequest()
        r.machineID = machine
        return try await call("ListProjects", r, timeout: 20)
    }

    func pairings() async throws -> [PairingDetails] {
        let r: Hermesh_Local_V1_ListPairingRequestsResponse = try await call("ListPairingRequests", Hermesh_Local_V1_ListPairingRequestsRequest())
        return r.pairings
    }

    func lookupPairing(_ code: String) async throws -> PairingDetails {
        var r = Hermesh_Local_V1_LookupPairingRequest()
        r.code = code
        let res: Hermesh_Local_V1_LookupPairingResponse = try await call("LookupPairing", r)
        return res.details
    }

    func approveClient(_ code: String, spec: GrantSpec) async throws {
        var r = Hermesh_Local_V1_ApproveClientPairingRequest()
        r.code = code
        r.spec = spec
        let _: Hermesh_Local_V1_ApproveClientPairingResponse = try await call("ApproveClientPairing", r, timeout: signingTimeout)
    }

    func approveMachine(_ code: String, name: String) async throws -> Hermesh_Local_V1_ApproveMachinePairingResponse {
        var r = Hermesh_Local_V1_ApproveMachinePairingRequest()
        r.code = code
        r.machineName = name
        return try await call("ApproveMachinePairing", r, timeout: signingTimeout)
    }

    func approveDevice(_ code: String) async throws -> Hermesh_Local_V1_ApproveDevicePairingResponse {
        var r = Hermesh_Local_V1_ApproveDevicePairingRequest()
        r.code = code
        return try await call("ApproveDevicePairing", r, timeout: signingTimeout)
    }

    func updateClient(_ grantID: String, spec: GrantSpec) async throws -> Hermesh_Local_V1_UpdateClientResponse {
        var r = Hermesh_Local_V1_UpdateClientRequest()
        r.grantID = grantID
        r.spec = spec
        return try await call("UpdateClient", r, timeout: signingTimeout)
    }

    func renameClient(_ grantID: String, to label: String) async throws {
        var r = Hermesh_Local_V1_RenameClientRequest()
        r.grantID = grantID
        r.label = label
        let _: Hermesh_Local_V1_RenameClientResponse = try await call("RenameClient", r, timeout: signingTimeout)
    }

    func revokeClient(_ grantID: String) async throws -> [MachineAck] {
        var r = Hermesh_Local_V1_RevokeClientRequest()
        r.grantID = grantID
        let res: Hermesh_Local_V1_RevokeClientResponse = try await call("RevokeClient", r, timeout: 60)
        return res.acks
    }

    /// An agent's doorbells (docs/agent-names.md §7); nil where the relay keeps none.
    func listWakes(_ clientID: String) async throws -> [Wake]? {
        var r = Hermesh_Local_V1_ListWakesRequest()
        r.clientID = clientID
        do {
            let res: Hermesh_Local_V1_ListWakesResponse = try await call("ListWakes", r)
            return res.wakes
        } catch let e as APIError where e.code == "unimplemented" {
            return nil
        }
    }

    func setWake(_ clientID: String, name: String, url: String, authorization: String, keep: Bool) async throws -> Hermesh_Local_V1_SetWakeResponse {
        var r = Hermesh_Local_V1_SetWakeRequest()
        r.clientID = clientID
        r.name = name
        r.url = url
        r.authorization = authorization
        r.keep = keep
        return try await call("SetWake", r, timeout: 30)
    }

    func checkWake(_ clientID: String, name: String) async throws -> Hermesh_Local_V1_CheckWakeResponse {
        var r = Hermesh_Local_V1_CheckWakeRequest()
        r.clientID = clientID
        r.name = name
        return try await call("CheckWake", r, timeout: 30)
    }

    func clearWake(_ clientID: String, name: String) async throws {
        var r = Hermesh_Local_V1_ClearWakeRequest()
        r.clientID = clientID
        r.name = name
        let _: Hermesh_Local_V1_ClearWakeResponse = try await call("ClearWake", r)
    }

    func renameMachine(_ id: String, to name: String) async throws {
        var r = Hermesh_Local_V1_RenameMachineRequest()
        r.machineID = id
        r.machineName = name
        let _: Hermesh_Local_V1_RenameMachineResponse = try await call("RenameMachine", r, timeout: signingTimeout)
    }

    func setPaused(_ id: String, _ paused: Bool) async throws {
        var r = Hermesh_Local_V1_SetMachinePausedRequest()
        r.machineID = id
        r.paused = paused
        let _: Hermesh_Local_V1_SetMachinePausedResponse = try await call("SetMachinePaused", r, timeout: signingTimeout)
    }

    func setAgentDefaults(_ id: String, _ d: Hermesh_Identity_V1_AgentDefaults) async throws {
        var r = Hermesh_Local_V1_SetAgentDefaultsRequest()
        r.machineID = id
        r.defaults = d
        let _: Hermesh_Local_V1_SetAgentDefaultsResponse = try await call("SetAgentDefaults", r)
    }

    func removeMachine(_ id: String) async throws {
        var r = Hermesh_Local_V1_RemoveMachineRequest()
        r.machineID = id
        let _: Hermesh_Local_V1_RemoveMachineResponse = try await call("RemoveMachine", r, timeout: signingTimeout)
    }

    func checkAgents() async throws {
        let _: Hermesh_Local_V1_CheckAgentsResponse = try await call("CheckAgents", Hermesh_Local_V1_CheckAgentsRequest(), timeout: 30)
    }

    func addProject(_ r: Hermesh_Local_V1_AddProjectRequest) async throws {
        let _: Hermesh_Local_V1_AddProjectResponse = try await call("AddProject", r, timeout: signingTimeout)
    }

    func removeProject(machine: String, project: String) async throws {
        var r = Hermesh_Local_V1_RemoveProjectRequest()
        r.machineID = machine
        r.projectID = project
        let _: Hermesh_Local_V1_RemoveProjectResponse = try await call("RemoveProject", r, timeout: signingTimeout)
    }

    func removeDevice(_ keyID: String) async throws {
        var r = Hermesh_Local_V1_RemoveDeviceRequest()
        r.keyID = keyID
        let _: Hermesh_Local_V1_RemoveDeviceResponse = try await call("RemoveDevice", r, timeout: signingTimeout)
    }

    func leave() async throws {
        let _: Hermesh_Local_V1_LeaveResponse = try await call("Leave", Hermesh_Local_V1_LeaveRequest(), timeout: 30)
    }

    func setup(_ r: Hermesh_Local_V1_SetupRequest) async throws -> Hermesh_Local_V1_SetupResponse {
        try await call("Setup", r, timeout: signingTimeout)
    }

    func requestJoin(_ r: Hermesh_Local_V1_RequestJoinRequest) async throws -> Hermesh_Local_V1_RequestJoinResponse {
        try await call("RequestJoin", r, timeout: signingTimeout)
    }

    func waitJoin(_ code: String) async throws -> Hermesh_Local_V1_WaitJoinResponse {
        var r = Hermesh_Local_V1_WaitJoinRequest()
        r.code = code
        return try await call("WaitJoin", r, timeout: 90)
    }

    func confirmJoin(_ code: String, accept: Bool, match: String = "") async throws {
        var r = Hermesh_Local_V1_ConfirmJoinRequest()
        r.code = code
        r.accept = accept
        r.matchCode = match
        let _: Hermesh_Local_V1_ConfirmJoinResponse = try await call("ConfirmJoin", r, timeout: 60)
    }
}
