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
        let tile = Group {
            if !initialOnly, let a = Self.asset(id) {
                Image("Logos/\(a)").resizable().interpolation(.high)
            } else {
                Initial(text: id, color: Self.color(id))
            }
        }
        .frame(width: size, height: size)
        .clipShape(RoundedRectangle(cornerRadius: r, style: .continuous))

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
            TimelineView(.animation(paused: paused)) { timeline in
                let uHead = (timeline.date.timeIntervalSinceReferenceDate / 1.65).truncatingRemainder(dividingBy: 1)
                Canvas { ctx, s in
                    let span = s.width - lineWidth
                    let rCenter = max(1, cornerRadius - lineWidth / 2)
                    let cx = s.width / 2
                    let cy = s.height / 2
                    let centerRect = CGRect(x: lineWidth / 2, y: lineWidth / 2, width: span, height: span)
                    let trackPath = RoundedRectangle(cornerRadius: rCenter, style: .continuous).path(in: centerRect)
                    ctx.stroke(trackPath, with: .color(Color.primary.opacity(colorScheme == .dark ? 0.07 : 0.05)), lineWidth: lineWidth)

                    let arc = 0.36
                    let steps = 56
                    var comet = Path()
                    for i in 0 ... steps {
                        let p = Self.pointOnRoundedSquare(cx: cx, cy: cy, w: span, radius: rCenter, u: uHead - arc + Double(i) / Double(steps) * arc)
                        if i == 0 { comet.move(to: p) } else { comet.addLine(to: p) }
                    }

                    let pT = Self.pointOnRoundedSquare(cx: cx, cy: cy, w: span, radius: rCenter, u: uHead - arc)
                    let pH = Self.pointOnRoundedSquare(cx: cx, cy: cy, w: span, radius: rCenter, u: uHead)
                    let aT = atan2(pT.y - cy, pT.x - cx)
                    let aH = atan2(pH.y - cy, pH.x - cx)
                    var turnSpan = (aH - aT) / (.pi * 2)
                    turnSpan -= floor(turnSpan)
                    let capPad = min(0.032, (1 - turnSpan) * 0.45)
                    let crest = colorScheme == .dark
                        ? Color(red: 0.72, green: 0.78, blue: 1.0)
                        : Color(red: 0.01, green: 0.52, blue: 0.78)

                    var stops: [Gradient.Stop] = [.init(color: .accentColor.opacity(0), location: 0)]
                    let samples = 8
                    for k in 1 ... samples {
                        let t = Double(k) / Double(samples)
                        let pos = turnSpan * t
                        if pos <= 1 {
                            let c: Color = t < 0.82 ? .accentColor : crest
                            stops.append(.init(color: c.opacity(pow(t, 1.5)), location: pos))
                        }
                    }
                    if turnSpan + capPad <= 1 {
                        stops.append(.init(color: .accentColor, location: turnSpan + capPad))
                    }
                    if turnSpan + capPad + 0.015 <= 1 {
                        stops.append(.init(color: .accentColor.opacity(0), location: turnSpan + capPad + 0.015))
                    }

                    ctx.drawLayer { layer in
                        layer.stroke(comet, with: .color(.white), style: StrokeStyle(lineWidth: lineWidth, lineCap: .round, lineJoin: .round))
                        layer.blendMode = .sourceIn
                        layer.fill(
                            Path(CGRect(origin: .zero, size: s)),
                            with: .conicGradient(Gradient(stops: stops), center: CGPoint(x: cx, y: cy), angle: .radians(aT))
                        )
                    }
                }
            }
        }
    }

    private static func pointOnRoundedSquare(cx: CGFloat, cy: CGFloat, w: CGFloat, radius: CGFloat, u: Double) -> CGPoint {
        let r = max(0, min(radius, w / 2))
        let straight = max(0, w - 2 * r)
        let arc = 0.5 * .pi * r
        let perim = 4 * straight + 4 * arc
        let norm = u - floor(u)
        var d = CGFloat(norm) * perim
        let half = w / 2

        if d <= straight / 2 { return CGPoint(x: cx + d, y: cy - half) }
        d -= straight / 2
        if d <= arc {
            let a = -CGFloat.pi / 2 + d / r
            return CGPoint(x: cx + half - r + cos(a) * r, y: cy - half + r + sin(a) * r)
        }
        d -= arc
        if d <= straight { return CGPoint(x: cx + half, y: cy - half + r + d) }
        d -= straight
        if d <= arc {
            let a = d / r
            return CGPoint(x: cx + half - r + cos(a) * r, y: cy + half - r + sin(a) * r)
        }
        d -= arc
        if d <= straight { return CGPoint(x: cx + half - r - d, y: cy + half) }
        d -= straight
        if d <= arc {
            let a = CGFloat.pi / 2 + d / r
            return CGPoint(x: cx - half + r + cos(a) * r, y: cy + half - r + sin(a) * r)
        }
        d -= arc
        if d <= straight { return CGPoint(x: cx - half, y: cy + half - r - d) }
        d -= straight
        if d <= arc {
            let a = CGFloat.pi + d / r
            return CGPoint(x: cx - half + r + cos(a) * r, y: cy - half + r + sin(a) * r)
        }
        d -= arc
        return CGPoint(x: cx - half + r + d, y: cy - half)
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

private struct Shimmer: ViewModifier {
    let on: Bool
    let paused: Bool
    @Environment(\.accessibilityReduceMotion) private var still

    func body(content: Content) -> some View {
        if on && !still {
            content.mask {
                TimelineView(.animation(paused: paused)) { timeline in
                    // The band's middle runs from half the view's width before it to half after; it is 0.72 of it
                    // wide, and lets 0.3 through (the label's colour to the tertiary label's).
                    let t = timeline.date.timeIntervalSinceReferenceDate.truncatingRemainder(dividingBy: 3.2)
                    let x = -0.5 + 2 * ease(min(t / 3, 1))
                    LinearGradient(colors: [.black, .black.opacity(0.3), .black],
                                   startPoint: UnitPoint(x: x - 0.36, y: 0.5), endPoint: UnitPoint(x: x + 0.36, y: 0.5))
                }
            }
        } else {
            content
        }
    }

    /// CSS's ease-in-out, near enough.
    private func ease(_ x: Double) -> Double { x < 0.5 ? 4 * x * x * x : 1 - pow(-2 * x + 2, 3) / 2 }
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
            DispatchQueue.main.async { hideKeyboard() }
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

private func hideKeyboard() {
    UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
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

/// Back to a conversation's end, once it was scrolled well up from it (the Mac's): an arrow in a glass circle, or
/// a "Back to question" glass capsule when a question waits at the bottom.
struct ToEndButton: View {
    var waitingAgent: String? = nil
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            if let waitingAgent {
                HStack(spacing: 7) {
                    AgentLogo(id: waitingAgent, size: 18)
                    Text("Back to question")
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
            } else {
                Image(systemName: "arrow.down").font(.body.weight(.semibold)).frame(width: 44, height: 44)
                    .glassEffect(.regular.interactive(), in: .circle)
                    .contentShape(.circle)
            }
        }
        .buttonStyle(.plain)
        .accessibilityLabel(waitingAgent != nil ? Text("Back to question") : Text("Back to the Bottom"))
        .accessibilityIdentifier("to-end")
    }
}

extension View {
    /// The button back to the end, centred 12 over this (what is at the bottom of a conversation, or nothing),
    /// while `away`: it takes no room, so nothing moves as it comes and goes.
    func toEndButton(_ away: Bool, waitingAgent: String? = nil, action: @escaping () -> Void) -> some View {
        overlay(alignment: .top) {
            ZStack {
                if away {
                    ToEndButton(waitingAgent: waitingAgent, action: action)
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
