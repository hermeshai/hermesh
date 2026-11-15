import HermeshHer
import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI

// Progress (docs/her-team.md §15): the work Her sent out, as cards under what the owner said that set it off, and
// behind a sub-agent's card the whole record of that piece of work. Everything is read from the conversation the
// app already follows for Her (HerChat: her sub-agents' own conversations come with it), so the pages ask her for
// nothing more; a coding session's card opens the session's own page.

/// How a card stands: a sub-agent's state, or the same for a coding session; gone: the app holds the session no more.
private enum Stand: Equatable {
    case starting, running, waiting, done, failed, stopped, gone

    init(_ s: HerAgentState) {
        self = switch s {
        case .starting: .starting
        case .running: .running
        case .waiting: .waiting
        case .done: .done
        case .failed: .failed
        case .stopped: .stopped
        }
    }

    init(session s: Session?) {
        guard let s else { self = .gone; return }
        self = s.status == .waitingInput ? .waiting : s.isFailed ? .failed : s.isActive ? .running : .done
    }

    var atWork: Bool { self == .running || self == .starting }

    var words: LocalizedStringKey {
        switch self {
        case .starting: "Starting"
        case .running: "In progress"
        case .waiting: "Needs your approval"
        case .done: "Done"
        case .failed: "Failed"
        case .stopped: "Stopped"
        case .gone: "No longer on this iPhone"
        }
    }
}

/// A group as the page draws it: its cards' states, and where it stands as a whole.
private struct GroupView: Identifiable {
    enum Overall: Int { case waiting, running, ended }

    var group: HerProgressGroup
    var stands: [Stand]
    var tokens: Int
    var id: String { group.key }

    var overall: Overall { stands.contains(.waiting) ? .waiting : stands.contains(where: \.atWork) ? .running : .ended }
}

@MainActor private func stand(_ t: HerProgressTask, _ model: AppModel) -> Stand {
    switch t {
    case .agent(let l): Stand(herAgentState(l))
    case .session(_, let started): Stand(session: model.session(started.sessionID))
    }
}

private func tokens(_ s: Session?) -> Int { s.map { Int($0.progress.usage.inputTokens + $0.progress.usage.outputTokens) } ?? 0 }

@MainActor private func groupViews(_ groups: [HerProgressGroup], _ model: AppModel) -> [GroupView] {
    groups.map { g in
        let used = g.tasks.reduce(0) { n, t in
            switch t {
            case .agent(let l): n + (l.agent.map(herAgentTokens) ?? 0)
            case .session(_, let started): n + tokens(model.session(started.sessionID))
            }
        }
        return GroupView(group: g, stands: g.tasks.map { stand($0, model) }, tokens: used)
    }
}

/// "Took 13 min 7 s": a time in its two largest units.
private func tookText(_ ms: Double) -> String {
    Duration.seconds(max(1, (ms / 1000).rounded())).formatted(.units(allowed: [.hours, .minutes, .seconds], width: .narrow, maximumUnitCount: 2))
}

private func when(_ ts: Double, now: Date) -> String { shortTime(Date(timeIntervalSince1970: ts / 1000), now: now) }

/// The page the menu's Progress opens: Her's environment as the Her entry shows it.
struct ProgressPage: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Group {
            if let m = herPick(herEnvironments(model.machines), remembered: model.her.machine) {
                ProgressBoard(machine: m).id(m.machineID)
            } else {
                ContentUnavailableView {
                    Label("Her isn’t on", systemImage: "waveform.path.ecg")
                } description: {
                    Text("The work Her sends out shows here once she runs on one of your machines.")
                }
            }
        }
        .background(Color(.systemGroupedBackground))
        .navigationTitle("Progress")
        .rootToolbar()
    }
}

/// The board: a line that sums the work up, then each group, those that wait and those at work first.
private struct ProgressBoard: View {
    @Environment(AppModel.self) private var model
    let machine: HerMachine
    /// Whether each group shows its cards: as it stood when it first showed (one that had ended is folded), then as the owner left it.
    @State private var open: [String: Bool] = [:]

