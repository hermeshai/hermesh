import AppKit

/// Clipboard is this Mac's clipboard both ways: it takes the viewer's text,
/// and tells hermeshd what is copied here once the session has started. In
/// tests a file stands for it (`--test-clipboard`).
final class Clipboard {
    /// The most text sent either way, in bytes (internal/screen ClipboardMax).
    static let maxBytes = 60000

    private let out: Output
    private let file: String?
    // Touched on the main queue only, where NSPasteboard is safe.
    private var seenCount = 0  // the pasteboard's changeCount last looked at
    private var seenFile: String?  // the file's text last looked at
    private var last: String?  // the text last sent, or set from the viewer
    private var timer: DispatchSourceTimer?

    init(out: Output, file: String?) {
        self.out = out
        self.file = file
    }

    /// Whether a file stands for the clipboard.
    var isFile: Bool { file != nil }

    /// set gives the clipboard the viewer's text. It is not sent back.
    func set(_ text: String) {
        DispatchQueue.main.sync {
            last = text
            if let file {
                try? Data(text.utf8).write(to: URL(fileURLWithPath: file), options: .atomic)
                seenFile = text
                return
            }
            let pb = NSPasteboard.general
            pb.clearContents()
            pb.setString(text, forType: .string)
            seenCount = pb.changeCount
        }
    }

    /// watch starts sending what is copied from now on.
    func watch() {
        DispatchQueue.main.async { [self] in
            guard timer == nil else { return }
            if let file { seenFile = try? String(contentsOfFile: file, encoding: .utf8) } else { seenCount = NSPasteboard.general.changeCount }
            let t = DispatchSource.makeTimerSource(queue: .main)
            let every: DispatchTimeInterval = file != nil ? .milliseconds(200) : .milliseconds(500)
            t.schedule(deadline: .now() + every, repeating: every)
            t.setEventHandler { [self] in poll() }
            t.resume()
            timer = t
        }
    }

    private func poll() {
        var text: String?
        if let file {
            text = try? String(contentsOfFile: file, encoding: .utf8)
            if text == seenFile { return }
            seenFile = text
        } else {
            let pb = NSPasteboard.general
            if pb.changeCount == seenCount { return }
            seenCount = pb.changeCount
            text = pb.string(forType: .string)  // nil when what was copied isn't text
        }
        guard let text, !text.isEmpty, text.utf8.count <= Clipboard.maxBytes, text != last else { return }
        last = text
        out.send(.clipboard, json: ClipboardMessage(text: text))
    }
}
