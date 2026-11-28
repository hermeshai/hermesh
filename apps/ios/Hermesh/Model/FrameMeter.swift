#if targetEnvironment(simulator)
import Darwin
import QuartzCore
import SwiftUI

/// Simulator performance meter (-DemoManyTools / -DemoPerfSuite): counts every display frame since launch, late frames
/// (> 1.5x frame interval), severe hitches (> 3.0x), cumulative hitch excess ms, longest frame gaps over 3s and 1.5s,
/// cumulative process CPU time (user + system ms), and physical memory footprint (KiB).
@MainActor final class FrameMeter: NSObject {
    static let shared = FrameMeter()
    private var link: CADisplayLink?
    private var last: CFTimeInterval = 0
    private(set) var frames = 0
    private(set) var late = 0
    private(set) var severe = 0
    private var hitchSeconds: CFTimeInterval = 0
    /// The gaps of the last three seconds: when each ended, and how long it was.
    private var recent: [(at: CFTimeInterval, gap: CFTimeInterval)] = []

    func start() {
        guard link == nil else { return }
        let l = CADisplayLink(target: self, selector: #selector(tick(_:)))
        l.add(to: .main, forMode: .common)
        link = l
    }

    @objc private func tick(_ l: CADisplayLink) {
        let expected = max(1.0 / 120.0, l.targetTimestamp - l.timestamp)
        if last > 0 {
            let gap = l.timestamp - last
            frames += 1
            if gap > expected * 1.5 {
                late += 1
                hitchSeconds += gap - expected
            }
            if gap > expected * 3.0 {
                severe += 1
            }
            recent.append((l.timestamp, gap))
            if let i = recent.firstIndex(where: { $0.at >= l.timestamp - 3 }), i > 0 { recent.removeFirst(i) }
        }
        last = l.timestamp
    }

    private static func processCPUMilliseconds() -> Int {
        var usage = rusage()
        guard getrusage(RUSAGE_SELF, &usage) == 0 else { return 0 }
        let userMs = Double(usage.ru_utime.tv_sec) * 1000 + Double(usage.ru_utime.tv_usec) / 1000
        let sysMs = Double(usage.ru_stime.tv_sec) * 1000 + Double(usage.ru_stime.tv_usec) / 1000
        return Int((userMs + sysMs).rounded())
    }

    private static func physicalFootprintKiB() -> Int {
        var vm = task_vm_info_data_t()
        var count = mach_msg_type_number_t(MemoryLayout<task_vm_info_data_t>.size / MemoryLayout<natural_t>.size)
        let kr = withUnsafeMutablePointer(to: &vm) {
            $0.withMemoryRebound(to: integer_t.self, capacity: Int(count)) {
                task_info(mach_task_self_, task_flavor_t(TASK_VM_INFO), $0, &count)
            }
        }
        return kr == KERN_SUCCESS ? Int(vm.phys_footprint / 1024) : 0
    }

    var summary: String {
        let now = CACurrentMediaTime()
        let longest3s = recent.map(\.gap).max() ?? 0
        let maxGap1500 = recent.lazy.filter { $0.at >= now - 1.5 }.map(\.gap).max() ?? 0
        let hitchMs = Int((hitchSeconds * 1000).rounded())
        let cpuMs = Self.processCPUMilliseconds()
        let memKB = Self.physicalFootprintKiB()
        return "frames \(frames) late \(late) ms \(Int((now * 1000).rounded())) longest3s \(Int((longest3s * 1000).rounded())) severe \(severe) hitchMs \(hitchMs) maxGap1500 \(Int((maxGap1500 * 1000).rounded())) cpuMs \(cpuMs) memKB \(memKB)"
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
