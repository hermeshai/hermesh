import HermeshHer
import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI
import UserNotifications

/// The account, laid out as iOS Settings: the service this iPhone belongs
/// to (its web console a tap away), usage, machines and agents, notifications,
/// appearance and about; account actions stay at the bottom.
/// Settings, from the account picture (docs/settings-environments.md §4.2), laid out as iOS Settings:
/// the account at the top, opening its page and devices; usage; the app's own settings; every
/// environment, each opening its page; about.
struct AccountSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var scenePhase
    @State private var notifications: UNAuthorizationStatus?
    @AppStorage("appearance") private var appearance = "system"

    private var appearanceSelection: Binding<String> {
        model.isDemo ? Binding(get: { model.demoAppearance }, set: { model.demoAppearance = $0 }) : $appearance
    }

    var body: some View {
        @Bindable var model = model
        NavigationStack(path: $model.accountPath) {
            List {
                Section {
                    NavigationLink(value: Route.account) { AccountCard() }
                        .accessibilityIdentifier("account-card")
                }
                Section {
                    if !model.isDemo {
                        NavigationLink {
                            NotificationsView()
                        } label: {
                            LabeledContent {
                                Text(notifications.map { [.authorized, .provisional, .ephemeral].contains($0) } == true ? "On" : "Off")
                            } label: {
                                Label { Text("Notifications") } icon: { SettingsIcon(symbol: "bell.badge.fill", color: .red) }
                            }
                        }
                        .accessibilityIdentifier("notifications")
                    }
                    NavigationLink(value: Route.appearance) {
                        LabeledContent {
                            Text(appearanceSelection.wrappedValue == "light" ? "Light" : appearanceSelection.wrappedValue == "dark" ? "Dark" : "System")
                        } label: {
                            Label { Text("Appearance") } icon: { SettingsIcon(symbol: "sun.max.fill", color: .gray) }
                        }
                    }
                    .accessibilityIdentifier("account-appearance")
                    NavigationLink(value: Route.grants) {
                        LabeledContent {
                            Text("\(model.clients.filter { !$0.revoked }.count)")
                        } label: {
                            Label { Text("Access") } icon: { SettingsIcon(symbol: "key.fill", color: .brown) }
                        }
                    }
                    .accessibilityIdentifier("account-grants")
                    NavigationLink {
                        NetworkDiagnosticsView()
                    } label: {
                        LabeledContent {
                            Text(networkSummary).foregroundStyle(.secondary).monospacedDigit()
                        } label: {
                            Label { Text("Network Diagnostics") } icon: { SettingsIcon(symbol: "antenna.radiowaves.left.and.right", color: .green) }
                        }
                    }
                    .accessibilityIdentifier("account-network-diagnostics")
                } header: {
                    Text("Settings").accessibilityIdentifier("account-settings")
                }
                Section {
                    EnvironmentRows(trusted: Set(model.devices.map(\.machineID).filter { !$0.isEmpty }))
                    Button("Add Environment…") { model.sheet = .addMachine }
                        .accessibilityIdentifier("add-environment")
                } header: {
                    Text("Environments").accessibilityIdentifier("account-machines")
                }
                Section {
                    LabeledContent("Version", value: model.status.version)
                    PrivacyPolicyLink()
                        .accessibilityIdentifier("account-privacy-policy")
                } header: {
                    Text("About").accessibilityIdentifier("account-about")
                }
            }
            .navigationDestination(for: Route.self) { routePage($0) }
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { CloseButton() }
            // The app takes the choice too; this sheet shows it at once.
            .preferredColorScheme(appearanceSelection.wrappedValue == "light" ? .light : appearanceSelection.wrappedValue == "dark" ? .dark : nil)
            // Again when back from the notifications page or from Settings.
            .onAppear { Task { await readNotifications() } }
            .onChange(of: scenePhase) { if scenePhase == .active { Task { await readNotifications() } } }
        }
    }

    private var networkSummary: String {
        if model.networkKind == "offline" { return String(localized: "Offline") }
        if let active = model.machines.first(where: { $0.machineID == model.her.machine }) ?? model.machines.first(where: \.online),
           active.online, active.hasTunnel, active.tunnel.phase == "ready" {
            let path = herPathLabel(active.tunnel.path)
            return active.tunnel.rttMs > 0 ? "\(path) · \(active.tunnel.rttMs) ms" : path
        }
        if model.status.relayConnected {
            let rtt = model.status.relayHealth.rttMs
            return rtt > 0 ? "\(rtt) ms" : String(localized: "Connected")
        }
        return String(localized: "Connecting…")
    }

    private func readNotifications() async {
        notifications = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus
    }
}

