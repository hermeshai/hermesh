import Foundation
import HermeshKit
import HermeshProto
import HermeshSessions
import Network
import Observation
import SwiftUI
import UserNotifications

/// Where the side menu leads.
enum Root: Hashable {
    case overview, grants, machines, sessions
    case session(String)

    /// A page with a large title (a session's is in the bar).
    var titleLarge: Bool {
        if case .session = self { return false }
        return true
    }
}

/// Pages pushed onto the main stack.
enum Route: Hashable {
    case session(String)
    case grant(String) // grant id
    case machine(String) // machine id
    case machineAgent(String, String) // machine id, coding agent
    case grantSessions(String) // client id
    case machineSessions(String) // machine id
}

/// Sheets over everything.
enum SheetKind: Identifiable, Hashable {
    case account
    case pairing(String) // pairing code
    case sessionInfo(String)
    case renew(String) // grant id
    case scope(String) // grant id
    case connectAgent
    case addMachine
    case enterCode
    case scan
    case firstTask(String) // client id
    case addDevice
    case addProject(String, String) // machine id, coding agent
    case search
    /// An agent's doorbell: set one up (for one bot of several: named), or,
    /// with a wake, see and change it.
    case wake(String, String, Wake?, named: Bool = false) // client id, the agent's name

    var id: Self { self }
}

/// A short confirmation at the bottom, with Undo when the action can be undone.
struct Toast: Identifiable, Equatable {
    let id = UUID()
    let text: String
    var undo: (@MainActor () -> Void)?

    static func == (a: Toast, b: Toast) -> Bool { a.id == b.id }
}

/// Why what the app shows is not current, while it is not: the sync line
/// at the top of the pages says so.
enum Sync: Equatable {
    /// Connecting to the relay, or the world not read yet since the app came to the front.
    case connecting
    /// The relay has not been reached for a while: what shows is last known.
    case offline
    /// Reading a machine again (by its name), or that many.
    case machine(String)
    case machines(Int)

    var text: String {
        switch self {
        case .connecting: String(localized: "Connecting to Hermesh…")
        case .offline: String(localized: "Can’t reach Hermesh")
        case .machine(let name): String(localized: "Updating from \(name)…")
        case .machines(let n): String(localized: "Updating from \(n) machines…")
        }
    }
}

/// Everything the app shows, kept current while it is in front: the owner's
/// world as the in-process hermeshd pushes it (Watch), nothing polled;
/// older sessions are paged. What it last showed is kept on the phone, so
/// that a launch shows it at once while the Watch brings it up to date.
@Observable
final class AppModel {
    /// The one model: the app's scene and its notification handling share it.
    static let shared = AppModel()

    enum Phase: Equatable {
        case starting
        case failed(String)
        case onboarding
        case ready
    }

    var phase: Phase = .starting
    var status = StatusResponse()
    let signer = Signer()

    /// As screens show them: those of a machine that cannot be read now with
    /// their state unknown (withKnownState).
    var sessions: [Session] { withKnownState(heldSessions, unreachable: Set(unreachable.map(\.machineID))) }
    /// As hermeshd last told of them.
    private var heldSessions: [Session] = []
    var loadingOlder = false
    var noOlder = false
    var unreachable: [Unreachable] = []
    var machines: [Machine] = []
    var clients: [Client] = []
    var devices: [Device] = []
    var projects: [Project] = []
    var pairings: [PairingDetails] = []
    var usage: UsageYear?
    /// Today's work, as the machines count it.
    var activity: DayActivity?
    var now = Date()
    /// There is a world to show: kept from before, or watched.
    var hasWorld = false
    /// The Watch's first message came since the app came to the front.
    var watched = false
    /// Since when the relay has not been reached, while it is not.
    var relayDownSince: Date?
    /// The last conversations opened.
    @ObservationIgnored let conversations = Conversations()

    /// Why what shows is not current, while it is not (the sync line).
    var sync: Sync? {
        guard phase == .ready else { return nil }
        if !watched { return .connecting }
        if !status.relayConnected {
            if let d = relayDownSince, now.timeIntervalSince(d) > 10 { return .offline }
            return .connecting
        }
        let reading = machines.filter(\.syncing)
        if reading.count == 1 { return .machine(reading[0].machineName) }
        if reading.count > 1 { return .machines(reading.count) }
        return nil
    }

