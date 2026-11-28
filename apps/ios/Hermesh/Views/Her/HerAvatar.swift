import SwiftUI

/// Her's avatar: her cat, with no ground of its own (the Mac draws the same one, her/Avatar.tsx). A round body
/// with two ears behind it, a lighter face, two eyes, a cat's mouth and a blush, in soft shading with no outline.
/// She breathes and blinks; entering her conversation, she waves hello; busy, she looks around and bobs; asleep (she
/// runs nowhere yet), her eyes are shut and she breathes slowly. Still with Reduce Motion.
struct HerAvatar: View {
    var size: CGFloat
    var busy = false
    var asleep = false
    var greet = false
    @Environment(\.accessibilityReduceMotion) private var still
    @State private var greeted = false
    @State private var greetStart: TimeInterval?

    var body: some View {
        TimelineView(.animation(minimumInterval: greetStart != nil ? 1 / 60 : 1 / 30, paused: still)) { timeline in
            let t = still ? nil : timeline.date.timeIntervalSinceReferenceDate
            let wave: Double? = if let start = greetStart, let t, !asleep {
                t >= start && t - start < HerCat.greetDuration ? (t - start) / HerCat.greetDuration : nil
            } else {
                nil
            }
            Canvas { ctx, _ in HerCat.draw(&ctx, size: size, time: t, busy: busy, asleep: asleep, wave: wave) }
        }
        .frame(width: size, height: size)
        .onAppear {
            if greet && !greeted && !asleep && !still {
                greeted = true
                greetStart = Date.timeIntervalSinceReferenceDate
            }
        }
        .onChange(of: greet) { was, now in
            if now && !was && !asleep && !still {
                greeted = true
                greetStart = Date.timeIntervalSinceReferenceDate
            } else if !now {
                greeted = false
                greetStart = nil
            }
        }
        .task(id: greetStart) {
            guard greetStart != nil else { return }
            try? await Task.sleep(for: .milliseconds(1300))
            if !Task.isCancelled { greetStart = nil }
        }
        .accessibilityHidden(true)
    }
}

/// Her's cat at small sizes (rows, model badges, subtitle lines), matching Cat.tsx `small = true`:
/// body, ears, face and larger solid eyes alone, still.
struct HerSmallCat: View {
    var size: CGFloat

    var body: some View {
        Canvas { ctx, _ in HerCat.draw(&ctx, size: size, time: nil, busy: false, small: true) }
            .frame(width: size, height: size)
            .accessibilityHidden(true)
    }
}

/// The cat, on a 120 grid of which the box 8 16 104 104 shows.
enum HerCat {
    static let greetDuration = 1.26

    private static func pt(_ x: CGFloat, _ y: CGFloat) -> CGPoint { CGPoint(x: x, y: y) }

    private static func sample(_ u: Double, _ stops: [(Double, CGFloat)]) -> CGFloat {
        guard let first = stops.first, let last = stops.last else { return 0 }
        if u <= first.0 { return first.1 }
        if u >= last.0 { return last.1 }
        for i in 0 ..< stops.count - 1 {
            let (t0, v0) = stops[i], (t1, v1) = stops[i + 1]
            if u <= t1 {
                let p = (u - t0) / max(1e-6, t1 - t0)
                let s = (1 - cos(.pi * p)) / 2
                return v0 + (v1 - v0) * CGFloat(s)
            }
        }
        return last.1
    }

    private static let body: Path = {
        var p = Path()
        p.move(to: pt(60, 30))
        p.addCurve(to: pt(103, 72), control1: pt(84, 30), control2: pt(100, 48))
        p.addCurve(to: pt(60, 110), control1: pt(106, 96), control2: pt(92, 110))
        p.addCurve(to: pt(17, 72), control1: pt(28, 110), control2: pt(14, 96))
        p.addCurve(to: pt(60, 30), control1: pt(20, 48), control2: pt(36, 30))
        p.closeSubpath()
        return p
    }()

    private static let face: Path = {
        var p = Path()
        p.move(to: pt(46, 47))
        p.addLine(to: pt(74, 47))
        p.addCurve(to: pt(92, 65.5), control1: pt(86, 47), control2: pt(92, 55))
        p.addCurve(to: pt(60, 88), control1: pt(92, 80), control2: pt(82, 88))
        p.addCurve(to: pt(28, 65.5), control1: pt(38, 88), control2: pt(28, 80))
        p.addCurve(to: pt(46, 47), control1: pt(28, 55), control2: pt(34, 47))
        p.closeSubpath()
        return p
    }()

    private static func triangle(_ a: CGPoint, _ b: CGPoint, _ c: CGPoint) -> Path {
        var p = Path()
        p.move(to: a)
        p.addLine(to: b)
        p.addLine(to: c)
        p.closeSubpath()
        return p
    }

