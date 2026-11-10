import SwiftUI

/// Her's avatar: her cat, with no ground of its own (the Mac draws the same one, her/Avatar.tsx). A round body
/// with two ears behind it, a lighter face, two eyes, a cat's mouth and a blush, in soft shading with no outline.
/// She breathes and blinks; busy, she looks around and bobs. Still with Reduce Motion.
struct HerAvatar: View {
    var size: CGFloat
    var busy = false
    @Environment(\.accessibilityReduceMotion) private var still

    var body: some View {
        TimelineView(.animation(minimumInterval: 1 / 30, paused: still)) { timeline in
            let t = still ? nil : timeline.date.timeIntervalSinceReferenceDate
            Canvas { ctx, _ in HerCat.draw(&ctx, size: size, time: t, busy: busy) }
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }
}

/// The cat, on a 120 grid of which the box 8 16 104 104 shows.
enum HerCat {
    private static func pt(_ x: CGFloat, _ y: CGFloat) -> CGPoint { CGPoint(x: x, y: y) }

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

    private static let coat = Gradient(stops: [.init(color: Color(hex: "f1f3ff"), location: 0), .init(color: Color(hex: "d7ddff"), location: 0.55), .init(color: Color(hex: "b3bcf2"), location: 1)])
    private static let earColor = Color(hex: "cbd3fb")
    private static let blush = Color(hex: "ffa3be")
    private static let ink = Color(hex: "3c2f3a")

    /// Draws her in a square of `size`; `time` nil holds her still.
    static func draw(_ ctx: inout GraphicsContext, size: CGFloat, time: Double?, busy: Bool) {
        let k = size / 104
        ctx.scaleBy(x: k, y: k)
        ctx.translateBy(x: -8, y: -16)
        let fill = GraphicsContext.Shading.radialGradient(coat, center: pt(44, 50), startRadius: 0, endRadius: 72)
        // Blurred in a layer of its own, by the Mac's standard deviation: half of it as a radius here, in points.
        func soft(_ deviation: CGFloat, _ draw: (inout GraphicsContext) -> Void) {
            ctx.drawLayer { layer in
                layer.addFilter(.blur(radius: deviation * k / 2))
                draw(&layer)
            }
        }
        soft(1.1) { $0.fill(Path(ellipseIn: CGRect(x: 26, y: 107.4, width: 68, height: 7.2)), with: .color(Color(hex: "4b4660").opacity(0.12))) }

        // She breathes from the ground up; busy, a quicker bob.
        let t = time ?? 0
        let breath = time == nil ? 0 : (1 - cos(2 * .pi * t / (busy ? 0.8 : 3.4))) / 2
        ctx.translateBy(x: 60, y: 110)
        ctx.scaleBy(x: 1 + (busy ? 0.03 : 0.02) * breath, y: 1 - (busy ? 0.04 : 0.025) * breath)
        ctx.translateBy(x: -60, y: -110)

        for side in [CGAffineTransform.identity, mirror] {
            let e = ear.applying(side)
            ctx.fill(e, with: .color(earColor))
            ctx.stroke(e, with: .color(earColor), style: StrokeStyle(lineWidth: 8, lineJoin: .round))
            soft(0.8) {
                let inside = earInside.applying(side)
                $0.fill(inside, with: .color(blush.opacity(0.55)))
                $0.stroke(inside, with: .color(blush.opacity(0.55)), style: StrokeStyle(lineWidth: 3, lineJoin: .round))
            }
            ctx.fill(Path(ellipseIn: CGRect(x: 8.5, y: 78.5, width: 15, height: 15)).applying(side), with: fill)
        }
        ctx.fill(body, with: fill)
        soft(1.1) { $0.fill(Path(ellipseIn: CGRect(x: 30, y: 43.4, width: 14, height: 7.2)).applying(CGAffineTransform(translationX: 37, y: 47).rotated(by: -.pi / 6).translatedBy(x: -37, y: -47)), with: .color(.white.opacity(0.5))) }
        soft(0.8) { $0.fill(face, with: .color(Color(hex: "fafbff"))) }
        soft(2.4) {
            for x in [37.0, 83.0] { $0.fill(Path(ellipseIn: CGRect(x: x - 6.5, y: 68.6, width: 13, height: 8.8)), with: .color(blush.opacity(0.6))) }
        }

        // The eyes: a blink every 4.8 s; busy, they look from side to side, a little up.
        let phase = (t / 4.8).truncatingRemainder(dividingBy: 1)
        let open = time != nil && abs(phase - 0.47) < 0.02 ? 0.1 + 0.9 * abs(phase - 0.47) / 0.02 : 1
        let look = busy && time != nil ? CGSize(width: -2.5 * cos(2 * .pi * t / 2.4), height: -2.5) : .zero
        for x in [48.5, 71.5] {
            var eye = ctx
            eye.translateBy(x: x + look.width, y: 64 + look.height)
            eye.scaleBy(x: 1, y: open)
            eye.fill(Path(ellipseIn: CGRect(x: -4.1, y: -4.7, width: 8.2, height: 9.4)), with: .color(ink))
            eye.fill(Path(ellipseIn: CGRect(x: 0.05, y: -2.85, width: 2.5, height: 2.5)), with: .color(.white))
        }
        ctx.stroke(mouth, with: .color(ink), style: StrokeStyle(lineWidth: 2.1, lineCap: .round, lineJoin: .round))
    }
}
