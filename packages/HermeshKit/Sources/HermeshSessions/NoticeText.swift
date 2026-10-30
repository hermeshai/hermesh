import Foundation
import HermeshProto
import UserNotifications

public typealias Notice = Hermesh_Session_V1_Notice

/// A notice's words on the lock screen, as the app says them. The relay
/// and Apple carry only the notice's ciphertext; the notification extension
/// opens it and fills the notification with this.
public enum NoticeText {
    public static func fill(_ c: UNMutableNotificationContent, with n: Notice) {
        var info = c.userInfo
        info["session"] = n.sessionID
        c.threadIdentifier = n.kind == "pairing" ? "pairing" : n.sessionID
        switch n.kind {
        case "question":
            c.title = String(localized: "\(agentTitle(n.agent)) asks", bundle: .module)
            c.subtitle = n.title
            c.body = n.question.items.map(\.text).joined(separator: "\n")
            c.categoryIdentifier = "question"
            c.interruptionLevel = .timeSensitive
            info["question"] = n.question.questionID
            info["item"] = n.question.items.first?.id ?? ""
            info["options"] = n.question.items.first?.options.map(\.label) ?? []
        case "failure":
            c.title = String(localized: "“\(n.title)” failed", bundle: .module)
            c.body = [failureTitle(n.failure), n.grant, n.project].filter { !$0.isEmpty }.joined(separator: " · ")
        case "turn_done":
            c.title = String(localized: "“\(n.title)”: turn \(Int(n.turn)) done", bundle: .module)
            let files = Int(n.diffStat.filesChanged)
            c.body = files == 0 ? String(localized: "No file changes", bundle: .module)
                : String(localized: "\(Int(n.diffStat.filesChanged)) files +\(Int(n.diffStat.insertions)) −\(Int(n.diffStat.deletions))", bundle: .module)
        case "session_started":
            c.title = n.title
            let by = n.grant.isEmpty ? n.machine : String(localized: "\(n.grant) started it · \(n.machine)", bundle: .module)
            c.body = [by, [n.project, agentTitle(n.agent)].filter { !$0.isEmpty }.joined(separator: " · ")].filter { !$0.isEmpty }.joined(separator: "\n")
        case "pairing":
            switch n.pairingKind {
            case .device: c.title = String(localized: "“\(n.pairingLabel)” wants to become your trusted device", bundle: .module)
            case .machine: c.title = String(localized: "A machine calling itself “\(n.pairingLabel)” wants to join", bundle: .module)
            default: c.title = String(localized: "“\(n.pairingLabel)” wants to connect to your computer", bundle: .module)
            }
            c.body = String(localized: "Verification code \(n.verificationCode). Open Hermesh to compare it and approve; if you don’t know it, deny it.", bundle: .module)
            c.interruptionLevel = .timeSensitive
            info["pairing"] = n.pairingCode
        default:
            break
        }
        c.userInfo = info
    }

    /// A question one tap answers from the notification: its first item is a
    /// single choice, and the rest may be left out.
    public static func quickAnswerable(_ q: Question) -> Bool {
        guard let item = q.items.first else { return false }
        return !item.options.isEmpty && !item.multiSelect && !item.secret && q.items.dropFirst().allSatisfy(\.optional)
    }
}
