import Foundation
import HermeshHer
import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftProtobuf

/// A disposable world. No sockets, credentials, files or real agent processes.
/// It speaks the same messages as the core so the normal screens stay in use.
final class DemoService {
    let herReview = HerReviewDemo()
    private var world = Hermesh_Local_V1_WatchResponse()
    private var events: [String: [SessionEvent]] = [:]
    private var watchers: [UUID: AsyncThrowingStream<Data, Error>.Continuation] = [:]
    private var followers: [UUID: (String, AsyncThrowingStream<Data, Error>.Continuation)] = [:]
    private var tasks: [String: Task<Void, Never>] = [:]
    #if DEBUG
    /// What was written to the demo's Her (DemoHer.swift).
    var herWaiting: [Hermesh_Local_V1_HerWaiting] = []
    #endif

    init() {
        seed()
        herReview.codingChanged = { [weak self] in self?.updateHerTask() }
    }

    func stop() {
        herReview.stop()
        tasks.values.forEach { $0.cancel() }
        tasks.removeAll()
        watchers.values.forEach { $0.finish() }
        followers.values.forEach { $0.1.finish() }
        watchers.removeAll()
        followers.removeAll()
    }

    private func seed() {
        let now = Google_Protobuf_Timestamp(date: .now)
        world.reset = true
        world.status = .with {
            $0.initialized = true; $0.ownerID = "demo-owner"; $0.hosted = true
            $0.account = String(localized: "Demo Workspace"); $0.relayConnected = true
            $0.version = Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? ""
            $0.deviceKeyID = "demo-iphone"
        }
        world.machines = [machine("demo-mac", "Mac mini", "darwin/arm64"), machine("demo-linux", "Linux Studio", "linux/amd64")]
        world.machines[0].her = .with { $0.enabled = true; $0.running = true; $0.modelReady = true }
        world.devices = [.with { $0.keyID = "demo-iphone"; $0.label = "iPhone"; $0.platform = "ios"; $0.thisDevice = true }]
        world.projects = world.machines.map { m in .with {
            $0.machineID = m.machineID; $0.projectID = "hello-world"; $0.path = "/Demo/Projects/hello-world"
            $0.defaultBaseRef = "main"; $0.workspace = .worktree; $0.agents = ["claude", "codex"]
            $0.description_p = String(localized: "A sample project for exploring Hermesh")
        } }
        world.clients = [.with {
            $0.clientID = "demo-client"; $0.grantID = "demo-grant"; $0.label = "ChatGPT"; $0.online = true
            $0.grantVersion = 1; $0.expiresAt = .init(date: .now.addingTimeInterval(30 * 86400))
            $0.spec = .with { $0.label = "ChatGPT"; $0.machineIds = ["*"]; $0.projectIds = ["*"]; $0.agents = ["*"]; $0.ttlDays = 30 }
        }]
        let question = Question.with {
            $0.questionID = "demo-question"; $0.turn = 1; $0.askedAt = now
            $0.items = [.with {
                $0.id = "greeting"; $0.header = String(localized: "Greeting")
                $0.text = String(localized: "Which greeting should the sample app show?")
                $0.options = [.with { $0.label = "Hello, world!" }, .with { $0.label = "Welcome aboard!" }]
                $0.allowOther = true
            }]
        }
        sampleQuestion = question
        world.sessions = [session("demo-question-session", title: String(localized: "Choose a greeting"), machine: "demo-mac", status: .waitingInput),
                          session("demo-finished-session", title: String(localized: "Add a welcome page"), machine: "demo-linux", status: .idle)]
        world.sessions[0].progress.pendingQuestion = question
        append("demo-question-session", .with { $0.turnStarted = .with { $0.turn = 1; $0.asker = "demo-client" } })
        append("demo-question-session", .with { $0.userMessage.text = String(localized: "Build a welcome page for our sample project. Ask me which greeting to use.") })
        append("demo-question-session", .with { $0.agentMessage.text = String(localized: "I’ve checked the sample project. Choose a greeting below, and I’ll update the page and run its tests.") })
        append("demo-question-session", .with { $0.questionAsked.question = question })
        append("demo-finished-session", .with { $0.turnStarted = .with { $0.turn = 1; $0.asker = "demo-client" } })
        append("demo-finished-session", .with { $0.userMessage.text = String(localized: "Add a welcome page and test it.") })
        finish("demo-finished-session", greeting: "Hello, world!")
        #if DEBUG
        let extra = UserDefaults.standard.integer(forKey: "DemoSessions")
        for i in 0 ..< max(0, extra) {
            var s = session("demo-extra-\(i)", title: "Sidebar Task \(i + 1)", machine: i.isMultiple(of: 2) ? "demo-mac" : "demo-linux", status: .idle)
            s.agent = i.isMultiple(of: 2) ? "codex" : "claude"
            s.updatedAt = .init(date: .now.addingTimeInterval(Double(-600 - i * 120)))
            world.sessions.append(s)
        }
        if UserDefaults.standard.bool(forKey: "DemoSidebarActivity") {
            world.sessions.append(session("demo-running", title: "Running sidebar task", machine: "demo-mac", status: .running))
            world.status.relayConnected = false
            // A step still at work, for its line in the session.
            append("demo-running", .with { $0.turnStarted = .with { $0.turn = 1; $0.asker = "demo-client" } })
            append("demo-running", .with { $0.userMessage.text = "Run the tests." })
            append("demo-running", .with { $0.toolCall = .with { $0.callID = "read"; $0.kind = "read"; $0.name = "read"; $0.summary = "package.json"; $0.decision = "allow" } })
            append("demo-running", .with { $0.toolResult = .with { $0.callID = "read"; $0.summary = "{}" } })
            append("demo-running", .with { $0.toolCall = .with { $0.callID = "test"; $0.kind = "shell"; $0.name = "npm test"; $0.summary = "npm test"; $0.decision = "allow" } })
        }
        #endif
        #if targetEnvironment(simulator)
        if Self.sessionDetailRegression {
            seedSessionDetailRegression()
        }
        if Self.manyTools {
            seedManyTools()
        }
        #endif
        _ = pairing(.client)
    }

