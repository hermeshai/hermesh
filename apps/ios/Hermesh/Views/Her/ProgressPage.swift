import HermeshHer
import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI

// Progress (docs/her-team.md §15): the work Her sent out to her sub-agents as a feed, one post a piece of work, with
// what set it off at its foot; behind a post the whole record of that piece of work. Everything is read from the
// conversation the app already follows for Her (HerChat: her sub-agents' own conversations come with it), so the
// pages ask her for nothing more. Coding sessions she started are not here: they are on their own pages.

/// How a post stands: its sub-agent's state.
private enum Stand: Equatable {
    case starting, running, waiting, done, failed, stopped

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

    var atWork: Bool { self == .running || self == .starting }
    /// It waits for the owner, or could not go on: the posts that carry a mark, above the rest.
    var needsOwner: Bool { self == .waiting || self == .failed }

    /// In the app's language (a resource read with String(localized:) would follow the device's region instead).
    var words: String {
        switch self {
        case .starting: String(localized: "Starting")
        case .running: String(localized: "In progress")
        case .waiting: String(localized: "Needs your approval")
        case .done: String(localized: "Done")
        case .failed: String(localized: "Failed")
        case .stopped: String(localized: "Stopped")
        }
    }
}

/// How many of Her's sub-agents wait for the owner, and how many are at work: what marks Progress on her page.
@MainActor func herProgressMarks(_ chat: HerChat) -> (waiting: Int, running: Int) {
    let all = chat.progress.flatMap(\.tasks).map(stand)
    return (all.count(where: { $0 == .waiting }), all.count(where: \.atWork))
}

private func stand(_ t: HerProgressTask) -> Stand {
    switch t {
    case .agent(let l): Stand(herAgentState(l))
    }
}

/// "Took 13 min 7 s": a time in its two largest units.
private func tookText(_ ms: Double) -> String {
    Duration.seconds(max(1, (ms / 1000).rounded())).formatted(.units(allowed: [.hours, .minutes, .seconds], width: .narrow, maximumUnitCount: 2))
}

/// "21 min": how long something has been at work, in its largest unit.
private func workedText(_ ms: Double) -> String {
    Duration.seconds(max(1, (ms / 1000).rounded(.down))).formatted(.units(allowed: [.hours, .minutes, .seconds], width: .abbreviated, maximumUnitCount: 1))
}

private func when(_ ts: Double, now: Date) -> String { shortTime(Date(timeIntervalSince1970: ts / 1000), now: now) }

private func parentTime(_ g: HerProgressGroup) -> Double {
    switch g.parent {
    case .user(_, let ts), .routine(_, let ts): ts
    case .earlier: 0
    }
}

/// A piece of work as the feed shows it: the work, what set it off, how it stands, and what its post says (read
/// once, here).
private struct Post: Identifiable {
    var task: HerProgressTask
    var group: HerProgressGroup
    var stand: Stand
    var agent: HerAgentPost
    /// When it began its work (at work) or last changed (otherwise), in ms: the feed's order.
    var at: Double
    var id: String { task.id }

    var title: String {
        switch task {
        case .agent(let l): herAgentTitle(l)
        }
    }
}

private func posts(_ groups: [HerProgressGroup]) -> [Post] {
    groups.flatMap { g in
        g.tasks.map { t in
            switch t {
            case .agent(let l): Post(task: t, group: g, stand: stand(t), agent: herAgentPost(l), at: l.agent?.since ?? parentTime(g))
            }
        }
    }
}

/// What a sheet shows: the thing the owner said (a group's key).
private struct Source: Identifiable, Hashable { let id: String }

/// The page Progress on Her's page opens, pushed over her conversation: her environment as her page shows it.
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
        .background(Color(.systemBackground))
        .navigationTitle("Progress")
    }
}

/// The feed: what needs the owner, then what is at work now, then what ended; the latest first in each.
private struct ProgressBoard: View {
    @Environment(AppModel.self) private var model
    let machine: HerMachine
    @State private var source: Source?

