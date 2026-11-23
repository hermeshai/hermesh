import HermeshKit
import HermeshProto
import HermeshSessions
import PhotosUI
import SwiftUI
import UniformTypeIdentifiers

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
    @Environment(\.scenePhase) private var scenePhase
    /// The session shown; empty on a new one.
    let threadID: String
    private let askedMachine: String
    private let askedProject: String

    @State private var machineID = ""
    @State private var projectID = scratchProject
    /// Where a new session works and on which branch (§16): this session's only.
    @State private var place = ThreadPlace()
    /// Its project's branches as its folder says, once read.
    @State private var branches: ThreadBranches?
    @State private var pickingBranch = false
    @State private var selection = Hermesh_Thread_V2_ModelSelection()
    @State private var mode: Hermesh_Identity_V1_RuntimeMode = .auto
    @State private var plan = false
    @State private var providers: [Hermesh_Thread_V2_ProviderInstance] = []
    @State private var providersStale = false
    @State private var ceiling: Hermesh_Identity_V1_RuntimeMode = .unspecified
    @State private var draft = ""
    @State private var sending = false
    /// What goes with the next message: uploaded to hermeshd as it is picked (as on the Mac).
    @State private var attached: [Attached] = []
    /// Picked and still being read or uploaded: they show, and Send waits for them.
    @State private var uploading = 0
    @State private var attaching = false
    @State private var pickingPhotos = false
    @State private var pickingFiles = false
    @State private var photos: [PhotosPickerItem] = []
    @State private var projection: Hermesh_Thread_V2_ThreadProjection?
    /// The session's events are followed: its subscription has been open a moment (followThread).
    @State private var following = false
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
                newSession.navigationTitle(String(localized: "New Session")).rootToolbar(sync: false)
            } else if let s = asSession {
                session(s)
            } else {
                PageLoader(text: "Reading the session…", bare: true)
            }
        }
        .navigationBarTitleDisplayMode(.inline)
        .task(id: threadID) { await start() }
        // Opened again at once when its machine can be reached again, or the app is back in front.
        .task(id: "\(threadID)/\(reachable)/\(scenePhase == .active)") { if !isNew { await followThread() } }
        .task(id: "\(machineID)/\(projectID)") { await loadProviders() }
        .onChange(of: model.projects) { ThreadChoices.keepProjects(model.projects, machines: model.machines) }
        .onChange(of: model.addedProject) {
            // The project Add a Project just added, from this page: the new session takes it.
            guard let added = model.addedProject, added.machineID == machineID else { return }
            projectID = added.projectID
            place = ThreadPlace(worktree: added.worktree)
            model.addedProject = nil
        }
        .sheet(isPresented: $pickingModel) {
            ModelSheet(providers: providers, selection: $selection, stale: providersStale, machineName: machine?.machineName ?? "",
                       projectAgents: projects(on: machineID).list.first { $0.projectID == projectID }?.agents ?? [],
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
        // A new session's project's branches, read from its folder (only the owner's devices can): again as the page
        // opens or the project changes, since a branch switched on the machine isn't sent here.
        .task(id: "\(machineID)/\(projectID)/\(isNew)") { await loadBranches() }
    }

    // MARK: A new session

    /// The question in the middle ("What should we build in <project>?", the environment under it), both menus set
    /// as last time; the box at the bottom, where a session's is.
    private var newSession: some View {
        // In a scroll view that always bounces, though it fits: there is nothing to scroll, so a drag puts the keyboard away
        // at once (a session's page has it follow the finger); a tap on the page puts it away too.
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
                    if needsUpdate {
                        Text("Hermesh on \(machine?.machineName ?? "") needs an update to start sessions.")
                            .font(.footnote).foregroundStyle(.orange).frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal, 22)
                            .accessibilityIdentifier("needs-update")
                    }
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
                VStack(spacing: 0) {
                    ConnectionCapsule(machine: machine, linkUp: following && reachable)
                    VStack(spacing: 6) {
                        if let req = pendingRequest(of: projection) { requestPanel(req, s) }
                        queue
                        offlineNote
                        box
                    }
                }
                .padding(.bottom, 4)
            }
        }
        .scrollDismissesKeyboard(.interactively)
        .keyboardAwayOnTap()
        .toolbar {
            ToolbarItem(placement: .principal) {
                VStack(spacing: 0) {
                    Text(s.displayTitle).font(.subheadline.weight(.semibold)).lineLimit(1)
                    Text("\(grant) · \(model.machineName(s.machineID))/\(s.projectID)").font(.caption2).foregroundStyle(.secondary).lineLimit(1)
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

    /// What waits for the next turn, in a card over the box: each can steer the turn now, or go.
    @ViewBuilder private var queue: some View {
        let queued = queuedRuns(of: projection)
        if !queued.isEmpty {
            VStack(alignment: .leading, spacing: 4) {
                Text("Queue (\(queued.count))").font(.caption).foregroundStyle(.secondary)
                ForEach(Array(queued.enumerated()), id: \.element.runID) { i, run in
                    HStack {
                        Text(verbatim: run.prompt).font(.subheadline).lineLimit(1)
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

    // MARK: The box

    /// Open: being typed in, holding words or files, or while one of its sheets shows.
    private var boxOpen: Bool {
        focused || !draft.isEmpty || !attached.isEmpty || attaching || pickingModel
    }

    /// Her's box (docs/thread-composer.md §16): one line (＋, the text, Send) until it is tapped; then two lines and,
    /// along its bottom, ＋, the model (how hard it thinks is in its sheet), Stop and Send.
    private var box: some View {
        let ready = !sending && !needsUpdate && uploading == 0 && (!draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !attached.isEmpty)
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
                TextField(isNew ? String(localized: "Describe what to build, debug or plan") : running ? String(localized: "Say more; it goes to the next turn") : String(localized: "Keep going"),
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
                        await upload(name: url.lastPathComponent, mime: mime, data: data, picture: nil)
                    } else {
                        model.show(String(localized: "Couldn’t read \(url.lastPathComponent)"))
                    }
                    uploading -= 1
                }
            }
        }
    }

    /// Closes the sheet, then opens a picker once it has gone (one presentation at a time), as Her's.
    private func add(photos: Bool) {
        attaching = false
        Task {
            try? await Task.sleep(for: .milliseconds(400))
            if photos { pickingPhotos = true } else { pickingFiles = true }
        }
    }

    private func upload(name: String, mime: String, data: Data, picture: UIImage?) async {
        guard let live = model.api?.live else { return }
        do {
            let ref = try await live.uploadThreadBlob(machineID: machineID, name: name, mimeType: mime, data: data).attachment
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

    /// ＋: photos, files and the permission mode, in a sheet from the bottom.
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

    /// The model: its provider's logo and its name; the sheet to choose another.
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

    /// Over a new session's box: the working copy and, once its folder was read, the branch (a worktree's base, or
    /// the branch the folder works on). Only for a repository.
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

    /// What a session's box sits under besides its cards: that it waits for its machine.
    @ViewBuilder private var offlineNote: some View {
        if thread?.offlineQueued == true {
            Text("Queued Offline").font(.footnote).foregroundStyle(.orange)
                .frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal, 22)
                .accessibilityIdentifier("thread-offline-queued")
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
        place = ThreadPlace(worktree: list.first { $0.projectID == id }?.workspace == .worktree)
    }

    private func setMode(_ m: Hermesh_Identity_V1_RuntimeMode) {
        mode = m
        guard !isNew else { return }
        update { $0.runtimeMode = m }
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
    }

    /// A session's projection, then its events as they come, until the page goes; read and subscribed again after
    /// the subscription ended or failed (in 1 s, then up to every 30 s), and at once when its machine can be reached
    /// again (the task's id). `following` says it is open, for the line above the box.
    private func followThread() async {
        guard let live = model.api?.live else { return }
        var failures = 0
        while !Task.isCancelled {
            do {
                let first = try await live.getThread(threadID: threadID)
                if projection == nil { apply(first.projection.thread) } // its model and modes: later, the owner's own choices stay
                projection = first.projection
                // Open a moment without failing: followed (a subscription that can't reach the machine fails at once).
                let open = Task {
                    try? await Task.sleep(for: .seconds(1))
                    if !Task.isCancelled { following = true }
                }
                defer { open.cancel() }
                for try await ev in live.subscribeThread(threadID: threadID, afterSeq: first.projection.thread.lastSeq) {
                    projection = applyThreadEvent(ev, to: projection)
                    failures = 0
                }
            } catch {
                // Never read and not to be read now: the session as the list has it, with the box and the line
                // above it saying why, rather than a wait for good.
                if projection == nil, let t = model.threads.first(where: { $0.threadID == threadID }) {
                    apply(t)
                    projection = .with { $0.thread = t }
                }
            }
            if following { failures = 0 }
            following = false
            guard !Task.isCancelled else { return }
            failures += 1
            try? await Task.sleep(for: .seconds(min(30, 1 << min(5, failures - 1))))
        }
    }

    /// The page's machine can be reached now: this iPhone reaches the relay, and the machine is online and answers.
    private var reachable: Bool {
        guard let m = machine else { return true }
        return m.local || (model.status.relayConnected && m.online && m.unreachable.isEmpty)
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

    private func loadBranches() async {
        branches = nil
        guard isNew, online, let path = projects(on: machineID).list.first(where: { $0.projectID == projectID && $0.repo })?.path,
              let res = try? await model.api?.folders(machine: machineID, path: path), res.repo else { return }
        branches = ThreadBranches(current: res.branch, default: res.defaultBranch, all: res.branches, inWorktrees: res.worktreeBranches)
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
        guard !text.isEmpty || !attached.isEmpty, let live = model.api?.live else { return }
        let files = attached
        if isNew {
            sending = true
            Task {
                defer { sending = false }
                var r = Hermesh_Thread_V2_LaunchThreadRequest()
                r.commandID = UUID().uuidString
                r.machineID = machineID
                r.projectID = projectID
                r.initialPrompt = text
                r.attachments = files.map(\.ref)
                if !selection.instanceID.isEmpty { r.modelSelection = selection }
                r.runtimeMode = clamped
                r.interactionMode = plan ? .plan : .code
                r.workspace = projectID == scratchProject ? .with { $0.mode = .inPlace } : place.workspace(branches)
                do {
                    let res = try await live.launchThread(r)
                    ThreadChoices.remember(machine: machineID, project: projectID, selection: selection, mode: clamped)
                    draft = ""
                    attached = []
                    model.open(.thread(res.thread.threadID))
                } catch { model.report(error) }
            }
            return
        }
        draft = ""
        attached = []
        var r = Hermesh_Thread_V2_DispatchMessageRequest()
        r.commandID = UUID().uuidString
        r.threadID = threadID
        r.text = text
        r.attachments = files.map(\.ref)
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
                if attached.isEmpty { attached = files }
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

/// A photo or file attached to the next message: hermeshd's reference to it, and the photo itself to show.
private struct Attached: Identifiable {
    let ref: Hermesh_Thread_V2_AttachmentRef
    let picture: UIImage?
    var id: String { ref.blobSha256 }
}

/// A repository's branches as its folder says (ListFolders): the one checked out, the default, those other worktrees have.
struct ThreadBranches: Equatable {
    var current: String
    var `default`: String
    var all: [String]
    var inWorktrees: [String]
}

/// Where a new session works, and on which branch (docs/thread-composer.md §16): its folder on a branch (by default
/// the one checked out), or a worktree from a branch (by default the default one, from origin when asked).
struct ThreadPlace: Equatable {
    var worktree = true
    var base = ""
    var on = ""
    var fromRemote = false

    init(worktree: Bool = true) { self.worktree = worktree }

    func chosen(_ b: ThreadBranches) -> String {
        worktree ? (base.isEmpty ? (b.default.isEmpty ? b.current : b.default) : base) : (on.isEmpty ? b.current : on)
    }

    /// What LaunchThread asks for: a worktree from its base, or the folder, switched first to another branch.
    func workspace(_ b: ThreadBranches?) -> Hermesh_Thread_V2_WorkspaceStrategy {
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

/// ＋ (§16): photos and files at the top; the permission mode under them, each with what it allows. It stays open
/// as a mode is chosen; a picker opens once it has gone.
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

/// The branch (§16): a worktree's base (and starting from the remote), or the branch the folder works on. The one
/// checked out first, then the default; in the folder, one another worktree has checked out can't be chosen.
private struct BranchSheet: View {
    @Environment(\.dismiss) private var dismiss
    @Binding var place: ThreadPlace
    let branches: ThreadBranches
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

/// The model: one sheet, one column (docs/thread-composer.md §16). A search over every provider; the providers
/// that can be used one under another, a model a row; those not installed, turned off or not for this project
/// left out. Choosing closes it. At the top, how hard the chosen model thinks, when it has levels: dragged along a track.
private struct ModelSheet: View {
    @Environment(\.dismiss) private var dismiss
    let providers: [Hermesh_Thread_V2_ProviderInstance]
    @Binding var selection: Hermesh_Thread_V2_ModelSelection
    let stale: Bool
    let machineName: String
    let projectAgents: [String]
    let levelTitle: (String, String) -> String
    let changed: ((from: String, to: String)) -> Void
    @State private var query = ""

    var body: some View {
        let usable = providers.filter { p in providerUnusable(p, projectAgents: projectAgents).map { $0 == .signedOut } ?? true }
        let groups: [(provider: Hermesh_Thread_V2_ProviderInstance, models: [Hermesh_Thread_V2_ModelCatalogEntry])] =
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

    private func row(_ p: Hermesh_Thread_V2_ProviderInstance, _ m: Hermesh_Thread_V2_ModelCatalogEntry) -> some View {
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

/// How hard the model thinks: its levels side by side on a track, least to most, the chosen one under a plate of liquid
/// glass. The glass is dragged: it follows the finger, swells a little, and the name under it grows; let go, it settles
/// on the nearest level with a small bounce. A tap goes straight to a level; each level passed ticks. VoiceOver swipes
/// up and down through them.
private struct ThinkingSlider: View {
    let levels: [(id: String, title: String)]
    @Binding var value: String
    /// Where the glass's left edge is while dragged, in points from the first level's.
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
                        // Tinted so it stands off the track: whiter in light, a little lighter in dark.
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
