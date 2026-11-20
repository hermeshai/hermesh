import Foundation
import HermeshHer
import HermeshKit
import HermeshProto
import os
import UIKit

private let herConnLog = Logger(subsystem: "ai.hermesh.app", category: "connectivity")

/// The Her entry while the app runs (docs/her-server.md §9): the
/// environment shown, kept on this iPhone, and each environment's chat,
/// kept so that going back to one shows it at once.
@Observable
final class HerPlace {
    /// The environment shown last.
    var machine: String? {
        didSet { if persistent { UserDefaults.standard.set(machine, forKey: "her-env") } }
    }
    @ObservationIgnored private let persistent: Bool
    @ObservationIgnored private var chats: [String: HerChat] = [:]

    init(persistent: Bool) {
        self.persistent = persistent
        machine = persistent ? UserDefaults.standard.string(forKey: "her-env") : nil
    }

    /// An environment's chat: the one kept, or a new one.
    func chat(_ machineID: String, model: AppModel) -> HerChat {
        if let c = chats[machineID] { return c }
        let c = HerChat(machineID: machineID, model: model)
        chats[machineID] = c
        return c
    }

    /// Returns an already-created chat without allocating a new one.
    func existingChat(_ machineID: String) -> HerChat? { chats[machineID] }

    func stop() {
        for chat in chats.values { chat.stop() }
        chats.removeAll()
    }

    /// Pauses active Her streams while the app is in the background so no
    /// zombie half-open sockets survive iOS suspension.
    func pause() {
        for chat in chats.values { chat.pause() }
    }

    /// Resumes active Her streams on returning to the foreground, clearing
    /// any retry backoff immediately.
    func resume() {
        for chat in chats.values { chat.resume() }
    }

    /// Forces an immediate reconnect of active Her streams.
    func reconnectNow() {
        for chat in chats.values { chat.reconnectNow() }
    }

    /// Wakes every environment chat's (or one machine's) retry backoff sleep after connectivity returns.
    func wakeRetries(for machineID: String? = nil) {
        if let machineID {
            chats[machineID]?.wakeRetry()
            return
        }
        for chat in chats.values {
            chat.wakeRetry()
        }
    }
}

/// One environment's Her, as the page shows it: the environment's connection
/// (her's assistants and models), the conversation's (bots/open, then its
/// events), what this iPhone keeps of both, the messages waiting here for
/// the machine, the onboarding and the sign-ins. Connections are open only
/// while the page shows the environment; the menu's count comes from Watch.
@Observable
final class HerChat {
    let machineID: String
    @ObservationIgnored private weak var model: AppModel?

    var bots: [HerBot] = []
    var models = HerModels.none
    /// The conversation as her sends it, while it has been opened.
    var live: HerSession?
    /// What this iPhone keeps of Her, until her answers and while the machine is offline.
    var kept: HerKept?
    /// her can't open the conversation: no model works in the environment.
    var noModel = false
    var waiting: [Hermesh_Local_V1_HerWaiting] = []
    var flow: HerOnboarding?
    /// Secure cards answered on this iPhone, by card id.
    var answered: [String: HerCardStatus] = [:]
    /// The connector sign-ins this iPhone runs, by card id.
    @ObservationIgnored var signingIn: [String: Task<Void, Never>] = [:]
    /// The cards whose sign-in started since this iPhone answered them: the browser opens once for each answer.
    @ObservationIgnored var signInStarted: Set<String> = []
    /// Pictures Her sent, read from the machine, by path; nil for one that can't be shown.
    var pictures: [String: UIImage?] = [:]
    /// What the owner wrote and has not sent.
    var draft = HerDraft()
    var aiConsentPrompt: HerAIConsentPrompt?
    private(set) var aiSharingChanged = 0
    @ObservationIgnored private var checkingConsent = false
    /// The composer is to take the keyboard: something put words in it for the owner to finish.
    var wantsFocus = false
    /// The routine's run whose message the conversation is to bring into view, and mark.
    var revealRun: String?
    /// The message one replied to, which the conversation is to bring into view and mark (docs/her-reply.md).
    var revealMessage: String?
    /// Moves each time the owner says something here (a message sent, an answer tapped): the conversation goes to its end.
    private(set) var spoken = 0
    /// What was sent and HerSend has not answered for, or refused (docs/her-sending.md §5.2); the drafts they were, to
    /// send again or edit.
    private(set) var sending: [HerOutgoing] = []
    @ObservationIgnored private var outgoing: [String: HerDraft] = [:]
    /// The conversation's connection is opening again after it was open: a reply may take a moment.
    private(set) var reconnecting = false
    /// When the current conversation connection attempt started (first connect or reconnect); nil once open.
    private(set) var convConnectingSince: Date?
    /// Consecutive failed conversation connection attempts since last connected.
    private(set) var convRetries = 0
    /// When the next automatic reconnect attempt is scheduled while backing off.
    private(set) var nextRetryAt: Date?
    /// Most recent environment / conversation stream error (never silently discarded).
    private(set) var envError: String?
    private(set) var convError: String?
    /// True while the app is in the background and streams are intentionally paused.
    private(set) var paused = false
    /// Earlier history is being read.
    private(set) var paging = false
    /// The sub-agents whose earlier history is being read.
    private(set) var agentPaging: Set<String> = []
    /// Moves when her says a connector changed (mcp/changed): the connectors pages read them again.
    private(set) var connectorsChanged = 0
    /// Moves when her says her settings, skills or models changed (config/changed, skills/changed, models/changed):
    /// the pages that show them read them again.
    private(set) var settingsChanged = 0
    /// The connections are open.
    private(set) var envUp = false
    private(set) var convUp = false
    @ObservationIgnored private var env: HerConnection?
    @ObservationIgnored private var conv: HerConnection?
    /// The pages holding the connections, what they follow, and the tasks that keep them.
    @ObservationIgnored private var holders = 0
    @ObservationIgnored private var following: Following?
    @ObservationIgnored private var envTask: Task<Void, Never>?
    @ObservationIgnored private var convTask: Task<Void, Never>?
    @ObservationIgnored private var letGo: Task<Void, Never>?
    @ObservationIgnored private var retryWaits: [UUID: Task<Void, Never>] = [:]
    @ObservationIgnored private var retryGen = 0
    /// Server-issued hub epoch paired with `live?.lastSeq` for incremental `bots/open`.
    @ObservationIgnored private var resumeEpoch: String?
    @ObservationIgnored private var progressMemo: (key: ProgressKey, groups: [HerProgressGroup])?
    @ObservationIgnored private var chatGPT: Task<Void, Never>?

