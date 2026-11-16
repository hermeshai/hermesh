import HermeshHer
import HermeshKit
import SwiftUI

/// Connects one: an offered entry, or an address of the owner's own (docs/her-connectors-v2-demo.html). It says
/// what connecting means and has one button; her asks the address how it signs in, and the sheet goes on by what
/// she found: added as it is, signed in to with an account in the browser, or asked for a token when there is no
/// account to sign in with. A token can also be the owner's choice.
struct HerConnectSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let state: HerConnectorsState
    let machine: HerMachine

    private enum Step: Equatable {
        case address, intro, token(HerConnectWay.TokenReason), done(Int), failed(String)
    }

    /// The offered entry this is about: the one it opened for, or the one the address typed turned out to be.
    @State private var entry: HerCatalogEntry?
    @State private var step: Step
    /// What is under way, in the button's words.
    @State private var busy: String?
    @State private var name: String
    @State private var url: String
    @State private var probe: HerConnectorProbe?
    @State private var token = ""
    @State private var work: Task<Void, Never>?
    @State private var height: CGFloat = 0

    init(state: HerConnectorsState, machine: HerMachine, entry: HerCatalogEntry?, found: HerConnectorProbe? = nil) {
        self.state = state
        self.machine = machine
        _entry = State(initialValue: entry)
        _name = State(initialValue: entry?.id ?? "")
        _url = State(initialValue: entry?.url ?? "")
        _probe = State(initialValue: found)
        _step = State(initialValue: found?.error.map(Step.failed) ?? (entry == nil ? .address : .intro))
    }

    private var title: String { entry?.name ?? name }
    private var host: String { herHost(url) }
    private var way: HerConnectWay { probe.map(HerConnectWay.init) ?? entry?.way ?? .open }
    private var typing: Bool {
        switch step {
        case .address, .token: true
        default: false
        }
    }

    var body: some View {
        ScrollView {
            VStack(spacing: 0) { content }
                .padding(.horizontal, 24).padding(.top, 28).padding(.bottom, 8)
                .frame(maxWidth: .infinity)
                .background(GeometryReader { g in Color.clear.preference(key: SheetHeight.self, value: g.size.height) })
        }
        .scrollBounceBehavior(.basedOnSize)
        .onPreferenceChange(SheetHeight.self) { height = $0 }
        // As tall as what it says, short of the screen's top so that the page shows above it; the whole screen while
        // there is something to type, for the keyboard. What is taller scrolls.
        .presentationDetents(typing || height == 0 ? [.large] : [.height(min(height, tallest))])
        .presentationDragIndicator(.visible)
        // Whatever is under way stops when the sheet goes; the list says what came of it.
        .onDisappear { work?.cancel() }
    }

    @ViewBuilder private var content: some View {
        switch step {
        case .address: address
        case .intro: intro
        case .token(let why): tokenStep(why)
        case .done(let tools): done(tools)
        case .failed(let why): failed(why)
        }
    }

    // MARK: The steps

    /// An address of the owner's own: only the address; its name comes from it.
    @ViewBuilder private var address: some View {
        Text("Add a Custom Connector").font(.title2.weight(.bold)).multilineTextAlignment(.center)
        Text("Enter an MCP server’s address. \(machine.machineName) asks it how it signs in.").tagline()
        TextField("https://mcp.example.com/mcp", text: $url, axis: .vertical)
            .font(.callout.monospaced()).keyboardType(.URL).autocorrectionDisabled().textInputAutocapitalization(.never)
            .field()
            .disabled(busy != nil)
            .accessibilityIdentifier("connect-address")
        if let problem = addressProblem, !problem.isEmpty {
            Text(problem).font(.footnote).foregroundStyle(.red).frame(maxWidth: .infinity, alignment: .leading).padding(.top, 8)
        }
        Text("Connectors that run as a command are added on the Mac.").fine()
        buttons(String(localized: "Cancel"), { dismiss() }, String(localized: "Continue"), id: "connect-continue", disabled: addressProblem != nil) { ask() }
    }

    /// What connecting means, and the one button.
    @ViewBuilder private var intro: some View {
        HerConnectorTile(name: entry?.id ?? name, icon: probe?.icon, size: 60)
        Text(title).font(.title2.weight(.bold)).padding(.top, 12)
        Text(entry.map { String(localized: $0.about) } ?? host).tagline()
        VStack(alignment: .leading, spacing: 16) {
            ForEach(points, id: \.title) { p in
                HStack(alignment: .top, spacing: 14) {
                    Image(systemName: p.symbol).font(.title3).frame(width: 28)
                    VStack(alignment: .leading, spacing: 3) {
                        Text(p.title).font(.headline)
                        Text(p.text).font(.subheadline).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.top, 22)
        Divider().padding(.top, 18)
        if entry == nil {
            Text("This connector isn’t one Her offers. Only add services you trust: Her does what their tools say.").fine(.orange)
        } else {
            Text("You can disconnect it at any time, on its page here.").fine()
        }
        buttons(String(localized: "Cancel"), { dismiss() }, goWords, id: "connect-go") { go() }
        if busy == nil, way == .account {
            Button("Connect with a Token Instead") { step = .token(.chosen) }
                .font(.footnote).foregroundStyle(.secondary).padding(.vertical, 8)
                .accessibilityIdentifier("connect-token-instead")
        }
    }

    @ViewBuilder private func tokenStep(_ why: HerConnectWay.TokenReason) -> some View {
        HerConnectorTile(name: entry?.id ?? name, icon: probe?.icon, size: 64)
        Text("Connect \(title) with a Token").font(.title2.weight(.bold)).multilineTextAlignment(.center).padding(.top, 14)
        Text(tokenWhy(why)).tagline()
        SecureField("Paste the Token", text: $token)
            .font(.callout.monospaced()).textContentType(.oneTimeCode).autocorrectionDisabled().textInputAutocapitalization(.never)
            .field()
            .disabled(busy != nil)
            .accessibilityIdentifier("connect-token")
        if let page = entry?.tokenPage {
            Link(String(localized: "Get a token from \(title)"), destination: page).font(.footnote).padding(.top, 10)
        }
        Text("It is sent to \(host) as Authorization: Bearer and kept on \(machine.machineName); the configuration only refers to it.").fine()
        buttons(why == .chosen ? String(localized: "Back") : String(localized: "Cancel"), {
            if why == .chosen { token = ""; step = .intro } else { dismiss() }
        }, String(localized: "Connect"), id: "connect-token-go", disabled: token.trimmingCharacters(in: .whitespaces).isEmpty) {
            add(.token(token.trimmingCharacters(in: .whitespaces)))
        }
    }

    @ViewBuilder private func done(_ tools: Int) -> some View {
        Image(systemName: "checkmark.circle.fill").font(.system(size: 64)).foregroundStyle(.green)
        Text("Added \(title)").font(.title2.weight(.bold)).padding(.top, 12)
            .accessibilityIdentifier("connect-done")
        Text("\(tools) tools · Her can use it now").tagline()
        buttons(String(localized: "View \(title)"), {
            let name = name
            dismiss()
            model.path.append(.herConnector(name))
        }, String(localized: "Done"), id: "connect-close") { dismiss() }
    }

    @ViewBuilder private func failed(_ why: String) -> some View {
        Image(systemName: "xmark.circle.fill").font(.system(size: 64)).foregroundStyle(.red)
        Text("Couldn’t Connect \(title)").font(.title2.weight(.bold)).multilineTextAlignment(.center).padding(.top, 12)
        Text(why).font(.footnote.monospaced()).foregroundStyle(.secondary).textSelection(.enabled)
            .frame(maxWidth: .infinity, alignment: .leading).padding(12)
            .background(Palette.fill2, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .padding(.top, 16)
            .accessibilityIdentifier("connect-failed")
        buttons(entry == nil ? String(localized: "Edit the Address") : String(localized: "Cancel"), {
            if entry == nil { probe = nil; step = .address } else { dismiss() }
        }, String(localized: "Try Again"), id: "connect-retry") {
            probe = nil
            ask()
        }
    }

    // MARK: What the steps say

    /// What connecting means for the way it signs in: as whom Her acts, when she asks, and where what lets her in
    /// is kept.
    private var points: [(symbol: String, title: String, text: String)] {
        let asks = (symbol: "switch.2", title: String(localized: "Her asks before she changes anything"),
                    text: String(localized: "She uses the tools \(title) marks as read-only without asking, and asks you before any other."))
        switch way {
        case .account:
            return [("person.crop.circle", String(localized: "Her acts as you"),
                     String(localized: "She can see and do no more there than your account can, and no more than you agree to when you sign in.")),
                    asks,
                    ("lock", String(localized: "The sign-in stays on \(machine.machineName)"),
                     String(localized: "You sign in at \(host) in this iPhone’s browser. The sign-in is kept on \(machine.machineName), not on this iPhone or Hermesh’s servers."))]
        case .token:
            return [("key", String(localized: "Her acts with a token of yours"), String(localized: "She can see and do there what the token allows.")),
                    asks,
                    ("lock", String(localized: "The token stays on \(machine.machineName)"),
                     String(localized: "It is sent to \(host) and kept on \(machine.machineName), not on this iPhone or Hermesh’s servers."))]
        case .open:
            return [("bolt", String(localized: "No sign-in needed"), String(localized: "It is open to anyone: Her uses the tools it offers, with no account of yours.")),
                    asks,
                    ("network", String(localized: "\(machine.machineName) connects to it"),
                     String(localized: "\(machine.machineName) reaches \(host) itself; nothing passes through Hermesh’s servers."))]
        }
    }

    private var goWords: String {
        switch way {
        case .account: String(localized: "Sign In and Connect")
        case .token: String(localized: "Continue")
        case .open: String(localized: "Connect")
        }
    }

    private func tokenWhy(_ why: HerConnectWay.TokenReason) -> String {
        switch why {
        case .chosen: String(localized: "A token can let Her reach less than your account does.")
        case .noAccountSignIn: String(localized: "\(title) offers no sign-in with an account.")
        case .registeredAppsOnly: String(localized: "\(title) lets only apps registered with it sign in with an account, and Her isn’t one yet.")
        }
    }

    /// Why the address step can't go on, if it can't.
    private var addressProblem: String? {
        let u = url.trimmingCharacters(in: .whitespacesAndNewlines)
        if u.isEmpty { return "" }
        guard let parsed = URL(string: u), let scheme = parsed.scheme, parsed.host() != nil else {
            return String(localized: "Enter an address that starts with https://.")
        }
        if !["http", "https"].contains(scheme) { return String(localized: "Only http and https addresses work.") }
        return nil
    }

    // MARK: What the buttons do

    /// Asks the address how it signs in. For one of the owner's own this ends on what was found, for the owner to
    /// read before going on; for an offered entry it goes on at once.
    private func ask() {
        url = url.trimmingCharacters(in: .whitespacesAndNewlines)
        // An address typed that is an offered entry's, not connected yet, is that entry.
        if entry == nil, let e = herCatalogEntry(url: url), !state.shown.contains(where: { $0.name == e.id }) {
            entry = e
            url = e.url
        }
        let offered = entry != nil
        name = entry?.id ?? herNewConnectorName(url, taken: Set(state.shown.map(\.name)).union(herConnectorCatalog.flatMap(\.entries).map(\.id)))
        let (name, url) = (name, url)
        run(String(localized: "Asking \(herHost(url))…")) {
            let found = try await state.calls.probe(name: name, url: url)
            guard !Task.isCancelled else { return }
            probe = found
            if let error = found.error {
                busy = nil
                step = .failed(error)
            } else if offered {
                act(found)
            } else {
                busy = nil
                step = .intro
            }
        }
    }

    private func go() {
        if let probe { act(probe) } else { ask() }
    }

    /// Goes on by what her found.
    private func act(_ found: HerConnectorProbe) {
        switch HerConnectWay(found) {
        case .open: add(.none)
        case .account: add(.oauth)
        case .token(let why):
            busy = nil
            step = .token(why)
        }
    }

    private func add(_ signIn: HerConnectorSignIn) {
        token = ""
        let (name, url, chat) = (name, url, state.chat)
        run(signIn == .oauth ? String(localized: "Waiting for the Sign-In…") : String(localized: "Connecting…")) {
            let tools = try await state.calls.add(name: name, url: url, signIn: signIn) { server in
                await chat.signIn(connector: name, server: server)
            }
            await state.load()
            guard !Task.isCancelled else { return }
            busy = nil
            // A sign-in the owner closed leaves the sheet where it was.
            if let tools {
                Haptic.success.play()
                step = .done(tools.count)
            }
        }
    }

    private func run(_ words: String, _ body: @escaping @MainActor () async throws -> Void) {
        busy = words
        work = Task {
            do {
                try await body()
            } catch {
                guard !Task.isCancelled else { return }
                busy = nil
                step = .failed(error.localizedDescription)
            }
        }
    }

    // MARK: The buttons

    /// A step's two buttons side by side: the way back on the left, the way on on the right, which says what is
    /// under way while something is. While it is, the way back is Cancel, which stops it and closes the sheet.
    private func buttons(_ back: String, _ backAction: @escaping () -> Void, _ words: String, id: String, disabled: Bool = false,
                         action: @escaping () -> Void) -> some View {
        HStack(spacing: 12) {
            Button {
                if busy != nil { dismiss() } else { backAction() }
            } label: {
                Text(busy != nil ? String(localized: "Cancel") : back).font(.headline).lineLimit(1).minimumScaleFactor(0.8)
                    .frame(maxWidth: .infinity).frame(height: 34)
            }
            .buttonStyle(.bordered).buttonBorderShape(.capsule).tint(.secondary)
            .accessibilityIdentifier("connect-back")
            Button(action: action) {
                HStack(spacing: 6) {
                    if busy != nil { ProgressView().tint(.white).controlSize(.small) }
                    Text(busy ?? words).font(.headline).lineLimit(1).minimumScaleFactor(0.7)
                }
                .frame(maxWidth: .infinity).frame(height: 34)
            }
            .buttonStyle(.borderedProminent).buttonBorderShape(.capsule)
            .disabled(disabled || busy != nil)
            .accessibilityIdentifier(id)
        }
        .padding(.top, 20)
    }

    /// How tall the sheet grows: short of the screen's top, with room for the page above it.
    private var tallest: CGFloat {
        let screen = (UIApplication.shared.connectedScenes.first as? UIWindowScene)?.screen.bounds.height ?? 800
        return screen * 0.82
    }
}

private struct SheetHeight: PreferenceKey {
    static let defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = max(value, nextValue()) }
}

private extension View {
    /// The line under a sheet's title.
    func tagline() -> some View {
        font(.callout).foregroundStyle(.secondary).multilineTextAlignment(.center).padding(.top, 6)
    }

    /// A sheet's small print.
    func fine(_ colour: Color = .secondary) -> some View {
        font(.footnote).foregroundStyle(colour).frame(maxWidth: .infinity, alignment: .leading).padding(.top, 12)
    }

    /// A sheet's one field.
    func field() -> some View {
        padding(.horizontal, 16).padding(.vertical, 14)
            .background(Palette.fill2, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
            .padding(.top, 20)
    }
}
