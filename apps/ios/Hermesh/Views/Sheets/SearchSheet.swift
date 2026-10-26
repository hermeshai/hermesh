import HermeshSessions
import SwiftUI

/// Search, on a page of its own: the recent sessions until something is
/// typed, then the sessions that match. Cancelling the search closes it.
struct SearchSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""
    @State private var searching = false

    var body: some View {
        let q = query.trimmingCharacters(in: .whitespaces)
        let found = q.isEmpty
            ? model.sessions.filter { $0.status != .archived }.sorted { $0.updatedDate > $1.updatedDate }
            : searchSessions(model.sessions, query: q, filter: SessionFilter(), text: model.searchText)
        NavigationStack {
            List {
                Section {
                    ForEach(found, id: \.sessionID) { s in
                        Button {
                            model.sheet = nil
                            model.open(.session(s.sessionID))
                        } label: {
                            SessionRow(session: s)
                        }
                        .buttonStyle(.plain)
                        .listRowInsets(EdgeInsets())
                    }
                } header: {
                    q.isEmpty ? Text("Recent") : Text("Results")
                }
            }
            .listStyle(.plain)
            .overlay {
                if found.isEmpty && !q.isEmpty {
                    ContentUnavailableView("No sessions found", systemImage: "magnifyingglass")
                }
            }
            .navigationTitle("Search")
            .navigationBarTitleDisplayMode(.inline)
            .searchable(text: $query, isPresented: $searching, prompt: "Search sessions")
            .task { searching = true }
            .onChange(of: searching) { if !searching { dismiss() } }
            .toolbar { CloseButton() }
        }
    }
}
