import HermeshKit
import HermeshProto
import HermeshSessions
import PhotosUI
import SwiftUI
import UniformTypeIdentifiers

// Unified session page on iPhone: a new session where the owner picks the machine,
// project, model, reasoning effort, and permission mode; and any session under way
// (started by Owner, Her, External Grant, or CLI) over the same interactive composer.

/// What a new session used last, and what each machine had when last read, kept on this iPhone.
enum SessionChoices {
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
    static func lastSelection(_ machine: String) -> Hermesh_Session_V1_ModelSelection? {
        mine(machine)?.selection.flatMap { try? Hermesh_Session_V1_ModelSelection(serializedBytes: $0) }
    }

    static func remember(machine: String, project: String, selection: Hermesh_Session_V1_ModelSelection, mode: Hermesh_Identity_V1_RuntimeMode) {
        var k = kept
        k.machineID = machine
        k.byMachine[machine] = Mine(projectID: project, selection: try? selection.serializedData(), mode: mode.rawValue)
        kept = k
    }

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

    static func keepProviders(_ machine: String, _ res: Hermesh_Session_V1_ListProvidersResponse) {
        var k = kept
        k.providers[machine] = try? res.serializedData()
        kept = k
    }

    static func lastProviders(_ machine: String) -> Hermesh_Session_V1_ListProvidersResponse? {
        kept.providers[machine].flatMap { try? Hermesh_Session_V1_ListProvidersResponse(serializedBytes: $0) }
    }
}

