import HermeshHer
import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI

/// Her's page (docs/her-panel-demo.html), from her mark: her name, and under it three tabs, one kind of thing in
/// each. Work: what she is doing (Progress), the files she sent and her scheduled tasks. Abilities: what she
/// remembers, her skills, the apps and services she is connected to, and whether she searches the web and uses
/// browsers. Settings: her model, whether she asks before acting, her notifications, her environment, and clearing
/// her conversation. Each card is only a way in, with at most a word of how it stands; what it holds is on its page.
/// Remote Desktop, used often, is the glass button at the top right. A Her that is on without her grant
/// (docs/her-mesh.md §3.4) has one more row, which signs it.
struct HerSettings: View {
    @Environment(AppModel.self) private var model

    private var reviewDemo: Bool {
        #if DEBUG
        if DemoService.herDemoOn { return false } // preserve the existing developer-only settings gallery
        #endif
        return model.isDemo
    }

    var body: some View {
        if reviewDemo {
            List {
                Section {
                    Text("Her uses preset sample tasks in this demo. No model, account, computer or service is connected.")
                    Button("Progress") { model.path.append(.progress) }
                } header: { Text("Demo Mode") }
                Section {
                    Text("In your own workspace, Her can use memory, files, scheduled tasks and connected tools on your Mac or Linux computer. Exit the demo to configure them.")
                }
            }
            .navigationTitle("Her")
        } else {
            HerEnvironmentPage { chat, machine in HerPageBody(chat: chat, machine: machine) }
        }
    }
}

/// The three tabs of Her's page.
enum HerTab: String, CaseIterable {
    case work, abilities, settings
}

