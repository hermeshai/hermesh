import AppKit
import Foundation
import HermeshKit
import HermeshProto
import LocalAuthentication

/// Watches hermeshd's signing requests and answers them: it creates the
/// root key when asked, and shows each batch in the panel, one at a time,
/// before signing exactly what it showed.
@MainActor
final class Signer {
    private let options: Options
    private let api: LocalAPI
    private let store: RootKeyStore
    private let panel = Panel()
    private let signerBundle = Bundle.main.bundleURL

    /// Requests not shown yet, by batch, and the batches in arrival order.
    private var queued: [String: [SigningRequest]] = [:]
    private var order: [String] = []
    /// Batches whose requests stopped arriving, and the timers that wait
    /// for that.
    private var settled: Set<String> = []
    private var settling: [String: Task<Void, Never>] = [:]
    /// Requests already answered (a new watch replays the pending ones).
    private var answered: Set<String> = []
    /// The batch in the panel, with the hints it was built with.
    private var shown: Confirmation?
    private var hints = SigningPreview.Hints()
    private var preparing = false
    private var signing = false
    private var creatingKeys = 0
    private var followUp = false
    /// Bumped when authentication restarts or stops; a superseded one is
    /// ignored.
    private var generation = 0

    init(options: Options, store: RootKeyStore) {
        self.options = options
        self.store = store
        api = LocalAPI(socketPath: options.socket)
    }

    /// The parent only receives a busy bit, never request bodies or key material.
    private func reportBusy() {
        let busy = shown != nil || !queued.isEmpty || preparing || creatingKeys > 0 || followUp
        FileHandle.standardOutput.write(Data("HERMESH_SIGNER_BUSY=\(busy ? 1 : 0)\n".utf8))
    }

    func activate() { if shown != nil || followUp { panel.show() } }

    func start() {
        reportBusy()
        let m = panel.model
        m.onReject = { [weak self] in self?.reject() }
        m.onRetry = { [weak self] in self?.authenticate() }
        m.onDismiss = { [weak self] in self?.dismiss() }
        m.onToggleDetails = { [weak self] in
            guard let self else { return }
            panel.model.details.toggle()
            panel.show() // resized to the cards
        }
        if options.testKey != nil, options.testDecision == nil {
            m.onTestApprove = { [weak self] in Task { await self?.approve(context: nil) } }
        }
        Task { await watch() }
    }

    private func watch() async {
        var failing = false
        while true {
            do {
                for try await r in api.stream("WatchSigningRequests", Hermesh_Local_V1_WatchSigningRequestsRequest(), as: SigningRequest.self) {
                    failing = false
                    receive(r)
                }
            } catch {
                if !failing { log("watching signing requests: \(error.localizedDescription); retrying every second") }
                failing = true
            }
            try? await Task.sleep(for: .seconds(1))
        }
    }

    private func receive(_ r: SigningRequest) {
        guard !answered.contains(r.requestID) else { return }
        if r.createRootKey {
            creatingKeys += 1
            reportBusy()
            answered.insert(r.requestID)
            Task { await createKey(r) }
            return
        }
        if r.deleteRootKey {
            answered.insert(r.requestID)
            Task { await deleteKey(r) }
            return
        }
        // More of the batch on screen: show it all, and authenticate again.
        if let c = shown, c.batchID == r.batchID, !signing {
            guard !c.requests.contains(where: { $0.requestID == r.requestID }) else { return }
            present(Confirmation(batchID: c.batchID, requests: c.requests + [r], hints: hints, signerBundle: signerBundle))
            return
        }
        var reqs = queued[r.batchID] ?? []
        guard !reqs.contains(where: { $0.requestID == r.requestID }) else { return }
        if reqs.isEmpty { order.append(r.batchID) }
        reqs.append(r)
        queued[r.batchID] = reqs
        reportBusy()
        let id = r.batchID
        settled.remove(id)
        settling[id]?.cancel()
        settling[id] = Task {
            try? await Task.sleep(for: .milliseconds(150))
            guard !Task.isCancelled else { return }
            self.settled.insert(id)
            self.showNext()
        }
    }

