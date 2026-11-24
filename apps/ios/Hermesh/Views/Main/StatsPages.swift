import HermeshProto
import HermeshSessions
import SwiftUI

// MARK: Usage

/// How the page is looked at, in one row (the range, what the bars are in parts of, and the button the filters are
/// behind; under it the filters in force), then a card a measure (sessions, tokens) with its number, its change
/// against the span before, its parts, its bars and its figures, then 18 weeks of tokens.
struct UsagePage: View {
    @Environment(AppModel.self) private var model
    /// The part seen alone in the charts, the bar picked in them, and whether Other is opened to the parts in it.
    @State private var solo: String?
    @State private var picked: Int?
    @State private var opened = false

    var body: some View {
        let stats = model.stats
        let period = stats.period
        let res = stats.page(model.now)
        let page = usagePage(res, period: period, now: model.now)
        let choices = usageChoices(stats.answer(stats.menusRequest(model.now)), filter: stats.filter)
        let weeks = stats.answer(stats.weeksRequest(model.now))
        // The last answer shows while a new state's is asked: one of another grouping has no parts to draw.
        let split = usageParts(res, period: period, group: stats.group, filter: stats.filter, agents: codingAgentIDs, machines: model.machines.map(\.machineID))
            .flatMap { $0.all.isEmpty ? nil : $0 }
        let byProject = !stats.filter.machineID.isEmpty || split?.by == .project
        VStack(alignment: .leading, spacing: 10) {
            controls(choices, byProject: byProject)
            heading(Text("Sessions"), delta: page.sessionsDelta)
            MeasureCard(id: "usage-sessions", big: Text("\(page.sessions)"), unit: Text("sessions worked"),
                        values: page.buckets.map { Double($0.sessions) }, now: page.now, period: period, loading: res == nil,
                        facts: [(Text("Done"), Text("\(page.done)")), (Text("Failed"), Text("\(page.failedTurns) turns")), (Text("Time at work"), Text(durationText(page.busySeconds)))],
                        parts: measured(split) { Double($0.sessions) }, whole: Double(page.sessions), day: model.now,
                        text: { "\(Int($0))" }, said: { String(localized: "\(Int($0)) sessions") }, picked: $picked, opened: $opened, onSolo: alone)
            heading(Text(verbatim: "Token"), delta: page.tokensDelta)
            MeasureCard(id: "usage-tokens", big: Text(verbatim: tokensText(page.tokens)), unit: Text(verbatim: "token"),
                        values: page.buckets.map { Double($0.tokens) }, now: page.now, period: period, loading: res == nil,
                        facts: [(Text("Input"), Text(verbatim: tokensText(page.inputTokens))), (Text("Output"), Text(verbatim: tokensText(page.outputTokens)))],
                        parts: measured(split) { Double($0.tokens) }, whole: Double(page.tokens), day: model.now,
                        text: { tokensText(UInt64($0)) }, said: { "\(tokensText(UInt64($0))) token" }, picked: $picked, opened: $opened, onSolo: alone)
            WeeksCard(usage: weeks.map { usageYear($0.turns, today: model.now, weeks: 18) })
            Text(foot(res)).font(.caption).foregroundStyle(.secondary).padding(.horizontal, 4)
                .accessibilityIdentifier("usage-foot")
        }
        // What was picked in one state says nothing of another.
        .onChange(of: stats.group) { solo = nil; picked = nil; opened = false }
        .onChange(of: stats.period) { picked = nil }
        .onChange(of: stats.filter) { picked = nil }
    }

    /// A part tapped in a legend is seen alone in both charts; tapped again, all of them.
    private func alone(_ id: String) {
        withAnimation(.smooth) { solo = solo == id ? nil : id }
    }