struct SessionPage: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var scenePhase
    /// The session shown; empty on a new one.
    let sessionID: String
    private let askedMachine: String
    private let askedProject: String

    @State private var machineID = ""
    @State private var projectID = scratchProject
    @State private var place = SessionPlace()
    @State private var branches: SessionBranches?
    @State private var pickingBranch = false
    @State private var selection = Hermesh_Session_V1_ModelSelection()
    @State private var mode: Hermesh_Identity_V1_RuntimeMode = .auto
    @State private var plan = false
    @State private var providers: [Hermesh_Session_V1_ProviderInstance] = []
    @State private var providersStale = false
    @State private var ceiling: Hermesh_Identity_V1_RuntimeMode = .unspecified
    @State private var draft = ""
    @State private var sending = false
    @State private var attached: [Attached] = []
    @State private var uploading = 0
    @State private var attaching = false
    @State private var pickingPhotos = false
    @State private var pickingFiles = false
    @State private var photos: [PhotosPickerItem] = []
    @State private var projection: Hermesh_Session_V1_SessionProjection?
    @State private var initialized = false
    @State private var following = false
    @State private var pickingModel = false
    @State private var atEnd = true
    @State private var spoken = 0
    @State private var position = ScrollPosition(edge: .bottom)
    @State private var scrollBox = ConversationScrollBox()
    @State private var renaming = false
    @State private var deleting = false
    @State private var showingBackgroundTasks = false
    /// Messages to a session go out one after another, in the order sent.
    @State private var lastSend: Task<Void, Never>?
    /// A new session's first message, from Send until its machine answers.
    @State private var starting: Starting?
    @FocusState private var focused: Bool

    init(machineID: String, projectID: String) {
        sessionID = ""
        askedMachine = machineID
        askedProject = projectID
    }

    init(sessionID: String) {
        self.sessionID = sessionID
        askedMachine = ""
        askedProject = ""
    }

    private var isNew: Bool { sessionID.isEmpty }
    private var listedSession: Session? { isNew ? nil : model.session(sessionID) }
    private var activeProjection: Hermesh_Session_V1_SessionProjection? {
        projection?.session.sessionID == sessionID ? projection : nil
    }
    private var currentSession: Session? {
        if !isNew, let p = activeProjection { return p.session }
        return listedSession
    }
    private var activeBgTasks: [Hermesh_Session_V1_BackgroundTask] { activeBackgroundTasks(of: activeProjection) }
    private var allBgTasks: [Hermesh_Session_V1_BackgroundTask] { backgroundTasks(of: activeProjection) }
    private var effectiveMachineID: String { isNew ? machineID : (currentSession?.machineID ?? machineID) }
    private var effectiveProjectID: String { isNew ? projectID : (currentSession?.projectID ?? projectID) }
    private var machine: Machine? { model.machines.first { $0.machineID == effectiveMachineID } }
    private var online: Bool { machine.map { $0.online || $0.local } ?? true }
    private var running: Bool {
        switch currentSession?.status ?? .idle {
        case .preparing, .running: true
        case .waitingInput: !(currentSession?.activeRunID.isEmpty ?? true)
        default: false
        }
    }

    private func projects(on id: String) -> (list: [Project], stale: Bool) {
        let live = model.projects.filter { $0.machineID == id }
        guard let m = model.machines.first(where: { $0.machineID == id }), !m.online, !m.local else { return (live, false) }
        let list = live.isEmpty ? SessionChoices.lastProjects(id) : live
        return (list, !list.isEmpty)
    }

    private var displaySession: Session? {
        guard var s = currentSession else { return nil }
        if !selection.instanceID.isEmpty || !selection.model.isEmpty {
            if let entry = modelEntry(providers, selection) {
                s.model = entry.shortName.isEmpty ? entry.name : entry.shortName
            } else if !selection.model.isEmpty {
                s.model = selection.model
            }
            if let r = reasoningOf(providers, selection) {
                s.effort = r.value
            }
        }
        if s.branch.isEmpty && s.hasWorkspaceStrategy {
            s.branch = s.workspaceStrategy.branch
        }
        if let f = sessionFailure(currentSession) { s.progress.lastFailure = f }
        return s
    }

    private func historyReady(_ s: Session) -> Bool {
        guard initialized, let p = projection, p.session.sessionID == sessionID else { return false }
        let targetSeq = listedSession?.progress.lastEventSeq ?? s.progress.lastEventSeq
        if p.session.progress.lastEventSeq >= targetSeq { return true }
        return !p.items.isEmpty && !reachable
    }

    var body: some View {
        Group {
            if isNew, let starting {
                startingPage(starting)
            } else if isNew {
                newSession.navigationTitle(String(localized: "New Session")).rootToolbar(sync: false)
            } else if let s = displaySession {
                session(s)
                    .onChange(of: s.pendingQuestion?.questionID) { _, asked in if asked != nil { Haptic.asks.play() } }
                    .onChange(of: s.status) { was, now in
                        guard [.queued, .preparing, .running, .waitingInput].contains(was), now == .idle || now == .crashed,
                              let cur = currentSession else { return }
                        model.sessionFiles.model(s.sessionID, api: model.api).refreshLive()
                        if cur.isFailed { Haptic.error.play() } else if !cur.progress.hasLastFailure { Haptic.success.play() }
                    }
            } else {
                PageLoader(text: "Reading the session…", bare: true).sessionBar(nil, renaming: $renaming, deleting: $deleting)
            }
        }
        .navigationBarTitleDisplayMode(.inline)
        .task(id: sessionID) { await start() }
        // Keep following while not background-paused; quick app switches (< 15 s) stay live with zero reconnect.
        .task(id: "\(sessionID)/\(!model.isBackgroundPaused)") {
            if !isNew && !model.isBackgroundPaused { await followSession() }
        }
        .onChange(of: reachable) {
            if reachable && !following { wakeRetry() }
        }
        .task(id: "\(effectiveMachineID)/\(effectiveProjectID)") { await loadProviders() }
        .onChange(of: model.projects) { SessionChoices.keepProjects(model.projects, machines: model.machines) }
        .onChange(of: model.addedProject) {
            guard let added = model.addedProject, added.machineID == machineID else { return }
            projectID = added.projectID
            place = SessionPlace(worktree: added.worktree)
            model.addedProject = nil
        }
        .onDisappear { if !isNew, let projection, projection.session.sessionID == sessionID { model.conversations.keep(sessionID, projection) } }
        .onChange(of: scenePhase) { _, phase in
            if phase == .background, !isNew, let projection, projection.session.sessionID == sessionID {
                model.conversations.keep(sessionID, projection)
            }
        }
        .sheet(isPresented: $pickingModel) {
            ModelSheet(providers: providers, selection: Binding(
                get: { selection },
                set: { next in
                    selection = next
                    if !isNew { update { $0.modelSelection = next } }
                }
            ), stale: providersStale, machineName: machine?.machineName ?? "",
            projectAgents: projects(on: effectiveMachineID).list.first { $0.projectID == effectiveProjectID }?.agents ?? [],
            levelTitle: levelTitle) { changed in
                model.show(String(localized: "\(modelTitle) has no “\(levelTitle(changed.from))” level; thinking is now “\(levelTitle(changed.to))”"))
            }
        }
        .sheet(isPresented: $attaching) {
            AttachSheet(mode: clamped, ceiling: ceiling, modeTitle: modeTitle, modeHint: modeHint, onMode: setMode) { photos in add(photos: photos) }
        }
        .sheet(isPresented: $pickingBranch) {
            if let branches { BranchSheet(place: $place, branches: branches) }
        }
        .sheet(isPresented: $showingBackgroundTasks) {
            BackgroundTasksSheet(tasks: allBgTasks, onStop: stopBackgroundTask)
        }
        // A new session's project's branches, read from its folder (only the owner's devices can): again as the page
        // opens or the project changes, since a branch switched on the machine isn't sent here.
        .task(id: "\(machineID)/\(projectID)/\(isNew)") { await loadBranches() }
    }

    // MARK: A new session

    private var newSession: some View {
        ScrollView {
            ask
                .frame(maxWidth: .infinity)
                .containerRelativeFrame(.vertical)
                .contentShape(Rectangle())
                .onTapGesture { focused = false }
        }
        .scrollBounceBehavior(.always)
        .scrollDismissesKeyboard(.immediately)
        .safeAreaInset(edge: .bottom) {
            VStack(spacing: 0) {
                ConnectionCapsule(machine: machine, linkUp: reachable)
                VStack(spacing: 6) {
                    placeRow
                    box
                }
            }
            .padding(.bottom, 4)
        }
    }

    private var ask: some View {
        let (here, stale) = projects(on: machineID)
        let projectTitle = projectID == scratchProject ? String(localized: "Temporary Workspace") : projectID
        return VStack(spacing: 18) {
            VStack(spacing: 2) {
                Text("What should we build in")
                HStack(spacing: 0) {
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
                    } label: {
                        Text(verbatim: projectTitle).lineLimit(1).underline(color: .secondary)
                    }
                    .tint(.primary)
                    .accessibilityIdentifier("thread-project")
                    Text("What should we build in · end")
                }
            }
            .font(.title.weight(.semibold))
            .multilineTextAlignment(.center)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("new-session-title")
            Menu {
                Section("Environment") {
                    ForEach(model.machines, id: \.machineID) { m in
                        let note: String? = !m.online && !m.local ? String(localized: "Offline · messages wait until it’s back") : nil
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
            } label: {
                HStack(spacing: 6) {
                    Image(systemName: m_icon)
                    Text(verbatim: machine?.machineName ?? "").lineLimit(1)
                    if !online { Circle().fill(.tertiary).frame(width: 6, height: 6) }
                    Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(.tertiary)
                }
                .font(.subheadline).foregroundStyle(.secondary)
            }
            .tint(.secondary)
            .accessibilityIdentifier("thread-machine")
        }
        .padding(.horizontal, 28)
    }

    // MARK: A session starting

    /// Shown from Send until the machine answers, as the session's page will be: its title, the time, the owner's message
    /// faded with where it is under it ("Sending…" once that takes 400 ms), or why it was not sent, with Try Again and Edit.
    private func startingPage(_ st: Starting) -> some View {
        let text = st.request.initialPrompt
        let note = st.refused.map { why in PromptNote(kind: .refused(why: why, retry: retryStart, edit: editStart)) } ?? PromptNote(kind: .sending)
        return ScrollView {
            VStack(alignment: .leading, spacing: 13) {
                if let line = turnTimeLine(st.at, previous: nil, now: model.now) {
                    Text(line).font(.caption).foregroundStyle(.tertiary).frame(maxWidth: .infinity)
                        .padding(.top, 8)
                }
                PromptBubble(text: text, sent: st.at, attachments: st.request.attachments, machineID: st.request.machineID,
                             faded: st.refused == nil, note: note) { EmptyView() }
            }
            .padding(.horizontal, 20)
            .padding(.top, 13).padding(.bottom, 21)
        }
        .defaultScrollAnchor(.bottom, for: .initialOffset)
        .keyboardAwayOnTap()
        .safeAreaInset(edge: .bottom) {
            VStack(spacing: 0) {
                ConnectionCapsule(machine: machine, linkUp: reachable)
                box
            }
            .padding(.bottom, 4)
        }
        .sessionBar(nil, renaming: $renaming, deleting: $deleting,
                    starting: .init(title: firstLine(text), place: "\(machine?.machineName ?? "")/\(projectID)"))
        .accessibilityIdentifier("session-starting")
    }

    private func firstLine(_ text: String) -> String {
        text.split(separator: "\n").map { $0.trimmingCharacters(in: .whitespaces) }.first { !$0.isEmpty } ?? String(localized: "New Session")
    }

    // MARK: A session under way

    /// Its conversation: the same rows, ending, and toolbar across every session; under it, what the agent waits on,
    /// then the box. A run that went well needs no line at its end.
    private func session(_ s: Session) -> some View {
        let activeProj = activeProjection
        let grant = model.grantLabel(s)
        let steps = groupSteps(sessionTranscript(activeProj))
        let files = model.sessionFiles.model(s.sessionID, api: model.api)
        return ScrollView {
            VStack(alignment: .leading, spacing: 13) {
                if s.status == .archived { ArchivedNote(session: s) }
                ForEach(Array(steps.enumerated()), id: \.element.id) { i, step in
                    StepView(step: step, session: s, previousTurn: previousTurn(steps, before: i))
                }
                .environment(\.sessionFiles, files)
                .environment(\.openURL, OpenURLAction { url in
                    guard let route = FileRefLink.route(url, session: s.sessionID) else { return .systemAction }
                    model.path.append(route)
                    return .handled
                })
                workLine(activeProj).frame(maxWidth: .infinity, alignment: .leading)
                if let f = sessionFailure(currentSession) {
                    FailedView(session: s, failure: f, grant: grant)
                } else if currentSession?.status == .idle, let last = steps.last {
                    // A turn that went well needs no line; one whose background tasks still run says it waits on them.
                    if case .item(.turnEnd) = last {} else if (currentSession?.activeBackgroundCount ?? 0) > 0 || activeBgTasks.contains(where: \.awaited) {
                        let count = max(Int(currentSession?.activeBackgroundCount ?? 0), activeBgTasks.filter(\.awaited).count)
                        let since = activeBgTasks.first(where: \.awaited).flatMap { dateOf($0.startedAt, set: $0.hasStartedAt) }
                        WorkLine(
                            text: Text("This reply finished; waiting for \(count) background task(s) to report back…"),
                            since: since
                        )
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
            }
            .padding(.horizontal, 20)
            .padding(.top, 13).padding(.bottom, 21)
            .background(ConversationScrollFinder(box: scrollBox))
        }
        .keyboardAwayOnTap()
        .followsEnd(id: sessionID, ready: historyReady(s), spoken: spoken, atEnd: $atEnd, position: $position, scrollBox: scrollBox) {
            if s.status != .archived {
                VStack(spacing: 0) {
                    ConnectionCapsule(machine: machine, linkUp: following && reachable)
                    VStack(spacing: 6) {
                        if let req = pendingRequest(of: activeProj) {
                            requestPanel(req, s)
                        } else if let q = s.pendingQuestion {
                            QuestionPanel(session: s, question: q) { answers, decline in
                                if await model.answer(s, answers: answers, decline: decline) { spoken += 1 }
                            }
                            .id(q.questionID)
                        }
                        backgroundBar
                        queue(activeProj)
                        box
                    }
                }
                .padding(.bottom, 4)
            }
        }
        .scrollDismissesKeyboard(.interactively)
        .sessionBar(s, renaming: $renaming, deleting: $deleting,
                    backgroundTasks: allBgTasks.isEmpty ? nil : .init(running: !activeBgTasks.isEmpty) { showingBackgroundTasks = true })
        .sessionDialogs(s, renaming: $renaming, deleting: $deleting)
    }

    @ViewBuilder private func requestPanel(_ req: Hermesh_Session_V1_RuntimeRequest, _ s: Session) -> some View {
        if req.kind == .userInput {
            QuestionPanel(session: s, question: sessionQuestion(req)) { answers, decline in
                await respondNow(req) { r in
                    r.decline = decline
                    r.decision = decline ? .decline : .acceptOnce
                    if !decline {
                        for a in answers {
                            r.answers[a.itemID] = .with { $0.selectedOptions = a.selected; $0.customText = a.text }
                        }
                    }
                }
            }
            .id(req.requestID)
        } else {
            SessionRequestPanel(session: s, request: req) { build in await respondNow(req, build) }
                .id(req.requestID)
        }
    }

    /// Active background tasks or long-lived daemons running after or alongside turns.
    @ViewBuilder private var backgroundBar: some View {
        let active = activeBgTasks
        if !active.isEmpty {
            let awaited = active.filter(\.awaited)
            let primary = awaited.first ?? active[0]
            let label = primary.title.isEmpty ? (primary.command.isEmpty ? primary.taskID : primary.command) : primary.title
            let summary: String = if awaited.count == 1 {
                String(localized: "Background task · \(label)")
            } else if awaited.count > 1 {
                String(localized: "\(awaited.count) background tasks running")
            } else if active.count == 1 {
                String(localized: "Background service · \(label)")
            } else {
                String(localized: "\(active.count) background services running")
            }
            HStack(spacing: 8) {
                Button {
                    showingBackgroundTasks = true
                } label: {
                    HStack(spacing: 8) {
                        if !awaited.isEmpty {
                            MovingMark(size: 16, motion: .seq)
                        } else {
                            Image(systemName: "bolt.horizontal").font(.footnote).foregroundStyle(.secondary)
                        }
                        Text(verbatim: summary).font(.subheadline).lineLimit(1).foregroundStyle(.primary)
                        if let since = dateOf(primary.startedAt, set: primary.hasStartedAt) {
                            Text(since, style: .timer).font(.caption.monospacedDigit()).foregroundStyle(.tertiary)
                        }
                        Spacer(minLength: 0)
                        Text("Details").font(.footnote).foregroundStyle(.secondary)
                    }
                }
                .buttonStyle(.plain)
                Button(active.count > 1 ? String(localized: "Stop All") : String(localized: "Stop")) {
                    stopBackgroundTask(active.count == 1 ? active[0].taskID : "")
                }
                .font(.footnote.weight(.medium))
                .tint(.red)
                .accessibilityIdentifier("thread-background-stop")
            }
            .padding(.horizontal, 14).padding(.vertical, 10)
            .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
            .padding(.horizontal, 20)
            .accessibilityIdentifier("thread-background-bar")
        }
    }

    /// What waits for the next turn, in a card over the box: each can steer the turn now, or go.
    @ViewBuilder private func queue(_ activeProj: Hermesh_Session_V1_SessionProjection?) -> some View {
        let queued = queuedRuns(of: activeProj)
        if !queued.isEmpty {
            VStack(alignment: .leading, spacing: 4) {
                Text("Queue (\(queued.count))").font(.caption).foregroundStyle(.secondary)
                ForEach(Array(queued.enumerated()), id: \.element.runID) { i, run in
                    HStack {
                        Text(verbatim: run.prompt.isEmpty ? run.attachments.map(\.name).joined(separator: ", ") : run.prompt).font(.subheadline).lineLimit(1)
                        Spacer()
                        Button("Steer Now") { manage(run, .promoteToSteer) }.font(.footnote).tint(.accentColor)
                        Button("Remove", systemImage: "xmark") { manage(run, .cancel) }.labelStyle(.iconOnly).font(.footnote).tint(.secondary)
                    }
                    .accessibilityIdentifier("thread-queued-\(i)")
                }
            }
            .padding(.horizontal, 14).padding(.vertical, 10)
            .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
            .padding(.horizontal, 20)
        }
    }

    @ViewBuilder private func workLine(_ activeProj: Hermesh_Session_V1_SessionProjection?) -> some View {
        switch currentSession?.status {
        case .preparing: WorkLine(text: Text("Starting…"))
        case .queued where currentSession?.offlineQueued != true: WorkLine(text: Text(statusTitle(.queued)))
        case .running:
            let run = activeProj?.runs.first { $0.runID == currentSession?.activeRunID }
            let text = switch sessionWork(activeProj) {
            case .tool: Text("Running tools…")
            case .thinking: Text("Thinking…")
            case .replying: Text("Replying…")
            case .working: Text("Working…")
            }
            WorkLine(text: text, since: run.flatMap { dateOf($0.startedAt, set: $0.hasStartedAt) })
        default: EmptyView()
        }
    }

    // MARK: The box

    private var boxOpen: Bool {
        focused || !draft.isEmpty || !attached.isEmpty || attaching || pickingModel
    }

    private var box: some View {
        let ready = !sending && starting == nil && uploading == 0 && (!draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !attached.isEmpty)
        let open = boxOpen
        let ends = HStack(spacing: 8) {
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
        return VStack(alignment: .leading, spacing: 6) {
            if !attached.isEmpty || uploading > 0 {
                ScrollView(.horizontal) {
                    HStack(spacing: 6) {
                        ForEach(attached) { a in
                            if let picture = a.picture {
                                ZStack(alignment: .topTrailing) {
                                    Image(uiImage: picture).resizable().scaledToFill().frame(width: 56, height: 56)
                                        .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
                                    Button { attached.removeAll { $0.id == a.id } } label: {
                                        Image(systemName: "xmark.circle.fill").symbolRenderingMode(.palette).foregroundStyle(.white, .black.opacity(0.55))
                                    }
                                    .padding(3)
                                    .accessibilityLabel(Text("Remove \(a.ref.name)"))
                                }
                            } else {
                                HerChip(name: a.ref.name) { attached.removeAll { $0.id == a.id } }
                            }
                        }
                        ForEach(0 ..< uploading, id: \.self) { _ in
                            ProgressView().frame(width: 56, height: 56)
                                .background(Palette.fill2, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                                .accessibilityIdentifier("thread-attach-loading")
                        }
                    }
                    .padding(.horizontal, 6).padding(.top, 6)
                }
                .scrollIndicators(.hidden)
                .accessibilityIdentifier("thread-attached")
            }
            HStack(alignment: .bottom, spacing: 8) {
                if !open { plusButton }
                TextField(isNew && starting == nil ? String(localized: "Describe what to build, debug or plan") : running ? String(localized: "Say more; it goes to the next turn") : String(localized: "Keep going"),
                          text: $draft, axis: .vertical)
                    .lineLimit(open ? 2 ... 6 : 1 ... 6)
                    .focused($focused)
                    .padding(.vertical, 8).padding(.leading, open ? 10 : 0)
                    .accessibilityIdentifier("thread-composer")
                if !open { ends }
            }
            if open {
                HStack(spacing: 6) {
                    plusButton
                    modelButton
                    Spacer(minLength: 0)
                    ends
                }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("thread-bar")
            }
        }
        .padding(6)
        .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 24, style: .continuous))
        .keyboardAwayOnFlickDown()
        .animation(.snappy(duration: 0.22), value: open)
        .padding(.horizontal, 12)
        .photosPicker(isPresented: $pickingPhotos, selection: $photos, maxSelectionCount: 10, matching: .images)
        .onChange(of: photos) {
            let picked = photos
            guard !picked.isEmpty else { return }
            photos = []
            uploading += picked.count
            Task {
                for item in picked {
                    if let data = try? await item.loadTransferable(type: Data.self), let jpeg = HerComposer.jpeg(data) {
                        await upload(name: String(localized: "Photo") + ".jpg", mime: "image/jpeg", data: jpeg, picture: UIImage(data: jpeg))
                    }
                    uploading -= 1
                }
            }
        }
        .fileImporter(isPresented: $pickingFiles, allowedContentTypes: [.item], allowsMultipleSelection: true) { result in
            guard case .success(let urls) = result else { return }
            uploading += urls.count
            Task {
                for url in urls {
                    let scoped = url.startAccessingSecurityScopedResource()
                    let data = try? Data(contentsOf: url)
                    if scoped { url.stopAccessingSecurityScopedResource() }
                    if let data {
                        let mime = UTType(filenameExtension: url.pathExtension)?.preferredMIMEType ?? "application/octet-stream"
                        let picture = (mime.lowercased().hasPrefix("image/") || fileView(url.lastPathComponent) == .image) ? UIImage(data: data) : nil
                        await upload(name: url.lastPathComponent, mime: mime, data: data, picture: picture)
                    } else {
                        model.show(String(localized: "Couldn’t read \(url.lastPathComponent)"))
                    }
                    uploading -= 1
                }
            }
        }
    }

    private func add(photos: Bool) {
        attaching = false
        Task {
            try? await Task.sleep(for: .milliseconds(400))
            if photos { pickingPhotos = true } else { pickingFiles = true }
        }
    }

    private func upload(name: String, mime: String, data: Data, picture: UIImage?) async {
        guard let api = model.api else { return }
        do {
            let ref = try await api.uploadBlob(machineID: effectiveMachineID, name: name, mimeType: mime, data: data).attachment
            SessionAttachmentStore.shared.keep(sha256: ref.blobSha256, name: name, data: data, picture: picture)
            if !attached.contains(where: { $0.id == ref.blobSha256 }) { attached.append(Attached(ref: ref, picture: picture)) }
        } catch { model.report(error) }
    }

    private var modelTitle: String {
        guard let m = modelEntry(providers, selection) else { return String(localized: "Model") }
        return m.shortName.isEmpty ? m.name : m.shortName
    }

    private func levelTitle(_ id: String, _ label: String = "") -> String {
        switch id {
        case "minimal": String(localized: "Minimal")
        case "low": String(localized: "Low")
        case "medium": String(localized: "Medium")
        case "high": String(localized: "High")
        case "xhigh": String(localized: "Extra High")
        case "max": String(localized: "Max")
        default: label.isEmpty ? id : label
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

    private var plusButton: some View {
        Button {
            focused = false
            attaching = true
        } label: {
            Image(systemName: "plus").font(.body.weight(.medium)).frame(width: 36, height: 36)
                .background(Palette.fill2, in: .circle)
        }
        .tint(.primary)
        .accessibilityLabel(Text("Add Photos or Files"))
        .accessibilityIdentifier("thread-attach")
        .disabled(model.isDemo)
    }

    private var modelButton: some View {
        Button {
            focused = false
            pickingModel = true
        } label: {
            HStack(spacing: 6) {
                if let inst = providerInstance(providers, selection.instanceID) { AgentLogo(id: inst.instanceID, size: 16) }
                Text(verbatim: modelTitle).lineLimit(1)
                Image(systemName: "chevron.down").font(.caption2.weight(.semibold)).foregroundStyle(.tertiary)
            }
            .font(.subheadline)
            .padding(.horizontal, 11).frame(height: 36)
            .background(Palette.fill2, in: Capsule())
        }
        .tint(.primary)
        .accessibilityIdentifier("thread-model")
    }

    @ViewBuilder private var placeRow: some View {
        let project = projects(on: machineID).list.first { $0.projectID == projectID }
        if project?.repo == true || project?.workspace == .worktree {
            HStack(spacing: 6) {
                Menu {
                    Section("Working copy") {
                        Button { place.worktree = false } label: {
                            Label { Text("Current checkout"); Text("Works on the project’s own folder") } icon: { if !place.worktree { Image(systemName: "checkmark") } }
                        }
                        Button { place.worktree = true } label: {
                            Label { Text("Separate worktree"); Text("A branch of its own; your files are left alone") } icon: { if place.worktree { Image(systemName: "checkmark") } }
                        }
                    }
                } label: { chip(icon: place.worktree ? "arrow.triangle.branch" : "folder", place.worktree ? String(localized: "Separate worktree") : String(localized: "Current checkout")) }
                    .tint(.primary)
                    .accessibilityIdentifier("thread-worktree")
                if let branches {
                    let name = place.chosen(branches)
                    Button {
                        focused = false
                        pickingBranch = true
                    } label: {
                        HStack(spacing: 4) {
                            Image(systemName: "point.topleft.down.to.point.bottomright.curvepath")
                            if place.worktree {
                                Text("From \(Text(verbatim: (place.fromRemote ? "origin/" : "") + name).font(.footnote.monospaced()).foregroundStyle(.primary))").lineLimit(1)
                            } else {
                                Text(verbatim: name).font(.footnote.monospaced()).lineLimit(1)
                            }
                            Image(systemName: "chevron.right").font(.caption2.weight(.semibold)).foregroundStyle(.tertiary)
                        }
                        .font(.footnote).foregroundStyle(.secondary)
                        .padding(.horizontal, 10).frame(height: 30)
                        .background(Palette.fill, in: Capsule())
                    }
                    .tint(.primary)
                    .accessibilityIdentifier("thread-branch")
                }
                Spacer(minLength: 0)
            }
            .padding(.horizontal, 18)
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
        pick(project: projectOnMachine(projects(on: id).list, machineID: id, current: projectID, lastUsed: SessionChoices.mine(id)?.projectID), in: projects(on: id).list)
    }

    private func pick(project id: String, in list: [Project]) {
        projectID = id
        place = SessionPlace(worktree: list.first { $0.projectID == id }?.workspace == .worktree)
    }

    private func setMode(_ m: Hermesh_Identity_V1_RuntimeMode) {
        mode = m
        guard !isNew else { return }
        update { $0.runtimeMode = m }
    }

    // MARK: Talking to hermeshd

    private func start() async {
        initialized = false
        atEnd = true
        if isNew {
            projection = nil
            let known = { (id: String) in !id.isEmpty && model.machines.contains { $0.machineID == id } }
            let mid = [askedMachine, SessionChoices.lastMachine ?? ""].first(where: known) ?? model.machines.first(where: \.local)?.machineID ?? model.machines.first?.machineID ?? ""
            machineID = mid
            let list = projects(on: mid).list
            pick(project: askedProject.isEmpty ? projectOnMachine(list, machineID: mid, current: "", lastUsed: SessionChoices.mine(mid)?.projectID) : askedProject, in: list)
            if let m = SessionChoices.mine(mid)?.mode, let saved = Hermesh_Identity_V1_RuntimeMode(rawValue: m) { mode = saved }
            SessionChoices.keepProjects(model.projects, machines: model.machines)
            initialized = true
            return
        }
        if let cached = model.conversations.projection(sessionID) {
            apply(cached.session)
            projection = cached
        } else {
            projection = nil
        }
        initialized = true
    }

    @State private var retrySleep: Task<Void, Never>?

    private func wakeRetry() {
        retrySleep?.cancel()
        retrySleep = nil
    }

    private func waitBeforeRetry(seconds: Int) async {
        let sleep = Task<Void, Never> { try? await Task.sleep(for: .seconds(seconds)) }
        retrySleep = sleep
        defer { if retrySleep == sleep { retrySleep = nil } }
        await withTaskCancellationHandler {
            await sleep.value
        } onCancel: {
            sleep.cancel()
        }
    }

    /// A session's projection, then its events as they come, until the page goes: renders local SQLite cache in 0 ms
    /// when available, then subscribes incrementally from `lastEventSeq` (confirming `following` in 1 RTT with no
    /// artificial 1 s sleep). Retries in 1..30 s on disconnect, waking immediately when `reachable` becomes true.
    private func followSession() async {
        guard let api = model.api else { return }
        var applied = projection != nil
        // Opened right after Send on what it knew: read the whole session once, not only what comes after it.
        var seeded = model.seededSessions.remove(sessionID) != nil
            || (projection?.items.contains(where: { $0.itemID == "starting" }) == true)
        if projection == nil,
           let cached = try? await api.getSession(sessionID, cachedOnly: true),
           cached.hasProjection, cached.projection.hasSession {
            applied = true
            apply(cached.projection.session)
            projection = cached.projection
        }
        var failures = 0
        while !Task.isCancelled {
            do {
                if projection == nil || failures > 0 || seeded {
                    let first = try await api.getSession(sessionID)
                    if !applied && first.projection.hasSession {
                        applied = true
                        apply(first.projection.session)
                    }
                    if !first.fromCache {
                        seeded = false
                        projection = first.projection
                        following = true
                        failures = 0
                    } else if projection == nil || (projection?.items.isEmpty == true && !first.projection.items.isEmpty) {
                        projection = first.projection
                    }
                }
                if seeded {
                    // GetSession fell back to local cache: keep the seeded message visible and retry
                    // GetSession until the machine answers instead of subscribing from a stale seq.
                    throw APIError(code: "unavailable", message: "waiting for session from machine")
                }
                let afterSeq = (projection?.items.isEmpty == true) ? 0 : (projection?.session.progress.lastEventSeq ?? 0)
                for try await ev in api.subscribeSession(sessionID, afterSeq: afterSeq) {
                    following = true
                    failures = 0
                    projection = applySessionEvent(ev, to: projection)
                    if ev.hasSession && ev.session.interactionMode != .unspecified {
                        plan = ev.session.interactionMode == .plan
                    }
                }
            } catch let e as APIError where e.code == "not_found" {
                if !Task.isCancelled {
                    model.path.removeAll { $0 == .session(sessionID) }
                    if case .session(let id) = model.root, id == sessionID { model.root = .overview }
                }
                return
            } catch {
                if projection == nil, let s = model.session(sessionID) {
                    apply(s)
                    projection = .with { $0.session = s }
                }
            }
            if following { failures = 0 }
            following = false
            guard !Task.isCancelled else { return }
            failures += 1
            await waitBeforeRetry(seconds: min(30, 1 << min(5, failures - 1)))
        }
    }

    private var reachable: Bool {
        guard let m = machine else { return true }
        return m.local || (model.status.relayConnected && m.online && m.unreachable.isEmpty)
    }

    private func apply(_ s: Session) {
        machineID = s.machineID
        projectID = s.projectID
        if s.hasModelSelection { selection = s.modelSelection }
        if s.runtimeMode != .unspecified { mode = s.runtimeMode }
        plan = s.interactionMode == .plan
        if s.maxRuntimeMode != .unspecified { ceiling = s.maxRuntimeMode }
    }

    private func loadBranches() async {
        branches = nil
        guard isNew, online, let path = projects(on: machineID).list.first(where: { $0.projectID == projectID && $0.repo })?.path,
              let res = try? await model.api?.folders(machine: machineID, path: path), res.repo else { return }
        branches = SessionBranches(current: res.branch, default: res.defaultBranch, all: res.branches, inWorktrees: res.worktreeBranches)
    }

    private func loadProviders() async {
        let mid = effectiveMachineID
        let pid = effectiveProjectID
        guard !mid.isEmpty, let api = model.api else { return }
        var res: Hermesh_Session_V1_ListProvidersResponse?
        do {
            res = try await api.listProviders(machineID: mid, projectID: pid == scratchProject ? "" : pid)
            if let res { SessionChoices.keepProviders(mid, res) }
            providersStale = false
        } catch {
            res = SessionChoices.lastProviders(mid)
            providersStale = res != nil
        }
        guard let res else { return }
        providers = res.providers
        if res.maxRuntimeMode != .unspecified { ceiling = res.maxRuntimeMode }
        guard isNew else { return }
        if let last = SessionChoices.lastSelection(mid), let inst = providerInstance(res.providers, last.instanceID),
           providerUnusable(inst, projectAgents: []) == nil, inst.models.contains(where: { $0.slug == last.model }) {
            selection = last
        } else if providerInstance(res.providers, selection.instanceID).map({ providerUnusable($0, projectAgents: []) != nil }) ?? true,
                  let first = preferredProvider(res.providers) {
            selection = defaultSelection(first)
        }
    }

    private func send() {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty || !attached.isEmpty, let api = model.api else { return }
        let files = attached
        if isNew {
            do {
                var r = Hermesh_Session_V1_StartSessionRequest()
                r.commandID = UUID().uuidString
                r.machineID = machineID
                r.projectID = projectID
                r.initialPrompt = text
                r.locale = AppLocale.current
                r.attachments = files.map(\.ref)
                if !selection.instanceID.isEmpty { r.modelSelection = selection }
                r.runtimeMode = clamped
                r.interactionMode = plan ? .plan : .code
                r.workspace = projectID == scratchProject ? .with { $0.mode = .inPlace } : place.workspace(branches)
                let st = Starting(request: r, files: files)
                draft = ""
                attached = []
                starting = st
                Task { await begin(st, api) }
            }
            return
        }
        draft = ""
        attached = []
        spoken += 1
        var r = Hermesh_Session_V1_DispatchMessageRequest()
        r.commandID = UUID().uuidString
        r.sessionID = sessionID
        r.text = text
        r.locale = AppLocale.current
        r.attachments = files.map(\.ref)
        r.intent = .queue
        if !selection.instanceID.isEmpty { r.modelSelection = selection }
        r.runtimeMode = clamped
        r.interactionMode = plan ? .plan : .code
        let before = lastSend
        lastSend = Task {
            await before?.value
            do {
                let res = try await api.dispatchMessage(r)
                if res.queuedOffline {
                    var next = projection ?? .init()
                    if res.hasSession { next.session = res.session }
                    if res.hasRun, !next.runs.contains(where: { $0.runID == res.run.runID }) {
                        next.runs.append(res.run)
                    }
                    projection = next
                }
            } catch {
                model.report(error)
                if draft.isEmpty { draft = text }
                if attached.isEmpty { attached = files }
            }
        }
    }

    /// Starts the session: once its machine answers (or the core keeps it for a machine offline), its page opens with the
    /// message already in it; refused, the message stays with why. The same command id each time, so a retry after an
    /// answer lost on the way makes no second session.
    private func begin(_ st: Starting, _ api: AppAPI) async {
        sending = true
        defer { sending = false }
        let began = Date.now
        do {
            let res = try await api.startSession(st.request)
            SessionChoices.remember(machine: st.request.machineID, project: st.request.projectID, selection: selection, mode: clamped)
            // "Sending…", once shown (at 400 ms), stays half a second.
            let took = Date.now.timeIntervalSince(began)
            if took > 0.4 && took < 0.9 { try? await Task.sleep(for: .seconds(0.9 - took)) }
            let id = res.session.sessionID
            model.conversations.keep(id, startedProjection(res, st))
            model.seededSessions.insert(id)
            starting = nil
            model.open(.session(id))
        } catch {
            guard starting?.request.commandID == st.request.commandID else { return }
            starting?.refused = (error as? APIError)?.message ?? error.localizedDescription
        }
    }

    private func retryStart() {
        guard var st = starting, let api = model.api else { return }
        st.refused = nil
        starting = st
        Task { await begin(st, api) }
    }

    private func editStart() {
        guard let st = starting else { return }
        draft = st.request.initialPrompt
        attached = st.files
        starting = nil
    }

    /// What the session's page shows until it reads the session: the session, its first run, and the message in it.
    private func startedProjection(_ res: Hermesh_Session_V1_StartSessionResponse, _ st: Starting) -> Hermesh_Session_V1_SessionProjection {
        var p = Hermesh_Session_V1_SessionProjection()
        p.session = res.session
        p.session.progress.lastEventSeq = 0
        if res.hasInitialRun { p.runs = [res.initialRun] }
        p.items = [.with {
            $0.itemID = "starting"
            $0.sessionID = res.session.sessionID
            $0.runID = res.initialRun.runID
            $0.seq = 1
            $0.kind = .userMessage
            $0.status = "completed"
            $0.text = st.request.initialPrompt
            $0.attachments = st.request.attachments
            $0.createdAt = .init(date: st.at)
        }]
        return p
    }

    private func stop() {
        Task { _ = try? await model.api?.interrupt(sessionID) }
    }

    private func stopBackgroundTask(_ taskID: String = "") {
        Task {
            do { _ = try await model.api?.stopBackgroundTask(sessionID, taskID: taskID) }
            catch { model.report(error) }
        }
    }

    private func update(_ build: (inout Hermesh_Session_V1_UpdateSessionRequest) -> Void) {
        var r = Hermesh_Session_V1_UpdateSessionRequest()
        r.commandID = UUID().uuidString
        r.sessionID = sessionID
        build(&r)
        Task {
            do {
                let res = try await model.api?.updateSession(r)
                if let s = res?.session, projection != nil { projection?.session = s }
            } catch { model.report(error) }
        }
    }

    private func respondNow(_ req: Hermesh_Session_V1_RuntimeRequest, _ build: (inout Hermesh_Session_V1_RespondRuntimeRequestRequest) -> Void) async {
        var r = Hermesh_Session_V1_RespondRuntimeRequestRequest()
        r.commandID = UUID().uuidString
        r.sessionID = sessionID
        r.requestID = req.requestID
        build(&r)
        do {
            _ = try await model.api?.respondRuntimeRequest(r)
            spoken += 1
        } catch { model.report(error) }
    }

    private func manage(_ run: Hermesh_Session_V1_Run, _ op: Hermesh_Session_V1_QueueOperation) {
        var r = Hermesh_Session_V1_ManageQueueRequest()
        r.commandID = UUID().uuidString
        r.sessionID = sessionID
        r.runID = run.runID
        r.operation = op
        if op == .promoteToSteer { spoken += 1 }
        Task { do { _ = try await model.api?.manageQueue(r) } catch { model.report(error) } }
    }
}

private struct WorkLine: View {
    let text: Text
    var since: Date?

    var body: some View {
        HStack(spacing: 8) {
            MovingMark(size: 18, motion: .seq)
            text.foregroundStyle(.secondary)
            if let since { Text(since, style: .timer).monospacedDigit().foregroundStyle(.tertiary) }
        }
        .font(.footnote).lineLimit(1)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("thread-working")
    }
}

/// A new session's first message on its way: what was asked, the files with it, when, and why it was refused if it was.
private struct Starting {
    let request: Hermesh_Session_V1_StartSessionRequest
    let files: [Attached]
    let at = Date.now
    var refused: String?
}

private struct Attached: Identifiable {
    let ref: Hermesh_Session_V1_AttachmentRef
    let picture: UIImage?
    var id: String { ref.blobSha256 }
}

struct SessionBranches: Equatable {
    var current: String
    var `default`: String
    var all: [String]
    var inWorktrees: [String]
}

struct SessionPlace: Equatable {
    var worktree = true
    var base = ""
    var on = ""
    var fromRemote = false

    init(worktree: Bool = true) { self.worktree = worktree }

    func chosen(_ b: SessionBranches) -> String {
        worktree ? (base.isEmpty ? (b.default.isEmpty ? b.current : b.default) : base) : (on.isEmpty ? b.current : on)
    }

    func workspace(_ b: SessionBranches?) -> Hermesh_Session_V1_WorkspaceStrategy {
        .with {
            if worktree {
                $0.mode = .worktree
                $0.baseRef = base.isEmpty ? b?.default ?? "" : base
                $0.fromRemote = fromRemote
            } else {
                $0.mode = .inPlace
                if !on.isEmpty, on != b?.current { $0.branch = on }
            }
        }
    }
}

private struct AttachSheet: View {
    @Environment(\.dismiss) private var dismiss
    let mode: Hermesh_Identity_V1_RuntimeMode
    let ceiling: Hermesh_Identity_V1_RuntimeMode
    let modeTitle: (Hermesh_Identity_V1_RuntimeMode) -> String
    let modeHint: (Hermesh_Identity_V1_RuntimeMode) -> String
    let onMode: (Hermesh_Identity_V1_RuntimeMode) -> Void
    let add: (_ photos: Bool) -> Void

    var body: some View {
        List {
            Section {
                HStack(spacing: 10) {
                    tile("Photos", "photo.on.rectangle", id: "thread-attach-photos") { add(true) }
                    tile("Files", "folder", id: "thread-attach-files") { add(false) }
                }
                .listRowInsets(EdgeInsets())
                .listRowBackground(Color.clear)
            }
            Section("Permission mode") {
                ForEach(orderedRuntimeModes, id: \.rawValue) { m in
                    let allowed = isRuntimeModeAllowed(m, ceiling: ceiling)
                    Button { onMode(m) } label: {
                        HStack(alignment: .firstTextBaseline, spacing: 10) {
                            Image(systemName: "checkmark").font(.subheadline.weight(.semibold)).foregroundStyle(Color.accentColor).opacity(clampRuntimeMode(mode, ceiling: ceiling) == m ? 1 : 0)
                            VStack(alignment: .leading, spacing: 2) {
                                Text(modeTitle(m)).foregroundStyle(.primary)
                                Text(allowed ? modeHint(m) : String(localized: "Above the limit this machine or grant allows")).font(.footnote).foregroundStyle(.secondary)
                            }
                        }
                    }
                    .tint(.primary)
                    .disabled(!allowed)
                    .accessibilityIdentifier("thread-mode-\(m.rawValue)")
                }
            }
        }
        .contentMargins(.top, 18, for: .scrollContent)
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
        .accessibilityIdentifier("thread-attach-sheet")
    }

    private func tile(_ title: LocalizedStringKey, _ icon: String, id: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            VStack(alignment: .leading, spacing: 10) {
                Image(systemName: icon).font(.title3).foregroundStyle(.tint)
                Text(title).foregroundStyle(.primary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(14)
            .background(Color(uiColor: .secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier(id)
    }
}

private struct BranchSheet: View {
    @Environment(\.dismiss) private var dismiss
    @Binding var place: SessionPlace
    let branches: SessionBranches
    @State private var query = ""

    var body: some View {
        let chosen = place.chosen(branches)
        let q = query.trimmingCharacters(in: .whitespaces).lowercased()
        let order = { (n: String) in n == branches.current ? 0 : n == branches.default ? 1 : 2 }
        let list = ([branches.current] + branches.all.filter { $0 != branches.current })
            .filter { !$0.isEmpty && !$0.hasPrefix("hermesh/") && (q.isEmpty || $0.lowercased().contains(q)) }
            .sorted { order($0) != order($1) ? order($0) < order($1) : $0 < $1 }
        NavigationStack {
            List {
                if place.worktree {
                    Section {
                        Toggle(isOn: $place.fromRemote) {
                            VStack(alignment: .leading, spacing: 2) {
                                Text("Start from remote")
                                Text("Fetches the latest commits first, then branches from them").font(.footnote).foregroundStyle(.secondary)
                            }
                        }
                        .accessibilityIdentifier("branch-remote")
                    }
                } else if chosen != branches.current {
                    Text("The project folder switches to this branch before the session starts; it can’t while there are uncommitted changes.")
                        .font(.footnote).foregroundStyle(.orange)
                        .accessibilityIdentifier("branch-switch-note")
                }
                Section {
                    ForEach(list, id: \.self) { n in
                        let tags = [n == branches.current ? String(localized: "Current") : nil, n == branches.default ? String(localized: "Default") : nil,
                                    branches.inWorktrees.contains(n) ? String(localized: "Worktree") : nil].compactMap { $0 }
                        let blocked = !place.worktree && branches.inWorktrees.contains(n) && n != branches.current
                        Button {
                            if place.worktree { place.base = n } else { place.on = n }
                            dismiss()
                        } label: {
                            HStack(spacing: 12) {
                                Image(systemName: "point.topleft.down.to.point.bottomright.curvepath").foregroundStyle(.secondary)
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(verbatim: n).font(.body.monospaced()).foregroundStyle(.primary).lineLimit(1).truncationMode(.middle)
                                    if blocked {
                                        Text("Checked out in another worktree").font(.caption).foregroundStyle(.secondary)
                                    } else if !tags.isEmpty {
                                        Text(verbatim: tags.joined(separator: " · ")).font(.caption).foregroundStyle(.secondary)
                                    }
                                }
                                Spacer(minLength: 0)
                                if n == chosen { Image(systemName: "checkmark").foregroundStyle(.tint) }
                            }
                        }
                        .tint(.primary)
                        .disabled(blocked)
                        .accessibilityIdentifier("branch-item-\(n)")
                    }
                    if list.isEmpty { Text("No matching branches").foregroundStyle(.secondary) }
                }
            }
            .searchable(text: $query, prompt: Text("Search branches"))
            .navigationTitle(place.worktree ? String(localized: "Base branch") : String(localized: "Branch to work on"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() }.accessibilityIdentifier("branch-done") } }
        }
        .presentationDetents([.medium, .large])
    }
}

private struct ModelSheet: View {
    @Environment(\.dismiss) private var dismiss
    let providers: [Hermesh_Session_V1_ProviderInstance]
    @Binding var selection: Hermesh_Session_V1_ModelSelection
    let stale: Bool
    let machineName: String
    let projectAgents: [String]
    let levelTitle: (String, String) -> String
    let changed: ((from: String, to: String)) -> Void
    @State private var query = ""

    var body: some View {
        let usable = providers.filter { p in providerUnusable(p, projectAgents: projectAgents).map { $0 == .signedOut } ?? true }
        let groups: [(provider: Hermesh_Session_V1_ProviderInstance, models: [Hermesh_Session_V1_ModelCatalogEntry])] =
            query.trimmingCharacters(in: .whitespaces).isEmpty ? usable.map { ($0, $0.models) } : findModels(usable, query: query).map { ($0.provider, $0.models) }
        NavigationStack {
            List {
                if stale {
                    Text("\(machineName) is offline: these are the models last read there").font(.footnote).foregroundStyle(.secondary)
                }
                if usable.isEmpty {
                    Text("No models on \(machineName) yet").foregroundStyle(.secondary)
                }
                if query.isEmpty, let r = reasoningOf(providers, selection), r.choices.count > 1 {
                    Section {
                        ThinkingSlider(levels: r.choices.map { ($0.id, levelTitle($0.id, $0.label)) },
                                       value: Binding(get: { r.value }, set: { selection = withReasoning(selection, id: r.id, value: $0) }))
                    }
                }
                ForEach(groups, id: \.provider.instanceID) { g in
                    Section {
                        ForEach(g.models, id: \.slug) { m in row(g.provider, m) }
                    } header: {
                        HStack(spacing: 6) {
                            AgentLogo(id: g.provider.instanceID, size: 16)
                            Text(g.provider.displayName)
                            if providerUnusable(g.provider, projectAgents: projectAgents) == .signedOut { Text(verbatim: "· " + String(localized: "Signed out")) }
                        }
                        .accessibilityIdentifier("provider-\(g.provider.instanceID)")
                    }
                }
                if !usable.isEmpty && groups.isEmpty { Text("No matching models").foregroundStyle(.secondary) }
            }
            .searchable(text: $query, prompt: Text("Search models or providers"))
            .navigationTitle("Model")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() }.accessibilityIdentifier("model-done") } }
        }
        .presentationDetents([.medium, .large])
    }

    private func row(_ p: Hermesh_Session_V1_ProviderInstance, _ m: Hermesh_Session_V1_ModelCatalogEntry) -> some View {
        Button {
            let r = selectModel(providers, from: selection, instanceID: p.instanceID, slug: m.slug)
            selection = r.selection
            if let c = r.changed { changed(c) }
            dismiss()
        } label: {
            HStack {
                Text(m.name)
                if !m.badge.isEmpty { Text("New").font(.caption2).foregroundStyle(.tint) }
                Spacer()
                if selection.instanceID == p.instanceID && selection.model == m.slug { Image(systemName: "checkmark").foregroundStyle(.tint) }
            }
        }
        .tint(.primary)
        .accessibilityIdentifier("model-\(m.slug)")
    }
}

private struct ThinkingSlider: View {
    let levels: [(id: String, title: String)]
    @Binding var value: String
    @State private var dragged: CGFloat?
    @Environment(\.colorScheme) private var scheme
    private let pad: CGFloat = 4

    var body: some View {
        let last = levels.count - 1
        let chosen = levels.firstIndex { $0.id == value } ?? 0
        VStack(alignment: .leading, spacing: 14) {
            Label {
                Text("Reasoning Effort")
            } icon: {
                Image(systemName: "gauge.with.dots.needle.67percent").foregroundStyle(.secondary)
            }
            .font(.subheadline)
            GeometryReader { g in
                let column = (g.size.width - pad * 2) / CGFloat(levels.count)
                let x = dragged ?? CGFloat(chosen) * column
                let shown = min(max(Int((x / column).rounded()), 0), last)
                let along = { (px: CGFloat) in min(max(px - pad - column / 2, 0), column * CGFloat(last)) }
                ZStack(alignment: .leading) {
                    Capsule().fill(Palette.fill2)
                    Capsule().fill(.clear)
                        .glassEffect(.regular.tint(.white.opacity(scheme == .dark ? 0.16 : 0.85)), in: Capsule())
                        .shadow(color: .black.opacity(0.12), radius: 4, y: 1)
                        .frame(width: column, height: g.size.height - pad * 2)
                        .scaleEffect(x: dragged == nil ? 1 : 1.14, y: dragged == nil ? 1 : 1.22)
                        .offset(x: pad + x)
                    HStack(spacing: 0) {
                        ForEach(Array(levels.enumerated()), id: \.offset) { i, l in
                            Text(verbatim: l.title)
                                .font(.subheadline.weight(i == shown ? .semibold : .regular))
                                .foregroundStyle(i == shown ? .primary : .secondary)
                                .lineLimit(1).minimumScaleFactor(0.7)
                                .scaleEffect(dragged != nil && i == shown ? 1.1 : 1)
                                .frame(maxWidth: .infinity)
                        }
                    }
                    .padding(.horizontal, pad)
                    .allowsHitTesting(false)
                }
                .contentShape(Capsule())
                .gesture(DragGesture(minimumDistance: 0)
                    .onChanged { v in
                        withAnimation(dragged == nil ? .spring(response: 0.3, dampingFraction: 0.75) : .interactiveSpring) { dragged = along(v.location.x) }
                    }
                    .onEnded { v in
                        value = levels[min(max(Int((along(v.location.x) / column).rounded()), 0), last)].id
                        withAnimation(.spring(response: 0.38, dampingFraction: 0.62)) { dragged = nil }
                    })
                .sensoryFeedback(.selection, trigger: shown)
            }
            .frame(height: 46)
        }
        .padding(.vertical, 6)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text("Reasoning Effort"))
        .accessibilityValue(Text(verbatim: levels[chosen].title))
        .accessibilityAdjustableAction { d in
            switch d {
            case .increment: value = levels[min(chosen + 1, last)].id
            case .decrement: value = levels[max(chosen - 1, 0)].id
            @unknown default: break
            }
        }
        .accessibilityIdentifier("model-thinking")
    }
}

/// Live sheet inspecting active and recent background tasks on a session.
private struct BackgroundTasksSheet: View {
    let tasks: [Hermesh_Session_V1_BackgroundTask]
    let onStop: (String) -> Void

    private var runningTasks: [Hermesh_Session_V1_BackgroundTask] {
        tasks.filter { $0.status == .running }
    }

    private var recentTasks: [Hermesh_Session_V1_BackgroundTask] {
        tasks.filter { $0.status != .running }
    }

    var body: some View {
        NavigationStack {
            List {
                if !runningTasks.isEmpty {
                    Section("Running") {
                        ForEach(runningTasks, id: \.taskID) { bt in
                            taskRow(bt)
                        }
                    }
                }
                if !recentTasks.isEmpty {
                    Section("Recent") {
                        ForEach(recentTasks, id: \.taskID) { bt in
                            taskRow(bt)
                        }
                    }
                }
            }
            .navigationTitle("Background Tasks")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                CloseButton()
                if runningTasks.count > 1 {
                    ToolbarItem(placement: .topBarTrailing) {
                        Button("Stop All", role: .destructive) { onStop("") }
                    }
                }
            }
        }
        .presentationDetents([.medium, .large])
    }

    private func taskRow(_ bt: Hermesh_Session_V1_BackgroundTask) -> some View {
        let title = bt.title.isEmpty ? (bt.command.isEmpty ? bt.taskID : bt.command) : bt.title
        return VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                Image(systemName: iconName(bt.kind))
                    .foregroundStyle(bt.status == .running ? Color.accentColor : .secondary)
                Text(verbatim: title).font(.subheadline.weight(.medium)).lineLimit(1)
                Spacer()
                if bt.status == .running, let since = dateOf(bt.startedAt, set: bt.hasStartedAt) {
                    Text(since, style: .timer).font(.caption.monospacedDigit()).foregroundStyle(.secondary)
                }
                if bt.status == .running {
                    Button("Stop", role: .destructive) { onStop(bt.taskID) }
                        .buttonStyle(.bordered)
                        .controlSize(.small)
                } else {
                    Text(statusText(bt)).font(.caption).foregroundStyle(bt.status == .failed ? .red : .secondary)
                }
            }
            if !bt.command.isEmpty && bt.command != title {
                Text(verbatim: bt.command)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
            }
            if !bt.outputTail.isEmpty {
                ScrollView(.vertical) {
                    Text(verbatim: bt.outputTail)
                        .font(.system(size: 11.5, design: .monospaced))
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .textSelection(.enabled)
                }
                .frame(maxHeight: 150)
                .padding(8)
                .background(Palette.fill, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            } else if !bt.summary.isEmpty {
                Text(verbatim: bt.summary).font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 4)
        .accessibilityIdentifier("bg-task-\(bt.taskID)")
    }

    private func iconName(_ kind: Hermesh_Session_V1_BackgroundTaskKind) -> String {
        switch kind {
        case .subagent: "person.2"
        case .schedule: "clock"
        case .daemon: "bolt.horizontal"
        default: "terminal"
        }
    }

    private func statusText(_ bt: Hermesh_Session_V1_BackgroundTask) -> String {
        switch bt.status {
        case .running: String(localized: "Running")
        case .completed: bt.hasExitCode ? String(localized: "Completed (\(bt.exitCode))") : String(localized: "Completed")
        case .failed: bt.hasExitCode ? String(localized: "Failed (\(bt.exitCode))") : String(localized: "Failed")
        default: String(localized: "Stopped")
        }
    }
}