    var body: some View {
        let chat = model.her.chat(machine.machineID, model: model)
        let groups = groupViews(chat.progress, model)
        // Those that wait and those at work first, the latest first; then the ended ones.
        let sorted = groups.reversed().enumerated().sorted { ($0.element.overall.rawValue, $0.offset) < ($1.element.overall.rawValue, $1.offset) }.map(\.element)
        let firstEnded = sorted.first { $0.overall == .ended }?.id
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 12) {
                if let line = summary(groups) { Text(line).font(.subheadline).foregroundStyle(.secondary).padding(.horizontal, 6) }
                if chat.session == nil {
                    notRead
                } else if groups.isEmpty {
                    empty(connected: chat.convUp)
                }
                ForEach(sorted) { g in
                    if g.id == firstEnded {
                        Text("Ended").font(.footnote).foregroundStyle(.secondary).padding(.horizontal, 14).padding(.top, 8)
                    }
                    GroupCard(view: g, open: Binding(get: { open[g.id] ?? (g.overall != .ended) }, set: { open[g.id] = $0 }))
                        .onAppear { if open[g.id] == nil { open[g.id] = g.overall != .ended } }
                }
                if chat.live?.before != nil, chat.convUp {
                    HStack {
                        Spacer()
                        if chat.paging { ProgressView().controlSize(.small) } else {
                            Button("Earlier Work") { Task { await chat.earlier() } }.font(.subheadline).accessibilityIdentifier("progress-earlier")
                        }
                        Spacer()
                    }
                    .frame(minHeight: 44)
                }
            }
            .padding(.horizontal, 16).padding(.bottom, 40)
        }
        .herConnections(chat, machine: machine)
        .accessibilityIdentifier("progress")
    }

    /// "2 at work, 1 waiting for you, 3 done."
    private func summary(_ groups: [GroupView]) -> String? {
        let all = groups.flatMap(\.stands)
        let parts = [
            all.filter(\.atWork).count,
            all.filter { $0 == .waiting }.count,
            all.filter { $0 == .done }.count,
        ]
        let words = [
            parts[0] > 0 ? String(localized: "\(parts[0]) at work") : nil,
            parts[1] > 0 ? String(localized: "\(parts[1]) waiting for you") : nil,
            parts[2] > 0 ? String(localized: "\(parts[2]) done") : nil,
        ].compactMap { $0 }
        return words.isEmpty ? nil : String(localized: "Work Her sent out: \(listText(words)).")
    }

    private var notRead: some View {
        Group {
            if herReachable(machine) {
                PageLoader(text: "Reading the conversation…", bare: true)
            } else {
                ContentUnavailableView {
                    Label("\(machine.machineName) is offline", systemImage: "wifi.slash")
                } description: {
                    Text("The work Her sent out shows here once the machine is back.")
                }
            }
        }
        .frame(maxWidth: .infinity).padding(.top, 60)
    }

    private func empty(connected: Bool) -> some View {
        VStack(spacing: 8) {
            HerAvatar(size: 56, asleep: !connected)
            Text("No work sent out yet").font(.headline)
            Text(connected ? "Give Her something to do: each part she sends out shows up here." : "The work Her sent out shows here once the machine is back.")
                .font(.subheadline).foregroundStyle(.secondary).multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity).padding(.horizontal, 24).padding(.top, 60)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("progress-empty")
    }
}

/// What the owner said (or what else set the work off), Her's last words since, and under them the cards.
private struct GroupCard: View {
    @Environment(AppModel.self) private var model
    let view: GroupView
    @Binding var open: Bool

    var body: some View {
        let g = view.group
        VStack(spacing: 6) {
            Button {
                withAnimation(.snappy(duration: 0.25)) { open.toggle() }
            } label: {
                header(g)
            }
            .buttonStyle(.plain)
            .accessibilityValue(open ? Text("Expanded") : Text("Collapsed"))
            .accessibilityIdentifier("progress-group-\(title(g))")
            if open {
                ForEach(Array(g.tasks.enumerated()), id: \.element.id) { i, task in
                    TaskCard(task: task, stand: view.stands[i])
                }
            }
        }
        .padding(6)
        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
    }

    private func title(_ g: HerProgressGroup) -> String {
        switch g.parent {
        case .user(let text, _): text
        case .routine(let name, _): name
        case .earlier: "earlier"
        }
    }

