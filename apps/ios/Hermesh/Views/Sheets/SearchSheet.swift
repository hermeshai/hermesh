import HermeshSessions
import SwiftUI

/// Search, on a page of its own, as the App Store has it: the field at the
/// top under the title, focused. Before anything is typed, the recent
/// searches, the states to search within (as tokens in the field) and the
/// recent sessions; while typing, completions under the field and the
/// sessions that match. Cancelling the search closes it.
struct SearchSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""
    @State private var tokens: [SearchState] = []
    @State private var searching = false
    @State private var recent = RecentSearches.load()

    var body: some View {
        let q = query.trimmingCharacters(in: .whitespaces)
        NavigationStack {
            List {
                if q.isEmpty && tokens.isEmpty {
                    suggestions
                } else {
                    results(q)
                }
            }
            .listStyle(.plain)
            .navigationTitle("Search")
            .searchable(text: $query, tokens: $tokens, isPresented: $searching, placement: .navigationBarDrawer(displayMode: .always), prompt: "Search sessions") { t in
                Label(t.title, systemImage: t.symbol)
            }
            .searchSuggestions { completions(q) }
            .onSubmit(of: .search) { remember(q) }
            .task { searching = true }
            .onChange(of: searching) { if !searching { dismiss() } }
            .toolbar { CloseButton() }
        }
    }

    // MARK: Before typing

    @ViewBuilder private var suggestions: some View {
        if !recent.isEmpty {
            Section {
                ForEach(recent, id: \.self) { r in
                    Button {
                        query = r
                    } label: {
                        Label(r, systemImage: "clock.arrow.circlepath").foregroundStyle(.primary)
                    }
                    .swipeActions { Button("Delete", role: .destructive) { forget(r) } }
                }
            } header: {
                HStack(alignment: .firstTextBaseline) {
                    header("Recent Searches")
                    Spacer()
                    Button("Clear") { forget(nil) }.font(.subheadline).textCase(nil)
                        .accessibilityIdentifier("clear-recent")
                }
            }
        }
        Section {
            ForEach(SearchState.allCases) { t in
                let n = model.sessions.filter(t.matches).count
                Button {
                    tokens = [t]
                } label: {
                    HStack(spacing: 12) {
                        Image(systemName: t.symbol).font(.body.weight(.medium)).foregroundStyle(t.color)
                            .frame(width: 30, height: 30)
                            .background(t.color.opacity(0.14), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
                        Text(t.title).foregroundStyle(.primary)
                        Spacer()
                        Text("\(n)").foregroundStyle(.secondary).monospacedDigit()
                    }
                }
                .accessibilityIdentifier("search-state-\(t.rawValue)")
            }
        } header: {
            header("Search Within")
        }
        let latest = model.sessions.filter { $0.status != .archived }.sorted { $0.updatedDate > $1.updatedDate }.prefix(8)
        if !latest.isEmpty {
            Section {
                ForEach(Array(latest), id: \.sessionID) { s in sessionRow(s) }
            } header: {
                header("Recent")
            }
        }
    }

    // MARK: While typing

    @ViewBuilder private func completions(_ q: String) -> some View {
        if !q.isEmpty {
            ForEach(SearchState.allCases.filter { !tokens.contains($0) && $0.title.localizedCaseInsensitiveContains(q) }) { t in
                Label(t.title, systemImage: t.symbol).searchCompletion(t)
            }
            let titles = Array(Set(model.sessions.map(\.displayTitle).filter { $0.localizedCaseInsensitiveContains(q) && $0 != q })).sorted().prefix(4)
            ForEach(titles, id: \.self) { title in
                Label(title, systemImage: "magnifyingglass").searchCompletion(title)
            }
        }
    }

    @ViewBuilder private func results(_ q: String) -> some View {
        let found = searchSessions(model.sessions, query: q, filter: SessionFilter(), text: model.searchText)
            .filter { s in tokens.isEmpty || tokens.contains { $0.matches(s) } }
        if found.isEmpty {
            ContentUnavailableView.search(text: q)
                .listRowSeparator(.hidden)
                .accessibilityIdentifier("no-results")
        } else {
            Section {
                ForEach(found, id: \.sessionID) { s in sessionRow(s) }
            } header: {
                header("\(found.count) results")
            }
        }
    }

    // MARK: Parts

    private func header(_ title: LocalizedStringKey) -> some View {
        Text(title).font(.title3.bold()).foregroundStyle(.primary).textCase(nil)
    }

    private func sessionRow(_ s: Session) -> some View {
        Button {
            remember(query.trimmingCharacters(in: .whitespaces))
            model.sheet = nil
            model.open(.session(s.sessionID))
        } label: {
            SessionRow(session: s)
        }
        .buttonStyle(.plain)
        .listRowInsets(EdgeInsets())
    }

    private func remember(_ q: String) {
        guard !q.isEmpty else { return }
        recent = RecentSearches.add(q, to: recent)
    }

    /// One recent search, or (nil) all of them.
    private func forget(_ q: String?) {
        recent = q.map { q in recent.filter { $0 != q } } ?? []
        RecentSearches.save(recent)
    }
}

/// A state to search within, a token in the search field.
enum SearchState: String, CaseIterable, Identifiable, Hashable {
    case waiting, running, failed, done

    var id: String { rawValue }

    var title: String {
        switch self {
        case .waiting: String(localized: "Needs You")
        case .running: String(localized: "In Progress")
        case .failed: String(localized: "Failed")
        case .done: String(localized: "Done")
        }
    }

    var symbol: String {
        switch self {
        case .waiting: "questionmark.bubble"
        case .running: "bolt"
        case .failed: "exclamationmark.triangle"
        case .done: "checkmark.circle"
        }
    }

    var color: Color {
        switch self {
        case .waiting: .orange
        case .running: .green
        case .failed: .red
        case .done: .secondary
        }
    }

    func matches(_ s: Session) -> Bool {
        switch self {
        case .waiting: s.status == .waitingInput
        case .running: s.isActive
        case .failed: s.isFailed
        case .done: s.status == .idle && !s.isFailed
        }
    }
}

/// The searches made, newest first, kept on this iPhone.
enum RecentSearches {
    private static let key = "recentSearches"

    static func load() -> [String] { UserDefaults.standard.stringArray(forKey: key) ?? [] }

    static func save(_ list: [String]) { UserDefaults.standard.set(list, forKey: key) }

    static func add(_ q: String, to list: [String]) -> [String] {
        let next = Array(([q] + list.filter { $0 != q }).prefix(8))
        save(next)
        return next
    }
}
