import HermeshProto
import HermeshSessions
import SwiftUI

// Small pieces the screens share.

struct PrivacyPolicyLink: View {
    @Environment(\.locale) private var locale

    var body: some View {
        let language = locale.language.languageCode?.identifier == "zh" ? "zh" : "en"
        Link("Privacy Policy", destination: officialCenter.appending(path: "\(language)/privacy"))
    }
}

/// A coding agent's or personal agent's logo, or a tile with its initial.
struct AgentLogo: View {
    let id: String
    var size: CGFloat = 28
    /// Its initial even when id names an app (a grant whose owner chose its initial).
    var initialOnly = false
    /// At work in the sidebar or rail: a brand-blue light orbits its rounded square.
    var running = false
    /// While no one sees it (the menu closed): the orbit does not draw.
    var paused = false
    @Environment(\.accessibilityReduceMotion) private var still
    @Environment(\.colorScheme) private var colorScheme

    /// The logo asset for a coding agent id or a personal agent's name.
    static func asset(_ id: String) -> String? {
        let known = ["claude", "codex", "gemini", "antigravity", "cursor", "copilot", "opencode", "qwen", "goose", "auggie", "kimi", "kiro", "qoder", "deepseek", "codebuddy", "grok", "grokbot", "muse", "openclaw"]
        let byName = ["Claude": "claude", "Claude Code": "claude", "ChatGPT": "codex", "Codex": "codex", "Grok Bot": "grokbot", "Grok": "grok",
                      "Muse": "muse", "OpenClaw": "openclaw", "Cursor": "cursor", "Gemini": "gemini"]
        if known.contains(id) { return id }
        // A hosted connection is "Name (host)": its app's logo only from the app's own domain, as any app can call itself Muse.
        if id.hasSuffix(")"), let open = id.range(of: " (", options: .backwards) {
            let name = String(id[..<open.lowerBound])
            let host = String(id[open.upperBound..<id.index(before: id.endIndex)])
            guard domains[name, default: []].contains(where: { host == $0 || host.hasSuffix("." + $0) }) else { return nil }
            return byName[name]
        }
        return byName[id]
    }

    /// Where each app's hosted MCP connector signs in; matches personal.ts in packages/ui
    /// (Grok Bot signs in as Cursor).
    static let domains = ["Muse": ["meta.ai"], "ChatGPT": ["chatgpt.com"], "Claude": ["claude.ai"], "Grok Bot": ["cursor.com", "anysphere.cursor-mcp"],
                          "Cursor": ["cursor.com"]]

    /// The name a grant is shown by: a hosted app's own name ("Claude"), without where it
    /// signs in, once that is its app's domain; else its label whole.
    static func name(_ label: String) -> String {
        guard asset(label) != nil, label.hasSuffix(")"), let open = label.range(of: " (", options: .backwards) else { return label }
        return String(label[..<open.lowerBound])
    }

    var body: some View {
        let r = size * 0.26
        let isHer = !initialOnly && id == herAgent
        let tile = Group {
            if isHer {
                HerSmallCat(size: size)
            } else if !initialOnly, let a = Self.asset(id) {
                Image("Logos/\(a)").resizable().interpolation(.high)
                    .frame(width: size, height: size)
                    .clipShape(RoundedRectangle(cornerRadius: r, style: .continuous))
            } else {
                Initial(text: id, color: Self.color(id))
                    .frame(width: size, height: size)
                    .clipShape(RoundedRectangle(cornerRadius: r, style: .continuous))
            }
        }
        .frame(width: size, height: size)

        if running {
            let lw: CGFloat = 1.35
            let outset = lw - 0.1
            tile
                .overlay {
                    RoundedRectangle(cornerRadius: r, style: .continuous)
                        .strokeBorder(Color.primary.opacity(colorScheme == .dark ? 0.10 : 0.075), lineWidth: 0.5)
                }
                .background {
                    orbit(cornerRadius: r + outset, lineWidth: lw).padding(-outset)
                }
                .accessibilityHidden(true)
        } else {
            tile.accessibilityHidden(true)
        }
    }