    private func machine(_ id: String, _ name: String, _ platform: String) -> Machine {
        .with {
            $0.machineID = id; $0.machineName = name; $0.online = true; $0.version = "Demo"
            $0.status = .with {
                $0.name = name; $0.platform = platform; $0.maxSessions = 4
                $0.agents = ["claude", "codex"].map { name in .with {
                    $0.name = name; $0.available = true; $0.loggedIn = true; $0.version = "Demo"
                    $0.models = [.with { $0.id = "demo-model"; $0.name = "Demo Model"; $0.efforts = ["low", "high"] }]
                } }
            }
        }
    }

    private func session(_ id: String, title: String, machine: String, status: SessionStatus) -> Session {
        .with {
            $0.sessionID = id; $0.title = title; $0.machineID = machine; $0.projectID = "hello-world"
            $0.agent = "codex"; $0.createdBy = "demo-client"; $0.status = status
            $0.createdAt = .init(date: .now.addingTimeInterval(-300)); $0.updatedAt = .init(date: .now)
            $0.branch = "hermesh/welcome"; $0.workspace = .worktree; $0.progress.turnCount = status == .idle ? 1 : 0
        }
    }

    /// The normal Add screens lead to a simulated request instead of an install command or camera.
    func pairing(_ kind: Hermesh_Identity_V1_PairingKind) -> String {
        let code = "demo-\(kind.rawValue)"
        world.pairings.removeAll { $0.code == code }
        world.pairings.append(.with {
            $0.kind = kind; $0.code = code; $0.verificationCode = "DEMO"; $0.subjectID = "\(code)-subject"; $0.confirmed = true
            $0.label = kind == .client ? "Claude" : kind == .machine ? "Demo MacBook" : "Demo iPad"
            $0.platform = kind == .device ? "ios" : "macos"
            $0.expiresAt = .init(date: .now.addingTimeInterval(86400))
        })
        publish()
        return code
    }

    private func publish() {
        world.workVersion += 1
        if let data = try? world.jsonUTF8Data() { watchers.values.forEach { $0.yield(data) } }
    }

    private func append(_ id: String, _ value: SessionEvent) {
        var event = value
        event.seq = UInt64(events[id, default: []].count + 1)
        event.ts = .init(date: .now); event.turnID = "1"
        events[id, default: []].append(event)
        if let i = world.sessions.firstIndex(where: { $0.sessionID == id }) { world.sessions[i].progress.lastEventSeq = event.seq }
        if let data = try? event.jsonUTF8Data() {
            for (_, (session, c)) in followers where session == id { c.yield(data) }
        }
    }

    private func finish(_ id: String, greeting: String) {
        guard let i = world.sessions.firstIndex(where: { $0.sessionID == id }) else { return }
        append(id, .with { $0.toolCall = .with { $0.callID = "edit"; $0.kind = "edit"; $0.name = "apply_patch"; $0.summary = "src/welcome.ts"; $0.decision = "allow" } })
        append(id, .with { $0.toolResult = .with { $0.callID = "edit"; $0.summary = String(localized: "Updated the sample welcome page.") } })
        append(id, .with { $0.toolCall = .with { $0.callID = "test"; $0.kind = "shell"; $0.name = "npm test"; $0.summary = "npm test"; $0.decision = "allow" } })
        append(id, .with { $0.toolResult = .with { $0.callID = "test"; $0.summary = "PASS  welcome.test.ts\n2 tests passed (simulated)" } })
        let reply = String(localized: "The sample page now says “\(greeting)”. Both example tests passed. Open Session Info to inspect the changes. This is a simulated result; no code was run.")
        append(id, .with { $0.agentMessage.text = reply })
        append(id, .with { $0.turnCompleted = .with { $0.turn = 1; $0.result = reply; $0.diffStat = .with { $0.filesChanged = 1; $0.insertions = 1; $0.deletions = 1 } } })
        world.sessions[i].status = .idle; world.sessions[i].result = reply
        world.sessions[i].progress.turnCount = 1
        world.sessions[i].progress.clearPendingQuestion()
        world.sessions[i].progress.diffStat = .with { $0.filesChanged = 1; $0.insertions = 1; $0.deletions = 1 }
        world.sessions[i].progress.lastMessageExcerpt = reply
        world.sessions[i].updatedAt = .init(date: .now)
        greetings[id] = greeting
        publish()
    }

