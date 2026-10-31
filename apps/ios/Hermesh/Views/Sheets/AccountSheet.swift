import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI
import UserNotifications

/// The account, laid out as iOS Settings: the service this iPhone belongs
/// to (its web console a tap away), usage (the days in a grid), appearance,
/// notifications and about; signing out and erasing everything last. The trusted devices are on the Machines page.
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

    var body: some View {
        NavigationStack {
            List {
                Section { service }
                if let u = model.usage {
                    Section {
                        UsageSummary(usage: u)
                    } header: {
                        Text("Usage")
                    }
                }
                Section {
                    HStack(spacing: 0) {
                        AppearanceOption(tag: "system", title: "System", selection: $appearance)
                        AppearanceOption(tag: "light", title: "Light", selection: $appearance)
                        AppearanceOption(tag: "dark", title: "Dark", selection: $appearance)
                    }
                    .padding(.vertical, 8)
                } header: {
                    Text("Appearance")
                }
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
                } header: {
                    Text("About")
                }
                // What ends: signing this iPhone out, and erasing everything (its own page).
                Section {
                    Button(role: .destructive) { signingOut = true } label: { endRow("Sign Out…") }
                    if !model.status.removed {
                        Button(role: .destructive) { erasing = true } label: { endRow("Erase Everything…") }
                            .accessibilityIdentifier("erase-everything")
                    }
                }
            }
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
            .preferredColorScheme(appearance == "light" ? .light : appearance == "dark" ? .dark : nil)
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
