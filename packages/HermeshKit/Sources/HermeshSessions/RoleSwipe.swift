import Foundation

/// Pure horizontal-swipe physics for the New Session Agent <-> Her role switcher:
/// progress 0 is Agent (left), progress 1 is Her (right).
public enum RoleSwipeMath {
    /// Horizontal distance (pt) between the Agent and Her icons; dragging this far moves progress 0 -> 1 (1:1 tracking).
    public static let spacing: Double = 88

    /// Maximum asymptotic overscroll (in progress units) past 0 or 1.
    public static let overscrollLimit: Double = 0.28

    /// Projection window (s) turning release velocity into flick distance.
    public static let velocityHorizon: Double = 0.16

    /// Hysteresis margin around 0.5 so hovering near the midpoint does not chatter selection haptics.
    public static let hapticDeadband: Double = 0.02

    /// Maps an unclamped progress value into rubber-banded progress, staying exact in [0, 1]
    /// and asymptotically approaching [-limit, 1 + limit] beyond the ends.
    public static func rubberBand(_ raw: Double, limit: Double = overscrollLimit) -> Double {
        guard limit > 0 else { return min(max(raw, 0), 1) }
        if raw < 0 {
            let d = -raw
            return -(d * limit) / (d + limit)
        }
        if raw > 1 {
            let d = raw - 1
            return 1 + (d * limit) / (d + limit)
        }
        return raw
    }

    /// Raw (un-clamped) progress for a horizontal translation (pt; negative drags left toward Her).
    public static func rawProgress(start: Double, translationX: Double, spacing: Double = spacing) -> Double {
        guard spacing > 0 else { return start }
        return start - translationX / spacing
    }

    /// Interactive progress during a drag, with 1:1 tracking in [0, 1] and soft rubber-banding past either end.
    public static func dragProgress(start: Double, translationX: Double, spacing: Double = spacing) -> Double {
        rubberBand(rawProgress(start: start, translationX: translationX, spacing: spacing))
    }

    /// Decides whether a released drag lands on Her (true -> 1) or Agent (false -> 0),
    /// combining dragged distance and release velocity.
    public static func landsOnHer(start: Double, translationX: Double, velocityX: Double, spacing: Double = spacing) -> Bool {
        let raw = rawProgress(start: start, translationX: translationX, spacing: spacing)
        let projected = raw - (velocityX * velocityHorizon) / max(spacing, 1)
        return projected >= 0.5
    }

    /// Normalized initial spring velocity (fraction of remaining distance per second) for SwiftUI's
    /// `interpolatingSpring(duration:bounce:initialVelocity:)`, clamped to avoid wild overshoot on fast flicks.
    public static func initialSpringVelocity(current: Double, target: Double, velocityX: Double, spacing: Double = spacing) -> Double {
        let remaining = target - current
        guard abs(remaining) > 0.05, spacing > 0 else { return 0 }
        let progressVelocity = -velocityX / spacing
        return min(max(progressVelocity / remaining, -4), 6)
    }

    /// Returns whether the live preview detent flipped across the midpoint (with hysteresis) during a drag.
    public static func hapticDetent(progress: Double, currentIsHer: Bool) -> Bool {
        if currentIsHer {
            return progress <= 0.5 - hapticDeadband ? false : true
        }
        return progress >= 0.5 + hapticDeadband ? true : false
    }
}
