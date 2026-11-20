import CoreGraphics
import Testing
@testable import HermeshKit

private let portraitBounds = CGSize(width: 393, height: 852)
private let portraitSafe = CGRect(x: 0, y: 59, width: 393, height: 759)
private let landscapeBounds = CGSize(width: 852, height: 393)
private let landscapeSafe = CGRect(x: 59, y: 0, width: 734, height: 372)

private func approx(_ a: CGFloat, _ b: CGFloat, tol: CGFloat = 0.01) -> Bool {
    abs(a - b) <= tol
}

@Test func fitPlacementRespectsSafeAreaInPortraitAndLandscape() {
    var vp = ScreenViewport(contentSize: CGSize(width: 1920, height: 1200))

    vp.layout(bounds: portraitBounds, safeRect: portraitSafe)
    #expect(vp.fitted)
    #expect(!vp.isTransformed)
    #expect(approx(vp.pictureFrame.width, 393))
    #expect(approx(vp.pictureFrame.midY, portraitBounds.height / 2))
    #expect(vp.pictureFrame.minY >= portraitSafe.minY)
    #expect(vp.pictureFrame.maxY <= portraitSafe.maxY)

    // Landscape: bottom edge stays above Home Indicator, sides clear of Dynamic Island.
    vp.layout(bounds: landscapeBounds, safeRect: landscapeSafe)
    #expect(vp.fitted)
    #expect(approx(vp.pictureFrame.height, landscapeSafe.height))
    #expect(vp.pictureFrame.minX >= landscapeSafe.minX)
    #expect(vp.pictureFrame.maxX <= landscapeSafe.maxX)
    #expect(vp.pictureFrame.minY >= landscapeSafe.minY)
    #expect(vp.pictureFrame.maxY <= landscapeSafe.maxY)
}

@Test func bottomBarCanBeDraggedToCenterWithoutSnapBackOrLosingPicture() {
    for (bounds, safe) in [(portraitBounds, portraitSafe), (landscapeBounds, landscapeSafe)] {
        var vp = ScreenViewport(contentSize: CGSize(width: 1920, height: 1200))
        vp.layout(bounds: bounds, safeRect: safe)

        // Drag upward hard enough to push the bottom bar to the center of the safe area.
        vp.pan(dx: 0, dy: -2000)
        #expect(!vp.fitted)
        #expect(vp.isTransformed)
        #expect(approx(vp.pictureFrame.maxY, safe.midY))

        // Re-running layout (no bounds change) must not snap the viewport back.
        vp.layout(bounds: bounds, safeRect: safe)
        #expect(approx(vp.pictureFrame.maxY, safe.midY))

        // Extreme drags in every direction still keep safeRect center inside pictureFrame.
        for (dx, dy) in [(CGFloat(5000), CGFloat(5000)), (-5000, -5000), (5000, -5000), (-5000, 5000)] {
            vp.pan(dx: dx, dy: dy)
            let f = vp.pictureFrame
            #expect(f.minX <= safe.midX + 0.01 && f.maxX >= safe.midX - 0.01)
            #expect(f.minY <= safe.midY + 0.01 && f.maxY >= safe.midY - 0.01)
            let visible = f.intersection(safe)
            #expect(visible.width > 0 && visible.height > 0)
        }

        // One-tap fit restores exact fit placement.
        vp.resetFit()
        #expect(vp.fitted)
        #expect(!vp.isTransformed)
        #expect(approx(vp.scale, vp.minScale))
        #expect(vp.pictureFrame.maxY <= safe.maxY + 0.01)
    }
}

@Test func pinchAnchoredOnBottomBarHasZeroDriftAndAccurateMapping() {
    var vp = ScreenViewport(contentSize: CGSize(width: 1920, height: 1200))
    vp.layout(bounds: portraitBounds, safeRect: portraitSafe)

    // Drag remote bottom edge to safe center.
    vp.pan(dx: 0, dy: -2000)
    #expect(approx(vp.pictureFrame.maxY, portraitSafe.midY))

    // Target a remote bottom-bar button at (960, 1170) — near bottom center.
    let remoteTarget = CGPoint(x: 960, y: 1170)
    let anchorBefore = vp.onScreen(remoteTarget)

    // Pinch 3x centered directly on that bottom-bar point.
    vp.pinch(from: anchorBefore, to: anchorBefore, factor: 3.0)
    let anchorAfter = vp.onScreen(remoteTarget)
    #expect(approx(anchorAfter.x, anchorBefore.x, tol: 0.05))
    #expect(approx(anchorAfter.y, anchorBefore.y, tol: 0.05))

    // Clicking that screen point maps back to the exact remote pixel.
    let hit = vp.picturePoint(at: anchorAfter)
    #expect(hit != nil)
    if let hit {
        #expect(approx(hit.x, remoteTarget.x, tol: 0.05))
        #expect(approx(hit.y, remoteTarget.y, tol: 0.05))
    }

    // Pinching while simultaneously shifting midpoint carries the remote point to the new midpoint.
    let shifted = CGPoint(x: anchorAfter.x + 24, y: anchorAfter.y - 18)
    vp.pinch(from: anchorAfter, to: shifted, factor: 1.5)
    let afterShift = vp.onScreen(remoteTarget)
    #expect(approx(afterShift.x, shifted.x, tol: 0.05))
    #expect(approx(afterShift.y, shifted.y, tol: 0.05))
}

