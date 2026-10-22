import CryptoKit
import Foundation
import HermeshProto
import SwiftProtobuf
import Testing
@testable import HermeshKit

/// The Chinese strings file: English key → Chinese.
private func chinese() throws -> [String: String] {
    let url = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        .appendingPathComponent("Sources/HermeshKit/Resources/zh-Hans.lproj/Localizable.strings")
    return try #require(PropertyListSerialization.propertyList(from: Data(contentsOf: url), format: nil) as? [String: String])
}

private func specifiers(_ s: String) -> [String] {
    s.matches(of: #/%(?:\d+\$)?(@|lld)/#).map { String($0.output.1) }.sorted()
}

@Test func everyStringHasAChineseTranslation() throws {
    let entries = try chinese()
    #expect(!entries.isEmpty)
    for (key, zh) in entries {
        #expect(!zh.isEmpty, "\(key)")
        #expect(specifiers(zh) == specifiers(key), "\(key)")
    }
}

@Test func everyMacStringHasItsIPhoneWording() throws {
    let entries = try chinese()
    for key in entries.keys where key.contains("this Mac") && !key.contains("agents on this Mac") && !key.contains("Share the agents") {
        let iPhone = key.replacingOccurrences(of: "Mac", with: "iPhone").replacingOccurrences(of: "Touch ID", with: "Face ID")
        #expect(entries[iPhone] != nil, "\(iPhone)")
    }
}

private func request<M: SwiftProtobuf.Message>(_ m: M) -> SigningRequest {
    var r = SigningRequest()
    r.type = M.protoMessageName
    r.body = try! m.serializedBytes()
    r.message = SigningPreview.signingInput(type: r.type, body: r.body)
    return r
}

@Test func theIPhoneSaysIPhone() throws {
    var p = Hermesh_Identity_V1_PairingRequest()
    p.kind = .device
    p.subjectPublicKey = Data("key".utf8)
    p.ownerID = "o_x"
    defer { SigningPreview.device = .mac }
    SigningPreview.device = .mac
    #expect(try SigningPreview.card(request(p)).title == "Add this Mac to your Hermesh")
    SigningPreview.device = .iPhone
    #expect(try SigningPreview.card(request(p)).title == "Add this iPhone to your Hermesh")
    #expect(SigningPreview.firstSetupSummary.contains("Face ID"))
}

@Test func theBundleHasTheChineseStrings() throws {
    let bundle = HermeshKitResources.bundle
    let lang = try #require(bundle.localizations.first { $0.lowercased() == "zh-hans" })
    let path = try #require(bundle.path(forResource: lang, ofType: "lproj"))
    let zh = try #require(Bundle(path: path))
    let title = zh.localizedString(forKey: "Approve “%@” to connect to your computer", value: nil, table: nil)
    #expect(String(format: title, "bot") == "批准「bot」连接你的电脑")
    #expect(zh.localizedString(forKey: "Add this iPhone to your Hermesh", value: nil, table: nil) == "让这台 iPhone 加入你的 Hermesh")
}

@Test func errorsComeFromTheConnectBody() {
    let e = LocalAPI.parseError(Data(#"{"code":"not_found","message":"gone"}"#.utf8), status: 404, headers: [:])
    #expect(e.code == "not_found" && e.message == "gone")
    #expect(LocalAPI.parseError(Data("x".utf8), status: 502, headers: [:]).code == "http_502")
}

/// A notice Go sealed (identity.SealNotice) opens here: the same HPKE suite on both sides.
@Test func aNoticeSealedInGoOpens() throws {
    func hex(_ s: String) -> Data {
        Data(stride(from: 0, to: s.count, by: 2).map { i in UInt8(s[s.index(s.startIndex, offsetBy: i) ..< s.index(s.startIndex, offsetBy: i + 2)], radix: 16)! })
    }
    let key = try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: hex("e9a02f20d8bc86953f80f7bba05ab60bf736c5d7cc74bba86a7ef7672d2c2370"))
    let sealed = hex("dbc8510e4f35c4fbdaac8c74445d34ab2ac75dce8edf8f0bb366045bb2a4f069590d84200a8a738ae79065f6af1cd0c502b3134c1792cf640fb1f1fccd34d0dfae2177c644bc5bf76b85")
    let n = try NoticeKey.open(sealed, with: key)
    #expect(n.kind == "question" && n.title == "选颜色" && n.sessionID == "s_1")
    var tampered = sealed
    tampered[tampered.count - 1] ^= 1
    #expect(throws: (any Error).self) { try NoticeKey.open(tampered, with: key) }
}