    private func stopTask(_ id: String) {
        // The stopped callback falls through to update Work's session; completed work is left intact.
        if id == HerReviewDemo.codingID, herReview.codingState != .stopped {
            if herReview.codingState == .running { herReview.interrupt() }
            return
        }
        guard let i = world.sessions.firstIndex(where: { $0.sessionID == id }) else { return }
        tasks.removeValue(forKey: id)?.cancel()
        if world.sessions[i].progress.hasPendingQuestion {
            let questionID = world.sessions[i].progress.pendingQuestion.questionID
            append(id, .with { $0.questionResolved = .with { $0.questionID = questionID; $0.resolution = .cancelled } })
        }
        world.sessions[i].status = .idle
        world.sessions[i].progress.clearPendingQuestion()
        world.sessions[i].updatedAt = .init(date: .now)
        append(id, .with { $0.agentMessage.text = String(localized: "The simulated task was stopped.") })
    }

    func runTask(client: String) throws -> String {
        guard let c = world.clients.first(where: { $0.clientID == client && !$0.revoked }),
              let project = world.projects.first(where: { p in
                  (c.spec.machineIds.isEmpty || c.spec.machineIds.contains("*") || c.spec.machineIds.contains(p.machineID)) &&
                  (c.spec.projectIds.isEmpty || c.spec.projectIds.contains("*") || c.spec.projectIds.contains(p.projectID)) &&
                  world.machines.contains { $0.machineID == p.machineID && $0.online && !$0.status.paused }
              }),
              let agent = ["codex", "claude"].first(where: {
                  (c.spec.agents.isEmpty || c.spec.agents.contains("*") || c.spec.agents.contains($0)) &&
                  (project.agents.isEmpty || project.agents.contains($0))
              }) else { throw APIError(code: "failed_precondition", message: String(localized: "Connect an agent and enable a machine with a project first, or exit and reopen the demo to start again.")) }
        let id = "demo-task-" + UUID().uuidString
        var s = session(id, title: String(localized: "Choose a greeting"), machine: project.machineID, status: .waitingInput)
        s.projectID = project.projectID; s.agent = agent
        s.createdBy = client
        s.createdAt = .init(date: .now)
        s.progress.pendingQuestion = sampleQuestion
        world.sessions.append(s)
        append(id, .with { $0.turnStarted = .with { $0.turn = 1; $0.asker = client } })
        append(id, .with { $0.userMessage.text = String(localized: "Build a welcome page for our sample project. Ask me which greeting to use.") })
        append(id, .with { $0.questionAsked.question = sampleQuestion })
        publish()
        return id
    }

    // UI automation must be able to interrupt even if finding the button takes longer than the demo task.
    private static let holdWorkTaskForUITest: Bool = {
        #if targetEnvironment(simulator)
        ProcessInfo.processInfo.arguments.contains("-HermeshHoldDemoWorkTask")
        #else
        false
        #endif
    }()

    // Simulator-only fixture for ToolStepsUITests: turns of hundreds of tool calls in a row.
    static let manyTools: Bool = {
        #if targetEnvironment(simulator)
        ProcessInfo.processInfo.arguments.contains("-DemoManyTools")
        #else
        false
        #endif
    }()

    // Simulator-only fixture for SessionDetailUITests (runs in both Debug and Release CI).
    private static let sessionDetailRegression: Bool = {
        #if targetEnvironment(simulator)
        ProcessInfo.processInfo.arguments.contains("-DemoSessionDetailRegression")
        #else
        false
        #endif
    }()

    private var sampleQuestion = Question()
    private var greetings: [String: String] = [:]
    private var wakes: [Wake] = []
    /// Storage (docs/settings-environments.md §5.7): the worktrees released, and auto-release, by machine.
    private var released: Set<String> = []
    private var autoRelease: [String: Bool] = [:]

    /// A machine's tasks as worktrees: sizes made up from their ids, the running ones busy, one with changes.
    private func demoWorkspaces(_ machine: String) -> [Hermesh_Machine_V1_Workspace] {
        let sessions = world.sessions.filter { $0.machineID == machine && !released.contains($0.sessionID) && $0.status != .archived }
        return sessions.enumerated().map { i, s in
            Hermesh_Machine_V1_Workspace.with {
                $0.sessionID = s.sessionID; $0.projectID = s.projectID; $0.title = s.displayTitle; $0.status = s.status
                $0.path = "~/.hermesh/worktrees/\(s.sessionID)"
                $0.sizeBytes = UInt64(s.sessionID.unicodeScalars.reduce(0) { $0 + Int($1.value) } % 2_400 + 300) * 1_000_000
                $0.lastActive = s.updatedAt
                if s.isInterruptible { $0.block = .busy } else if i == 1 { $0.block = .changes }
            }
        }.sorted { $0.sizeBytes > $1.sizeBytes }
    }

