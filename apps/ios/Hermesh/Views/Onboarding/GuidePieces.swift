import SwiftUI

// The pieces the first run and the guide share (docs/onboarding.md §5.1):
// a page with its bar and title, the progress, cards and rows, the key's
// diagram and the notifications a phone gets.

/// The guide's four parts: the account, a computer, its coding agents, and
/// connecting an agent.
enum GuidePart: Int {
    case account, computer, codingAgent, connect
}

/// The progress through the guide, a thin bar that grows from where the last
/// page left it.
struct GuideProgress: View {
    let part: GuidePart
    @State private var shown: Double = GuideProgress.last
    /// What the last page showed, so the next one grows from there.
    private static var last: Double = 0

    var body: some View {
        let target = Double(part.rawValue + 1) / 4
        GeometryReader { g in
            ZStack(alignment: .leading) {
                Capsule().fill(Color(.systemFill))
                Capsule().fill(Color.accentColor).frame(width: g.size.width * shown)
            }
        }
        .frame(height: 5)
        .onAppear {
            withAnimation(.smooth(duration: 0.7).delay(0.15)) { shown = target }
            Self.last = target
        }
        .accessibilityElement()
        .accessibilityLabel(Text("Step \(part.rawValue + 1) of 4"))
    }
}

/// The top of a page: back, the progress and Later, each when there is one.
struct GuideBar: View {
    var part: GuidePart?
    var back: (() -> Void)?
    var later: (() -> Void)?

    var body: some View {
        HStack(spacing: 14) {
            if let back {
                Button(action: back) {
                    Image(systemName: "chevron.left").font(.headline).frame(width: 30, height: 30)
                }
                .buttonStyle(.glass).buttonBorderShape(.circle)
                .accessibilityLabel(Text("Back"))
                .accessibilityIdentifier("back")
            } else {
                Color.clear.frame(width: 44, height: 44)
            }
            if let part { GuideProgress(part: part) } else { Spacer() }
            if let later {
                Button("Later", action: later).frame(minWidth: 44, alignment: .trailing)
                    .accessibilityIdentifier("later")
            } else {
                Color.clear.frame(width: 44, height: 44)
            }
        }
        .padding(.horizontal, 16)
        .frame(height: 52)
    }
}

/// A page of the first run or the guide: the bar, a large title, what the
/// page shows, and its buttons kept at the bottom.
struct GuidePage<Content: View, Footer: View>: View {
    var part: GuidePart?
    var back: (() -> Void)?
    var later: (() -> Void)?
    var centered = false
    var icon: AnyView?
    let title: Text
    var lede: Text?
    @ViewBuilder var content: Content
    @ViewBuilder var footer: Footer

    var body: some View {
        VStack(spacing: 0) {
            GuideBar(part: part, back: back, later: later)
            ScrollView {
                VStack(alignment: centered ? .center : .leading, spacing: 0) {
                    if centered { Spacer(minLength: 60) }
                    if let icon { icon.padding(.horizontal, 20).padding(.top, 18) }
                    title.font(.largeTitle.bold()).multilineTextAlignment(centered ? .center : .leading)
                        .padding(.horizontal, 20).padding(.top, 14)
                        .accessibilityAddTraits(.isHeader)
                    if let lede {
                        lede.font(.body).foregroundStyle(.secondary).multilineTextAlignment(centered ? .center : .leading)
                            .padding(.horizontal, 20).padding(.top, 10)
                    }
                    VStack(alignment: .leading, spacing: 13) { content }.padding(.top, 22)
                }
                .frame(maxWidth: .infinity, alignment: centered ? .center : .leading)
            }
            .scrollBounceBehavior(.basedOnSize)
            VStack(spacing: 4) { footer }
                .padding(.horizontal, 16).padding(.top, 12).padding(.bottom, 8)
        }
        .background(Color(.systemGroupedBackground))
    }
}

/// The page's main action, across the width.
struct GuideButton: View {
    let title: Text
    var systemImage: String?
    var id: String?
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 8) {
                if let systemImage { Image(systemName: systemImage) }
                title
            }
            .frame(maxWidth: .infinity)
        }
        .buttonStyle(.glassProminent).controlSize(.large)
        .accessibilityIdentifier(id ?? "")
    }
}