    /// The parts as a measure's card draws them: named, coloured, and by the measure's own number.
    private func measured(_ split: UsageParts?, _ value: (UsagePart.Bucket) -> Double) -> MeasureParts? {
        guard let split else { return nil }
        func row(_ p: UsagePart) -> MeasureParts.Row {
            let other = p.id == UsagePart.other
            let name = other ? String(localized: "Other") : split.by == .agent ? agentTitle(p.id) : split.by == .machine ? model.machineName(p.id) : p.id
            // One of those among Other, seen alone, is drawn in the accent: grey would say nothing of it.
            return MeasureParts.Row(id: p.id, name: name, short: split.by == .agent && !other ? agentShortTitle(p.id) : name,
                                    color: p.color >= 0 ? partColor(p.color) : p.id == solo && !other ? .accentColor : partGrey,
                                    value: value(.init(sessions: p.sessions, tokens: p.tokens)), buckets: p.buckets.map(value))
        }
        let all = split.all.map(row), drawn = split.drawn.map(row)
        return MeasureParts(all: all, drawn: drawn, folded: split.folded, solo: (all + drawn).contains { $0.id == solo } ? solo : nil)
    }

    /// A measure's heading: its name, and its change against the span before.
    private func heading(_ title: Text, delta: Int?) -> some View {
        OverviewHeading(title: title) {
            if let delta {
                Text(verbatim: "\(deltaPrefix) \(delta >= 0 ? "↑" : "↓") \(abs(delta))%").monospacedDigit()
            }
        }
        .padding(.top, 6)
    }

    private var deltaPrefix: String {
        switch model.stats.period {
        case .today: String(localized: "Than yesterday at this time")
        case .week: String(localized: "Than the 7 days before")
        case .month: String(localized: "Than the 30 days before")
        }
    }

