import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI

// A v2 session on the iPhone (docs/thread-composer.md): a new one, where the
// owner picks the machine, the project, the model and how far it may go, and
// one under way, its conversation over the same box. The box is Her's; what
// the session runs on and with is the line under it.

/// What a new session used last, and what each machine had when last read, kept on this iPhone (§4.1, §5.3).
enum ThreadChoices {
    struct Mine: Codable {
        var projectID: String?
        var selection: Data?
        var mode: Int?
    }

    private struct Kept: Codable {
        var machineID: String?
        var byMachine: [String: Mine] = [:]
        var projects: [String: [Data]] = [:]
        var providers: [String: Data] = [:]
    }

    private static let key = "threadComposer"

    private static var kept: Kept {
        get { UserDefaults.standard.data(forKey: key).flatMap { try? JSONDecoder().decode(Kept.self, from: $0) } ?? Kept() }
        set { UserDefaults.standard.set(try? JSONEncoder().encode(newValue), forKey: key) }
    }

    static var lastMachine: String? { kept.machineID }
    static func mine(_ machine: String) -> Mine? { kept.byMachine[machine] }
    static func lastSelection(_ machine: String) -> Hermesh_Thread_V2_ModelSelection? {
        mine(machine)?.selection.flatMap { try? Hermesh_Thread_V2_ModelSelection(serializedBytes: $0) }
    }

    /// After a session starts: its machine, and what it used there.
    static func remember(machine: String, project: String, selection: Hermesh_Thread_V2_ModelSelection, mode: Hermesh_Identity_V1_RuntimeMode) {
        var k = kept
        k.machineID = machine
        k.byMachine[machine] = Mine(projectID: project, selection: try? selection.serializedData(), mode: mode.rawValue)
        kept = k
    }

    /// The projects of the machines that answered; an empty list never replaces what was kept (a machine going
    /// offline may lose its projects a moment before it shows offline).
    static func keepProjects(_ projects: [Project], machines: [Machine]) {
        var k = kept
        for m in machines where m.online || m.local {
            let its = projects.filter { $0.machineID == m.machineID }
            if !its.isEmpty { k.projects[m.machineID] = its.compactMap { try? $0.serializedData() } }
        }
        kept = k
    }

    static func lastProjects(_ machine: String) -> [Project] {
        (kept.projects[machine] ?? []).compactMap { try? Project(serializedBytes: $0) }
    }

    static func keepProviders(_ machine: String, _ res: Hermesh_Thread_V2_ListProvidersResponse) {
        var k = kept
        k.providers[machine] = try? res.serializedData()
        kept = k
    }

    static func lastProviders(_ machine: String) -> Hermesh_Thread_V2_ListProvidersResponse? {
        kept.providers[machine].flatMap { try? Hermesh_Thread_V2_ListProvidersResponse(serializedBytes: $0) }
    }
}

struct ThreadPage: View {
    @Environment(AppModel.self) private var model
    /// The session shown; empty on a new one.
    let threadID: String
    private let askedMachine: String
    private let askedProject: String

    @State private var machineID = ""
    @State private var projectID = scratchProject
    @State private var worktree = true
    @State private var selection = Hermesh_Thread_V2_ModelSelection()
    @State private var mode: Hermesh_Identity_V1_RuntimeMode = .auto
    @State private var plan = false
    @State private var providers: [Hermesh_Thread_V2_ProviderInstance] = []
    @State private var providersStale = false
    @State private var ceiling: Hermesh_Identity_V1_RuntimeMode = .unspecified
    @State private var draft = ""
    @State private var sending = false
    @State private var projection: Hermesh_Thread_V2_ThreadProjection?
    @State private var pickingModel = false
    /// Whether the conversation shows its end, so it follows what comes (as a session's, SessionView).
    @State private var atEnd = true
    @State private var position = ScrollPosition(edge: .bottom)
    @State private var scrollBox = ConversationScrollBox()
    @State private var renaming = false
    @State private var deleting = false
    /// Messages to a session go out one after another, in the order sent.
    @State private var lastSend: Task<Void, Never>?
    @FocusState private var focused: Bool

