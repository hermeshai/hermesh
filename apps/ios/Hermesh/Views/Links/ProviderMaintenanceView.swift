import HermeshProto
import HermeshSessions
import SwiftUI

private typealias Capability = Hermesh_Machine_V1_ProviderMaintenance
private typealias Operation = Hermesh_Machine_V1_ProviderOperation

private enum MaintenanceAction: String {
    case install, update, verify, cancel
    case enableAuto = "enable_auto"
    case disableAuto = "disable_auto"

    var title: LocalizedStringKey {
        switch self {
        case .install: "Install"
        case .update: "Update"
        case .verify: "Verify Command-Line Tool"
        case .cancel: "Cancel Task"
        case .enableAuto: "Enable Automatic Updates"
        case .disableAuto: "Disable Automatic Updates"
        }
    }
}

private func phaseTitle(_ phase: String) -> LocalizedStringKey {
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

struct ProviderMaintenanceView: View {
    @Environment(AppModel.self) private var model
    let machineID: String

    var body: some View {
        List {
            if let m = model.machines.first(where: { $0.machineID == machineID }).map(model.providerMachine) {
                if !m.online { Label("Offline", systemImage: "wifi.slash") }
                let runsAsRoot = m.status.providerMaintenance.contains { $0.account == "root" }
                    && m.status.providerMaintenance.allSatisfy { $0.package.isEmpty }
                Section {
                    ForEach(m.status.providerMaintenance.filter { cap in
                        !cap.package.isEmpty || !cap.managedVersion.isEmpty || m.status.agents.contains { $0.name == cap.provider && $0.available }
                    }, id: \.provider) { cap in
                        NavigationLink {
                            ProviderActions(machineID: machineID, provider: cap.provider)
                        } label: {
                            HStack {
                                AgentLogo(id: cap.provider, size: 28)
                                Text(agentTitle(cap.provider))
                                Spacer()
                                if !cap.managedVersion.isEmpty { Text(cap.managedVersion).foregroundStyle(.secondary) }
                            }
                        }
                        .accessibilityIdentifier("maintain-\(cap.provider)")
                    }
                } header: { Text(m.machineName) } footer: {
                    if runsAsRoot {
                        Text("Hermesh runs as root in this environment, so remote tool installation is off. Install one in a terminal or reinstall Hermesh for a non-root user (--user), then Check Again.")
                    }
                }
                Section("Task History") {
                    if m.status.providerOperations.isEmpty { Text("No provider tasks yet.").foregroundStyle(.secondary) }
                    ForEach(m.status.providerOperations.sorted { $0.updatedUnixMs > $1.updatedUnixMs }, id: \.request.requestID) { job in
                        VStack(alignment: .leading, spacing: 4) {
                            Text(agentTitle(job.request.provider)).bold()
                            HStack {
                                Text(MaintenanceAction(rawValue: job.request.action)?.title ?? "Unknown")
                                Text(phaseTitle(job.phase))
                            }
                            Text(Date(timeIntervalSince1970: Double(job.updatedUnixMs) / 1000), format: .dateTime.month().day().hour().minute())
                            if !job.detail.isEmpty { Text(job.detail) }
                        }
                        .font(.subheadline)
                        .accessibilityElement(children: .combine)
                        .accessibilityIdentifier("history-\(job.request.provider)-\(job.request.action)-\(job.phase)")
                    }
                }
            }
        }
        .navigationTitle("Installation and Updates")
        .navigationBarTitleDisplayMode(.inline)
    }
}

struct ProviderActions: View {
    @Environment(AppModel.self) private var model
    let machineID: String
    let provider: String
    @State private var sending = false
    @State private var pending: Hermesh_Identity_V1_MaintainProvider?

    var body: some View {
        List {
            if let m = model.machines.first(where: { $0.machineID == machineID }).map(model.providerMachine),
               let cap = m.status.providerMaintenance.first(where: { $0.provider == provider }) {
                let info = m.status.agents.first { $0.name == provider }
                let latest = m.status.providerOperations.last { $0.request.provider == provider }
                let active = ["preflight", "waiting", "installing", "verifying"].contains(latest?.phase ?? "")
                Section {
                    LabeledContent("Machine", value: m.machineName)
                    if !m.online { Label("Offline", systemImage: "wifi.slash") }
                    if !cap.account.isEmpty { LabeledContent("Account", value: cap.account) }
                    if let info, info.available {
                        LabeledContent("Installed Version", value: info.version)
                            .accessibilityIdentifier("installed-version-\(info.version)")
                    }
                    if !cap.package.isEmpty && (!cap.managedVersion.isEmpty || info?.available != true) {
                        LabeledContent("Package", value: cap.package)
                        LabeledContent("Target Version", value: cap.targetVersion)
                    }
                }
                Section {
                    if !cap.package.isEmpty && cap.managedVersion.isEmpty && info?.available != true && info?.checking != true
                        && (info == nil || info?.problem == .notFound || info?.problem == .appWithoutCli) {
                        button(.install, cap, latest)
                    }
                    if !cap.managedVersion.isEmpty && !cap.package.isEmpty {
                        if cap.managedVersion != cap.targetVersion { button(.update, cap, latest) }
                        button(cap.autoUpdate ? .disableAuto : .enableAuto, cap, latest)
                    } else if info?.available == true {
                        Text("Updates are managed by the original installer.").foregroundStyle(.secondary)
                    }
                    if info?.available == true { button(.verify, cap, latest) }
                }
                .disabled(!m.online || sending || active)
                if sending { ProgressView() }
                if let latest {
                    Section("Latest Task") {
                        Text(phaseTitle(latest.phase))
                        if !latest.detail.isEmpty { Text(latest.detail).font(.subheadline) }
                        if ["preflight", "waiting"].contains(latest.phase) {
                            button(.cancel, cap, latest).disabled(!m.online || sending)
                        }
                    }
                }
            }
        }
        .navigationTitle(agentTitle(provider))
        .navigationBarTitleDisplayMode(.inline)
    }

    private func button(_ action: MaintenanceAction, _ cap: Capability, _ latest: Operation?) -> some View {
        Button {
            var op = Hermesh_Identity_V1_MaintainProvider()
            op.provider = provider
            op.action = action.rawValue
            op.expectedVersion = cap.managedVersion
            op.targetVersion = cap.targetVersion
            // Reuse an unacknowledged request when retrying the same action.
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
                await model.maintainProvider(machine: machineID, operation: op)
            }
        } label: { Text(action.title) }
        .accessibilityIdentifier("maintenance-\(action.rawValue)")
    }
}
