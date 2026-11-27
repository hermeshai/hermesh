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
    @State private var screenWidth: CGFloat = 0

    var body: some View {
        @Bindable var model = model
        SideMenu(model: model, open: $model.drawerOpen, panAnywhere: model.path.isEmpty)
            .environment(\.screenWidth, screenWidth)
            .ignoresSafeArea(edges: model.isDemo ? [.top, .horizontal] : .all)
            .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { screenWidth = $0 }
            // Over the menu and the page alike: a session archived from the menu says so where it can be seen.
            .overlay(alignment: .bottom) {
                if let t = model.toast {
                    ToastView(toast: t) { model.toast = nil }
                        .padding(.bottom, 8)
                        .transition(.move(edge: .bottom).combined(with: .opacity))
                }
            }
            .animation(.smooth(duration: 0.35), value: model.toast)
            .sheet(item: Binding(get: { model.accountPresented ? nil : model.sheet }, set: { model.sheet = $0 })) { sheet in
                SheetHost(kind: sheet).environment(model)
            }
            .sheet(isPresented: $model.accountPresented) {
                SheetHost(kind: .account).environment(model)
                    .sheet(item: $model.sheet) { sheet in
                        SheetHost(kind: sheet).environment(model)
                    }
            }
    }
}

/// The page under the menu: the navigation stack.
private struct PageView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var model = model
        NavigationStack(path: $model.path) {
            rootPage.navigationDestination(for: Route.self) { routePage($0) }
        }
        .overlay(alignment: .top) { // in the middle of a root page's bar while its title is large
            if model.path.isEmpty && model.root.titleLarge && !model.rootTitleSmall && model.shownSync != nil {
                SyncLine().frame(height: 44)
            }
        }
        .task(id: model.sync) { await model.showSync(model.sync) }
    }

    @ViewBuilder private var rootPage: some View {
        switch model.root {
        case .her: HerView()
        case .overview: OverviewView()
        case .grants: GrantsView()
        case .machines: EnvironmentsView()
        case .newSession(let machine, let project): SessionPage(machineID: machine, projectID: project).id("new/\(machine)/\(project)")
        case .session(let id): SessionOrHerPage(sessionID: id).id(id)
        }
    }
}

/// Routes a task session to Her's chat when it is a conversation of hers (docs/her-conversations.md §3.2), else SessionPage.
struct SessionOrHerPage: View {
    @Environment(AppModel.self) private var model
    let sessionID: String

    var body: some View {
        if model.session(sessionID)?.isHerConversation == true {
            HerConversationPage(sessionID: sessionID)
        } else {
            SessionPage(sessionID: sessionID)
        }
    }
}