/// The account at the top of Settings: its picture, name, service and whether it is reached.
private struct AccountCard: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let account = model.status.hosted && !model.status.account.isEmpty
        HStack(spacing: 14) {
            if account {
                AccountPicture(status: model.status).frame(width: 56, height: 56)
            } else {
                Mark(size: 34).frame(width: 56, height: 56)
                    .background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            }
            VStack(alignment: .leading, spacing: 3) {
                Text(account ? model.status.account : model.status.hosted ? String(localized: "Hermesh service") : String(localized: "Your own server"))
                    .font(.title3.weight(.semibold)).lineLimit(1).truncationMode(.middle)
                Text("\(Text(model.status.hosted ? "Hermesh service" : "Your own server")) · \(Text(model.status.relayConnected ? "Connected" : "Not connected"))")
                    .font(.subheadline).foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 4)
        .accessibilityElement(children: .combine)
    }
}

/// The appearance, as Display & Brightness shows it.
struct AppearanceView: View {
    @Environment(AppModel.self) private var model
    @AppStorage("appearance") private var appearance = "system"

    var body: some View {
        let selection = model.isDemo ? Binding(get: { model.demoAppearance }, set: { model.demoAppearance = $0 }) : $appearance
        List {
            Section {
                HStack(spacing: 0) {
                    AppearanceOption(tag: "system", title: "System", selection: selection)
                    AppearanceOption(tag: "light", title: "Light", selection: selection)
                    AppearanceOption(tag: "dark", title: "Dark", selection: selection)
                }
                .padding(.vertical, 8)
            }
        }
        .navigationTitle("Appearance")
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// The account and its devices: who and where; every trusted device, this iPhone first, the others
/// removed with a swipe; the web console and addresses; signing out, erasing everything, deleting
/// the account.
struct AccountView: View {
    @Environment(AppModel.self) private var model
    @State private var signingOut = false
    @State private var erasing = false
    @State private var removing: Device?
    /// The web console, while the alert before opening it the first time shows.
    @State private var console: URL?
    @AppStorage("consoleOpened") private var consoleOpened = false
    @Environment(\.openURL) private var openURL

    private var accountDeletionURL: URL? {
        let status = model.status
        guard !model.isDemo, status.initialized, status.hosted, !status.signedOut, !status.removed else { return nil }
        let origins = Bundle.main.object(forInfoDictionaryKey: "HermeshAccountDeletionOrigins") as? String ?? ""
        return HostedAccountDeletion.browserURL(consoleURL: status.consoleURL, enabledOrigins: origins)
    }

    var body: some View {
        List {
            Section { service }
            Section {
                ForEach(model.devices.sorted { $0.thisDevice && !$1.thisDevice }, id: \.keyID) { d in
                    HStack(spacing: 12) {
                        Image(systemName: d.platform == "ios" ? "iphone" : "laptopcomputer").frame(width: 28, height: 28).foregroundStyle(.secondary)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(d.thisDevice ? String(localized: "This iPhone") : d.label)
                            Text(d.machineID.isEmpty ? String(localized: "Approves; doesn’t run tasks") : String(localized: "Also runs tasks · \(model.machines.first { $0.machineID == d.machineID }?.online == true ? String(localized: "Online") : String(localized: "Offline"))"))
                                .font(.subheadline).foregroundStyle(.secondary)
                        }
                    }
                    .accessibilityElement(children: .combine)
                    .accessibilityIdentifier("device-\(d.thisDevice ? "this" : d.label)")
                    .swipeActions {
                        if !d.thisDevice { Button("Remove", role: .destructive) { removing = d } }
                    }
                }
            } header: {
                Text("Trusted Devices")
            } footer: {
                Text("Every trusted device can approve requests to connect. If one gets lost, remove it from another (swipe left on it).")
            }
            Section {
                if let url = URL(string: model.status.consoleURL), !model.status.consoleURL.isEmpty {
                    Button("Web Console") { if consoleOpened { openURL(url) } else { console = url } }
                        .accessibilityIdentifier("web-console")
                }
                // The official service shows no connection address: agents pair by a link, machines by the Add a
                // Machine command.
                if !model.status.hosted {
                    CopyAddressRow(title: "Connection address", address: model.status.connectAddress)
                    // An own server's MCP address carries its key; the official one is in Connect Your Agent.
                    if !model.status.mcpURL.isEmpty {
                        CopyAddressRow(title: "MCP address", address: model.status.mcpURL)
                            .accessibilityIdentifier("mcpAddress")
                    }
                }
            } footer: {
                if !model.status.consoleURL.isEmpty { Text("View environments and access in any browser, or revoke access in an emergency.") }
            }
            // What ends: signing this iPhone out, and erasing everything (its own page).
            if !model.isDemo {
                Section {
                    Button(role: .destructive) { signingOut = true } label: { endRow("Sign Out…") }
                    if !model.status.removed {
                        Button(role: .destructive) { erasing = true } label: { endRow("Erase Everything…") }
                            .accessibilityIdentifier("erase-everything")
                    }
                    if let url = accountDeletionURL {
                        Button(role: .destructive) { openURL(url) } label: { endRow("Delete Account…") }
                            .accessibilityIdentifier("delete-hosted-account")
                    }
                } header: {
                    Text("Actions").accessibilityIdentifier("account-actions")
                } footer: {
                    if accountDeletionURL != nil {
                        Text("Delete Account opens the hosted account page in your browser. Erase Everything resets your workspace and its devices.")
                    }
                }
            }
        }
        .navigationTitle("Account and Devices")
        .navigationBarTitleDisplayMode(.inline)
        .navigationDestination(isPresented: $erasing) { EraseView() }
        .alert("Open the web console?", isPresented: Binding(get: { console != nil }, set: { if !$0 { console = nil } }), presenting: console) { url in
            Button("Cancel", role: .cancel) {}
            Button("Open") {
                consoleOpened = true
                openURL(url)
            }
        } message: { url in
            Text("Safari opens \(url.host ?? url.absoluteString), the web console: your machines and grants in any browser, and revoking access in an emergency.")
        }
        .confirmationDialog(removing.map { String(localized: "Remove the trusted device “\($0.label)”?") } ?? "", isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }), titleVisibility: .visible) {
            Button("Remove", role: .destructive) { if let d = removing { Task { await model.removeDevice(d) } } }
        } message: {
            Text("This device will immediately lose approval rights and its own machine will be removed. Machines and agents it previously approved will be re-signed by this iPhone so they keep working (requires Face ID).")
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

    /// The service this iPhone belongs to: on the official service the account, its picture and the
    /// service; on a server of one's own the mark and where it is; whether it is reached.
    private var service: some View {
        let account = model.status.hosted && !model.status.account.isEmpty
        return VStack(spacing: 6) {
            if account {
                AccountPicture(status: model.status).frame(width: 72, height: 72)
            } else {
                Mark(size: 44).frame(width: 72, height: 72)
                    .background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            }
            if account {
                Text(model.status.account).font(.title2.bold()).lineLimit(1).truncationMode(.middle)
                Text("Hermesh service").font(.subheadline).foregroundStyle(.secondary)
            } else {
                Text(model.status.hosted ? "Hermesh service" : "Your own server").font(.title2.bold())
                Text(host).font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
            }
            HStack(spacing: 5) {
                Circle().fill(model.status.relayConnected ? Color.green : .secondary).frame(width: 7, height: 7)
                Text(model.status.relayConnected ? "Connected" : "Not connected")
            }
            .font(.footnote).foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity).listRowBackground(Color.clear)
        .accessibilityElement(children: .combine)
    }

    /// One of the buttons at the end: its title in the middle of the row, the separator across it.
    private func endRow(_ title: LocalizedStringKey) -> some View {
        Text(title).frame(maxWidth: .infinity)
            .alignmentGuide(.listRowSeparatorLeading) { _ in 0 }
    }

    private var host: String {
        URL(string: model.status.relayURL)?.host ?? model.status.relayURL
    }
}

struct NotificationsView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var scenePhase
    @State private var status: UNAuthorizationStatus?
    @State private var prefs = NotificationPrefs.current