    /// How the page is looked at, in one row: the range, what the bars are in parts of (its three ways at once,
    /// each saying "by" what), and the one button the filters are behind. A filter in force is said under the row
    /// and let go by its cross, so the row is there only when something is filtered.
    private func controls(_ c: UsageChoices, byProject: Bool) -> some View {
        @Bindable var stats = model.stats
        let f = stats.filter
        let place = f.machineID.isEmpty ? "" : model.machineName(f.machineID) + (f.projectID.isEmpty ? "" : "/" + f.projectID)
        let inForce: [(id: String, name: String, clear: () -> Void)] = [
            f.agent.isEmpty ? nil : ("agent", agentTitle(f.agent), { stats.filter.agent = "" }),
            f.machineID.isEmpty ? nil : ("place", place, { stats.filter.machineID = ""; stats.filter.projectID = "" }),
            f.client.isEmpty ? nil : ("client", model.senderName(f.client), { stats.filter.client = "" }),
        ].compactMap { $0 }
        return VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Menu {
                    ForEach(WorkPeriod.allCases, id: \.self) { p in
                        choice(Text(periodTitle(p)), counts: nil, on: stats.period == p) { stats.period = p }
                    }
                } label: {
                    chip(Text(periodTitle(stats.period)), on: false)
                }
                .accessibilityIdentifier("filter-range")
                Picker("Group", selection: $stats.group.animation(.smooth)) {
                    // Their own words (short in English, so the three fit the row), not the session list's.
                    Text("Agents · bars").tag(UsageGroup.agent)
                    // By where is by project once one environment is all there is to split.
                    (byProject ? Text("Projects · bars") : Text("Machines · bars")).tag(UsageGroup.place)
                    Text("Total").tag(UsageGroup.total)
                }
                .pickerStyle(.segmented)
                .accessibilityIdentifier("usage-group")
                Menu {
                    filterMenus(c)
                } label: {
                    HStack(spacing: 4) {
                        Image(systemName: "line.3.horizontal.decrease")
                        if !inForce.isEmpty { Text(verbatim: "\(inForce.count)").monospacedDigit() }
                    }
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(inForce.isEmpty ? Color.primary : .accentColor)
                    .padding(.horizontal, 9).frame(minWidth: 32, minHeight: 32)
                    .background(inForce.isEmpty ? Color(.secondarySystemGroupedBackground) : Color.accentColor.opacity(0.14), in: .capsule)
                    .contentShape(.capsule)
                }
                .menuOrder(.fixed)
                .accessibilityLabel(Text("Filters"))
                .accessibilityValue(Text(verbatim: listText(inForce.map(\.name))))
                .accessibilityIdentifier("filters")
            }
            if !inForce.isEmpty {
                ScrollView(.horizontal) {
                    HStack(spacing: 6) {
                        ForEach(inForce, id: \.id) { x in
                            Button { withAnimation(.smooth) { x.clear() } } label: {
                                HStack(spacing: 6) {
                                    Text(verbatim: x.name).lineLimit(1)
                                    Image(systemName: "xmark").font(.caption2.weight(.bold))
                                }
                                .foregroundStyle(Color.accentColor)
                                .padding(.horizontal, 10).frame(height: 32)
                                .background(Color.accentColor.opacity(0.14), in: .capsule)
                                .contentShape(.capsule)
                            }
                            .buttonStyle(.plain)
                            .accessibilityLabel(Text("Remove \(x.name)"))
                            .accessibilityIdentifier("filter-in-force-\(x.id)")
                        }
                    }
                    .padding(.horizontal, 16)
                }
                .scrollIndicators(.hidden)
                .padding(.horizontal, -16)
            }
        }
        .font(.subheadline)
    }

    /// The three filters, a menu each inside the one button's: the coding agent, where and the sender; each says
    /// under its name what is chosen.
    @ViewBuilder private func filterMenus(_ c: UsageChoices) -> some View {
        let stats = model.stats
        let f = stats.filter
        Menu {
            choice(Text("All"), counts: nil, on: f.agent.isEmpty) { stats.filter.agent = "" }
            Section {
                ForEach(c.agents) { a in choice(Text(verbatim: agentTitle(a.id)), counts: a, on: a.id == f.agent) { stats.filter.agent = a.id } }
            }
        } label: {
            Label { Text("Agent Provider"); Text(verbatim: f.agent.isEmpty ? String(localized: "All") : agentTitle(f.agent)) } icon: { check(!f.agent.isEmpty) }
        }
        .accessibilityIdentifier("filter-agent")
        Menu {
            choice(Text("All"), counts: nil, on: f.machineID.isEmpty) { stats.filter.machineID = ""; stats.filter.projectID = "" }
            Section {
                ForEach(c.machines) { m in
                    Menu {
                        choice(Text("All Projects"), counts: m, on: f.machineID == m.id && f.projectID.isEmpty) { stats.filter.machineID = m.id; stats.filter.projectID = "" }
                        Section {
                            ForEach(m.projects) { p in
                                choice(Text(verbatim: p.id), counts: p, on: f.machineID == m.id && f.projectID == p.id) { stats.filter.machineID = m.id; stats.filter.projectID = p.id }
                            }
                        }
                    } label: {
                        Label { Text(verbatim: model.machineName(m.id)); Text(counts(m)) } icon: { check(f.machineID == m.id) }
                    }
                }
            }
        } label: {
            Label {
                Text("Environment / Project")
                Text(verbatim: f.machineID.isEmpty ? String(localized: "All") : model.machineName(f.machineID) + (f.projectID.isEmpty ? "" : "/" + f.projectID))
            } icon: { check(!f.machineID.isEmpty) }
        }
        .accessibilityIdentifier("filter-place")
        Menu {
            choice(Text("All"), counts: nil, on: f.client.isEmpty) { stats.filter.client = "" }
            Section {
                ForEach(c.clients) { s in choice(Text(verbatim: model.senderName(s.id)), counts: s, on: s.id == f.client) { stats.filter.client = s.id } }
            }
        } label: {
            Label { Text("Sender"); Text(verbatim: f.client.isEmpty ? String(localized: "All") : model.senderName(f.client)) } icon: { check(!f.client.isEmpty) }
        }
        .accessibilityIdentifier("filter-client")
    }

    /// A line of a filter's menu: its check, its name and, under it, its work in the range.
    private func choice(_ title: Text, counts c: UsageChoice?, on: Bool, _ pick: @escaping () -> Void) -> some View {
        Button { withAnimation(.smooth) { pick() } } label: {
            if let c {
                Label { title; Text(counts(c)) } icon: { check(on) }
            } else {
                Label { title } icon: { check(on) }
            }
        }
    }

    @ViewBuilder private func check(_ on: Bool) -> some View {
        if on { Image(systemName: "checkmark") }
    }

    private func counts(_ c: UsageChoice) -> String {
        c.sessions == 0 && c.tokens == 0 ? String(localized: "No sessions") : String(localized: "\(c.sessions) sessions · \(tokensText(c.tokens)) token")
    }

    private func chip(_ title: Text, on: Bool) -> some View {
        HStack(spacing: 3) {
            title.lineLimit(1)
            Image(systemName: "chevron.down").font(.caption2.weight(.semibold))
        }
        .foregroundStyle(on ? Color.accentColor : .primary)
        .padding(.horizontal, 10).frame(height: 32)
        .background(on ? Color.accentColor.opacity(0.14) : Color(.secondarySystemGroupedBackground), in: .capsule)
        .contentShape(.capsule)
        .fixedSize()
    }

    private func periodTitle(_ p: WorkPeriod) -> LocalizedStringKey {
        switch p {
        case .today: "Today"
        case .week: "7 Days"
        case .month: "30 Days"
        }
    }

    /// Under the cards: that input tokens include caches, and machines counted only up to their last sync.
    private func foot(_ res: Hermesh_Local_V1_QueryWorkResponse?) -> String {
        var out = String(localized: "Input includes each provider’s cache reads and writes.")
        let sources = res?.sources ?? []
        let syncing = sources.filter(\.syncing).map { model.machineName($0.machineID) }
        let behind = sources.filter { !$0.live && !$0.syncing && $0.hasSyncedAt }
        if !syncing.isEmpty {
            out += " " + String(localized: "Syncing \(listText(syncing))’s work…")
        } else if let oldest = behind.map(\.syncedAt.date).min() {
            out += " " + String(localized: "\(listText(behind.map { model.machineName($0.machineID) })) counted up to \(oldest.formatted(date: .abbreviated, time: .shortened))")
        }
        return out
    }
}

