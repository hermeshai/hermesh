import HermeshProto
import UIKit

/// How touches operate the machine (docs/computer-control.md §6.1).
enum ScreenMode: String {
    /// A tap clicks where it lands; one finger moves the picture.
    case direct
    /// One finger moves a pointer drawn here; a tap clicks where it is.
    case trackpad
}

/// The machine's picture and the owner's fingers on it. Zooming and moving
/// the picture happen here, with no round trip; what the machine should do
/// goes out through `send`.
///
/// | Gesture                        | Direct touch             | Trackpad                |
/// |--------------------------------|--------------------------|-------------------------|
/// | tap                            | click where touched      | click at the pointer    |
/// | double tap                     | double click             | double click            |
/// | long press, or two-finger tap  | right click              | right click             |
/// | long press, then drag          | drag                     | drag                    |
/// | one-finger drag                | move the picture         | move the pointer        |
/// | two-finger drag                | scroll                   | scroll                  |
/// | pinch                          | zoom                     | zoom                    |
final class ScreenSurfaceView: UIView {
    let picture = ScreenPictureView()
    let keys = ScreenKeyField()
    var send: ((Hermesh_Screen_V1_Input) -> Void)?

    /// The picture's size in its own pixels.
    var contentSize = CGSize(width: 1920, height: 1200) {
        didSet {
            guard contentSize != oldValue else { return }
            fit = true
            loc = CGPoint(x: contentSize.width / 2, y: contentSize.height / 2)
            setNeedsLayout()
        }
    }

    var mode = ScreenMode.direct {
        didSet {
            guard mode != oldValue else { return }
            pointer.isHidden = mode != .trackpad
            if mode == .trackpad { follow() }
            place()
        }
    }

    // Where the picture sits: scale, and the offset of its top left corner.
    private var s: CGFloat = 1, tx: CGFloat = 0, ty: CGFloat = 0
    private var fit = true
    private var minScale: CGFloat = 1
    private var maxScale: CGFloat { max(minScale * 6, 1.5) }
    /// The trackpad's pointer, in the picture's pixels.
    private var loc = CGPoint(x: 960, y: 600)

    private let pointer = CAShapeLayer()
    private let ring = CAShapeLayer()
    private let haptic = UIImpactFeedbackGenerator(style: .medium)

    override init(frame: CGRect) {
        super.init(frame: frame)
        backgroundColor = .black
        isMultipleTouchEnabled = true
        clipsToBounds = true
        addSubview(picture)
        addSubview(keys)
        // An arrow, its tip at the layer's origin.
        let arrow = UIBezierPath()
        for (i, p) in [(0, 0), (0, 16), (4.2, 12.2), (7, 18.6), (9.6, 17.5), (6.8, 11.2), (12.5, 11.2)].enumerated() {
            let pt = CGPoint(x: p.0, y: p.1)
            if i == 0 { arrow.move(to: pt) } else { arrow.addLine(to: pt) }
        }
        arrow.close()
        pointer.path = arrow.cgPath
        pointer.fillColor = UIColor.black.cgColor
        pointer.strokeColor = UIColor.white.cgColor
        pointer.lineWidth = 1.2
        pointer.lineJoin = .round
        pointer.shadowOpacity = 0.4
        pointer.shadowRadius = 1.5
        pointer.shadowOffset = CGSize(width: 0, height: 1)
        pointer.isHidden = true
        layer.addSublayer(pointer)
        ring.path = UIBezierPath(ovalIn: CGRect(x: -32, y: -32, width: 64, height: 64)).cgPath
        ring.fillColor = UIColor.systemBlue.withAlphaComponent(0.22).cgColor
        ring.strokeColor = UIColor.systemBlue.withAlphaComponent(0.9).cgColor
        ring.lineWidth = 2
        ring.isHidden = true
        layer.addSublayer(ring)
        isAccessibilityElement = true
        accessibilityIdentifier = "screen-surface"
    }

