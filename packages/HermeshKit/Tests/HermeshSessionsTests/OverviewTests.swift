import CoreGraphics
import Foundation
import HermeshProto
import Testing
@testable import HermeshSessions

private func failed(_ id: String, by: String = "c1", machine: String = "m1") -> Session {
    var s = session(id, .idle, by: by, machine: machine)
    s.progress.lastFailure.kind = .unknown
    return s
}

@Test func whatNeedsTheOwnerIsInOrderOfUrgency() {
    var asked = session("q1", .waitingInput, updated: -10), askedEarlier = session("q0", .waitingInput, updated: -90)
    ask(&asked)
    ask(&askedEarlier)
    let team = teamState([session("r", .running), failed("f"), askedEarlier, asked, session("d", .idle)], requests: 1)
    let all = needs(team: team, pairings: ["482913"], expiring: ["g1"], guide: true, herOff: true, notificationsOff: true, secondDevice: true)
    #expect(all.map(\.id) == ["q:q1", "q:q0", "p:482913", "f:f", "g:g1", "guide", "her", "notifications", "device"])
    #expect(needs(team: teamState([session("d", .idle)], requests: 0), pairings: [], expiring: [], guide: false, herOff: false, notificationsOff: false,
                  secondDevice: false).isEmpty)
}

@Test func theDeckKeepsItsPlace() {
    let all: [Need] = [.question("a"), .pairing("1"), .failed("b")]
    #expect(deckFront(all, was: nil, at: 0) == 0)
    #expect(deckFront(all, was: "p:1", at: 0) == 1, "the card in front stays in front when the list changes around it")
    #expect(deckFront([.question("a"), .failed("b")], was: "p:1", at: 1) == 1, "handled: the next takes its place")
    #expect(deckFront([.question("a")], was: "f:b", at: 2) == 0, "the last was handled: the one before")
    #expect(deckFront([], was: "q:a", at: 0) == 0)
    #expect(arrived(all, before: ["q:a", "f:b"]) == .pairing("1"), "a request to connect that just came goes in front")
    #expect(arrived(all, before: ["q:a", "p:1"]) == nil, "a failure waits its turn")
    #expect(arrived(all, before: all.map(\.id)) == nil)
}

@Test func theMapOfOneSenderAndOneEnvironmentDrawsWhatCanBeAdded() {
    let map = linkMap(senders: [MapSender(clientID: "her", title: "Her")], environments: [MapEnvironment(machineID: "m1", title: "MacBook Pro", agents: ["claude"])],
                      team: teamState([], requests: 0), owner: "You")
    #expect(map.senders.map(\.id) == ["s:her", "s:+"] && map.senders[1].kind == .addSender)
    #expect(map.environments.map(\.id) == ["e:m1", "e:+"] && map.environments[1].kind == .addEnvironment)
    #expect(map.lines.isEmpty)
    let layout = LinkMapLayout(map, width: 329)
    #expect(layout.size.height == 104 && layout.frames.count == 4 && layout.links.count == 4)
}

@Test func theMapDrawsALineForEachSessionNotFinished() {
    var asked = session("q", .waitingInput, by: "her", machine: "m1")
    ask(&asked)
    let team = teamState([session("r1", .running, by: "her", machine: "m1"), asked, failed("f", by: "gpt", machine: "m2"),
                          session("r2", .queued, by: "cursor", machine: "m3"), session("done", .idle, by: "claude", machine: "m3"),
                          session("mine", .running, by: "owner", machine: "m1"), session("gone", .running, by: "revoked", machine: "m1")], requests: 0)
    let senders = ["claude", "gpt", "cursor", "her"].map { MapSender(clientID: $0, title: $0) }
    let environments = [MapEnvironment(machineID: "m3", title: "vps", online: false), MapEnvironment(machineID: "m2", title: "mini"),
                        MapEnvironment(machineID: "m1", title: "MacBook", local: true)]
    let map = linkMap(senders: senders, environments: environments, team: team, owner: "You")
    #expect(map.senders.map(\.id) == ["s:her", "s:gpt", "s:cursor", "s:owner", "s:claude"], "the busiest first; the owner while a session of theirs runs")
    #expect(map.senders.first { $0.id == "s:owner" }?.title == "You")
    #expect(map.environments.map(\.id) == ["e:m1", "e:m2", "e:m3"], "this device's own, then online, then offline")
    #expect(map.environments.last?.offline == true)
    #expect(Set(map.lines.map(\.id)) == ["r1", "q", "f", "r2", "mine"], "a finished session and one from a grant not shown have no line")
    #expect(map.lines.filter(\.atWork).map(\.id).sorted() == ["mine", "r1", "r2"])

    let layout = LinkMapLayout(map, width: 329)
    #expect(layout.size.height == 200, "five senders, 40 apart")
    let r1 = layout.ways["r1"]!, q = layout.ways["q"]!
    #expect(r1.count == 2 && r1[0].from.x == layout.frames["s:her"]!.maxX && r1[1].to.x == layout.frames["e:m1"]!.minX)
    #expect(abs(r1[0].from.y - q[0].from.y) == 5, "two lines of the same two nodes run side by side")

    #expect(map.touched(by: "s:her", sessions: team.running + team.waiting + team.failed + team.finished) == ["s:her", "e:m1"])
    #expect(sessions(through: "e:m3", team: team).map(\.sessionID) == ["r2", "done"], "at work, then finished")
}

@Test func aSideWithNoRoomSaysHowManyMore() {
    let senders = (1...8).map { MapSender(clientID: "c\($0)", title: "c\($0)") }
    let map = linkMap(senders: senders, environments: (1...3).map { MapEnvironment(machineID: "m\($0)", title: "m\($0)") },
                      team: teamState([session("r", .running, by: "c8")], requests: 0), owner: "You")
    #expect(map.senders.count == 6 && map.senders.first?.id == "s:c8" && map.senders.last?.kind == .more(3))
    #expect(map.environments.count == 3, "three is not few: nothing to add is drawn")
}

@Test func theOverviewIsMadeFast() {
    let all = (0..<500).map { i in session("s\(i)", i % 9 == 0 ? .running : .idle, updated: Double(-i), by: "c\(i % 5)", machine: "m\(i % 3)") }
    let senders = (0..<5).map { MapSender(clientID: "c\($0)", title: "c\($0)") }, environments = (0..<3).map { MapEnvironment(machineID: "m\($0)", title: "m\($0)") }
    let clock = ContinuousClock()
    var lines = 0
    let took = clock.measure {
        for _ in 0..<20 {
            let team = teamState(all, requests: 0)
            let map = linkMap(senders: senders, environments: environments, team: team, owner: "You")
            lines += LinkMapLayout(map, width: 329).ways.count
            _ = needs(team: team, pairings: [], expiring: [], guide: false, herOff: false, notificationsOff: false, secondDevice: false)
        }
    }
    #expect(lines == 20 * 56)
    #expect(took < .milliseconds(20 * 20), "500 held sessions: \(took / 20) a pass") // generous for a debug build on a shared machine
}
