import SwiftUI

/// Where Computer Control's bar sits: against one edge of the page, a
/// fraction of the way along it (0 at the top or leading end), and whether it
/// is folded into a round button. The owner drags it; it keeps to the edge
/// nearest where it is let go, so it never stops in the middle of the
/// picture. Upright and on its side each keep their own place.
struct ScreenBarPlace: Equatable {
    var edge: Edge
    var along: CGFloat

    /// Bottom right, where the bar first was.
    static func standard(wide: Bool) -> ScreenBarPlace {
        wide ? ScreenBarPlace(edge: .trailing, along: 1) : ScreenBarPlace(edge: .bottom, along: 1)
    }

    static func load(wide: Bool) -> ScreenBarPlace {
        let parts = (UserDefaults.standard.string(forKey: key(wide)) ?? "").split(separator: ":")
        guard parts.count == 2, let raw = Int8(parts[0]), let edge = Edge(rawValue: raw), let along = Double(parts[1]) else { return standard(wide: wide) }
        return ScreenBarPlace(edge: edge, along: min(1, max(0, along)))
    }

    func save(wide: Bool) {
        UserDefaults.standard.set("\(edge.rawValue):\(Double(along))", forKey: ScreenBarPlace.key(wide))
    }

    private static func key(_ wide: Bool) -> String { wide ? "screen-bar-wide" : "screen-bar-upright" }

    /// The bar runs down the page against a side.
    var vertical: Bool { edge == .leading || edge == .trailing }

    /// The centre of a bar of this size in a page of that size, margin from its edge.
    func center(size: CGSize, in page: CGSize, margin: CGFloat) -> CGPoint {
        func spot(_ length: CGFloat, _ room: CGFloat) -> CGFloat {
            let lo = margin + length / 2, hi = room - margin - length / 2
            return hi < lo ? room / 2 : lo + (hi - lo) * along
        }
        switch edge {
        case .top: return CGPoint(x: spot(size.width, page.width), y: margin + size.height / 2)
        case .bottom: return CGPoint(x: spot(size.width, page.width), y: page.height - margin - size.height / 2)
        case .leading: return CGPoint(x: margin + size.width / 2, y: spot(size.height, page.height))
        case .trailing: return CGPoint(x: page.width - margin - size.width / 2, y: spot(size.height, page.height))
        }
    }

    /// The place nearest a point the bar was let go at.
    static func nearest(_ p: CGPoint, in page: CGSize) -> ScreenBarPlace {
        let gaps: [(Edge, CGFloat)] = [(.top, p.y), (.bottom, page.height - p.y), (.leading, p.x), (.trailing, page.width - p.x)]
        let edge = gaps.min { $0.1 < $1.1 }!.0
        let along = edge == .top || edge == .bottom ? p.x / max(1, page.width) : p.y / max(1, page.height)
        return ScreenBarPlace(edge: edge, along: min(1, max(0, along)))
    }
}

/// The bar's controls, laid along it, or the round button it folds into.
/// The page draws the controls; this places them and moves them.
struct ScreenBarHost<Bar: View, Folded: View>: View {
    @Binding var folded: Bool
    /// The controls, told whether they run down the page.
    @ViewBuilder var bar: (_ vertical: Bool) -> Bar
    @ViewBuilder var ball: () -> Folded

    @State private var place: ScreenBarPlace?
    @State private var wide = false
    @State private var size = CGSize(width: 44, height: 44)
    /// Where the bar's centre is while a finger drags it.
    @State private var dragged: CGPoint?

    private let margin: CGFloat = 8

    var body: some View {
        GeometryReader { geo in
            let page = geo.size
            let place = place ?? .standard(wide: page.width > page.height)
            let at = dragged ?? place.center(size: size, in: page, margin: margin)
            content(vertical: place.vertical)
                .onGeometryChange(for: CGSize.self) { $0.size } action: { size = $0 }
                .gesture(drag(from: place.center(size: size, in: page, margin: margin), page: page))
                .position(at)
                .onAppear { turn(page) }
                .onChange(of: page.width > page.height) { turn(page) }
        }
    }

    @ViewBuilder private func content(vertical: Bool) -> some View {
        if folded { ball() } else { bar(vertical) }
    }

    /// Takes the place this way of holding the phone keeps.
    private func turn(_ page: CGSize) {
        wide = page.width > page.height
        place = .load(wide: wide)
    }

    private func drag(from start: CGPoint, page: CGSize) -> some Gesture {
        // A finger that moves a little more than a tap would drags; the buttons keep their taps.
        DragGesture(minimumDistance: 8, coordinateSpace: .local)
            .onChanged { g in
                dragged = CGPoint(x: start.x + g.translation.width, y: start.y + g.translation.height)
            }
            .onEnded { g in
                // A flick carries it on the way it was going.
                let end = CGPoint(x: min(page.width, max(0, start.x + g.predictedEndTranslation.width)),
                                  y: min(page.height, max(0, start.y + g.predictedEndTranslation.height)))
                let next = ScreenBarPlace.nearest(end, in: page)
                next.save(wide: wide)
                withAnimation(.spring(duration: 0.35, bounce: 0.2)) {
                    place = next
                    dragged = nil
                }
            }
    }
}
