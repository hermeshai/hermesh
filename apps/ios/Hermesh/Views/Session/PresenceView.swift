import HermeshHer
import HermeshProto
import HermeshSessions
import SwiftUI
import UIKit

// Who a new session goes to (docs/her-conversations.md §3.1, §3.10): a coding agent, or Her.
// The one chosen stands in the middle with its name under it; the other waits small beside it,
// Agent on the left and Her on the right. Swiping horizontally tracks the finger 1:1 with soft
// rubber-banding at either end and settles on a velocity-preserving spring; tapping the smaller
// one swaps them with a gentle spring, while the background light shifts smoothly between Agent's
// cyan/violet and Her's lavender/pink.

enum NewSessionWho: String {
    case agent, her

    private static let key = "new-session-who"

    /// Who the last new session went to; the first time, Agent (D19).
    static func remembered(persistent: Bool) -> NewSessionWho {
        guard persistent else { return .agent }
        return UserDefaults.standard.string(forKey: key) == "her" ? .her : .agent
    }

    static func remember(_ who: NewSessionWho, persistent: Bool) {
        guard persistent else { return }
        UserDefaults.standard.set(who.rawValue, forKey: key)
    }
}

/// A new conversation with Her: the model chosen on this page, else the last Her model chosen on this machine, else Her's configured default; none known yet, hers to pick.
func herSelection(providers: [Hermesh_Session_V1_ProviderInstance] = [], machineID: String = "", prev: Hermesh_Session_V1_ModelSelection? = nil) -> Hermesh_Session_V1_ModelSelection {
    let her = providers.first { $0.instanceID == herAgent && providerUnusable($0, projectAgents: []) == nil }
    if let prev, prev.instanceID == herAgent, !prev.model.isEmpty, her?.models.contains(where: { $0.slug == prev.model }) == true {
        return prev
    }
    if !machineID.isEmpty, let last = SessionChoices.lastHerSelection(machineID), !last.model.isEmpty,
       her?.models.contains(where: { $0.slug == last.model }) == true {
        return last
    }
    if let her {
        return defaultSelection(her)
    }
    var sel = Hermesh_Session_V1_ModelSelection()
    sel.instanceID = herAgent
    return sel
}

func isDefaultHerSelection(_ her: Hermesh_Session_V1_ProviderInstance, _ sel: Hermesh_Session_V1_ModelSelection) -> Bool {
    let def = defaultSelection(her)
    return sel.model == def.model && (reasoningOf([her], sel)?.value ?? "") == (reasoningOf([her], def)?.value ?? "")
}

/// An agent for a new session on the machine: as last time there, else the one already chosen, else Claude, Codex, Gemini, or the first that works.
func agentSelection(providers: [Hermesh_Session_V1_ProviderInstance], machineID: String, prev: Hermesh_Session_V1_ModelSelection? = nil) -> Hermesh_Session_V1_ModelSelection? {
    let nonHer = providers.filter { $0.instanceID != herAgent }
    let usable = nonHer.filter { providerUnusable($0, projectAgents: []) == nil }
    if let last = SessionChoices.lastSelection(machineID), last.instanceID != herAgent,
       let lastInst = usable.first(where: { $0.instanceID == last.instanceID }),
       lastInst.models.contains(where: { $0.slug == last.model }) {
        return last
    }
    if let prev, !prev.instanceID.isEmpty, prev.instanceID != herAgent, usable.contains(where: { $0.instanceID == prev.instanceID }) {
        return prev
    }
    if let first = preferredProvider(nonHer) {
        return defaultSelection(first)
    }
    return nil
}

/// Soft ambient aurora behind the new-session prompt, continuously tinted by role progress (0 = Agent, 1 = Her).
struct NewSessionAurora: View {
    let progress: CGFloat
    @Environment(\.colorScheme) private var scheme
    @Environment(\.accessibilityReduceMotion) private var still
    @State private var drift = false

    var body: some View {
        let opacity = scheme == .dark ? 0.20 : 0.16
        AuroraLayer(progress: progress, opacity: opacity)
            .offset(x: still ? 0 : (drift ? 12 : -12), y: still ? 0 : (drift ? -6 : 6))
            .onAppear {
                guard !still else { return }
                withAnimation(.easeInOut(duration: 12).repeatForever(autoreverses: true)) {
                    drift = true
                }
            }
            .allowsHitTesting(false)
            .accessibilityHidden(true)
    }
}