    var body: some View {
        List {
            Section {
                Toggle("Her replies", isOn: pref(\.herReplies)).accessibilityIdentifier("notify-her-replies")
                Toggle("Session results", isOn: pref(\.results)).accessibilityIdentifier("notify-results")
                Toggle("New sessions", isOn: pref(\.sessionsStarted)).accessibilityIdentifier("notify-started")
            } header: { Text("notificationSettings.sessions") } footer: {
                Text("When a turn ends, show the reply or the reason it failed.")
            }
            Section {
                Toggle("Her questions and approvals", isOn: pref(\.herRequests)).accessibilityIdentifier("notify-her-requests")
                Toggle("Session questions and permissions", isOn: pref(\.questions)).accessibilityIdentifier("notify-questions")
                LabeledContent("Requests to connect") { Text("Always") }
            } header: { Text("Needs your attention") } footer: {
                Text("Includes tool approvals, plan reviews, and requests for sensitive input.")
            }
            Section {
                if status == .denied {
                    Label("Notifications are off for Hermesh", systemImage: "bell.slash").foregroundStyle(.secondary)
                }
                Button("Notification settings on this iPhone") {
                    if status == .notDetermined {
                        Task {
                            _ = try? await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge, .timeSensitive])
                            status = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus
                            await model.askForNotifications()
                        }
                    } else if let url = URL(string: UIApplication.openNotificationSettingsURLString) {
                        UIApplication.shared.open(url)
                    }
                }
            } footer: { Text("These settings apply to this device. Notifications stay quiet while you’re viewing the conversation.") }
        }
        .navigationTitle("Notifications")
        .navigationBarTitleDisplayMode(.inline)
        .task(id: scenePhase) { status = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus }
    }

    private func pref(_ key: WritableKeyPath<NotificationPrefs, Bool>) -> Binding<Bool> {
        Binding(get: { prefs[keyPath: key] }, set: { v in
            prefs[keyPath: key] = v
            prefs.save()
            model.notificationPrefsChanged()
        })
    }
}

