import CoreMedia
import UIKit
import VideoToolbox

/// Shows the machine's frames, each as soon as it arrives: no clock, no
/// queue. Frames come as H.264 in Annex B, a keyframe with its parameter
/// sets first.
///
/// A frame is decoded on the thread that brings it and becomes a layer's
/// contents there and then, in a transaction of its own: it is on screen at
/// the next refresh. Measured on the Simulator against letting
/// AVSampleBufferDisplayLayer decode and show it, this is 6 ms sooner, and
/// 3 ms sooner than going through the main thread, which may be busy with
/// the owner's fingers (docs/computer-control.md §5).
final class ScreenPictureView: UIView {
    // What show uses, on the thread that feeds frames, under lock.
    private nonisolated let lock = NSLock()
    private nonisolated(unsafe) let picture = CALayer()
    private nonisolated(unsafe) var sps: Data?
    private nonisolated(unsafe) var pps: Data?
    private nonisolated(unsafe) var format: CMVideoFormatDescription?
    private nonisolated(unsafe) var decoder: VTDecompressionSession?
    private nonisolated(unsafe) var waitingForKey = true
    private nonisolated(unsafe) var shown = false
    private nonisolated(unsafe) var needsKeyframe: (@Sendable () -> Void)?

    /// Called (on the thread that feeds frames) when the frames it has can't
    /// be shown and a keyframe is needed.
    var onNeedsKeyframe: (@Sendable () -> Void)? {
        get { lock.withLock { needsKeyframe } }
        set { lock.withLock { needsKeyframe = newValue } }
    }

    /// Called when the first frame was shown.
    var onFirstFrame: (() -> Void)?

    override init(frame: CGRect) {
        super.init(frame: frame)
        isUserInteractionEnabled = false
        picture.contentsGravity = .resize
        layer.addSublayer(picture)
    }

    required init?(coder: NSCoder) { fatalError() }

    deinit { if let decoder { VTDecompressionSessionInvalidate(decoder) } }

    override func layoutSubviews() {
        super.layoutSubviews()
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        picture.frame = bounds
        CATransaction.commit()
    }

    /// Shows one frame. Any thread, one at a time.
    nonisolated func show(key: Bool, annexB: Data) {
        lock.lock()
        defer { lock.unlock() }
        var units: [Data] = []
        for unit in ScreenPictureView.units(annexB) {
            switch unit[unit.startIndex] & 0x1F {
            case 7: if unit != sps { sps = unit; format = nil }
            case 8: if unit != pps { pps = unit; format = nil }
            case 9: break // access unit delimiter
            default: units.append(unit)
            }
        }
        if waitingForKey && !key { return }
        if format == nil, let sps, let pps {
            format = sps.withUnsafeBytes { s in
                pps.withUnsafeBytes { p in
                    var out: CMVideoFormatDescription?
                    let sets = [s.bindMemory(to: UInt8.self).baseAddress!, p.bindMemory(to: UInt8.self).baseAddress!]
                    let sizes = [s.count, p.count]
                    CMVideoFormatDescriptionCreateFromH264ParameterSets(allocator: nil, parameterSetCount: 2, parameterSetPointers: sets, parameterSetSizes: sizes,
                                                                        nalUnitHeaderLength: 4, formatDescriptionOut: &out)
                    return out
                }
            }
            // Another size, or another stream: another decoder.
            if let decoder, let format, !VTDecompressionSessionCanAcceptFormatDescription(decoder, formatDescription: format) {
                VTDecompressionSessionInvalidate(decoder)
                self.decoder = nil
            }
        }
        guard let format, !units.isEmpty else { return }
        // AVCC: each unit after its 4-byte length.
        var body = Data(capacity: annexB.count + 16)
        for u in units {
            var n = UInt32(u.count).bigEndian
            body.append(Data(bytes: &n, count: 4))
            body.append(u)
        }
        var block: CMBlockBuffer?
        guard CMBlockBufferCreateWithMemoryBlock(allocator: nil, memoryBlock: nil, blockLength: body.count, blockAllocator: nil, customBlockSource: nil,
                                                 offsetToData: 0, dataLength: body.count, flags: 0, blockBufferOut: &block) == noErr, let block,
            body.withUnsafeBytes({ CMBlockBufferReplaceDataBytes(with: $0.baseAddress!, blockBuffer: block, offsetIntoDestination: 0, dataLength: body.count) }) == noErr
        else { return }
        var sample: CMSampleBuffer?
        var size = body.count
        guard CMSampleBufferCreateReady(allocator: nil, dataBuffer: block, formatDescription: format, sampleCount: 1, sampleTimingEntryCount: 0,
                                        sampleTimingArray: nil, sampleSizeEntryCount: 1, sampleSizeArray: &size, sampleBufferOut: &sample) == noErr, let sample
        else { return }

        if decoder == nil {
            // Pictures a layer can show as they are.
            let attrs: [CFString: Any] = [kCVPixelBufferPixelFormatTypeKey: kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange,
                                          kCVPixelBufferIOSurfacePropertiesKey: [:] as CFDictionary]
            VTDecompressionSessionCreate(allocator: nil, formatDescription: format, decoderSpecification: nil, imageBufferAttributes: attrs as CFDictionary,
                                         outputCallback: nil, decompressionSessionOut: &decoder)
            if let decoder { VTSessionSetProperty(decoder, key: kVTDecompressionPropertyKey_RealTime, value: kCFBooleanTrue) }
        }
        var image: CVImageBuffer?
        if let decoder {
            // Without the asynchronous flag the picture is there when this returns.
            let status = VTDecompressionSessionDecodeFrame(decoder, sampleBuffer: sample, flags: [], infoFlagsOut: nil) { _, _, buffer, _, _ in image = buffer }
            if status != noErr { image = nil }
        }
        guard let image, let surface = CVPixelBufferGetIOSurface(image)?.takeUnretainedValue() else {
            // The decoder gave up (the app was away, a frame was bad): another one, from a keyframe.
            if let decoder { VTDecompressionSessionInvalidate(decoder) }
            decoder = nil
            waitingForKey = true
            needsKeyframe?()
            return
        }
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        picture.contents = surface
        CATransaction.commit()
        CATransaction.flush()
        waitingForKey = false
        if !shown {
            shown = true
            Task { @MainActor [weak self] in self?.onFirstFrame?() }
        }
    }

    /// The units of an Annex B stream: what stands between its start codes.
    nonisolated static func units(_ data: Data) -> [Data] {
        var out: [Data] = []
        data.withUnsafeBytes { (raw: UnsafeRawBufferPointer) in
            let b = raw.bindMemory(to: UInt8.self)
            let n = b.count
            var start = -1, i = 0
            while i + 3 <= n {
                if b[i] == 0, b[i + 1] == 0, b[i + 2] == 1 {
                    if start >= 0 {
                        var end = i
                        if end > start, b[end - 1] == 0 { end -= 1 } // the zero of a 4-byte start code
                        if end > start { out.append(Data(b[start ..< end])) }
                    }
                    start = i + 3
                    i += 3
                } else {
                    i += 1
                }
            }
            if start >= 0, start < n { out.append(Data(b[start ..< n])) }
        }
        return out
    }
}
