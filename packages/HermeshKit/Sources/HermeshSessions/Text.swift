import Foundation
import HermeshProto

// Words and times for sessions. A port of @hermesh/core's text.ts and
// calendar.ts.

public typealias FailureKind = Hermesh_Session_V1_FailureKind
public typealias Failure = Hermesh_Session_V1_Failure

/// "09:05", on a 24-hour clock.
public func hourMinute(_ d: Date, calendar: Calendar = .current) -> String {
    let c = calendar.dateComponents([.hour, .minute], from: d)
    return String(format: "%02d:%02d", c.hour ?? 0, c.minute ?? 0)
}

/// "Oct 3" / "10月3日".
public func monthDay(_ d: Date) -> String { d.formatted(.dateTime.month(.abbreviated).day()) }

/// "Mon" / "周一".
public func weekdayName(_ d: Date) -> String { d.formatted(.dateTime.weekday(.abbreviated)) }

/// When something last changed: "15:42" today, "Yesterday", a weekday within the week, else the date.
public func shortTime(_ d: Date, now: Date, calendar: Calendar = .current) -> String {
    if calendar.isDate(d, inSameDayAs: now) { return hourMinute(d, calendar: calendar) }
    if isYesterday(d, now: now, calendar: calendar) { return String(localized: "Yesterday", bundle: .module) }
    if withinWeek(d, now: now, calendar: calendar) { return weekdayName(d) }
    return monthDay(d)
}

/// How long something has been running (seconds): "4:12", "1:02:03".
public func clockText(_ seconds: Double) -> String {
    let s = max(0, Int(seconds))
    let m = s / 60
    return m >= 60 ? String(format: "%d:%02d:%02d", m / 60, m % 60, s % 60) : String(format: "%d:%02d", m, s % 60)
}

/// "just now", "12 min ago", "3 hr ago", "Yesterday 14:20", "Oct 15".
public func agoText(_ d: Date, now: Date, calendar: Calendar = .current) -> String {
    let s = now.timeIntervalSince(d)
    if s < 60 { return String(localized: "just now", bundle: .module) }
    if s < 3600 { return String(localized: "\(Int(s / 60)) min ago", bundle: .module) }
    if calendar.isDate(d, inSameDayAs: now) { return String(localized: "\(Int(s / 3600)) hr ago", bundle: .module) }
    if isYesterday(d, now: now, calendar: calendar) { return String(localized: "Yesterday \(hourMinute(d, calendar: calendar))", bundle: .module) }
    return monthDay(d)
}

/// The session list's time: "just now", "12 min ago" today, then "Yesterday", a weekday, or the date.
public func listTime(_ d: Date, now: Date, calendar: Calendar = .current) -> String {
    calendar.isDate(d, inSameDayAs: now) ? agoText(d, now: now, calendar: calendar) : shortTime(d, now: now, calendar: calendar)
}

/// 412K, 1.8M.
public func tokensText(_ n: UInt64) -> String {
    let v = Double(n)
    if n >= 1_000_000 { return String(format: "%.1fM", v / 1e6) }
    return n >= 1000 ? "\(n / 1000)K" : "\(n)"
}

/// "22 min left" until a question expires.
public func minutesLeft(until: Date, now: Date) -> String {
    let m = max(1, Int((until.timeIntervalSince(now) / 60).rounded(.up)))
    return String(localized: "\(m) min left", bundle: .module)
}

public func statusTitle(_ s: SessionStatus) -> String {
    switch s {
    case .queued: String(localized: "Queued", bundle: .module)
    case .preparing: String(localized: "Preparing", bundle: .module)
    case .running: String(localized: "Running", bundle: .module)
    case .idle: String(localized: "Idle", bundle: .module)
    case .archived: String(localized: "Archived", bundle: .module)
    case .crashed: String(localized: "Crashed", bundle: .module)
    case .waitingInput: String(localized: "Waiting for you", bundle: .module)
    default: String(localized: "State unknown", bundle: .module) // its machine cannot be read now
    }
}

public func failureTitle(_ k: FailureKind) -> String {
    switch k {
    case .auth: String(localized: "Not signed in", bundle: .module)
    case .quota: String(localized: "Out of quota", bundle: .module)
    case .rateLimit: String(localized: "Rate limited", bundle: .module)
    case .providerError: String(localized: "Model service error", bundle: .module)
    case .network: String(localized: "Network error", bundle: .module)
    case .contextOverflow: String(localized: "Context is full", bundle: .module)
    case .modelUnavailable: String(localized: "Model unavailable", bundle: .module)
    case .invalidRequest: String(localized: "Request rejected", bundle: .module)
    case .refused: String(localized: "The model refused", bundle: .module)
    case .limitReached: String(localized: "Step limit reached", bundle: .module)
    case .resumeRejected: String(localized: "Can’t resume the session", bundle: .module)
    case .agentCrashed: String(localized: "The agent quit unexpectedly", bundle: .module)
    case .stalled: String(localized: "No progress for a long time", bundle: .module)
    case .timeout: String(localized: "Took too long", bundle: .module)
    case .startFailed: String(localized: "Couldn’t start the agent", bundle: .module)
    default: String(localized: "Failed", bundle: .module)
    }
}