/// A row's icon as in iOS Settings: a white symbol on a rounded square of
/// one colour.
struct SettingsIcon: View {
    let symbol: String
    let color: Color

    var body: some View {
        Image(systemName: symbol)
            .font(.system(size: 15, weight: .semibold))
            .foregroundStyle(.white)
            .frame(width: 29, height: 29)
            .background(color.gradient, in: RoundedRectangle(cornerRadius: 7, style: .continuous))
            .accessibilityHidden(true)
    }
}

/// One appearance to choose, as Display & Brightness shows them, kept low:
/// the top of a small phone in it (System half light, half dark), its name
/// and a check.
struct AppearanceOption: View {
    let tag: String
    let title: LocalizedStringKey
    @Binding var selection: String

    var body: some View {
        let on = selection == tag
        Button {
            withAnimation(.smooth) { selection = tag }
        } label: {
            VStack(spacing: 7) {
                phone
                    .frame(width: 62, height: 72)
                    .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
                    .overlay(RoundedRectangle(cornerRadius: 12, style: .continuous).strokeBorder(on ? Color.accentColor : Color.primary.opacity(0.12), lineWidth: on ? 2.5 : 1))
                Text(title).font(.subheadline)
                Image(systemName: on ? "checkmark.circle.fill" : "circle")
                    .font(.body)
                    .foregroundStyle(on ? Color.accentColor : Color.secondary.opacity(0.5))
            }
            .frame(maxWidth: .infinity)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(title))
        .accessibilityAddTraits(on ? [.isButton, .isSelected] : .isButton)
        .accessibilityIdentifier("appearance-\(tag)")
    }

    @ViewBuilder private var phone: some View {
        switch tag {
        case "light": screen(dark: false)
        case "dark": screen(dark: true)
        default:
            ZStack {
                screen(dark: false)
                screen(dark: true).mask(HStack(spacing: 0) { Color.clear; Color.black })
            }
        }
    }

