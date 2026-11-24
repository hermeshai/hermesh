import HermeshProto
import HermeshSessions
import SwiftUI

// MARK: Usage

/// The filter row (the system's menus: range, coding agent, where, sender), then a card a measure (sessions,
/// tokens) with its number, its change against the span before, its bars and its figures, then 18 weeks of tokens.
struct UsagePage: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let stats = model.stats
        let period = stats.period
        let res = stats.page(model.now)
        let page = usagePage(res, period: period, now: model.now)
        let choices = usageChoices(stats.answer(stats.menusRequest(model.now)), filter: stats.filter)
        let weeks = stats.answer(stats.weeksRequest(model.now))
        VStack(alignment: .leading, spacing: 10) {
            filters(choices)
            heading(Text("Sessions"), delta: page.sessionsDelta)
            MeasureCard(id: "usage-sessions", big: Text("\(page.sessions)"), unit: Text("sessions worked"),
                        values: page.buckets.map { Double($0.sessions) }, now: page.now, period: period, loading: res == nil,
                        facts: [(Text("Done"), Text("\(page.done)")), (Text("Failed"), Text("\(page.failedTurns) turns")), (Text("Time at work"), Text(durationText(page.busySeconds)))])
            heading(Text(verbatim: "Token"), delta: page.tokensDelta)
            MeasureCard(id: "usage-tokens", big: Text(verbatim: tokensText(page.tokens)), unit: Text(verbatim: "token"),
                        values: page.buckets.map { Double($0.tokens) }, now: page.now, period: period, loading: res == nil,
                        facts: [(Text("Input"), Text(verbatim: tokensText(page.inputTokens))), (Text("Output"), Text(verbatim: tokensText(page.outputTokens)))])
            WeeksCard(usage: weeks.map { usageYear($0.turns, today: model.now, weeks: 18) })
            Text(foot(res)).font(.caption).foregroundStyle(.secondary).padding(.horizontal, 4)
                .accessibilityIdentifier("usage-foot")
        }
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

    /// The four filters, in one row: the range, then the coding agent, where and the sender. A chosen one is tinted.
    private func filters(_ c: UsageChoices) -> some View {
        let stats = model.stats
        let f = stats.filter
        return HStack(spacing: 6) {
            Menu {
                ForEach(WorkPeriod.allCases, id: \.self) { p in
                    choice(Text(periodTitle(p)), counts: nil, on: stats.period == p) { stats.period = p }
                }
            } label: {
                chip(Text(periodTitle(stats.period)), on: false)
            }
            .accessibilityIdentifier("filter-range")
            Divider().frame(height: 18)
            Menu {
                choice(Text("All"), counts: nil, on: f.agent.isEmpty) { stats.filter.agent = "" }
                Section {
                    ForEach(c.agents) { a in choice(Text(verbatim: agentTitle(a.id)), counts: a, on: a.id == f.agent) { stats.filter.agent = a.id } }
                }
            } label: {
                chip(f.agent.isEmpty ? Text("Agent Provider") : Text(verbatim: agentTitle(f.agent)), on: !f.agent.isEmpty)
            }
            .menuOrder(.fixed)
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
                chip(f.machineID.isEmpty ? Text("Environment / Project") : Text(verbatim: model.machineName(f.machineID) + (f.projectID.isEmpty ? "" : "/" + f.projectID)), on: !f.machineID.isEmpty)
            }
            .menuOrder(.fixed)
            .accessibilityIdentifier("filter-place")
            Menu {
                choice(Text("All"), counts: nil, on: f.client.isEmpty) { stats.filter.client = "" }
                Section {
                    ForEach(c.clients) { s in choice(Text(verbatim: model.senderName(s.id)), counts: s, on: s.id == f.client) { stats.filter.client = s.id } }
                }
            } label: {
                chip(f.client.isEmpty ? Text("Sender") : Text(verbatim: model.senderName(f.client)), on: !f.client.isEmpty)
            }
            .menuOrder(.fixed)
            .accessibilityIdentifier("filter-client")
        }
        .font(.subheadline)
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

/// A measure: its number and unit, its bars an hour or a day (past in ink grey, now in the accent, to come faint),
/// the axis under them, and its figures.
struct MeasureCard: View {
    let id: String
    let big: Text
    let unit: Text
    let values: [Double]
    let now: Int
    let period: WorkPeriod
    let loading: Bool
    let facts: [(Text, Text)]

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
            HStack(alignment: .bottom, spacing: period == .month ? 2 : period == .week ? 8 : 3) {
                ForEach(values.indices, id: \.self) { i in
                    RoundedRectangle(cornerRadius: period == .week ? 5 : 3, style: .continuous)
                        .fill(loading ? AnyShapeStyle(Skeleton.grey) : i > now ? AnyShapeStyle(.quaternary) : i == now ? AnyShapeStyle(Color.accentColor) : AnyShapeStyle(Color.primary.opacity(0.32)))
                        .frame(height: i > now ? 3 : max(3, CGFloat(values[i] / top) * 56))
                }
            }
            .frame(height: 56, alignment: .bottom)
            .accessibilityHidden(true)
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