    init(machineID: String, model: AppModel) {
        self.machineID = machineID
        self.model = model
    }

    /// Her: the environment's default assistant, as her lists it, else as this iPhone keeps it.
    var bot: HerBot? { bots.first(where: \.isDefault) ?? kept?.bot }

    /// The conversation shown: her's, else the kept one.
    var session: HerSession? { live ?? kept?.session }

    private var api: AppAPI? { model?.api }
    /// A demo can only open its in-memory transport; it never falls back to the core.
    private var transport: (any HerLocal)? {
        if let demo = api?.demo {
            #if DEBUG
            if DemoService.herDemoOn { return nil } // the existing scrolling gallery remains a kept snapshot
            #endif
            return demo.herReview
        }
        return api?.live.map { HerTunnel(api: $0) }
    }

    private var name: String { model?.machineName(machineID) ?? machineID }

    private func report(_ e: Error) { model?.show((e as? APIError)?.message ?? e.localizedDescription) }

    private var notConnected: Error { APIError(code: "unavailable", message: String(localized: "Can’t reach \(name) right now")) }

    // MARK: Connections

    func stop() {
        envTask?.cancel(); convTask?.cancel(); letGo?.cancel()
        envTask = nil; convTask = nil
        env = nil; conv = nil
        envUp = false; convUp = false
        convConnectingSince = nil; nextRetryAt = nil
        wakeRetry()
        for task in signingIn.values { task.cancel() }
    }

    /// Cancels active streams on entering the background while keeping `following`
    /// so `resume()` immediately reopens them when returning to the front.
    func pause() {
        paused = true
        envTask?.cancel(); convTask?.cancel(); letGo?.cancel()
        envTask = nil; convTask = nil
        env = nil; conv = nil
        envUp = false; convUp = false
        reconnecting = false
        convConnectingSince = nil
        nextRetryAt = nil
        wakeRetry()
    }

    /// Reopens streams after returning to the foreground, resetting retry backoff.
    func resume() {
        let wasPaused = paused
        paused = false
        convRetries = 0
        nextRetryAt = nil
        wakeRetry()
        if live == nil {
            Task { await self.loadKept() }
        }
        guard holders > 0, let f = following else { return }
        if wasPaused || (f.running && envTask == nil) || (f.running && f.bot != nil && convTask == nil) {
            startStreams(for: f)
        }
    }

    /// Forces an immediate teardown and fresh open of active Her streams.
    func reconnectNow() {
        paused = false
        envTask?.cancel(); convTask?.cancel(); letGo?.cancel()
        envTask = nil; convTask = nil
        env = nil; conv = nil
        envUp = false; convUp = false
        convRetries = 0; nextRetryAt = nil
        envError = nil; convError = nil
        wakeRetry()
        guard holders > 0, let f = following else { return }
        startStreams(for: f)
    }

    /// Wakes any backoff sleep currently waiting to retry, resetting backoff
    /// progression without disturbing live streams.
    func wakeRetry() {
        retryGen += 1
        nextRetryAt = nil
        for task in retryWaits.values { task.cancel() }
        retryWaits.removeAll()
        if live == nil {
            Task { await self.loadKept() }
        }
    }

    /// After a connection ended: again in 1 s, then up to every 30 s while it keeps failing.
    /// Returns true if woken early (caller should reset its backoff counter).
    @discardableResult
    private func pause(_ failures: Int, forConv: Bool = false) async -> Bool {
        let gen = retryGen
        let id = UUID()
        let seconds = min(30, 1 << min(5, max(0, failures - 1)))
        if forConv {
            convRetries = failures
            nextRetryAt = Date().addingTimeInterval(Double(seconds))
        }
        let sleep = Task<Void, Never> { try? await Task.sleep(for: .seconds(seconds)) }
        retryWaits[id] = sleep
        defer {
            retryWaits.removeValue(forKey: id)
            if forConv { nextRetryAt = nil }
        }
        await withTaskCancellationHandler {
            await sleep.value
        } onCancel: {
            sleep.cancel()
        }
        return retryGen != gen
    }

    /// What the connections follow: they open again when it changes.
    struct Following: Hashable {
        var bot: String?
        var session: String?
        /// The machine is reached and her runs there.
        var running: Bool
        var modelReady: Bool
    }

    /// Holds the connections open for a page that shows this environment, until the page's task is cancelled. Pages
    /// of Her's hold them one after another, so they stay as the owner goes from one to the next, and close a moment
    /// after the last one let go: her conversation is read once, not again for each page.
    func hold(_ f: Following) async {
        holders += 1
        letGo?.cancel()
        follow(f)
        while !Task.isCancelled { try? await Task.sleep(for: .seconds(3600)) }
        holders -= 1
        guard holders == 0 else { return }
        letGo = Task {
            try? await Task.sleep(for: .seconds(1))
            guard !Task.isCancelled, holders == 0 else { return }
            envTask?.cancel()
            convTask?.cancel()
            envTask = nil
            convTask = nil
            following = nil
        }
    }