    /// A screen, its top: a title, and two rows on cards.
    private func screen(dark: Bool) -> some View {
        let bg = dark ? Color.black : Color(white: 0.95)
        let card = dark ? Color(white: 0.17) : .white
        let ink = dark ? Color(white: 0.55) : Color(white: 0.72)
        return RoundedRectangle(cornerRadius: 12, style: .continuous)
            .fill(bg)
            .overlay(alignment: .top) {
                VStack(alignment: .leading, spacing: 5) {
                    Capsule().fill(ink).frame(width: 24, height: 5)
                    ForEach(0 ..< 2, id: \.self) { i in
                        RoundedRectangle(cornerRadius: 4, style: .continuous).fill(card)
                            .frame(height: i == 0 ? 26 : 18)
                            .overlay(alignment: .leading) { Capsule().fill(ink).frame(width: i == 0 ? 26 : 20, height: 3).padding(.leading, 5) }
                    }
                }
                .padding(.horizontal, 7).padding(.top, 10)
            }
    }
}

/// Which notifications the owner wants (connection requests always come).
struct NotificationPrefs: Codable, Equatable {
    var sessionsStarted = true
    var questions = true
    var results = true
    var herReplies = true
    var herRequests = true

    private enum LegacyKeys: String, CodingKey { case failures, turnsDone }
    private static let key = "notificationPrefs"

    init() {}

    /// What an older app saved lacks the newer kinds: those take their defaults.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let d = NotificationPrefs()
        sessionsStarted = try c.decodeIfPresent(Bool.self, forKey: .sessionsStarted) ?? d.sessionsStarted
        questions = try c.decodeIfPresent(Bool.self, forKey: .questions) ?? d.questions
        if let saved = try c.decodeIfPresent(Bool.self, forKey: .results) {
            results = saved
        } else {
            let legacy = try decoder.container(keyedBy: LegacyKeys.self)
            let old = try [LegacyKeys.failures, .turnsDone].compactMap { try legacy.decodeIfPresent(Bool.self, forKey: $0) }
            results = old.isEmpty || old.contains(true)
        }
        herReplies = try c.decodeIfPresent(Bool.self, forKey: .herReplies) ?? d.herReplies
        herRequests = try c.decodeIfPresent(Bool.self, forKey: .herRequests) ?? d.herRequests
    }

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
            }
            // Always in sight, however long what is signed reads: the owner never looks for them.
            .safeAreaBar(edge: .bottom) { buttons }
            .navigationTitle("Needs Your Confirmation")
            .navigationBarTitleDisplayMode(.inline)
        }
        .interactiveDismissDisabled(model.signer.working)
    }

    /// Deny and Approve side by side; one over the other in the largest text sizes, Approve on top.
    private var buttons: some View {
        let layout = size.isAccessibilitySize ? AnyLayout(VStackLayout(spacing: 10)) : AnyLayout(HStackLayout(spacing: 10))
        return layout {
            if size.isAccessibilitySize { approve; deny } else { deny; approve }
        }
        .controlSize(.large)
        .padding(.horizontal, 20).padding(.vertical, 8)
    }

    @Environment(\.dynamicTypeSize) private var size

    private var deny: some View {
        Button { model.signer.rejectShown() } label: { Text("Deny").frame(maxWidth: .infinity) }
            .buttonStyle(.glass)
    }

    private var approve: some View {
        Button { model.signer.approveShown() } label: { Label("Approve", systemImage: "faceid").frame(maxWidth: .infinity) }
            .buttonStyle(.glassProminent)
            .disabled(model.signer.working)
            .accessibilityIdentifier("sign-approve")
    }
}

/// The two digits to type on the device or machine just approved, as a card.
struct MatchSheet: View {
    @Environment(\.dismiss) private var dismiss
    let label: String?
    let digits: String

    var body: some View {
        VStack(spacing: 0) {
            Text(label.map { String(localized: "Type these digits on “\($0)”") } ?? String(localized: "Type these digits on the new device"))
                .font(.title2.bold()).multilineTextAlignment(.center)
            DigitTiles(digits: digits).padding(.top, 26)
            Text("They prove the approval came from your own device.")
                .font(.subheadline).foregroundStyle(.secondary).multilineTextAlignment(.center).padding(.top, 18)
            CardButton(title: "Done") { dismiss() }.padding(.top, 30)
        }
        .padding(.horizontal, 26).padding(.top, 40).padding(.bottom, 6)
        .fittedSheet()
    }
}

