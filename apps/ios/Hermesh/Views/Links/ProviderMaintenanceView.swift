import HermeshProto
import HermeshSessions
import SwiftUI

private typealias Capability = Hermesh_Machine_V1_ProviderMaintenance
private typealias Operation = Hermesh_Machine_V1_ProviderOperation

enum MaintenanceAction: String {
    case install, update, verify, cancel
    case enableAuto = "enable_auto"
    case disableAuto = "disable_auto"

    var title: LocalizedStringKey {
        switch self {
        case .install: "Install"
        case .update: "Update"
        case .verify: "Check Again"
        case .cancel: "Cancel"
        case .enableAuto: "Enable Automatic Updates"
        case .disableAuto: "Disable Automatic Updates"
        }
    }
}

func providerOperationActive(_ phase: String) -> Bool {
    ["preflight", "waiting", "installing", "verifying"].contains(phase)
}

func providerPhaseTitle(_ phase: String) -> LocalizedStringKey {
    switch phase {
    case "preflight": "Checking Requirements"
    case "waiting": "Waiting for Machine to Be Idle"
    case "installing": "Running Installer"
    case "verifying": "Verifying Command-Line Tool"
    case "succeeded": "Completed"
    case "failed": "Failed"
    case "cancelled": "Cancelled"
    case "interrupted": "Interrupted"
    default: "Unknown"
    }
}

/// Confirmation sheet presented before installing a provider CLI onto an environment.
struct ProviderInstallSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let machineID: String
    let provider: String
    @State private var sending = false
    @State private var pending: Hermesh_Identity_V1_MaintainProvider?

    var body: some View {
        NavigationStack {
            List {
                if let m = model.machines.first(where: { $0.machineID == machineID }).map(model.providerMachine),
                   let cap = m.status.providerMaintenance.first(where: { $0.provider == provider }) {
                    let latest = m.status.providerOperations.last { $0.request.provider == provider }
                    Section {
                        VStack(spacing: 8) {
                            AgentLogo(id: provider, size: 54)
                            Text("Install \(agentTitle(provider)) on “\(m.machineName)”?").font(.headline).multilineTextAlignment(.center)
                            Text("Installed into the default command-line directory so you can also run it directly in Terminal.")
                                .font(.subheadline).foregroundStyle(.secondary).multilineTextAlignment(.center)
                        }
                        .frame(maxWidth: .infinity).listRowBackground(Color.clear)
                    }
                    Section {
                        LabeledContent("Environment", value: m.machineName)
                        if !cap.account.isEmpty { LabeledContent("Account", value: cap.account) }
                        LabeledContent("Package", value: "npm · \(cap.package)@\(cap.targetVersion)")
                    } footer: {
                        Text("Requires Face ID approval. After installation completes, sign in to \(agentTitle(provider)) to start tasks.")
                    }
                    Section {
                        Button {
                            var op = Hermesh_Identity_V1_MaintainProvider()
                            op.provider = provider
                            op.action = MaintenanceAction.install.rawValue
                            op.expectedVersion = cap.managedVersion
                            op.targetVersion = cap.targetVersion
                            op.requestID = pending?.requestID ?? ""
                            if op != pending || op.requestID == latest?.request.requestID || op.requestID.isEmpty {
                                op.requestID = UUID().uuidString
                            }
                            pending = op
                            sending = true
                            Task {
                                defer { sending = false }
                                await model.maintainProvider(machine: machineID, operation: op)
                                dismiss()
                            }
                        } label: {
                            HStack {
                                Spacer()
                                if sending { ProgressView() } else { Text("Install").bold() }
                                Spacer()
                            }
                        }
                        .disabled(!m.online || sending)
                        .accessibilityIdentifier("maintenance-install")
                    }
                }
            }
            .navigationTitle(agentTitle(provider))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }.disabled(sending)
                }
            }
        }
        .presentationDetents([.medium, .large])
    }
}

/// Embedded CLI maintenance & unified availability test section inside MachineAgentView.
struct ProviderMaintenanceSection: View {
    @Environment(AppModel.self) private var model
    let machine: Machine
    let agent: String
    let info: AgentStatus
    let testing: Bool
    let testResult: AnyView?
    let runTest: () -> Void
    @State private var sending = false
    @State private var pending: Hermesh_Identity_V1_MaintainProvider?

