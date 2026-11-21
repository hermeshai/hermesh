import HermeshHer
import HermeshKit
import SwiftUI

/// A secure card under Her's words (docs/her-secure-cards.md §4): a lock,
/// what it asks for ("MCP Connection") and the machine it is for, drawn by
/// the app alone, as a message's words can't be. A `connect` card asks to
/// add a connector (or sign in to one again): what the owner types goes
/// into the answer and leaves the page as it is sent; a sign-in then runs
/// on this iPhone. Once it ended, one line stays.
struct HerSecureCard: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var scenePhase
    let chat: HerChat
    let card: HerCard?
    let machine: HerMachine
    @State private var values: [String: String] = [:]
    @State private var revealed: Set<String> = []
    @State private var sending = false

    var body: some View {
        if let card, let session = chat.session, card.connect != nil || card.siteLogin != nil {
            let here = chat.answered[card.id] != nil
            let shown = herCardShown(herCardNow(card, session, answered: chat.answered[card.id], now: model.now), here: here)
            Group {
                if let connect = card.connect {
                    let name = herConnectorName(connect.name)
                    if case .ended(let outcome, let tools, _) = shown {
                        HStack(spacing: 10) {
                            endedConnect(outcome, tools: tools, name: name, rotated: connect.isKeyRotate)
                            // Connected: its page is a tap away.
                            if outcome == "done" {
                                Button("Manage") { model.path.append(.herConnector(connect.name)) }
                                    .font(.footnote).buttonStyle(.bordered).controlSize(.small)
                                    .accessibilityIdentifier("her-card-manage")
                            }
                        }
                    } else {
                        connectForm(card, connect, shown: shown, name: name)
                    }
                } else if let site = card.siteLogin {
                    if case .ended(let outcome, _, let note) = shown {
                        endedSiteLogin(outcome, site: site, note: note)
                    } else if site.isChromeTakeover {
                        chromeTakeoverCard(card, site, shown: shown)
                    } else {
                        siteLoginForm(card, site, shown: shown)
                    }
                }
            }
            // her waits for the owner to sign in: the browser opens here, once for each answer.
            .task(id: shown == .signIn) {
                if card.connect != nil, shown == .signIn, !chat.signInStarted.contains(card.id) {
                    chat.signInStarted.insert(card.id)
                    chat.signIn(card)
                }
            }
            // Nothing typed stays once the form is gone, disconnected, or app leaves foreground.
            .onChange(of: shown) { if case .form = shown {} else { values = [:]; revealed = [] } }
            .onChange(of: chat.convUp) { if !chat.convUp { values = [:]; revealed = [] } }
            .onChange(of: scenePhase) { if scenePhase != .active { values = [:]; revealed = [] } }
        } else {
            Label("Update Hermesh to answer this request", systemImage: "lock").font(.footnote).foregroundStyle(.secondary)
        }
    }

    private func endedConnect(_ outcome: String, tools: Int?, name: String, rotated: Bool) -> some View {
        let text: String = switch outcome {
        case "done":
            if rotated {
                tools.map { String(localized: "\(name) key updated · \($0) tools") } ?? String(localized: "\(name) key updated")
            } else {
                tools.map { String(localized: "\(name) connected · \($0) tools") } ?? String(localized: "\(name) connected")
            }
        case "failed": String(localized: "Couldn’t connect to \(name)")
        case "declined": String(localized: "Declined")
        case "replaced": String(localized: "Replaced by a newer request")
        case "elsewhere": String(localized: "Answered on another device")
        default: String(localized: "Expired")
        }
        return Label(text, systemImage: outcome == "done" ? "checkmark" : "lock")
            .font(.footnote).foregroundStyle(.secondary)
            .accessibilityIdentifier("her-card-ended")
    }

    private func endedSiteLogin(_ outcome: String, site: HerCard.SiteLogin, note: String?) -> some View {
        let host = site.host
        let text: String = switch outcome {
        case "done":
            if site.isChromeTakeover {
                String(localized: "Signed in via Chrome on \(host)")
            } else if site.isAppFill {
                site.isVerifyOnly
                    ? String(localized: "Verification submitted in \(host)")
                    : String(localized: "Submitted securely in \(host)")
            } else if site.isVerifyOnly {
                if let browser = site.browser, !browser.isEmpty {
                    String(localized: "Verification submitted on \(host) (\(browser))")
                } else {
                    String(localized: "Verification submitted on \(host)")
                }
            } else if let browser = site.browser, !browser.isEmpty {
                String(localized: "Sign-in submitted on \(host) (\(browser))")
            } else {
                String(localized: "Sign-in submitted on \(host)")
            }
        case "failed":
            note ?? (site.isChromeTakeover
                ? String(localized: "Chrome sign-in invalidated on \(host)")
                : site.isAppFill
                    ? String(localized: "Couldn’t fill fields in \(host)")
                    : String(localized: "Couldn’t fill sign-in for \(host)"))
        case "declined":
            site.isChromeTakeover
                ? String(localized: "Cancelled — fall back to manual login")
                : site.isAppFill
                    ? String(localized: "Switched to manual entry")
                    : site.isVerifyOnly
                        ? String(localized: "Switched to manual verification")
                        : String(localized: "Switched to manual sign-in")
        case "replaced": String(localized: "Replaced by a newer request")
        case "elsewhere": String(localized: "Answered on another device")
        default: String(localized: "Expired")
        }
        return HStack(spacing: 6) {
            Image(systemName: outcome == "done" ? "checkmark.circle.fill" : "lock.fill")
                .foregroundStyle(outcome == "done" ? Color.green : Color.secondary)
            Text(text).foregroundStyle(.secondary)
        }
        .font(.footnote)
        .accessibilityIdentifier("her-card-ended")
    }

    private func connectForm(_ card: HerCard, _ connect: HerCard.Connect, shown: HerCardShown, name: String) -> some View {
        let busy = sending || shown == .form(note: nil, busy: true)
        let ready = connect.fields.allSatisfy { !(values[$0.id] ?? "").trimmingCharacters(in: .whitespaces).isEmpty }
        let titleText: String = connect.isKeyRotate
            ? String(localized: "Update key for \(name)")
            : connect.reconnect
                ? String(localized: "Her needs to sign in to \(name) again")
                : String(localized: "Her wants to connect to \(name)")
        return VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 5) {
                Image(systemName: "lock.fill")
                Text("MCP Connection · \(machine.machineName)")
                if card.askedBy == "her" {
                    Label("You didn’t request this", systemImage: "exclamationmark.triangle").foregroundStyle(.orange)
                }
            }
            .font(.caption).foregroundStyle(.secondary)
            // Who wants to connect to what, then the address, large: where what Her sends goes.
            VStack(alignment: .leading, spacing: 2) {
                Text(titleText).font(.subheadline).foregroundStyle(.secondary)
                Text(connect.host).font(.headline).textSelection(.enabled).accessibilityIdentifier("her-card-target")
                if !connect.url.isEmpty {
                    Text(connect.url).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                }
            }
            if connect.fields.isEmpty {
                Text("Once connected, Her can use its tools and sends them what they need. Only connect services you trust.").font(.subheadline).foregroundStyle(.secondary)
            } else {
                Text("Encrypted in transit to \(machine.machineName)’s credential store and never shown to the AI model. Only connect services you trust.").font(.subheadline).foregroundStyle(.secondary)
            }
            if shown == .signIn {
                HStack(spacing: 8) {
                    ProgressView()
                    Text("Waiting for you to sign in and authorize in your browser…")
                }
                .font(.subheadline).foregroundStyle(.secondary)
                HStack(spacing: 8) {
                    Button { cancel(card, signingIn: true) } label: { Text("Cancel").frame(maxWidth: .infinity) }.buttonStyle(.glass)
                    Button { chat.signIn(card) } label: { Text("Open Sign-In Page Again").frame(maxWidth: .infinity).lineLimit(1).minimumScaleFactor(0.8) }.buttonStyle(.glass)
                }
            } else {
                ForEach(connect.fields, id: \.id) { f in
                    // A secret for her alone: nothing fills, remembers or corrects it.
                    SecureField(f.label, text: Binding(get: { values[f.id] ?? "" }, set: { values[f.id] = $0 }))
                        .textContentType(.oneTimeCode).autocorrectionDisabled().textInputAutocapitalization(.never)
                        .padding(.horizontal, 12).padding(.vertical, 10)
                        .background(Palette.fill2, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                        .disabled(busy || !chat.convUp)
                }
                if !chat.convUp {
                    Text("\(machine.machineName) is offline. Credentials aren’t saved or queued offline.").font(.footnote).foregroundStyle(.secondary)
                }
                if case .form(let note?, _) = shown { Text(note).font(.footnote).foregroundStyle(.red).textSelection(.enabled) }
                HStack(spacing: 8) {
                    Button { cancel(card, signingIn: false) } label: { Text("Deny").frame(maxWidth: .infinity) }
                        .buttonStyle(.glass)
                        .accessibilityIdentifier("her-card-deny")
                    Button {
                        let typed = Dictionary(uniqueKeysWithValues: connect.fields.map { ($0.id, (values[$0.id] ?? "").trimmingCharacters(in: .whitespaces)) })
                        values = [:]
                        chat.signInStarted.remove(card.id)
                        answer(card, card.approve(typed))
                    } label: {
                        Text(busy ? String(localized: "Connecting…") : connect.isKeyRotate ? String(localized: "Update Key") : connect.reconnect ? String(localized: "Sign In Again") : String(localized: "Authorize")).frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.glassProminent)
                    .disabled(busy || !ready || !chat.convUp)
                    .accessibilityIdentifier("her-card-connect")
                }
            }
        }
        .controlSize(.large)
        .disabled(!chat.convUp)
        .padding(14)
        .herReply()
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("her-card")
        .herSide()
    }

    private func chromeTakeoverCard(_ card: HerCard, _ site: HerCard.SiteLogin, shown: HerCardShown) -> some View {
        let busy = sending || shown == .form(note: nil, busy: true)
        let tabLine = [site.tab.map { String(localized: "Tab \($0)") }, site.title, site.browser]
            .compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · ")
        return VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 5) {
                Image(systemName: "lock.fill")
                Text("Chrome Sign-In · \(machine.machineName)")
                if card.askedBy == "her" {
                    Label("You didn’t request this", systemImage: "exclamationmark.triangle").foregroundStyle(.orange)
                }
            }
            .font(.caption).foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 2) {
                Text("Confirm Chrome sign-in target").font(.subheadline).foregroundStyle(.secondary)
                Text(site.origin).font(.headline).textSelection(.enabled).accessibilityIdentifier("her-card-target")
                if !tabLine.isEmpty {
                    Text(tabLine).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                }
            }
            Text("Opens this Chrome tab on \(machine.machineName) so you can sign in directly with Chrome’s login or password manager. Model access to the page, screenshots, and debugging is paused while active. Hermesh never reads, stores, or forwards Chrome passwords or cookies, and never auto-submits.")
                .font(.subheadline).foregroundStyle(.secondary)
            if !chat.convUp {
                Text("\(machine.machineName) is offline. Secrets are not queued offline.").font(.footnote).foregroundStyle(.secondary)
            }
            if shown == .signIn {
                HStack(spacing: 8) {
                    ProgressView()
                    Text("Sign in natively in Chrome. Model access is paused.")
                }
                .font(.subheadline).foregroundStyle(.secondary)
                HStack(spacing: 8) {
                    Button { cancel(card, signingIn: false) } label: { Text("Cancel").frame(maxWidth: .infinity) }
                        .buttonStyle(.glass)
                        .accessibilityIdentifier("her-card-deny")
                    Button { answer(card, HerCard.takeoverStart) } label: { Text("Bring Tab to Front").frame(maxWidth: .infinity).lineLimit(1).minimumScaleFactor(0.8) }
                        .buttonStyle(.glass)
                    Button { answer(card, HerCard.takeoverDone) } label: { Text("I’m Signed In").frame(maxWidth: .infinity) }
                        .buttonStyle(.glassProminent)
                        .accessibilityIdentifier("her-card-done")
                }
                .disabled(busy || !chat.convUp)
            } else {
                if case .form(let note?, _) = shown { Text(note).font(.footnote).foregroundStyle(.red).textSelection(.enabled) }
                HStack(spacing: 8) {
                    Button { cancel(card, signingIn: false) } label: { Text("Cancel").frame(maxWidth: .infinity) }
                        .buttonStyle(.glass)
                        .accessibilityIdentifier("her-card-deny")
                    Button { answer(card, HerCard.takeoverStart) } label: { Text("Confirm & Open in Chrome").frame(maxWidth: .infinity).lineLimit(1).minimumScaleFactor(0.8) }
                        .buttonStyle(.glassProminent)
                        .accessibilityIdentifier("her-card-open")
                }
                .disabled(busy || !chat.convUp)
            }
        }
        .controlSize(.large)
        .disabled(!chat.convUp)
        .padding(14)
        .herReply()
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("her-card")
        .herSide()
    }

    private func siteLoginForm(_ card: HerCard, _ site: HerCard.SiteLogin, shown: HerCardShown) -> some View {
        let busy = sending || shown == .form(note: nil, busy: true)
        let ready = site.fields.allSatisfy { field in
            let raw = values[field.id] ?? ""
            return field.secret ? !raw.isEmpty : !raw.trimmingCharacters(in: .whitespaces).isEmpty
        }
        let appFill = site.isAppFill
        let verifyOnly = site.isVerifyOnly
        let isHttps = !appFill && site.origin.hasPrefix("https://")
        let targetMeta: String = {
            if appFill {
                return [site.title, machine.machineName].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · ")
            }
            let browser = (site.browser?.isEmpty == false) ? site.browser! : String(localized: "Built-in Browser")
            return "\(browser) · \(machine.machineName)"
        }()
        return VStack(alignment: .leading, spacing: 0) {
            VStack(alignment: .leading, spacing: 12) {
                HStack(spacing: 10) {
                    ZStack {
                        RoundedRectangle(cornerRadius: 10, style: .continuous)
                            .fill((isHttps || appFill) ? Color.green.opacity(0.14) : Palette.fill2)
                        Image(systemName: appFill ? "macwindow" : isHttps ? "checkmark.shield.fill" : "lock.fill")
                            .font(.system(size: 17, weight: .semibold))
                            .foregroundStyle((isHttps || appFill) ? Color.green : Color.secondary)
                    }
                    .frame(width: 36, height: 36)
                    .accessibilityHidden(true)

                    VStack(alignment: .leading, spacing: 2) {
                        HStack(spacing: 6) {
                            Text(site.host)
                                .font(.headline)
                                .lineLimit(1)
                                .truncationMode(.middle)
                                .textSelection(.enabled)
                                .accessibilityIdentifier("her-card-target")
                            if isHttps || appFill {
                                Text(appFill ? String(localized: "App") : "HTTPS")
                                    .font(.caption2.weight(.semibold))
                                    .foregroundStyle(.green)
                                    .padding(.horizontal, 6)
                                    .padding(.vertical, 2)
                                    .background(Color.green.opacity(0.14), in: Capsule())
                                    .fixedSize()
                            }
                        }
                        Text(verbatim: targetMeta)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                }

                if card.askedBy == "her" {
                    Label("You didn’t request this", systemImage: "exclamationmark.triangle.fill")
                        .font(.caption.weight(.medium))
                        .foregroundStyle(.orange)
                        .padding(.horizontal, 10)
                        .padding(.vertical, 6)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Color.orange.opacity(0.14), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                }

                VStack(spacing: 9) {
                    ForEach(site.fields, id: \.id) { f in
                        let binding = Binding(get: { values[f.id] ?? "" }, set: { values[f.id] = $0 })
                        if f.secret {
                            let isShown = revealed.contains(f.id)
                            HStack(spacing: 6) {
                                Group {
                                    if isShown {
                                        TextField(f.label, text: binding)
                                    } else {
                                        SecureField(f.label, text: binding)
                                    }
                                }
                                .textContentType(.oneTimeCode)
                                .autocorrectionDisabled()
                                .textInputAutocapitalization(.never)
                                .disabled(busy || !chat.convUp)

                                Button {
                                    if isShown {
                                        revealed.remove(f.id)
                                    } else {
                                        revealed.insert(f.id)
                                    }
                                } label: {
                                    Image(systemName: isShown ? "eye.slash" : "eye")
                                        .font(.subheadline)
                                        .foregroundStyle(.secondary)
                                        .frame(width: 28, height: 28)
                                        .contentShape(Rectangle())
                                }
                                .buttonStyle(.plain)
                                .disabled(busy || !chat.convUp)
                                .accessibilityLabel(isShown ? String(localized: "Hide password") : String(localized: "Show password"))
                            }
                            .padding(.leading, 12)
                            .padding(.trailing, 6)
                            .padding(.vertical, 7)
                            .background(Palette.fill2, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                        } else {
                            TextField(f.label, text: binding)
                                .keyboardType(f.kind == "email" ? .emailAddress : .default)
                                .autocorrectionDisabled()
                                .textInputAutocapitalization(.never)
                                .padding(.horizontal, 12)
                                .padding(.vertical, 10)
                                .background(Palette.fill2, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                                .disabled(busy || !chat.convUp)
                        }
                    }
                }

                if !chat.convUp {
                    Text("\(machine.machineName) is offline. Credentials aren’t saved or queued offline.")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
                if case .form(let note?, _) = shown {
                    Text(note).font(.footnote).foregroundStyle(.red).textSelection(.enabled)
                }

                HStack(spacing: 8) {
                    Button {
                        values = [:]
                        revealed = []
                        cancel(card, signingIn: false)
                    } label: {
                        Text(appFill ? String(localized: "Enter Manually") : verifyOnly ? String(localized: "Verify Manually") : String(localized: "Sign In Manually"))
                            .lineLimit(1)
                            .minimumScaleFactor(0.85)
                            .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.glass)
                    .accessibilityIdentifier("her-card-deny")

                    Button {
                        let typed = Dictionary(uniqueKeysWithValues: site.fields.map { f in
                            let raw = values[f.id] ?? ""
                            return (f.id, f.secret ? raw : raw.trimmingCharacters(in: .whitespaces))
                        })
                        values = [:]
                        revealed = []
                        answer(card, card.fill(typed))
                    } label: {
                        HStack(spacing: 5) {
                            Image(systemName: "lock.fill").font(.caption)
                            Text(
                                busy
                                    ? (verifyOnly ? String(localized: "Verifying…") : appFill ? String(localized: "Submitting…") : String(localized: "Signing in…"))
                                    : (verifyOnly ? String(localized: "Fill & Verify") : appFill ? String(localized: "Fill & Submit") : String(localized: "Fill & Sign In"))
                            )
                        }
                        .lineLimit(1)
                        .minimumScaleFactor(0.85)
                        .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.glassProminent)
                    .disabled(busy || !ready || !chat.convUp)
                    .accessibilityIdentifier("her-card-fill")
                }
                .controlSize(.regular)
            }
            .padding(14)

            Rectangle()
                .fill(Palette.separator)
                .frame(height: 0.5)

            HStack(spacing: 6) {
                Image(systemName: "lock.fill")
                    .font(.caption2)
                    .foregroundStyle(.green)
                Text(appFill ? String(localized: "Filled directly into app · never saved or seen by AI") : String(localized: "Filled directly · never saved or seen by AI"))
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 9)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Palette.fill)
        }
        .disabled(!chat.convUp)
        .herReply()
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("her-card")
        .herSide()
    }

    private func cancel(_ card: HerCard, signingIn: Bool) {
        values = [:]
        if signingIn { chat.stopSignIn(card) }
        answer(card, HerCard.decline)
    }

    private func answer(_ card: HerCard, _ a: JSON) {
        sending = true
        Task {
            await chat.answer(card, a)
            sending = false
        }
    }
}