/// "20 hr 8 min" of work.
func durationText(_ seconds: UInt64) -> String {
    let m = Int((Double(seconds) / 60).rounded())
    if m < 1 { return String(localized: "Under 1 min") }
    if m < 60 { return String(localized: "\(m) min") }
    return m % 60 == 0 ? String(localized: "\(m / 60) hr") : String(localized: "\(m / 60) hr \(m % 60) min")
}

/// The colours the parts are told apart by: the Mac's six (overview.css --us-s1…), in light and in dark.
private let partColors: [(light: UInt32, dark: UInt32)] = [(0x2A78D6, 0x3987E5), (0xEB6834, 0xD95926), (0x1BAF7A, 0x199E70), (0xEDA100, 0xC98500), (0xE87BA4, 0xD55181), (0x008300, 0x0CA30C)]

private func dynamic(_ light: UInt32, _ dark: UInt32) -> Color {
    Color(UIColor { t in
        let v = t.userInterfaceStyle == .dark ? dark : light
        return UIColor(red: CGFloat(v >> 16 & 0xFF) / 255, green: CGFloat(v >> 8 & 0xFF) / 255, blue: CGFloat(v & 0xFF) / 255, alpha: 1)
    })
}

private func partColor(_ i: Int) -> Color { dynamic(partColors[i % partColors.count].light, partColors[i % partColors.count].dark) }
/// Other's, and that of each part among it.
private let partGrey = dynamic(0xA1A1A8, 0x6E6E75)

/// A measure's parts as its card draws them: every one (the busiest first), those the bars are coloured by, how
/// many are added up as Other, and the one seen alone.
struct MeasureParts {
    struct Row: Identifiable {
        let id: String
        let name: String
        /// For the legend's narrow cells.
        let short: String
        let color: Color
        let value: Double
        let buckets: [Double]
    }

    var all: [Row]
    var drawn: [Row]
    var folded: Int
    var solo: String?

    var alone: Row? { solo.flatMap { id in (all + drawn).first { $0.id == id } } }
}

