import Foundation
import Testing
@testable import HermeshKit

/// The preview vectors every signer passes (docs/desktop-app.md §5.6),
/// written by the Go signer's tests (internal/preview/vectors_test.go):
/// whether a batch is refused, each card's verification code, and the
/// values from the signed bytes each card shows, whatever its words.
private struct Vector: Decodable {
    struct Request: Decodable {
        var type: String
        var body: Data
        var message: Data?
        var confirmed: Bool?
    }
    struct Hints: Decodable {
        var machineNames: [String: String]?
        var devices: [String: String]?
    }
    struct Card: Decodable {
        var code: String?
        var shows: [String]
    }
    var name: String
    var requests: [Request]
    var hints: Hints
    var refused: Bool?
    var cards: [Card]?
}

private func vectors() throws -> [Vector] {
    let url = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        .appendingPathComponent("internal/preview/testdata/vectors.json")
    return try JSONDecoder().decode([Vector].self, from: Data(contentsOf: url))
}

@Test func thePreviewVectors() throws {
    let vs = try vectors()
    #expect(vs.count > 10)
    defer { SigningPreview.device = .mac }
    SigningPreview.device = .mac
    for v in vs {
        let reqs = v.requests.map { r in
            var s = SigningRequest()
            s.type = r.type
            s.body = r.body
            s.message = r.message ?? SigningPreview.signingInput(type: r.type, body: r.body)
            s.confirmed = r.confirmed ?? false
            return s
        }
        let hints = SigningPreview.Hints(machineNames: v.hints.machineNames ?? [:], devices: v.hints.devices)
        if v.refused == true {
            #expect(throws: SigningPreview.RefusalList.self, "\(v.name)") { try SigningPreview.cards(reqs, hints) }
            continue
        }
        let cards = try SigningPreview.cards(reqs, hints)
        let want = v.cards ?? []
        try #require(cards.count == want.count, "\(v.name)")
        for (c, w) in zip(cards, want) {
            #expect(c.code == w.code, "\(v.name): code")
            let text = ([c.title, c.who, c.why, c.warning ?? ""] + c.lines + c.rows.map(\.value) + c.ids).joined(separator: "\n")
            for s in w.shows {
                #expect(text.contains(s), "\(v.name) doesn't show “\(s)”:\n\(text)")
            }
        }
    }
}
