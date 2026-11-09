import HermeshHer
import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI

/// Her's settings, a page of their own (the Mac's AssistantPanel, cut to
/// what is used on a phone): where she runs, and her other environments; the
/// model she answers with, among those her environment can use; whether she
/// asks before acting or has full access; her notifications; another machine
/// to run her on; clearing her conversation. Models, connectors and the rest
/// of an environment are set on the Mac.
struct HerSettings: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var clearing = false
    @State private var fullAccess = false
    @State private var adding = false

    var body: some View {
        let envs = herEnvironments(model.machines)
        if let machine = herPick(envs, remembered: model.her.machine) {
            let chat = model.her.chat(machine.machineID, model: model)
            form(chat, machine: machine, envs: envs)
                .herConnections(chat, machine: machine)
        }
    }

    private func form(_ chat: HerChat, machine: HerMachine, envs: [HerMachine]) -> some View {
        let bot = chat.bot
        return Form {
            Section {
                VStack(spacing: 10) {
                    HerAvatar(size: 88)
                    Text(bot?.name ?? "Her").font(.title2.weight(.semibold))
                }
                .frame(maxWidth: .infinity)
                .listRowBackground(Color.clear)
            }
            Section {
                if envs.count > 1 {
                    Picker("Runs On", selection: Binding(get: { machine.machineID }, set: { model.her.machine = $0 })) {
                        ForEach(envs, id: \.machineID) { e in
                            let reach = herReachable(e) ? String(localized: "Online") : String(localized: "Offline")
                            Text(e.machineName)
                            Text(e.her.unread > 0 ? String(localized: "\(reach) · \(e.her.unread) unread") : reach)
                        }
                    }
                    .accessibilityIdentifier("her-environment")
                } else {
                    LabeledContent("Runs On", value: machine.machineName)
                        .accessibilityIdentifier("her-environment")
                }
                Button("Turn On Her on Another Machine…") { adding = true }
            }
            if let s = chat.live {
                Section {
                    Picker("Model", selection: Binding(get: { s.model }, set: { m in Task { await chat.op(["type": "set_model", "model": .string(m)]) } })) {
                        ForEach(chat.models.menu(current: s.model), id: \.id) { m in
                            Text(chat.models.provider(m.provider).map { "\(m.name) · \($0.name)" } ?? m.name).tag(m.id)
                        }
                    }
                    .accessibilityIdentifier("her-model")
                    LabeledContent("Before Acting") {
                        Picker("Before Acting", selection: Binding(get: { s.permissionMode == "bypass-permissions" }, set: { full in
                            if full { fullAccess = true } else if s.permissionMode == "bypass-permissions" { setMode("manual", chat: chat, machine: machine) }
                        })) {
                            Text("Ask Me").tag(false)
                            Text("Full Access").tag(true)
                        }
                        .pickerStyle(.segmented).fixedSize()
                    }
                    .accessibilityIdentifier("her-mode")
                } footer: {
                    Text("The agents and scheduled tasks Her starts work the same way, while you are away as well.")
                }
            }
            if let bot {
                Section {
                    Toggle("Notifications", isOn: Binding(get: { !bot.muted }, set: { on in
                        Task {
                            do { _ = try await chat.call("bots/update", ["bot_id": .string(bot.id), "muted": .bool(!on)]) } catch { model.show(error.localizedDescription) }
                        }
                    }))
                } footer: {
                    Text("Off, no device notifies you of Her; what is unread still shows.")
                }
                Section {
                    Button("Clear Conversation", role: .destructive) { clearing = true }
                }
            }
        }
        .listSectionSpacing(.compact)
        .contentMargins(.top, 0, for: .scrollContent)
        .navigationBarTitleDisplayMode(.inline)
        // What stays is said before it goes.
        .confirmationDialog("Clear your chat with “\(bot?.name ?? "Her")”?", isPresented: $clearing, titleVisibility: .visible) {
            Button("Clear", role: .destructive) {
                guard let bot else { return }
                Task {
                    do {
                        _ = try await chat.call("bots/clear", ["bot_id": .string(bot.id)])
                        model.show(String(localized: "Chat cleared"))
                        dismiss()
                    } catch { model.show(error.localizedDescription) }
                }
            }
        } message: {
            Text("The chat history is cleared and the work it is doing stops. Its files, scheduled tasks and settings stay.")
        }
        // What full access means, before it is turned on (the Mac's AccessSheet).
        .confirmationDialog("Give “\(bot?.name ?? "Her")” full access?", isPresented: $fullAccess, titleVisibility: .visible) {
            Button("Turn On Full Access", role: .destructive) { setMode("bypass-permissions", chat: chat, machine: machine) }
        } message: {
            Text("It acts on \(machine.machineName) without asking you, and so do the agents and scheduled tasks it starts. Her reads web pages and messages from anyone, so turn this on only if you trust everything on that machine to it.")
        }
        .sheet(isPresented: $adding) { HerAddSheet().environment(model) }
    }

    /// How freely Her acts (SetHerAssistantMode): her conversation says so once it is set.
    private func setMode(_ mode: String, chat: HerChat, machine: HerMachine) {
        guard let bot = chat.bot, let api = model.api else { return }
        var r = Hermesh_Local_V1_SetHerAssistantModeRequest()
        r.machineID = machine.machineID
        r.botID = bot.id
        r.permissionMode = mode
        Task {
            do {
                let _: Hermesh_Local_V1_SetHerAssistantModeResponse = try await api.call("SetHerAssistantMode", r, timeout: 30)
                if mode == "bypass-permissions" { model.show(String(localized: "\(bot.name) has full access")) }
            } catch { model.report(error) }
        }
    }
}