private struct HerPageBody: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let machine: HerMachine
    @AppStorage("herTab") private var tab = HerTab.work
    /// What the Abilities tab's cards say of web search and the browsers, once read.
    @State private var searching: Bool?
    @State private var browsing: Bool?
    /// Her remembers nothing yet: the Memory card says so, as her settings' row did.
    @State private var memoryEmpty: Bool?
    /// Moves each time the page shows again, back from one of its pages: what the cards say is read again.
    @State private var shown = 0
    @State private var connectors: HerConnectorsState
    @State private var fullAccess = false
    @State private var clearing = false
    @State private var granting = false
    @State private var aiSharing: JSON?
    @State private var revokingAISharing = false
    /// The Computer Control session on screen (docs/computer-control.md).
    @State private var control: ScreenSession?

    init(chat: HerChat, machine: HerMachine) {
        self.chat = chat
        self.machine = machine
        _connectors = State(initialValue: HerConnectorsState(chat: chat))
    }

    var body: some View {
        // A page of its own making, not a list: the field of colour under the cards reaches past them, unclipped.
        ScrollView {
            VStack(spacing: 0) {
                VStack(spacing: 10) {
                    HerAvatar(size: 88, busy: chat.live.map(herBusy) ?? false)
                    Text(chat.bot?.name ?? "Her").font(.title2.weight(.semibold))
                }
                .frame(maxWidth: .infinity)
                HerTabs(tab: $tab).padding(.top, 8)
                Group {
                    switch tab {
                    case .work: work
                    case .abilities: abilities
                    case .settings: settings
                    }
                }
                .padding(.top, 4)
                // The tab left goes at once; the one chosen comes in.
                .transition(.asymmetric(insertion: .opacity.combined(with: .offset(y: 6)), removal: .identity))
                .id(tab)
            }
            .padding(.horizontal, 16).padding(.bottom, 32)
        }
        .background(Color(.systemGroupedBackground))
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            if machine.status.screen.available, model.api?.live != nil {
                ToolbarItem(placement: .topBarTrailing) {
                    Button { openControl() } label: { Label("Remote Desktop", systemImage: "display").labelStyle(.titleAndIcon) }
                        .disabled(!herReachable(machine))
                        .accessibilityIdentifier("her-computer-control")
                }
            }
        }
        .animation(.snappy(duration: 0.28), value: tab)
        .modifier(HerLoadsConnectors(state: connectors))
        .onAppear { shown += 1 }
        .task(id: "\(herReachable(machine)) \(chat.envUp) \(chat.settingsChanged) \(tab == .abilities) \(shown)") {
            guard herReachable(machine), tab == .abilities else { return }
            // Web search and the browsers at once, on requests of their own; her memory on her connection, as its page reads it.
            if herAnswers(machine.her, "web_search/status") != false { searching = (try? await chat.abilities.search())?.enabled }
            if herAnswers(machine.her, "browser/status") != false { browsing = (try? await chat.abilities.browser())?.controlling }
            if chat.envUp, let bot = chat.bot, herAnswers(machine.her, "bots/memory") != false { memoryEmpty = (try? await HerMemory.load(chat, bot: bot.id))?.isEmpty }
        }
        .task(id: "\(tab == .settings) \(herReachable(machine)) \(chat.live?.model ?? "") \(chat.aiSharingChanged) \(shown)") {
            aiSharing = nil
            if tab == .settings, herReachable(machine) { aiSharing = try? await chat.aiSharingStatus() }
        }
        .herAISharingAlert(chat)
        .confirmationDialog("Clear your chat with “\(chat.bot?.name ?? "Her")”?", isPresented: $clearing, titleVisibility: .visible) {
            Button("Clear", role: .destructive) { clear() }
        } message: {
            Text("The chat history is cleared and the work it is doing stops. What it remembers, its files, scheduled tasks and settings stay.")
        }
        // What full access means, before it is turned on (the Mac's AccessSheet).
        .confirmationDialog("Give “\(chat.bot?.name ?? "Her")” full access?", isPresented: $fullAccess, titleVisibility: .visible) {
            Button("Turn On Full Access", role: .destructive) { setMode("bypass-permissions") }
        } message: {
            Text("It acts on \(machine.machineName) without asking you, and so do the agents and scheduled tasks it starts. Her reads web pages and messages from anyone, so turn this on only if you trust everything on that machine to it.")
        }
        .fullScreenCover(item: $control) { control in
            ScreenControlView(session: control).environment(model)
        }
    }

    // MARK: Work

    @ViewBuilder private var work: some View {
        let bot = chat.bot
        let older = herAnswers(machine.her, "deliveries/list") == false
        let routines: HerEntryStatus? = switch herRoutineDot(bot?.routines ?? []) {
        case .failed: .attention(String(localized: "Last run failed"))
        case .running: .active(String(localized: "Running…"))
        case nil: nil
        }
        // Progress is no card here: a button over her conversation, as often as it is looked at (docs/her-team.md §15).
        HerEntryCards {
            HStack(spacing: 10) {
                HerEntryCard(title: "Files", symbol: "tray", id: "her-files",
                        status: older ? .attention(String(localized: "Needs Update")) : (bot?.newDeliveries ?? 0) > 0 ? .plain(String(localized: "\(bot?.newDeliveries ?? 0) new")) : nil) { go(.herFiles) }
                HerEntryCard(title: "Scheduled Tasks", symbol: "clock", id: "her-routines", status: routines) { go(.routines) }
            }
        }
    }

    // MARK: Abilities

    @ViewBuilder private var abilities: some View {
        let lacksMemory = herAnswers(machine.her, "bots/memory") == false
        let lacksSkills = herAnswers(machine.her, "skills/list") == false
        HerEntryCards {
            HStack(spacing: 10) {
                HerEntryCard(title: "Memory", symbol: "brain", id: "her-memory",
                             status: lacksMemory ? .attention(String(localized: "Needs Update")) : memoryEmpty == true ? .plain(String(localized: "Nothing Yet")) : nil) { go(.herMemory) }
                HerEntryCard(title: "Skills", symbol: "book", id: "her-skills", status: lacksSkills ? .attention(String(localized: "Needs Update")) : nil) { go(.herSkills) }
            }
            HerConnectorTray(state: connectors)
            HStack(spacing: 10) {
                HerEntryCard(title: "Web Search", symbol: "magnifyingglass", id: "her-search", status: searching.map { .plain($0 ? String(localized: "her.status.on", defaultValue: "On") : String(localized: "her.status.off", defaultValue: "Off")) }) { go(.herSearch) }
                HerEntryCard(title: "Browser", symbol: "macwindow", id: "her-browser", status: browsing.map { .plain($0 ? String(localized: "her.status.on", defaultValue: "On") : String(localized: "her.status.off", defaultValue: "Off")) }) { go(.herBrowser) }
            }
        }
    }

    // MARK: Settings

    @ViewBuilder private var settings: some View {
        VStack(spacing: 0) {
            if herLacksGrant(machine) {
                HerGroup(footer: herGrantNote) {
                    Button { allowMesh() } label: { HerRowLabel { Text("Let Her use your computers").foregroundStyle(Color.accentColor) } }
                        .buttonStyle(HerRowStyle())
                        .disabled(granting || !herReachable(machine))
                        .accessibilityIdentifier("her-allow-mesh")
                }
            }
            HerGroup(footer: String(localized: "The agents and scheduled tasks Her starts work the same way, while you are away as well. Off, no device notifies you of Her; what is unread still shows.")) {
                NavigationLink(value: Route.herModel) {
                    HerRowLabel(chevron: true) {
                        Text("Model")
                        Spacer(minLength: 12)
                        Text(modelLine).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
                .buttonStyle(HerRowStyle())
                .accessibilityIdentifier("her-model-page")
                if let s = chat.live {
                    HerDivider()
                    HerRowLabel {
                        Text("Before Acting")
                        Spacer(minLength: 12)
                        Picker("Before Acting", selection: Binding(get: { s.permissionMode == "bypass-permissions" }, set: { full in
                            if full { fullAccess = true } else if s.permissionMode == "bypass-permissions" { setMode("manual") }
                        })) {
                            Text("Ask Me").tag(false)
                            Text("Full Access").tag(true)
                        }
                        .pickerStyle(.segmented).fixedSize()
                    }
                    .accessibilityElement(children: .contain)
                    .accessibilityIdentifier("her-mode")
                }
                if let bot = chat.bot {
                    HerDivider()
                    HerRowLabel {
                        Toggle("Notifications", isOn: Binding(get: { !bot.muted }, set: { on in
                            Task {
                                do { _ = try await chat.callAlone("bots/update", ["bot_id": .string(bot.id), "muted": .bool(!on)]) } catch { model.show(error.localizedDescription) }
                            }
                        }))
                        .accessibilityIdentifier("her-notifications")
                    }
                }
            }
            HerGroup {
                NavigationLink(value: Route.herEnvironment) {
                    HerRowLabel(chevron: true) {
                        Text("Environment")
                        Spacer(minLength: 12)
                        Text(machine.machineName).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
                .buttonStyle(HerRowStyle())
                .accessibilityIdentifier("her-environment")
            }
            if let recipient = aiSharing?["allowed"], let endpoint = recipient["endpoint"].string {
                HerGroup(footer: endpoint) {
                    Button(role: .destructive) {
                        revokingAISharing = true
                        Task {
                            defer { revokingAISharing = false }
                            do {
                                try await chat.revokeAISharing()
                                aiSharing = nil
                            } catch { model.show(error.localizedDescription) }
                        }
                    } label: {
                        HerRowLabel { Text("Revoke Third-Party AI Sharing").foregroundStyle(.red) }
                    }
                    .buttonStyle(HerRowStyle())
                    .disabled(revokingAISharing || !herReachable(machine))
                    .accessibilityIdentifier("her-ai-sharing-revoke")
                }
            } else if let endpoint = aiSharing?["recipient"]["endpoint"].string {
                HerGroup(footer: endpoint) {
                    Button { chat.reviewAISharing() } label: {
                        HerRowLabel(chevron: true) { Text("Review Third-Party AI Sharing") }
                    }
                    .buttonStyle(HerRowStyle())
                    .disabled(!herReachable(machine))
                    .accessibilityIdentifier("her-ai-sharing-review")
                }
            }
            if chat.bot != nil {
                HerGroup {
                    Button(role: .destructive) { clearing = true } label: { HerRowLabel { Text("Clear Conversation").foregroundStyle(.red) } }
                        .buttonStyle(HerRowStyle())
                        .accessibilityIdentifier("her-clear")
                }
            }
        }
        .padding(.top, 10)
    }

    /// The model and, when it offers levels, how hard it thinks: "GPT-5.5 · High".
    private var modelLine: String {
        guard let s = chat.live else { return "" }
        let title = chat.models.title(s.model)
        return chat.models.thinkingLevels(s.model).isEmpty ? title : "\(title) · \(herThinkingName(s.thinking))"
    }

    private func go(_ r: Route) { model.path.append(r) }

    private func clear() {
        guard let bot = chat.bot else { return }
        Task {
            do {
                _ = try await chat.callAlone("bots/clear", ["bot_id": .string(bot.id)])
                model.show(String(localized: "Chat cleared"))
            } catch { model.show(error.localizedDescription) }
        }
    }

    /// Computer Control opens once the owner is known to hold the phone.
    private func openControl() {
        Task {
            guard await Authentication.confirm(String(localized: "Control \(machine.machineName)")) != nil else { return }
            control = ScreenSession(machineID: machine.machineID, machineName: machine.machineName, linux: machine.status.platform.hasPrefix("linux/"))
        }
    }

    /// Lets Her use the owner's computers: SetHer once more, which signs her grant alone. The row said what it
    /// gives, so Face ID asks at once; her machine then says she holds it, and the row goes.
    private func allowMesh() {
        guard let api = model.api else { return }
        granting = true
        model.signer.expect(.types([SigningPreview.adminCommand, SigningPreview.grant]))
        var r = Hermesh_Local_V1_SetHerRequest()
        r.machineID = machine.machineID
        r.enabled = true
        Task {
            do { let _: Hermesh_Local_V1_SetHerResponse = try await api.call("SetHer", r, timeout: signingTimeout) } catch { model.report(error) }
            granting = false
        }
    }

    /// How freely Her acts (SetHerAssistantMode): her conversation says so once it is set.
    private func setMode(_ mode: String) {
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

/// The tabs under her name: a capsule of three, the one shown filled.
private struct HerTabs: View {
    @Binding var tab: HerTab
    @Namespace private var pill

    var body: some View {
        HStack(spacing: 2) {
            ForEach(HerTab.allCases, id: \.self) { t in
                Button { tab = t } label: {
                    Label(title(t), systemImage: symbol(t))
                        .font(.subheadline.weight(t == tab ? .semibold : .medium))
                        .foregroundStyle(t == tab ? .primary : .secondary)
                        .frame(maxWidth: .infinity, minHeight: 44)
                        .background {
                            if t == tab { Capsule().fill(Palette.fill3).matchedGeometryEffect(id: "pill", in: pill) }
                        }
                        .contentShape(.capsule)
                }
                .buttonStyle(.plain)
                .accessibilityAddTraits(t == tab ? .isSelected : [])
                .accessibilityIdentifier("her-tab-\(t.rawValue)")
            }
        }
        .padding(4)
        .background(Color(.secondarySystemGroupedBackground), in: .capsule)
        .padding(.vertical, 10)
    }

    private func title(_ t: HerTab) -> LocalizedStringKey {
        switch t {
        case .work: "Work"
        case .abilities: "Abilities"
        case .settings: "Settings"
        }
    }

    private func symbol(_ t: HerTab) -> String {
        switch t {
        case .work: "checklist"
        case .abilities: "puzzlepiece.extension"
        case .settings: "gearshape"
        }
    }
}

/// The cards of a tab: panes of glass over a field of Her's own colours, which reaches past them.
private struct HerEntryCards<Content: View>: View {
    @ViewBuilder let content: Content
    @State private var size: CGSize?

    var body: some View {
        VStack(spacing: 10) { content }
            .coordinateSpace(.named(HerCardTint.space))
            .onGeometryChange(for: CGSize.self) { $0.size } action: { size = $0 }
            .environment(\.herCardsSize, size)
            .padding(.top, 14)
    }
}

extension EnvironmentValues {
    /// The size of the cards' area, which their colours are drawn across.
    @Entry var herCardsSize: CGSize?
}

/// Her's colours behind a card's glass, and only there: the part of one field, drawn across the cards' area, that
/// the card covers. The page around the cards, the gaps between them included, keeps its own background. Drawn on a
/// canvas the card's size, so nothing of it reaches past the card (a larger view would widen the card's frame for
/// VoiceOver, and where a tap on it lands).
private struct HerCardTint: ViewModifier {
    static let space = "her-cards"
    @Environment(\.herCardsSize) private var area

    func body(content: Content) -> some View {
        content.background {
            if let area {
                GeometryReader { g in HerCardField(area: area, at: g.frame(in: .named(Self.space)).origin) }
                    .clipShape(RoundedRectangle(cornerRadius: 26, style: .continuous))
                    .allowsHitTesting(false)
                    .accessibilityHidden(true)
            }
        }
    }
}

/// Her's lilac, rose, sky and violet, blurred into one another across `area`, as seen from `at` in it.
private struct HerCardField: View {
    @Environment(\.colorScheme) private var scheme
    let area: CGSize
    let at: CGPoint

    var body: some View {
        Canvas { ctx, _ in
            let w = area.width, h = area.height, r = min(w, h * 1.6)
            ctx.opacity = scheme == .dark ? 0.85 : 0.75
            ctx.addFilter(.blur(radius: 40))
            ctx.translateBy(x: -at.x, y: -at.y)
            for (rgb, x, y, k) in [(0x8f9bf0, 0.16, 0.16, 0.4), (0xf29bc0, 0.84, 0.22, 0.36), (0x7fc4ea, 0.56, 0.62, 0.42), (0xb59cf2, 0.14, 0.9, 0.38)] {
                let radius = r * k
                let color = Color(red: Double(rgb >> 16 & 0xff) / 255, green: Double(rgb >> 8 & 0xff) / 255, blue: Double(rgb & 0xff) / 255)
                ctx.fill(Path(ellipseIn: CGRect(x: w * x - radius, y: h * y - radius, width: radius * 2, height: radius * 2)), with: .color(color))
            }
        }
    }
}

/// A group of rows, as a grouped list draws one, with words under it.
private struct HerGroup<Content: View>: View {
    var footer: String?
    @ViewBuilder let content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 7) {
            VStack(spacing: 0) { content }
                .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
            if let footer {
                Text(footer).font(.footnote).foregroundStyle(.secondary).padding(.horizontal, 16)
            }
        }
        .padding(.top, 18)
    }
}

/// A row of a group: its words, and what goes at its end.
private struct HerRowLabel<Content: View>: View {
    var chevron = false
    @ViewBuilder let content: Content

    var body: some View {
        HStack(spacing: 6) {
            content
            if chevron { Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(.tertiary) }
        }
        .foregroundStyle(.primary)
        .frame(maxWidth: .infinity, minHeight: 52, alignment: .leading)
        .padding(.horizontal, 16)
        .contentShape(.rect)
    }
}

/// A row that leads somewhere or does something: shaded while pressed, as a list's row is.
private struct HerRowStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label.background(configuration.isPressed ? Palette.fill2 : .clear)
    }
}

private struct HerDivider: View {
    var body: some View { Divider().padding(.leading, 16) }
}

/// What a card says of how its page stands, when it says anything.
enum HerEntryStatus {
    case plain(String)
    /// Something waits for the owner.
    case attention(String)
    /// Something runs now.
    case active(String)
}

/// A way in: its name, at most a word of how it stands, and its glyph, on a pane of glass.
private struct HerEntryCard: View {
    let title: LocalizedStringKey
    let symbol: String
    let id: String
    var status: HerEntryStatus?
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            VStack(alignment: .leading, spacing: 3) {
                Text(title).font(.title3.weight(.bold))
                switch status {
                case .plain(let s): Text(s).font(.subheadline).foregroundStyle(.secondary)
                case .attention(let s): Label { Text(s) } icon: { Circle().fill(.orange).frame(width: 8, height: 8) }.font(.subheadline).foregroundStyle(.orange)
                case .active(let s): Text(s).font(.subheadline).foregroundStyle(Color.accentColor)
                case nil: EmptyView()
                }
                Spacer(minLength: 0)
            }
            .frame(maxWidth: .infinity, minHeight: 84, alignment: .topLeading)
            .overlay(alignment: .bottomTrailing) {
                Image(systemName: symbol).font(.title3).foregroundStyle(.secondary).accessibilityHidden(true)
            }
            .padding(.horizontal, 16).padding(.vertical, 14)
            .contentShape(.rect(cornerRadius: 26))
            .glassEffect(.regular.interactive(), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
            .modifier(HerCardTint())
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier(id)
    }
}

/// The connectors as themselves: each one on, marked where it needs the owner, then a place to add one. The tray
/// opens the connectors page; a service opens its own.
private struct HerConnectorTray: View {
    @Environment(AppModel.self) private var model
    let state: HerConnectorsState

    var body: some View {
        let on = state.shown.filter(\.enabled)
        let attention = on.filter { state.look($0).needsOwner }.count
        let room = on.count > 4 ? 3 : 4
        VStack(alignment: .leading, spacing: 12) {
            Button { model.path.append(.herConnectors) } label: {
                HStack(spacing: 6) {
                    Text("Connectors").font(.title3.weight(.bold)).foregroundStyle(.primary)
                    Spacer(minLength: 8)
                    if let list = state.list {
                        if attention > 0 {
                            Label { Text("\(attention) need attention") } icon: { Circle().fill(.orange).frame(width: 8, height: 8) }.foregroundStyle(.orange)
                        } else if list.isEmpty {
                            Text("None Yet").foregroundStyle(.secondary)
                        } else {
                            Text("\(on.count) on").foregroundStyle(.secondary)
                        }
                    }
                    Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(.tertiary)
                }
                .font(.subheadline)
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("her-connectors")
            HStack(alignment: .top, spacing: 0) {
                ForEach(on.prefix(room)) { c in
                    Button { model.path.append(.herConnector(c.name)) } label: {
                        tile(name: herConnectorTitle(c.name)) {
                            HerConnectorTile(name: c.name, icon: c.icon, command: c.command, size: 46)
                                .overlay(alignment: .topTrailing) {
                                    if state.look(c).needsOwner { Circle().fill(.orange).frame(width: 11, height: 11).offset(x: 3, y: -3) }
                                }
                        }
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("her-tray-\(c.name)")
                    Spacer(minLength: 0)
                }
                if on.count > room {
                    Button { model.path.append(.herConnectors) } label: {
                        tile(name: String(localized: "More")) {
                            Text(verbatim: "+\(on.count - room)").font(.headline).foregroundStyle(.secondary)
                                .frame(width: 46, height: 46).background(Palette.fill2, in: RoundedRectangle(cornerRadius: 11, style: .continuous))
                        }
                    }
                    .buttonStyle(.plain)
                    Spacer(minLength: 0)
                }
                Button { model.path.append(.herConnectors) } label: {
                    tile(name: String(localized: "Add")) {
                        Image(systemName: "plus").font(.title3).foregroundStyle(.secondary).frame(width: 46, height: 46)
                            .overlay(RoundedRectangle(cornerRadius: 11, style: .continuous).strokeBorder(.secondary, lineWidth: 1.2))
                    }
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("her-tray-add")
            }
        }
        .padding(.horizontal, 16).padding(.top, 14).padding(.bottom, 12)
        .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 26, style: .continuous))
        .modifier(HerCardTint())
    }

    private func tile(name: String, @ViewBuilder icon: () -> some View) -> some View {
        VStack(spacing: 6) {
            icon()
            Text(name).font(.caption2).foregroundStyle(.secondary).lineLimit(1).frame(maxWidth: 60)
        }
        .contentShape(.rect)
    }
}

/// What turning Her on lets her do (docs/her-mesh.md §3.6), said before it is signed.
private var herGrantNote: String {
    String(localized: "Her can read coding sessions on your computers. Starting tasks, sending follow-ups and answering agents require a verifiable AI recipient. Turn Her off to end access.")
}

/// Her was on once on m and her data is still there (docs/her-server.md §5.6): as m says while it answers, else
/// as last seen. A machine older than this says neither.
func herData(_ m: HerMachine) -> Bool { m.hasStatus ? m.status.her.data : m.her.data }

/// Where Her can run, to turn her on (docs/her-server.md §9.2): the owner's Macs and Linux machines, those online
/// first; one offline, a Windows one or one that can't run her shows why. Turning her on is signed with Face ID,
/// and the same confirmation lets her use the owner's computers (docs/her-mesh.md §3.3): the line under the
/// choices says so.
struct HerChoices: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dynamicTypeSize) private var size
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
            } else {
                Text(herGrantNote)
                    .font(.footnote).foregroundStyle(.secondary).frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal, 4).padding(.top, 4)
            }
        }
    }

    private func row(_ c: HerChoice) -> some View {
        let m = c.machine
        let detail: String = switch c.why {
        case .windows: String(localized: "Update Hermesh on this PC to run Her")
        case .needsUpdate: String(localized: "Hermesh on this computer is older. Update it there to run Her")
        case .offline: m.hasLastSeen ? String(localized: "Offline · seen \(agoText(m.lastSeen.date, now: model.now))") : String(localized: "Offline")
        case .unsupported(let why): String(localized: "Can’t run Her: \(why)")
        // Its operating system once it has answered.
        case nil: m.hasStatus ? String(localized: "Online · \(platformText(m.status.platform))") : String(localized: "Online")
        }
        // In the largest text sizes the button goes under the machine, whose name then has the row's width.
        let layout = size.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 10)) : AnyLayout(HStackLayout(spacing: 12))
        return layout {
            HStack(spacing: 12) {
                Image(systemName: machineIcon(m)).font(.body).frame(width: 36, height: 36)
                    .background(Color(.systemBackground), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                VStack(alignment: .leading, spacing: 2) {
                    Text(m.machineName).font(.subheadline.weight(.semibold))
                    Text(detail).font(.caption).foregroundStyle(.secondary).lineLimit(size.isAccessibilitySize ? nil : 2)
                    if herData(m) { Text("Her’s data from before is still here").font(.caption).foregroundStyle(.secondary) }
                }
            }
            if !size.isAccessibilitySize { Spacer(minLength: 8) }
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
        if signedHere { model.signer.expect(.types([SigningPreview.adminCommand, SigningPreview.grant])) }
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

/// The Her entry while Her runs nowhere (docs/her-team.md §4.9): her, asleep, and where she should run. Not Now
/// makes Work the first page until a machine runs her.
struct HerFirstRun: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        ScrollView {
            VStack(spacing: 0) {
                HerAvatar(size: 112, asleep: true)
                Text("Choose where Her runs").font(.title2.bold()).padding(.top, 10)
                Text("She runs on a computer of yours and leads the coding agents on all of them. Her conversations, files and model keys stay on that machine.")
                    .font(.subheadline).foregroundStyle(.secondary).multilineTextAlignment(.center).padding(.top, 4).padding(.bottom, 18)
                HerChoices()
                Button("Not Now") {
                    model.herLater = true
                    model.open(.overview)
                }
                .font(.subheadline).foregroundStyle(.secondary)
                .frame(minHeight: 44).padding(.top, 8)
                .accessibilityIdentifier("her-not-now")
            }
            .padding(.horizontal, 20).padding(.top, 8)
        }
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { MenuToolbarItem() }
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

/// A thinking level as the Mac's thinking menu names it.
func herThinkingName(_ l: String) -> String {
    switch l {
    case "default": String(localized: "Default")
    case "off": String(localized: "No Thinking")
    case "on": String(localized: "Thinking On")
    case "minimal": String(localized: "Minimal")
    default: effortName(l)
    }
}