    @ViewBuilder private func orbit(cornerRadius: CGFloat, lineWidth: CGFloat) -> some View {
        let ring = RoundedRectangle(cornerRadius: cornerRadius, style: .continuous)
        if still {
            ring.strokeBorder(Color.accentColor.opacity(0.8), lineWidth: lineWidth)
        } else {
            OrbitRingView(cornerRadius: cornerRadius, lineWidth: lineWidth, isDark: colorScheme == .dark, paused: paused)
        }
    }

    /// A color for a name without a logo, the same every time.
    static func color(_ s: String) -> Color {
        var h = 0
        for u in s.unicodeScalars { h = (h * 31 + Int(u.value)) & 0xffff }
        return Color(hue: Double(h % 360) / 360, saturation: 0.55, brightness: 0.78)
    }

    private struct Initial: View {
        let text: String
        let color: Color
        var body: some View {
            GeometryReader { g in
                ZStack {
                    color
                    Text(String(text.prefix(1)).uppercased()).font(.system(size: g.size.width * 0.5, weight: .semibold)).foregroundStyle(.white)
                }
            }
        }
    }
}

/// A session's state as a dot: orange while it waits on the owner, red when
/// it failed. One running has none, its title shimmering instead (as in the
/// menu); an idle or archived one has none either (as on the Mac), each
/// keeping the dot's room so titles line up.
struct StatusDot: View {
    let session: Session

    var body: some View {
        ZStack {
            // One running has no dot: its title shimmers (ShimmerText).
            if session.needsAttention {
                Circle().fill(color).frame(width: 9, height: 9)
            }
        }
        .frame(width: 20)
        .accessibilityLabel(statusTitle(session.status))
    }

    private var color: Color {
        if session.status == .waitingInput { return .orange }
        return .red
    }
}

/// A running session's title, in the menu and in the lists, shimmering (`shimmer`).
struct ShimmerText: View {
    let title: String
    /// While no one sees it (the menu closed): it does not draw.
    let paused: Bool

    init(_ title: String, paused: Bool) {
        self.title = title
        self.paused = paused
    }

    var body: some View {
        Text(title).foregroundStyle(Color(.label)).shimmer(paused: paused)
    }
}

extension View {
    /// Something at work, as the Mac's sidebar has a running title (app.css .stitle.run): a fainter band crosses it
    /// from left to right in 3 s, and after a breath (0.2 s) crosses it again; it is the one mark of work in
    /// progress, on a title, a step's words or an icon. Off (`on` false) it is the view as it is; while no one sees it
    /// (`paused`) it does not draw; with Reduce Motion it stays still.
    func shimmer(_ on: Bool = true, paused: Bool = false) -> some View {
        modifier(Shimmer(on: on, paused: paused))
    }
}

private struct OrbitRingView: UIViewRepresentable {
    let cornerRadius: CGFloat
    let lineWidth: CGFloat
    let isDark: Bool
    let paused: Bool

    func makeUIView(context: Context) -> OrbitRingUIView {
        OrbitRingUIView()
    }

    func updateUIView(_ view: OrbitRingUIView, context: Context) {
        view.configure(cornerRadius: cornerRadius, lineWidth: lineWidth, isDark: isDark, paused: paused)
    }

    final class OrbitRingUIView: UIView {
        private let trackLayer = CAShapeLayer()
        private let maskLayer = CAShapeLayer()
        private let conicHolder = CALayer()
        private let conicLayer = CAGradientLayer()
        private var cornerRadius: CGFloat = 0
        private var lineWidth: CGFloat = 0
        private var isDark = false
        private var paused = false

        override init(frame: CGRect) {
            super.init(frame: frame)
            isUserInteractionEnabled = false
            backgroundColor = .clear
            trackLayer.fillColor = UIColor.clear.cgColor
            maskLayer.fillColor = UIColor.clear.cgColor
            conicLayer.type = .conic
            conicLayer.startPoint = CGPoint(x: 0.5, y: 0.5)
            conicLayer.endPoint = CGPoint(x: 1.0, y: 0.5)
            conicHolder.mask = maskLayer
            conicHolder.addSublayer(conicLayer)
            layer.addSublayer(trackLayer)
            layer.addSublayer(conicHolder)
        }

        required init?(coder: NSCoder) { fatalError() }

