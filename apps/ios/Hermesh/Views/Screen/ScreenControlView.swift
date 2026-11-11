import HermeshKit
import HermeshProto
import SwiftUI

/// Which way the phone may be held: upright everywhere, and on its side
/// too while Computer Control shows a wide screen.
enum ScreenOrientation {
    @MainActor static var allowed: UIInterfaceOrientationMask = .portrait {
        didSet {
            guard allowed != oldValue else { return }
            for case let scene as UIWindowScene in UIApplication.shared.connectedScenes {
                scene.keyWindow?.rootViewController?.setNeedsUpdateOfSupportedInterfaceOrientations()
                if allowed == .portrait { scene.requestGeometryUpdate(.iOS(interfaceOrientations: .portrait)) }
            }
        }
    }
}

/// One Computer Control session as its page shows it.
@MainActor @Observable
final class ScreenSession {
    let machineID: String
    let machineName: String
    /// The machine is a Linux one: its keys are Ctrl, Alt and Super, and its texts are its own.
    let linux: Bool

    /// The connection, as the core last reported it.
    var link = ScreenLink()
    /// The first frame is on screen.
    var live = false
    /// Why the session ended; nil while it lasts.
    var ended: String?
    /// Why the machine has no picture ("screen_recording", "consent", "locked", …), or empty.
    var machineError = ""
    /// What the machine said with it.
    var machineDetail = ""
    /// Whether the machine can post input; false leaves the owner watching.
    var canControl = true
    var keyboard = false {
        didSet {
            guard keyboard != oldValue else { return }
            if keyboard { surface.keys.becomeFirstResponder() } else { surface.keys.resignFirstResponder() }
        }
    }

    var mode: ScreenMode {
        didSet {
            guard mode != oldValue else { return }
            UserDefaults.standard.set(mode.rawValue, forKey: "screen-mode")
            surface.mode = mode
            // In trackpad mode the pointer is drawn here, so the picture leaves it out.
            var p = Hermesh_Screen_V1_ShowPointer()
            p.shown = mode == .direct
            var input = Hermesh_Screen_V1_Input()
            input.showPointer = p
            connection?.send(input)
        }
    }

    /// A word the page shows for a moment (text copied from the machine), and its identifier.
    var flashed: (text: String, id: String)?
    @ObservationIgnored private var flashEnd: Task<Void, Never>?

    func flash(_ text: String, id: String) {
        withAnimation { flashed = (text, id) }
        flashEnd?.cancel()
        flashEnd = Task { [weak self] in
            try? await Task.sleep(for: .seconds(2))
            guard !Task.isCancelled else { return }
            withAnimation(.easeOut(duration: 0.4)) { self?.flashed = nil }
        }
    }

    @ObservationIgnored let surface = ScreenSurfaceView()
    @ObservationIgnored private var connection: ScreenConnection?
    @ObservationIgnored private var endpoint: (port: Int, token: String)?

    init(machineID: String, machineName: String, linux: Bool = false) {
        self.machineID = machineID
        self.machineName = machineName
        self.linux = linux
        mode = ScreenMode(rawValue: UserDefaults.standard.string(forKey: "screen-mode") ?? "") ?? .direct
        surface.mode = mode
        surface.send = { [weak self] in self?.connection?.send($0) }
        surface.keys.send = { [weak self] in self?.connection?.send($0) }
        surface.keys.onDismiss = { [weak self] in self?.keyboard = false }
        surface.keys.onPasteTooLong = { [weak self] in self?.flash(String(localized: "Too much text to paste"), id: "screen-clipboard-too-long") }
        if linux { surface.keys.useLinux() }
        surface.picture.onFirstFrame = { [weak self] in
            self?.live = true
            self?.surface.accessibilityValue = "live"
        }
    }

    /// Opens the session (again, after it ended).
    func start(api: LocalAPI) {
        guard case .loopback(let port, let token) = api.endpoint else { return }
        endpoint = (port, token)
        connect()
    }

    func reconnect() { if endpoint != nil { connect() } }