    private func startStreams(for f: Following) {
        if f.running && envTask == nil {
            envTask = Task { await self.followEnvironment() }
        }
        if f.running, let bot = f.bot, convTask == nil {
            convTask = Task { await self.followConversation(botID: bot) }
        }
    }

    private func follow(_ f: Following) {
        if paused {
            following = f
            return
        }
        if envTask == nil || f.running != following?.running {
            envTask?.cancel()
            envTask = nil
            if f.running { envTask = Task { await self.followEnvironment() } }
        }
        // Another Her (her data erased, Her on again): what was said was to the one before, and this iPhone's copy
        // was of hers too. Until the new one's conversation opens (with no model yet, it doesn't), it shows nothing.
        if let was = following?.bot, let now = f.bot, was != now {
            live = nil
            resumeEpoch = nil
            if kept?.bot.id != now { kept = nil }
        }
        // A model connected opens the conversation her refused for want of one; otherwise what the machine says of
        // its models changes nothing on an open conversation (docs/her-sending.md §5.4).
        var reopen = f
        reopen.modelReady = following?.modelReady ?? f.modelReady
        if convTask == nil || reopen != following || (f.modelReady != following?.modelReady && noModel) {
            convTask?.cancel()
            convTask = nil
            if f.running, let bot = f.bot { convTask = Task { await self.followConversation(botID: bot) } }
        }
        following = f
    }

    /// The environment's connection, until the task is cancelled: her's assistants and models as she pushes them;
    /// one that ends opens again after a while.
    func followEnvironment() async {
        guard let transport else { return }
        var failures = 0
        while !Task.isCancelled && !paused {
            let started = Date()
            let c = HerConnection(local: transport, machineID: machineID)
            var didOpen = false
            do {
                events: for try await e in c.open(setup: [("bots/subscribe", [:]), ("models/list", [:])]) {
                    switch e {
                    case .opened(let r):
                        guard case .success(let listed)? = r.first else {
                            if case .failure(let err)? = r.first { envError = err.message }
                            break events
                        }
                        didOpen = true
                        failures = 0
                        envError = nil
                        bots = (listed["bots"].array ?? []).compactMap(HerBot.init)
                        if case .success(let m)? = r.dropFirst().first { models = HerModels(m) }
                        env = c
                        envUp = true
                        let openMs = Int(Date().timeIntervalSince(started) * 1000)
                        herConnLog.info("her.env.open machine=\(self.machineID, privacy: .public) open_ms=\(openMs)")
                    case .notification("bots/changed", let params):
                        bots = mergeBots(bots, params)
                    case .notification("models/changed", _):
                        settingsChanged += 1
                        Task { if let m = try? await c.call("models/list") { models = HerModels(m) } }
                    case .notification("mcp/changed", _):
                        connectorsChanged += 1
                    case .notification("config/changed", _), .notification("skills/changed", _):
                        settingsChanged += 1
                    default:
                        break
                    }
                }
            } catch {
                if !Task.isCancelled && !paused {
                    let msg = (error as? APIError)?.message ?? error.localizedDescription
                    envError = msg
                    let afterMs = Int(Date().timeIntervalSince(started) * 1000)
                    herConnLog.warning("her.env.error machine=\(self.machineID, privacy: .public) opened=\(didOpen) after_ms=\(afterMs) err=\(msg, privacy: .public)")
                }
            }
            if env === c { env = nil; envUp = false }
            guard !Task.isCancelled && !paused else { break }
            failures += 1
            if await pause(failures) { failures = 0 }
        }
    }

    /// The conversation's connection, until the task is cancelled: opened with bots/open in its setup, so the
    /// snapshot comes ahead of its events, which never carry words as they are written.
    func followConversation(botID: String) async {
        guard let transport else { return }
        var failures = 0
        while !Task.isCancelled && !paused {
            let started = Date()
            if convConnectingSince == nil { convConnectingSince = started }
            let c = HerConnection(local: transport, machineID: machineID)
            var opened: String?
            // Its last items, earlier ones read as it is scrolled up (earlier()).
            // When reconnecting to the same live conversation, pass (session_id,
            // resume_epoch, after_seq) so her sends only missed events instead
            // of a full 100-item snapshot.
            var openObj: [String: JSON] = [
                "bot_id": .string(botID),
                "events": ["exclude": ["text_delta", "thinking_delta", "tool_call_delta", "tool_progress"]],
            ]
            if testWindow != .null { openObj["history_limit"] = testWindow }
            if let existing = self.live, let epoch = self.resumeEpoch, !epoch.isEmpty {
                openObj["session_id"] = .string(existing.sessionID)
                openObj["resume_epoch"] = .string(epoch)
                openObj["after_seq"] = .number(Double(existing.lastSeq))
            }
            let open = JSON.object(openObj)
            do {
                events: for try await e in c.open(setup: [("bots/open", open)]) {
                    switch e {
                    case .opened(let r):
                        switch r.first {
                        case .success(let s)?:
                            failures = 0
                            convRetries = 0
                            convError = nil
                            convConnectingSince = nil
                            nextRetryAt = nil
                            opened = s["session_id"].string
                            if let epoch = s["resume_epoch"].string, !epoch.isEmpty {
                                self.resumeEpoch = epoch
                            }
                            let incremental = s["incremental"].bool == true && opened == self.live?.sessionID
                            if incremental {
                                let missed = s["events"].array ?? []
                                let was = self.live
                                for ev in missed { self.live?.apply(ev) }
                                if let latest = s["last_seq"].int, let cur = self.live?.lastSeq, latest > cur {
                                    self.live?.lastSeq = latest
                                }
                                feel(was, self.live)
                            } else {
                                self.live = HerSession(snapshot: s["snapshot"])
                            }
                            noModel = false
                            conv = c
                            convUp = true
                            reconnecting = false
                            settle { _ in true }
                            let openMs = Int(Date().timeIntervalSince(started) * 1000)
                            herConnLog.info("her.conv.open machine=\(self.machineID, privacy: .public) incremental=\(incremental) open_ms=\(openMs)")
                        case .failure(let refused)?:
                            // Without a model, the page says so; deleted meanwhile, the list hears of it too. The
                            // connection stays until what it waits on changes.
                            convConnectingSince = nil
                            if refused.kind == "no_model" { noModel = true } else if refused.kind != "bot_not_found" {
                                convError = refused.message
                                report(refused)
                            }
                        case nil:
                            break events
                        }
                    case .notification("session/event", let p) where p["session_id"].string == opened:
                        let e = p["event"]
                        // What a task that ended early hands back goes back into the composer, once.
                        let fresh = (e["seq"].int ?? 0) > (self.live?.lastSeq ?? .max)
                        let back = fresh ? herReturned(e) : []
                        if fresh, draft.reply == nil, let reply = herReturnedReply(e) { draft.reply = reply }
                        let was = self.live
                        self.live?.apply(e)
                        feel(was, self.live)
                        if !back.isEmpty { draft.text = (back + [draft.text]).filter { !$0.isEmpty }.joined(separator: "\n\n") }
                        settle()
                    case .notification("session/detached", let p) where p["session_id"].string == opened:
                        break events
                    default:
                        break
                    }
                }
            } catch {
                if !Task.isCancelled && !paused {
                    let msg = (error as? APIError)?.message ?? error.localizedDescription
                    convError = msg
                    let afterMs = Int(Date().timeIntervalSince(started) * 1000)
                    herConnLog.warning("her.conv.error machine=\(self.machineID, privacy: .public) opened=\(opened != nil) after_ms=\(afterMs) retries=\(failures + 1) err=\(msg, privacy: .public)")
                }
            }
            if conv === c { conv = nil; convUp = false; reconnecting = !Task.isCancelled && !paused }
            guard !Task.isCancelled && !paused else {
                convConnectingSince = nil
                break
            }
            if convConnectingSince == nil { convConnectingSince = Date() }
            failures += 1
            if await pause(failures, forConv: true) { failures = 0 }
        }
    }