        override func didMoveToWindow() {
            super.didMoveToWindow()
            syncAnimation()
        }

        override func layoutSubviews() {
            super.layoutSubviews()
            let s = bounds.size
            guard s.width > lineWidth, s.height > lineWidth else { return }
            let centerRect = bounds.insetBy(dx: lineWidth / 2, dy: lineWidth / 2)
            let rCenter = max(1, cornerRadius - lineWidth / 2)
            let path = UIBezierPath(roundedRect: centerRect, cornerRadius: rCenter).cgPath
            CATransaction.begin()
            CATransaction.setDisableActions(true)
            trackLayer.frame = bounds
            trackLayer.path = path
            trackLayer.lineWidth = lineWidth
            conicHolder.frame = bounds
            maskLayer.frame = bounds
            maskLayer.path = path
            maskLayer.lineWidth = lineWidth
            let diag = hypot(s.width, s.height)
            conicLayer.bounds = CGRect(x: 0, y: 0, width: diag, height: diag)
            conicLayer.position = CGPoint(x: s.width / 2, y: s.height / 2)
            CATransaction.commit()
            syncAnimation()
        }

        func configure(cornerRadius: CGFloat, lineWidth: CGFloat, isDark: Bool, paused: Bool) {
            let styleChanged = self.cornerRadius != cornerRadius || self.lineWidth != lineWidth || self.isDark != isDark
            self.cornerRadius = cornerRadius
            self.lineWidth = lineWidth
            self.isDark = isDark
            self.paused = paused
            if styleChanged {
                let trait = UITraitCollection(userInterfaceStyle: isDark ? .dark : .light)
                let accent = UIColor.tintColor.resolvedColor(with: trait)
                let crest = isDark
                    ? UIColor(red: 0.72, green: 0.78, blue: 1.0, alpha: 1)
                    : UIColor(red: 0.01, green: 0.52, blue: 0.78, alpha: 1)
                CATransaction.begin()
                CATransaction.setDisableActions(true)
                trackLayer.strokeColor = UIColor.label.resolvedColor(with: trait).withAlphaComponent(isDark ? 0.07 : 0.05).cgColor
                conicLayer.colors = [
                    accent.withAlphaComponent(0).cgColor,
                    accent.withAlphaComponent(0.18).cgColor,
                    accent.withAlphaComponent(0.65).cgColor,
                    crest.cgColor,
                    accent.cgColor,
                    accent.withAlphaComponent(0).cgColor,
                    accent.withAlphaComponent(0).cgColor,
                ]
                conicLayer.locations = [0, 0.12, 0.27, 0.34, 0.36, 0.38, 1.0]
                CATransaction.commit()
                setNeedsLayout()
            }
            syncAnimation()
        }

        private func syncAnimation() {
            let shouldRun = !paused && window != nil && bounds.width > 0
            if shouldRun {
                guard conicLayer.animation(forKey: "orbit") == nil else { return }
                let rot = CABasicAnimation(keyPath: "transform.rotation.z")
                rot.fromValue = 0
                rot.toValue = Double.pi * 2
                rot.duration = 1.65
                rot.repeatCount = .infinity
                rot.isRemovedOnCompletion = false
                conicLayer.add(rot, forKey: "orbit")
            } else if conicLayer.animation(forKey: "orbit") != nil {
                conicLayer.removeAnimation(forKey: "orbit")
            }
        }
    }
}

private struct Shimmer: ViewModifier {
    let on: Bool
    let paused: Bool
    @Environment(\.accessibilityReduceMotion) private var still

    func body(content: Content) -> some View {
        if on && !still && !paused {
            content.mask {
                ShimmerMaskView()
            }
        } else {
            content
        }
    }
}

/// CoreAnimation RenderServer-driven shimmer mask (`0%` main-thread CPU while animating).
private struct ShimmerMaskView: UIViewRepresentable {
    func makeUIView(context: Context) -> ShimmerMaskUIView {
        ShimmerMaskUIView()
    }

    func updateUIView(_ uiView: ShimmerMaskUIView, context: Context) {}

    final class ShimmerMaskUIView: UIView {
        override class var layerClass: AnyClass { CAGradientLayer.self }
        private var grad: CAGradientLayer { layer as! CAGradientLayer }

