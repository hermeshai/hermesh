import AuthenticationServices
import CryptoKit
import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI
import UserNotifications

/// The official service the app signs in to.
let officialCenter = URL(string: "https://hermesh.io")!

/// The first run (docs/onboarding.md §5.1): first time, or already using
/// Hermesh on another device; signing in (or one's own server), then this
/// iPhone's management key and the guide, or another device's approval and
/// its two digits; then notifications.
struct OnboardingView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.webAuthenticationSession) private var webAuth
    @AppStorage(Guide.key) private var guide = ""

    enum Step: Hashable {
        case welcome
        case ownServer
        /// Chose the first time, but the account already has trusted devices.
        case inUse(owner: String, relay: String)
        /// Chose another device, but the account is new.
        case newAccount
        /// The management key, made with `pending` once asked for.
        case key
        /// Asking to join this owner, then waiting for another device.
        case approve(owner: String, relay: String)
        case digits(code: String, by: String)
        case joined
        /// Last; the guide follows when this iPhone was set up here.
        case notifications(guided: Bool)
    }

    @State private var step: Step = .welcome
    @State private var forward = true
    /// The card chosen on the first page: already using Hermesh elsewhere.
    @State private var joining = false
    /// Signing in again for another account: no cookies kept from the last.
    @State private var anotherAccount = false
    /// The setup the key page runs, and the server it names.
    @State private var pending: Hermesh_Local_V1_SetupRequest?
    @State private var server = "hermesh.io"
    /// The request to join, once made: its code and the code to compare.
    @State private var request: (code: String, verification: String)?
    @State private var address = ""
    @State private var claim = ""
    @State private var claiming = false
    @State private var digits = ""
    @State private var busy = false
    @State private var error: String?
    @State private var lost = false
    @State private var waitTask: Task<Void, Never>?
    @FocusState private var typing: Bool

    var body: some View {
        ZStack {
            page(step)
                .id(step)
                .transition(.asymmetric(insertion: .move(edge: forward ? .trailing : .leading), removal: .move(edge: forward ? .leading : .trailing)))
        }
        .clipped()
        .background(Color(.systemGroupedBackground))
        .onChange(of: model.onboardingLink) { _, url in if let url { handle(url) } }
    }

    private func go(_ next: Step, back: Bool = false) {
        error = nil
        forward = !back
        withAnimation(.smooth(duration: 0.4)) { step = next }
    }

    @ViewBuilder private func page(_ s: Step) -> some View {
        switch s {
        case .welcome: welcome
        case .ownServer: ownServer
        case .inUse(let owner, let relay): inUse(owner: owner, relay: relay)
        case .newAccount: newAccount
        case .key: keyPage
        case .approve(let owner, let relay): approve(owner: owner, relay: relay)
        case .digits(let code, let by): digitsPage(code: code, by: by)
        case .joined: joinedPage
        case .notifications(let guided): notificationsPage(guided: guided)
        }
    }

    // MARK: Pages

    private var welcome: some View {
        VStack(spacing: 0) {
            ErasedNote().padding(.horizontal, 16).padding(.top, 8)
            Spacer()
            Mark(size: 88)
            Text(verbatim: "Hermesh").font(.largeTitle.bold()).padding(.top, 14)
            Spacer()
            if let error { Text(error).font(.footnote).foregroundStyle(.red).multilineTextAlignment(.center).padding(.horizontal, 28).padding(.bottom, 12) }
            GuideCard {
                GuideChoice(systemImage: "sparkle", accent: true, title: Text("First time using Hermesh"), id: "start-new") { signIn(joining: false) }
                GuideDivider()
                GuideChoice(systemImage: "laptopcomputer.and.iphone", title: Text("Already using it on another device"), id: "start-join") { signIn(joining: true) }
            }
            .disabled(busy)
            Button("Use Your Own Server…") { go(.ownServer) }
                .padding(.vertical, 14)
                .accessibilityIdentifier("own-server")
        }
        .padding(.bottom, 8)
    }

    private var ownServer: some View {
        VStack(alignment: .leading, spacing: 14) {
            Button {
                go(.welcome, back: true)
            } label: {
                Image(systemName: "chevron.left").font(.headline).frame(width: 30, height: 30)
            }
            .buttonStyle(.glass).buttonBorderShape(.circle)
            .accessibilityLabel(Text("Back"))
            .padding(.top, 8)
            Spacer().frame(height: 30)
            Text(claiming ? "Set up your own server" : "Join your own server").font(.title.bold())
            Text(claiming ? "Type your server’s address and the claim code it printed when it started. This iPhone becomes its owner." : "Type the connection address one of your trusted devices shows (on a Mac: Settings › Account and Devices).")
                .foregroundStyle(.secondary)
            TextField(claiming ? "relay.example.com" : "relay.example.com/o_…", text: $address)
                .font(.body.monospaced()).textInputAutocapitalization(.never).autocorrectionDisabled().keyboardType(.URL)
                .padding(14).background(.fill.tertiary, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                .focused($typing)
                .accessibilityIdentifier("connection-address")
            if claiming {
                TextField("Claim code", text: $claim)
                    .font(.body.monospaced()).textInputAutocapitalization(.characters).autocorrectionDisabled()
                    .padding(14).background(.fill.tertiary, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                    .accessibilityIdentifier("claim-code")
            }
            Button(claiming ? "Join a server you already use instead" : "A new server? Enter its claim code…") {
                claiming.toggle()
                error = nil
            }
            .font(.footnote)
            .accessibilityIdentifier("claim-toggle")
            if let error { Text(error).font(.footnote).foregroundStyle(.red) }
            Spacer()
            Button {
                typing = false
                if claiming { return claimServer() }
                guard let a = ConnectAddress(address) else { return error = String(localized: "That isn’t a connection address. It looks like relay.example.com/o_…") }
                server = URL(string: a.relay)?.host ?? a.relay
                go(.approve(owner: a.owner, relay: a.relay))
            } label: {
                Text("Continue").frame(maxWidth: .infinity)
            }
            .buttonStyle(.glassProminent).controlSize(.large)
            .disabled(address.trimmingCharacters(in: .whitespaces).isEmpty || (claiming && claim.trimmingCharacters(in: .whitespaces).isEmpty) || busy)
            .accessibilityIdentifier("continue")
        }
        .padding(.horizontal, 28)
        .padding(.bottom, 20)
    }

    private func inUse(owner: String, relay: String) -> some View {
        GuidePage(centered: true, icon: AnyView(GuideTile(systemImage: "laptopcomputer.and.iphone", size: 64)), title: Text("This account is already in use")) {
            EmptyView()
        } footer: {
            GuideButton(title: Text("Approve on Your Other Device"), id: "approve-elsewhere") { go(.approve(owner: owner, relay: relay)) }
            GuideLink(title: Text("Use Another Account"), id: "another-account") { useAnotherAccount() }
        }
    }

    private var newAccount: some View {
        GuidePage(centered: true, icon: AnyView(GuideTile(systemImage: "sparkle", size: 64)), title: Text("This is a new account")) {
            EmptyView()
        } footer: {
            GuideButton(title: Text("Start with This iPhone"), id: "start-here") { go(.key) }
            GuideLink(title: Text("Use Another Account"), id: "another-account") { useAnotherAccount() }
        }
    }

    private var keyPage: some View {
        GuidePage(part: .account, back: { go(.welcome, back: true) }, title: Text("Create your management key")) {
            GuideCard { KeyDiagram(server: server).padding(.horizontal, 8).padding(.top, 10).padding(.bottom, 6) }
            GuideCard {
                keyRow("lock", Text("Only on this iPhone"), Text("In its Secure Enclave; it can’t be exported"))
                GuideDivider()
                keyRow("faceid", Text("Everything that joins needs you"), Text("New computers, agents and devices need Face ID"))
                GuideDivider()
                keyRow("server.rack", Text("The server can’t approve"), Text("\(server) only relays; it can’t approve for you"))
            }
        } footer: {
            if let error { Text(error).font(.footnote).foregroundStyle(.red).multilineTextAlignment(.center) }
            GuideButton(title: Text("Create with Face ID"), systemImage: "faceid", id: "create-key") { setUp() }
                .disabled(busy)
        }
    }

    private func keyRow(_ symbol: String, _ title: Text, _ detail: Text) -> some View {
        HStack(spacing: 12) {
            GuideTile(systemImage: symbol)
            VStack(alignment: .leading, spacing: 2) {
                title.font(.body.weight(.semibold))
                detail.font(.subheadline).foregroundStyle(.secondary)
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 16).padding(.vertical, 10)
    }

    private func approve(owner: String, relay: String) -> some View {
        GuidePage(back: { cancelJoin() }, icon: AnyView(GuideTile(systemImage: "laptopcomputer.and.iphone", size: 64)), title: Text("Approve on your other device")) {
            if let request {
                VStack(spacing: 4) {
                    Text("Verification code").font(.subheadline).foregroundStyle(.secondary)
                    Text(request.verification).font(.system(size: 30, weight: .semibold, design: .monospaced)).tracking(2)
                        .lineLimit(1).minimumScaleFactor(0.6)
                }
                .frame(maxWidth: .infinity).padding(.vertical, 22)
                .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
                .padding(.horizontal, 16)
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("verification-code")
            }
            if let error { Text(error).font(.footnote).foregroundStyle(.red).padding(.horizontal, 20) }
        } footer: {
            if request != nil { GuideWaiting(text: Text("Waiting for approval…")) }
            if error != nil {
                GuideButton(title: Text("Try Again"), id: "join") { join(owner: owner, relay: relay) }
            }
            GuideLink(title: Text("No other device?"), id: "no-other-device") { lost = true }
        }
        .task { if request == nil && !busy { join(owner: owner, relay: relay) } }
        .alert("Can’t join", isPresented: $lost) {
            Button("OK", role: .cancel) {}
        } message: {
            Text("You can only set up again on this iPhone; your machines and agents then need pairing again.")
        }
    }

    private func digitsPage(code: String, by: String) -> some View {
        GuidePage(back: { cancelJoin() }, title: by.isEmpty ? Text("Type the digits on your other device") : Text("Type the digits on \(by)")) {
            HStack(spacing: 14) {
                ForEach(0 ..< 2, id: \.self) { i in
                    let d = Array(digits)
                    Text(i < d.count ? String(d[i]) : "")
                        .font(.system(size: 48, weight: .semibold, design: .monospaced))
                        .frame(width: 76, height: 94)
                        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 22, style: .continuous))
                        .overlay(RoundedRectangle(cornerRadius: 22, style: .continuous).strokeBorder(i == d.count && typing ? Color.accentColor : .clear, lineWidth: 2))
                }
            }
            .frame(maxWidth: .infinity)
            .contentShape(.rect)
            .onTapGesture { typing = true }
            .overlay {
                TextField(String(""), text: $digits)
                    .keyboardType(.numberPad).focused($typing).opacity(0.02)
                    .onChange(of: digits) { _, d in
                        digits = String(d.filter(\.isNumber).prefix(2))
                        if digits.count == 2 { confirm(code: code) }
                    }
                    .accessibilityIdentifier("digits")
            }
            .onAppear { typing = true }
            if let error { Text(error).font(.footnote).foregroundStyle(.red).frame(maxWidth: .infinity).multilineTextAlignment(.center) }
            if busy { MovingMark(size: 22, motion: .seq).frame(maxWidth: .infinity) }
        } footer: {
            EmptyView()
        }
    }

    private var joinedPage: some View {
        GuidePage(centered: true, icon: AnyView(Image(systemName: "checkmark.circle.fill").font(.system(size: 64)).foregroundStyle(.green)), title: Text("Joined")) {
            EmptyView()
        } footer: {
            EmptyView()
        }
        .task {
            try? await Task.sleep(for: .seconds(1.2))
            go(.notifications(guided: false))
        }
    }

    private func notificationsPage(guided: Bool) -> some View {
        GuidePage(part: guided ? .account : nil, title: Text("Turn on notifications")) {
            LockScreenPreview()
            Label("Notifications are end-to-end encrypted; the server can’t read them", systemImage: "lock")
                .font(.footnote).foregroundStyle(.secondary).frame(maxWidth: .infinity)
        } footer: {
            GuideButton(title: Text("Turn On Notifications"), id: "notifications-on") {
                Task {
                    _ = try? await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge, .timeSensitive])
                    await finish()
                }
            }
            GuideLink(title: Text("Not Now"), id: "notifications-later") { Task { await finish() } }
        }
    }

    // MARK: Doing it

    private func finish() async {
        await model.ready()
    }

    /// The service's sign-in page in a browser sheet; it comes back with a
    /// link code (no owner yet: set one up) or the owner to join.
    private func signIn(joining: Bool) {
        self.joining = joining
        error = nil
        busy = true
        Task {
            defer { busy = false }
            let verifier = Data((0 ..< 32).map { _ in UInt8.random(in: 0 ... 255) }).base64URL
            let challenge = Data(SHA256.hash(data: Data(verifier.utf8))).base64URL
            var c = URLComponents(url: officialCenter.appendingPathComponent("app-login"), resolvingAgainstBaseURL: false)!
            c.queryItems = [URLQueryItem(name: "challenge", value: challenge)]
            do {
                let back = try await webAuth.authenticate(using: c.url!, callbackURLScheme: "hermesh", preferredBrowserSession: anotherAccount ? .ephemeral : nil)
                await follow(back, verifier: verifier)
            } catch let e as ASWebAuthenticationSessionError where e.code == .canceledLogin {
            } catch {
                self.error = error.localizedDescription
            }
        }
    }

    private func useAnotherAccount() {
        anotherAccount = true
        pending = nil
        go(.welcome, back: true)
    }

    private func handle(_ url: URL) {
        model.onboardingLink = nil
        Task { await follow(url, verifier: nil) }
    }

    /// Where signing in leads: the card chosen and the account agree, or
    /// the page that says they don't.
    private func follow(_ url: URL, verifier: String?) async {
        guard let link = AppLink(url) else { return }
        do {
            let relay = try await connectURL(officialCenter)
            server = officialCenter.host ?? "hermesh.io"
            switch link {
            case .join(let owner):
                go(joining || verifier == nil ? .approve(owner: owner, relay: relay) : .inUse(owner: owner, relay: relay))
            case .link(let code):
                guard let verifier else { return }
                var r = Hermesh_Local_V1_SetupRequest()
                r.relayURL = relay
                r.claimCode = code
                r.claimVerifier = verifier
                pending = r
                go(joining ? .newAccount : .key)
            }
        } catch {
            self.error = (error as? APIError)?.message ?? error.localizedDescription
            go(.welcome, back: true)
        }
    }

    /// Your own server, new: this iPhone claims it with the code it printed,
    /// once its key is made.
    private func claimServer() {
        var t = address.trimmingCharacters(in: .whitespacesAndNewlines)
        if !t.contains("://") { t = "https://" + t }
        guard let center = URL(string: t), center.host != nil else { return error = String(localized: "That isn’t a server address. It looks like relay.example.com") }
        error = nil
        busy = true
        Task {
            defer { busy = false }
            do {
                var r = Hermesh_Local_V1_SetupRequest()
                r.relayURL = try await connectURL(center)
                r.claimCode = claim.trimmingCharacters(in: .whitespacesAndNewlines)
                pending = r
                server = center.host ?? t
                go(.key)
            } catch {
                self.error = (error as? APIError)?.message ?? error.localizedDescription
            }
        }
    }

    /// The management key: the signer makes it (Face ID) as the owner is set up.
    private func setUp() {
        guard var r = pending, let api = model.api else { return }
        r.rootLabel = UIDevice.current.name
        r.rootPlatform = "ios"
        error = nil
        busy = true
        Task {
            defer { busy = false }
            do {
                model.signer.expect(.firstSetup)
                _ = try await api.setup(r)
                guide = Guide.on
                go(.notifications(guided: true))
            } catch {
                self.error = (error as? APIError)?.message ?? error.localizedDescription
            }
        }
    }

    private func join(owner: String, relay: String) {
        error = nil
        busy = true
        waitTask = Task {
            defer { busy = false }
            guard let api = model.api else { return }
            do {
                var r = Hermesh_Local_V1_RequestJoinRequest()
                r.relayURL = relay
                r.ownerID = owner
                r.rootLabel = UIDevice.current.name
                r.rootPlatform = "ios"
                model.signer.expect(.firstSetup)
                let res = try await api.requestJoin(r)
                request = (res.code, res.verificationCode)
                while !Task.isCancelled {
                    let w = try await api.waitJoin(res.code)
                    if w.done && !w.matchRequired {
                        try await api.confirmJoin(res.code, accept: true)
                        go(.joined)
                        return
                    }
                    if w.done {
                        go(.digits(code: res.code, by: w.approvedBy))
                        return
                    }
                    if w.expired { throw APIError(code: "expired", message: String(localized: "The request expired. Start again.")) }
                }
            } catch {
                if Task.isCancelled { return }
                request = nil
                self.error = (error as? APIError)?.message ?? error.localizedDescription
            }
        }
    }

    private func cancelJoin() {
        waitTask?.cancel()
        if let code = request?.code { Task { try? await model.api?.confirmJoin(code, accept: false) } }
        request = nil
        digits = ""
        go(.welcome, back: true)
    }

    private func confirm(code: String) {
        busy = true
        error = nil
        Task {
            defer { busy = false }
            do {
                try await model.api?.confirmJoin(code, accept: true, match: digits)
                go(.joined)
            } catch let e as APIError where e.code == "invalid_argument" {
                error = String(localized: "Those aren’t the digits. Check the other device and try again.")
                digits = ""
            } catch {
                self.error = (error as? APIError)?.message ?? error.localizedDescription
            }
        }
    }
}