/// An address shown in Account, copied with a tap.
private struct CopyAddressRow: View {
    @Environment(AppModel.self) private var model
    let title: LocalizedStringKey
    let address: String

    var body: some View {
        Button {
            UIPasteboard.general.string = address
            model.show(String(localized: "Copied"))
        } label: {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text(title)
                    Text(address).font(.footnote.monospaced()).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
                }
                Spacer()
                Image(systemName: "doc.on.doc").font(.subheadline).foregroundStyle(.tertiary)
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityHint(Text("Copies it"))
    }
}

/// Always-accessible full-chain network diagnostics inside Settings.
private struct NetworkDiagnosticsView: View {
    var body: some View {
        NetworkDiagnosticsList(focusMachine: nil, focusChat: nil, focusState: nil, reconnectID: "settings-diagnostics-reconnect")
            .navigationTitle("Network Diagnostics")
            .navigationBarTitleDisplayMode(.inline)
    }
}

/// Shared diagnostics list used by both Settings -> Network Diagnostics and
/// Her's connection diagnostics sheet: hero summary card + two-line rows with
/// compact right-aligned status/RTT pills so nothing ever wraps awkwardly.
struct NetworkDiagnosticsList: View {
    @Environment(AppModel.self) private var model
    let focusMachine: HerMachine?
    let focusChat: HerChat?
    let focusState: HerEnvState?
    let reconnectID: String
    var onReconnect: (() -> Void)? = nil

    var body: some View {
        List {
            Section {
                DiagnosticHeroCard(hero: heroSummary)
            }
            Section("Connection Path") {
                DiagnosticRow(
                    symbol: networkSymbol,
                    iconColor: model.networkKind == "offline" ? .red : .blue,
                    title: "iPhone",
                    subtitle: herNetworkLabel(model.networkKind),
                    badge: model.networkKind == "offline" ? String(localized: "No network") : String(localized: "Healthy"),
                    badgeColor: model.networkKind == "offline" ? .red : .green
                )
                DiagnosticRow(
                    symbol: "cloud.fill",
                    iconColor: relayColor,
                    title: String(localized: "Relay"),
                    subtitle: relayHost,
                    badge: relayBadge,
                    badgeColor: relayColor,
                    error: relayError
                )
            }
            if !displayMachines.isEmpty {
                Section("Machines") {
                    ForEach(displayMachines, id: \.machineID) { m in
                        let t = tunnelPresentation(for: m)
                        DiagnosticRow(
                            symbol: "laptopcomputer",
                            iconColor: t.color,
                            title: m.machineName.isEmpty ? m.machineID : m.machineName,
                            subtitle: t.subtitle,
                            badge: t.badge,
                            badgeColor: t.color,
                            error: t.error
                        )
                    }
                }
            }
            if let chat = activeHerChat {
                let h = herPresentation(for: chat)
                Section("Her") {
                    DiagnosticRow(
                        symbol: "sparkles",
                        iconColor: h.color,
                        title: "Her",
                        subtitle: h.subtitle,
                        badge: h.badge,
                        badgeColor: h.color,
                        error: chat.convError ?? chat.envError
                    )
                    if !chat.waiting.isEmpty {
                        Label {
                            Text("\(chat.waiting.count) message(s) waiting to be delivered")
                                .font(.subheadline)
                                .foregroundStyle(.secondary)
                        } icon: {
                            Image(systemName: "clock")
                                .foregroundStyle(.orange)
                        }
                    }
                }
            }
            Section {
                Button {
                    model.reconnectNow()
                    onReconnect?()
                } label: {
                    Label("Reconnect Now", systemImage: "arrow.clockwise")
                        .font(.body.weight(.medium))
                        .frame(maxWidth: .infinity, alignment: .center)
                }
                .accessibilityIdentifier(reconnectID)
            }
        }
    }

    private var displayMachines: [Hermesh_Local_V1_Machine] {
        if let focusMachine {
            if let match = model.machines.first(where: { $0.machineID == focusMachine.machineID }) {
                return [match]
            }
            return [focusMachine]
        }
        return model.machines
    }

    private var primaryMachine: Hermesh_Local_V1_Machine? {
        if let focusMachine {
            return model.machines.first(where: { $0.machineID == focusMachine.machineID }) ?? focusMachine
        }
        if let id = model.her.machine, let m = model.machines.first(where: { $0.machineID == id }) {
            return m
        }
        return model.machines.first(where: \.online) ?? model.machines.first
    }

