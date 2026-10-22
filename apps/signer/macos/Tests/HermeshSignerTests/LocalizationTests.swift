import Foundation
import Testing
@testable import HermeshSigner
import HermeshKit

/// The Chinese strings file: English key → Chinese.
private func chinese() throws -> [String: String] {
    let url = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        .appendingPathComponent("Sources/HermeshSigner/Resources/zh-Hans.lproj/Localizable.strings")
    return try #require(PropertyListSerialization.propertyList(from: Data(contentsOf: url), format: nil) as? [String: String])
}

/// The format specifiers of s (%@, %lld, positional or not), sorted.
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

@Test func theBundleHasTheChineseStrings() throws {
    // Its folder is zh-Hans.lproj, or zh-hans.lproj from SwiftPM's older build system; Foundation takes either.
    let lang = try #require(Bundle.module.localizations.first { $0.lowercased() == "zh-hans" })
    let path = try #require(Bundle.module.path(forResource: lang, ofType: "lproj"))
    let zh = try #require(Bundle(path: path))
    #expect(zh.localizedString(forKey: "Deny", value: nil, table: nil) == "拒绝")
    // The cards' text comes from HermeshKit's bundle, which the signer app carries too.
    let kit = HermeshKitResources.bundle
    let kitLang = try #require(kit.localizations.first { $0.lowercased() == "zh-hans" })
    let kitPath = try #require(kit.path(forResource: kitLang, ofType: "lproj"))
    let kitZh = try #require(Bundle(path: kitPath))
    let add = kitZh.localizedString(forKey: "Add project “%@” to %@", value: nil, table: nil)
    #expect(String(format: add, "demo", "机器「box」") == "在机器「box」上添加项目「demo」")
}
