import HermeshProto
import HermeshSessions
import SwiftUI

// Small pieces the screens share.

/// A coding agent's or personal agent's logo, or a tile with its initial.
struct AgentLogo: View {
    let id: String
    var size: CGFloat = 28
    /// Its initial even when id names an app (a grant whose owner chose its initial).
    var initialOnly = false

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
        Group {
            if !initialOnly, let a = Self.asset(id) {
                Image("Logos/\(a)").resizable().interpolation(.high)
            } else {
                Initial(text: id, color: Self.color(id))
            }
        }
        .frame(width: size, height: size)
        .clipShape(RoundedRectangle(cornerRadius: size * 0.26, style: .continuous))
        .accessibilityHidden(true)
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

/// A running session's title, in the menu and in the lists, as the Mac's
/// sidebar has it (app.css .stitle.run): a fainter band crosses it from left
/// to right in 3 s, and after a breath (0.2 s) crosses it again. With Reduce
/// Motion it stays still.
struct ShimmerText: View {
    let title: String
    /// While no one sees it (the menu closed): it does not draw.
    let paused: Bool
    @Environment(\.accessibilityReduceMotion) private var still

    init(_ title: String, paused: Bool) {
        self.title = title
        self.paused = paused
    }

    var body: some View {
        if still {
            Text(title)
        } else {
            TimelineView(.animation(paused: paused)) { timeline in
                // The band's middle runs from half the title's width before it to half after; it is 0.72 of it wide.
                let t = timeline.date.timeIntervalSinceReferenceDate.truncatingRemainder(dividingBy: 3.2)
                let x = -0.5 + 2 * ease(min(t / 3, 1))
                Text(title).foregroundStyle(LinearGradient(
                    colors: [Color(.label), Color(.tertiaryLabel), Color(.label)],
                    startPoint: UnitPoint(x: x - 0.36, y: 0.5), endPoint: UnitPoint(x: x + 0.36, y: 0.5)))
            }
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
/// its initial), else its app's logo, else a tile with its initial.
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
    /// The owner's messages: near black on the right in the light, a faint white in the dark.
    static let bubble = Color(UIColor { $0.userInterfaceStyle == .dark ? UIColor(white: 1, alpha: 0.09) : UIColor(white: 0, alpha: 0.86) })
    static let bubbleText = Color(UIColor { $0.userInterfaceStyle == .dark ? UIColor(white: 1, alpha: 0.88) : .white })
}
