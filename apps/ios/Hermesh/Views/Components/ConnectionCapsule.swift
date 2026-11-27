import HermeshHer
import HermeshKit
import SwiftUI

/// Follows the connection a page uses while it is down (docs/connection-status-v2-demo.html): a short wait never
/// shows, and once shown it stays at least half a second (`ConnectionWaits.shownAtLeast`). Gives the current notice
/// and an action that opens `ConnectionDiagnosticsSheet` to whichever inline status slot the page already has.
struct FollowingConnection<Content: View>: View {
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
    @ViewBuilder let content: (ConnectionNotice?, @escaping () -> Void) -> Content

    @State private var downSince: Date?
    @State private var shown: ConnectionNotice?
    @State private var shownAt = Date.distantPast
    @State private var diagnosing = false

    private struct Following: Equatable {
        var linkUp: Bool
        var active: Bool
        var machineOnline: Bool?
        var machineUnreachable: Bool?
    }

    var body: some View {
        ZStack {
            content(shown, { diagnosing = true })
        }
        .animation(still ? .easeOut(duration: 0.15) : .easeOut(duration: 0.2), value: shown)
        .task(id: Following(linkUp: linkUp, active: phase == .active, machineOnline: machine?.online, machineUnreachable: machine.map { !$0.unreachable.isEmpty })) { await follow() }
        .sheet(isPresented: $diagnosing) { ConnectionDiagnosticsSheet(machine: machine, chat: chat, state: state) }
    }

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
}

/// Active work on a session turn, merged in place with any connection notice at the end of the conversation.
struct SessionWorkStatus: Equatable {
    var text: String
    var since: Date?
    var thinkingText: String? = nil
}

/// Live chain-of-thought preview under the active working line: shows the latest
/// three lines anchored to the bottom with a top fade when overflowing, and opens
/// inline to a scrollable full view on tap. Unmounts cleanly once thinking ends.
struct LiveThinkingView<Header: View>: View {
    let text: String
    @ViewBuilder let header: () -> Header

    @State private var open = false
    @State private var atEnd = true
    @State private var measuredHeight: CGFloat = 0
    @ScaledMetric(relativeTo: .footnote) private var lineHeight: CGFloat = 19