    func call(_ method: String, _ data: Data) throws -> Data {
        // Protobuf JSON keeps this dispatcher independent of the callers' generic response types.
        let r = try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
        func string(_ key: String) -> String { r[key] as? String ?? "" }
        func encoded(_ message: some SwiftProtobuf.Message) throws -> Data { try message.jsonUTF8Data() }
        func object(_ value: [String: Any] = [:]) throws -> Data { try JSONSerialization.data(withJSONObject: value) }
        let id = string("sessionId")
        #if DEBUG
        if Self.herDemoOn, let answer = try herDemo(method, r) { return answer }
        #endif
        switch method {
        case "GetStatus": return try encoded(world.status)
        case "ListMachines": return try encoded(Hermesh_Local_V1_ListMachinesResponse.with { $0.machines = world.machines })
        case "ListClients": return try encoded(Hermesh_Local_V1_ListClientsResponse.with { $0.clients = world.clients })
        case "ListDevices": return try encoded(Hermesh_Local_V1_ListDevicesResponse.with { $0.devices = world.devices })
        case "ListProjects": return try encoded(Hermesh_Local_V1_ListProjectsResponse.with {
            $0.projects = world.projects.filter { string("machineId").isEmpty || $0.machineID == string("machineId") }
        })
        case "ListPairingRequests": return try encoded(Hermesh_Local_V1_ListPairingRequestsResponse.with { $0.pairings = world.pairings })
        case "ListSessions": return try encoded(Hermesh_Local_V1_ListSessionsResponse.with { $0.sessions = world.sessions })
        case "LookupPairing":
            guard let p = world.pairings.first(where: { $0.code == string("code") }) else { throw APIError(code: "not_found", message: String(localized: "Use Add Machine or Connect Agent to create a demo request.")) }
            return try encoded(Hermesh_Local_V1_LookupPairingResponse.with { $0.details = p })
        case "AnswerQuestion", "InterruptSession", "ArchiveSession", "UnarchiveSession", "RenameSession":
            guard let i = world.sessions.firstIndex(where: { $0.sessionID == id }) else { throw CancellationError() }
            if method == "AnswerQuestion" {
                let request = try Hermesh_Local_V1_AnswerQuestionRequest(jsonUTF8Data: data)
                guard world.sessions[i].status == .waitingInput else { throw CancellationError() }
                append(id, .with { $0.questionResolved = .with {
                    $0.questionID = "demo-question"; $0.resolution = request.decline ? .declined : .answered
                    $0.by = "owner"; $0.answers = request.answers
                } })
                world.sessions[i].progress.clearPendingQuestion(); world.sessions[i].status = .running
                let answer = request.answers.first
                let text = answer?.text ?? ""
                let greeting = answer?.selected.first ?? (text.isEmpty ? "Hello, world!" : text)
                if !Self.holdWorkTaskForUITest {
                    tasks[id] = Task { [weak self] in
                        // Leave enough time to inspect and interrupt the simulated Work task.
                        try? await Task.sleep(for: .seconds(8))
                        guard !Task.isCancelled else { return }
                        self?.finish(id, greeting: greeting)
                        self?.tasks[id] = nil
                    }
                }
            } else if method == "InterruptSession" {
                stopTask(id)
            } else if method == "RenameSession" { world.sessions[i].title = string("title") }
            else { world.sessions[i].status = method == "ArchiveSession" ? .archived : .idle }
            world.sessions[i].updatedAt = .init(date: .now)
            publish()
            return try encoded(Hermesh_Local_V1_SessionResponse.with { $0.session = world.sessions[i] })
        case "DeleteSession":
            if id == HerReviewDemo.codingID { herReview.interrupt() }
            tasks.removeValue(forKey: id)?.cancel(); world.sessions.removeAll { $0.sessionID == id }; events[id] = nil
            world.deletedSessionIds = [id]; publish(); world.deletedSessionIds = []
        case "GetDiff":
            guard let greeting = greetings[id] else { return try object() }
            let literal = String(decoding: try JSONEncoder().encode(greeting), as: UTF8.self)
            return try encoded(Hermesh_Session_V1_GetDiffResponse.with {
                $0.diff = "diff --git a/src/welcome.ts b/src/welcome.ts\n--- a/src/welcome.ts\n+++ b/src/welcome.ts\n@@ -1 +1 @@\n-export const greeting = \"Hi\";\n+export const greeting = \(literal);\n"
            })
        case "ApproveClientPairing", "ApproveMachinePairing", "ApproveDevicePairing":
            guard let p = world.pairings.first(where: { $0.code == string("code") }) else { throw CancellationError() }
            world.pairings.removeAll { $0.code == p.code }
            if method == "ApproveClientPairing" {
                let request = try Hermesh_Local_V1_ApproveClientPairingRequest(jsonUTF8Data: data)
                world.clients.removeAll { $0.clientID == p.subjectID }
                world.clients.append(.with {
                    $0.clientID = p.subjectID; $0.grantID = p.code; $0.label = request.spec.label; $0.spec = request.spec
                    $0.online = true; $0.grantVersion = 1; $0.expiresAt = .init(date: .now.addingTimeInterval(Double(request.spec.ttlDays) * 86400))
                })
                publish()
                return try object(["clientId": p.subjectID, "grantId": p.code])
            } else if method == "ApproveMachinePairing" {
                world.machines.removeAll { $0.machineID == p.subjectID }
                world.machines.append(machine(p.subjectID, string("machineName"), "darwin/arm64"))
                publish(); return try object(["machineId": p.subjectID])
            } else {
                world.devices.removeAll { $0.keyID == p.subjectID }
                world.devices.append(.with { $0.keyID = p.subjectID; $0.label = p.label; $0.platform = p.platform })
                publish(); return try object(["keyId": p.subjectID, "matchCode": "42"])
            }
        case "UpdateClient", "RenameClient", "RevokeClient":
            if let i = world.clients.firstIndex(where: { $0.grantID == string("grantId") }) {
                if method == "UpdateClient" {
                    let request = try Hermesh_Local_V1_UpdateClientRequest(jsonUTF8Data: data)
                    world.clients[i].spec = request.spec
                    if request.spec.ttlDays > 0 {
                        world.clients[i].expiresAt = .init(date: .now.addingTimeInterval(Double(request.spec.ttlDays) * 86400))
                    }
                } else if method == "RenameClient" { world.clients[i].label = string("label") }
                else {
                    world.clients[i].revoked = true; world.clients[i].online = false
                    let client = world.clients[i].clientID
                    for session in world.sessions where session.createdBy == client && session.isInterruptible {
                        stopTask(session.sessionID)
                    }
                }
                world.clients[i].grantVersion += 1
            }
            publish()
        case "RenameMachine", "SetMachinePaused", "SetAgentDefaults", "RemoveMachine", "CheckAgents":
            let mid = string("machineId")
            if let i = world.machines.firstIndex(where: { $0.machineID == mid }) {
                switch method {
                case "RenameMachine": world.machines[i].machineName = string("machineName")
                case "SetMachinePaused": world.machines[i].status.paused = r["paused"] as? Bool ?? false
                case "SetAgentDefaults":
                    let d = try Hermesh_Local_V1_SetAgentDefaultsRequest(jsonUTF8Data: data).defaults
                    if let a = world.machines[i].status.agents.firstIndex(where: { $0.name == d.agent }) { world.machines[i].status.agents[a].defaults = d }
                case "RemoveMachine":
                    for session in world.sessions where session.machineID == mid && session.isInterruptible { stopTask(session.sessionID) }
                    world.machines.remove(at: i); world.projects.removeAll { $0.machineID == mid }
                default: break
                }
            }
            publish()
        case "RemoveDevice": world.devices.removeAll { $0.keyID == string("keyId") }; publish()
        case "AddProject":
            let request = try Hermesh_Local_V1_AddProjectRequest(jsonUTF8Data: data)
            world.projects.removeAll { $0.machineID == request.machineID && $0.projectID == request.projectID }
            world.projects.append(.with {
                $0.machineID = request.machineID; $0.projectID = request.projectID; $0.path = request.path
                $0.workspace = request.workspace; $0.defaultBaseRef = request.defaultBaseRef
                $0.description_p = request.description_p; $0.agents = request.agents
            }); publish()
        case "ListWorkspaces":
            let mid = string("machineId")
            return try encoded(Hermesh_Machine_V1_ListWorkspacesResponse.with {
                $0.workspaces = demoWorkspaces(mid)
                $0.scannedAt = .init(date: Date())
                $0.autoRelease = autoRelease[mid] ?? false
            })
        case "ReleaseWorkspaces":
            let request = try Hermesh_Local_V1_ReleaseWorkspacesRequest(jsonUTF8Data: data)
            let rows = demoWorkspaces(request.machineID)
            return try encoded(Hermesh_Machine_V1_ReleaseWorkspacesResponse.with {
                $0.results = request.sessionIds.map { id in
                    let w = rows.first { $0.sessionID == id }
                    let ok = w != nil && w!.block == .unspecified
                    if ok { released.insert(id) }
                    return .with { $0.sessionID = id; $0.released = ok; $0.block = w?.block ?? .unverified; $0.blockPath = w?.blockPath ?? "" }
                }
            })
        case "SetWorkspacePolicy": autoRelease[string("machineId")] = r["autoRelease"] as? Bool ?? false
        case "RemoveProject": world.projects.removeAll { $0.machineID == string("machineId") && $0.projectID == string("projectId") }; publish()
        case "ListMachineRepos": return try object(["repos": [["path": "~/Projects/hello-world", "branch": "main"], ["path": "~/Projects/playground", "branch": "main"]]])
        case "ListMachineFolders":
            let path = string("path").isEmpty ? "~" : string("path")
            let folders: [[String: Any]]
            if path == "~" {
                folders = [["name": "Projects", "path": "~/Projects"]]
            } else if path == "~/Projects" {
                folders = ["hello-world", "playground"].map { ["name": $0, "path": "~/Projects/\($0)", "repo": true, "branch": "main"] }
            } else { folders = [] }
            let repo = path != "~" && path != "~/Projects"
            return try object(["path": path, "fullPath": path.replacingOccurrences(of: "~", with: "/Demo"), "repo": repo, "branch": repo ? "main" : "", "branches": repo ? ["main"] : [], "folders": folders])
        case "TestMachineAgent": return try object(["ok": true, "reply": String(localized: "Hello from the demo agent! This response is simulated."), "millis": 120])
        case "QueryWork":
            return try encoded(queryWork(try Hermesh_Local_V1_QueryWorkRequest(jsonUTF8Data: data)))
        case "ListWakes": return try encoded(Hermesh_Local_V1_ListWakesResponse.with { $0.wakes = wakes.filter { $0.clientID == string("clientId") } })
        case "SetWake", "CheckWake":
            let client = string("clientId"), name = string("name")
            var wake = wakes.first { $0.clientID == client && $0.name == name } ?? Wake()
            wake.clientID = client; wake.name = name
            if method == "SetWake" { wake.url = string("url") }
            wake.state = "on"; wake.lastStatus = 200; wake.lastRingAt = .init(date: .now)
            wake.recent.insert(.with { $0.at = .init(date: .now); $0.check = true; $0.status = 200 }, at: 0)
            wakes.removeAll { $0.clientID == client && $0.name == name }; wakes.append(wake)
            // A simulated check only: never open the URL or store its authorization.
            return try encoded(Hermesh_Local_V1_SetWakeResponse.with { $0.wake = wake; $0.check = .with { $0.ok = true; $0.status = 200 } })
        case "ClearWake": wakes.removeAll { $0.clientID == string("clientId") && $0.name == string("name") }
        case "GetHerConversation":
            return try encoded(Hermesh_Local_V1_GetHerConversationResponse.with {
                $0.bot = herReview.keptBot; $0.snapshot = herReview.keptSnapshot
            })
        case "HerSend":
            let request = try Hermesh_Local_V1_HerSendRequest(jsonUTF8Data: data)
            guard request.machineID == "demo-mac", request.localPaths.isEmpty, request.pictures.isEmpty else {
                throw APIError(code: "demo_only", message: String(localized: "The demo uses sample text only. No personal files or photos are read."))
            }
            try herReview.send(request.text, id: request.id)
            return try encoded(Hermesh_Local_V1_HerSendResponse.with { $0.delivered = true })
        case "ListHerWaiting", "DeleteHerWaiting": break
        default: throw APIError(code: "unimplemented", message: String(localized: "This action needs a real connection. Exit the demo to set up your own workspace."))
        }
        return try object()
    }

