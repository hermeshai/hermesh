import HermeshHer
import HermeshKit
import SwiftUI

/// The line above a page's box while the connection that page uses is down (docs/connection-status-demo.html): a
/// glass capsule, as the box is, saying why and what happens to what is sent meanwhile; gone once the connection is
/// back. What it says is `connectionNotice`'s (HermeshKit), as on the Mac: a short wait never shows, and once shown it
/// stays at least half a second. A tap opens the connection diagnostics, where Reconnect Now is.
struct ConnectionCapsule: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var phase
    @Environment(\.accessibilityReduceMotion) private var still
    /// The machine the page's connection goes to, when it has one.
    let machine: Machine?
    /// The connection the page uses is up, or the page needs none now.
    let linkUp: Bool
    /// Her's, on her page: the diagnostics show her connection too.
    var chat: HerChat? = nil
    var state: HerEnvState? = nil

    @State private var downSince: Date?
    @State private var shown: ConnectionNotice?
    @State private var shownAt = Date.distantPast
    @State private var diagnosing = false

    /// What the task that follows the connection reads of this view: it starts again when any of it changes (the
    /// view it began with is the one it sees).
    private struct Following: Equatable {
        var linkUp: Bool
        var active: Bool
        var machineOnline: Bool?
        var machineUnreachable: Bool?
    }

    var body: some View {
        ZStack {
            if let shown { capsule(shown) }
        }
        .frame(maxWidth: .infinity)
        .animation(still ? .easeOut(duration: 0.15) : .snappy(duration: 0.32, extraBounce: 0.08), value: shown)
        .task(id: Following(linkUp: linkUp, active: phase == .active, machineOnline: machine?.online, machineUnreachable: machine.map { !$0.unreachable.isEmpty })) { await follow() }
        .sheet(isPresented: $diagnosing) { ConnectionDiagnosticsSheet(machine: machine, chat: chat, state: state) }
    }

    /// Follows the connection while it is down, or while the line still shows.
    private func follow() async {
        if linkUp { downSince = nil } else if downSince == nil { downSince = .now }
        while !Task.isCancelled {
            let notice = phase == .active && !model.isDemo ? connectionNotice(facts(now: .now)) : nil
            if let notice {
                if shown == nil { shownAt = .now }
                if shown != notice { shown = notice }
            } else if shown != nil {
                let left = ConnectionWaits.shownAtLeast - Date.now.timeIntervalSince(shownAt)
                if left > 0 {
                    try? await Task.sleep(for: .seconds(left))
                    continue
                }
                shown = nil
            }
            if linkUp && shown == nil { return }
            try? await Task.sleep(for: .milliseconds(250))
        }
    }

    private func facts(now: Date) -> ConnectionFacts {
        ConnectionFacts(
            noNetwork: model.networkKind == "offline",
            relayUp: model.status.relayConnected,
            relayDownFor: model.relayDownSince.map { now.timeIntervalSince($0) } ?? 0,
            machineLocal: machine?.local ?? false,
            machineOnline: machine?.online ?? true,
            machineUnreachable: !(machine?.unreachable.isEmpty ?? true),
            linkUp: linkUp,
            linkDownFor: downSince.map { now.timeIntervalSince($0) } ?? 0
        )
    }

    private func capsule(_ notice: ConnectionNotice) -> some View {
        Button { diagnosing = true } label: {
            HStack(spacing: 7) {
                glyph(notice)
                words(notice)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .shimmer(notice.retrying, paused: phase != .active)
                Image(systemName: "chevron.right").font(.caption2.weight(.semibold)).foregroundStyle(.tertiary)
            }
            .font(.subheadline.weight(.medium))
            .padding(.leading, 12).padding(.trailing, 13)
            .frame(height: 34)
            .contentShape(.capsule)
            .glassEffect(.regular.interactive(), in: .capsule)
        }
        .buttonStyle(.plain)
        .accessibilityHint(Text("Opens connection diagnostics"))
        .accessibilityIdentifier("connection-capsule")
        .padding(.horizontal, 16)
        .padding(.bottom, 8)
        .transition(still ? .opacity : .opacity.combined(with: .scale(scale: 0.9, anchor: .bottom)).combined(with: .offset(y: 6)))
    }

    @ViewBuilder private func glyph(_ notice: ConnectionNotice) -> some View {
        switch notice {
        case .noNetwork: Image(systemName: "wifi.slash").foregroundStyle(.orange)
        case .cantReachHermesh: Image(systemName: "icloud.slash").foregroundStyle(.orange)
        // Offline as the machine lists mark it: a ring.
        case .offline: Circle().strokeBorder(.secondary, lineWidth: 1.5).frame(width: 8, height: 8)
        case .connecting, .notResponding: BreathingDot(paused: phase != .active)
        }
    }

    private func words(_ notice: ConnectionNotice) -> Text {
        let name = machine.map { $0.machineName.isEmpty ? $0.machineID : $0.machineName }
        switch notice {
        case .noNetwork: return Text("No network connection")
        case .connecting: return name.map { Text("Connecting to \($0)…") } ?? Text("Connecting to Hermesh…")
        case .cantReachHermesh: return Text("Can’t reach Hermesh · Retrying")
        case .offline: return Text("**\(name ?? "")** is offline · messages wait until it’s back")
        case .notResponding: return Text("**\(name ?? "")** isn’t responding · Retrying")
        }
    }
}

/// An orange dot that breathes: something is being tried again. Still with Reduce Motion.
private struct BreathingDot: View {
    let paused: Bool
    @Environment(\.accessibilityReduceMotion) private var still

    var body: some View {
        TimelineView(.animation(minimumInterval: 1 / 20, paused: paused || still)) { timeline in
            let t = paused || still ? 0.0 : timeline.date.timeIntervalSinceReferenceDate
            Circle()
                .fill(.orange.opacity(paused || still ? 1 : 0.35 + 0.65 * (0.5 + 0.5 * sin(t * 3.8))))
                .frame(width: 7, height: 7)
        }
        .accessibilityHidden(true)
    }
}

/// The connection diagnostics, from the line above a page's box: the page's machine first, and Her's connection on
/// her page.
struct ConnectionDiagnosticsSheet: View {
    @Environment(\.dismiss) private var dismiss
    let machine: Machine?
    let chat: HerChat?
    let state: HerEnvState?

    var body: some View {
        NavigationStack {
            NetworkDiagnosticsList(
                focusMachine: machine,
                focusChat: chat,
                focusState: state,
                reconnectID: "connection-diagnostics-reconnect",
                onReconnect: { dismiss() }
            )
            .navigationTitle("Connection Diagnostics")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
        .presentationDetents([.medium, .large])
    }
}
