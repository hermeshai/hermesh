import CoreGraphics
import Foundation

/// Input posts the viewer's mouse and keyboard to this Mac. Positions come
/// as fractions of the main display.
final class Input {
    private let source = CGEventSource(stateID: .hidSystemState)
    private var held: Set<UInt32> = []  // CGMouseButton raw values held down
    private var last = CGPoint.zero
    /// When set, events are written here as JSON lines and not posted (tests).
    private let log: FileHandle?

    init(logPath: String?) {
        if let logPath {
            FileManager.default.createFile(atPath: logPath, contents: nil)
            log = FileHandle(forWritingAtPath: logPath)
        } else {
            log = nil
            // The first event posted takes some 20 ms to reach the window server; let it be this one,
            // a move to where the pointer is already, and not the owner's first touch.
            if Input.canControl, let here = CGEvent(source: nil)?.location {
                CGEvent(mouseEventSource: source, mouseType: .mouseMoved, mouseCursorPosition: here, mouseButton: .left)?.post(tap: .cghidEventTap)
            }
        }
    }

    static var canControl: Bool { CGPreflightPostEventAccess() }

    /// The main display's place among the screens. Asking the window server
    /// for it with every event made one in ten wait 3 ms and more behind its
    /// other work, some 60 ms: it is asked off to the side, a second apart at
    /// most, and events use the answer last had.
    private let boundsLock = NSLock()
    private var bounds = CGDisplayBounds(CGMainDisplayID())
    private var boundsAsked = Date()

    private func displayBounds() -> CGRect {
        boundsLock.lock()
        defer { boundsLock.unlock() }
        if Date().timeIntervalSince(boundsAsked) > 1 {
            boundsAsked = Date()
            DispatchQueue.global(qos: .utility).async { [self] in
                let b = CGDisplayBounds(CGMainDisplayID())
                boundsLock.lock(); bounds = b; boundsLock.unlock()
            }
        }
        return bounds
    }

    private func point(_ x: Double?, _ y: Double?) -> CGPoint {
        let b = displayBounds()
        let fx = min(1, max(0, x ?? 0)), fy = min(1, max(0, y ?? 0))
        return CGPoint(x: b.minX + fx * (b.width - 1), y: b.minY + fy * (b.height - 1))
    }

    func handle(_ raw: Data) {
        if let log {
            log.write(raw); log.write(Data("\n".utf8))
            return
        }
        guard let m = try? JSONDecoder().decode(InputMessage.self, from: raw) else { return }
        if let v = m.move { move(to: point(v.x, v.y)) }
        if let v = m.button { button(v) }
        if let v = m.wheel { wheel(v) }
        if let v = m.key { key(v) }
        if let v = m.text, let t = v.text { type(t) }
    }

    private func move(to p: CGPoint) {
        last = p
        var type = CGEventType.mouseMoved, button = CGMouseButton.left
        if held.contains(CGMouseButton.left.rawValue) { type = .leftMouseDragged } else if held.contains(CGMouseButton.right.rawValue) { type = .rightMouseDragged; button = .right }
        CGEvent(mouseEventSource: source, mouseType: type, mouseCursorPosition: p, mouseButton: button)?.post(tap: .cghidEventTap)
    }

    private func button(_ v: InputMessage.Button) {
        let p = point(v.x, v.y), right = v.button == "MOUSE_BUTTON_RIGHT", down = v.down ?? false
        let button: CGMouseButton = right ? .right : .left
        if p != last { move(to: p) }
        let type: CGEventType = right ? (down ? .rightMouseDown : .rightMouseUp) : (down ? .leftMouseDown : .leftMouseUp)
        let e = CGEvent(mouseEventSource: source, mouseType: type, mouseCursorPosition: p, mouseButton: button)
        e?.setIntegerValueField(.mouseEventClickState, value: Int64(max(1, v.clicks ?? 1)))
        e?.post(tap: .cghidEventTap)
        if down { held.insert(button.rawValue) } else { held.remove(button.rawValue) }
    }