    init(machineID: String, projectID: String) {
        threadID = ""
        askedMachine = machineID
        askedProject = projectID
    }

    init(threadID: String) {
        self.threadID = threadID
        askedMachine = ""
        askedProject = ""
    }

    private var thread: Hermesh_Thread_V2_Thread? { projection?.thread }
    private var isNew: Bool { threadID.isEmpty }
    private var machine: Machine? { model.machines.first { $0.machineID == machineID } }
    private var online: Bool { machine.map { $0.online || $0.local } ?? true }
    /// A machine from before threads can't take a new session: said, not hidden (§5.2).
    private var needsUpdate: Bool { isNew && machine.map { $0.hasStatus && !$0.status.threads } ?? false }
    private var running: Bool { [.preparing, .running, .waitingRequest].contains(thread?.status ?? .idle) }

    /// A machine's projects: live while it answers, else what was last read of it.
    private func projects(on id: String) -> (list: [Project], stale: Bool) {
        let live = model.projects.filter { $0.machineID == id }
        guard let m = model.machines.first(where: { $0.machineID == id }), !m.online, !m.local else { return (live, false) }
        let list = live.isEmpty ? ThreadChoices.lastProjects(id) : live
        return (list, !list.isEmpty)
    }

    /// The thread as a session (as the list has it), for a session's rows, cards and info.
    private var asSession: Session? {
        guard let thread else { return nil }
        var s = threadAsSession(thread)
        if let f = threadFailure(thread) { s.progress.lastFailure = f }
        return s
    }

    var body: some View {
        Group {
            if isNew {
                newSession.navigationTitle(String(localized: "New Session")).rootToolbar()
            } else if let s = asSession {
                session(s)
            } else {
                PageLoader(text: "Reading the session…", bare: true)
            }
        }
        .navigationBarTitleDisplayMode(.inline)
        .task(id: threadID) { await start() }
        .task(id: "\(machineID)/\(projectID)") { await loadProviders() }
        .onChange(of: model.projects) { ThreadChoices.keepProjects(model.projects, machines: model.machines) }
        .onChange(of: model.addedProject) {
            // The project Add a Project just added, from this page: the new session takes it.
            guard let added = model.addedProject, added.machineID == machineID else { return }
            projectID = added.projectID
            worktree = added.worktree
            model.addedProject = nil
        }
        .sheet(isPresented: $pickingModel) {
            ModelSheet(providers: providers, selection: $selection, stale: providersStale, machineName: machine?.machineName ?? "",
                       projectAgents: projects(on: machineID).list.first { $0.projectID == projectID }?.agents ?? []) { changed in
                model.show(String(localized: "\(modelTitle) has no “\(levelTitle(changed.from))” level; thinking is now “\(levelTitle(changed.to))”"))
            }
        }
    }

    // MARK: A new session

