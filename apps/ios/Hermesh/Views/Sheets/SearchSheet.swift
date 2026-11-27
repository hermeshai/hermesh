import HermeshSessions
import SwiftUI

/// Live task search aligned with the Mac search palette:
/// - Instant keystroke filtering without `.searchSuggestions` occlusion
/// - Single-row essential filter chips (Project, Agent, Status, adaptive Environment, Include archived)
/// - Unified `AgentLogo` + status capsule + muted Git marks + keyword match highlighting
/// - Long-press `SessionGitPreviewCard` (with excerpt + Git delivery status) and quick actions
struct SearchSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""
    @State private var filter = SessionFilter()
    @State private var selectedState: SearchState?
    @State private var searching = false
    @State private var recent: [String] = []
    @Environment(\.barsOnTheSide) private var sideBars

    private var hasActiveFilter: Bool { filter.any || selectedState != nil }

    var body: some View {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        NavigationStack {
            VStack(spacing: 0) {
                filterBar
                Divider()
                List {
                    if q.isEmpty && !hasActiveFilter {
                        initialContent
                    } else {
                        results(q)
                    }
                }
                .listStyle(.plain)
                .scrollDismissesKeyboard(.immediately)
                .accessibilityIdentifier("session-search")
            }
            .navigationTitle("Search")
            .searchable(text: $query, isPresented: $searching, placement: .navigationBarDrawer(displayMode: .always), prompt: "Search sessions")
            .onSubmit(of: .search) { remember(q) }
            .task {
                if !model.isDemo { recent = RecentSearches.load() }
                searching = true
                await model.refreshGitStatus()
            }
            .onChange(of: searching) { if !searching { dismiss() } }
            // On the side, the bar shows while searching: its Close would be a second one under the field's.
            .toolbar { if !sideBars { CloseButton() } }
        }
    }

    // MARK: Essential Filter Bar

    private var visibleSessionsForChoices: [Session] {
        filter.archived ? model.sessions : model.sessions.filter { $0.status != .archived }
    }

    private var projectChoices: [String] {
        var set = Set(visibleSessionsForChoices.map(\.projectID).filter { !$0.isEmpty })
        for p in model.projects where !p.projectID.isEmpty { set.insert(p.projectID) }
        if !filter.project.isEmpty { set.insert(filter.project) }
        return set.sorted { $0.localizedStandardCompare($1) == .orderedAscending }
    }

    private var agentChoices: [String] {
        var set = Set(visibleSessionsForChoices.map(\.agent).filter { !$0.isEmpty })
        if !filter.agent.isEmpty { set.insert(filter.agent) }
        return set.sorted { a, b in
            if (a == herAgent) != (b == herAgent) { return a == herAgent }
            return agentTitle(a).localizedStandardCompare(agentTitle(b)) == .orderedAscending
        }
    }

    private var showEnvironmentFilter: Bool {
        model.machines.count > 1 || !filter.machine.isEmpty
    }

    private var filterBar: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 7) {
                if !projectChoices.isEmpty {
                    projectChip
                }
                if !agentChoices.isEmpty {
                    agentChip
                }
                statusChip
                if showEnvironmentFilter {
                    environmentChip
                }
                archivedChip
                if hasActiveFilter {
                    Button {
                        resetFilters()
                    } label: {
                        Text("Reset")
                            .font(.subheadline.weight(.medium))
                            .foregroundStyle(.tint)
                            .padding(.horizontal, 6)
                            .frame(height: 30)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("search-filter-reset")
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 8)
        }
    }

    private var projectChip: some View {
        let active = !filter.project.isEmpty
        return Menu {
            Button {
                filter.project = ""
            } label: {
                if !active { Label("All", systemImage: "checkmark") } else { Text("All") }
            }
            Divider()
            ForEach(projectChoices, id: \.self) { proj in
                let count = visibleSessionsForChoices.filter { $0.projectID == proj }.count
                Button {
                    filter.project = filter.project == proj ? "" : proj
                } label: {
                    if filter.project == proj {
                        Label("\(proj) (\(count))", systemImage: "checkmark")
                    } else {
                        Text("\(proj) (\(count))")
                    }
                }
                .accessibilityIdentifier("search-project-\(proj)")
            }
        } label: {
            FilterChipLabel(
                title: active ? filter.project : String(localized: "Project"),
                active: active,
                hasChevron: true
            )
        }
        .accessibilityIdentifier("search-filter-project")
    }

    private var agentChip: some View {
        let active = !filter.agent.isEmpty
        return Menu {
            Button {
                filter.agent = ""
            } label: {
                if !active { Label("All", systemImage: "checkmark") } else { Text("All") }
            }
            Divider()
            ForEach(agentChoices, id: \.self) { agent in
                let count = visibleSessionsForChoices.filter { $0.agent == agent }.count
                Button {
                    filter.agent = filter.agent == agent ? "" : agent
                } label: {
                    if filter.agent == agent {
                        Label("\(agentTitle(agent)) (\(count))", systemImage: "checkmark")
                    } else {
                        Text("\(agentTitle(agent)) (\(count))")
                    }
                }
                .accessibilityIdentifier("search-agent-\(agent)")
            }
        } label: {
            FilterChipLabel(
                title: active ? agentShortTitle(filter.agent) : String(localized: "Agent"),
                active: active,
                hasChevron: true
            )
        }
        .accessibilityIdentifier("search-filter-agent")
    }

    private var statusChip: some View {
        let active = selectedState != nil
        return Menu {
            Button {
                selectedState = nil
            } label: {
                if !active { Label("All", systemImage: "checkmark") } else { Text("All") }
            }
            Divider()
            ForEach(SearchState.allCases) { t in
                let count = visibleSessionsForChoices.filter(t.matches).count
                Button {
                    selectedState = selectedState == t ? nil : t
                } label: {
                    Label("\(t.title) (\(count))", systemImage: selectedState == t ? "checkmark" : t.symbol)
                }
                .accessibilityIdentifier("search-state-\(t.rawValue)")
            }
        } label: {
            FilterChipLabel(
                title: selectedState?.title ?? String(localized: "Status"),
                active: active,
                hasChevron: true,
                dotColor: selectedState?.color
            )
        }
        .accessibilityIdentifier("search-filter-status")
    }

    private var environmentChip: some View {
        let active = !filter.machine.isEmpty
        return Menu {
            Button {
                filter.machine = ""
            } label: {
                if !active { Label("All", systemImage: "checkmark") } else { Text("All") }
            }
            Divider()
            ForEach(model.machines, id: \.machineID) { m in
                let name = m.machineName.isEmpty ? model.machineName(m.machineID) : m.machineName
                let count = visibleSessionsForChoices.filter { $0.machineID == m.machineID }.count
                Button {
                    filter.machine = filter.machine == m.machineID ? "" : m.machineID
                } label: {
                    if filter.machine == m.machineID {
                        Label("\(name) (\(count))", systemImage: "checkmark")
                    } else {
                        Text("\(name) (\(count))")
                    }
                }
                .accessibilityIdentifier("search-machine-\(m.machineID)")
            }
        } label: {
            FilterChipLabel(
                title: active ? model.machineName(filter.machine) : String(localized: "Environment"),
                active: active,
                hasChevron: true
            )
        }
        .accessibilityIdentifier("search-filter-machine")
    }

    private var archivedChip: some View {
        let archivedCount = model.sessions.filter { $0.status == .archived }.count
        let title = archivedCount > 0 && !filter.archived
            ? String(localized: "Include Archived (\(archivedCount))")
            : String(localized: "Include archived")
        return Button {
            filter.archived.toggle()
        } label: {
            FilterChipLabel(
                title: title,
                active: filter.archived,
                hasChevron: false,
                leadingSymbol: "archivebox"
            )
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("search-filter-archived")
    }

    private func resetFilters() {
        filter = SessionFilter()
        selectedState = nil
    }

    // MARK: Initial Content (Before Typing)

    @ViewBuilder private var initialContent: some View {
        if !recent.isEmpty {
            Section {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 8) {
                        ForEach(recent, id: \.self) { r in
                            Button {
                                query = r
                            } label: {
                                HStack(spacing: 5) {
                                    Image(systemName: "clock.arrow.circlepath")
                                        .font(.caption2.weight(.medium))
                                        .foregroundStyle(.secondary)
                                    Text(r)
                                        .font(.subheadline)
                                        .foregroundStyle(.primary)
                                        .lineLimit(1)
                                }
                                .padding(.horizontal, 11)
                                .padding(.vertical, 6)
                                .background(Color(uiColor: .tertiarySystemFill), in: .capsule)
                            }
                            .buttonStyle(.plain)
                            .contextMenu {
                                Button("Delete", role: .destructive) { forget(r) }
                            }
                        }
                    }
                    .padding(.horizontal, 16)
                    .padding(.vertical, 4)
                }
                .listRowInsets(EdgeInsets())
                .listRowSeparator(.hidden)
            } header: {
                HStack(alignment: .firstTextBaseline) {
                    sectionHeader("Recent Searches")
                    Spacer()
                    Button("Clear") { forget(nil) }
                        .font(.subheadline)
                        .textCase(nil)
                        .accessibilityIdentifier("clear-recent")
                }
            }
        }

        let latest = Array(searchSessions(model.sessions, query: "", filter: SessionFilter(), text: model.searchText).prefix(12))
        if !latest.isEmpty {
            Section {
                ForEach(latest, id: \.sessionID) { s in
                    sessionRow(s, query: "")
                }
            } header: {
                sectionHeader("Recent")
            }
        }
    }

    // MARK: Live Results

    @ViewBuilder private func results(_ q: String) -> some View {
        let found = searchSessions(model.sessions, query: q, filter: filter, text: model.searchText)
            .filter { s in selectedState?.matches(s) ?? true }
        let hiddenArchivedCount: Int = if filter.archived {
            0
        } else {
            searchSessions(
                model.sessions.filter { $0.status == .archived },
                query: q,
                filter: SessionFilter(grant: filter.grant, project: filter.project, agent: filter.agent, machine: filter.machine, archived: true),
                text: model.searchText
            )
            .filter { s in selectedState?.matches(s) ?? true }
            .count
        }

        if found.isEmpty {
            VStack(spacing: 12) {
                if q.isEmpty {
                    ContentUnavailableView(
                        "No Matching Tasks",
                        systemImage: "magnifyingglass",
                        description: Text("Try adjusting or clearing your filters.")
                    )
                } else {
                    ContentUnavailableView.search(text: q)
                }
                HStack(spacing: 10) {
                    if !filter.archived {
                        Button {
                            filter.archived = true
                        } label: {
                            if hiddenArchivedCount > 0 {
                                Label("Include Archived (\(hiddenArchivedCount))", systemImage: "archivebox")
                            } else {
                                Label("Include archived", systemImage: "archivebox")
                            }
                        }
                        .buttonStyle(.borderedProminent)
                        .buttonBorderShape(.capsule)
                        .controlSize(.small)
                        .accessibilityIdentifier("empty-include-archived")
                    }
                    if hasActiveFilter {
                        Button("Reset") {
                            resetFilters()
                        }
                        .buttonStyle(.bordered)
                        .buttonBorderShape(.capsule)
                        .controlSize(.small)
                        .accessibilityIdentifier("empty-reset-filters")
                    }
                }
            }
            .frame(maxWidth: .infinity)
            .padding(.bottom, 16)
            .listRowSeparator(.hidden)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("no-results")
        } else {
            Section {
                ForEach(found, id: \.sessionID) { s in
                    sessionRow(s, query: q)
                }
                if !filter.archived && hiddenArchivedCount > 0 {
                    Button {
                        filter.archived = true
                    } label: {
                        HStack(spacing: 8) {
                            Image(systemName: "archivebox")
                                .font(.footnote)
                                .foregroundStyle(.secondary)
                            Text("Show \(hiddenArchivedCount) archived matches")
                                .font(.footnote.weight(.medium))
                                .foregroundStyle(.tint)
                            Spacer()
                        }
                        .padding(.horizontal, 16)
                        .padding(.vertical, 10)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("show-archived-matches")
                }
            } header: {
                sectionHeader("\(found.count) results")
            }
        }
    }

    // MARK: Parts

    private func sectionHeader(_ title: LocalizedStringKey) -> some View {
        Text(title)
            .font(.footnote.weight(.semibold))
            .foregroundStyle(.secondary)
            .textCase(nil)
    }

    private func sessionRow(_ s: Session, query q: String) -> some View {
        SearchSessionRow(session: s, query: q) {
            remember(q)
            if s.status == .archived && !model.showArchived {
                model.showArchived = true
            }
            model.sheet = nil
            model.open(.session(s.sessionID))
        }
        .listRowInsets(EdgeInsets())
    }

    private func remember(_ q: String) {
        guard !q.isEmpty else { return }
        recent = RecentSearches.add(q, to: recent, persistent: !model.isDemo)
    }

    /// One recent search, or (nil) all of them.
    private func forget(_ q: String?) {
        recent = q.map { q in recent.filter { $0 != q } } ?? []
        if !model.isDemo { RecentSearches.save(recent) }
    }
}

