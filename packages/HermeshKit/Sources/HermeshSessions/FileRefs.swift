import Foundation

// What a session's reply points at (docs/session-files.md §3) and how a file opens (§4):
// her-client's refs.ts and fileView.ts on the iPhone. Both run the cases in
// packages/her-client/testdata/file-refs.json, so the Mac and the iPhone read a reply alike.

/// A file or folder as a reply names it: the path as written, and the lines it points to.
public struct FileRef: Equatable, Sendable {
    public var path: String
    public var line: Int?
    public var endLine: Int?

    public init(path: String, line: Int? = nil, endLine: Int? = nil) {
        self.path = path
        self.line = line
        self.endLine = endLine
    }
}

private func regex(_ pattern: String) -> NSRegularExpression { try! NSRegularExpression(pattern: pattern) }

/// Lines at a path's end: `:42`, `:42:7` (the column is not used), `:42-60`, `#L42`, `#L42-L60`.
private let linesAtEnd = regex(#"(?::(\d+)(?::\d+|-(\d+))?|#L(\d+)(?:-L?(\d+))?)$"#)
/// A scheme before a colon: `mailto:`, `javascript:`; a path's own colons come after a slash.
private let scheme = regex(#"^[A-Za-z][A-Za-z0-9+.-]*:"#)
private let drive = regex(#"^[A-Za-z]:[\\/]"#)
private let extensionAtEnd = regex(#"\.[A-Za-z0-9]{1,8}$"#)

private func matches(_ re: NSRegularExpression, _ s: String) -> Bool {
    re.firstMatch(in: s, range: NSRange(s.startIndex..., in: s)) != nil
}

private func withLines(_ text: String) -> FileRef {
    let ns = text as NSString
    guard let m = linesAtEnd.firstMatch(in: text, range: NSRange(location: 0, length: ns.length)) else { return FileRef(path: text) }
    func group(_ i: Int) -> Int? {
        let r = m.range(at: i)
        return r.location == NSNotFound ? nil : Int(ns.substring(with: r))
    }
    guard let line = group(1) ?? group(3), line >= 1 else { return FileRef(path: text) }
    let end = group(2) ?? group(4)
    return FileRef(path: ns.substring(to: m.range.location), line: line, endLine: end.flatMap { $0 > line ? $0 : nil })
}

private func decoded(_ path: String) -> String { path.removingPercentEncoding ?? path }

/// Files that have no extension and are still plainly files.
private let bareNames: Set<String> = ["Makefile", "Dockerfile", "LICENSE", "Gemfile", "Rakefile", "Procfile", "Justfile", "Brewfile", "Vagrantfile", "CODEOWNERS", "Containerfile", "Jenkinsfile"]

/// A name with an extension, or a well-known one without.
private func fileName(_ name: String) -> Bool { matches(extensionAtEnd, name) || bareNames.contains(name) }

private func isWeb(_ s: String) -> Bool {
    let lower = s.lowercased()
    return (lower.hasPrefix("http://") || lower.hasPrefix("https://")) && URL(string: s) != nil
}

/// A link's target as a file: a path (relative, absolute, `~/`, a `file://` address), with the
/// lines it points to. Nil for a web address, another scheme (`mailto:`), an anchor, nothing.
/// `windows`: a drive's path (`C:\`) is a path, not a scheme.
public func fileOfLink(_ href: String, windows: Bool = false) -> FileRef? {
    let target = href.trimmingCharacters(in: .whitespacesAndNewlines)
    if target.isEmpty || target.hasPrefix("#") || isWeb(target) { return nil }
    if target.lowercased().hasPrefix("file:") {
        var rest = String(target.dropFirst(5))
        if rest.hasPrefix("//") {
            rest.removeFirst(2)
            let host = rest.prefix { $0 != "/" }
            if !host.isEmpty && host.lowercased() != "localhost" { return nil }
            rest.removeFirst(host.count)
        }
        let (pathPart, hash) = rest.firstIndex(of: "#").map { (String(rest[..<$0]), String(rest[$0...])) } ?? (rest, "")
        let path = decoded(String(pathPart.prefix { $0 != "?" }))
        let ref = withLines(path + hash)
        var out = ref
        if windows, matches(regex(#"^/[A-Za-z]:/"#), ref.path) { out.path = String(ref.path.dropFirst()) }
        // A fragment that is not lines is a place in the file.
        if out.line == nil, let i = out.path.firstIndex(of: "#") { out.path = String(out.path[..<i]) }
        return out.path.isEmpty ? nil : out
    }
    let ref = withLines(target)
    // "tel:123" has a scheme; "README.md:12" and "Makefile:3" are files with a line.
    if matches(scheme, target) && !(windows && matches(drive, target)) {
        let before = String(target[..<target.firstIndex(of: ":")!])
        if !(ref.line != nil && ref.path == before && fileName(before)) { return nil }
    }
    // A fragment that is not lines (`#usage`) is a place in the file, which opens at its start.
    var path = ref.path
    if ref.line == nil, let i = path.firstIndex(of: "#") { path = String(path[..<i]) }
    if path.isEmpty { return nil }
    return FileRef(path: decoded(path), line: ref.line, endLine: ref.endLine)
}

private let letterOrNumber = regex(#"[\p{L}\p{N}]"#)
private let whitespace = regex(#"\s"#)
private let explicitPath = regex(#"^(/|~/|\.\.?/)"#)
private let shellChars = regex(#"[*?<>|"`]"#)
private let leadingSigil = regex(#"^[-$@#!]"#)

/// Inline code that names a file or folder plainly enough to look it up: one line, no shell or
/// glob characters, a slash or an extension (or a well-known name like `Makefile`), spaces only
/// in an explicit path. Whether it exists is for the machine to say; only then is it a file.
public func fileOfCode(_ text: String, windows: Bool = false) -> FileRef? {
    let code = text.trimmingCharacters(in: .whitespacesAndNewlines)
    if code.isEmpty || code.utf16.count > 300 || code.contains("\n") || code.contains("://") { return nil }
    if matches(shellChars, code) || matches(leadingSigil, code) { return nil }
    let ref = withLines(code)
    let path = ref.path
    if !matches(letterOrNumber, path) { return nil }
    if matches(whitespace, path) && !matches(explicitPath, path) && !(windows && matches(drive, path)) { return nil }
    let slash = path.contains("/") || (windows && path.contains("\\"))
    if !slash && !fileName(path) { return nil }
    if matches(scheme, path) && !(windows && matches(drive, path)) { return nil }
    return ref
}

/// What shows a file.
public enum FileView: String, Sendable {
    case markdown, svg, image, pdf, page, media, office, other, text, code
}

/// Where a click takes a file on the Mac: over the window, the file panel, or Quick Look.
public enum FileOpen: String, Sendable {
    case preview, panel, quicklook
}

private let views: [(NSRegularExpression, FileView)] = [
    (regex(#"(?i)\.(md|markdown)$"#), .markdown),
    (regex(#"(?i)\.svg$"#), .svg),
    (regex(#"(?i)\.(png|jpe?g|gif|webp|bmp|ico|heic|tiff?)$"#), .image),
    (regex(#"(?i)\.pdf$"#), .pdf),
    (regex(#"(?i)\.html?$"#), .page),
    (regex(#"(?i)\.(mp3|m4a|wav|aac|flac|mp4|mov|m4v|avi|mkv|webm)$"#), .media),
    (regex(#"(?i)\.(docx?|xlsx?|xlsm|pptx?|key|numbers|pages|rtf|odt|ods|odp|epub)$"#), .office),
    (regex(#"(?i)\.(zip|rar|7z|tar|gz|tgz|dmg|pkg|app|ipa|apk|exe|bin)$"#), .other),
    (regex(#"(?i)\.(txt|text|log|csv|tsv)$"#), .text),
]

/// Names with no extension that are code all the same.
private let codeNames: Set<String> = ["Makefile", "Dockerfile", "Gemfile", "Rakefile", "Procfile", "Justfile", "Brewfile", "Vagrantfile", "Containerfile", "Jenkinsfile"]

/// A path's last part.
public func fileBaseName(_ path: String) -> String {
    var p = Substring(path)
    while p.last == "/" || p.last == "\\" { p = p.dropLast() }
    return String(p.split(whereSeparator: { $0 == "/" || $0 == "\\" }).last ?? "")
}

public func fileView(_ path: String) -> FileView {
    let name = fileBaseName(path)
    for (re, view) in views where matches(re, name) { return view }
    let stem = name.hasPrefix(".") ? String(name.dropFirst()) : name
    if !stem.contains(".") || stem.hasSuffix(".") { return codeNames.contains(name) ? .code : .text }
    return .code
}

/// A result to look at opens over the window; code and text, and anything a reply points at a
/// line of, in the file panel; what the app does not show itself, in Quick Look.
public func fileOpens(_ path: String, line: Int? = nil) -> FileOpen {
    if line != nil { return .panel }
    switch fileView(path) {
    case .media, .office, .other: return .quicklook
    case .text, .code: return .panel
    default: return .preview
    }
}
