/// How a wide screen (regular width, wider than tall: iPhone Duo open, on its side) shares its width, as the
/// Mac's window does (docs/her-team.md §14.9, apps/desktop layout.ts): the menu beside the page, the session
/// list as a column of its own, the page. The page keeps `pageMin`; what is beside it gives way as the screen
/// narrows: the menu shrinks to its rail of icons (it is never hidden), then the list beside a session (alone,
/// the list is the page).
public struct WideLayout: Equatable, Sendable {
    /// The menu is its rail of icons: the owner's choice, or no room for more.
    public var rail: Bool
    /// The session list stands as a column beside what it leads to.
    public var list: Bool
    /// The expanded menu fits beside the page: asked for from the rail, it stands there; else over the page.
    public var menuFits: Bool

    public init(rail: Bool, list: Bool, menuFits: Bool) {
        self.rail = rail
        self.list = list
        self.menuFits = menuFits
    }

    /// Widths (pt): the list's column, and the least the page keeps.
    public static let listWidth = 320.0
    public static let pageMin = 400.0
}

/// The layout at a width. menu and rail: the widths of the expanded menu and of its rail, the screen's leading
/// safe area included. expanded: the owner's choice. list: a session list is among the pages shown.
public func wideLayout(width: Double, menu: Double, rail: Double, expanded: Bool, list: Bool) -> WideLayout {
    let menuFits = width >= menu + (list ? WideLayout.listWidth : 0) + WideLayout.pageMin
    let railed = !(expanded && menuFits)
    let side = railed ? rail : menu
    return WideLayout(rail: railed, list: list && width - side >= WideLayout.listWidth + WideLayout.pageMin, menuFits: menuFits)
}