/// A measure: its number and unit, its parts when it is in parts, its bars an hour or a day, the axis under them,
/// and its figures. Whole, a bar is ink grey (now in the accent, to come faint); in parts, its parts one on
/// another, the busiest at its foot, each in its colour. The parts are named over the bars in two columns, each
/// with its number and share: tapped, a part is seen alone; Other opens in place to the parts in it. A bar tapped
/// (or, held, the one under the finger) says over itself what it stands for.
struct MeasureCard: View {
    let id: String
    let big: Text
    let unit: Text
    let values: [Double]
    let now: Int
    let period: WorkPeriod
    let loading: Bool
    let facts: [(Text, Text)]
    var parts: MeasureParts?
    /// The measure over the range, for each part's share; today, for a picked bar's day.
    var whole: Double = 0
    var day = Date()
    /// A part's number in its cell ("23", "19.4M"), and a bar's said whole ("10 sessions").
    var text: (Double) -> String = { "\(Int($0))" }
    var said: (Double) -> String = { "\(Int($0))" }
    @Binding var picked: Int?
    @Binding var opened: Bool
    var onSolo: (String) -> Void = { _ in }
    @Environment(\.dynamicTypeSize) private var typeSize
    @Environment(\.calendar) private var calendar

    private let height: CGFloat = 72