private struct AuroraLayer: View, Animatable {
    var progress: CGFloat
    let opacity: Double

    var animatableData: CGFloat {
        get { progress }
        set { progress = newValue }
    }

    var body: some View {
        let t = Double(min(max(progress, 0), 1))
        let overshoot = progress - CGFloat(t)
        let c1 = Color(
            red: (8 + (179 - 8) * t) / 255,
            green: (145 + (188 - 145) * t) / 255,
            blue: (178 + (242 - 178) * t) / 255
        )
        let c2 = Color(
            red: (109 + (255 - 109) * t) / 255,
            green: (60 + (163 - 60) * t) / 255,
            blue: (240 + (190 - 240) * t) / 255
        )
        let o1 = CGSize(
            width: -110 + 45 * CGFloat(t) - overshoot * 28,
            height: -55 - 40 * CGFloat(t)
        )
        let o2 = CGSize(
            width: 105 - 25 * CGFloat(t) - overshoot * 28,
            height: -80 + 5 * CGFloat(t)
        )

        ZStack {
            Circle()
                .fill(RadialGradient(colors: [c1.opacity(opacity), .clear], center: .center, startRadius: 0, endRadius: 240))
                .frame(width: 480, height: 480)
                .offset(o1)
            Circle()
                .fill(RadialGradient(colors: [c2.opacity(opacity), .clear], center: .center, startRadius: 0, endRadius: 220))
                .frame(width: 440, height: 440)
                .offset(o2)
        }
    }
}

/// Role switcher at the top of a new session: Agent (Claude + Codex tilted tiles) on the left, Her (her cat) on the right.
struct NewSessionPresence: View {
    let who: NewSessionWho
    let progress: CGFloat
    let onWho: (NewSessionWho) -> Void
    @Environment(\.accessibilityReduceMotion) private var still

