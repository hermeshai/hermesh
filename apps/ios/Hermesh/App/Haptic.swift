import SwiftUI

/// The haptics the app plays (Apple's HIG, Playing haptics): few, short, each at a moment that matters and always
/// with what the screen shows, each pattern for what Apple says it means. The system's own controls (toggles,
/// pickers, menus, swipe actions) play theirs; System Haptics off in Settings, none plays.
enum Haptic: String {
    /// A message left the composer.
    case sent
    /// Her's first words since the owner's.
    case reply
    /// An agent waits on the owner: a question, an approval, a card to fill.
    case asks
    /// Something the owner did and waited for went through: a pairing approved, an answer taken, a code scanned, a
    /// session's turn done, a screen's connection back.
    case success
    /// Something went wrong: the alert that says so, a message not delivered, a task that failed, a screen's session ended.
    case error
    /// Something may go wrong: a screen's connection dropped, and it tries again.
    case warning
    /// A message swiped far enough: letting go replies to it.
    case armed

    func play() {
        switch self {
        case .sent: UIImpactFeedbackGenerator(style: .light).impactOccurred()
        case .reply: UIImpactFeedbackGenerator(style: .soft).impactOccurred()
        case .asks: UIImpactFeedbackGenerator(style: .medium).impactOccurred()
        case .success: UINotificationFeedbackGenerator().notificationOccurred(.success)
        case .error: UINotificationFeedbackGenerator().notificationOccurred(.error)
        case .warning: UINotificationFeedbackGenerator().notificationOccurred(.warning)
        case .armed: UIImpactFeedbackGenerator(style: .light).impactOccurred()
        }
        if HapticLog.shared.on { HapticLog.shared.played.append(self) }
    }
}

/// What played, for UI tests (-HermeshHapticLog), which can't feel it: RootView shows it in an element of its own.
@Observable
final class HapticLog {
    static let shared = HapticLog()
    let on = ProcessInfo.processInfo.arguments.contains("-HermeshHapticLog")
    var played: [Haptic] = []
}

extension View {
    /// Under -HermeshHapticLog, the haptics that played, as an element of their own ("haptics", its value their names).
    @ViewBuilder func hapticLog() -> some View {
        if HapticLog.shared.on {
            overlay(alignment: .topLeading) {
                Color.clear.frame(width: 1, height: 1)
                    .accessibilityElement()
                    .accessibilityIdentifier("haptics")
                    .accessibilityValue(HapticLog.shared.played.map(\.rawValue).joined(separator: " "))
            }
        } else {
            self
        }
    }
}

// Temporary CI routing verification.
