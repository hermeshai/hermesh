import Foundation
import HermeshKit
import HermeshProto
import LocalAuthentication
import Observation

/// A batch to sign, as the confirmation shows it: built from exactly the
/// requests that are signed if the person approves.
struct Confirmation: Identifiable {
    let id: String // the batch
    let requests: [SigningRequest]
    let cards: [Card]
    /// The first setup: one line, with the cards behind Details.
    let summary: String?
    /// Non-empty: the batch cannot be shown in full and is refused.
    let refusals: [Refusal]
    /// The Face ID prompt.
    let reason: String
    /// After signing a root cert that adds a device, or the cert of a
    /// machine that asked for them: the digits to type there.
    let match: (label: String?, digits: String)?

    init(batch: String, requests: [SigningRequest], hints: SigningPreview.Hints) {
        id = batch
        self.requests = requests
        do {
            cards = try SigningPreview.cards(requests, hints)
            refusals = []
        } catch let e as SigningPreview.RefusalList {
            cards = []
            refusals = e.refusals
        } catch {
            cards = []
            refusals = requests.map { .unsupported($0.type) }
        }
        let firstSetup = !cards.isEmpty && SigningPreview.isFirstSetup(requests)
        summary = firstSetup ? SigningPreview.firstSetupSummary : nil
        // Everything comes from this app: the owner role runs in its process.
        reason = cards.isEmpty ? "" : SigningPreview.reason(cards, caller: CallerInfo(program: nil), reissue: SigningPreview.isReissue(requests, hints), firstSetup: firstSetup)
        match = cards.isEmpty ? nil : requests.lazy.compactMap { SigningPreview.joinMatchCode($0, hints) }.first.map { ($0.label, $0.digits) }
    }
}

/// What the person already confirmed in the app, so the batch it causes
/// goes straight to Face ID instead of a second confirmation: the
/// verification code they compared (approving a pairing), or the kind of
/// change they chose.
enum Expectation: Equatable {
    case code(String)
    case types(Set<String>)
    case firstSetup

    func matches(_ c: Confirmation) -> Bool {
        guard c.refusals.isEmpty, !c.cards.isEmpty else { return false }
        switch self {
        case .code(let code): return c.cards.contains { $0.code == code }
        case .types(let t): return c.requests.allSatisfy { t.contains($0.type) }
        case .firstSetup: return c.summary != nil || c.requests.allSatisfy { $0.type == SigningPreview.pairingRequest || $0.type == SigningPreview.ownerDeviceCert }
        }
    }
}

/// The owner role asks this app to create the root key and to sign with
/// it. Each batch is signed only after the person confirmed it: in the
/// app's own sheet that led to it (Expectation), or in the signing sheet,
/// which shows what the bytes say. Every signature needs Face ID.
@Observable
final class Signer {
    /// The batch waiting for the person in the signing sheet.
    var shown: Confirmation?
    /// Two digits to type on the device or machine just approved.
    var match: (label: String?, digits: String)?
    /// The approval sheet shows a machine's digits itself, while set.
    @ObservationIgnored var inlineMatch = false
    /// Set while Face ID runs and the batch is signed.
    var working = false
    var failure: String?

    @ObservationIgnored private var api: LocalAPI?
    @ObservationIgnored private var expected: Expectation?
    @ObservationIgnored private var queued: [String: [SigningRequest]] = [:]
    @ObservationIgnored private var order: [String] = []
    @ObservationIgnored private var settling: [String: Task<Void, Never>] = [:]
    @ObservationIgnored private var answered: Set<String> = []
    @ObservationIgnored private var busy = false
    @ObservationIgnored private var watching: Task<Void, Never>?

    func start(_ api: LocalAPI) {
        self.api = api
        watching?.cancel()
        watching = Task { await watch() }
    }

    /// The next batch was confirmed in the app already.
    func expect(_ e: Expectation) { expected = e }

    private func watch() async {
        while !Task.isCancelled {
            if let api {
                do {
                    for try await r in api.stream("WatchSigningRequests", Hermesh_Local_V1_WatchSigningRequestsRequest(), as: SigningRequest.self) {
                        receive(r)
                    }
                } catch {}
            }
            try? await Task.sleep(for: .seconds(1))
        }
    }

    private func receive(_ r: SigningRequest) {
        guard !answered.contains(r.requestID) else { return }
        if r.createRootKey {
            answered.insert(r.requestID)
            Task { await createKey(r) }
            return
        }
        if r.deleteRootKey {
            answered.insert(r.requestID)
            Task { await deleteKey(r) }
            return
        }
        var reqs = queued[r.batchID] ?? []
        guard !reqs.contains(where: { $0.requestID == r.requestID }) else { return }
        if reqs.isEmpty { order.append(r.batchID) }
        reqs.append(r)
        queued[r.batchID] = reqs
        let id = r.batchID
        settling[id]?.cancel()
        // A batch's requests arrive one by one: wait until they stop.
        settling[id] = Task {
            try? await Task.sleep(for: .milliseconds(150))
            guard !Task.isCancelled else { return }
            self.settling[id] = nil
            self.next()
        }
    }