// MARK: Links and addresses

/// hermesh://link?code=… (signed in; no owner yet) or hermesh://join?owner=… (join the account's owner).
enum AppLink {
    case link(String)
    case join(String)

    init?(_ url: URL) {
        guard url.scheme == "hermesh", let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems else { return nil }
        switch url.host {
        case "link": guard let c = items.first(where: { $0.name == "code" })?.value, !c.isEmpty else { return nil }; self = .link(c)
        case "join": guard let o = items.first(where: { $0.name == "owner" })?.value, !o.isEmpty else { return nil }; self = .join(o)
        default: return nil
        }
    }
}

/// A pairing code to look up, from a link or a scan: hermesh://pair?code=…,
/// a center's https://host/p/CODE, or the bare code as a machine shows it.
enum PairingLink {
    static func code(_ url: URL) -> String? {
        if url.scheme == "hermesh", url.host == "pair" {
            return URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems?.first { $0.name == "code" }?.value.flatMap(bare)
        }
        guard ["https", "http"].contains(url.scheme ?? "") else { return nil }
        let parts = url.path.split(separator: "/")
        guard parts.count == 2, parts[0] == "p" else { return nil }
        return bare(String(parts[1]))
    }

    static func code(scanned s: String) -> String? {
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        if let u = URL(string: t), u.scheme != nil { return code(u) }
        return bare(t)
    }

