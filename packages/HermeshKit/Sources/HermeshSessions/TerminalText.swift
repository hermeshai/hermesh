import Foundation

// Pure helpers for terminal code blocks, command prompt stripping, secret masking,
// runnable shell script checks, and ANSI SGR / unified diff output parsing.

private let shellLanguages: Set<String> = [
    "bash", "sh", "zsh", "shell", "shellscript", "console", "terminal",
    "fish", "ksh", "csh", "tcsh", "powershell", "pwsh", "ps1", "cmd", "bat", "batch",
]

private let cliHeads: Set<String> = [
    "git", "npm", "pnpm", "yarn", "bun", "npx", "bunx", "node", "deno",
    "python", "python3", "pip", "pip3", "uv", "poetry", "cargo", "rustc", "rustup",
    "go", "gofmt", "make", "cmake", "docker", "docker-compose", "kubectl", "helm",
    "terraform", "aws", "gcloud", "az", "gh", "brew", "apt", "apt-get", "dnf", "pacman",
    "systemctl", "journalctl", "launchctl", "curl", "wget", "ssh", "scp", "rsync",
    "tar", "unzip", "grep", "rg", "find", "fd", "jq", "sed", "awk", "chmod", "chown",
    "mkdir", "rm", "cp", "mv", "ls", "cd", "cat", "head", "tail", "echo", "printf",
    "export", "source", "which", "ps", "kill", "pkill", "lsof", "swift", "xcodebuild",
    "playwright", "vitest", "jest", "pytest", "hermesh", "hermeshd", "claude", "codex",
]

private let interactiveTUIs: Set<String> = [
    "vim", "nvim", "vi", "nano", "emacs", "htop", "top", "btop",
    "less", "more", "man", "tmux", "screen", "ssh", "sftp", "ftp", "telnet",
]

private let bareREPLs: Set<String> = [
    "python", "python3", "node", "deno", "bun", "irb", "pry",
    "psql", "mysql", "sqlite3", "bash", "zsh", "sh", "fish",
]

public struct EffectiveCodeLang: Equatable, Sendable {
    public let lang: String
    public let shell: Bool
    public let sniffed: Bool

    public init(lang: String, shell: Bool, sniffed: Bool) {
        self.lang = lang
        self.shell = shell
        self.sniffed = sniffed
    }
}

public struct RunnableShellPlan: Equatable, Sendable {
    public let command: String
    public let destructive: Bool

    public init(command: String, destructive: Bool) {
        self.command = command
        self.destructive = destructive
    }
}

public func isShellLanguage(_ lang: String) -> Bool {
    shellLanguages.contains(lang.trimmingCharacters(in: .whitespacesAndNewlines).lowercased())
}

/// Whether text holds characters that do not show but change what runs or reads:
/// controls (except tab/newline/CR), zero-width chars, and bidirectional overrides.
public func hiddenChars(_ text: String) -> Bool {
    text.unicodeScalars.contains { u in
        let v = u.value
        return (v <= 0x0008)
            || v == 0x000B || v == 0x000C
            || (0x000E ... 0x001F).contains(v)
            || v == 0x007F
            || (0x200B ... 0x200F).contains(v)
            || (0x202A ... 0x202E).contains(v)
            || (0x2060 ... 0x2064).contains(v)
            || (0x2066 ... 0x2069).contains(v)
            || v == 0xFEFF
    }
}