    /// The sync line's: as sync, but nothing for a wait under 400 ms, and
    /// once shown for at least 500 ms, so that a short wait never flashes.
    var shownSync: Sync?
    @ObservationIgnored private var syncShownAt = Date.distantPast

    /// Follows sync into shownSync (run again at each change of it).
    func showSync(_ sync: Sync?) async {
        if let sync {
            if shownSync == nil {
                try? await Task.sleep(for: .milliseconds(400))
                guard !Task.isCancelled else { return }
                syncShownAt = .now
            }
            shownSync = sync
        } else if shownSync != nil {
            let left = 0.5 - Date.now.timeIntervalSince(syncShownAt)
            if left > 0 { try? await Task.sleep(for: .seconds(left)) }
            guard !Task.isCancelled else { return }
            shownSync = nil
        }
    }

    // Navigation
    var root: Root = .overview
    var path: [Route] = []
    /// The root page scrolled far enough that its title is small.
    var rootTitleSmall = false
    var drawerOpen = false
    var sheet: SheetKind?
    /// How the session list is laid out, whether it shows archived sessions,
    /// and its collapsed sections ("grouping:key"); kept on this iPhone.
    var listView = SessionListLayout() { didSet { save(listView, "listView") } }
    var showArchived = false { didSet { save(showArchived, "showArchived") } }
    var collapsed: Set<String> = [] { didSet { save(collapsed, "collapsed") } }
    var toast: Toast?
    var problem: String?
    /// Pairing requests the owner put off ("Deny"): hidden until they expire.
    var dismissedPairings: Set<String> = []
    /// A hermesh:// link the app was opened with, for onboarding.
    var onboardingLink: URL?

    init() {
        listView = Self.saved("listView") ?? SessionListLayout()
        showArchived = Self.saved("showArchived") ?? false
        collapsed = Self.saved("collapsed") ?? []
    }

    private func save(_ v: some Encodable, _ key: String) {
        UserDefaults.standard.set(try? JSONEncoder().encode(v), forKey: "sessions." + key)
    }

    private static func saved<T: Decodable>(_ key: String) -> T? {
        UserDefaults.standard.data(forKey: "sessions." + key).flatMap { try? JSONDecoder().decode(T.self, from: $0) }
    }

    @ObservationIgnored private(set) var api: LocalAPI?
    @ObservationIgnored private var cursor: SessionCursor?
    @ObservationIgnored private var watchTask: Task<Void, Never>?
    @ObservationIgnored private var clockTask: Task<Void, Never>?
    @ObservationIgnored private var toastTask: Task<Void, Never>?
    @ObservationIgnored private var activityTask: Task<Void, Never>?
    @ObservationIgnored private var activityAsked = Date.distantPast
    /// The overview is on screen: today's work is kept current only then.
    @ObservationIgnored var overviewShown = false
    @ObservationIgnored private var saveTask: Task<Void, Never>?
    @ObservationIgnored private var active = false
    @ObservationIgnored private var network: NWPathMonitor?
    @ObservationIgnored private var networkSeen: String?

    // MARK: Lifecycle

    func start() async {
        if api == nil {
            do {
                api = try Core.start()
            } catch {
                phase = .failed(error.localizedDescription)
                return
            }
            signer.start(api!)
            let kept = restoreWorld()
            if kept != nil { phase = .ready } // shown at once; the status, which the core has at hand, confirms it
            watchNetwork()
        }
        // The core answers from memory, never waiting on the network: an
        // error means it is not answering at all, not that this iPhone
        // belongs to no one.
        var answered = false
        for attempt in 0 ..< 5 {
            if let s = try? await api?.status() {
                status = s
                answered = true
                break
            }
            try? await Task.sleep(for: .milliseconds(200 * (attempt + 1)))
        }
        guard answered else {
            if phase != .ready { phase = .failed(String(localized: "The Hermesh core isn’t answering. Quit the app and open it again.")) }
            return
        }
        if !status.initialized || (hasWorld && keptOwner != status.ownerID) { forgetWorld() }
        phase = status.initialized ? .ready : .onboarding
        if active { begin() }
    }

