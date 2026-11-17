import HermeshHer
import HermeshKit
import HermeshSessions
import SwiftUI

// Her's scheduled tasks (docs/kernel/routine-design.md §8), as the Mac's routines panel shows them, on pages pushed
// over her conversation from her page's top: the list, a task, and one of its runs. They run on her machine. New
// tasks and changes go through the chat: these pages put a sentence in the composer.

/// The environment her page shows, with Her as it lists her, for a page of her scheduled tasks.
private struct RoutinesPlace<Content: View>: View {
    @Environment(AppModel.self) private var model
    @ViewBuilder let content: (HerChat, HerMachine, HerBot) -> Content

    var body: some View {
        if let m = herPick(herEnvironments(model.machines), remembered: model.her.machine) {
            let chat = model.her.chat(m.machineID, model: model)
            Group {
                if herAnswers(m.her, "bots/routine") == false {
                    NeedsUpdateView(machineName: m.machineName)
                } else if let bot = chat.bot {
                    content(chat, m, bot)
                } else {
                    PageLoader(text: "Reading the conversation…", bare: true)
                }
            }
            .herConnections(chat, machine: m)
        } else {
            ContentUnavailableView {
                Label("Her isn’t on", systemImage: "clock")
            } description: {
                Text("Her’s scheduled tasks show here once she runs on one of your machines.")
            }
        }
    }
}

/// A sentence for the owner to finish, in Her's composer, which is ready to type on.
@MainActor private func draft(_ text: String, _ chat: HerChat, _ model: AppModel) {
    chat.put(text)
    model.openHer(machineID: chat.machineID)
}

// MARK: The list

/// The list: what runs, then what is paused, then one-offs that are over; each paused and resumed in its row.
struct RoutinesPage: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        RoutinesPlace { chat, machine, bot in
            RoutinesList(chat: chat, machine: machine, bot: bot)
        }
        .navigationTitle("Scheduled Tasks")
        .navigationBarTitleDisplayMode(.inline)
    }
}

private struct RoutinesList: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let machine: HerMachine
    let bot: HerBot

    var body: some View {
        let groups = herRoutineGroups(bot.routines)
        List {
            if !bot.routines.isEmpty {
                Section {
                    ForEach(groups.active) { RoutineRow(chat: chat, bot: bot, routine: $0) }
                }
                if !groups.paused.isEmpty {
                    Section("Paused") {
                        ForEach(groups.paused) { RoutineRow(chat: chat, bot: bot, routine: $0) }
                    }
                }
                if !groups.finished.isEmpty {
                    Section("Finished") {
                        ForEach(groups.finished) { RoutineRow(chat: chat, bot: bot, routine: $0) }
                    }
                }
                Section {} footer: {
                    Text("Scheduled tasks run on \(machine.machineName). They don’t run while it is off or asleep, and catch up once, with the latest, when it is back.")
                }
            }
        }
        .listSectionSpacing(.compact)
        .overlay {
            if let error = bot.routinesError {
                ContentUnavailableView("Scheduled Tasks Aren’t Available", systemImage: "exclamationmark.triangle", description: Text(error))
            } else if bot.routines.isEmpty {
                ContentUnavailableView {
                    Label("No Scheduled Tasks Yet", systemImage: "clock")
                } description: {
                    Text("Just tell \(bot.name), for example “give me a pre-market brief every morning at 8”.")
                } actions: {
                    Button("New Scheduled Task") { draft(String(localized: "Set up a scheduled task for me: "), chat, model) }
                        .buttonStyle(.glass)
                        .accessibilityIdentifier("routines-new-empty")
                }
            }
        }
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button("New Scheduled Task", systemImage: "plus") { draft(String(localized: "Set up a scheduled task for me: "), chat, model) }
                    .accessibilityIdentifier("routines-new")
            }
        }
        .accessibilityIdentifier("routines")
    }
}

