import Foundation
import Testing
@testable import HermeshSessions

// The cases her-client runs too (packages/her-client/testdata/file-refs.json): the Mac and the
// iPhone read a reply's files, and open them, the same way.
private let shared: [String: Any] = {
    let url = URL(fileURLWithPath: #filePath).deletingLastPathComponent().appendingPathComponent("../../../her-client/testdata/file-refs.json")
    return try! JSONSerialization.jsonObject(with: Data(contentsOf: url)) as! [String: Any]
}()

private func ref(_ any: Any?) -> FileRef? {
    guard let d = any as? [String: Any] else { return nil }
    return FileRef(path: d["path"] as! String, line: d["line"] as? Int, endLine: d["endLine"] as? Int)
}

@Test func linksAreFilesWithTheirLinesAsTheMacReadsThem() {
    for c in shared["links"] as! [[String: Any]] {
        let href = c["href"] as! String
        #expect(fileOfLink(href, windows: c["windows"] as? Bool ?? false) == ref(c["ref"]), "\(href)")
    }
}

@Test func inlineCodeIsAFileOnlyWhenPlainlyAPath() {
    for c in shared["code"] as! [[String: Any]] {
        let text = c["text"] as! String
        #expect(fileOfCode(text) == ref(c["ref"]), "\(text)")
    }
}

@Test func aFileShowsAndOpensByItsName() {
    for (name, view) in shared["views"] as! [String: String] {
        #expect(fileView(name).rawValue == view, "\(name)")
    }
    for c in shared["opens"] as! [[String: Any]] {
        let path = c["path"] as! String
        #expect(fileOpens(path, line: c["line"] as? Int).rawValue == c["to"] as! String, "\(path)")
    }
    for c in shared["images"] as! [[String: Any]] {
        let href = c["href"] as! String
        #expect(sessionImageRef(href) == c["path"] as? String, "\(href)")
    }
    for c in shared["sameImage"] as! [[String: Any]] {
        let a = c["a"] as! String, b = c["b"] as! String
        #expect(sameImagePath(a, b) == (c["same"] as! Bool), "\(a) vs \(b)")
    }
    for c in shared["pageTitle"] as! [[String: Any]] {
        let path = c["path"] as! String
        let title = c["title"] as? String
        #expect(pageTabTitle(path, pageTitle: title) == c["label"] as! String, "\(path) (\(title ?? "nil"))")
    }
}

@Test func resolveDocPathResolvesRelativePathsInsideMarkdownDocuments() {
    #expect(resolveDocPath("", "imgs/a.png") == "imgs/a.png")
    #expect(resolveDocPath("docs/guide", "./shot.png") == "docs/guide/shot.png")
    #expect(resolveDocPath("docs/guide", "../assets/a.png") == "docs/assets/a.png")
    #expect(resolveDocPath("/Users/me/.gemini/antigravity-cli/brain/123", "dark.png") == "/Users/me/.gemini/antigravity-cli/brain/123/dark.png")
    #expect(resolveDocPath("/Users/me/docs", "../../etc/a.png") == "/Users/etc/a.png")
    #expect(resolveDocPath("/Users/me/docs", "../../../etc/a.png") == "/etc/a.png")
    #expect(resolveDocPath("docs/guide", "/tmp/shot.png") == "/tmp/shot.png")
    #expect(resolveDocPath("/Users/me/docs", "~/shot.png") == "~/shot.png")
}