    private func header(_ g: HerProgressGroup) -> some View {
        let waits = view.stands.contains(.waiting)
        let running = view.stands.filter(\.atWork).count
        return HStack(alignment: .top, spacing: 11) {
            Image(systemName: { if case .user = g.parent { "person.fill" } else { "clock" } }())
                .font(.footnote).foregroundStyle(.secondary)
                .frame(width: 32, height: 32).background(Palette.fill2, in: .circle)
                .padding(.top, 2)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 8) {
                    who(g).font(.footnote).foregroundStyle(.secondary).lineLimit(1)
                    Spacer(minLength: 0)
                    if waits { Circle().fill(.orange).frame(width: 9, height: 9).accessibilityLabel("Needs your approval") }
                    if running > 0 {
                        ProgressView().controlSize(.mini)
                        Text("\(running)").font(.footnote).monospacedDigit().foregroundStyle(.secondary)
                    }
                    if view.overall == .ended { Image(systemName: "checkmark").font(.footnote.weight(.semibold)).foregroundStyle(.green).accessibilityHidden(true) }
                    Caret(open: open)
                }
                if case .user(let text, _) = g.parent {
                    Text(text).font(.headline).lineLimit(2).multilineTextAlignment(.leading)
                }
                if let said = g.said {
                    Text("Her: \(firstWords(said))").font(.subheadline).foregroundStyle(.secondary).lineLimit(2).multilineTextAlignment(.leading).padding(.top, 2)
                }
                let total = view.tokens > 0 ? String(localized: "\(g.tasks.count) sent out · \(tokensText(UInt64(view.tokens))) tokens") : String(localized: "\(g.tasks.count) sent out")
                Text(total).font(.caption).monospacedDigit().foregroundStyle(.secondary).padding(.top, 2)
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, minHeight: 60, alignment: .leading)
        .contentShape(.rect)
    }

    private func who(_ g: HerProgressGroup) -> Text {
        switch g.parent {
        case .user(_, let ts): Text("You · \(when(ts, now: model.now))")
        case .routine(let name, let ts): Text("Scheduled task “\(name)” · \(when(ts, now: model.now))")
        case .earlier: Text("Asked earlier")
        }
    }

    private func firstWords(_ text: String) -> String {
        text.split(separator: "\n").map { $0.trimmingCharacters(in: .whitespaces) }.first { !$0.isEmpty } ?? ""
    }
}

/// The mark at a card's top right: a ring while it works, a dot and a word when it waits or failed, a check when done.
private struct StandMark: View {
    let stand: Stand

    var body: some View {
        switch stand {
        case .starting, .running:
            ProgressView().controlSize(.mini)
        case .waiting:
            HStack(spacing: 5) {
                Circle().fill(.orange).frame(width: 8, height: 8)
                Text(stand.words).font(.footnote).foregroundStyle(.orange)
            }
        case .done:
            Image(systemName: "checkmark").font(.footnote.weight(.semibold)).foregroundStyle(.green)
        case .failed:
            HStack(spacing: 5) {
                Circle().fill(.red).frame(width: 8, height: 8)
                Text(stand.words).font(.footnote).foregroundStyle(.red)
            }
        case .stopped:
            Text(stand.words).font(.footnote).foregroundStyle(.secondary)
        case .gone:
            EmptyView()
        }
    }
}

/// A sub-agent's small picture: a cat's head, Her's own being a cat.
private struct AgentTile: View {
    var size: CGFloat = 26

    var body: some View {
        Image(systemName: "cat").font(.system(size: size * 0.54, weight: .medium)).foregroundStyle(Color.accentColor)
            .frame(width: size, height: size)
            .background(Color.accentColor.opacity(0.12), in: RoundedRectangle(cornerRadius: size * 0.3, style: .continuous))
            .accessibilityHidden(true)
    }
}

/// How long and how much: "Running 4:12 · 55K tokens", "Took 13 min 7 s · 86K tokens".
private struct AgentFacts: View {
    @Environment(AppModel.self) private var model
    let agent: HerAgent

    var body: some View {
        let used = herAgentTokens(agent)
        let took = herAgentElapsed(agent, now: model.now.timeIntervalSince1970 * 1000)
        HStack(spacing: 4) {
            if agent.status == "running" {
                Text("Running \(Text(Date(timeIntervalSince1970: agent.since / 1000), style: .timer))")
            } else if took > 0 {
                Text("Took \(tookText(took))")
            }
            if used > 0 {
                if agent.status == "running" || took > 0 { Text(verbatim: "·") }
                Text("\(tokensText(UInt64(used))) tokens")
            }
        }
        .monospacedDigit()
    }
}

