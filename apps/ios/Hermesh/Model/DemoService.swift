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
    var world = Hermesh_Local_V1_WatchResponse()
    var projections: [String: Hermesh_Session_V1_SessionProjection] = [:]
    var events: [String: [SessionEvent]] = [:]
    private var watchers: [UUID: AsyncThrowingStream<Data, Error>.Continuation] = [:]
    var followers: [UUID: (String, AsyncThrowingStream<Data, Error>.Continuation)] = [:]
    var tasks: [String: Task<Void, Never>] = [:]
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
        world.machines = [
            machine("demo-mac", "Mac mini", "darwin/arm64"),
            machine("demo-linux", Self.bigUsage ? "Production Linux Build Workstation (us-west-2)" : "Linux Studio", "linux/amd64"),
        ]
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
        let multiDemo = ProcessInfo.processInfo.arguments.contains("-HermeshDemoMultiQuestion")
        let question = Question.with {
            $0.questionID = "demo-question"; $0.turn = 1; $0.askedAt = now
            if multiDemo {
                $0.items = [
                    .with {
                        $0.id = "greeting"
                        $0.text = String(localized: "Which greeting should the sample app show?")
                        $0.options = [.with { $0.label = "(Recommended) Hello, world!" }, .with { $0.label = "Welcome aboard!" }]
                        $0.allowOther = true
                    },
                    .with {
                        $0.id = "style"
                        $0.text = "Which layout style should it use?"
                        $0.options = [
                            .with { $0.label = "(Recommended) Compact card"; $0.description_p = "Fits above the composer" },
                            .with { $0.label = "Full banner"; $0.description_p = "Spans the page width" },
                        ]
                        $0.allowOther = true
                    },
                    .with {
                        $0.id = "checks"
                        $0.text = "Run tests after updating?"
                        $0.options = [.with { $0.label = "(Recommended) Run unit tests" }, .with { $0.label = "Skip tests" }]
                        $0.allowOther = true
                    },
                ]
            } else {
                $0.items = [.with {
                    $0.id = "greeting"; $0.header = String(localized: "Greeting")
                    $0.text = String(localized: "Which greeting should the sample app show?")
                    $0.options = [.with { $0.label = "Hello, world!" }, .with { $0.label = "Welcome aboard!" }]
                    $0.allowOther = true
                }]
            }
        }
        sampleQuestion = question
        var qSession = session("demo-question-session", title: String(localized: "Choose a greeting"), machine: "demo-mac", status: .waitingInput)
        qSession.pendingRequestID = "demo-question"
        qSession.activeRunID = "r1"
        qSession.progress.pendingQuestion = question
        let doneSession = session("demo-finished-session", title: String(localized: "Add a welcome page"), machine: "demo-linux", status: .idle)
        world.sessions = [qSession, doneSession]

        beginRun("demo-question-session", runID: "r1", ordinal: 1, asker: "demo-client", status: .waitingRequest)
        appendItem("demo-question-session", runID: "r1", kind: .userMessage) {
            $0.actor = .with { $0.kind = .externalAgent; $0.id = "demo-client" }
            $0.text = String(localized: "Build a welcome page for our sample project. Ask me which greeting to use.")
        }
        appendItem("demo-question-session", runID: "r1", kind: .assistantMessage) {
            $0.text = String(localized: "I’ve checked the sample project. Choose a greeting below, and I’ll update the page and run its tests.")
        }
        openQuestionRequest("demo-question-session", runID: "r1", question: question)

        beginRun("demo-finished-session", runID: "r1", ordinal: 1, asker: "demo-client", status: .running)
        appendItem("demo-finished-session", runID: "r1", kind: .userMessage) {
            $0.actor = .with { $0.kind = .externalAgent; $0.id = "demo-client" }
            $0.text = String(localized: "Add a welcome page and test it.")
        }
        finish("demo-finished-session", greeting: "Hello, world!")

        #if DEBUG
        let extra = UserDefaults.standard.integer(forKey: "DemoSessions")
        for i in 0 ..< max(0, extra) {
            var s = session("demo-extra-\(i)", title: "Sidebar Task \(i + 1)", machine: i.isMultiple(of: 2) ? "demo-mac" : "demo-linux", status: .idle)
            // With large usage, as many agents as a busy owner's, the busiest first.
            s.agent = Self.bigUsage ? ["codex", "codex", "codex", "antigravity", "claude", "gemini", "opencode"][i % 7] : i.isMultiple(of: 2) ? "codex" : "claude"
            s.updatedAt = .init(date: .now.addingTimeInterval(Double(-600 - i * 120)))
            world.sessions.append(s)
            syncSession(s.sessionID)
        }
        if UserDefaults.standard.bool(forKey: "DemoSidebarActivity") {
            var s = session("demo-running", title: "Running sidebar task", machine: "demo-mac", status: .running)
            s.activeRunID = "r1"
            world.sessions.append(s)
            world.status.relayConnected = false
            beginRun("demo-running", runID: "r1", ordinal: 1, asker: "demo-client", status: .running)
            appendItem("demo-running", runID: "r1", kind: .userMessage) { $0.text = "Run the tests." }
            appendItem("demo-running", runID: "r1", kind: .toolCall) { $0.toolName = "Read"; $0.filePath = "package.json"; $0.output = "{}" }
            appendItem("demo-running", runID: "r1", kind: .commandExecution) { $0.command = "npm test"; $0.status = "running" }
        }
        if UserDefaults.standard.bool(forKey: "DemoMultiProject") {
            world.projects.append(.with {
                $0.machineID = "demo-mac"; $0.projectID = "alpha"; $0.path = "/Demo/Projects/alpha"
                $0.defaultBaseRef = "main"; $0.workspace = .worktree; $0.agents = ["claude", "codex"]
            })
            for i in 0 ..< 8 {
                var s = session("demo-alpha-\(i)", title: "Alpha Task \(i + 1)", machine: "demo-mac", status: .idle)
                s.projectID = "alpha"
                s.updatedAt = .init(date: .now.addingTimeInterval(Double(-100 - i * 60)))
                world.sessions.append(s)
                syncSession(s.sessionID)
            }
        }
        #endif
        #if targetEnvironment(simulator)
        if Self.sessionDetailRegression {
            seedSessionDetailRegression()
        }
        if Self.manyTools {
            seedManyTools()
        }
        if Self.codeBlockRunner {
            seedCodeBlockRunner()
        }
        if Self.liveProgress {
            seedLiveProgress()
        }
        if Self.liveThinking {
            seedLiveThinking()
        }
        if UserDefaults.standard.bool(forKey: "DemoGitStatus") || ProcessInfo.processInfo.arguments.contains("-DemoGitStatus") {
            seedGitStatusDemo()
        }
        if Self.questionFlow {
            seedQuestionFlow()
        }
        if Self.planReviewFlow {
            seedPlanReviewFlow()
        }
        #endif
        _ = pairing(.client)
        #if targetEnvironment(simulator)
        if Self.linkMap {
            seedLinkMap()
        }
        if Self.perfSuite {
            seedPerfSuite()
        }
        #endif
    }

    private static func demoLimits(_ agent: String) -> Hermesh_Machine_V1_AgentLimits {
        let window = { (minutes: UInt32, used: UInt32, resetsIn: TimeInterval) in
            Hermesh_Machine_V1_LimitWindow.with { $0.minutes = minutes; $0.usedPercent = used; $0.resetsAt = .init(date: .now.addingTimeInterval(resetsIn)) }
        }
        return .with {
            $0.observedAt = .init(date: .now.addingTimeInterval(-120))
            if agent == "claude" {
                $0.windows = [window(300, 82, 140 * 60), window(10080, 41, 3 * 86400)]
            } else {
                $0.plan = "pro"
                $0.windows = [window(10080, 61, 2 * 86400)]
            }
        }
    }

    func machine(_ id: String, _ name: String, _ platform: String) -> Machine {
        .with {
            $0.machineID = id; $0.machineName = name; $0.online = true; $0.version = "Demo"
            $0.status = .with {
                $0.name = name; $0.platform = platform; $0.maxSessions = 4
                $0.agents = ["claude", "codex"].map { name in .with {
                    $0.name = name; $0.available = true; $0.loggedIn = true; $0.version = "Demo"
                    $0.models = [.with { $0.id = "demo-model"; $0.name = "Demo Model"; $0.efforts = ["low", "high"] }]
                    // What the agents last said of their limits, on the Mac mini: Claude Code's five hours nearly used up.
                    if id == "demo-mac" { $0.limits = Self.demoLimits(name) }
                } }
            }
        }
    }

    func session(_ id: String, title: String, machine: String, status: SessionStatus) -> Session {
        .with {
            $0.sessionID = id; $0.title = title; $0.machineID = machine; $0.projectID = "hello-world"
            $0.agent = "codex"; $0.createdBy = "demo-client"; $0.status = status
            $0.modelSelection = .with { $0.instanceID = "codex"; $0.model = "demo-model" }
            $0.runtimeMode = .auto; $0.maxRuntimeMode = .fullAccess
            $0.createdAt = .init(date: .now.addingTimeInterval(-300)); $0.updatedAt = .init(date: .now)
            $0.branch = "hermesh/welcome"; $0.workspace = .worktree; $0.progress.turnCount = status == .idle ? 1 : 0
        }
    }

    func syncSession(_ id: String) {
        guard let s = world.sessions.first(where: { $0.sessionID == id }) else { return }
        projections[id, default: .init()].session = s
    }

    func emitEvent(_ id: String, _ build: (inout SessionEvent) -> Void) {
        let seq = UInt64(events[id, default: []].count + 1)
        if let i = world.sessions.firstIndex(where: { $0.sessionID == id }) {
            world.sessions[i].progress.lastEventSeq = seq
            projections[id, default: .init()].session = world.sessions[i]
        }
        var ev = SessionEvent()
        ev.seq = seq
        ev.sessionID = id
        ev.occurredAt = .init(date: .now)
        if let s = world.sessions.first(where: { $0.sessionID == id }) { ev.session = s }
        build(&ev)
        events[id, default: []].append(ev)
        if let data = try? ev.jsonUTF8Data() {
            for (_, (session, c)) in followers where session == id { c.yield(data) }
        }
    }

    func beginRun(_ id: String, runID: String, ordinal: UInt32, asker: String, status: Hermesh_Session_V1_RunStatus, startedAt: Date = .now) {
        let actor: Hermesh_Session_V1_Actor = .with {
            $0.kind = asker == "owner" ? .owner : (asker.lowercased() == "her" ? .her : .externalAgent)
            $0.id = asker
        }
        let run: Hermesh_Session_V1_Run = .with {
            $0.runID = runID; $0.sessionID = id; $0.ordinal = ordinal; $0.status = status
            $0.author = actor; $0.createdAt = .init(date: startedAt); $0.startedAt = .init(date: startedAt)
        }
        var p = projections[id, default: .init()]
        if let idx = p.runs.firstIndex(where: { $0.runID == runID }) { p.runs[idx] = run } else { p.runs.append(run) }
        projections[id] = p
        emitEvent(id) { $0.type = "run.started"; $0.run = run }
    }

    @discardableResult
    func appendItem(_ id: String, runID: String, kind: Hermesh_Session_V1_TurnItemKind, _ more: (inout Hermesh_Session_V1_TurnItem) -> Void) -> Hermesh_Session_V1_TurnItem {
        var p = projections[id, default: .init()]
        let itemSeq = UInt64((p.items.last?.seq ?? 0) + 1)
        var it = Hermesh_Session_V1_TurnItem()
        it.itemID = "it-\(id)-\(itemSeq)"
        it.sessionID = id
        it.runID = runID
        it.seq = itemSeq
        it.kind = kind
        it.status = "completed"
        it.createdAt = .init(date: .now)
        it.updatedAt = .init(date: .now)
        more(&it)
        p.items.append(it)
        projections[id] = p
        emitEvent(id) { $0.type = "item.updated"; $0.item = it }
        return it
    }

    private func openQuestionRequest(_ id: String, runID: String, question: Question) {
        let req: Hermesh_Session_V1_RuntimeRequest = .with {
            $0.requestID = question.questionID; $0.sessionID = id; $0.runID = runID
            $0.kind = .userInput; $0.status = .pending
            $0.createdAt = question.askedAt
            $0.userInput.questions = question.items
        }
        projections[id, default: .init()].requests.append(req)
        appendItem(id, runID: runID, kind: .runtimeRequest) { $0.requestID = req.requestID; $0.status = "waiting" }
        emitEvent(id) { $0.type = "request.opened"; $0.request = req }
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

    private func finish(_ id: String, greeting: String) {
        guard let i = world.sessions.firstIndex(where: { $0.sessionID == id }) else { return }
        let runID = world.sessions[i].activeRunID.isEmpty ? "r1" : world.sessions[i].activeRunID
        appendItem(id, runID: runID, kind: .fileChange) {
            $0.filePath = "src/welcome.ts"
            $0.input = "{\n  \"path\": \"src/welcome.ts\"\n}"
            $0.diff = String(localized: "Updated the sample welcome page.")
        }
        appendItem(id, runID: runID, kind: .commandExecution) {
            $0.command = "npm test"
            $0.input = "{\n  \"command\": \"npm test\"\n}"
            $0.output = "PASS  welcome.test.ts\n2 tests passed (simulated)"
        }
        let reply = String(localized: "The sample page now says “\(greeting)”. Both example tests passed. Open Session Info to inspect the changes. This is a simulated result; no code was run.")
        appendItem(id, runID: runID, kind: .assistantMessage) { $0.text = reply }
        world.sessions[i].status = .idle; world.sessions[i].result = reply
        world.sessions[i].activeRunID = ""
        world.sessions[i].pendingRequestID = ""
        world.sessions[i].progress.turnCount = max(1, world.sessions[i].progress.turnCount)
        world.sessions[i].progress.clearPendingQuestion()
        world.sessions[i].progress.diffStat = .with { $0.filesChanged = 1; $0.insertions = 1; $0.deletions = 1 }
        world.sessions[i].progress.lastMessageExcerpt = reply
        let branchName = world.sessions[i].branch.isEmpty ? "hermesh/welcome" : world.sessions[i].branch
        world.sessions[i].gitStatus = .with {
            $0.state = .pushed
            $0.branch = branchName
            $0.targetRef = "origin/\(branchName)"
            $0.headCommit = "a4c91e2"
            $0.headSubject = "feat: add sample welcome page"
        }
        world.sessions[i].updatedAt = .init(date: .now)
        if let rIdx = projections[id]?.runs.firstIndex(where: { $0.runID == runID }) {
            projections[id]?.runs[rIdx].status = .completed
            projections[id]?.runs[rIdx].completedAt = .init(date: .now)
            let doneRun = projections[id]!.runs[rIdx]
            emitEvent(id) { $0.type = "run.completed"; $0.run = doneRun }
        } else {
            syncSession(id)
        }
        greetings[id] = greeting
        publish()
    }

    private func stopTask(_ id: String) {
        if id == HerReviewDemo.codingID, herReview.codingState != .stopped {
            if herReview.codingState == .running { herReview.interrupt() }
            return
        }
        guard let i = world.sessions.firstIndex(where: { $0.sessionID == id }) else { return }
        tasks.removeValue(forKey: id)?.cancel()
        if let reqIdx = projections[id]?.requests.firstIndex(where: { $0.status == .pending }) {
            projections[id]?.requests[reqIdx].status = .dismissed
            projections[id]?.requests[reqIdx].questionResolution = .cancelled
            let req = projections[id]!.requests[reqIdx]
            emitEvent(id) { $0.type = "request.resolved"; $0.request = req }
        }
        let runID = world.sessions[i].activeRunID.isEmpty ? "r1" : world.sessions[i].activeRunID
        world.sessions[i].status = .idle
        world.sessions[i].activeRunID = ""
        world.sessions[i].pendingRequestID = ""
        world.sessions[i].progress.clearPendingQuestion()
        world.sessions[i].updatedAt = .init(date: .now)
        appendItem(id, runID: runID, kind: .assistantMessage) { $0.text = String(localized: "The simulated task was stopped.") }
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
        s.activeRunID = "r1"
        s.pendingRequestID = sampleQuestion.questionID
        s.progress.pendingQuestion = sampleQuestion
        world.sessions.append(s)
        beginRun(id, runID: "r1", ordinal: 1, asker: client, status: .waitingRequest)
        appendItem(id, runID: "r1", kind: .userMessage) {
            $0.actor = .with { $0.kind = .externalAgent; $0.id = client }
            $0.text = String(localized: "Build a welcome page for our sample project. Ask me which greeting to use.")
        }
        openQuestionRequest(id, runID: "r1", question: sampleQuestion)
        publish()
        return id
    }

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

    // Simulator-only comprehensive stress-test dataset for PerfStressUITests.
    static let perfSuite: Bool = {
        #if targetEnvironment(simulator)
        ProcessInfo.processInfo.arguments.contains("-DemoPerfSuite")
        #else
        false
        #endif
    }()

    // Simulator-only: each turn counts as a very busy owner's do (billions of tokens over a month, hours of work), so
    // the Usage page can be checked at its largest numbers (DemoUITests.testUsageAtLargeNumbers).
    private static let bigUsage: Bool = {
        #if targetEnvironment(simulator)
        ProcessInfo.processInfo.arguments.contains("-DemoBigUsage")
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

    // Simulator-only fixture for CodeBlockRunnerUITests.
    private static let codeBlockRunner: Bool = {
        #if targetEnvironment(simulator)
        ProcessInfo.processInfo.arguments.contains("-DemoCodeBlockRunner")
        #else
        false
        #endif
    }()

    // Simulator-only fixture for LiveProgressUITests.
    private static let liveProgress: Bool = {
        #if targetEnvironment(simulator)
        ProcessInfo.processInfo.arguments.contains("-DemoLiveProgress")
        #else
        false
        #endif
    }()

    // Simulator-only fixture for LiveThinkingUITests.
    private static let liveThinking: Bool = {
        #if targetEnvironment(simulator)
        ProcessInfo.processInfo.arguments.contains("-DemoLiveThinking")
        #else
        false
        #endif
    }()

    // Simulator-only fixture for QuestionFlowUITests.
    private static let questionFlow: Bool = {
        #if targetEnvironment(simulator)
        ProcessInfo.processInfo.arguments.contains("-DemoQuestionFlow")
        #else
        false
        #endif
    }()

    // Simulator-only fixture for PlanReviewFlowUITests.
    private static let planReviewFlow: Bool = {
        #if targetEnvironment(simulator)
        ProcessInfo.processInfo.arguments.contains("-DemoPlanReviewFlow")
        #else
        false
        #endif
    }()

    // Simulator-only fixture for multi-node, multi-session LinkMap verification.
    private static let linkMap: Bool = {
        #if targetEnvironment(simulator)
        ProcessInfo.processInfo.arguments.contains("-DemoLinkMap")
        #else
        false
        #endif
    }()

    private var sampleQuestion = Question()
    private var greetings: [String: String] = [:]
    private var wakes: [Wake] = []
    private var released: Set<String> = []
    private var autoRelease: [String: Bool] = [:]

    #if targetEnvironment(simulator)
    private func seedGitStatusDemo() {
        let fixtures: [(id: String, title: String, agent: String, offset: TimeInterval, add: UInt32, del: UInt32, files: UInt32, git: GitStatus)] = [
            (
                "demo-git-pushed",
                "Git Pushed Task",
                "claude",
                -30,
                42,
                8,
                3,
                .with {
                    $0.state = .pushed
                    $0.branch = "hermesh/pushed"
                    $0.targetRef = "origin/hermesh/pushed"
                    $0.headCommit = "7b2e91f"
                    $0.headSubject = "feat(sidebar): sync pushed git mark"
                }
            ),
            (
                "demo-git-merged",
                "Git Merged Task",
                "codex",
                -60,
                128,
                16,
                5,
                .with {
                    $0.state = .merged
                    $0.branch = "hermesh/merged"
                    $0.targetRef = "main"
                    $0.headCommit = "473868e"
                    $0.headSubject = "feat(vcs): detect merged branch into main"
                }
            ),
            (
                "demo-git-unpushed",
                "Git Unpushed Task",
                "claude",
                -90,
                34,
                12,
                2,
                .with {
                    $0.state = .unpushed
                    $0.branch = "hermesh/unpushed"
                    $0.targetRef = "main"
                    $0.commitsAhead = 2
                    $0.headCommit = "c19d40a"
                    $0.headSubject = "refactor: extract git status helpers"
                }
            ),
            (
                "demo-git-dirty",
                "Git Dirty Task — Refactor sidebar delivery perception and long-press status card",
                "codex",
                -120,
                19,
                5,
                3,
                .with {
                    $0.state = .dirty
                    $0.branch = "hermesh/dirty"
                    $0.targetRef = "main"
                    $0.dirtyFiles = 3
                    $0.headCommit = "8e01f5b"
                    $0.headSubject = "wip: draft preview layout"
                }
            ),
            (
                "demo-git-clean",
                "Git Clean Task",
                "gemini",
                -150,
                0,
                0,
                0,
                .with {
                    $0.state = .clean
                    $0.branch = "hermesh/clean"
                    $0.targetRef = "main"
                }
            ),
        ]
        for f in fixtures {
            var s = session(f.id, title: f.title, machine: "demo-mac", status: .idle)
            s.agent = f.agent
            s.branch = f.git.branch
            s.updatedAt = .init(date: .now.addingTimeInterval(f.offset))
            s.progress.diffStat = .with {
                $0.filesChanged = f.files
                $0.insertions = f.add
                $0.deletions = f.del
            }
            s.gitStatus = f.git
            world.sessions.append(s)
            syncSession(s.sessionID)
        }
    }
    #endif

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
        let rpc = method.split(separator: "/").last.map(String.init) ?? method
        let r = try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
        func string(_ key: String) -> String { r[key] as? String ?? "" }
        func encoded(_ message: some SwiftProtobuf.Message) throws -> Data { try message.jsonUTF8Data() }
        func object(_ value: [String: Any] = [:]) throws -> Data { try JSONSerialization.data(withJSONObject: value) }
        var dec = JSONDecodingOptions(); dec.ignoreUnknownFields = true
        let id = string("sessionId")
        #if DEBUG
        if Self.herDemoOn, let answer = try herDemo(rpc, r) { return answer }
        #endif
        #if targetEnvironment(simulator)
        if Self.perfSuite, let answer = try perfSuiteCall(rpc, data: data, id: id) { return answer }
        #endif
        switch rpc {
        case "GetStatus": return try encoded(world.status)
        case "ListMachines": return try encoded(Hermesh_Local_V1_ListMachinesResponse.with { $0.machines = world.machines })
        case "ListClients": return try encoded(Hermesh_Local_V1_ListClientsResponse.with { $0.clients = world.clients })
        case "ListDevices": return try encoded(Hermesh_Local_V1_ListDevicesResponse.with { $0.devices = world.devices })
        case "ListProjects": return try encoded(Hermesh_Local_V1_ListProjectsResponse.with {
            $0.projects = world.projects.filter { string("machineId").isEmpty || $0.machineID == string("machineId") }
        })
        case "ListPairingRequests": return try encoded(Hermesh_Local_V1_ListPairingRequestsResponse.with { $0.pairings = world.pairings })
        case "ListSessions": return try encoded(Hermesh_Local_V1_ListSessionsResponse.with { $0.sessions = world.sessions })
        case "RefreshGitStatus":
            let req = (try? Hermesh_Session_V1_RefreshGitStatusRequest(jsonUTF8Data: data, options: dec)) ?? .init()
            let wanted = Set(req.sessionIds)
            let matched = world.sessions.filter {
                (wanted.isEmpty || wanted.contains($0.sessionID)) && (req.machineID.isEmpty || $0.machineID == req.machineID)
            }
            return try encoded(Hermesh_Session_V1_RefreshGitStatusResponse.with { $0.sessions = matched })
        case "ListProviders":
            return try encoded(Hermesh_Session_V1_ListProvidersResponse.with {
                $0.maxRuntimeMode = .fullAccess
                $0.providers = ["claude", "codex"].map { pid in
                    .with {
                        $0.instanceID = pid; $0.driver = pid; $0.displayName = pid == "claude" ? "Claude Code" : "Codex"
                        $0.enabled = true; $0.installed = true; $0.authenticated = true
                        $0.models = [.with { $0.slug = "demo-model"; $0.name = "Demo Model"; $0.shortName = "Demo Model"; $0.isDefault = true }]
                    }
                }
            })
        case "GetSession":
            let req = (try? Hermesh_Session_V1_GetSessionRequest(jsonUTF8Data: data, options: dec)) ?? .init()
            if req.cachedOnly {
                return try encoded(Hermesh_Session_V1_GetSessionResponse())
            }
            guard let s = world.sessions.first(where: { $0.sessionID == id }) else {
                throw APIError(code: "not_found", message: "session not found")
            }
            #if targetEnvironment(simulator)
            if Self.sessionDetailRegression, id == "demo-delayed-history" {
                var empty = Hermesh_Session_V1_SessionProjection()
                empty.session = s
                empty.session.progress.lastEventSeq = 0
                return try encoded(Hermesh_Session_V1_GetSessionResponse.with { $0.projection = empty })
            }
            #endif
            var p = projections[id] ?? .init()
            p.session = s
            return try encoded(Hermesh_Session_V1_GetSessionResponse.with { $0.projection = p })
        case "StartSession":
            let req = try Hermesh_Session_V1_StartSessionRequest(jsonUTF8Data: data, options: dec)
            let newID = "demo-started-" + UUID().uuidString.prefix(8)
            var s = session(String(newID), title: req.title.isEmpty ? String(req.initialPrompt.prefix(40)) : req.title, machine: req.machineID.isEmpty ? "demo-mac" : req.machineID, status: .idle)
            s.projectID = req.projectID.isEmpty ? "hello-world" : req.projectID
            s.createdBy = "owner"
            if req.hasModelSelection { s.modelSelection = req.modelSelection; s.agent = req.modelSelection.instanceID }
            world.sessions.insert(s, at: 0)
            beginRun(s.sessionID, runID: "r1", ordinal: 1, asker: "owner", status: .completed)
            appendItem(s.sessionID, runID: "r1", kind: .userMessage) { $0.actor = .with { $0.kind = .owner; $0.id = "owner" }; $0.text = req.initialPrompt }
            appendItem(s.sessionID, runID: "r1", kind: .assistantMessage) { $0.text = String(localized: "Simulated session response in Demo Mode.") }
            publish()
            return try encoded(Hermesh_Session_V1_StartSessionResponse.with { $0.session = world.sessions[0] })
        case "DispatchMessage":
            let req = try Hermesh_Session_V1_DispatchMessageRequest(jsonUTF8Data: data, options: dec)
            guard let i = world.sessions.firstIndex(where: { $0.sessionID == req.sessionID }) else { throw CancellationError() }
            if req.intent == .queue, [.preparing, .running, .waitingInput].contains(world.sessions[i].status), !world.sessions[i].activeRunID.isEmpty {
                var p = projections[req.sessionID, default: .init()]
                let qRun: Hermesh_Session_V1_Run = .with {
                    $0.runID = "q_\(UUID().uuidString.prefix(8))"
                    $0.sessionID = req.sessionID
                    $0.status = .queued
                    $0.queueOrder = Double((p.runs.count + 1) * 1000)
                    $0.author = .with { $0.kind = .owner; $0.id = "owner" }
                    $0.prompt = req.text
                    $0.attachments = req.attachments
                    $0.createdAt = .init(date: .now)
                }
                p.runs.append(qRun)
                world.sessions[i].queuedRunCount += 1
                world.sessions[i].updatedAt = .init(date: .now)
                p.session = world.sessions[i]
                projections[req.sessionID] = p
                emitEvent(req.sessionID) { $0.type = "queue.updated"; $0.run = qRun }
                publish()
                return try encoded(Hermesh_Session_V1_DispatchMessageResponse.with { $0.session = world.sessions[i]; $0.run = qRun })
            }
            let ord = UInt32((projections[req.sessionID]?.runs.count ?? 0) + 1)
            let rid = "r\(ord)"
            beginRun(req.sessionID, runID: rid, ordinal: ord, asker: "owner", status: .completed)
            appendItem(req.sessionID, runID: rid, kind: .userMessage) { $0.actor = .with { $0.kind = .owner; $0.id = "owner" }; $0.text = req.text }
            appendItem(req.sessionID, runID: rid, kind: .assistantMessage) { $0.text = String(localized: "Simulated follow-up reply in Demo Mode.") }
            world.sessions[i].progress.turnCount = ord
            world.sessions[i].updatedAt = .init(date: .now)
            syncSession(req.sessionID)
            publish()
            return try encoded(Hermesh_Session_V1_DispatchMessageResponse.with { $0.session = world.sessions[i] })
        case "ManageQueue":
            let req = try Hermesh_Session_V1_ManageQueueRequest(jsonUTF8Data: data, options: dec)
            guard let i = world.sessions.firstIndex(where: { $0.sessionID == req.sessionID }) else { throw CancellationError() }
            var p = projections[req.sessionID, default: .init()]
            guard let rIdx = p.runs.firstIndex(where: { $0.runID == req.runID && $0.status == .queued }) else {
                throw APIError(code: "failed_precondition", message: "run is no longer queued")
            }
            switch req.operation {
            case .promoteToSteer:
                var qRun = p.runs[rIdx]
                qRun.status = .cancelled
                qRun.completedAt = .init(date: .now)
                p.runs[rIdx] = qRun
                projections[req.sessionID] = p
                let activeRunID = world.sessions[i].activeRunID.isEmpty ? "r1" : world.sessions[i].activeRunID
                let item = appendItem(req.sessionID, runID: activeRunID, kind: .userMessage) {
                    $0.status = "promoted_to_steer"
                    $0.actor = qRun.author
                    $0.text = qRun.prompt
                    $0.attachments = qRun.attachments
                }
                p = projections[req.sessionID, default: .init()]
                let rem = p.runs.filter { $0.status == .queued }
                world.sessions[i].queuedRunCount = UInt32(rem.count)
                world.sessions[i].updatedAt = .init(date: .now)
                syncSession(req.sessionID)
                emitEvent(req.sessionID) { $0.type = "queue.promoted"; $0.run = qRun; $0.item = item }
                publish()
                return try encoded(Hermesh_Session_V1_ManageQueueResponse.with { $0.session = world.sessions[i]; $0.queuedRuns = rem })
            case .cancel:
                p.runs[rIdx].status = .cancelled
                p.runs[rIdx].completedAt = .init(date: .now)
                let cancelled = p.runs[rIdx]
                let rem = p.runs.filter { $0.status == .queued }
                world.sessions[i].queuedRunCount = UInt32(rem.count)
                world.sessions[i].updatedAt = .init(date: .now)
                p.session = world.sessions[i]
                projections[req.sessionID] = p
                emitEvent(req.sessionID) { $0.type = "queue.updated"; $0.run = cancelled }
                publish()
                return try encoded(Hermesh_Session_V1_ManageQueueResponse.with { $0.session = world.sessions[i]; $0.queuedRuns = rem })
            default:
                let rem = p.runs.filter { $0.status == .queued }
                return try encoded(Hermesh_Session_V1_ManageQueueResponse.with { $0.session = world.sessions[i]; $0.queuedRuns = rem })
            }
        case "LookupPairing":
            guard let p = world.pairings.first(where: { $0.code == string("code") }) else { throw APIError(code: "not_found", message: String(localized: "Use Add Machine or Connect Agent to create a demo request.")) }
            return try encoded(Hermesh_Local_V1_LookupPairingResponse.with { $0.details = p })
        case "RespondRuntimeRequest":
            let req = try Hermesh_Session_V1_RespondRuntimeRequestRequest(jsonUTF8Data: data, options: dec)
            guard let i = world.sessions.firstIndex(where: { $0.sessionID == req.sessionID }),
                  world.sessions[i].status == .waitingInput else { throw CancellationError() }
            let declined = req.decline || req.decision == .decline
            var greeting = "Hello, world!"
            world.sessions[i].progress.clearPendingQuestion()
            world.sessions[i].pendingRequestID = ""
            world.sessions[i].status = .running
            world.sessions[i].updatedAt = .init(date: .now)
            syncSession(req.sessionID)
            if let rIdx = projections[req.sessionID]?.requests.firstIndex(where: { $0.requestID == req.requestID || $0.status == .pending }) {
                let kind = projections[req.sessionID]!.requests[rIdx].kind
                projections[req.sessionID]?.requests[rIdx].status = .resolved
                if kind == .planReview {
                    let approved = req.approvePlan
                    projections[req.sessionID]?.requests[rIdx].resolvedDecision = approved ? .acceptOnce : .decline
                    projections[req.sessionID]?.requests[rIdx].resolvedFeedback = req.feedback
                    if approved {
                        world.sessions[i].interactionMode = .code
                        syncSession(req.sessionID)
                    }
                } else {
                    projections[req.sessionID]?.requests[rIdx].resolvedDecision = declined ? .decline : .acceptOnce
                    projections[req.sessionID]?.requests[rIdx].questionResolution = declined ? .declined : .answered
                    projections[req.sessionID]?.requests[rIdx].resolvedAnswers = req.answers
                }
                projections[req.sessionID]?.requests[rIdx].resolvedBy = .with { $0.kind = .owner; $0.id = "owner" }
                if let firstAns = req.answers["greeting"] ?? req.answers.values.first {
                    let custom = firstAns.customText
                    greeting = firstAns.selectedOptions.first ?? (custom.isEmpty ? "Hello, world!" : custom)
                    if req.answers.count > 1 {
                        let summary = ["greeting", "style", "checks"].compactMap { key -> String? in
                            guard let a = req.answers[key] else { return nil }
                            return a.selectedOptions.first ?? (a.customText.isEmpty ? nil : a.customText)
                        }.joined(separator: " / ")
                        if !summary.isEmpty { greeting = summary }
                    }
                }
                let updatedReq = projections[req.sessionID]!.requests[rIdx]
                emitEvent(req.sessionID) { $0.type = "request.resolved"; $0.request = updatedReq }
            }
            publish()
            if !Self.holdWorkTaskForUITest {
                let sid = req.sessionID
                tasks[sid] = Task { [weak self] in
                    try? await Task.sleep(for: .seconds(8))
                    guard !Task.isCancelled else { return }
                    self?.finish(sid, greeting: greeting)
                    self?.tasks[sid] = nil
                }
            }
            return try encoded(Hermesh_Session_V1_RespondRuntimeRequestResponse.with { $0.session = world.sessions[i] })
        case "InterruptSession":
            guard let i = world.sessions.firstIndex(where: { $0.sessionID == id }) else { throw CancellationError() }
            stopTask(id)
            publish()
            return try encoded(Hermesh_Session_V1_InterruptSessionResponse.with { $0.session = world.sessions[i] })
        case "StopBackgroundTask":
            let req = try Hermesh_Session_V1_StopBackgroundTaskRequest(jsonUTF8Data: data, options: dec)
            guard let i = world.sessions.firstIndex(where: { $0.sessionID == req.sessionID }) else { throw CancellationError() }
            var stopped: [Hermesh_Session_V1_BackgroundTask] = []
            if var p = projections[req.sessionID] {
                for idx in p.backgroundTasks.indices where p.backgroundTasks[idx].status == .running && (req.taskID.isEmpty || p.backgroundTasks[idx].taskID == req.taskID) {
                    p.backgroundTasks[idx].status = .stopped
                    p.backgroundTasks[idx].completedAt = .init(date: .now)
                    stopped.append(p.backgroundTasks[idx])
                }
                world.sessions[i].activeBackgroundCount = UInt32(p.backgroundTasks.filter { $0.status == .running && $0.awaited }.count)
                world.sessions[i].activeDaemonCount = UInt32(p.backgroundTasks.filter { $0.status == .running && !$0.awaited }.count)
                p.session = world.sessions[i]
                projections[req.sessionID] = p
            }
            publish()
            return try encoded(Hermesh_Session_V1_StopBackgroundTaskResponse.with { $0.session = world.sessions[i]; $0.stoppedTasks = stopped })
        case "UpdateSession":
            let req = try Hermesh_Session_V1_UpdateSessionRequest(jsonUTF8Data: data, options: dec)
            guard let i = world.sessions.firstIndex(where: { $0.sessionID == req.sessionID }) else { throw CancellationError() }
            let pinOnly = req.hasPinned_p && req.title.isEmpty && !req.hasArchived_p && !req.hasModelSelection && req.runtimeMode == .unspecified && req.interactionMode == .unspecified
            if !req.title.isEmpty { world.sessions[i].title = req.title }
            if req.hasPinned_p { world.sessions[i].pinned = req.pinned }
            if req.hasArchived_p { world.sessions[i].status = req.archived ? .archived : .idle }
            if req.hasModelSelection { world.sessions[i].modelSelection = req.modelSelection }
            if req.runtimeMode != .unspecified { world.sessions[i].runtimeMode = req.runtimeMode }
            if req.interactionMode != .unspecified { world.sessions[i].interactionMode = req.interactionMode }
            if !pinOnly { world.sessions[i].updatedAt = .init(date: .now) }
            syncSession(req.sessionID)
            emitEvent(req.sessionID) { $0.type = "session.updated" }
            publish()
            return try encoded(Hermesh_Session_V1_UpdateSessionResponse.with { $0.session = world.sessions[i] })
        case "DeleteSession":
            if id == HerReviewDemo.codingID { herReview.interrupt() }
            tasks.removeValue(forKey: id)?.cancel()
            world.sessions.removeAll { $0.sessionID == id }
            projections[id] = nil
            events[id] = nil
            world.deletedSessionIds = [id]; publish(); world.deletedSessionIds = []
            return try encoded(Hermesh_Session_V1_DeleteSessionResponse())
        case "GetDiff":
            guard let greeting = greetings[id] else { return try object() }
            let literal = String(decoding: try JSONEncoder().encode(greeting), as: UTF8.self)
            return try encoded(Hermesh_Session_V1_GetDiffResponse.with {
                $0.diff = "diff --git a/src/welcome.ts b/src/welcome.ts\n--- a/src/welcome.ts\n+++ b/src/welcome.ts\n@@ -1 +1 @@\n-export const greeting = \"Hi\";\n+export const greeting = \(literal);\n"
            })
        case "ApproveClientPairing", "ApproveMachinePairing", "ApproveDevicePairing":
            guard let p = world.pairings.first(where: { $0.code == string("code") }) else { throw CancellationError() }
            world.pairings.removeAll { $0.code == p.code }
            if rpc == "ApproveClientPairing" {
                let request = try Hermesh_Local_V1_ApproveClientPairingRequest(jsonUTF8Data: data, options: dec)
                world.clients.removeAll { $0.clientID == p.subjectID }
                world.clients.append(.with {
                    $0.clientID = p.subjectID; $0.grantID = p.code; $0.label = request.spec.label; $0.spec = request.spec
                    $0.online = true; $0.grantVersion = 1; $0.expiresAt = .init(date: .now.addingTimeInterval(Double(request.spec.ttlDays) * 86400))
                })
                publish()
                return try object(["clientId": p.subjectID, "grantId": p.code])
            } else if rpc == "ApproveMachinePairing" {
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
                if rpc == "UpdateClient" {
                    let request = try Hermesh_Local_V1_UpdateClientRequest(jsonUTF8Data: data, options: dec)
                    world.clients[i].spec = request.spec
                    if request.spec.ttlDays > 0 {
                        world.clients[i].expiresAt = .init(date: .now.addingTimeInterval(Double(request.spec.ttlDays) * 86400))
                    }
                } else if rpc == "RenameClient" { world.clients[i].label = string("label") }
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
                switch rpc {
                case "RenameMachine": world.machines[i].machineName = string("machineName")
                case "SetMachinePaused": world.machines[i].status.paused = r["paused"] as? Bool ?? false
                case "SetAgentDefaults":
                    let d = try Hermesh_Local_V1_SetAgentDefaultsRequest(jsonUTF8Data: data, options: dec).defaults
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
            let request = try Hermesh_Local_V1_AddProjectRequest(jsonUTF8Data: data, options: dec)
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
            let request = try Hermesh_Local_V1_ReleaseWorkspacesRequest(jsonUTF8Data: data, options: dec)
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
            return try encoded(queryWork(try Hermesh_Local_V1_QueryWorkRequest(jsonUTF8Data: data, options: dec)))
        case "ListWakes": return try encoded(Hermesh_Local_V1_ListWakesResponse.with { $0.wakes = wakes.filter { $0.clientID == string("clientId") } })
        case "SetWake", "CheckWake":
            let client = string("clientId"), name = string("name")
            var wake = wakes.first { $0.clientID == client && $0.name == name } ?? Wake()
            wake.clientID = client; wake.name = name
            if rpc == "SetWake" { wake.url = string("url") }
            wake.state = "on"; wake.lastStatus = 200; wake.lastRingAt = .init(date: .now)
            wake.recent.insert(.with { $0.at = .init(date: .now); $0.check = true; $0.status = 200 }, at: 0)
            wakes.removeAll { $0.clientID == client && $0.name == name }; wakes.append(wake)
            return try encoded(Hermesh_Local_V1_SetWakeResponse.with { $0.wake = wake; $0.check = .with { $0.ok = true; $0.status = 200 } })
        case "ClearWake": wakes.removeAll { $0.clientID == string("clientId") && $0.name == string("name") }
        case "GetHerConversation":
            return try encoded(Hermesh_Local_V1_GetHerConversationResponse.with {
                $0.bot = herReview.keptBot; $0.snapshot = herReview.keptSnapshot
            })
        case "HerSend":
            let request = try Hermesh_Local_V1_HerSendRequest(jsonUTF8Data: data, options: dec)
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

    private func updateHerTask() {
        let id = HerReviewDemo.codingID
        switch herReview.codingState {
        case .running:
            world.sessions.removeAll { $0.sessionID == id }
            var s = session(id, title: "Her sample task", machine: "demo-linux", status: .running)
            s.createdBy = "her"
            s.activeRunID = "r1"
            // Her's own grant, as a workspace with Her has it: the task she sends says it is hers, by her face.
            if !world.clients.contains(where: { $0.clientID == "her" }) {
                world.clients.append(.with {
                    $0.clientID = "her"; $0.grantID = "demo-her-grant"; $0.label = "Her"; $0.online = true
                    $0.herMachineID = "demo-mac"; $0.grantVersion = 1
                    $0.spec = .with { $0.label = "Her"; $0.machineIds = ["*"]; $0.projectIds = ["*"]; $0.agents = ["*"] }
                })
            }
            world.sessions.append(s)
            projections[id] = .init()
            events[id] = []
            greetings[id] = nil
            beginRun(id, runID: "r1", ordinal: 1, asker: "her", status: .running)
            appendItem(id, runID: "r1", kind: .userMessage) {
                $0.actor = .with { $0.kind = .her; $0.id = "her" }
                $0.text = String(localized: "Update the sample welcome page. This task was delegated by Her; no code is executed.")
            }
        case .done:
            finish(id, greeting: "Welcome from Her!")
            return
        case .failed:
            guard let i = world.sessions.firstIndex(where: { $0.sessionID == id }) else { return }
            let message = String(localized: "The sample check failed. No code ran. Return to Her and retry the sample task.")
            world.sessions[i].status = .crashed
            world.sessions[i].activeRunID = ""
            world.sessions[i].error = message
            world.sessions[i].progress.lastFailure = .with { $0.message = message; $0.retryable = true }
            world.sessions[i].result = message
            appendItem(id, runID: "r1", kind: .assistantMessage) { $0.text = message }
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
        // As hermeshd answers: only what the filter keeps, the range's totals (and per bucket unless NONE), and with
        // previous the as many days before it.
        let f = r.filter
        for s in world.sessions where (f.client.isEmpty || f.client == s.createdBy) && (f.agent.isEmpty || f.agent == s.agent)
            && (f.machineID.isEmpty || f.machineID == s.machineID) && (f.projectID.isEmpty || f.projectID == s.projectID) {
            let day = calendar.dateComponents([.day], from: start, to: calendar.startOfDay(for: s.createdAt.date)).day ?? 0
            let before = r.previous && day < 0 && day >= -Int(r.days)
            guard day >= 0 && day < Int(r.days) || before else { continue }
            let bucket = UInt32(r.bucket == .hour ? day * 24 + calendar.component(.hour, from: s.createdAt.date) : max(day, 0))
            let group = Hermesh_Local_V1_WorkGroup.with {
                if r.groupBy.contains(.client) { $0.client = s.createdBy }
                if r.groupBy.contains(.agent) { $0.agent = s.agent }
                if r.groupBy.contains(.machine) { $0.machineID = s.machineID }
                if r.groupBy.contains(.project) { $0.projectID = s.projectID }
            }
            let done = s.status == .idle || s.status == .archived
            let count = Hermesh_Local_V1_SessionCount.with { $0.bucket = bucket; $0.group = group; $0.sessions = 1; $0.done = done ? 1 : 0 }
            let turn = Hermesh_Local_V1_TurnCount.with {
                $0.bucket = bucket; $0.group = group; $0.turns = 1
                $0.inputTokens = Self.bigUsage ? 23_456_789 : 1200; $0.outputTokens = Self.bigUsage ? 63_210 : 350; $0.busySeconds = Self.bigUsage ? 1_720 : 45
            }
            if before {
                if !r.turnsOnly { result.previousSessions.append(count) }
                if done { result.previousTurns.append(turn) }
                continue
            }
            if !r.turnsOnly {
                if r.bucket != .none { result.sessions.append(count) }
                result.rangeSessions.append(count)
            }
            if done {
                if r.bucket != .none { result.turns.append(turn) }
                result.rangeTurns.append(turn)
            }
        }
        result.sources = world.machines.map { m in .with { $0.machineID = m.machineID; $0.live = true; $0.syncedAt = .init(date: .now) } }
        return result
    }

    func stream(_ method: String, _ data: Data) throws -> AsyncThrowingStream<Data, Error> {
        let rpc = method.split(separator: "/").last.map(String.init) ?? method
        var dec = JSONDecodingOptions(); dec.ignoreUnknownFields = true
        let token = UUID()
        if rpc == "Watch" {
            return AsyncThrowingStream { c in
                watchers[token] = c
                do { c.yield(try world.jsonUTF8Data()) } catch { c.finish(throwing: error) }
                c.onTermination = { [weak self] _ in Task { @MainActor in self?.watchers[token] = nil } }
            }
        }
        if rpc == "SubscribeSession" {
            let request = try Hermesh_Session_V1_SubscribeSessionRequest(jsonUTF8Data: data, options: dec)
            return AsyncThrowingStream { c in
                followers[token] = (request.sessionID, c)
                #if targetEnvironment(simulator)
                if Self.sessionDetailRegression,
                   streamSessionDetailRegression(request: request, token: token, continuation: c) {
                    return
                }
                if Self.liveProgress, request.sessionID == "demo-live-progress" {
                    startLiveProgressCompletion()
                }
                if Self.liveThinking, request.sessionID == "demo-live-thinking" {
                    startLiveThinkingCompletion()
                }
                #endif
                do {
                    for e in events[request.sessionID, default: []] where e.seq > request.afterSeq { c.yield(try e.jsonUTF8Data()) }
                } catch { c.finish(throwing: error) }
                c.onTermination = { [weak self] _ in Task { @MainActor in self?.followers[token] = nil } }
            }
        }
        if rpc == "RunCommand" {
            let request = try Hermesh_Session_V1_RunCommandRequest(jsonUTF8Data: data, options: dec)
            guard world.sessions.contains(where: { $0.sessionID == request.sessionID }) else {
                throw APIError(code: "not_found", message: "session not found")
            }
            return streamDemoRunCommand(request)
        }
        throw APIError(code: "unimplemented", message: String(localized: "This action needs a real connection. Exit the demo to set up your own workspace."))
    }

    private func streamDemoRunCommand(_ request: Hermesh_Session_V1_RunCommandRequest) -> AsyncThrowingStream<Data, Error> {
        let cmd = request.command
        return AsyncThrowingStream { c in
            let job = Task {
                func emit(_ build: (inout Hermesh_Session_V1_RunCommandEvent) -> Void) throws {
                    var ev = Hermesh_Session_V1_RunCommandEvent()
                    build(&ev)
                    c.yield(try ev.jsonUTF8Data())
                }
                do {
                    if cmd.contains("sleep") {
                        try emit { $0.outputDelta = "starting step 1...\n" }
                        for _ in 0 ..< 50 {
                            try await Task.sleep(for: .milliseconds(100))
                            if Task.isCancelled { return }
                        }
                        try emit {
                            $0.done = true
                            $0.exitCode = 0
                            $0.durationMs = 5000
                        }
                        c.finish()
                        return
                    }
                    try await Task.sleep(for: .milliseconds(50))
                    if Task.isCancelled { return }
                    if cmd.contains("exit 1") {
                        try emit { $0.outputDelta = "\u{001B}[31mFAIL\u{001B}[0m check exited with status 1\n" }
                        try emit {
                            $0.done = true
                            $0.exitCode = 1
                            $0.durationMs = 65
                        }
                        c.finish()
                        return
                    }
                    try emit { $0.outputDelta = "\u{001B}[32m✓\u{001B}[0m workspace ready\n" }
                    try await Task.sleep(for: .milliseconds(50))
                    if Task.isCancelled { return }
                    let body = cmd.contains("rm -rf")
                        ? "cleaned ./dist\n"
                        : "README.md\nsrc/\npackage.json\n"
                    try emit { $0.outputDelta = body }
                    try emit {
                        $0.done = true
                        $0.exitCode = 0
                        $0.durationMs = 110
                    }
                    c.finish()
                } catch is CancellationError {
                    c.finish()
                } catch {
                    c.finish(throwing: error)
                }
            }
            c.onTermination = { _ in job.cancel() }
        }
    }

    #if targetEnvironment(simulator)
    /// Two sessions of one turn of many tool calls in a row, nothing said between them (one folded group): "Many steps",
    /// done, of 600 calls with outputs of a line to a page; "Many steps, running", which adds a call a second.
    private func seedManyTools() {
        @discardableResult
        func call(_ id: String, runID: String = "r1", _ i: Int, result: Bool) -> Hermesh_Session_V1_TurnItem {
            let kinds = [("read", "Read", "internal/pkg\(i % 40)/file\(i % 90).go"), ("search", "Grep", "log.Printf"),
                         ("edit", "Edit", "internal/pkg\(i % 40)/file\((i * 7) % 90).go"), ("shell", "Bash", "go test ./internal/pkg\(i % 40)/... -run Test\(i) -count=1")]
            let (kind, name, summary) = kinds[(i * 7919) % 4]
            let isError = result && (i % 37 == 11)
            let lines = 1 + (i * 37) % 60
            let output = result ? (1 ... lines).map { "\($0)\tlog.Printf(\"handled %d of %s\", n, \(i * 1000 + $0))" }.joined(separator: "\n") : ""
            return appendItem(id, runID: runID, kind: kind == "shell" ? .commandExecution : .toolCall) {
                $0.itemID = "\(id)-\(i)"
                $0.toolName = name
                if kind == "shell" {
                    $0.command = summary
                    $0.input = "{\n  \"command\": \"\(summary)\"\n}"
                    $0.exitCode = isError ? 1 : 0
                } else if kind == "search" {
                    $0.text = summary
                    $0.input = "{\n  \"pattern\": \"\(summary)\"\n}"
                } else {
                    $0.filePath = summary
                    $0.input = "{\n  \"path\": \"\(summary)\"\n}"
                }
                $0.status = result ? (isError ? "failed" : "completed") : "running"
                $0.output = output
            }
        }
        func start(_ id: String, title: String, status: SessionStatus) {
            var s = session(id, title: title, machine: "demo-mac", status: status)
            s.agent = "codex"
            s.createdBy = "owner"
            s.updatedAt = .init(date: .now.addingTimeInterval(40))
            s.progress.turnCount = status == .running ? 0 : 1
            if status == .running {
                s.activeRunID = "r1"
                s.progress.turnStartedAt = .init(date: .now)
            }
            world.sessions.append(s)
            beginRun(id, runID: "r1", ordinal: 1, asker: "owner", status: status == .running ? .running : .completed)
            appendItem(id, runID: "r1", kind: .userMessage) {
                $0.actor = .with { $0.kind = .owner; $0.id = "owner" }
                $0.text = "Switch every log call in the repository to structured logging, and keep the tests green."
            }
            appendItem(id, runID: "r1", kind: .assistantMessage) {
                $0.text = "Sure. First I'll find every call, then change them one by one."
                // The running one's page is longer than the screen: for scrolling it while steps come.
                if status == .running {
                    $0.text += "\n\n" + (1 ... 24).map { "\($0). Package \($0): read its log calls, switch them to slog, run its tests." }.joined(separator: "\n")
                }
            }
        }
        start("demo-many-steps", title: "Many steps", status: .idle)
        for i in 0 ..< 600 { call("demo-many-steps", i, result: true) }
        appendItem("demo-many-steps", runID: "r1", kind: .assistantMessage) {
            $0.text = "Done: after 600 steps every call uses slog, and the tests pass."
        }
        syncSession("demo-many-steps")

        // A long conversation of small groups (three calls a turn, but 26 in the eleventh: a card that stops growing and
        // scrolls inside) under a long title: for scrolling the page from on a card, and for the bar's capsule.
        let small = "demo-small-steps"
        var s = session(small, title: "Small steps: keep every log call structured across the whole repository and its tests", machine: "demo-mac", status: .idle)
        s.agent = "codex"
        s.createdBy = "owner"
        s.updatedAt = .init(date: .now.addingTimeInterval(40))
        s.progress.turnCount = 12
        world.sessions.append(s)
        for t in 1 ... 12 {
            let rid = "r\(t)"
            beginRun(small, runID: rid, ordinal: UInt32(t), asker: "owner", status: .completed)
            appendItem(small, runID: rid, kind: .userMessage) {
                $0.actor = .with { $0.kind = .owner; $0.id = "owner" }
                $0.text = "Small prompt #\(t): look at package \(t) and tell me what its logging does."
            }
            if t == 11 {
                for i in 500 ..< 526 { call(small, runID: rid, i, result: true) }
            } else {
                for k in 0 ..< 3 { call(small, runID: rid, t * 10 + k, result: true) }
            }
            appendItem(small, runID: rid, kind: .assistantMessage) {
                $0.text = "Small reply #\(t): package \(t) logs through the shared logger; nothing to change there. Its tests cover the three call sites I read."
            }
        }
        syncSession(small)

        start("demo-many-steps-running", title: "Many steps, running", status: .running)
        for i in 0 ..< 80 { call("demo-many-steps-running", i, result: true) }
        call("demo-many-steps-running", 80, result: false)
        tasks["demo-many-steps-running"] = Task { @MainActor [weak self] in
            var i = 80
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(1))
                guard let self, !Task.isCancelled else { return }
                // The call under way finishes and the next starts.
                let itemID = "demo-many-steps-running-\(i)"
                if let idx = self.projections["demo-many-steps-running"]?.items.firstIndex(where: { $0.itemID == itemID }) {
                    var doneItem = self.projections["demo-many-steps-running"]!.items[idx]
                    doneItem.status = "completed"
                    doneItem.output = "ok"
                    doneItem.updatedAt = .init(date: .now)
                    self.projections["demo-many-steps-running"]?.items[idx] = doneItem
                    self.emitEvent("demo-many-steps-running") { $0.type = "item.updated"; $0.item = doneItem }
                }
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
            if status == .running {
                s.activeRunID = "r\(turns)"
                s.progress.turnStartedAt = .init(date: now)
            }
            world.sessions.append(s)

            for t in 1 ... turns {
                let runID = "r\(t)"
                let runningTurn = status == .running && t == turns
                beginRun(id, runID: runID, ordinal: UInt32(t), asker: "demo-client", status: runningTurn ? .running : .completed, startedAt: now.addingTimeInterval(Double(-turns + t)))
                appendItem(id, runID: runID, kind: .userMessage) {
                    $0.actor = .with { $0.kind = .externalAgent; $0.id = "demo-client" }
                    $0.text = "\(prefix) prompt #\(t): inspect module \(t) and report the current status."
                }
                let marker = t == 1 ? "\(prefix) start #1" : (t == turns ? "\(prefix) latest #\(turns)" : "\(prefix) item #\(t)")
                appendItem(id, runID: runID, kind: .assistantMessage) {
                    $0.text = """
                    \(marker): completed check for step \(t).

                    Reviewed the implementation details and verified layout spacing, event ordering, and scrolling behavior across paragraphs for entry \(t).
                    """
                }
            }
        }

        addHistory(id: "demo-long-history", title: "Long history session", prefix: "Long", turns: 24, status: .idle, updatedOffset: 20)
        addHistory(id: "demo-delayed-history", title: "Delayed history session", prefix: "Delayed", turns: 24, status: .idle, updatedOffset: 10)
        addHistory(id: "demo-streaming-session", title: "Streaming session", prefix: "Stream", turns: 20, status: .running, updatedOffset: 30)
    }

    private func seedCodeBlockRunner() {
        let id = "demo-codeblock-runner"
        var s = session(id, title: "Code block runner session", machine: "demo-mac", status: .idle)
        s.agent = "claude"
        s.createdBy = "owner"
        s.updatedAt = .init(date: .now.addingTimeInterval(50))
        s.progress.turnCount = 4
        world.sessions.insert(s, at: 0)

        let turns: [(prompt: String, reply: String)] = [
            (
                "Give me a multi-line script to list workspace files.",
                """
                Here is a multi-line script to list workspace files:

                ```bash
                #!/usr/bin/env bash
                # List workspace entries
                for entry in README.md src package.json; do
                  echo "$entry"
                done
                ```
                """
            ),
            (
                "Give me a long-running check command.",
                """
                Long-running check:

                ```bash
                echo "starting..." && sleep 5
                ```
                """
            ),
            (
                "Give me a command to clean dist.",
                """
                Destructive cleanup:

                ```bash
                rm -rf ./dist && echo "cleaned"
                ```
                """
            ),
            (
                "Show an example curl command with an API key.",
                """
                Masked secret check:

                ```bash
                OPENAI_API_KEY=sk-proj-1234567890abcdefghij curl -fsSL https://api.example.com/v1/models
                ```
                """
            ),
        ]

        for (idx, t) in turns.enumerated() {
            let ord = UInt32(idx + 1)
            let rid = "r\(ord)"
            beginRun(id, runID: rid, ordinal: ord, asker: "owner", status: .completed)
            appendItem(id, runID: rid, kind: .userMessage) {
                $0.actor = .with { $0.kind = .owner; $0.id = "owner" }
                $0.text = t.prompt
            }
            appendItem(id, runID: rid, kind: .assistantMessage) {
                $0.text = t.reply
            }
        }
        syncSession(id)
    }

    private func streamSessionDetailRegression(
        request: Hermesh_Session_V1_SubscribeSessionRequest,
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
                    for n in 1... {
                        try? await Task.sleep(for: .milliseconds(350))
                        guard !Task.isCancelled, let self else { return }
                        let draftItem = self.appendItem(id, runID: "r20", kind: .assistantMessage) {
                            $0.streaming = true
                            $0.status = "running"
                            $0.text = "Live draft #\(n): streaming incremental analysis line 1.\n\nContinuing with additional context for chunk \(n)."
                        }
                        try? await Task.sleep(for: .milliseconds(250))
                        guard !Task.isCancelled else { return }
                        var committed = draftItem
                        committed.streaming = false
                        committed.status = "completed"
                        committed.text = """
                        Live update #\(n): committed streamed message body for step \(n).

                        This paragraph grows the bottom of the session while verifying auto-follow and scroll-up stability.
                        """
                        if let idx = self.projections[id]?.items.firstIndex(where: { $0.itemID == committed.itemID }) {
                            self.projections[id]?.items[idx] = committed
                        }
                        self.emitEvent(id) { $0.type = "item.updated"; $0.item = committed }
                    }
                }
            }
            return true
        }
        return false
    }

    private func seedLiveProgress() {
        let id = "demo-live-progress"
        var s = session(id, title: "Live progress session", machine: "demo-mac", status: .running)
        s.agent = "antigravity"
        s.createdBy = "owner"
        s.activeRunID = "r2"
        s.updatedAt = .init(date: .now.addingTimeInterval(50))
        s.progress.turnCount = 1
        s.progress.turnStartedAt = .init(date: .now)
        world.sessions.append(s)

        beginRun(id, runID: "r1", ordinal: 1, asker: "owner", status: .completed, startedAt: .now.addingTimeInterval(-20))
        appendItem(id, runID: "r1", kind: .userMessage) {
            $0.actor = .with { $0.kind = .owner; $0.id = "owner" }
            $0.text = "Optimize sidebar halo GPU usage"
        }
        appendItem(id, runID: "r1", kind: .todoList) {
            $0.itemID = "r1:todo:todo"
            $0.todoSteps = [
                .with { $0.text = "Check motion constraints"; $0.status = "completed" },
                .with { $0.text = "Audit main-thread layout"; $0.status = "completed" },
                .with { $0.text = "Rewrite CALayer halo"; $0.status = "in_progress" },
                .with { $0.text = "Verify GPU usage"; $0.status = "pending" },
                .with { $0.text = "Update perf docs"; $0.status = "pending" },
            ]
        }
        appendItem(id, runID: "r1", kind: .toolCall) {
            $0.itemID = "r1:call_legacy_steps"
            $0.toolName = "Run update_steps"
            $0.status = "completed"
            $0.input = #"{"steps":[{"title":"Check motion constraints","status":"done"},{"title":"Audit main-thread layout","status":"done"},{"title":"Rewrite CALayer halo","status":"in_progress"},{"title":"Verify GPU usage","status":"pending"},{"title":"Update perf docs","status":"pending"}]}"#
        }
        appendItem(id, runID: "r1", kind: .assistantMessage) {
            $0.text = "Running GPU benchmark in background."
        }

        let bt: Hermesh_Session_V1_BackgroundTask = .with {
            $0.taskID = "bg-bench"
            $0.sessionID = id
            $0.runID = "r1"
            $0.kind = .command
            $0.status = .completed
            $0.title = "Benchmark CALayer sprite orbit"
            $0.command = "python3 bench.py"
            $0.outputTail = "GPU dropped from 24.9% to 2.7%"
            $0.awaited = true
            $0.startedAt = .init(date: .now.addingTimeInterval(-15))
            $0.completedAt = .init(date: .now.addingTimeInterval(-2))
        }
        projections[id, default: .init()].backgroundTasks.append(bt)

        let contRun: Hermesh_Session_V1_Run = .with {
            $0.runID = "r2"
            $0.sessionID = id
            $0.ordinal = 2
            $0.status = .running
            $0.author = .with { $0.kind = .system; $0.id = "background" }
            $0.wakeReason = "Benchmark CALayer sprite orbit"
            $0.createdAt = .init(date: .now)
            $0.startedAt = .init(date: .now)
        }
        projections[id, default: .init()].runs.append(contRun)
        emitEvent(id) { $0.type = "run.started"; $0.run = contRun }
        appendItem(id, runID: "r2", kind: .commandExecution) {
            $0.itemID = "r2:cmd:1"
            $0.text = "Run HermeshKit tests and iOS Simulator UI tests"
            $0.command = "swift test"
            $0.status = "running"
        }
        syncSession(id)
    }

    private func startLiveProgressCompletion() {
        let id = "demo-live-progress"
        guard tasks[id] == nil else { return }
        tasks[id] = Task { @MainActor [weak self] in
            try? await Task.sleep(for: .milliseconds(12000))
            guard !Task.isCancelled, let self,
                  let sIdx = self.world.sessions.firstIndex(where: { $0.sessionID == id }) else { return }
            if let cmdIdx = self.projections[id]?.items.firstIndex(where: { $0.itemID == "r2:cmd:1" }) {
                var doneCmd = self.projections[id]!.items[cmdIdx]
                doneCmd.status = "completed"
                doneCmd.output = "All tests passed"
                self.projections[id]?.items[cmdIdx] = doneCmd
                self.emitEvent(id) { $0.type = "item.updated"; $0.item = doneCmd }
            }
            self.appendItem(id, runID: "r2", kind: .todoList) {
                $0.itemID = "r2:todo:todo"
                $0.todoSteps = [
                    .with { $0.text = "Check motion constraints"; $0.status = "completed" },
                    .with { $0.text = "Audit main-thread layout"; $0.status = "completed" },
                    .with { $0.text = "Rewrite CALayer halo"; $0.status = "completed" },
                    .with { $0.text = "Verify GPU usage"; $0.status = "completed" },
                    .with { $0.text = "Update perf docs"; $0.status = "completed" },
                ]
            }
            self.appendItem(id, runID: "r2", kind: .assistantMessage) {
                $0.text = "GPU usage reduced to 2.7%."
            }
            self.world.sessions[sIdx].status = .idle
            self.world.sessions[sIdx].activeRunID = ""
            self.world.sessions[sIdx].progress.turnCount = 2
            self.world.sessions[sIdx].updatedAt = .init(date: .now)
            if let rIdx = self.projections[id]?.runs.firstIndex(where: { $0.runID == "r2" }) {
                self.projections[id]?.runs[rIdx].status = .completed
                self.projections[id]?.runs[rIdx].completedAt = .init(date: .now)
                let doneRun = self.projections[id]!.runs[rIdx]
                self.emitEvent(id) { $0.type = "run.completed"; $0.run = doneRun }
            }
            self.publish()
        }
    }

    private func seedLiveThinking() {
        let id = "demo-live-thinking"
        var s = session(id, title: "Live thinking session", machine: "demo-mac", status: .running)
        s.agent = "claude"
        s.createdBy = "owner"
        s.activeRunID = "r1"
        s.updatedAt = .init(date: .now.addingTimeInterval(60))
        s.progress.turnCount = 0
        s.progress.turnStartedAt = .init(date: .now)
        world.sessions.insert(s, at: 0)

        beginRun(id, runID: "r1", ordinal: 1, asker: "owner", status: .running, startedAt: .now)
        appendItem(id, runID: "r1", kind: .userMessage) {
            $0.actor = .with { $0.kind = .owner; $0.id = "owner" }
            $0.text = "Trace the reconnect sequence and verify reasoning cleanup."
        }
        appendItem(id, runID: "r1", kind: .reasoning) {
            $0.itemID = "r1:rsn:1"
            $0.status = "running"
            $0.streaming = true
            $0.text = """
            Line 1: Inspect SubscribeSession afterSeq continuity across tunnel upgrade.
            Line 2: Confirm EventReasoningDelta updates TurnItem with streaming=true.
            Line 3: Verify collapsed preview stays anchored to the latest three lines.
            Line 4: Verify tapping the thinking header opens the full scrollable chain.
            Line 5: Verify reasoning disappears completely once the turn finishes.
            """
        }
        syncSession(id)
    }

    private func startLiveThinkingCompletion() {
        let id = "demo-live-thinking"
        guard tasks[id] == nil else { return }
        tasks[id] = Task { @MainActor [weak self] in
            try? await Task.sleep(for: .milliseconds(9000))
            guard !Task.isCancelled, let self,
                  let sIdx = self.world.sessions.firstIndex(where: { $0.sessionID == id }) else { return }
            if let rsnIdx = self.projections[id]?.items.firstIndex(where: { $0.itemID == "r1:rsn:1" }) {
                var doneRsn = self.projections[id]!.items[rsnIdx]
                doneRsn.status = "completed"
                doneRsn.streaming = false
                self.projections[id]?.items[rsnIdx] = doneRsn
                self.emitEvent(id) { $0.type = "item.updated"; $0.item = doneRsn }
            }
            self.appendItem(id, runID: "r1", kind: .assistantMessage) {
                $0.text = "Final answer after thinking."
            }
            self.world.sessions[sIdx].status = .idle
            self.world.sessions[sIdx].activeRunID = ""
            self.world.sessions[sIdx].progress.turnCount = 1
            self.world.sessions[sIdx].updatedAt = .init(date: .now)
            if let rIdx = self.projections[id]?.runs.firstIndex(where: { $0.runID == "r1" }) {
                self.projections[id]?.runs[rIdx].status = .completed
                self.projections[id]?.runs[rIdx].completedAt = .init(date: .now)
                let doneRun = self.projections[id]!.runs[rIdx]
                self.emitEvent(id) { $0.type = "run.completed"; $0.run = doneRun }
            }
            self.publish()
        }
    }

    private func seedQuestionFlow() {
        let id = "demo-question-flow"
        let q = Question.with {
            $0.questionID = "req-flow-q1"
            $0.turn = 12
            $0.askedAt = .init(date: .now)
            $0.expiresAt = .init(date: .now.addingTimeInterval(1800))
            $0.items = [.with {
                $0.id = "noise"
                $0.text = "How should sidebar status colors reduce visual noise?"
                $0.allowOther = true
                $0.options = [
                    .with { $0.label = "(Recommended) Keep sage green & muted purple with lower sidebar brightness so titles stay primary" },
                    .with { $0.label = "Strict monochrome icons in sidebar rows and keep semantic colors only inside the hover card" },
                    .with { $0.label = "Merge pushed and merged states into one muted green hue distinguished only by icon shape" },
                ]
            }]
        }

        var s = session(id, title: "Question flow session", machine: "demo-mac", status: .waitingInput)
        s.agent = "antigravity"
        s.createdBy = "owner"
        s.activeRunID = "r12"
        s.pendingRequestID = q.questionID
        s.progress.pendingQuestion = q
        s.progress.turnCount = 11
        s.updatedAt = .init(date: .now)
        world.sessions.insert(s, at: 0)

        for t in 1 ... 11 {
            let rid = "r\(t)"
            beginRun(id, runID: rid, ordinal: UInt32(t), asker: "owner", status: .completed, startedAt: .now.addingTimeInterval(Double(-120 + t * 5)))
            appendItem(id, runID: rid, kind: .userMessage) {
                $0.actor = .with { $0.kind = .owner; $0.id = "owner" }
                $0.text = "Flow prompt #\(t): review sidebar palette contrast."
            }
            appendItem(id, runID: rid, kind: .assistantMessage) {
                $0.text = "Flow context #\(t): measured luminance across sidebar rows and hover popovers for entry \(t)."
            }
        }

        beginRun(id, runID: "r12", ordinal: 12, asker: "owner", status: .waitingRequest, startedAt: .now)
        appendItem(id, runID: "r12", kind: .userMessage) {
            $0.actor = .with { $0.kind = .owner; $0.id = "owner" }
            $0.text = "Please propose three color noise-reduction options."
        }
        appendItem(id, runID: "r12", kind: .assistantMessage) {
            $0.text = "Flow latest analysis: I compared saturation levels in Sidebar.tsx and prepared three options below."
        }
        appendItem(id, runID: "r12", kind: .toolCall) {
            $0.toolName = "Run ask_question"
            $0.status = "completed"
        }
        openQuestionRequest(id, runID: "r12", question: q)
        syncSession(id)
    }

    private func seedPlanReviewFlow() {
        let id = "demo-plan-review-flow"
        let reqID = "req-plan-flow-1"
        var s = session(id, title: "Plan review flow session", machine: "demo-mac", status: .waitingInput)
        s.agent = "antigravity"
        s.createdBy = "owner"
        s.interactionMode = .plan
        s.pendingRequestID = reqID
        s.progress.turnCount = 10
        s.updatedAt = .init(date: .now)
        world.sessions.insert(s, at: 0)

        for t in 1 ... 9 {
            let rid = "r\(t)"
            beginRun(id, runID: rid, ordinal: UInt32(t), asker: "owner", status: .completed, startedAt: .now.addingTimeInterval(Double(-120 + t * 5)))
            appendItem(id, runID: rid, kind: .userMessage) {
                $0.actor = .with { $0.kind = .owner; $0.id = "owner" }
                $0.text = "Plan background #\(t): review multi-device session title sync."
            }
            appendItem(id, runID: rid, kind: .assistantMessage) {
                $0.text = "Plan analysis #\(t): checked desktop, iOS, and CLI title rendering consistency for step \(t)."
            }
        }

        beginRun(id, runID: "r10", ordinal: 10, asker: "owner", status: .completed, startedAt: .now)
        appendItem(id, runID: "r10", kind: .userMessage) {
            $0.actor = .with { $0.kind = .owner; $0.id = "owner" }
            $0.text = "Design the internationalized session title mechanism and ask for my approval before coding."
        }
        appendItem(id, runID: "r10", kind: .proposedPlan) {
            $0.planTitle = "Hermesh Internationalized Session Title Design Plan"
            $0.text = """
            1. Single authoritative Session.title generated in the initiating client's UI language.
            2. Zero-config lightweight model routing on the machine side with automatic offline fallback.
            3. Right-click or long-press Regenerate Title to re-title after switching language.
            """
        }
        let req = Hermesh_Session_V1_RuntimeRequest.with {
            $0.requestID = reqID
            $0.sessionID = id
            $0.runID = "r10"
            $0.kind = .planReview
            $0.status = .pending
            $0.responseMode = .resumeTurn
            $0.planReview = .with {
                $0.planID = "plan-1"
                $0.title = "Hermesh Internationalized Session Title Design Plan"
            }
        }
        projections[id, default: .init()].requests.append(req)
        appendItem(id, runID: "r10", kind: .runtimeRequest) {
            $0.requestID = reqID
            $0.status = "waiting"
        }
        emitEvent(id) { $0.type = "request.opened"; $0.request = req }
        syncSession(id)
    }

    private func seedLinkMap() {
        func envMachine(_ id: String, _ name: String, local: Bool, agents: [String]) -> Machine {
            .with {
                $0.machineID = id; $0.machineName = name; $0.local = local; $0.online = true; $0.version = "Demo"
                $0.status = .with {
                    $0.name = name; $0.platform = "darwin/arm64"; $0.maxSessions = 8
                    $0.agents = agents.map { a in .with {
                        $0.name = a; $0.available = true; $0.loggedIn = true; $0.version = "Demo"
                        $0.models = [.with { $0.id = "demo-model"; $0.name = "Demo Model"; $0.efforts = ["high"] }]
                    } }
                }
            }
        }
        var homebook = envMachine("m-homebook", "yee-homebook", local: true, agents: ["claude", "codex", "antigravity", "deepseek"])
        homebook.her = .with { $0.enabled = true; $0.running = true; $0.modelReady = true }
        let mini = envMachine("m-mini", "yi-mac-mini", local: false, agents: ["claude", "codex"])
        world.machines = [homebook, mini]
        world.devices = [
            .with { $0.keyID = "demo-iphone"; $0.label = "iPhone"; $0.platform = "ios"; $0.thisDevice = true },
            .with { $0.keyID = "demo-mac-device"; $0.label = "MacBook"; $0.platform = "macos" },
        ]
        world.projects = world.machines.map { m in .with {
            $0.machineID = m.machineID; $0.projectID = "hermesh"; $0.path = "/Demo/hermesh"
            $0.defaultBaseRef = "main"; $0.workspace = .worktree; $0.agents = ["claude", "codex", "antigravity", "deepseek"]
        } }
        func grant(_ id: String, _ label: String, herMachine: String = "") -> Client {
            .with {
                $0.clientID = id; $0.grantID = "g-" + id; $0.label = label; $0.online = true
                $0.herMachineID = herMachine; $0.grantVersion = 1
                $0.expiresAt = .init(date: .now.addingTimeInterval(30 * 86400))
                $0.spec = .with { $0.label = label; $0.machineIds = ["*"]; $0.projectIds = ["*"]; $0.agents = ["*"]; $0.ttlDays = 30 }
            }
        }
        world.clients = [
            grant("c-claude", "Claude (claude.ai)"),
            grant("c-her", "Her", herMachine: "m-homebook"),
            grant("c-muse", "Muse (agent.meta.ai)"),
        ]
        world.pairings.removeAll()
        projections.removeAll()
        events.removeAll()

        let specs: [(id: String, title: String, by: String, machine: String, agent: String, elapsed: TimeInterval)] = [
            ("lm-1", "Polish streaming typewriter cadence", "owner", "m-homebook", "antigravity", 930),
            ("lm-2", "Double-click inline session rename on Mac", "owner", "m-homebook", "antigravity", 1395),
            ("lm-3", "Inspect Claude desktop terminal CLI flags", "owner", "m-homebook", "antigravity", 1009),
            ("lm-4", "Fix mobile and Mac session archive error", "owner", "m-homebook", "antigravity", 1576),
            ("lm-5", "Refine mobile connection status copy", "owner", "m-homebook", "antigravity", 4171),
            ("lm-6", "Investigate relay reconnect jitter", "c-claude", "m-homebook", "claude", 412),
            ("lm-7", "Add offline world cache unit tests", "c-claude", "m-homebook", "claude", 265),
            ("lm-8", "Audit nightly build and bundle size", "c-her", "m-mini", "codex", 188),
        ]
        world.sessions = specs.map { sp in
            var s = session(sp.id, title: sp.title, machine: sp.machine, status: .running)
            s.projectID = "hermesh"
            s.agent = sp.agent
            s.createdBy = sp.by
            s.activeRunID = "r1"
            let started = Date.now.addingTimeInterval(-sp.elapsed)
            s.createdAt = .init(date: started)
            s.updatedAt = .init(date: .now)
            s.progress.turnStartedAt = .init(date: started)
            return s
        }
        for sp in specs {
            beginRun(sp.id, runID: "r1", ordinal: 1, asker: sp.by, status: .running)
            appendItem(sp.id, runID: "r1", kind: .userMessage) { $0.text = sp.title }
        }
    }
    #endif
}