    private func connect() {
        guard let endpoint else { return }
        connection?.close()
        link = ScreenLink()
        ended = nil
        machineError = ""
        machineDetail = ""
        let c = ScreenConnection()
        connection = c
        let picture = surface.picture
        c.onFrame = { key, data in picture.show(key: key, annexB: data) }
        picture.onNeedsKeyframe = { [weak c] in c?.requestKeyframe() }
        c.onEvent = { [weak self, weak c] ev in
            Task { @MainActor in
                guard let self, self.connection === c else { return }
                switch ev.event {
                case .format(let f): self.surface.contentSize = CGSize(width: Int(f.width), height: Int(f.height))
                case .state(let s):
                    self.machineError = s.error == "ended" ? "" : s.error
                    self.machineDetail = s.detail
                    self.canControl = s.canControl
                case .clipboard(let c):
                    // Copied on the machine: the phone's to paste too.
                    UIPasteboard.general.string = c.text
                    self.flash(String(localized: "Copied from \(self.machineName)"), id: "screen-clipboard-copied")
                case nil: break
                }
            }
        }
        c.onLink = { [weak self, weak c] link in
            Task { @MainActor in
                guard let self, self.connection === c else { return }
                if link.phase == "failed" {
                    self.ended = link.error ?? ""
                    self.keyboard = false
                } else {
                    self.link = link
                }
            }
        }
        c.start(port: endpoint.port, token: endpoint.token, machineID: machineID, name: UIDevice.current.name, pointer: mode == .direct)
    }

    /// Ends the session; reconnect opens it again.
    func stop() {
        connection?.close()
        connection = nil
        keyboard = false
    }
}

private struct ScreenSurface: UIViewRepresentable {
    let session: ScreenSession
    func makeUIView(context: Context) -> ScreenSurfaceView { session.surface }
    func updateUIView(_ view: ScreenSurfaceView, context: Context) {}
}

