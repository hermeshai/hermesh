import CoreGraphics
import Foundation

/// Geometry and viewport state for the remote desktop surface: fit placement inside
/// the unobstructed safe area, overscroll panning without losing the picture,
/// finger-anchored pinch zooming, strict blank-area hit testing, and rotation/keyboard continuity.
public struct ScreenViewport: Equatable, Sendable {
    /// The remote picture's size in its own pixels.
    public var contentSize: CGSize
    /// Full surface bounds size (edge-to-edge).
    public private(set) var bounds: CGSize = .zero
    /// Unobstructed safe rectangle inside `bounds` (excluding hardware insets and keyboard).
    public private(set) var safeRect: CGRect = .zero
    /// Current picture scale (points per remote pixel).
    public private(set) var scale: CGFloat = 1
    /// Picture top-left origin in surface coordinates.
    public private(set) var tx: CGFloat = 0
    public private(set) var ty: CGFloat = 0
    /// Scale that fits `contentSize` inside `safeRect`.
    public private(set) var minScale: CGFloat = 1
    /// True while the picture is in its default fitted placement.
    public private(set) var fitted: Bool = true

    public init(contentSize: CGSize = CGSize(width: 1920, height: 1200)) {
        self.contentSize = contentSize
    }

    public var maxScale: CGFloat { max(minScale * 6, 1.5) }
    public var isTransformed: Bool { !fitted }

    public var pictureFrame: CGRect {
        CGRect(x: tx, y: ty, width: contentSize.width * scale, height: contentSize.height * scale)
    }

    /// Updates `contentSize` and resets the viewport to fit.
    public mutating func setContentSize(_ newSize: CGSize) {
        guard newSize != contentSize, newSize.width > 0, newSize.height > 0 else { return }
        contentSize = newSize
        fitted = true
        if bounds.width > 0, bounds.height > 0 {
            layout(bounds: bounds, safeRect: safeRect)
        }
    }

    /// Recomputes layout for updated surface bounds and safe area (initial layout, rotation, keyboard).
    public mutating func layout(bounds newBounds: CGSize, safeRect newSafe: CGRect) {
        guard newBounds.width > 0, newBounds.height > 0, contentSize.width > 0, contentSize.height > 0 else { return }
        let effective = (newSafe.width > 0 && newSafe.height > 0) ? newSafe : CGRect(origin: .zero, size: newBounds)
        guard let fit = fitPlacement(bounds: newBounds, safeRect: effective) else { return }

        let oldSafe = safeRect
        let oldScale = scale
        bounds = newBounds
        safeRect = effective
        minScale = fit.minScale

        if fitted {
            scale = fit.minScale
            tx = fit.tx
            ty = fit.ty
            return
        }

        // Preserve the remote picture point sitting at the center of the safe area across
        // orientation changes and keyboard appearance/dismissal.
        if oldSafe.width > 0, oldSafe.height > 0, oldSafe != effective, oldScale > 0 {
            let focusX = (oldSafe.midX - tx) / oldScale
            let focusY = (oldSafe.midY - ty) / oldScale
            scale = min(maxScale, max(minScale, scale))
            tx = effective.midX - focusX * scale
            ty = effective.midY - focusY * scale
        }
        clamp()
    }

    /// Restores the picture to its default fitted scale and placement inside `safeRect`.
    public mutating func resetFit() {
        fitted = true
        guard let fit = fitPlacement(bounds: bounds, safeRect: safeRect) else { return }
        minScale = fit.minScale
        scale = fit.minScale
        tx = fit.tx
        ty = fit.ty
    }

    /// Pans the canvas by `(dx, dy)` in surface points, allowing overscroll up to the safe center.
    public mutating func pan(dx: CGFloat, dy: CGFloat) {
        guard safeRect.width > 0, safeRect.height > 0 else { return }
        tx += dx
        ty += dy
        fitted = false
        clamp()
    }

    /// Zooms by `factor` while carrying the content point under `oldCenter` to `newCenter` in one step.
    public mutating func pinch(from oldCenter: CGPoint, to newCenter: CGPoint, factor: CGFloat) {
        guard safeRect.width > 0, safeRect.height > 0, scale > 0, factor.isFinite, factor > 0 else { return }
        let s2 = min(maxScale, max(minScale, scale * factor))
        let k = s2 / scale
        tx = newCenter.x - (oldCenter.x - tx) * k
        ty = newCenter.y - (oldCenter.y - ty) * k
        scale = s2
        fitted = false
        clamp()
    }

