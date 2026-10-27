import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI

/// The grants: which agents can use which projects, machines and coding
/// agents, and for how long.
struct GrantsView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let active = model.clients.filter { !$0.revoked }
        List {
            Section {
                ForEach(active, id: \.grantID) { c in
                    NavigationLink(value: Route.grant(c.grantID)) { GrantRow(client: c) }
                        .accessibilityIdentifier("grant-\(c.label)")
                }
                if active.isEmpty {
                    if model.hasWorld { Text("No grants yet.").foregroundStyle(.secondary) } else { PageLoader(text: "Reading your grants…") }
                }
            } footer: {
                Text("A grant decides which of your projects, machines and coding agents an agent can use, and for how long.")
            }
        }
        .navigationTitle("Grants")
        .rootToolbar()
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button("Connect an Agent", systemImage: "plus") { model.sheet = .connectAgent }
            }
        }
    }
}

struct GrantRow: View {
    @Environment(AppModel.self) private var model
    let client: Client

    var body: some View {
        let days = client.daysLeft(now: model.now)
        HStack(spacing: 12) {
            GrantAvatar(label: client.label)
            VStack(alignment: .leading, spacing: 2) {
                Text(client.label)
                HStack(spacing: 0) {
                    Text(projectsText(client))
                    Text(" · ")
                    Text(days <= 7 ? String(localized: "ends in \(days) days") : String(localized: "\(days) days left")).foregroundStyle(days <= 7 ? .orange : .secondary)
                }
                .font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
            }
        }
    }

    func projectsText(_ c: Client) -> String {
        SigningPreview.isAll(c.spec.projectIds) ? String(localized: "All projects") : listText(c.spec.projectIds)
    }
}

/// One grant: what it can use, how it connects, and renewing, changing or
/// disconnecting it.
struct GrantView: View {
    @Environment(AppModel.self) private var model
    let id: String
    @State private var confirmDisconnect = false
    @State private var wakes: [Wake]?

    var body: some View {
        Group {
            if let c = model.grant(id: id) {
                content(c)
            } else {
                ContentUnavailableView("This grant is gone", systemImage: "checkmark.shield")
            }
        }
        .navigationBarTitleDisplayMode(.inline)
    }

    private func content(_ c: Client) -> some View {
        let days = c.daysLeft(now: model.now)
        let sessions = model.sessions.filter { $0.createdBy == c.clientID }
        return List {
            Section {
                VStack(spacing: 8) {
                    GrantAvatar(label: c.label, size: 58)
                    Text(c.label).font(.title2.bold())
                    Text("Valid until \(c.expiresAt.date.formatted(date: .abbreviated, time: .omitted)) · \(days) days left")
                        .font(.subheadline).foregroundStyle(days <= 7 ? .orange : .secondary)
                }
                .frame(maxWidth: .infinity).listRowBackground(Color.clear)
            }
            Section {
                LabeledContent("Projects", value: SigningPreview.isAll(c.spec.projectIds) ? String(localized: "All projects (including ones added later)") : listText(c.spec.projectIds))
                LabeledContent("Machines", value: SigningPreview.isAll(c.spec.machineIds) ? String(localized: "All machines (including ones that join later)") : listText(c.spec.machineIds.map(model.machineName)))
                LabeledContent("Coding agents", value: SigningPreview.isAll(c.spec.agents) ? String(localized: "All coding agents") : listText(c.spec.agents.map(agentTitle)))
            } header: {
                Text("What it can use")
            } footer: {
                Text("In these projects it can do anything you can.")
            }
            Section {
                if !c.custodian.isEmpty {
                    LabeledContent("Connects", value: String(localized: "Through \(URL(string: c.custodian)?.host ?? c.custodian), which keeps its key"))
                }
                Button {
                    model.path.append(.grantSessions(c.clientID))
                } label: {
                    LabeledContent("Sessions", value: String(localized: "\(sessions.filter { Calendar.current.isDateInToday($0.updatedDate) }.count) today · \(sessions.count) in all"))
                }
                .foregroundStyle(.primary)
                LabeledContent("Last online", value: c.online ? String(localized: "Now") : (c.hasLastSeen ? agoText(c.lastSeen.date, now: model.now) : "—"))
                LabeledContent("Fingerprint", value: c.fingerprint).font(.body.monospaced())
            }
            Section {
                Button("Change What It Can Use…") { model.sheet = .scope(c.grantID) }
                Button("Renew…") { model.sheet = .renew(c.grantID) }.accessibilityIdentifier("renew")
                Button("Disconnect…", role: .destructive) { confirmDisconnect = true }.accessibilityIdentifier("disconnect")
            } footer: {
                Text("Once disconnected, it loses access at once; running sessions are interrupted, and the sessions themselves stay.")
            }
            if let wakes { WakesSection(client: c, wakes: wakes) }
        }
        .navigationTitle(c.label)
        // Its doorbells, again whenever a sheet closes: one may have changed them.
        .task(id: model.sheet) {
            if model.sheet == nil, let api = model.api { wakes = try? await api.listWakes(c.clientID) }
        }
        .confirmationDialog("Disconnect “\(c.label)”?", isPresented: $confirmDisconnect, titleVisibility: .visible) {
            Button("Disconnect", role: .destructive) { Task { await model.disconnect(c) } }
        } message: {
            Text("It loses access at once; running sessions are interrupted, and the sessions themselves stay. To use it again, connect it again.")
        }
    }
}

/// An agent's doorbells (docs/agent-names.md §7): the webhooks Hermesh calls
/// when messages wait for the agent or one of its bots. Left out where the
/// relay keeps none.
struct WakesSection: View {
    @Environment(AppModel.self) private var model
    let client: Client
    let wakes: [Wake]

    var body: some View {
        Section {
            ForEach(wakes, id: \.name) { w in
                Button {
                    model.sheet = .wake(client.clientID, client.label, w)
                } label: {
                    HStack {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(w.name.isEmpty ? String(localized: "\(client.label) itself") : w.name).foregroundStyle(.primary)
                            Text(sub(w)).font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
                        }
                        Spacer()
                        Text(w.state == "on" ? String(localized: "Turned On") : String(localized: "Not working"))
                            .font(.subheadline).foregroundStyle(w.state == "on" ? .green : .red)
                    }
                }
                .accessibilityIdentifier("wake-\(w.name)")
            }
            if wakes.isEmpty { Text("Nothing wakes this agent yet.").foregroundStyle(.secondary) }
            Button("Add…") { model.sheet = .wake(client.clientID, client.label, nil) }.accessibilityIdentifier("wake-add")
        } header: {
            Text("Wake on New Messages")
        } footer: {
            Text("Hermesh calls a webhook, such as a Grok Bot routine’s, to wake this agent or one of its bots when messages wait for it. It never says what they say.")
        }
    }

    /// When it last called, or why it stopped.
    private func sub(_ w: Wake) -> String {
        guard w.state == "on" else { return wakeReason(status: w.lastStatus, reason: w.reason) }
        return w.hasLastRingAt ? String(localized: "Called \(agoText(w.lastRingAt.date, now: model.now))") : String(localized: "Not called yet")
    }
}