    private static let ear = triangle(pt(30, 53), pt(26, 25), pt(50, 37))
    private static let earInside = triangle(pt(33, 45), pt(31.5, 33), pt(42.5, 38.5))
    /// The other side: x mirrored about the middle.
    private static let mirror = CGAffineTransform(a: -1, b: 0, c: 0, d: 1, tx: 120, ty: 0)

    private static let mouth: Path = {
        var p = Path()
        p.move(to: pt(55.2, 71.2))
        p.addQuadCurve(to: pt(60, 71.2), control: pt(57.6, 74.5))
        p.addQuadCurve(to: pt(64.8, 71.2), control: pt(62.4, 74.5))
        return p
    }()

    private static let wavePaw: Path = {
        var p = Path()
        p.move(to: pt(88.5, 77.5))
        p.addCurve(to: pt(97.4, 54.4), control1: pt(90, 69), control2: pt(94.5, 59))
        p.addCurve(to: pt(111.8, 53.6), control1: pt(100.4, 49.4), control2: pt(108, 48.8))
        p.addCurve(to: pt(108.6, 72.6), control1: pt(115.4, 58.2), control2: pt(113.3, 65.6))
        p.addCurve(to: pt(96.4, 83.5), control1: pt(105.2, 77.6), control2: pt(101.4, 82.5))
        p.addCurve(to: pt(88.5, 77.5), control1: pt(91.4, 84.5), control2: pt(87.5, 82.4))
        p.closeSubpath()
        return p
    }()

    private static let coat = Gradient(stops: [.init(color: Color(hex: "f1f3ff"), location: 0), .init(color: Color(hex: "d7ddff"), location: 0.55), .init(color: Color(hex: "b3bcf2"), location: 1)])
    private static let pawCoat = Gradient(stops: [.init(color: Color(hex: "ecefff"), location: 0), .init(color: Color(hex: "d4dcff"), location: 0.52), .init(color: Color(hex: "c3cbf6"), location: 1)])
    private static let pawMask = Gradient(stops: [.init(color: .white, location: 0), .init(color: .white, location: 0.72), .init(color: .clear, location: 1)])
    private static let earColor = Color(hex: "cbd3fb")
    private static let blush = Color(hex: "ffa3be")
    private static let ink = Color(hex: "3c2f3a")

