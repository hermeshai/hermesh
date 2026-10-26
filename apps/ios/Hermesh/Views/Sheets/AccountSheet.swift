import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI
import UserNotifications

/// The account: the service, usage, the trusted devices, the appearance,
/// notifications.
struct AccountSheet: View {
    @Environment(AppModel.self) private var model
    @State private var removing: Device?
    @State private var signingOut = false
    @State private var notifications: UNAuthorizationStatus?
    @AppStorage("appearance") private var appearance = "system"

    var body: some View {
        NavigationStack {
            List {
                Section {
                    VStack(spacing: 8) {
                        Mark(size: 56)
                        Text(model.status.hosted ? String(localized: "Hermesh service · \(host)") : String(localized: "Your own server · \(host)"))
                            .font(.subheadline)
                        Text(model.status.relayConnected ? "Connected" : "Not connected").font(.footnote).foregroundStyle(model.status.relayConnected ? .green : .secondary)
                    }
                    .frame(maxWidth: .infinity).listRowBackground(Color.clear)
                }
                if let u = model.usage {
                    Section {
                        NavigationLink {
                            UsageView()
                        } label: {
                            Label {
                                Text("Last 30 days **\(tokensText(u.last30))** tokens · active **\(u.active)** days")
                            } icon: {
                                Image(systemName: "chart.bar").foregroundStyle(.secondary)
                            }
                        }
                        .accessibilityIdentifier("usage")
                    } header: {
                        Text("Usage")
                    }
                }
                Section {
                    ForEach(model.devices, id: \.keyID) { d in
                        HStack(spacing: 12) {
                            Image(systemName: d.platform == "ios" ? "iphone" : "laptopcomputer").frame(width: 28).foregroundStyle(.secondary)
                            VStack(alignment: .leading, spacing: 2) {
                                Text(d.thisDevice ? String(localized: "This iPhone") : d.label)
                                if !d.machineID.isEmpty {
                                    Text(d.machineOnline ? "Also runs tasks · online" : "Also runs tasks · offline").font(.subheadline).foregroundStyle(.secondary)
                                } else if d.thisDevice {
                                    Text("Approves; doesn’t run tasks").font(.subheadline).foregroundStyle(.secondary)
                                }
                            }
                        }
                        .swipeActions {
                            if !d.thisDevice { Button("Remove", role: .destructive) { removing = d } }
                        }
                        .contextMenu {
                            if !d.thisDevice { Button("Remove…", systemImage: "minus.circle", role: .destructive) { removing = d } }
                        }
                    }
                } header: {
                    Text("Trusted devices")
                } footer: {
                    Text("Every trusted device can approve requests to connect. If one gets lost, remove it from another (swipe left on it).")
                }
                Section {
                    Picker("Appearance", selection: $appearance) {
                        Text("System").tag("system")
                        Text("Light").tag("light")
                        Text("Dark").tag("dark")
                    }
                    .pickerStyle(.segmented)
                    .accessibilityIdentifier("appearance")
                } header: {
                    Text("Appearance")
                }
                Section {
                    if notifications == .denied {
                        Button("Allow Notifications in Settings") {
                            if let url = URL(string: UIApplication.openNotificationSettingsURLString) { UIApplication.shared.open(url) }
                        }
                    } else {
                        LabeledContent("Requests to connect", value: String(localized: "Always"))
                        Toggle("Agent questions", isOn: pref(\.questions))
                        Toggle("Failed sessions", isOn: pref(\.failures))
                        Toggle("Finished turns", isOn: pref(\.turnsDone))
                    }
                } header: {
                    Text("Notifications")
                } footer: {
                    Text("A question expires after 30 minutes if no one answers.")
                }
                Section {
                    LabeledContent("Connection address") {
                        Text(model.status.connectAddress).font(.footnote.monospaced()).textSelection(.enabled)
                    }
                    if !model.status.consoleURL.isEmpty, let url = URL(string: model.status.consoleURL) {
                        Link(destination: url) {
                            LabeledContent("Web console", value: String(localized: "Machines and grants in any browser; emergency revocation"))
                        }
                    }
                    LabeledContent("Version", value: model.status.version)
                }
                Section {
                    Button("Sign Out…", role: .destructive) { signingOut = true }
                }
            }
            .navigationTitle("Account and Devices")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { CloseButton() }
            .task { notifications = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus }
            .confirmationDialog(removing.map { String(localized: "Remove “\($0.label)”?") } ?? "", isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }), titleVisibility: .visible) {
                Button("Remove", role: .destructive) { if let d = removing { Task { await model.removeDevice(d) } } }
            } message: {
                Text("It can no longer approve anything at once, and its own machine is removed with it. The machines and agents it approved are reissued by this iPhone and keep working (needs Face ID).")
            }
            .confirmationDialog("Sign this iPhone out?", isPresented: $signingOut, titleVisibility: .visible) {
                if !model.status.removed && model.devices.contains(where: { $0.thisDevice }) {
                    Button("OK", role: .cancel) {}
                } else {
                    Button("Sign Out", role: .destructive) { Task { await model.signOut() } }
                }
            } message: {
                if !model.status.removed && model.devices.contains(where: { $0.thisDevice }) {
                    Text("It is still one of your trusted devices. First remove it on another trusted device, under Account and Devices; then sign out here.")
                } else {
                    Text("This iPhone is no longer in your Hermesh account. After signing out, you can set it up again or join your account again.")
                }
            }
        }
    }

    private var host: String {
        URL(string: model.status.relayURL)?.host ?? model.status.relayURL
    }

    private func pref(_ key: WritableKeyPath<NotificationPrefs, Bool>) -> Binding<Bool> {
        Binding(get: { NotificationPrefs.current[keyPath: key] }, set: { v in
            var p = NotificationPrefs.current
            p[keyPath: key] = v
            p.save()
            model.notificationPrefsChanged()
        })
    }
}

