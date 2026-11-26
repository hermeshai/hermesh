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