    /// The Her protocol and the normal session screens share the same disposable coding task.
    private func updateHerTask() {
        let id = HerReviewDemo.codingID
        switch herReview.codingState {
        case .running:
            world.sessions.removeAll { $0.sessionID == id }
            var s = session(id, title: "Her sample task", machine: "demo-linux", status: .running)
            s.createdBy = "her"
            world.sessions.append(s)
            events[id] = []
            greetings[id] = nil
            append(id, .with { $0.turnStarted = .with { $0.turn = 1; $0.asker = "Her" } })
            append(id, .with { $0.userMessage.text = String(localized: "Update the sample welcome page. This task was delegated by Her; no code is executed.") })
        case .done:
            finish(id, greeting: "Welcome from Her!")
            return
        case .failed:
            guard let i = world.sessions.firstIndex(where: { $0.sessionID == id }) else { return }
            let message = String(localized: "The sample check failed. No code ran. Return to Her and retry the sample task.")
            world.sessions[i].status = .crashed
            world.sessions[i].error = message
            world.sessions[i].progress.lastFailure = .with { $0.message = message; $0.retryable = true }
            world.sessions[i].result = message
            append(id, .with { $0.agentMessage.text = message })
        case .stopped:
            stopTask(id)
        case nil: return
        }
        publish()
    }