@ViewBuilder func routePage(_ route: Route) -> some View {
    switch route {
    case .machines: EnvironmentsView(isRoot: false)
    case .tasks: TasksView()
    case .grants: GrantsView(isRoot: false)
    case .session(let id): SessionOrHerPage(sessionID: id).id(id)
    case .grant(let id): GrantView(id: id).syncSubtitle()
    case .machine(let id): EnvironmentView(id: id).syncSubtitle()
    case .machineAgent(let machine, let agent): MachineAgentView(machineID: machine, agent: agent).syncSubtitle()
    case .environmentName(let id): EnvironmentNameView(id: id)
    case .providers(let id): ProvidersView(id: id)
    case .projects(let id): ProjectsView(id: id)
    case .project(let machine, let project): ProjectView(machineID: machine, projectID: project)
    case .environmentHer(let id): EnvironmentHerView(id: id)
    case .storage(let id): StorageView(id: id)
    case .storageProject(let machine, let project): StorageProjectView(machineID: machine, projectID: project)
    case .account: AccountView()
    case .appearance: AppearanceView()
    case .herSettings: HerSettings()
    case .herConnectors: HerConnectorsPage()
    case .herConnector(let name): HerConnectorPage(name: name)
    case .herConnectorTools(let name): HerConnectorToolsPage(name: name)
    case .herConnectorGroup(let id): HerConnectorGroupPage(id: id)
    case .progress: ProgressPage()
    case .progressAgent(let id): ProgressAgentPage(agentID: id).id(id)
    case .routines: RoutinesPage()
    case .herFiles: HerFilesPage()
    case .sessionFile(let session, let run, let path, let line, let end): SessionFilePage(sessionID: session, run: run, path: path, line: line, endLine: end)
    case .sessionFolder(let session, let dir): SessionFolderPage(sessionID: session, dir: dir).id("\(session)/\(dir)")
    case .sessionAttachment(let session, let machine, let ref): SessionAttachmentPage(sessionID: session, machineID: machine, ref: ref)
    case .routine(let id): RoutinePage(routineID: id)
    case .routineRun(let routine, let run): RoutineRunPage(routineID: routine, runID: run)
    case .herMemory: HerEnvironmentPage { chat, machine in HerMemoryRoute(chat: chat, machine: machine) }
    case .herModel: HerModelPage()
    case .herProvider(let id): HerProviderPage(id: id)
    case .herEnvironment: HerEnvironmentSettings()
    case .herStorage: HerStoragePage()
    case .herConsole: HerConsolePage()
    case .herSkills: HerSkillsPage()
    case .herSkill(let dir): HerSkillPage(dir: dir)
    case .herSearch: HerSearchPage()
    case .herBrowser: HerBrowserPage()
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
            rail: UIHostingController(rootView: RailView().environment(model)),
            page: UIHostingController(rootView: PageView().environment(model)),
            width: 322)
    }

    func updateUIViewController(_ c: SideMenuController, context: Context) {
        c.panAnywhere = panAnywhere
        c.onChange = { open = $0 }
        c.onLayout = { model.wide = $0; model.verticalBars = $1 }
        c.onRail = { model.menuRail = $0 }
        c.onWidth = { model.menuWidth = $0 }
        c.set(width: model.menuWidth)
        c.set(rail: model.menuRail)
        c.set(open: open)
    }
}

/// A horizontal pan recognizer inside a page that claims right-swipes at a window point
/// (for instance swiping Her back to Agent on New Session) so the side menu yields to it.
@MainActor protocol HorizontalSwipeBackRecognizer: UIGestureRecognizer {
    func claimsRightSwipe(at pointInWindow: CGPoint) -> Bool
}

final class SideMenuController: UIViewController, UIGestureRecognizerDelegate {
    /// How the menu and the page share the screen. An iPhone: the page
    /// slides aside by the menu's width and stays in view, a card beside it
    /// (drawer). Where the system stands the bars on the side (iPhone Duo
    /// closed), that strip of the screen is the bars' in every state, and a
    /// page beside the menu would lie under them: the menu takes the whole
    /// page's place (cover). At regular width, wider than tall (iPhone Duo
    /// open, on its side), both show side by side (beside), as the Mac's
    /// sidebar: the menu expanded, or its rail of icons, never hidden
    /// (WideLayout); with no room for it expanded, it shows over the page
    /// when asked. Upright, the page keeps its width and the menu slides
    /// over it, as on an iPhone.
    private enum Arrangement { case drawer, cover, beside }

