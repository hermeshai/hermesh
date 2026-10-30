import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI

/// Where the guide stands, kept on this iPhone for its owner (forgotten with
/// the world, docs/onboarding.md §5.2).
enum Guide {
    static let key = "guide"
    /// The page the guide is on, so that it comes back there when iOS ended
    /// the app meanwhile (say while the person was in a terminal).
    static let stepKey = "guide.step"
    /// Set up on this iPhone: the guide follows the first run.
    static let on = "on"
    /// Put off: Get Started on the overview leads back to it.
    static let later = "later"
    static let done = "done"
}

/// After this iPhone set an owner up: a computer (a Mac, or Linux), its
/// coding agents and a project, an agent connected, and its first task.
/// Later leaves the rest to Get Started.
struct GuideView: View {
    @Environment(AppModel.self) private var model
    @AppStorage(Guide.key) private var guide = ""
    @AppStorage(Guide.stepKey) private var savedStep = ""

    enum Step: Hashable {
        case computer, mac, linux
        case agents(String) // machine id
        case connect
        case tryIt(String) // client id

        var saved: String {
            switch self {
            case .computer: "computer"
            case .mac: "mac"
            case .linux: "linux"
            case .agents(let m): "agents:" + m
            case .connect: "connect"
            case .tryIt(let c): "try:" + c
            }
        }

        init?(saved: String) {
            let parts = saved.split(separator: ":", maxSplits: 1).map(String.init)
            switch (parts.first, parts.count > 1 ? parts[1] : nil) {
            case ("computer", nil): self = .computer
            case ("mac", nil): self = .mac
            case ("linux", nil): self = .linux
            case ("agents", let m?): self = .agents(m)
            case ("connect", nil): self = .connect
            case ("try", let c?): self = .tryIt(c)
            default: return nil
            }
        }
    }

    @State private var step: Step?
    @State private var forward = true
    @State private var path: [Route] = []
    /// What was there when the step began: the machine or agent that joins
    /// after it is the one the guide goes on with.
    @State private var knownMachines: Set<String> = []
    @State private var knownClients: Set<String> = []
    @State private var scanning = false

    var body: some View {
        @Bindable var model = model
        NavigationStack(path: $path) {
            ZStack {
                if let step {
                    page(step)
                        .id(step)
                        .transition(.asymmetric(insertion: .move(edge: forward ? .trailing : .leading), removal: .move(edge: forward ? .leading : .trailing)))
                }
            }
            .clipped()
            .background(Color(.systemGroupedBackground))
            .toolbar(.hidden, for: .navigationBar)
            .navigationDestination(for: Route.self) { route in
                if case .machineAgent(let machine, let agent) = route { MachineAgentView(machineID: machine, agent: agent) }
            }
        }
        .sheet(item: $model.sheet) { sheet in
            Group {
                if case .pairing(let code) = sheet { GuideApprovalSheet(code: code) } else { SheetHost(kind: sheet) }
            }
            .environment(model)
        }
        .overlay(alignment: .bottom) {
            if let t = model.toast {
                ToastView(toast: t) { model.toast = nil }.padding(.bottom, 24).transition(.move(edge: .bottom).combined(with: .opacity))
            }
        }
        .animation(.smooth(duration: 0.35), value: model.toast)
        .onAppear { if step == nil { step = Step(saved: savedStep) ?? firstStep() } }
        .onChange(of: step) { _, s in savedStep = s?.saved ?? "" }
        .onChange(of: model.pairings.map(\.code)) { askToApprove() }
        .onChange(of: model.sheet == nil) { _, closed in if closed { moveOn() } }
    }

    /// Where to start, or to come back to from Get Started: the first part not done.
    private func firstStep() -> Step {
        guard let m = model.machines.first else { return .computer }
        if model.projects.isEmpty { return .agents(m.machineID) }
        guard let c = model.clients.first(where: { !$0.revoked }) else { return .connect }
        return .tryIt(c.clientID)
    }

