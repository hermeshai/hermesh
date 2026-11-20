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
        } else if model.hasWorld {
            HerFirstRun()
        } else { // the machines are not read yet: whether she runs is not known
            PageLoader(text: "Reading your machines…").toolbar { MenuToolbarItem() }
        }
    }
}

/// One environment's Her: the menu button, her mark large in the middle with her name tag over its lower edge, and
/// the conversation scrolling under them, fading as it reaches them. The tag says what the team is doing
/// (docs/her-team.md §4.2) and opens the team panel; her mark opens her page (docs/her-panel-demo.html), where her
/// work, her abilities and her settings are, or closes the panel while it shows; it is marked while something of hers
/// waits for the owner. The header keeps its size and position while the keyboard opens.
private struct HerPage: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var phase
    @Environment(\.accessibilityReduceMotion) private var still
    /// With the bars on the side, the menu button is in the system's bar, as on every page; the top is still hers.
    @Environment(\.barsOnTheSide) private var sideBars
    let machine: HerMachine
    /// The team panel shows.
    @State private var team = false
    /// Connection diagnostics sheet opened from the tag when a network issue overrides team status.
    @State private var showDiagnostics = false
    /// Staged debounce for Her's own connecting state: hidden (<400ms), subtle dot (0.4-3s), prominent text (>3s).
    @State private var linkTier: HerLinkTier = .hidden
    /// How tall the status bar is, where the conversation fades out.
    @State private var statusBar: CGFloat = 0
    /// How tall the bar under it is (the buttons, her mark and the tag), where the conversation fades out.
    @State private var barHeight: CGFloat = 0
    /// How tall the band where the conversation fades out is.
    private let fade: CGFloat = 20

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
        // The whole page whatever shows: her mark at the top and the panel's dim over all of it, not around a
        // status as tall as its words.
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        // Scrolled up, the conversation fades out in a short band at the bar's lower edge and is gone under her mark,
        // the buttons and the clock: no blurred message reads behind them, and no line where a blur would stop.
        .scrollEdgeEffectHidden(true, for: .top)
        .mask {
            VStack(spacing: 0) {
                Color.clear.frame(height: max(statusBar + barHeight - fade, statusBar))
                LinearGradient(colors: [.clear, .black], startPoint: .top, endPoint: .bottom).frame(height: fade)
                Color.black
            }
            .ignoresSafeArea()
        }
        // Over the conversation and its composer, under her mark: the panel grows from where the tag is.
        .overlay(alignment: .top) {
            if team {
                ZStack(alignment: .top) {
                    Color.black.opacity(0.22).ignoresSafeArea()
                        .onTapGesture { showTeam(false) }
                        .accessibilityElement()
                        .accessibilityLabel("Close")
                        .accessibilityAddTraits(.isButton)
                        .accessibilityAction { showTeam(false) }
                        .accessibilityIdentifier("team-close")
                    TeamPanel { showTeam(false) }
                        .padding(.top, -tagHeight - 4)
                        .transition(still ? .opacity : .scale(scale: 0.3, anchor: .top).combined(with: .opacity))
                }
                .transition(.opacity)
            }
        }
        .toolbar(sideBars ? .visible : .hidden, for: .navigationBar)
        // With the bars on the side (iPhone Duo), the menu button and Progress are in the system's bar.
        .toolbar {
            if sideBars {
                MenuToolbarItem()
                if chat.bot != nil { ToolbarItem(placement: .topBarTrailing) { HerProgressButton(marks: herProgressMarks(chat), inBar: true) } }
            }
        }
        .safeAreaBar(edge: .top) { header(chat, state: state) }
        // Outside the bar, the top inset is the status bar's (or the system's bar's, beside the menu).
        .onGeometryChange(for: CGFloat.self) { $0.safeAreaInsets.top } action: { statusBar = $0 }
        // A row opened, or a sheet: the panel is done.
        .onChange(of: model.path) { team = false }
        .onChange(of: model.sheet) { team = false }
        .onChange(of: model.accountPresented) { team = false }
        .onChange(of: model.drawerOpen) { team = false }
        .onAppear { model.her.machine = machine.machineID }
        .herConnections(chat, machine: machine)
        .sheet(isPresented: $showDiagnostics) {
            HerDiagnosticsSheet(chat: chat, machine: machine, state: state)
        }
        // Progressive debounce for Her's own link: silent < 400 ms, subtle breathing dot 0.4–3 s, text > 3 s.
        .task(id: herWaiting(chat, state: state)) {
            linkTier = .hidden
            guard herWaiting(chat, state: state) else { return }
            try? await Task.sleep(for: .milliseconds(400))
            guard !Task.isCancelled else { return }
            linkTier = .subtle
            try? await Task.sleep(for: .milliseconds(2600))
            guard !Task.isCancelled else { return }
            linkTier = .prominent
        }
        // Nothing is unread while the owner looks.
        .task(id: [chat.bot?.unread ?? 0, chat.envUp ? 1 : 0, phase == .active ? 1 : 0]) { if phase == .active { await chat.markRead() } }
        .herAISharingAlert(chat)
    }

    private func header(_ chat: HerChat, state: HerEnvState) -> some View {
        ZStack(alignment: .top) {
            HStack(spacing: 8) {
                if !sideBars && !model.menuBeside { HerMenuButton() }
                Spacer()
                // Progress, looked at often: a button of its own, not a card of her page (docs/her-team.md §15).
                if chat.bot != nil, !sideBars { HerProgressButton(marks: herProgressMarks(chat)) }
            }
            VStack(spacing: -16) {
                avatar(chat, size: 72).zIndex(1)
                tag(chat, state: state)
            }
        }
        // The tag's words grow with the text size, up to what the top has room for.
        .dynamicTypeSize(...DynamicTypeSize.xxLarge)
        .padding(.horizontal, 16).padding(.bottom, 4)
        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { barHeight = $0 }
    }

    private func avatar(_ chat: HerChat, size: CGFloat) -> some View {
        // A scheduled task whose last run failed, which her page's card says; what waits in Progress, its button does.
        let waits = herRoutineDot(chat.bot?.routines ?? []) == .failed
        return Button {
            // Over the panel, it closes it: her page would open from under it.
            if team { showTeam(false) } else { model.path.append(.herSettings) }
        } label: {
            HerAvatar(size: size, busy: chat.live.map(herBusy) ?? false)
                .overlay(alignment: .topTrailing) {
                    if waits {
                        Circle().fill(.orange).frame(width: size * 0.17, height: size * 0.17)
                            .overlay(Circle().strokeBorder(Color(.systemBackground), lineWidth: 2.5))
                            .offset(x: -size * 0.03, y: size * 0.05)
                            .accessibilityHidden(true)
                    }
                }
        }
        .buttonStyle(.plain)
        .accessibilityLabel(waits ? Text("\(chat.bot?.name ?? "Her"), something is waiting for you") : Text(chat.bot?.name ?? "Her"))
        .accessibilityHint(team ? Text("Closes the team panel") : Text("Opens Her’s page"))
        .accessibilityIdentifier("her-avatar")
    }

    private func tag(_ chat: HerChat, state: HerEnvState) -> some View {
        let net = tagNetwork(chat, state: state)
        return Button {
            if net.overridesTeam {
                showDiagnostics = true
            } else {
                showTeam(true)
            }
        } label: {
            HerTag(name: chat.bot?.name ?? "Her",
                   session: chat.live,
                   team: teamState(model.sessions, requests: model.pairings.count),
                   network: net,
                   paused: phase != .active)
                .frame(height: tagHeight)
                .padding(.horizontal, 14)
                .glassEffect(.regular.interactive(), in: .capsule)
        }
        .buttonStyle(.plain)
        .accessibilityHint(net.overridesTeam ? Text("Opens connection diagnostics") : Text("Shows what your agents are doing"))
        .accessibilityIdentifier(net.isOffline ? "her-offline" : "her-tag")
        // The panel takes its place.
        .opacity(team ? 0 : 1)
        .allowsHitTesting(!team)
    }

    /// Whether Her's own usable link is still connecting (ignores slow outer
    /// relay reconnects whenever Her's direct QUIC/TCP conversation is live).
    private func herWaiting(_ chat: HerChat, state: HerEnvState) -> Bool {
        guard !model.isDemo, !chat.paused else { return false }
        if model.networkKind == "offline" { return false }
        if case .offline = state { return false }
        if case .failed = state { return false }
        if chat.convUp { return false }
        if state == .ready && !chat.noModel { return true }
        return chat.bot == nil && !model.status.relayConnected
    }

    private func tagNetwork(_ chat: HerChat, state: HerEnvState) -> HerTagNetwork {
        guard !model.isDemo, !chat.paused else { return .healthy }
        if model.networkKind == "offline" { return .noNetwork }
        if case .offline = state { return .offline(machineName: machine.machineName) }
        if case .failed = state { return .unreachable }
        // Once Her's own conversation stream is up (e.g. QUIC LAN/IPv6), stay
        // completely healthy regardless of outer relay reconnects.
        if chat.convUp { return .healthy }
        let relay = model.status.relayHealth
        if !model.status.relayConnected && (relay.phase == "offline" || relay.retryCount >= 3) {
            return .unreachable
        }
        guard herWaiting(chat, state: state) else { return .healthy }
        switch linkTier {
        case .hidden:
            return .healthy
        case .subtle:
            return .subtleConnecting
        case .prominent:
            if chat.convError != nil && chat.convRetries >= 3 {
                return .unreachable
            }
            return .reconnecting
        }
    }

    private let tagHeight: CGFloat = 36

    private func showTeam(_ on: Bool) {
        if on { UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil) }
        withAnimation(still ? .easeOut(duration: 0.15) : .snappy(duration: 0.36, extraBounce: 0.08)) { team = on }
    }
}