/// Computer Control's page: the machine's screen, full screen, and one small
/// bar with the connection's quality, the mode, the keyboard and Close, which
/// folds into a round button and moves to any edge (docs/computer-control.md §6.3).
struct ScreenControlView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @Environment(\.scenePhase) private var phase
    @State var session: ScreenSession
    /// The app went to the background with the session open: it opens again in front.
    @State private var away = false
    /// The bar folded into a round button, as the owner left it last time.
    @AppStorage("screen-bar-folded") private var folded = false
    /// The owner put the relay's hint away, for as long as the page shows.
    @State private var hintDismissed = false

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()
            ScreenSurface(session: session)
                .ignoresSafeArea(.container)
            if session.machineError == "locked" || session.ended == "the screen is locked" {
                // GNOME ends a session when it locks, and starts none (docs/computer-control-linux.md §3.7).
                notice(String(localized: "\(session.machineName) is locked"),
                       String(localized: "GNOME shares no locked screen. Unlock it at the computer, then connect again.")) {
                    Button("Reconnect") { session.reconnect() }.buttonStyle(.borderedProminent)
                }
            } else if let ended = session.ended {
                notice(String(localized: "The session ended"), reason(ended)) {
                    Button("Reconnect") { session.reconnect() }.buttonStyle(.borderedProminent)
                }
            } else if session.machineError == "screen_recording" {
                notice(String(localized: "\(session.machineName) can’t show its screen yet"),
                       String(localized: "On that Mac, open System Settings › Privacy & Security › Screen & System Audio Recording and turn on Hermesh. Then connect again.")) {
                    Button("Reconnect") { session.reconnect() }.buttonStyle(.borderedProminent)
                }
            } else if session.machineError == "consent" {
                // The desktop asks at the computer, the first time (docs/computer-control-linux.md §3.2).
                notice(String(localized: "\(session.machineName) is asking at its screen"),
                       String(localized: "Someone at that computer allows it there, once. After that it connects without asking.")) {
                    Button("Reconnect") { session.reconnect() }.buttonStyle(.borderedProminent)
                }
            } else if session.machineError == "no_display" {
                notice(String(localized: "\(session.machineName) has no desktop running"),
                       String(localized: "Log in to its desktop, then connect again.")) {
                    Button("Reconnect") { session.reconnect() }.buttonStyle(.borderedProminent)
                }
            } else if !session.machineError.isEmpty {
                notice(String(localized: "\(session.machineName) can’t show its screen"), session.machineDetail.isEmpty ? session.machineError : session.machineDetail) {
                    Button("Reconnect") { session.reconnect() }.buttonStyle(.borderedProminent)
                }
            } else if !session.live {
                VStack(spacing: 14) {
                    ProgressView().tint(.white)
                    Text("Connecting to \(session.machineName)…").foregroundStyle(.white.opacity(0.8))
                }
            }
        }
        .overlay(alignment: .top) {
            VStack(spacing: 8) {
                if session.live, session.ended == nil, !session.canControl {
                    Text(session.linux ? String(localized: "You can watch, not control: \(session.machineName) shares its screen without its keyboard and mouse.")
                         : String(localized: "You can watch, not control: on \(session.machineName), turn on Accessibility for hermeshd."))
                        .font(.footnote)
                        .foregroundStyle(.white)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal, 14).padding(.vertical, 8)
                        .background(.black.opacity(0.7), in: .capsule)
                        .accessibilityIdentifier("screen-watch-only")
                } else if let flashed = session.flashed {
                    Text(flashed.text)
                        .font(.footnote)
                        .foregroundStyle(.white.opacity(0.9))
                        .padding(.horizontal, 14).padding(.vertical, 8)
                        .background(.black.opacity(0.7), in: .capsule)
                        .transition(.opacity)
                        .accessibilityIdentifier(flashed.id)
                }
                if relayedNearby, !hintDismissed { relayHint }
            }
            .padding(.top, 8).padding(.horizontal, 16)
        }
        .overlay { bar }
        .statusBarHidden()
        .persistentSystemOverlays(.hidden)
        .defersSystemGestures(on: .all)
        .preferredColorScheme(.dark)
        .onAppear {
            ScreenOrientation.allowed = .allButUpsideDown
            if let api = model.api?.live { session.start(api: api) }
        }
        .onDisappear {
            session.stop()
            ScreenOrientation.allowed = .portrait
        }
        // In the background the core stops serving, and nobody is looking: the session ends there,
        // and opens again when the app is back in front.
        .onChange(of: phase) { _, phase in
            if phase == .background, session.ended == nil {
                away = true
                session.stop()
            } else if phase == .active, away {
                away = false
                session.reconnect()
            }
        }
    }

    /// Why the session ended, in the owner's words where the core's are known.
    private func reason(_ ended: String) -> String {
        switch ended {
        case "the machine ended the session": String(localized: "Another device took over, or the session was stopped on \(session.machineName).")
        case "the connection to the machine was lost": String(localized: "The connection to \(session.machineName) was lost.")
        default: ended
        }
    }

    private func notice(_ title: String, _ detail: String, @ViewBuilder actions: () -> some View) -> some View {
        VStack(spacing: 12) {
            Text(title).font(.headline).foregroundStyle(.white)
            if !detail.isEmpty {
                Text(detail).font(.subheadline).foregroundStyle(.white.opacity(0.7)).multilineTextAlignment(.center)
            }
            actions().padding(.top, 4)
        }
        .padding(24)
        .frame(maxWidth: 420)
    }

    /// The picture goes through the relay though the two look to be on one
    /// network: the iPhone asks before an app's first packet to the local
    /// network (and a Mac before hermeshd's), and until the owner allows it
    /// there is no direct path. The session moves to it by itself once there is.
    private var relayedNearby: Bool {
        session.live && session.ended == nil && session.link.phase == "connected" && session.link.path == "relay" && session.link.nearby == true
    }

    private var relayHint: some View {
        HStack(alignment: .top, spacing: 4) {
            VStack(alignment: .leading, spacing: 6) {
                Text(session.linux ? String(localized: "Relayed, though \(session.machineName) looks to be on this network. To connect directly, allow Local Network for Hermesh on this iPhone.")
                     : String(localized: "Relayed, though \(session.machineName) looks to be on this network. To connect directly, allow Local Network for Hermesh on this iPhone, and for hermeshd on \(session.machineName)."))
                    .font(.footnote)
                    .foregroundStyle(.white)
                    .fixedSize(horizontal: false, vertical: true)
                Button("Open Settings") {
                    if let url = URL(string: UIApplication.openSettingsURLString) { UIApplication.shared.open(url) }
                }
                .font(.footnote.weight(.semibold))
                .accessibilityIdentifier("screen-relay-settings")
            }
            Spacer(minLength: 0)
            Button { withAnimation(.snappy) { hintDismissed = true } } label: {
                Image(systemName: "xmark").font(.caption.weight(.semibold)).foregroundStyle(.white.opacity(0.7)).frame(width: 30, height: 30)
            }
            .accessibilityLabel(Text("Dismiss"))
            .accessibilityIdentifier("screen-relay-dismiss")
        }
        .padding(.leading, 14).padding(.trailing, 4).padding(.vertical, 10)
        .background(.black.opacity(0.7), in: .rect(cornerRadius: 16))
        .frame(maxWidth: 420)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("screen-relay-hint")
    }

    /// The connection's quality in a word the owner can act on: a number while
    /// direct, and that it goes through the relay when it does.
    private var quality: String {
        guard session.link.phase == "connected" else {
            return session.link.phase == "reconnecting" ? String(localized: "Reconnecting…") : String(localized: "Connecting…")
        }
        let ms = max(1, session.link.rtt_ms ?? 1)
        return session.link.path == "relay" ? String(localized: "Relayed · \(ms) ms") : String(localized: "\(ms) ms")
    }

    /// The connection's colour: green while direct and quick, orange through
    /// the relay or slow, grey until the picture comes.
    private var health: Color {
        guard session.link.phase == "connected", session.live else { return .gray }
        return session.link.path == "relay" || (session.link.rtt_ms ?? 0) > 150 ? .orange : .green
    }

    /// The bar, or the round button it folds into; dragged to any edge (ScreenBarHost).
    private var bar: some View {
        ScreenBarHost(folded: $folded) { vertical in
            let layout = vertical ? AnyLayout(VStackLayout(spacing: 2)) : AnyLayout(HStackLayout(spacing: 2))
            layout {
                Button { withAnimation(.snappy) { folded = true } } label: {
                    Image(systemName: "arrow.down.right.and.arrow.up.left").frame(width: 38, height: 38)
                }
                .accessibilityLabel(Text("Hide Controls"))
                .accessibilityIdentifier("screen-fold")
                // Down a side the dot alone: its colour says how the connection is.
                HStack(spacing: 6) {
                    Circle().fill(health).frame(width: 7, height: 7)
                    if !vertical { Text(quality).font(.caption).monospacedDigit().foregroundStyle(.white.opacity(0.8)) }
                }
                .padding(vertical ? .vertical : .horizontal, 6)
                .accessibilityElement(children: .combine)
                .accessibilityLabel(Text(quality))
                .accessibilityIdentifier("screen-quality")
                Button { session.mode = session.mode == .direct ? .trackpad : .direct } label: {
                    Image(systemName: session.mode == .trackpad ? "cursorarrow.motionlines" : "hand.tap")
                        .frame(width: 38, height: 38)
                }
                .accessibilityLabel(session.mode == .trackpad ? Text("Trackpad Mode") : Text("Direct Touch"))
                .accessibilityIdentifier("screen-mode")
                Button { session.keyboard.toggle() } label: {
                    Image(systemName: session.keyboard ? "keyboard.fill" : "keyboard").frame(width: 38, height: 38)
                }
                .accessibilityLabel(Text("Keyboard"))
                .accessibilityIdentifier("screen-keyboard")
                Button { dismiss() } label: {
                    Image(systemName: "xmark").frame(width: 38, height: 38)
                }
                .accessibilityLabel(Text("Close"))
                .accessibilityIdentifier("screen-close")
            }
            .font(.system(size: 17))
            .foregroundStyle(.white)
            .padding(3)
            .glassEffect(.regular.interactive(), in: .capsule)
        } ball: {
            Button { withAnimation(.snappy) { folded = false } } label: {
                Circle().fill(health).frame(width: 10, height: 10)
                    .frame(width: 44, height: 44)
            }
            .glassEffect(.regular.interactive(), in: .circle)
            .accessibilityLabel(Text("Show Controls"))
            .accessibilityValue(Text(quality))
            .accessibilityIdentifier("screen-unfold")
        }
    }
}