    private func go(_ next: Step, back: Bool = false) {
        forward = !back
        switch next {
        case .mac, .linux: knownMachines = Set(model.machines.map(\.machineID))
        case .connect: knownClients = Set(model.clients.map(\.clientID))
        default: break
        }
        withAnimation(.smooth(duration: 0.4)) { step = next }
        askToApprove()
    }

    private func putOff() {
        savedStep = ""
        guide = Guide.later
    }

    /// A request this step waits for opens to approve as soon as it comes.
    private func askToApprove() {
        guard model.sheet == nil else { return }
        let kinds: [Hermesh_Identity_V1_PairingKind]
        switch step {
        case .mac: kinds = [.device, .machine]
        case .linux: kinds = [.machine]
        case .connect: kinds = [.client]
        default: return
        }
        if let p = model.pairings.first(where: { kinds.contains($0.kind) }) { model.sheet = .pairing(p.code) }
    }

    /// A request approved and its sheet closed: on to what comes next.
    private func moveOn() {
        switch step {
        case .mac, .linux:
            if let m = model.machines.first(where: { !knownMachines.contains($0.machineID) }) { go(.agents(m.machineID)) }
        case .connect:
            if let c = model.clients.first(where: { !knownClients.contains($0.clientID) && !$0.revoked }) { go(.tryIt(c.clientID)) }
        default: break
        }
    }

    @ViewBuilder private func page(_ s: Step) -> some View {
        switch s {
        case .computer: computer
        case .mac: mac
        case .linux: linux
        case .agents(let machine): agents(machine)
        case .connect: connect
        case .tryIt(let client): tryIt(client)
        }
    }

    // MARK: A computer

    private var computer: some View {
        GuidePage(part: .computer, title: Text("Where does your code live?")) {
            GuideCard {
                GuideChoice(systemImage: "laptopcomputer", title: Text(verbatim: "Mac"), id: "computer-mac") { go(.mac) }
                GuideDivider()
                GuideChoice(systemImage: "server.rack", title: Text(verbatim: "Linux"), subtitle: Text("Servers, VPS"), id: "computer-linux") { go(.linux) }
            }
        } footer: {
            GuideLink(title: Text("Set Up Later"), id: "set-up-later") { putOff() }
        }
    }

    private static let macDownload = "https://hermesh.io/download/mac"

    private var mac: some View {
        GuidePage(part: .computer, back: { go(.computer, back: true) }, later: putOff, title: Text("Install Hermesh on your Mac")) {
            GuideCard {
                numbered(1) {
                    Text("Download Hermesh for Mac").font(.body.weight(.semibold))
                    Text(verbatim: "hermesh.io/download/mac").font(.subheadline.monospaced()).foregroundStyle(.secondary)
                    HStack(spacing: 10) {
                        Button {
                            UIPasteboard.general.string = Self.macDownload
                            model.show(String(localized: "Copied"))
                        } label: {
                            Label("Copy Link", systemImage: "doc.on.doc").frame(maxWidth: .infinity)
                        }
                        ShareLink(item: URL(string: Self.macDownload)!) {
                            Label("AirDrop…", systemImage: "square.and.arrow.up").frame(maxWidth: .infinity)
                        }
                    }
                    .buttonStyle(.glass).padding(.top, 8)
                }
                GuideDivider(inset: 52)
                numbered(2) {
                    if model.status.hosted {
                        Text("Sign in with the same account").font(.body.weight(.semibold))
                        if !model.status.account.isEmpty { Text(model.status.account).font(.subheadline).foregroundStyle(.secondary) }
                    } else {
                        Text("Choose Use Your Own Server… and type").font(.body.weight(.semibold))
                        Text(model.status.connectAddress).font(.subheadline.monospaced()).foregroundStyle(.secondary).textSelection(.enabled)
                    }
                }
            }
        } footer: {
            GuideWaiting(text: Text("Waiting for your Mac to sign in…"))
        }
    }