private enum HerLinkTier: Equatable {
    case hidden, subtle, prominent
}

/// Single-slot network state rendered inside Her's bottom name tag.
/// Severe network/offline states outrank team task status and open diagnostics on tap.
private enum HerTagNetwork: Equatable {
    case healthy
    case subtleConnecting
    case noNetwork
    case offline(machineName: String)
    case reconnecting
    case unreachable

    var overridesTeam: Bool {
        switch self {
        case .healthy, .subtleConnecting: false
        case .noNetwork, .offline, .reconnecting, .unreachable: true
        }
    }

    var isOffline: Bool {
        if case .offline = self { return true }
        return false
    }
}

/// Her's name tag: the single status slot under her avatar. Severe network/offline
/// issues take priority over team activity; brief reconnects (0.4–3 s) show only a
/// subtle breathing dot beside her name; while Her answers (and isn't awaiting
/// approval), her live status replaces her name in place, keeping the team panel tap.
private struct HerTag: View {
    @Environment(\.accessibilityReduceMotion) private var still
    let name: String
    let session: HerSession?
    let team: TeamState
    let network: HerTagNetwork
    /// Nothing turns while the app is not in front.
    let paused: Bool

    private static let sweep = 2.4

    var body: some View {
        TimelineView(.periodic(from: .now, by: session?.live?.retryAt != nil ? 1 : 60)) { tick in
            let status = network.overridesTeam ? nil : session.flatMap { herStatus($0, now: tick.date.timeIntervalSince1970 * 1000) }
            let statusText = status.map(Self.words(for:))
            content(statusText: statusText)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(Text(verbatim: spoken(statusText: statusText)))
        }
    }