    private var activeHerChat: HerChat? {
        if let focusChat { return focusChat }
        guard let id = model.her.machine, !id.isEmpty else { return nil }
        return model.her.existingChat(id)
    }

    private var heroSummary: DiagnosticHero {
        if model.networkKind == "offline" {
            return DiagnosticHero(
                symbol: "wifi.slash",
                color: .red,
                title: String(localized: "No network connection"),
                subtitle: String(localized: "Check Wi-Fi or cellular settings")
            )
        }
        // The machine this is about is offline (while the relay says so): that first, not the relay that is fine.
        if model.status.relayConnected, let m = primaryMachine, !m.online, !m.local {
            let name = m.machineName.isEmpty ? m.machineID : m.machineName
            return DiagnosticHero(
                symbol: "desktopcomputer",
                color: .secondary,
                title: String(localized: "\(name) is offline"),
                subtitle: m.hasLastSeen ? String(localized: "Last online \(agoText(m.lastSeen.date, now: model.now))") : String(localized: "Offline")
            )
        }
        if let m = primaryMachine, m.online, m.hasTunnel, m.tunnel.phase == "ready" {
            let name = m.machineName.isEmpty ? m.machineID : m.machineName
            let path = herPathLabel(m.tunnel.path)
            let detail = m.tunnel.rttMs > 0 ? "\(path) · \(Int(m.tunnel.rttMs)) ms" : path
            let isDirect = m.tunnel.path != "relay" && !m.tunnel.path.isEmpty
            if isDirect {
                return DiagnosticHero(
                    symbol: "bolt.horizontal.circle.fill",
                    color: m.tunnel.rttMs >= 250 ? .orange : .green,
                    title: String(localized: "Direct · \(name)"),
                    subtitle: detail
                )
            }
            return DiagnosticHero(
                symbol: "cloud.fill",
                color: m.tunnel.rttMs >= 250 ? .orange : .green,
                title: String(localized: "Relayed · \(name)"),
                subtitle: detail
            )
        }
        if model.status.relayConnected {
            let rtt = model.status.relayHealth.rttMs
            let sub = rtt > 0 ? "\(relayHost) · \(Int(rtt)) ms" : relayHost
            return DiagnosticHero(
                symbol: "network",
                color: rtt >= 250 ? .orange : .green,
                title: String(localized: "Relay connected"),
                subtitle: sub
            )
        }
        return DiagnosticHero(
            symbol: "arrow.trianglehead.2.clockwise",
            color: .orange,
            title: String(localized: "Reconnecting…"),
            subtitle: herNetworkLabel(model.networkKind)
        )
    }

    private var networkSymbol: String {
        if model.networkKind == "offline" { return "wifi.slash" }
        let parts = model.networkKind.split(separator: "+")
        if parts.contains("wifi") { return "wifi" }
        if parts.contains("wired") { return "cable.connector" }
        if parts.contains("cell") { return "antenna.radiowaves.left.and.right" }
        return "network"
    }

    private var relayHost: String {
        let host = URL(string: model.status.relayURL)?.host ?? model.status.relayURL
        return host.isEmpty ? String(localized: "Hermesh service") : host
    }

    private var relayBadge: String {
        let rh = model.status.relayHealth
        if model.status.relayConnected {
            return rh.rttMs > 0 ? "\(Int(rh.rttMs)) ms" : String(localized: "Connected")
        }
        if rh.retryCount > 0 { return String(localized: "Reconnecting") }
        return rh.phase == "offline" ? String(localized: "Offline") : String(localized: "Connecting…")
    }

    private var relayColor: Color {
        if model.status.relayConnected { return model.status.relayHealth.rttMs >= 250 ? .orange : .green }
        return model.status.relayHealth.phase == "offline" ? .red : .orange
    }

    private var relayError: String? {
        let e = model.status.relayHealth.lastError
        return e.isEmpty ? nil : e
    }