/// A card: who, how it stands, what the work is, what it does now or how it ended, how long and how many tokens.
/// A sub-agent's opens its record; a coding session's, the session's page.
private struct TaskCard: View {
    @Environment(AppModel.self) private var model
    let task: HerProgressTask
    let stand: Stand

    var body: some View {
        switch task {
        case .agent(let l):
            Button {
                if let a = l.agent { model.path.append(.progressAgent(a.id)) }
            } label: {
                card(icon: AgentTile(), doer: Text("Sub-agent"), title: herAgentTitle(l)) {
                    switch herAgentLine(l) {
                    case .tool(let t): StepWords(tool: t)
                    case .text(let t): Text(t)
                    case nil: stand == .starting ? Text("Starting…") : stand == .running ? Text("Thinking about the next step") : Text(verbatim: "")
                    }
                } facts: {
                    if let a = l.agent { AgentFacts(agent: a) }
                }
            }
            .buttonStyle(.plain)
            .disabled(l.agent == nil)
            .accessibilityIdentifier("progress-card-\(herAgentTitle(l))")
        case .session(_, let started):
            let s = model.session(started.sessionID)
            let agent = s?.agent ?? started.agent
            Button { model.openSession(started.sessionID) } label: {
                card(icon: AgentLogo(id: agent, size: 26), doer: Text(verbatim: "\(agentShortTitle(agent)) · \(s.map { model.machineName($0.machineID) } ?? started.machine)"),
                     title: s?.displayTitle ?? started.title) {
                    sessionLine(s)
                } facts: {
                    sessionFacts(s)
                }
            }
            .buttonStyle(.plain)
            .disabled(s == nil)
            .accessibilityIdentifier("progress-card-\(s?.displayTitle ?? started.title)")
        }
    }

    private func card(icon: some View, doer: Text, title: String, @ViewBuilder line: () -> some View, @ViewBuilder facts: () -> some View) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                icon
                doer.font(.footnote).foregroundStyle(.secondary).lineLimit(1)
                Spacer(minLength: 0)
                StandMark(stand: stand)
            }
            .frame(minHeight: 26)
            Text(title).font(.callout.weight(.semibold)).lineLimit(2).multilineTextAlignment(.leading)
            line().font(.subheadline).foregroundStyle(.secondary).lineLimit(2).multilineTextAlignment(.leading)
            facts().font(.caption).foregroundStyle(.secondary).lineLimit(1).padding(.top, 2)
        }
        .padding(.horizontal, 14).padding(.vertical, 13)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(.systemGroupedBackground), in: RoundedRectangle(cornerRadius: 20, style: .continuous))
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
    }

    @ViewBuilder private func sessionLine(_ s: Session?) -> some View {
        if let s {
            switch stand {
            case .waiting: Text("Waiting for an answer")
            case .failed: Text(s.progress.lastFailure.message.isEmpty ? String(localized: "Its last turn failed") : s.progress.lastFailure.message)
            case .running: Text(s.progress.currentTool).font(.footnote.monospaced())
            default:
                let d = s.progress.diffStat
                if d.filesChanged > 0 { Text(verbatim: "\(String(localized: "\(d.filesChanged) files")) · +\(d.insertions) −\(d.deletions)") } else { Text("Done") }
            }
        } else {
            Text(stand.words)
        }
    }

    @ViewBuilder private func sessionFacts(_ s: Session?) -> some View {
        let used = tokens(s)
        HStack(spacing: 4) {
            if let s, stand == .running, let start = s.turnStartedDate {
                Text("Running \(Text(start, style: .timer))")
                if used > 0 { Text(verbatim: "·") }
            }
            if used > 0 { Text("\(tokensText(UInt64(used))) tokens") }
        }
        .monospacedDigit()
    }
}

/// A call in words: what it did or does, and to what; a command as code.
private struct StepWords: View {
    let tool: HerToolStep
    var failed: Bool { tool.state == .failed || tool.state == .denied }

    var body: some View {
        HStack(spacing: 5) {
            Text(verb).foregroundStyle(failed ? AnyShapeStyle(.red) : AnyShapeStyle(.secondary)).layoutPriority(1)
            if tool.category == "execute" {
                CodeText(text: tool.object, lang: "bash").font(.footnote.monospaced())
            } else {
                Text(tool.object).foregroundStyle(.primary)
            }
        }
        .lineLimit(1)
    }