    private var newSession: some View {
        ScrollView {
            VStack(spacing: 18) {
                Text("New Session").font(.title2.weight(.semibold)).padding(.top, 60)
                    .accessibilityIdentifier("new-session-title")
                box
                options
                if needsUpdate {
                    Text("Hermesh on \(machine?.machineName ?? "") needs an update to start sessions.")
                        .font(.footnote).foregroundStyle(.orange).frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal, 20)
                        .accessibilityIdentifier("needs-update")
                }
            }
            .padding(.bottom, 40)
        }
        .scrollDismissesKeyboard(.interactively)
    }

    // MARK: A session under way

    /// Its conversation as a session's page has it (SessionView): the same rows, the same ending, the same
    /// toolbar; under it, what the agent waits on, then the box.
    private func session(_ s: Session) -> some View {
        let grant = model.grantLabel(s)
        let steps = groupSteps(threadTranscript(projection))
        return ScrollView {
            VStack(alignment: .leading, spacing: 13) {
                if s.status == .archived { ArchivedNote(session: s) }
                ForEach(Array(steps.enumerated()), id: \.element.id) { i, step in
                    StepView(step: step, session: s, grant: grant, previousTurn: previousTurn(steps, before: i))
                }
                if let f = threadFailure(thread) {
                    FailedView(session: s, failure: f, grant: grant)
                } else if thread?.status == .idle, let last = steps.last {
                    if case .item(.turnEnd) = last {} else {
                        DoneLine(text: String(localized: "This turn is done. When \(grant) sends another message, it continues in this session."))
                    }
                }
            }
            .padding(.horizontal, 20)
            .padding(.top, 13).padding(.bottom, 21)
            .background(ConversationScrollFinder(box: scrollBox))
        }
        .followsEnd(id: threadID, ready: projection != nil, atEnd: $atEnd, position: $position, scrollBox: scrollBox) {
            // Archived, it is read-only, as a session is: no box.
            if s.status != .archived {
                VStack(spacing: 6) {
                    if let req = pendingRequest(of: projection) { requestPanel(req, s) }
                    queue
                    box
                    options
                }
                .padding(.bottom, 4)
            }
        }
        .scrollDismissesKeyboard(.interactively)
        .toolbar {
            ToolbarItem(placement: .principal) {
                VStack(spacing: 0) {
                    Text(s.displayTitle).font(.subheadline.weight(.semibold)).lineLimit(1)
                    if model.shownSync != nil {
                        SyncLine(small: true)
                    } else {
                        Text("\(grant) · \(model.machineName(s.machineID))/\(s.projectID)").font(.caption2).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
            }
            if model.path.isEmpty { MenuToolbarItem() }
            ToolbarItemGroup(placement: .topBarTrailing) {
                Button("Session Info", systemImage: "info.circle") { model.sheet = .sessionInfo(s.sessionID) }
                    .accessibilityIdentifier("session-info")
                Menu {
                    Button("Copy Session ID", systemImage: "doc.on.doc") {
                        UIPasteboard.general.string = s.sessionID
                        model.show(String(localized: "Copied the session ID"))
                    }
                    SessionActions(session: s, renaming: $renaming, deleting: $deleting)
                } label: {
                    Image(systemName: "ellipsis")
                }
                .accessibilityLabel("More")
                .accessibilityIdentifier("session-more")
            }
        }
        .sessionDialogs(s, renaming: $renaming, deleting: $deleting)
    }

    /// What the agent waits on: a question as a session's panel asks it, the rest as Her's approval.
    @ViewBuilder private func requestPanel(_ req: Hermesh_Thread_V2_RuntimeRequest, _ s: Session) -> some View {
        if req.kind == .userInput {
            QuestionPanel(session: s, question: threadQuestion(req)) { answers, decline in
                await respondNow(req) { r in
                    r.decision = decline ? .decline : .acceptOnce
                    for a in answers {
                        r.answers[a.itemID] = .with { $0.selectedOptions = a.selected; $0.customText = a.text }
                    }
                }
            }
            .id(req.requestID)
        } else {
            ThreadRequestPanel(session: s, request: req) { build in await respondNow(req, build) }
                .id(req.requestID)
        }
    }

    /// What waits for the next turn: each can steer the turn now, or go.
    @ViewBuilder private var queue: some View {
        let queued = queuedRuns(of: projection)
        if !queued.isEmpty {
            VStack(alignment: .leading, spacing: 4) {
                ForEach(Array(queued.enumerated()), id: \.element.runID) { i, run in
                    HStack {
                        Text(verbatim: run.prompt).font(.footnote).lineLimit(1)
                        Spacer()
                        Button("Steer Now") { manage(run, .promoteToSteer) }.font(.footnote)
                        Button("Remove", systemImage: "xmark") { manage(run, .cancel) }.labelStyle(.iconOnly).font(.footnote)
                    }
                    .accessibilityIdentifier("thread-queued-\(i)")
                }
            }
            .padding(.horizontal, 22).padding(.top, 6)
        }
    }

    // MARK: The box and the line under it

    /// Her's box: the text, and Send (Stop beside it while a turn runs).
    private var box: some View {
        let ready = !sending && !needsUpdate && !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        return HStack(alignment: .bottom, spacing: 8) {
            TextField(isNew ? String(localized: "Describe what to build, debug or plan") : running ? String(localized: "Say more; it goes to the next turn") : String(localized: "Keep going"),
                      text: $draft, axis: .vertical)
                .lineLimit(isNew ? 3 ... 8 : 1 ... 6)
                .focused($focused)
                .padding(.vertical, 8).padding(.leading, 10)
                .accessibilityIdentifier("thread-composer")
            if running {
                Button { stop() } label: {
                    Image(systemName: "stop.fill").font(.footnote).frame(width: 36, height: 36).background(Palette.fill2, in: .circle)
                }
                .tint(.primary)
                .accessibilityLabel(Text("Stop"))
                .accessibilityIdentifier("thread-stop")
            }
            Button { send() } label: {
                Image(systemName: "arrow.up").font(.body.weight(.semibold)).frame(width: 36, height: 36)
                    .foregroundStyle(.white)
                    .background(Color("AccentColor").opacity(ready ? 1 : 0.4), in: .circle)
            }
            .disabled(!ready)
            .accessibilityLabel(Text(online ? "Send" : "Queue Until It’s Online"))
            .accessibilityIdentifier("thread-send")
        }
        .padding(6)
        .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 24, style: .continuous))
        .padding(.horizontal, 12)
    }

    private var modelTitle: String {
        guard let m = modelEntry(providers, selection) else { return String(localized: "Model") }
        return m.shortName.isEmpty ? m.name : m.shortName
    }

    private func levelTitle(_ id: String) -> String {
        switch id {
        case "minimal": String(localized: "Minimal")
        case "low": String(localized: "Low")
        case "medium": String(localized: "Medium")
        case "high": String(localized: "High")
        case "xhigh": String(localized: "Extra High")
        case "max": String(localized: "Max")
        default: id
        }
    }

    private func modeTitle(_ m: Hermesh_Identity_V1_RuntimeMode) -> String {
        switch m {
        case .supervised: String(localized: "Supervised")
        case .autoEdits: String(localized: "Auto-Accept Edits")
        case .fullAccess: String(localized: "Full Access")
        default: String(localized: "Smart Auto")
        }
    }

    private func modeHint(_ m: Hermesh_Identity_V1_RuntimeMode) -> String {
        switch m {
        case .supervised: String(localized: "Asks before each file change, command or outside tool")
        case .autoEdits: String(localized: "Changes files in the project; asks before commands and sensitive files")
        case .fullAccess: String(localized: "Does anything, within the machine’s limit")
        default: String(localized: "Changes files and runs routine builds and tests; asks before risky steps")
        }
    }

    /// What the session runs on and with, under the box. In a session, where it runs is fixed.
    private var options: some View {
        let (here, stale) = projects(on: machineID)
        let project = here.first { $0.projectID == projectID }
        let projectTitle = projectID == scratchProject ? String(localized: "Temporary Workspace") : projectID
        return ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 4) {
                if isNew {
                    Menu {
                        Section("Environment") {
                            ForEach(model.machines, id: \.machineID) { m in
                                // A menu item shows a second text as its subtitle only when it is always there.
                                let note: String? = m.hasStatus && !m.status.threads ? String(localized: "Needs update")
                                    : !m.online && !m.local ? String(localized: "Offline · messages wait until it’s back") : nil
                                Button { pick(machine: m.machineID) } label: {
                                    if let note {
                                        Label { Text(m.machineName); Text(note) } icon: { if m.machineID == machineID { Image(systemName: "checkmark") } }
                                    } else {
                                        Label { Text(m.machineName) } icon: { if m.machineID == machineID { Image(systemName: "checkmark") } }
                                    }
                                }
                                .accessibilityIdentifier("machine-\(m.machineName)")
                            }
                        }
                    } label: { chip(icon: m_icon, machine?.machineName ?? "") }
                        .accessibilityIdentifier("thread-machine")
                    Menu {
                        Section(stale ? String(localized: "Projects on \(machine?.machineName ?? "") · last known") : String(localized: "Projects on \(machine?.machineName ?? "")")) {
                            ForEach(here, id: \.projectID) { p in
                                Button { pick(project: p.projectID, in: here) } label: {
                                    Label { Text(p.projectID); Text(p.path) } icon: { if p.projectID == projectID { Image(systemName: "checkmark") } }
                                }
                                .accessibilityIdentifier("project-\(p.projectID)")
                            }
                            Button { pick(project: scratchProject, in: here) } label: {
                                Label { Text("Temporary Workspace") } icon: { if projectID == scratchProject { Image(systemName: "checkmark") } }
                            }
                        }
                        Button("Add Project…", systemImage: "plus") { model.sheet = .addProject(machineID, forSession: true) }
                            .disabled(!online)
                            .accessibilityIdentifier("thread-add-project")
                    } label: { chip(icon: projectID == scratchProject ? "tray" : "folder", projectTitle) }
                        .accessibilityIdentifier("thread-project")
                    if project?.repo == true || project?.workspace == .worktree {
                        Toggle(isOn: $worktree) { Text("Separate worktree").font(.footnote) }
                            .toggleStyle(.button).buttonStyle(.bordered).controlSize(.small)
                            .accessibilityIdentifier("thread-worktree")
                    }
                } else if let thread {
                    Label("\(model.machineName(thread.machineID)) · \(thread.projectID == scratchProject ? String(localized: "Temporary Workspace") : thread.projectID)", systemImage: "laptopcomputer")
                        .font(.footnote).foregroundStyle(.tertiary).lineLimit(1)
                    if thread.offlineQueued {
                        Text("Queued Offline").font(.footnote).foregroundStyle(.orange).accessibilityIdentifier("thread-offline-queued")
                    }
                }
                Button { pickingModel = true } label: {
                    let level = reasoningOf(providers, selection).map { levelTitle($0.value) }
                    chip(icon: nil, level.map { "\(modelTitle) \($0)" } ?? modelTitle)
                }
                .accessibilityIdentifier("thread-model")
                Menu {
                    Section("Permission mode") {
                        ForEach(orderedRuntimeModes, id: \.rawValue) { m in
                            Button { setMode(m) } label: {
                                Label {
                                    Text(modeTitle(m))
                                    Text(isRuntimeModeAllowed(m, ceiling: ceiling) ? modeHint(m) : String(localized: "Above the limit this machine or grant allows"))
                                } icon: { if clamped == m { Image(systemName: "checkmark") } }
                            }
                            .disabled(!isRuntimeModeAllowed(m, ceiling: ceiling))
                        }
                    }
                    Toggle(isOn: Binding(get: { plan }, set: { setPlan($0) })) { Label("Plan First", systemImage: "list.bullet.clipboard") }
                } label: { chip(icon: nil, plan ? "\(modeTitle(clamped)) · \(String(localized: "Plan"))" : modeTitle(clamped)) }
                    .accessibilityIdentifier("thread-mode")
            }
            .padding(.horizontal, 16)
        }
    }

    private var m_icon: String { machine?.local == true ? "laptopcomputer" : "server.rack" }
    private var clamped: Hermesh_Identity_V1_RuntimeMode { clampRuntimeMode(mode, ceiling: ceiling) }

    private func chip(icon: String?, _ title: String) -> some View {
        HStack(spacing: 4) {
            if let icon { Image(systemName: icon) }
            Text(verbatim: title).lineLimit(1)
            Image(systemName: "chevron.down").font(.caption2.weight(.semibold)).foregroundStyle(.tertiary)
        }
        .font(.footnote).foregroundStyle(.primary)
        .padding(.horizontal, 10).frame(height: 32)
        .background(Palette.fill2, in: Capsule())
    }

    // MARK: Choosing

    private func pick(machine id: String) {
        machineID = id
        pick(project: projectOnMachine(projects(on: id).list, machineID: id, current: projectID, lastUsed: ThreadChoices.mine(id)?.projectID), in: projects(on: id).list)
    }

    private func pick(project id: String, in list: [Project]) {
        projectID = id
        worktree = list.first { $0.projectID == id }?.workspace == .worktree
    }

    private func setMode(_ m: Hermesh_Identity_V1_RuntimeMode) {
        mode = m
        guard !isNew else { return }
        update { $0.runtimeMode = m }
    }

    private func setPlan(_ on: Bool) {
        plan = on
        guard !isNew else { return }
        update { $0.interactionMode = on ? .plan : .code }
    }

    // MARK: Talking to hermeshd

    private func start() async {
        if isNew {
            let known = { (id: String) in !id.isEmpty && model.machines.contains { $0.machineID == id } }
            let mid = [askedMachine, ThreadChoices.lastMachine ?? ""].first(where: known) ?? model.machines.first(where: \.local)?.machineID ?? model.machines.first?.machineID ?? ""
            machineID = mid
            let list = projects(on: mid).list
            pick(project: askedProject.isEmpty ? projectOnMachine(list, machineID: mid, current: "", lastUsed: ThreadChoices.mine(mid)?.projectID) : askedProject, in: list)
            if let m = ThreadChoices.mine(mid)?.mode, let saved = Hermesh_Identity_V1_RuntimeMode(rawValue: m) { mode = saved }
            ThreadChoices.keepProjects(model.projects, machines: model.machines)
            return
        }
        guard let live = model.api?.live else { return }
        do {
            let first = try await live.getThread(threadID: threadID)
            apply(first.projection.thread)
            projection = first.projection
            for try await ev in live.subscribeThread(threadID: threadID, afterSeq: first.projection.thread.lastSeq) {
                projection = applyThreadEvent(ev, to: projection)
            }
        } catch {}
    }

    /// A session's own model and modes, from hermeshd.
    private func apply(_ th: Hermesh_Thread_V2_Thread) {
        machineID = th.machineID
        projectID = th.projectID
        selection = th.modelSelection
        mode = th.runtimeMode
        plan = th.interactionMode == .plan
        ceiling = th.maxRuntimeMode
    }

    private func loadProviders() async {
        guard !machineID.isEmpty, let live = model.api?.live else { return }
        var res: Hermesh_Thread_V2_ListProvidersResponse?
        do {
            res = try await live.listThreadProviders(machineID: machineID, projectID: projectID == scratchProject ? "" : projectID)
            if let res { ThreadChoices.keepProviders(machineID, res) }
            providersStale = false
        } catch {
            // The machine doesn't answer: what was last read there, if anything.
            res = ThreadChoices.lastProviders(machineID)
            providersStale = res != nil
        }
        guard let res else { return }
        providers = res.providers
        if res.maxRuntimeMode != .unspecified { ceiling = res.maxRuntimeMode }
        guard isNew else { return }
        if let last = ThreadChoices.lastSelection(machineID), let inst = providerInstance(res.providers, last.instanceID),
           providerUnusable(inst, projectAgents: []) == nil, inst.models.contains(where: { $0.slug == last.model }) {
            selection = last
        } else if providerInstance(res.providers, selection.instanceID).map({ providerUnusable($0, projectAgents: []) != nil }) ?? true,
                  let first = preferredProvider(res.providers) {
            selection = defaultSelection(first)
        }
    }

    private func send() {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, let live = model.api?.live else { return }
        if isNew {
            sending = true
            Task {
                defer { sending = false }
                var r = Hermesh_Thread_V2_LaunchThreadRequest()
                r.commandID = UUID().uuidString
                r.machineID = machineID
                r.projectID = projectID
                r.initialPrompt = text
                if !selection.instanceID.isEmpty { r.modelSelection = selection }
                r.runtimeMode = clamped
                r.interactionMode = plan ? .plan : .code
                r.workspace.mode = projectID != scratchProject && worktree ? .worktree : .inPlace
                do {
                    let res = try await live.launchThread(r)
                    ThreadChoices.remember(machine: machineID, project: projectID, selection: selection, mode: clamped)
                    draft = ""
                    model.open(.thread(res.thread.threadID))
                } catch { model.report(error) }
            }
            return
        }
        draft = ""
        var r = Hermesh_Thread_V2_DispatchMessageRequest()
        r.commandID = UUID().uuidString
        r.threadID = threadID
        r.text = text
        // After whatever runs, and at once when nothing does: hermeshd decides (§4.5).
        r.intent = .queue
        if !selection.instanceID.isEmpty { r.modelSelection = selection }
        r.runtimeMode = clamped
        r.interactionMode = plan ? .plan : .code
        let before = lastSend
        lastSend = Task {
            await before?.value
            do { _ = try await live.dispatchThreadMessage(r) } catch {
                model.report(error)
                if draft.isEmpty { draft = text }
            }
        }
    }

    private func stop() {
        Task { _ = try? await model.api?.live?.interruptThreadRun(threadID: threadID) }
    }

    private func update(_ build: (inout Hermesh_Thread_V2_UpdateThreadConfigRequest) -> Void) {
        var r = Hermesh_Thread_V2_UpdateThreadConfigRequest()
        r.threadID = threadID
        build(&r)
        Task { do { _ = try await model.api?.live?.updateThreadConfig(r) } catch { model.report(error) } }
    }

    private func respondNow(_ req: Hermesh_Thread_V2_RuntimeRequest, _ build: (inout Hermesh_Thread_V2_RespondRuntimeRequestRequest) -> Void) async {
        var r = Hermesh_Thread_V2_RespondRuntimeRequestRequest()
        r.commandID = UUID().uuidString
        r.threadID = threadID
        r.requestID = req.requestID
        build(&r)
        do { _ = try await model.api?.live?.respondThreadRuntimeRequest(r) } catch { model.report(error) }
    }

    private func manage(_ run: Hermesh_Thread_V2_Run, _ op: Hermesh_Thread_V2_QueueOperation) {
        var r = Hermesh_Thread_V2_ManageQueueRequest()
        r.commandID = UUID().uuidString
        r.threadID = threadID
        r.runID = run.runID
        r.operation = op
        Task { do { _ = try await model.api?.live?.manageThreadQueue(r) } catch { model.report(error) } }
    }
}