    var body: some View {
        let top = max(values.max() ?? 0, 1)
        VStack(alignment: .leading, spacing: 14) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                big.font(.system(size: 48, weight: .bold)).monospacedDigit().contentTransition(.numericText())
                unit.foregroundStyle(.secondary)
            }
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("\(id)-big")
            .redacted(reason: loading ? .placeholder : [])
            if let parts, !loading { legend(parts).padding(.top, -4) }
            GeometryReader { g in
                let width = g.size.width
                HStack(alignment: .bottom, spacing: period == .month ? 2 : period == .week ? 8 : 3) {
                    ForEach(values.indices, id: \.self) { i in
                        bar(i, top: top).frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottom)
                            .opacity(picked != nil && picked != i ? 0.35 : 1)
                    }
                }
                .contentShape(Rectangle())
                .accessibilityHidden(true)
                // A tap picks a bar, and again lets it go; held, the finger moves along them.
                .gesture(SpatialTapGesture().onEnded { e in
                    let i = index(e.location.x, in: width)
                    withAnimation(.snappy(duration: 0.15)) { picked = picked == i ? nil : i }
                })
                .simultaneousGesture(LongPressGesture(minimumDuration: 0.15).sequenced(before: DragGesture(minimumDistance: 0)).onChanged { v in
                    if case .second(true, let drag?) = v { picked = index(drag.location.x, in: width) }
                })
                .overlay(alignment: .topLeading) {
                    if let i = picked, i <= now, i < values.count, !loading {
                        let x = width * (CGFloat(i) + 0.5) / CGFloat(values.count)
                        callout(i)
                            .fixedSize()
                            .alignmentGuide(.leading) { d in -min(max(x - d.width / 2, 0), max(width - d.width, 0)) }
                            .alignmentGuide(.top) { d in d.height + 8 }
                            .allowsHitTesting(false)
                            .transition(.opacity)
                            .accessibilityElement(children: .combine)
                            .accessibilityIdentifier("\(id)-callout")
                    }
                }
            }
            .frame(height: height)
            .zIndex(1)
            if period.hourly { HourAxis(hour: now) } else { DayAxis(period: period) }
            Divider()
            HStack(alignment: .top) {
                ForEach(facts.indices, id: \.self) { i in
                    VStack(alignment: .leading, spacing: 2) {
                        facts[i].0.font(.caption).foregroundStyle(.secondary)
                        facts[i].1.font(.callout.weight(.semibold)).monospacedDigit()
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .accessibilityElement(children: .combine)
                }
            }
            .redacted(reason: loading ? .placeholder : [])
        }
        .padding(.horizontal, 18).padding(.top, 16).padding(.bottom, 14)
        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier(id)
    }

    /// The bar a point across the chart is over, of those that have come.
    private func index(_ x: CGFloat, in width: CGFloat) -> Int {
        min(max(Int(x / max(width, 1) * CGFloat(values.count)), 0), max(min(now, values.count - 1), 0))
    }

    /// A bucket's bar: to come, a faint stub; whole, ink grey (now in the accent); in parts, its parts one on
    /// another with a hairline between, or the one part seen alone.
    @ViewBuilder private func bar(_ i: Int, top: Double) -> some View {
        let radius: CGFloat = period == .week ? 5 : 3
        if loading || i > now {
            RoundedRectangle(cornerRadius: radius, style: .continuous).fill(loading ? AnyShapeStyle(Skeleton.grey) : AnyShapeStyle(.quaternary)).frame(height: 3)
        } else if let parts {
            let shown = (parts.alone.map { [$0] } ?? parts.drawn).filter { $0.buckets.indices.contains(i) && $0.buckets[i] > 0 }
            let v = shown.reduce(0) { $0 + $1.buckets[i] }
            if v > 0 {
                let h = max(3, CGFloat(v / top) * height), gap: CGFloat = 1.5
                let room = max(h - gap * CGFloat(shown.count - 1), 1)
                VStack(spacing: gap) {
                    ForEach(shown.reversed()) { p in
                        Rectangle().fill(p.color).frame(height: room * CGFloat(p.buckets[i] / v))
                    }
                }
                .frame(height: h)
                .clipShape(RoundedRectangle(cornerRadius: radius, style: .continuous))
            } else {
                RoundedRectangle(cornerRadius: radius, style: .continuous).fill(.quaternary).frame(height: 3)
            }
        } else {
            RoundedRectangle(cornerRadius: radius, style: .continuous)
                .fill(i == now ? AnyShapeStyle(Color.accentColor) : AnyShapeStyle(Color.primary.opacity(0.32)))
                .frame(height: max(3, CGFloat(values[i] / top) * height))
        }
    }

    /// The parts in two columns (one at the largest type): each its colour, short name, number and share. Many:
    /// the busiest and Other, which opens in place to every part; those in it are grey.
    private func legend(_ parts: MeasureParts) -> some View {
        let other = parts.drawn.first { $0.id == UsagePart.other }
        let cells = other != nil && opened ? parts.all : parts.drawn.filter { $0.id != UsagePart.other }
        return LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 6), count: typeSize.isAccessibilitySize ? 1 : 2), spacing: 2) {
            ForEach(cells) { p in
                Button { onSolo(p.id) } label: { cell(p, name: Text(verbatim: p.short), on: parts.solo == p.id) }
                    .buttonStyle(.plain)
                    .opacity(parts.solo != nil && parts.solo != p.id ? 0.4 : 1)
                    .accessibilityLabel(Text(verbatim: "\(p.name), \(said(p.value)), \(share(p.value))"))
                    .accessibilityAddTraits(parts.solo == p.id ? .isSelected : [])
                    .accessibilityIdentifier("\(id)-part-\(p.id)")
            }
            if let other {
                Button { withAnimation(.smooth) { opened.toggle() } } label: {
                    if opened {
                        HStack(spacing: 4) { Text("Show Less"); Image(systemName: "chevron.up").font(.caption2.weight(.semibold)) }
                            .foregroundStyle(Color.accentColor).frame(maxWidth: .infinity, minHeight: 34)
                    } else {
                        cell(other, name: Text("\(parts.folded) others"), on: false, more: true)
                    }
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("\(id)-others")
            }
        }
        .font(.subheadline)
        .padding(.horizontal, -8)
    }

    private func cell(_ p: MeasureParts.Row, name: Text, on: Bool, more: Bool = false) -> some View {
        HStack(spacing: 6) {
            RoundedRectangle(cornerRadius: 2.5, style: .continuous).fill(p.color).frame(width: 9, height: 9)
            name.lineLimit(1)
            Spacer(minLength: 2)
            Text(verbatim: text(p.value)).fontWeight(.semibold).monospacedDigit()
            Text(verbatim: share(p.value)).font(.caption).foregroundStyle(.secondary).monospacedDigit().frame(minWidth: 30, alignment: .trailing)
            if more { Image(systemName: "chevron.down").font(.caption2.weight(.semibold)).foregroundStyle(.tertiary) }
        }
        .padding(.horizontal, 8).frame(minHeight: 34)
        .background(on ? Color(.tertiarySystemFill) : .clear, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        .contentShape(Rectangle())
    }

    private func share(_ value: Double) -> String { whole > 0 ? "\(Int((value / whole * 100).rounded()))%" : "" }

    /// What a picked bar stands for: its whole, when, and each part that has any; with one part seen alone, that part's.
    private func callout(_ i: Int) -> some View {
        let alone = parts?.alone
        let rows = alone == nil ? (parts?.drawn ?? []).filter { $0.buckets.indices.contains(i) && $0.buckets[i] > 0 } : []
        return VStack(alignment: .leading, spacing: 0) {
            Text(verbatim: said(alone.map { $0.buckets[i] } ?? values[i])).font(.subheadline.weight(.semibold)).monospacedDigit()
            Text(verbatim: (alone.map { $0.name + " · " } ?? "") + when(i)).font(.caption).foregroundStyle(.secondary).monospacedDigit()
            if !rows.isEmpty {
                Divider().padding(.vertical, 6)
                Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 2) {
                    ForEach(rows) { p in
                        GridRow {
                            HStack(spacing: 6) {
                                RoundedRectangle(cornerRadius: 2.5, style: .continuous).fill(p.color).frame(width: 8, height: 8)
                                Text(verbatim: p.name)
                            }
                            Text(verbatim: text(p.buckets[i])).fontWeight(.semibold).monospacedDigit().gridColumnAlignment(.trailing)
                        }
                    }
                }
                .font(.footnote)
            }
        }
        .padding(.horizontal, 11).padding(.vertical, 8)
        .background(dynamic(0xFFFFFF, 0x2C2C2E), in: RoundedRectangle(cornerRadius: 13, style: .continuous))
        .shadow(color: .black.opacity(0.2), radius: 12, y: 4)
    }

    /// The hour or the day of a bar: "14:00–15:00", "Nov 23, Mon".
    private func when(_ i: Int) -> String {
        if period.hourly { return String(format: "%d:00–%d:00", i, i + 1) }
        let first = period.firstDay(day, calendar: calendar)
        return (calendar.date(byAdding: .day, value: i, to: first) ?? first).formatted(.dateTime.month(.abbreviated).day().weekday(.abbreviated))
    }
}