    /// Clamps scale into `[minScale, maxScale]` and translation so `safeRect` center always
    /// stays inside `pictureFrame`. Every remote edge can be dragged to the center of the screen
    /// without snapping back and without ever losing the picture off-screen.
    public mutating func clamp() {
        guard safeRect.width > 0, safeRect.height > 0, contentSize.width > 0, contentSize.height > 0 else { return }
        scale = min(maxScale, max(minScale, scale))
        let cw = contentSize.width * scale
        let ch = contentSize.height * scale
        let cx = safeRect.midX
        let cy = safeRect.midY
        tx = min(cx, max(cx - cw, tx))
        ty = min(cy, max(cy - ch, ty))
    }

    /// Projects a remote pixel coordinate to surface points.
    public func onScreen(_ contentPoint: CGPoint) -> CGPoint {
        CGPoint(x: tx + contentPoint.x * scale, y: ty + contentPoint.y * scale)
    }

    /// Strict hit-test: returns the remote pixel coordinate under `screenPoint`, or `nil` when
    /// `screenPoint` falls in the blank/overscroll area outside the picture.
    public func picturePoint(at screenPoint: CGPoint) -> CGPoint? {
        guard scale > 0, contentSize.width > 0, contentSize.height > 0 else { return nil }
        let x = (screenPoint.x - tx) / scale
        let y = (screenPoint.y - ty) / scale
        guard x >= 0, x <= contentSize.width, y >= 0, y <= contentSize.height else { return nil }
        return CGPoint(
            x: min(contentSize.width - 1, max(0, x)),
            y: min(contentSize.height - 1, max(0, y))
        )
    }

    /// Clamped inverse projection, used only for an in-progress remote drag that started inside
    /// the picture or for placing the trackpad pointer on screen.
    public func clampedPicturePoint(at screenPoint: CGPoint) -> CGPoint {
        guard scale > 0, contentSize.width > 0, contentSize.height > 0 else { return .zero }
        return CGPoint(
            x: min(contentSize.width - 1, max(0, (screenPoint.x - tx) / scale)),
            y: min(contentSize.height - 1, max(0, (screenPoint.y - ty) / scale))
        )
    }

    /// A guaranteed-visible remote pixel coordinate at the center of `safeRect`.
    public func visiblePointerTarget() -> CGPoint {
        clampedPicturePoint(at: CGPoint(x: safeRect.midX, y: safeRect.midY))
    }

    /// Pans only along axes where the picture currently extends past `safeRect` and the trackpad
    /// pointer approaches that edge. Never shifts a fitted picture or fights user overscroll.
    public mutating func follow(pointer loc: CGPoint, margin: CGFloat = 36) {
        guard safeRect.width > 0, safeRect.height > 0 else { return }
        let mx = min(margin, safeRect.width / 3)
        let my = min(margin, safeRect.height / 3)
        let p = onScreen(loc)
        let cw = contentSize.width * scale
        let ch = contentSize.height * scale
        var moved = false

        if p.x < safeRect.minX + mx, tx < safeRect.minX {
            tx = min(safeRect.minX, tx + (safeRect.minX + mx - p.x))
            moved = true
        } else if p.x > safeRect.maxX - mx, tx + cw > safeRect.maxX {
            tx = max(safeRect.maxX - cw, tx - (p.x - (safeRect.maxX - mx)))
            moved = true
        }

        if p.y < safeRect.minY + my, ty < safeRect.minY {
            ty = min(safeRect.minY, ty + (safeRect.minY + my - p.y))
            moved = true
        } else if p.y > safeRect.maxY - my, ty + ch > safeRect.maxY {
            ty = max(safeRect.maxY - ch, ty - (p.y - (safeRect.maxY - my)))
            moved = true
        }

        if moved {
            fitted = false
            clamp()
        }
    }

    private func fitPlacement(bounds: CGSize, safeRect: CGRect) -> (minScale: CGFloat, tx: CGFloat, ty: CGFloat)? {
        guard safeRect.width > 0, safeRect.height > 0, contentSize.width > 0, contentSize.height > 0 else { return nil }
        let ms = min(safeRect.width / contentSize.width, safeRect.height / contentSize.height)
        let cw = contentSize.width * ms
        let ch = contentSize.height * ms
        // Center on the screen while keeping the entire fitted picture inside safeRect.
        let fitTx = min(safeRect.maxX - cw, max(safeRect.minX, bounds.width / 2 - cw / 2))
        let fitTy = min(safeRect.maxY - ch, max(safeRect.minY, bounds.height / 2 - ch / 2))
        return (ms, fitTx, fitTy)
    }
}