    private func numbered(_ n: Int, @ViewBuilder _ content: () -> some View) -> some View {
        HStack(alignment: .top, spacing: 12) {
            Text(verbatim: "\(n)").font(.footnote.bold()).foregroundStyle(Color.accentColor)
                .frame(width: 24, height: 24).background(Color.accentColor.opacity(0.12), in: Circle())
            VStack(alignment: .leading, spacing: 2) { content() }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 16).padding(.vertical, 12)
    }

    private var linux: some View {
        let hosted = model.status.hosted
        // Without the official service, the address in the command brings its request here by itself.
        let command = hosted ? AddMachineSheet.install : AddMachineSheet.install + " -s -- \(model.status.connectAddress)"
        return GuidePage(part: .computer, back: { go(.computer, back: true) }, later: putOff, title: Text("Install Hermesh on Linux")) {
            GuideCard {
                Text(command).font(.footnote.monospaced()).textSelection(.enabled)
                    .padding(14).frame(maxWidth: .infinity, alignment: .leading)
            }
            CopyShareButtons(text: command, shareTitle: "Share…").padding(.horizontal, 16)
            Text("Run it as the user you code as, not as root.").font(.footnote).foregroundStyle(.secondary).padding(.horizontal, 32)
        } footer: {
            if hosted {
                GuideButton(title: Text("Scan the QR Code"), systemImage: "qrcode.viewfinder", id: "scan") { model.sheet = .scan }
            } else {
                GuideWaiting(text: Text("Waiting for that machine to connect…"))
            }
            GuideLink(title: Text("Enter the Pairing Code…"), id: "enter-code") { model.sheet = .enterCode }
        }
    }

    // MARK: Its coding agents

