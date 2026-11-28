import HermeshProto
import HermeshSessions
import SwiftUI

/// Standalone Sessions Hub page on iPhone Duo (`Root.sessions`, opened from the `</>` tab in the vertical `UITabBar`):
/// pairs a live network-aware environment filter strip at the top with the full `MenuSessions` session list (reusing
/// all groupings, sorting, drag reorder, archived toggle, Git delivery marks and Her sub-session trees), defaulting to
/// two-line metadata rows for scannability while relying on the adjacent `💬` tab to start a new session.
struct SessionsHubView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var phase
    @Environment(\.barsOnTheSide) private var sideBars
    @Environment(\.menuCoversPage) private var menuCoversPage
    @Environment(\.self) private var env
    @AppStorage("sessionsHubMetadata") private var hubMetadata = true
    @State private var selectedMachineID: String?
    @State private var diagnosticsMachine: Machine?
    @State private var diagnosticsPresented = false

    private var duoClosedPhysicalBar: Bool {
        guard !model.menuBeside else { return false }
        if #available(iOS 27.1, *) { return env.toolbarVerticalEdge != nil }
        return false
    }
    private var ownsVerticalBar: Bool { sideBars && !menuCoversPage }
    private var animPaused: Bool { phase != .active || menuCoversPage }

    private var selectedMachine: Machine? {
        guard let id = selectedMachineID else { return nil }
        return model.machines.first { $0.machineID == id }
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            List {
                if !model.machines.isEmpty {
                    environmentSection.plainRow()
                }
                if !model.unreachableMachines.isEmpty {
                    UnreachableNotice()
                        .padding(.vertical, 6)
                        .plainRow()
                } else if let m = selectedMachine, model.status.relayConnected, !m.online, !m.local {
                    offlineMachineBanner(m)
                        .plainRow()
                }
                MenuSessions(machineFilter: selectedMachineID, metadataOverride: hubMetadata)
            }
            .listStyle(.plain)
            .scrollContentBackground(.hidden)
            .environment(\.defaultMinListRowHeight, 0)
            .scrollIndicators(.hidden)
            .scrollDismissesKeyboard(.immediately)
            .onScrollPhaseChange { _, phase in if phase == .interacting { hideKeyboard() } }
            .refreshable {
                if !model.status.relayConnected {
                    model.reconnectNow()
                }
                await model.refreshGitStatus(force: true)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Color(.systemBackground))
        .ignoresSafeArea(edges: duoClosedPhysicalBar ? .top : [])
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(ownsVerticalBar ? .visible : .hidden, for: .navigationBar)
        .toolbar {
            if !menuCoversPage {
                if sideBars {
                    MenuToolbarItem()
                    if #available(iOS 27.1, *) {
                        ToolbarItemGroup(placement: .topBarTrailing) {
                            Button("Search", systemImage: "magnifyingglass") { model.sheet = .search }
                                .accessibilityIdentifier("search")
                            ViewMenu(inBar: true, metadata: $hubMetadata)
                        }
                        .axisBehavior(.verticalPreferred)
                    } else {
                        ToolbarItem(placement: .topBarTrailing) {
                            Button("Search", systemImage: "magnifyingglass") { model.sheet = .search }
                                .accessibilityIdentifier("search")
                        }
                        ToolbarItem(placement: .topBarTrailing) {
                            ViewMenu(inBar: true, metadata: $hubMetadata)
                        }
                    }
                }
            }
        }
        .sheet(isPresented: $diagnosticsPresented) {
            ConnectionDiagnosticsSheet(machine: diagnosticsMachine ?? selectedMachine, chat: nil, state: nil)
        }
        .onChange(of: model.machines.map(\.machineID)) { _, ids in
            if let sel = selectedMachineID, !ids.contains(sel) {
                selectedMachineID = nil
            }
        }
        .task { await model.refreshGitStatus() }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("sessions-hub")
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 8) {
                if !sideBars {
                    GlassMenuButton()
                }
                Text("notificationSettings.sessions")
                    .font(.title.bold())
                    .foregroundStyle(.primary)
                    .accessibilityAddTraits(.isHeader)
                    .accessibilityIdentifier("sessions-hub-title")
                Spacer()
                if !sideBars {
                    Button {
                        model.sheet = .search
                    } label: {
                        Image(systemName: "magnifyingglass")
                            .font(.body.weight(.medium))
                            .frame(width: 44, height: 44)
                            .glassEffect(.regular.interactive(), in: .circle)
                            .contentShape(.circle)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Search")
                    .accessibilityIdentifier("search")
                    ViewMenu(metadata: $hubMetadata).frame(width: 44, height: 44)
                }
            }
            .frame(minHeight: 44)

            if let banner = connectionBannerState {
                Button {
                    diagnosticsMachine = selectedMachine
                    diagnosticsPresented = true
                } label: {
                    HStack(spacing: 7) {
                        switch banner.kind {
                        case .noNetwork:
                            Image(systemName: "wifi.slash")
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(.orange)
                        case .cantReach:
                            Image(systemName: "icloud.slash")
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(.orange)
                        case .syncing:
                            MovingMark(size: 13, motion: .seq, mono: true)
                        }
                        Text(banner.text)
                            .lineLimit(1)
                            .shimmer(banner.kind == .syncing, paused: animPaused)
                        Image(systemName: "chevron.right")
                            .font(.caption2.weight(.semibold))
                            .foregroundStyle(.tertiary)
                    }
                    .font(.footnote)
                    .foregroundStyle(banner.kind == .syncing ? Color.secondary : Color.orange)
                    .padding(.vertical, 2)
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityElement(children: .combine)
                .accessibilityHint(Text("Opens connection diagnostics"))
                .accessibilityIdentifier("sync-line")
                .transition(.opacity)
            }
        }
        .animation(.easeOut(duration: 0.2), value: connectionBannerState?.text)
        .padding(.leading, sideBars ? 24 : 16)
        .padding(.trailing, 16)
        .padding(.top, duoClosedPhysicalBar ? 24 : 8)
        .padding(.bottom, 8)
    }

    private enum BannerKind: Equatable {
        case noNetwork, cantReach, syncing
    }

    private var connectionBannerState: (kind: BannerKind, text: String)? {
        if model.networkKind == "offline" {
            return (.noNetwork, String(localized: "No network connection"))
        }
        guard let sync = model.shownSync else { return nil }
        if case .offline = sync {
            return (.cantReach, "\(sync.text) · \(String(localized: "Reconnecting"))")
        }
        return (.syncing, sync.text)
    }

    private var environmentSection: some View {
        let sorted = model.machines.sorted { $0.machineName.localizedStandardCompare($1.machineName) == .orderedAscending }
        return VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text("Environments")
                    .font(.footnote.weight(.medium))
                    .foregroundStyle(.secondary)
                Spacer()
                Button {
                    model.sheet = .addMachine
                } label: {
                    Image(systemName: "plus")
                        .font(.footnote.weight(.semibold))
                        .foregroundStyle(.secondary)
                        .frame(width: 32, height: 28)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Add Environment…")
                .accessibilityIdentifier("hub-add-machine")
            }
            .padding(.leading, 26)
            .padding(.trailing, 14)

            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 8) {
                    allMachinesPill
                    ForEach(sorted, id: \.machineID) { m in
                        machinePill(m)
                    }
                }
                .padding(.horizontal, 24)
                .padding(.vertical, 2)
            }
        }
        .padding(.bottom, 6)
    }

    private var allMachinesPill: some View {
        let selected = selectedMachineID == nil
        return Button {
            withAnimation(.snappy) { selectedMachineID = nil }
        } label: {
            Text("All")
                .font(.subheadline.weight(.medium))
                .foregroundStyle(.primary)
                .padding(.horizontal, 14)
                .frame(height: 36)
                .background(selected ? Color(.secondarySystemFill) : Color.clear, in: .capsule)
                .overlay {
                    Capsule().strokeBorder(selected ? Color.accentColor.opacity(0.55) : Color(.separator), lineWidth: 1)
                }
                .contentShape(.capsule)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityIdentifier("hub-env-all")
    }

    private enum PillConnectionState: Equatable {
        case online(highLatency: Bool, running: Int)
        case connecting(reconnecting: Bool)
        case paused
        case unreachable
        case offline
    }

    /// Evaluates the true real-time connection state of `m`, never reporting a stale `holdingState` green dot while
    /// iPhone has no network, Relay is disconnected/reconnecting, or the machine's tunnel is dialing/unreachable.
    private func pillState(for m: Machine) -> PillConnectionState {
        if m.local {
            return .online(highLatency: false, running: Int(m.status.runningSessions))
        }
        if model.networkKind == "offline" {
            return .offline
        }
        if !model.watched || !model.status.relayConnected {
            let reconnecting = model.status.relayHealth.retryCount > 0
                || (model.relayDownSince.map { model.now.timeIntervalSince($0) > 10 } ?? false)
            return .connecting(reconnecting: reconnecting)
        }
        if !m.online {
            return .offline
        }
        if !m.unreachable.isEmpty || (m.hasTunnel && m.tunnel.phase == "unreachable") {
            return .unreachable
        }
        if m.syncing || (m.hasTunnel && (m.tunnel.phase == "dialing" || m.tunnel.phase == "retrying")) {
            return .connecting(reconnecting: m.hasTunnel && m.tunnel.phase == "retrying")
        }
        if m.status.paused {
            return .paused
        }
        let slow = m.hasTunnel && m.tunnel.phase == "ready" && m.tunnel.rttMs >= 250
        return .online(highLatency: slow, running: Int(m.status.runningSessions))
    }

    private func machinePill(_ m: Machine) -> some View {
        let selected = selectedMachineID == m.machineID
        let st = pillState(for: m)
        let isMuted: Bool = if case .offline = st { true } else { false }
        let isConnecting: Bool = if case .connecting = st { true } else { false }
        let statusTag: (text: String, color: Color)? = switch st {
        case .online(_, let running):
            running > 0 ? ("· \(running)", .secondary) : nil
        case .connecting(let reconnecting):
            (String(localized: reconnecting ? "Reconnecting" : "Connecting…"), .orange)
        case .paused:
            (String(localized: "Paused"), .orange)
        case .unreachable:
            (String(localized: "Unreachable"), .red)
        case .offline:
            (String(localized: "Offline"), .secondary)
        }
        let a11yState: String = switch st {
        case .online: String(localized: "Online")
        case .connecting(let reconnecting): String(localized: reconnecting ? "Reconnecting" : "Connecting…")
        case .paused: String(localized: "Paused")
        case .unreachable: String(localized: "Unreachable")
        case .offline: String(localized: "Offline")
        }

        return Button {
            withAnimation(.snappy) {
                selectedMachineID = selected ? nil : m.machineID
            }
        } label: {
            HStack(spacing: 7) {
                Image(systemName: machineIcon(m))
                    .font(.caption.weight(.medium))
                    .foregroundStyle(isMuted ? .secondary : .primary)
                    .overlay(alignment: .topLeading) {
                        pillIndicator(st)
                            .offset(x: -3, y: -3)
                    }
                Text(verbatim: m.machineName)
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(isMuted ? .secondary : .primary)
                if let tag = statusTag {
                    Text(verbatim: tag.text)
                        .font(.caption)
                        .foregroundStyle(tag.color)
                        .monospacedDigit()
                        .shimmer(isConnecting, paused: animPaused)
                }
            }
            .padding(.horizontal, 14)
            .frame(height: 36)
            .background(selected ? Color(.secondarySystemFill) : Color.clear, in: .capsule)
            .overlay {
                Capsule().strokeBorder(selected ? Color.accentColor.opacity(0.55) : Color(.separator), lineWidth: 1)
            }
            .contentShape(.capsule)
        }
        .buttonStyle(.plain)
        .contextMenu {
            Button("Connection Diagnostics", systemImage: "waveform.path.ecg") {
                diagnosticsMachine = m
                diagnosticsPresented = true
            }
            Button("Environment", systemImage: "desktopcomputer") {
                model.path.append(.machine(m.machineID))
            }
        }
        .accessibilityValue(Text(a11yState))
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityIdentifier("hub-env-\(m.machineName)")
    }

    @ViewBuilder private func pillIndicator(_ st: PillConnectionState) -> some View {
        switch st {
        case .online(let highLatency, _):
            Circle()
                .fill(highLatency ? Color.orange : Color.green)
                .frame(width: 7, height: 7)
                .overlay { Circle().strokeBorder(Color(.systemBackground), lineWidth: 1.4) }
        case .connecting:
            PulsingMachineDot(color: .orange, paused: animPaused)
        case .paused:
            Circle()
                .fill(Color.orange)
                .frame(width: 7, height: 7)
                .overlay { Circle().strokeBorder(Color(.systemBackground), lineWidth: 1.4) }
        case .unreachable:
            Circle()
                .fill(Color.red)
                .frame(width: 7, height: 7)
                .overlay { Circle().strokeBorder(Color(.systemBackground), lineWidth: 1.4) }
        case .offline:
            Circle()
                .fill(Color(.systemBackground))
                .frame(width: 7, height: 7)
                .overlay { Circle().strokeBorder(Color.secondary, lineWidth: 1.5) }
        }
    }

    private func offlineMachineBanner(_ m: Machine) -> some View {
        let seen = m.hasLastSeen ? agoText(m.lastSeen.date, now: model.now) : String(localized: "a long time ago")
        return Button {
            diagnosticsMachine = m
            diagnosticsPresented = true
        } label: {
            HStack(spacing: 10) {
                Circle()
                    .strokeBorder(Color.secondary, lineWidth: 1.5)
                    .frame(width: 8, height: 8)
                Text("\(m.machineName) · \(String(localized: "Last online \(seen)"))")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                Spacer(minLength: 0)
                Image(systemName: "chevron.right")
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(.tertiary)
            }
            .padding(.horizontal, 26)
            .padding(.vertical, 6)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("hub-machine-offline-banner")
    }
}

/// Timeline-driven breathing indicator dot for machines that are connecting, reconnecting, or syncing inside `List`
/// (uses `TimelineView` rather than implicit `.repeatForever` so collection-view cell reuse never freezes the pulse).
private struct PulsingMachineDot: View {
    let color: Color
    let paused: Bool
    @Environment(\.accessibilityReduceMotion) private var still

    var body: some View {
        if still || paused {
            Circle()
                .fill(color)
                .frame(width: 7, height: 7)
                .overlay { Circle().strokeBorder(Color(.systemBackground), lineWidth: 1.4) }
        } else {
            TimelineView(.animation) { timeline in
                let t = timeline.date.timeIntervalSinceReferenceDate
                let p = (sin(t * .pi * 1.6) + 1) * 0.5 // smooth 1.25s breathing cycle
                ZStack {
                    Circle()
                        .fill(color.opacity(0.32 * (1 - p)))
                        .frame(width: 7 + 5 * p, height: 7 + 5 * p)
                    Circle()
                        .fill(color.opacity(0.45 + 0.55 * p))
                        .frame(width: 7, height: 7)
                        .overlay { Circle().strokeBorder(Color(.systemBackground), lineWidth: 1.3) }
                }
            }
            .frame(width: 12, height: 12)
        }
    }
}