    private static func bare(_ s: String) -> String? {
        let c = s.uppercased()
        return c.wholeMatch(of: /[0-9A-Z]{4}-[0-9A-Z]{4}-[0-9A-Z]{4}/) != nil ? c : nil
    }
}

/// A connection address, "relay.example.com/o_…": where devices connect and the owner.
struct ConnectAddress {
    let relay: String
    let owner: String

    init?(_ s: String) {
        var t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        if t.isEmpty { return nil }
        if !t.contains("://") { t = "https://" + t }
        guard let u = URL(string: t), let scheme = u.scheme, ["https", "http"].contains(scheme), let host = u.host else { return nil }
        let parts = u.path.split(separator: "/").map(String.init)
        guard let owner = parts.last, owner.hasPrefix("o_"), owner.count == 26 else { return nil }
        let prefix = parts.dropLast().isEmpty ? "" : "/" + parts.dropLast().joined(separator: "/")
        let port = u.port.map { ":\($0)" } ?? ""
        relay = "\(scheme)://\(host)\(port)\(prefix)"
        self.owner = owner
    }
}

/// Where a center's devices connect: its .well-known/hermesh.json, else itself.
func connectURL(_ center: URL) async throws -> String {
    let base = center.absoluteString.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
    let (data, res) = try await URLSession.shared.data(from: center.appendingPathComponent(".well-known/hermesh.json"))
    let status = (res as? HTTPURLResponse)?.statusCode ?? 0
    if status == 404 { return base }
    guard status == 200 else { throw APIError(code: "unavailable", message: "\(center.host ?? base): HTTP \(status)") }
    let s = (try? JSONSerialization.jsonObject(with: data) as? [String: Any])?["connect_url"] as? String ?? ""
    guard !s.isEmpty else { return base }
    guard let u = URL(string: s), let scheme = u.scheme, scheme == "https" || (scheme == "http" && base.hasPrefix("http://")), let host = u.host,
          u.query == nil, u.fragment == nil, u.path.replacingOccurrences(of: "/", with: "").isEmpty
    else { throw APIError(code: "invalid", message: "The service gave an invalid connect URL: \(s)") }
    return "\(scheme)://\(host)\(u.port.map { ":\($0)" } ?? "")"
}

