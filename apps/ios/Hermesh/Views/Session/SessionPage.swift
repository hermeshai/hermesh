import HermeshHer
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
        var herSelection: Data?
        var mode: Int?
        var worktrees: [String: Bool]?
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

    static func lastHerSelection(_ machine: String) -> Hermesh_Session_V1_ModelSelection? {
        mine(machine)?.herSelection.flatMap { try? Hermesh_Session_V1_ModelSelection(serializedBytes: $0) }
    }

    static func rememberHer(machine: String, selection: Hermesh_Session_V1_ModelSelection) {
        guard !machine.isEmpty, !selection.model.isEmpty else { return }
        var k = kept
        var m = k.byMachine[machine] ?? Mine()
        m.herSelection = try? selection.serializedData()
        k.byMachine[machine] = m
        kept = k
    }

    static func forgetHer(machine: String) {
        guard !machine.isEmpty else { return }
        var k = kept
        guard var m = k.byMachine[machine], m.herSelection != nil else { return }
        m.herSelection = nil
        k.byMachine[machine] = m
        kept = k
    }

    static func worktree(machine: String, project: String) -> Bool? {
        guard !machine.isEmpty, !project.isEmpty, project != scratchProject else { return nil }
        return mine(machine)?.worktrees?[project]
    }

    static func rememberWorktree(machine: String, project: String, worktree: Bool) {
        guard !machine.isEmpty, !project.isEmpty, project != scratchProject else { return }
        var k = kept
        var m = k.byMachine[machine] ?? Mine()
        var w = m.worktrees ?? [:]
        w[project] = worktree
        m.worktrees = w
        k.byMachine[machine] = m
        kept = k
    }

    static func forgetWorktree(machine: String, project: String) {
        guard !machine.isEmpty, !project.isEmpty else { return }
        var k = kept
        guard var m = k.byMachine[machine], var w = m.worktrees, w.removeValue(forKey: project) != nil else { return }
        m.worktrees = w.isEmpty ? nil : w
        k.byMachine[machine] = m
        kept = k
    }

    static func remember(machine: String, project: String, selection: Hermesh_Session_V1_ModelSelection, mode: Hermesh_Identity_V1_RuntimeMode, worktree: Bool? = nil) {
        guard !machine.isEmpty else { return }
        var k = kept
        var m = k.byMachine[machine] ?? Mine()
        m.projectID = project
        m.selection = try? selection.serializedData()
        m.mode = mode.rawValue
        if !project.isEmpty, project != scratchProject, let worktree {
            var w = m.worktrees ?? [:]
            w[project] = worktree
            m.worktrees = w
        }
        k.machineID = machine
        k.byMachine[machine] = m
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
    @Environment(\.barsOnTheSide) private var sideBars
    @Environment(\.screenWidth) private var screenWidth
    /// The session shown; empty on a new one.
    let sessionID: String
    private let askedMachine: String
    private let askedProject: String

    @State private var machineID = ""
    @State private var projectID = scratchProject
    @State private var place = SessionPlace()
    @State private var placeEdited = false
    @State private var worktreeTurns: Double = 0
    @State private var branches: SessionBranches?
    @State private var pickingBranch = false
    @State private var selection = Hermesh_Session_V1_ModelSelection()
    @State private var mode: Hermesh_Identity_V1_RuntimeMode = .auto
    @State private var plan = false
    @State private var providers: [Hermesh_Session_V1_ProviderInstance] = []
    @State private var providersStale = false
    @State private var ceiling: Hermesh_Identity_V1_RuntimeMode = .unspecified
    @State private var composerState = SessionComposerState()
    @State private var renderCache = SessionRenderCache()
    @State private var sending = false
    @State private var attached: [Attached] = []
    @State private var loadedDraftKey: String?
    /// A picture in the box, large (the session's Lightbox).
    @State private var boxLightbox: SessionLightboxPresentation?
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
    @State private var selectedBgTaskID: String?
    @State private var askHeight: CGFloat = 0
    @State private var askSpace: CGFloat = 0
    /// Messages to a session go out one after another, in the order sent.
    @State private var lastSend: Task<Void, Never>?
    /// Queued run IDs with an in-flight ManageQueue action.
    @State private var managingRuns: Set<String> = []
    /// A new session's first message, from Send until its machine answers.
    @State private var starting: Starting?
    /// First-use third-party AI data-sharing disclosure when starting a Her conversation before accepting on Her's page.
    @State private var aiConsentPrompt: HerAIConsentPrompt?
    /// Moves when the owner switches between Agent and Her on a new session, animating the prompt in.
    @State private var rise = false
    /// Continuous visual position between Agent (0) and Her (1), driven directly by horizontal swipe and spring release.
    @State private var roleProgress: CGFloat = 0
    @State private var isDraggingRole = false
    @State private var roleDragStart: CGFloat = 0
    @State private var roleDragLastHaptic: NewSessionWho = .agent
    @State private var roleDragEndedAt: CFTimeInterval = 0
    @State private var roleSwipeFrame: CGRect = .zero
    @State private var roleHaptic = UISelectionFeedbackGenerator()
    @State private var savedAgentMachineID = ""
    @State private var savedAgentProjectID = ""
    @Environment(\.accessibilityReduceMotion) private var still
    /// True once the wide trailing panel has settled open, so the conversation lays out once at rest rather than across every spring frame.
    @State private var trailingSettled = false
    @State private var questionTyping = false
    @State private var boxOpen = false
    @State private var boxFocused = false
    @ScaledMetric(relativeTo: .body) private var composerLineHeight: CGFloat = 20.5
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
    private var draft: String {
        get { composerState.text }
        nonmutating set { composerState.text = newValue }
    }
    private var draftBinding: Binding<String> {
        Binding(get: { composerState.text }, set: { composerState.text = $0 })
    }
    private var draftKey: String {
        if !isNew { return ComposerDraftStore.key(sessionID: sessionID) }
        if let starting { return "starting:\(starting.request.commandID)" }
        return ComposerDraftStore.newSessionKey
    }
    private var herEnvs: [HerMachine] { herEnvironments(model.machines) }
    private var herAt: HerMachine? { herPick(herEnvs, remembered: model.her.machine) }
    private var who: NewSessionWho { isNew && selection.instanceID == herAgent && herAt != nil ? .her : .agent }
    private var listedSession: Session? { isNew ? nil : model.session(sessionID) }
    private var activeProjection: Hermesh_Session_V1_SessionProjection? {
        projection?.session.sessionID == sessionID ? projection : nil
    }
    private var currentSession: Session? {
        if !isNew, let p = activeProjection {
            if let listed = listedSession,
               listed.progress.lastEventSeq > p.session.progress.lastEventSeq ||
               (listed.progress.lastEventSeq == p.session.progress.lastEventSeq && listed.updatedDate > p.session.updatedDate) {
                return listed
            }
            return p.session
        }
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
            } else if let s = displaySession, s.isHerConversation {
                HerConversationPage(sessionID: s.sessionID)
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
        .onAppear { loadDraftIfNeeded(for: draftKey) }
        .onChange(of: draftKey) { _, key in loadDraftIfNeeded(for: key) }
        .fullScreenCover(item: $boxLightbox) { SessionImageLightbox(presentation: $0) }
        .task(id: sessionID) { await start() }
        // Keep following while not background-paused; quick app switches (< 15 s) stay live with zero reconnect.
        .task(id: "\(sessionID)/\(!model.isBackgroundPaused)") {
            if !isNew && !model.isBackgroundPaused { await followSession() }
        }
        .onChange(of: reachable) {
            if reachable && !following { wakeRetry() }
        }
        .task(id: "\(effectiveMachineID)/\(effectiveProjectID)") { await loadProviders() }
        .onChange(of: model.projects) {
            SessionChoices.keepProjects(model.projects, machines: model.machines)
            if isNew, !placeEdited, let pr = model.projects.first(where: { $0.machineID == machineID && $0.projectID == projectID }) {
                let want = projectWorktree(pr, remembered: SessionChoices.worktree(machine: machineID, project: projectID))
                if place.worktree != want { place.worktree = want }
            }
        }
        .onChange(of: model.addedProject) {
            guard let added = model.addedProject, added.machineID == machineID else { return }
            placeEdited = true
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
            ModelSheet(providers: providersForRole(providers, forHer: who == .her),
                       selection: Binding(
                get: { selection },
                set: { next in
                    selection = next
                    if isNew, next.instanceID == herAgent, !effectiveMachineID.isEmpty, !model.isDemo {
                        if let her = providers.first(where: { $0.instanceID == herAgent }), isDefaultHerSelection(her, next) {
                            SessionChoices.forgetHer(machine: effectiveMachineID)
                        } else {
                            SessionChoices.rememberHer(machine: effectiveMachineID, selection: next)
                        }
                    }
                    if !isNew { update { $0.modelSelection = next } }
                }
            ), projection: isNew ? nil : activeProjection,
            hasActiveBackgroundTasks: !activeBgTasks.isEmpty,
            stale: providersStale, machineName: machine?.machineName ?? "",
            projectAgents: who == .her ? [] : projects(on: effectiveMachineID).list.first { $0.projectID == effectiveProjectID }?.agents ?? [],
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
        .sheet(isPresented: Binding(
            get: { showingBackgroundTasks && model.wide == nil },
            set: {
                showingBackgroundTasks = $0
                if !$0 { selectedBgTaskID = nil }
            }
        )) {
            BackgroundTasksPanel(
                tasks: allBgTasks,
                selectedTaskID: $selectedBgTaskID,
                onClose: {
                    showingBackgroundTasks = false
                    selectedBgTaskID = nil
                },
                onStop: stopBackgroundTask
            )
            .presentationDetents([.medium, .large])
            .presentationDragIndicator(.visible)
        }
        .alert("Share data with third-party AI?", isPresented: Binding(
            get: { aiConsentPrompt != nil },
            set: { if !$0 { aiConsentPrompt = nil } }
        ), presenting: aiConsentPrompt) { consent in
            Button("Allow AI Sharing") { consent.allow() }
            Button("Don’t Allow", role: .cancel) {}
        } message: { consent in
            if let provider = consent.provider, let model = consent.model {
                Text("Provider: \(provider)\nModel: \(model)\n\nData: messages, chat history, images, attachment contents, memory, and tool results, which may include data from connected services.\n\nPurpose: generate replies and perform Her’s tasks, including scheduled tasks on your computer.\n\nNote: Her sends data through your computer to the AI model service configured there, including any custom provider or gateway.")
            } else {
                Text("Data: messages, chat history, images, attachment contents, memory, and tool results, which may include data from connected services.\n\nPurpose: generate replies and perform Her’s tasks, including scheduled tasks on your computer.\n\nNote: Her sends data through your computer to the AI model service configured there, including any custom provider or gateway.")
            }
        }
        .onChange(of: model.path) { focused = false }
        .onChange(of: showingBackgroundTasks || deleting) { _, on in if on { focused = false; hideKeyboard() } }
        // A new session's project's branches, read from its folder (only the owner's devices can): again as the page
        // opens or the project changes, since a branch switched on the machine isn't sent here.
        .task(id: "\(machineID)/\(projectID)/\(isNew)") { await loadBranches() }
    }

    // MARK: A new session

    private var askFits: Bool {
        askSpace <= 0 || askHeight <= 0 || askSpace >= askHeight + 24
    }

    private var newSession: some View {
        ScrollView {
            Color.clear
                .frame(maxWidth: .infinity)
                .containerRelativeFrame(.vertical) { h, _ in max(0, askSpace > 0 ? askSpace : h) }
                .overlay {
                    ask
                        .fixedSize(horizontal: false, vertical: true)
                        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { askHeight = $0 }
                        .opacity(askFits ? 1 : 0)
                        .animation(.easeOut(duration: 0.18), value: askFits)
                        .allowsHitTesting(askFits)
                        .accessibilityHidden(!askFits)
                }
                .clipped()
                .contentShape(Rectangle())
                .onTapGesture { focused = false }
                .onGeometryChange(for: CGRect.self) { $0.frame(in: .global) } action: { roleSwipeFrame = $0 }
                .gesture(
                    RoleSwipeGesture(
                        enabled: herAt != nil && askFits,
                        canSwipeRight: who == .her,
                        rubberBandRightOnAgent: model.menuBeside,
                        activeWindowFrame: roleSwipeFrame,
                        onBegan: beginRoleDrag,
                        onChanged: updateRoleDrag,
                        onEnded: endRoleDrag
                    )
                )
        }
        .onScrollGeometryChange(for: CGFloat.self) { $0.containerSize.height } action: { _, h in
            askSpace = h
        }
        .onChange(of: who, initial: true) { _, next in
            guard !isDraggingRole else { return }
            let target: CGFloat = next == .her ? 1 : 0
            guard roleProgress != target else { return }
            if rise {
                withAnimation(still ? .easeOut(duration: 0.2) : .spring(response: 0.55, dampingFraction: 0.72)) {
                    roleProgress = target
                }
            } else {
                var t = Transaction()
                t.disablesAnimations = true
                withTransaction(t) { roleProgress = target }
            }
        }
        .scrollIndicators(.hidden)
        .scrollBounceBehavior(.always)
        .scrollDismissesKeyboard(.immediately)
        .keyboardAwayOnTap()
        .background {
            if herAt != nil {
                NewSessionAurora(progress: roleProgress).ignoresSafeArea()
            }
        }
        .scrollEdgeEffectStyle(.soft, for: .bottom)
        .safeAreaBar(edge: .bottom) {
            VStack(spacing: 6) {
                if who == .agent {
                    placeRow
                        .transition(.opacity.combined(with: .move(edge: .bottom)))
                }
                box
            }
            .animation(still ? .easeOut(duration: 0.2) : .smooth(duration: 0.26), value: who)
            .padding(.bottom, 4)
        }
    }

    private var agentPreviewTarget: (machineID: String, projectID: String) {
        if who == .agent { return (machineID, projectID) }
        let known = { (id: String) in !id.isEmpty && model.machines.contains { $0.machineID == id } }
        let mid = [savedAgentMachineID, SessionChoices.lastMachine ?? ""].first(where: known)
            ?? model.machines.first(where: \.local)?.machineID
            ?? model.machines.first?.machineID ?? ""
        let list = projects(on: mid).list
        let preferred = (!savedAgentProjectID.isEmpty && (savedAgentProjectID == scratchProject || list.contains { $0.projectID == savedAgentProjectID }))
            ? savedAgentProjectID
            : SessionChoices.mine(mid)?.projectID
        let pid = projectOnMachine(list, machineID: mid, current: "", lastUsed: preferred)
        return (mid, pid)
    }

    private var ask: some View {
        let preview = agentPreviewTarget
        return VStack(spacing: 14) {
            if herAt != nil {
                NewSessionPresence(who: who, progress: roleProgress, onWho: tapWho)
                ZStack(alignment: .top) {
                    agentAsk(machineID: preview.machineID, projectID: preview.projectID, isActive: who == .agent)
                        .modifier(RolePromptEffect(progress: roleProgress, isHer: false, reduceMotion: still))
                        .allowsHitTesting(!isDraggingRole && who == .agent)
                        .accessibilityHidden(who != .agent)

                    if let herTarget = (who == .her ? machine : nil) ?? herAt {
                        HerNewSessionAsk(machine: herTarget, envs: herEnvs, reachable: reachable, isActive: who == .her) { nextMachine in
                            machineID = nextMachine
                            model.her.machine = nextMachine
                        }
                        .modifier(RolePromptEffect(progress: roleProgress, isHer: true, reduceMotion: still))
                        .allowsHitTesting(!isDraggingRole && who == .her)
                        .accessibilityHidden(who != .her)
                    }
                }
            } else {
                agentAsk(machineID: machineID, projectID: projectID, isActive: true)
            }
        }
        .padding(.horizontal, 28)
    }

    private func agentAsk(machineID targetMachineID: String, projectID targetProjectID: String, isActive: Bool) -> some View {
        let (here, stale) = projects(on: targetMachineID)
        let targetMachine = model.machines.first { $0.machineID == targetMachineID }
        let targetOnline = targetMachine.map { $0.online || $0.local } ?? true
        let projectTitle = targetProjectID == scratchProject ? String(localized: "Temporary Workspace") : targetProjectID
        return VStack(spacing: 18) {
            VStack(spacing: 2) {
                Text("What should we build in")
                HStack(spacing: 0) {
                    Menu {
                        Section(stale ? String(localized: "Projects on \(targetMachine?.machineName ?? "") · last known") : String(localized: "Projects on \(targetMachine?.machineName ?? "")")) {
                            ForEach(here, id: \.projectID) { p in
                                Button { pick(project: p.projectID, in: here) } label: {
                                    Label { Text(p.projectID); Text(p.path) } icon: { if p.projectID == targetProjectID { Image(systemName: "checkmark") } }
                                }
                                .accessibilityIdentifier("project-\(p.projectID)")
                            }
                            Button { pick(project: scratchProject, in: here) } label: {
                                Label { Text("Temporary Workspace") } icon: { if targetProjectID == scratchProject { Image(systemName: "checkmark") } }
                            }
                        }
                        Button("Add Project…", systemImage: "plus") { model.sheet = .addProject(targetMachineID, forSession: true) }
                            .disabled(!targetOnline)
                            .accessibilityIdentifier("thread-add-project")
                    } label: {
                        Text(verbatim: projectTitle).lineLimit(1).underline(color: .secondary)
                    }
                    .tint(.primary)
                    .accessibilityIdentifier(isActive ? "thread-project" : "")
                    Text("What should we build in · end")
                }
                .accessibilityIdentifier(isActive ? "project-scratch" : "")
            }
            .font(.title.weight(.semibold))
            .multilineTextAlignment(.center)
            .accessibilityElement(children: isActive ? .contain : .ignore)
            .accessibilityIdentifier(isActive ? "new-session-title" : "")
            VStack(spacing: 8) {
                Menu {
                    Section("Environment") {
                        ForEach(model.machines, id: \.machineID) { m in
                            let note: String? = !m.online && !m.local ? String(localized: "Offline · messages wait until it’s back") : nil
                            Button { pick(machine: m.machineID) } label: {
                                if let note {
                                    Label { Text(m.machineName); Text(note) } icon: { if m.machineID == targetMachineID { Image(systemName: "checkmark") } }
                                } else {
                                    Label { Text(m.machineName) } icon: { if m.machineID == targetMachineID { Image(systemName: "checkmark") } }
                                }
                            }
                            .accessibilityIdentifier("machine-\(m.machineName)")
                        }
                    }
                } label: {
                    HStack(spacing: 6) {
                        Image(systemName: m_icon)
                        Text(verbatim: targetMachine?.machineName ?? "").lineLimit(1)
                        if !targetOnline { Circle().fill(.tertiary).frame(width: 6, height: 6) }
                        Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(.tertiary)
                    }
                    .font(.subheadline).foregroundStyle(.secondary)
                }
                .tint(.secondary)
                .accessibilityIdentifier(isActive ? "thread-machine" : "")
                ConnectionLine(machine: targetMachine, linkUp: reachable, centered: true)
                    .frame(minHeight: 24)
            }
        }
    }

    private func tapWho(_ next: NewSessionWho) {
        guard !isDraggingRole, CACurrentMediaTime() - roleDragEndedAt > 0.08 else { return }
        guard isNew, next != who, next != .her || herAt != nil else { return }
        roleHaptic.selectionChanged()
        chooseWho(next)
        withAnimation(still ? .easeOut(duration: 0.2) : .spring(response: 0.55, dampingFraction: 0.72)) {
            roleProgress = next == .her ? 1 : 0
        }
    }

    private func beginRoleDrag() {
        isDraggingRole = true
        roleDragStart = who == .her ? 1 : 0
        roleDragLastHaptic = who
        roleHaptic.prepare()
        var t = Transaction()
        t.disablesAnimations = true
        withTransaction(t) { roleProgress = roleDragStart }
    }

    private func updateRoleDrag(_ translationX: CGFloat) {
        let next = CGFloat(RoleSwipeMath.dragProgress(start: Double(roleDragStart), translationX: Double(translationX)))
        var t = Transaction()
        t.disablesAnimations = true
        withTransaction(t) { roleProgress = next }
        let detent: NewSessionWho = RoleSwipeMath.hapticDetent(progress: Double(next), currentIsHer: roleDragLastHaptic == .her) ? .her : .agent
        if detent != roleDragLastHaptic {
            roleDragLastHaptic = detent
            roleHaptic.selectionChanged()
            roleHaptic.prepare()
        }
    }

    private func endRoleDrag(_ translationX: CGFloat, _ velocityX: CGFloat, _ completed: Bool) {
        isDraggingRole = false
        roleDragEndedAt = CACurrentMediaTime()
        let target: NewSessionWho = if completed {
            RoleSwipeMath.landsOnHer(start: Double(roleDragStart), translationX: Double(translationX), velocityX: Double(velocityX)) ? .her : .agent
        } else {
            who
        }
        let targetProgress: CGFloat = target == .her ? 1 : 0
        let initialVelocity = RoleSwipeMath.initialSpringVelocity(
            current: Double(roleProgress),
            target: Double(targetProgress),
            velocityX: completed ? Double(velocityX) : 0
        )
        if target != who {
            if roleDragLastHaptic != target { roleHaptic.selectionChanged() }
            chooseWho(target)
        }
        let anim: Animation = still
            ? .easeOut(duration: 0.2)
            : .interpolatingSpring(duration: 0.42, bounce: 0.16, initialVelocity: initialVelocity)
        withAnimation(anim) {
            roleProgress = targetProgress
        }
    }

    private func chooseWho(_ next: NewSessionWho) {
        guard isNew, next != who, next != .her || herAt != nil else { return }
        NewSessionWho.remember(next, persistent: !model.isDemo)
        rise = true
        if next == .her, let herAt {
            savedAgentMachineID = machineID
            savedAgentProjectID = projectID
            machineID = herAt.machineID
            projectID = scratchProject
            selection = herSelection(providers: herAt.machineID == effectiveMachineID ? providers : [], machineID: herAt.machineID)
            return
        }
        let target = agentPreviewTarget
        machineID = target.machineID
        let list = projects(on: target.machineID).list
        pick(project: target.projectID, in: list)
        selection = (target.machineID == effectiveMachineID ? agentSelection(providers: providers, machineID: target.machineID) : nil) ?? Hermesh_Session_V1_ModelSelection()
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
                ConnectionLine(machine: machine, linkUp: reachable)
            }
            .padding(.horizontal, 20)
            .padding(.top, 13).padding(.bottom, 21)
        }
        .defaultScrollAnchor(.bottom, for: .initialOffset)
        .scrollDismissesKeyboard(.interactively)
        .keyboardAwayOnTap()
        .scrollEdgeEffectStyle(.soft, for: .bottom)
        .safeAreaBar(edge: .bottom) {
            box.padding(.bottom, 4)
        }
        .sessionBar(nil, renaming: $renaming, deleting: $deleting,
                    starting: .init(title: firstLine(text),
                                    place: st.request.modelSelection.instanceID == herAgent ? "Her · \(machine?.machineName ?? "")" : "\(machine?.machineName ?? "")/\(projectID)"))
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
        let memo = renderCache.resolve(activeProj, sessionID: s.sessionID)
        let steps = memo.steps
        let previousTurns = memo.previousTurns
        let writing = memo.writing
        let files = model.sessionFiles.model(s.sessionID, api: model.api)
        let codeActions = codeActions(for: s)
        let pendingReq = s.status == .archived ? nil : pendingRequest(of: activeProj)
        let fallbackQuestion: Question? = if pendingReq == nil, s.status != .archived, let q = s.pendingQuestion,
            activeProj?.requests.contains(where: { $0.requestID == q.questionID && $0.status != .pending }) != true {
            q
        } else {
            nil
        }
        let hasInlineQuestion = pendingReq?.kind == .userInput || fallbackQuestion != nil
        let hasInlinePlan = pendingReq?.kind == .planReview
        let hasInlinePrompt = hasInlineQuestion || hasInlinePlan
        let todos = memo.todos
        let workStatus = sessionWorkStatus(activeProj, steps: steps)
        let todoWindow = windowTodoSteps(todos, workText: workStatus?.text)
        let todoEndSummary: TodoEndSummary? = todoWindow.flatMap { w in
            w.active.first.map { TodoEndSummary(stepNumber: w.stepNumber, total: w.total, text: $0.text, failed: w.hasFailed) }
        }
        // A wide screen: the side panel or background tasks beside the conversation (docs/session-files.md §15).
        let panel = model.wide != nil && files.panelOpen
        let wideBg = model.wide != nil && !panel && showingBackgroundTasks && !allBgTasks.isEmpty
        let trailingKind: WideTrailingPanelKind = panel ? .files : wideBg ? .background : .none
        // Enlarged, the panel covers the conversation, which keeps its width and place underneath. On iPhone Duo
        // (`sideBars`), the split aligns with the physical center fold (`screenWidth / 2`).
        return GeometryReader { geo in
        let panelWidth = sidePanelWidth(geo)
        ZStack(alignment: .trailing) {
        ScrollView {
            ConversationStack(lazy: renderCache.useLazy(sessionID: s.sessionID, stepCount: steps.count)) {
                if s.status == .archived { ArchivedNote(session: s) }
                ForEach(Array(steps.enumerated()), id: \.element.id) { i, step in
                    StepView(step: step, session: s, previousTurn: previousTurns[i], live: writing.contains(step.id))
                }
                .environment(\.sessionFiles, files)
                .environment(\.sessionCodeActions, codeActions)
                .environment(\.openBackgroundTasks) { reason in presentBackgroundTasks(focus: reason) }
                .environment(\.openURL, OpenURLAction { url in
                    guard let route = FileRefLink.route(url, session: s.sessionID) else { return .systemAction }
                    if case .sessionFile(_, let r, let p, let line, _) = route, isSessionImage(p, line: line > 0 ? line : nil) {
                        files.openLightbox(run: r, path: p, machineID: s.machineID)
                        return .handled
                    }
                    // A wide screen: a tab of the side panel, the menu shrinking to its rail to give it room.
                    if model.wide != nil {
                        switch route {
                        case .sessionFile(_, let r, let p, let line, let end):
                            files.openTab(run: r, path: p, line: line > 0 ? line : nil, endLine: end > 0 ? end : nil)
                        case .sessionFolder(_, let dir):
                            files.reveal("\(dir)/x")
                            files.openProject()
                        default: model.path.append(route)
                        }
                        model.menuRail = true
                        return .handled
                    }
                    model.path.append(route)
                    return .handled
                })
                if let req = pendingReq, req.kind == .userInput {
                    inlineQuestionPanel(req, s)
                } else if let req = pendingReq, req.kind == .planReview {
                    PlanReviewPanel(session: s, request: req, isTyping: $questionTyping) { build in
                        await respondNow(req, build)
                    }
                    .id(req.requestID)
                } else if let q = fallbackQuestion {
                    QuestionPanel(session: s, question: q, isTyping: $questionTyping) { answers, decline in
                        if await model.answer(s, answers: answers, decline: decline) { spoken += 1 }
                    }
                    .id(q.questionID)
                }
                if let todoWindow {
                    SessionTodoSpine(window: todoWindow, since: workStatus?.since, hasWork: workStatus != nil, thinkingText: workStatus?.thinkingText)
                }
                ConnectionLine(machine: machine, linkUp: s.status == .archived || (following && reachable), work: todoWindow == nil ? workStatus : nil)
                if let f = sessionFailure(currentSession) {
                    FailedView(session: s, failure: f, grant: grant)
                }
            }
            .padding(.horizontal, 20)
            .padding(.top, 13).padding(.bottom, 21)
            .background(ConversationScrollFinder(box: scrollBox))
        }
        .keyboardAwayOnTap()
        .onChange(of: questionTyping) { _, typingNow in
            if typingNow {
                focused = false
                atEnd = true
                withAnimation(.smooth(duration: 0.2)) { position.scrollTo(edge: .bottom) }
            }
        }
        .onChange(of: hasInlinePrompt) { _, has in
            if !has && questionTyping { questionTyping = false }
        }
        .followsEnd(id: sessionID, ready: historyReady(s), spoken: spoken,
                    waitingAgent: hasInlinePrompt && !questionTyping ? s.agent : nil,
                    waitingPlan: hasInlinePlan,
                    todoSummary: todoEndSummary, atEnd: $atEnd, position: $position, scrollBox: scrollBox) {
            if s.status != .archived {
                VStack(spacing: 6) {
                    if let req = pendingReq, req.kind != .userInput, req.kind != .planReview {
                        SessionRequestPanel(session: s, request: req) { build in await respondNow(req, build) }
                            .id(req.requestID)
                    }
                    backgroundBar
                    queue(activeProj)
                    if !(hasInlinePrompt && questionTyping) {
                        box
                    }
                }
                .padding(.bottom, 4)
            }
        }
        .scrollDismissesKeyboard(.interactively)
        .sessionBar(s, renaming: $renaming, deleting: $deleting,
                    backgroundTasks: allBgTasks.isEmpty ? nil : .init(runningCount: activeBgTasks.count, isOpen: showingBackgroundTasks) {
                        toggleBackgroundTasks(files: files)
                    })
        .sessionDialogs(s, renaming: $renaming, deleting: $deleting)
        .padding(.trailing, (panel || wideBg) && trailingSettled ? panelWidth : 0)
        .animation(nil, value: trailingSettled)
        .accessibilityHidden(panel && files.panelMax)
        .onChange(of: files.panelOpen) { _, on in
            if on && showingBackgroundTasks { showingBackgroundTasks = false }
        }
        .onChange(of: trailingKind) { _, next in
            if next == .none, trailingSettled {
                var tx = Transaction()
                tx.disablesAnimations = true
                withTransaction(tx) { trailingSettled = false }
            }
        }
        if model.wide != nil {
            wideTrailingPanel(for: s, files: files, kind: trailingKind, panelWidth: panelWidth)
        }
        }
        }
        .sheet(item: Binding(get: { files.sharing }, set: { files.sharing = $0 })) {
            HerShareSheet(url: $0).presentationDetents([.medium, .large])
        }
        .fullScreenCover(item: Binding(get: { files.lightbox }, set: { files.lightbox = $0 })) {
            SessionImageLightbox(presentation: $0)
        }
        .onChange(of: files.sharing != nil || files.lightbox != nil) { _, on in if on { focused = false; hideKeyboard() } }
    }

    private func sidePanelWidth(_ geo: GeometryProxy) -> CGFloat {
        if sideBars, screenWidth > 0 {
            let fromFold = round(geo.frame(in: .global).maxX - screenWidth / 2)
            return min(max(320, fromFold), max(320, geo.size.width - 260))
        }
        return min(470, round(geo.size.width * 0.54))
    }

    private func wideTrailingPanel(for s: Session, files: SessionFilesModel, kind: WideTrailingPanelKind, panelWidth: CGFloat) -> some View {
        WideSidePanelHost(
            kind: kind,
            splitWidth: panelWidth,
            isMax: files.panelMax,
            reduceMotion: still,
            filesKey: ObjectIdentifier(files),
            sideBars: sideBars,
            makeFiles: { wideFilesRoot(for: s, files: files) },
            makeBackground: { wideBackgroundRoot() },
            onSettled: { settled in
                guard trailingSettled != settled else { return }
                var tx = Transaction()
                tx.disablesAnimations = true
                withTransaction(tx) { trailingSettled = settled }
            },
            onCloseFiles: {
                files.panelOpen = false
                files.panelMax = false
                files.panelDrawer = false
            },
            onCloseBackground: {
                showingBackgroundTasks = false
                selectedBgTaskID = nil
            }
        )
    }

    private func wideFilesRoot(for s: Session, files: SessionFilesModel) -> some View {
        SessionSidePanel(files: files, sideBars: sideBars)
            .environment(model)
            .environment(\.openURL, OpenURLAction { url in
                guard let route = FileRefLink.route(url, session: s.sessionID) else { return .systemAction }
                switch route {
                case .sessionFile(_, let r, let p, let line, let end):
                    if isSessionImage(p, line: line > 0 ? line : nil) {
                        files.openLightbox(run: r, path: p, machineID: s.machineID)
                    } else {
                        files.openTab(run: r, path: p, line: line > 0 ? line : nil, endLine: end > 0 ? end : nil)
                    }
                case .sessionFolder(_, let dir):
                    files.reveal("\(dir)/x")
                    files.openProject()
                default:
                    model.path.append(route)
                }
                return .handled
            })
    }

    private func wideBackgroundRoot() -> some View {
        BackgroundTasksPanel(
            tasks: allBgTasks,
            selectedTaskID: $selectedBgTaskID,
            onClose: {
                showingBackgroundTasks = false
                selectedBgTaskID = nil
            },
            onStop: stopBackgroundTask
        )
        .environment(model)
    }

    private func presentBackgroundTasks(focus reason: String = "") {
        if !reason.isEmpty {
            let matched = allBgTasks.first { $0.taskID == reason || $0.title == reason || $0.command == reason }
            selectedBgTaskID = matched?.taskID
        } else {
            selectedBgTaskID = nil
        }
        if model.wide != nil {
            let files = model.sessionFiles.model(sessionID, api: model.api)
            files.panelOpen = false
            files.panelMax = false
            files.panelDrawer = false
            model.menuRail = true
            showingBackgroundTasks = true
        } else {
            withAnimation(.snappy(duration: 0.26)) { showingBackgroundTasks = true }
        }
    }

    private func toggleBackgroundTasks(files: SessionFilesModel) {
        if showingBackgroundTasks {
            if model.wide != nil {
                showingBackgroundTasks = false
                selectedBgTaskID = nil
            } else {
                withAnimation(.snappy(duration: 0.24)) {
                    showingBackgroundTasks = false
                    selectedBgTaskID = nil
                }
            }
        } else {
            presentBackgroundTasks()
        }
    }

    @ViewBuilder private func inlineQuestionPanel(_ req: Hermesh_Session_V1_RuntimeRequest, _ s: Session) -> some View {
        QuestionPanel(session: s, question: sessionQuestion(req), isTyping: $questionTyping) { answers, decline in
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
    }

    /// Active background tasks or long-lived daemons running after or alongside turns.
    @ViewBuilder private var backgroundBar: some View {
        let active = activeBgTasks
        if !active.isEmpty {
            let awaited = active.filter(\.awaited)
            let primary = awaited.first ?? active[0]
            let leadLabel = BackgroundTaskFormat.label(of: primary)
            let finishedCount = allBgTasks.count - active.count
            let headline: String = if awaited.count == 1 {
                leadLabel
            } else if awaited.count > 1 {
                String(localized: "\(awaited.count) background tasks running")
            } else if active.count == 1 {
                leadLabel
            } else {
                String(localized: "\(active.count) background services running")
            }
            TimelineView(.periodic(from: .now, by: 1)) { ctx in
                let elapsed = BackgroundTaskFormat.timing(of: primary, now: ctx.date)
                let subline = [BackgroundTaskFormat.kindTitle(primary.kind), elapsed].filter { !$0.isEmpty }.joined(separator: " · ")
                Button {
                    if model.wide != nil && showingBackgroundTasks {
                        showingBackgroundTasks = false
                    } else {
                        presentBackgroundTasks()
                    }
                } label: {
                    HStack(spacing: 10) {
                        ZStack {
                            RoundedRectangle(cornerRadius: 8, style: .continuous)
                                .fill(Color.accentColor.opacity(0.15))
                                .frame(width: 28, height: 28)
                            Image(systemName: BackgroundTaskFormat.iconName(primary.kind))
                                .font(.system(size: 13, weight: .semibold))
                                .foregroundStyle(Color.accentColor)
                        }
                        VStack(alignment: .leading, spacing: 1.5) {
                            Text(verbatim: headline)
                                .font(.subheadline.weight(.medium))
                                .lineLimit(1)
                                .foregroundStyle(.primary)
                            if !subline.isEmpty {
                                Text(verbatim: subline)
                                    .font(.caption.monospacedDigit())
                                    .lineLimit(1)
                                    .foregroundStyle(.secondary)
                            }
                        }
                        Spacer(minLength: 4)
                        if finishedCount > 0 {
                            Text(String(localized: "Completed \(finishedCount)"))
                                .font(.caption2.weight(.medium).monospacedDigit())
                                .foregroundStyle(.secondary)
                                .padding(.horizontal, 7).padding(.vertical, 3)
                                .background(Palette.fill2, in: Capsule())
                        }
                        Image(systemName: "chevron.right")
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(.tertiary)
                    }
                    .padding(.horizontal, 12).padding(.vertical, 9)
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                .padding(.horizontal, 20)
                .accessibilityIdentifier("thread-background-bar")
            }
        }
    }

    /// What waits for the next turn, in a card over the box: each can steer the turn now, or go.
    @ViewBuilder private func queue(_ activeProj: Hermesh_Session_V1_SessionProjection?) -> some View {
        let queued = queuedRuns(of: activeProj)
        if !queued.isEmpty {
            VStack(alignment: .leading, spacing: 4) {
                Text("Queue (\(queued.count))").font(.caption).foregroundStyle(.secondary)
                ForEach(Array(queued.enumerated()), id: \.element.runID) { i, run in
                    let rowBusy = managingRuns.contains(run.runID)
                    HStack {
                        Text(verbatim: run.prompt.isEmpty ? run.attachments.map(\.name).joined(separator: ", ") : run.prompt).font(.subheadline).lineLimit(1)
                        Spacer()
                        if rowBusy {
                            ProgressView().controlSize(.mini)
                        }
                        Button("Steer Now") { manage(run, .promoteToSteer) }
                            .font(.footnote).tint(.accentColor)
                            .disabled(rowBusy)
                            .accessibilityIdentifier("thread-queue-promote-\(i)")
                        Button("Remove", systemImage: "xmark") { manage(run, .cancel) }
                            .labelStyle(.iconOnly).font(.footnote).tint(.secondary)
                            .disabled(rowBusy)
                            .accessibilityIdentifier("thread-queue-cancel-\(i)")
                    }
                    .accessibilityIdentifier("thread-queued-\(i)")
                }
            }
            .padding(.horizontal, 14).padding(.vertical, 10)
            .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
            .padding(.horizontal, 20)
        }
    }

    private func sessionWorkStatus(_ activeProj: Hermesh_Session_V1_SessionProjection?, steps: [Step]) -> SessionWorkStatus? {
        switch currentSession?.status {
        case .preparing:
            return SessionWorkStatus(text: String(localized: "Starting…"))
        case .queued where currentSession?.offlineQueued != true:
            return SessionWorkStatus(text: statusTitle(.queued))
        case .running:
            let run = activeProj?.runs.first { $0.runID == currentSession?.activeRunID }
            let defaultText = switch sessionWork(activeProj) {
            case .tool: String(localized: "Running tools…")
            case .thinking: String(localized: "Thinking…")
            case .replying: String(localized: "Replying…")
            case .waiting: String(localized: "Waiting for model response…")
            case .working: String(localized: "Working…")
            }
            return SessionWorkStatus(
                text: sessionFocusText(activeProj) ?? defaultText,
                since: run.flatMap { dateOf($0.startedAt, set: $0.hasStartedAt) },
                thinkingText: liveReasoningText(activeProj)
            )
        case .idle where sessionFailure(currentSession) == nil:
            guard let last = steps.last else { return nil }
            if case .item(.turnEnd) = last { return nil }
            guard (currentSession?.activeBackgroundCount ?? 0) > 0 || activeBgTasks.contains(where: \.awaited) else { return nil }
            let count = max(Int(currentSession?.activeBackgroundCount ?? 0), activeBgTasks.filter(\.awaited).count)
            let since = activeBgTasks.first(where: \.awaited).flatMap { dateOf($0.startedAt, set: $0.hasStartedAt) }
            return SessionWorkStatus(
                text: String(localized: "This reply finished; waiting for \(count) background task(s) to report back…"),
                since: since
            )
        default:
            return nil
        }
    }

    // MARK: The box

    private var boxShouldOpen: Bool {
        focused || !draft.isEmpty || !attached.isEmpty || attaching || pickingModel
    }

    private var composerAnimation: Animation? {
        still ? .easeOut(duration: 0.16) : .smooth(duration: 0.20)
    }

    private var isShellDraft: Bool {
        guard !isNew else { return false }
        let trimmed = draft.drop(while: { $0 == " " || $0 == "\t" })
        return trimmed.first == "!"
    }

    private var canLiveSteer: Bool {
        guard let cur = currentSession, cur.status == .running, cur.pendingRequestID.isEmpty else { return false }
        let targetID = selection.instanceID.isEmpty ? cur.modelSelection.instanceID : selection.instanceID
        let inst = providers.first { $0.instanceID == targetID }
        guard inst?.capabilities.midTurnSteer == true else { return false }
        return selection.instanceID.isEmpty || cur.modelSelection.instanceID.isEmpty || selection.instanceID == cur.modelSelection.instanceID
    }

    private var box: some View {
        ComposerScope { composerBox }
    }

    private var composerBox: some View {
        let ready = !sending && starting == nil && uploading == 0 && (!draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !attached.isEmpty)
        let open = boxOpen
        let shellDraft = isShellDraft
        let store = SessionAttachmentStore.shared
        let waitingInput = currentSession?.status == .waitingInput
        let liveSteer = canLiveSteer
        let ends = HStack(spacing: 8) {
            if ready && online && !shellDraft && running && !waitingInput {
                if liveSteer {
                    Button { send(explicitIntent: .queue) } label: {
                        Text("Queue")
                            .font(.footnote.weight(.medium))
                            .padding(.horizontal, 11).frame(height: 36)
                            .background(Palette.fill2, in: Capsule())
                    }
                    .tint(.primary)
                    .accessibilityIdentifier("thread-queue")
                } else {
                    Button { send(explicitIntent: .steer) } label: {
                        Label("Steer", systemImage: "bolt.fill")
                            .labelStyle(.iconOnly)
                            .font(.footnote.weight(.semibold))
                            .frame(width: 36, height: 36)
                            .foregroundStyle(Color("AccentColor"))
                            .background(Color("AccentColor").opacity(0.15), in: .circle)
                    }
                    .accessibilityLabel(Text("Steer Now"))
                    .accessibilityIdentifier("thread-steer")
                }
            }
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
            .accessibilityLabel(Text(online ? (running && !waitingInput ? (liveSteer && !shellDraft ? "Steer" : "Queue") : "Send") : "Queue Until It’s Online"))
            .accessibilityIdentifier("thread-send")
        }
        let boxShape = RoundedRectangle(cornerRadius: 24, style: .continuous)
        let hasFocusGlow = isNew && starting == nil && herAt != nil
        let glowColor = who == .her ? Color(hex: "b3bcf2") : Color.accentColor
        return VStack(alignment: .leading, spacing: 6) {
            if !attached.isEmpty || uploading > 0 {
                ScrollView(.horizontal) {
                    HStack(spacing: 6) {
                        ForEach(attached) { a in
                            if let picture = a.picture ?? (store.pictures[a.id] ?? nil) {
                                ZStack(alignment: .topTrailing) {
                                    Button { openAttached(a) } label: {
                                        Image(uiImage: picture).resizable().scaledToFill().frame(width: 56, height: 56)
                                            .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
                                    }
                                    .buttonStyle(.plain)
                                    .accessibilityLabel(Text(verbatim: a.ref.name))
                                    .accessibilityIdentifier("thread-attached-\(a.ref.name)")
                                    Button { removeAttached(a.id) } label: {
                                        Image(systemName: "xmark.circle.fill").symbolRenderingMode(.palette).foregroundStyle(.white, .black.opacity(0.55))
                                    }
                                    .padding(3)
                                    .accessibilityLabel(Text("Remove \(a.ref.name)"))
                                }
                            } else {
                                HerChip(name: a.ref.name) { removeAttached(a.id) }
                                    .contentShape(.capsule)
                                    .onTapGesture { openAttached(a) }
                                    .accessibilityAddTraits(attachmentOpensInApp(a.ref) ? .isButton : [])
                                    .accessibilityIdentifier("thread-attached-\(a.ref.name)")
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
            ZStack(alignment: .bottom) {
                TextField(isNew && starting == nil ? (who == .her ? String(localized: "Message Her") : String(localized: "Describe what to build, debug or plan")) : running ? (liveSteer ? String(localized: "Say more to steer this turn") : String(localized: "Say more; it goes to the next turn")) : String(localized: "Keep going"),
                          text: draftBinding, axis: .vertical)
                    .font(shellDraft ? .body.monospaced() : .body)
                    .lineLimit(open ? 2 ... 6 : 1 ... 6)
                    .focused($focused)
                    .padding(.vertical, 8)
                    .frame(minHeight: open ? composerLineHeight * 2 + 16 : max(36, composerLineHeight + 16), alignment: .top)
                    .accessibilityIdentifier("thread-composer")
                    .padding(.leading, open ? 10 : 44)
                    .padding(.trailing, open ? 0 : (running ? 88 : 44))
                    .padding(.bottom, open ? 42 : 0)

                HStack(spacing: 6) {
                    plusButton
                    if open {
                        modelButton
                            .transition(.opacity.combined(with: .scale(scale: 0.94, anchor: .leading)))
                        if shellDraft {
                            Label("Direct shell", systemImage: "terminal")
                                .font(.caption.weight(.medium))
                                .foregroundStyle(.secondary)
                                .padding(.horizontal, 9).frame(height: 36)
                                .background(Palette.fill2, in: Capsule())
                                .accessibilityIdentifier("thread-shell-badge")
                                .transition(.opacity.combined(with: .scale(scale: 0.94, anchor: .leading)))
                        }
                    }
                    Spacer(minLength: 0)
                    ends
                }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier(open ? "thread-bar" : "")
            }
        }
        .padding(6)
        .glassEffect(.regular, in: boxShape)
        .background {
            if hasFocusGlow {
                ComposerFocusHalo(color: glowColor)
                    .opacity(boxFocused ? 1 : 0)
            }
        }
        .overlay {
            if hasFocusGlow {
                boxShape
                    .strokeBorder(glowColor.opacity(0.45), lineWidth: 1)
                    .opacity(boxFocused ? 1 : 0)
                    .allowsHitTesting(false)
            }
        }
        .keyboardAwayOnFlickDown()
        .animation(composerAnimation, value: who)
        .onAppear {
            boxOpen = boxShouldOpen
            boxFocused = focused
        }
        .onChange(of: [boxShouldOpen, focused]) {
            guard boxOpen != boxShouldOpen || boxFocused != focused else { return }
            Task { @MainActor in
                withAnimation(composerAnimation) {
                    boxOpen = boxShouldOpen
                    boxFocused = focused
                }
            }
        }
        .onChange(of: composerState.text) { _, next in
            guard loadedDraftKey == draftKey else { return }
            model.drafts.save(ComposerDraft(text: next, attachments: attached.map(\.ref)), for: draftKey)
        }
        .padding(.horizontal, 12)
        .photosPicker(isPresented: $pickingPhotos, selection: $photos, maxSelectionCount: 10, matching: .images)
        .onChange(of: photos) {
            let picked = photos
            guard !picked.isEmpty else { return }
            let targetKey = draftKey
            let targetMachineID = effectiveMachineID
            photos = []
            uploading += picked.count
            Task {
                for item in picked {
                    if let data = try? await item.loadTransferable(type: Data.self), let jpeg = HerComposer.jpeg(data) {
                        await upload(name: String(localized: "Photo") + ".jpg", mime: "image/jpeg", data: jpeg, picture: UIImage(data: jpeg), targetKey: targetKey, targetMachineID: targetMachineID)
                    }
                    uploading -= 1
                }
            }
        }
        .fileImporter(isPresented: $pickingFiles, allowedContentTypes: [.item], allowsMultipleSelection: true) { result in
            guard case .success(let urls) = result else { return }
            let targetKey = draftKey
            let targetMachineID = effectiveMachineID
            uploading += urls.count
            Task {
                for url in urls {
                    let scoped = url.startAccessingSecurityScopedResource()
                    let data = try? Data(contentsOf: url)
                    if scoped { url.stopAccessingSecurityScopedResource() }
                    if let data {
                        let mime = UTType(filenameExtension: url.pathExtension)?.preferredMIMEType ?? "application/octet-stream"
                        let picture = (mime.lowercased().hasPrefix("image/") || fileView(url.lastPathComponent) == .image) ? UIImage(data: data) : nil
                        await upload(name: url.lastPathComponent, mime: mime, data: data, picture: picture, targetKey: targetKey, targetMachineID: targetMachineID)
                    } else {
                        model.show(String(localized: "Couldn’t read \(url.lastPathComponent)"))
                    }
                    uploading -= 1
                }
            }
        }
    }

    private func isImageRef(_ ref: Hermesh_Session_V1_AttachmentRef) -> Bool {
        ref.mimeType.lowercased().hasPrefix("image/") || fileView(ref.name) == .image
    }

    private func loadDraftIfNeeded(for key: String) {
        guard loadedDraftKey != key else { return }
        let saved = model.drafts.draft(for: key)
        loadedDraftKey = key
        draft = saved.text
        let store = SessionAttachmentStore.shared
        attached = saved.attachments.map { ref in
            Attached(ref: ref, picture: store.pictures[ref.blobSha256] ?? nil)
        }
        boxOpen = boxShouldOpen
        boxFocused = focused
        for ref in saved.attachments where isImageRef(ref) && store.pictures[ref.blobSha256] == nil {
            Task { await store.loadPicture(api: model.api, sessionID: sessionID, machineID: effectiveMachineID, ref: ref) }
        }
    }

    private func removeAttached(_ sha: String) {
        attached.removeAll { $0.id == sha }
        if let key = loadedDraftKey {
            model.drafts.removeAttachment(sha256: sha, for: key)
        }
    }

    private func add(photos: Bool) {
        attaching = false
        Task {
            try? await Task.sleep(for: .milliseconds(400))
            if photos { pickingPhotos = true } else { pickingFiles = true }
        }
    }

    private func upload(name: String, mime: String, data: Data, picture: UIImage?, targetKey: String, targetMachineID: String) async {
        guard let api = model.api else { return }
        do {
            let ref = try await api.uploadBlob(machineID: targetMachineID, name: name, mimeType: mime, data: data).attachment
            SessionAttachmentStore.shared.keep(sha256: ref.blobSha256, name: name, data: data, picture: picture)
            model.drafts.addAttachment(ref, for: targetKey)
            if loadedDraftKey == targetKey, !attached.contains(where: { $0.id == ref.blobSha256 }) {
                attached.append(Attached(ref: ref, picture: picture))
            }
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
        let reasoning = reasoningOf(providers, selection)
        let effortText = reasoning.map { r in levelTitle(r.value, r.choices.first { $0.id == r.value }?.label ?? r.value) }
        return Button {
            focused = false
            pickingModel = true
        } label: {
            HStack(spacing: 6) {
                if let inst = providerInstance(providers, selection.instanceID) {
                    AgentLogo(id: inst.instanceID, size: 16)
                } else if selection.instanceID == herAgent {
                    AgentLogo(id: herAgent, size: 16)
                }
                Text(verbatim: modelTitle).lineLimit(1)
                if let effortText {
                    Text(verbatim: effortText).foregroundStyle(.secondary).lineLimit(1)
                }
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
                Button {
                    setWorktree(!place.worktree)
                } label: {
                    HStack(spacing: 4) {
                        Image(systemName: place.worktree ? "arrow.triangle.branch" : "folder")
                            .contentTransition(.symbolEffect(.replace))
                        Text(verbatim: place.worktree ? String(localized: "Separate worktree") : String(localized: "Current checkout"))
                            .lineLimit(1)
                        Image(systemName: "arrow.left.arrow.right")
                            .font(.caption2.weight(.semibold))
                            .foregroundStyle(.tertiary)
                            .rotationEffect(.degrees(worktreeTurns * 180))
                    }
                    .font(.footnote).foregroundStyle(.primary)
                    .padding(.horizontal, 10).frame(height: 32)
                    .background(Palette.fill2, in: Capsule())
                }
                .tint(.primary)
                .sensoryFeedback(.selection, trigger: worktreeTurns)
                .contextMenu {
                    Section("Working copy") {
                        Button { setWorktree(false) } label: {
                            Label { Text("Current checkout"); Text("Works on the project’s own folder") } icon: { Image(systemName: !place.worktree ? "checkmark" : "folder") }
                        }
                        Button { setWorktree(true) } label: {
                            Label { Text("Separate worktree"); Text("A branch of its own; your files are left alone") } icon: { Image(systemName: place.worktree ? "checkmark" : "arrow.triangle.branch") }
                        }
                    }
                }
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

    // MARK: Choosing

    private func setWorktree(_ next: Bool) {
        placeEdited = true
        guard place.worktree != next else { return }
        withAnimation(.spring(duration: 0.3, bounce: 0.25)) {
            worktreeTurns += 1
            place.worktree = next
        }
        SessionChoices.rememberWorktree(machine: machineID, project: projectID, worktree: next)
    }

    private func findProject(machine id: String, project pid: String) -> Project? {
        projects(on: id).list.first { $0.projectID == pid } ?? SessionChoices.lastProjects(id).first { $0.projectID == pid }
    }

    private func pick(machine id: String) {
        machineID = id
        pick(project: projectOnMachine(projects(on: id).list, machineID: id, current: projectID, lastUsed: SessionChoices.mine(id)?.projectID), in: projects(on: id).list)
    }

    private func pick(project id: String, in list: [Project]) {
        projectID = id
        placeEdited = false
        let pr = list.first { $0.projectID == id } ?? SessionChoices.lastProjects(machineID).first { $0.projectID == id }
        place = SessionPlace(worktree: projectWorktree(pr, remembered: SessionChoices.worktree(machine: machineID, project: id)))
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
        rise = false
        if isNew {
            projection = nil
            let known = { (id: String) in !id.isEmpty && model.machines.contains { $0.machineID == id } }
            let mid = [askedMachine, SessionChoices.lastMachine ?? ""].first(where: known) ?? model.machines.first(where: \.local)?.machineID ?? model.machines.first?.machineID ?? ""
            let list = projects(on: mid).list
            let pid = (askedProject.isEmpty || askedProject == herSection) ? projectOnMachine(list, machineID: mid, current: "", lastUsed: SessionChoices.mine(mid)?.projectID) : askedProject
            let wantsHer = askedProject == herSection || (askedMachine.isEmpty && askedProject.isEmpty && NewSessionWho.remembered(persistent: !model.isDemo) == .her)
            let her = wantsHer ? herAt : nil
            machineID = her?.machineID ?? mid
            pick(project: her != nil ? scratchProject : pid, in: projects(on: her?.machineID ?? mid).list)
            if her != nil {
                if askedProject == herSection { NewSessionWho.remember(.her, persistent: !model.isDemo) }
                selection = herSelection()
            } else if selection.instanceID == herAgent {
                selection = Hermesh_Session_V1_ModelSelection()
            }
            isDraggingRole = false
            savedAgentMachineID = ""
            savedAgentProjectID = ""
            var t = Transaction()
            t.disablesAnimations = true
            withTransaction(t) { roleProgress = her != nil ? 1 : 0 }
            if let m = SessionChoices.mine(mid)?.mode, let saved = Hermesh_Identity_V1_RuntimeMode(rawValue: m) { mode = saved }
            SessionChoices.keepProjects(model.projects, machines: model.machines)
            initialized = true
            return
        }
        if projection?.session.sessionID != sessionID {
            if let cached = model.conversations.projection(sessionID) {
                apply(cached.session)
                projection = cached
            } else {
                projection = nil
            }
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
        // To Her: the model chosen on this page, else the last Her model chosen on this machine, else Her's default.
        if selection.instanceID == herAgent && herAt != nil {
            selection = herSelection(providers: res.providers, machineID: mid, prev: selection)
        } else if let next = agentSelection(providers: res.providers, machineID: mid, prev: selection) {
            selection = next
        }
    }

    private func send(explicitIntent: Hermesh_Session_V1_DispatchIntent? = nil) {
        if focused, let field = UIResponder.herFirst, (field as? UITextInput)?.markedTextRange != nil {
            focused = false
            _ = field.resignFirstResponder()
            Task { @MainActor in sendCommitted(explicitIntent: explicitIntent) }
            return
        }
        focused = false
        hideKeyboard()
        sendCommitted(explicitIntent: explicitIntent)
    }

    private func sendCommitted(explicitIntent: Hermesh_Session_V1_DispatchIntent? = nil) {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty || !attached.isEmpty, let api = model.api else { return }
        if isNew && who == .her && !model.isDemo && !model.her.aiConsentAccepted {
            guard aiConsentPrompt == nil else { return }
            let entry = modelEntry(providers, selection)
            let slug = entry?.slug ?? selection.model
            let providerLabel = slug.split(separator: "/", maxSplits: 1).first.map(String.init)
            let modelLabel = entry?.name ?? (slug.isEmpty ? nil : slug)
            aiConsentPrompt = HerAIConsentPrompt(provider: providerLabel, model: modelLabel, allow: {
                model.her.aiConsentAccepted = true
                send(explicitIntent: explicitIntent)
            })
            return
        }
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
                model.drafts.clear(ComposerDraftStore.newSessionKey)
                loadedDraftKey = "starting:\(st.request.commandID)"
                draft = ""
                attached = []
                starting = st
                Task { await begin(st, api) }
            }
            return
        }
        let targetKey = draftKey
        let modelDrafts = model.drafts
        modelDrafts.clear(targetKey)
        draft = ""
        attached = []
        spoken += 1
        let defaultIntent: Hermesh_Session_V1_DispatchIntent = if !online {
            .queue
        } else if currentSession?.status == .waitingInput {
            .auto
        } else if running {
            canLiveSteer ? .steer : .queue
        } else {
            .auto
        }
        var r = Hermesh_Session_V1_DispatchMessageRequest()
        r.commandID = UUID().uuidString
        r.sessionID = sessionID
        r.text = text
        r.locale = AppLocale.current
        r.attachments = files.map(\.ref)
        r.intent = explicitIntent ?? defaultIntent
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
                let restored = modelDrafts.restore(ComposerDraft(text: text, attachments: files.map(\.ref)), for: targetKey)
                if loadedDraftKey == targetKey {
                    draft = restored.text
                    let store = SessionAttachmentStore.shared
                    let bySha = Dictionary(uniqueKeysWithValues: (files + attached).map { ($0.id, $0.picture) })
                    attached = restored.attachments.map { ref in
                        Attached(ref: ref, picture: bySha[ref.blobSha256] ?? (store.pictures[ref.blobSha256] ?? nil))
                    }
                }
            }
        }
    }

    private func codeActions(for s: Session) -> SessionCodeActions? {
        guard !s.sessionID.isEmpty, s.status != .archived, let api = model.api else { return nil }
        let sid = s.sessionID
        let mid = s.machineID
        return SessionCodeActions(
            sessionID: sid,
            runStream: { cmd in api.runCommand(sid, command: cmd, machineID: mid) },
            sendOutputToChat: { cmd, code, out, stopped in
                sendOutputToChat(sessionID: sid, command: cmd, exitCode: code, output: out, stopped: stopped)
            }
        )
    }

    private func sendOutputToChat(sessionID sid: String, command: String, exitCode: Int32, output: String, stopped: Bool) {
        guard !sid.isEmpty, currentSession?.status != .archived, let api = model.api else { return }
        let cleanOut = stripTerminalEscapes(output).trimmingCharacters(in: .whitespacesAndNewlines)
        let capped: String = if cleanOut.count > 12_000 {
            "\(cleanOut.prefix(6_000))\n...[truncated]...\n\(cleanOut.suffix(6_000))"
        } else {
            cleanOut
        }
        let header = stopped
            ? String(localized: "Command stopped manually; output so far:")
            : String(localized: "Command result (exit code \(Int(exitCode))):")
        let bodyText = capped.isEmpty
            ? "\(header)\n```bash\n\(command)\n```\n\(String(localized: "(no output)"))"
            : "\(header)\n```bash\n\(command)\n```\n```text\n\(capped)\n```"
        spoken += 1
        var r = Hermesh_Session_V1_DispatchMessageRequest()
        r.commandID = UUID().uuidString
        r.sessionID = sid
        r.text = bodyText
        r.locale = AppLocale.current
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
            // An agent's choices are kept for the next session; Her's stay hers (NewSessionWho).
            if st.request.modelSelection.instanceID != herAgent {
                let pr = findProject(machine: st.request.machineID, project: st.request.projectID)
                let canWt = pr?.repo == true || pr?.workspace == .worktree
                SessionChoices.remember(
                    machine: st.request.machineID,
                    project: st.request.projectID,
                    selection: selection,
                    mode: clamped,
                    worktree: canWt ? st.request.workspace.mode == .worktree : nil
                )
            }
            // "Sending…", once shown (at 400 ms), stays half a second.
            let took = Date.now.timeIntervalSince(began)
            if took > 0.4 && took < 0.9 { try? await Task.sleep(for: .seconds(0.9 - took)) }
            let id = res.session.sessionID
            model.drafts.move(from: "starting:\(st.request.commandID)", to: ComposerDraftStore.key(sessionID: id))
            model.noteStarted(res.session)
            if !res.session.isHerConversation {
                model.conversations.keep(id, startedProjection(res, st))
                model.seededSessions.insert(id)
            }
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
        let cmdKey = "starting:\(st.request.commandID)"
        model.drafts.restore(ComposerDraft(text: st.request.initialPrompt, attachments: st.files.map(\.ref)), for: ComposerDraftStore.newSessionKey)
        model.drafts.move(from: cmdKey, to: ComposerDraftStore.newSessionKey)
        starting = nil
        loadedDraftKey = nil
        loadDraftIfNeeded(for: ComposerDraftStore.newSessionKey)
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
            do {
                guard let res = try await model.api?.stopBackgroundTask(sessionID, taskID: taskID) else { return }
                if var cur = projection {
                    var stoppedByID = Dictionary(uniqueKeysWithValues: res.stoppedTasks.map { ($0.taskID, $0) })
                    let stoppedItems = Set(res.stoppedTasks.map(\.itemID).filter { !$0.isEmpty })
                    for idx in cur.backgroundTasks.indices {
                        if let hit = stoppedByID.removeValue(forKey: cur.backgroundTasks[idx].taskID) {
                            cur.backgroundTasks[idx] = hit
                        }
                    }
                    for bt in res.stoppedTasks where stoppedByID[bt.taskID] != nil {
                        cur.backgroundTasks.append(bt)
                    }
                    if !stoppedItems.isEmpty {
                        for idx in cur.items.indices where stoppedItems.contains(cur.items[idx].itemID) && cur.items[idx].status == "running" {
                            cur.items[idx].streaming = false
                            cur.items[idx].status = "interrupted"
                        }
                    }
                    if res.hasSession { cur.session = res.session }
                    projection = cur
                }
            } catch { model.report(error) }
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
        guard !managingRuns.contains(run.runID), let api = model.api else { return }
        var r = Hermesh_Session_V1_ManageQueueRequest()
        r.commandID = UUID().uuidString
        r.sessionID = sessionID
        r.runID = run.runID
        r.operation = op
        managingRuns.insert(run.runID)
        Task {
            defer { managingRuns.remove(run.runID) }
            do {
                let res = try await api.manageQueue(r)
                if op == .promoteToSteer { spoken += 1 }
                reconcileQueueResponse(res)
            } catch {
                model.report(error)
            }
        }
    }

    private func reconcileQueueResponse(_ res: Hermesh_Session_V1_ManageQueueResponse) {
        guard var cur = projection else { return }
        let prevSeq = cur.session.progress.lastEventSeq
        let resSeq = res.session.progress.lastEventSeq
        if resSeq > 0, prevSeq >= resSeq, !res.session.offlineQueued, !cur.session.offlineQueued {
            return
        }
        var stillQueued = Dictionary(uniqueKeysWithValues: res.queuedRuns.map { ($0.runID, $0) })
        let shown = Set(cur.items.filter { $0.kind == .userMessage }.map(\.runID))
        let activeID = res.hasSession ? res.session.activeRunID : cur.session.activeRunID
        for idx in cur.runs.indices {
            let rid = cur.runs[idx].runID
            if let q = stillQueued.removeValue(forKey: rid) {
                cur.runs[idx] = q
            } else if cur.runs[idx].status == .queued, !shown.contains(rid), rid != activeID {
                cur.runs[idx].status = .cancelled
            }
        }
        for q in res.queuedRuns where stillQueued[q.runID] != nil {
            cur.runs.append(q)
        }
        if res.hasSession { cur.session = res.session }
        projection = cur
    }
}

/// A new session's first message on its way: what was asked, the files with it, when, and why it was refused if it was.
private struct Starting {
    let request: Hermesh_Session_V1_StartSessionRequest
    let files: [Attached]
    let at = Date.now
    var refused: String?
}

extension SessionPage {
    /// A file in the box, before it is sent: a picture large, with the box's other pictures; a page, Markdown or code on
    /// its page. Its bytes are on this iPhone already.
    fileprivate func openAttached(_ a: Attached) {
        focused = false
        hideKeyboard()
        let store = SessionAttachmentStore.shared
        let hasPic = { (x: Attached) in (x.picture ?? (store.pictures[x.id] ?? nil)) != nil }
        if hasPic(a) {
            let pictures = attached.filter(hasPic)
            boxLightbox = SessionLightboxPresentation(sessionID: sessionID, machineID: effectiveMachineID, run: "", items: pictures.map { .attachment($0.ref) },
                                                      initialIndex: pictures.firstIndex { $0.id == a.id } ?? 0)
        } else if attachmentOpensInApp(a.ref) {
            model.path.append(.sessionAttachment(sessionID, effectiveMachineID, a.ref))
        }
    }
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

func crossProviderSwitchFrom(_ proj: Hermesh_Session_V1_SessionProjection?, current: Hermesh_Session_V1_ModelSelection, next: Hermesh_Session_V1_ModelSelection) -> String? {
    guard let proj, proj.hasSession, !next.instanceID.isEmpty, next.instanceID != current.instanceID else { return nil }
    let s = proj.session
    let hasHistory = !s.activeProviderSessionID.isEmpty || s.progress.turnCount > 0
    guard hasHistory else { return nil }
    var bound = ""
    for r in proj.runs.reversed() where r.status != .queued && r.status != .rolledBack && !r.modelSelection.instanceID.isEmpty {
        bound = r.modelSelection.instanceID
        break
    }
    if bound.isEmpty {
        bound = !current.instanceID.isEmpty ? current.instanceID : (!s.modelSelection.instanceID.isEmpty ? s.modelSelection.instanceID : s.agent)
    }
    guard !bound.isEmpty, next.instanceID != bound else { return nil }
    return bound
}

private struct PendingProviderSwitch {
    let fromName: String
    let toName: String
    let selection: Hermesh_Session_V1_ModelSelection
    let changed: (from: String, to: String)?
}

private struct ModelSheet: View {
    @Environment(\.dismiss) private var dismiss
    let providers: [Hermesh_Session_V1_ProviderInstance]
    @Binding var selection: Hermesh_Session_V1_ModelSelection
    let projection: Hermesh_Session_V1_SessionProjection?
    let hasActiveBackgroundTasks: Bool
    let stale: Bool
    let machineName: String
    let projectAgents: [String]
    let levelTitle: (String, String) -> String
    let changed: ((from: String, to: String)) -> Void
    @State private var query = ""
    @State private var pendingSwitch: PendingProviderSwitch?

    var body: some View {
        let usable = providers
            .filter { p in providerUnusable(p, projectAgents: p.instanceID == herAgent ? [] : projectAgents).map { $0 == .signedOut } ?? true }
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
        .alert(pendingSwitch.map { String(localized: "Switch to \($0.toName)?") } ?? "", isPresented: Binding(
            get: { pendingSwitch != nil },
            set: { if !$0 { pendingSwitch = nil } }
        ), presenting: pendingSwitch) { sw in
            Button(String(localized: "Switch to \(sw.toName)")) {
                pendingSwitch = nil
                selection = sw.selection
                if let c = sw.changed { changed(c) }
                dismiss()
            }
            Button("Cancel", role: .cancel) { pendingSwitch = nil }
        } message: { sw in
            if hasActiveBackgroundTasks {
                Text("Workspace file changes are kept. On your next message, running background tasks in \(sw.fromName) will stop and earlier conversation will be summarized for \(sw.toName).")
            } else {
                Text("Workspace file changes are kept. On your next message, earlier conversation with \(sw.fromName) will be summarized for \(sw.toName) without full raw tool outputs.")
            }
        }
    }

    private func row(_ p: Hermesh_Session_V1_ProviderInstance, _ m: Hermesh_Session_V1_ModelCatalogEntry) -> some View {
        Button {
            let r = selectModel(providers, from: selection, instanceID: p.instanceID, slug: m.slug)
            if let fromID = crossProviderSwitchFrom(projection, current: selection, next: r.selection) {
                let fromName = providers.first { $0.instanceID == fromID }.map { $0.displayName.isEmpty ? fromID : $0.displayName } ?? fromID
                let toName = p.displayName.isEmpty ? p.instanceID : p.displayName
                pendingSwitch = PendingProviderSwitch(fromName: fromName, toName: toName, selection: r.selection, changed: r.changed)
                return
            }
            selection = r.selection
            if let c = r.changed { changed(c) }
            dismiss()
        } label: {
            HStack {
                Text(m.name)
                if p.instanceID == herAgent && m.isDefault {
                    Text("Default").font(.caption2).foregroundStyle(.secondary)
                } else if !m.badge.isEmpty {
                    Text("New").font(.caption2).foregroundStyle(.tint)
                }
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

// MARK: Background Tasks Formatting & Views

private enum BackgroundTaskFormat {
    private static let genericSchedules: Set<String> = ["run schedule", "schedule", "schedulewakeup"]

    static func label(of bt: Hermesh_Session_V1_BackgroundTask) -> String {
        let title = bt.title.replacingOccurrences(of: #"\s+"#, with: " ", options: .regularExpression).trimmingCharacters(in: .whitespacesAndNewlines)
        let cmd = bt.command.replacingOccurrences(of: #"\s+"#, with: " ", options: .regularExpression).trimmingCharacters(in: .whitespacesAndNewlines)
        let sum = bt.summary.replacingOccurrences(of: #"\s+"#, with: " ", options: .regularExpression).trimmingCharacters(in: .whitespacesAndNewlines)
        if genericSchedules.contains(title.lowercased()) {
            if !sum.isEmpty && sum != "stopped by user" { return sum }
            if !cmd.isEmpty { return "\(String(localized: "Schedule")) · \(cmd)" }
        }
        if !title.isEmpty { return title }
        if !cmd.isEmpty { return cmd }
        return bt.taskID
    }

    static func iconName(_ kind: Hermesh_Session_V1_BackgroundTaskKind) -> String {
        switch kind {
        case .subagent: "sparkles"
        case .schedule: "clock"
        case .daemon: "waveform.path.ecg"
        default: "terminal"
        }
    }

    static func kindTitle(_ kind: Hermesh_Session_V1_BackgroundTaskKind) -> String {
        switch kind {
        case .subagent: String(localized: "Sub-agent")
        case .schedule: String(localized: "Schedule")
        case .daemon: String(localized: "Service")
        default: "Shell"
        }
    }

    static func stripANSI(_ text: String) -> String {
        text.replacingOccurrences(of: #"\u{001B}\[[0-9;]*[A-Za-z]"#, with: "", options: .regularExpression)
    }

    static func latestOutputLine(_ outputTail: String) -> String {
        stripANSI(outputTail)
            .components(separatedBy: .newlines)
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .last { !$0.isEmpty } ?? ""
    }

    static func timing(of bt: Hermesh_Session_V1_BackgroundTask, now: Date) -> String {
        guard let started = dateOf(bt.startedAt, set: bt.hasStartedAt) else { return "" }
        let end: Date
        if bt.status == .running {
            end = now
        } else if let done = dateOf(bt.completedAt, set: bt.hasCompletedAt) ?? dateOf(bt.updatedAt, set: bt.hasUpdatedAt) {
            end = done
        } else {
            return ""
        }
        let sec = max(1, Int(round(end.timeIntervalSince(started))))
        return Duration.seconds(sec).formatted(
            .units(allowed: [.days, .hours, .minutes, .seconds], width: .abbreviated, maximumUnitCount: 2)
        )
    }

    static func statusLine(of bt: Hermesh_Session_V1_BackgroundTask, now: Date) -> (text: String, tint: Color?) {
        let dur = timing(of: bt, now: now)
        switch bt.status {
        case .running:
            return (dur, nil)
        case .completed:
            let base = String(localized: "Completed")
            return ([base, dur].filter { !$0.isEmpty }.joined(separator: " · "), nil)
        case .failed:
            let base = bt.hasExitCode && bt.exitCode != 0
                ? String(localized: "Failed (exit \(bt.exitCode))")
                : String(localized: "Failed")
            return ([base, dur].filter { !$0.isEmpty }.joined(separator: " · "), .red)
        default:
            let base = String(localized: "Stopped")
            return ([base, dur].filter { !$0.isEmpty }.joined(separator: " · "), .orange)
        }
    }
}

/// Interactive background tasks panel used both as a sheet on slab iPhones and as a side column on wide screens.
private struct BackgroundTasksPanel: View {
    let tasks: [Hermesh_Session_V1_BackgroundTask]
    @Binding var selectedTaskID: String?
    let onClose: () -> Void
    let onStop: (String) -> Void

    @State private var path: [String] = []
    @State private var foldRunning = false
    @State private var foldCompleted = false
    @State private var confirmStopAll = false

    private var runningTasks: [Hermesh_Session_V1_BackgroundTask] {
        tasks.filter { $0.status == .running }
    }

    private var recentTasks: [Hermesh_Session_V1_BackgroundTask] {
        tasks.filter { $0.status != .running }
    }

    private static let cardFill = Color(UIColor {
        $0.userInterfaceStyle == .dark
            ? UIColor(red: 0x1b / 255, green: 0x1b / 255, blue: 0x20 / 255, alpha: 1)
            : UIColor.white
    })

    private static let panelFill = Color(UIColor {
        $0.userInterfaceStyle == .dark
            ? UIColor(red: 0x0e / 255, green: 0x0e / 255, blue: 0x11 / 255, alpha: 1)
            : UIColor(red: 0xf2 / 255, green: 0xf3 / 255, blue: 0xf7 / 255, alpha: 1)
    })

    var body: some View {
        NavigationStack(path: $path) {
            VStack(spacing: 0) {
                headerBar
                TimelineView(.periodic(from: .now, by: runningTasks.isEmpty ? 30 : 1)) { ctx in
                    ScrollView {
                        LazyVStack(alignment: .leading, spacing: 10) {
                            if !runningTasks.isEmpty {
                                sectionHeader(
                                    title: String(localized: "Running"),
                                    count: runningTasks.count,
                                    folded: foldRunning
                                ) {
                                    withAnimation(.snappy(duration: 0.22)) { foldRunning.toggle() }
                                }
                                if !foldRunning {
                                    ForEach(runningTasks, id: \.taskID) { bt in
                                        taskCard(bt, now: ctx.date)
                                    }
                                }
                            }
                            if !recentTasks.isEmpty {
                                sectionHeader(
                                    title: String(localized: "Completed"),
                                    count: recentTasks.count,
                                    folded: foldCompleted,
                                    trailingNote: recentTasks.contains { $0.status == .failed } ? String(localized: "Failed") : nil
                                ) {
                                    withAnimation(.snappy(duration: 0.22)) { foldCompleted.toggle() }
                                }
                                .padding(.top, runningTasks.isEmpty ? 0 : 6)
                                if !foldCompleted {
                                    ForEach(recentTasks, id: \.taskID) { bt in
                                        taskCard(bt, now: ctx.date)
                                    }
                                }
                            }
                        }
                        .padding(.horizontal, 16)
                        .padding(.top, 4)
                        .padding(.bottom, 28)
                    }
                }
            }
            .background(Self.panelFill.ignoresSafeArea())
            .toolbar(.hidden, for: .navigationBar)
            .navigationDestination(for: String.self) { taskID in
                BackgroundTaskDetailView(
                    tasks: tasks,
                    taskID: taskID,
                    panelFill: Self.panelFill,
                    cardFill: Self.cardFill,
                    onBack: { if !path.isEmpty { path.removeLast() } },
                    onClose: onClose,
                    onStop: onStop
                )
                .toolbar(.hidden, for: .navigationBar)
            }
        }
        .onAppear {
            syncSelectedTask()
        }
        .onChange(of: selectedTaskID) { _, _ in
            syncSelectedTask()
        }
        .onChange(of: runningTasks.count) { _, cnt in
            if cnt <= 1 { confirmStopAll = false }
        }
        .accessibilityIdentifier("background-tasks-panel")
    }

    private func syncSelectedTask() {
        guard let id = selectedTaskID, tasks.contains(where: { $0.taskID == id }) else { return }
        if path.last != id { path = [id] }
    }

    private var headerBar: some View {
        HStack(spacing: 10) {
            Text("Background Tasks")
                .font(.title3.weight(.semibold))
                .foregroundStyle(.primary)
            Spacer(minLength: 4)
            if runningTasks.count > 1 {
                Button {
                    if confirmStopAll {
                        confirmStopAll = false
                        onStop("")
                    } else {
                        withAnimation(.snappy(duration: 0.18)) { confirmStopAll = true }
                    }
                } label: {
                    Text(confirmStopAll ? String(localized: "Confirm (\(runningTasks.count))") : String(localized: "Stop All"))
                        .font(.footnote.weight(confirmStopAll ? .semibold : .medium))
                        .foregroundStyle(confirmStopAll ? Color.red : .secondary)
                        .padding(.horizontal, 11).padding(.vertical, 6)
                        .background(confirmStopAll ? Color.red.opacity(0.15) : Palette.fill2, in: Capsule())
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("thread-background-stop")
            }
            Button(action: onClose) {
                Image(systemName: "xmark")
                    .font(.system(size: 13, weight: .semibold))
                    .foregroundStyle(.secondary)
                    .frame(width: 32, height: 32)
                    .background(Palette.fill2, in: Circle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel(String(localized: "Close"))
            .accessibilityIdentifier("background-tasks-close")
        }
        .padding(.horizontal, 18)
        .padding(.top, 14)
        .padding(.bottom, 8)
    }

    private func sectionHeader(title: String, count: Int, folded: Bool, trailingNote: String? = nil, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 5) {
                Text(verbatim: title)
                    .font(.footnote.weight(.medium))
                    .foregroundStyle(.secondary)
                Text(verbatim: "\(count)")
                    .font(.footnote.weight(.medium).monospacedDigit())
                    .foregroundStyle(.tertiary)
                Image(systemName: "chevron.down")
                    .font(.system(size: 11, weight: .semibold))
                    .foregroundStyle(.tertiary)
                    .rotationEffect(.degrees(folded ? -90 : 0))
                Spacer(minLength: 0)
                if let trailingNote {
                    Text(verbatim: trailingNote)
                        .font(.caption2.weight(.medium))
                        .foregroundStyle(.red)
                }
            }
            .padding(.horizontal, 4)
            .padding(.vertical, 4)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
    }

    private func taskCard(_ bt: Hermesh_Session_V1_BackgroundTask, now: Date) -> some View {
        let isRunning = bt.status == .running
        let title = BackgroundTaskFormat.label(of: bt)
        let peek = isRunning ? BackgroundTaskFormat.latestOutputLine(bt.outputTail) : ""
        let status = BackgroundTaskFormat.statusLine(of: bt, now: now)

        return HStack(alignment: .top, spacing: 11) {
            Image(systemName: BackgroundTaskFormat.iconName(bt.kind))
                .font(.system(size: 15, weight: .medium))
                .foregroundStyle(isRunning ? .primary : .secondary)
                .frame(width: 20, height: 20)
                .padding(.top, 1)

            VStack(alignment: .leading, spacing: 5) {
                Text(verbatim: title)
                    .font(.subheadline)
                    .foregroundStyle(isRunning ? Color.primary.opacity(0.76) : Color.primary)
                    .lineLimit(2)
                    .multilineTextAlignment(.leading)

                if !peek.isEmpty {
                    Text(verbatim: peek)
                        .font(.system(size: 11.5, design: .monospaced))
                        .foregroundStyle(.tertiary)
                        .lineLimit(1)
                }

                HStack(spacing: 8) {
                    Text(verbatim: BackgroundTaskFormat.kindTitle(bt.kind))
                        .font(.caption.weight(.medium))
                        .foregroundStyle(.primary)
                    if !status.text.isEmpty {
                        Text(verbatim: status.text)
                            .font(.caption.monospacedDigit())
                            .foregroundStyle(status.tint ?? .secondary)
                            .lineLimit(1)
                    }
                }
                .padding(.top, 1)
            }
            .frame(maxWidth: .infinity, alignment: .leading)

            if isRunning {
                BackgroundStopRingButton {
                    confirmStopAll = false
                    onStop(bt.taskID)
                }
                .accessibilityLabel(String(localized: "Stop"))
                .accessibilityIdentifier("bg-task-stop-\(bt.taskID)")
            } else {
                Image(systemName: "chevron.right")
                    .font(.system(size: 13, weight: .semibold))
                    .foregroundStyle(.tertiary)
                    .frame(width: 20, height: 22)
                    .padding(.top, 1)
            }
        }
        .padding(.horizontal, 15)
        .padding(.vertical, 14)
        .background(
            RoundedRectangle(cornerRadius: 18, style: .continuous)
                .fill(Self.cardFill)
                .overlay(
                    RoundedRectangle(cornerRadius: 18, style: .continuous)
                        .strokeBorder(Palette.settledAnswerBorder, lineWidth: 0.5)
                )
        )
        .contentShape(.rect)
        .onTapGesture {
            confirmStopAll = false
            selectedTaskID = bt.taskID
            path.append(bt.taskID)
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("bg-task-\(bt.taskID)")
    }
}

/// Circular subtle stop ring (`◎`) with rotating active progress arc and inner rounded square.
private struct BackgroundStopRingButton: View {
    let action: () -> Void
    @State private var spinning = false

    var body: some View {
        Button(action: action) {
            ZStack {
                Circle()
                    .stroke(Color.primary.opacity(0.24), lineWidth: 1.6)
                    .frame(width: 26, height: 26)
                Circle()
                    .trim(from: 0, to: 0.28)
                    .stroke(Color.primary.opacity(0.86), style: StrokeStyle(lineWidth: 1.7, lineCap: .round))
                    .frame(width: 26, height: 26)
                    .rotationEffect(.degrees(spinning ? 360 : 0))
                    .animation(.linear(duration: 2.4).repeatForever(autoreverses: false), value: spinning)
                RoundedRectangle(cornerRadius: 2, style: .continuous)
                    .fill(Color.primary)
                    .frame(width: 7.5, height: 7.5)
            }
            .frame(width: 30, height: 30)
            .contentShape(Circle())
        }
        .buttonStyle(.plain)
        .onAppear { spinning = true }
    }
}

/// Pushed execution detail view for any running or finished background task.
private struct BackgroundTaskDetailView: View {
    let tasks: [Hermesh_Session_V1_BackgroundTask]
    let taskID: String
    let panelFill: Color
    let cardFill: Color
    let onBack: () -> Void
    let onClose: () -> Void
    let onStop: (String) -> Void

    @State private var commandFolded = false
    @State private var copied = false

    private var task: Hermesh_Session_V1_BackgroundTask? {
        tasks.first { $0.taskID == taskID }
    }

    var body: some View {
        VStack(spacing: 0) {
            topBar
            Divider()
            if let bt = task {
                TimelineView(.periodic(from: .now, by: bt.status == .running ? 1 : 30)) { ctx in
                    detailContent(bt, now: ctx.date)
                }
            } else {
                Spacer()
            }
        }
        .background(panelFill.ignoresSafeArea())
        .accessibilityIdentifier("background-task-detail")
    }

    private var topBar: some View {
        HStack(spacing: 8) {
            Button(action: onBack) {
                HStack(spacing: 4) {
                    Image(systemName: "chevron.left")
                        .font(.system(size: 14, weight: .semibold))
                    Text("Background Tasks")
                        .font(.subheadline.weight(.medium))
                }
                .foregroundStyle(.primary)
                .padding(.vertical, 6)
                .padding(.trailing, 8)
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("background-detail-back")

            Spacer(minLength: 4)

            if let bt = task, bt.status == .running {
                Button {
                    onStop(bt.taskID)
                } label: {
                    Text("Stop")
                        .font(.footnote.weight(.medium))
                        .foregroundStyle(.red)
                        .padding(.horizontal, 11).padding(.vertical, 6)
                        .background(Color.red.opacity(0.14), in: Capsule())
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("bg-detail-stop-\(bt.taskID)")
            }

            Button(action: onClose) {
                Image(systemName: "xmark")
                    .font(.system(size: 13, weight: .semibold))
                    .foregroundStyle(.secondary)
                    .frame(width: 32, height: 32)
                    .background(Palette.fill2, in: Circle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel(String(localized: "Close"))
        }
        .padding(.horizontal, 16)
        .padding(.top, 12)
        .padding(.bottom, 10)
    }

    private func detailContent(_ bt: Hermesh_Session_V1_BackgroundTask, now: Date) -> some View {
        let isRunning = bt.status == .running
        let title = BackgroundTaskFormat.label(of: bt)
        let brief = bt.kind == .subagent
            ? (bt.command.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? bt.title : bt.command)
            : bt.command.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanOutput = BackgroundTaskFormat.stripANSI(bt.outputTail)
        let summaryTrimmed = bt.summary.trimmingCharacters(in: .whitespacesAndNewlines)
        let shownSummary = summaryTrimmed != "stopped by user" ? summaryTrimmed : ""
        let status = BackgroundTaskFormat.statusLine(of: bt, now: now)
        let badgeColor: Color = switch bt.status {
        case .running: .accentColor
        case .completed: .green
        case .failed: .red
        default: .orange
        }

        return ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                // 1. Hero status & title card
                VStack(alignment: .leading, spacing: 10) {
                    HStack(spacing: 7) {
                        Text(verbatim: status.text.isEmpty ? BackgroundTaskFormat.kindTitle(bt.kind) : (isRunning ? "\(String(localized: "Running")) · \(status.text)" : status.text))
                            .font(.caption.weight(.semibold).monospacedDigit())
                            .foregroundStyle(badgeColor)
                            .padding(.horizontal, 9).padding(.vertical, 3.5)
                            .background(badgeColor.opacity(0.14), in: Capsule())

                        Text(verbatim: BackgroundTaskFormat.kindTitle(bt.kind))
                            .font(.caption.weight(.medium))
                            .foregroundStyle(.secondary)
                            .padding(.horizontal, 8).padding(.vertical, 3.5)
                            .background(Palette.fill2, in: Capsule())
                    }

                    Text(verbatim: title)
                        .font(.callout.weight(.semibold))
                        .foregroundStyle(.primary)
                        .textSelection(.enabled)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(15)
                .background(cardSurface)

                // 2. Collapsible Command / Brief box
                if !brief.isEmpty {
                    VStack(alignment: .leading, spacing: 8) {
                        HStack {
                            Button {
                                withAnimation(.snappy(duration: 0.18)) { commandFolded.toggle() }
                            } label: {
                                HStack(spacing: 5) {
                                    Text(bt.kind == .subagent ? String(localized: "Task brief") : String(localized: "Command"))
                                        .font(.caption.weight(.medium))
                                        .foregroundStyle(.secondary)
                                    Image(systemName: "chevron.down")
                                        .font(.system(size: 10, weight: .semibold))
                                        .foregroundStyle(.tertiary)
                                        .rotationEffect(.degrees(commandFolded ? -90 : 0))
                                }
                            }
                            .buttonStyle(.plain)

                            Spacer()

                            Button {
                                UIPasteboard.general.string = brief
                                copied = true
                                Task {
                                    try? await Task.sleep(for: .milliseconds(1400))
                                    copied = false
                                }
                            } label: {
                                HStack(spacing: 4) {
                                    Image(systemName: copied ? "checkmark" : "doc.on.doc")
                                    Text(copied ? String(localized: "Copied") : String(localized: "Copy"))
                                }
                                .font(.caption2.weight(.medium))
                                .foregroundStyle(.secondary)
                                .padding(.horizontal, 8).padding(.vertical, 4)
                                .background(Palette.fill2, in: Capsule())
                            }
                            .buttonStyle(.plain)
                        }

                        if !commandFolded {
                            Text(verbatim: brief)
                                .font(bt.kind == .subagent ? .footnote : .system(size: 12, design: .monospaced))
                                .foregroundStyle(.primary)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .textSelection(.enabled)
                        }
                    }
                    .padding(14)
                    .background(cardSurface)
                }

                // 3. Terminal Output Box
                if !cleanOutput.isEmpty {
                    let lineCount = cleanOutput.components(separatedBy: .newlines).count
                    VStack(alignment: .leading, spacing: 0) {
                        HStack {
                            Text(String(localized: "Output · \(lineCount) lines"))
                                .font(.caption2.weight(.medium).monospacedDigit())
                                .foregroundStyle(Color.white.opacity(0.62))
                            Spacer()
                            if isRunning {
                                Text(String(localized: "Live"))
                                    .font(.caption2.weight(.semibold))
                                    .foregroundStyle(.green)
                            }
                        }
                        .padding(.horizontal, 13).padding(.vertical, 8)
                        .background(Color.white.opacity(0.05))

                        ScrollViewReader { proxy in
                            ScrollView(.vertical) {
                                VStack(alignment: .leading, spacing: 0) {
                                    Text(verbatim: cleanOutput)
                                        .font(.system(size: 11.5, design: .monospaced))
                                        .foregroundStyle(Color(white: 0.88))
                                        .frame(maxWidth: .infinity, alignment: .leading)
                                        .textSelection(.enabled)
                                    Color.clear.frame(height: 1).id("bg-log-bottom")
                                }
                                .padding(13)
                            }
                            .frame(maxHeight: 280)
                            .onAppear { proxy.scrollTo("bg-log-bottom", anchor: .bottom) }
                            .onChange(of: cleanOutput) { _, _ in
                                if isRunning { proxy.scrollTo("bg-log-bottom", anchor: .bottom) }
                            }
                        }
                    }
                    .background(
                        RoundedRectangle(cornerRadius: 16, style: .continuous)
                            .fill(Color(red: 0x0a / 255, green: 0x0b / 255, blue: 0x0f / 255))
                            .overlay(
                                RoundedRectangle(cornerRadius: 16, style: .continuous)
                                    .strokeBorder(Color.white.opacity(0.10), lineWidth: 0.5)
                            )
                    )
                } else if isRunning && shownSummary.isEmpty {
                    Text("No output yet")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .padding(.horizontal, 4)
                }

                // 4. Summary
                if !shownSummary.isEmpty {
                    VStack(alignment: .leading, spacing: 6) {
                        Text("Summary")
                            .font(.caption.weight(.medium))
                            .foregroundStyle(.secondary)
                        MarkdownText(text: shownSummary)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .padding(14)
                    .background(cardSurface)
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 14)
        }
    }

    private var cardSurface: some View {
        RoundedRectangle(cornerRadius: 16, style: .continuous)
            .fill(cardFill)
            .overlay(
                RoundedRectangle(cornerRadius: 16, style: .continuous)
                    .strokeBorder(Palette.settledAnswerBorder, lineWidth: 0.5)
            )
    }
}

/// Inline live checklist spine at the foot of the conversation: 18pt icon column aligned with `ConnectionLine`,
/// per-node spine segments (traversed sage solid, future 1pt dashed, zero tail after last step), and deduplicated live sub-focus + timer.
private struct SessionTodoSpine: View {
    private enum SpineSegment {
        case none
        case traversed
        case activeBridge
        case future
    }

    @Environment(\.scenePhase) private var phase
    let window: TodoSpineWindow
    let since: Date?
    let hasWork: Bool
    var thinkingText: String? = nil

    private static let tokenRegex = /`([^`]+)`|(\b[a-zA-Z0-9_.-]+\/[a-zA-Z0-9_.\/-]+|\b[a-zA-Z0-9_-]+\.(?:swift|tsx?|jsx?|go|rs|py|css|html|md|json|ya?ml|sql|db|app)\b)/

    private var activeOrderByIndex: [Int: Int] {
        Dictionary(uniqueKeysWithValues: window.active.enumerated().map { ($0.element.index, $0.offset) })
    }
    private var firstActiveIndex: Int { window.active.first?.index ?? 0 }
    private var lastActiveIndex: Int { window.active.last?.index ?? 0 }

    private func segment(for step: IndexedTodoStep) -> SpineSegment {
        guard step.index < window.total - 1 else { return .none }
        if step.index < firstActiveIndex, step.status == .completed || step.status == .skipped {
            return .traversed
        }
        if step.index >= lastActiveIndex {
            return .future
        }
        return .activeBridge
    }

    var body: some View {
        let activeOrder = activeOrderByIndex
        VStack(alignment: .leading, spacing: 0) {
            ForEach(window.all, id: \.index) { step in
                if let order = activeOrder[step.index] {
                    activeNode(step, order: order)
                } else {
                    passiveNode(step)
                }
            }
        }
        .padding(.top, 3)
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("thread-todo-card")
    }

    @ViewBuilder private func activeNode(_ step: IndexedTodoStep, order: Int) -> some View {
        let isLast = step.index == lastActiveIndex
        let showSub = isLast && window.subFocus != nil
        let deduplicatedWork = isLast && window.subFocus == nil && hasWork
        HStack(alignment: .top, spacing: 9) {
            glyphBox(active: true) { stepGlyph(step.status) }
            VStack(alignment: .leading, spacing: 3) {
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    Text(Self.styledStepText(step.text, meta: order == 0 ? "\(window.stepNumber)/\(window.total)" : nil))
                        .font(.subheadline.weight(.medium))
                        .foregroundStyle(step.status == .failed ? .red : .primary)
                        .fixedSize(horizontal: false, vertical: true)
                    if order > 0 {
                        Text("Parallel")
                            .font(.caption2.weight(.medium))
                            .foregroundStyle(Color.accentColor)
                            .padding(.horizontal, 5.5).padding(.vertical, 1.5)
                            .background(Color.accentColor.opacity(0.15), in: RoundedRectangle(cornerRadius: 4, style: .continuous))
                            .fixedSize()
                    } else if !showSub, let since {
                        HStack(spacing: 3) {
                            Text("·")
                            Text(since, style: .timer).monospacedDigit()
                        }
                        .font(.caption)
                        .foregroundStyle(.tertiary)
                        .lineLimit(1)
                        .fixedSize()
                    }
                }
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier(deduplicatedWork ? "thread-working" : "thread-todo-active-\(step.index)")

                if showSub, let subFocus = window.subFocus {
                    let subHeader = HStack(alignment: .firstTextBaseline, spacing: 6) {
                        Text(verbatim: subFocus.replacingOccurrences(of: "/", with: "/\u{200B}"))
                            .font(.footnote)
                            .foregroundStyle(window.hasFailed ? .red : .secondary)
                            .lineLimit(2)
                            .shimmer(!window.hasFailed, paused: phase != .active)
                        if let since {
                            HStack(spacing: 3) {
                                Text("·")
                                Text(since, style: .timer).monospacedDigit()
                            }
                            .font(.caption)
                            .foregroundStyle(.tertiary)
                            .lineLimit(1)
                            .fixedSize()
                        }
                    }
                    .accessibilityElement(children: .combine)
                    .accessibilityIdentifier("thread-working")

                    if let thinkingText, !thinkingText.isEmpty {
                        LiveThinkingView(text: thinkingText) {
                            subHeader
                        }
                    } else {
                        subHeader
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.vertical, 6.5)
        .background { spineLine(segment(for: step), active: true) }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("thread-todo-step-\(step.index)")
    }

    @ViewBuilder private func passiveNode(_ step: IndexedTodoStep) -> some View {
        HStack(alignment: .top, spacing: 9) {
            glyphBox(active: false) { stepGlyph(step.status) }
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(Self.styledStepText(step.text))
                    .font(.subheadline)
                    .foregroundStyle(step.status == .pending ? .secondary : .tertiary)
                    .opacity(step.status == .pending ? 0.88 : 1)
                    .fixedSize(horizontal: false, vertical: true)
                if step.status == .skipped {
                    Text("Skipped")
                        .font(.caption2.weight(.medium))
                        .foregroundStyle(.tertiary)
                        .padding(.horizontal, 5).padding(.vertical, 1)
                        .background(Palette.fill2, in: RoundedRectangle(cornerRadius: 4, style: .continuous))
                        .fixedSize()
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.vertical, 4.5)
        .background { spineLine(segment(for: step), active: false) }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("thread-todo-step-\(step.index)")
    }

    @ViewBuilder private func spineLine(_ seg: SpineSegment, active: Bool) -> some View {
        if seg != .none {
            GeometryReader { geo in
                let startY: CGFloat = active ? 25 : 22.5
                let endY: CGFloat = geo.size.height + 6.5
                if endY > startY {
                    Path { p in
                        p.move(to: CGPoint(x: 8, y: startY))
                        p.addLine(to: CGPoint(x: 8, y: endY))
                    }
                    .stroke(
                        seg == .traversed ? Color.green.opacity(0.34) : (seg == .future ? Color(UIColor.quaternaryLabel) : Palette.separator),
                        style: seg == .future ? StrokeStyle(lineWidth: 1, dash: [2.5, 2.5]) : StrokeStyle(lineWidth: 1)
                    )
                }
            }
        }
    }

    private func glyphBox<Content: View>(active: Bool, @ViewBuilder _ content: () -> Content) -> some View {
        content()
            .frame(width: 16, height: 20)
    }

    @ViewBuilder private func stepGlyph(_ status: TodoStepStatus) -> some View {
        switch status {
        case .inProgress:
            MovingMark(size: 16, motion: .seq)
        case .completed:
            Image(systemName: "checkmark")
                .font(.system(size: 10.5, weight: .semibold))
                .foregroundStyle(Color.green.opacity(0.8))
        case .skipped:
            Image(systemName: "minus")
                .font(.system(size: 10.5, weight: .semibold))
                .foregroundStyle(.tertiary)
        case .failed:
            Image(systemName: "xmark")
                .font(.system(size: 10.5, weight: .bold))
                .foregroundStyle(.red)
        case .pending:
            Circle()
                .strokeBorder(Color(UIColor.quaternaryLabel), lineWidth: 1.35)
                .frame(width: 10, height: 10)
        }
    }

    private static func styledStepText(_ raw: String, meta: String? = nil) -> AttributedString {
        var out = AttributedString()
        var cursor = raw.startIndex
        for match in raw.matches(of: tokenRegex) {
            if match.range.lowerBound > cursor {
                out.append(AttributedString(String(raw[cursor ..< match.range.lowerBound])))
            }
            let token = String(match.output.1 ?? match.output.2 ?? match.output.0)
                .replacingOccurrences(of: "/", with: "/\u{200B}")
            var codePart = AttributedString(" \(token) ")
            codePart.font = .footnote.monospaced()
            codePart.backgroundColor = UIColor.tertiarySystemFill
            out.append(codePart)
            cursor = match.range.upperBound
        }
        if cursor < raw.endIndex {
            out.append(AttributedString(String(raw[cursor...])))
        }
        if let meta, !meta.isEmpty {
            var metaPart = AttributedString("  \(meta)")
            metaPart.font = .caption.monospacedDigit()
            metaPart.foregroundColor = UIColor.tertiaryLabel
            out.append(metaPart)
        }
        return out
    }
}

/// Soft outer glow around the new-session composer, cut out on the inside so translucent glass stays untinted while
/// the halo morphs with the box's exact continuous rounded-rectangle bounds on every frame.
private struct ComposerFocusHalo: View {
    let color: Color
    private let pad: CGFloat = 48

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: 24, style: .continuous)
        ZStack {
            shape
                .fill(color.opacity(0.32))
                .offset(y: 4)
                .blur(radius: 14)
            shape
                .fill(.black)
                .blendMode(.destinationOut)
        }
        .padding(pad)
        .compositingGroup()
        .padding(-pad)
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }
}

/// Isolates composer text observation from `SessionPage.body` so keystrokes never re-evaluate or diff conversation rows.
@Observable private final class SessionComposerState {
    var text = ""
}

private struct ComposerScope<Content: View>: View {
    @ViewBuilder let content: () -> Content
    var body: some View { content() }
}

private struct ConversationStack<Content: View>: View {
    let lazy: Bool
    @ViewBuilder let content: () -> Content

    var body: some View {
        if lazy {
            LazyVStack(alignment: .leading, spacing: 13, content: content)
        } else {
            VStack(alignment: .leading, spacing: 13, content: content)
        }
    }
}

/// Memoizes reduced transcript steps, O(N) `previousTurns` dates, live reply IDs, and todo steps across non-transcript UI updates.
@MainActor private final class SessionRenderCache {
    struct Key: Equatable {
        let sessionID: String
        let lastEventSeq: UInt64
        let itemCount: Int
        let lastItemID: String
        let lastTextLen: Int
        let lastStatus: String
        let lastStreaming: Bool
        let prevTextLen: Int
        let runCount: Int
        let lastRunStatus: Int
        let reqCount: Int
        let lastReqStatus: Int
    }

    private var key: Key?
    private var steps: [Step] = []
    private var previousTurns: [Date?] = []
    private var writing: Set<UInt64> = []
    private var todos: [TodoStepView] = []
    private var latchedLazy: (sessionID: String, lazy: Bool)?

    /// Latches `LazyVStack` vs `VStack` once a session's initial steps load so streaming growth never flips container identity mid-scroll.
    func useLazy(sessionID: String, stepCount: Int) -> Bool {
        if let latchedLazy, latchedLazy.sessionID == sessionID {
            return latchedLazy.lazy
        }
        guard stepCount > 0 else { return false }
        let lazy = stepCount > 100
        latchedLazy = (sessionID, lazy)
        return lazy
    }

    func resolve(
        _ proj: Hermesh_Session_V1_SessionProjection?,
        sessionID: String
    ) -> (steps: [Step], previousTurns: [Date?], writing: Set<UInt64>, todos: [TodoStepView]) {
        let items = proj?.items ?? []
        let last = items.last
        let prev = items.count >= 2 ? items[items.count - 2] : nil
        let nextKey = Key(
            sessionID: sessionID,
            lastEventSeq: proj?.session.progress.lastEventSeq ?? 0,
            itemCount: items.count,
            lastItemID: last?.itemID ?? "",
            lastTextLen: last?.text.utf8.count ?? 0,
            lastStatus: last?.status ?? "",
            lastStreaming: last?.streaming ?? false,
            prevTextLen: prev?.text.utf8.count ?? 0,
            runCount: proj?.runs.count ?? 0,
            lastRunStatus: proj?.runs.last?.status.rawValue ?? 0,
            reqCount: proj?.requests.count ?? 0,
            lastReqStatus: proj?.requests.last?.status.rawValue ?? 0
        )
        if key == nextKey {
            return (steps, previousTurns, writing, todos)
        }
        let builtSteps = groupSteps(sessionTranscript(proj))
        var builtPrev = [Date?](repeating: nil, count: builtSteps.count)
        var seenTurn: Date?
        for (idx, step) in builtSteps.enumerated() {
            builtPrev[idx] = seenTurn
            if case .item(.turn(_, _, let at, _)) = step {
                seenTurn = at
            }
        }
        let builtWriting = sessionLiveReplies(proj)
        let builtTodos = activeTodoSteps(proj)
        key = nextKey
        steps = builtSteps
        previousTurns = builtPrev
        writing = builtWriting
        todos = builtTodos
        return (builtSteps, builtPrev, builtWriting, builtTodos)
    }
}