private struct FilterChipLabel: View {
    @Environment(\.colorScheme) private var colorScheme
    let title: String
    let active: Bool
    let hasChevron: Bool
    var leadingSymbol: String?
    var dotColor: Color?

    var body: some View {
        let dark = colorScheme == .dark
        let fg: Color = active
            ? (dark ? Color(red: 0.56, green: 0.64, blue: 1.0) : Color(red: 0.18, green: 0.29, blue: 0.90))
            : .primary
        let bg: Color = active
            ? Color.accentColor.opacity(dark ? 0.20 : 0.12)
            : Color(uiColor: .tertiarySystemFill)
        let border: Color = active
            ? Color.accentColor.opacity(dark ? 0.34 : 0.24)
            : .clear

        HStack(spacing: 5) {
            if let dotColor {
                Circle()
                    .fill(dotColor)
                    .frame(width: 6.5, height: 6.5)
                    .accessibilityHidden(true)
            } else if let leadingSymbol {
                Image(systemName: leadingSymbol)
                    .font(.caption.weight(.medium))
                    .accessibilityHidden(true)
            }
            Text(title)
                .font(.footnote.weight(active ? .semibold : .medium))
                .lineLimit(1)
            if hasChevron {
                Image(systemName: "chevron.down")
                    .font(.system(size: 9.5, weight: .semibold))
                    .opacity(0.65)
                    .accessibilityHidden(true)
            }
        }
        .foregroundStyle(fg)
        .padding(.horizontal, 11)
        .frame(height: 30)
        .background(bg, in: .capsule)
        .overlay {
            Capsule().strokeBorder(border, lineWidth: 0.75)
        }
    }
}