    private func content(statusText: String?) -> some View {
        HStack(spacing: 7) {
            HStack(spacing: 5) {
                if let statusText {
                    TimelineView(.animation(paused: paused || still)) { t in
                        let swept = t.date.timeIntervalSinceReferenceDate / Self.sweep
                        let phase = swept - swept.rounded(.down)
                        Text(verbatim: statusText)
                            .font(.subheadline.weight(.medium))
                            .foregroundStyle(.secondary)
                            .mask { if paused || still { Rectangle() } else { light(at: phase) } }
                    }
                } else {
                    Text(name).font(.headline)
                }
                if network == .subtleConnecting {
                    breathingDot(size: 6)
                        .transition(.opacity)
                }
            }
            if network.overridesTeam {
                Capsule().fill(.tertiary).frame(width: 1, height: 14)
                switch network {
                case .noNetwork:
                    dot(.orange)
                    Text("No network").foregroundStyle(.orange)
                case .offline:
                    dot(.secondary)
                    Text("Offline").foregroundStyle(.secondary)
                case .reconnecting:
                    breathingDot(size: 7)
                    Text("Reconnecting").foregroundStyle(.orange)
                case .unreachable:
                    dot(.red)
                    Text("Connection lost").foregroundStyle(.red)
                case .healthy, .subtleConnecting:
                    EmptyView()
                }
            } else if team.tag != .quiet {
                Capsule().fill(.tertiary).frame(width: 1, height: 14)
                switch team.tag {
                case .waiting(let agent):
                    dot(.orange)
                    Text("\(agentShortTitle(agent)) is waiting").foregroundStyle(.orange)
                case .needYou(let n):
                    dot(.orange)
                    Text("\(n) need you").foregroundStyle(.orange)
                case .failed(let n):
                    dot(.red)
                    Text("\(n) failed")
                case .working(let n):
                    HStack(spacing: -5) {
                        ForEach(team.runningAgents.prefix(3), id: \.self) { AgentLogo(id: $0, size: 18) }
                    }
                    Text("\(n) working").foregroundStyle(.secondary)
                case .quiet:
                    EmptyView()
                }
                if !team.running.isEmpty {
                    if case .working = team.tag {} else {
                        Text(verbatim: "\(team.running.count)").foregroundStyle(.secondary).monospacedDigit()
                    }
                    runningRing
                }
            }
        }
        .font(.subheadline.weight(.medium))
        .lineLimit(1)
        .animation(still ? nil : .snappy, value: team.tag)
        .animation(still ? nil : .easeInOut(duration: 0.22), value: network)
        .animation(still ? nil : .easeInOut(duration: 0.2), value: statusText)
    }