extension Data {
    var base64URL: String {
        base64EncodedString().replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "")
    }
}

/// Once, after this iPhone erased everything (or was erased from another
/// device): what was done, and who erases the next time it connects.
struct ErasedNote: View {
    @Environment(AppModel.self) private var model
    @AppStorage("erasedSeen") private var seen = ""

    var body: some View {
        let e = model.status.erased
        let id = e.eraseID.isEmpty ? String(e.at.seconds) : e.eraseID
        if model.status.hasErased && seen != id {
            HStack(alignment: .top, spacing: 10) {
                Image(systemName: e.by.isEmpty ? "checkmark.circle.fill" : "info.circle.fill").foregroundStyle(.secondary)
                VStack(alignment: .leading, spacing: 3) {
                    Text(e.by.isEmpty ? String(localized: "Hermesh erased everything") : String(localized: "You erased everything in Hermesh on “\(e.by)”"))
                        .font(.subheadline.weight(.semibold))
                    if !e.by.isEmpty { Text("Hermesh on this iPhone is erased too.") }
                    if !e.pending.isEmpty { Text("\(listText(e.pending)) were offline: reset or pair each again when you next use it.") }
                }
                .font(.footnote).foregroundStyle(.secondary)
                Spacer(minLength: 0)
                Button { seen = id } label: { Image(systemName: "xmark").font(.footnote.weight(.semibold)) }
                    .buttonStyle(.plain).foregroundStyle(.tertiary)
                    .accessibilityLabel(Text("Close"))
            }
            .padding(12)
            .background(.fill.tertiary, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("erased-note")
        }
    }
}