    var body: some View {
        let chat = model.her.chat(machine.machineID, model: model)
        let all = posts(chat.progress).sorted { $0.at > $1.at }
        let need = all.filter(\.stand.needsOwner)
        let working = all.filter(\.stand.atWork)
        let ended = all.filter { !$0.stand.needsOwner && !$0.stand.atWork }
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 0) {
                if chat.session == nil {
                    notRead
                } else if all.isEmpty {
                    empty(connected: chat.convUp)
                }
                // One ForEach for the whole feed: a post that moves to another part keeps one identity, so the
                // lazy stack draws it as it is now (with a ForEach a part, it kept the row it had).
                ForEach(rows(need: need, working: working, ended: ended)) { row in
                    switch row {
                    case .part(let title):
                        Text(title).font(.footnote.weight(.semibold)).foregroundStyle(.secondary)
                            .padding(.top, 24)
                            .accessibilityAddTraits(.isHeader)
                    case .post(let p):
                        PostView(post: p, open: open, why: { source = Source(id: p.group.key) })
                    }
                }
                if chat.live?.before != nil, chat.convUp {
                    HStack {
                        Spacer()
                        if chat.paging { ProgressView().controlSize(.small) } else {
                            Button("Earlier Work") { Task { await chat.earlier() } }.font(.subheadline).accessibilityIdentifier("progress-earlier")
                        }
                        Spacer()
                    }
                    .frame(minHeight: 44).padding(.top, 8)
                }
            }
            .padding(.horizontal, 20).padding(.bottom, 40)
        }
        .herConnections(chat, machine: machine)
        .accessibilityIdentifier("progress")
        .sheet(item: $source) { s in
            SourceSheet(chat: chat, machineID: machine.machineID, key: s.id) { task in
                source = nil
                open(task)
            }
        }
    }

    private enum Row: Identifiable {
        case part(LocalizedStringKey)
        case post(Post)

        var id: String {
            switch self {
            case .part(let title): "part:\(title)"
            case .post(let p): p.id
            }
        }
    }

    /// Each part's title, then its posts; a part with none is left out.
    private func rows(need: [Post], working: [Post], ended: [Post]) -> [Row] {
        [("Needs you", need), ("Now", working), ("Earlier", ended)].flatMap { (title: LocalizedStringKey, posts: [Post]) in
            posts.isEmpty ? [] : [Row.part(title)] + posts.map(Row.post)
        }
    }

    private func open(_ task: HerProgressTask) {
        switch task {
        case .agent(let l): if let a = l.agent { model.path.append(.progressAgent(a.id)) }
        }
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

/// A post: its title, a few lines of how it goes, one piece that shows where it stands, and at its foot one time
/// (how long it has worked, or how long ago it ended) with the tokens it used, and ⓘ, which shows what set it off
/// in a sheet. Only one that needs the owner carries a mark.
private struct PostView: View {
    @Environment(AppModel.self) private var model
    let post: Post
    let open: (HerProgressTask) -> Void
    let why: () -> Void

    var body: some View {
        let words = post.agent.words
        VStack(alignment: .leading, spacing: 0) {
            Button { open(post.task) } label: {
                VStack(alignment: .leading, spacing: 4) {
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        if post.stand.needsOwner {
                            Circle().fill(post.stand == .waiting ? Color.orange : Color.red).frame(width: 8, height: 8)
                                .alignmentGuide(.firstTextBaseline) { $0.height + 2 }
                        }
                        Text(post.title).font(.headline).lineLimit(2).multilineTextAlignment(.leading)
                    }
                    if let words {
                        Text(words).font(.subheadline).foregroundStyle(.secondary).lineLimit(4).multilineTextAlignment(.leading)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .disabled(!canOpen)
            .accessibilityLabel([post.title, post.stand.words, words].compactMap { $0 }.joined(separator: ", "))
            .accessibilityIdentifier("progress-card-\(post.title)")
            piece
            HStack(spacing: 6) {
                PostTime(post: post)
                let used = tokensUsed
                if used > 0 {
                    Text(verbatim: "· \(tokensText(UInt64(used)))").font(.footnote).monospacedDigit().foregroundStyle(.tertiary)
                        .accessibilityLabel(Text("\(tokensText(UInt64(used))) tokens"))
                }
                Spacer(minLength: 8)
                Button(action: why) {
                    Image(systemName: "info.circle").font(.title3).foregroundStyle(.secondary)
                        .frame(width: 44, height: 44).contentShape(.rect)
                }
                .buttonStyle(.plain)
                .disabled(post.group.parent == .earlier)
                .accessibilityLabel(Text("Where it came from"))
                .accessibilityIdentifier("progress-why")
            }
            .padding(.top, 2)
        }
        .padding(.top, 16)
        .overlay(alignment: .bottom) { Rectangle().fill(Palette.separator).frame(height: 0.5) }
    }

    private func openThis() { open(post.task) }

    private var canOpen: Bool {
        switch post.task {
        case .agent(let l): l.agent != nil
        }
    }

    private var tokensUsed: Int {
        switch post.task {
        case .agent(let l): l.agent.map(herAgentTokens) ?? 0
        }
    }

    /// What shows where it stands: the step it is on while it works, what waits for the owner, what it made.
    @ViewBuilder private var piece: some View {
        switch (post.task, post.stand) {
        case (.agent, .starting):
            Well { Text("Starting…").foregroundStyle(.secondary) }
        case (.agent, .running):
            Button(action: openThis) {
                Well {
                    HStack(spacing: 12) {
                        StepBeat(count: post.agent.calls)
                        if let now = post.agent.now { StepWords(tool: now) } else { Text("Thinking about the next step").foregroundStyle(.secondary) }
                    }
                }
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("progress-now")
        case (.agent, .waiting):
            Well {
                HStack(spacing: 12) {
                    Text("Needs your approval").foregroundStyle(.orange)
                    Spacer(minLength: 0)
                    Button("Review", action: openThis).buttonStyle(.glassProminent).controlSize(.small).accessibilityIdentifier("progress-review")
                }
            }
        case (.agent, .done):
            if let first = post.agent.files.first {
                let files = post.agent.files
                Button(action: openThis) {
                    Well {
                        HStack(spacing: 8) {
                            Image(systemName: "doc").foregroundStyle(.secondary).accessibilityHidden(true)
                            Text((first as NSString).lastPathComponent).lineLimit(1)
                            Spacer(minLength: 0)
                            if files.count > 1 { Text("\(files.count) files").foregroundStyle(.secondary) }
                        }
                    }
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("progress-files")
            }
        default:
            EmptyView()
        }
    }
}

/// The well a post's piece sits in.
private struct Well<Content: View>: View {
    @ViewBuilder let content: Content

    var body: some View {
        content
            .font(.subheadline)
            .padding(.horizontal, 14).padding(.vertical, 12)
            .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
            .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 16, style: .continuous))
            .contentShape(.rect)
            .padding(.top, 12)
    }
}

/// A sub-agent's calls as bars, the last beating while it works: at most the last eight.
private struct StepBeat: View {
    let count: Int
    @Environment(\.accessibilityReduceMotion) private var still
    @State private var low = false
    private static let heights: [CGFloat] = [8, 12, 8, 16, 11, 8, 16, 12, 16, 11, 8, 14]

    var body: some View {
        let n = min(max(count, 1), 8)
        HStack(alignment: .bottom, spacing: 3) {
            ForEach(0..<n, id: \.self) { i in
                let last = i == n - 1
                Capsule().fill(Color.accentColor.opacity(last ? 1 : 0.34))
                    .frame(width: 4, height: Self.heights[(count - n + i + Self.heights.count) % Self.heights.count])
                    .scaleEffect(x: 1, y: last && low ? 0.45 : 1, anchor: .bottom)
            }
        }
        .frame(height: 18, alignment: .bottom)
        .accessibilityHidden(true)
        .onAppear {
            guard !still else { return }
            withAnimation(.easeInOut(duration: 0.9).repeatForever(autoreverses: true)) { low = true }
        }
    }
}

/// The time at a post's foot: while it works, how long it has, in the accent; else how long ago it ended.
private struct PostTime: View {
    @Environment(AppModel.self) private var model
    let post: Post

    var body: some View {
        let now = model.now.timeIntervalSince1970 * 1000
        Group {
            if post.stand.atWork || post.stand == .waiting {
                let since: Double = switch post.task {
                case .agent(let l): l.agent?.since ?? post.at
                }
                Text(workedText(now - since)).foregroundStyle(post.stand == .waiting ? AnyShapeStyle(.secondary) : AnyShapeStyle(Color.accentColor))
            } else {
                Text(agoText(Date(timeIntervalSince1970: post.at / 1000), now: model.now)).foregroundStyle(.tertiary)
            }
        }
        .font(.footnote).monospacedDigit().lineLimit(1)
    }
}

/// What set the work off, at a post's foot: a tap shows it whole. The tokens the work used beside it.
private struct FromLine: View {
    @Environment(AppModel.self) private var model
    let group: HerProgressGroup
    let tokens: Int
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 8) {
                HStack(spacing: 6) {
                    Image(systemName: "text.bubble").font(.caption).foregroundStyle(.tertiary).accessibilityHidden(true)
                    origin.lineLimit(1)
                }
                .padding(.leading, 9).padding(.trailing, 11).frame(height: 28)
                .background(Color(.secondarySystemBackground), in: .capsule)
                Spacer(minLength: 8)
                if tokens > 0 {
                    Text(verbatim: tokensText(UInt64(tokens))).foregroundStyle(.tertiary).monospacedDigit()
                        .accessibilityLabel(Text("\(tokensText(UInt64(tokens))) tokens"))
                }
            }
            .font(.footnote).foregroundStyle(.secondary)
            .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .disabled(group.parent == .earlier)
        .accessibilityIdentifier("progress-from")
    }

    private var origin: Text {
        switch group.parent {
        case .user(let text, let ts): Text("You \(when(ts, now: model.now)) · \(text)")
        case .routine(let name, let ts): Text("Scheduled task “\(name)” · \(when(ts, now: model.now))")
        case .earlier: Text("Asked earlier")
        }
    }
}

/// What set work off, shown whole in a sheet: what the owner said and when, what Her said back, and everything it
/// sent out, each as it stands; a tap opens one.
private struct SourceSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let chat: HerChat
    let machineID: String
    let key: String
    /// Here: the piece of work being looked at, marked as such.
    var here: String? = nil
    let open: (HerProgressTask) -> Void

    var body: some View {
        let group = chat.progress.first { $0.key == key }
        NavigationStack {
            ScrollView {
                if let group { content(group) }
            }
            .navigationTitle(group.map(title) ?? "")
            .navigationSubtitle(group.map(subtitle) ?? "")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) { Button(role: .close) { dismiss() } }
            }
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
        .accessibilityIdentifier("progress-source")
    }

    private func title(_ g: HerProgressGroup) -> String {
        switch g.parent {
        case .user: String(localized: "What you said")
        case .routine: String(localized: "What set it off")
        case .earlier: ""
        }
    }

    private func subtitle(_ g: HerProgressGroup) -> String {
        switch g.parent {
        case .user(_, let ts): String(localized: "\(when(ts, now: model.now)) · to Her")
        case .routine(let name, let ts): String(localized: "Scheduled task “\(name)” · \(when(ts, now: model.now))")
        case .earlier: ""
        }
    }

    private func content(_ g: HerProgressGroup) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            if case .user(let text, _) = g.parent {
                Text(text).font(.body).foregroundStyle(Palette.bubbleText)
                    .padding(.horizontal, 15).padding(.vertical, 10)
                    .background(Palette.bubble, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
                    .frame(maxWidth: .infinity, alignment: .trailing).padding(.leading, 40)
                    .textSelection(.enabled)
            }
            if let said = g.said {
                HStack(alignment: .bottom, spacing: 8) {
                    HerAvatar(size: 26)
                    Text(said).font(.body)
                        .padding(.horizontal, 15).padding(.vertical, 10)
                        .background(Palette.reply, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
                        .textSelection(.enabled)
                }
                .padding(.trailing, 40)
            }
            if g.tasks.count > 1 {
                Text("This sent out \(g.tasks.count)").font(.footnote).foregroundStyle(.secondary).padding(.horizontal, 8).padding(.top, 6)
                VStack(spacing: 0) {
                    ForEach(g.tasks) { task in row(task) }
                }
                .padding(4)
                .background(Color(.secondarySystemBackground).opacity(0.6), in: RoundedRectangle(cornerRadius: 22, style: .continuous))
            }
            if let run = g.run {
                Button {
                    dismiss()
                    model.path.append(.routineRun(run.routineID, run.runID))
                } label: {
                    Text("Open This Run").frame(maxWidth: .infinity)
                }
                .buttonStyle(.glass).controlSize(.large)
                .padding(.top, 4)
                .accessibilityIdentifier("progress-open-run")
            }
            Button {
                model.openHer(machineID: machineID)
            } label: {
                Text("Open the Conversation").frame(maxWidth: .infinity)
            }
            .buttonStyle(.glass).controlSize(.large)
            .padding(.top, 4)
        }
        .padding(.horizontal, 16).padding(.top, 8).padding(.bottom, 20)
    }

    private func row(_ task: HerProgressTask) -> some View {
        let s = stand(task)
        let mine = task.id == here
        let title: String = switch task {
        case .agent(let l): herAgentTitle(l)
        }
        return Button { if !mine { open(task) } } label: {
            HStack(spacing: 10) {
                StandMark(stand: s).frame(width: 18)
                VStack(alignment: .leading, spacing: 1) {
                    Text(title).font(.body.weight(mine ? .semibold : .regular)).lineLimit(1)
                    Text(s.words).font(.footnote).foregroundStyle(.secondary).lineLimit(1)
                }
                Spacer(minLength: 0)
                if mine { Text("Here").font(.caption).foregroundStyle(.secondary).padding(.horizontal, 8).padding(.vertical, 2).background(Palette.fill2, in: .capsule) }
                Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(.tertiary).accessibilityHidden(true)
            }
            .padding(.horizontal, 12).frame(minHeight: 52)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(mine ? .isSelected : [])
    }
}