    private func next() {
        guard !busy, shown == nil, let id = order.first(where: { settling[$0] == nil }), let reqs = queued.removeValue(forKey: id) else { return }
        order.removeAll { $0 == id }
        busy = true
        Task {
            let c = Confirmation(batch: id, requests: reqs, hints: await hints())
            if !c.refusals.isEmpty {
                reject(c)
                failure = String(localized: "Denied: Hermesh can’t show this request in full. \(c.refusals.map(\.text).joined(separator: " "))")
                return finish()
            }
            if let e = expected, e.matches(c) {
                expected = nil
                await approve(c)
            } else {
                shown = c
            }
        }
    }

    /// The person approved the batch in the signing sheet.
    func approveShown() {
        guard let c = shown else { return }
        Task { await approve(c) }
    }

    /// The person denied the batch in the signing sheet.
    func rejectShown() {
        guard let c = shown else { return }
        reject(c)
        shown = nil
        finish()
    }

    private func approve(_ c: Confirmation) async {
        working = true
        defer { working = false }
        guard let ctx = await Authentication.confirm(c.reason) else {
            // Not confirmed: the sheet stays, to try again or deny.
            if shown == nil { shown = c }
            return
        }
        do {
            let messages = c.requests.map(\.message)
            let keyID = c.requests.first?.rootKeyID ?? ""
            let sigs = try await Task.detached { try RootKey.sign(messages, keyID: keyID, context: Authentication.skipped ? nil : ctx) }.value
            for (r, sig) in zip(c.requests, sigs) {
                var req = Hermesh_Local_V1_CompleteSigningRequestRequest()
                req.requestID = r.requestID
                req.signature = sig
                answered.insert(r.requestID)
                let _: Hermesh_Local_V1_CompleteSigningRequestResponse? = try await api?.call("CompleteSigningRequest", req)
            }
            shown = nil
            if let m = c.match, !inlineMatch { match = m }
        } catch {
            reject(c)
            shown = nil
            failure = String(localized: "Nothing was signed: \(error.localizedDescription)")
        }
        finish()
    }

    private func finish() {
        busy = false
        next()
    }

    private func reject(_ c: Confirmation) {
        let reqs = c.requests.filter { !answered.contains($0.requestID) }
        for r in reqs { answered.insert(r.requestID) }
        guard let api else { return }
        Task.detached {
            for r in reqs {
                var req = Hermesh_Local_V1_CompleteSigningRequestRequest()
                req.requestID = r.requestID
                req.rejected = true
                let _: Hermesh_Local_V1_CompleteSigningRequestResponse? = try? await api.call("CompleteSigningRequest", req)
            }
        }
    }

    /// The owner role needs a new root key: create it and answer with its
    /// public key. Creating it needs no Face ID; every use of it does.
    private func createKey(_ r: SigningRequest) async {
        var c = Hermesh_Local_V1_CompleteSigningRequestRequest()
        c.requestID = r.requestID
        do {
            c.publicKey = try await Task.detached { try RootKey.create() }.value
        } catch {
            c.rejected = true
            failure = error.localizedDescription
        }
        let _: Hermesh_Local_V1_CompleteSigningRequestResponse? = try? await api?.call("CompleteSigningRequest", c)
    }

    /// The owner role no longer uses a root key (the iPhone left that
    /// owner): delete it, without asking.
    private func deleteKey(_ r: SigningRequest) async {
        var c = Hermesh_Local_V1_CompleteSigningRequestRequest()
        c.requestID = r.requestID
        let id = r.rootKeyID
        do {
            try await Task.detached { try RootKey.delete(keyID: id) }.value
        } catch {
            c.rejected = true
        }
        let _: Hermesh_Local_V1_CompleteSigningRequestResponse? = try? await api?.call("CompleteSigningRequest", c)
    }

    /// Names and "before" values for the cards, from the owner's lists.
    private func hints() async -> SigningPreview.Hints {
        guard let api else { return .init() }
        async let devices = try? api.devices()
        async let machines = try? api.machines()
        async let clients = try? api.clients()
        var h = SigningPreview.Hints()
        if let d = await devices { h.devices = Dictionary(d.map { ($0.keyID, $0.label) }, uniquingKeysWith: { a, _ in a }) }
        if let m = await machines { h.machineNames = Dictionary(m.map { ($0.machineID, $0.machineName) }, uniquingKeysWith: { a, _ in a }) }
        if let c = await clients { h.clients = Dictionary(c.map { ($0.grantID, $0) }, uniquingKeysWith: { a, _ in a }) }
        return h
    }
}
