import HermeshSessions
import SwiftUI

/// The side menu: search at the top, the three places (Overview, Grants,
/// Machines), then the sessions by state as the Mac's sidebar lists them,
/// and at the bottom the account.
struct DrawerView: View {
    @Environment(AppModel.self) private var model
    let width: CGFloat
    /// The size of the menu's buttons: the system's toolbar buttons, and the
    /// smallest target the HIG asks for.
    private let button: CGFloat = 44

    var body: some View {
        ZStack(alignment: .bottom) {
            ScrollView {
                VStack(alignment: .leading, spacing: 2) {
                    HStack {
                        Text("Hermesh").font(.largeTitle.bold())
                        Spacer()
                        Button {
                            model.sheet = .search
                        } label: {
                            // The button's size itself: the .glass button style pads its label.
                            Image(systemName: "magnifyingglass").font(.body.weight(.medium)).frame(width: button, height: button)
                                .glassEffect(.regular.interactive(), in: .circle)
                                .contentShape(.circle)
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Search")
                        .accessibilityIdentifier("search")
                    }
                    .padding(.leading, 24).padding(.trailing, 16).padding(.bottom, 14)
                    places
                    lists
                }
                .padding(.top, 8)
                .padding(.bottom, 110)
            }
            .scrollIndicators(.hidden)
            bottomBar
        }
        .frame(width: width)
        .frame(maxHeight: .infinity)
        .background(Color(.menuBackground))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("drawer")
    }

    private var places: some View {
        VStack(spacing: 2) {
            place(.overview, "Overview", "square.grid.2x2")
            place(.grants, "Grants", "checkmark.shield")
            place(.machines, "Machines", "desktopcomputer")
        }
    }

    private func place(_ r: Root, _ title: LocalizedStringKey, _ icon: String) -> some View {
        Button {
            model.open(r)
        } label: {
            Label(title, systemImage: icon).font(.body).frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 16).frame(height: 48)
        }
        .buttonStyle(RowStyle(selected: model.root == r))
        .padding(.horizontal, 10)
    }

    @ViewBuilder private var lists: some View {
        let visible = model.sessions.filter { $0.status != .archived }
        let waiting = visible.filter { $0.status == .waitingInput }.sorted { $0.updatedDate > $1.updatedDate }
        let failed = visible.filter { $0.isFailed }.sorted { $0.updatedDate > $1.updatedDate }
        let running = visible.filter(\.isActive).sorted { $0.createdDate > $1.createdDate }
        let recent = visible.filter { !$0.isActive && !$0.needsAttention }.sorted { $0.updatedDate > $1.updatedDate }.prefix(8)
        if !waiting.isEmpty || !model.pairings.isEmpty || !failed.isEmpty {
            header("Needs You")
            ForEach(waiting, id: \.sessionID) { s in item(s) { Circle().fill(.orange).frame(width: 9, height: 9) } }
            ForEach(model.pairings, id: \.code) { p in
                Button {
                    model.drawerOpen = false
                    model.sheet = .pairing(p.code)
                } label: {
                    row(PairingText.title(p)) { Circle().fill(.orange).frame(width: 9, height: 9) }
                }
                .buttonStyle(RowStyle(selected: false))
                .padding(.horizontal, 10)
            }
            ForEach(failed, id: \.sessionID) { s in item(s) { Circle().fill(.red).frame(width: 9, height: 9) } }
        }
        if !running.isEmpty {
            header("In Progress")
            ForEach(running, id: \.sessionID) { s in item(s) { ProgressView().controlSize(.small) } }
        }
        if !recent.isEmpty {
            header("Recent")
            ForEach(Array(recent), id: \.sessionID) { s in item(s) { EmptyView() } }
        }
        Button {
            model.open(.sessions)
        } label: {
            HStack(spacing: 4) {
                Text("All Sessions")
                Image(systemName: "chevron.right").font(.caption.weight(.semibold))
            }
            .font(.subheadline).foregroundStyle(.secondary).padding(.horizontal, 26).padding(.vertical, 12)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
    }

    private func header(_ title: LocalizedStringKey) -> some View {
        Text(title).font(.footnote).foregroundStyle(.secondary).padding(.horizontal, 26).padding(.top, 20).padding(.bottom, 6)
    }

    private func item(_ s: Session, @ViewBuilder mark: () -> some View) -> some View {
        let selected: Bool = { if case .session(let id) = model.root { return id == s.sessionID && model.path.isEmpty } else { return false } }()
        return Button {
            model.open(.session(s.sessionID))
        } label: {
            row(s.displayTitle, mark: mark)
        }
        .buttonStyle(RowStyle(selected: selected))
        .padding(.horizontal, 10)
        .accessibilityIdentifier("drawer-session-\(s.displayTitle)")
    }

    private func row(_ title: String, @ViewBuilder mark: () -> some View) -> some View {
        HStack(spacing: 12) {
            Text(title).lineLimit(1).frame(maxWidth: .infinity, alignment: .leading)
            mark().frame(width: 18)
        }
        .padding(.horizontal, 16).frame(height: 44)
    }

    private var bottomBar: some View {
        HStack {
            Button {
                model.sheet = .account
            } label: {
                Image(systemName: "person.fill")
                    .font(.body.weight(.medium)).foregroundStyle(.white).frame(width: button, height: button)
                    .background(LinearGradient(colors: [Color(red: 0.03, green: 0.57, blue: 0.70), Color(red: 0.18, green: 0.42, blue: 0.94), Color(red: 0.43, green: 0.24, blue: 0.94)], startPoint: .topLeading, endPoint: .bottomTrailing), in: .circle)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Account and devices")
            .accessibilityIdentifier("account")
            Spacer()
        }
        .padding(.horizontal, 16).padding(.bottom, 8).padding(.top, 24)
        .background(LinearGradient(colors: [Color(.menuBackground).opacity(0), Color(.menuBackground)], startPoint: .top, endPoint: .center))
    }
}

/// A row of the menu: highlighted while selected, and under the finger
/// from the moment it touches down.
private struct RowStyle: ButtonStyle {
    let selected: Bool

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .background(selected || configuration.isPressed ? AnyShapeStyle(.fill.secondary) : AnyShapeStyle(.clear), in: .capsule)
            .contentShape(.capsule)
    }
}
