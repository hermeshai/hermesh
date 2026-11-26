import HermeshHer
import HermeshProto
import HermeshSessions
import SwiftUI

/// The link map (docs/overview-usage.md §4.2), in a card of the overview's:
/// who may use the owner's environments down the left, Hermesh in the middle,
/// the environments down the right, and one weighted line for each (sender,
/// environment) pair with sessions that have not finished, with dots moving
/// along those at work. What state a session is in is the deck's to say.
///
/// A node picked keeps what goes through it (lit) and dims the rest; the
/// node picked again, or Hermesh, shows all. When the deck's card in front
/// changes (spot moves), all but what it is about (mine) dips for a moment.
struct LinkMapView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.accessibilityReduceMotion) private var still
    let map: LinkMap
    /// With a node picked: the nodes its sessions touch.
    let lit: Set<String>?
    let mine: Set<String>
    let mineLine: String?
    let spot: Int
    /// The app is in front: the dots move.
    let awake: Bool
    @Binding var picked: String?
    @State private var dipped = false

    var body: some View {
        // The height follows the rows, not the width.
        GeometryReader { geo in
            let layout = LinkMapLayout(map, width: geo.size.width)
            ZStack(alignment: .topLeading) {
                if map.lines.contains(where: \.atWork) {
                    Circle()
                        .fill(RadialGradient(colors: [Color.live.opacity(0.22), .clear], center: .center, startRadius: 4, endRadius: 52))
                        .frame(width: 104, height: 104)
                        .position(layout.hub)
                        .allowsHitTesting(false)
                }
                ForEach(map.senders + map.environments) { n in
                    if let link = layout.links[n.id] {
                        Curves(curves: [link])
                            .stroke(Color.primary.opacity(0.13), style: StrokeStyle(lineWidth: 1, dash: n.kind == .sender || n.kind == .environment ? [] : [2, 5]))
                            .opacity(shown(node: n.id, own: mineLine == nil))
                        if let seg = segment(for: n.id) {
                            Curves(curves: [link])
                                .stroke(Color.live.opacity(seg.alpha), style: StrokeStyle(lineWidth: seg.width, lineCap: .round))
                                .opacity(seg.opacity)
                        }
                    }
                }
                if !still {
                    Comets(ways: map.lines.filter { $0.atWork && shown($0) == 1 }.compactMap { layout.ways[$0.id] }, awake: awake)
                        .frame(width: layout.size.width, height: layout.size.height)
                        .allowsHitTesting(false)
                }
                hub.position(layout.hub)
                let centerSpan = max(72, layout.size.width - (map.senders.compactMap { layout.frames[$0.id]?.width }.max() ?? 0)
                    - (map.environments.compactMap { layout.frames[$0.id]?.width }.max() ?? 0) - 12)
                VStack(spacing: 1.5) {
                    Text(verbatim: "Hermesh").font(.caption.weight(.semibold))
                    Text("End-to-end encrypted").font(.system(size: 10)).foregroundStyle(.secondary)
                        .lineLimit(1).minimumScaleFactor(0.82)
                }
                .frame(width: centerSpan)
                .position(x: layout.hub.x, y: layout.hub.y + layout.hubRadius + 20)
                .allowsHitTesting(false)
                ForEach(map.senders + map.environments) { n in
                    if let frame = layout.frames[n.id] {
                        node(n).frame(width: frame.width, height: frame.height).position(x: frame.midX, y: frame.midY)
                            .opacity(shown(node: n.id, own: true))
                    }
                }
            }
            .animation(.easeInOut(duration: 0.3), value: picked)
            .animation(.easeInOut(duration: 0.35), value: dipped)
        }
        .frame(height: LinkMapLayout.height(for: map))
        .padding(.horizontal, 12)
        .padding(.vertical, 12)
        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
        .onChange(of: spot) {
            guard !mine.isEmpty, !still else { return }
            dipped = true
            Task {
                try? await Task.sleep(for: .seconds(0.9))
                dipped = false
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("link-map")
    }

    /// How much of a node (and its link) shows: dimmed when another is
    /// picked and nothing of the picked one's reaches it; dipped for a
    /// moment when the deck turns to something that is not about it. own:
    /// what the deck's card is about counts for it (a session's plain links
    /// under its line dip with the rest).
    private func shown(node id: String, own: Bool) -> Double {
        (lit.map { $0.contains(id) } ?? true ? 1 : 0.14) * (dipped && !(own && mine.contains(id)) ? 0.2 : 1)
    }

    private func shown(_ line: LinkMap.Line) -> Double {
        (picked == nil || line.sender == picked || line.environment == picked ? 1 : 0.14) * (dipped && line.id != mineLine ? 0.2 : 1)
    }

    /// Stroke weight and opacity for a node's half-link to the hub, summed across
    /// its active lines so shared hub segments draw once without alpha overdraw.
    private func segment(for nodeID: String) -> (width: CGFloat, alpha: Double, opacity: Double)? {
        let touching = map.lines.filter { $0.sender == nodeID || $0.environment == nodeID }
        guard !touching.isEmpty else { return nil }
        let focused = touching.filter { shown($0) == 1 }
        let active = focused.isEmpty ? touching : focused
        let total = active.reduce(0) { $0 + $1.count }
        let alpha = min(0.8, 0.58 + 0.06 * Double(max(total - 1, 0)))
        let opacity = focused.isEmpty ? (touching.map(shown).max() ?? 0.14) : 1
        return (LinkMap.strokeWidth(for: total), alpha, opacity)
    }

    private var hub: some View {
        Button { withAnimation(.smooth) { picked = nil } } label: {
            Mark(size: 29)
                .frame(width: 50, height: 50)
                .background(Color(UIColor.mapHubFill), in: Circle())
                .overlay { Circle().strokeBorder(Color.primary.opacity(0.1), lineWidth: 0.8) }
                .shadow(color: .black.opacity(0.14), radius: 7, y: 2)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Hermesh")
        .accessibilityIdentifier("map-hub")
    }

    @ViewBuilder private func node(_ n: LinkMap.Node) -> some View {
        switch n.kind {
        case .sender:
            box(n) {
                HStack(spacing: 7) {
                    senderPicture(n)
                    Text(n.title).font(.subheadline.weight(.medium)).lineLimit(1).minimumScaleFactor(0.82)
                    Spacer(minLength: 0)
                }
            }
        case .environment:
            box(n) {
                VStack(alignment: .leading, spacing: 4) {
                    Text(n.title).font(.subheadline.weight(.medium)).lineLimit(1).minimumScaleFactor(0.82)
                    HStack(spacing: 3.5) { ForEach(n.agents.prefix(5), id: \.self) { AgentLogo(id: $0, size: 15) } }.frame(height: 15)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .opacity(n.offline ? 0.45 : 1)
            }
            .accessibilityLabel(Text(verbatim: ([n.title] + n.agents.map(agentTitle)).joined(separator: ", ")))
        case .more(let count):
            Text("\(count) more").font(.caption).foregroundStyle(.secondary)
        }
    }

    private func box(_ n: LinkMap.Node, @ViewBuilder _ content: () -> some View) -> some View {
        Button {
            withAnimation(.smooth) { picked = picked == n.id ? nil : n.id }
        } label: {
            content()
                .padding(.horizontal, 9)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(picked == n.id ? Color.accentColor.opacity(0.14) : Color(UIColor.mapNodeFill), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                .overlay {
                    RoundedRectangle(cornerRadius: 14, style: .continuous)
                        .strokeBorder(picked == n.id ? Color.accentColor : Color.primary.opacity(0.08), lineWidth: picked == n.id ? 1.5 : 0.8)
                }
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(picked == n.id ? .isSelected : [])
        .accessibilityIdentifier("map-\(n.title)")
    }

    @ViewBuilder private func senderPicture(_ n: LinkMap.Node) -> some View {
        if let c = model.clients.first(where: { LinkMap.sender($0.clientID) == n.id }) {
            GrantAvatar(client: c, size: 24)
        } else {
            Image(systemName: "person.fill").font(.system(size: 12)).foregroundStyle(.secondary)
                .frame(width: 24, height: 24).background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 6.5, style: .continuous))
        }
    }
}

private extension UIColor {
    static let mapNodeFill = UIColor {
        $0.userInterfaceStyle == .dark ? .tertiarySystemGroupedBackground : UIColor(red: 0.96, green: 0.965, blue: 0.98, alpha: 1)
    }

    static let mapHubFill = UIColor {
        $0.userInterfaceStyle == .dark ? .tertiarySystemGroupedBackground : .white
    }
}

/// Curves of the map as one path: a line's two, joined by a straight run through the hub.
private nonisolated struct Curves: Shape {
    let curves: [MapCurve]

    func path(in rect: CGRect) -> Path { Path(Curves.path(curves)) }

    static func path(_ curves: [MapCurve]) -> CGPath {
        let p = CGMutablePath()
        for (i, c) in curves.enumerated() {
            if i == 0 { p.move(to: c.from) } else { p.addLine(to: c.from) }
            p.addCurve(to: c.to, control1: c.control1, control2: c.control2)
        }
        return p
    }
}

/// The dots along the sessions at work: layers the render server moves
/// along each way, so the app runs no code per frame. Put back when the
/// ways change and when the app comes to the front (animations do not
/// outlive the background).
private struct Comets: UIViewRepresentable {
    let ways: [[MapCurve]]
    let awake: Bool

    func makeUIView(context: Context) -> CometsView { CometsView() }

    func updateUIView(_ view: CometsView, context: Context) {
        view.show(ways.map(Curves.path), awake: awake && !ProcessInfo.processInfo.isLowPowerModeEnabled)
    }
}

final class CometsView: UIView {
    private var paths: [CGPath] = []
    private var awake = false

    override init(frame: CGRect) {
        super.init(frame: frame)
        isUserInteractionEnabled = false
        registerForTraitChanges([UITraitUserInterfaceStyle.self]) { (view: CometsView, _) in view.draw() }
    }

    required init?(coder: NSCoder) { fatalError() }

    func show(_ paths: [CGPath], awake: Bool) {
        guard paths != self.paths || awake != self.awake else { return }
        self.paths = paths
        self.awake = awake
        draw()
    }

    override func didMoveToWindow() {
        super.didMoveToWindow()
        if window != nil { draw() }
    }

    /// Two comets a way, half a lap apart: a dot and three fainter ones after it.
    private func draw() {
        layer.sublayers?.forEach { $0.removeFromSuperlayer() }
        guard awake, window != nil else { return }
        let color = UIColor.live.resolvedColor(with: traitCollection).cgColor
        let now = layer.convertTime(CACurrentMediaTime(), from: nil)
        for (i, path) in paths.enumerated() {
            let lap = 4.2 + Double(i) * 0.6
            for comet in 0..<2 {
                for tail in 0..<4 {
                    let r = 2.6 - 0.32 * CGFloat(tail), base = tail == 0 ? 1 : 0.45 - 0.1 * Float(tail)
                    let dot = CALayer()
                    dot.bounds = CGRect(x: 0, y: 0, width: 2 * r, height: 2 * r)
                    dot.cornerRadius = r
                    dot.backgroundColor = color
                    dot.opacity = 0
                    let move = CAKeyframeAnimation(keyPath: "position")
                    move.path = path
                    move.calculationMode = .paced
                    let fade = CAKeyframeAnimation(keyPath: "opacity")
                    fade.values = [0, base, base, 0]
                    fade.keyTimes = [0, 0.1, 0.9, 1]
                    let fly = CAAnimationGroup()
                    fly.animations = [move, fade]
                    fly.duration = lap
                    fly.repeatCount = .infinity
                    fly.beginTime = now + Double(tail) * 0.05 - Double(comet) * lap / 2 - lap
                    layer.addSublayer(dot)
                    dot.add(fly, forKey: "fly")
                }
            }
        }
    }
}