/// A small mark of how work stands: a ring while it works, a dot when it waits or failed, a check when done.
private struct StandMark: View {
    let stand: Stand

    var body: some View {
        switch stand {
        case .starting, .running: ProgressView().controlSize(.mini)
        case .waiting: Circle().fill(.orange).frame(width: 8, height: 8)
        case .done: Image(systemName: "checkmark").font(.caption.weight(.semibold)).foregroundStyle(.green)
        case .failed: Circle().fill(.red).frame(width: 8, height: 8)
        default: Circle().fill(.tertiary).frame(width: 8, height: 8)
        }
    }
}

/// A call in words: what it did or does, and to what; a command as code.
private struct StepWords: View {
    let tool: HerToolStep
    var failed: Bool { tool.state == .failed || tool.state == .denied }

    var body: some View {
        HStack(spacing: 5) {
            Text(herStepVerb(tool)).foregroundStyle(failed ? AnyShapeStyle(.red) : AnyShapeStyle(.secondary)).layoutPriority(1)
            if tool.category == "execute" {
                CodeText(text: tool.object, lang: "bash").font(.footnote.monospaced())
            } else {
                Text(tool.object).foregroundStyle(.primary)
            }
        }
        .lineLimit(1)
    }
}

/// What a step does, as Progress and Her's busy bubble say it: "Reading", "Ran"; a refused one "Denied".
func herStepVerb(_ tool: HerToolStep) -> String {
    switch tool.state {
    case .denied: return String(localized: "Denied")
    case .cancelled: return String(localized: "Not run")
    case .awaiting: return String(localized: "Needs approval:")
    default: break
    }
    let live = tool.state == .live
    switch tool.category {
    case "read": return live ? String(localized: "Reading") : String(localized: "Read")
    case "edit": return live ? String(localized: "Editing") : String(localized: "Edited")
    case "delete": return live ? String(localized: "Deleting") : String(localized: "Deleted")
    case "move": return live ? String(localized: "Moving") : String(localized: "Moved")
    case "search": return live ? String(localized: "Searching") : String(localized: "Searched")
    case "execute": return live ? String(localized: "Running") : String(localized: "Ran")
    case "fetch": return live ? String(localized: "Fetching") : String(localized: "Fetched")
    default: return live ? String(localized: "Calling") : String(localized: "Called")
    }
}