    /// The app came to the front: keep everything current.
    func foreground() {
        active = true
        watched = false
        relayDownSince = nil
        Core.resume { [self] in // the core listens again
            guard active, phase == .ready else { return }
            begin()
            Task { await askForNotifications() }
        }
    }

    /// The app went to the background: stop the work that keeps it current,
    /// and keep what it shows for the next launch.
    func background() {
        active = false
        watchTask?.cancel()
        clockTask?.cancel()
        activityTask?.cancel()
        watchTask = nil
        clockTask = nil
        activityTask = nil
        if phase == .ready { saveWorld(now: true) }
        Core.pause()
    }

    /// Wi-Fi to cellular and back: the connections made on the old network
    /// may be dead without anything noticing, so the core connects again.
    private func watchNetwork() {
        let m = NWPathMonitor()
        m.pathUpdateHandler = { [weak self] path in
            let seen = path.status == .satisfied ? path.availableInterfaces.map(\.name).joined(separator: ",") : ""
            Task { @MainActor in self?.networkChanged(to: seen) }
        }
        m.start(queue: .global(qos: .utility))
        network = m
    }

    private func networkChanged(to seen: String) {
        defer { networkSeen = seen }
        guard let before = networkSeen, before != seen, !seen.isEmpty, active, phase == .ready else { return }
        Core.networkChanged()
    }

    // MARK: What is kept on the phone

    /// The owner the world kept on the phone belongs to.
    @ObservationIgnored private var keptOwner = ""

    private var worldFile: URL { Core.dataDirectory.appendingPathComponent("world.pb") }

    /// The world as the app last showed it, from the phone, as a Watch's first message.
    private func restoreWorld() -> Hermesh_Local_V1_WatchResponse? {
        guard let data = try? Data(contentsOf: worldFile), var w = try? Hermesh_Local_V1_WatchResponse(serializedBytes: data) else { return nil }
        w.pairings.removeAll { $0.hasExpiresAt && $0.expiresAt.date < Date() }
        w.status.relayConnected = false
        apply(w)
        saveTask?.cancel() // nothing new to keep
        watched = false
        keptOwner = w.status.ownerID
        return w
    }

    /// Keeps what the app shows: the newest sessions, and all else it holds.
    /// now: written before this returns, as the app leaves the front (it may
    /// be suspended right after); else off the main thread.
    private func saveWorld(now: Bool = false) {
        saveTask?.cancel()
        var w = Hermesh_Local_V1_WatchResponse()
        w.reset = true
        w.status = status
        w.machines = machines
        w.clients = clients
        w.devices = devices
        w.projects = projects
        w.pairings = pairings
        w.sessions = Array(heldSessions.sorted { $0.updatedDate > $1.updatedDate }.prefix(200))
        if let c = w.sessions.count < heldSessions.count ? w.sessions.last.map(cursorAt) : cursor { w.next = c }
        keptOwner = status.ownerID
        guard let data = try? w.serializedData() else { return }
        let url = worldFile
        let options: Data.WritingOptions = [.atomic, .completeFileProtectionUntilFirstUserAuthentication]
        if now {
            try? data.write(to: url, options: options)
        } else {
            Task.detached(priority: .utility) { try? data.write(to: url, options: options) }
        }
    }

    /// Keeps it a little after the last change, while the app is in front.
    private func saveSoon() {
        saveTask?.cancel()
        saveTask = Task {
            try? await Task.sleep(for: .seconds(3))
            guard !Task.isCancelled else { return }
            saveWorld()
        }
    }

    /// Forgets the world: another owner, or none.
    private func forgetWorld() {
        saveTask?.cancel()
        heldSessions = []
        machines = []
        clients = []
        devices = []
        projects = []
        pairings = []
        cursor = nil
        hasWorld = false
        keptOwner = ""
        try? FileManager.default.removeItem(at: worldFile)
        conversations.forgetAll()
    }

