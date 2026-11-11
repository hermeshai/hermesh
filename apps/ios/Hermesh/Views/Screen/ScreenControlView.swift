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

    /// The connection, as the core last reported it.
    var link = ScreenLink()
    /// The first frame is on screen.
    var live = false
    /// Why the session ended; nil while it lasts.
    var ended: String?
    /// Why the machine has no picture ("screen_recording", …), or empty.
    var machineError = ""
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

    @ObservationIgnored let surface = ScreenSurfaceView()
    @ObservationIgnored private var connection: ScreenConnection?
    @ObservationIgnored private var endpoint: (port: Int, token: String)?

    init(machineID: String, machineName: String) {
        self.machineID = machineID
        self.machineName = machineName
        mode = ScreenMode(rawValue: UserDefaults.standard.string(forKey: "screen-mode") ?? "") ?? .direct
        surface.mode = mode
        surface.send = { [weak self] in self?.connection?.send($0) }
        surface.keys.send = { [weak self] in self?.connection?.send($0) }
        surface.keys.onDismiss = { [weak self] in self?.keyboard = false }
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
                    self.canControl = s.canControl
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
/// bar with the connection's quality, the mode, the keyboard and Close
/// (docs/computer-control.md §6.3).
struct ScreenControlView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @Environment(\.scenePhase) private var phase
    @State var session: ScreenSession
    /// The app went to the background with the session open: it opens again in front.
    @State private var away = false

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()
            ScreenSurface(session: session)
                .ignoresSafeArea(.container)
            if let ended = session.ended {
                notice(String(localized: "The session ended"), reason(ended)) {
                    Button("Reconnect") { session.reconnect() }.buttonStyle(.borderedProminent)
                }
            } else if session.machineError == "screen_recording" {
                notice(String(localized: "\(session.machineName) can’t show its screen yet"),
                       String(localized: "On that Mac, open System Settings › Privacy & Security › Screen & System Audio Recording and turn on Hermesh. Then connect again.")) {
                    Button("Reconnect") { session.reconnect() }.buttonStyle(.borderedProminent)
                }
            } else if !session.machineError.isEmpty {
                notice(String(localized: "\(session.machineName) can’t show its screen"), session.machineError) {
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
            if session.live, session.ended == nil, !session.canControl {
                Text("You can watch, not control: on \(session.machineName), turn on Accessibility for hermeshd.")
                    .font(.footnote)
                    .foregroundStyle(.white)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, 14).padding(.vertical, 8)
                    .background(.black.opacity(0.7), in: .capsule)
                    .padding(.top, 8).padding(.horizontal, 16)
                    .accessibilityIdentifier("screen-watch-only")
            }
        }
        .overlay(alignment: .bottomTrailing) { bar.padding(12) }
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
        case "the machine ended the session": String(localized: "Another device took over, or the session was stopped on that Mac.")
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

    /// The connection's quality in a word the owner can act on: a number while
    /// direct, and that it goes through the relay when it does.
    private var quality: String {
        guard session.link.phase == "connected" else {
            return session.link.phase == "reconnecting" ? String(localized: "Reconnecting…") : String(localized: "Connecting…")
        }
        let ms = max(1, session.link.rtt_ms ?? 1)
        return session.link.path == "relay" ? String(localized: "Relayed · \(ms) ms") : String(localized: "\(ms) ms")
    }

    private var bar: some View {
        HStack(spacing: 2) {
            HStack(spacing: 6) {
                Circle()
                    .fill(session.link.phase == "connected" && session.live ? (session.link.path == "relay" || (session.link.rtt_ms ?? 0) > 150 ? Color.orange : .green) : .gray)
                    .frame(width: 7, height: 7)
                Text(quality).font(.caption).monospacedDigit().foregroundStyle(.white.opacity(0.8))
            }
            .padding(.leading, 12).padding(.trailing, 8)
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("screen-quality")
            Button { session.mode = session.mode == .direct ? .trackpad : .direct } label: {
                Image(systemName: session.mode == .trackpad ? "cursorarrow.motionlines" : "hand.tap")
                    .frame(width: 40, height: 40)
            }
            .accessibilityLabel(session.mode == .trackpad ? Text("Trackpad Mode") : Text("Direct Touch"))
            .accessibilityIdentifier("screen-mode")
            Button { session.keyboard.toggle() } label: {
                Image(systemName: session.keyboard ? "keyboard.fill" : "keyboard").frame(width: 40, height: 40)
            }
            .accessibilityLabel(Text("Keyboard"))
            .accessibilityIdentifier("screen-keyboard")
            Button { dismiss() } label: {
                Image(systemName: "xmark").frame(width: 40, height: 40)
            }
            .accessibilityLabel(Text("Close"))
            .accessibilityIdentifier("screen-close")
        }
        .font(.system(size: 17))
        .foregroundStyle(.white)
        .padding(4)
        .glassEffect(.regular.interactive(), in: .capsule)
    }
}