    private func agents(_ machineID: String) -> some View {
        let m = model.machines.first { $0.machineID == machineID }
        let name = m?.machineName ?? model.machineName(machineID)
        let sorted = sortedAgents(m?.status.agents ?? [])
        let found = sorted.installed
        let rest = codingAgentIDs.filter { id in !found.contains { $0.name == id } }
        let looking = scanning || !sorted.checking.isEmpty
        return GuidePage(part: .codingAgent, back: { go(.computer, back: true) }, title: Text("Coding agents")) {
            GuideCaption(text: Text("Found \(found.count) on \(name)"))
            GuideCard {
                if found.isEmpty {
                    HStack(spacing: 10) {
                        if looking || m?.online != true { MovingMark(size: 18, motion: .seq) }
                        Group {
                            if m?.online != true {
                                Text("Waiting for \(name) to connect…")
                            } else if looking {
                                Text("Scanning \(name)…")
                            } else {
                                Text("No coding agents yet")
                            }
                        }
                        .foregroundStyle(.secondary)
                    }
                    .frame(maxWidth: .infinity, minHeight: 56)
                }
                ForEach(Array(found.enumerated()), id: \.element.name) { i, a in
                    if i > 0 { GuideDivider() }
                    NavigationLink(value: Route.machineAgent(machineID, a.name)) {
                        agentRow(a, projects: projectsFor(model.projects.filter { $0.machineID == machineID }, a.name).count)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("agent-\(a.name)")
                }
            }
            GuideCaption(text: Text("Also works with"))
            GuideCard {
                LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 4), count: 5), alignment: .center, spacing: 14) {
                    ForEach(rest, id: \.self) { id in
                        VStack(spacing: 6) {
                            AgentLogo(id: id, size: 40)
                            Text(agentTitle(id)).font(.caption2).foregroundStyle(.secondary).lineLimit(2).multilineTextAlignment(.center)
                        }
                        .frame(maxHeight: .infinity, alignment: .top)
                    }
                }
                .padding(.horizontal, 8).padding(.vertical, 16)
            }
        } footer: {
            GuideButton(title: Text("Continue"), id: "agents-continue") { go(.connect) }
            GuideLink(title: looking ? Text("Scanning \(name)…") : Text("Rescan after installing on \(name)"), systemImage: looking ? nil : "arrow.clockwise", id: "rescan") {
                Task { await rescan(machineID) }
            }
            .disabled(m?.online != true || looking)
        }
        // A machine looks for its coding agents when asked: just joined, with
        // none listed, it is asked once (as its page does).
        .task(id: m?.online) {
            guard m?.online == true, m?.status.agents.isEmpty == true else { return }
            await rescan(machineID)
        }
    }

    private func rescan(_ machineID: String) async {
        guard !scanning else { return }
        scanning = true
        await model.recheck(machine: machineID)
        scanning = false
    }

    private func agentRow(_ a: AgentStatus, projects: Int) -> some View {
        HStack(spacing: 12) {
            AgentLogo(id: a.name, size: 32)
            VStack(alignment: .leading, spacing: 2) {
                Text(agentTitle(a.name)).foregroundStyle(.primary)
                Group {
                    if !a.loggedIn {
                        Text("Not signed in").foregroundStyle(.orange)
                    } else if projects > 0 {
                        Text("Signed in · \(projects) projects")
                    } else {
                        Text("Signed in")
                    }
                }
                .font(.subheadline).foregroundStyle(.secondary)
            }
            Spacer(minLength: 0)
            Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(.tertiary)
        }
        .padding(.horizontal, 16).frame(minHeight: 60).contentShape(.rect)
    }

    // MARK: An agent, and its first task

    @State private var kind = PersonalAgentKind.all[1]

    private var connect: some View {
        let byConnector = kind.mcp && model.status.hosted
        let text = byConnector ? mcpConnectorURL : connectMessage(model.status, agent: kind.name)
        return GuidePage(part: .connect, back: { if let m = model.machines.first { go(.agents(m.machineID), back: true) } }, later: putOff, title: Text("Connect your agent")) {
            ScrollView(.horizontal) {
                HStack(spacing: 8) {
                    ForEach(PersonalAgentKind.all) { k in
                        Button {
                            kind = k
                        } label: {
                            HStack(spacing: 8) {
                                if let l = k.logo { AgentLogo(id: l, size: 28) }
                                Text(k.name)
                            }
                            .padding(.leading, 8).padding(.trailing, 14).frame(height: 44)
                            .background(Color(.secondarySystemGroupedBackground), in: .capsule)
                            .overlay(Capsule().strokeBorder(kind == k ? Color.accentColor : .clear, lineWidth: 2))
                        }
                        .buttonStyle(.plain)
                    }
                }
                .padding(.horizontal, 16)
            }
            .scrollIndicators(.hidden)
            GuideCaption(text: byConnector ? Text("In \(kind.name), add a connector") : Text("Send this to \(kind.name)"))
            GuideCard {
                Text(text).font(.footnote.monospaced()).textSelection(.enabled)
                    .padding(14).frame(maxWidth: .infinity, alignment: .leading)
            }
            CopyShareButtons(text: text, shareTitle: "Send…").padding(.horizontal, 16)
        } footer: {
            GuideWaiting(text: Text("Waiting for it to connect…"))
        }
    }

    private func tryIt(_ clientID: String) -> some View {
        let name = model.clientLabel(clientID)
        let first = model.sessions.filter { $0.createdBy == clientID }.min { $0.createdAt.date < $1.createdAt.date }
        let prompt = firstTaskPrompt(model)
        let machine = model.machines.first
        let agent = machine?.status.agents.first { $0.available && $0.loggedIn }?.name ?? "claude"
        return GuidePage(part: .connect, title: Text("Try it"), lede: Text("Send this to \(name)")) {
            if model.projects.isEmpty, let machine {
                GuideCard {
                    HStack(spacing: 12) {
                        GuideTile(systemImage: "folder")
                        Text("Add a project first").font(.body.weight(.semibold))
                        Spacer(minLength: 0)
                        Button("Add…") { model.sheet = .addProject(machine.machineID, agent) }
                            .buttonStyle(.glassProminent)
                            .accessibilityIdentifier("try-add-project")
                    }
                    .padding(.horizontal, 16).frame(minHeight: 60)
                }
            }
            GuideCard {
                Text(prompt).font(.body).textSelection(.enabled)
                    .padding(16).frame(maxWidth: .infinity, alignment: .leading)
            }
            CopyShareButtons(text: prompt, shareTitle: "Send…").padding(.horizontal, 16)
            if let s = first {
                GuideCard {
                    VStack(alignment: .leading, spacing: 10) {
                        HStack(spacing: 8) {
                            AgentLogo(id: s.agent, size: 22)
                            Text(verbatim: "\(agentTitle(s.agent)) · \(s.projectID)").font(.subheadline).foregroundStyle(.secondary)
                            Spacer(minLength: 0)
                            if s.isActive {
                                MovingMark(size: 18, motion: .seq)
                            } else {
                                Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
                            }
                        }
                        FirstAnswer(sessionID: s.sessionID)
                    }
                    .padding(16).frame(maxWidth: .infinity, alignment: .leading)
                }
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("first-session")
            }
        } footer: {
            if let s = first, !s.isActive {
                GuideButton(title: Text("Done"), id: "guide-done") {
                    savedStep = ""
                    guide = Guide.done
                }
            } else if first == nil {
                GuideWaiting(text: Text("Waiting for it to start…"))
            }
        }
    }
}