    /// Set up or joined: show the app.
    func ready() async {
        await refreshStatus()
        phase = .ready
        begin()
        await askForNotifications()
    }

    /// The push token, when notifications are allowed (each launch: tokens change).
    func askForNotifications() async {
        let s = await UNUserNotificationCenter.current().notificationSettings()
        if s.authorizationStatus == .authorized || s.authorizationStatus == .provisional {
            Push.shared.requestToken()
            await Push.shared.register(api: api)
        }
    }

    /// Whether that session is the page on screen (its notifications then stay quiet).
    func isShowing(session id: String) -> Bool {
        if case .session(let s)? = path.last { return s == id }
        if path.isEmpty, case .session(let s) = root { return s == id }
        return false
    }

    /// A tap or an action on a notification: answer the question with the
    /// option or text chosen, or open what it is about.
    func handle(_ response: UNNotificationResponse) async {
        if api == nil { await start() }
        guard let api else { return }
        // An action answered from the lock screen: the app is in the
        // background, its core paused until it is done.
        let woke = !active
        if woke { await withCheckedContinuation { k in Core.resume { k.resume() } } }
        defer { if woke && !active { Core.pause() } }
        var info = response.notification.request.content.userInfo
        // The extension may not have run (a Simulator's simulated push): open the notice here.
        if info["session"] == nil, let h = info["hermesh"] as? [String: Any], let b64 = h["n"] as? String,
           let sealed = Data(base64Encoded: b64), let notice = try? NoticeKey.open(sealed) {
            let c = UNMutableNotificationContent()
            NoticeText.fill(c, with: notice)
            info = c.userInfo
        }
        let session = info["session"] as? String ?? ""
        let action = response.actionIdentifier
        if action.hasPrefix("option.") || action == "reply", let question = info["question"] as? String {
            var a = Answer()
            a.itemID = info["item"] as? String ?? ""
            if let reply = response as? UNTextInputNotificationResponse {
                a.text = reply.userText
            } else if let i = Int(action.dropFirst("option.".count)), let options = info["options"] as? [String], i < options.count {
                a.selected = [options[i]]
            }
            do {
                replace(try await api.answer(session, question: question, answers: [a], decline: false))
            } catch {
                report(error)
            }
            return
        }
        guard action == UNNotificationDefaultActionIdentifier else { return }
        if let code = info["pairing"] as? String {
            if let p = try? await api.pairings() { pairings = p }
            sheet = .pairing(code)
        } else if !session.isEmpty {
            sheet = nil
            drawerOpen = false
            if !sessions.contains(where: { $0.sessionID == session }) { await listFirstPage() }
            open(.session(session))
        }
    }

    private func begin() {
        guard api != nil, watchTask == nil else { return }
        watchTask = Task { await watch() }
        clockTask = Task { await clock() }
        Task { await loadUsage() }
    }

    // MARK: Keeping current

    func refreshStatus() async {
        if let s = try? await api?.status() { status = s }
    }

    /// The time, for what counts down (grants about to expire).
    private func clock() async {
        while !Task.isCancelled {
            now = Date()
            try? await Task.sleep(for: .seconds(2))
        }
    }

    /// The first page, merged into what is loaded (a session a notification
    /// is about may not be in it yet).
    private func listFirstPage() async {
        guard let api, let page = try? await api.listSessions() else { return }
        heldSessions = mergeSessions(heldSessions, page.sessions)
    }

    private func watch() async {
        while !Task.isCancelled {
            guard let api else { return }
            do {
                for try await r in api.watch() { apply(r) }
            } catch {}
            try? await Task.sleep(for: .milliseconds(500)) // the core is in this process: it is back at once
        }
    }