    /// Draws her in a square of `size`; `time` nil holds her still; `wave` (0..<1) plays her greeting; `small` draws body, ears, face and larger eyes alone.
    static func draw(_ ctx: inout GraphicsContext, size: CGFloat, time: Double?, busy: Bool, asleep: Bool = false, small: Bool = false, wave: Double? = nil) {
        let k = size / 104
        ctx.scaleBy(x: k, y: k)
        ctx.translateBy(x: -8, y: -16)
        let fill = GraphicsContext.Shading.radialGradient(coat, center: pt(44, 50), startRadius: 0, endRadius: 72)
        if small {
            for side in [CGAffineTransform.identity, mirror] {
                let e = ear.applying(side)
                ctx.fill(e, with: .color(earColor))
                ctx.stroke(e, with: .color(earColor), style: StrokeStyle(lineWidth: 8, lineJoin: .round))
            }
            ctx.fill(body, with: fill)
            ctx.fill(face, with: .color(Color(hex: "fafbff")))
            for x in [47.5, 72.5] {
                ctx.fill(Path(ellipseIn: CGRect(x: x - 5.2, y: 64 - 5.9, width: 10.4, height: 11.8)), with: .color(ink))
            }
            return
        }
        // Blurred in a layer of its own, by the Mac's standard deviation: half of it as a radius here, in points.
        func soft(_ deviation: CGFloat, _ draw: (inout GraphicsContext) -> Void) {
            ctx.drawLayer { layer in
                layer.addFilter(.blur(radius: deviation * k / 2))
                draw(&layer)
            }
        }
        soft(1.1) { $0.fill(Path(ellipseIn: CGRect(x: 26, y: 107.4, width: 68, height: 7.2)), with: .color(Color(hex: "4b4660").opacity(0.12))) }

        // She breathes from the ground up; greeting, she perks and tilts; busy, a quicker bob.
        let t = time ?? 0
        if let u = wave {
            let dy = sample(u, [(0, 0), (0.10, 0), (0.24, -3.7), (0.44, -2.5), (0.62, -3.2), (0.82, 0), (1, 0)])
            let rot = sample(u, [(0, 0), (0.10, 0), (0.24, -4.2), (0.44, -2.8), (0.62, -4.0), (0.82, 0.8), (1, 0)]) * .pi / 180
            let sx = sample(u, [(0, 1), (0.10, 1.03), (0.24, 0.985), (0.62, 1.0), (0.82, 1.02), (1, 1)])
            let sy = sample(u, [(0, 1), (0.10, 0.96), (0.24, 1.02), (0.62, 1.0), (0.82, 0.98), (1, 1)])
            ctx.translateBy(x: 60, y: 110 + dy)
            ctx.rotate(by: .radians(Double(rot)))
            ctx.scaleBy(x: sx, y: sy)
            ctx.translateBy(x: -60, y: -110)
        } else {
            let breath = time == nil ? 0 : (1 - cos(2 * .pi * t / (busy ? 0.8 : asleep ? 5 : 3.4))) / 2
            ctx.translateBy(x: 60, y: 110)
            ctx.scaleBy(x: 1 + (busy ? 0.03 : 0.02) * breath, y: 1 - (busy ? 0.04 : 0.025) * breath)
            ctx.translateBy(x: -60, y: -110)
        }

        let earLRot = wave.map { sample($0, [(0, 0), (0.22, -5), (0.58, -3), (0.80, 0), (1, 0)]) * .pi / 180 } ?? 0
        let earRRot = wave.map { sample($0, [(0, 0), (0.20, 7), (0.38, -3), (0.56, 6), (0.76, 0), (1, 0)]) * .pi / 180 } ?? 0
        for (isRight, side) in [(false, CGAffineTransform.identity), (true, mirror)] {
            let pivot = pt(isRight ? 80 : 40, 45)
            let rot = isRight ? earRRot : earLRot
            let earTx = side.concatenating(
                CGAffineTransform(translationX: pivot.x, y: pivot.y)
                    .rotated(by: rot)
                    .translatedBy(x: -pivot.x, y: -pivot.y)
            )
            let e = ear.applying(earTx)
            ctx.fill(e, with: .color(earColor))
            ctx.stroke(e, with: .color(earColor), style: StrokeStyle(lineWidth: 8, lineJoin: .round))
            soft(0.8) {
                let inside = earInside.applying(earTx)
                $0.fill(inside, with: .color(blush.opacity(0.55)))
                $0.stroke(inside, with: .color(blush.opacity(0.55)), style: StrokeStyle(lineWidth: 3, lineJoin: .round))
            }
        }
        ctx.fill(Path(ellipseIn: CGRect(x: 8.5, y: 78.5, width: 15, height: 15)), with: fill)
        let tuckX = wave.map { sample($0, [(0, 0), (0.12, -8.8), (0.84, -8.8), (1, 0)]) } ?? 0
        ctx.fill(Path(ellipseIn: CGRect(x: 96.5 + tuckX, y: 78.5, width: 15, height: 15)), with: fill)

        ctx.fill(body, with: fill)
        soft(1.1) { $0.fill(Path(ellipseIn: CGRect(x: 30, y: 43.4, width: 14, height: 7.2)).applying(CGAffineTransform(translationX: 37, y: 47).rotated(by: -.pi / 6).translatedBy(x: -37, y: -47)), with: .color(.white.opacity(0.5))) }
        soft(0.8) { $0.fill(face, with: .color(Color(hex: "fafbff"))) }
        let blushAlpha = wave.map { Double(sample($0, [(0, 0.6), (0.22, 0.85), (0.74, 0.85), (1, 0.6)])) } ?? 0.6
        soft(2.4) {
            for x in [37.0, 83.0] { $0.fill(Path(ellipseIn: CGRect(x: x - 6.5, y: 68.6, width: 13, height: 8.8)), with: .color(blush.opacity(blushAlpha))) }
        }

        // The eyes: a blink every 4.8 s; greeting, smiling arcs between two quick blinks; busy, looking from side to side.
        if let u = wave {
            let smiling = u >= 0.10 && u < 0.84
            for x in [48.5, 71.5] {
                var eye = ctx
                eye.translateBy(x: x, y: 64)
                if smiling {
                    let arcScale = sample(u, [(0.10, 0.35), (0.19, 1.0), (0.75, 1.0), (0.83, 0.35)])
                    eye.scaleBy(x: 1, y: arcScale)
                    var arc = Path()
                    arc.move(to: pt(-4.4, 1.2))
                    arc.addQuadCurve(to: pt(4.4, 1.2), control: pt(0, -4.6))
                    eye.stroke(arc, with: .color(ink), style: StrokeStyle(lineWidth: 2.35, lineCap: .round))
                } else {
                    let openScale = u < 0.10
                        ? sample(u, [(0, 1.0), (0.09, 0.12)])
                        : sample(u, [(0.84, 0.12), (0.94, 1.0), (1.0, 1.0)])
                    eye.scaleBy(x: 1, y: openScale)
                    eye.fill(Path(ellipseIn: CGRect(x: -4.1, y: -4.7, width: 8.2, height: 9.4)), with: .color(ink))
                    eye.fill(Path(ellipseIn: CGRect(x: 0.05, y: -2.85, width: 2.5, height: 2.5)), with: .color(.white))
                }
            }
        } else {
            let phase = (t / 4.8).truncatingRemainder(dividingBy: 1)
            let open = time != nil && abs(phase - 0.47) < 0.02 ? 0.1 + 0.9 * abs(phase - 0.47) / 0.02 : 1
            let look = busy && time != nil ? CGSize(width: -2.5 * cos(2 * .pi * t / 2.4), height: -2.5) : .zero
            for x in [48.5, 71.5] {
                if asleep { // shut: a lid's curve
                    var lid = Path()
                    lid.move(to: pt(x - 4.4, 63.4))
                    lid.addQuadCurve(to: pt(x + 4.4, 63.4), control: pt(x, 68.2))
                    ctx.stroke(lid, with: .color(ink), style: StrokeStyle(lineWidth: 2.1, lineCap: .round))
                    continue
                }
                var eye = ctx
                eye.translateBy(x: x + look.width, y: 64 + look.height)
                eye.scaleBy(x: 1, y: open)
                eye.fill(Path(ellipseIn: CGRect(x: -4.1, y: -4.7, width: 8.2, height: 9.4)), with: .color(ink))
                eye.fill(Path(ellipseIn: CGRect(x: 0.05, y: -2.85, width: 2.5, height: 2.5)), with: .color(.white))
            }
        }
        ctx.stroke(mouth, with: .color(ink), style: StrokeStyle(lineWidth: 2.1, lineCap: .round, lineJoin: .round))

        // Raised waving paw, feathered seamlessly at the shoulder (94, 79) with a soft pink paw pad.
        if let u = wave {
            let alpha = Double(sample(u, [(0, 0), (0.09, 1), (0.85, 1), (0.93, 0), (1, 0)]))
            if alpha > 0.005 {
                let dx = sample(u, [(0, -6.8), (0.21, 0), (0.37, 1.45), (0.53, 0), (0.69, 1.25), (0.84, -2.1), (0.93, -6.8), (1, -6.8)])
                let dy = sample(u, [(0, 12.5), (0.21, 0), (0.37, 0.6), (0.53, 0), (0.69, 0.5), (0.84, 4.2), (0.93, 12.5), (1, 12.5)])
                let rot = sample(u, [(0, 34), (0.21, -9), (0.37, 20), (0.53, -8), (0.69, 18), (0.84, 20), (0.93, 34), (1, 34)]) * .pi / 180
                let sc = sample(u, [(0, 0.72), (0.21, 1), (0.84, 0.92), (0.93, 0.72), (1, 0.72)])

                var arm = ctx
                arm.opacity = alpha
                arm.translateBy(x: 94 + dx, y: 79 + dy)
                arm.rotate(by: .radians(Double(rot)))
                arm.scaleBy(x: sc, y: sc)
                arm.translateBy(x: -94, y: -79)

                arm.drawLayer { sh in
                    sh.addFilter(.blur(radius: 1.3 * k / 2))
                    let shadowOval = Path(ellipseIn: CGRect(x: -4.2, y: -6.8, width: 8.4, height: 13.6))
                        .applying(CGAffineTransform(translationX: 97.5, y: 59.5).rotated(by: 24 * .pi / 180))
                    sh.fill(shadowOval, with: .color(Color(hex: "4b4660").opacity(0.09)))
                }

                arm.drawLayer { layer in
                    layer.fill(wavePaw, with: .linearGradient(pawCoat, startPoint: pt(106, 50), endPoint: pt(91, 83)))
                    layer.drawLayer { pad in
                        pad.addFilter(.blur(radius: 0.3 * k / 2))
                        pad.translateBy(x: 104.8, y: 59.4)
                        pad.rotate(by: .radians(22 * .pi / 180))
                        let padColor = GraphicsContext.Shading.color(blush.opacity(0.82))
                        pad.fill(Path(ellipseIn: CGRect(x: -3.4, y: 2.3 - 2.5, width: 6.8, height: 5.0)), with: padColor)
                        for (tx, ty, tr, rx, ry) in [(-3.3, -2.0, -18.0, 1.3, 1.6), (0.0, -3.2, 0.0, 1.35, 1.7), (3.3, -2.0, 18.0, 1.3, 1.6)] {
                            let bean = Path(ellipseIn: CGRect(x: -rx, y: -ry, width: rx * 2, height: ry * 2))
                                .applying(CGAffineTransform(translationX: tx, y: ty).rotated(by: tr * .pi / 180))
                            pad.fill(bean, with: padColor)
                        }
                    }
                    layer.blendMode = .destinationIn
                    layer.fill(Path(CGRect(x: 70, y: 36, width: 54, height: 56)),
                               with: .linearGradient(pawMask, startPoint: pt(104, 68), endPoint: pt(93, 83.5)))
                }
            }
        }
    }
}