        override init(frame: CGRect) {
            super.init(frame: frame)
            isUserInteractionEnabled = false
            backgroundColor = .clear
            grad.colors = [
                UIColor.black.cgColor,
                UIColor.black.withAlphaComponent(0.3).cgColor,
                UIColor.black.cgColor,
            ]
            grad.locations = [0, 0.5, 1]
            grad.startPoint = CGPoint(x: -0.86, y: 0.5)
            grad.endPoint = CGPoint(x: -0.14, y: 0.5)
        }

        required init?(coder: NSCoder) { fatalError() }

        override func didMoveToWindow() {
            super.didMoveToWindow()
            syncAnimation()
        }

        override func layoutSubviews() {
            super.layoutSubviews()
            syncAnimation()
        }

        private func syncAnimation() {
            if window != nil && bounds.width > 0 {
                guard grad.animation(forKey: "shimmer") == nil else { return }
                let s = CABasicAnimation(keyPath: "startPoint.x")
                s.fromValue = -0.86
                s.toValue = 1.14
                s.duration = 3.0
                s.timingFunction = CAMediaTimingFunction(name: .easeInEaseOut)

                let e = CABasicAnimation(keyPath: "endPoint.x")
                e.fromValue = -0.14
                e.toValue = 1.86
                e.duration = 3.0
                e.timingFunction = CAMediaTimingFunction(name: .easeInEaseOut)

                let group = CAAnimationGroup()
                group.animations = [s, e]
                group.duration = 3.2
                group.repeatCount = .infinity
                group.isRemovedOnCompletion = false
                grad.add(group, forKey: "shimmer")
            } else {
                grad.removeAnimation(forKey: "shimmer")
            }
        }
    }
}

/// The Hermesh mark: three translucent beams at 0°, 60° and 120°, each 10
/// units off the centre so they never meet in one point.
struct Mark: View {
    var size: CGFloat = 64
    @Environment(\.colorScheme) private var scheme

    var body: some View {
        Canvas { ctx, s in
            let scale = s.width / 104
            ctx.translateBy(x: s.width / 2, y: s.height / 2)
            ctx.scaleBy(x: scale, y: scale)
            let colors: [Color] = scheme == .dark
                ? [Color(red: 0.13, green: 0.88, blue: 0.79), Color(red: 0.24, green: 0.48, blue: 1), Color(red: 0.48, green: 0.38, blue: 1)]
                : [Color(red: 0.03, green: 0.57, blue: 0.70), Color(red: 0.18, green: 0.42, blue: 0.94), Color(red: 0.43, green: 0.24, blue: 0.94)]
            ctx.blendMode = scheme == .dark ? .plusLighter : .multiply
            for i in 0 ..< 3 {
                let a = Double(i) * .pi / 3
                let d = CGPoint(x: cos(a), y: sin(a)), n = CGPoint(x: -d.y, y: d.x)
                let m = CGPoint(x: n.x * 10, y: n.y * 10)
                let p0 = CGPoint(x: m.x - d.x * 37, y: m.y - d.y * 37), p1 = CGPoint(x: m.x + d.x * 37, y: m.y + d.y * 37)
                var path = Path()
                path.move(to: p0)
                path.addLine(to: p1)
                ctx.opacity = scheme == .dark ? 0.66 : 0.78
                ctx.stroke(path, with: .linearGradient(Gradient(colors: colors), startPoint: p0, endPoint: p1), style: StrokeStyle(lineWidth: 12, lineCap: .round))
            }
        }
        .frame(width: size, height: size)
        .accessibilityLabel("Hermesh")
    }
}

/// Who this iPhone belongs to, in a circle: on the official service the
/// account's picture (from GitHub or Google), else its initial; on a server
/// of one's own, a server.
struct AccountPicture: View {
    let status: Hermesh_Local_V1_GetStatusResponse

    var body: some View {
        if status.hosted, let url = URL(string: status.accountAvatarURL), url.scheme == "https" {
            AsyncImage(url: url) { phase in
                if let image = phase.image {
                    image.resizable().scaledToFill()
                } else {
                    initial
                }
            }
            .clipShape(.circle)
        } else if status.hosted {
            initial
        } else {
            badge(Image(systemName: "server.rack").font(.body.weight(.semibold)))
        }
    }

