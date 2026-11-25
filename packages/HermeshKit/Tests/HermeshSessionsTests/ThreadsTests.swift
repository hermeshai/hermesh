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

@Test func sessionEventProjectionReducerAndRollback() {
    var initEv = Hermesh_Session_V1_SessionEvent()
    initEv.sessionID = "s_mac_1"
    initEv.seq = 1
    initEv.type = "session.created"
    initEv.session = .with {
        $0.sessionID = "s_mac_1"
        $0.status = .running
        $0.progress.lastEventSeq = 1
    }
    initEv.item = .with {
        $0.itemID = "it_1"
        $0.runID = "run_1"
        $0.seq = 1
        $0.kind = .assistantMessage
        $0.text = "Hello "
    }

    var state = applySessionEvent(initEv, to: nil)
    #expect(state.session.progress.lastEventSeq == 1)

    // Duplicate seq is ignored; streaming delta appends onto targetItemID.
    state = applySessionEvent(initEv, to: state)
    #expect(state.items.first?.text == "Hello ")

    var deltaEv = Hermesh_Session_V1_SessionEvent()
    deltaEv.sessionID = "s_mac_1"
    deltaEv.seq = 2
    deltaEv.type = "item.delta"
    deltaEv.targetItemID = "it_1"
    deltaEv.textDelta = "mesh!"
    state = applySessionEvent(deltaEv, to: state)
    state = applySessionEvent(deltaEv, to: state) // exact duplicate delta seq is not doubled
    #expect(state.items.first?.text == "Hello mesh!")
    #expect(state.items.first?.streaming == true)

    // An out-of-band unary session snapshot with higher lastEventSeq must not drop in-flight item events.
    var ahead = Hermesh_Session_V1_SessionProjection()
    ahead.session = .with { $0.sessionID = "s_mac_1"; $0.progress.lastEventSeq = 25 }
    ahead = applySessionEvent(initEv, to: ahead)
    #expect(ahead.items.first?.text == "Hello ")
    #expect(ahead.session.progress.lastEventSeq == 25)

    // Pending runtime request opens and resolves cleanly.
    var reqEv = Hermesh_Session_V1_SessionEvent()
    reqEv.sessionID = "s_mac_1"
    reqEv.seq = 3
    reqEv.type = "request.opened"
    reqEv.session = .with {
        $0.sessionID = "s_mac_1"
        $0.status = .waitingInput
        $0.pendingRequestID = "req_1"
    }
    reqEv.request = .with {
        $0.requestID = "req_1"
        $0.kind = .toolApproval
        $0.status = .pending
    }
    state = applySessionEvent(reqEv, to: state)
    #expect(pendingRequest(of: state)?.requestID == "req_1")
    #expect(state.session.status == .waitingInput)

    var resEv = Hermesh_Session_V1_SessionEvent()
    resEv.sessionID = "s_mac_1"
    resEv.seq = 4
    resEv.type = "request.resolved"
    resEv.session = .with {
        $0.sessionID = "s_mac_1"
        $0.status = .running
        $0.pendingRequestID = ""
    }
    resEv.request = .with {
        $0.requestID = "req_1"
        $0.kind = .toolApproval
        $0.status = .resolved
    }
    state = applySessionEvent(resEv, to: state)
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
    // A queued run whose message is in the conversation (kept on this device for an offline machine) is its bubble.
    var kept = state
    kept.items.append(.with { $0.itemID = "u"; $0.runID = "q_a"; $0.seq = 6; $0.kind = .userMessage; $0.text = "hi" })
    #expect(queuedRuns(of: kept).map(\.runID) == ["q_b"])

    var rbEv = Hermesh_Session_V1_SessionEvent()
    rbEv.sessionID = "s_mac_1"
    rbEv.seq = 6
    rbEv.type = "session.rolled_back"
    rbEv.checkpoint = .with {
        $0.checkpointID = "cp_1"
        $0.runOrdinal = 1
    }
    state = applySessionEvent(rbEv, to: state)
    #expect(state.checkpoints.map(\.checkpointID) == ["cp_1"])
    #expect(state.items.map(\.itemID) == ["it_1"])
    #expect(state.runs.first(where: { $0.runID == "run_2" })?.status == .rolledBack)
    #expect(queuedRuns(of: state).map(\.runID) == ["q_a", "q_b"])
}

@Test func polymorphicComposerActions() {
    #expect(composerPrimaryAction(hasSession: false, status: .idle, draftNotEmpty: false, machineOnline: true, supportsLiveSteer: true) == .start)
    #expect(composerPrimaryAction(hasSession: true, status: .running, draftNotEmpty: false, machineOnline: true, supportsLiveSteer: true) == .stop)
    #expect(composerPrimaryAction(hasSession: true, status: .running, draftNotEmpty: true, machineOnline: true, supportsLiveSteer: true) == .steer)
    #expect(composerPrimaryAction(hasSession: true, status: .running, draftNotEmpty: true, machineOnline: true, supportsLiveSteer: false) == .queue)
    #expect(composerPrimaryAction(hasSession: true, status: .idle, draftNotEmpty: true, machineOnline: false, supportsLiveSteer: true) == .queueOffline)
    #expect(composerPrimaryAction(hasSession: true, status: .idle, draftNotEmpty: true, machineOnline: true, supportsLiveSteer: true) == .send)
}

@Suite struct SessionComposerTests {
    private func effort(_ ids: [String], _ def: String) -> Hermesh_Session_V1_OptionDescriptor {
        var d = Hermesh_Session_V1_OptionDescriptor()
        d.id = "effort"
        d.role = "reasoning"
        d.type = .select
        d.choices = ids.map { id in var c = Hermesh_Session_V1_OptionChoice(); c.id = id; c.label = id; c.isDefault = id == def; return c }
        var v = Hermesh_Session_V1_OptionValue(); v.kind = .stringValue(def)
        d.currentValue = v
        return d
    }
    private func model(_ slug: String, _ levels: [String] = [], _ def: String = "", isDefault: Bool = false) -> Hermesh_Session_V1_ModelCatalogEntry {
        var m = Hermesh_Session_V1_ModelCatalogEntry()
        m.slug = slug; m.name = "Claude \(slug)"; m.shortName = slug; m.isDefault = isDefault
        if !levels.isEmpty { m.optionDescriptors = [effort(levels, def)] }
        return m
    }
    private var providers: [Hermesh_Session_V1_ProviderInstance] {
        var c = Hermesh_Session_V1_ProviderInstance()
        c.instanceID = "claude"; c.driver = "claude"; c.displayName = "Claude Code"; c.enabled = true; c.installed = true; c.authenticated = true
        c.models = [model("opus", ["low", "medium", "high", "max"], "high"), model("sonnet", ["low", "medium", "high"], "medium", isDefault: true), model("haiku")]
        var g = Hermesh_Session_V1_ProviderInstance()
        g.instanceID = "gemini"; g.driver = "acp"; g.acpFlavor = "gemini"; g.displayName = "Gemini CLI"; g.enabled = true; g.installed = true
        var gm = Hermesh_Session_V1_ModelCatalogEntry(); gm.slug = "gemini-3-pro"; gm.name = "Gemini 3 Pro"; gm.isDefault = true
        g.models = [gm]
        return [c, g]
    }
    private func sel(_ model: String, _ level: String? = nil) -> Hermesh_Session_V1_ModelSelection {
        var s = Hermesh_Session_V1_ModelSelection(); s.instanceID = "claude"; s.model = model
        if let level { s = withReasoning(s, id: "effort", value: level) }
        return s
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
