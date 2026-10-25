import AppKit
import CryptoKit
import Foundation
import HermeshProto
import LocalAuthentication
import LocalAuthenticationEmbeddedUI
import Testing
@testable import HermeshSigner
@testable import HermeshKit

let hermeshSigner = URL(fileURLWithPath: "/Applications/Hermesh.app/Contents/Helpers/Hermesh Signer.app")

func caller(_ executable: String) -> Hermesh_Local_V1_Caller {
    var c = Hermesh_Local_V1_Caller()
    c.pid = 42
    c.executable = executable
    return c
}

func fromHermesh(_ r: SigningRequest) -> SigningRequest {
    var r = r
    r.caller = caller("/Applications/Hermesh.app/Contents/MacOS/Hermesh")
    return r
}

// MARK: Caller

@Test func theAppTheSignerIsBundledInIsHermesh() {
    let c = CallerInfo.of(caller("/Applications/Hermesh.app/Contents/MacOS/Hermesh"), signerBundle: hermeshSigner)
    #expect(c.isHermesh)
    #expect(c.label == "Requested by the Hermesh app")
}

@Test(arguments: [
    ("/usr/local/bin/hermesh", "Requested by hermesh"),
    ("/Applications/Hermesh.app/Contents/Helpers/hermeshd", "Requested by hermeshd"),
    ("/Applications/Other.app/Contents/MacOS/Hermesh", "Requested by Hermesh"),
    ("/private/tmp/go-build/e2e.test", "Requested by e2e.test"),
])
func anyOtherProgramIsNot(executable: String, label: String) {
    let c = CallerInfo.of(caller(executable), signerBundle: hermeshSigner)
    #expect(!c.isHermesh)
    #expect(c.label == label)
    #expect(c.path == executable)
}

@Test func anUnknownCallerIsNotHermesh() {
    #expect(CallerInfo.of(caller(""), signerBundle: hermeshSigner) == CallerInfo(program: ""))
    #expect(CallerInfo.of(nil, signerBundle: hermeshSigner).label == "Requested by an unknown program")
}

// A signer that is not inside an app (a development build) never calls
// anything Hermesh.
@Test func aSignerOutsideAnAppTrustsNoCaller() {
    let dev = URL(fileURLWithPath: "/src/hermesh/apps/signer/macos/.build/debug")
    #expect(!CallerInfo.of(caller("/src/hermesh/apps/signer/macos/.build/MacOS/Hermesh"), signerBundle: dev).isHermesh)
}

@Test func aBatchFromAnotherProgramSaysSo() {
    var other = request(grant())
    other.caller = caller("/usr/local/bin/hermesh")
    let c = CallerInfo.of([fromHermesh(request(grant())), other], signerBundle: hermeshSigner)
    #expect(c.label == "Requested by hermesh")
}

// MARK: Touch ID reason

@Test func reasonNamesTheFirstRequestItsCodeAndTheCaller() throws {
    let card = try SigningPreview.card(request(grant()))
    #expect(SigningPreview.reason([card], caller: CallerInfo(program: nil), reissue: false) == "Approve “bot” to connect to your computer (verification code ZZ6X-PJ7Y-V1RA)")
    #expect(SigningPreview.reason([card, card], caller: CallerInfo(program: nil), reissue: false).hasSuffix("(1 of 2)"))
    #expect(SigningPreview.reason([card], caller: CallerInfo(program: "e2e.test"), reissue: false).hasPrefix("e2e.test requests: Approve"))
    #expect(SigningPreview.reason([card], caller: CallerInfo(program: ""), reissue: false).hasPrefix("Unknown program requests: "))
}

// MARK: Options

@Test func optionsDefaultToTheRealKey() throws {
    let o = try Options.parse([])
    #expect(o.socket == Paths.socket)
    #expect(o.testKey == nil && o.testDecision == nil && o.testReport == nil && o.parentPID == nil)
}

@Test func testOptions() throws {
    let o = try Options.parse(["--socket", "/tmp/s", "--parent-pid", "7", "--test-key", "/tmp/k", "--test-decision", "reject", "--test-report", "/tmp/r"])
    #expect(o == Options(socket: "/tmp/s", parentPID: 7, testKey: "/tmp/k", testDecision: .reject, testReport: "/tmp/r"))
}