    private func tunnelPresentation(for m: Hermesh_Local_V1_Machine) -> (subtitle: String, badge: String, color: Color, error: String?) {
        let err = m.hasTunnel && !m.tunnel.lastError.isEmpty ? m.tunnel.lastError : (m.unreachable.isEmpty ? nil : m.unreachable)
        guard m.online else {
            return (String(localized: "Offline"), String(localized: "Offline"), .secondary, err)
        }
        guard m.hasTunnel else {
            return (String(localized: "Online"), String(localized: "Online"), .green, err)
        }
        let th = m.tunnel
        switch th.phase {
        case "ready":
            let path = herPathLabel(th.path)
            let badge = th.rttMs > 0 ? "\(Int(th.rttMs)) ms" : String(localized: "Connected")
            let color: Color = th.rttMs >= 250 ? .orange : .green
            return (path, badge, color, err)
        case "dialing":
            return (String(localized: "Dialing…"), String(localized: "Connecting…"), .orange, err)
        case "retrying":
            return (String(localized: "Reconnecting…"), String(localized: "Reconnecting"), .orange, err)
        case "unreachable":
            return (String(localized: "Unreachable"), String(localized: "Unreachable"), .red, err)
        default:
            return (String(localized: "Offline"), String(localized: "Offline"), .secondary, err)
        }
    }

    private func herPresentation(for chat: HerChat) -> (subtitle: String, badge: String, color: Color) {
        let m = primaryMachine
        let machineName = m.flatMap { $0.machineName.isEmpty ? nil : $0.machineName }
        let path: String? = (m?.hasTunnel == true && m?.tunnel.phase == "ready") ? herPathLabel(m!.tunnel.path) : nil
        let subtitle: String = switch (machineName, path) {
        case let (name?, p?): "\(name) · \(p)"
        case let (name?, nil): name
        case let (nil, p?): p
        case (nil, nil): String(localized: "Assistant")
        }
        if chat.convUp {
            return (subtitle, String(localized: "Connected"), .green)
        }
        if case .offline = focusState {
            return (subtitle, String(localized: "Offline"), .secondary)
        }
        if case .starting = focusState {
            return (subtitle, String(localized: "Starting…"), .orange)
        }
        if case .failed = focusState {
            return (subtitle, String(localized: "Failed"), .red)
        }
        if chat.convRetries > 0 {
            return (subtitle, String(localized: "Reconnecting"), .orange)
        }
        return (subtitle, String(localized: "Connecting…"), chat.convError != nil || chat.envError != nil ? .red : .orange)
    }
}

private struct DiagnosticHero {
    let symbol: String
    let color: Color
    let title: String
    let subtitle: String
}

private struct DiagnosticHeroCard: View {
    let hero: DiagnosticHero

    var body: some View {
        HStack(spacing: 14) {
            ZStack {
                Circle()
                    .fill(hero.color.opacity(0.14))
                    .frame(width: 44, height: 44)
                Image(systemName: hero.symbol)
                    .font(.title3.weight(.semibold))
                    .foregroundStyle(hero.color)
            }
            VStack(alignment: .leading, spacing: 3) {
                Text(hero.title)
                    .font(.headline)
                    .lineLimit(1)
                Text(hero.subtitle)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .monospacedDigit()
            }
            Spacer(minLength: 0)
        }
        .padding(.vertical, 4)
    }
}

private struct DiagnosticRow: View {
    let symbol: String
    let iconColor: Color
    let title: String
    let subtitle: String
    let badge: String
    let badgeColor: Color
    var error: String? = nil

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 12) {
                Image(systemName: symbol)
                    .font(.system(size: 14, weight: .semibold))
                    .foregroundStyle(iconColor)
                    .frame(width: 30, height: 30)
                    .background(iconColor.opacity(0.14), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
                VStack(alignment: .leading, spacing: 2) {
                    Text(title)
                        .font(.body.weight(.medium))
                        .lineLimit(1)
                    Text(subtitle)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
                Spacer(minLength: 8)
                Text(badge)
                    .font(.caption.weight(.semibold))
                    .monospacedDigit()
                    .foregroundStyle(badgeColor)
                    .padding(.horizontal, 9)
                    .padding(.vertical, 4)
                    .background(badgeColor.opacity(0.14), in: .capsule)
                    .fixedSize()
            }
            if let error, !error.isEmpty {
                Text(error)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 6)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
            }
        }
        .padding(.vertical, 2)
    }
}
