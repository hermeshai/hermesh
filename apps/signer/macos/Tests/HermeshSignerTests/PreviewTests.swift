import CryptoKit
import Foundation
import HermeshProto
import SwiftProtobuf
import Testing
@testable import HermeshSigner
@testable import HermeshKit

typealias Grant = Hermesh_Identity_V1_Grant
typealias RootCert = Hermesh_Identity_V1_RootCert
typealias RootKey = Hermesh_Identity_V1_RootKey
typealias MachineCert = Hermesh_Identity_V1_MachineCert

let owner = "o_testowner"
let testKey = Data("fixed test key bytes".utf8)

/// A request as hermeshd posts it: message is the domain-separated body.
func request(type: String, body: Data, batch: String = "b1") -> SigningRequest {
    var r = SigningRequest()
    r.requestID = UUID().uuidString
    r.type = type
    r.body = body
    r.message = SigningPreview.signingInput(type: type, body: body)
    r.batchID = batch
    return r
}

func request<M: SwiftProtobuf.Message>(_ m: M, batch: String = "b1") -> SigningRequest {
    request(type: M.protoMessageName, body: try! m.serializedBytes(), batch: batch)
}

func hex(_ s: String) -> Data {
    var d = Data()
    var i = s.startIndex
    while i < s.endIndex {
        let j = s.index(i, offsetBy: 2)
        d.append(UInt8(s[i..<j], radix: 16)!)
        i = j
    }
    return d
}

func ts(_ seconds: Int64) -> Google_Protobuf_Timestamp { Google_Protobuf_Timestamp(seconds: seconds, nanos: 0) }

func grant(version: UInt64 = 1, projects: [String] = ["demo"], agents: [String] = ["gemini"]) -> Grant {
    var g = Grant()
    g.ownerID = owner
    g.grantID = "g_1"
    g.version = version
    g.clientID = "c_1"
    g.clientPublicKey = testKey
    g.clientLabel = "bot"
    g.machineIds = ["*"]
    g.projectIds = projects
    g.agents = agents
    g.taskVisibility = .own
    g.expiresAt = ts(1_792_400_000)
    return g
}

func rootKey(_ key: String, _ label: String) -> RootKey {
    var k = RootKey()
    k.publicKey = Data(key.utf8)
    k.label = label
    k.platform = "macos"
    return k
}

func rootCert(version: UInt64, _ keys: [RootKey]) -> RootCert {
    var c = RootCert()
    c.ownerID = owner
    c.version = version
    c.rootKeys = keys
    c.issuedAt = ts(1_792_400_000)
    return c
}

func devices(_ names: [String: String]) -> [String: String] {
    Dictionary(uniqueKeysWithValues: names.map { (SigningPreview.keyID(Data($0.key.utf8)), $0.value) })
}

// Values computed by pkg/identity (VerificationCode, JoinMatchCode,
// Fingerprint, ShortFingerprint, SigningInput) for the same inputs.
@Test func codesMatchTheGoImplementation() {
    #expect(SigningPreview.verificationCode(.machine, testKey, owner) == "K5FG-B5PE-48PS")
    #expect(SigningPreview.verificationCode(.client, testKey, owner) == "ZZ6X-PJ7Y-V1RA")
    #expect(SigningPreview.verificationCode(.device, testKey, owner) == "3EM1-TTND-MM6X")
    #expect(SigningPreview.joinMatch(Data("hermesh".utf8)) == "42")
    #expect(SigningPreview.joinMatch(Data("root cert body".utf8)) == "96")
    #expect(SigningPreview.keyID(testKey) == "osnyr62gef5hvxk5f5qrpjibfpr6x4tz2tdqbtt5bfljueq6fkzq")
    #expect(SigningPreview.fingerprint(testKey) == "osny r62g")
    #expect(SigningPreview.signingInput(type: "hermesh.identity.v1.Grant", body: Data([1, 2]))
        == hex("6865726d6573682d7369672d7631006865726d6573682e6964656e746974792e76312e4772616e74000102"))
}