// The app passes the language as Foundation's -AppleLanguages; Foundation reads it, the parser lets it be.
@Test func appleLanguagesIsLeftToFoundation() throws {
    let o = try Options.parse(["--socket", "/tmp/s", "-AppleLanguages", "(zh-Hans)", "--parent-pid", "7"])
    #expect(o == Options(socket: "/tmp/s", parentPID: 7))
}

@Test(arguments: [
    ["--test-decision", "approve"], // needs --test-key
    ["--test-report", "/tmp/r"],
    ["--test-key", "/tmp/k", "--test-decision", "maybe"],
    ["--parent-pid", "x"],
    ["--socket"],
    ["--verbose"],
    ["-AppleLanguages"],
])
func badOptions(args: [String]) {
    #expect(throws: SignerError.self) { try Options.parse(args) }
}

// MARK: Test key and report

@Test func testKeyFileSignsWithTheKeysItCreated() throws {
    let path = FileManager.default.temporaryDirectory.appendingPathComponent("hermesh-signer-\(UUID().uuidString).pem").path
    defer { for f in [path, path + ".handed"] { try? FileManager.default.removeItem(atPath: f) } }
    let store = try TestKeyFile(path: path)
    let first = try String(contentsOfFile: path, encoding: .utf8)
    #expect(first.hasPrefix("-----BEGIN PRIVATE KEY-----")) // PKCS#8, as identity.LoadKey reads it
    _ = try TestKeyFile(path: path)
    #expect(try String(contentsOfFile: path, encoding: .utf8) == first) // an existing key is kept

    // The first key created is the one in the file; the next is another.
    let firstPub = try store.create()
    #expect(firstPub == (try P256.Signing.PrivateKey(pemRepresentation: first).publicKey.derRepresentation))
    let secondPub = try store.create()
    #expect(secondPub != firstPub)
    let message = SigningPreview.signingInput(type: SigningPreview.grant, body: Data([1, 2]))
    for der in [firstPub, secondPub] {
        let pub = try P256.Signing.PublicKey(derRepresentation: der)
        let sig = try store.sign([message], keyID: keyID(spki: der), context: nil)[0]
        #expect(pub.isValidSignature(try P256.Signing.ECDSASignature(derRepresentation: sig), for: message))
    }
    try store.delete(keyID: keyID(spki: secondPub))
    #expect(throws: (any Error).self) { try store.sign([message], keyID: keyID(spki: secondPub), context: nil) }
    #expect(try store.sign([message], keyID: keyID(spki: firstPub), context: nil).count == 1)
}

@Test(.enabled(if: SecureEnclave.isAvailable)) func legacyKeyHandleMovesBesideTheOthers() throws {
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent("hermesh-keys-\(UUID().uuidString)")
    defer { try? FileManager.default.removeItem(at: dir) }
    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    let key = try SecureEnclave.P256.Signing.PrivateKey()
    try key.dataRepresentation.write(to: dir.appendingPathComponent("root-key.se"))
    let store = SecureEnclaveKey(blobDir: dir.appendingPathComponent("root-keys"))
    store.adoptLegacyBlob()
    #expect(!FileManager.default.fileExists(atPath: dir.appendingPathComponent("root-key.se").path))
    let moved = dir.appendingPathComponent("root-keys/\(keyID(spki: key.publicKey.derRepresentation)).se")
    #expect(try Data(contentsOf: moved) == key.dataRepresentation)
    store.adoptLegacyBlob() // nothing left to move
}