    private var initial: some View {
        badge(status.account.first.map { Text(String($0).uppercased()).font(.title3.weight(.semibold)) }
            ?? Text(Image(systemName: "person.fill")).font(.body.weight(.medium)))
    }

    private func badge(_ content: some View) -> some View {
        content.foregroundStyle(.white).frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(LinearGradient(colors: [Color(red: 0.03, green: 0.57, blue: 0.70), Color(red: 0.18, green: 0.42, blue: 0.94), Color(red: 0.43, green: 0.24, blue: 0.94)], startPoint: .topLeading, endPoint: .bottomTrailing), in: .circle)
    }
}

/// The mark and the name, as the brand's horizontal logo lays them out:
/// the mark 1.5 times the letters' cap height, 0.42 of it apart, the
/// letters' top a little below the mark's. height is the mark's.
struct Wordmark: View {
    var height: CGFloat = 22

    var body: some View {
        // In the logo's units, the mark 100 high (its beams fill 86 of
        // Mark's 104).
        let u = height / 100
        HStack(alignment: .top, spacing: 42 * u) {
            Mark(size: height * 104 / 86).frame(width: height, height: height)
            Image("Wordmark").renderingMode(.template).resizable().scaledToFit()
                .frame(height: 70.2 * u).padding(.top, 19.3 * u)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Hermesh")
        .accessibilityAddTraits(.isHeader)
    }
}

/// A verification code in three groups of four, large and monospaced, for
/// comparing with what the other side shows.
struct CodeTiles: View {
    let code: String
    var caption: LocalizedStringKey?

    var body: some View {
        VStack(spacing: 8) {
            HStack(spacing: 8) {
                ForEach(Array(code.split(separator: "-").enumerated()), id: \.offset) { _, g in
                    Text(g).font(.system(size: 24, weight: .semibold, design: .monospaced)).tracking(2)
                        .padding(.vertical, 10).frame(minWidth: 92)
                        .background(.fill.tertiary, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                }
            }
            if let caption { Text(caption).font(.subheadline).foregroundStyle(.secondary) }
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("verification-code")
    }
}

/// The digits to type on what was just approved: each one its own tile,
/// centred, read at a glance from the other screen.
struct DigitTiles: View {
    let digits: String

    var body: some View {
        HStack(spacing: 12) {
            ForEach(Array(digits.enumerated()), id: \.offset) { _, d in
                Text(String(d)).font(.system(size: 56, weight: .semibold, design: .rounded)).monospacedDigit()
                    .frame(width: 80, height: 96)
                    .background(.fill.tertiary, in: RoundedRectangle(cornerRadius: 22, style: .continuous))
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("match-digits")
    }
}

extension View {
    /// A sheet just as tall as what it shows, which iOS floats as a card
    /// (as when AirPods come near).
    func fittedSheet() -> some View { modifier(FittedSheet()) }
}

private struct FittedSheet: ViewModifier {
    @State private var height: CGFloat = 480

    func body(content: Content) -> some View {
        content
            .fixedSize(horizontal: false, vertical: true)
            .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { height = $0 }
            .frame(maxHeight: .infinity, alignment: .top)
            .presentationDetents([.height(height)])
    }
}

/// A card's close button, at its top right as the system's own cards have it.
struct CardCloseButton: View {
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: "xmark").font(.system(size: 15, weight: .semibold)).frame(width: 32, height: 32)
        }
        .buttonStyle(.glass).buttonBorderShape(.circle)
        .accessibilityLabel(Text("Close"))
        .accessibilityIdentifier("close")
    }
}

/// A card's one main action, the width of the card.
struct CardButton: View {
    let title: LocalizedStringKey
    var systemImage: String?
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 8) {
                if let systemImage { Image(systemName: systemImage) }
                Text(title)
            }
            .frame(maxWidth: .infinity)
        }
        .buttonStyle(.glassProminent).controlSize(.large) // the large size, as every main action's: no height of its own
    }
}

/// The toast at the bottom, with Undo when there is one.
struct ToastView: View {
    let toast: Toast
    var dismiss: () -> Void