    /// "Read", "Running"; a refused one "Denied".
    private var verb: LocalizedStringKey {
        switch tool.state {
        case .denied: return "Denied"
        case .cancelled: return "Not run"
        case .awaiting: return "Needs approval:"
        default: break
        }
        let live = tool.state == .live
        switch tool.category {
        case "read": return live ? "Reading" : "Read"
        case "edit": return live ? "Editing" : "Edited"
        case "delete": return live ? "Deleting" : "Deleted"
        case "move": return live ? "Moving" : "Moved"
        case "search": return live ? "Searching" : "Searched"
        case "execute": return live ? "Running" : "Ran"
        case "fetch": return live ? "Fetching" : "Fetched"
        default: return live ? "Calling" : "Called"
        }
    }
}

// MARK: A sub-agent's record

/// A sub-agent's card opened: what set it off and its siblings at the top, what Her asked of it, then everything
/// it thought, said and called, in order, and how it ended. Its thoughts show whole; a call is a line that opens
/// to its output. Rows are made as they scroll into view, and a long record's earlier part is read when asked for.
struct ProgressAgentPage: View {
    @Environment(AppModel.self) private var model
    let agentID: String
    /// Every call's output shows.
    @State private var allOpen = false
    /// The rows the owner opened or folded, by step.
    @State private var chosen: [String: Bool] = [:]
    @State private var stopping = false

    var body: some View {
        if let machine = herPick(herEnvironments(model.machines), remembered: model.her.machine) {
            let chat = model.her.chat(machine.machineID, model: model)
            let found = find(chat.progress)
            Group {
                if let found, let agent = found.launch.agent {
                    record(chat, found.group, found.launch, agent)
                } else if chat.session == nil {
                    PageLoader(text: "Reading the conversation…", bare: true)
                } else {
                    ContentUnavailableView("This record is gone", systemImage: "tray")
                }
            }
            .background(Color(.systemGroupedBackground))
            .navigationBarTitleDisplayMode(.inline)
            .herConnections(chat, machine: machine)
        }
    }

    private func find(_ groups: [HerProgressGroup]) -> (group: HerProgressGroup, launch: HerLaunch)? {
        for g in groups {
            for case .agent(let l) in g.tasks where l.agent?.id == agentID { return (g, l) }
        }
        return nil
    }

