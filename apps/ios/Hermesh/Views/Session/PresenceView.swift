import HermeshHer
import HermeshProto
import HermeshSessions
import SwiftUI

// Who a new session goes to (docs/her-conversations.md §3.1, §3.10): a coding agent, or Her.
// The one chosen stands in the middle with its name under it; the other waits small beside it,
// Agent on the left and Her on the right. Tapping the smaller one swaps them with a gentle spring,
// while the background light shifts between Agent's cyan/violet and Her's lavender/pink.

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

/// A new conversation with Her: the model chosen when it is one of hers, else her default; none known yet, hers to pick.
func herSelection(providers: [Hermesh_Session_V1_ProviderInstance] = [], prev: Hermesh_Session_V1_ModelSelection? = nil) -> Hermesh_Session_V1_ModelSelection {
    let her = providers.first { $0.instanceID == herAgent && providerUnusable($0, projectAgents: []) == nil }
    if let prev, prev.instanceID == herAgent, her?.models.contains(where: { $0.slug == prev.model }) == true {
        return prev
    }
    if let her {
        return defaultSelection(her)
    }
    var sel = Hermesh_Session_V1_ModelSelection()
    sel.instanceID = herAgent
    return sel
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

/// Soft ambient aurora behind the new-session prompt, tinted by who is chosen (presence.css .pr-aura).
struct NewSessionAurora: View {
    let who: NewSessionWho
    @Environment(\.colorScheme) private var scheme
    @Environment(\.accessibilityReduceMotion) private var still
    @State private var drift = false

    var body: some View {
        let opacity = scheme == .dark ? 0.20 : 0.16
        let c1 = who == .her ? Color(hex: "b3bcf2") : Color(hex: "0891b2")
        let c2 = who == .her ? Color(hex: "ffa3be") : Color(hex: "6d3cf0")
        let o1 = who == .her ? CGSize(width: -65, height: -95) : CGSize(width: -110, height: -55)
        let o2 = who == .her ? CGSize(width: 80, height: -75) : CGSize(width: 105, height: -80)

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
        .offset(x: still ? 0 : (drift ? 12 : -12), y: still ? 0 : (drift ? -6 : 6))
        .animation(still ? .easeInOut(duration: 0.35) : .easeInOut(duration: 1.2), value: who)
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

/// Role switcher at the top of a new session: Agent (Claude + Codex tilted tiles) on the left, Her (her cat) on the right.
struct NewSessionPresence: View {
    let who: NewSessionWho
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
                .scaleEffect(who == .agent ? 1 : 0.46)
                .opacity(who == .agent ? 1 : 0.5)
                .saturation(who == .agent ? 1 : 0.4)
                .offset(x: who == .agent ? 0 : -88)
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
                .scaleEffect(who == .her ? 1 : 0.46)
                .opacity(who == .her ? 1 : 0.5)
                .saturation(who == .her ? 1 : 0.4)
                .offset(x: who == .her ? 0 : 88)
                .accessibilityLabel(Text("Her"))
                .accessibilityAddTraits(who == .her ? .isSelected : [])
                .accessibilityIdentifier("presence-her")
            }
            .frame(width: 240, height: 84)
            .animation(still ? .easeOut(duration: 0.2) : .spring(response: 0.55, dampingFraction: 0.72), value: who)

            Text(who == .her ? String(localized: "Her") : String(localized: "Agent"))
                .font(.footnote)
                .foregroundStyle(.secondary)
                .accessibilityIdentifier("presence-name")
        }
        .sensoryFeedback(.selection, trigger: who)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("presence")
    }
}

/// What a new conversation with Her asks, and where she runs; offline, ConnectionLine says messages wait for her machine.
struct HerNewSessionAsk: View {
    let machine: HerMachine
    let envs: [HerMachine]
    let reachable: Bool
    let onMachine: (String) -> Void

    var body: some View {
        VStack(spacing: 14) {
            Text("What should Her do?")
                .font(.title.weight(.semibold))
                .multilineTextAlignment(.center)
                .accessibilityIdentifier("new-session-title")

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
                    .accessibilityIdentifier("launch-her-where")
                } else {
                    whereLabel(showChevron: false)
                        .accessibilityElement(children: .combine)
                        .accessibilityIdentifier("launch-her-where")
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