    /// Reads the page of the conversation before what it shows (session/history), while there is more and her is up;
    /// one at a time, and a page that comes after the conversation was opened again is dropped.
    func earlier() async {
        guard !paging, let conv, convUp, let s = live, let cursor = s.before else { return }
        paging = true
        defer { paging = false }
        guard let page = try? await conv.call("session/history", ["session_id": .string(s.sessionID), "before": .string(cursor), "limit": testWindow]),
              live?.sessionID == s.sessionID, live?.before == cursor else { return }
        live?.prepend(history: page)
    }

    /// Reads the page of a sub-agent's own history before what its record shows (session/history with the agent's
    /// cursor, docs/her-team.md §15.4): one at a time for each agent, and a page that comes after its record moved
    /// on is dropped.
    func earlier(agent id: String) async {
        guard !agentPaging.contains(id), let conv, convUp, let s = live, let cursor = s.agents.first(where: { $0.id == id }).flatMap(herAgentHistoryCursor) else { return }
        agentPaging.insert(id)
        defer { agentPaging.remove(id) }
        guard let page = try? await conv.call("session/history", ["session_id": .string(s.sessionID), "before": .string(cursor), "limit": testWindow]),
              live?.sessionID == s.sessionID, live?.agents.first(where: { $0.id == id })?.view.before == cursor else { return }
        live?.prepend(history: page, agent: id)
    }

    /// What tells one state of the conversation from another, as far as Progress reads it.
    private struct ProgressKey: Equatable {
        var session: String
        var seq: Int
        var entries: Int
        var agentEntries: Int
    }

    /// The work Her sent out (herProgress), worked out once for each state of the conversation.
    var progress: [HerProgressGroup] {
        guard let s = session else { return [] }
        let key = ProgressKey(session: s.sessionID, seq: s.lastSeq, entries: s.entries.count, agentEntries: s.agents.reduce(0) { $0 + $1.view.entries.count })
        if let m = progressMemo, m.key == key { return m.groups }
        let groups = herProgress(s)
        progressMemo = (key, groups)
        return groups
    }

    /// How many items her sends at a time, the last ones and each earlier page: her's 100, or a test's few (HerUITests,
    /// -HerHistoryLimit), so a short conversation is longer than they.
    private var testWindow: JSON {
        let n = UserDefaults.standard.integer(forKey: "HerHistoryLimit")
        return n > 0 ? .number(Double(n)) : nil
    }

    /// A request on the environment's connection; fails while it is down.
    func call(_ method: String, _ params: JSON) async throws -> JSON {
        guard let env else { throw notConnected }
        return try await env.call(method, params)
    }

    /// An action on the conversation (session/op); whether it went.
    @discardableResult
    func op(_ o: JSON) async -> Bool {
        guard let conv, let id = live?.sessionID else {
            report(notConnected)
            return false
        }
        do {
            _ = try await conv.call("session/op", ["session_id": .string(id), "op": o])
            return true
        } catch {
            report(error)
            return false
        }
    }

    /// Brings a message of the conversation into view and marks it a moment (Show in Conversation, from the files Her
    /// sent): earlier history is read until the conversation holds it, or there is none.
    func reveal(message key: String) async {
        for _ in 0 ..< 50 {
            if let s = session, herChat(s).contains(where: { $0.key == key }) { break }
            guard live?.before != nil, convUp else { break }
            await earlier()
        }
        revealMessage = key
    }

    /// The owner saw the list of the files Her sent up to `seq` (deliveries/seen): on a connection of its own, as the
    /// page that saw it is leaving, and its hold on the environment's connection with it.
    func seeDeliveries(bot: String, upTo seq: Int) async {
        _ = try? await callAlone("deliveries/seen", ["bot_id": .string(bot), "seq": .number(Double(seq))])
    }