    var panAnywhere = true
    var onChange: (Bool) -> Void = { _ in }
    /// Told how the wide screen is shared and whether the system stands its bars on the side, as they change.
    var onLayout: (WideLayout?, Bool) -> Void = { _, _ in }
    /// Told when the owner expands the menu beside the page or shrinks it to its rail, and the width they dragged it to.
    var onRail: (Bool) -> Void = { _ in }
    var onWidth: (Double) -> Void = { _ in }
    private let menu: UIViewController
    private let rail: UIViewController
    private let page: UIViewController
    private let width: CGFloat
    private let card = UIView() // moves, and casts the shadow
    private let clip = UIView() // rounds the page's corners
    private let dim = UIView() // fades the page back; a tap closes the menu
    private let divider = UIView() // between the menu and the page, side by side
    private let grip = UIView() // over the divider: drags the menu wider or narrower
    private let scrim = UIView() // fades the page back under the menu shown over it; a tap puts the menu away
    private let menuTap = UITapGestureRecognizer()
    private let railTap = UITapGestureRecognizer()
    private let menuPan = UIPanGestureRecognizer()
    private let railPan = UIPanGestureRecognizer()
    /// The menu's width beside the page; whether the owner shrank it to its rail there; and how the screen is
    /// shared, as last worked out.
    private var besideWidth: CGFloat = 322
    private var railChosen = false
    private var layout: WideLayout?
    private var verticalBars = false
    /// The menu shows over the page, from the rail, where there is no room for it beside the page.
    private var peek = false
    /// The menu moves between expanded and its rail: it stays in view until it rests.
    private var sliding = false
    private let railWidth: CGFloat = 74
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

