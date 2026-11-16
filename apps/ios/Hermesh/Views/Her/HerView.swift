import HermeshHer
import HermeshKit
import HermeshSessions
import SwiftUI

/// The Her entry (docs/her-server.md §9): the conversation with Her, the
/// environment's assistant, as the whole page, under her mark and her name;
/// a tap on them opens her settings, where her environments are. The first
/// run while Her runs nowhere.
struct HerView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let envs = herEnvironments(model.machines)
        if let m = herPick(envs, remembered: model.her.machine) {
            HerPage(machine: m).id(m.machineID)
        } else if model.hasWorld {
            HerFirstRun()
        } else { // the machines are not read yet: whether she runs is not known
            PageLoader(text: "Reading your machines…").toolbar { MenuToolbarItem() }
        }
    }
}

/// One environment's Her: the menu button, her mark large in the middle with her name tag over its lower edge,
/// Progress at the right (the work she sent out, docs/her-team.md §15), and the conversation scrolling under them,
/// fading as it reaches them. The tag says what the team is doing (docs/her-team.md §4.2) and opens the team panel;
/// her mark opens her settings, or closes the panel while it shows. While the keyboard is up her mark is small, in
/// the row of buttons beside a short tag, and the conversation has the room (docs/her-server.md §9).
private struct HerPage: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var phase
    @Environment(\.accessibilityReduceMotion) private var still
    /// With the bars on the side, the menu button is in the system's bar, as on every page; the top is still hers.
    @Environment(\.barsOnTheSide) private var sideBars
    let machine: HerMachine
    /// The team panel shows.
    @State private var team = false
    /// The keyboard is up.
    @State private var typing = false
    /// How tall the status bar is, where the conversation fades out.
    @State private var statusBar: CGFloat = 0

    var body: some View {
        let chat = model.her.chat(machine.machineID, model: model)
        let state = herState(machine)
        Group {
            if chat.bot != nil {
                HerConversation(chat: chat, machine: machine, state: state)
            } else {
                HerStatus(machine: machine, state: state)
            }
        }
        // The whole page whatever shows: her mark at the top and the panel's dim over all of it, not around a
        // status as tall as its words.
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        // Scrolled up, the conversation fades out under her mark and the buttons (the bar's own edge blurs it there)
        // and is gone by the status bar: the clock is never read over a message.
        .mask {
            VStack(spacing: 0) {
                Color.clear.frame(height: statusBar)
                LinearGradient(colors: [.clear, .black], startPoint: .top, endPoint: .bottom).frame(height: 28)
                Color.black
            }
            .ignoresSafeArea()
        }
        // Over the conversation and its composer, under her mark: the panel grows from where the tag is.
        .overlay(alignment: .top) {
            if team {
                ZStack(alignment: .top) {
                    Color.black.opacity(0.22).ignoresSafeArea()
                        .onTapGesture { showTeam(false) }
                        .accessibilityElement()
                        .accessibilityLabel("Close")
                        .accessibilityAddTraits(.isButton)
                        .accessibilityAction { showTeam(false) }
                        .accessibilityIdentifier("team-close")
                    TeamPanel { showTeam(false) }
                        .padding(.top, -tagHeight - 4)
                        .transition(still ? .opacity : .scale(scale: 0.3, anchor: .top).combined(with: .opacity))
                }
                .transition(.opacity)
            }
        }
        .toolbar(sideBars ? .visible : .hidden, for: .navigationBar)
        // With the bars on the side (iPhone Duo), Progress is in the system's bar too.
        .toolbar {
            if sideBars {
                MenuToolbarItem()
                if let bot = chat.bot {
                    ToolbarItem(placement: .topBarTrailing) { HerProgressButton(waiting: herProgressWaiting(chat, model), inBar: true) }
                    ToolbarItem(placement: .topBarTrailing) { HerRoutinesButton(dot: herRoutineDot(bot.routines), inBar: true) }
                }
            }
        }
        .safeAreaBar(edge: .top) { header(chat) }
        // Outside the bar, the top inset is the status bar's (or the system's bar's, beside the menu).
        .onGeometryChange(for: CGFloat.self) { $0.safeAreaInsets.top } action: { statusBar = $0 }
        .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillShowNotification)) { _ in setTyping(true) }
        .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillHideNotification)) { _ in setTyping(false) }
        // A row opened, or a sheet: the panel is done.
        .onChange(of: model.path) { team = false }
        .onChange(of: model.sheet) { team = false }
        .onChange(of: model.drawerOpen) { team = false }
        .onAppear { model.her.machine = machine.machineID }
        .herConnections(chat, machine: machine)
        // Nothing is unread while the owner looks.
        .task(id: [chat.bot?.unread ?? 0, chat.envUp ? 1 : 0, phase == .active ? 1 : 0]) { if phase == .active { await chat.markRead() } }
    }

    private func header(_ chat: HerChat) -> some View {
        // Typing, her mark gives the conversation its room; never while the panel hangs from the tag.
        let small = typing && !team
        return ZStack(alignment: .top) {
            HStack {
                if !sideBars && !model.menuBeside { HerMenuButton() }
                Spacer()
                if let bot = chat.bot, !sideBars {
                    HerProgressButton(waiting: herProgressWaiting(chat, model))
                    HerRoutinesButton(dot: herRoutineDot(bot.routines))
                }
            }
            if small {
                // In the row, between the buttons and clear of them (as wide a margin each side, so in the middle).
                HStack(spacing: 6) {
                    avatar(chat, size: 40)
                    tag(chat, short: true)
                }
                .frame(maxWidth: .infinity, minHeight: 44)
                .padding(.horizontal, 104)
            } else {
                VStack(spacing: -16) {
                    avatar(chat, size: 72).zIndex(1)
                    tag(chat, short: false)
                }
            }
        }
        // The tag's words grow with the text size, up to what the top has room for.
        .dynamicTypeSize(...DynamicTypeSize.xxLarge)
        .padding(.horizontal, 16).padding(.bottom, 4)
        .animation(still ? nil : .snappy(duration: 0.3), value: small)
    }

    private func avatar(_ chat: HerChat, size: CGFloat) -> some View {
        Button {
            // Over the panel, it closes it: the settings would open from under it.
            if team { showTeam(false) } else { model.path.append(.herSettings) }
        } label: {
            HerAvatar(size: size, busy: chat.live.map(herBusy) ?? false)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(Text(chat.bot?.name ?? "Her"))
        .accessibilityHint(team ? Text("Closes the team panel") : Text("Opens Her’s settings"))
        .accessibilityIdentifier("her-avatar")
        .matchedGeometryEffect(id: "avatar", in: top)
    }

    private func tag(_ chat: HerChat, short: Bool) -> some View {
        Button {
            showTeam(true)
        } label: {
            HerTag(name: chat.bot?.name ?? "Her", team: teamState(model.sessions, requests: model.pairings.count), paused: phase != .active, short: short)
                .frame(height: tagHeight)
                .padding(.horizontal, short ? 12 : 14)
                .glassEffect(.regular.interactive(), in: .capsule)
        }
        .buttonStyle(.plain)
        .accessibilityHint(Text("Shows what your agents are doing"))
        .accessibilityIdentifier("her-tag")
        // The panel takes its place.
        .opacity(team ? 0 : 1)
        .allowsHitTesting(!team)
        .matchedGeometryEffect(id: "tag", in: top)
    }

    @Namespace private var top
    private let tagHeight: CGFloat = 36

    private func setTyping(_ on: Bool) {
        guard typing != on else { return }
        withAnimation(still ? nil : .snappy(duration: 0.3)) { typing = on }
    }

    private func showTeam(_ on: Bool) {
        if on { UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil) }
        withAnimation(still ? .easeOut(duration: 0.15) : .snappy(duration: 0.36, extraBounce: 0.08)) { team = on }
    }
}

