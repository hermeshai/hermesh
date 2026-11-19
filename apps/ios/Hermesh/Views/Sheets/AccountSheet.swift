import HermeshHer
import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI
import UserNotifications

/// The account, laid out as iOS Settings: the service this iPhone belongs
/// to (its web console a tap away), usage, machines and agents, notifications,
/// appearance and about; account actions stay at the bottom.
struct AccountSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var scenePhase
    @State private var signingOut = false
    @State private var erasing = false
    /// The web console, while the alert before opening it the first time shows.
    @State private var console: URL?
    @AppStorage("consoleOpened") private var consoleOpened = false
    @Environment(\.openURL) private var openURL
    @State private var notifications: UNAuthorizationStatus?
    @AppStorage("appearance") private var appearance = "system"

    private var appearanceSelection: Binding<String> {
        model.isDemo ? Binding(get: { model.demoAppearance }, set: { model.demoAppearance = $0 }) : $appearance
    }

    var body: some View {
        @Bindable var model = model
        NavigationStack(path: $model.accountPath) {
            List {
                Section { service }
                if let u = model.usage {
                    Section {
                        UsageSummary(usage: u)
                    } header: {
                        Text("Usage").accessibilityIdentifier("account-usage")
                    }
                }
                // The machines and the agents connected to the owner, managed on pages of their own (the menu
                // lists sessions only, docs/her-team.md §16).
                Section {
                    manage("Machines", symbol: "desktopcomputer", color: .blue, opens: .machines, id: "account-machines")
                    manage("Connected Agents", symbol: "link", color: .indigo, opens: .grants, id: "account-grants")
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
                if !model.isDemo {
                    Section {
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
                }
                Section {
                    HStack(spacing: 0) {
                        AppearanceOption(tag: "system", title: "System", selection: appearanceSelection)
                        AppearanceOption(tag: "light", title: "Light", selection: appearanceSelection)
                        AppearanceOption(tag: "dark", title: "Dark", selection: appearanceSelection)
                    }
                    .padding(.vertical, 8)
                } header: {
                    Text("Appearance").accessibilityIdentifier("account-appearance")
                }
                Section {
                    // The official service shows no connection address: agents
                    // pair by a link, machines by the Add a Machine command.
                    if !model.status.hosted {
                        CopyAddressRow(title: "Connection address", address: model.status.connectAddress)
                        // An own server's MCP address carries its key; the
                        // official one is in Connect Your Agent.
                        if !model.status.mcpURL.isEmpty {
                            CopyAddressRow(title: "MCP address", address: model.status.mcpURL)
                                .accessibilityIdentifier("mcpAddress")
                        }
                    }
                    LabeledContent("Version", value: model.status.version)
                    PrivacyPolicyLink()
                        .accessibilityIdentifier("account-privacy-policy")
                } header: {
                    Text("About").accessibilityIdentifier("account-about")
                }
                // What ends: signing this iPhone out, and erasing everything (its own page).
                if !model.isDemo {
                    Section {
                        Button(role: .destructive) { signingOut = true } label: { endRow("Sign Out…") }
                        if !model.status.removed {
                            Button(role: .destructive) { erasing = true } label: { endRow("Erase Everything…") }
                                .accessibilityIdentifier("erase-everything")
                        }
                    } header: {
                        Text("Actions").accessibilityIdentifier("account-actions")
                    }
                }
            }
            .navigationDestination(for: Route.self) { routePage($0) }
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
            .navigationTitle("Account and Devices")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { CloseButton() }
            // The app takes the choice too; this sheet shows it at once.
            .preferredColorScheme(appearanceSelection.wrappedValue == "light" ? .light : appearanceSelection.wrappedValue == "dark" ? .dark : nil)
            // Again when back from the notifications page or from Settings.
            .onAppear { Task { await readNotifications() } }
            .onChange(of: scenePhase) { if scenePhase == .active { Task { await readNotifications() } } }
            .refreshable { await model.loadUsage() }
            .confirmationDialog("Sign this iPhone out?", isPresented: $signingOut, titleVisibility: .visible) {
                if !model.status.removed && model.devices.contains(where: { $0.thisDevice }) {
                    Button("OK", role: .cancel) {}
                } else {
                    Button("Sign Out", role: .destructive) { Task { await model.signOut() } }
                }
            } message: {
                if !model.status.removed && model.devices.contains(where: { $0.thisDevice }) {
                    Text("It is still one of your trusted devices. First remove it on another trusted device, under Machines; then sign out here.")
                } else {
                    Text("This iPhone is no longer in your Hermesh account. After signing out, you can set it up again or join your account again.")
                }
            }
        }
    }

    /// Management stays in the account's stack, including its detail pages.
    private func manage(_ title: LocalizedStringKey, symbol: String, color: Color, opens route: Route, id: String) -> some View {
        NavigationLink(value: route) {
            Label { Text(title) } icon: { SettingsIcon(symbol: symbol, color: color) }
        }
        .accessibilityIdentifier(id)
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

    /// The service this iPhone belongs to, as Settings shows the account at
    /// its top: on the official service the account, its picture and the
    /// service; on a server of one's own the mark and where it is; whether
    /// it is reached; and a way to the web console.
    private var service: some View {
        let account = model.status.hosted && !model.status.account.isEmpty
        return HStack(spacing: 14) {
            if account {
                AccountPicture(status: model.status).frame(width: 60, height: 60)
            } else {
                Mark(size: 38)
                    .frame(width: 60, height: 60)
                    .background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 15, style: .continuous))
            }
            VStack(alignment: .leading, spacing: 3) {
                if account {
                    Text(model.status.account).font(.title3.weight(.semibold)).lineLimit(1).truncationMode(.middle)
                    Text("Hermesh service").font(.subheadline).foregroundStyle(.secondary)
                } else {
                    Text(model.status.hosted ? "Hermesh service" : "Your own server").font(.title3.weight(.semibold))
                    Text(host).font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
                }
                HStack(spacing: 5) {
                    Circle().fill(model.status.relayConnected ? Color.green : .secondary).frame(width: 7, height: 7)
                    Text(model.status.relayConnected ? "Connected" : "Not connected")
                }
                .font(.footnote).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityElement(children: .combine)
            // The service's web console, where it has one: the first time,
            // an alert says where it goes; after that, straight there.
            if let url = URL(string: model.status.consoleURL), !model.status.consoleURL.isEmpty {
                Button {
                    if consoleOpened { openURL(url) } else { console = url }
                } label: {
                    Image(systemName: "arrow.up.right").font(.subheadline.weight(.semibold))
                        .frame(width: 36, height: 36)
                        .background(Color(.tertiarySystemFill), in: .circle)
                }
                .buttonStyle(.borderless)
                .accessibilityLabel("Web console")
                .accessibilityIdentifier("web-console")
            }
        }
        .padding(.vertical, 4)
    }

    /// One of the buttons at the end: its title in the middle of the row,
    /// the separator across it.
    private func endRow(_ title: LocalizedStringKey) -> some View {
        Text(title).frame(maxWidth: .infinity)
            .alignmentGuide(.listRowSeparatorLeading) { _ in 0 }
    }

    private func readNotifications() async {
        notifications = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus
    }

    private var host: String {
        URL(string: model.status.relayURL)?.host ?? model.status.relayURL
    }

}

/// Notifications, a page of its own as in Settings: requests to connect
/// always come; the agents' questions, failed sessions and finished turns
/// to choose. When iOS has them off, the way to turn them on.
struct NotificationsView: View {
    @Environment(AppModel.self) private var model
    @State private var status: UNAuthorizationStatus?

    var body: some View {
        List {
            if status == .denied {
                Section {
                    Label("Notifications are off for Hermesh", systemImage: "bell.slash").foregroundStyle(.secondary)
                    Button("Allow Notifications in Settings") {
                        if let url = URL(string: UIApplication.openNotificationSettingsURLString) { UIApplication.shared.open(url) }
                    }
                }
            }
            Section {
                LabeledContent {
                    Text("Always")
                } label: {
                    Label { Text("Requests to connect") } icon: { SettingsIcon(symbol: "lock.shield.fill", color: .blue) }
                }
            }
            Section {
                Toggle(isOn: pref(\.sessionsStarted)) {
                    Label { Text("New sessions") } icon: { SettingsIcon(symbol: "plus.bubble.fill", color: .indigo) }
                }
                .accessibilityIdentifier("notify-started")
                Toggle(isOn: pref(\.questions)) {
                    Label { Text("Agent questions") } icon: { SettingsIcon(symbol: "questionmark.bubble.fill", color: .orange) }
                }
                .accessibilityIdentifier("notify-questions")
                Toggle(isOn: pref(\.failures)) {
                    Label { Text("Failed sessions") } icon: { SettingsIcon(symbol: "exclamationmark.triangle.fill", color: .red) }
                }
                Toggle(isOn: pref(\.turnsDone)) {
                    Label { Text("Finished turns") } icon: { SettingsIcon(symbol: "checkmark", color: .green) }
                }
                .accessibilityIdentifier("notify-turns")
            } footer: {
                Text("A question expires after 30 minutes if no one answers.")
            }
            .disabled(status == .denied)
        }
        .navigationTitle("Notifications")
        .navigationBarTitleDisplayMode(.inline)
        .task { status = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus }
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

/// Usage in place: the last 30 days, active days and streak, the weeks as
/// a grid of days in ink grey, and each coding agent's share.
struct UsageSummary: View {
    let usage: UsageYear

    var body: some View {
        let u = usage
        VStack(alignment: .leading, spacing: 14) {
            HStack(alignment: .top) {
                stat(tokensText(u.last30), "Last 30 days · tokens")
                stat(String(localized: "\(u.active) days"), "Active days")
                stat(String(localized: "\(u.streak) days"), "Streak")
            }
            Grid(horizontalSpacing: 3, verticalSpacing: 3) {
                ForEach(0 ..< 7, id: \.self) { row in
                    GridRow {
                        ForEach(0 ..< u.weeks, id: \.self) { col in
                            let cell = u.cells[col * 7 + row]
                            RoundedRectangle(cornerRadius: 2.5, style: .continuous)
                                .fill(cell.map { Color.primary.opacity([0.07, 0.18, 0.32, 0.5, 0.74][$0.level]) } ?? .clear)
                                .aspectRatio(1, contentMode: .fit)
                        }
                    }
                }
            }
            .accessibilityElement()
            .accessibilityLabel(Text("Usage over the last \(u.weeks) weeks"))
            if !u.byAgent.isEmpty {
                HStack(spacing: 14) {
                    ForEach(u.byAgent.prefix(3), id: \.agent) { a in
                        Text("\(agentTitle(a.agent)) \(Int((a.share * 100).rounded()))%")
                    }
                }
                .font(.footnote).foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 6)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("usage")
    }

    private func stat(_ value: String, _ label: LocalizedStringKey) -> some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(value).font(.title3.weight(.semibold)).monospacedDigit()
            Text(label).font(.caption).foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// Which notifications the owner wants (connection requests always come).
struct NotificationPrefs: Codable, Equatable {
    var sessionsStarted = true
    var questions = true
    var failures = true
    var turnsDone = false

    private static let key = "notificationPrefs"

    init() {}

    /// What an older app saved lacks the newer kinds: those take their defaults.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let d = NotificationPrefs()
        sessionsStarted = try c.decodeIfPresent(Bool.self, forKey: .sessionsStarted) ?? d.sessionsStarted
        questions = try c.decodeIfPresent(Bool.self, forKey: .questions) ?? d.questions
        failures = try c.decodeIfPresent(Bool.self, forKey: .failures) ?? d.failures
        turnsDone = try c.decodeIfPresent(Bool.self, forKey: .turnsDone) ?? d.turnsDone
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