    /// A request to her on a connection of its own (HerCall without one): it goes at once, without waiting for the
    /// environment's connection, which a page that just came up may still be opening through the relay.
    func callAlone(_ method: String, _ params: JSON) async throws -> JSON {
        guard let transport else { throw notConnected }
        return try await HerConnection.call(transport, machineID: machineID, method, params)
    }

    /// What the next message replies to: above the composer's words, which take the keyboard.
    func reply(_ quote: HerReply) {
        draft.reply = quote
        wantsFocus = true
    }

    /// Words for the owner to finish (a scheduled task's page writes them), after what the composer holds, ready to
    /// type on.
    func put(_ text: String) {
        let had = draft.text.trimmingCharacters(in: .whitespacesAndNewlines)
        draft.text = had.isEmpty ? text : had + "\n" + text
        wantsFocus = true
    }

    /// A tapped answer to a question is the owner's message: while Her works, it waits for the step under way.
    func say(_ text: String) async -> Bool {
        guard await checkAISharing(then: { [weak self] in
            Task { _ = await self?.sendAnswer(text) }
        }) else { return false }
        return await sendAnswer(text)
    }

    private func sendAnswer(_ text: String) async -> Bool {
        Haptic.sent.play()
        spoken += 1
        return await op(prompt([.text(text)]))
    }

    private func prompt(_ input: [HerUserContent]) -> JSON {
        let content = JSON.array(input.map(\.json))
        return live?.live != nil ? ["type": "prompt", "input": content, "mode": "steer"] : ["type": "prompt", "input": content]
    }

    /// Nothing is unread while the owner looks (bots/read).
    func markRead() async {
        guard let bot, bot.unread > 0, envUp else { return }
        _ = try? await call("bots/read", ["bot_id": .string(bot.id)])
    }

    // MARK: What this iPhone keeps

    func loadKept() async {
        guard let api else { return }
        var r = Hermesh_Local_V1_GetHerConversationRequest()
        r.machineID = machineID
        if let res: Hermesh_Local_V1_GetHerConversationResponse = try? await api.call("GetHerConversation", r) { kept = herKept(res) }
    }

    func refreshWaiting() async {
        guard let api else { return }
        var r = Hermesh_Local_V1_ListHerWaitingRequest()
        r.machineID = machineID
        if let res: Hermesh_Local_V1_ListHerWaitingResponse = try? await api.call("ListHerWaiting", r) { waiting = res.waiting }
    }

    /// Drops a message waiting here: one her keeps refusing holds back those after it.
    func discard(_ id: String) async {
        guard let api else { return }
        var r = Hermesh_Local_V1_DeleteHerWaitingRequest()
        r.machineID = machineID
        r.id = id
        do {
            let _: Hermesh_Local_V1_DeleteHerWaitingResponse = try await api.call("DeleteHerWaiting", r)
        } catch { report(error) }
        await refreshWaiting()
    }

    // MARK: Third-party AI sharing

    func aiSharingStatus() async throws -> JSON {
        let reference = live?.model
        return try await callAlone("ai_consent/status", ["model": reference.map(JSON.string) ?? .null])
    }

    func reviewAISharing() {
        Task { _ = await checkAISharing(then: {}) }
    }

    /// The draft and any retry stay put while the owner decides. The host checks again at inference.
    private func checkAISharing(then send: @escaping () -> Void) async -> Bool {
        if model?.isDemo == true { return true }
        guard !checkingConsent, aiConsentPrompt == nil else { return false }
        if let machine = model?.machines.first(where: { $0.machineID == machineID }),
           herAnswers(machine.her, "ai_consent/status") == false {
            model?.show(String(localized: "Update Her’s computer to review third-party AI sharing."))
            return false
        }
        guard let reference = live?.model, convUp else {
            model?.show(String(localized: "Connect to Her’s computer to check who will receive your data."))
            return false
        }
        checkingConsent = true
        defer { checkingConsent = false }
        do {
            let status = try await callAlone("ai_consent/status", ["model": .string(reference)])
            guard live?.model == reference, convUp else { return false }
            guard let endpoint = status["recipient"]["endpoint"].string,
                  let provider = status["recipient"]["provider"].string,
                  let revision = status["revision"].string else {
                model?.show(String(localized: "Her can’t verify a single recipient for this model. Choose another model to send."))
                return false
            }
            if status["allowed"] == status["recipient"] { return true }
            let label = models.providers.first(where: { $0.id == provider })?.name(language: Locale.current.identifier) ?? provider
            let modelLabel = models.title(reference)
            aiConsentPrompt = HerAIConsentPrompt(provider: label, endpoint: endpoint, model: modelLabel, allow: { [weak self] in
                guard let self, self.live?.model == reference else { return }
                do {
                    _ = try await self.callAlone("ai_consent/allow", [
                        "model": .string(reference), "recipient": status["recipient"], "revision": .string(revision),
                    ])
                    self.aiSharingChanged += 1
                    if self.live?.model == reference, self.convUp { send() }
                } catch { self.report(error) }
            })
        } catch { report(error) }
        return false
    }

    func revokeAISharing() async throws {
        aiConsentPrompt = nil
        _ = try await callAlone("ai_consent/revoke", [:])
        aiSharingChanged += 1
    }

    // MARK: Sending

    /// Sends the draft (docs/her-sending.md): the composer is empty at once and the message shows as being sent, under
    /// an id of its own. It goes through the core, which keeps it, then delivers it, on the conversation this page has
    /// open or on one of its own, now or when the machine is back; sent again, her takes it once.
    func send() {
        let sent = draft
        guard bot != nil, api != nil, !sent.isEmpty else { return }
        Task {
            if await checkAISharing(then: { [weak self] in self?.sendIfCurrent(sent) }) {
                sendIfCurrent(sent)
            }
        }
    }

