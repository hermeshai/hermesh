import HermeshSessions
import SwiftUI
import UIKit
import os

/// The side menu and, over it, the page it leads to. The page slides right
/// to show the menu: a drag from anywhere on a root page, or the menu
/// button. Pushed pages keep the system's swipe back. The menu sits on the
/// darkest layer; the page rises above it as a lighter card that fades back.
struct MainView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var model = model
        SideMenu(model: model, open: $model.drawerOpen, panAnywhere: model.path.isEmpty)
            .ignoresSafeArea(edges: model.isDemo ? [.top, .horizontal] : .all)
            .sheet(item: $model.sheet) { sheet in
                SheetHost(kind: sheet).environment(model)
            }
    }
}

/// The page under the menu: the navigation stack, and the toast over it.
private struct PageView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var model = model
        NavigationStack(path: $model.path) {
            rootPage
                .navigationDestination(for: Route.self) { route in
                    switch route {
                    case .session(let id): SessionView(id: id) // the sync line in its title's second line
                    case .grant(let id): GrantView(id: id).syncSubtitle()
                    case .machine(let id): MachineView(id: id).syncSubtitle()
                    case .machineAgent(let machine, let agent): MachineAgentView(machineID: machine, agent: agent).syncSubtitle()
                    case .grantSessions(let id): ScopedSessionsView(filter: SessionFilter(grant: id), title: String(localized: "“\(model.clients.first { $0.clientID == id }?.label ?? "")” Sessions"), about: model.clients.first { $0.clientID == id }.map { .grant($0.grantID) }).syncSubtitle()
                    case .machineSessions(let id): ScopedSessionsView(filter: SessionFilter(machine: id), title: String(localized: "Sessions on \(model.machineName(id))"), about: .machine(id)).syncSubtitle()
                    case .agentSessions(let machine, let agent):
                        ScopedSessionsView(filter: SessionFilter(agent: agent, machine: machine), title: String(localized: "\(agentTitle(agent)) on \(model.machineName(machine))"), about: .machineAgent(machine, agent)).syncSubtitle()
                    case .sessions: SessionsView().syncSubtitle()
                    case .herSettings: HerSettings()
                    }
                }
        }
        .overlay(alignment: .top) { // in the middle of a root page's bar while its title is large
            if model.path.isEmpty && model.root.titleLarge && !model.rootTitleSmall && model.shownSync != nil {
                SyncLine().frame(height: 44)
            }
        }
        .task(id: model.sync) { await model.showSync(model.sync) }
        .overlay(alignment: .bottom) {
            if let t = model.toast {
                ToastView(toast: t) { model.toast = nil }
                    .padding(.bottom, 8)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }
        }
        .animation(.smooth(duration: 0.35), value: model.toast)
    }

    @ViewBuilder private var rootPage: some View {
        switch model.root {
        case .her: HerView()
        case .overview: OverviewView()
        case .grants: GrantsView()
        case .machines: MachinesView()
        case .session(let id): SessionView(id: id).id(id) // a page of its own: the one left keeps its conversation as it goes
        }
    }
}

/// The menu and the page in a UIKit container. The page moves as one view,
/// navigation bar and all, on a spring timed to the system's push and pop.
/// (A SwiftUI offset moved the navigation bar's frame, and the bar laid its
/// large title out again, off the page's margin.)
private struct SideMenu: UIViewControllerRepresentable {
    let model: AppModel
    @Binding var open: Bool
    let panAnywhere: Bool

    func makeUIViewController(context: Context) -> SideMenuController {
        SideMenuController(
            menu: UIHostingController(rootView: DrawerView().environment(model)),
            page: UIHostingController(rootView: PageView().environment(model)),
            width: 322)
    }

    func updateUIViewController(_ c: SideMenuController, context: Context) {
        c.panAnywhere = panAnywhere
        c.onChange = { open = $0 }
        c.onBeside = { model.menuBeside = $0 }
        c.onHidden = { model.menuHidden = $0 }
        c.onWidth = { model.menuWidth = $0 }
        c.set(width: model.menuWidth)
        c.set(hidden: model.menuHidden)
        c.set(open: open)
    }
}