/// Which notifications the owner wants (connection requests always come).
struct NotificationPrefs: Codable, Equatable {
    var questions = true
    var failures = true
    var turnsDone = false

    private static let key = "notificationPrefs"

    static var current: NotificationPrefs {
        guard let d = UserDefaults.standard.data(forKey: key), let p = try? JSONDecoder().decode(NotificationPrefs.self, from: d) else { return NotificationPrefs() }
        return p
    }

    func save() {
        if let d = try? JSONEncoder().encode(self) { UserDefaults.standard.set(d, forKey: Self.key) }
    }
}

/// A batch the app had not asked for, shown from the bytes to be signed,
/// to approve with Face ID or deny.
struct SigningSheet: View {
    @Environment(AppModel.self) private var model
    let confirmation: Confirmation

    var body: some View {
        NavigationStack {
            List {
                if let summary = confirmation.summary {
                    Section { Text(summary) }
                }
                ForEach(Array(confirmation.cards.enumerated()), id: \.offset) { _, card in
                    Section {
                        VStack(alignment: .leading, spacing: 6) {
                            Text(card.title).font(.headline)
                            if !card.who.isEmpty { Text(card.who).font(.subheadline).foregroundStyle(.secondary) }
                        }
                        if let code = card.code { CodeTiles(code: code, caption: "Must match what the other side shows").frame(maxWidth: .infinity) }
                        ForEach(card.rows, id: \.kind) { r in LabeledContent(r.label, value: r.value) }
                        if card.rows.isEmpty { ForEach(card.lines, id: \.self) { Text($0).font(.subheadline) } }
                        Text(card.why).font(.subheadline).foregroundStyle(.secondary)
                        if let w = card.warning { Label(w, systemImage: "exclamationmark.triangle").font(.subheadline).foregroundStyle(.orange) }
                    }
                }
                Section {
                    HStack(spacing: 10) {
                        Button { model.signer.rejectShown() } label: { Text("Deny").frame(maxWidth: .infinity) }
                            .buttonStyle(.glass)
                        Button { model.signer.approveShown() } label: { Label("Approve", systemImage: "faceid").frame(maxWidth: .infinity) }
                            .buttonStyle(.glassProminent)
                            .disabled(model.signer.working)
                            .accessibilityIdentifier("sign-approve")
                    }
                    .controlSize(.large)
                    .listRowBackground(Color.clear).listRowInsets(EdgeInsets())
                }
            }
            .navigationTitle("Needs Your Confirmation")
            .navigationBarTitleDisplayMode(.inline)
        }
        .interactiveDismissDisabled(model.signer.working)
    }
}

/// The two digits to type on the device or machine just approved.
struct MatchSheet: View {
    @Environment(\.dismiss) private var dismiss
    let label: String?
    let digits: String

    var body: some View {
        VStack(spacing: 18) {
            Text(label.map { String(localized: "Type these digits on “\($0)”") } ?? String(localized: "Type these digits on the new device"))
                .font(.title3.bold()).multilineTextAlignment(.center)
            HStack(spacing: 14) {
                ForEach(Array(digits), id: \.self) { d in
                    Text(String(d)).font(.system(size: 44, weight: .semibold, design: .monospaced))
                        .frame(width: 72, height: 88).background(.fill.tertiary, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
                }
            }
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("match-digits")
            Text("They prove the approval came from your own device.").font(.subheadline).foregroundStyle(.secondary)
            Button("Done") { dismiss() }.buttonStyle(.glassProminent).controlSize(.large)
        }
        .padding(28)
        .presentationDetents([.medium])
    }
}
