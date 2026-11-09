import HermeshHer
import HermeshKit
import HermeshSessions
import SwiftUI

/// The Her entry (docs/her-server.md §9): the conversation with Her, the
/// environment's assistant, as the whole page; her environments one at a
/// time, chosen beside her name when there are several. The first run while
/// Her runs nowhere.
struct HerView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let envs = herEnvironments(model.machines)
        if let m = herPick(envs, remembered: model.her.machine) {
            HerPage(machine: m, envs: envs).id(m.machineID)
        } else {
            HerFirstRun()
        }
    }
}

/// One environment's Her: her name at the top (with the environments beside it when there are several), her
/// settings at the right, and the conversation. Its connections are open while it shows.
private struct HerPage: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var phase
    let machine: HerMachine
    let envs: [HerMachine]
    @State private var settings = false
    @State private var adding = false

    /// What the conversation's connection follows: opened again when any of it changes (a cleared conversation is a
    /// new one; a model just connected opens it).
    private struct Opening: Hashable {
        var bot: String?
        var session: String?
        var running: Bool
        var modelReady: Bool
    }

    var body: some View {
        let chat = model.her.chat(machine.machineID, model: model)
        let state = herState(machine)
        let running = herReachable(machine) && machine.her.running
        Group {
            if chat.bot != nil {
                HerConversation(chat: chat, machine: machine, state: state)
            } else {
                HerStatus(machine: machine, state: state)
            }
        }
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarLeading) { MenuButton() }
            ToolbarItem(placement: .principal) { title(chat) }
            if chat.bot != nil {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Assistant Settings", systemImage: "slider.horizontal.3") { settings = true }
                        .accessibilityIdentifier("her-settings")
                }
            }
        }
        .onAppear { model.her.machine = machine.machineID }
        .task(id: running) { if running { await chat.followEnvironment() } }
        .task(id: Opening(bot: chat.bot?.id, session: chat.bot?.sessionID, running: running, modelReady: machine.her.modelReady)) {
            if running, let bot = chat.bot { await chat.followConversation(botID: bot.id) }
        }
        .task { await chat.loadKept() }
        .task(id: herReachable(machine)) { await chat.refreshWaiting() }
        // Delivered meanwhile: what waits is asked for again as the conversation grows.
        .onChange(of: chat.live?.entries.count) { if !chat.waiting.isEmpty { Task { await chat.refreshWaiting() } } }
        // Nothing is unread while the owner looks.
        .task(id: [chat.bot?.unread ?? 0, chat.envUp ? 1 : 0, phase == .active ? 1 : 0]) { if phase == .active { await chat.markRead() } }
        .sheet(isPresented: $settings) {
            HerSettings(chat: chat, machine: machine, single: envs.count == 1) {
                settings = false
                adding = true
            }
            .environment(model)
        }
        .sheet(isPresented: $adding) { HerAddSheet().environment(model) }
    }

    /// Her's mark and name; with several environments, the one shown beside them, as a menu of all of them.
    @ViewBuilder private func title(_ chat: HerChat) -> some View {
        let label = HStack(spacing: 7) {
            Mark(size: 24)
            Text(chat.bot?.name ?? "Her").font(.headline)
            if envs.count > 1 {
                Text(machine.machineName).font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
                Image(systemName: "chevron.down").font(.caption2.weight(.bold)).foregroundStyle(.secondary)
            }
        }
        if envs.count > 1 {
            Menu {
                Picker("Environment", selection: Binding(get: { machine.machineID }, set: { model.her.machine = $0 })) {
                    ForEach(envs, id: \.machineID) { e in
                        Text(e.machineName)
                        Text(e.her.unread > 0 ? String(localized: "\(herReachable(e) ? String(localized: "Online") : String(localized: "Offline")) · \(e.her.unread) unread")
                             : herReachable(e) ? String(localized: "Online") : String(localized: "Offline"))
                    }
                }
                Divider()
                Button("Add Environment…", systemImage: "plus") { adding = true }
            } label: {
                label
            }
            .accessibilityIdentifier("her-environments")
        } else {
            label.accessibilityElement(children: .combine)
        }
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