    private func queryWork(_ r: Hermesh_Local_V1_QueryWorkRequest) -> Hermesh_Local_V1_QueryWorkResponse {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: r.timeZone) ?? .current
        let parts = r.firstDay.split(separator: "-").compactMap { Int($0) }
        guard parts.count == 3, let start = calendar.date(from: DateComponents(year: parts[0], month: parts[1], day: parts[2])) else { return .init() }
        var result = Hermesh_Local_V1_QueryWorkResponse()
        for s in world.sessions {
            let day = calendar.dateComponents([.day], from: start, to: calendar.startOfDay(for: s.createdAt.date)).day ?? 0
            guard day >= 0, day < Int(r.days) else { continue }
            let bucket = UInt32(r.bucket == .hour ? day * 24 + calendar.component(.hour, from: s.createdAt.date) : day)
            let group = Hermesh_Local_V1_WorkGroup.with {
                if r.groupBy.contains(.client) { $0.client = s.createdBy }
                if r.groupBy.contains(.agent) { $0.agent = s.agent }
                if r.groupBy.contains(.machine) { $0.machineID = s.machineID }
                if r.groupBy.contains(.project) { $0.projectID = s.projectID }
            }
            let done = s.status == .idle || s.status == .archived
            let count = Hermesh_Local_V1_SessionCount.with { $0.bucket = bucket; $0.group = group; $0.sessions = 1; $0.done = done ? 1 : 0 }
            result.sessions.append(count); result.rangeSessions.append(count)
            if done {
                let turn = Hermesh_Local_V1_TurnCount.with {
                    $0.bucket = bucket; $0.group = group; $0.turns = 1; $0.inputTokens = 1200; $0.outputTokens = 350; $0.busySeconds = 45
                }
                result.turns.append(turn); result.rangeTurns.append(turn)
            }
        }
        result.sources = world.machines.map { m in .with { $0.machineID = m.machineID; $0.live = true; $0.syncedAt = .init(date: .now) } }
        return result
    }

    func stream(_ method: String, _ data: Data) throws -> AsyncThrowingStream<Data, Error> {
        let token = UUID()
        if method == "Watch" {
            return AsyncThrowingStream { c in
                watchers[token] = c
                do { c.yield(try world.jsonUTF8Data()) } catch { c.finish(throwing: error) }
                c.onTermination = { [weak self] _ in Task { @MainActor in self?.watchers[token] = nil } }
            }
        }
        if method == "StreamSessionEvents" {
            let request = try Hermesh_Local_V1_StreamSessionEventsRequest(jsonUTF8Data: data)
            return AsyncThrowingStream { c in
                followers[token] = (request.sessionID, c)
                #if targetEnvironment(simulator)
                if Self.sessionDetailRegression,
                   streamSessionDetailRegression(request: request, token: token, continuation: c) {
                    return
                }
                #endif
                do {
                    for e in events[request.sessionID, default: []] where e.seq > request.afterSeq { c.yield(try e.jsonUTF8Data()) }
                } catch { c.finish(throwing: error) }
                c.onTermination = { [weak self] _ in Task { @MainActor in self?.followers[token] = nil } }
            }
        }
        throw APIError(code: "unimplemented", message: String(localized: "This action needs a real connection. Exit the demo to set up your own workspace."))
    }

    #if targetEnvironment(simulator)
    /// Two sessions of one turn of many tool calls in a row, nothing said between them (one folded group): "Many steps",
    /// done, of 600 calls with outputs of a line to a page; "Many steps, running", which adds a call a second.
    private func seedManyTools() {
        func call(_ id: String, _ i: Int, result: Bool) {
            let kinds = [("read", "Read", "internal/pkg\(i % 40)/file\(i % 90).go"), ("search", "Grep", "log.Printf"),
                         ("edit", "Edit", "internal/pkg\(i % 40)/file\((i * 7) % 90).go"), ("shell", "Bash", "go test ./internal/pkg\(i % 40)/... -run Test\(i) -count=1")]
            let (kind, name, summary) = kinds[(i * 7919) % 4]
            append(id, .with { $0.toolCall = .with { $0.callID = "\(id)-\(i)"; $0.kind = kind; $0.name = name; $0.summary = summary; $0.decision = "allow" } })
            guard result else { return }
            let lines = 1 + (i * 37) % 60
            let output = (1 ... lines).map { "\($0)\tlog.Printf(\"handled %d of %s\", n, \(i * 1000 + $0))" }.joined(separator: "\n")
            append(id, .with { $0.toolResult = .with { $0.callID = "\(id)-\(i)"; $0.summary = output; $0.isError = i % 37 == 11 } })
        }
        func start(_ id: String, title: String, status: SessionStatus) {
            var s = session(id, title: title, machine: "demo-mac", status: status)
            s.agent = "codex"
            s.updatedAt = .init(date: .now.addingTimeInterval(40))
            s.progress.turnCount = status == .running ? 0 : 1
            if status == .running { s.progress.turnStartedAt = .init(date: .now) }
            world.sessions.append(s)
            append(id, .with { $0.turnStarted = .with { $0.turn = 1; $0.asker = "owner" } })
            append(id, .with { $0.userMessage.text = "Switch every log call in the repository to structured logging, and keep the tests green." })
            append(id, .with { $0.agentMessage.text = "Sure. First I'll find every call, then change them one by one." })
        }
        start("demo-many-steps", title: "Many steps", status: .idle)
        for i in 0 ..< 600 { call("demo-many-steps", i, result: true) }
        append("demo-many-steps", .with { $0.agentMessage.text = "Done: after 600 steps every call uses slog, and the tests pass." })
        append("demo-many-steps", .with { $0.turnCompleted = .with { $0.turn = 1 } })

        start("demo-many-steps-running", title: "Many steps, running", status: .running)
        for i in 0 ..< 80 { call("demo-many-steps-running", i, result: true) }
        call("demo-many-steps-running", 80, result: false)
        tasks["demo-many-steps-running"] = Task { @MainActor [weak self] in
            var i = 80
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(1))
                guard let self, !Task.isCancelled else { return }
                // The call under way finishes and the next starts.
                append("demo-many-steps-running", .with { $0.toolResult = .with { $0.callID = "demo-many-steps-running-\(i)"; $0.summary = "ok" } })
                i += 1
                call("demo-many-steps-running", i, result: false)
            }
        }
    }

    private func seedSessionDetailRegression() {
        let now = Date.now

        func addHistory(id: String, title: String, prefix: String, turns: Int, status: SessionStatus, updatedOffset: TimeInterval) {
            var s = session(id, title: title, machine: "demo-mac", status: status)
            s.agent = "codex"
            s.updatedAt = .init(date: now.addingTimeInterval(updatedOffset))
            s.progress.turnCount = UInt32(status == .running ? max(0, turns - 1) : turns)
            if status == .running { s.progress.turnStartedAt = .init(date: now) }
            world.sessions.append(s)

            for t in 1 ... turns {
                append(id, .with { $0.turnStarted = .with { $0.turn = UInt32(t); $0.asker = "demo-client" } })
                append(id, .with { $0.userMessage.text = "\(prefix) prompt #\(t): inspect module \(t) and report the current status." })
                let marker = t == 1 ? "\(prefix) start #1" : (t == turns ? "\(prefix) latest #\(turns)" : "\(prefix) item #\(t)")
                append(id, .with {
                    $0.agentMessage.text = """
                    \(marker): completed check for step \(t).

                    Reviewed the implementation details and verified layout spacing, event ordering, and scrolling behavior across paragraphs for entry \(t).
                    """
                })
                if status != .running || t < turns {
                    append(id, .with { $0.turnCompleted = .with { $0.turn = UInt32(t) } })
                }
            }
        }

        addHistory(id: "demo-long-history", title: "Long history session", prefix: "Long", turns: 24, status: .idle, updatedOffset: 20)
        addHistory(id: "demo-delayed-history", title: "Delayed history session", prefix: "Delayed", turns: 24, status: .idle, updatedOffset: 10)
        addHistory(id: "demo-streaming-session", title: "Streaming session", prefix: "Stream", turns: 20, status: .running, updatedOffset: 30)
    }

    private func streamSessionDetailRegression(
        request: Hermesh_Local_V1_StreamSessionEventsRequest,
        token: UUID,
        continuation c: AsyncThrowingStream<Data, Error>.Continuation
    ) -> Bool {
        if request.sessionID == "demo-delayed-history", request.afterSeq == 0 {
            let pending = events[request.sessionID, default: []]
            let mid = pending.count / 2
            let delayTask = Task { @MainActor [weak self] in
                try? await Task.sleep(for: .milliseconds(550))
                guard !Task.isCancelled, self?.followers[token] != nil else { return }
                for e in pending[..<mid] {
                    if let data = try? e.jsonUTF8Data() { c.yield(data) }
                }
                try? await Task.sleep(for: .milliseconds(350))
                guard !Task.isCancelled, self?.followers[token] != nil else { return }
                for e in pending[mid...] {
                    if let data = try? e.jsonUTF8Data() { c.yield(data) }
                }
            }
            c.onTermination = { [weak self] _ in
                delayTask.cancel()
                Task { @MainActor in self?.followers[token] = nil }
            }
            return true
        }
        if request.sessionID == "demo-streaming-session" {
            do {
                for e in events[request.sessionID, default: []] where e.seq > request.afterSeq { c.yield(try e.jsonUTF8Data()) }
            } catch { c.finish(throwing: error) }
            c.onTermination = { [weak self] _ in Task { @MainActor in self?.followers[token] = nil } }
            if tasks[request.sessionID] == nil {
                let id = request.sessionID
                tasks[id] = Task { @MainActor [weak self] in
                    // Keep the simulator fixture streaming until the test leaves the demo.
                    for n in 1... {
                        try? await Task.sleep(for: .milliseconds(350))
                        guard !Task.isCancelled, let self else { return }
                        var draft = SessionEvent()
                        draft.agentDraft = .with {
                            $0.whole = true
                            $0.text = "Live draft #\(n): streaming incremental analysis line 1.\n\nContinuing with additional context for chunk \(n)."
                        }
                        if let data = try? draft.jsonUTF8Data() {
                            for (_, (session, stream)) in self.followers where session == id { stream.yield(data) }
                        }
                        try? await Task.sleep(for: .milliseconds(250))
                        guard !Task.isCancelled else { return }
                        self.append(id, .with {
                            $0.agentMessage.text = """
                            Live update #\(n): committed streamed message body for step \(n).

                            This paragraph grows the bottom of the session while verifying auto-follow and scroll-up stability.
                            """
                        })
                    }
                }
            }
            return true
        }
        return false
    }
    #endif
}
