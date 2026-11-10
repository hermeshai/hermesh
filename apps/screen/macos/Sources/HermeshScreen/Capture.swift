import CoreMedia
import Foundation
import ScreenCaptureKit

/// Capture hands the main display's picture to the encoder, a frame each
/// time the screen changes.
final class Capture: NSObject, SCStreamOutput, SCStreamDelegate {
    private let out: Output
    private let encoder: Encoder
    private let queue = DispatchQueue(label: "hermesh.screen.capture")
    private var stream: SCStream?
    private var config = SCStreamConfiguration()
    /// The picture last captured (on queue): a still screen sends no frames,
    /// and a keyframe asked for meanwhile is made from this.
    private var last: CVImageBuffer?
    /// Frames a second: the most the viewer asked for, and what is captured now.
    private var most = 60, rate = 60

    init(out: Output, encoder: Encoder) {
        self.out = out
        self.encoder = encoder
    }

    func start(maxEdge: Int, fps: Int, pointer: Bool) async throws {
        let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
        guard let display = content.displays.first(where: { $0.displayID == CGMainDisplayID() }) ?? content.displays.first else {
            throw NSError(domain: "hermesh.screen", code: 1, userInfo: [NSLocalizedDescriptionKey: "no display"])
        }
        let filter = SCContentFilter(display: display, excludingWindows: [])
        // The picture's size: the display's own pixels, made smaller to fit maxEdge, both even.
        let scale = Double(filter.pointPixelScale)
        let pw = Double(display.width) * scale, ph = Double(display.height) * scale
        let k = min(1, Double(maxEdge) / max(pw, ph))
        config.width = max(2, Int(pw * k) / 2 * 2)
        config.height = max(2, Int(ph * k) / 2 * 2)
        most = fps
        rate = fps
        config.minimumFrameInterval = CMTime(value: 1, timescale: CMTimeScale(fps))
        config.pixelFormat = kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange
        config.showsCursor = pointer
        config.queueDepth = 5 // one of them is kept as the last picture
        let s = SCStream(filter: filter, configuration: config, delegate: self)
        try s.addStreamOutput(self, type: .screen, sampleHandlerQueue: queue)
        try await s.startCapture()
        stream = s
    }

    /// showPointer: whether the picture has the pointer in it.
    func showPointer(_ shown: Bool) {
        config.showsCursor = shown
        stream?.updateConfiguration(config) { _ in }
        keyframe()
    }

    /// follow captures fewer frames a second when the network carries less,
    /// so that what gets through makes fewer, clearer frames; and more again
    /// once it carries comfortably more than where it stepped down.
    func follow(bitrate bps: Int) {
        var want = rate
        if bps < 800_000 { want = 30 } else if bps < 1_800_000 { want = min(rate, 60) }
        if bps >= 2_200_000 { want = most } else if bps >= 1_000_000 { want = max(want, 60) }
        want = min(want, most)
        guard want != rate else { return }
        rate = want
        config.minimumFrameInterval = CMTime(value: 1, timescale: CMTimeScale(want))
        stream?.updateConfiguration(config) { _ in }
    }

    /// keyframe sends one now, from the last picture when the screen is still.
    func keyframe() {
        encoder.requestKeyframe()
        queue.async { [self] in
            if let last { encoder.encode(last, time: CMClockGetTime(CMClockGetHostTimeClock())) }
        }
    }

    func stream(_ stream: SCStream, didOutputSampleBuffer sample: CMSampleBuffer, of type: SCStreamOutputType) {
        guard type == .screen, let image = sample.imageBuffer,
              let list = CMSampleBufferGetSampleAttachmentsArray(sample, createIfNecessary: false) as? [[SCStreamFrameInfo: Any]],
              let raw = list.first?[.status] as? Int, SCFrameStatus(rawValue: raw) == .complete
        else { return }
        last = image
        var stamps = Stamps(captured: Stamps.now())
        if let shown = list.first?[.displayTime] as? UInt64 { stamps.displayed = Stamps.wall(machTime: shown) }
        encoder.encode(image, time: sample.presentationTimeStamp, stamps: stamps)
    }

    func stream(_ stream: SCStream, didStopWithError error: Error) {
        out.send(.state, json: StateMessage(error: "capture", detail: error.localizedDescription, can_control: false))
        exit(1)
    }
}

/// TestSource stands in for the screen in tests: a pattern that moves with
/// every frame, encoded like the real picture.
final class TestSource {
    private let encoder: Encoder
    private var timer: DispatchSourceTimer?
    private var frame = 0

    init(encoder: Encoder) { self.encoder = encoder }

    func start(fps: Int) {
        let t = DispatchSource.makeTimerSource(queue: DispatchQueue(label: "hermesh.screen.test"))
        t.schedule(deadline: .now(), repeating: 1.0 / Double(fps))
        t.setEventHandler { [weak self] in self?.tick(fps: fps) }
        t.resume()
        timer = t
    }

    private func tick(fps: Int) {
        let w = 640, h = 400
        var pb: CVPixelBuffer?
        let attrs = [kCVPixelBufferIOSurfacePropertiesKey: [:] as CFDictionary] as CFDictionary
        guard CVPixelBufferCreate(nil, w, h, kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange, attrs, &pb) == kCVReturnSuccess, let pb else { return }
        CVPixelBufferLockBaseAddress(pb, [])
        if let y = CVPixelBufferGetBaseAddressOfPlane(pb, 0)?.assumingMemoryBound(to: UInt8.self) {
            let stride = CVPixelBufferGetBytesPerRowOfPlane(pb, 0)
            let bar = (frame * 8) % w
            for row in 0..<h {
                for col in 0..<w {
                    let on = abs(col - bar) < 24
                    y[row * stride + col] = on ? 235 : UInt8(40 + (col * 120 / w) + (row * 40 / h))
                }
            }
        }
        if let uv = CVPixelBufferGetBaseAddressOfPlane(pb, 1) {
            memset(uv, 128, CVPixelBufferGetBytesPerRowOfPlane(pb, 1) * (h / 2))
        }
        CVPixelBufferUnlockBaseAddress(pb, [])
        let now = Stamps.now()
        encoder.encode(pb, time: CMTime(value: CMTimeValue(frame), timescale: CMTimeScale(fps)), stamps: Stamps(displayed: now, captured: now))
        frame += 1
    }
}
