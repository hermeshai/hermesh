import CoreGraphics
import Foundation
import IOKit.pwr_mgt

// hermesh-screen [--test-source] [--input-log <file>] | --status
//
// Started by hermeshd for one Computer Control session
// (docs/computer-control.md §4.1). It captures the main display, encodes it
// and writes the frames to stdout; it reads the viewer's input from stdin
// and posts it. When stdin closes it lets go of what it holds and exits.

var testSource = false
var inputLog: String?
var args = CommandLine.arguments.dropFirst().makeIterator()
while let a = args.next() {
    switch a {
    case "--status":
        // As hermeshd's child, this answers for hermeshd: macOS holds it responsible.
        let status: [String: Bool] = ["screen_recording": CGPreflightScreenCaptureAccess(), "accessibility": Input.canControl]
        FileHandle.standardOutput.write(try! JSONSerialization.data(withJSONObject: status))
        exit(0)
    case "--test-source": testSource = true
    case "--input-log": inputLog = args.next()
    default:
        FileHandle.standardError.write(Data("hermesh-screen: unknown argument \(a)\n".utf8))
        exit(2)
    }
}

signal(SIGPIPE, SIG_IGN)

let out = Output()
var encoder: Encoder?
var capture: Capture?
var source: TestSource?
var awake: IOPMAssertionID = 0

let input = Input(logPath: inputLog)

func canControl() -> Bool { inputLog != nil || Input.canControl }

func start(_ m: StartMessage) {
    guard encoder == nil else { return }
    let fps = m.fps ?? 30
    let enc = Encoder(out: out, fps: fps, bitrate: m.bitrate ?? 3_000_000)
    encoder = enc
    if testSource {
        out.send(.state, json: StateMessage(error: "", detail: "", can_control: canControl()))
        let s = TestSource(encoder: enc)
        s.start(fps: fps)
        source = s
        return
    }
    if !CGPreflightScreenCaptureAccess() {
        // Lists Hermesh under Screen & System Audio Recording, and asks the first time.
        CGRequestScreenCaptureAccess()
        out.send(.state, json: StateMessage(error: "screen_recording", detail: "", can_control: canControl()))
        return
    }
    // Wake the display, and keep it awake while the viewer looks.
    var wake: IOPMAssertionID = 0
    IOPMAssertionDeclareUserActivity("Hermesh Computer Control" as CFString, kIOPMUserActiveLocal, &wake)
    IOPMAssertionCreateWithName(kIOPMAssertionTypePreventUserIdleDisplaySleep as CFString, IOPMAssertionLevel(kIOPMAssertionLevelOn), "Hermesh Computer Control" as CFString, &awake)
    let cap = Capture(out: out, encoder: enc)
    capture = cap
    Task {
        do {
            try await cap.start(maxEdge: m.max_edge ?? 1920, fps: fps, pointer: m.pointer ?? true)
            out.send(.state, json: StateMessage(error: "", detail: "", can_control: canControl()))
        } catch {
            out.send(.state, json: StateMessage(error: "capture", detail: error.localizedDescription, can_control: canControl()))
        }
    }
}

let work = DispatchQueue(label: "hermesh.screen.main", qos: .userInteractive)
// The owner's input is read on a thread the system runs at once, as it does a person's own.
let reader = Thread {
    readMessages { kind, payload in
        if kind == .input {
            // Straight on, without waiting for whatever else is being set up.
            input.handle(Data(payload))
            return
        }
        work.sync {
            switch kind {
            case .start:
                if let m = try? JSONDecoder().decode(StartMessage.self, from: payload) { start(m) }
            case .bitrate:
                if let m = try? JSONDecoder().decode(BitrateMessage.self, from: payload) {
                    encoder?.setBitrate(m.bps)
                    capture?.follow(bitrate: m.bps)
                }
            case .keyframe:
                if let capture { capture.keyframe() } else { encoder?.requestKeyframe() }
            case .pointer:
                if let m = try? JSONDecoder().decode(PointerMessage.self, from: payload) { capture?.showPointer(m.shown) }
            default:
                break
            }
        }
    }
    // hermeshd closed the pipe, or is gone.
    input.releaseAll()
    exit(0)
}
reader.qualityOfService = .userInteractive
reader.start()
dispatchMain()
