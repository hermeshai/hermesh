import Foundation
import HermeshProto
import Testing
import UserNotifications
@testable import HermeshSessions

@Test func aQuestionNoticeReadsAsTheAppSaysIt() {
    var n = Notice()
    n.kind = "question"
    n.sessionID = "s1"
    n.title = "选颜色"
    n.agent = "claude"
    var item = Hermesh_Session_V1_QuestionItem()
    item.id = "choice"
    item.text = "Which color?"
    var red = Hermesh_Session_V1_QuestionOption()
    red.label = "Red"
    item.options = [red]
    var note = Hermesh_Session_V1_QuestionItem()
    note.id = "note"
    note.optional = true
    n.question.questionID = "q1"
    n.question.items = [item, note]
    let c = UNMutableNotificationContent()
    NoticeText.fill(c, with: n)
    #expect(c.title == "Claude Code asks" && c.subtitle == "选颜色" && c.body.hasPrefix("Which color?"))
    #expect(c.threadIdentifier == "s1" && c.categoryIdentifier == "question" && c.interruptionLevel == .timeSensitive)
    #expect(c.userInfo["session"] as? String == "s1" && c.userInfo["question"] as? String == "q1" && c.userInfo["item"] as? String == "choice")
    #expect(c.userInfo["options"] as? [String] == ["Red"])
    #expect(NoticeText.quickAnswerable(n.question))
    n.question.items[1].optional = false
    #expect(!NoticeText.quickAnswerable(n.question))
}

@Test func otherNotices() {
    func filled(_ edit: (inout Notice) -> Void) -> UNMutableNotificationContent {
        var n = Notice()
        n.title = "deploy"
        edit(&n)
        let c = UNMutableNotificationContent()
        NoticeText.fill(c, with: n)
        return c
    }
    let f = filled { $0.kind = "failure"; $0.failure = .rateLimit; $0.grant = "bot"; $0.project = "app" }
    #expect(f.title == "“deploy” failed" && f.body == "Rate limited · bot · app")
    let d = filled { $0.kind = "turn_done"; $0.turn = 2; $0.diffStat.filesChanged = 3; $0.diffStat.insertions = 10; $0.diffStat.deletions = 1 }
    #expect(d.title == "“deploy”: turn 2 done" && d.body == "3 files +10 −1")
    let s = filled { $0.kind = "session_started"; $0.sessionID = "s2"; $0.grant = "Grok"; $0.machine = "MacBook Pro"; $0.project = "app"; $0.agent = "claude" }
    #expect(s.title == "deploy" && s.body == "Grok started it · MacBook Pro\napp · Claude Code" && s.threadIdentifier == "s2")
    let p = filled { $0.kind = "pairing"; $0.pairingKind = .client; $0.pairingLabel = "Cursor"; $0.verificationCode = "AAAA-BBBB-CCCC"; $0.pairingCode = "X" }
    #expect(p.title == "“Cursor” wants to connect to your computer" && p.body.contains("AAAA-BBBB-CCCC") && p.userInfo["pairing"] as? String == "X")
}
