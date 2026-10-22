import HermeshKit
import HermeshProto
import HermeshSessions
import UserNotifications

/// Opens a Hermesh notice on the phone: the relay and Apple carry only its
/// ciphertext (and a generic "something needs you", shown if this fails),
/// so this is where it gets its words. A question gets its options as
/// actions, answerable from the lock screen after unlocking.
final class NotificationService: UNNotificationServiceExtension {
    override func didReceive(_ request: UNNotificationRequest, withContentHandler contentHandler: @escaping (UNNotificationContent) -> Void) {
        guard let content = request.content.mutableCopy() as? UNMutableNotificationContent,
              let hermesh = content.userInfo["hermesh"] as? [String: Any], let n = hermesh["n"] as? String,
              let sealed = Data(base64Encoded: n), let notice = try? NoticeKey.open(sealed)
        else {
            return contentHandler(request.content)
        }
        NoticeText.fill(content, with: notice)
        guard notice.kind == "question", NoticeText.quickAnswerable(notice.question), let item = notice.question.items.first else {
            return contentHandler(content)
        }
        // This question's options as its own category's actions.
        let id = "question.\(notice.question.questionID)"
        content.categoryIdentifier = id
        let options = item.options.prefix(4).enumerated().map { i, o in
            UNNotificationAction(identifier: "option.\(i)", title: o.label, options: [.authenticationRequired])
        }
        let reply = UNTextInputNotificationAction(identifier: "reply", title: String(localized: "Answer…"), options: [.authenticationRequired],
                                                  textInputButtonTitle: String(localized: "Answer"), textInputPlaceholder: item.text)
        let category = UNNotificationCategory(identifier: id, actions: options + [reply], intentIdentifiers: [])
        let center = UNUserNotificationCenter.current()
        center.getNotificationCategories { existing in
            // Keep the static ones and the last few questions'.
            let kept = existing.filter { !$0.identifier.hasPrefix("question.") } + Array(existing.filter { $0.identifier.hasPrefix("question.") }.prefix(20))
            center.setNotificationCategories(Set(kept).union([category]))
            contentHandler(content)
        }
    }
}
