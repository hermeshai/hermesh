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
        let known = ["claude", "codex", "gemini", "cursor", "copilot", "opencode", "qwen", "goose", "auggie", "kimi", "kiro", "qoder", "deepseek", "codebuddy", "grok", "grokbot", "muse", "openclaw"]
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

/// A session's state as a dot: green and pulsing while it runs, orange
/// while it waits on the owner, red when it failed. An idle or archived
/// session has none (as on the Mac), but keeps the dot's room so titles line up.
struct StatusDot: View {
    let session: Session
    @State private var pulse = false

    var body: some View {
        ZStack {
            if session.isActive {
                Circle().fill(.green.opacity(0.4)).frame(width: 9, height: 9).scaleEffect(pulse ? 2.3 : 1).opacity(pulse ? 0 : 0.6)
                    .onAppear { withAnimation(.easeOut(duration: 1.8).repeatForever(autoreverses: false)) { pulse = true } }
            }
            if session.isActive || session.needsAttention {
                Circle().fill(color).frame(width: 9, height: 9)
            }
        }
        .frame(width: 20)
        .accessibilityLabel(statusTitle(session.status))
    }

    private var color: Color {
        if session.status == .waitingInput { return .orange }
        if session.isFailed { return .red }
        return .green
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
