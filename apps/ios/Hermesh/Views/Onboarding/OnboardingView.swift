import AuthenticationServices
import CryptoKit
import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI
import UserNotifications

/// The official service the app signs in to.
let officialCenter = URL(string: "https://hermesh.io")!

/// Joining an owner, or setting one up: sign in (or type your own server's
/// connection address), create the owner key in the Secure Enclave, wait for
/// another trusted device to approve, type its two digits, allow
/// notifications.
struct OnboardingView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.webAuthenticationSession) private var webAuth

    enum Step: Equatable {
        case welcome
        case ownServer
        /// Ready to join this owner, through this relay.
        case join(owner: String, relay: String)
        case waiting(code: String, verification: String)
        case digits(code: String, by: String)
        case setup
        case notifications
    }

    @State private var step: Step = .welcome
    @State private var address = ""
    @State private var claim = ""
    @State private var claiming = false
    @State private var digits = ""
    @State private var busy = false
    @State private var error: String?
    @State private var waitTask: Task<Void, Never>?
    @FocusState private var typing: Bool

    var body: some View {
        VStack(spacing: 0) {
            switch step {
            case .welcome: welcome
            case .ownServer: ownServer
            case .join(let owner, let relay): joinIntro(owner: owner, relay: relay)
            case .waiting(let code, let verification): waiting(code: code, verification: verification)
            case .digits(let code, let by): digitsStep(code: code, by: by)
            case .setup: VStack(spacing: 16) { MovingMark(size: 72); Text("Setting up…").foregroundStyle(.secondary) }.frame(maxHeight: .infinity)
            case .notifications: notificationsStep
            }
        }
        .padding(.horizontal, 28)
        .padding(.bottom, 20)
        .animation(.smooth, value: step)
        .onChange(of: model.onboardingLink) { _, url in if let url { handle(url) } }
    }

    // MARK: Steps

    private var welcome: some View {
        VStack(spacing: 14) {
            ErasedNote()
            Spacer()
            Mark(size: 96)
            Text("Hermesh").font(.largeTitle.bold())
            Text("Keep an eye on your agents from your phone: answer their questions, approve new connections, and stop them when needed.")
                .font(.body).foregroundStyle(.secondary).multilineTextAlignment(.center)
            Spacer()
            if let error { Text(error).font(.footnote).foregroundStyle(.red).multilineTextAlignment(.center) }
            Button {
                signIn()
            } label: {
                Text("Sign In").frame(maxWidth: .infinity)
            }
            .buttonStyle(.glassProminent).controlSize(.large).disabled(busy)
            .accessibilityIdentifier("sign-in")
            Button("Use Your Own Server…") { step = .ownServer }
                .padding(.top, 4)
                .accessibilityIdentifier("own-server")
            Text("Signing in the first time creates your account.").font(.footnote).foregroundStyle(.secondary)
        }
    }

    private var ownServer: some View {
        VStack(alignment: .leading, spacing: 14) {
            Button {
                step = .welcome
                error = nil
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
                error = nil
                step = .join(owner: a.owner, relay: a.relay)
            } label: {
                Text("Continue").frame(maxWidth: .infinity)
            }
            .buttonStyle(.glassProminent).controlSize(.large)
            .disabled(address.trimmingCharacters(in: .whitespaces).isEmpty || (claiming && claim.trimmingCharacters(in: .whitespaces).isEmpty) || busy)
            .accessibilityIdentifier("continue")
        }
    }

    private func joinIntro(owner: String, relay: String) -> some View {
        VStack(spacing: 14) {
            Spacer()
            Image(systemName: "iphone").font(.system(size: 52)).foregroundStyle(.secondary)
            Text("Add this iPhone to your account").font(.title.bold()).multilineTextAlignment(.center)
            Text("This iPhone creates an owner key in its Secure Enclave, then one of your trusted devices approves it.")
                .foregroundStyle(.secondary).multilineTextAlignment(.center)
            Spacer()
            Text("This iPhone doesn’t run tasks.")
                .font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center)
            if let error { Text(error).font(.footnote).foregroundStyle(.red).multilineTextAlignment(.center) }
            Button {
                join(owner: owner, relay: relay)
            } label: {
                Label("Continue", systemImage: "faceid").frame(maxWidth: .infinity)
            }
            .buttonStyle(.glassProminent).controlSize(.large).disabled(busy)
            .accessibilityIdentifier("join")
            Button("Cancel") { step = .welcome; error = nil }
        }
    }

    private func waiting(code: String, verification: String) -> some View {
        VStack(spacing: 16) {
            Spacer()
            WaitingMark(size: 88)
            Text("Approve on your other device").font(.title2.bold()).multilineTextAlignment(.center)
            Text("A request to join appears on your trusted devices. Approve it if the code is the same.")
                .foregroundStyle(.secondary).multilineTextAlignment(.center)
            CodeTiles(code: verification, caption: "Verification code").padding(.top, 8)
            Text("Waiting for approval…").foregroundStyle(.secondary).padding(.top, 8)
            Spacer()
            if let error { Text(error).font(.footnote).foregroundStyle(.red).multilineTextAlignment(.center) }
            Button("Cancel") {
                waitTask?.cancel()
                Task { try? await model.api?.confirmJoin(code, accept: false) }
                step = .welcome
            }
        }
    }

    private func digitsStep(code: String, by: String) -> some View {
        VStack(spacing: 16) {
            Spacer().frame(height: 40)
            Text(by.isEmpty ? String(localized: "Type the two digits your other device shows") : String(localized: "Type the two digits “\(by)” shows"))
                .font(.title2.bold()).multilineTextAlignment(.center)
            Text("This proves the approval came from your own device.").foregroundStyle(.secondary).multilineTextAlignment(.center)
            TextField(String("– –"), text: $digits)
                .keyboardType(.numberPad).font(.system(size: 44, weight: .semibold, design: .monospaced)).multilineTextAlignment(.center)
                .frame(width: 160, height: 88).background(.fill.tertiary, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
                .onChange(of: digits) { _, d in
                    digits = String(d.filter(\.isNumber).prefix(2))
                    if digits.count == 2 { confirm(code: code) }
                }
                .focused($typing)
                .onAppear { typing = true }
                .accessibilityIdentifier("digits")
            if let error { Text(error).font(.footnote).foregroundStyle(.red).multilineTextAlignment(.center) }
            if busy { MovingMark(size: 22, motion: .seq) }
            Spacer()
            Button("Cancel") {
                Task { try? await model.api?.confirmJoin(code, accept: false) }
                step = .welcome
            }
        }
    }

    private var notificationsStep: some View {
        VStack(spacing: 14) {
            Spacer()
            Image(systemName: "checkmark.circle.fill").font(.system(size: 64)).foregroundStyle(.green)
            Text("This iPhone is one of your trusted devices").font(.title.bold()).multilineTextAlignment(.center)
            Text("Requests to connect and your agents’ questions come here. Without notifications you’ll see them only while Hermesh is open.").foregroundStyle(.secondary).multilineTextAlignment(.center)
            Spacer()
            Button {
                Task {
                    _ = try? await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge, .timeSensitive])
                    await finish()
                }
            } label: {
                Text("Continue").frame(maxWidth: .infinity)
            }
            .buttonStyle(.glassProminent).controlSize(.large)
            .accessibilityIdentifier("finish")
        }
    }

    // MARK: Doing it

    private func finish() async {
        await model.ready()
    }

    /// The service's sign-in page in a browser sheet; it comes back with a
    /// link code (no owner yet: set one up) or the owner to join.
    private func signIn() {
        error = nil
        busy = true
        Task {
            defer { busy = false }
            let verifier = Data((0 ..< 32).map { _ in UInt8.random(in: 0 ... 255) }).base64URL
            let challenge = Data(SHA256.hash(data: Data(verifier.utf8))).base64URL
            var c = URLComponents(url: officialCenter.appendingPathComponent("app-login"), resolvingAgainstBaseURL: false)!
            c.queryItems = [URLQueryItem(name: "challenge", value: challenge)]
            do {
                let back = try await webAuth.authenticate(using: c.url!, callbackURLScheme: "hermesh")
                await follow(back, verifier: verifier)
            } catch let e as ASWebAuthenticationSessionError where e.code == .canceledLogin {
            } catch {
                self.error = error.localizedDescription
            }
        }
    }

    private func handle(_ url: URL) {
        model.onboardingLink = nil
        Task { await follow(url, verifier: nil) }
    }

    private func follow(_ url: URL, verifier: String?) async {
        guard let link = AppLink(url) else { return }
        do {
            let relay = try await connectURL(officialCenter)
            switch link {
            case .join(let owner):
                step = .join(owner: owner, relay: relay)
            case .link(let code):
                guard let verifier else { return }
                step = .setup
                var r = Hermesh_Local_V1_SetupRequest()
                r.relayURL = relay
                r.claimCode = code
                r.claimVerifier = verifier
                r.rootLabel = UIDevice.current.name
                r.rootPlatform = "ios"
                model.signer.expect(.firstSetup)
                _ = try await model.api?.setup(r)
                step = .notifications
            }
        } catch {
            self.error = (error as? APIError)?.message ?? error.localizedDescription
            step = .welcome
        }
    }

    /// Your own server, new: this iPhone claims it with the code it printed.
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
                r.rootLabel = UIDevice.current.name
                r.rootPlatform = "ios"
                step = .setup
                model.signer.expect(.firstSetup)
                _ = try await model.api?.setup(r)
                step = .notifications
            } catch {
                self.error = (error as? APIError)?.message ?? error.localizedDescription
                step = .ownServer
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
                step = .waiting(code: res.code, verification: res.verificationCode)
                while !Task.isCancelled {
                    let w = try await api.waitJoin(res.code)
                    if w.done && !w.matchRequired {
                        try await api.confirmJoin(res.code, accept: true)
                        step = .notifications
                        return
                    }
                    if w.done {
                        step = .digits(code: res.code, by: w.approvedBy)
                        return
                    }
                    if w.expired { throw APIError(code: "expired", message: String(localized: "The request expired. Start again.")) }
                }
            } catch {
                if Task.isCancelled { return }
                self.error = (error as? APIError)?.message ?? error.localizedDescription
                step = .join(owner: owner, relay: relay)
            }
        }
    }

    private func confirm(code: String) {
        busy = true
        error = nil
        Task {
            defer { busy = false }
            do {
                try await model.api?.confirmJoin(code, accept: true, match: digits)
                step = .notifications
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
