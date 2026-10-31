import Foundation
import HermeshProto
import Testing
@testable import HermeshSessions

@Test func unifiedDiffFilesHunksAndLineNumbers() {
    let text = ["diff --git a/src/a.ts b/src/a.ts", "index 1..2 100644", "--- a/src/a.ts", "+++ b/src/a.ts", "@@ -18,4 +18,5 @@ export function f() {",
                "   return {", "-    sameSite: \"none\",", "+    sameSite: \"lax\",", "+    secure: true,", "     path: \"/\",", "\\ No newline at end of file",
                "diff --git a/img.png b/img.png", "Binary files a/img.png and b/img.png differ",
                "diff --git a/new.txt b/new.txt", "new file mode 100644", "--- /dev/null", "+++ b/new.txt", "@@ -0,0 +1 @@", "+hello"].joined(separator: "\n")
    let files = parseUnifiedDiff(text)
    #expect(files.map(\.path) == ["src/a.ts", "img.png", "new.txt"])
    #expect(files[0].insertions == 2 && files[0].deletions == 1)
    let lines = files[0].hunks[0].lines
    #expect(lines.map(\.kind) == [.context, .removed, .added, .added, .context, .note])
    #expect(lines[0].old == 18 && lines[0].new == 18 && lines[1].old == 19 && lines[1].new == nil)
    #expect(lines[3].new == 20 && lines[4].old == 20 && lines[4].new == 21)
    #expect(files[1].binary && files[1].hunks.isEmpty)
    #expect(files[2].insertions == 1 && files[2].hunks[0].lines[0].new == 1)
    #expect(parseUnifiedDiff("").isEmpty)
}

@Test func usageLevelsStreakAndShares() {
    let since = usageSince(now, calendar: utc)
    func day(_ back: Int, _ tokens: UInt64, _ agent: String = "claude") -> UsageDay {
        var d = UsageDay()
        let date = utc.date(byAdding: .day, value: -back, to: now)!
        d.bucket = UInt32(utc.dateComponents([.day], from: since, to: utc.startOfDay(for: date)).day!)
        d.group.agent = agent
        d.inputTokens = tokens
        return d
    }
    let y = usageYear([day(0, 100), day(1, 200), day(2, 300, "codex"), day(5, 400), day(40, 50)], today: now, calendar: utc)
    #expect(y.cells.count == 53 * 7)
    #expect(y.cells.compactMap { $0 }.last?.key == dayKey(now, calendar: utc))
    #expect(y.today == 100 && y.active == 5 && y.streak == 3)
    #expect(y.byAgent.first?.agent == "claude" && abs(y.byAgent.first!.share - 750.0 / 1050) < 1e-9)
    #expect(y.cells.compactMap { $0 }.filter { $0.tokens == 400 }.first?.level == 4)
    #expect(utc.component(.weekday, from: usageSince(now, calendar: utc)) == 1) // a Sunday
    // Asked from that Sunday to today, by day and agent.
    let r = usageRequest(now, calendar: utc)
    #expect(r.firstDay == dayKey(since, calendar: utc) && Int(r.days) == utc.dateComponents([.day], from: since, to: utc.startOfDay(for: now)).day! + 1 && r.groupBy == [.agent])
}
