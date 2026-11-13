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
/// and the conversation scrolling under them. The tag says what the team is doing (docs/her-team.md §4.2) and
/// opens the team panel; her mark opens her settings.
private struct HerPage: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var phase
    @Environment(\.accessibilityReduceMotion) private var still
    /// With the bars on the side, the menu button is in the system's bar, as on every page; the top is still hers.
    @Environment(\.barsOnTheSide) private var sideBars
    let machine: HerMachine
    /// The team panel shows.
    @State private var team = false

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
        // Over the conversation and its composer, under her mark: the panel grows from where the tag is.
        .overlay(alignment: .top) {
            if team {
                ZStack(alignment: .top) {
                    Color.black.opacity(0.22).ignoresSafeArea()
                        .onTapGesture { showTeam(false) }
                        .accessibilityLabel("Close")
                        .accessibilityAddTraits(.isButton)
                        .accessibilityIdentifier("team-close")
                    TeamPanel { showTeam(false) }
                        .padding(.top, -tagHeight - 4)
                        .transition(still ? .opacity : .scale(scale: 0.3, anchor: .top).combined(with: .opacity))
                }
                .transition(.opacity)
            }
        }
        .toolbar(sideBars ? .visible : .hidden, for: .navigationBar)
        .toolbar { if sideBars { MenuToolbarItem() } }
        .safeAreaBar(edge: .top) { header(chat) }
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
        ZStack(alignment: .top) {
            if !sideBars && !model.menuInView {
                HStack {
                    HerMenuButton()
                    Spacer()
                }
            }
            VStack(spacing: -16) {
                Button {
                    model.path.append(.herSettings)
                } label: {
                    HerAvatar(size: 72, busy: chat.live.map(herBusy) ?? false)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(Text(chat.bot?.name ?? "Her"))
                .accessibilityHint(Text("Opens Her’s settings"))
                .accessibilityIdentifier("her-avatar")
                .zIndex(1)
                Button {
                    showTeam(true)
                } label: {
                    HerTag(name: chat.bot?.name ?? "Her", team: teamState(model.sessions, requests: model.pairings.count), paused: phase != .active)
                        .frame(height: tagHeight)
                        .padding(.horizontal, 14)
                        .glassEffect(.regular.interactive(), in: .capsule)
                }
                .buttonStyle(.plain)
                .accessibilityHint(Text("Shows what your agents are doing"))
                .accessibilityIdentifier("her-tag")
                // The panel takes its place.
                .opacity(team ? 0 : 1)
                .allowsHitTesting(!team)
            }
        }
        .padding(.horizontal, 16).padding(.bottom, 4)
    }

    private let tagHeight: CGFloat = 36

    private func showTeam(_ on: Bool) {
        if on { UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil) }
        withAnimation(still ? .easeOut(duration: 0.15) : .snappy(duration: 0.36, extraBounce: 0.08)) { team = on }
    }
}

/// Her's name tag: her name and, beside it, the team's state, the first that holds (TeamState.Tag): one session
/// that waits, by its coding agent; how many things wait; how many sessions failed; how many run, with their
/// agents' logos. While something needs the owner and sessions also run, their number follows, at a ring.
private struct HerTag: View {
    let name: String
    let team: TeamState
    /// Nothing turns while the app is not in front.
    let paused: Bool

    var body: some View {
        HStack(spacing: 7) {
            Text(name).font(.system(size: 17, weight: .semibold))
            if team.tag != .quiet {
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
                    if paused { Circle().strokeBorder(.tertiary, lineWidth: 1.5).frame(width: 12, height: 12) } else { ProgressView().controlSize(.mini) }
                }
            }
        }
        .font(.system(size: 15, weight: .medium))
        .lineLimit(1)
        .animation(.snappy, value: team.tag)
        .accessibilityElement(children: .combine)
    }

    private func dot(_ color: Color) -> some View { Circle().fill(color).frame(width: 9, height: 9) }
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
        .padding(.horizontal, 10).padding(.bottom, 24)
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
/// new one; a model just connected opens it); and what this iPhone keeps.
private struct HerConnections: ViewModifier {
    let chat: HerChat
    let machine: HerMachine

    private struct Opening: Hashable {
        var bot: String?
        var session: String?
        var running: Bool
        var modelReady: Bool
    }

    func body(content: Content) -> some View {
        let running = herReachable(machine) && machine.her.running
        content
            .task(id: running) { if running { await chat.followEnvironment() } }
            .task(id: Opening(bot: chat.bot?.id, session: chat.bot?.sessionID, running: running, modelReady: machine.her.modelReady)) {
                if running, let bot = chat.bot { await chat.followConversation(botID: bot.id) }
            }
            .task { await chat.loadKept() }
            .task(id: herReachable(machine)) { await chat.refreshWaiting() }
            // Delivered meanwhile: what waits is asked for again as the conversation grows.
            .onChange(of: chat.live?.entries.count) { if !chat.waiting.isEmpty { Task { await chat.refreshWaiting() } } }
    }
}

extension View {
    func herConnections(_ chat: HerChat, machine: HerMachine) -> some View { modifier(HerConnections(chat: chat, machine: machine)) }
}

/// The menu button, as root pages have it in their bar: Her's page draws its own top.
private struct HerMenuButton: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Button {
            model.drawerOpen = true
        } label: {
            Image(systemName: model.menuBeside ? "sidebar.left" : "line.3.horizontal").font(.body.weight(.medium)).frame(width: 44, height: 44)
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
            }
        case .starting:
            PageLoader(text: "Her is starting on \(name)…")
        default:
            PageLoader(text: "Reading the conversation…", bare: true)
        }
    }
}