// MARK: A sub-agent's record

/// A sub-agent's post opened: its title and how it stands, how long, how many tokens and steps, what set it off,
/// what Her asked of it, then everything it thought, said and called, in order, and how it ended. Its thoughts
/// show whole; a call is a line that opens to its output. Rows are made as they scroll into view, and a long
/// record's earlier part is read when asked for.
struct ProgressAgentPage: View {
    @Environment(AppModel.self) private var model
    let agentID: String
    /// Every call's output shows.
    @State private var allOpen = false
    /// The rows the owner opened or folded, by step.
    @State private var chosen: [String: Bool] = [:]
    @State private var stopping = false
    @State private var source: Source?

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
            .sheet(item: $source) { s in
                SourceSheet(chat: chat, machineID: machine.machineID, key: s.id, here: found?.launch.callID) { task in
                    source = nil
                    switch task {
                    case .agent(let l):
                        if let id = l.agent?.id, id != agentID { model.path[model.path.count - 1] = .progressAgent(id) }
                    }
                }
            }
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
        let calls = herAgentCalls(agent)
        return ScrollView {
            LazyVStack(alignment: .leading, spacing: 12) {
                VStack(alignment: .leading, spacing: 8) {
                    Text(title).font(.title2.bold()).multilineTextAlignment(.leading)
                    HStack(spacing: 8) {
                        HStack(spacing: 6) {
                            if state == .running || state == .starting { ProgressView().controlSize(.mini) }
                            Text(Stand(state).words)
                        }
                        .font(.footnote)
                        .foregroundStyle(state == .waiting ? AnyShapeStyle(.orange) : state == .failed ? AnyShapeStyle(.red) : state == .running ? AnyShapeStyle(Color.accentColor) : AnyShapeStyle(.secondary))
                        .padding(.horizontal, 10).frame(height: 26).background(Color(.secondarySystemGroupedBackground), in: .capsule)
                        .accessibilityElement(children: .combine)
                        .accessibilityIdentifier("progress-state")
                        Text("Sub-agent").font(.subheadline).foregroundStyle(.secondary)
                    }
                }
                .padding(.horizontal, 4)
                Facts(agent: agent, calls: calls)
                FromLine(group: group, tokens: 0) { source = Source(id: group.key) }
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
                    Text("Steps").font(.footnote.weight(.semibold)).foregroundStyle(.secondary)
                    Spacer()
                    if calls > 0 {
                        Button(allOpen ? "Thinking Only" : "Expand All Output") {
                            allOpen.toggle()
                            chosen = [:]
                        }
                        .font(.footnote)
                    }
                }
                .padding(.horizontal, 4).padding(.top, 6).frame(minHeight: 30)
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
}

/// How long, how many tokens, how many steps: three tiles.
private struct Facts: View {
    @Environment(AppModel.self) private var model
    let agent: HerAgent
    let calls: Int

    var body: some View {
        let running = agent.status == "running"
        let took = herAgentElapsed(agent, now: model.now.timeIntervalSince1970 * 1000)
        HStack(spacing: 8) {
            tile(running ? "Running for" : "Took") {
                if running { Text(Date(timeIntervalSince1970: agent.since / 1000), style: .timer) } else { Text(took > 0 ? tookText(took) : "—") }
            }
            tile("Tokens") { Text(verbatim: tokensText(UInt64(herAgentTokens(agent)))) }
            tile("Steps") { Text(verbatim: "\(calls)") }
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("progress-facts")
    }

    private func tile(_ label: LocalizedStringKey, @ViewBuilder value: () -> some View) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(label).font(.caption).foregroundStyle(.secondary)
            value().font(.title3.weight(.semibold)).monospacedDigit().lineLimit(1).minimumScaleFactor(0.7)
        }
        .padding(.horizontal, 12).padding(.vertical, 10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 16, style: .continuous))
    }
}

/// A step of the record. report: the agent's last words, which are its report to Her.
struct StepRow: View {
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