/// Her's name tag: her name and, beside it, the team's state, the first that holds (TeamState.Tag): one session
/// that waits, by its coding agent; how many things wait; how many sessions failed; how many run, with their
/// agents' logos. While something needs the owner and sessions also run, their number follows, at a ring. Short
/// (while the keyboard is up), her name and the state's dot or ring alone; VoiceOver reads the words either way.
private struct HerTag: View {
    @Environment(\.accessibilityReduceMotion) private var still
    let name: String
    let team: TeamState
    /// Nothing turns while the app is not in front.
    let paused: Bool
    var short = false

    var body: some View {
        HStack(spacing: 7) {
            Text(name).font(.headline)
            if short {
                switch team.tag {
                case .waiting, .needYou: dot(.orange)
                case .failed: dot(.red)
                case .working: runningRing
                case .quiet: EmptyView()
                }
            } else if team.tag != .quiet {
                Capsule().fill(.tertiary).frame(width: 1, height: 14)
                switch team.tag {
                case .waiting(let agent):
                    dot(.orange)
                    Text("\(agentShortTitle(agent)) is waiting").foregroundStyle(.orange)
                case .needYou(let n):
                    dot(.orange)
                    Text("\(n) need you").foregroundStyle(.orange)
                case .failed(let n):
                    dot(.red)
                    Text("\(n) failed")
                case .working(let n):
                    HStack(spacing: -5) {
                        ForEach(team.runningAgents.prefix(3), id: \.self) { AgentLogo(id: $0, size: 18) }
                    }
                    Text("\(n) working").foregroundStyle(.secondary)
                case .quiet:
                    EmptyView()
                }
                if !team.running.isEmpty {
                    if case .working = team.tag {} else {
                        Text(verbatim: "\(team.running.count)").foregroundStyle(.secondary).monospacedDigit()
                    }
                    runningRing
                }
            }
        }
        .font(.subheadline.weight(.medium))
        .lineLimit(1)
        .animation(still ? nil : .snappy, value: team.tag)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(verbatim: spoken))
    }

    /// What the tag says, in words.
    private var spoken: String {
        var parts = [name]
        switch team.tag {
        case .waiting(let agent): parts.append(String(localized: "\(agentShortTitle(agent)) is waiting"))
        case .needYou(let n): parts.append(String(localized: "\(n) need you"))
        case .failed(let n): parts.append(String(localized: "\(n) failed"))
        case .working(let n): parts.append(String(localized: "\(n) working"))
        case .quiet: break
        }
        if case .working = team.tag {} else if team.tag != .quiet && !team.running.isEmpty { parts.append("\(team.running.count)") }
        return parts.joined(separator: ", ")
    }

    private func dot(_ color: Color) -> some View { Circle().fill(color).frame(width: 9, height: 9) }

    /// Like Her's avatar, draw the continuous motion without a never-ending UIKit animation.
    private var runningRing: some View {
        TimelineView(.animation(minimumInterval: 1 / 30, paused: paused || still)) { timeline in
            let angle = paused || still ? 0 : timeline.date.timeIntervalSinceReferenceDate.truncatingRemainder(dividingBy: 1) * 360
            Canvas { context, size in
                var arc = Path()
                arc.addArc(center: CGPoint(x: size.width / 2, y: size.height / 2), radius: 5,
                           startAngle: .degrees(angle), endAngle: .degrees(angle + 270), clockwise: false)
                context.stroke(arc, with: .color(.secondary), style: StrokeStyle(lineWidth: 1.5, lineCap: .round))
            }
        }
        .frame(width: 12, height: 12)
        .accessibilityHidden(true)
    }
}