/// A plain action under the main one.
struct GuideLink: View {
    let title: Text
    var systemImage: String?
    var id: String?
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 8) {
                if let systemImage { Image(systemName: systemImage) }
                title
            }
            .frame(maxWidth: .infinity).padding(.vertical, 12)
        }
        .accessibilityIdentifier(id ?? "")
    }
}

/// Waiting for something on another device, under the page's content.
struct GuideWaiting: View {
    let text: Text

    var body: some View {
        HStack(spacing: 8) {
            MovingMark(size: 18, motion: .seq)
            text.foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity).padding(.vertical, 12)
    }
}

/// A rounded group of rows, as in the app's lists.
struct GuideCard<Content: View>: View {
    @ViewBuilder var content: Content

    var body: some View {
        VStack(spacing: 0) { content }
            .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
            .padding(.horizontal, 16)
    }
}

/// A row's thin line, inset past its icon.
struct GuideDivider: View {
    var inset: CGFloat = 60
    var body: some View { Divider().padding(.leading, inset) }
}

/// A small heading over a card.
struct GuideCaption: View {
    let text: Text
    var body: some View {
        text.font(.subheadline.weight(.semibold)).foregroundStyle(.secondary).padding(.horizontal, 20).padding(.top, 4)
    }
}

/// A tinted square with a symbol, before a row's text.
struct GuideTile: View {
    let systemImage: String
    var accent = true
    var size: CGFloat = 32

    var body: some View {
        Image(systemName: systemImage)
            .font(.system(size: size * 0.5, weight: .medium))
            .foregroundStyle(accent ? Color.accentColor : .secondary)
            .frame(width: size, height: size)
            .background(accent ? Color.accentColor.opacity(0.12) : Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: size * 0.28, style: .continuous))
    }
}

/// A row that leads on: an icon, a title (and a line under it) and a chevron.
struct GuideChoice: View {
    let systemImage: String
    var accent = false
    let title: Text
    var subtitle: Text?
    var id: String?
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 12) {
                GuideTile(systemImage: systemImage, accent: accent)
                VStack(alignment: .leading, spacing: 2) {
                    title.font(.body.weight(.semibold)).foregroundStyle(.primary)
                    if let subtitle { subtitle.font(.subheadline).foregroundStyle(.secondary) }
                }
                Spacer(minLength: 0)
                Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(.tertiary)
            }
            .padding(.horizontal, 16).frame(minHeight: 60).contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier(id ?? "")
    }
}

// MARK: The management key

/// What the management key does: a request from a computer, an agent or a
/// device goes through the server to this iPhone, and the approval signed
/// here goes back. The server only relays.
struct KeyDiagram: View {
    /// The server's name: hermesh.io, or the host of one's own.
    let server: String
    @Environment(\.accessibilityReduceMotion) private var still

    private let width: CGFloat = 340, height: CGFloat = 222
    private let xs: [CGFloat] = [66, 170, 274]
    private let key = CGPoint(x: 170, y: 180)

    var body: some View {
        GeometryReader { g in
            let scale = g.size.width / width
            canvas.frame(width: width, height: height).scaleEffect(scale, anchor: .topLeading)
        }
        .aspectRatio(width / height, contentMode: .fit)
        .accessibilityElement()
        .accessibilityLabel(Text("Requests from your computers, agents and devices go through \(server) to this iPhone; you approve them here with the management key, and \(server) only relays."))
    }

    private var canvas: some View {
        ZStack(alignment: .topLeading) {
            TimelineView(.animation(paused: still)) { t in
                Canvas { ctx, _ in draw(&ctx, t: t.date.timeIntervalSinceReferenceDate) }
            }
            ForEach(Array(nodes.enumerated()), id: \.offset) { i, n in
                node(n.symbol).position(x: xs[i], y: 34)
                n.label.font(.footnote.weight(.semibold)).fixedSize()
                    .position(x: xs[i] + 27, y: 34).offset(x: 14)
            }
            Text(server).font(.caption2.monospaced()).foregroundStyle(.secondary).lineLimit(1)
                .frame(width: 180, alignment: .leading).position(x: 34 + 90, y: 109)
            Text("Only relays").font(.caption2).foregroundStyle(.secondary).fixedSize()
                .frame(width: 120, alignment: .trailing).position(x: 306 - 60, y: 109)
            ring.position(key)
            Circle().fill(Color.accentColor).frame(width: 52, height: 52).position(key)
            Image(systemName: "key").font(.system(size: 22, weight: .medium)).foregroundStyle(.white).position(key)
            Text("This iPhone").font(.caption2).foregroundStyle(.secondary).fixedSize()
                .frame(width: 100, alignment: .leading).position(x: 210 + 50, y: 180)
        }
        .frame(width: width, height: height, alignment: .topLeading)
    }

