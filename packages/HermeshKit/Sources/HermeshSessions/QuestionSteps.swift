import Foundation
import HermeshProto

public struct ParsedOptionLabel: Equatable, Sendable {
    public let recommended: Bool
    public let text: String

    public init(recommended: Bool, text: String) {
        self.recommended = recommended
        self.text = text
    }
}

public func parseOptionLabel(_ label: String) -> ParsedOptionLabel {
    let trimmed = label.trimmingCharacters(in: .whitespaces)
    if trimmed.range(of: #"^(?:\(Recommended\)|（推荐）)\s*"#, options: [.regularExpression, .caseInsensitive]) != nil,
       let range = label.range(of: #"^\s*(?:\(Recommended\)|（推荐）)\s*"#, options: [.regularExpression, .caseInsensitive]) {
        return ParsedOptionLabel(recommended: true, text: String(label[range.upperBound...]))
    }
    return ParsedOptionLabel(recommended: false, text: label)
}

public func isQuestionItemAnswered(
    _ item: Hermesh_Session_V1_QuestionItem,
    picked: [String: Set<String>],
    text: [String: String]
) -> Bool {
    !(picked[item.id] ?? []).isEmpty || !(text[item.id] ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
}

public func isQuestionReady(
    _ items: [Hermesh_Session_V1_QuestionItem],
    picked: [String: Set<String>],
    text: [String: String]
) -> Bool {
    items.allSatisfy { $0.optional || isQuestionItemAnswered($0, picked: picked, text: text) }
}

public func initialQuestionStep(
    _ items: [Hermesh_Session_V1_QuestionItem],
    picked: [String: Set<String>],
    text: [String: String]
) -> Int {
    guard items.count > 1 else { return 0 }
    let hasRequired = items.contains { !$0.optional }
    return items.firstIndex { it in
        hasRequired ? (!it.optional && !isQuestionItemAnswered(it, picked: picked, text: text)) : !isQuestionItemAnswered(it, picked: picked, text: text)
    } ?? 0
}

/// Next unanswered item to auto-advance into after a single-choice tap; nil when ready to review/submit.
public func nextAutoQuestionStep(
    _ items: [Hermesh_Session_V1_QuestionItem],
    picked: [String: Set<String>],
    text: [String: String],
    from: Int
) -> Int? {
    guard items.count > 1 else { return nil }
    let hasRequired = items.contains { !$0.optional }
    for offset in 1..<items.count {
        let idx = (from + offset) % items.count
        let it = items[idx]
        if hasRequired ? (!it.optional && !isQuestionItemAnswered(it, picked: picked, text: text)) : !isQuestionItemAnswered(it, picked: picked, text: text) {
            return idx
        }
    }
    return nil
}

/// Target step for Continue / Skip when advancing manually from multi-select, text, or optional items.
public func nextManualQuestionStep(
    _ items: [Hermesh_Session_V1_QuestionItem],
    picked: [String: Set<String>],
    text: [String: String],
    from: Int
) -> Int? {
    if let auto = nextAutoQuestionStep(items, picked: picked, text: text, from: from) {
        return auto
    }
    return from + 1 < items.count ? from + 1 : nil
}

public func applyQuestionChoice(
    _ item: Hermesh_Session_V1_QuestionItem,
    label: String,
    picked: inout [String: Set<String>],
    text: inout [String: String]
) {
    if item.multiSelect {
        var cur = picked[item.id, default: []]
        if cur.contains(label) { cur.remove(label) } else { cur.insert(label) }
        picked[item.id] = cur
    } else {
        picked[item.id] = [label]
        if !(text[item.id] ?? "").isEmpty {
            text[item.id] = ""
        }
    }
}

public func applyQuestionCustomText(
    _ item: Hermesh_Session_V1_QuestionItem,
    value: String,
    picked: inout [String: Set<String>],
    text: inout [String: String]
) {
    text[item.id] = value
    if !item.multiSelect && !value.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        picked[item.id] = []
    }
}

public struct SettledQuestionRow: Equatable, Sendable {
    public enum StatusKind: Equatable, Sendable {
        case answered
        case skipped
        case declined
        case timedOut
        case withdrawn
    }

    public let prompt: String
    public let choices: [ParsedOptionLabel]
    public let status: StatusKind
    public let statusText: String

    public init(prompt: String, choices: [ParsedOptionLabel], status: StatusKind, statusText: String = "") {
        self.prompt = prompt
        self.choices = choices
        self.status = status
        self.statusText = statusText
    }
}

private func settledItemPrompt(_ it: Hermesh_Session_V1_QuestionItem) -> String {
    let header = it.header.trimmingCharacters(in: .whitespacesAndNewlines)
    let text = it.text.trimmingCharacters(in: .whitespacesAndNewlines)
    let generic = text.range(of: #"^the agent has (?:a|\d+) questions?\.?$"#, options: [.regularExpression, .caseInsensitive]) != nil
    if !header.isEmpty && (text.isEmpty || generic) { return header }
    if !header.isEmpty && header != text { return "\(header) · \(text)" }
    return text.isEmpty ? header : text
}

public func settledQuestionRows(
    _ question: Question,
    resolution r: QuestionResolved,
    name: (String) -> String = { $0 }
) -> [SettledQuestionRow] {
    let you = r.by.isEmpty || r.by == "owner"
    let items = question.items
    if r.resolution != .answered {
        let (kind, note): (SettledQuestionRow.StatusKind, String) = switch r.resolution {
        case .declined:
            (.declined, you ? String(localized: "You chose not to answer", bundle: .module) : String(localized: "\(name(r.by)) chose not to answer", bundle: .module))
        case .timedOut:
            (.timedOut, String(localized: "No one answered; the agent went on by itself", bundle: .module))
        default:
            (.withdrawn, String(localized: "The question was withdrawn", bundle: .module))
        }
        let prompts = items.map(settledItemPrompt).filter { !$0.isEmpty }
        if prompts.isEmpty {
            return [SettledQuestionRow(prompt: "", choices: [], status: kind, statusText: note)]
        }
        return prompts.enumerated().map { idx, p in
            SettledQuestionRow(prompt: p, choices: [], status: kind, statusText: idx == prompts.count - 1 ? note : "")
        }
    }

    if items.isEmpty {
        let fallback = r.answers.flatMap { a in
            a.selected.map(parseOptionLabel) + (a.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? [] : [ParsedOptionLabel(recommended: false, text: a.text.trimmingCharacters(in: .whitespacesAndNewlines))])
        }
        return [SettledQuestionRow(prompt: "", choices: fallback, status: fallback.isEmpty ? .skipped : .answered, statusText: fallback.isEmpty ? String(localized: "Skipped", bundle: .module) : "")]
    }

    return items.map { it in
        let prompt = settledItemPrompt(it)
        let matched = r.answers.first(where: { $0.itemID == it.id })
            ?? (items.count == 1 && r.answers.count == 1 && r.answers[0].itemID.isEmpty ? r.answers[0] : nil)
        var choices: [ParsedOptionLabel] = (matched?.selected ?? []).map(parseOptionLabel)
        let custom = (matched?.text ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        if !custom.isEmpty {
            choices.append(ParsedOptionLabel(recommended: false, text: custom))
        }
        if choices.isEmpty {
            return SettledQuestionRow(prompt: prompt, choices: [], status: .skipped, statusText: String(localized: "Skipped", bundle: .module))
        }
        return SettledQuestionRow(prompt: prompt, choices: choices, status: .answered)
    }
}
