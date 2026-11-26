import Foundation
import HermeshKit
import HermeshProto
import SwiftProtobuf

/// The same messages drive both the real core and the in-memory demo.
final class AppAPI {
    let live: LocalAPI?
    let demo: DemoService?

    init(live: LocalAPI) { self.live = live; demo = nil }
    init(demo: DemoService) { self.demo = demo; live = nil }

    func call<Req: SwiftProtobuf.Message, Res: SwiftProtobuf.Message>(_ method: String, _ req: Req, as _: Res.Type = Res.self, timeout: Int = 10) async throws -> Res {
        if let live { return try await live.call(method, req, as: Res.self, timeout: timeout) }
        guard let demo else { throw CancellationError() }
        var o = JSONDecodingOptions()
        o.ignoreUnknownFields = true
        return try Res(jsonUTF8Data: demo.call(method, req.jsonUTF8Data()), options: o)
    }

    func stream<Req: SwiftProtobuf.Message, Res: SwiftProtobuf.Message>(_ method: String, _ req: Req, as _: Res.Type = Res.self) -> AsyncThrowingStream<Res, Error> {
        if let live { return live.stream(method, req, as: Res.self) }
        var o = JSONDecodingOptions()
        o.ignoreUnknownFields = true
        let options = o
        return AsyncThrowingStream { continuation in
            guard let demo else { continuation.finish(); return }
            let task = Task {
                do {
                    for try await data in try demo.stream(method, req.jsonUTF8Data()) {
                        continuation.yield(try Res(jsonUTF8Data: data, options: options))
                    }
                    continuation.finish()
                } catch { continuation.finish(throwing: error) }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }
}

// LocalService + SessionService messages by shorter names, and calls typed.

typealias StatusResponse = Hermesh_Local_V1_GetStatusResponse
typealias Machine = Hermesh_Local_V1_Machine
typealias Client = Hermesh_Local_V1_Client
typealias Device = Hermesh_Local_V1_Device
typealias EraseItem = Hermesh_Local_V1_EraseItem
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

/// The active UI locale normalized to a BCP 47 tag ("zh-CN", "zh-TW", "en", ...).
enum AppLocale {
    static var current: String {
        let raw = (Bundle.main.preferredLocalizations.first ?? Locale.preferredLanguages.first ?? "en")
            .replacingOccurrences(of: "_", with: "-")
        let lower = raw.lowercased()
        if lower.hasPrefix("zh") {
            if lower.contains("hant") || lower.contains("-tw") || lower.contains("-hk") || lower.contains("-mo") {
                return "zh-TW"
            }
            return "zh-CN"
        }
        if lower.hasPrefix("en") { return "en" }
        return String(lower.split(separator: "-").first ?? "en")
    }
}

private let sessionSvc = "/hermesh.session.v1.SessionService"

extension AppAPI {
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

    // MARK: Unified SessionService

    func listProviders(machineID: String, projectID: String = "", refresh: Bool = false) async throws -> Hermesh_Session_V1_ListProvidersResponse {
        var r = Hermesh_Session_V1_ListProvidersRequest()
        r.machineID = machineID
        r.projectID = projectID
        r.refresh = refresh
        return try await call("\(sessionSvc)/ListProviders", r, timeout: 15)
    }

    func startSession(_ r: Hermesh_Session_V1_StartSessionRequest) async throws -> Hermesh_Session_V1_StartSessionResponse {
        var req = r
        if req.locale.isEmpty { req.locale = AppLocale.current }
        return try await call("\(sessionSvc)/StartSession", req, timeout: 30)
    }

    func getSession(_ sessionID: String, machineID: String = "", cachedOnly: Bool = false) async throws -> Hermesh_Session_V1_GetSessionResponse {
        var r = Hermesh_Session_V1_GetSessionRequest()
        r.machineID = machineID
        r.sessionID = sessionID
        r.cachedOnly = cachedOnly
        return try await call("\(sessionSvc)/GetSession", r, timeout: 15)
    }

    func subscribeSession(_ sessionID: String, machineID: String = "", afterSeq: UInt64 = 0) -> AsyncThrowingStream<Hermesh_Session_V1_SessionEvent, Error> {
        var r = Hermesh_Session_V1_SubscribeSessionRequest()
        r.machineID = machineID
        r.sessionID = sessionID
        r.afterSeq = afterSeq
        return stream("\(sessionSvc)/SubscribeSession", r)
    }

    func runCommand(_ sessionID: String, command: String, machineID: String = "") -> AsyncThrowingStream<Hermesh_Session_V1_RunCommandEvent, Error> {
        var r = Hermesh_Session_V1_RunCommandRequest()
        r.machineID = machineID
        r.sessionID = sessionID
        r.command = command
        return stream("\(sessionSvc)/RunCommand", r)
    }

    func dispatchMessage(_ r: Hermesh_Session_V1_DispatchMessageRequest) async throws -> Hermesh_Session_V1_DispatchMessageResponse {
        var req = r
        if req.locale.isEmpty { req.locale = AppLocale.current }
        return try await call("\(sessionSvc)/DispatchMessage", req, timeout: 20)
    }

    func uploadBlob(machineID: String = "", name: String, mimeType: String, data: Data) async throws -> Hermesh_Session_V1_UploadBlobResponse {
        var r = Hermesh_Session_V1_UploadBlobRequest()
        r.machineID = machineID
        r.name = name
        r.mimeType = mimeType
        r.data = data
        return try await call("\(sessionSvc)/UploadBlob", r, timeout: 60)
    }

    // A session's files (docs/session-files.md §6): read when opened, as a turn left them or as they are now.

    func resolveFiles(_ sessionID: String, run: String, paths: [String]) async throws -> Hermesh_Session_V1_ResolveFilesResponse {
        var r = Hermesh_Session_V1_ResolveFilesRequest()
        r.sessionID = sessionID
        r.runID = run
        r.paths = paths
        return try await call("\(sessionSvc)/ResolveFiles", r, timeout: 20)
    }

    func listFiles(_ sessionID: String, run: String, dir: String) async throws -> Hermesh_Session_V1_ListFilesResponse {
        var r = Hermesh_Session_V1_ListFilesRequest()
        r.sessionID = sessionID
        r.runID = run
        r.dir = dir
        return try await call("\(sessionSvc)/ListFiles", r, timeout: 20)
    }

    func readFile(_ sessionID: String, run: String, path: String) async throws -> Hermesh_Session_V1_ReadFileResponse {
        var r = Hermesh_Session_V1_ReadFileRequest()
        r.sessionID = sessionID
        r.runID = run
        r.path = path
        return try await call("\(sessionSvc)/ReadFile", r, timeout: 30)
    }

    func readFileBytes(_ sessionID: String, run: String, path: String, offset: UInt64, length: UInt32 = 0) async throws -> Hermesh_Session_V1_ReadFileBytesResponse {
        var r = Hermesh_Session_V1_ReadFileBytesRequest()
        r.sessionID = sessionID
        r.runID = run
        r.path = path
        r.offset = offset
        r.length = length
        return try await call("\(sessionSvc)/ReadFileBytes", r, timeout: 30)
    }

    /// A whole file, a part at a time.
    func readWholeFile(_ sessionID: String, run: String, path: String) async throws -> (data: Data, mime: String) {
        var data = Data()
        var mime = ""
        var size: UInt64 = .max
        while UInt64(data.count) < size {
            let r = try await readFileBytes(sessionID, run: run, path: path, offset: UInt64(data.count))
            size = r.size
            if mime.isEmpty { mime = r.mimeType }
            if r.data.isEmpty { break }
            data.append(r.data)
        }
        return (data, mime)
    }

    func downloadBlob(_ sessionID: String, sha256: String, machineID: String = "") async throws -> Data {
        var r = Hermesh_Session_V1_DownloadBlobRequest()
        r.machineID = machineID
        r.sessionID = sessionID
        r.blobSha256 = sha256
        let res: Hermesh_Session_V1_DownloadBlobResponse = try await call("\(sessionSvc)/DownloadBlob", r, timeout: 30)
        return res.data
    }

    func respondRuntimeRequest(_ r: Hermesh_Session_V1_RespondRuntimeRequestRequest) async throws -> Hermesh_Session_V1_RespondRuntimeRequestResponse {
        try await call("\(sessionSvc)/RespondRuntimeRequest", r, timeout: 15)
    }

    func answer(_ sessionID: String, question: String, answers: [Answer], decline: Bool) async throws -> Hermesh_Session_V1_Session {
        var r = Hermesh_Session_V1_RespondRuntimeRequestRequest()
        r.commandID = UUID().uuidString
        r.sessionID = sessionID
        r.requestID = question
        r.decline = decline
        r.decision = decline ? .decline : .acceptOnce
        if !decline {
            for a in answers {
                r.answers[a.itemID] = .with {
                    $0.selectedOptions = a.selected
                    $0.customText = a.text
                }
            }
        }
        return try await respondRuntimeRequest(r).session
    }

    func interrupt(_ sessionID: String, runID: String = "", force: Bool = false) async throws -> Hermesh_Session_V1_Session {
        var r = Hermesh_Session_V1_InterruptSessionRequest()
        r.commandID = UUID().uuidString
        r.sessionID = sessionID
        r.runID = runID
        r.force = force
        let res: Hermesh_Session_V1_InterruptSessionResponse = try await call("\(sessionSvc)/InterruptSession", r, timeout: 15)
        return res.session
    }

    func stopBackgroundTask(_ sessionID: String, taskID: String = "") async throws -> Hermesh_Session_V1_StopBackgroundTaskResponse {
        var r = Hermesh_Session_V1_StopBackgroundTaskRequest()
        r.sessionID = sessionID
        r.taskID = taskID
        return try await call("\(sessionSvc)/StopBackgroundTask", r, timeout: 15)
    }

    func manageQueue(_ r: Hermesh_Session_V1_ManageQueueRequest) async throws -> Hermesh_Session_V1_ManageQueueResponse {
        try await call("\(sessionSvc)/ManageQueue", r, timeout: 15)
    }

    func updateSession(_ r: Hermesh_Session_V1_UpdateSessionRequest) async throws -> Hermesh_Session_V1_UpdateSessionResponse {
        try await call("\(sessionSvc)/UpdateSession", r, timeout: 15)
    }

    func archive(_ sessionID: String, _ archived: Bool) async throws -> Hermesh_Session_V1_UpdateSessionResponse {
        var r = Hermesh_Session_V1_UpdateSessionRequest()
        r.commandID = UUID().uuidString
        r.sessionID = sessionID
        r.hasArchived_p = true
        r.archived = archived
        return try await call("\(sessionSvc)/UpdateSession", r, timeout: 60)
    }

    func delete(_ sessionID: String) async throws {
        var r = Hermesh_Session_V1_DeleteSessionRequest()
        r.commandID = UUID().uuidString
        r.sessionID = sessionID
        let _: Hermesh_Session_V1_DeleteSessionResponse = try await call("\(sessionSvc)/DeleteSession", r, timeout: 15)
    }

    func rename(_ sessionID: String, to title: String) async throws -> Hermesh_Session_V1_Session {
        var r = Hermesh_Session_V1_UpdateSessionRequest()
        r.commandID = UUID().uuidString
        r.sessionID = sessionID
        r.title = title
        return try await updateSession(r).session
    }

    func diff(_ sessionID: String) async throws -> String {
        var r = Hermesh_Session_V1_GetDiffRequest()
        r.sessionID = sessionID
        let res: Hermesh_Session_V1_GetDiffResponse = try await call("\(sessionSvc)/GetDiff", r, timeout: 30)
        return res.diff
    }

    // MARK: LocalService

    /// Work counted over a range, from what the core holds (docs/work-stats.md §7).
    func queryWork(_ r: Hermesh_Local_V1_QueryWorkRequest) async throws -> Hermesh_Local_V1_QueryWorkResponse {
        try await call("QueryWork", r, timeout: 30)
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

    /// eraseHer: Her's data there is erased first (docs/her-server.md §5.6).
    func removeMachine(_ id: String, eraseHer: Bool) async throws {
        var r = Hermesh_Local_V1_RemoveMachineRequest()
        r.machineID = id
        r.eraseHer = eraseHer
        let _: Hermesh_Local_V1_RemoveMachineResponse = try await call("RemoveMachine", r, timeout: signingTimeout)
    }

    func checkAgents() async throws {
        let _: Hermesh_Local_V1_CheckAgentsResponse = try await call("CheckAgents", Hermesh_Local_V1_CheckAgentsRequest(), timeout: 30)
    }

    /// Check Again on a machine: it looks at its coding agents now.
    func checkAgents(machine: String) async throws {
        var r = Hermesh_Local_V1_CheckAgentsRequest()
        r.machineID = machine
        let _: Hermesh_Local_V1_CheckAgentsResponse = try await call("CheckAgents", r, timeout: 60)
    }

    /// The git repositories under a machine's home folder.
    func repos(machine: String) async throws -> [Hermesh_Machine_V1_Repo] {
        var r = Hermesh_Local_V1_ListMachineReposRequest()
        r.machineID = machine
        let res: Hermesh_Local_V1_ListMachineReposResponse = try await call("ListMachineRepos", r, timeout: 30)
        return res.repos
    }

    /// The folders in one of a machine's folders ("~/code", or a full path).
    func folders(machine: String, path: String, hidden: Bool = false) async throws -> Hermesh_Machine_V1_ListFoldersResponse {
        var r = Hermesh_Local_V1_ListMachineFoldersRequest()
        r.machineID = machine
        r.path = path
        r.hidden = hidden
        return try await call("ListMachineFolders", r, timeout: 60)
    }

    /// A new empty folder on a machine, under its home folder (CreateFolder); a git repository with one empty commit when gitInit.
    func createFolder(machine: String, parent: String, name: String, gitInit: Bool) async throws {
        var r = Hermesh_Local_V1_CreateMachineFolderRequest()
        r.machineID = machine
        r.parent = parent
        r.name = name
        r.gitInit = gitInit
        let _: Hermesh_Local_V1_CreateMachineFolderResponse = try await call("CreateMachineFolder", r, timeout: signingTimeout)
    }

    /// Test: one fixed turn of a coding agent on a machine.
    func testAgent(machine: String, agent: String) async throws -> Hermesh_Machine_V1_TestAgentResponse {
        var r = Hermesh_Local_V1_TestMachineAgentRequest()
        r.machineID = machine
        r.agent = agent
        return try await call("TestMachineAgent", r, timeout: 120)
    }

    /// A machine's storage: its tasks' worktrees (docs/settings-environments.md §5.7); cached, the last scan.
    func workspaces(machine: String, cached: Bool) async throws -> Hermesh_Machine_V1_ListWorkspacesResponse {
        var r = Hermesh_Local_V1_ListWorkspacesRequest()
        r.machineID = machine
        r.cached = cached
        return try await call("ListWorkspaces", r, timeout: 120)
    }

    /// Releases tasks' worktrees on a machine; each one kept says why.
    func releaseWorkspaces(machine: String, sessions: [String]) async throws -> Hermesh_Machine_V1_ReleaseWorkspacesResponse {
        var r = Hermesh_Local_V1_ReleaseWorkspacesRequest()
        r.machineID = machine
        r.sessionIds = sessions
        return try await call("ReleaseWorkspaces", r, timeout: 120)
    }

    func setWorkspacePolicy(machine: String, autoRelease: Bool) async throws {
        var r = Hermesh_Local_V1_SetWorkspacePolicyRequest()
        r.machineID = machine
        r.autoRelease = autoRelease
        let _: Hermesh_Local_V1_SetWorkspacePolicyResponse = try await call("SetWorkspacePolicy", r)
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

    /// eraseHer: Her's data on its own machine is erased first, with the same approval (docs/her-server.md §5.6).
    func removeDevice(_ keyID: String, eraseHer: Bool = false) async throws {
        var r = Hermesh_Local_V1_RemoveDeviceRequest()
        r.keyID = keyID
        r.eraseHer = eraseHer
        let _: Hermesh_Local_V1_RemoveDeviceResponse = try await call("RemoveDevice", r, timeout: signingTimeout)
    }

    func leave() async throws {
        let _: Hermesh_Local_V1_LeaveResponse = try await call("Leave", Hermesh_Local_V1_LeaveRequest(), timeout: 30)
    }

    func eraseEverything() async throws -> String {
        let r: Hermesh_Local_V1_EraseEverythingResponse = try await call("EraseEverything", Hermesh_Local_V1_EraseEverythingRequest(), timeout: signingTimeout)
        return r.eraseID
    }

    func eraseStatus(_ eraseID: String) async throws -> Hermesh_Local_V1_EraseStatusResponse {
        var r = Hermesh_Local_V1_EraseStatusRequest()
        r.eraseID = eraseID
        return try await call("EraseStatus", r, timeout: 30)
    }

    func finishErase() async throws {
        let _: Hermesh_Local_V1_FinishEraseResponse = try await call("FinishErase", Hermesh_Local_V1_FinishEraseRequest(), timeout: 90)
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