    /// Shows the oldest settled batch when the panel is free.
    private func showNext() {
        guard shown == nil, !preparing, let id = order.first(where: { settled.contains($0) }) else { return }
        preparing = true
        Task {
            let h = await self.fetchHints()
            self.preparing = false
            self.order.removeAll { $0 == id }
            self.settled.remove(id)
            self.settling.removeValue(forKey: id)?.cancel()
            guard let reqs = self.queued.removeValue(forKey: id) else { return self.showNext() }
            self.hints = h
            self.present(Confirmation(batchID: id, requests: reqs, hints: h, signerBundle: self.signerBundle))
        }
    }

    private func present(_ c: Confirmation) {
        guard c.refusals.isEmpty else {
            log("refused batch \(c.batchID): \(c.refusals.map(\.text).joined(separator: " "))")
            report(c, "reject")
            answer(c.requests)
            done(then: .notice(title: String(localized: "Denied: Hermesh can’t show this request in full", bundle: .module), lines: c.refusals.map(\.text)))
            return
        }
        shown = c
        followUp = false
        reportBusy()
        panel.model.details = false
        panel.model.state = .confirm(c)
        panel.show()
        if let d = options.testDecision {
            Task {
                switch d {
                case .approve: await self.approve(context: nil)
                case .reject: self.reject()
                }
            }
        } else if options.testKey == nil {
            authenticate()
        }
    }

