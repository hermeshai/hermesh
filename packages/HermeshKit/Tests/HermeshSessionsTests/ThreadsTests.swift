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
    state = applyThreadEvent(deltaEv, to: state) // exact duplicate delta seq is not doubled
    #expect(state.items.first?.text == "Hello mesh!")
    #expect(state.items.first?.streaming == true)

    // An out-of-band unary thread snapshot with higher lastSeq must not drop in-flight item events.
    var ahead = Hermesh_Thread_V2_ThreadProjection()
    ahead.thread = .with { $0.threadID = "th_mac_1"; $0.lastSeq = 25 }
    ahead = applyThreadEvent(initEv, to: ahead)
    #expect(ahead.items.first?.text == "Hello ")
    #expect(ahead.thread.lastSeq == 25)

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

// The new-session composer's rules (ThreadComposer.swift), as the Mac's composer.test.ts checks them.
@Suite struct ThreadComposerTests {
    private func effort(_ ids: [String], _ def: String) -> Hermesh_Thread_V2_OptionDescriptor {
        var d = Hermesh_Thread_V2_OptionDescriptor()
        d.id = "effort"
        d.role = "reasoning"
        d.type = .select
        d.choices = ids.map { id in var c = Hermesh_Thread_V2_OptionChoice(); c.id = id; c.label = id; c.isDefault = id == def; return c }
        var v = Hermesh_Thread_V2_OptionValue(); v.kind = .stringValue(def)
        d.currentValue = v
        return d
    }
    private func model(_ slug: String, _ levels: [String] = [], _ def: String = "", isDefault: Bool = false) -> Hermesh_Thread_V2_ModelCatalogEntry {
        var m = Hermesh_Thread_V2_ModelCatalogEntry()
        m.slug = slug; m.name = "Claude \(slug)"; m.shortName = slug; m.isDefault = isDefault
        if !levels.isEmpty { m.optionDescriptors = [effort(levels, def)] }
        return m
    }
    private var providers: [Hermesh_Thread_V2_ProviderInstance] {
        var c = Hermesh_Thread_V2_ProviderInstance()
        c.instanceID = "claude"; c.driver = "claude"; c.displayName = "Claude Code"; c.enabled = true; c.installed = true; c.authenticated = true
        c.models = [model("opus", ["low", "medium", "high", "max"], "high"), model("sonnet", ["low", "medium", "high"], "medium", isDefault: true), model("haiku")]
        var g = Hermesh_Thread_V2_ProviderInstance()
        g.instanceID = "gemini"; g.driver = "acp"; g.acpFlavor = "gemini"; g.displayName = "Gemini CLI"; g.enabled = true; g.installed = true
        var gm = Hermesh_Thread_V2_ModelCatalogEntry(); gm.slug = "gemini-3-pro"; gm.name = "Gemini 3 Pro"; gm.isDefault = true
        g.models = [gm]
        return [c, g]
    }
    private func sel(_ model: String, _ level: String? = nil) -> Hermesh_Thread_V2_ModelSelection {
        var s = Hermesh_Thread_V2_ModelSelection(); s.instanceID = "claude"; s.model = model
        if let level { s = withReasoning(s, id: "effort", value: level) }
        return s
    }

    @Test func testThreadsAreSessionRows() {
        var th = Hermesh_Thread_V2_Thread()
        th.threadID = "th_ab_01"; th.machineID = "m1"; th.projectID = "hermesh"; th.title = "Fix it"; th.status = .waitingRequest
        th.modelSelection.instanceID = "codex"
        var owner = Hermesh_Thread_V2_Actor(); owner.kind = .owner; owner.id = "owner"
        th.createdBy = owner
        let s = threadAsSession(th)
        #expect([s.sessionID, s.machineID, s.projectID, s.agent, s.createdBy, s.title] == ["th_ab_01", "m1", "hermesh", "codex", "owner", "Fix it"])
        #expect(s.status == .waitingInput)
        var bot = Hermesh_Thread_V2_Actor(); bot.kind = .externalAgent; bot.id = "c_grok"
        th.createdBy = bot; th.status = .failed
        #expect(threadAsSession(th).createdBy == "c_grok")
        #expect(threadAsSession(th).status == .crashed)
        th.status = .idle; th.activeBackgroundCount = 1
        #expect(threadAsSession(th).status == .running)
        th.activeBackgroundCount = 0; th.activeDaemonCount = 1
        #expect(threadAsSession(th).status == .idle)
        #expect(isThreadID("th_x")); #expect(!isThreadID("s_x"))
    }

    @Test func testThinkingLevelsFollowTheModel() {
        #expect(reasoningOf(providers, sel("opus"))?.value == "high")
        #expect(reasoningOf(providers, sel("haiku")) == nil)
        #expect(reasoningOf(providers, selectModel(providers, from: sel("opus", "low"), instanceID: "claude", slug: "sonnet").selection)?.value == "low")
        let r = selectModel(providers, from: sel("opus", "max"), instanceID: "claude", slug: "sonnet")
        #expect(r.changed?.from == "max"); #expect(r.changed?.to == "medium")
        #expect(selectModel(providers, from: sel("opus", "max"), instanceID: "claude", slug: "haiku").changed == nil)
        #expect(defaultSelection(providers[0]).model == "sonnet")
    }

    @Test func testProvidersSearchAndChoice() {
        #expect(findModels(providers, query: "son").map { $0.provider.instanceID } == ["claude"])
        #expect(findModels(providers, query: "gemini").map { $0.provider.instanceID } == ["gemini"])
        #expect(providerUnusable(providers[0], projectAgents: []) == nil)
        #expect(providerUnusable(providers[1], projectAgents: []) == .signedOut)
        #expect(providerUnusable(providers[1], projectAgents: ["claude"]) == .notForProject)
        #expect(preferredProvider(providers.reversed())?.instanceID == "claude")
    }

    @Test func testProjectOnAnotherMachine() {
        func p(_ m: String, _ id: String) -> Hermesh_Local_V1_Project { var x = Hermesh_Local_V1_Project(); x.machineID = m; x.projectID = id; return x }
        let projects = [p("a", "hermesh"), p("b", "hermesh"), p("b", "relay")]
        #expect(projectOnMachine(projects, machineID: "b", current: "hermesh") == "hermesh")
        #expect(projectOnMachine(projects, machineID: "b", current: "website", lastUsed: "relay") == "relay")
        #expect(projectOnMachine(projects, machineID: "c", current: "hermesh", lastUsed: "relay") == scratchProject)
    }

    @Test func testGrantsANewProjectReaches() {
        func c(_ id: String, _ machines: [String], _ projects: [String], revoked: Bool = false) -> Hermesh_Local_V1_Client {
            var x = Hermesh_Local_V1_Client(); x.clientID = id; x.revoked = revoked
            x.spec.machineIds = machines; x.spec.projectIds = projects
            return x
        }
        let clients = [c("all", ["*"], ["*"]), c("relay", ["b"], ["relay"]), c("other", ["a"], ["*"]), c("gone", ["*"], ["*"], revoked: true), Hermesh_Local_V1_Client()]
        #expect(grantsReaching(clients, machineID: "b", projectID: "relay").map { "\($0.client.clientID):\($0.all)" } == ["all:true", "relay:false"])
        #expect(grantsReaching(clients, machineID: "b", projectID: "").map(\.client.clientID) == ["all"])
    }
}