    init(menu: UIViewController, rail: UIViewController, page: UIViewController, width: CGFloat) {
        self.menu = menu
        self.rail = rail
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
        addChild(rail)
        view.addSubview(rail.view)
        rail.didMove(toParent: self)
        rail.view.isHidden = true
        view.addSubview(divider)
        scrim.backgroundColor = UIColor.black.withAlphaComponent(0.22)
        scrim.alpha = 0
        scrim.addGestureRecognizer(UITapGestureRecognizer(target: self, action: #selector(closePeek)))
        scrim.isAccessibilityElement = true
        scrim.accessibilityLabel = String(localized: "Close the menu")
        scrim.accessibilityTraits = .button
        scrim.accessibilityIdentifier = "menu-scrim"
        view.addSubview(scrim)
        view.addSubview(grip)
        grip.addGestureRecognizer(UIPanGestureRecognizer(target: self, action: #selector(resize(_:))))
        // Beside the page, a finger also moves the menu: right on the rail expands it, left on the menu shrinks it.
        let out = UISwipeGestureRecognizer(target: self, action: #selector(swipeRail))
        out.direction = .right
        rail.view.addGestureRecognizer(out)
        let back = UISwipeGestureRecognizer(target: self, action: #selector(swipeMenu))
        back.direction = .left
        menu.view.addGestureRecognizer(back)
        for (tap, host) in [(menuTap, menu.view!), (railTap, rail.view!)] {
            tap.addTarget(self, action: #selector(dismissKeyboardFromMenu))
            tap.cancelsTouchesInView = false
            tap.delaysTouchesBegan = false
            tap.delaysTouchesEnded = false
            tap.delegate = self
            host.addGestureRecognizer(tap)
        }
        for (pan, host) in [(menuPan, menu.view!), (railPan, rail.view!)] {
            pan.addTarget(self, action: #selector(panMenu(_:)))
            pan.cancelsTouchesInView = false
            pan.delaysTouchesBegan = false
            pan.delaysTouchesEnded = false
            pan.delegate = self
            host.addGestureRecognizer(pan)
        }
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
        // The rail keeps its width clear of the screen's leading safe area (the island, on its side).
        let railFull = railWidth + view.safeAreaInsets.left
        let wide = arrangement == .beside ? wideLayout(width: b.width, menu: besideWidth, expanded: !railChosen) : nil
        let railed = wide?.rail == true
        if !railed { peek = false }
        let pageX = wide == nil ? 0 : railed ? railFull : besideWidth
        let pageBounds = CGRect(x: 0, y: 0, width: b.width - pageX, height: b.height)
        menu.view.bounds = CGRect(x: 0, y: 0, width: menuWidth, height: b.height)
        // As a rail, the expanded menu waits off the screen's leading edge, and comes over the page when asked.
        menu.view.center = CGPoint(x: menuWidth / 2 - (railed && !peek ? menuWidth : 0), y: b.midY)
        rail.view.frame = CGRect(x: 0, y: 0, width: railFull, height: b.height)
        rail.view.isHidden = wide == nil
        rail.view.accessibilityElementsHidden = !railed || peek
        scrim.frame = b
        scrim.alpha = peek ? 1 : 0
        scrim.isHidden = !peek && !sliding
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
        // On the page's edge, not the menu's: the expanded menu lies over what is under its own width.
        divider.frame = CGRect(x: pageX, y: 0, width: hairline, height: b.height)
        divider.isHidden = wide == nil
        // Mostly over the menu: the page's edge is where its swipe back starts.
        grip.frame = CGRect(x: pageX - 16, y: 0, width: 22, height: b.height)
        grip.isHidden = wide == nil || railed
        // Out of view, the menu is out of VoiceOver's way too.
        if !railed || peek { menu.view.isHidden = false } else if !sliding { menu.view.isHidden = true }
        let bars: Bool = if #available(iOS 27.1, *) { traitCollection.verticalBarEdge != .unspecified } else { false }
        if wide != layout || bars != verticalBars {
            layout = wide
            verticalBars = bars
            // After the layout pass: this writes the app's state, which lays views out again.
            DispatchQueue.main.async { [self] in onLayout(wide, bars) }
        }
        guard before != (arrangement, travel) else { return }
        // Another arrangement (the device opened or closed, or turned): at rest in it, the menu closed.
        generation += 1
        for v in [card, menu.view!, dim] { v.layer.removeAllAnimations() }
        settle()
        if arrangement == .beside {
            card.transform = .identity
            menu.view.transform = .identity
            menu.view.alpha = 1
            // Over the page and the rail, for when it shows over them.
            for v in [rail.view!, divider, scrim, menu.view!, grip] { view.bringSubviewToFront(v) }
        } else {
            view.sendSubviewToBack(menu.view)
            apply(0)
        }
        let wasOpen = isOpen
        isOpen = false
        // After the layout pass: this writes the app's state, which lays views out again.
        if wasOpen { DispatchQueue.main.async { [self] in onChange(false) } }
    }

    /// Opens or closes the menu to match the app's state. Beside the page, opening it expands the rail: beside
    /// the page where there is room (the owner's choice from then on), else over the page until it is put away
    /// or a place is chosen.
    func set(open: Bool) {
        guard arrangement != .beside else {
            if !open {
                if peek { hideKeyboard(); peek = false; slide() }
            } else if let layout, layout.rail, !layout.menuFits {
                if !peek { hideKeyboard(); peek = true; slide() }
            } else {
                // After the update this is called in: these write the app's state.
                let railed = layout?.rail == true
                if railed { hideKeyboard() }
                DispatchQueue.main.async { [self] in
                    onChange(false)
                    if railed { onRail(false) }
                }
            }
            return
        }
        guard open != isOpen else { return }
        hideKeyboard()
        animate(open: open, velocity: 0)
    }

    /// The owner's choice of the menu beside the page, expanded or its rail: the page slides to its new place.
    func set(rail: Bool) {
        guard rail != railChosen else { return }
        hideKeyboard()
        railChosen = rail
        slide()
    }

    /// Beside the page, moves the menu, its rail and the page to where the layout now has them.
    private func slide() {
        guard arrangement == .beside, view.window != nil else { return view.setNeedsLayout() }
        menu.view.isHidden = false
        scrim.isHidden = false
        sliding = true
        generation += 1
        let g = generation
        UIView.animate(springDuration: 0.32, bounce: 0) {
            self.view.setNeedsLayout()
            self.view.layoutIfNeeded()
        } completion: { _ in
            guard g == self.generation else { return }
            self.sliding = false
            self.view.setNeedsLayout()
        }
    }

    @objc private func closePeek() {
        guard peek else { return }
        hideKeyboard()
        peek = false
        slide()
        onChange(false)
    }

    @objc private func swipeRail() {
        guard arrangement == .beside else { return }
        hideKeyboard()
        onChange(true) // as the rail's own button
    }

    @objc private func swipeMenu() {
        guard arrangement == .beside else { return }
        hideKeyboard()
        if peek { closePeek() } else { onRail(true) }
    }

    @objc private func dismissKeyboardFromMenu() {
        hideKeyboard()
    }

    @objc private func panMenu(_ g: UIPanGestureRecognizer) {
        if g.state == .began { hideKeyboard() }
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
            hideKeyboard()
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
        hideKeyboard()
        animate(open: false, velocity: 0)
        onChange(false)
    }

    @objc private func pan(_ g: UIPanGestureRecognizer) {
        switch g.state {
        case .began:
            hideKeyboard()
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
        if g === menuPan || g === railPan { return true }
        guard let pan = g as? UIPanGestureRecognizer else { return true }
        guard arrangement != .beside else { return false }
        let v = pan.velocity(in: view)
        guard abs(v.x) > abs(v.y) * 1.4 else { return false }
        guard isOpen || !(panAnywhere && v.x > 0) || !scrollsBack(at: pan.location(in: view)) else { return false }
        return isOpen ? v.x < 0 : panAnywhere && v.x > 0
    }

    /// A sideways scroller or horizontal swipe control under the finger that is not at its start
    /// (the overview's deck on a later card, or New Session on Her): a swipe to the right is its own,
    /// to go back in it, before it is the menu's.
    private func scrollsBack(at point: CGPoint) -> Bool {
        let inWindow = view.convert(point, to: nil)
        var v = view.hitTest(point, with: nil)
        while let cur = v {
            if let s = cur as? UIScrollView, s.isScrollEnabled, s.contentSize.width > s.bounds.width + 1, s.contentOffset.x > 1 - s.adjustedContentInset.left { return true }
            if cur.gestureRecognizers?.contains(where: { ($0 as? HorizontalSwipeBackRecognizer)?.claimsRightSwipe(at: inWindow) == true }) == true { return true }
            v = cur.superview
        }
        return false
    }

    /// Alongside the page's scrolling, but not a row's tap: the page follows
    /// the finger, so it lifts on the row it went down on, which would open.
    func gestureRecognizer(_ g: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer) -> Bool {
        if [menuTap, railTap, menuPan, railPan].contains(where: { $0 === g || $0 === other }) { return true }
        if g is HorizontalSwipeBackRecognizer || other is HorizontalSwipeBackRecognizer { return false }
        return other is UIPanGestureRecognizer
    }

    func gestureRecognizer(_ g: UIGestureRecognizer, shouldRequireFailureOf other: UIGestureRecognizer) -> Bool {
        guard !isOpen, g is UIPanGestureRecognizer, g !== menuPan, g !== railPan else { return false }
        if let back = other as? HorizontalSwipeBackRecognizer,
           back.claimsRightSwipe(at: view.convert(g.location(in: view), to: nil)) {
            return true
        }
        return false
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

/// Connect an agent, add an environment, or scan or enter a pairing code: shared by Overview's `+` and the sidebar's
/// account menu.
struct NewMenuItems: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Button("Connect an Agent…", systemImage: "link") { model.sheet = .connectAgent }
        Button("Add Environment…", systemImage: "desktopcomputer") { model.sheet = .addMachine }
        Button("Scan a Code…", systemImage: "qrcode.viewfinder") { model.sheet = .scan }
        Button("Enter a Pairing Code…", systemImage: "number") { model.sheet = .enterCode }
    }
}

/// New: connect an agent, add an environment, or scan or enter a pairing code.
struct NewMenu: View {
    var body: some View {
        Menu {
            NewMenuItems()
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

extension EnvironmentValues {
    /// Whether the system stands this view's bars on the side, not along the top (iPhone Duo closed, or open and
    /// on its side): what a view that draws its own top reads, to leave its buttons to the system's bar.
    var barsOnTheSide: Bool {
        if #available(iOS 27.1, *) { toolbarVerticalEdge != nil } else { false }
    }
    /// Full hardware screen width of the root container, used to align split boundaries with iPhone Duo's center fold.
    @Entry var screenWidth: CGFloat = 0
}

/// Beside the expanded menu, in the menu button's place on a root page: the page takes the menu's room, and the menu
/// shrinks to its rail (whose own button expands it again).
struct WidenPageButton: View {
    @Environment(AppModel.self) private var model
    /// In a page's own bar: a circle of the system's glass, as its other buttons.
    var glass = false

    var body: some View {
        Button {
            model.menuRail = true
        } label: {
            if glass {
                Image(systemName: "arrow.up.left.and.arrow.down.right").font(.body.weight(.medium)).frame(width: 44, height: 44)
                    .glassEffect(.regular.interactive(), in: .circle)
                    .contentShape(.circle)
            } else {
                Image(systemName: "arrow.up.left.and.arrow.down.right")
            }
        }
        .accessibilityLabel("Collapse the menu")
        .accessibilityIdentifier("menu-hide")
    }
}

/// In a root page's bar: where the system stands the bars on the side (iPhone Duo), the thumb-zone Liquid Glass
/// capsule at the bottom of the vertical bar (`[ Menu | Her | Overview ]` when closed, `[ Her | Overview ]` when
/// unfolded beside the menu, with no low-frequency avatar outside the sidebar); elsewhere the menu button at the top
/// leading end, or the button that widens the page over the expanded menu.
struct MenuToolbarItem: ToolbarContent {
    @Environment(AppModel.self) private var model
    @Environment(\.barsOnTheSide) private var sideBars

    var body: some ToolbarContent {
        if #available(iOS 27.1, *), sideBars {
            if model.menuBeside && model.menuInView {
                ToolbarItem(placement: .topBarLeading) { WidenPageButton() }
            }
            ToolbarItemGroup(placement: .bottomBar) {
                if !model.menuBeside { MenuButton() }
                DuoHerButton()
                DuoOverviewButton()
            }
            .axisBehavior(.verticalPreferred)
        } else if !model.menuBeside {
            ToolbarItem(placement: .topBarLeading) { MenuButton() }
        } else if model.menuInView {
            ToolbarItem(placement: .topBarLeading) { WidenPageButton() }
        }
    }
}

extension View {
    /// The menu button on a root page, and the sync line in the middle of
    /// its bar: once the title is small, in its place; while it is large,
    /// the bar shows nothing there (UIKit hides a title view then), so the
    /// line is laid over the bar (PageView). A page with a box (`sync` false) says its connection in its own inline
    /// status slot instead (`ConnectionLine` / `HerConnectionBubble`).
    @ViewBuilder func rootToolbar(enabled: Bool = true, sync: Bool = true) -> some View {
        if enabled { modifier(RootToolbar(sync: sync)) } else { self.syncSubtitle() }
    }
}

private struct RootToolbar: ViewModifier {
    @Environment(AppModel.self) private var model
    let sync: Bool

    func body(content: Content) -> some View {
        content
            .onScrollGeometryChange(for: Bool.self) { g in g.contentOffset.y + g.contentInsets.top > smallTitleAfter } action: { _, small in
                model.rootTitleSmall = small
            }
            .onAppear { model.rootTitleSmall = false } // a root page shows from its top
            .toolbar {
                MenuToolbarItem()
                if sync && (model.rootTitleSmall || !model.root.titleLarge) && model.shownSync != nil { ToolbarItem(placement: .principal) { SyncLine() } }
            }
    }
}

/// How far a root page scrolls before its title is small.
private let smallTitleAfter: CGFloat = 44