// Bodies marshaled by Go (proto.MarshalOptions{Deterministic: true}) are
// shown: re-serializing them here gives back the same bytes.
@Test(arguments: [
    ("hermesh.identity.v1.Grant", "0a0b6f5f746573746f776e65721203675f3118012203635f312a1466697865642074657374206b65792062797465733203626f743a012a420464656d6f42037765624a0667656d696e694a05636f64657858016a080880b5d7d606100572080880b5d7d6061005"),
    ("hermesh.identity.v1.RootCert", "0a0b6f5f746573746f776e65721a150a056b6579206112054d616320411a056d61636f731a150a056b6579206212054d616320421a056d61636f7320022a080880b5d7d6061005"),
    ("hermesh.identity.v1.AdminCommand", "0a0b6f5f746573746f776e6572120263311a036d5f3122080880b5d7d606100552130a0464656d6f12092f7372632f64656d6f2002"),
    ("hermesh.identity.v1.OwnerDeviceCert", "0a0b6f5f746573746f776e65722200"),
])
func goBodiesAreShown(type: String, body: String) throws {
    _ = try SigningPreview.card(request(type: type, body: hex(body)))
}

@Test func newGrantShowsCodeAndScope() throws {
    let r = request(type: SigningPreview.grant, body: hex("0a0b6f5f746573746f776e65721203675f3118012203635f312a1466697865642074657374206b65792062797465733203626f743a012a420464656d6f42037765624a0667656d696e694a05636f64657858016a080880b5d7d606100572080880b5d7d6061005"))
    let c = try SigningPreview.card(r)
    #expect(c.title == "Approve “bot” to connect to your computer")
    #expect(c.code == "ZZ6X-PJ7Y-V1RA")
    #expect(c.who.isEmpty)
    // What stays on the card: the scope in one line, and the full trust it gives. The rest is behind ⓘ.
    #expect(c.rows.map(\.kind) == [.project, .codingAgent, .expires])
    #expect(c.rows.map(\.label) == ["Projects", "Coding agents", "Expires"])
    #expect(c.rows.map(\.value).prefix(2) == ["demo, web", "Gemini CLI, Codex"])
    #expect(c.rows[2].value.hasPrefix("Until "))
    #expect(c.warning?.hasPrefix("Full trust") == true)
    #expect(c.why.contains("owner key") && c.why.contains("end-to-end"))
    #expect(c.lines.contains("Projects it can use: demo, web"))
    #expect(c.lines.contains("On these machines: All"))
    #expect(c.lines.contains("Coding agents it can run: Gemini CLI, Codex"))
    #expect(c.lines.contains { $0.contains("including reading credentials and accessing the network") })
    #expect(c.ids.isEmpty)
}

@Test func grantLimitsBeyondTheDefaultsAreShown() throws {
    var g = grant()
    g.taskVisibility = .all
    g.maxConcurrentSessions = 3
    let c = try SigningPreview.card(request(g))
    #expect(c.lines.contains("Can also see other personal agents’ sessions in these projects"))
    #expect(c.lines.contains("Sessions at once: up to 3"))
    // A limit beyond the defaults stays on the card, not behind ⓘ.
    #expect(c.rows.contains(CardRow(kind: .extra, value: "Can also see other personal agents’ sessions in these projects")))
    #expect(c.rows.contains(CardRow(kind: .extra, value: "Sessions at once: up to 3")))
    #expect(try !SigningPreview.card(request(grant())).lines.contains { $0.lowercased().contains("session") })
}

// An MCP connection's grant says which server holds the agent's key.
@Test func custodialGrantSaysWhoHoldsTheKey() throws {
    var g = grant()
    g.custodian = "https://hermesh.io"
    let c = try SigningPreview.card(request(g))
    #expect(c.rows.contains(CardRow(kind: .extra, value: "Connects through hermesh.io, which holds its key")))
    #expect(c.lines.contains("Connects through hermesh.io, which holds its key"))
    #expect(c.why.contains("hermesh.io holds this agent’s key") && !c.why.contains("end-to-end"))
    #expect(try !SigningPreview.card(request(grant())).lines.contains { $0.contains("holds its key") })
}

