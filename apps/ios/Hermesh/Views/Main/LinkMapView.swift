import HermeshHer
import HermeshProto
import HermeshSessions
import SwiftUI

/// The link map (docs/overview-usage.md §4.2), drawn on the page: who may
/// use the owner's environments down the left, Hermesh in the middle, the
/// environments down the right, and a line for each session that has not
/// finished, with a dot moving along those at work. The lines are alike:
/// what state a session is in is the deck's to say.
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
                ForEach(map.senders + map.environments) { n in
                    if let link = layout.links[n.id] {
                        Curves(curves: [link])
                            .stroke(Color.primary.opacity(0.13), style: StrokeStyle(lineWidth: 1, dash: n.kind == .sender || n.kind == .environment ? [] : [2, 5]))
                            .opacity(shown(node: n.id, own: mineLine == nil))
                    }
                }
                ForEach(map.lines) { line in
                    if let way = layout.ways[line.id] {
                        Curves(curves: way).stroke(Color.live.opacity(0.6), style: StrokeStyle(lineWidth: 1.4, lineCap: .round))
                            .opacity(shown(line))
                    }
                }
                if !still {
                    Comets(ways: map.lines.filter { $0.atWork && shown($0) == 1 }.compactMap { layout.ways[$0.id] }, awake: awake)
                        .frame(width: layout.size.width, height: layout.size.height)
                        .allowsHitTesting(false)
                }
                hub.position(layout.hub)
                VStack(spacing: 1) {
                    Text(verbatim: "Hermesh").font(.caption2.weight(.semibold))
                    Text("End-to-end encrypted").font(.system(size: 10)).foregroundStyle(.secondary)
                }
                .fixedSize()
                .position(x: layout.hub.x, y: layout.hub.y + layout.hubRadius + 19)
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
        .frame(height: LinkMapLayout(map, width: 329).size.height)
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

    private var hub: some View {
        Button { withAnimation(.smooth) { picked = nil } } label: {
            Mark(size: 26)
                .frame(width: 44, height: 44)
                .background(Color(.secondarySystemGroupedBackground), in: Circle())
                .overlay { Circle().strokeBorder(Color.primary.opacity(0.08), lineWidth: 0.8) }
                .shadow(color: .black.opacity(0.1), radius: 5, y: 2)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Hermesh")
        .accessibilityIdentifier("map-hub")
    }

    @ViewBuilder private func node(_ n: LinkMap.Node) -> some View {
        switch n.kind {
        case .sender:
            box(n) {
                HStack(spacing: 6) {
                    senderPicture(n)
                    Text(n.title).font(.footnote.weight(.medium)).lineLimit(1).minimumScaleFactor(0.85)
                    Spacer(minLength: 0)
                }
            }
        case .environment:
            box(n) {
                VStack(alignment: .leading, spacing: 3) {
                    Text(n.title).font(.footnote.weight(.medium)).lineLimit(1).minimumScaleFactor(0.85)
                    HStack(spacing: 3) { ForEach(n.agents.prefix(5), id: \.self) { AgentLogo(id: $0, size: 13) } }.frame(height: 13)
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
                .padding(.horizontal, 8)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(picked == n.id ? Color.accentColor.opacity(0.12) : Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 11, style: .continuous))
                .overlay {
                    RoundedRectangle(cornerRadius: 11, style: .continuous)
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
            GrantAvatar(client: c, size: 20)
        } else {
            Image(systemName: "person.fill").font(.system(size: 11)).foregroundStyle(.secondary)
                .frame(width: 20, height: 20).background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 5, style: .continuous))
        }
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
                    let r = 2.4 - 0.3 * CGFloat(tail), base = tail == 0 ? 1 : 0.45 - 0.1 * Float(tail)
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
