import Foundation
import Testing
@testable import HermeshHer

// An assistant's routines as bots/subscribe lists them, a run's rows, and what the conversation shows of them: the
// line for a change Her made, and the run a message of hers passes on.

private func routine(_ id: String, state: String = "active", running: Bool = false, last: JSON = nil, next: JSON = nil) -> JSON {
    ["id": .string(id), "name": .string("Brief \(id)"), "when": "every weekday at 8", "state": .string(state), "running": .bool(running), "last_run": last, "next_run": next]
}

private func run(_ id: String, _ outcome: JSON) -> HerRoutineRun {
    HerRoutineRun(["id": .string(id), "scheduled": "2026-11-15T08:00:00Z", "started": "2026-11-15T08:00:02.500Z", "outcome": outcome])!
}

@Test func aBotListsItsRoutines() {
    let bot = HerBot(["id": "b1", "name": "Her", "default": true, "typing": true, "routines": [
        routine("r1", next: "2026-11-16T08:00:00Z"),
        routine("r2", state: "paused"),
        ["name": "no id"],
    ]])!
    #expect(bot.routines.map(\.id) == ["r1", "r2"], "one without an id is left out")
    #expect(bot.typing && bot.routinesError == nil)
    #expect(bot.routines[0].nextRun == Date(timeIntervalSince1970: 1_794_816_000))
    #expect(HerBot(["id": "b1", "routines": [], "routines_error": "database is locked"])!.routinesError == "database is locked")
    #expect(HerBot(["id": "b1"])!.routines.isEmpty, "a her too old to list them")
}

@Test func theButtonIsMarkedForAFailureFirst() {
    let failed = HerRoutine(routine("r1", last: ["started": "2026-11-15T08:00:00Z", "outcome": "failed"]))!
    let running = HerRoutine(routine("r2", running: true, last: ["started": "2026-11-15T09:00:00Z"]))!
    let fine = HerRoutine(routine("r3", last: ["started": "2026-11-15T08:00:00Z", "outcome": "reported"]))!
    #expect(herRoutineDot([fine, running, failed]) == .failed)
    #expect(herRoutineDot([fine, running]) == .running)
    #expect(herRoutineDot([fine]) == nil)
}

@Test func aRowSaysTheFirstThingThatHolds() {
    let started = Date(timeIntervalSince1970: 1_794_733_200)
    #expect(herRoutineLine(HerRoutine(routine("r", running: true, last: ["started": "2026-11-15T09:00:00Z", "outcome": "failed"]))!) == .running(since: started))
    #expect(herRoutineLine(HerRoutine(routine("r", last: ["started": "2026-11-15T09:00:00Z", "outcome": "failed"], next: "2026-11-16T08:00:00Z"))!) == .failed(at: started))
    #expect(herRoutineLine(HerRoutine(routine("r", next: "2026-11-16T08:00:00Z"))!) == .next(when: "every weekday at 8", at: Date(timeIntervalSince1970: 1_794_816_000)))
    #expect(herRoutineLine(HerRoutine(routine("r", state: "paused"))!) == .when("every weekday at 8"))
    let groups = herRoutineGroups([HerRoutine(routine("a"))!, HerRoutine(routine("p", state: "paused"))!, HerRoutine(routine("f", state: "finished"))!])
    #expect(groups.active.map(\.id) == ["a"] && groups.paused.map(\.id) == ["p"] && groups.finished.map(\.id) == ["f"])
}

@Test func threeQuietRunsInARowFoldIntoOne() {
    let runs = [run("r6", "reported"), run("r5", "quiet"), run("r4", "quiet"), run("r3", "quiet"), run("r2", "failed"), run("r1", "quiet"), run("r0", "quiet")]
    #expect(herRunRows(runs).map(\.id) == ["r6", "quiet:r5", "r2", "r1", "r0"], "two in a row stay as they are")
    guard case .quiet(_, let folded) = herRunRows(runs)[1] else { Issue.record("not folded"); return }
    #expect(folded.map(\.id) == ["r5", "r4", "r3"])
    #expect(herRunRows(runs, open: ["r5"]).map(\.id) == ["r6", "r5", "r4", "r3", "r2", "r1", "r0"], "unfolded")
}