    /// A Watch message applied: the first replaces everything but the
    /// sessions, which are merged so older pages loaded stay, less those it
    /// shows were deleted meanwhile (resetSessions).
    private func apply(_ r: Hermesh_Local_V1_WatchResponse) {
        if r.reset {
            machines = []
            clients = []
            devices = []
            projects = []
            pairings = []
            cursor = relistedCursor(loaded: cursor, first: r.hasNext ? r.next : nil)
            noOlder = !r.hasNext && cursor == nil
            watched = true
            hasWorld = true
        }
        if r.hasStatus {
            status = r.status
            relayDownSince = status.relayConnected ? nil : relayDownSince ?? Date()
        }
        machines = upsert(machines, r.machines, removed: r.removedMachineIds, key: \.machineID).sorted { $0.machineName < $1.machineName }
        clients = upsert(clients, r.clients, removed: r.removedGrantIds, key: \.grantID).sorted { $0.label < $1.label }
        devices = upsert(devices, r.devices, removed: r.removedDeviceKeyIds, key: \.keyID)
        projects = upsert(projects, r.projects, removed: r.removedProjects.map { "\($0.machineID)/\($0.projectID)" }, key: { "\($0.machineID)/\($0.projectID)" })
        pairings = upsert(pairings, r.pairings, removed: r.removedPairingCodes, key: \.code).filter { !dismissedPairings.contains($0.code) }
        unreachable = machines.filter { !$0.unreachable.isEmpty }.map { m in
            var u = Unreachable()
            u.machineID = m.machineID
            u.machineName = m.machineName
            u.error = m.unreachable
            return u
        }
        if r.reset {
            let readNow = Set(machines.filter { $0.online && $0.unreachable.isEmpty && !$0.syncing }.map(\.machineID))
            heldSessions = mergeSessions(resetSessions(heldSessions, r.sessions, next: r.hasNext ? r.next : nil, readNow: readNow), [], deleted: r.deletedSessionIds)
            activityChanged()
        } else if !r.sessions.isEmpty || !r.deletedSessionIds.isEmpty {
            heldSessions = mergeSessions(heldSessions, r.sessions, deleted: r.deletedSessionIds)
            activityChanged()
        }
        r.deletedSessionIds.forEach(conversations.forget)
        if watched { saveSoon() }
    }

    /// The next older page, when the end of the list comes into view.
    func loadOlder() async {
        guard !loadingOlder, !noOlder, let api, let cursor else { return }
        loadingOlder = true
        defer { loadingOlder = false }
        if let page = try? await api.listSessions(before: cursor) {
            heldSessions = mergeSessions(heldSessions, page.sessions)
            if page.hasNext { self.cursor = page.next } else { noOlder = true }
        } else {
            try? await Task.sleep(for: .seconds(2))
        }
        olderKey += 1
    }

    /// Today's work, asked of the machines.
    func loadActivity() async {
        guard let api else { return }
        let today = dayKey(Date())
        activityAsked = Date()
        if let r = try? await api.activity(date: today) {
            activity = DayActivity(date: today, hours: r.hours, clients: r.clients)
        }
    }

    /// Sessions changed: today's work again, once a minute at most, while
    /// the overview shows it.
    private func activityChanged() {
        guard overviewShown, activityTask == nil else { return }
        activityTask = Task {
            try? await Task.sleep(for: .seconds(max(2, 60 - Date().timeIntervalSince(activityAsked))))
            guard !Task.isCancelled else { return }
            activityTask = nil
            await loadActivity()
        }
    }
    /// Changes after each page asked for: the list's last row asks again
    /// while it still shows (the page may have added nothing it shows: all
    /// archived, or in a folded group), or after a page failed.
    var olderKey = 0

    func loadUsage() async {
        guard let api else { return }
        let since = usageSince(Date(), weeks: 18)
        if let r = try? await api.usage(since: dayKey(since)) {
            usage = usageYear(r.days, today: Date(), weeks: 18)
        }
    }

    // MARK: Looking things up

    func session(_ id: String) -> Session? { sessions.first { $0.sessionID == id } }

    func grant(id: String) -> Client? { clients.first { $0.grantID == id } }

    /// Who started a session, as the person knows them.
    func grantLabel(_ s: Session) -> String { clientLabel(s.createdBy) }

    /// A client by its id (a session's created_by), as the person knows it.
    func clientLabel(_ clientID: String) -> String {
        clients.first { $0.clientID == clientID }?.label ?? String(localized: "Disconnected grant")
    }

    func machineName(_ id: String) -> String { machines.first { $0.machineID == id }?.machineName ?? String(id.prefix(12)) }