    private static func words(for status: HermeshHer.HerStatus) -> String {
        switch status {
        case .typing: String(localized: "Typing…")
        case .compacting: String(localized: "Compacting…")
        case .retrying: String(localized: "Retrying…")
        }
    }

    private func light(at phase: Double) -> some View {
        GeometryReader { g in
            let w = g.size.width
            LinearGradient(stops: [.init(color: .black, location: 0.38), .init(color: .black.opacity(0.28), location: 0.5), .init(color: .black, location: 0.62)],
                           startPoint: .leading, endPoint: .trailing)
                .frame(width: w * 3)
                .offset(x: -2 * w + 2 * w * phase)
        }
    }

    private func breathingDot(size: CGFloat) -> some View {
        TimelineView(.animation(minimumInterval: 1 / 20, paused: paused || still)) { timeline in
            let t = paused || still ? 0.0 : timeline.date.timeIntervalSinceReferenceDate
            let opacity = 0.35 + 0.65 * (0.5 + 0.5 * sin(t * 3.8))
            Circle()
                .fill(.orange.opacity(opacity))
                .frame(width: size, height: size)
        }
        .accessibilityHidden(true)
    }

    /// What the tag says, in words.
    private func spoken(statusText: String?) -> String {
        var parts = [statusText ?? name]
        switch network {
        case .healthy:
            break
        case .subtleConnecting:
            parts.append(String(localized: "Connecting…"))
        case .noNetwork:
            return (parts + [String(localized: "No network")]).joined(separator: ", ")
        case .offline(let machineName):
            return (parts + [String(localized: "\(machineName) is offline")]).joined(separator: ", ")
        case .reconnecting:
            return (parts + [String(localized: "Reconnecting")]).joined(separator: ", ")
        case .unreachable:
            return (parts + [String(localized: "Connection lost")]).joined(separator: ", ")
        }
        switch team.tag {
        case .waiting(let agent): parts.append(String(localized: "\(agentShortTitle(agent)) is waiting"))
        case .needYou(let n): parts.append(String(localized: "\(n) need you"))
        case .failed(let n): parts.append(String(localized: "\(n) failed"))
        case .working(let n): parts.append(String(localized: "\(n) working"))
        case .quiet: break
        }
        if case .working = team.tag {} else if team.tag != .quiet && !team.running.isEmpty { parts.append("\(team.running.count)") }
        return parts.joined(separator: ", ")
    }

    private func dot(_ color: Color) -> some View { Circle().fill(color).frame(width: 9, height: 9) }