    private func record(_ chat: HerChat, _ group: HerProgressGroup, _ launch: HerLaunch, _ agent: HerAgent) -> some View {
        let state = herAgentState(launch)
        let all = herSteps(agent.view)
        // Its first message is the task, which shows as what Her asked.
        let steps = { if case .said? = all.first?.kind { Array(all.dropFirst()) } else { all } }()
        let report = state == .done ? steps.last { if case .text = $0.kind { true } else { false } }?.key : nil
        let asking = chat.live.flatMap(herPendingApproval)
        let title = herAgentTitle(launch)
        return ScrollView {
            LazyVStack(alignment: .leading, spacing: 14) {
                parentLine(group)
                if group.tasks.count > 1 { siblings(group) }
                VStack(alignment: .leading, spacing: 8) {
                    Text(title).font(.title2.bold()).multilineTextAlignment(.leading)
                    HStack(spacing: 8) {
                        AgentTile(size: 22)
                        Text("Sub-agent").font(.subheadline).foregroundStyle(.secondary)
                        Text(Stand(state).words).font(.footnote)
                            .foregroundStyle(state == .waiting ? AnyShapeStyle(.orange) : state == .failed ? AnyShapeStyle(.red) : AnyShapeStyle(.secondary))
                            .padding(.horizontal, 10).frame(height: 24).background(Palette.fill2, in: .capsule)
                            .accessibilityIdentifier("progress-state")
                    }
                    AgentFacts(agent: agent).font(.subheadline).foregroundStyle(.secondary)
                        .accessibilityElement(children: .combine).accessibilityIdentifier("progress-facts")
                }
                .padding(.horizontal, 4)
                let brief = herAgentBrief(launch)
                if !brief.isEmpty {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("What Her asked").font(.footnote).foregroundStyle(.secondary)
                        Text(brief).font(.callout).textSelection(.enabled)
                    }
                    .padding(.horizontal, 14).padding(.vertical, 12)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 20, style: .continuous))
                    .accessibilityElement(children: .combine)
                    .accessibilityIdentifier("progress-brief")
                }
                HStack {
                    Text("Steps · \(herAgentCalls(agent))").font(.footnote).foregroundStyle(.secondary)
                    Spacer()
                    if herAgentCalls(agent) > 0 {
                        Button(allOpen ? "Thinking Only" : "Expand All Output") {
                            allOpen.toggle()
                            chosen = [:]
                        }
                        .font(.footnote)
                    }
                }
                .padding(.horizontal, 4).frame(minHeight: 30)
                if chat.convUp, herAgentHistoryCursor(agent) != nil {
                    HStack {
                        Spacer()
                        if chat.agentPaging.contains(agent.id) { ProgressView().controlSize(.small) } else {
                            Button("Earlier Steps") { Task { await chat.earlier(agent: agent.id) } }.font(.subheadline).accessibilityIdentifier("progress-earlier-steps")
                        }
                        Spacer()
                    }
                    .frame(minHeight: 44)
                }
                ForEach(steps) { step in
                    StepRow(step: step, report: step.key == report, open: Binding(
                        get: { chosen[step.key] ?? opensByDefault(step) },
                        set: { chosen[step.key] = $0 }))
                }
                switch state {
                case .waiting where asking?.agent?.id != agent.id:
                    Text("Another approval is ahead of this one; it shows here when its turn comes.").font(.footnote).foregroundStyle(.secondary).padding(.horizontal, 4)
                case .stopped:
                    Text("Stopped").font(.footnote).foregroundStyle(.secondary).padding(.horizontal, 4)
                default:
                    EmptyView()
                }
            }
            .padding(.horizontal, 16).padding(.top, 8).padding(.bottom, 40)
        }
        .accessibilityIdentifier("progress-record")
        // While it works the page begins at what it does now.
        .defaultScrollAnchor(agent.status == "running" ? .bottom : .top)
        .navigationTitle(title)
        .toolbar {
            if agent.status == "running", chat.convUp {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Stop") {
                        stopping = true
                        Task {
                            if await chat.op(["type": "agent_interrupt", "agent": .string(agent.id)]) { model.show(String(localized: "Asked “\(title)” to stop")) }
                            stopping = false
                        }
                    }
                    .disabled(stopping)
                    .accessibilityIdentifier("progress-stop")
                }
            }
        }
        // What it asks is answered here, as under Her's conversation.
        .safeAreaInset(edge: .bottom) {
            if let asking, asking.agent?.id == agent.id {
                HerApprovalPanel(chat: chat, approval: asking.approval, agent: asking.agent, waiting: asking.waiting).id(asking.approval.callID)
            }
        }
    }

    /// A thought shows whole; a call's output, once all are asked for.
    private func opensByDefault(_ s: HerStep) -> Bool {
        if case .thinking = s.kind { return true }
        return allOpen
    }

    private func parentLine(_ g: HerProgressGroup) -> some View {
        Group {
            switch g.parent {
            case .user(let text, let ts): Text("You · \(when(ts, now: model.now)): \(text)")
            case .routine(let name, let ts): Text("Scheduled task “\(name)” · \(when(ts, now: model.now))")
            case .earlier: Text("Asked earlier")
            }
        }
        .font(.footnote).foregroundStyle(.secondary).lineLimit(2).padding(.horizontal, 4)
    }

    /// The other cards of the same group, each with how it stands: a tap shows that one instead.
    private func siblings(_ g: HerProgressGroup) -> some View {
        ScrollView(.horizontal) {
            HStack(spacing: 8) {
                ForEach(g.tasks) { task in
                    let mine: Bool = { if case .agent(let l) = task { l.agent?.id == agentID } else { false } }()
                    Button {
                        switch task {
                        case .agent(let l):
                            if let id = l.agent?.id, id != agentID { model.path[model.path.count - 1] = .progressAgent(id) }
                        case .session(_, let started):
                            model.openSession(started.sessionID)
                        }
                    } label: {
                        HStack(spacing: 7) {
                            sibMark(stand(task, model))
                            Text({ switch task {
                            case .agent(let l): herAgentTitle(l)
                            case .session(_, let started): model.session(started.sessionID)?.displayTitle ?? started.title
                            } }()).lineLimit(1)
                        }
                        .font(.subheadline.weight(mine ? .semibold : .regular))
                        .foregroundStyle(mine ? AnyShapeStyle(Color(.systemBackground)) : AnyShapeStyle(.secondary))
                        .padding(.horizontal, 14).frame(height: 44)
                        .background(mine ? AnyShapeStyle(Color.primary) : AnyShapeStyle(Color(.secondarySystemGroupedBackground)), in: .capsule)
                    }
                    .buttonStyle(.plain)
                    .accessibilityAddTraits(mine ? .isSelected : [])
                }
            }
        }
        .scrollIndicators(.hidden)
        .scrollClipDisabled()
    }

    @ViewBuilder private func sibMark(_ s: Stand) -> some View {
        switch s {
        case .starting, .running: ProgressView().controlSize(.mini)
        case .waiting: Circle().fill(.orange).frame(width: 8, height: 8)
        case .done: Image(systemName: "checkmark").font(.caption.weight(.semibold)).foregroundStyle(.green)
        case .failed: Circle().fill(.red).frame(width: 8, height: 8)
        default: Circle().fill(.tertiary).frame(width: 8, height: 8)
        }
    }
}