    private var bodyText: String {
        text.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private var peekHeight: CGFloat { lineHeight * 3 }
    private var overflows: Bool { measuredHeight > peekHeight + 1 }

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            if bodyText.isEmpty {
                header()
            } else {
                Button {
                    atEnd = true
                    withAnimation(.easeOut(duration: 0.16)) { open.toggle() }
                } label: {
                    HStack(alignment: .center, spacing: 4) {
                        header()
                        Image(systemName: "chevron.down")
                            .font(.caption2.weight(.semibold))
                            .foregroundStyle(.tertiary)
                            .rotationEffect(.degrees(open ? 0 : -90))
                    }
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("live-thinking-toggle")
                .accessibilityValue(open ? "expanded" : "collapsed")

                ScrollViewReader { proxy in
                    ScrollView(.vertical) {
                        VStack(alignment: .leading, spacing: 0) {
                            Text(verbatim: bodyText)
                                .font(.footnote)
                                .foregroundStyle(.secondary)
                                .lineSpacing(2)
                                .textSelection(.enabled)
                                .fixedSize(horizontal: false, vertical: true)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { measuredHeight = $0 }
                            Color.clear.frame(height: 1).id("live-thinking-end")
                        }
                    }
                    .scrollDisabled(!open)
                    .scrollIndicators(open ? .automatic : .hidden)
                    .defaultScrollAnchor(.bottom)
                    .onScrollGeometryChange(for: Bool.self) { g in
                        (g.contentSize.height - g.contentOffset.y - g.containerSize.height) < 12
                    } action: { _, near in
                        if open { atEnd = near }
                    }
                    .onChange(of: bodyText) { _, _ in
                        if !open || atEnd {
                            proxy.scrollTo("live-thinking-end", anchor: .bottom)
                        }
                    }
                    .onChange(of: open) { _, isOpen in
                        if !isOpen || atEnd {
                            proxy.scrollTo("live-thinking-end", anchor: .bottom)
                        }
                    }
                    .frame(height: open
                        ? min(max(measuredHeight, lineHeight), 220)
                        : (measuredHeight > 0 ? min(measuredHeight, peekHeight) : peekHeight))
                    .mask {
                        if !open && overflows {
                            VStack(spacing: 0) {
                                LinearGradient(colors: [.clear, .black], startPoint: .top, endPoint: .bottom)
                                    .frame(height: 22)
                                Color.black
                            }
                        } else {
                            Color.black
                        }
                    }
                    .accessibilityIdentifier("live-thinking-body")
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// The inline status line at the end of a session's conversation (or under the environment picker on a new session):
/// what the agent is doing while the connection is up, the connection state while idle and disconnected, or both in
/// one line while a running session reconnects. A tap on a connection notice opens `ConnectionDiagnosticsSheet`.
struct ConnectionLine: View {
    @Environment(\.scenePhase) private var phase
    let machine: Machine?
    let linkUp: Bool
    var work: SessionWorkStatus? = nil
    var centered = false

    var body: some View {
        FollowingConnection(machine: machine, linkUp: linkUp) { notice, openDiagnostics in
            if let notice {
                if let work {
                    HStack(spacing: 0) {
                        noticeButton(notice, workText: work.text, openDiagnostics: openDiagnostics)
                    }
                    .frame(maxWidth: centered ? nil : .infinity, alignment: centered ? .center : .leading)
                    .accessibilityElement(children: .contain)
                    .accessibilityIdentifier("thread-working")
                } else {
                    noticeButton(notice, workText: nil, openDiagnostics: openDiagnostics)
                        .frame(maxWidth: centered ? nil : .infinity, alignment: centered ? .center : .leading)
                }
            } else if let work {
                if let thinking = work.thinkingText, !thinking.isEmpty {
                    HStack(alignment: .top, spacing: 8) {
                        MovingMark(size: 18, motion: .seq)
                        LiveThinkingView(text: thinking) {
                            HStack(alignment: .firstTextBaseline, spacing: 6) {
                                Text(verbatim: work.text)
                                    .foregroundStyle(.secondary)
                                    .shimmer(true, paused: phase != .active)
                                    .lineLimit(2)
                                    .multilineTextAlignment(centered ? .center : .leading)
                                if let since = work.since {
                                    Text(since, style: .timer)
                                        .monospacedDigit()
                                        .foregroundStyle(.tertiary)
                                        .lineLimit(1)
                                        .fixedSize()
                                }
                            }
                            .accessibilityElement(children: .combine)
                            .accessibilityIdentifier("thread-working")
                        }
                    }
                    .font(.footnote)
                    .frame(maxWidth: centered ? nil : .infinity, alignment: centered ? .center : .leading)
                } else {
                    HStack(alignment: .top, spacing: 8) {
                        MovingMark(size: 18, motion: .seq)
                        Text(verbatim: work.text)
                            .foregroundStyle(.secondary)
                            .lineLimit(2)
                            .multilineTextAlignment(centered ? .center : .leading)
                        if let since = work.since {
                            Text(since, style: .timer)
                                .monospacedDigit()
                                .foregroundStyle(.tertiary)
                                .lineLimit(1)
                                .fixedSize()
                        }
                    }
                    .font(.footnote)
                    .frame(maxWidth: centered ? nil : .infinity, alignment: centered ? .center : .leading)
                    .accessibilityElement(children: .combine)
                    .accessibilityIdentifier("thread-working")
                }
            }
        }
    }

    private func noticeButton(_ notice: ConnectionNotice, workText: String?, openDiagnostics: @escaping () -> Void) -> some View {
        Button(action: openDiagnostics) {
            HStack(alignment: .top, spacing: 8) {
                connectionGlyph(notice, markSize: 18)
                Group {
                    if let workText, notice.retrying {
                        Text("\(stripTrailingEllipsis(workText)) · \(connectionNoticeWords(notice, machine: machine))")
                            .shimmer(true, paused: phase != .active)
                    } else {
                        connectionNoticeWords(notice, machine: machine)
                            .shimmer(notice.retrying, paused: phase != .active)
                    }
                }
                .foregroundStyle(.secondary)
                .lineLimit(workText == nil ? 1 : 2)
                .multilineTextAlignment(centered ? .center : .leading)
                Image(systemName: "chevron.right").font(.caption2.weight(.semibold)).foregroundStyle(.tertiary)
                    .padding(.top, 3)
            }
            .font(.footnote)
            .padding(.vertical, 3)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityHint(Text("Opens connection diagnostics"))
        .accessibilityIdentifier("connection-line")
        .transition(.opacity)
    }
}

/// The `···` bubble at the foot of Her's conversation while Her's connection is down: swept by the same 2.4 s light
/// while retrying, still while offline or without network. A tap opens `ConnectionDiagnosticsSheet`.
struct HerConnectionBubble: View {
    @Environment(\.scenePhase) private var phase
    @Environment(\.accessibilityReduceMotion) private var still
    let notice: ConnectionNotice
    let machine: Machine?
    let openDiagnostics: () -> Void

    private static let sweep = 2.4

    var body: some View {
        Button(action: openDiagnostics) {
            HStack(spacing: 10) {
                if notice.retrying {
                    TimelineView(.animation(paused: phase != .active || still)) { t in
                        let swept = t.date.timeIntervalSinceReferenceDate / Self.sweep
                        let sweepPhase = swept - swept.rounded(.down)
                        HStack(spacing: 8) {
                            HStack(spacing: 3) {
                                ForEach(0 ..< 3, id: \.self) { _ in Circle().fill(.secondary).frame(width: 5, height: 5) }
                            }
                            connectionNoticeWords(notice, machine: machine)
                                .font(.subheadline)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                        }
                        .mask { if phase != .active || still { Rectangle() } else { sweepLight(at: sweepPhase) } }
                    }
                } else {
                    HStack(spacing: 8) {
                        connectionGlyph(notice, markSize: 16)
                        connectionNoticeWords(notice, machine: machine)
                            .font(.subheadline)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                }
                Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(.tertiary)
            }
            .padding(.horizontal, 15)
            .frame(minHeight: 44)
            .background(Palette.reply, in: .capsule)
            .overlay(Capsule().strokeBorder(Palette.separator, lineWidth: 0.5))
            .contentShape(.capsule)
        }
        .buttonStyle(.plain)
        .accessibilityHint(Text("Opens connection diagnostics"))
        .accessibilityIdentifier("connection-line")
        .transition(.opacity)
    }

    private func sweepLight(at phase: Double) -> some View {
        GeometryReader { g in
            let w = g.size.width
            LinearGradient(stops: [.init(color: .black, location: 0.38), .init(color: .black.opacity(0.28), location: 0.5), .init(color: .black, location: 0.62)],
                           startPoint: .leading, endPoint: .trailing)
                .frame(width: w * 3)
                .offset(x: -2 * w + 2 * w * phase)
        }
    }
}

@ViewBuilder func connectionGlyph(_ notice: ConnectionNotice, markSize: CGFloat) -> some View {
    switch notice {
    case .noNetwork: Image(systemName: "wifi.slash").font(.footnote.weight(.medium)).foregroundStyle(.orange)
    case .cantReachHermesh: Image(systemName: "icloud.slash").font(.footnote.weight(.medium)).foregroundStyle(.orange)
    case .offline: Circle().strokeBorder(.secondary, lineWidth: 1.5).frame(width: 8, height: 8)
    case .connecting, .notResponding: MovingMark(size: markSize, motion: .wait)
    }
}

func connectionNoticeWords(_ notice: ConnectionNotice, machine: Machine?) -> Text {
    let name = machine.map { $0.machineName.isEmpty ? $0.machineID : $0.machineName }
    switch notice {
    case .noNetwork: return Text("No network connection")
    case .connecting: return name.map { Text("Connecting to \($0)…") } ?? Text("Connecting to Hermesh…")
    case .cantReachHermesh: return Text("Can’t reach Hermesh · Retrying")
    case .offline: return Text("**\(name ?? "")** is offline · messages wait until it’s back")
    case .notResponding: return Text("**\(name ?? "")** isn’t responding · Retrying")
    }
}

private func stripTrailingEllipsis(_ text: String) -> String {
    if text.hasSuffix("…") { return String(text.dropLast()) }
    if text.hasSuffix("...") { return String(text.dropLast(3)) }
    return text
}

/// The connection diagnostics sheet: the page's machine first, and Her's connection on her page.
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