@Test func strictHitTestRejectsBlankAreaOutsidePicture() {
    var vp = ScreenViewport(contentSize: CGSize(width: 1920, height: 1200))
    vp.layout(bounds: portraitBounds, safeRect: portraitSafe)

    // Top and bottom letterbox in portrait 1x fit return nil.
    #expect(vp.picturePoint(at: CGPoint(x: 200, y: vp.pictureFrame.minY - 5)) == nil)
    #expect(vp.picturePoint(at: CGPoint(x: 200, y: vp.pictureFrame.maxY + 5)) == nil)

    // After dragging bottom bar to center, blank area below it returns nil.
    vp.pan(dx: 0, dy: -2000)
    #expect(vp.picturePoint(at: CGPoint(x: 200, y: portraitSafe.midY + 1)) == nil)
    #expect(vp.picturePoint(at: CGPoint(x: 200, y: portraitSafe.midY + 120)) == nil)

    // Point inside the bottom bar returns valid coordinates; clamped helper still clamps for drags.
    let inside = vp.picturePoint(at: CGPoint(x: 200, y: portraitSafe.midY - 2))
    #expect(inside != nil)
    let clamped = vp.clampedPicturePoint(at: CGPoint(x: 200, y: portraitSafe.midY + 120))
    #expect(approx(clamped.y, 1199))
}

@Test func trackpadFollowRespectsSafeRectAndDoesNotFightOverscrollOrFit() {
    var vp = ScreenViewport(contentSize: CGSize(width: 1920, height: 1200))
    vp.layout(bounds: portraitBounds, safeRect: portraitSafe)
    let fittedFrame = vp.pictureFrame

    // Moving pointer to corners while fitted must never wiggle the picture.
    vp.follow(pointer: CGPoint(x: 0, y: 0))
    vp.follow(pointer: CGPoint(x: 1919, y: 1199))
    #expect(vp.fitted)
    #expect(vp.pictureFrame == fittedFrame)

    // Overscrolling bottom bar to center then moving pointer to bottom bar must not snap back.
    vp.pan(dx: 0, dy: -2000)
    let overscrolled = vp.pictureFrame
    vp.follow(pointer: CGPoint(x: 960, y: 1180))
    #expect(vp.pictureFrame == overscrolled)

    // When zoomed in beyond safeRect, moving pointer near right edge reveals hidden right content.
    vp.resetFit()
    vp.pinch(from: CGPoint(x: portraitSafe.midX, y: portraitSafe.midY),
             to: CGPoint(x: portraitSafe.midX, y: portraitSafe.midY),
             factor: 3.0)
    let beforeTx = vp.tx
    vp.follow(pointer: CGPoint(x: 1900, y: 600))
    #expect(vp.tx < beforeTx)
    #expect(vp.onScreen(CGPoint(x: 1900, y: 600)).x <= portraitSafe.maxX)
}

@Test func rotationAndKeyboardKeepCenterFocusOnBottomBar() {
    var vp = ScreenViewport(contentSize: CGSize(width: 1920, height: 1200))
    vp.layout(bounds: portraitBounds, safeRect: portraitSafe)

    // Place bottom-center at safe center and zoom in 2x.
    vp.pan(dx: 0, dy: -2000)
    let bottomCenter = CGPoint(x: 960, y: 1200)
    let centerPt = CGPoint(x: portraitSafe.midX, y: portraitSafe.midY)
    vp.pinch(from: centerPt, to: centerPt, factor: 2.0)
    #expect(approx(vp.onScreen(bottomCenter).y, portraitSafe.midY, tol: 0.5))

    // Rotate to landscape: bottom-center stays anchored at landscape safe center.
    vp.layout(bounds: landscapeBounds, safeRect: landscapeSafe)
    #expect(!vp.fitted)
    #expect(approx(vp.onScreen(bottomCenter).x, landscapeSafe.midX, tol: 0.5))
    #expect(approx(vp.onScreen(bottomCenter).y, landscapeSafe.midY, tol: 0.5))

    // Rotate back to portrait and open software keyboard (bounds shrinks vertically).
    vp.layout(bounds: portraitBounds, safeRect: portraitSafe)
    let kbBounds = CGSize(width: 393, height: 516)
    let kbSafe = CGRect(x: 0, y: 59, width: 393, height: 457)
    vp.layout(bounds: kbBounds, safeRect: kbSafe)
    #expect(approx(vp.onScreen(bottomCenter).y, kbSafe.midY, tol: 0.5))
}