/// 18 weeks of tokens by day, under the filters, with the active days and the streak.
struct WeeksCard: View {
    let usage: UsageYear?

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            OverviewHeading(title: Text("Last 18 Weeks")) {
                if let u = usage { Text("\(u.active) active days · \(u.streak)-day streak").monospacedDigit() }
            }
            .padding(.top, 6)
            Grid(horizontalSpacing: 3, verticalSpacing: 3) {
                ForEach(0 ..< 7, id: \.self) { row in
                    GridRow {
                        ForEach(0 ..< (usage?.weeks ?? 18), id: \.self) { col in
                            let cell = usage?.cells[col * 7 + row]
                            RoundedRectangle(cornerRadius: 2.5, style: .continuous)
                                .fill(usage == nil ? AnyShapeStyle(Skeleton.grey) : cell.map { AnyShapeStyle(Color.accentColor.opacity([0.1, 0.3, 0.5, 0.75, 1][$0.level])) } ?? AnyShapeStyle(.clear))
                                .aspectRatio(1, contentMode: .fit)
                        }
                    }
                }
            }
            .padding(14)
            .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
            .accessibilityElement()
            .accessibilityLabel(Text("Usage over the last \(usage?.weeks ?? 18) weeks"))
            .accessibilityIdentifier("usage-weeks")
        }
    }
}

// MARK: Limits

/// What each coding agent last said of its account's rate limits (§7.4): a card an agent, the one with the least
/// left first; a row a window, what is left of it and when it resets.
struct LimitsPage: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let cards = agentLimits(model.machines.map { (machineID: $0.machineID, agents: $0.status.agents) }, now: model.now)
        VStack(alignment: .leading, spacing: 10) {
            ForEach(cards) { c in LimitsCard(card: c) }
            Text("Each provider reports its limits as it runs; Hermesh only passes them on and doesn’t read your accounts. For a provider that hasn’t run in a while, these are the numbers from its last run.")
                .font(.caption).foregroundStyle(.secondary).padding(.horizontal, 4)
        }
    }
}

struct LimitsCard: View {
    @Environment(AppModel.self) private var model
    let card: AgentLimitsCard

    /// The agents that report their limits (the others never will).
    static let reporting: Set<String> = ["claude", "codex"]

