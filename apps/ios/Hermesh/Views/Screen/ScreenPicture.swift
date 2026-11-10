import AVFoundation
import CoreMedia
import UIKit

/// Shows the machine's frames, each as soon as it arrives: no clock, no
/// queue. Frames come as H.264 in Annex B, a keyframe with its parameter
/// sets first.
final class ScreenPictureView: UIView {
    override class var layerClass: AnyClass { AVSampleBufferDisplayLayer.self }

    // What show uses, on the thread that feeds frames, under lock.
    private nonisolated let lock = NSLock()
    private nonisolated(unsafe) var renderer: AVSampleBufferVideoRenderer!
    private nonisolated(unsafe) var sps: Data?
    private nonisolated(unsafe) var pps: Data?
    private nonisolated(unsafe) var format: CMVideoFormatDescription?
    private nonisolated(unsafe) var waitingForKey = true
    private nonisolated(unsafe) var shown = false
    private nonisolated(unsafe) var needsKeyframe: (@Sendable () -> Void)?

    /// Called (on the thread that feeds frames) when the frames it has can't
    /// be shown and a keyframe is needed.
    var onNeedsKeyframe: (@Sendable () -> Void)? {
        get { lock.withLock { needsKeyframe } }
        set { lock.withLock { needsKeyframe = newValue } }
    }

    /// Called when the first frame was handed over.
    var onFirstFrame: (() -> Void)?

    override init(frame: CGRect) {
        super.init(frame: frame)
        let display = layer as! AVSampleBufferDisplayLayer
        display.videoGravity = .resize
        renderer = display.sampleBufferRenderer
        isUserInteractionEnabled = false
    }

    required init?(coder: NSCoder) { fatalError() }

    /// Hands one frame to the screen. Any thread, one at a time.
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
        }
        guard let format, !units.isEmpty, let renderer else { return }
        if renderer.status == .failed || renderer.requiresFlushToResumeDecoding {
            // The app was away, or the decoder gave up: start again from a keyframe.
            renderer.flush()
            if !key {
                waitingForKey = true
                needsKeyframe?()
                return
            }
        }
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
        var timing = CMSampleTimingInfo(duration: .invalid, presentationTimeStamp: CMClockGetTime(CMClockGetHostTimeClock()), decodeTimeStamp: .invalid)
        var size = body.count
        guard CMSampleBufferCreateReady(allocator: nil, dataBuffer: block, formatDescription: format, sampleCount: 1, sampleTimingEntryCount: 1,
                                        sampleTimingArray: &timing, sampleSizeEntryCount: 1, sampleSizeArray: &size, sampleBufferOut: &sample) == noErr, let sample
        else { return }
        if let list = CMSampleBufferGetSampleAttachmentsArray(sample, createIfNecessary: true), CFArrayGetCount(list) > 0 {
            let first = unsafeBitCast(CFArrayGetValueAtIndex(list, 0), to: CFMutableDictionary.self)
            CFDictionarySetValue(first, Unmanaged.passUnretained(kCMSampleAttachmentKey_DisplayImmediately).toOpaque(), Unmanaged.passUnretained(kCFBooleanTrue).toOpaque())
        }
        renderer.enqueue(sample)
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