@Test func reportLineSaysWhatThePanelShowed() throws {
    var rc = request(rootCert(version: 2, [rootKey("key a", "Mac A"), rootKey("key b", "Mac B")]))
    rc.caller = caller("/private/tmp/go-build/e2e.test")
    let c = Confirmation(batchID: "b9", requests: [rc], hints: .init(devices: devices(["key a": "Mac A"])), signerBundle: hermeshSigner)
    let path = FileManager.default.temporaryDirectory.appendingPathComponent("hermesh-report-\(UUID().uuidString).jsonl").path
    defer { try? FileManager.default.removeItem(atPath: path) }
    ReportLine(c, decision: "approve").append(to: path)
    ReportLine(createdKey: Data([1, 2, 3]), batchID: "b0").append(to: path)
    let lines = try String(contentsOfFile: path, encoding: .utf8).split(separator: "\n")
    #expect(lines.count == 2)
    let first = try #require(JSONSerialization.jsonObject(with: Data(lines[0].utf8)) as? [String: Any])
    #expect(first["batch_id"] as? String == "b9")
    #expect(first["decision"] as? String == "approve")
    #expect(first["caller"] as? String == "Requested by e2e.test")
    #expect(first["caller_is_hermesh"] as? Bool == false)
    #expect(first["match_code"] as? String == SigningPreview.joinMatch(rc.body))
    let card = try #require((first["cards"] as? [[String: Any]])?.first)
    #expect(card["title"] as? String == "Add “Mac B” as a trusted device")
    #expect(card["code"] as? String == SigningPreview.verificationCode(.device, Data("key b".utf8), owner))
    let second = try #require(JSONSerialization.jsonObject(with: Data(lines[1].utf8)) as? [String: Any])
    #expect(second["decision"] as? String == "create_root_key")
    #expect(second["public_key"] as? String == "AQID")
}

// MARK: Panel

@MainActor @Test func panelSizesToItsContent() {
    let model = PanelModel()
    model.state = .confirm(Confirmation(batchID: "b1", requests: [request(grant())], hints: .init(), signerBundle: hermeshSigner))
    let one = Panel.measure(model)
    #expect(one.width == 440 && one.height > 300)
    model.state = .confirm(Confirmation(batchID: "b1", requests: Array(repeating: request(grant()), count: 6), hints: .init(), signerBundle: hermeshSigner))
    #expect(Panel.measure(model).height < 700) // many cards scroll
    var device = Hermesh_Identity_V1_OwnerDeviceCert()
    device.ownerID = owner
    device.label = "Mac A"
    model.state = .confirm(Confirmation(batchID: "b2", requests: [request(rootCert(version: 1, [rootKey("key a", "Mac A")])), request(device)], hints: .init(), signerBundle: hermeshSigner))
    let line = Panel.measure(model)
    model.details = true
    #expect(Panel.measure(model).height > line.height) // Details shows the cards
    model.details = false
    model.state = .match(.init(label: "Mac B", digits: "42"))
    #expect(Panel.measure(model).height > 150)
}

// The panel is sized from a measurement without a title bar, so its content
// must start at its own padding, not under the hidden title bar: there it
// was pushed down and clipped the first setup's summary.
@MainActor @Test func panelContentIsNotInsetByTheTitleBar() async throws {
    _ = NSApplication.shared
    var device = Hermesh_Identity_V1_OwnerDeviceCert()
    device.ownerID = owner
    let panel = Panel()
    panel.model.state = .confirm(Confirmation(batchID: "b1", requests: [request(rootCert(version: 1, [rootKey("key a", "Mac A")])), request(device)], hints: .init(), signerBundle: hermeshSigner))
    panel.show()
    defer { panel.hide() }
    try await Task.sleep(for: .milliseconds(300))
    let view = try #require(NSApp.windows.last { $0.isVisible && $0.title == "Hermesh Approval" }?.contentView)
    let rep = try #require(view.bitmapImageRepForCachingDisplay(in: view.bounds))
    view.cacheDisplay(in: view.bounds, to: rep)
    let firstRow = (0..<rep.pixelsHigh).first { y in (0..<rep.pixelsWide).contains { x in (rep.colorAt(x: x, y: y)?.alphaComponent ?? 0) > 0.5 } }
    let top = CGFloat(try #require(firstRow)) * view.bounds.height / CGFloat(rep.pixelsHigh)
    #expect(top < 32) // the 20-point padding, then the shield; the title bar is 32 more
}

// LAAuthenticationView draws at its control size whatever its frame.
@MainActor @Test func touchIDFitsItsFrame() {
    let v = LAAuthenticationView(context: LAContext(), controlSize: TouchIDView.size)
    #expect(v.fittingSize.width <= TouchIDView.side && v.fittingSize.height <= TouchIDView.side)
}

@Test func dataDirectoryFollowsHermeshDataDir() {
    #expect(Paths.dataDirectory(["HERMESH_DATA_DIR": "/tmp/hm-data"]).path == "/tmp/hm-data")
    #expect(Paths.dataDirectory([:]).path.hasSuffix("/Library/Application Support/Hermesh"))
}
