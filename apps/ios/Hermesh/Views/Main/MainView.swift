import HermeshSessions
import SwiftUI
import UIKit

/// The side menu and, over it, the page it leads to. The page slides right
/// to show the menu: a drag from anywhere on a root page, or the menu
/// button. Pushed pages keep the system's swipe back. The menu sits on the
/// darkest layer; the page rises above it as a lighter card that fades back.
struct MainView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var model = model
        SideMenu(model: model, open: $model.drawerOpen, panAnywhere: model.path.isEmpty)
            .ignoresSafeArea()
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
                    Group {
                    switch route {
                    case .session(let id): SessionView(id: id)
                    case .grant(let id): GrantView(id: id)
                    case .machine(let id): MachineView(id: id)
                    case .machineAgent(let machine, let agent): MachineAgentView(machineID: machine, agent: agent)
                    case .grantSessions(let id): ScopedSessionsView(filter: SessionFilter(grant: id), title: String(localized: "“\(model.clients.first { $0.clientID == id }?.label ?? "")” Sessions"))
                    case .machineSessions(let id): ScopedSessionsView(filter: SessionFilter(machine: id), title: String(localized: "Sessions on \(model.machineName(id))"))
                    }
                    }
                    .syncLine()
                }
        }
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
        case .overview: OverviewView()
        case .grants: GrantsView()
        case .machines: MachinesView()
        case .sessions: SessionsView()
        case .session(let id): SessionView(id: id).syncLine() // its title is in the bar
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
        let width: CGFloat = 322
        return SideMenuController(
            menu: UIHostingController(rootView: DrawerView(width: width).environment(model)),
            page: UIHostingController(rootView: PageView().environment(model)),
            width: width)
    }

    func updateUIViewController(_ c: SideMenuController, context: Context) {
        c.panAnywhere = panAnywhere
        c.onChange = { open = $0 }
        c.set(open: open)
    }
}

final class SideMenuController: UIViewController, UIGestureRecognizerDelegate {
    var panAnywhere = true
    var onChange: (Bool) -> Void = { _ in }
    private let menu: UIViewController
    private let page: UIViewController
    private let width: CGFloat
    private let card = UIView() // moves, and casts the shadow
    private let clip = UIView() // rounds the page's corners
    private let dim = UIView() // fades the page back; a tap closes the menu
    private let radius: CGFloat = 44
    private var isOpen = false
    private var startX: CGFloat = 0
    private var generation = 0
    private var sideMargins = NSDirectionalEdgeInsets.zero