// A pairing confirmed in the browser (a pairing link, an MCP consent) has
// no code on the agent's side to compare.
@Test func confirmedPairingShowsNoCode() throws {
    #expect(try SigningPreview.card(request(grant())).code != nil)
    var r = request(grant())
    r.confirmed = true
    #expect(try SigningPreview.card(r).code == nil)
}

@Test func changedGrantShowsWhatChanges() throws {
    var cur = Hermesh_Local_V1_Client()
    cur.grantID = "g_1"
    cur.spec.projectIds = ["demo"]
    cur.spec.agents = ["gemini"]
    cur.spec.machineIds = ["*"]
    cur.expiresAt = ts(1_790_000_000)
    let c = try SigningPreview.card(request(grant(version: 2, projects: ["demo", "web"])), .init(clients: ["g_1": cur]))
    #expect(c.title == "Change the access of personal agent “bot”")
    #expect(c.code == nil)
    #expect(c.lines.contains("Projects: demo → demo, web"))
    #expect(!c.lines.contains { $0.hasPrefix("Coding agents: ") })

    let unknown = try SigningPreview.card(request(grant(version: 2)))
    #expect(unknown.lines.first == "New settings:")
}

@Test func addingADeviceShowsItsCodeAndTheMatchDigits() throws {
    let r = request(rootCert(version: 2, [rootKey("key a", "Mac A"), rootKey("key b", "Mac B")]))
    let hints = SigningPreview.Hints(devices: devices(["key a": "Mac A"]))
    let c = try SigningPreview.card(r, hints)
    #expect(c.title == "Add “Mac B” as a trusted device")
    #expect(c.code == SigningPreview.verificationCode(.device, Data("key b".utf8), owner))
    #expect(c.rows == [CardRow(kind: .device, value: "Mac B"), CardRow(kind: .trustedDevices, value: "2 in all once it joins")])
    let m = try #require(SigningPreview.joinMatchCode(r, hints))
    #expect(m.label == "Mac B")
    #expect(m.digits == SigningPreview.joinMatch(r.body))

    // Found by the code it shows (a Linux trusted device): nothing to compare.
    var found = r
    found.confirmed = true
    #expect(try SigningPreview.card(found, hints).code == nil)
}

@Test func removingADeviceAndReissuing() throws {
    let rc = request(rootCert(version: 3, [rootKey("key a", "Mac A")]))
    var cur = Hermesh_Local_V1_Client()
    cur.grantID = "g_1"
    cur.spec.projectIds = ["demo"]
    cur.spec.agents = ["gemini"]
    cur.spec.machineIds = ["*"]
    var mc = MachineCert()
    mc.ownerID = owner
    mc.machineID = "m_1"
    mc.machinePublicKey = testKey
    let hints = SigningPreview.Hints(machineNames: ["m_1": "box"], clients: ["g_1": cur], devices: devices(["key a": "Mac A", "key b": "Mac B"]))
    let batch = [rc, request(mc), request(grant(version: 2))].map(fromHermesh)
    let cards = try SigningPreview.cards(batch, hints)
    #expect(cards.map(\.title) == ["Remove trusted device “Mac B”", "Reissue the certificate of machine “box”", "Reissue the grant of “bot”"])
    #expect(cards[1].why == "Once approved, this machine stays yours, unchanged, now signed by this Mac.")
    #expect(SigningPreview.joinMatchCode(rc, hints) == nil)
    let c = Confirmation(batchID: "b1", requests: batch, hints: hints, signerBundle: hermeshSigner)
    #expect(c.reason == "Remove trusted device “Mac B”, and reissue the credentials it signed (2)")
    #expect(c.match == nil)
    let alone = Confirmation(batchID: "b2", requests: [fromHermesh(rc)], hints: hints, signerBundle: hermeshSigner)
    #expect(alone.reason == "Remove trusted device “Mac B”")
}