    private var nodes: [(symbol: String, label: Text)] {
        [("laptopcomputer", Text("Computer")), ("sparkle", Text(verbatim: "agent")), ("iphone", Text("Device"))]
    }

    private func node(_ symbol: String) -> some View {
        ZStack {
            Circle().fill(Color(.systemGroupedBackground)).overlay(Circle().strokeBorder(Color(.separator), lineWidth: 1))
            Image(systemName: symbol).font(.system(size: 16, weight: .medium)).foregroundStyle(.primary)
        }
        .frame(width: 42, height: 42)
        .overlay(alignment: .topTrailing) {
            Image(systemName: "checkmark").font(.system(size: 8, weight: .heavy)).foregroundStyle(.white)
                .frame(width: 15, height: 15).background(.green, in: Circle())
                .overlay(Circle().strokeBorder(Color(.secondarySystemGroupedBackground), lineWidth: 2))
                .offset(x: 3, y: -3)
        }
    }

    private var ring: some View {
        TimelineView(.animation(paused: still)) { t in
            Circle().strokeBorder(Color.accentColor.opacity(0.5), style: StrokeStyle(lineWidth: 1.2, dash: [3, 5]))
                .frame(width: 70, height: 70)
                .rotationEffect(.degrees(t.date.timeIntervalSinceReferenceDate.truncatingRemainder(dividingBy: 18) * 20))
        }
    }

    /// From a computer, agent or device (at x) down to the key.
    private func curve(_ x: CGFloat) -> (CGPoint, CGPoint, CGPoint, CGPoint) {
        (CGPoint(x: x, y: 58), CGPoint(x: x, y: 118), CGPoint(x: 170, y: 112), CGPoint(x: 170, y: 150))
    }

    private func point(_ c: (CGPoint, CGPoint, CGPoint, CGPoint), _ t: CGFloat) -> CGPoint {
        let u = 1 - t
        let a = u * u * u, b = 3 * u * u * t, d = 3 * u * t * t, e = t * t * t
        return CGPoint(x: a * c.0.x + b * c.1.x + d * c.2.x + e * c.3.x, y: a * c.0.y + b * c.1.y + d * c.2.y + e * c.3.y)
    }

    private func draw(_ ctx: inout GraphicsContext, t: Double) {
        for x in xs {
            let c = curve(x)
            var p = Path()
            p.move(to: c.0)
            p.addCurve(to: c.3, control1: c.1, control2: c.2)
            ctx.stroke(p, with: .color(.accentColor.opacity(0.55)), lineWidth: 1.6)
        }
        // The server, over the lines: they go through it.
        let band = Path(roundedRect: CGRect(x: 14, y: 94, width: 312, height: 30), cornerRadius: 15)
        ctx.fill(band, with: .color(Color(.systemGroupedBackground)))
        ctx.stroke(band, with: .color(Color(.separator)), lineWidth: 1)
        guard !still else { return }
        // Each request goes down (grey), then its approval comes back (blue).
        for (i, x) in xs.enumerated() {
            let phase = ((t - Double(i) * 0.6).truncatingRemainder(dividingBy: 3.6) + 3.6).truncatingRemainder(dividingBy: 3.6) / 3.6
            let c = curve(x)
            if phase < 0.4 {
                let p = point(c, phase / 0.4)
                ctx.fill(Path(ellipseIn: CGRect(x: p.x - 3, y: p.y - 3, width: 6, height: 6)), with: .color(Color(.tertiaryLabel)))
            } else if phase > 0.5 && phase < 0.9 {
                let p = point(c, 1 - (phase - 0.5) / 0.4)
                ctx.fill(Path(ellipseIn: CGRect(x: p.x - 3.4, y: p.y - 3.4, width: 6.8, height: 6.8)), with: .color(.accentColor))
            }
        }
    }
}