    var attentionCount: Int { sessions.filter { $0.status == .waitingInput }.count + pairings.count }

    /// Grants that end within a week.
    var expiringClients: [Client] {
        clients.filter { !$0.revoked && $0.hasExpiresAt && $0.expiresAt.date.timeIntervalSince(now) < 7 * 86400 }
    }

    /// Machines whose sessions could not be listed, with how long they have been gone.
    var unreachableMachines: [(name: String, lastSeen: Date?, online: Bool)] {
        unreachable.map { u in
            let m = machines.first { $0.machineID == u.machineID }
            return (u.machineName.isEmpty ? machineName(u.machineID) : u.machineName, m.flatMap { $0.hasLastSeen ? $0.lastSeen.date : nil }, m?.online ?? false)
        }
    }

    func searchText(_ s: Session) -> String {
        [s.displayTitle, s.projectID, grantLabel(s), machineName(s.machineID), agentTitle(s.agent)].joined(separator: " ")
    }

    // MARK: Navigation

    func open(_ r: Root) {
        root = r
        path = []
        drawerOpen = false
    }

    func openSession(_ id: String) {
        if case .session(let cur) = root, cur == id, path.isEmpty { return }
        if path.last == .session(id) { return }
        path.append(.session(id))
    }

    func show(_ text: String, undo: (@MainActor () -> Void)? = nil) {
        toast = Toast(text: text, undo: undo)
        toastTask?.cancel()
        toastTask = Task {
            try? await Task.sleep(for: .seconds(undo == nil ? 2.4 : 5))
            guard !Task.isCancelled else { return }
            withAnimation { self.toast = nil }
        }
    }

    func report(_ error: Error) {
        problem = (error as? APIError)?.message ?? error.localizedDescription
    }

    /// The owner changed which notifications they want: tell the machines
    /// (they encrypt and send them; see Push).
    func notificationPrefsChanged() {
        Task { await Push.shared.register(api: api) }
    }

    // MARK: Actions

    private func replace(_ s: Session) { heldSessions = mergeSessions(heldSessions, [s]) }

    func answer(_ s: Session, answers: [Answer], decline: Bool) async -> Bool {
        guard let api, let q = s.pendingQuestion else { return false }
        do {
            replace(try await api.answer(s.sessionID, question: q.questionID, answers: answers, decline: decline))
            show(decline ? String(localized: "Told it to decide by itself") : String(localized: "Answered; \(agentTitle(s.agent)) is working again"))
            return true
        } catch {
            report(error)
            return false
        }
    }

    func interrupt(_ s: Session) async {
        guard let api else { return }
        do {
            replace(try await api.interrupt(s.sessionID))
            show(String(localized: "Interrupted “\(s.displayTitle)”"))
        } catch { report(error) }
    }

    func setArchived(_ s: Session, _ archived: Bool, undoable: Bool = true) async {
        guard let api else { return }
        do {
            replace(try await api.archive(s.sessionID, archived))
            let text = archived ? String(localized: "Archived “\(s.displayTitle)”") : String(localized: "Unarchived “\(s.displayTitle)”")
            var undo: (@MainActor () -> Void)?
            if undoable {
                undo = { [weak self] in
                    Task { await self?.setArchived(s, !archived, undoable: false) }
                }
            }
            show(text, undo: undo)
        } catch { report(error) }
    }

    /// Deletes a session for good (its branch stays); off its page if it is open.
    func delete(_ s: Session) async -> Bool {
        guard let api else { return false }
        do {
            try await api.delete(s.sessionID)
            heldSessions = mergeSessions(heldSessions, [], deleted: [s.sessionID])
            conversations.forget(s.sessionID)
            path.removeAll { $0 == .session(s.sessionID) }
            if case .session(let id) = root, id == s.sessionID { root = .sessions }
            show(String(localized: "Deleted “\(s.displayTitle)”"))
            return true
        } catch {
            report(error)
            return false
        }
    }

    func rename(_ s: Session, to title: String) async {
        let title = title.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let api, !title.isEmpty, title != s.displayTitle else { return }
        do {
            replace(try await api.rename(s.sessionID, to: title))
        } catch { report(error) }
    }

