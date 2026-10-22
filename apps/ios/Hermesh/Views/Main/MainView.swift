import HermeshSessions
import SwiftUI

/// The side menu and, over it, the page it leads to. The page slides right
/// to show the menu: a drag from anywhere on a root page, or the menu
/// button. Pushed pages keep the system's swipe back.
struct MainView: View {
    @Environment(AppModel.self) private var model
    @GestureState private var drag: CGFloat = 0
    private let width: CGFloat = 322

    var body: some View {
        @Bindable var model = model
        let offset = max(0, min(width, (model.drawerOpen ? width : 0) + drag))
        ZStack(alignment: .leading) {
            DrawerView(width: width)
            NavigationStack(path: $model.path) {
                rootPage
                    .navigationDestination(for: Route.self) { route in
                        switch route {
                        case .session(let id): SessionView(id: id)
                        case .grant(let id): GrantView(id: id)
                        case .machine(let id): MachineView(id: id)
                        case .usage: UsageView()
                        }
                    }
            }
            .overlay(alignment: .bottom) {
                if let t = model.toast {
                    ToastView(toast: t) { model.toast = nil }
                        .padding(.bottom, 8)
                        .transition(.move(edge: .bottom).combined(with: .opacity))
                }
            }
            .clipShape(RoundedRectangle(cornerRadius: offset > 0 ? 44 : 0, style: .continuous))
            .overlay {
                if offset > 0 {
                    Color.black.opacity(0.18 * offset / width)
                        .clipShape(RoundedRectangle(cornerRadius: 44, style: .continuous))
                        .onTapGesture { withAnimation(.smooth(duration: 0.35)) { model.drawerOpen = false } }
                        .accessibilityLabel("Close the menu")
                        .accessibilityAddTraits(.isButton)
                        .accessibilityIdentifier("drawer-dim")
                }
            }
            .shadow(color: .black.opacity(offset > 0 ? 0.2 : 0), radius: 24, x: -6)
            .offset(x: offset)
            .simultaneousGesture(panGesture, including: model.path.isEmpty ? .all : .subviews)
        }
        .animation(.smooth(duration: 0.35), value: model.toast)
        .sheet(item: $model.sheet) { sheet in
            SheetHost(kind: sheet).environment(model)
        }
    }

    @ViewBuilder private var rootPage: some View {
        switch model.root {
        case .overview: OverviewView()
        case .grants: GrantsView()
        case .machines: MachinesView()
        case .sessions: SessionsView()
        case .session(let id): SessionView(id: id)
        }
    }

    private var panGesture: some Gesture {
        DragGesture(minimumDistance: 18)
            .updating($drag) { v, state, _ in
                guard abs(v.translation.width) > abs(v.translation.height) * 1.4 else { return }
                if model.drawerOpen ? v.translation.width < 0 : v.translation.width > 0 { state = v.translation.width }
            }
            .onEnded { v in
                guard abs(v.translation.width) > abs(v.translation.height) * 1.4 else { return }
                let open = model.drawerOpen ? v.predictedEndTranslation.width > -width / 2 : v.predictedEndTranslation.width > width / 2
                withAnimation(.smooth(duration: 0.35)) { model.drawerOpen = open }
            }
    }
}

/// The menu button on root pages, with a dot when something needs the owner.
struct MenuButton: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Button {
            withAnimation(.smooth(duration: 0.35)) { model.drawerOpen = true }
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
    /// The menu button on a root page.
    func rootToolbar() -> some View {
        toolbar {
            ToolbarItem(placement: .topBarLeading) { MenuButton() }
        }
    }
}