final class SideMenuController: UIViewController, UIGestureRecognizerDelegate {
    /// How the menu and the page share the screen. An iPhone: the page
    /// slides aside by the menu's width and stays in view, a card beside it
    /// (drawer). Where the system stands the bars on the side (iPhone Duo
    /// closed), that strip of the screen is the bars' in every state, and a
    /// page beside the menu would lie under them: the menu takes the whole
    /// page's place (cover). At regular width, wider than tall (iPhone Duo
    /// open, on its side), both show side by side (beside), unless the owner
    /// hid the menu; upright, the page keeps its width and the menu slides
    /// over it, as on an iPhone.
    private enum Arrangement { case drawer, cover, beside }

    var panAnywhere = true
    var onChange: (Bool) -> Void = { _ in }
    /// Told when the menu starts or stops standing beside the page.
    var onBeside: (Bool) -> Void = { _ in }
    /// Told when the owner asks for the menu beside the page while it is hidden, and the width they dragged it to.
    var onHidden: (Bool) -> Void = { _ in }
    var onWidth: (Double) -> Void = { _ in }
    private let menu: UIViewController
    private let page: UIViewController
    private let width: CGFloat
    private let card = UIView() // moves, and casts the shadow
    private let clip = UIView() // rounds the page's corners
    private let dim = UIView() // fades the page back; a tap closes the menu
    private let divider = UIView() // between the menu and the page, side by side
    private let grip = UIView() // over the divider: drags the menu wider or narrower
    /// The menu's width beside the page, and whether it is hidden there.
    private var besideWidth: CGFloat = 322
    private var hidden = false
    private var gripStart: CGFloat?
    /// Covering, what moves is a picture of the page; the page stays where it rests, hidden.
    private var standIn: UIView?
    /// The menu moving, from a finger or a button until it comes to rest: an animation interval, which a
    /// hitches trace or XCTOSSignpostMetric(subsystem: "io.hermesh.ios", category: "menu", name: "move") measures.
    private let signposter = OSSignposter(subsystem: "io.hermesh.ios", category: "menu")
    private var moving: OSSignpostIntervalState?
    private let radius: CGFloat = 44
    private var arrangement = Arrangement.drawer
    /// How far the page moves to show the menu.
    private var travel: CGFloat
    private var isOpen = false
    private var startX: CGFloat = 0
    private var generation = 0
    private var sideMargins = NSDirectionalEdgeInsets.zero

    init(menu: UIViewController, page: UIViewController, width: CGFloat) {
        self.menu = menu
        self.page = page
        self.width = width
        travel = width
        super.init(nibName: nil, bundle: nil)
    }

    required init?(coder: NSCoder) { fatalError("init(coder:) is not used") }