    func approveClient(_ p: PairingDetails, spec: GrantSpec) async -> Bool {
        guard let api else { return false }
        // Confirmed requests show no code: what the person chose here is the grant.
        signer.expect(p.confirmed ? .types([SigningPreview.grant]) : .code(p.verificationCode))
        do {
            try await api.approveClient(p.code, spec: spec)
            pairings.removeAll { $0.code == p.code }
            clients = (try? await api.clients()) ?? clients
            return true
        } catch {
            report(error)
            return false
        }
    }

    /// Approves a machine; the answer carries the two digits to type on it
    /// when it asked for them (the approval sheet shows them itself).
    func approveMachine(_ p: PairingDetails, name: String) async -> Hermesh_Local_V1_ApproveMachinePairingResponse? {
        guard let api else { return nil }
        // Found by its code: nothing to compare, the code binds its key.
        signer.expect(p.confirmed ? .types([SigningPreview.machineCert]) : .code(p.verificationCode))
        signer.inlineMatch = true
        defer { signer.inlineMatch = false }
        do {
            let res = try await api.approveMachine(p.code, name: name)
            pairings.removeAll { $0.code == p.code }
            machines = (try? await api.machines()) ?? machines
            return res
        } catch {
            report(error)
            return nil
        }
    }

    /// A pairing code from a scan or a link: its request, to approve.
    func openPairingCode(_ code: String) async {
        guard let api else { return }
        do {
            let p = try await api.lookupPairing(code)
            pairings.removeAll { $0.code == p.code }
            pairings.append(p)
            sheet = .pairing(p.code)
        } catch {
            report(error)
        }
    }

    /// A link the app was opened with: a pairing code (a scanned machine,
    /// an agent's link) once set up, else onboarding's.
    func open(_ url: URL) {
        if phase == .ready, let code = PairingLink.code(url) {
            Task { await openPairingCode(code) }
        } else {
            onboardingLink = url
        }
    }

    func approveDevice(_ p: PairingDetails) async -> Bool {
        guard let api else { return false }
        signer.expect(p.confirmed ? .types([SigningPreview.rootCert]) : .code(p.verificationCode))
        do {
            _ = try await api.approveDevice(p.code)
            pairings.removeAll { $0.code == p.code }
            devices = (try? await api.devices()) ?? devices
            return true
        } catch {
            report(error)
            return false
        }
    }

    /// Renews or changes a grant: the new version is signed (Face ID).
    func update(_ c: Client, spec: GrantSpec, done: String) async -> Bool {
        guard let api else { return false }
        signer.expect(.types([SigningPreview.grant]))
        do {
            _ = try await api.updateClient(c.grantID, spec: spec)
            clients = (try? await api.clients()) ?? clients
            show(done)
            return true
        } catch {
            report(error)
            return false
        }
    }

    /// Gives an agent another name: its grant signed again with that alone changed (Face ID).
    func rename(_ c: Client, to label: String) async {
        let label = label.trimmingCharacters(in: .whitespacesAndNewlines)
        // Kept as shown ("Claude" for "Claude (claude.ai)"): nothing to sign.
        guard let api, !label.isEmpty, label != c.label, label != AgentLogo.name(c.label) else { return }
        signer.expect(.types([SigningPreview.grant]))
        do {
            try await api.renameClient(c.grantID, to: label)
            clients = (try? await api.clients()) ?? clients
        } catch { report(error) }
    }

    func disconnect(_ c: Client) async {
        guard let api else { return }
        do {
            let acks = try await api.revokeClient(c.grantID)
            clients = (try? await api.clients()) ?? clients
            let offline = acks.filter { !$0.ok }.map(\.machineName)
            show(offline.isEmpty ? String(localized: "Disconnected “\(c.label)”")
                : String(localized: "Disconnected “\(c.label)”; \(listText(offline)) will hear once online"))
            if case .grant(let id)? = path.last, id == c.grantID { path.removeLast() }
        } catch { report(error) }
    }

