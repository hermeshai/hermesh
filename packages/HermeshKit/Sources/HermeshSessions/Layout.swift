/// How a wide screen (regular width, wider than tall: iPhone Duo open, on its side) shares its width, as the
/// Mac's window does (docs/her-team.md §16, apps/desktop layout.ts): the menu, with the sessions in it, beside
/// the page. The page keeps `pageMin`; as the screen narrows the menu shrinks to its rail of icons (it is never
/// hidden).
public struct WideLayout: Equatable, Sendable {
    /// The menu is its rail of icons: the owner's choice, or no room for more.
    public var rail: Bool
    /// The expanded menu fits beside the page: asked for from the rail, it stands there; else over the page.
    public var menuFits: Bool

    public init(rail: Bool, menuFits: Bool) {
        self.rail = rail
        self.menuFits = menuFits
    }

    /// The least the page keeps (pt).
    public static let pageMin = 400.0
}

/// The layout at a width. menu: the width of the expanded menu, the screen's leading safe area included.
/// expanded: the owner's choice.
public func wideLayout(width: Double, menu: Double, expanded: Bool) -> WideLayout {
    let menuFits = width >= menu + WideLayout.pageMin
    return WideLayout(rail: !(expanded && menuFits), menuFits: menuFits)
}