    init(menu: UIViewController, page: UIViewController, width: CGFloat) {
        self.menu = menu
        self.page = page
        self.width = width
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
        // Closed, the shadow falls off the screen's left edge.
        card.layer.shadowColor = UIColor.black.cgColor
        card.layer.shadowOpacity = 0.15
        card.layer.shadowRadius = 12
        card.layer.shadowOffset = CGSize(width: -6, height: 0)
        let pan = UIPanGestureRecognizer(target: self, action: #selector(pan(_:)))
        pan.delegate = self
        view.addGestureRecognizer(pan)
        settle()
        apply(0)
    }

    override func viewDidLayoutSubviews() {
        super.viewDidLayoutSubviews()
        let b = view.bounds
        menu.view.bounds = CGRect(x: 0, y: 0, width: width, height: b.height)
        menu.view.center = CGPoint(x: width / 2, y: b.midY)
        // Bounds and center: the card's transform moves it.
        card.bounds = b
        card.center = CGPoint(x: b.midX, y: b.midY)
        card.layer.shadowPath = UIBezierPath(roundedRect: b, cornerRadius: radius).cgPath
        clip.frame = b
        page.view.frame = b
        if card.transform.isIdentity { sideMargins = page.systemMinimumLayoutMargins }
        dim.frame = b
    }

    /// Opens or closes the menu to match the app's state.
    func set(open: Bool) {
        guard open != isOpen else { return }
        animate(open: open, velocity: 0)
    }

    /// Lays the two out for p, from 0 (closed) to 1 (open).
    private func apply(_ p: CGFloat) {
        card.transform = CGAffineTransform(translationX: width * p, y: 0)
        menu.view.transform = CGAffineTransform(translationX: -width * 0.25 * (1 - p), y: 0)
        menu.view.alpha = 0.4 + 0.6 * p
        dim.alpha = p
    }

    /// The page as a card, while the menu shows or moves.
    private func lift() {
        pinMargins()
        clip.layer.cornerRadius = radius
        dim.isHidden = false
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
    }

    /// Stops the page where it is on screen, mid-animation or not.
    private func stop() -> CGFloat {
        let x = card.layer.presentation()?.affineTransform().tx ?? card.transform.tx
        for v in [card, menu.view!, dim] { v.layer.removeAllAnimations() }
        apply(x / width)
        return x
    }

    /// velocity is relative to the distance left to go, as UIKit's springs take it.
    private func animate(open: Bool, velocity: CGFloat) {
        isOpen = open
        generation += 1
        let g = generation
        lift()
        // 0.32 s: measured frame by frame, the page then reaches 90% of the
        // way in a quarter second, as a push does (0.5 s took 0.4 s).
        UIView.animate(springDuration: 0.32, bounce: 0, initialSpringVelocity: velocity, options: [.allowUserInteraction, .beginFromCurrentState]) {
            self.apply(open ? 1 : 0)
        } completion: { _ in
            if g == self.generation && !self.isOpen { self.settle() }
        }
    }

    @objc private func closeFromDim() {
        animate(open: false, velocity: 0)
        onChange(false)
    }

    @objc private func pan(_ g: UIPanGestureRecognizer) {
        switch g.state {
        case .began:
            generation += 1
            startX = stop()
            lift()
        case .changed:
            apply(min(width, max(0, startX + g.translation(in: view).x)) / width)
        case .ended, .cancelled, .failed:
            let x = card.transform.tx
            let v = g.velocity(in: view).x
            // Where a flick would carry the page (UIScrollView's deceleration).
            let open = g.state == .ended ? x + v * 0.998 / (1 - 0.998) / 1000 > width / 2 : isOpen
            let left = (open ? width : 0) - x
            animate(open: open, velocity: abs(left) < 1 ? 0 : v / left)
            onChange(open)
        default:
            break
        }
    }

    func gestureRecognizerShouldBegin(_ g: UIGestureRecognizer) -> Bool {
        guard let pan = g as? UIPanGestureRecognizer else { return true }
        let v = pan.velocity(in: view)
        guard abs(v.x) > abs(v.y) * 1.4 else { return false }
        return isOpen ? v.x < 0 : panAnywhere && v.x > 0
    }

    func gestureRecognizer(_ g: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer) -> Bool {
        true
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

/// New: connect an agent, add a machine, or enter a pairing code.
struct NewMenu: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Menu {
            Button("Connect an Agent…", systemImage: "link") { model.sheet = .connectAgent }
            Button("Add a Machine…", systemImage: "desktopcomputer") { model.sheet = .addMachine }
            Button("Enter a Pairing Code…", systemImage: "number") { model.sheet = .enterCode }
        } label: {
            Image(systemName: "plus")
        }
        .accessibilityLabel("New")
        .accessibilityIdentifier("new")
    }
}

/// The menu button on root pages, with a dot when something needs the owner.
struct MenuButton: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Button {
            model.drawerOpen = true
        } label: {
            Image(systemName: "line.3.horizontal")
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

extension View {
    /// The menu button on a root page, and the sync line in its subtitle's
    /// place: under the large title, and in the bar once the title is small
    /// (a large-title page shows nothing in the middle of its bar).
    func rootToolbar() -> some View {
        toolbar {
            ToolbarItem(placement: .topBarLeading) { MenuButton() }
            ToolbarItem(placement: .largeSubtitle) { SyncLine(inBar: true).frame(maxWidth: .infinity, alignment: .leading) }
            ToolbarItem(placement: .subtitle) { SyncLine(inBar: true) }
        }
    }
}