/// What the first session says, as it says it: its last message, from the
/// session's events (lists leave it out).
private struct FirstAnswer: View {
    @Environment(AppModel.self) private var model
    let sessionID: String
    @State private var text = ""

    var body: some View {
        Text(text).font(.body)
            .task(id: sessionID) {
                guard let api = model.api else { return }
                do {
                    for try await e in api.events(sessionID) {
                        if case .agentMessage(let m) = e.kind { text = m.text }
                    }
                } catch {}
            }
    }
}

/// The hosted MCP connector's address (docs/mcp-gateway.md).
let mcpConnectorURL = "https://mcp.hermesh.io/mcp"

/// A request the guide waits for, to approve: a Mac or another device (a
/// trusted device, maybe a machine too), a machine, or an agent. Then the
/// two digits to type on it, until it joins.
struct GuideApprovalSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let code: String
    @State private var name = ""
    @State private var scope: ScopeChoice = {
        var s = ScopeChoice()
        s.allProjects = true
        return s
    }()
    @State private var busy = false
    /// Approved: who joins, the digits to type there, and how to tell it joined.
    @State private var joining: (label: String, digits: String, machineID: String?)?
    @State private var detent: PresentationDetent = .large

    private var pairing: PairingDetails? { model.pairings.first { $0.code == code } }

    var body: some View {
        NavigationStack {
            Group {
                if let joining {
                    GuideJoining(label: joining.label, digits: joining.digits, machineID: joining.machineID) { dismiss() }
                } else if let p = pairing {
                    form(p)
                } else {
                    ContentUnavailableView("This request is gone", systemImage: "checkmark.circle", description: Text("It may have been approved elsewhere, or it expired."))
                }
            }
            .toolbar { CloseButton() }
        }
        .presentationDetents([.medium, .large], selection: $detent)
        .task { if let p = pairing { name = p.label } }
    }

    private func form(_ p: PairingDetails) -> some View {
        List {
            Section {
                VStack(spacing: 10) {
                    switch p.kind {
                    case .client: AgentLogo(id: p.label, size: 56)
                    case .machine: GuideTile(systemImage: p.platform.hasPrefix("darwin") ? "laptopcomputer" : "server.rack", size: 64)
                    default: GuideTile(systemImage: "laptopcomputer", size: 64)
                    }
                    Text(p.kind == .client ? String(localized: "\(p.label) wants to connect") : String(localized: "\(p.label) wants to join"))
                        .font(.title2.bold()).multilineTextAlignment(.center)
                    let who = [p.hostname == p.label ? "" : p.hostname, platformText(p.platform)].filter { !$0.isEmpty }.joined(separator: " · ")
                    if p.kind == .machine && !who.isEmpty { Text(who).font(.subheadline).foregroundStyle(.secondary) }
                }
                .frame(maxWidth: .infinity)
                .listRowBackground(Color.clear)
            }
            if !p.confirmed {
                Section {
                    LabeledContent("Verification code") { Text(p.verificationCode).font(.body.monospaced()) }
                        .accessibilityIdentifier("verification-code")
                } footer: {
                    Text("Approve if it matches what \(p.label) shows")
                }
            }
            switch p.kind {
            case .machine:
                Section {
                    LabeledContent("Name") {
                        TextField("Machine name", text: $name).multilineTextAlignment(.trailing)
                            .textInputAutocapitalization(.never).autocorrectionDisabled()
                            .accessibilityIdentifier("machine-name")
                    }
                }
            case .client:
                ScopeRows(scope: $scope, projects: model.projects, showsTTL: false)
            default:
                EmptyView()
            }
            Section {
                VStack(spacing: 4) {
                    GuideButton(title: Text("Approve"), systemImage: "faceid", id: "approve") { approve(p) }
                        .disabled(busy || (p.kind == .client && !scope.complete) || (p.kind == .machine && name.isEmpty))
                    GuideLink(title: Text("Deny"), id: "deny") {
                        model.dismissedPairings.insert(p.code)
                        model.pairings.removeAll { $0.code == p.code }
                        dismiss()
                    }
                }
                .listRowBackground(Color.clear).listRowInsets(EdgeInsets())
            }
        }
    }

    private func approve(_ p: PairingDetails) {
        busy = true
        Task {
            defer { busy = false }
            switch p.kind {
            case .client:
                var spec = scope.spec
                spec.label = p.label
                if await model.approveClient(p, spec: spec) { dismiss() }
            case .machine:
                if let res = await model.approveMachine(p, name: name) {
                    joining = (name, res.matchCode, res.machineID.isEmpty ? p.subjectID : res.machineID)
                    detent = .medium
                }
            default:
                if let res = await model.approveDevice(p, inline: true) {
                    joining = (p.label, res.matchCode, nil)
                    detent = .medium
                }
            }
        }
    }
}