private let promptPrefixRe = try! NSRegularExpression(pattern: #"^[$%]\s+"#)
private let envAssignPrefixRe = try! NSRegularExpression(pattern: #"^[A-Za-z_][A-Za-z0-9_]*=\S*\s+"#)
private let wrapperPrefixRe = try! NSRegularExpression(pattern: #"^(?:sudo|env|command|time|nohup)\s+(?:-\S+\s+)*"#)
private let hasPromptLineRe = try! NSRegularExpression(pattern: #"^[$%]\s+[A-Za-z0-9_./~-]"#)
private let proseFollowerRe = try! NSRegularExpression(pattern: #"^(?:sure|to|the|a|an|it|this|that|your|our)\b"#, options: [.caseInsensitive])
private let shellPunctRe = try! NSRegularExpression(pattern: #"[-|>&;$=]"#)
private let promptExtractRe = try! NSRegularExpression(pattern: #"^\s*[$%]\s+(.*)$"#)

private func matches(_ re: NSRegularExpression, in text: String) -> Bool {
    re.firstMatch(in: text, range: NSRange(text.startIndex..., in: text)) != nil
}

private func stripLeading(_ re: NSRegularExpression, from text: String) -> String {
    guard let m = re.firstMatch(in: text, range: NSRange(text.startIndex..., in: text)),
          let r = Range(m.range, in: text) else { return text }
    return String(text[r.upperBound...])
}

private func stripCommandWrappers(_ line: String) -> String {
    var s = line.trimmingCharacters(in: .whitespacesAndNewlines)
    s = stripLeading(promptPrefixRe, from: s)
    while matches(envAssignPrefixRe, in: s) {
        s = stripLeading(envAssignPrefixRe, from: s)
    }
    while matches(wrapperPrefixRe, in: s) {
        s = stripLeading(wrapperPrefixRe, from: s)
    }
    return s
}

private func firstCommandToken(_ line: String) -> String {
    let s = stripCommandWrappers(line)
    let delimiters = CharacterSet.whitespacesAndNewlines.union(CharacterSet(charactersIn: "|;&()<>"))
    let tok = s.components(separatedBy: delimiters).first ?? ""
    if let slash = tok.lastIndex(of: "/") {
        return String(tok[tok.index(after: slash)...])
    }
    return tok
}

/// Sniffs untagged or `text` fences that clearly contain CLI commands.
public func sniffShellCommand(_ text: String) -> Bool {
    let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
    guard let firstChar = trimmed.first, !["{", "[", "<"].contains(firstChar) else { return false }
    let lines = trimmed.replacingOccurrences(of: "\r\n", with: "\n").components(separatedBy: "\n")
    if lines.count > 12 { return false }
    let nonEmpty = lines.map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty && !$0.hasPrefix("#") }
    guard let first = nonEmpty.first else { return false }
    let hasPrompt = matches(hasPromptLineRe, in: first)
    let head = firstCommandToken(first)
    if !hasPrompt && !cliHeads.contains(head) { return false }
    if !hasPrompt && ["make", "go", "find", "time", "more"].contains(head) {
        let rest = String(first.dropFirst(head.count)).trimmingCharacters(in: .whitespaces)
        if rest.isEmpty || (matches(proseFollowerRe, in: rest) && !matches(shellPunctRe, in: first)) {
            return false
        }
    }
    return true
}

public func effectiveCodeLang(lang: String, text: String) -> EffectiveCodeLang {
    let norm = lang.trimmingCharacters(in: .whitespacesAndNewlines)
    if isShellLanguage(norm) {
        return EffectiveCodeLang(lang: norm, shell: true, sniffed: false)
    }
    let lower = norm.lowercased()
    if (lower.isEmpty || lower == "text" || lower == "plaintext") && sniffShellCommand(text) {
        return EffectiveCodeLang(lang: "bash", shell: true, sniffed: true)
    }
    return EffectiveCodeLang(lang: norm, shell: false, sniffed: false)
}

private func trimTrailingSpaces(_ s: String) -> String {
    var end = s.endIndex
    while end > s.startIndex {
        let prev = s.index(before: end)
        if s[prev] == " " || s[prev] == "\t" || s[prev] == "\r" {
            end = prev
        } else {
            break
        }
    }
    return String(s[..<end])
}

/// Strips leading `$ ` / `% ` shell prompts when copying or running a shell block.
/// If prompt lines are mixed with sample output lines (as in `console` blocks), keeps only the commands.
public func cleanShellCommand(_ text: String) -> String {
    let rawLines = text.replacingOccurrences(of: "\r\n", with: "\n").components(separatedBy: "\n")
    let hasAnyPrompt = rawLines.contains { matches(promptExtractRe, in: $0) }
    if !hasAnyPrompt {
        return text.trimmingCharacters(in: .whitespacesAndNewlines)
    }
    var out: [String] = []
    var continuing = false
    for line in rawLines {
        if continuing {
            let trimmed = trimTrailingSpaces(line)
            out.append(trimmed)
            continuing = trimmed.hasSuffix("\\")
            continue
        }
        if let m = promptExtractRe.firstMatch(in: line, range: NSRange(line.startIndex..., in: line)),
           let r = Range(m.range(at: 1), in: line) {
            let cmd = trimTrailingSpaces(String(line[r]))
            out.append(cmd)
            continuing = cmd.hasSuffix("\\")
        }
    }
    return out.joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines)
}

// Each pattern captures the secret token itself in capture group 1.
private let secretPatterns: [NSRegularExpression] = [
    try! NSRegularExpression(pattern: #"\b(sk-(?:ant-|proj-|live-|test-)?[A-Za-z0-9_-]{16,})\b"#),
    try! NSRegularExpression(pattern: #"\b((?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,})\b"#),
    try! NSRegularExpression(pattern: #"\b(github_pat_[A-Za-z0-9_]{22,})\b"#),
    try! NSRegularExpression(pattern: #"\b(AKIA[0-9A-Z]{16})\b"#),
    try! NSRegularExpression(pattern: #"\b(xox[baprs]-[A-Za-z0-9-]{16,})\b"#),
    try! NSRegularExpression(pattern: #"\b(AIza[0-9A-Za-z_-]{32,})\b"#),
    try! NSRegularExpression(pattern: #"\bBearer\s+([A-Za-z0-9._~+/=-]{20,})"#),
    try! NSRegularExpression(pattern: #"\b[A-Z0-9_]*(?:API_KEY|SECRET_KEY|ACCESS_TOKEN|AUTH_TOKEN|PRIVATE_KEY|CLIENT_SECRET)\s*=\s*["']?([A-Za-z0-9._~+/=-]{12,})"#),
]

public func findSecretRanges(_ text: String) -> [Range<String.Index>] {
    let fullRange = NSRange(text.startIndex..., in: text)
    var nsRanges: [NSRange] = []
    for re in secretPatterns {
        for m in re.matches(in: text, range: fullRange) {
            let r = m.range(at: 1)
            if r.location != NSNotFound && r.length > 0 {
                nsRanges.append(r)
            }
        }
    }
    if nsRanges.isEmpty { return [] }
    nsRanges.sort { a, b in
        a.location != b.location ? a.location < b.location : a.length < b.length
    }
    var merged: [NSRange] = [nsRanges[0]]
    for cur in nsRanges.dropFirst() {
        let prev = merged[merged.count - 1]
        let prevEnd = prev.location + prev.length
        let curEnd = cur.location + cur.length
        if cur.location <= prevEnd {
            merged[merged.count - 1] = NSRange(location: prev.location, length: max(prevEnd, curEnd) - prev.location)
        } else {
            merged.append(cur)
        }
    }
    return merged.compactMap { Range($0, in: text) }
}

public func hasSecrets(_ text: String) -> Bool {
    !findSecretRanges(text).isEmpty
}

public func maskSecrets(_ text: String) -> String {
    let ranges = findSecretRanges(text)
    if ranges.isEmpty { return text }
    var out = ""
    var pos = text.startIndex
    for r in ranges {
        out += text[pos ..< r.lowerBound]
        let raw = text[r]
        let keep = min(4, max(2, raw.count / 5))
        out += raw.prefix(keep) + "••••••••"
        pos = r.upperBound
    }
    out += text[pos...]
    return out
}

private let destructivePatterns: [NSRegularExpression] = [
    try! NSRegularExpression(pattern: #"\brm\s+(?:-[A-Za-z]*[rf][A-Za-z]*\b|--recursive\b|--force\b)"#),
    try! NSRegularExpression(pattern: #"(?:^|[;&|\n]\s*)sudo\s+"#),
    try! NSRegularExpression(pattern: #"\bgit\s+(?:reset\s+--hard\b|clean\s+-[A-Za-z]*f|push\s+(?:--force|-f)\b|checkout\s+--\s*\.)"#),
    try! NSRegularExpression(pattern: #"\b(?:mkfs(?:\.\w+)?|fdisk|dd\s+if=|diskutil\s+erase\w*)\b"#),
    try! NSRegularExpression(pattern: #"\b(?:curl|wget)\b[^\n|]*\|\s*(?:sudo\s+)?(?:ba|z|k)?sh\b"#),
]

public func isDestructiveShell(_ command: String) -> Bool {
    let activeLines = command
        .components(separatedBy: "\n")
        .map { $0.trimmingCharacters(in: .whitespaces) }
        .filter { !$0.isEmpty && !$0.hasPrefix("#") }
        .joined(separator: "\n")
    return destructivePatterns.contains { matches($0, in: activeLines) }
}

/// Evaluates whether a shell block (single-line or multi-line script, including shebangs/comments)
/// can run non-interactively in the session's workspace, and whether it warrants an inline confirmation.
public func inspectRunnableShell(lang: String, text: String) -> RunnableShellPlan? {
    let eff = effectiveCodeLang(lang: lang, text: text)
    guard eff.shell, !hiddenChars(text) else { return nil }
    let clean = cleanShellCommand(text)
    guard !clean.isEmpty, clean.count <= 4000 else { return nil }

    let execLines = clean
        .components(separatedBy: "\n")
        .map { $0.trimmingCharacters(in: .whitespaces) }
        .filter { !$0.isEmpty && !$0.hasPrefix("#") }
    guard !execLines.isEmpty else { return nil }

    for line in execLines {
        let head = firstCommandToken(line)
        if head.isEmpty { continue }
        if interactiveTUIs.contains(head) { return nil }
        let withoutPrompt = stripLeading(promptPrefixRe, from: line).trimmingCharacters(in: .whitespaces)
        let rest = withoutPrompt.hasPrefix(head)
            ? String(withoutPrompt.dropFirst(head.count)).trimmingCharacters(in: .whitespaces)
            : String(stripCommandWrappers(line).dropFirst(head.count)).trimmingCharacters(in: .whitespaces)
        if bareREPLs.contains(head) && rest.isEmpty { return nil }
    }

    return RunnableShellPlan(command: clean, destructive: isDestructiveShell(clean))
}

public func runnableShellCommand(lang: String, text: String) -> String? {
    inspectRunnableShell(lang: lang, text: text)?.command
}

// MARK: - Lightweight ANSI SGR + Unified Diff Output Parser

public enum TerminalAnsiColor: Equatable, Sendable {
    case gray, red, green, yellow, blue, magenta, cyan, fg
    case rgb(UInt8, UInt8, UInt8)
}

public struct TerminalSpanStyle: Equatable, Sendable {
    public var color: TerminalAnsiColor?
    public var bold: Bool
    public var dim: Bool
    public var italic: Bool
    public var underline: Bool

    public init(
        color: TerminalAnsiColor? = nil,
        bold: Bool = false,
        dim: Bool = false,
        italic: Bool = false,
        underline: Bool = false
    ) {
        self.color = color
        self.bold = bold
        self.dim = dim
        self.italic = italic
        self.underline = underline
    }

    public var isEmpty: Bool {
        color == nil && !bold && !dim && !italic && !underline
    }
}

public struct TerminalSpan: Equatable, Sendable {
    public let text: String
    public let style: TerminalSpanStyle?

    public init(text: String, style: TerminalSpanStyle? = nil) {
        self.text = text
        self.style = style
    }
}

public enum TerminalDiffKind: String, Equatable, Sendable {
    case add, del, hunk, meta
}

public struct TerminalLine: Equatable, Sendable {
    public let diff: TerminalDiffKind?
    public let spans: [TerminalSpan]

    public init(diff: TerminalDiffKind? = nil, spans: [TerminalSpan]) {
        self.diff = diff
        self.spans = spans
    }
}

private func ansiNamedColor(_ code: Int) -> TerminalAnsiColor? {
    switch code {
    case 30, 90: .gray
    case 31, 91: .red
    case 32, 92: .green
    case 33, 93: .yellow
    case 34, 94: .blue
    case 35, 95: .magenta
    case 36, 96: .cyan
    case 37, 97: .fg
    default: nil
    }
}

private func xterm256Color(_ n: Int) -> TerminalAnsiColor? {
    guard (0 ... 255).contains(n) else { return nil }
    if n < 8 { return ansiNamedColor(30 + n) }
    if n < 16 { return ansiNamedColor(90 + (n - 8)) }
    if n >= 232 {
        let v = UInt8(clamping: 8 + (n - 232) * 10)
        return .rgb(v, v, v)
    }
    let idx = n - 16
    let levels: [UInt8] = [0, 95, 135, 175, 215, 255]
    return .rgb(levels[idx / 36], levels[(idx % 36) / 6], levels[idx % 6])
}

private let sgrOnlyRe = try! NSRegularExpression(pattern: #"\x1B\[([0-9;]*)m"#)
private let allEscapesRe = try! NSRegularExpression(
    pattern: #"\x1B\][\s\S]*?(?:\x07|\x1B\\)|\x1B\[[0-?]*[ -/]*[@-~]|\x1B[@-_]"#
)
private let nonSgrEscapesRe = try! NSRegularExpression(
    pattern: #"\x1B\][\s\S]*?(?:\x07|\x1B\\)|\x1B\[[0-?]*[ -/]*[@-ln-~]"#
)

private func visibleLength(_ s: String) -> Int {
    let stripped = sgrOnlyRe.stringByReplacingMatches(in: s, range: NSRange(s.startIndex..., in: s), withTemplate: "")
    return stripped.count
}

private func sliceAfterVisible(_ s: String, skip: Int) -> String {
    guard skip > 0 else { return s }
    var seen = 0
    var i = s.startIndex
    while i < s.endIndex && seen < skip {
        if s[i] == "\u{001B}" {
            let next = s.index(after: i)
            if next < s.endIndex && s[next] == "[" {
                var cur = s.index(after: next)
                if let mIdx = s[cur...].firstIndex(of: "m") {
                    cur = s.index(after: mIdx)
                    i = cur
                    continue
                }
            }
        }
        seen += 1
        i = s.index(after: i)
    }
    return String(s[i...])
}

private func applyCarriageReturns(_ line: String) -> String {
    guard line.contains("\r") else { return line }
    let parts = line.components(separatedBy: "\r")
    var acc = ""
    for p in parts where !p.isEmpty {
        let pVis = visibleLength(p)
        acc = pVis >= visibleLength(acc) ? p : p + sliceAfterVisible(acc, skip: pVis)
    }
    return acc
}

private func applySGR(_ codes: [Int], to current: TerminalSpanStyle) -> TerminalSpanStyle {
    if codes.isEmpty { return TerminalSpanStyle() }
    var next = current
    var i = 0
    while i < codes.count {
        let c = codes[i]
        switch c {
        case 0:
            next = TerminalSpanStyle()
        case 1:
            next.bold = true
        case 2:
            next.dim = true
        case 3:
            next.italic = true
        case 4:
            next.underline = true
        case 22:
            next.bold = false
            next.dim = false
        case 23:
            next.italic = false
        case 24:
            next.underline = false
        case 39:
            next.color = nil
        case 38:
            if i + 2 < codes.count, codes[i + 1] == 5 {
                next.color = xterm256Color(codes[i + 2])
                i += 2
            } else if i + 4 < codes.count, codes[i + 1] == 2 {
                let r = UInt8(clamping: max(0, min(255, codes[i + 2])))
                let g = UInt8(clamping: max(0, min(255, codes[i + 3])))
                let b = UInt8(clamping: max(0, min(255, codes[i + 4])))
                next.color = .rgb(r, g, b)
                i += 4
            }
        default:
            if let named = ansiNamedColor(c) {
                next.color = named
            }
        }
        i += 1
    }
    return next
}

private func looksLikeUnifiedDiff(_ lines: [String]) -> Bool {
    var markers = 0
    var addOrDel = 0
    for l in lines.prefix(80) {
        if l.hasPrefix("@@ ") || l.hasPrefix("diff --git ") || l.hasPrefix("--- ") || l.hasPrefix("+++ ") {
            markers += 1
        } else if (l.hasPrefix("+") && !l.hasPrefix("+++")) || (l.hasPrefix("-") && !l.hasPrefix("---")) {
            addOrDel += 1
        }
    }
    if markers > 0 { return true }
    guard addOrDel >= 2 else { return false }
    return lines.allSatisfy { l in
        guard let first = l.first else { return true }
        return [" ", "+", "-", "@", "\\"].contains(first)
    }
}

/// Strips all terminal escape sequences (for clipboard copy or sending to chat).
public func stripTerminalEscapes(_ raw: String) -> String {
    let normalized = raw.replacingOccurrences(of: "\r\n", with: "\n")
    let cleaned = allEscapesRe.stringByReplacingMatches(
        in: normalized,
        range: NSRange(normalized.startIndex..., in: normalized),
        withTemplate: ""
    )
    return cleaned
        .components(separatedBy: "\n")
        .map(applyCarriageReturns)
        .joined(separator: "\n")
}

/// Parses raw command/diff output into styled lines (ANSI SGR + unified diff fallback).
public func parseTerminalLines(_ raw: String) -> [TerminalLine] {
    let normalized = raw.replacingOccurrences(of: "\r\n", with: "\n")
    let csiOnly = nonSgrEscapesRe.stringByReplacingMatches(
        in: normalized,
        range: NSRange(normalized.startIndex..., in: normalized),
        withTemplate: ""
    )
    let rawLines = csiOnly.components(separatedBy: "\n").map(applyCarriageReturns)
    let hasAnsi = csiOnly.contains("\u{001B}[")
    let isDiff = !hasAnsi && looksLikeUnifiedDiff(rawLines)

    var style = TerminalSpanStyle()

    return rawLines.map { line in
        if !hasAnsi {
            var diff: TerminalDiffKind?
            if isDiff {
                if line.hasPrefix("@@ ") { diff = .hunk }
                else if line.hasPrefix("+++ ") || line.hasPrefix("--- ") || line.hasPrefix("diff --git ") { diff = .meta }
                else if line.hasPrefix("+") { diff = .add }
                else if line.hasPrefix("-") { diff = .del }
            }
            return TerminalLine(diff: diff, spans: [TerminalSpan(text: line)])
        }

        var spans: [TerminalSpan] = []
        var last = line.startIndex
        let matches = sgrOnlyRe.matches(in: line, range: NSRange(line.startIndex..., in: line))
        for m in matches {
            guard let fullRange = Range(m.range, in: line) else { continue }
            if fullRange.lowerBound > last {
                let chunk = String(line[last ..< fullRange.lowerBound])
                spans.append(TerminalSpan(text: chunk, style: style.isEmpty ? nil : style))
            }
            let rawCodes: String
            if let r1 = Range(m.range(at: 1), in: line) {
                rawCodes = String(line[r1])
            } else {
                rawCodes = ""
            }
            let codes: [Int] = rawCodes.isEmpty
                ? [0]
                : rawCodes.components(separatedBy: ";").map { $0.isEmpty ? 0 : (Int($0) ?? 0) }
            style = applySGR(codes, to: style)
            last = fullRange.upperBound
        }
        if last < line.endIndex || spans.isEmpty {
            let chunk = String(line[last...])
            spans.append(TerminalSpan(text: chunk, style: style.isEmpty ? nil : style))
        }
        return TerminalLine(spans: spans)
    }
}