    func setPaused(_ m: Machine, _ paused: Bool) async {
        guard let api else { return }
        signer.expect(.types([SigningPreview.adminCommand]))
        do {
            try await api.setPaused(m.machineID, paused)
            machines = (try? await api.machines()) ?? machines
            show(paused ? String(localized: "“\(m.machineName)” takes no new sessions") : String(localized: "“\(m.machineName)” takes new sessions again"))
        } catch { report(error) }
    }

    /// What a coding agent's sessions on m run with when the client names
    /// no model.
    func setAgentDefaults(_ m: Machine, _ d: Hermesh_Identity_V1_AgentDefaults) async {
        guard let api else { return }
        do {
            try await api.setAgentDefaults(m.machineID, d)
            machines = (try? await api.machines()) ?? machines
        } catch { report(error) }
    }

    /// Adds a project (or again, to change it), as the person just asked.
    func addProject(_ r: Hermesh_Local_V1_AddProjectRequest) async throws {
        signer.expect(.types([SigningPreview.adminCommand]))
        try await api?.addProject(r)
    }

    func removeProject(machine: String, project: String) async throws {
        signer.expect(.types([SigningPreview.adminCommand]))
        try await api?.removeProject(machine: machine, project: project)
    }

    /// Adds p again, for agents and with workspace (adding again changes it).
    func saveProject(_ p: Project, agents: [String], workspace: Hermesh_Identity_V1_Workspace) async throws {
        var r = Hermesh_Local_V1_AddProjectRequest()
        r.machineID = p.machineID
        r.projectID = p.projectID
        r.path = p.path
        r.workspace = workspace
        r.defaultBaseRef = workspace == .inPlace ? "" : p.defaultBaseRef
        r.description_p = p.description_p
        r.agents = agents
        try await addProject(r)
    }

    func rename(_ m: Machine, to name: String) async {
        guard let api else { return }
        signer.expect(.types([SigningPreview.adminCommand]))
        do {
            try await api.renameMachine(m.machineID, to: name)
            machines = (try? await api.machines()) ?? machines
        } catch { report(error) }
    }

    func remove(_ m: Machine) async {
        guard let api else { return }
        do {
            try await api.removeMachine(m.machineID)
            machines = (try? await api.machines()) ?? machines
            show(String(localized: "Removed “\(m.machineName)”"))
            if case .machine(let id)? = path.last, id == m.machineID { path.removeLast() }
        } catch { report(error) }
    }

    /// Check Again: that machine looks at its coding agents now.
    func recheck(machine id: String) async {
        guard let api else { return }
        do {
            try await api.checkAgents(machine: id)
            machines = (try? await api.machines()) ?? machines
        } catch { report(error) }
    }

    func removeDevice(_ d: Device) async {
        guard let api else { return }
        do {
            try await api.removeDevice(d.keyID)
            devices = (try? await api.devices()) ?? devices
            machines = (try? await api.machines()) ?? machines
            show(String(localized: "Removed “\(d.label)”"))
            if case .machine(let id)? = path.last, id == d.machineID { path.removeLast() }
        } catch { report(error) }
    }

    /// Leaves the owner: this iPhone forgets it, and the owner role has the
    /// signer delete its key.
    func signOut() async {
        guard let api else { return }
        do {
            try await api.leave()
            background()
            forgetWorld()
            sheet = nil
            active = true // still in front: the core serves the onboarding
            await withCheckedContinuation { k in Core.resume { k.resume() } }
            await refreshStatus()
            phase = .onboarding
        } catch { report(error) }
    }
}

/// list with each changed item (by key) replaced where it was or added, and
/// those removed dropped.
private func upsert<T>(_ list: [T], _ changed: [T], removed: [String], key: (T) -> String) -> [T] {
    if changed.isEmpty && removed.isEmpty { return list }
    let gone = Set(removed)
    var out = list.filter { !gone.contains(key($0)) }
    var at: [String: Int] = [:]
    for (i, x) in out.enumerated() { at[key(x)] = i }
    for c in changed {
        if let i = at[key(c)] { out[i] = c } else {
            at[key(c)] = out.count
            out.append(c)
        }
    }
    return out
}
