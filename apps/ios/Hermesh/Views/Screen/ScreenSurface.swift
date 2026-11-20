import HermeshKit
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
    var onViewChanged: ((Bool) -> Void)?
    var onStateUpdated: (() -> Void)?
    var panning = false

    /// The picture's size in its own pixels.
    var contentSize: CGSize {
        get { viewport.contentSize }
        set {
            guard newValue != viewport.contentSize else { return }
            viewport.setContentSize(newValue)
            loc = CGPoint(x: newValue.width / 2, y: newValue.height / 2)
            syncTransformed()
            setNeedsLayout()
        }
    }

    var mode = ScreenMode.direct {
        didSet {
            guard mode != oldValue else { return }
            if mode == .direct { panning = false }
            pointer.isHidden = mode != .trackpad
            if mode == .trackpad {
                let safe = currentSafeRect()
                if !safe.contains(viewport.onScreen(loc)) {
                    loc = viewport.visiblePointerTarget()
                }
            }
            place()
            onStateUpdated?()
        }
    }

    private(set) var viewport = ScreenViewport()
    private var transformed = false
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
        startDisplayLink()
    }

    private func startDisplayLink() {
        guard fullRate == nil, window != nil else { return }
        let link = CADisplayLink(target: self, selector: #selector(refreshed))
        link.preferredFrameRateRange = CAFrameRateRange(minimum: 80, maximum: 120, preferred: 120)
        link.add(to: .main, forMode: .common)
        fullRate = link
    }

    /// Re-arms frame presentation and display refresh before connecting.
    func prepare() {
        picture.prepare()
        startDisplayLink()
    }

    /// Stops frame decoding and 120Hz link immediately when dismissing.
    func stop() {
        fullRate?.invalidate()
        fullRate = nil
        if case .one(let g) = gesture { g.timer?.cancel() }
        showRing(nil)
        touches.removeAll()
        gesture = .none
        picture.stop()
    }

    @objc private func refreshed() {}

    override func safeAreaInsetsDidChange() {
        super.safeAreaInsetsDidChange()
        setNeedsLayout()
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        viewport.layout(bounds: bounds.size, safeRect: currentSafeRect())
        syncTransformed()
        place()
        onStateUpdated?()
    }

    /// Restores the picture to its default fitted placement inside the safe area.
    func resetView() {
        viewport.resetFit()
        panning = false
        syncTransformed()
        place()
        onStateUpdated?()
    }

    private func currentSafeRect() -> CGRect {
        guard bounds.width > 0, bounds.height > 0 else { return .zero }
        guard let window else {
            let r = bounds.inset(by: safeAreaInsets)
            return r.width > 0 && r.height > 0 ? r : bounds
        }
        let windowSafe = convert(window.bounds.inset(by: window.safeAreaInsets), from: window)
        let r = bounds.intersection(windowSafe)
        return !r.isNull && r.width > 0 && r.height > 0 ? r : bounds
    }

    private func syncTransformed() {
        let nowTransformed = viewport.isTransformed
        if nowTransformed != transformed {
            transformed = nowTransformed
            onViewChanged?(nowTransformed)
        }
    }

    private func place() {
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        picture.frame = viewport.pictureFrame
        pointer.position = viewport.onScreen(loc)
        CATransaction.commit()
    }

    /// Where on the machine an initiated gesture at `p` lands, or nil when it should not fire.
    /// Direct touch strictly requires hitting inside the picture; trackpad mode requires not panning
    /// and the virtual pointer being visible on screen.
    private func actionTarget(at p: CGPoint) -> CGPoint? {
        if mode == .trackpad {
            guard !panning, bounds.contains(viewport.onScreen(loc)) else { return nil }
            return loc
        }
        return viewport.picturePoint(at: p)
    }

    /// Fallback coordinate for finishing or continuing a remote drag that already started inside the picture.
    private func dragTarget(at p: CGPoint) -> CGPoint {
        mode == .trackpad ? loc : viewport.clampedPicturePoint(at: p)
    }

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
        case pan(CGPoint)
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

    private func averageCenter() -> CGPoint {
        var x: CGFloat = 0, y: CGFloat = 0
        for t in touches {
            let p = t.location(in: self)
            x += p.x; y += p.y
        }
        let n = CGFloat(max(1, touches.count))
        return CGPoint(x: x / n, y: y / n)
    }

    private func cancelOne(_ g: One) {
        g.timer?.cancel()
        showRing(nil)
        if g.drag { button(dragTarget(at: g.last), down: false) }
    }

    private func pruneTouches() {
        let hadOne: One? = if case .one(let g) = gesture { g } else { nil }
        touches.removeAll { $0.phase == .ended || $0.phase == .cancelled }
        if touches.isEmpty {
            if let g = hadOne { cancelOne(g) }
            gesture = .none
        }
    }

    override func touchesBegan(_ new: Set<UITouch>, with event: UIEvent?) {
        pruneTouches()
        for t in new where !touches.contains(t) { touches.append(t) }
        let now = event?.timestamp ?? ProcessInfo.processInfo.systemUptime
        if touches.count == 1 {
            let g = One()
            g.start = touches[0].location(in: self)
            g.last = g.start
            g.began = now
            g.lastTime = now
            if actionTarget(at: g.start) != nil {
                let work = DispatchWorkItem { [weak self, weak g] in
                    guard let self, let g, case .one(let cur) = self.gesture, cur === g, !g.moved else { return }
                    guard self.actionTarget(at: g.start) != nil else { return }
                    g.long = true
                    self.haptic.impactOccurred()
                    self.showRing(self.mode == .trackpad ? self.viewport.onScreen(self.loc) : g.start)
                }
                g.timer = work
                DispatchQueue.main.asyncAfter(deadline: .now() + longPress, execute: work)
            }
            gesture = .one(g)
        } else if touches.count == 2 {
            if case .one(let g) = gesture { cancelOne(g) }
            let g = Two()
            (g.center0, g.distance0) = centerAndDistance()
            g.center = g.center0
            g.distance = g.distance0
            g.began = now
            gesture = .two(g)
        } else if touches.count >= 3 {
            if case .one(let g) = gesture { cancelOne(g) }
            gesture = .pan(averageCenter())
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
                // If the user panned away so the pointer is off-screen, bring it into the visible area instead of snapping back.
                if mode == .trackpad && !panning && !currentSafeRect().contains(viewport.onScreen(loc)) {
                    loc = viewport.visiblePointerTarget()
                }
            }
            let dx = p.x - g.last.x, dy = p.y - g.last.y, dt = max(0.001, now - g.lastTime)
            g.last = p
            g.lastTime = now
            if g.long && !g.drag {
                if let target = actionTarget(at: g.start) {
                    g.drag = true
                    button(target, down: true)
                }
            }
            if mode == .direct || (panning && !g.drag) {
                if g.drag {
                    move(viewport.clampedPicturePoint(at: p))
                    showRing(p)
                } else {
                    viewport.pan(dx: dx, dy: dy)
                    syncTransformed()
                    place()
                    onStateUpdated?()
                }
            } else {
                // Faster fingers move the pointer further.
                let gain = 1 + min(2, hypot(dx, dy) / CGFloat(dt * 1000) * 1.4)
                let scale = max(viewport.scale, 0.0001)
                loc.x = min(contentSize.width - 1, max(0, loc.x + dx * gain / scale))
                loc.y = min(contentSize.height - 1, max(0, loc.y + dy * gain / scale))
                viewport.follow(pointer: loc)
                syncTransformed()
                place()
                onStateUpdated?()
                move(loc)
                if g.drag { showRing(viewport.onScreen(loc)) }
            }
        case .two(let g):
            guard touches.count == 2 else { return }
            let (c, d) = centerAndDistance()
            if g.kind == .undecided {
                if abs(d - g.distance0) > 16 { g.kind = .pinch } else if hypot(c.x - g.center0.x, c.y - g.center0.y) > 10 { g.kind = .scroll }
            }
            switch g.kind {
            case .pinch:
                viewport.pinch(from: g.center, to: c, factor: d / max(1, g.distance))
                syncTransformed()
                place()
                onStateUpdated?()
            case .scroll:
                // The content follows the fingers.
                if let target = actionTarget(at: g.center0) {
                    let scale = max(viewport.scale, 0.0001)
                    wheel(target, dx: -(c.x - g.center.x) / scale, dy: -(c.y - g.center.y) / scale)
                }
            case .undecided:
                break
            }
            g.center = c
            g.distance = d
        case .pan(let prev):
            let c = averageCenter()
            viewport.pan(dx: c.x - prev.x, dy: c.y - prev.y)
            syncTransformed()
            place()
            onStateUpdated?()
            gesture = .pan(c)
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
                button(dragTarget(at: g.last), down: false)
            } else if g.long && !g.moved {
                if !cancelled, let target = actionTarget(at: g.start) { click(target, right: true) }
            } else if !g.moved && !cancelled, let target = actionTarget(at: g.start) {
                let double = now - lastTap.time < 0.32 && hypot(g.start.x - lastTap.at.x, g.start.y - lastTap.at.y) < 28
                lastTap = double ? (0, .zero) : (now, g.start)
                ripple(mode == .trackpad ? viewport.onScreen(loc) : g.start)
                click(target, clicks: double ? 2 : 1)
            }
            gesture = .none
        case .two(let g):
            if g.kind == .undecided && now - g.began < 0.35 && !cancelled {
                if let target = actionTarget(at: g.center0) {
                    ripple(mode == .trackpad ? viewport.onScreen(loc) : g.center0)
                    click(target, right: true)
                }
                gesture = .done
            } else if g.kind == .pinch && (mode == .direct || panning) && touches.count == 1 && !cancelled {
                // Lifting one finger after a pinch keeps one-finger panning continuous.
                let next = One()
                let p = touches[0].location(in: self)
                next.start = p
                next.last = p
                next.began = now
                next.lastTime = now
                next.moved = true
                gesture = .one(next)
            } else {
                gesture = .done
            }
        case .pan:
            gesture = touches.count >= 3 ? .pan(averageCenter()) : .done
        default:
            break
        }
        if touches.isEmpty { gesture = .none }
    }
}