    var body: some View {
        HStack(spacing: 12) {
            Text(toast.text).font(.subheadline).lineLimit(2)
            if let undo = toast.undo {
                Button("Undo") {
                    undo()
                    dismiss()
                }
                .font(.subheadline.weight(.semibold))
            }
        }
        .padding(.horizontal, 18).padding(.vertical, 12)
        .glassEffect(.regular, in: .capsule)
        .padding(.horizontal, 24)
        .accessibilityIdentifier("toast")
    }
}

/// A grant's avatar: the icon its owner picked (a picture, an app's logo or
/// its initial), else Her's cat for her own grant, else its app's logo, else
/// a tile with its initial.
struct GrantAvatar: View {
    let client: Client
    var size: CGFloat = 28
    var body: some View {
        if client.icon == "image", let picture = UIImage(data: client.image) {
            Image(uiImage: picture).resizable().interpolation(.high)
                .frame(width: size, height: size)
                .clipShape(RoundedRectangle(cornerRadius: size * 0.26, style: .continuous))
                .accessibilityHidden(true)
        } else if client.icon.hasPrefix("app:") {
            AgentLogo(id: String(client.icon.dropFirst(4)), size: size)
        } else if !client.herMachineID.isEmpty && client.icon.isEmpty {
            HerAvatar(size: size)
        } else {
            AgentLogo(id: client.label, size: size, initialOnly: !client.icon.isEmpty)
        }
    }
}

extension Hermesh_Local_V1_Client {
    /// Days until the grant ends (0 on its last day).
    func daysLeft(now: Date) -> Int { max(0, Int(expiresAt.date.timeIntervalSince(now) / 86400)) }
}

/// The session page's colours: the Mac app's tokens (app.css), light and dark.
enum Palette {
    static let fill = Color.primary.opacity(0.045)
    static let fill2 = Color.primary.opacity(0.075)
    static let fill3 = Color.primary.opacity(0.12)
    static let separator = Color.primary.opacity(0.08)
    /// Code blocks and tool output.
    static let code = Color(UIColor { $0.userInterfaceStyle == .dark ? UIColor(red: 0x18 / 255, green: 0x18 / 255, blue: 0x1b / 255, alpha: 1)
        : UIColor(red: 0xf6 / 255, green: 0xf7 / 255, blue: 0xfa / 255, alpha: 1) })
    /// The owner's messages, on the right: the brand's blue (the app's accent), set apart from what answers them.
    static let bubble = Color.accentColor
    static let bubbleText = Color.white
    /// Her's messages, on the left: a shade off the page (the Mac's --bot-reply), with a hairline.
    static let reply = Color(UIColor { $0.userInterfaceStyle == .dark ? UIColor(white: 1, alpha: 0.06)
        : UIColor(red: 0xf4 / 255, green: 0xf4 / 255, blue: 0xf7 / 255, alpha: 1) })
    /// The lines a reply pointed at in a file (the Mac's --target).
    static let target = Color(UIColor { $0.userInterfaceStyle == .dark ? UIColor(red: 1, green: 196 / 255, blue: 0, alpha: 0.16)
        : UIColor(red: 1, green: 196 / 255, blue: 0, alpha: 0.22) })
    /// Elevated crisp surface for settled decision answers (the Mac's --tq-ans).
    static let settledAnswer = Color(UIColor { $0.userInterfaceStyle == .dark ? UIColor(white: 1, alpha: 0.055)
        : UIColor(white: 1, alpha: 0.92) })
    static let settledAnswerBorder = Color(UIColor { $0.userInterfaceStyle == .dark ? UIColor(white: 1, alpha: 0.09)
        : UIColor(white: 0, alpha: 0.085) })
    static let settledAnswerShadow = Color(UIColor { $0.userInterfaceStyle == .dark ? .clear
        : UIColor(white: 0, alpha: 0.03) })
}

/// Putting the keyboard away, as Messages does (docs/thread-composer.md §16): there is no button for it on iPhone.
extension View {
    /// A tap on the page (its blank parts or what's on it, outside any text input) puts it away; what was tapped still does what it does.
    func keyboardAwayOnTap() -> some View {
        gesture(DismissKeyboardTapGesture())
    }