/// The team panel (docs/her-team.md §4.3): what needs the owner, answered in place where one tap can, what
/// runs and what ended last, on glass under Her's mark; its head leads to Work.
private struct TeamPanel: View {
    @Environment(AppModel.self) private var model
    let close: () -> Void

    var body: some View {
        let team = teamState(model.sessions, requests: model.pairings.count)
        let today = model.work[.today]?.sessions
        // As tall as what it lists, and scrolling once that is taller than the page.
        ViewThatFits(in: .vertical) {
            content(team, today: today)
            ScrollView { content(team, today: today) }.scrollIndicators(.hidden)
        }
        .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 34, style: .continuous))
        // Short of the composer's row however much it lists: the dim to tap stays in sight on a small iPhone.
        .padding(.horizontal, 10).padding(.bottom, 88)
        .task { await model.loadWork(.today) }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("team-panel")
    }

    private func content(_ team: TeamState, today: Int?) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline) {
                Text("Team").font(.title2.bold())
                Spacer()
                Button {
                    close()
                    model.open(.overview)
                } label: {
                    HStack(spacing: 3) {
                        if let today { Text("Today · \(today) sessions") } else { Text("Work") }
                        Image(systemName: "chevron.right").font(.caption2.weight(.semibold))
                    }
                    .font(.subheadline).foregroundStyle(.secondary)
                    .frame(minHeight: 44).contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("team-work")
            }
            .padding(.horizontal, 8)
            .contentShape(.rect)
            .simultaneousGesture(DragGesture(minimumDistance: 20).onEnded { drag in
                if drag.translation.height < -30 && abs(drag.translation.height) > abs(drag.translation.width) { close() }
            })
            if team.waiting.isEmpty && team.running.isEmpty && team.failed.isEmpty && model.pairings.isEmpty {
                Text("Nothing is running now.").font(.callout).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity).padding(.vertical, 18)
            }
            TeamLists(team: team, pairings: model.pairings, finished: 2, finishedTitle: "Just Finished", fill: AnyShapeStyle(Color(.secondarySystemGroupedBackground).opacity(0.72)))
        }
        .padding(.horizontal, 12).padding(.top, 30).padding(.bottom, 14)
    }
}