private struct SearchSessionRow: View {
    @Environment(AppModel.self) private var model
    @Environment(\.colorScheme) private var colorScheme
    let session: Session
    let query: String
    let onOpen: () -> Void
    @State private var renaming = false
    @State private var deleting = false

    private var running: Bool {
        session.isActive && !session.needsAttention
    }

    private var subtitleText: String {
        let base = model.senderLine(session)
        guard model.machines.count > 1, !session.machineID.isEmpty else { return base }
        return "\(base) · \(model.machineName(session.machineID))"
    }

    var body: some View {
        let sender = model.senderLine(session)
        let now = model.now
        Button(action: onOpen) {
            rowBody
        }
        .buttonStyle(.plain)
        .contextMenu {
            Button("Open", systemImage: "arrow.up.right", action: onOpen)
            Divider()
            SessionActions(session: session, renaming: $renaming, deleting: $deleting)
        } preview: {
            SessionGitPreviewCard(session: session, senderLine: sender, now: now, showExcerpt: true)
                .environment(\.colorScheme, colorScheme)
                .preferredColorScheme(colorScheme)
        }
        .sessionDialogs(session, renaming: $renaming, deleting: $deleting)
    }

    private var rowBody: some View {
        let branch = session.effectiveBranch
        let branchMatched = !query.isEmpty && !branch.isEmpty && branch.range(of: query, options: [.caseInsensitive, .diacriticInsensitive]) != nil
        let titleMatched = !query.isEmpty && session.displayTitle.range(of: query, options: [.caseInsensitive, .diacriticInsensitive]) != nil
        let excerpt = session.searchExcerpt
        let excerptMatched = !query.isEmpty && (excerpt?.text.range(of: query, options: [.caseInsensitive, .diacriticInsensitive]) != nil)

        return HStack(alignment: .top, spacing: 12) {
            AgentLogo(id: session.agent, size: 22, running: running, paused: false)
                .frame(width: 26, height: 22)
                .padding(.top, 1)

            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    titleView
                        .lineLimit(1)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    statusBadge
                    gitBadge
                }

                HStack(spacing: 8) {
                    Text(highlighted(subtitleText, secondary: true))
                        .font(.footnote)
                        .lineLimit(1)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    timeView
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .monospacedDigit()
                }

                if branchMatched {
                    HStack(spacing: 5) {
                        GitBranchIcon()
                            .stroke(.tertiary, style: StrokeStyle(lineWidth: 1.25, lineCap: .round, lineJoin: .round))
                            .frame(width: 11.5, height: 11.5)
                            .accessibilityHidden(true)
                        Text(highlighted(branch, secondary: true))
                            .font(.system(size: 11.5, design: .monospaced))
                            .lineLimit(1)
                            .truncationMode(.middle)
                    }
                    .padding(.top, 1)
                } else if let excerpt, (excerptMatched && !titleMatched) || (query.isEmpty && session.needsAttention) {
                    Text(highlighted(excerpt.text, secondary: true))
                        .font(.caption)
                        .lineLimit(1)
                        .padding(.top, 1)
                }
            }
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 10)
        .frame(minHeight: 52)
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("session-\(session.displayTitle)")
    }

    @ViewBuilder private var titleView: some View {
        let baseColor: Color = session.status == .archived ? .secondary : .primary
        if query.isEmpty && (running || session.titleGenerating) {
            Text(session.displayTitle)
                .font(.body)
                .foregroundStyle(baseColor)
                .shimmer(true, paused: false)
        } else {
            Text(highlighted(session.displayTitle, secondary: session.status == .archived))
                .font(.body)
        }
    }

    @ViewBuilder private var statusBadge: some View {
        let dark = colorScheme == .dark
        if session.status == .waitingInput {
            badgePill(String(localized: "Needs You"), fg: .orange, bg: Color.orange.opacity(dark ? 0.16 : 0.12))
        } else if session.isFailed {
            let label = session.status == .crashed ? statusTitle(.crashed) : String(localized: "Failed")
            badgePill(label, fg: .red, bg: Color.red.opacity(dark ? 0.16 : 0.12))
        } else if running {
            let green = dark ? Color(red: 0.25, green: 0.81, blue: 0.53) : Color(red: 0.09, green: 0.57, blue: 0.31)
            badgePill(String(localized: "In Progress"), fg: green, bg: green.opacity(dark ? 0.16 : 0.12))
        } else if session.status == .archived {
            badgePill(statusTitle(.archived), fg: .secondary, bg: Color(uiColor: .tertiarySystemFill))
        }
    }

    private func badgePill(_ text: String, fg: Color, bg: Color) -> some View {
        Text(text)
            .font(.system(size: 11, weight: .semibold))
            .foregroundStyle(fg)
            .lineLimit(1)
            .padding(.horizontal, 6.5)
            .padding(.vertical, 2)
            .background(bg, in: .capsule)
    }

    @ViewBuilder private var gitBadge: some View {
        if let gitMark = session.gitMark {
            let color = gitMark == .pushed ? GitPalette.pushed(colorScheme) : GitPalette.merged(colorScheme)
            Group {
                if gitMark == .pushed {
                    GitPushedIcon().stroke(color, style: StrokeStyle(lineWidth: 1.35, lineCap: .round, lineJoin: .round))
                } else {
                    GitMergedIcon().stroke(color, style: StrokeStyle(lineWidth: 1.35, lineCap: .round, lineJoin: .round))
                }
            }
            .frame(width: 14.5, height: 14.5)
            .opacity(session.status == .archived ? 0.55 : 0.80)
            .accessibilityHidden(true)
        }
    }

    @ViewBuilder private var timeView: some View {
        if session.status == .running, let start = session.turnStartedDate {
            Text(start, style: .timer)
        } else {
            Text(listTime(session.updatedDate, now: model.now))
        }
    }

    private func highlighted(_ text: String, secondary: Bool) -> AttributedString {
        let dark = colorScheme == .dark
        let baseColor: Color = secondary ? .secondary : .primary
        let markFg: Color = dark
            ? Color(red: 0.77, green: 0.82, blue: 1.0)
            : Color(red: 0.14, green: 0.25, blue: 0.83)
        let markBg: Color = Color.accentColor.opacity(dark ? 0.28 : 0.15)
        var out = AttributedString()
        for seg in highlightSegments(text, query: query) {
            var part = AttributedString(seg.text)
            if seg.highlighted {
                part.foregroundColor = markFg
                part.backgroundColor = markBg
                part.inlinePresentationIntent = .stronglyEmphasized
            } else {
                part.foregroundColor = baseColor
            }
            out.append(part)
        }
        return out
    }
}

/// A state filter choice for task search.
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

    static func add(_ q: String, to list: [String], persistent: Bool = true) -> [String] {
        let next = Array(([q] + list.filter { $0 != q }).prefix(8))
        if persistent { save(next) }
        return next
    }
}