    private func wheel(_ v: InputMessage.Wheel) {
        let p = point(v.x, v.y)
        if p != last { move(to: p) }
        // The viewer sends how far the content should move: down the page is positive.
        CGEvent(scrollWheelEvent2Source: source, units: .pixel, wheelCount: 2, wheel1: Int32(-(v.dy ?? 0)), wheel2: Int32(-(v.dx ?? 0)), wheel3: 0)?.post(tap: .cghidEventTap)
    }

    private static let named: [String: CGKeyCode] = [
        "NAMED_KEY_RETURN": 36, "NAMED_KEY_TAB": 48, "NAMED_KEY_SPACE": 49, "NAMED_KEY_BACKSPACE": 51, "NAMED_KEY_ESCAPE": 53,
        "NAMED_KEY_LEFT": 123, "NAMED_KEY_RIGHT": 124, "NAMED_KEY_DOWN": 125, "NAMED_KEY_UP": 126,
        "NAMED_KEY_DELETE": 117, "NAMED_KEY_HOME": 115, "NAMED_KEY_END": 119, "NAMED_KEY_PAGE_UP": 116, "NAMED_KEY_PAGE_DOWN": 121,
    ]
    // The ANSI layout's keys, for shortcuts such as ⌘C.
    private static let characters: [Character: CGKeyCode] = [
        "a": 0, "s": 1, "d": 2, "f": 3, "h": 4, "g": 5, "z": 6, "x": 7, "c": 8, "v": 9, "b": 11, "q": 12, "w": 13, "e": 14, "r": 15,
        "y": 16, "t": 17, "1": 18, "2": 19, "3": 20, "4": 21, "6": 22, "5": 23, "=": 24, "9": 25, "7": 26, "-": 27, "8": 28, "0": 29,
        "]": 30, "o": 31, "u": 32, "[": 33, "i": 34, "p": 35, "l": 37, "j": 38, "'": 39, "k": 40, ";": 41, "\\": 42, ",": 43, "/": 44,
        "n": 45, "m": 46, ".": 47, "`": 50, " ": 49,
    ]

    private func key(_ v: InputMessage.Key) {
        var code: CGKeyCode?
        if let k = v.key, let c = Input.named[k] { code = c } else if let ch = v.character?.lowercased().first { code = Input.characters[ch] }
        guard let code else { return }
        var flags: CGEventFlags = []
        if v.command == true { flags.insert(.maskCommand) }
        if v.option == true { flags.insert(.maskAlternate) }
        if v.control == true { flags.insert(.maskControl) }
        if v.shift == true { flags.insert(.maskShift) }
        for down in [true, false] {
            let e = CGEvent(keyboardEventSource: source, virtualKey: code, keyDown: down)
            e?.flags = flags
            e?.post(tap: .cghidEventTap)
        }
    }

    /// type enters text as it is, whatever input source the Mac has selected.
    private func type(_ text: String) {
        let units = Array(text.utf16)
        var i = 0
        while i < units.count {
            var n = min(16, units.count - i)
            if i + n < units.count, UTF16.isLeadSurrogate(units[i + n - 1]) { n -= 1 }  // keep a pair together
            let chunk = Array(units[i..<i + n])
            for down in [true, false] {
                let e = CGEvent(keyboardEventSource: source, virtualKey: 0, keyDown: down)
                e?.flags = []
                e?.keyboardSetUnicodeString(stringLength: chunk.count, unicodeString: chunk)
                e?.post(tap: .cghidEventTap)
            }
            i += n
        }
    }

    /// releaseAll lets go of every button still held, so that nothing stays
    /// pressed after the viewer left.
    func releaseAll() {
        guard log == nil else { return }
        for raw in held {
            let right = raw == CGMouseButton.right.rawValue
            CGEvent(mouseEventSource: source, mouseType: right ? .rightMouseUp : .leftMouseUp, mouseCursorPosition: last, mouseButton: right ? .right : .left)?.post(tap: .cghidEventTap)
        }
        held.removeAll()
    }
}