/// Her's connections while a page of hers shows the environment (docs/her-server.md §5.1): the environment's,
/// while her runs there; the conversation's, opened again when what it follows changes (a cleared conversation is a
/// new one; a model just connected opens it); and what this iPhone keeps. The pages hold them one after another
/// (HerChat.hold), so going from her conversation to Progress, a card's record or her settings opens nothing again.
private struct HerConnections: ViewModifier {
    let chat: HerChat
    let machine: HerMachine

    func body(content: Content) -> some View {
        let running = herReachable(machine) && machine.her.running
        let following = HerChat.Following(bot: chat.bot?.id, session: chat.bot?.sessionID, running: running, modelReady: machine.her.modelReady)
        content
            .task(id: following) { await chat.hold(following) }
            .task { await chat.loadKept() }
            .task(id: herReachable(machine)) { await chat.refreshWaiting() }
            // Delivered meanwhile: what waits is asked for again as the conversation grows.
            .onChange(of: chat.live?.entries.count) { if !chat.waiting.isEmpty { Task { await chat.refreshWaiting() } } }
    }
}

extension View {
    func herConnections(_ chat: HerChat, machine: HerMachine) -> some View { modifier(HerConnections(chat: chat, machine: machine)) }
}

/// Progress, at the top right of Her's page, or in the system's bar where it stands on the side (inBar): the work
/// she sent out, marked while a piece of it waits for the owner.
private struct HerProgressButton: View {
    @Environment(AppModel.self) private var model
    let waiting: Int
    var inBar = false

    var body: some View {
        Group {
            if inBar {
                // The bar draws the button, as it does the menu button beside it.
                Button { model.path.append(.progress) } label: {
                    Image(systemName: "waveform.path.ecg")
                        .overlay(alignment: .topTrailing) {
                            if waiting > 0 { Circle().fill(.orange).frame(width: 8, height: 8).offset(x: 5, y: -4) }
                        }
                }
            } else {
                Button { model.path.append(.progress) } label: {
                    Image(systemName: "waveform.path.ecg").font(.body.weight(.medium)).frame(width: 44, height: 44)
                        .overlay(alignment: .topTrailing) {
                            if waiting > 0 { Circle().fill(.orange).frame(width: 8, height: 8).offset(x: -9, y: 11) }
                        }
                        .glassEffect(.regular.interactive(), in: .circle)
                        .contentShape(.circle)
                }
                .buttonStyle(.plain)
            }
        }
        .accessibilityLabel(waiting > 0 ? Text("Progress, \(waiting) waiting for you") : Text("Progress"))
        .accessibilityIdentifier("her-progress")
    }
}

/// Her's scheduled tasks, beside Progress: marked while one's last run failed (orange) or one runs (blue).
private struct HerRoutinesButton: View {
    @Environment(AppModel.self) private var model
    let dot: HerRoutineDot?
    var inBar = false