/// Where Her can run, to turn her on (docs/her-server.md §9.2): the owner's Macs and Linux machines, those online
/// first; one offline, a Windows one or one that can't run her shows why. Turning her on is signed with Face ID.
struct HerChoices: View {
    @Environment(AppModel.self) private var model
    /// Inside a sheet the signing sheet can't show over it: Face ID asks at once, the owner having chosen here.
    var signedHere = false
    var onDone: (String) -> Void = { _ in }
    @State private var turning: String?

    var body: some View {
        let choices = herChoices(model.machines)
        VStack(spacing: 8) {
            ForEach(choices, id: \.machine.machineID) { c in row(c) }
            if choices.isEmpty {
                Text("Her runs on one of your Macs or Linux servers. Add one in Machines first.")
                    .font(.subheadline).foregroundStyle(.secondary).frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }

    private func row(_ c: HerChoice) -> some View {
        let m = c.machine
        let detail: String = switch c.why {
        case .windows: String(localized: "Windows isn’t supported yet")
        case .offline: m.hasLastSeen ? String(localized: "Offline · seen \(agoText(m.lastSeen.date, now: model.now))") : String(localized: "Offline")
        case .unsupported(let why): String(localized: "Can’t run Her: \(why)")
        case nil: String(localized: "Online · \(platformText(m.status.platform))")
        }
        return HStack(spacing: 12) {
            Image(systemName: machineIcon(m)).font(.body).frame(width: 36, height: 36)
                .background(Color(.systemBackground), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            VStack(alignment: .leading, spacing: 2) {
                Text(m.machineName).font(.subheadline.weight(.semibold))
                Text(detail).font(.caption).foregroundStyle(.secondary).lineLimit(2)
            }
            Spacer(minLength: 8)
            if turning == m.machineID {
                ProgressView()
            } else {
                Button("Turn On Her") { turnOn(m) }
                    .buttonStyle(.glassProminent).controlSize(.small)
                    .disabled(c.why != nil || turning != nil)
                    .accessibilityIdentifier("her-turn-on-\(m.machineName)")
            }
        }
        .padding(12)
        .background(Palette.fill2, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .opacity(c.why == nil ? 1 : 0.6)
    }

    private func turnOn(_ m: HerMachine) {
        guard let api = model.api else { return }
        turning = m.machineID
        if signedHere { model.signer.expect(.types([SigningPreview.adminCommand])) }
        var r = Hermesh_Local_V1_SetHerRequest()
        r.machineID = m.machineID
        r.enabled = true
        Task {
            do {
                let _: Hermesh_Local_V1_SetHerResponse = try await api.call("SetHer", r, timeout: signingTimeout)
                // Turned on: the machine says so through Watch in a moment, and the page becomes its Her's.
                model.her.machine = m.machineID
                onDone(m.machineID)
            } catch {
                turning = nil
                model.report(error)
            }
        }
    }
}

/// The Her entry while Her runs nowhere: where she should run.
struct HerFirstRun: View {
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                Mark(size: 52)
                Text("Choose where Her runs").font(.title2.bold()).padding(.top, 14)
                Text("Her’s conversations, files and model keys stay on that machine. You can add other machines later.")
                    .font(.subheadline).foregroundStyle(.secondary).padding(.top, 4).padding(.bottom, 18)
                HerChoices()
            }
            .padding(.horizontal, 20).padding(.top, 8)
        }
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { ToolbarItem(placement: .topBarLeading) { MenuButton() } }
    }
}

/// Another environment: the first run's choices, in a sheet.
struct HerAddSheet: View {
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    Text("Her’s conversations, files and model keys stay on that machine.").font(.subheadline).foregroundStyle(.secondary)
                    HerChoices(signedHere: true) { _ in dismiss() }
                }
                .padding(20)
            }
            .navigationTitle("Add an Environment")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } } }
        }
    }
}