    override func viewDidLoad() {
        super.viewDidLoad()
        view.backgroundColor = .menuBackground
        addChild(menu)
        view.addSubview(menu.view)
        menu.didMove(toParent: self)
        view.addSubview(card)
        card.addSubview(clip)
        clip.clipsToBounds = true
        clip.layer.cornerCurve = .continuous
        addChild(page)
        clip.addSubview(page.view)
        page.didMove(toParent: self)
        clip.addSubview(dim)
        dim.backgroundColor = .pageFade
        dim.layer.cornerRadius = radius
        dim.layer.cornerCurve = .continuous
        dim.layer.borderWidth = 0.5
        dim.layer.borderColor = UIColor.separator.resolvedColor(with: traitCollection).cgColor
        registerForTraitChanges([UITraitUserInterfaceStyle.self]) { (self: Self, _) in
            self.dim.layer.borderColor = UIColor.separator.resolvedColor(with: self.traitCollection).cgColor
        }
        dim.addGestureRecognizer(UITapGestureRecognizer(target: self, action: #selector(closeFromDim)))
        dim.isAccessibilityElement = true
        dim.accessibilityLabel = String(localized: "Close the menu")
        dim.accessibilityTraits = .button
        dim.accessibilityIdentifier = "drawer-dim"
        divider.backgroundColor = .separator
        divider.isHidden = true
        view.addSubview(divider)
        view.addSubview(grip)
        grip.addGestureRecognizer(UIPanGestureRecognizer(target: self, action: #selector(resize(_:))))
        // Closed, the shadow falls off the screen's left edge.
        card.layer.shadowColor = UIColor.black.cgColor
        card.layer.shadowRadius = 12
        card.layer.shadowOffset = CGSize(width: -6, height: 0)
        let pan = UIPanGestureRecognizer(target: self, action: #selector(pan(_:)))
        pan.delegate = self
        view.addGestureRecognizer(pan)
        var traits: [UITrait] = [UITraitHorizontalSizeClass.self]
        if #available(iOS 27.1, *) { traits += UITraitCollection.systemTraitsAffectingVerticalBarEdge }
        registerForTraitChanges(traits) { (self: Self, _) in self.view.setNeedsLayout() }
        settle()
        apply(0)
    }

    /// The arrangement that fits the screen as it is.
    private var fitting: Arrangement {
        if traitCollection.horizontalSizeClass == .regular && view.bounds.width > view.bounds.height { return .beside }
        if #available(iOS 27.1, *), traitCollection.verticalBarEdge != .unspecified { return .cover }
        return .drawer
    }

    override func viewDidLayoutSubviews() {
        super.viewDidLayoutSubviews()
        let b = view.bounds
        let before = (arrangement, travel)
        arrangement = fitting
        // Covering, the menu is as wide as the screen (its safe area keeps it
        // clear of the bars' strip), and the page leaves with its shadow.
        let menuWidth = arrangement == .cover ? b.width : arrangement == .beside ? besideWidth : width
        travel = arrangement == .cover ? b.width + 24 : width
        let pageX = arrangement == .beside && !hidden ? besideWidth : 0
        let pageBounds = CGRect(x: 0, y: 0, width: b.width - pageX, height: b.height)
        menu.view.bounds = CGRect(x: 0, y: 0, width: menuWidth, height: b.height)
        menu.view.center = CGPoint(x: menuWidth / 2, y: b.midY)
        // Bounds and center: the card's transform moves it.
        card.bounds = pageBounds
        card.center = CGPoint(x: pageX + pageBounds.midX, y: b.midY)
        card.layer.shadowPath = UIBezierPath(roundedRect: pageBounds, cornerRadius: radius).cgPath
        card.layer.shadowOpacity = arrangement == .beside ? 0 : 0.15
        clip.frame = pageBounds
        page.view.frame = pageBounds
        if arrangement == .beside {
            // The page starts beside the menu, not at the screen's edge: it keeps the screen's margins, as this view has them.
            sideMargins = systemMinimumLayoutMargins
            pinMargins()
        } else if card.transform.isIdentity {
            sideMargins = page.systemMinimumLayoutMargins
        }
        dim.frame = pageBounds
        let hairline = 1 / max(traitCollection.displayScale, 1)
        divider.frame = CGRect(x: pageX - hairline, y: 0, width: hairline, height: b.height)
        divider.isHidden = arrangement != .beside || hidden
        // Mostly over the menu: the page's edge is where its swipe back starts.
        grip.frame = CGRect(x: pageX - 16, y: 0, width: 22, height: b.height)
        grip.isHidden = divider.isHidden
        if arrangement != .beside || !hidden { menu.view.isHidden = false }
        guard before != (arrangement, travel) else { return }
        // Another arrangement (the device opened or closed, or turned): at rest in it, the menu closed.
        generation += 1
        for v in [card, menu.view!, dim] { v.layer.removeAllAnimations() }
        settle()
        if arrangement == .beside {
            card.transform = .identity
            menu.view.transform = .identity
            menu.view.alpha = 1
            menu.view.isHidden = hidden // under the page, out of VoiceOver's way
        } else {
            apply(0)
        }
        let beside = arrangement == .beside, wasOpen = isOpen
        isOpen = false
        // After the layout pass: these write the app's state, which lays views out again.
        DispatchQueue.main.async { [self] in
            if wasOpen { onChange(false) }
            onBeside(beside)
        }
    }

    /// Opens or closes the menu to match the app's state. Beside the page, opening it shows it if it was hidden.
    func set(open: Bool) {
        guard arrangement != .beside else {
            if open {
                onChange(false)
                if hidden { onHidden(false) }
            }
            return
        }
        guard open != isOpen else { return }
        animate(open: open, velocity: 0)
    }

    /// Hides or shows the menu beside the page: the page slides over it, or aside to show it.
    func set(hidden: Bool) {
        guard hidden != self.hidden else { return }
        self.hidden = hidden
        guard arrangement == .beside, view.window != nil else { return view.setNeedsLayout() }
        menu.view.isHidden = false
        generation += 1
        let g = generation
        UIView.animate(springDuration: 0.32, bounce: 0) {
            self.view.setNeedsLayout()
            self.view.layoutIfNeeded()
        } completion: { _ in
            if g == self.generation && self.hidden && self.arrangement == .beside { self.menu.view.isHidden = true }
        }
    }

    /// The menu's width beside the page, as the owner left it; not while they drag it.
    func set(width: Double) {
        guard gripStart == nil, CGFloat(width) != besideWidth else { return }
        besideWidth = CGFloat(width)
        view.setNeedsLayout()
    }

    /// Drags the menu beside the page wider or narrower, the page keeping room for its own.
    @objc private func resize(_ g: UIPanGestureRecognizer) {
        switch g.state {
        case .began:
            gripStart = besideWidth
        case .changed:
            guard let start = gripStart else { return }
            besideWidth = min(max(start + g.translation(in: view).x, 240), min(400, view.bounds.width - 400))
            view.setNeedsLayout()
            view.layoutIfNeeded()
        default:
            gripStart = nil
            onWidth(Double(besideWidth))
        }
    }

    /// Lays the two out for p, from 0 (closed) to 1 (open).
    private func apply(_ p: CGFloat) {
        card.transform = CGAffineTransform(translationX: travel * p, y: 0)
        // Covering, the menu keeps still too: where it rests, its bars are on the side.
        menu.view.transform = arrangement == .cover ? .identity : CGAffineTransform(translationX: -width * 0.25 * (1 - p), y: 0)
        menu.view.alpha = 0.4 + 0.6 * p
        dim.alpha = p
    }

    /// The page as a card, while the menu shows or moves.
    private func lift() {
        pinMargins()
        clip.layer.cornerRadius = radius
        dim.isHidden = false
        menu.view.accessibilityElementsHidden = false
    }

    /// A view controller's root view keeps the system's side margins from
    /// the screen's edges, not its own: moved right, the navigation stack's
    /// leading margin would shrink to 0 and its large title touch the
    /// card's edge. So the stack keeps the margins it has at rest.
    private func pinMargins() {
        var stack = page.children
        while let c = stack.popLast() {
            if let nav = c as? UINavigationController {
                nav.viewRespectsSystemMinimumLayoutMargins = false
                nav.view.directionalLayoutMargins = NSDirectionalEdgeInsets(top: 0, leading: sideMargins.leading, bottom: 0, trailing: sideMargins.trailing)
            }
            stack += c.children
        }
    }

    /// The page flat and edge to edge again, the menu closed.
    private func settle() {
        clip.layer.cornerRadius = 0
        dim.isHidden = true
        standDown()
        // Under the page, the menu is out of VoiceOver's reach.
        menu.view.accessibilityElementsHidden = arrangement != .beside
    }

    /// Covering, a picture of the page moves in its place, and the page waits under the menu where it rests,
    /// hidden. Where the system keeps a strip of the screen for the bars (iPhone Duo closed), a view moving
    /// across it has its safe area change on every frame, and SwiftUI laid the whole page out again each
    /// time a finger moved it. (Held still inside the card by a transform, it still did: the card moves
    /// first, and its safe area is worked out at once.) Call it with the card at rest.
    private func standUp() {
        guard arrangement == .cover, standIn == nil, let picture = page.view.snapshotView(afterScreenUpdates: false) else { return }
        picture.frame = clip.bounds
        clip.insertSubview(picture, at: 0)
        standIn = picture
        page.view.isHidden = true
        view.insertSubview(page.view, at: 0)
    }

    /// The page itself again, in the card. Call it with the card at rest too, its model value at least.
    private func standDown() {
        guard let picture = standIn else { return }
        clip.insertSubview(page.view, at: 0)
        page.view.isHidden = false
        picture.removeFromSuperview()
        standIn = nil
    }

    /// Stops the page where it is on screen, mid-animation or not.
    private func stop() -> CGFloat {
        let x = card.layer.presentation()?.affineTransform().tx ?? card.transform.tx
        for v in [card, menu.view!, dim] { v.layer.removeAllAnimations() }
        apply(x / travel)
        return x
    }

    /// velocity is relative to the distance left to go, as UIKit's springs take it.
    private func animate(open: Bool, velocity: CGFloat) {
        isOpen = open
        generation += 1
        let g = generation
        startMoving()
        lift()
        // Opening, the page leaves as its picture.
        if open { standUp() }
        // 0.32 s: measured frame by frame, the page then reaches 90% of the
        // way in a quarter second, as a push does (0.5 s took 0.4 s).
        UIView.animate(springDuration: 0.32, bounce: 0, initialSpringVelocity: velocity, options: [.allowUserInteraction, .beginFromCurrentState]) {
            self.apply(open ? 1 : 0)
        } completion: { _ in
            guard g == self.generation else { return }
            if !self.isOpen { self.settle() }
            if let m = self.moving { self.signposter.endInterval("move", m); self.moving = nil }
        }
        // Closing, the page itself comes back (it may have changed meanwhile), now that the card's model value
        // is at rest: the animation alone moves it.
        if !open { standDown() }
    }

    private func startMoving() {
        if moving == nil { moving = signposter.beginAnimationInterval("move") }
    }

    @objc private func closeFromDim() {
        animate(open: false, velocity: 0)
        onChange(false)
    }

    @objc private func pan(_ g: UIPanGestureRecognizer) {
        switch g.state {
        case .began:
            generation += 1
            startMoving()
            standUp() // before the card stops where it is: its model value is at rest, or the page is up already
            startX = stop()
            lift()
        case .changed:
            apply(min(travel, max(0, startX + g.translation(in: view).x)) / travel)
        case .ended, .cancelled, .failed:
            let x = card.transform.tx
            let v = g.velocity(in: view).x
            // Where a flick would carry the page (UIScrollView's deceleration).
            let open = g.state == .ended ? x + v * 0.998 / (1 - 0.998) / 1000 > travel / 2 : isOpen
            let left = (open ? travel : 0) - x
            animate(open: open, velocity: abs(left) < 1 ? 0 : v / left)
            onChange(open)
        default:
            break
        }
    }

    func gestureRecognizerShouldBegin(_ g: UIGestureRecognizer) -> Bool {
        guard let pan = g as? UIPanGestureRecognizer else { return true }
        guard arrangement != .beside else { return false }
        let v = pan.velocity(in: view)
        guard abs(v.x) > abs(v.y) * 1.4 else { return false }
        return isOpen ? v.x < 0 : panAnywhere && v.x > 0
    }

    /// Alongside the page's scrolling, but not a row's tap: the page follows
    /// the finger, so it lifts on the row it went down on, which would open.
    func gestureRecognizer(_ g: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer) -> Bool {
        other is UIPanGestureRecognizer
    }
}

extension UIColor {
    /// The menu's ground. Dark, a near-black grey rather than the pages'
    /// black base (as Material's dark surface, #121212), so the menu reads
    /// as a panel; light, the pages' white.
    static let menuBackground = UIColor { t in
        t.userInterfaceStyle == .dark ? UIColor(white: 0.07, alpha: 1) : .systemBackground
    }

    /// Fades the page back over the menu, to a card a step lighter than the
    /// menu: the system's elevated grey (#1C1C1E) in the dark, a light grey
    /// in the light.
    static let pageFade = UIColor { t in
        (t.userInterfaceStyle == .dark ? UIColor.tertiarySystemBackground : .secondarySystemBackground)
            .resolvedColor(with: t).withAlphaComponent(0.65)
    }
}

/// New: connect an agent, add a machine, or scan or enter a pairing code.
struct NewMenu: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Menu {
            Button("Connect an Agent…", systemImage: "link") { model.sheet = .connectAgent }
            Button("Add a Machine…", systemImage: "desktopcomputer") { model.sheet = .addMachine }
            Button("Scan a Code…", systemImage: "qrcode.viewfinder") { model.sheet = .scan }
            Button("Enter a Pairing Code…", systemImage: "number") { model.sheet = .enterCode }
        } label: {
            Image(systemName: "plus")
        }
        .accessibilityLabel("New")
        .accessibilityIdentifier("new")
    }
}

/// The menu button on root pages, with a dot when something needs the owner; where the menu stands beside the
/// page but is hidden, it shows it again.
struct MenuButton: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Button {
            model.drawerOpen = true
        } label: {
            Image(systemName: model.menuBeside ? "sidebar.left" : "line.3.horizontal")
                .overlay(alignment: .topTrailing) {
                    if model.attentionCount > 0 {
                        Circle().fill(.orange).frame(width: 8, height: 8).offset(x: 5, y: -4)
                    }
                }
        }
        .accessibilityLabel(model.attentionCount > 0 ? Text("Menu, \(model.attentionCount) need you") : Text("Menu"))
        .accessibilityIdentifier("menu")
    }
}

extension EnvironmentValues {
    /// Whether the system stands this view's bars on the side, not along the top (iPhone Duo closed, or open and
    /// on its side): what a view that draws its own top reads, to leave its buttons to the system's bar.
    var barsOnTheSide: Bool {
        if #available(iOS 27.1, *) { toolbarVerticalEdge != nil } else { false }
    }
}

/// The menu button in a root page's bar; none while the menu stands in view beside the page.
struct MenuToolbarItem: ToolbarContent {
    @Environment(AppModel.self) private var model

    var body: some ToolbarContent {
        if !model.menuInView { ToolbarItem(placement: .topBarLeading) { MenuButton() } }
    }
}

extension View {
    /// The menu button on a root page, and the sync line in the middle of
    /// its bar: once the title is small, in its place; while it is large,
    /// the bar shows nothing there (UIKit hides a title view then), so the
    /// line is laid over the bar (PageView).
    func rootToolbar() -> some View { modifier(RootToolbar()) }
}

private struct RootToolbar: ViewModifier {
    @Environment(AppModel.self) private var model

    func body(content: Content) -> some View {
        content
            .onScrollGeometryChange(for: Bool.self) { g in g.contentOffset.y + g.contentInsets.top > smallTitleAfter } action: { _, small in
                model.rootTitleSmall = small
            }
            .onAppear { model.rootTitleSmall = false } // a root page shows from its top
            .toolbar {
                MenuToolbarItem()
                if (model.rootTitleSmall || !model.root.titleLarge) && model.shownSync != nil { ToolbarItem(placement: .principal) { SyncLine() } }
            }
    }
}

/// How far a root page scrolls before its title is small.
private let smallTitleAfter: CGFloat = 44
