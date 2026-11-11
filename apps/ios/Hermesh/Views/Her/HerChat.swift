import Foundation
import HermeshHer
import HermeshKit
import HermeshProto
import UIKit

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
    /// Earlier history is being read.
    private(set) var paging = false
    /// The connections are open.
    private(set) var envUp = false
    private(set) var convUp = false
    @ObservationIgnored private var env: HerConnection?
    @ObservationIgnored private var conv: HerConnection?
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
    private var name: String { model?.machineName(machineID) ?? machineID }

    private func report(_ e: Error) { model?.show((e as? APIError)?.message ?? e.localizedDescription) }

    private var notConnected: Error { APIError(code: "unavailable", message: String(localized: "Can’t reach \(name) right now")) }

    // MARK: Connections

    /// After a connection ended: again in 1 s, then up to every 30 s while it keeps failing.
    private func pause(_ failures: Int) async {
        try? await Task.sleep(for: .seconds(min(30, 1 << min(5, max(0, failures - 1)))))
    }

    /// The environment's connection, until the task is cancelled: her's assistants and models as she pushes them;
    /// one that ends opens again after a while.
    func followEnvironment() async {
        guard let live = api?.live else { return }
        var failures = 0
        while !Task.isCancelled {
            let c = HerConnection(local: HerTunnel(api: live), machineID: machineID)
            do {
                events: for try await e in c.open(setup: [("bots/subscribe", [:]), ("models/list", [:])]) {
                    switch e {
                    case .opened(let r):
                        guard case .success(let listed)? = r.first else { break events }
                        failures = 0
                        bots = (listed["bots"].array ?? []).compactMap(HerBot.init)
                        if case .success(let m)? = r.dropFirst().first { models = HerModels(m) }
                        env = c
                        envUp = true
                    case .notification("bots/changed", let params):
                        bots = mergeBots(bots, params)
                    case .notification("models/changed", _):
                        Task { if let m = try? await c.call("models/list") { models = HerModels(m) } }
                    default:
                        break
                    }
                }
            } catch {}
            if env === c { env = nil; envUp = false }
            failures += 1
            await pause(failures)
        }
    }

    /// The conversation's connection, until the task is cancelled: opened with bots/open in its setup, so the
    /// snapshot comes ahead of its events, which never carry words as they are written.
    func followConversation(botID: String) async {
        guard let live = api?.live else { return }
        var failures = 0
        while !Task.isCancelled {
            let c = HerConnection(local: HerTunnel(api: live), machineID: machineID)
            var opened: String?
            // Its last items, earlier ones read as it is scrolled up (earlier()).
            let open: JSON = ["bot_id": .string(botID), "events": ["exclude": ["text_delta", "thinking_delta", "tool_call_delta", "tool_progress"]],
                              "history_limit": testWindow]
            do {
                events: for try await e in c.open(setup: [("bots/open", open)]) {
                    switch e {
                    case .opened(let r):
                        switch r.first {
                        case .success(let s)?:
                            failures = 0
                            opened = s["session_id"].string
                            self.live = HerSession(snapshot: s["snapshot"])
                            noModel = false
                            conv = c
                            convUp = true
                        case .failure(let refused)?:
                            // Without a model, the page says so; deleted meanwhile, the list hears of it too. The
                            // connection stays until what it waits on changes.
                            if refused.kind == "no_model" { noModel = true } else if refused.kind != "bot_not_found" { report(refused) }
                        case nil:
                            break events
                        }
                    case .notification("session/event", let p) where p["session_id"].string == opened:
                        self.live?.apply(p["event"])
                    case .notification("session/detached", let p) where p["session_id"].string == opened:
                        break events
                    default:
                        break
                    }
                }
            } catch {}
            if conv === c { conv = nil; convUp = false }
            failures += 1
            await pause(failures)
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

    /// A tapped answer to a question is the owner's message: while Her works, it waits for the step under way.
    func say(_ text: String) async -> Bool {
        await op(prompt([.text(text)]))
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

    // MARK: Sending

    /// Sends the draft. While the conversation is open, it goes on it (its pictures in it, its files uploaded
    /// first); otherwise, or behind messages that wait, it goes through the core, which delivers it now or keeps
    /// it here until the machine is back (HerSend). A draft that did not go comes back.
    func send(direct: Bool) async {
        let sent = draft
        guard let bot, let api, !sent.isEmpty else { return }
        draft = HerDraft()
        do {
            if direct, let conv, let id = live?.sessionID {
                var uploaded: [String] = []
                for f in sent.files {
                    var r = Hermesh_Local_V1_HerUploadRequest()
                    r.machineID = machineID
                    r.botID = bot.id
                    r.localPath = f.path
                    let res: Hermesh_Local_V1_HerUploadResponse = try await api.call("HerUpload", r, timeout: 120)
                    uploaded.append(res.path)
                }
                var input: [HerUserContent] = sent.text.isEmpty ? [] : [.text(sent.text)]
                input += sent.photos.map { .image(mime: "image/jpeg", data: $0.data.base64EncodedString()) }
                if !uploaded.isEmpty { input.append(attachedBlock(uploaded)) }
                _ = try await conv.call("session/op", ["session_id": .string(id), "op": prompt(input)])
            } else {
                var r = Hermesh_Local_V1_HerSendRequest()
                r.machineID = machineID
                r.botID = bot.id
                r.text = sent.text
                r.localPaths = sent.files.map(\.path)
                r.pictures = sent.photos.map { p in .with { $0.name = p.name; $0.data = p.data } }
                let _: Hermesh_Local_V1_HerSendResponse = try await api.call("HerSend", r, timeout: 120)
                await refreshWaiting()
            }
        } catch {
            report(error)
            draft = sent.joined(draft)
        }
    }

    // MARK: Files Her sent

    /// A picture Her sent, read from her folder on the machine (files/read).
    func loadPicture(_ path: String) async {
        guard pictures[path] == nil, let conv, let id = live?.sessionID else { return }
        let read = try? await conv.call("files/read", ["session_id": .string(id), "path": .string(path)])
        pictures.updateValue(read?["image"]["data"].string.flatMap { Data(base64Encoded: $0) }.flatMap(UIImage.init(data:)), forKey: path)
    }

    /// A picture that was read and can't be shown: it shows as a file.
    func unreadable(_ path: String) -> Bool {
        if case .some(.none) = pictures[path] { return true }
        return false
    }

    /// A file on Her's machine (one she sent, or one the owner sent her), brought to this iPhone (HerDownload), to preview and save.
    func download(_ path: String) async -> URL? {
        guard let api, let bot else { return nil }
        do {
            let dir = FileManager.default.temporaryDirectory.appendingPathComponent("her/\(machineID)/\(UUID().uuidString)")
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            var r = Hermesh_Local_V1_HerDownloadRequest()
            r.machineID = machineID
            r.botID = bot.id
            r.path = path
            r.localDir = dir.path
            let res: Hermesh_Local_V1_HerDownloadResponse = try await api.call("HerDownload", r, timeout: 120)
            return URL(fileURLWithPath: res.localPath)
        } catch {
            report(error)
            return nil
        }
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

    /// The owner's answer to a card (cards/answer); a connect card that needs a sign-in has it run here.
    func answer(_ card: HerCard, _ answer: JSON) async {
        guard let conv, let id = live?.sessionID else { return report(notConnected) }
        do {
            let r = try await conv.call("cards/answer", ["session_id": .string(id), "card_id": .string(card.id), "answer": answer])
            answered[card.id] = HerCardStatus(state: r["state"].string ?? "open", note: r["note"].string)
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
        return HerSignInDeps(catcher: catcher, call: { method, params in try await HerConnection.call(tunnel, machineID: machine, method, params) },
                             open: { url in await HerBrowser.shared.open(url, closed: closed) })
    }
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
        HerDraft(text: [text, later.text].filter { !$0.isEmpty }.joined(separator: "\n"), photos: photos + later.photos, files: files + later.files)
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