/// A task in the list: its name and what it is up to, opening it; its switch pauses and resumes it, and a one-off
/// that has run has nothing left to switch.
private struct RoutineRow: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let bot: HerBot
    let routine: HerRoutine

    var body: some View {
        HStack(spacing: 12) {
            Button {
                model.path.append(.routine(routine.id))
            } label: {
                VStack(alignment: .leading, spacing: 3) {
                    Text(routine.name).foregroundStyle(routine.state == "active" ? .primary : .secondary)
                    RoutineSecondLine(routine: routine).font(.subheadline)
                }
                .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            if routine.state == "finished" {
                Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(.tertiary)
            } else {
                EnabledSwitch(chat: chat, bot: bot, routine: routine).labelsHidden()
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("routine-row")
    }
}

/// What a task is up to: running, its last run failed, when it runs next, or its schedule.
private struct RoutineSecondLine: View {
    @Environment(AppModel.self) private var model
    let routine: HerRoutine

    var body: some View {
        switch herRoutineLine(routine) {
        case .running(let since):
            if let since {
                Text("Running · \(Text(since, style: .timer))").foregroundStyle(Color.accentColor)
            } else {
                Text("Running…").foregroundStyle(Color.accentColor)
            }
        case .failed(let at):
            Text(at.map { String(localized: "Last run failed · \(momentText($0, now: model.now))") } ?? String(localized: "Last run failed")).foregroundStyle(.red)
        case .next(let when, let at):
            Text("Next \(momentText(at, now: model.now)) · \(when)").foregroundStyle(.secondary)
        case .when(let when):
            Text(when).foregroundStyle(.secondary)
        }
    }
}

/// Paused and resumed right here; the switch shows the owner's choice until her says how it stands.
private struct EnabledSwitch: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let bot: HerBot
    let routine: HerRoutine
    @State private var asked: Bool?

    var body: some View {
        Toggle("Enabled", isOn: Binding {
            asked ?? (routine.state == "active")
        } set: { on in
            asked = on
            Task {
                do {
                    _ = try await chat.call("bots/routine_set_enabled", ["bot_id": .string(bot.id), "routine_id": .string(routine.id), "enabled": .bool(on)])
                } catch {
                    asked = nil
                    model.show(error.localizedDescription)
                }
            }
        })
        .onChange(of: routine.state) { asked = nil }
        .accessibilityIdentifier("routine-enabled")
    }
}

// MARK: A task

/// A task: whether it runs, when, what it is told, what can be done to it, and its runs.
struct RoutinePage: View {
    let routineID: String

    var body: some View {
        RoutinesPlace { chat, machine, bot in
            if let routine = bot.routines.first(where: { $0.id == routineID }) {
                RoutineDetailList(chat: chat, bot: bot, routine: routine)
            } else {
                ContentUnavailableView("This Scheduled Task Is Gone", systemImage: "clock.badge.xmark")
            }
        }
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// A page of a task's runs.
private let page = 20

private struct RoutineDetailList: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let bot: HerBot
    let routine: HerRoutine
    @State private var detail: HerRoutineDetail?
    @State private var runs: [HerRoutineRun]?
    @State private var more = false
    @State private var unfolded: Set<String> = []
    @State private var asking: Ask?
    @State private var busy = false

    private enum Ask: Identifiable {
        case runNow, delete
        var id: Self { self }
    }

    /// What tells one state of the task from another: its row changes with every run, which is when its records have
    /// something new.
    private struct Reload: Equatable {
        var routine: HerRoutine
        var up: Bool
    }

    private var ask: JSON { params([:]) }

    private func params(_ more: [String: JSON]) -> JSON {
        .object(["bot_id": .string(bot.id), "routine_id": .string(routine.id)].merging(more) { $1 })
    }

    var body: some View {
        List {
            Section {
                if routine.state != "finished" {
                    EnabledSwitch(chat: chat, bot: bot, routine: routine)
                }
                if case .running(let since?) = herRoutineLine(routine) {
                    LabeledContent("Running for") { Text(since, style: .timer).monospacedDigit().foregroundStyle(Color.accentColor) }
                }
                LabeledContent("When", value: routine.when)
                if let upcoming = detail?.upcoming, !upcoming.isEmpty {
                    LabeledContent("Next Runs", value: upcoming.map { momentText($0, now: model.now) }.formatted(.list(type: .and, width: .narrow)))
                }
            }
            Section("Instructions") {
                if let detail {
                    Text(detail.prompt).textSelection(.enabled).accessibilityIdentifier("routine-prompt")
                } else {
                    ProgressView().frame(maxWidth: .infinity)
                }
            }
            Section {
                if routine.running {
                    Button("Stop", systemImage: "stop.fill") { act("bots/routine_stop") }.disabled(busy)
                        .accessibilityIdentifier("routine-stop")
                } else {
                    Button("Run Now", systemImage: "play.fill") { asking = .runNow }.disabled(busy)
                        .accessibilityIdentifier("routine-run-now")
                }
                Button("Change in the Chat", systemImage: "pencil") {
                    draft(String(localized: "Change the scheduled task “\(routine.name)” to: "), chat, model)
                }
                .accessibilityIdentifier("routine-edit")
                Button("Delete Scheduled Task", systemImage: "trash", role: .destructive) { asking = .delete }.disabled(busy)
                    .accessibilityIdentifier("routine-delete")
            }
            Section("Recent Runs") {
                if let runs, runs.isEmpty {
                    Text("It hasn’t run yet.").foregroundStyle(.secondary)
                } else if let runs {
                    ForEach(herRunRows(runs, open: unfolded)) { row in
                        switch row {
                        case .run(let r):
                            RunRow(routineID: routine.id, run: r, relaying: r.id == runs.first?.id && bot.typing)
                        case .quiet(let key, let folded):
                            QuietRow(runs: folded) { withAnimation(.snappy) { _ = unfolded.insert(key) } }
                        }
                    }
                    if more {
                        Button("Earlier Runs") { earlier() }.accessibilityIdentifier("routine-earlier")
                    }
                } else {
                    ProgressView().frame(maxWidth: .infinity)
                }
            }
        }
        .listSectionSpacing(.compact)
        .navigationTitle(routine.name)
        .task(id: Reload(routine: routine, up: chat.envUp)) { await load() }
        .confirmationDialog(asking == .delete ? String(localized: "Delete the scheduled task “\(routine.name)”?") : String(localized: "Run “\(routine.name)” now?"),
                            isPresented: Binding { asking != nil } set: { if !$0 { asking = nil } }, titleVisibility: .visible, presenting: asking) { a in
            switch a {
            case .runNow:
                Button("Run Now") { act("bots/routine_run_now") }.accessibilityIdentifier("routine-run-now-confirm")
            case .delete:
                Button("Delete", role: .destructive) { remove() }.accessibilityIdentifier("routine-delete-confirm")
            }
        } message: { a in
            switch a {
            case .runNow: Text("It really does the task, as when its time comes.")
            case .delete: Text("It won’t run again.")
            }
        }
        .accessibilityIdentifier("routine")
    }

    private func load() async {
        guard chat.envUp else { return }
        async let d = try? chat.call("bots/routine", ask)
        async let r = try? chat.call("bots/routine_runs", params(["limit": .number(Double(page))]))
        if let d = await d { detail = HerRoutineDetail(d["routine"]) }
        if let r = await r {
            let list = (r["runs"].array ?? []).compactMap(HerRoutineRun.init)
            runs = list
            more = list.count == page
        }
    }

    private func earlier() {
        Task {
            do {
                let r = try await chat.call("bots/routine_runs", params(["limit": .number(Double(page)), "before": runs?.last.map { .string($0.id) } ?? nil]))
                let list = (r["runs"].array ?? []).compactMap(HerRoutineRun.init)
                runs = (runs ?? []) + list
                more = list.count == page
            } catch { model.show(error.localizedDescription) }
        }
    }

    private func act(_ method: String) {
        busy = true
        Task {
            do { _ = try await chat.call(method, ask) } catch { model.show(error.localizedDescription) }
            busy = false
        }
    }

    private func remove() {
        busy = true
        let name = routine.name, id = routine.id
        Task {
            do {
                _ = try await chat.call("bots/routine_delete", ask)
                if model.path.last == .routine(id) { model.path.removeLast() }
                model.show(String(localized: "Deleted the scheduled task “\(name)”"))
            } catch { model.show(error.localizedDescription) }
            busy = false
        }
    }
}

/// What a run came to, in the owner's words, and whether it is something to look at.
private func result(_ run: HerRoutineRun, relaying: Bool) -> (text: String, warn: Bool) {
    switch run.outcome {
    case "reported":
        if run.delivered != nil { return (String(localized: "Sent to you"), false) }
        return relaying ? (String(localized: "Passing it on"), false) : (String(localized: "Not delivered"), true)
    case "quiet": return (String(localized: "Nothing to report"), false)
    case "failed": return (String(localized: "Failed"), true)
    case "skipped": return (String(localized: "Skipped"), false)
    case "interrupted": return (String(localized: "Stopped"), false)
    default: return (String(localized: "Running…"), false)
    }
}

/// A run: when, how long and what it said, and what it came to; a skipped one did nothing, so it opens nothing.
private struct RunRow: View {
    @Environment(AppModel.self) private var model
    let routineID: String
    let run: HerRoutineRun
    let relaying: Bool

    var body: some View {
        let r = result(run, relaying: relaying)
        let skipped = run.outcome == "skipped"
        let said = run.outcome == "failed" ? run.report : skipped ? String(localized: "The run before it was still going") : run.note
        let second = [run.took(now: model.now).map { String(localized: "Ran for \(routineDuration($0))") },
                      run.lateBy.map { String(localized: "\(routineDuration($0)) late") }, said.map(firstLine)].compactMap { $0 }.joined(separator: " · ")
        Button {
            model.path.append(.routineRun(routineID, run.id))
        } label: {
            HStack(spacing: 10) {
                VStack(alignment: .leading, spacing: 3) {
                    Text(run.at.map { momentText($0, now: model.now) } ?? "—").foregroundStyle(.primary)
                    if !second.isEmpty { Text(second).font(.subheadline).foregroundStyle(.secondary).lineLimit(2) }
                }
                Spacer(minLength: 8)
                Text(r.text).font(.subheadline).foregroundStyle(r.warn ? AnyShapeStyle(.red) : run.outcome == nil ? AnyShapeStyle(Color.accentColor) : AnyShapeStyle(.secondary))
                if !skipped { Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(.tertiary) }
            }
            .frame(minHeight: 44)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .disabled(skipped)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("routine-run")
    }
}

/// Several quiet runs in a row, as one row that opens to each.
private struct QuietRow: View {
    @Environment(AppModel.self) private var model
    let runs: [HerRoutineRun]
    let unfold: () -> Void

    var body: some View {
        let from = runs.last?.at.map { momentText($0, now: model.now) } ?? ""
        let to = runs.first?.at.map { momentText($0, now: model.now) } ?? ""
        Button(action: unfold) {
            HStack(spacing: 10) {
                VStack(alignment: .leading, spacing: 3) {
                    Text("\(from) to \(to)").foregroundStyle(.primary)
                    Text("\(runs.count) runs, open to see each").font(.subheadline).foregroundStyle(.secondary)
                }
                Spacer(minLength: 8)
                Text("Nothing to report").font(.subheadline).foregroundStyle(.secondary)
                Image(systemName: "chevron.down").font(.footnote.weight(.semibold)).foregroundStyle(.tertiary)
            }
            .frame(minHeight: 44)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("routine-quiet")
    }
}

// MARK: A run

/// One run: how it went, what it said, and its steps, which answer "why did it not tell me".
struct RoutineRunPage: View {
    let routineID: String
    let runID: String

    var body: some View {
        RoutinesPlace { chat, machine, bot in
            RunDetail(chat: chat, bot: bot, routineID: routineID, runID: runID, routine: bot.routines.first { $0.id == routineID })
        }
        .navigationTitle("One Run")
        .navigationBarTitleDisplayMode(.inline)
    }
}

private struct RunDetail: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let bot: HerBot
    let routineID: String
    let runID: String
    let routine: HerRoutine?
    @State private var run: HerRoutineRun?
    @State private var steps: [HerStep] = []
    @State private var failed: String?
    /// The run cannot be read: an older her refuses the runs of a deleted task, or there is no such run.
    @State private var gone = false
    /// The task's name, and whether it has been deleted since: its runs are kept.
    @State private var routineName = ""
    @State private var routineDeleted = false
    /// The steps the owner opened or folded.
    @State private var chosen: [String: Bool] = [:]

    private struct Reload: Equatable {
        var last: HerRoutineRunBrief?
        var up: Bool
    }

    var body: some View {
        Group {
            if let run {
                list(run)
            } else if gone {
                ContentUnavailableView("This Run Is Gone", systemImage: "clock.badge.xmark", description: Text("Its scheduled task may have been deleted."))
            } else if let failed {
                ContentUnavailableView("Can’t Read This Run", systemImage: "exclamationmark.triangle", description: Text(failed))
            } else {
                PageLoader(text: "Reading the run…", bare: true)
            }
        }
        .task(id: Reload(last: routine?.lastRun, up: chat.envUp)) { await load() }
    }

    private func list(_ run: HerRoutineRun) -> some View {
        let r = result(run, relaying: bot.typing)
        // Opened from the conversation or Progress, the task it is a run of is a tap away.
        let fromTask = model.path.dropLast().last == .routine(routineID)
        return List {
            Section {
                if !fromTask, let routine {
                    Button { model.path.append(.routine(routineID)) } label: {
                        HStack {
                            LabeledContent("Scheduled Task", value: routine.name)
                            Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(.tertiary)
                        }
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("routine-run-task")
                } else if !fromTask, !routineName.isEmpty {
                    // Deleted, or not in her list yet: named, with nothing to open.
                    LabeledContent("Scheduled Task", value: routineDeleted ? String(localized: "\(routineName) (Deleted)") : routineName)
                        .accessibilityIdentifier("routine-run-task")
                }
                LabeledContent("Result") {
                    Text(run.delivered.map { "\(r.text) \(momentText($0, now: model.now))" } ?? r.text)
                        .foregroundStyle(r.warn ? AnyShapeStyle(.red) : run.outcome == nil ? AnyShapeStyle(Color.accentColor) : AnyShapeStyle(.secondary))
                }
                .accessibilityIdentifier("routine-run-result")
                if run.delivered != nil {
                    Button("Show in Chat") {
                        chat.revealRun = run.id
                        model.openHer(machineID: chat.machineID)
                    }
                    .accessibilityIdentifier("routine-show-in-chat")
                }
                if let late = run.lateBy, let scheduled = run.scheduled {
                    LabeledContent("Scheduled for", value: "\(momentText(scheduled, now: model.now)) · \(String(localized: "\(routineDuration(late)) late"))")
                }
                LabeledContent("Started", value: run.started.map { momentText($0, now: model.now) } ?? "—")
                LabeledContent("Took", value: run.took(now: model.now).map(routineDuration) ?? "—")
                LabeledContent("Usage", value: run.tokens > 0 ? String(localized: "\(tokensText(UInt64(run.tokens))) tokens") : "—")
            }
            if run.outcome == "quiet", let note = run.note {
                Section("What It Found") { Text(note).textSelection(.enabled) }
            }
            if let report = run.report {
                Section(run.outcome == "failed" ? "Why" : "Report") {
                    MarkdownText(text: report).textSelection(.enabled).padding(.vertical, 4).accessibilityIdentifier("routine-run-report")
                }
            }
            if !steps.isEmpty {
                Section("Steps") {
                    ForEach(steps) { step in
                        StepRow(step: step, open: Binding(get: { chosen[step.key] ?? opensByDefault(step) }, set: { chosen[step.key] = $0 }))
                    }
                }
            }
        }
        .listSectionSpacing(.compact)
        .accessibilityIdentifier("routine-run-page")
    }

    /// A thought shows whole; a call's output, when it is opened.
    private func opensByDefault(_ s: HerStep) -> Bool {
        if case .thinking = s.kind { return true }
        return false
    }

    private func load() async {
        guard chat.envUp else { return }
        do {
            let r = try await chat.call("bots/routine_run", ["bot_id": .string(bot.id), "routine_id": .string(routineID), "run_id": .string(runID)])
            run = HerRoutineRun(r["run"])
            routineName = r["routine_name"].string ?? ""
            routineDeleted = r["routine_deleted"].bool ?? false
            steps = herRunSteps(r["transcript"], run: runID)
            failed = nil
        } catch let refused as HerRPCError where refused.kind == "routine_not_found" {
            if run == nil { gone = true }
        } catch {
            if run == nil { failed = error.localizedDescription }
        }
    }
}

// MARK: Words

/// "Today 08:00", "Tonight 21:30", "Tomorrow 08:00", "Yesterday 08:00", else the date and time: a moment a task ran
/// or will run.
func momentText(_ d: Date, now: Date, calendar: Calendar = .current) -> String {
    let time = hourMinute(d, calendar: calendar)
    if calendar.isDate(d, inSameDayAs: now) {
        return calendar.component(.hour, from: d) >= 18 ? String(localized: "Tonight \(time)") : String(localized: "Today \(time)")
    }
    if let tomorrow = calendar.date(byAdding: .day, value: 1, to: now), calendar.isDate(d, inSameDayAs: tomorrow) { return String(localized: "Tomorrow \(time)") }
    if let yesterday = calendar.date(byAdding: .day, value: -1, to: now), calendar.isDate(d, inSameDayAs: yesterday) { return String(localized: "Yesterday \(time)") }
    return "\(monthDay(d)) \(time)"
}

/// How long something took or has run, in its two largest units: "48 s", "2 min 14 s".
func routineDuration(_ seconds: TimeInterval) -> String {
    Duration.seconds(max(1, seconds.rounded())).formatted(.units(allowed: [.days, .hours, .minutes, .seconds], width: .abbreviated, maximumUnitCount: 2))
}

/// A text's first line that says something.
private func firstLine(_ text: String) -> String {
    text.split(separator: "\n").map { $0.trimmingCharacters(in: .whitespaces) }.first { !$0.isEmpty } ?? ""
}
