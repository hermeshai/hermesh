import SwiftUI

// Waiting, in the brand's motion (as packages/ui's mark.tsx and brand.css):
// motion only says what is happening, it only ever uses the mark, and every
// animation is the mark's locked shape at rest. A wait under 400 ms shows
// nothing; once shown, a loader stays at least 500 ms; with Reduce Motion
// nothing moves, the mark breathes instead.

/// How the mark moves: pulse, loading (a bright pulse runs along each beam
/// and the three meet in the middle); seq, busy at small sizes (the beams
/// light up in turn); wait, waiting for the other side (the beams hover
/// apart and breathe).
enum MarkMotion {
    case pulse, seq, wait
}

/// The mark, moving.
struct MovingMark: View {
    var size: CGFloat = 32
    var motion: MarkMotion = .pulse
    /// In the text colour, as in buttons and small inline waits.
    var mono = false
    @Environment(\.colorScheme) private var scheme
    @Environment(\.accessibilityReduceMotion) private var still

    var body: some View {
        TimelineView(.animation) { timeline in
            let t = timeline.date.timeIntervalSinceReferenceDate
            Canvas { ctx, s in draw(&ctx, s, t) }
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }

    // The beams: at 0°, 60° and 120°, each 10 units off the centre, 74 long, 12 wide.
    private static let beams: [(p0: CGPoint, p1: CGPoint, n: CGPoint)] = (0 ..< 3).map { i in
        let a = Double(i) * .pi / 3
        let d = CGPoint(x: cos(a), y: sin(a)), n = CGPoint(x: -d.y, y: d.x)
        let m = CGPoint(x: n.x * 10, y: n.y * 10)
        return (CGPoint(x: m.x - d.x * 37, y: m.y - d.y * 37), CGPoint(x: m.x + d.x * 37, y: m.y + d.y * 37), n)
    }

    // The Aurora gradient's nine stops (--aurora-g0..8), on light and on dark.
    private static let light = ["0891b2", "008cc2", "0084d3", "0079e3", "2f6bf0", "4361f2", "5356f3", "614af2", "6d3cf0"].map(Color.init(hex:))
    private static let dark = ["22e1c9", "00d0de", "00b9f1", "009cfe", "3e7bff", "5175ff", "616eff", "6e68ff", "7b61ff"].map(Color.init(hex:))

    private func draw(_ ctx: inout GraphicsContext, _ s: CGSize, _ t: Double) {
        let pad: CGFloat = motion == .wait ? 20 : 0 // room for the beams to move apart
        let scale = s.width / (104 + 2 * pad) // as Mark
        ctx.translateBy(x: s.width / 2, y: s.height / 2)
        ctx.scaleBy(x: scale, y: scale)
        let dark = scheme == .dark
        let base = mono ? 1 : dark ? 0.6 : 0.72
        if !mono { ctx.blendMode = dark ? .plusLighter : .multiply }
        let breathe = still ? 0.35 + 0.65 * ease(alternate(t, 2)) : 1 // Reduce Motion: the whole mark fades in and out
        for (i, b) in Self.beams.enumerated() {
            var path = Path()
            path.move(to: b.p0)
            path.addLine(to: b.p1)
            let shading: GraphicsContext.Shading = mono ? .foreground : .linearGradient(Gradient(colors: dark ? Self.dark : Self.light), startPoint: b.p0, endPoint: b.p1)
            let style = StrokeStyle(lineWidth: 12, lineCap: .round)
            var beam = ctx
            switch (motion, still) {
            case (_, true):
                beam.opacity = base * breathe
                beam.stroke(path, with: shading, style: style)
            case (.pulse, false):
                beam.opacity = mono ? 0.28 : 0.26
                beam.stroke(path, with: shading, style: style)
                // A dash 26% of the beam long, from before its tail to past its head.
                let start = -0.28 + 1.3 * ease((t / 1.6).truncatingRemainder(dividingBy: 1))
                let from = max(0, start), to = min(1, start + 0.26)
                if to > from {
                    beam.opacity = 1
                    beam.stroke(path.trimmedPath(from: from, to: to), with: shading, style: style)
                }
            case (.seq, false):
                let p = ((t - 0.4 * Double(i)) / 1.2).truncatingRemainder(dividingBy: 1)
                let lit = p < 0 ? 0 : p < 0.3 ? p / 0.3 : p < 0.6 ? (0.6 - p) / 0.3 : 0
                beam.opacity = (0.28 + 0.72 * max(0, lit)) * (mono ? 1 : base / 0.72)
                beam.stroke(path, with: shading, style: style)
            case (.wait, false):
                let e = ease(alternate(t, 2.4))
                let apart = 9 * (1 - 0.65 * e)
                beam.translateBy(x: b.n.x * apart, y: b.n.y * apart)
                beam.opacity = base * (0.6 + 0.4 * e)
                beam.stroke(path, with: shading, style: style)
            }
        }
    }

    /// 0 → 1 → 0 over twice period seconds.
    private func alternate(_ t: Double, _ period: Double) -> Double {
        let p = (t / period).truncatingRemainder(dividingBy: 2)
        return p > 1 ? 2 - p : p
    }

    /// The brand's ease-in-out, cubic-bezier(0.45, 0, 0.55, 1), near enough.
    private func ease(_ x: Double) -> Double { x < 0.5 ? 4 * x * x * x : 1 - pow(-2 * x + 2, 3) / 2 }
}

extension Color {
    nonisolated init(hex: String) {
        let v = UInt32(hex, radix: 16) ?? 0
        self.init(red: Double(v >> 16 & 0xff) / 255, green: Double(v >> 8 & 0xff) / 255, blue: Double(v & 0xff) / 255)
    }
}

/// A wait the page has the room for: the mark, pulsing, and what is being
/// done ("Reading the conversation…", never just "Loading…"); bare, the
/// mark alone, which says it to VoiceOver only.
struct PageLoader: View {
    let text: LocalizedStringKey
    var bare = false
    var body: some View {
        Delayed(active: true) {
            VStack(spacing: 12) {
                MovingMark(size: 40)
                if !bare {
                    Text(text).font(.subheadline).foregroundStyle(.secondary).multilineTextAlignment(.center)
                }
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, bare ? 0 : 34)
            .accessibilityElement(children: .combine)
            .accessibilityLabel(Text(text))
            .accessibilityIdentifier("loading")
        }
    }
}

/// A wait in a line: the small mark in the text colour, its beams lighting
/// up in turn, and what is being done.
struct InlineLoader: View {
    let text: LocalizedStringKey
    var body: some View {
        HStack(spacing: 8) {
            MovingMark(size: 15, motion: .seq, mono: true)
            Text(text)
        }
        .foregroundStyle(.secondary)
        .accessibilityElement(children: .combine)
    }
}

/// Waiting for the other side (another device, a machine to connect): the
/// beams hover apart and breathe.
struct WaitingMark: View {
    var size: CGFloat = 64
    var body: some View { MovingMark(size: size, motion: .wait) }
}

/// Its content once active has lasted 400 ms, and then for at least 500 ms,
/// so that a short wait never flashes.
struct Delayed<Content: View>: View {
    let active: Bool
    @ViewBuilder var content: () -> Content
    @State private var shown = false
    @State private var since = Date.distantPast