    required init?(coder: NSCoder) { fatalError() }

    // MARK: The view

    /// While the picture shows, an iPhone whose screen can refresh 120 times a
    /// second does (the app's Info.plist allows it): a frame then waits half
    /// as long for the next refresh as at 60. The link does nothing else.
    private var fullRate: CADisplayLink?

    override func didMoveToWindow() {
        super.didMoveToWindow()
        fullRate?.invalidate()
        fullRate = nil
        guard window != nil else { return }
        let link = CADisplayLink(target: self, selector: #selector(refreshed))
        link.preferredFrameRateRange = CAFrameRateRange(minimum: 80, maximum: 120, preferred: 120)
        link.add(to: .main, forMode: .common)
        fullRate = link
    }

    @objc private func refreshed() {}

    override func layoutSubviews() {
        super.layoutSubviews()
        minScale = min(bounds.width / contentSize.width, bounds.height / contentSize.height)
        if fit { s = minScale }
        clampView()
        if mode == .trackpad { follow() }
        place()
    }

    private func clampView() {
        s = min(maxScale, max(minScale, s))
        fit = s <= minScale * 1.001
        let cw = contentSize.width * s, ch = contentSize.height * s
        tx = cw <= bounds.width ? (bounds.width - cw) / 2 : min(0, max(bounds.width - cw, tx))
        ty = ch <= bounds.height ? (bounds.height - ch) / 2 : min(0, max(bounds.height - ch, ty))
    }

    private func place() {
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        picture.frame = CGRect(x: tx, y: ty, width: contentSize.width * s, height: contentSize.height * s)
        pointer.position = onScreen(loc)
        CATransaction.commit()
    }

    private func zoom(at p: CGPoint, by f: CGFloat) {
        let s2 = min(maxScale, max(minScale, s * f)), k = s2 / s
        tx = p.x - (p.x - tx) * k
        ty = p.y - (p.y - ty) * k
        s = s2
        clampView()
    }

    /// Keeps the trackpad's pointer on screen.
    private func follow() {
        let m: CGFloat = 36, p = onScreen(loc)
        if p.x < m { tx += m - p.x } else if p.x > bounds.width - m { tx -= p.x - (bounds.width - m) }
        if p.y < m { ty += m - p.y } else if p.y > bounds.height - m { ty -= p.y - (bounds.height - m) }
        clampView()
    }

    private func onScreen(_ c: CGPoint) -> CGPoint { CGPoint(x: tx + c.x * s, y: ty + c.y * s) }

    private func inPicture(_ p: CGPoint) -> CGPoint {
        CGPoint(x: min(contentSize.width - 1, max(0, (p.x - tx) / s)), y: min(contentSize.height - 1, max(0, (p.y - ty) / s)))
    }

    /// Where on the machine a gesture at p lands: under the finger, or at the pointer.
    private func target(_ p: CGPoint) -> CGPoint { mode == .trackpad ? loc : inPicture(p) }

    // MARK: What is sent

    private func button(_ c: CGPoint, right: Bool = false, down: Bool, clicks: UInt32 = 1) {
        var b = Hermesh_Screen_V1_Button()
        b.x = c.x / contentSize.width
        b.y = c.y / contentSize.height
        b.button = right ? .right : .left
        b.down = down
        b.clicks = clicks
        var input = Hermesh_Screen_V1_Input()
        input.button = b
        send?(input)
    }

    private func click(_ c: CGPoint, right: Bool = false, clicks: UInt32 = 1) {
        button(c, right: right, down: true, clicks: clicks)
        button(c, right: right, down: false, clicks: clicks)
    }

    private func move(_ c: CGPoint) {
        var m = Hermesh_Screen_V1_Move()
        m.x = c.x / contentSize.width
        m.y = c.y / contentSize.height
        var input = Hermesh_Screen_V1_Input()
        input.move = m
        send?(input)
    }

    private func wheel(_ c: CGPoint, dx: CGFloat, dy: CGFloat) {
        var w = Hermesh_Screen_V1_Wheel()
        w.x = c.x / contentSize.width
        w.y = c.y / contentSize.height
        w.dx = dx
        w.dy = dy
        var input = Hermesh_Screen_V1_Input()
        input.wheel = w
        send?(input)
    }

    // MARK: Feedback drawn here

    private func ripple(_ p: CGPoint) {
        let r = CAShapeLayer()
        r.path = UIBezierPath(ovalIn: CGRect(x: -22, y: -22, width: 44, height: 44)).cgPath
        r.fillColor = UIColor.clear.cgColor
        r.strokeColor = UIColor.white.withAlphaComponent(0.9).cgColor
        r.lineWidth = 2
        r.position = p
        r.opacity = 0
        layer.addSublayer(r)
        let grow = CABasicAnimation(keyPath: "transform.scale")
        grow.fromValue = 0.3
        grow.toValue = 1
        let fade = CABasicAnimation(keyPath: "opacity")
        fade.fromValue = 1
        fade.toValue = 0
        let group = CAAnimationGroup()
        group.animations = [grow, fade]
        group.duration = 0.36
        group.timingFunction = CAMediaTimingFunction(name: .easeOut)
        CATransaction.begin()
        CATransaction.setCompletionBlock { r.removeFromSuperlayer() }
        r.add(group, forKey: nil)
        CATransaction.commit()
    }

    private func showRing(_ p: CGPoint?) {
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        ring.isHidden = p == nil
        if let p { ring.position = p }
        CATransaction.commit()
    }

    // MARK: Gestures

    private enum Gesture {
        case none
        case one(One)
        case two(Two)
        /// A two-finger gesture ended with a finger still down: nothing more until all are up.
        case done
    }

    private final class One {
        var start = CGPoint.zero, last = CGPoint.zero
        var began: TimeInterval = 0, lastTime: TimeInterval = 0
        var moved = false, long = false, drag = false
        var timer: DispatchWorkItem?
    }

    private final class Two {
        enum Kind { case undecided, pinch, scroll }
        var kind = Kind.undecided
        var center0 = CGPoint.zero, distance0: CGFloat = 0
        var center = CGPoint.zero, distance: CGFloat = 0
        var began: TimeInterval = 0
    }

    private var touches: [UITouch] = []
    private var gesture = Gesture.none
    private var lastTap: (time: TimeInterval, at: CGPoint) = (0, .zero)
    private let slop: CGFloat = 10
    private let longPress: TimeInterval = 0.42

    private func centerAndDistance() -> (CGPoint, CGFloat) {
        let a = touches[0].location(in: self), b = touches[1].location(in: self)
        return (CGPoint(x: (a.x + b.x) / 2, y: (a.y + b.y) / 2), hypot(a.x - b.x, a.y - b.y))
    }

    override func touchesBegan(_ new: Set<UITouch>, with event: UIEvent?) {
        for t in new where !touches.contains(t) { touches.append(t) }
        let now = event?.timestamp ?? ProcessInfo.processInfo.systemUptime
        if touches.count == 1 {
            let g = One()
            g.start = touches[0].location(in: self)
            g.last = g.start
            g.began = now
            g.lastTime = now
            let work = DispatchWorkItem { [weak self, weak g] in
                guard let self, let g, case .one(let cur) = self.gesture, cur === g, !g.moved else { return }
                g.long = true
                self.haptic.impactOccurred()
                self.showRing(self.mode == .trackpad ? self.onScreen(self.loc) : g.start)
            }
            g.timer = work
            DispatchQueue.main.asyncAfter(deadline: .now() + longPress, execute: work)
            gesture = .one(g)
        } else if touches.count == 2 {
            if case .one(let g) = gesture {
                g.timer?.cancel()
                showRing(nil)
                if g.drag { button(target(g.last), down: false) }
            }
            let g = Two()
            (g.center0, g.distance0) = centerAndDistance()
            g.center = g.center0
            g.distance = g.distance0
            g.began = now
            gesture = .two(g)
        }
    }

    override func touchesMoved(_ moved: Set<UITouch>, with event: UIEvent?) {
        let now = event?.timestamp ?? ProcessInfo.processInfo.systemUptime
        switch gesture {
        case .one(let g):
            guard let t = touches.first else { return }
            let p = t.location(in: self)
            if !g.moved {
                if hypot(p.x - g.start.x, p.y - g.start.y) < slop { return }
                g.moved = true
                if !g.long { g.timer?.cancel() }
            }
            let dx = p.x - g.last.x, dy = p.y - g.last.y, dt = max(0.001, now - g.lastTime)
            g.last = p
            g.lastTime = now
            if g.long && !g.drag {
                g.drag = true
                button(target(g.start), down: true)
            }
            if mode == .direct {
                if g.drag {
                    move(inPicture(p))
                    showRing(p)
                } else {
                    tx += dx
                    ty += dy
                    clampView()
                    place()
                }
            } else {
                // Faster fingers move the pointer further.
                let gain = 1 + min(2, hypot(dx, dy) / CGFloat(dt * 1000) * 1.4)
                loc.x = min(contentSize.width - 1, max(0, loc.x + dx * gain / s))
                loc.y = min(contentSize.height - 1, max(0, loc.y + dy * gain / s))
                follow()
                place()
                move(loc)
                if g.drag { showRing(onScreen(loc)) }
            }
        case .two(let g):
            guard touches.count == 2 else { return }
            let (c, d) = centerAndDistance()
            if g.kind == .undecided {
                if abs(d - g.distance0) > 16 { g.kind = .pinch } else if hypot(c.x - g.center0.x, c.y - g.center0.y) > 10 { g.kind = .scroll }
            }
            switch g.kind {
            case .pinch:
                zoom(at: c, by: d / max(1, g.distance))
                tx += c.x - g.center.x
                ty += c.y - g.center.y
                clampView()
                place()
            case .scroll:
                // The content follows the fingers.
                wheel(target(g.center0), dx: -(c.x - g.center.x) / s, dy: -(c.y - g.center.y) / s)
            case .undecided:
                break
            }
            g.center = c
            g.distance = d
        default:
            break
        }
    }

    override func touchesEnded(_ ended: Set<UITouch>, with event: UIEvent?) { lift(ended, cancelled: false, event: event) }
    override func touchesCancelled(_ ended: Set<UITouch>, with event: UIEvent?) { lift(ended, cancelled: true, event: event) }

    private func lift(_ ended: Set<UITouch>, cancelled: Bool, event: UIEvent?) {
        let now = event?.timestamp ?? ProcessInfo.processInfo.systemUptime
        touches.removeAll { ended.contains($0) }
        switch gesture {
        case .one(let g):
            g.timer?.cancel()
            showRing(nil)
            if g.drag {
                button(target(g.last), down: false)
            } else if g.long && !g.moved {
                if !cancelled { click(target(g.start), right: true) }
            } else if !g.moved && !cancelled {
                let double = now - lastTap.time < 0.32 && hypot(g.start.x - lastTap.at.x, g.start.y - lastTap.at.y) < 28
                lastTap = double ? (0, .zero) : (now, g.start)
                ripple(mode == .trackpad ? onScreen(loc) : g.start)
                click(target(g.start), clicks: double ? 2 : 1)
            }
            gesture = .none
        case .two(let g):
            if g.kind == .undecided && now - g.began < 0.35 && !cancelled {
                ripple(mode == .trackpad ? onScreen(loc) : g.center0)
                click(target(g.center0), right: true)
            }
            gesture = .done
        default:
            break
        }
        if touches.isEmpty { gesture = .none }
    }
}
