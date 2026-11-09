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
        } else {
            HerFirstRun()
        }
    }
}

/// One environment's Her: the menu button, her mark large in the middle with her name on a capsule over its lower
/// edge, and the conversation scrolling under them.
private struct HerPage: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var phase
    let machine: HerMachine

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
        .toolbar(.hidden, for: .navigationBar)
        .safeAreaBar(edge: .top) { header(chat) }
        .onAppear { model.her.machine = machine.machineID }
        .herConnections(chat, machine: machine)
        // Nothing is unread while the owner looks.
        .task(id: [chat.bot?.unread ?? 0, chat.envUp ? 1 : 0, phase == .active ? 1 : 0]) { if phase == .active { await chat.markRead() } }
    }

    private func header(_ chat: HerChat) -> some View {
        ZStack(alignment: .top) {
            HStack {
                HerMenuButton()
                Spacer()
            }
            Button {
                model.path.append(.herSettings)
            } label: {
                VStack(spacing: -16) {
                    HerAvatar(size: 72)
                    Text(chat.bot?.name ?? "Her")
                        .font(.system(size: 17, weight: .semibold))
                        .padding(.horizontal, 16).padding(.vertical, 8)
                        .glassEffect(.regular.interactive(), in: .capsule)
                }
            }
            .buttonStyle(.plain)
            .accessibilityHint(Text("Opens Her’s settings"))
            .accessibilityIdentifier("her-name")
        }
        .padding(.horizontal, 16).padding(.bottom, 4)
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

/// Her's avatar: the Hermesh mark on the app icon's dark ground, round.
struct HerAvatar: View {
    var size: CGFloat

    var body: some View {
        // The mark 64% of the icon, as on the app icon (its beams fill 86 of Mark's 104).
        Mark(size: size * 0.64 * 104 / 86)
            .environment(\.colorScheme, .dark)
            .frame(width: size, height: size)
            .background(LinearGradient(colors: [Color(white: 0.17), Color(red: 0.06, green: 0.06, blue: 0.08)], startPoint: .top, endPoint: .bottom), in: .circle)
            .overlay(Circle().strokeBorder(.white.opacity(0.08)))
            .accessibilityHidden(true)
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
            }
        case .starting:
            PageLoader(text: "Her is starting on \(name)…")
        default:
            PageLoader(text: "Reading the conversation…", bare: true)
        }
    }
}