/// What to do about a failure; the daemon's hint when there is no advice of our own.
public func failureAdvice(_ f: Failure) -> String {
    switch f.kind {
    case .auth: String(localized: "Sign in by running the coding agent’s CLI once on that machine, as the same user.", bundle: .module)
    case .quota: String(localized: "Try again once the quota resets, or use another agent or model.", bundle: .module)
    case .rateLimit, .providerError, .network: String(localized: "A temporary problem: sending another message later will do.", bundle: .module)
    case .contextOverflow, .resumeRejected: String(localized: "The next message continues in a new context; the changes in the worktree are all there.", bundle: .module)
    case .modelUnavailable: String(localized: "Start a new session with another model.", bundle: .module)
    case .limitReached: String(localized: "Say “continue” to let it go on.", bundle: .module)
    case .agentCrashed, .stalled: String(localized: "The next message resumes the session.", bundle: .module)
    case .startFailed: String(localized: "Open that machine’s page in Hermesh Settings to see what keeps it from starting agents.", bundle: .module)
    default: f.hint
    }
}

/// Shortens what a tool call shows: paths inside the session's worktree are
/// made relative to it, and a command run through `sh -lc '…'` is shown as
/// the command.
public func toolSummary(_ s: String, sessionID: String) -> String {
    var out = s
    if !sessionID.isEmpty, let re = try? NSRegularExpression(pattern: #"\S*/"# + NSRegularExpression.escapedPattern(for: sessionID) + "/") {
        out = re.stringByReplacingMatches(in: out, range: NSRange(out.startIndex..., in: out), withTemplate: "")
    }
    if let re = try? NSRegularExpression(pattern: #"^(?:/bin/|/usr/bin/)?(?:ba|z)?sh -l?c '(.*)'$"#, options: [.dotMatchesLineSeparators]) {
        out = re.stringByReplacingMatches(in: out, range: NSRange(out.startIndex..., in: out), withTemplate: "$1")
    }
    return out
}

/// What a session is doing now, in a few words: "Editing store_test.go",
/// "Running go test ./...", or its state.
public func phaseText(_ s: Session) -> String {
    switch s.status {
    case .queued: return String(localized: "Queued", bundle: .module)
    case .preparing: return String(localized: "Preparing the worktree…", bundle: .module)
    case .waitingInput: return String(localized: "Waiting for you", bundle: .module)
    case .running: break
    default: return statusTitle(s.status)
    }
    let tool = s.progress.currentTool
    if tool.isEmpty { return String(localized: "Thinking", bundle: .module) }
    let name = String(tool.drop { $0 == ":" })
    let parts: [String]
    if let i = name.firstIndex(of: ":") {
        parts = [String(name[..<i]), String(name[name.index(after: i)...])].filter { !$0.isEmpty }.map { $0.trimmingCharacters(in: .whitespaces) }
    } else {
        parts = [name].filter { !$0.isEmpty }.map { $0.trimmingCharacters(in: .whitespaces) }
    }
    let summary = toolSummary(parts.count > 1 ? parts[1] : parts.first ?? "", sessionID: s.sessionID)
    switch s.progress.phase {
    case "editing": return String(localized: "Editing \(summary)", bundle: .module)
    case "running_shell": return String(localized: "Running \(summary)", bundle: .module)
    default: return parts.count > 1 ? "\(parts[0]) \(summary)" : summary
    }
}

/// Coding agents by the id a session or grant names.
public func agentTitle(_ id: String) -> String {
    switch id {
    case "claude": "Claude Code"
    case "codex": "Codex"
    case "gemini": "Gemini CLI"
    case "cursor": "Cursor"
    case "copilot": "Copilot CLI"
    case "opencode": "OpenCode"
    case "qwen": "Qwen Code"
    case "goose": "Goose"
    case "auggie": "Augment"
    case "kimi": "Kimi"
    case "kiro": "Kiro"
    case "qoder": "Qoder"
    case "deepseek": "DeepSeek Harness"
    case "codebuddy": "CodeBuddy Code"
    case "grok": "Grok"
    default: id
    }
}

/// Items joined the way this language lists them ("a, b" / "a、b").
public func listText(_ items: [String]) -> String {
    items.joined(separator: String(localized: ", ", bundle: .module, comment: "Separates the items of a list"))
}