@Test func rootCertWithoutTheDeviceList() throws {
    let r = request(rootCert(version: 2, [rootKey("key a", "Mac A"), rootKey("key b", "Mac B")]))
    #expect(try SigningPreview.card(r).title == "Update your list of trusted devices")
    let m = try #require(SigningPreview.joinMatchCode(r, .init()))
    #expect(m.label == nil)
}

@Test func firstRootCertCreatesTheOwner() throws {
    let c = try SigningPreview.card(request(rootCert(version: 1, [rootKey("key a", "Mac A")])))
    #expect(c.title == "Create your owner identity")
    #expect(c.lines.isEmpty && c.who.isEmpty)
    #expect(c.ids == ["Owner identity ID: \(owner)", "Device name: Mac A"]) // behind ⓘ
    #expect(SigningPreview.joinMatchCode(request(rootCert(version: 1, [rootKey("key a", "Mac A")])), .init()) == nil)
}

@Test func machineCerts() throws {
    var m = MachineCert()
    m.ownerID = owner
    m.machineID = "m_1"
    m.machinePublicKey = testKey
    let new = try SigningPreview.card(request(m))
    #expect(new.title == "Approve a new machine")
    #expect(new.code == "K5FG-B5PE-48PS")
    #expect(new.lines.contains("Fingerprint: osny r62g"))

    #expect(try SigningPreview.card(request(m), .init(machineNames: ["m_1": "box"])).title == "Reissue the certificate of machine “box”")

    #expect(SigningPreview.joinMatchCode(request(m), .init()) == nil)
    // A Mac joining as a machine only asked for the two digits.
    var asked = request(m)
    asked.showMatch = true
    let match = try #require(SigningPreview.joinMatchCode(asked, .init()))
    #expect(match.label == nil && match.machine)
    #expect(match.digits == SigningPreview.joinMatch(asked.body))
    #expect(Confirmation(batchID: "b1", requests: [asked], hints: .init(), signerBundle: hermeshSigner).match
        == Confirmation.JoinMatch(label: nil, digits: match.digits, machine: true))

    m.deviceKeyID = "k1"
    let own = try SigningPreview.card(request(m))
    #expect(own.title == "Share the agents on this Mac")
    #expect(own.code == nil)
    #expect(own.ids == ["Machine ID: m_1", "Fingerprint: osny r62g"])
}

@Test func joinRequestAndDeviceCert() throws {
    var p = Hermesh_Identity_V1_PairingRequest()
    p.kind = .device
    p.subjectPublicKey = testKey
    p.label = "Mac B"
    p.ownerID = owner
    let c = try SigningPreview.card(request(p))
    #expect(c.title == "Add this Mac to your Hermesh")
    #expect(c.code == "3EM1-TTND-MM6X")
    #expect(c.ids == ["Device name: Mac B", "Owner identity ID: \(owner)"])

    p.kind = .client
    #expect(throws: Refusal.unsupported(SigningPreview.pairingRequest)) { try SigningPreview.card(request(p)) }

    var d = Hermesh_Identity_V1_OwnerDeviceCert()
    d.ownerID = owner
    d.label = "Mac A"
    #expect(try SigningPreview.card(request(d)).title == "Issue this Mac a certificate for end-to-end connections")
}

@Test func adminCommands() throws {
    var a = Hermesh_Identity_V1_AdminCommand()
    a.ownerID = owner
    a.targetMachineID = "m_1"
    var p = Hermesh_Identity_V1_AddProject()
    p.projectID = "demo"
    p.path = "/src/demo"
    a.addProject = p
    let c = try SigningPreview.card(request(a), .init(machineNames: ["m_1": "box"]))
    #expect(c.title == "Add project “demo” to machine “box”")
    #expect(c.lines.contains("Directory: /src/demo"))
    #expect(c.lines.contains("Available coding agents: All"))
    a.addProject.agents = ["claude", "codex"]
    #expect(try SigningPreview.card(request(a)).lines.contains("Available coding agents: Claude Code, Codex"))

    a.command = nil
    #expect(throws: Refusal.unsupported(SigningPreview.adminCommand)) { try SigningPreview.card(request(a)) }
}