    private func sendIfCurrent(_ sent: HerDraft) {
        guard draft == sent else { return }
        Haptic.sent.play()
        spoken += 1
        draft = HerDraft()
        let id = "hw_" + UUID().uuidString.lowercased().replacingOccurrences(of: "-", with: "")
        outgoing[id] = sent
        sending.append(HerOutgoing(id: id, text: sent.text.trimmingCharacters(in: .whitespacesAndNewlines),
                                   images: sent.photos.map { HerImage(mime: "image/jpeg", data: $0.data.base64EncodedString()) },
                                   files: sent.files.map(\.name), ts: Date.now.timeIntervalSince1970 * 1000, reply: sent.reply))
        Task { await deliver(id) }
    }

    /// A message that was not sent, sent again as it was.
    func retry(_ id: String) {
        Task {
            let resend = { [weak self] in self?.retryIfPending(id) ?? () }
            if await checkAISharing(then: resend) { resend() }
        }
    }

    private func retryIfPending(_ id: String) {
        guard let i = sending.firstIndex(where: { $0.id == id }) else { return }
        sending[i].refused = nil
        Task { await deliver(id) }
    }

    /// A message that was not sent, back into the composer, before what is there.
    func edit(_ id: String) {
        guard let sent = outgoing.removeValue(forKey: id) else { return }
        sending.removeAll { $0.id == id }
        draft = sent.joined(draft)
    }

    /// HerSend for a message being sent. Kept in the outbox, the outbox shows it; taken, the conversation will; an error
    /// says it is in neither, unless the outbox has it after all (the answer was lost, not the message).
    private func deliver(_ id: String) async {
        guard let sent = outgoing[id], let bot, let api else { return }
        var r = Hermesh_Local_V1_HerSendRequest()
        r.id = id
        r.machineID = machineID
        r.botID = bot.id
        r.text = sent.text
        r.localPaths = sent.files.map(\.path)
        r.pictures = sent.photos.map { p in .with { $0.name = p.name; $0.data = p.data } }
        if let reply = sent.reply { r.reply = reply.proto }
        if convUp, let connection = conv?.connectionID, let session = live?.sessionID {
            r.connectionID = connection
            r.sessionID = session
        }
        do {
            let res: Hermesh_Local_V1_HerSendResponse = try await api.call("HerSend", r, timeout: 120)
            if !res.delivered {
                await refreshWaiting()
                forget(id)
            } else if let i = sending.firstIndex(where: { $0.id == id }) {
                sending[i].delivered = true
                // Taken: the conversation shows it in a moment, unless it never will (a command such as /context).
                Task {
                    try? await Task.sleep(for: .seconds(5))
                    if convUp { settle { $0.id == id } }
                }
            }
        } catch {
            await refreshWaiting()
            if waiting.contains(where: { $0.id == id }) {
                forget(id)
            } else if let i = sending.firstIndex(where: { $0.id == id }) {
                sending[i].refused = (error as? APIError)?.message ?? error.localizedDescription
                Haptic.error.play()
            }
        }
        settle()
    }

    /// What an event brought, felt while the owner looks at the conversation: an approval Her waits on, a card or a
    /// question of hers, her first words since the owner's (once a turn, not each), a task that failed.
    private func feel(_ was: HerSession?, _ now: HerSession?) {
        guard let was, let now, was != now, model?.root == .her, model?.path.isEmpty == true else { return }
        if let asked = herPendingApproval(now)?.approval.callID, asked != herPendingApproval(was)?.approval.callID {
            return Haptic.asks.play()
        }
        if now.failure != nil, was.failure == nil { return Haptic.error.play() }
        let said = herChat(now)
        guard let last = said.last, last.key != herChat(was).last?.key else { return }
        switch last.kind {
        case .card, .question(_, _, true): Haptic.asks.play()
        case .her, .attachment, .question, .session, .work:
            if said.count > 1, case .user = said[said.count - 2].kind { Haptic.reply.play() }
        default: break
        }
    }

    private func forget(_ id: String) {
        sending.removeAll { $0.id == id }
        outgoing[id] = nil
    }

    /// Drops what was sent once the conversation has it, and what her took that it doesn't have and won't (`lost`)
    /// (docs/her-sending.md §5.2).
    private func settle(lost: (HerOutgoing) -> Bool = { _ in false }) {
        guard let s = live, !sending.isEmpty else { return }
        let left = Set(herStillSending(sending, said: herChat(s), lost: lost).map(\.id))
        for m in sending where !left.contains(m.id) { forget(m.id) }
    }

    // MARK: Files Her sent

    /// A picture Her sent, read from the machine (files/read): the copy her kept of it as it was sent, when there is
    /// one, and the file where it is now when there is none or the copy can't be read (docs/her-deliverables.md
    /// §4.8). Kept by what it reads (HerFile.source): a picture sent again after it changed is another picture.
    func loadPicture(_ f: HerFile) async {
        guard pictures[f.source] == nil, let conv, let id = live?.sessionID else { return }
        var picture: UIImage?
        for path in f.snapshot == nil ? [f.path] : [f.source, f.path] where picture == nil {
            let read = try? await conv.call("files/read", ["session_id": .string(id), "path": .string(path)])
            picture = read?["image"]["data"].string.flatMap { Data(base64Encoded: $0) }.flatMap(UIImage.init(data:))
        }
        pictures.updateValue(picture, forKey: f.source)
    }

    /// A picture that was read and can't be shown: it shows as a file.
    func unreadable(_ f: HerFile) -> Bool {
        if case .some(.none) = pictures[f.source] { return true }
        return false
    }