    var body: some View {
        Group {
            if inBar {
                Button { model.path.append(.routines) } label: {
                    Image(systemName: "clock").overlay(alignment: .topTrailing) { mark.offset(x: 5, y: -4) }
                }
            } else {
                Button { model.path.append(.routines) } label: {
                    Image(systemName: "clock").font(.body.weight(.medium)).frame(width: 44, height: 44)
                        .overlay(alignment: .topTrailing) { mark.offset(x: -9, y: 11) }
                        .glassEffect(.regular.interactive(), in: .circle)
                        .contentShape(.circle)
                }
                .buttonStyle(.plain)
            }
        }
        .accessibilityLabel(dot == .failed ? Text("Scheduled Tasks, a last run failed") : dot == .running ? Text("Scheduled Tasks, one running") : Text("Scheduled Tasks"))
        .accessibilityIdentifier("her-routines")
    }

    @ViewBuilder private var mark: some View {
        if let dot { Circle().fill(dot == .failed ? Color.orange : Color.accentColor).frame(width: 8, height: 8) }
    }
}

/// The menu button, as root pages have it in their bar: Her's page draws its own top.
private struct HerMenuButton: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Button {
            model.drawerOpen = true
        } label: {
            Image(systemName: "line.3.horizontal").font(.body.weight(.medium)).frame(width: 44, height: 44)
                .overlay(alignment: .topTrailing) {
                    if model.attentionCount > 0 {
                        Circle().fill(.orange).frame(width: 8, height: 8).offset(x: -9, y: 11)
                    }
                }
                .glassEffect(.regular.interactive(), in: .circle)
                .contentShape(.circle)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(model.attentionCount > 0 ? Text("Menu, \(model.attentionCount) need you") : Text("Menu"))
        .accessibilityIdentifier("menu")
    }
}

/// In place of the conversation while the environment has none to show: Her starting there, failing to, or the
/// machine away.
private struct HerStatus: View {
    @Environment(AppModel.self) private var model
    let machine: HerMachine
    let state: HerEnvState

    var body: some View {
        let name = machine.machineName
        switch state {
        case .offline(let seen):
            ContentUnavailableView {
                Label("\(name) is offline", systemImage: "wifi.slash")
            } description: {
                if let seen {
                    Text("Her’s conversation is kept on that machine; it shows here once the machine is back. Last online \(agoText(seen, now: model.now)).")
                } else {
                    Text("Her’s conversation is kept on that machine; it shows here once the machine is back.")
                }
            }
        case .failed(let error):
            ContentUnavailableView {
                Label("Her couldn’t start on \(name)", systemImage: "exclamationmark.triangle")
            } description: {
                Text(error).textSelection(.enabled)
            } actions: {
                HerWayOut()
            }
        case .starting:
            HerStarting(name: name)
        default:
            PageLoader(text: "Reading the conversation…", bare: true)
        }
    }
}

/// Her starting on a machine; past the while it usually takes, it says so, and what the owner can do meanwhile.
private struct HerStarting: View {
    let name: String
    @State private var long = false

    var body: some View {
        VStack(spacing: 0) {
            PageLoader(text: "Her is starting on \(name)…")
            if long {
                VStack(spacing: 16) {
                    Text("This is taking longer than usual. You can keep waiting, look at Her’s settings, or run her on another machine.")
                        .font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center)
                    HerWayOut()
                }
                .padding(.horizontal, 32)
                .transition(.opacity)
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("her-starting-long")
            }
        }
        .task {
            try? await Task.sleep(for: .seconds(15))
            withAnimation { long = true }
        }
    }
}

/// Where the owner can go while Her doesn't run where she was turned on: her settings (turning her off there), or
/// another machine to run her on.
private struct HerWayOut: View {
    @Environment(AppModel.self) private var model
    @State private var adding = false

    var body: some View {
        VStack(spacing: 10) {
            Button("Her’s Settings") { model.path.append(.herSettings) }
                .buttonStyle(.glass)
                .accessibilityIdentifier("her-status-settings")
            Button("Turn On Her on Another Machine…") { adding = true }
                .buttonStyle(.glass)
                .accessibilityIdentifier("her-status-another")
        }
        .sheet(isPresented: $adding) { HerAddSheet().environment(model) }
    }
}
