import CoreMedia
import Foundation
import VideoToolbox

/// Encoder turns pictures into H.264 for a viewer that shows each frame as
/// it arrives: real time, no reordering, a keyframe only at the start and
/// when asked. A picture that arrives while the one before is still being
/// encoded is dropped, never queued.
final class Encoder {
    private let out: Output
    private var session: VTCompressionSession?
    private let lock = NSLock()
    private var busy = 0
    private var wantKey = true
    private var width = 0, height = 0
    private var bitrate: Int
    private let fps: Int

    init(out: Output, fps: Int, bitrate: Int) {
        self.out = out
        self.fps = fps
        self.bitrate = bitrate
    }

    deinit { if let s = session { VTCompressionSessionInvalidate(s) } }

    /// setBitrate changes the target, now and for a session made later.
    func setBitrate(_ bps: Int) {
        lock.lock(); bitrate = bps; let s = session; lock.unlock()
        if let s { VTSessionSetProperty(s, key: kVTCompressionPropertyKey_AverageBitRate, value: bps as CFNumber) }
    }

    func requestKeyframe() { lock.lock(); wantKey = true; lock.unlock() }

    private func makeSession(width: Int, height: Int) -> VTCompressionSession? {
        var made: VTCompressionSession?
        // Low-latency rate control first; without it where the encoder has none.
        for spec in [[kVTVideoEncoderSpecification_EnableLowLatencyRateControl: kCFBooleanTrue] as CFDictionary, nil] {
            let status = VTCompressionSessionCreate(allocator: nil, width: Int32(width), height: Int32(height), codecType: kCMVideoCodecType_H264,
                                                    encoderSpecification: spec, imageBufferAttributes: nil, compressedDataAllocator: nil,
                                                    outputCallback: nil, refcon: nil, compressionSessionOut: &made)
            if status == noErr, made != nil { break }
        }
        guard let s = made else { return nil }
        VTSessionSetProperty(s, key: kVTCompressionPropertyKey_RealTime, value: kCFBooleanTrue)
        VTSessionSetProperty(s, key: kVTCompressionPropertyKey_ProfileLevel, value: kVTProfileLevel_H264_High_AutoLevel)
        VTSessionSetProperty(s, key: kVTCompressionPropertyKey_AllowFrameReordering, value: kCFBooleanFalse)
        VTSessionSetProperty(s, key: kVTCompressionPropertyKey_MaxKeyFrameInterval, value: 360_000 as CFNumber)
        VTSessionSetProperty(s, key: kVTCompressionPropertyKey_MaxKeyFrameIntervalDuration, value: 36_000 as CFNumber)
        VTSessionSetProperty(s, key: kVTCompressionPropertyKey_ExpectedFrameRate, value: fps as CFNumber)
        VTSessionSetProperty(s, key: kVTCompressionPropertyKey_AverageBitRate, value: bitrate as CFNumber)
        VTCompressionSessionPrepareToEncodeFrames(s)
        return s
    }

    func encode(_ image: CVImageBuffer, time: CMTime) {
        let w = CVPixelBufferGetWidth(image), h = CVPixelBufferGetHeight(image)
        lock.lock()
        if busy > 0 { lock.unlock(); return }
        if session == nil || w != width || h != height {
            if let s = session { VTCompressionSessionInvalidate(s) }
            session = makeSession(width: w, height: h)
            width = w; height = h; wantKey = true
            if session != nil { out.send(.format, json: FormatMessage(width: w, height: h)) }
        }
        guard let s = session else {
            lock.unlock()
            out.send(.state, json: StateMessage(error: "encoder", detail: "no H.264 encoder", can_control: false))
            return
        }
        let key = wantKey
        wantKey = false
        busy += 1
        lock.unlock()
        let props = key ? [kVTEncodeFrameOptionKey_ForceKeyFrame: kCFBooleanTrue] as CFDictionary : nil
        let status = VTCompressionSessionEncodeFrame(s, imageBuffer: image, presentationTimeStamp: time, duration: .invalid, frameProperties: props, infoFlagsOut: nil) { [weak self] status, _, sample in
            guard let self else { return }
            self.lock.lock(); self.busy -= 1; self.lock.unlock()
            if status == noErr, let sample { self.emit(sample) } else if status != noErr { self.requestKeyframe() }
        }
        if status != noErr {
            lock.lock(); busy -= 1; wantKey = true
            // The session went bad (the display changed, the Mac slept): make another for the next picture.
            VTCompressionSessionInvalidate(s); session = nil
            lock.unlock()
        }
    }

    /// emit writes one access unit as Annex B, with the parameter sets before a keyframe.
    private func emit(_ sample: CMSampleBuffer) {
        guard let block = CMSampleBufferGetDataBuffer(sample), let format = CMSampleBufferGetFormatDescription(sample) else { return }
        var key = true
        if let list = CMSampleBufferGetSampleAttachmentsArray(sample, createIfNecessary: false) as? [[CFString: Any]], let first = list.first {
            key = !((first[kCMSampleAttachmentKey_NotSync] as? Bool) ?? false)
        }
        let start: [UInt8] = [0, 0, 0, 1]
        let micros = UInt64(max(0, CMTimeGetSeconds(CMSampleBufferGetPresentationTimeStamp(sample))) * 1_000_000)
        var payload = Data(capacity: CMBlockBufferGetDataLength(block) + 128)
        payload.append(key ? 1 : 0)
        for shift in stride(from: 56, through: 0, by: -8) { payload.append(UInt8((micros >> UInt64(shift)) & 0xFF)) }
        if key {
            var count = 0
            CMVideoFormatDescriptionGetH264ParameterSetAtIndex(format, parameterSetIndex: 0, parameterSetPointerOut: nil, parameterSetSizeOut: nil, parameterSetCountOut: &count, nalUnitHeaderLengthOut: nil)
            for i in 0..<count {
                var p: UnsafePointer<UInt8>?
                var n = 0
                if CMVideoFormatDescriptionGetH264ParameterSetAtIndex(format, parameterSetIndex: i, parameterSetPointerOut: &p, parameterSetSizeOut: &n, parameterSetCountOut: nil, nalUnitHeaderLengthOut: nil) == noErr, let p {
                    payload.append(contentsOf: start)
                    payload.append(p, count: n)
                }
            }
        }
        let total = CMBlockBufferGetDataLength(block)
        var bytes = [UInt8](repeating: 0, count: total)
        guard CMBlockBufferCopyDataBytes(block, atOffset: 0, dataLength: total, destination: &bytes) == noErr else { return }
        var off = 0
        while off + 4 <= total {  // AVCC: each unit after its 4-byte length
            let n = Int(bytes[off]) << 24 | Int(bytes[off + 1]) << 16 | Int(bytes[off + 2]) << 8 | Int(bytes[off + 3])
            off += 4
            guard n > 0, off + n <= total else { break }
            payload.append(contentsOf: start)
            payload.append(contentsOf: bytes[off..<off + n])
            off += n
        }
        out.send(.video, payload)
    }
}