/// The model: one sheet, nothing nested (docs/thread-composer.md §4.4). A search over every provider; the
/// providers, and the chosen one's models; how hard it thinks, at the bottom. Choosing leaves it open, to set
/// the level next.
private struct ModelSheet: View {
    @Environment(\.dismiss) private var dismiss
    let providers: [Hermesh_Thread_V2_ProviderInstance]
    @Binding var selection: Hermesh_Thread_V2_ModelSelection
    let stale: Bool
    let machineName: String
    let projectAgents: [String]
    let changed: ((from: String, to: String)) -> Void
    @State private var shown = ""
    @State private var query = ""
    @State private var searching = false

    var body: some View {
        NavigationStack {
            List {
                if stale {
                    Text("\(machineName) is offline: these are the models last read there").font(.footnote).foregroundStyle(.secondary)
                }
                if providers.isEmpty {
                    Text("No models on \(machineName) yet").foregroundStyle(.secondary)
                }
                if !query.trimmingCharacters(in: .whitespaces).isEmpty {
                    let found = findModels(providers, query: query)
                    ForEach(found, id: \.provider.instanceID) { g in
                        Section(g.provider.displayName) { ForEach(g.models, id: \.slug) { m in row(g.provider, m) } }
                    }
                    if found.isEmpty { Text("No matching models").foregroundStyle(.secondary) }
                } else {
                    Section {
                        ScrollView(.horizontal, showsIndicators: false) {
                            HStack(spacing: 6) {
                                ForEach(providers, id: \.instanceID) { p in
                                    let why = providerUnusable(p, projectAgents: projectAgents)
                                    Button { shown = p.instanceID } label: {
                                        HStack(spacing: 6) {
                                            AgentLogo(id: p.instanceID, size: 18)
                                            Text(p.displayName)
                                            if let why { Text(reasonTitle(why)).foregroundStyle(.secondary) }
                                        }
                                        .font(.footnote).padding(.horizontal, 10).frame(height: 32)
                                        .background(p.instanceID == shown ? Palette.fill3 : Palette.fill2, in: Capsule())
                                    }
                                    .buttonStyle(.plain)
                                    .accessibilityIdentifier("provider-\(p.instanceID)")
                                }
                            }
                        }
                    }
                    if let inst = providerInstance(providers, shown) {
                        Section(inst.displayName) { ForEach(inst.models, id: \.slug) { m in row(inst, m) } }
                    }
                }
                if let r = reasoningOf(providers, selection) {
                    Section("Thinking") {
                        Picker("Thinking", selection: Binding(get: { r.value }, set: { selection = withReasoning(selection, id: r.id, value: $0) })) {
                            ForEach(r.choices, id: \.id) { c in Text(levelTitle(c.id, c.label)).tag(c.id) }
                        }
                        .pickerStyle(.segmented)
                        .accessibilityIdentifier("thinking")
                    }
                } else if let m = modelEntry(providers, selection) {
                    Section("Thinking") {
                        Text("\(m.shortName.isEmpty ? m.name : m.shortName) has no levels to choose").foregroundStyle(.secondary)
                    }
                }
            }
            .searchable(text: $query, isPresented: $searching, prompt: Text("Search models or providers"))
            .navigationTitle("Model")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() }.accessibilityIdentifier("model-done") } }
            .onAppear { shown = selection.instanceID.isEmpty ? providers.first?.instanceID ?? "" : selection.instanceID }
        }
        .presentationDetents([.medium, .large])
    }

    private func row(_ p: Hermesh_Thread_V2_ProviderInstance, _ m: Hermesh_Thread_V2_ModelCatalogEntry) -> some View {
        let why = providerUnusable(p, projectAgents: projectAgents)
        return Button {
            let r = selectModel(providers, from: selection, instanceID: p.instanceID, slug: m.slug)
            selection = r.selection
            if let c = r.changed { changed(c) }
            // Found by a search: back to its provider's models, where the level and Done are.
            if searching {
                shown = p.instanceID
                query = ""
                searching = false
            }
        } label: {
            HStack {
                Text(m.name)
                if !m.badge.isEmpty { Text("New").font(.caption2).foregroundStyle(.tint) }
                Spacer()
                if selection.instanceID == p.instanceID && selection.model == m.slug { Image(systemName: "checkmark").foregroundStyle(.tint) }
            }
        }
        .tint(.primary)
        .disabled(why != nil && why != .signedOut)
        .accessibilityIdentifier("model-\(m.slug)")
    }

    private func reasonTitle(_ why: ProviderUnusable) -> String {
        switch why {
        case .off: String(localized: "Turned off")
        case .notInstalled: String(localized: "Not installed")
        case .notForProject: String(localized: "Not set up for this project")
        case .signedOut: String(localized: "Signed out")
        }
    }

    private func levelTitle(_ id: String, _ label: String) -> String {
        switch id {
        case "minimal": String(localized: "Minimal")
        case "low": String(localized: "Low")
        case "medium": String(localized: "Medium")
        case "high": String(localized: "High")
        case "xhigh": String(localized: "Extra High")
        case "max": String(localized: "Max")
        default: label
        }
    }
}