    /// Starts Touch ID (or the login password) for the batch on screen,
    /// replacing any authentication already running.
    private func authenticate() {
        guard let c = shown, !signing else { return }
        generation += 1
        let g = generation
        panel.model.auth?.invalidate()
        let ctx = LAContext()
        panel.model.auth = ctx
        panel.model.biometry = ctx.canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: nil)
        panel.model.authError = nil
        panel.show()
        Task {
            try? await Task.sleep(for: .milliseconds(100)) // let the panel embed the Touch ID view first
            guard g == self.generation else { return }
            do {
                try await Self.evaluate(ctx, reason: c.reason)
                guard g == self.generation else { return }
                await self.approve(context: ctx)
            } catch {
                guard g == self.generation else { return }
                log("authentication: \(error.localizedDescription)")
                self.panel.model.authError = String(localized: "Verification wasn’t completed.", bundle: .module)
                self.panel.show()
            }
        }
    }

    /// Touch ID when this Mac has it, shown in the panel; otherwise, or when
    /// the person asks for it, the login password.
    private static func evaluate(_ ctx: LAContext, reason: String) async throws {
        if ctx.canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: nil) {
            do {
                _ = try await ctx.evaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, localizedReason: reason)
                return
            } catch let e as LAError where e.code == .userFallback || e.code == .biometryLockout {}
        }
        _ = try await ctx.evaluatePolicy(.deviceOwnerAuthentication, localizedReason: reason)
    }

    /// Signs exactly the requests on screen and completes them.
    private func approve(context: LAContext?) async {
        guard let c = shown, !signing else { return }
        signing = true
        panel.model.working = true
        let store = self.store
        let messages = c.requests.map(\.message)
        let keyID = c.requests.first?.rootKeyID ?? ""
        do {
            let sigs = try await Task.detached { try store.sign(messages, keyID: keyID, context: context) }.value
            report(c, "approve")
            for (r, sig) in zip(c.requests, sigs) {
                var req = Hermesh_Local_V1_CompleteSigningRequestRequest()
                req.requestID = r.requestID
                req.signature = sig
                answered.insert(r.requestID)
                let _: Hermesh_Local_V1_CompleteSigningRequestResponse = try await api.call("CompleteSigningRequest", req)
            }
            done(then: c.match.map { .match($0) })
        } catch {
            log("signing batch \(c.batchID): \(error.localizedDescription)")
            answer(c.requests.filter { !answered.contains($0.requestID) })
            done(then: .notice(title: String(localized: "Nothing Was Signed", bundle: .module),
                                lines: [String(localized: "Signing didn’t finish: \(error.localizedDescription)", bundle: .module)]))
        }
    }

    private func reject() {
        guard let c = shown, !signing else { return }
        report(c, "reject")
        answer(c.requests)
        done(then: nil)
    }

    /// The batch on screen is answered: show what follows it (in tests,
    /// nothing), then the next batch.
    private func done(then next: PanelModel.State?) {
        shown = nil
        signing = false
        generation += 1
        panel.model.auth?.invalidate()
        panel.model.auth = nil
        panel.model.authError = nil
        panel.model.working = false
        followUp = next != nil && options.testDecision == nil
        if let next, options.testDecision == nil {
            panel.model.state = next
            panel.show()
        } else {
            panel.hide()
        }
        showNext()
        reportBusy()
    }

    private func dismiss() {
        guard shown == nil else { return }
        followUp = false
        panel.hide()
        showNext()
        reportBusy()
    }

    /// Rejects reqs. hermeshd drops the rest of a batch once one is
    /// rejected, so later answers may fail; that is fine.
    private func answer(_ reqs: [SigningRequest]) {
        for r in reqs { answered.insert(r.requestID) }
        let api = self.api
        Task.detached {
            for r in reqs {
                var c = Hermesh_Local_V1_CompleteSigningRequestRequest()
                c.requestID = r.requestID
                c.rejected = true
                let _: Hermesh_Local_V1_CompleteSigningRequestResponse? = try? await api.call("CompleteSigningRequest", c)
            }
        }
    }

    /// hermeshd needs a new root key: create it (keeping the others) and
    /// answer with its public key, without a panel.
    private func createKey(_ r: SigningRequest) async {
        defer { creatingKeys -= 1; reportBusy() }
        let store = self.store
        var c = Hermesh_Local_V1_CompleteSigningRequestRequest()
        c.requestID = r.requestID
        do {
            let pub = try await Task.detached { try store.create() }.value
            c.publicKey = pub
            if let path = options.testReport { ReportLine(createdKey: pub, batchID: r.batchID).append(to: path) }
        } catch {
            log("creating the root key: \(error.localizedDescription)")
            c.rejected = true
        }
        do {
            let _: Hermesh_Local_V1_CompleteSigningRequestResponse = try await api.call("CompleteSigningRequest", c)
        } catch {
            log("answering the root key request: \(error.localizedDescription)")
        }
    }

    /// hermeshd no longer uses a root key (the Mac left that owner): delete
    /// it, without a panel, since that gives nothing to anyone.
    private func deleteKey(_ r: SigningRequest) async {
        let store = self.store
        var c = Hermesh_Local_V1_CompleteSigningRequestRequest()
        c.requestID = r.requestID
        do {
            try await Task.detached { try store.delete(keyID: r.rootKeyID) }.value
        } catch {
            log("deleting the root key: \(error.localizedDescription)")
            c.rejected = true
        }
        do {
            let _: Hermesh_Local_V1_CompleteSigningRequestResponse = try await api.call("CompleteSigningRequest", c)
        } catch {
            log("answering the root key deletion: \(error.localizedDescription)")
        }
    }

    /// Names and "before" values for the panel, from hermeshd's lists.
    private func fetchHints() async -> SigningPreview.Hints {
        async let devices = try? api.call("ListDevices", Hermesh_Local_V1_ListDevicesRequest(), as: Hermesh_Local_V1_ListDevicesResponse.self)
        async let machines = try? api.call("ListMachines", Hermesh_Local_V1_ListMachinesRequest(), as: Hermesh_Local_V1_ListMachinesResponse.self)
        async let clients = try? api.call("ListClients", Hermesh_Local_V1_ListClientsRequest(), as: Hermesh_Local_V1_ListClientsResponse.self)
        var h = SigningPreview.Hints()
        if let d = await devices { h.devices = Dictionary(d.devices.map { ($0.keyID, $0.label) }, uniquingKeysWith: { a, _ in a }) }
        if let m = await machines { h.machineNames = Dictionary(m.machines.map { ($0.machineID, $0.machineName) }, uniquingKeysWith: { a, _ in a }) }
        if let c = await clients { h.clients = Dictionary(c.clients.map { ($0.grantID, $0) }, uniquingKeysWith: { a, _ in a }) }
        return h
    }

    private func report(_ c: Confirmation, _ decision: String) {
        if let path = options.testReport { ReportLine(c, decision: decision).append(to: path) }
    }
}
