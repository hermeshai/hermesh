import Foundation

// The pipe to hermeshd (internal/screen/pipe.go has the other end): messages
// of a 4-byte big-endian length, then one byte of kind and the payload.

enum Kind: UInt8 {
    // To the helper.
    case start = 0x73  // s: JSON StartMessage
    case bitrate = 0x62  // b: JSON BitrateMessage
    case keyframe = 0x6B  // k
    case input = 0x69  // i: JSON InputMessage
    case pointer = 0x63  // c: JSON PointerMessage, whether the picture shows the pointer
    // From the helper.
    case format = 0x66  // f: JSON FormatMessage
    case video = 0x76  // v: 1 byte of flags (1 = keyframe), three times of 8 bytes (Stamps), Annex B
    case state = 0x65  // e: JSON StateMessage
    case log = 0x6C  // l: text
    case clipboard = 0x78  // x: JSON ClipboardMessage, what was copied on this Mac
}

let maxMessage = 16 << 20

/// When a frame passed each stage here, in microseconds since 1970, for
/// measuring where the picture's delay comes from (zero: not known).
struct Stamps {
    /// The screen showed what the frame holds.
    var displayed: UInt64 = 0
    /// The frame was handed to this program.
    var captured: UInt64 = 0
    /// It came out of the encoder.
    var encoded: UInt64 = 0

    static func now() -> UInt64 { UInt64(Date().timeIntervalSince1970 * 1_000_000) }

    /// The wall time of a moment on the Mach clock (the display's).
    static func wall(machTime: UInt64) -> UInt64 {
        var tb = mach_timebase_info_data_t()
        mach_timebase_info(&tb)
        let ago = Int64(mach_absolute_time()) - Int64(machTime)
        let micros = ago * Int64(tb.numer) / Int64(tb.denom) / 1000
        return UInt64(Int64(now()) - micros)
    }
}

/// Output writes whole messages to stdout, one at a time.
final class Output {
    private let lock = NSLock()
    private let fd: Int32

    init(fd: Int32 = STDOUT_FILENO) { self.fd = fd }

    func send(_ kind: Kind, _ payload: Data = Data()) {
        var head = [UInt8](repeating: 0, count: 5)
        let n = UInt32(payload.count + 1)
        head[0] = UInt8(n >> 24); head[1] = UInt8((n >> 16) & 0xFF); head[2] = UInt8((n >> 8) & 0xFF); head[3] = UInt8(n & 0xFF)
        head[4] = kind.rawValue
        lock.lock(); defer { lock.unlock() }
        guard writeAll(head) else { exit(0) }  // hermeshd went away
        if !payload.isEmpty {
            let ok = payload.withUnsafeBytes { writeAll($0) }
            if !ok { exit(0) }
        }
    }

    func send<T: Encodable>(_ kind: Kind, json: T) {
        if let data = try? JSONEncoder().encode(json) { send(kind, data) }
    }

    func log(_ text: String) { send(.log, Data(text.utf8)) }

    private func writeAll(_ bytes: [UInt8]) -> Bool { bytes.withUnsafeBytes { writeAll($0) } }

    private func writeAll(_ buf: UnsafeRawBufferPointer) -> Bool {
        var off = 0
        while off < buf.count {
            let n = write(fd, buf.baseAddress! + off, buf.count - off)
            if n < 0 && errno == EINTR { continue }
            if n <= 0 { return false }
            off += n
        }
        return true
    }
}

/// readMessages calls handle for each message on stdin, until it closes.
func readMessages(fd: Int32 = STDIN_FILENO, handle: (Kind, Data) -> Void) {
    func readFull(_ count: Int) -> Data? {
        var data = Data(count: count)
        var off = 0
        while off < count {
            let n = data.withUnsafeMutableBytes { read(fd, $0.baseAddress! + off, count - off) }
            if n < 0 && errno == EINTR { continue }
            if n <= 0 { return nil }
            off += n
        }
        return data
    }
    while let head = readFull(4) {
        let n = Int(head[0]) << 24 | Int(head[1]) << 16 | Int(head[2]) << 8 | Int(head[3])
        guard n >= 1, n <= maxMessage, let body = readFull(n) else { return }
        if let kind = Kind(rawValue: body[0]) { handle(kind, body.dropFirst()) }
    }
}

// The JSON messages. Input is hermesh.screen.v1.Input as protojson writes it
// with the proto's field names: one of its members is set.

struct StartMessage: Decodable {
    var max_edge: Int?
    var fps: Int?
    var bitrate: Int?
    var pointer: Bool?
}

struct BitrateMessage: Decodable { var bps: Int }
struct PointerMessage: Decodable { var shown: Bool }

struct FormatMessage: Encodable {
    var width: Int
    var height: Int
}

struct StateMessage: Encodable {
    /// "" while all is well; else "screen_recording", "no_display", "capture", "encoder".
    var error: String
    var detail: String
    /// Whether keyboard and mouse events can be posted (Accessibility).
    var can_control: Bool
}

struct ClipboardMessage: Encodable { var text: String }

struct InputMessage: Decodable {
    struct Move: Decodable { var x: Double?; var y: Double? }
    struct Button: Decodable { var x: Double?; var y: Double?; var button: String?; var down: Bool?; var clicks: Int? }
    struct Wheel: Decodable { var x: Double?; var y: Double?; var dx: Double?; var dy: Double? }
    struct Key: Decodable { var key: String?; var character: String?; var command: Bool?; var option: Bool?; var control: Bool?; var shift: Bool? }
    struct Text: Decodable { var text: String? }
    struct Clipboard: Decodable { var text: String?; var paste: String? }
    var move: Move?
    var button: Button?
    var wheel: Wheel?
    var key: Key?
    var text: Text?
    var clipboard: Clipboard?
}