// MARK: Refusals

@Test func aMessageThatIsNotTheBodyIsRefused() {
    var r = request(grant())
    r.message = SigningPreview.signingInput(type: SigningPreview.machineCert, body: r.body)
    #expect(throws: Refusal.messageMismatch(SigningPreview.grant)) { try SigningPreview.card(r) }
}

@Test func anUnknownTypeIsRefused() {
    let r = request(type: "hermesh.identity.v1.RevocationList", body: Data())
    #expect(throws: Refusal.unknownType("hermesh.identity.v1.RevocationList")) { try SigningPreview.card(r) }
}

@Test func anUnparseableBodyIsRefused() {
    let r = request(type: SigningPreview.grant, body: Data([0xFF, 0xFF, 0xFF]))
    #expect(throws: Refusal.unparseable(SigningPreview.grant)) { try SigningPreview.card(r) }
}

// Field 100, varint 1: a field this signer does not know.
let unknownField = Data([0xA0, 0x06, 0x01])

@Test func anUnknownFieldIsRefused() throws {
    let body: Data = try grant().serializedBytes() + unknownField
    #expect(throws: Refusal.unknownFields(SigningPreview.grant)) { try SigningPreview.card(request(type: SigningPreview.grant, body: body)) }
}

@Test func anUnknownFieldInANestedMessageIsRefused() throws {
    var c = RootCert()
    c.ownerID = owner
    let key: Data = try rootKey("key a", "Mac A").serializedBytes() + unknownField
    let body: Data = try c.serializedBytes() + Data([0x1A, UInt8(key.count)]) + key // root_keys = 3
    #expect(throws: Refusal.unknownFields(SigningPreview.rootCert)) { try SigningPreview.card(request(type: SigningPreview.rootCert, body: body)) }
}

@Test func oneBadRequestRefusesTheBatch() throws {
    let bad = request(type: SigningPreview.grant, body: try grant().serializedBytes() + unknownField)
    let c = Confirmation(batchID: "b1", requests: [request(grant()), bad], hints: .init(), signerBundle: hermeshSigner)
    #expect(c.cards.isEmpty)
    #expect(c.refusals == [.unknownFields(SigningPreview.grant)])
}

// The first setup says it in one line; anything that also adds someone, or
// a later root cert, keeps its cards.
@Test func firstSetupIsOneLine() throws {
    let root = rootCert(version: 1, [rootKey("key a", "Mac A")])
    var device = Hermesh_Identity_V1_OwnerDeviceCert()
    device.ownerID = owner
    device.devicePublicKey = testKey
    device.label = "Mac A"
    var machine = MachineCert()
    machine.ownerID = owner
    machine.machineID = "m_1"
    machine.machinePublicKey = testKey
    machine.deviceKeyID = SigningPreview.keyID(Data("key a".utf8))
    let setup = [request(root), request(device), request(machine)]
    let c = Confirmation(batchID: "b1", requests: setup, hints: .init(), signerBundle: hermeshSigner)
    #expect(c.summary == "Creates your owner key in this Mac’s Secure Enclave. Without it, no one can use the services on your computers, and only you can approve, with Touch ID.")
    #expect(c.cards.count == 3) // behind Details
    #expect(c.reason.hasSuffix("Create your owner key")) // prefixed with the caller: none in these requests
    #expect(SigningPreview.isFirstSetup([request(root), request(device)]))

    var other = machine
    other.deviceKeyID = SigningPreview.keyID(Data("key b".utf8))
    #expect(!SigningPreview.isFirstSetup([request(root), request(device), request(other)]))
    #expect(!SigningPreview.isFirstSetup(setup + [request(grant())]))
    #expect(!SigningPreview.isFirstSetup([request(rootCert(version: 2, [rootKey("key a", "Mac A")])), request(device)]))
    #expect(!SigningPreview.isFirstSetup([request(device)]))
    #expect(Confirmation(batchID: "b2", requests: [request(grant())], hints: .init(), signerBundle: hermeshSigner).summary == nil)
}