    /// Like Her's avatar, draw the continuous motion without a never-ending UIKit animation.
    private var runningRing: some View {
        TimelineView(.animation(minimumInterval: 1 / 30, paused: paused || still)) { timeline in
            let angle = paused || still ? 0 : timeline.date.timeIntervalSinceReferenceDate.truncatingRemainder(dividingBy: 1) * 360
            Canvas { context, size in
                var arc = Path()
                arc.addArc(center: CGPoint(x: size.width / 2, y: size.height / 2), radius: 5,
                           startAngle: .degrees(angle), endAngle: .degrees(angle + 270), clockwise: false)
                context.stroke(arc, with: .color(.secondary), style: StrokeStyle(lineWidth: 1.5, lineCap: .round))
            }
        }
        .frame(width: 12, height: 12)
        .accessibilityHidden(true)
    }
}

/// The team panel (docs/her-team.md §4.3): what needs the owner, answered in place where one tap can, what
/// runs and what ended last, on glass under Her's mark; its head leads to Work.
private struct TeamPanel: View {
    @Environment(AppModel.self) private var model
    let close: () -> Void

    var body: some View {
        let team = teamState(model.sessions, requests: model.pairings.count)
        let today = model.work[.today]?.sessions
        // As tall as what it lists, and scrolling once that is taller than the page.
        ViewThatFits(in: .vertical) {
            content(team, today: today)
            ScrollView { content(team, today: today) }.scrollIndicators(.hidden)
        }
        .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 34, style: .continuous))
        // Short of the composer's row however much it lists: the dim to tap stays in sight on a small iPhone.
        .padding(.horizontal, 10).padding(.bottom, 88)
        .task { await model.loadWork(.today) }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("team-panel")
    }

    private func content(_ team: TeamState, today: Int?) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline) {
                Text("Team").font(.title2.bold())
                Spacer()
                Button {
                    close()
                    model.open(.overview)
                } label: {
                    HStack(spacing: 3) {
                        if let today { Text("Today · \(today) sessions") } else { Text("Overview") }
                        Image(systemName: "chevron.right").font(.caption2.weight(.semibold))
                    }
                    .font(.subheadline).foregroundStyle(.secondary)
                    .frame(minHeight: 44).contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("team-work")
            }
            .padding(.horizontal, 8)
            .contentShape(.rect)
            .simultaneousGesture(DragGesture(minimumDistance: 20).onEnded { drag in
                if drag.translation.height < -30 && abs(drag.translation.height) > abs(drag.translation.width) { close() }
            })
            if team.waiting.isEmpty && team.running.isEmpty && team.failed.isEmpty && model.pairings.isEmpty {
                Text("Nothing is running now.").font(.callout).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity).padding(.vertical, 18)
            }
            TeamLists(team: team, pairings: model.pairings, finished: 2, finishedTitle: "Just Finished", fill: AnyShapeStyle(Color(.secondarySystemGroupedBackground).opacity(0.72)))
        }
        .padding(.horizontal, 12).padding(.top, 30).padding(.bottom, 14)
    }
}

/// Her's connections while a page of hers shows the environment (docs/her-server.md §5.1): the environment's,
/// while her runs there; the conversation's, opened again when what it follows changes (a cleared conversation is a
/// new one; a model just connected opens it); and what this iPhone keeps. The pages hold them one after another
/// (HerChat.hold), so going from her conversation to Progress, a card's record or her settings opens nothing again.
private struct HerConnections: ViewModifier {
    let chat: HerChat
    let machine: HerMachine

    func body(content: Content) -> some View {
        let running = herReachable(machine) && machine.her.running
        let following = HerChat.Following(bot: chat.bot?.id, session: chat.bot?.sessionID, running: running, modelReady: machine.her.modelReady)
        content
            .task(id: following) { await chat.hold(following) }
            .task { await chat.loadKept() }
            .task(id: herReachable(machine)) { await chat.refreshWaiting() }
            // Delivered meanwhile: what waits is asked for again as the conversation grows.
            .onChange(of: chat.live?.entries.count) { if !chat.waiting.isEmpty { Task { await chat.refreshWaiting() } } }
    }
}

extension View {
    func herConnections(_ chat: HerChat, machine: HerMachine) -> some View { modifier(HerConnections(chat: chat, machine: machine)) }
}

