import Foundation
import HermeshKit

/// What the panel shows for one batch, built from exactly the requests the
/// signer will sign if the person approves.
struct Confirmation {
    struct JoinMatch: Codable, Equatable {
        let label: String?
        let digits: String
        /// A machine joined (a Mac as a machine only), not a trusted device.
        var machine = false
    }

    let batchID: String
    let requests: [SigningRequest]
    let caller: CallerInfo
    let cards: [Card]
    /// The first setup: one line, with the cards behind Details.
    let summary: String?
    /// Non-empty: the batch cannot be shown in full and is rejected.
    let refusals: [Refusal]
    /// The Touch ID prompt.
    let reason: String
    /// After signing a root cert that adds a device, or the cert of a
    /// machine that asked for them: the digits to type there.
    let match: JoinMatch?

    init(batchID: String, requests: [SigningRequest], hints: SigningPreview.Hints, signerBundle: URL) {
        self.batchID = batchID
        self.requests = requests
        caller = CallerInfo.of(requests, signerBundle: signerBundle)
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
        reason = cards.isEmpty ? "" : SigningPreview.reason(cards, caller: caller, reissue: SigningPreview.isReissue(requests, hints), firstSetup: firstSetup)
        match = cards.isEmpty ? nil : requests.lazy.compactMap { SigningPreview.joinMatchCode($0, hints) }.first.map { JoinMatch(label: $0.label, digits: $0.digits, machine: $0.machine) }
    }
}

/// --test-report: one JSON line per decision, with what the panel showed.
struct ReportLine: Encodable {
    var batchId: String
    var decision: String // "approve", "reject" or "create_root_key"
    var publicKey: Data? // create_root_key: the new key, SPKI DER (base64)
    var types: [String] = []
    var caller: String?
    var callerIsHermesh: Bool?
    var cards: [Card] = []
    var summary: String?
    var refused: [String]?
    var reason: String?
    var matchCode: String?

    init(_ c: Confirmation, decision: String) {
        batchId = c.batchID
        self.decision = decision
        types = c.requests.map(\.type)
        caller = c.caller.label
        callerIsHermesh = c.caller.isHermesh
        cards = c.cards
        summary = c.summary
        refused = c.refusals.isEmpty ? nil : c.refusals.map(\.text)
        reason = c.reason
        matchCode = decision == "approve" ? c.match?.digits : nil
    }

    init(createdKey: Data, batchID: String) {
        batchId = batchID
        decision = "create_root_key"
        publicKey = createdKey
    }

    func append(to path: String) {
        let encoder = JSONEncoder()
        encoder.keyEncodingStrategy = .convertToSnakeCase
        encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        guard var line = try? encoder.encode(self) else { return }
        line.append(0x0A)
        if !FileManager.default.fileExists(atPath: path) { FileManager.default.createFile(atPath: path, contents: nil) }
        guard let f = FileHandle(forWritingAtPath: path) else { return log("cannot write the report \(path)") }
        defer { try? f.close() }
        _ = try? f.seekToEnd()
        try? f.write(contentsOf: line)
    }
}