/// A step of the record. report: the agent's last words, which are its report to Her.
private struct StepRow: View {
    let step: HerStep
    let report: Bool
    @Binding var open: Bool

    var body: some View {
        switch step.kind {
        case .thinking(let text, let redacted):
            VStack(alignment: .leading, spacing: 0) {
                fold(openable: !redacted) {
                    Text("Thought").foregroundStyle(.secondary).layoutPriority(1)
                    Text(redacted ? String(localized: "The provider keeps this reasoning private") : firstLine(text)).foregroundStyle(redacted ? .secondary : .primary)
                }
                if open, !redacted {
                    Text(text.trimmingCharacters(in: .whitespacesAndNewlines)).font(.subheadline).textSelection(.enabled)
                        .padding(.horizontal, 12).padding(.vertical, 10)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                        .padding(.bottom, 4)
                }
            }
        case .tool(let tool):
            VStack(alignment: .leading, spacing: 0) {
                fold(openable: !tool.output.isEmpty) {
                    if tool.state == .live { Spinner() }
                    StepWords(tool: tool)
                }
                if open, !tool.output.isEmpty {
                    OutputText(text: tool.output)
                        .padding(10)
                        .background(Palette.code, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                        .padding(.bottom, 4)
                        .accessibilityIdentifier("progress-output")
                }
            }
        case .text(let text):
            if report {
                VStack(alignment: .leading, spacing: 6) {
                    Label("Its report to Her", systemImage: "checkmark").font(.footnote).foregroundStyle(.secondary).labelStyle(.titleAndIcon)
                    MarkdownText(text: text)
                }
                .padding(14)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 20, style: .continuous))
                .accessibilityIdentifier("progress-report")
            } else {
                MarkdownText(text: text).padding(.horizontal, 4)
            }
        case .said(let text):
            VStack(alignment: .leading, spacing: 4) {
                Text("Her added").font(.footnote).foregroundStyle(.secondary)
                Text(text).font(.callout).textSelection(.enabled)
            }
            .padding(.horizontal, 14).padding(.vertical, 12)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 20, style: .continuous))
        case .ended(let outcome, let error):
            if outcome == "failed" {
                VStack(alignment: .leading, spacing: 6) {
                    HStack(spacing: 6) {
                        Circle().fill(.red).frame(width: 8, height: 8)
                        Text("It failed, and stopped here").font(.footnote).foregroundStyle(.red)
                    }
                    if let error { Text(error.message).font(.callout).textSelection(.enabled) }
                }
                .padding(14)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 20, style: .continuous))
            } else {
                Text("This round was interrupted").font(.footnote).foregroundStyle(.secondary).padding(.horizontal, 4)
            }
        }
    }

    /// A line that opens what is under it, tall enough to tap.
    private func fold(openable: Bool, @ViewBuilder _ words: () -> some View) -> some View {
        Button {
            if openable { withAnimation(.snappy(duration: 0.25)) { open.toggle() } }
        } label: {
            HStack(spacing: 6) {
                words()
                Spacer(minLength: 0)
                if openable { Caret(open: open) }
            }
            .font(.subheadline).lineLimit(1)
            .padding(.horizontal, 4)
            .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityValue(openable ? (open ? Text("Expanded") : Text("Collapsed")) : Text(""))
    }

    private func firstLine(_ text: String) -> String {
        text.split(separator: "\n").map { $0.trimmingCharacters(in: .whitespaces) }.first { !$0.isEmpty } ?? ""
    }
}
