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

private let htmlExt = regex(#"(?i)\.html?$"#)
private let internalPageURL = regex(#"(?i)^(?:hermesh-file|about):"#)

/// The label for an HTML page tab: the page's document `<title>` when set, otherwise its filename
/// without `.html`/`.htm` (since the globe glyph already marks it as a page).
public func pageTabTitle(_ path: String, pageTitle: String? = nil) -> String {
    let title = (pageTitle ?? "")
        .split(whereSeparator: { $0.isWhitespace })
        .joined(separator: " ")
    if !title.isEmpty && !matches(internalPageURL, title) { return title }
    let name = fileBaseName(path)
    let stem = htmlExt.stringByReplacingMatches(in: name, range: NSRange(name.startIndex..., in: name), withTemplate: "")
    return stem.isEmpty ? name : stem
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

private let rasterImage = regex(#"(?i)\.(png|jpe?g|gif|webp)$"#)

/// Whether a session file path (without a line reference) is a raster image rendered inline and opened in the Lightbox.
public func isSessionImage(_ path: String, line: Int? = nil) -> Bool {
    line == nil && matches(rasterImage, fileBaseName(path))
}

/// Extracts a local/session raster image path from a Markdown image or link target (`![alt](href)` / `[label](href)`),
/// recognizing Windows drive paths regardless of the viewer's OS and rejecting `http(s):` / `data:` URLs.
public func sessionImageRef(_ href: String) -> String? {
    guard let ref = fileOfLink(href, windows: true), isSessionImage(ref.path, line: ref.line) else { return nil }
    return ref.path
}

/// Normalizes a reply's image path so `![alt](/a/b.png)` and `[b.png](file:///a/b.png)` match.
public func normalizeImagePath(_ path: String) -> String {
    var p = path.trimmingCharacters(in: .whitespacesAndNewlines).replacingOccurrences(of: "\\", with: "/")
    while p.hasPrefix("./") { p.removeFirst(2) }
    while p.contains("//") { p = p.replacingOccurrences(of: "//", with: "/") }
    let dotSeg = regex(#"(/\.)+(?=/|$)"#)
    p = dotSeg.stringByReplacingMatches(in: p, range: NSRange(p.startIndex..., in: p), withTemplate: "")
    if p.count > 1 && p.hasSuffix("/") { p.removeLast() }
    return p
}

private func isAbsImagePath(_ p: String) -> Bool {
    p.hasPrefix("/") || p.hasPrefix("~/") || matches(regex(#"^[A-Za-z]:/"#), p)
}

/// Whether two image references in the same reply point to the same file (for deduplicating `![alt](p)` + `[name](p)`).
public func sameImagePath(_ a: String, _ b: String) -> Bool {
    let na = normalizeImagePath(a)
    let nb = normalizeImagePath(b)
    if na.isEmpty || nb.isEmpty { return false }
    if na == nb { return true }
    let aa = isAbsImagePath(na)
    let ab = isAbsImagePath(nb)
    if aa == ab { return false }
    return aa ? na.hasSuffix("/\(nb)") : nb.hasSuffix("/\(na)")
}

/// Resolves a relative path inside a Markdown document at `baseDir` ("" for chat replies).
public func resolveDocPath(_ baseDir: String, _ path: String) -> String {
    var base = baseDir.trimmingCharacters(in: .whitespacesAndNewlines).replacingOccurrences(of: "\\", with: "/")
    while base.hasSuffix("/") { base.removeLast() }
    let p = path.trimmingCharacters(in: .whitespacesAndNewlines).replacingOccurrences(of: "\\", with: "/")
    if base.isEmpty || p.isEmpty || isAbsImagePath(p) { return path }
    let root: String = if base.hasPrefix("/") {
        "/"
    } else if base.hasPrefix("~/") {
        "~/"
    } else if matches(regex(#"^[A-Za-z]:/"#), base) {
        String(base.prefix(3))
    } else {
        ""
    }
    let segs = base.dropFirst(root.count).split(separator: "/", omittingEmptySubsequences: true)
        + p.split(separator: "/", omittingEmptySubsequences: true)
    var out: [Substring] = []
    for s in segs {
        if s == "." { continue }
        if s == ".." {
            if let last = out.last, last != ".." { out.removeLast() }
            else if root.isEmpty { out.append("..") }
            continue
        }
        out.append(s)
    }
    return root + out.joined(separator: "/")
}

// MARK: File tree & folder browser visual semantics (Scheme A: Seti + Linguist + Material role priority)

public enum FileGlyph: String, Sendable, Equatable {
    case folder, folderTest, go, test, swift, code, braces, sliders, markdown, page, terminal, cube, lock, git, image, pdf, doc
}

public enum FileTone: String, Sendable, Equatable {
    case folder, folderTest, cyan, blue, orange, green, amber, purple, doc, red, slate
}

public struct FileAppearance: Sendable, Equatable {
    public let glyph: FileGlyph
    public let tone: FileTone
    public let dimmed: Bool

    public init(glyph: FileGlyph, tone: FileTone, dimmed: Bool) {
        self.glyph = glyph
        self.tone = tone
        self.dimmed = dimmed
    }
}

public struct FileNameParts: Sendable, Equatable {
    public let stem: String
    public let sub: String
    public let ext: String

    public init(stem: String, sub: String, ext: String) {
        self.stem = stem
        self.sub = sub
        self.ext = ext
    }
}

private let lockFiles: Set<String> = [
    "go.sum", "pnpm-lock.yaml", "package-lock.json", "yarn.lock", "cargo.lock", "podfile.lock",
    "package.resolved", "gemfile.lock", "composer.lock", "poetry.lock", "uv.lock", "bun.lock", "bun.lockb",
]

private let buildShellFiles: Set<String> = [
    "makefile", "justfile", "rakefile", "procfile", "brewfile", "gemfile", "vagrantfile", "jenkinsfile",
]

private let gitIgnoreFiles: Set<String> = [
    ".gitignore", ".gitattributes", ".gitmodules", ".dockerignore", ".npmignore", ".prettierignore", ".eslintignore",
]

private let licenseFiles: Set<String> = ["license", "license.md", "license.txt", "copying"]
private let testDirPattern = regex(#"(?i)^(?:testdata|tests?|e2e|__tests__|specs?)$"#)
private let testDirSuffix = regex(#"[a-z0-9](?:UITests|Tests)$"#)
private let testFilePattern = regex(#"(?i)(?:_(?:[a-z0-9]+_)?test\.[a-z0-9]+|\.(?:[a-z0-9]+\.)?(?:test|spec)\.[a-z0-9]+)$"#)
private let pascalTestFilePattern = regex(#"(?:[a-z0-9](?:UITests?|Tests?)|^(?:UITests?|Tests?))\.(?:swift|kt|java)$"#)
private let pyTestPrefixPattern = regex(#"(?i)^test_[^.]+\.py$"#)
private let splitTestPattern = regex(#"(?i)^(.+?)((?:\.[a-z0-9]+)?\.(?:test|spec)|_(?:[a-z0-9]+_)?test)(\.[A-Za-z0-9]+)$"#)
private let splitPascalTestPattern = regex(#"^(.+[a-z0-9])(UITests?|Tests?)(\.(?:swift|kt|java))$"#)

/// Semantic icon glyph, OKLCH palette tone, and noise-dimming flag for a directory or file entry.
/// Priority: directory role -> exact filename / lockfile -> compound test role -> extension.
public func fileAppearance(_ pathOrName: String, dir: Bool = false) -> FileAppearance {
    let base = fileBaseName(pathOrName)
    let name = base.isEmpty ? pathOrName : base
    let lower = name.lowercased()

    if dir {
        if lower.hasPrefix(".") { return FileAppearance(glyph: .folder, tone: .slate, dimmed: true) }
        if matches(testDirPattern, lower) || matches(testDirSuffix, name) {
            return FileAppearance(glyph: .folderTest, tone: .folderTest, dimmed: false)
        }
        return FileAppearance(glyph: .folder, tone: .folder, dimmed: false)
    }

    if lockFiles.contains(lower) { return FileAppearance(glyph: .lock, tone: .slate, dimmed: true) }
    if lower == "go.mod" || lower == "go.work" { return FileAppearance(glyph: .cube, tone: .cyan, dimmed: false) }
    if lower == "cargo.toml" { return FileAppearance(glyph: .cube, tone: .orange, dimmed: false) }
    if lower == "dockerfile" || lower == "containerfile" || lower.hasPrefix("dockerfile.") || lower.hasSuffix(".dockerfile") {
        return FileAppearance(glyph: .cube, tone: .cyan, dimmed: false)
    }
    if buildShellFiles.contains(lower) { return FileAppearance(glyph: .terminal, tone: .green, dimmed: false) }
    if gitIgnoreFiles.contains(lower) { return FileAppearance(glyph: .git, tone: .red, dimmed: true) }
    if licenseFiles.contains(lower) { return FileAppearance(glyph: .doc, tone: .slate, dimmed: true) }
    if lower == ".env" || lower.hasPrefix(".env.") { return FileAppearance(glyph: .sliders, tone: .amber, dimmed: true) }

    if matches(testFilePattern, name) || matches(pascalTestFilePattern, name) || matches(pyTestPrefixPattern, name) {
        return FileAppearance(glyph: .test, tone: .green, dimmed: false)
    }

    let ext: String = if let dot = lower.lastIndex(of: "."), dot > lower.startIndex {
        String(lower[lower.index(after: dot)...])
    } else {
        ""
    }
    let dimmed = lower.hasPrefix(".")

    switch ext {
    case "go":
        return FileAppearance(glyph: .go, tone: .cyan, dimmed: dimmed)
    case "proto", "sql", "graphql", "gql":
        return FileAppearance(glyph: .cube, tone: .cyan, dimmed: dimmed)
    case "swift":
        return FileAppearance(glyph: .swift, tone: .orange, dimmed: dimmed)
    case "rs", "c", "h", "cpp", "cc", "cxx", "hpp", "zig":
        return FileAppearance(glyph: .code, tone: .orange, dimmed: dimmed)
    case "ts", "tsx", "mts", "cts", "py", "pyi", "java", "kt", "kts", "scala", "cs", "dart", "vue", "svelte":
        return FileAppearance(glyph: .code, tone: .blue, dimmed: dimmed)
    case "css", "scss", "sass", "less":
        return FileAppearance(glyph: .braces, tone: .blue, dimmed: dimmed)
    case "js", "mjs", "cjs", "jsx", "json", "jsonc", "json5":
        return FileAppearance(glyph: .braces, tone: .amber, dimmed: dimmed)
    case "yaml", "yml", "toml", "ini", "conf", "cfg", "plist", "xcconfig", "entitlements", "xml":
        return FileAppearance(glyph: .sliders, tone: .purple, dimmed: dimmed)
    case "md", "markdown", "mdx", "rst":
        return FileAppearance(glyph: .markdown, tone: .doc, dimmed: dimmed)
    case "html", "htm":
        return FileAppearance(glyph: .page, tone: .orange, dimmed: dimmed)
    case "sh", "bash", "zsh", "fish", "ps1", "bat", "cmd":
        return FileAppearance(glyph: .terminal, tone: .green, dimmed: dimmed)
    case "svg", "png", "jpg", "jpeg", "gif", "webp", "ico", "heic", "bmp", "tiff", "tif", "avif":
        return FileAppearance(glyph: .image, tone: .purple, dimmed: dimmed)
    case "pdf":
        return FileAppearance(glyph: .pdf, tone: .red, dimmed: dimmed)
    case "rb", "erb":
        return FileAppearance(glyph: .code, tone: .red, dimmed: dimmed)
    default:
        return FileAppearance(glyph: dimmed ? .sliders : .doc, tone: .slate, dimmed: dimmed)
    }
}

/// Splits a filename into primary stem, secondary test/role suffix, and muted extension
/// so `stem + sub + ext == name` always holds.
public func splitFileName(_ name: String, dir: Bool = false) -> FileNameParts {
    if dir { return FileNameParts(stem: name, sub: "", ext: "") }
    let ns = name as NSString
    let full = NSRange(location: 0, length: ns.length)
    if let m = splitTestPattern.firstMatch(in: name, range: full) ?? splitPascalTestPattern.firstMatch(in: name, range: full), m.range(at: 1).length > 0 {
        return FileNameParts(
            stem: ns.substring(with: m.range(at: 1)),
            sub: ns.substring(with: m.range(at: 2)),
            ext: ns.substring(with: m.range(at: 3))
        )
    }
    if let dot = name.lastIndex(of: "."), dot > name.startIndex, name.index(after: dot) < name.endIndex {
        return FileNameParts(stem: String(name[..<dot]), sub: "", ext: String(name[dot...]))
    }
    return FileNameParts(stem: name, sub: "", ext: "")
}