@Test func aRunsTimes() {
    let now = Date(timeIntervalSince1970: 1_794_729_660)
    let r = run("r1", nil)
    #expect(r.took(now: now) == 57.5, "still running: up to now")
    var done = HerRoutineRun(["id": "r", "scheduled": "2026-11-15T08:00:00Z", "started": "2026-11-15T08:04:00Z", "ended": "2026-11-15T08:05:00Z", "late": true, "outcome": "reported",
                              "report": "  ", "usage": ["per_model": ["m": ["input": 1000, "output": 200]]]])!
    #expect(done.took(now: now) == 60 && done.lateBy == 240 && done.tokens == 1200)
    #expect(done.report == nil, "nothing but spaces says nothing")
    done.late = false
    #expect(done.lateBy == nil)
    let skipped = HerRoutineRun(["id": "s", "scheduled": "2026-11-15T08:00:00Z", "outcome": "skipped", "note": "the last one still runs"])!
    #expect(skipped.took(now: now) == nil && skipped.at == Date(timeIntervalSince1970: 1_794_729_600))
}

@Test func aRunsStepsLeaveOutWhatItWasToldAndItsReport() {
    let steps = herRunSteps([
        said("m1", "[[mock:say:Time to stretch.]]", ts: 1000),
        reply("a1", [call("c1", "bash", ["command": "date"])], ts: 1100),
        result("c1", text: "Sun"),
        ["type": "message", "message_id": "a2", "message": ["role": "assistant", "content": [["type": "text", "text": "Time to stretch."]], "ts": 1200]],
    ], run: "run1")
    #expect(steps.map(\.key) == ["a1:0", "c1"], "its words before the call stay; its last words are its report")
}

@Test func aChangeHerMadeToARoutineIsALine() {
    let v = snapshot([
        said("u1", "remind me to stretch", ts: 1000),
        reply("a1", [call("c1", "manage_routine", ["action": "create"]), call("c2", "manage_routine", ["action": "list"])], ts: 2000, duration: 300),
        result("c1", details: ["action": "create", "routine_id": "rt1", "name": "Stretch", "when": "in a minute", "next_run": "2026-11-15T08:01:00Z"]),
        result("c2", details: [:]),
        reply("a3", [call("c3", "manage_routine", ["action": "delete"])], ts: 3000),
        result("c3", details: ["action": "delete", "routine_id": "rt1", "name": "Stretch", "when": ""], error: true),
    ])
    let items = herChat(v)
    #expect(items.map(\.key) == ["u1", "routine:c1"], "a list changes nothing; a refused change made none")
    #expect(items[1].ts == 2300)
    #expect(items[1].kind == .routine(HerRoutineChange(action: "create", routineID: "rt1", name: "Stretch", when: "in a minute", nextRun: Date(timeIntervalSince1970: 1_794_729_660))))
}

@Test func whatHerPassesOnFromARunNamesTheRun() {
    let report: JSON = ["type": "message", "message_id": "m2", "mail": ["kind": "routine", "from": ["id": "run7", "name": "Stretch"], "routine_id": "rt1"],
                        "message": ["role": "user", "content": [["type": "text", "text": "<routine_report>Time to stretch.</routine_report>"]], "ts": 5000]]
    let v = snapshot([
        report,
        reply("a2", [call("c2", "send_to_user", ["text": "Time to stretch."])], ts: 5100),
        result("c2"),
        ["type": "task_end", "task_id": "t2", "outcome": "completed", "ts": 5200],
        said("u3", "thanks", ts: 6000),
        reply("a3", [call("c3", "send_to_user", ["text": "Any time."])], ts: 6100),
        result("c3"),
    ])
    let items = herChat(v)
    #expect(items.map(\.key) == ["c2", "u3", "c3"], "the report itself is Her's to read")
    #expect(items[0].source == HerRoutineSource(routineID: "rt1", runID: "run7", name: "Stretch"))
    #expect(items[2].source == nil, "an answer to the owner is no run's")
    let groups = herProgress(snapshot([report, reply("a2", [call("c9", "spawn_agent", ["name": "w", "task": "go"])], ts: 5100)]))
    #expect(groups.first?.run == HerRoutineSource(routineID: "rt1", runID: "run7", name: "Stretch"))
}