    var body: some View {
        let cap = machine.status.providerMaintenance.first { $0.provider == agent }
        let latest = machine.status.providerOperations.last { $0.request.provider == agent }
        let active = providerOperationActive(latest?.phase ?? "")
        let managed = !(cap?.managedVersion.isEmpty ?? true)
        let hasUpdate = managed && !(cap?.targetVersion.isEmpty ?? true) && cap?.managedVersion != cap?.targetVersion
        let disabled = !machine.online || sending || active
        let title = agentTitle(agent)

        Section {
            if managed, let cap {
                LabeledContent {
                    if active {
                        HStack(spacing: 8) {
                            if machine.online { ProgressView() }
                            Text(machine.online ? providerPhaseTitle(latest?.phase ?? "") : "Offline").font(.subheadline)
                            if machine.online, let latest, ["preflight", "waiting"].contains(latest.phase) {
                                Button("Cancel") { submit(.cancel, cap: cap, latest: latest) }
                                    .buttonStyle(.bordered)
                                    .disabled(sending)
                                    .accessibilityIdentifier("maintenance-cancel")
                            }
                        }
                    } else if latest?.phase == "interrupted" {
                        Button("Check Again") { submit(.verify, cap: cap, latest: latest) }
                            .buttonStyle(.bordered)
                            .disabled(disabled)
                            .accessibilityIdentifier("maintenance-verify")
                    } else if hasUpdate {
                        Button(machine.status.runningSessions > 0 ? String(localized: "Update When Idle") : String(localized: "Update")) {
                            submit(.update, cap: cap, latest: latest)
                        }
                        .buttonStyle(.bordered)
                        .disabled(disabled)
                        .accessibilityIdentifier("maintenance-update")
                    } else {
                        Text("Up to date").foregroundStyle(.secondary)
                    }
                } label: {
                    VStack(alignment: .leading, spacing: 2) {
                        Text("Installed Version")
                        Text("\(info.version) · npm (\(cap.package))").font(.caption.monospaced()).foregroundStyle(.secondary)
                        if hasUpdate {
                            Text("Update \(cap.targetVersion) available").font(.caption).foregroundStyle(.tint)
                        } else if latest?.phase == "failed", let detail = latest?.detail, !detail.isEmpty {
                            Text(detail).font(.caption).foregroundStyle(.orange)
                        }
                    }
                }
                Toggle(isOn: Binding(
                    get: { cap.autoUpdate },
                    set: { on in submit(on ? .enableAuto : .disableAuto, cap: cap, latest: latest) }
                )) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text("Update Automatically When Idle")
                        Text("Upgrades when no tasks are running on \(machine.machineName)").font(.caption).foregroundStyle(.secondary)
                    }
                }
                .disabled(disabled)
                .accessibilityIdentifier("maintenance-auto")
            } else {
                LabeledContent("Installation Source") {
                    Text("External").foregroundStyle(.secondary)
                }
            }
            Button(action: runTest) {
                HStack {
                    Label("Test", systemImage: "play.circle")
                    if testing { Spacer(); ProgressView() }
                }
            }
            .disabled(testing || !machine.online || !info.loggedIn || active)
            .accessibilityIdentifier("test-agent")
            if let testResult { testResult }
        } header: {
            Text(managed ? "Installation and Updates" : "Command-Line Tool")
        } footer: {
            if managed {
                Text("One short turn, in a folder of its own, confirms \(title) works on \(machine.machineName). It keeps no session.")
            } else {
                Text("Updates are managed by the original installer. Testing runs one short turn on \(machine.machineName) without keeping a session.")
            }
        }
    }

    private func submit(_ action: MaintenanceAction, cap: Capability, latest: Operation?) {
        var op = Hermesh_Identity_V1_MaintainProvider()
        op.provider = agent
        op.action = action.rawValue
        op.expectedVersion = cap.managedVersion
        op.targetVersion = cap.targetVersion
        if action == .cancel {
            guard let latest else { return }
            op.requestID = latest.request.requestID
        } else {
            op.requestID = pending?.requestID ?? ""
            if op != pending || op.requestID == latest?.request.requestID || op.requestID.isEmpty {
                op.requestID = UUID().uuidString
            }
            pending = op
        }
        sending = true
        Task {
            defer { sending = false }
            await model.maintainProvider(machine: machine.machineID, operation: op)
        }
    }
}

/// Activity history for provider installations and updates on an environment.
struct ProviderHistoryView: View {
    @Environment(AppModel.self) private var model
    let machineID: String

    var body: some View {
        List {
            if let m = model.machines.first(where: { $0.machineID == machineID }).map(model.providerMachine) {
                if !m.online { Label("Offline", systemImage: "wifi.slash") }
                Section {
                    if m.status.providerOperations.isEmpty { Text("No provider tasks yet.").foregroundStyle(.secondary) }
                    ForEach(m.status.providerOperations.sorted { $0.updatedUnixMs > $1.updatedUnixMs }, id: \.request.requestID) { job in
                        HStack(spacing: 12) {
                            AgentLogo(id: job.request.provider, size: 28)
                            VStack(alignment: .leading, spacing: 3) {
                                Text(agentTitle(job.request.provider)).bold()
                                HStack(spacing: 6) {
                                    Text(MaintenanceAction(rawValue: job.request.action)?.title ?? "Unknown")
                                    if !job.request.targetVersion.isEmpty { Text(job.request.targetVersion).monospaced() }
                                    Text("·")
                                    Text(providerPhaseTitle(job.phase))
                                }
                                .foregroundStyle(.secondary)
                                Text(Date(timeIntervalSince1970: Double(job.updatedUnixMs) / 1000), format: .dateTime.month().day().hour().minute())
                                    .font(.caption).foregroundStyle(.secondary)
                                if !job.detail.isEmpty { Text(job.detail).font(.caption) }
                            }
                        }
                        .font(.subheadline)
                        .accessibilityElement(children: .combine)
                        .accessibilityIdentifier("history-\(job.request.provider)-\(job.request.action)-\(job.phase)")
                    }
                }
            }
        }
        .navigationTitle("Installation and Update Activity")
        .navigationBarTitleDisplayMode(.inline)
    }
}