/// Progress, at the top right of Her's conversation, or in the system's bar where it stands on the side (inBar): the
/// work she sent out to her sub-agents (docs/her-team.md §15), marked orange while one waits for the owner, else blue
/// while one is at work.
private struct HerProgressButton: View {
    @Environment(AppModel.self) private var model
    let marks: (waiting: Int, running: Int)
    var inBar = false

    var body: some View {
        Group {
            if inBar {
                // The bar draws the button, as it does the menu button beside it.
                Button { model.path.append(.progress) } label: {
                    Image(systemName: "waveform.path.ecg")
                        .overlay(alignment: .topTrailing) {
                            if let dot { Circle().fill(dot).frame(width: 8, height: 8).offset(x: 5, y: -4) }
                        }
                }
            } else {
                Button { model.path.append(.progress) } label: {
                    Image(systemName: "waveform.path.ecg").font(.body.weight(.medium)).frame(width: 44, height: 44)
                        .overlay(alignment: .topTrailing) {
                            if let dot { Circle().fill(dot).frame(width: 8, height: 8).offset(x: -9, y: 11) }
                        }
                        .glassEffect(.regular.interactive(), in: .circle)
                        .contentShape(.circle)
                }
                .buttonStyle(.plain)
            }
        }
        .accessibilityLabel(marks.waiting > 0 ? Text("Progress, \(marks.waiting) waiting for you")
            : marks.running > 0 ? Text("Progress, \(marks.running) at work") : Text("Progress"))
        .accessibilityIdentifier("her-progress")
    }

    private var dot: Color? { marks.waiting > 0 ? .orange : marks.running > 0 ? .accentColor : nil }
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
            } actions: {
                HerWayOut()
            }
        case .starting:
            HerStarting(name: name)
        default:
            PageLoader(text: "Reading the conversation…", bare: true)
        }
    }
}

/// Her starting on a machine; past the while it usually takes, it says so, and what the owner can do meanwhile.
private struct HerStarting: View {
    let name: String
    @State private var long = false

    var body: some View {
        VStack(spacing: 0) {
            PageLoader(text: "Her is starting on \(name)…")
            if long {
                VStack(spacing: 16) {
                    Text("This is taking longer than usual. You can keep waiting, look at Her’s settings, or run her on another machine.")
                        .font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center)
                    HerWayOut()
                }
                .padding(.horizontal, 32)
                .transition(.opacity)
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("her-starting-long")
            }
        }
        .task {
            try? await Task.sleep(for: .seconds(15))
            withAnimation { long = true }
        }
    }
}

/// Where the owner can go while Her doesn't run where she was turned on: her settings (turning her off there), or
/// another machine to run her on.
private struct HerWayOut: View {
    @Environment(AppModel.self) private var model
    @State private var adding = false

    var body: some View {
        VStack(spacing: 10) {
            Button("Her’s Settings") { model.path.append(.herSettings) }
                .buttonStyle(.glass)
                .accessibilityIdentifier("her-status-settings")
            Button("Turn On Her on Another Machine…") { adding = true }
                .buttonStyle(.glass)
                .accessibilityIdentifier("her-status-another")
        }
        .sheet(isPresented: $adding) { HerAddSheet().environment(model) }
    }
}


extension View {
    func herAISharingAlert(_ chat: HerChat) -> some View {
        modifier(HerAISharingAlert(chat: chat))
    }
}

private struct HerAISharingAlert: ViewModifier {
    let chat: HerChat

    func body(content: Content) -> some View {
        content.alert("Share data with third-party AI?", isPresented: Binding(
            get: { chat.aiConsentPrompt != nil },
            set: { if !$0 { chat.aiConsentPrompt = nil } }
        ), presenting: chat.aiConsentPrompt) { consent in
            Button("Allow AI Sharing") { Task { await consent.allow() } }
            Button("Don’t Allow", role: .cancel) {}
        } message: { consent in
            Text("Provider: \(consent.provider) · \(consent.endpoint)\nModel: \(consent.model)\n\nData: messages, chat history, images, attachment contents, memory, and tool results, which may include data from connected services.\n\nPurpose: generate replies and perform Her’s tasks, including scheduled tasks on this computer.")
        }
    }
}