    /// A quick flick down on the box puts it away. Only a flick: a slow drag (selecting text) leaves it.
    func keyboardAwayOnFlickDown() -> some View {
        simultaneousGesture(DragGesture(minimumDistance: 12).onEnded { v in
            let down = v.translation.height, flick = v.predictedEndTranslation.height - down
            if down > 16, flick > 30, abs(v.translation.width) < down { hideKeyboard() }
        })
    }
}

private struct DismissKeyboardTapGesture: UIGestureRecognizerRepresentable {
    func makeUIGestureRecognizer(context: Context) -> UITapGestureRecognizer {
        let g = UITapGestureRecognizer()
        g.cancelsTouchesInView = false
        g.delaysTouchesEnded = false
        g.delegate = context.coordinator
        return g
    }

    func updateUIGestureRecognizer(_ recognizer: UITapGestureRecognizer, context: Context) {}

    func handleUIGestureRecognizerAction(_ recognizer: UITapGestureRecognizer, context: Context) {
        if recognizer.state == .ended {
            hideKeyboard()
        }
    }

    func makeCoordinator(converter: CoordinateSpaceConverter) -> Coordinator { Coordinator() }

    final class Coordinator: NSObject, UIGestureRecognizerDelegate {
        func gestureRecognizer(_ g: UIGestureRecognizer, shouldReceive touch: UITouch) -> Bool {
            var v: UIView? = touch.view
            while let cur = v {
                if cur is UITextField || cur is UITextView { return false }
                v = cur.superview
            }
            return true
        }

        func gestureRecognizer(_ g: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer) -> Bool {
            true
        }
    }
}

func hideKeyboard() {
    guard let first = UIResponder.herFirst, first is UIKeyInput else { return }
    _ = first.resignFirstResponder()
}

extension UIResponder {
    private static weak var found: UIResponder?

    /// The view the keyboard types into, if any.
    static var herFirst: UIResponder? {
        found = nil
        UIApplication.shared.sendAction(#selector(herFind), to: nil, from: nil, for: nil)
        return found
    }

    @objc private func herFind() { UIResponder.found = self }
}

/// Inline "Recommended" badge rendered directly inside a SwiftUI Text run so multi-line option text wraps cleanly
/// back to the leading edge and scales proportionally with Dynamic Type.
struct RecommendedBadgeAttribute: TextAttribute {
    let selected: Bool
}

struct RecommendedTextRenderer: TextRenderer {
    let accent: Color

    func draw(layout: Text.Layout, in ctx: inout GraphicsContext) {
        for line in layout {
            for run in line {
                if let badge = run[RecommendedBadgeAttribute.self] {
                    let rect = run.typographicBounds.rect.insetBy(dx: -0.5, dy: -1.5)
                    let path = Path(roundedRect: rect, cornerRadius: 4.5, style: .continuous)
                    ctx.fill(path, with: .color(badge.selected ? accent : accent.opacity(0.15)))
                }
                ctx.draw(run)
            }
        }
    }
}

struct RecommendedOptionText: View {
    let parsed: ParsedOptionLabel
    var selected = false
    var font: Font = .subheadline.weight(.medium)
    var badgeFont: Font = .caption2.weight(.semibold)

    var body: some View {
        let bodyText = Text(verbatim: parsed.text).font(font)
        if parsed.recommended {
            let badge = Text(verbatim: "\u{00A0}\(String(localized: "Recommended"))\u{00A0}")
                .font(badgeFont)
                .foregroundStyle(selected ? Color.white : Color.accentColor)
                .customAttribute(RecommendedBadgeAttribute(selected: selected))
            let space = Text(verbatim: " ").font(font)
            Text("\(badge)\(space)\(bodyText)")
                .textRenderer(RecommendedTextRenderer(accent: .accentColor))
        } else {
            bodyText
        }
    }
}

struct TodoEndSummary: Equatable {
    var stepNumber: Int
    var total: Int
    var text: String
    var failed: Bool
}

/// Back to a conversation's end, once it was scrolled well up from it (the Mac's): an arrow in a glass circle, a
/// "Back to question" / "Back to plan" glass capsule when an inline prompt waits at the bottom, or a live step
/// progress capsule when a checklist runs at the bottom.
struct ToEndButton: View {
    var waitingAgent: String? = nil
    var waitingPlan: Bool = false
    var todoSummary: TodoEndSummary? = nil
    let action: () -> Void