// MARK: Notifications

/// What reaches this iPhone, as it shows on the lock screen: one notice of
/// each kind a machine or the server sends.
struct LockScreenPreview: View {
    @State private var shown = false
    @Environment(\.accessibilityReduceMotion) private var still

    var body: some View {
        VStack(spacing: 8) {
            Text(Date.now, format: .dateTime.month().day().weekday(.wide)).font(.subheadline.weight(.semibold)).opacity(0.9)
            Text(Date.now, format: .dateTime.hour(.defaultDigits(amPM: .omitted)).minute())
                .font(.system(size: 54, weight: .bold, design: .rounded)).monospacedDigit().padding(.bottom, 6)
            notice(0, when: Text("now"), title: Text("ChatGPT wants to connect"), body: Text("Tap to approve"))
            notice(1, when: Text("2 min ago"), title: Text(verbatim: "Claude Code · app"), body: Text("Delete the 3 old migration files?"), actions: true)
            notice(2, when: Text("8 min ago"), title: Text("ChatGPT started a session"), body: Text("Refactor sign-in · vps-1"))
            notice(3, when: Text("20 min ago"), title: Text("Refactor sign-in is done"), body: Text("Claude Code · 12 minutes"))
        }
        .foregroundStyle(.white)
        .padding(.horizontal, 10).padding(.top, 16).padding(.bottom, 12)
        .frame(maxWidth: .infinity)
        .background { Aurora().clipShape(RoundedRectangle(cornerRadius: 30, style: .continuous)) }
        .padding(.horizontal, 16)
        .onAppear { shown = true }
        .accessibilityElement(children: .combine)
    }

    private func notice(_ i: Int, when: Text, title: Text, body: Text, actions: Bool = false) -> some View {
        HStack(alignment: .top, spacing: 10) {
            Mark(size: 26).frame(width: 34, height: 34).background(.white, in: RoundedRectangle(cornerRadius: 9, style: .continuous))
                .environment(\.colorScheme, .light)
            VStack(alignment: .leading, spacing: 1) {
                HStack {
                    Text(verbatim: "Hermesh")
                    Spacer()
                    when
                }
                .font(.caption).foregroundStyle(.white.opacity(0.7))
                title.font(.subheadline.weight(.semibold))
                body.font(.subheadline)
                if actions {
                    HStack(spacing: 6) {
                        Text("Delete").padding(.horizontal, 14).padding(.vertical, 5).background(.white.opacity(0.2), in: Capsule())
                        Text("Keep").padding(.horizontal, 14).padding(.vertical, 5).background(.white.opacity(0.2), in: Capsule())
                    }
                    .font(.footnote.weight(.semibold)).padding(.top, 6)
                }
            }
        }
        .padding(.horizontal, 13).padding(.vertical, 11)
        .background(.ultraThinMaterial.opacity(0.85), in: RoundedRectangle(cornerRadius: 22, style: .continuous))
        .environment(\.colorScheme, .dark)
        .opacity(shown || still ? 1 : 0)
        .offset(y: shown || still ? 0 : -14)
        .animation(.smooth(duration: 0.55).delay(0.15 + Double(i) * 0.3), value: shown)
    }
}

/// The aurora behind the lock screen preview: the brand's colors on night blue.
private struct Aurora: View {
    var body: some View {
        ZStack {
            Color(red: 0.04, green: 0.06, blue: 0.13)
            RadialGradient(colors: [Color(red: 0.13, green: 0.88, blue: 0.79).opacity(0.55), .clear], center: UnitPoint(x: 0.15, y: 0.12), startRadius: 0, endRadius: 220)
            RadialGradient(colors: [Color(red: 0.48, green: 0.38, blue: 1).opacity(0.7), .clear], center: UnitPoint(x: 0.92, y: 0.3), startRadius: 0, endRadius: 260)
            RadialGradient(colors: [Color(red: 0.24, green: 0.48, blue: 1).opacity(0.75), .clear], center: UnitPoint(x: 0.5, y: 1), startRadius: 0, endRadius: 320)
        }
    }
}