    /// A file on Her's machine (one she sent, or one the owner sent her), brought to this iPhone (HerDownload), to
    /// preview and save. With `snapshot`, the copy her kept of a sent file is brought instead, as it was when sent;
    /// the file itself when the copy can't be had. With `sha256`, a file Her sent is kept here by its hash once
    /// brought, and comes from here next time, while the machine is offline too (docs/her-deliverables.md §4.6).
    func download(_ path: String, snapshot: String? = nil, sha256: String? = nil) async -> URL? {
        let kept = sha256.flatMap { keptFile($0, name: baseName(path)) }
        if let kept, FileManager.default.fileExists(atPath: kept.path) { return kept }
        if let snapshot, let url = try? await fetch(snapshot) { return keep(url, at: kept) }
        do {
            return try await fetch(path).map { keep($0, at: kept) }
        } catch {
            report(error)
            return nil
        }
    }

    /// Where a file Her sent is kept on this iPhone by its content's hash: in Caches, which the system may clear.
    func keptFile(_ sha256: String, name: String) -> URL? {
        guard sha256.count == 64, sha256.allSatisfy(\.isHexDigit), !machineID.contains("/") else { return nil }
        return FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask).first?
            .appendingPathComponent("Her Files/\(machineID)/\(sha256)/\(name)")
    }

    /// Whether this iPhone keeps that file.
    func keeps(_ sha256: String?, name: String) -> Bool {
        sha256.flatMap { keptFile($0, name: name) }.map { FileManager.default.fileExists(atPath: $0.path) } ?? false
    }

    /// `url` copied to where it is kept, which it is then read from; `url` as it is without one.
    private func keep(_ url: URL, at kept: URL?) -> URL {
        guard let kept else { return url }
        try? FileManager.default.createDirectory(at: kept.deletingLastPathComponent(), withIntermediateDirectories: true)
        return (try? FileManager.default.copyItem(at: url, to: kept)) != nil ? kept : url
    }

    private func fetch(_ path: String) async throws -> URL? {
        guard let api, let bot else { return nil }
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("her/\(machineID)/\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        var r = Hermesh_Local_V1_HerDownloadRequest()
        r.machineID = machineID
        r.botID = bot.id
        r.path = path
        r.localDir = dir.path
        let res: Hermesh_Local_V1_HerDownloadResponse = try await api.call("HerDownload", r, timeout: 120)
        return URL(fileURLWithPath: res.localPath)
    }

    // MARK: The onboarding

    /// No model works there: Her's onboarding begins, once.
    func beginOnboarding() {
        if flow == nil || flow?.step == .done { flow = HerOnboarding(at: .now) }
    }

    /// The ChatGPT sign-in: in the browser, its redirect caught on this iPhone.
    func signInChatGPT() {
        flow?.chooseChatGPT()
        chatGPT = Task {
            let end: HerSignInEnd
            do {
                end = await herSignInChatGPT(try await signInDeps { self.chatGPT?.cancel() })
            } catch {
                end = .failed(error.localizedDescription)
            }
            HerBrowser.shared.close()
            flow?.signInEnded(end)
        }
    }

    /// The ChatGPT sign-in from the Models page, outside the onboarding: in the browser, its redirect caught on this
    /// iPhone. Closing the browser, or cancelling the task, stops it.
    func signInChatGPTAlone() async -> HerSignInEnd {
        let running = SignInTask()
        let task = Task { () -> HerSignInEnd in
            do {
                return await herSignInChatGPT(try await signInDeps { running.task?.cancel() })
            } catch {
                return .failed(error.localizedDescription)
            }
        }
        running.task = task
        let end = await withTaskCancellationHandler { await task.value } onCancel: { task.cancel() }
        HerBrowser.shared.close()
        return end
    }

    /// Puts a file of this iPhone on the machine, where her can read it, as the console's upload does (a skill's
    /// archive); where it landed there.
    func upload(_ file: URL) async throws -> String {
        guard let api else { throw notConnected }
        var r = Hermesh_Local_V1_GetHerConsoleRequest()
        r.machineID = machineID
        let res: Hermesh_Local_V1_GetHerConsoleResponse = try await api.call("GetHerConsole", r)
        guard let base = URL(string: res.url), base.scheme == "http", base.host == "127.0.0.1", let port = base.port else { throw notConnected }
        let prefix = base.path.hasSuffix("/") ? base.path : base.path + "/"
        let data = try Data(contentsOf: file)
        let name = file.lastPathComponent.addingPercentEncoding(withAllowedCharacters: .alphanumerics.union(CharacterSet(charactersIn: "-._~"))) ?? "skill.zip"
        let (status, body) = try await LocalAPI.loopback(port: port, method: "POST", path: prefix + "upload", body: data, contentType: "application/octet-stream",
                                                         headers: ["Her-File-Name": name], timeout: 120)
        guard status == 200, let path = (try? JSON.parse(body))?["path"].string else { throw refusal(status, body) }
        return path
    }

    /// The environment's console, as this iPhone serves it on a loopback address under a token (GetHerConsole).
    func consoleURL() async throws -> URL {
        guard let api else { throw notConnected }
        var r = Hermesh_Local_V1_GetHerConsoleRequest()
        r.machineID = machineID
        let res: Hermesh_Local_V1_GetHerConsoleResponse = try await api.call("GetHerConsole", r)
        guard let base = URL(string: res.url), base.scheme == "http", base.host == "127.0.0.1", base.port != nil else { throw notConnected }
        return base
    }

    /// Another way: the sign-in under way stops.
    func stopChatGPT() {
        flow?.otherWay()
        chatGPT?.cancel()
    }

    /// The key goes to her (auth/set) and is let go: nothing here keeps it.
    func saveKey(_ key: String) async {
        guard let provider = flow?.provider else { return }
        flow?.checking()
        do {
            let r = try await call("auth/set", ["provider": .string(provider), "api_key": .string(key)])
            flow?.keySaved(verified: r["verified"].bool ?? false)
        } catch {
            flow?.keyFailed(error)
        }
    }

    // MARK: Secure cards

    /// The owner's answer to a card (cards/answer); strictly live, never queued offline.
    func answer(_ card: HerCard, _ answer: JSON) async {
        guard convUp, let conv, let id = live?.sessionID else { return report(notConnected) }
        do {
            let r = try await conv.call("cards/answer", ["session_id": .string(id), "card_id": .string(card.id), "answer": answer])
            answered[card.id] = HerCardStatus(state: r["state"].string ?? "open", note: r["note"].string)
            Haptic.success.play()
        } catch { report(error) }
    }

    /// The connector's sign-in for a card her waits on, in the browser; closing the browser stops it.
    func signIn(_ card: HerCard) {
        guard let name = card.connect?.name else { return }
        signingIn[card.id]?.cancel()
        signingIn[card.id] = Task {
            do {
                // How it went, her says on the card.
                if case .failed(let message) = await herSignInConnector(try await signInDeps { self.signingIn[card.id]?.cancel() }, name: name), !message.isEmpty {
                    model?.show(message)
                }
            } catch { report(error) }
            HerBrowser.shared.close()
            signingIn[card.id] = nil
        }
    }

    func stopSignIn(_ card: HerCard) {
        signingIn[card.id]?.cancel()
        signingIn[card.id] = nil
    }

    /// Signs in to the connector `name` on the connectors page, in the browser: one added already, or with `server`,
    /// one about to be added. Closing the browser, or cancelling the task, stops it.
    func signIn(connector name: String, server: JSON? = nil) async -> HerSignInEnd {
        let running = SignInTask()
        let task = Task { () -> HerSignInEnd in
            do {
                return await herSignInConnector(try await signInDeps { running.task?.cancel() }, name: name, server: server)
            } catch {
                return .failed(error.localizedDescription)
            }
        }
        running.task = task
        let end = await withTaskCancellationHandler { await task.value } onCancel: { task.cancel() }
        HerBrowser.shared.close()
        return end
    }

    /// A sign-in's ways: this iPhone's catcher (the console's routes on its core), her on a connection of its own,
    /// and the browser, whose closing calls `closed`.
    private func signInDeps(closed: @escaping @MainActor () -> Void) async throws -> HerSignInDeps {
        guard let api, let live = api.live else { throw notConnected }
        var r = Hermesh_Local_V1_GetHerConsoleRequest()
        r.machineID = machineID
        let res: Hermesh_Local_V1_GetHerConsoleResponse = try await api.call("GetHerConsole", r)
        guard let base = URL(string: res.url), base.scheme == "http", base.host == "127.0.0.1", let port = base.port else { throw notConnected }
        let prefix = base.path.hasSuffix("/") ? base.path : base.path + "/"
        let catcher = HerCatcher(start: { path in
            let (status, body) = try await LocalAPI.loopback(port: port, method: "POST", path: prefix + "signin", body: JSON.object(["path": .string(path)]).data())
            let j = try? JSON.parse(body)
            guard status == 200, let id = j?["id"].string, let redirect = j?["redirect_uri"].string else { throw refusal(status, body) }
            return (id, redirect)
        }, poll: { id in
            let (status, body) = try await LocalAPI.loopback(port: port, method: "GET", path: prefix + "signin/" + id)
            guard status == 200 else { throw refusal(status, body) }
            return try JSON.parse(body)
        })
        let tunnel = HerTunnel(api: live), machine = machineID
        // Once the catcher has the code the browser goes at once: the owner is not left on a page telling them to
        // close it, and its closing no longer stops the sign-in.
        return HerSignInDeps(catcher: catcher, call: { method, params in try await HerConnection.call(tunnel, machineID: machine, method, params) },
                             open: { url in await HerBrowser.shared.open(url, closed: closed) },
                             caught: { await HerBrowser.shared.close() })
    }
}