    var body: some View {
        VStack(spacing: 2) {
            ZStack {
                Button {
                    onWho(.agent)
                } label: {
                    ZStack {
                        AgentLogo(id: "claude", size: 40)
                            .shadow(color: .black.opacity(0.22), radius: 8, y: 4)
                            .rotationEffect(.degrees(-11))
                            .offset(x: -14, y: 4)
                        AgentLogo(id: "codex", size: 40)
                            .shadow(color: .black.opacity(0.22), radius: 8, y: 4)
                            .rotationEffect(.degrees(10))
                            .offset(x: 14, y: -4)
                    }
                    .frame(width: 76, height: 76)
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .modifier(RoleIconEffect(progress: progress, isHer: false))
                .accessibilityLabel(Text("Agent"))
                .accessibilityAddTraits(who == .agent ? .isSelected : [])
                .accessibilityIdentifier("presence-agent")

                Button {
                    onWho(.her)
                } label: {
                    HerAvatar(size: 72)
                        .shadow(color: Color(hex: "b3bcf2").opacity(0.35), radius: 10, y: 4)
                        .frame(width: 76, height: 76)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .modifier(RoleIconEffect(progress: progress, isHer: true))
                .accessibilityLabel(Text("Her"))
                .accessibilityAddTraits(who == .her ? .isSelected : [])
                .accessibilityIdentifier("presence-her")
            }
            .frame(width: 240, height: 84)

            ZStack {
                Text("Agent")
                    .modifier(RoleLabelEffect(progress: progress, isHer: false, reduceMotion: still))
                Text("Her")
                    .modifier(RoleLabelEffect(progress: progress, isHer: true, reduceMotion: still))
            }
            .font(.footnote)
            .foregroundStyle(.secondary)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(who == .her ? Text("Her") : Text("Agent"))
            .accessibilityIdentifier("presence-name")
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("presence")
    }
}

private struct RoleIconEffect: ViewModifier, Animatable {
    var progress: CGFloat
    let isHer: Bool

    var animatableData: CGFloat {
        get { progress }
        set { progress = newValue }
    }

    func body(content: Content) -> some View {
        let clamped = min(max(progress, 0), 1)
        let active = isHer ? clamped : (1 - clamped)
        let spacing = CGFloat(RoleSwipeMath.spacing)
        let offsetX = isHer ? spacing * (1 - progress) : -spacing * progress
        content
            .scaleEffect(0.46 + 0.54 * active)
            .opacity(0.50 + 0.50 * active)
            .saturation(0.40 + 0.60 * active)
            .offset(x: offsetX)
            .zIndex(active >= 0.5 ? 1 : 0)
    }
}

private struct RoleLabelEffect: ViewModifier, Animatable {
    var progress: CGFloat
    let isHer: Bool
    let reduceMotion: Bool

    var animatableData: CGFloat {
        get { progress }
        set { progress = newValue }
    }

    func body(content: Content) -> some View {
        let clamped = min(max(progress, 0), 1)
        let opacity = isHer ? max(0, 1 - (1 - clamped) * 2.2) : max(0, 1 - clamped * 2.2)
        let offsetX: CGFloat = reduceMotion ? 0 : (isHer ? 16 * (1 - progress) : -16 * progress)
        content
            .opacity(opacity)
            .offset(x: offsetX)
    }
}

/// Continuously crossfades and slides the Agent / Her prompt below the switcher as role progress moves 0 <-> 1.
struct RolePromptEffect: ViewModifier, Animatable {
    var progress: CGFloat
    let isHer: Bool
    let reduceMotion: Bool

    var animatableData: CGFloat {
        get { progress }
        set { progress = newValue }
    }

    func body(content: Content) -> some View {
        let clamped = min(max(progress, 0), 1)
        let opacity = isHer ? max(0, (clamped - 0.45) / 0.55) : max(0, 1 - clamped / 0.55)
        let offsetX: CGFloat = reduceMotion ? 0 : (isHer ? 56 * (1 - progress) : -56 * progress)
        let scale: CGFloat = reduceMotion ? 1 : (isHer ? 0.97 + 0.03 * clamped : 1 - 0.03 * clamped)
        content
            .opacity(opacity)
            .scaleEffect(scale)
            .offset(x: offsetX)
    }
}

final class RoleSwipePanRecognizer: UIPanGestureRecognizer, HorizontalSwipeBackRecognizer {
    var canSwipeRight = false
    var activeWindowFrame: CGRect = .zero

    func claimsRightSwipe(at pointInWindow: CGPoint) -> Bool {
        isEnabled && canSwipeRight && activeWindowFrame.contains(pointInWindow)
    }
}

/// Interactive horizontal swipe recognizer for New Session's Agent <-> Her switcher.
/// Cooperates with both the enclosing vertical ScrollView (keyboard dismissal) and SideMenuController (drawer swipe).
struct RoleSwipeGesture: UIGestureRecognizerRepresentable {
    let enabled: Bool
    let canSwipeRight: Bool
    let rubberBandRightOnAgent: Bool
    let activeWindowFrame: CGRect
    let onBegan: () -> Void
    let onChanged: (CGFloat) -> Void
    let onEnded: (CGFloat, CGFloat, Bool) -> Void

    func makeUIGestureRecognizer(context: Context) -> RoleSwipePanRecognizer {
        let pan = RoleSwipePanRecognizer()
        pan.maximumNumberOfTouches = 1
        pan.cancelsTouchesInView = true
        pan.delaysTouchesBegan = false
        pan.delaysTouchesEnded = false
        pan.delegate = context.coordinator
        sync(pan, coordinator: context.coordinator)
        return pan
    }

    func updateUIGestureRecognizer(_ recognizer: RoleSwipePanRecognizer, context: Context) {
        sync(recognizer, coordinator: context.coordinator)
    }

    func handleUIGestureRecognizerAction(_ recognizer: RoleSwipePanRecognizer, context: Context) {
        let tx = recognizer.translation(in: recognizer.view).x
        switch recognizer.state {
        case .began:
            onBegan()
            onChanged(tx)
        case .changed:
            onChanged(tx)
        case .ended:
            onEnded(tx, recognizer.velocity(in: recognizer.view).x, true)
        case .cancelled, .failed:
            onEnded(tx, 0, false)
        default:
            break
        }
    }

    func makeCoordinator(converter: CoordinateSpaceConverter) -> Coordinator { Coordinator() }

    private func sync(_ recognizer: RoleSwipePanRecognizer, coordinator: Coordinator) {
        recognizer.isEnabled = enabled
        recognizer.canSwipeRight = enabled && canSwipeRight
        recognizer.activeWindowFrame = activeWindowFrame
        coordinator.enabled = enabled
        coordinator.canSwipeRight = canSwipeRight
        coordinator.rubberBandRightOnAgent = rubberBandRightOnAgent
        coordinator.activeWindowFrame = activeWindowFrame
    }

    final class Coordinator: NSObject, UIGestureRecognizerDelegate {
        var enabled = false
        var canSwipeRight = false
        var rubberBandRightOnAgent = false
        var activeWindowFrame: CGRect = .zero

        func gestureRecognizer(_ gestureRecognizer: UIGestureRecognizer, shouldReceive touch: UITouch) -> Bool {
            guard enabled else { return false }
            var v: UIView? = touch.view
            while let cur = v {
                if cur is UITextField || cur is UITextView { return false }
                v = cur.superview
            }
            if !activeWindowFrame.isEmpty, let window = touch.window, !activeWindowFrame.contains(touch.location(in: window)) {
                return false
            }
            return true
        }

        func gestureRecognizerShouldBegin(_ gestureRecognizer: UIGestureRecognizer) -> Bool {
            guard enabled, let pan = gestureRecognizer as? UIPanGestureRecognizer else { return false }
            let v = pan.velocity(in: pan.view)
            guard abs(v.x) > abs(v.y) * 1.25 else { return false }
            // On Agent in drawer mode, a right-swipe opens the side menu instead of fighting it.
            if !canSwipeRight && !rubberBandRightOnAgent && v.x > 0 { return false }
            return true
        }

        func gestureRecognizer(_ gestureRecognizer: UIGestureRecognizer, shouldBeRequiredToFailBy otherGestureRecognizer: UIGestureRecognizer) -> Bool {
            if let scrollView = otherGestureRecognizer.view as? UIScrollView,
               otherGestureRecognizer === scrollView.panGestureRecognizer {
                return true
            }
            // When swiping Her back to Agent, beat SideMenuController's pan recognizer so the drawer stays closed.
            if canSwipeRight, otherGestureRecognizer is UIPanGestureRecognizer, otherGestureRecognizer !== gestureRecognizer {
                return true
            }
            return false
        }

        func gestureRecognizer(_ gestureRecognizer: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith otherGestureRecognizer: UIGestureRecognizer) -> Bool {
            false
        }
    }
}

/// What a new conversation with Her asks, and where she runs; offline, ConnectionLine says messages wait for her machine.
struct HerNewSessionAsk: View {
    let machine: HerMachine
    let envs: [HerMachine]
    let reachable: Bool
    var isActive = true
    let onMachine: (String) -> Void

    var body: some View {
        VStack(spacing: 14) {
            Text("What should Her do?")
                .font(.title.weight(.semibold))
                .multilineTextAlignment(.center)
                .accessibilityIdentifier(isActive ? "new-session-title" : "")

            VStack(spacing: 8) {
                if envs.count > 1 {
                    Menu {
                        Section("Environment") {
                            ForEach(envs, id: \.machineID) { m in
                                let note: String? = !m.online && !m.local ? String(localized: "Offline · messages wait until it’s back") : nil
                                Button { onMachine(m.machineID) } label: {
                                    if let note {
                                        Text(m.machineName)
                                        Text(note)
                                        if m.machineID == machine.machineID { Image(systemName: "checkmark") }
                                    } else {
                                        Label { Text(m.machineName) } icon: { if m.machineID == machine.machineID { Image(systemName: "checkmark") } }
                                    }
                                }
                                .accessibilityIdentifier("machine-\(m.machineName)")
                            }
                        }
                    } label: {
                        whereLabel(showChevron: true)
                    }
                    .tint(.secondary)
                    .accessibilityIdentifier(isActive ? "launch-her-where" : "")
                } else {
                    whereLabel(showChevron: false)
                        .accessibilityElement(children: .combine)
                        .accessibilityIdentifier(isActive ? "launch-her-where" : "")
                }
                ConnectionLine(machine: machine, linkUp: reachable, centered: true)
                    .frame(minHeight: 24)
            }
        }
    }

    private func whereLabel(showChevron: Bool) -> some View {
        HStack(spacing: 5) {
            HerSmallCat(size: 15)
            Text("Her on \(machine.machineName)").lineLimit(1)
            if !machine.online && !machine.local { Circle().fill(.tertiary).frame(width: 6, height: 6) }
            if showChevron {
                Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(.tertiary)
            }
        }
        .font(.subheadline)
        .foregroundStyle(.secondary)
    }
}