    private var waitingLabel: LocalizedStringKey {
        waitingPlan ? "Back to plan" : "Back to question"
    }

    var body: some View {
        Button(action: action) {
            if let waitingAgent {
                HStack(spacing: 7) {
                    AgentLogo(id: waitingAgent, size: 18)
                    Text(waitingLabel)
                        .font(.footnote.weight(.semibold))
                        .lineLimit(1)
                    Image(systemName: "arrow.down")
                        .font(.caption2.weight(.bold))
                        .foregroundStyle(.white)
                        .frame(width: 20, height: 20)
                        .background(Color.accentColor, in: .circle)
                }
                .padding(.leading, 10).padding(.trailing, 8).padding(.vertical, 8)
                .glassEffect(.regular.interactive(), in: .capsule)
                .contentShape(.capsule)
            } else if let todoSummary {
                HStack(spacing: 7) {
                    if todoSummary.failed {
                        Image(systemName: "xmark")
                            .font(.caption.weight(.bold))
                            .foregroundStyle(.red)
                            .frame(width: 14, height: 14)
                    } else {
                        MovingMark(size: 14, motion: .seq)
                    }
                    Text(verbatim: "\(todoSummary.stepNumber)/\(todoSummary.total)")
                        .font(.caption.monospacedDigit())
                        .foregroundStyle(.secondary)
                        .fixedSize()
                    Text(verbatim: todoSummary.text)
                        .font(.footnote.weight(.medium))
                        .lineLimit(1)
                        .frame(maxWidth: 196, alignment: .leading)
                    Image(systemName: "arrow.down")
                        .font(.caption2.weight(.bold))
                        .foregroundStyle(.white)
                        .frame(width: 20, height: 20)
                        .background(Color.accentColor, in: .circle)
                }
                .padding(.leading, 11).padding(.trailing, 8).padding(.vertical, 8)
                .glassEffect(.regular.interactive(), in: .capsule)
                .contentShape(.capsule)
            } else {
                Image(systemName: "arrow.down").font(.body.weight(.semibold)).frame(width: 44, height: 44)
                    .glassEffect(.regular.interactive(), in: .circle)
                    .contentShape(.circle)
            }
        }
        .buttonStyle(.plain)
        .accessibilityLabel(waitingAgent != nil ? Text(waitingLabel) : Text("Back to the Bottom"))
        .accessibilityIdentifier("to-end")
    }
}

extension View {
    /// The button back to the end, centred 12 over this (what is at the bottom of a conversation, or nothing),
    /// while `away`: it takes no room, so nothing moves as it comes and goes.
    func toEndButton(_ away: Bool, waitingAgent: String? = nil, waitingPlan: Bool = false, todoSummary: TodoEndSummary? = nil, action: @escaping () -> Void) -> some View {
        overlay(alignment: .top) {
            ZStack {
                if away {
                    ToEndButton(waitingAgent: waitingAgent, waitingPlan: waitingPlan, todoSummary: todoSummary, action: action)
                        .transition(.opacity.combined(with: .offset(y: 6)))
                }
            }
            .frame(minHeight: 44)
            .padding(.horizontal, 16)
            .alignmentGuide(.top) { $0[.bottom] + 12 }
            .animation(.easeOut(duration: 0.18), value: away)
        }
    }
}

/// How far a conversation is scrolled up from its end. Its offset counts from the top of what stands over the page's
/// top (a header), and containerSize is what shows between that and what stands over its bottom (the composer, the
/// keyboard); visibleRect counts both as seen, the end 80 points and more off (measured on an iPhone: at the end,
/// offset 244, content 1019, container 616, insets 158 and 99).
nonisolated func fromEnd(_ g: ScrollGeometry) -> CGFloat {
    g.contentSize.height - g.containerSize.height - g.contentInsets.top - g.contentOffset.y
}

/// Far enough up from a conversation's end to show the button back (the Mac's 240).
nonisolated func scrolledAway(_ g: ScrollGeometry) -> Bool { fromEnd(g) > 240 }
