import Foundation
import Testing
@testable import HermeshSessions

/// Every string the code looks up has its Chinese, with the same format specifiers.
@Test func everyStringHasAChineseTranslation() throws {
    let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    let strings = root.appendingPathComponent("Sources/HermeshSessions/Resources/zh-Hans.lproj/Localizable.strings")
    let zh = try #require(PropertyListSerialization.propertyList(from: Data(contentsOf: strings), format: nil) as? [String: String])
    let sources = try FileManager.default.contentsOfDirectory(at: root.appendingPathComponent("Sources/HermeshSessions"), includingPropertiesForKeys: nil)
        .filter { $0.pathExtension == "swift" }
    var keys: Set<String> = []
    for url in sources {
        let src = try String(contentsOf: url, encoding: .utf8)
        for m in src.matches(of: #/String\(localized: "((?:[^"\\]|\\.)*)"/#) {
            // \(…) becomes a specifier: whole numbers are %lld, anything else %@.
            var key = String(m.output.1)
            key = key.replacing(#/\\\((Int\([^)]*\)|s|m|n|m / 60|m % 60|s % 60)\)/#) { _ in "%lld" }
            key = key.replacing(#/\\\(([^()]|\([^()]*\))*\)/#) { _ in "%@" }
            keys.insert(key)
        }
    }
    #expect(keys.count > 80)
    func specifiers(_ s: String) -> [String] { s.matches(of: #/%(?:\d+\$)?(@|lld)/#).map { String($0.output.1) }.sorted() }
    for k in keys {
        guard let t = zh[k] else { Issue.record("no Chinese for \(k)"); continue }
        #expect(specifiers(t) == specifiers(k), "\(k)")
    }
}

@Test func theBundleSpeaksChinese() throws {
    let path = try #require(Bundle.module.path(forResource: Bundle.module.localizations.first { $0.lowercased() == "zh-hans" }, ofType: "lproj"))
    let zh = try #require(Bundle(path: path))
    #expect(zh.localizedString(forKey: "verb.running", value: nil, table: nil) == "正在运行")
    #expect(zh.localizedString(forKey: "Running", value: nil, table: nil) == "运行中")
}
