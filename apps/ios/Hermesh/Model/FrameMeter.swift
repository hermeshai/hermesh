#if targetEnvironment(simulator)
import QuartzCore
import SwiftUI

/// For ToolStepsUITests, in the Simulator's many-steps fixture only (-DemoManyTools): every frame the screen draws,
/// counted since launch, with the late ones (a frame and a half or more after the one before) and the longest gap of
/// the last three seconds, as the label of a tiny element the test reads before and after what it measures.
@MainActor final class FrameMeter: NSObject {
    static let shared = FrameMeter()
    private var link: CADisplayLink?
    private var last: CFTimeInterval = 0
    private(set) var frames = 0
    private(set) var late = 0
    /// The gaps of the last three seconds: when each ended, and how long it was.
    private var recent: [(at: CFTimeInterval, gap: CFTimeInterval)] = []

    func start() {
        guard link == nil else { return }
        let l = CADisplayLink(target: self, selector: #selector(tick(_:)))
        l.add(to: .main, forMode: .common)
        link = l
    }

    @objc private func tick(_ l: CADisplayLink) {
        let expected = l.targetTimestamp - l.timestamp
        if last > 0 {
            let gap = l.timestamp - last
            frames += 1
            if gap > expected * 1.5 { late += 1 }
            recent.append((l.timestamp, gap))
            if let i = recent.firstIndex(where: { $0.at >= l.timestamp - 3 }), i > 0 { recent.removeFirst(i) }
        }
        last = l.timestamp
    }

    var summary: String {
        let longest = recent.map(\.gap).max() ?? 0
        return "frames \(frames) late \(late) ms \(Int((CACurrentMediaTime() * 1000).rounded())) longest3s \(Int((longest * 1000).rounded()))"
    }
}

struct FrameMeterView: View {
    @State private var text = ""

    var body: some View {
        Color.primary.opacity(0.02)
            .frame(width: 2, height: 2)
            .allowsHitTesting(false)
            .accessibilityElement()
            .accessibilityLabel(Text(verbatim: text))
            .accessibilityIdentifier("frame-meter")
            .task {
                FrameMeter.shared.start()
                // The label follows the counts a few times a second.
                while !Task.isCancelled {
                    text = FrameMeter.shared.summary
                    try? await Task.sleep(for: .milliseconds(100))
                }
            }
    }
}
#endif
