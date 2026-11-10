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
let input = Input(logPath: inputLog)
var encoder: Encoder?
var capture: Capture?
var source: TestSource?
var awake: IOPMAssertionID = 0

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

let work = DispatchQueue(label: "hermesh.screen.main")
Thread.detachNewThread {
    readMessages { kind, payload in
        work.sync {
            switch kind {
            case .start:
                if let m = try? JSONDecoder().decode(StartMessage.self, from: payload) { start(m) }
            case .bitrate:
                if let m = try? JSONDecoder().decode(BitrateMessage.self, from: payload) { encoder?.setBitrate(m.bps) }
            case .keyframe:
                encoder?.requestKeyframe()
            case .input:
                input.handle(Data(payload))
            case .pointer:
                if let m = try? JSONDecoder().decode(PointerMessage.self, from: payload) { capture?.showPointer(m.shown) }
            default:
                break
            }
        }
    }
    // hermeshd closed the pipe, or is gone.
    work.sync { input.releaseAll() }
    exit(0)
}
dispatchMain()