    var body: some View {
        ZStack {
            if shown { content().transition(.opacity) }
        }
        .task(id: active) {
            if active {
                try? await Task.sleep(for: .milliseconds(400))
                guard !Task.isCancelled else { return }
                since = .now
                withAnimation(.easeOut(duration: 0.2)) { shown = true }
            } else if shown {
                let left = 0.5 - Date.now.timeIntervalSince(since)
                if left > 0 { try? await Task.sleep(for: .seconds(left)) }
                guard !Task.isCancelled else { return }
                withAnimation(.easeOut(duration: 0.2)) { shown = false }
            }
        }
    }
}

/// While what a page shows is not current yet (the app is connecting, or
/// reading a machine again after a while away): a quiet line saying so. A
/// root page has it in the middle of its navigation bar, where nothing else
/// is; a page with a title of its own has it floating over its top. Either
/// way nothing moves when it comes or goes.
struct SyncLine: View {
    @Environment(AppModel.self) private var model
    /// In the navigation bar: smaller, and without a backing.
    var inBar = false
    /// What it last said, kept while it fades out.
    @State private var said: Sync?

    var body: some View {
        Delayed(active: model.sync != nil) {
            if let sync = model.sync ?? said {
                HStack(spacing: 8) {
                    if case .offline = sync {
                        Mark(size: 15).opacity(0.4) // offline: the mark at rest, faint (as the menu bar's)
                    } else {
                        MovingMark(size: 15, motion: .seq, mono: true)
                    }
                    Text(sync.text).lineLimit(1)
                }
                .font(inBar ? .caption : .footnote).foregroundStyle(.secondary)
                .padding(.horizontal, inBar ? 0 : 14).padding(.vertical, inBar ? 0 : 8)
                .background(inBar ? AnyShapeStyle(.clear) : AnyShapeStyle(.thickMaterial), in: .capsule) // over the page's text, which glass lets through
                .shadow(color: .black.opacity(inBar ? 0 : 0.08), radius: 8, y: 2)
                .padding(.top, inBar ? 0 : 6)
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("sync-line")
            }
        }
        .onChange(of: model.sync, initial: true) { _, now in if let now { said = now } }
    }
}

extension View {
    /// The sync line floating at the top of the page.
    func syncLine() -> some View { overlay(alignment: .top) { SyncLine() } }
}