    var body: some View {
        let c = card
        let low = (c.least?.left ?? 101) <= limitLow
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 10) {
                AgentLogo(id: c.agent, size: 30)
                VStack(alignment: .leading, spacing: 1) {
                    Text(verbatim: agentTitle(c.agent)).font(.headline)
                    if c.observedAt != nil { Text(verbatim: reportedText(model, c)).font(.caption).foregroundStyle(.secondary).lineLimit(1) }
                }
                Spacer(minLength: 4)
                if low {
                    Label("Running Low", systemImage: "exclamationmark.triangle.fill").font(.caption.weight(.semibold)).foregroundStyle(.orange)
                        .padding(.horizontal, 8).frame(height: 24).background(Color.orange.opacity(0.15), in: .capsule)
                } else if !c.plan.isEmpty {
                    Text(verbatim: c.plan.prefix(1).uppercased() + c.plan.dropFirst()).font(.caption.weight(.medium)).foregroundStyle(.secondary)
                        .padding(.horizontal, 8).frame(height: 24).background(Color(.tertiarySystemFill), in: .capsule)
                }
            }
            if c.windows.isEmpty {
                Text(c.stale ? "Its windows have reset; updates after it next runs" : Self.reporting.contains(c.agent) ? "Not reported yet; shows after it runs a session" : "It doesn’t report its limits")
                    .font(.subheadline).foregroundStyle(.secondary)
            }
            ForEach(c.windows, id: \.minutes) { w in
                VStack(alignment: .leading, spacing: 6) {
                    HStack(alignment: .firstTextBaseline) {
                        Text(verbatim: windowName(w.minutes)).font(.subheadline.weight(.medium))
                        Spacer()
                        Text("Left").font(.footnote).foregroundStyle(.secondary)
                        Text(verbatim: "\(w.left)%").font(.title3.weight(.bold)).monospacedDigit()
                    }
                    GeometryReader { g in
                        ZStack(alignment: .leading) {
                            Capsule().fill(Color(.tertiarySystemFill))
                            Capsule().fill(w.left <= limitLow ? Color.orange : Color.primary.opacity(0.4)).frame(width: g.size.width * CGFloat(w.left) / 100)
                        }
                    }
                    .frame(height: 6)
                    Text(verbatim: resetText(w, now: model.now)).font(.caption).foregroundStyle(.secondary).monospacedDigit()
                }
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("limit-\(c.agent)-\(w.minutes)")
            }
        }
        .padding(16)
        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("limits-\(c.agent)")
    }
}

/// "5-Hour Window", "Weekly Window"; short: "5-hour", "weekly".
func windowName(_ minutes: Int, short: Bool = false) -> String {
    switch (minutes, short) {
    case (300, false): String(localized: "5-Hour Window")
    case (300, true): String(localized: "5-hour")
    case (10080, false): String(localized: "Weekly Window")
    case (10080, true): String(localized: "weekly")
    case (let m, false) where m < 1440: String(localized: "\(m / 60)-Hour Window")
    case (let m, true) where m < 1440: String(localized: "\(m / 60)-hour")
    case (let m, false): String(localized: "\(m / 1440)-Day Window")
    case (let m, true): String(localized: "\(m / 1440)-day")
    }
}

/// "Resets at 16:40", or on another day "Resets Thu 09:00"; for a window of a day or less, with how long is left.
func resetText(_ w: LimitShown, now: Date) -> String {
    let time = w.resetsAt.formatted(.dateTime.hour(.twoDigits(amPM: .omitted)).minute(.twoDigits))
    let at = Calendar.current.isDate(w.resetsAt, inSameDayAs: now) ? String(localized: "Resets at \(time)")
        : String(localized: "Resets \(w.resetsAt.formatted(.dateTime.weekday(.abbreviated))) \(time)")
    guard w.minutes <= 1440 else { return at }
    let m = max(1, Int((w.resetsAt.timeIntervalSince(now) / 60).rounded(.up)))
    let left = m < 60 ? String(localized: "\(m) min left") : m % 60 == 0 ? String(localized: "\(m / 60) hr left") : String(localized: "\(m / 60) hr \(m % 60) min left")
    return "\(at) · \(left)"
}

/// Who said it and when: "Reported by MacBook Pro · 2 min ago".
@MainActor func reportedText(_ model: AppModel, _ c: AgentLimitsCard) -> String {
    let machine = model.machineName(c.machineID)
    return String(localized: "Reported by \(machine) · \(c.observedAt.map { agoText($0, now: model.now) } ?? "")")
}