struct HerAIConsentPrompt {
    let provider: String
    let endpoint: String
    let model: String
    let allow: () async -> Void
}

/// A connector's sign-in under way, for the browser's closing to stop.
private final class SignInTask {
    var task: Task<HerSignInEnd, Never>?
}

/// Why the catcher said no: its JSON's `error`, or its text.
nonisolated private func refusal(_ status: Int, _ body: Data) -> HerSignInRefusal {
    let text = String(decoding: body, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
    return HerSignInRefusal((try? JSON.parse(body))?["error"].string ?? (text.isEmpty ? "HTTP \(status)" : text))
}

/// What the owner wrote to Her and has not sent: words, photos and files.
struct HerDraft: Equatable {
    var text = ""
    var photos: [Photo] = []
    var files: [File] = []
    /// What the message replies to (docs/her-reply.md): no message by itself.
    var reply: HerReply?

    struct Photo: Equatable, Identifiable {
        let id = UUID()
        var name: String
        /// JPEG.
        var data: Data
    }

    struct File: Equatable, Identifiable {
        let id = UUID()
        /// A copy in this app's caches, which the core reads.
        var path: String
        var name: String
    }

    var isEmpty: Bool { text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && photos.isEmpty && files.isEmpty }

    /// This draft, then what was written since it was sent.
    func joined(_ later: HerDraft) -> HerDraft {
        HerDraft(text: [text, later.text].filter { !$0.isEmpty }.joined(separator: "\n"), photos: photos + later.photos, files: files + later.files,
                 reply: reply ?? later.reply)
    }
}

/// Her's connections through this iPhone's core (HerOpen, HerCall).
nonisolated struct HerTunnel: HerLocal {
    let api: LocalAPI

    func herOpen(_ req: Hermesh_Local_V1_HerOpenRequest) -> AsyncThrowingStream<Hermesh_Her_V1_OpenEvent, Error> {
        api.stream("HerOpen", req)
    }

    // her's own requests may take a while: checking a key with its provider, connecting a connector.
    func herCall(_ req: Hermesh_Local_V1_HerCallRequest) async throws -> Hermesh_Her_V1_CallResponse {
        try await api.call("HerCall", req, timeout: 90)
    }
}
