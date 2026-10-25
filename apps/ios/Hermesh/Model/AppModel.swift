import Foundation
import HermeshKit
import HermeshProto
import HermeshSessions
import Observation
import SwiftUI
import UserNotifications

/// Where the side menu leads.
enum Root: Hashable {
    case overview, grants, machines, sessions
    case session(String)
}

/// Pages pushed onto the main stack.
enum Route: Hashable {
    case session(String)
    case grant(String) // grant id
    case machine(String) // machine id
    case usage
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
    case addProject(String) // machine id

    var id: Self { self }
}

/// A short confirmation at the bottom, with Undo when the action can be undone.
struct Toast: Identifiable, Equatable {
    let id = UUID()
    let text: String
    var undo: (@MainActor () -> Void)?

    static func == (a: Toast, b: Toast) -> Bool { a.id == b.id }
}

/// Everything the app shows, kept current while it is in front: sessions are
/// pushed (WatchSessions) and paged; machines, grants, devices and pairing
/// requests are polled every few seconds.
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

    var sessions: [Session] = []
    var loadingOlder = false
    var noOlder = false
    var unreachable: [Unreachable] = []
    var machines: [Machine] = []
    var clients: [Client] = []
    var devices: [Device] = []
    var pairings: [PairingDetails] = []
    var usage: UsageYear?
    var now = Date()

    // Navigation
    var root: Root = .overview
    var path: [Route] = []
    var drawerOpen = false
    var sheet: SheetKind?
    var filter = SessionFilter()
    var toast: Toast?
    var problem: String?
    /// Pairing requests the owner put off ("Deny"): hidden until they expire.
    var dismissedPairings: Set<String> = []
    /// A hermesh:// link the app was opened with, for onboarding.
    var onboardingLink: URL?

    @ObservationIgnored private(set) var api: LocalAPI?
    @ObservationIgnored private var cursor: SessionCursor?
    @ObservationIgnored private var watchTask: Task<Void, Never>?
    @ObservationIgnored private var pollTask: Task<Void, Never>?
    @ObservationIgnored private var toastTask: Task<Void, Never>?
    @ObservationIgnored private var active = false

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
        }
        await refreshStatus()
        phase = status.initialized ? .ready : .onboarding
        if active { begin() }
    }

    /// The app came to the front: keep everything current.
    func foreground() {
        active = true
        Core.resume()
        if phase == .ready {
            begin()
            Task { await askForNotifications() }
        }
    }

    /// The app went to the background: stop the work that keeps it current.
    func background() {
        active = false
        watchTask?.cancel()
        pollTask?.cancel()
        watchTask = nil
        pollTask = nil
        Core.pause()
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
        pollTask = Task { await poll() }
        Task { await loadUsage() }
    }

    // MARK: Keeping current

    func refreshStatus() async {
        if let s = try? await api?.status() { status = s }
    }

    private func poll() async {
        var tick = 0
        while !Task.isCancelled {
            now = Date()
            if let api {
                async let p = try? api.pairings()
                if tick % 2 == 0 {
                    async let m = try? api.machines()
                    async let c = try? api.clients()
                    async let d = try? api.devices()
                    async let s = try? api.status()
                    if let m = await m { machines = m }
                    if let c = await c { clients = c }
                    if let d = await d { devices = d }
                    if let s = await s { status = s }
                }
                if let p = await p { pairings = p.filter { !dismissedPairings.contains($0.code) } }
            }
            tick += 1
            try? await Task.sleep(for: .seconds(2))
        }
    }

    /// The first page, merged into what is loaded; again whenever the watch
    /// (re)starts or machines come and go.
    private func listFirstPage() async {
        guard let api, let page = try? await api.listSessions() else { return }
        sessions = mergeSessions(sessions, page.sessions)
        unreachable = page.unreachable
        cursor = relistedCursor(loaded: cursor, first: page.hasNext ? page.next : nil)
        if !page.hasNext { noOlder = true }
    }

    private func watch() async {
        while !Task.isCancelled {
            await listFirstPage()
            guard let api else { return }
            do {
                for try await r in api.watchSessions() {
                    if r.machinesChanged {
                        await listFirstPage()
                    }
                    sessions = mergeSessions(sessions.filter { !r.deletedSessionIds.contains($0.sessionID) }, r.sessions)
                    if !r.unreachable.isEmpty || r.machinesChanged { unreachable = r.unreachable }
                }
            } catch {}
            try? await Task.sleep(for: .seconds(2))
        }
    }

    /// The next older page, when the end of the list comes into view.
    func loadOlder() async {
        guard !loadingOlder, !noOlder, let api, let cursor else { return }
        loadingOlder = true
        defer { loadingOlder = false }
        guard let page = try? await api.listSessions(before: cursor) else { return }
        sessions = mergeSessions(sessions, page.sessions)
        if page.hasNext { self.cursor = page.next } else { noOlder = true }
    }

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

    /// The grant that started a session (by its client id), if still known.
    func grantFor(_ s: Session) -> Client? { clients.first { $0.clientID == s.createdBy } }

    /// Who started a session, as the person knows them.
    func grantLabel(_ s: Session) -> String {
        grantFor(s)?.label ?? String(localized: "Disconnected grant")
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
        withAnimation(.smooth(duration: 0.35)) { drawerOpen = false }
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

    private func replace(_ s: Session) { sessions = mergeSessions(sessions, [s]) }

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

    func approveClient(_ p: PairingDetails, spec: GrantSpec) async -> Bool {
        guard let api else { return false }
        signer.expect(.code(p.verificationCode))
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

    func approveMachine(_ p: PairingDetails, name: String) async -> Bool {
        guard let api else { return false }
        signer.expect(.code(p.verificationCode))
        do {
            _ = try await api.approveMachine(p.code, name: name)
            pairings.removeAll { $0.code == p.code }
            machines = (try? await api.machines()) ?? machines
            return true
        } catch {
            report(error)
            return false
        }
    }

    func approveDevice(_ p: PairingDetails) async -> Bool {
        guard let api else { return false }
        signer.expect(.code(p.verificationCode))
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

    func removeDevice(_ d: Device) async {
        guard let api else { return }
        do {
            try await api.removeDevice(d.keyID)
            devices = (try? await api.devices()) ?? devices
            show(String(localized: "Removed “\(d.label)”"))
        } catch { report(error) }
    }

    /// Leaves the owner: this iPhone forgets it, and the owner role has the
    /// signer delete its key.
    func signOut() async {
        guard let api else { return }
        do {
            try await api.leave()
            background()
            sessions = []
            clients = []
            machines = []
            devices = []
            pairings = []
            sheet = nil
            await refreshStatus()
            phase = .onboarding
        } catch { report(error) }
    }
}
