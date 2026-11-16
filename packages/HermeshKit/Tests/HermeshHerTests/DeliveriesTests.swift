import Foundation
import Testing
@testable import HermeshHer

// The cases her-client runs too (packages/her-client/testdata/deliveries.json), so both apps read the list of the
// files Her sent the same way.
private let shared: JSON = {
    let url = URL(fileURLWithPath: #filePath).deletingLastPathComponent().appendingPathComponent("../../../her-client/testdata/deliveries.json")
    return try! JSON.parse(Data(contentsOf: url))
}()
private let page = HerDeliveries(shared["page"])!
private let now = herDate(shared["now"].string!)!
private let zone = TimeZone(secondsFromGMT: shared["offset_minutes"].int! * 60)!
private func file(_ name: String) -> HerDeliveredFile { page.files.first { $0.name == name }! }
private func named(_ groups: [HerDeliveryGroup]) -> [[String]] { groups.map { $0.files.map(\.name) } }
private func expected(_ key: String) -> [JSON] { shared[key].array! }

@Test func theListGroupsByTheDayEachFileWasLastSentInLocalTime() {
    let groups = herDeliveryGroups(page.files, by: .time, now: now, zone: zone)
    #expect(groups.map(\.key) == expected("by_time").map { $0["key"].string! })
    #expect(named(groups) == expected("by_time").map { $0["files"].array!.map { $0.string! } })
    let late = herDate("2026-11-15T16:05:00Z")!
    #expect(herDeliveryDay(late, now: now, zone: TimeZone(secondsFromGMT: 0)!) == .yesterday)
    #expect(herDeliveryDay(late, now: now, zone: zone) == .today)
}

@Test func theListGroupsByWhatEachFileCameFrom() {
    let groups = herDeliveryGroups(page.files, by: .source, now: now, zone: zone)
    #expect(groups.map(\.key) == expected("by_source").map { $0["key"].string! })
    #expect(groups.map { $0.source?.name } == expected("by_source").map { $0["name"].string })
    #expect(named(groups) == expected("by_source").map { $0["files"].array!.map { $0.string! } })
}

@Test func aFileIsNewWhileItsLatestSendIsAfterWhatTheOwnerSaw() {
    #expect(page.files.filter { $0.isNew(seen: page.seen) }.map(\.name) == expected("new").map { $0.string! })
    #expect(page.next == 2 && page.seen == 4)
}

@Test func aVersionIsReadFromItsCopyAndFromWhereTheFileIsWithoutOne() {
    for c in expected("read") {
        let f = file(c["file"].string!)
        let read = f.file(f.versions[c["version"].int!])
        #expect(read.path == c["path"].string! && read.snapshot == c["snapshot"].string)
        #expect(read.source == (c["snapshot"].string ?? c["path"].string!))
    }
}

@Test func aReplyToAFileQuotesItsMessageByItsNameAndHersWords() {
    for c in expected("quotes") {
        let f = file(c["file"].string!)
        let q = c["quote"]
        #expect(f.quote(f.versions[c["version"].int!]) == HerReply(ref: q["ref"].string!, own: q["own"].bool!, text: q["text"].string!, partial: q["partial"].bool!))
    }
}

@Test func pagesJoinInOrderAFileOnce() {
    let joined = page.joined(HerDeliveries(shared["next_page"])!)
    #expect(joined.files.map(\.name) == expected("joined").map { $0.string! })
    #expect(joined.next == nil)
    let pic = joined.files.last!
    #expect(pic.kind == .image && pic.latest?.width == 3 && pic.latest?.snapshot == nil)
}

@Test func herRowSaysHowManyFilesAreNew() {
    let bot = HerBot(["id": "her", "new_deliveries": 2, "deliveries_latest": 9])!
    #expect(bot.newDeliveries == 2 && bot.deliveriesLatest == 9)
    #expect(HerBot(["id": "her"])!.newDeliveries == 0)
}