/// After approving: the two digits to type on what joins, until it does.
/// A Mac that joins makes its own machine, by its name.
private struct GuideJoining: View {
    @Environment(AppModel.self) private var model
    let label: String
    let digits: String
    let machineID: String?
    let close: () -> Void

    private var joined: Bool {
        model.machines.contains { m in m.online && (machineID.map { m.machineID == $0 } ?? (m.machineName == label)) }
    }

    var body: some View {
        VStack(spacing: 16) {
            Spacer()
            if joined {
                Image(systemName: "checkmark.circle.fill").font(.system(size: 60)).foregroundStyle(.green)
                Text("\(label) joined").font(.title2.bold()).multilineTextAlignment(.center)
                    .accessibilityIdentifier("machine-joined")
            } else if digits.isEmpty {
                WaitingMark(size: 64)
                Text("Waiting for \(label) to connect…").font(.title3.bold()).multilineTextAlignment(.center)
            } else {
                Text("Type on \(label)").font(.title2.bold()).multilineTextAlignment(.center)
                HStack(spacing: 14) {
                    ForEach(Array(digits.enumerated()), id: \.offset) { _, d in
                        Text(String(d)).font(.system(size: 48, weight: .semibold, design: .monospaced))
                            .frame(width: 76, height: 94).background(.fill.tertiary, in: RoundedRectangle(cornerRadius: 22, style: .continuous))
                    }
                }
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("match-digits")
                GuideWaiting(text: Text("Waiting for the digits…"))
            }
            Spacer()
        }
        .padding(28)
        .task(id: joined) {
            guard joined else { return }
            try? await Task.sleep(for: .seconds(1.3))
            close()
        }
    }
}
