import HermeshHer
import SwiftUI

/// Her's onboarding in the conversation while no model works there
/// (HerOnboarding): Her's messages, written by the app, and the owner's,
/// quiet answers to tap under Her's last one, and a masked field for a key
/// that goes to her and nowhere else. Once done, one quiet line and Her's
/// first words.
struct HerOnboardingView: View {
    let chat: HerChat
    let flow: HerOnboarding
    let machine: HerMachine
    @Environment(\.locale) private var locale

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            if let done = flow.done {
                Label("Connected to \(connectedName(done))", systemImage: "checkmark")
                    .font(.footnote).foregroundStyle(.secondary).frame(maxWidth: .infinity)
                HerWords(text: String(localized: "All set. I’ll talk with you using \(chat.models.title(done.model)); you can change it in Assistant Settings. What can I do for you?"))
            } else {
                let controls = flow.controls
                ForEach(Array(flow.said.enumerated()), id: \.offset) { i, s in
                    if s.byOwner {
                        HerBubble(text: text(s), images: [], files: [])
                    } else {
                        VStack(alignment: .leading, spacing: 10) {
                            Text(text(s)).frame(maxWidth: .infinity, alignment: .leading)
                            if let detail = detail(s) { Text(detail).font(.caption).foregroundStyle(.secondary).lineLimit(4).textSelection(.enabled) }
                            if let c = controls[i] { control(c, under: s) }
                        }
                    }
                }
                if flow.step == .connecting { HerTyping() }
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("her-onboarding")
    }

    private var language: String { locale.language.languageCode?.identifier ?? "en" }

    private func provider(_ id: String) -> String { chat.models.provider(id)?.name(language: language) ?? id }

    private func connectedName(_ done: HerOnboarding.Done) -> String {
        switch flow.connected {
        case .chatGPT: "ChatGPT"
        case .key(let p): provider(p)
        case nil: provider(String(done.model.prefix { $0 != "/" }))
        }
    }

    private func text(_ s: HerOnboarding.Said) -> String {
        let name = chat.bot?.name ?? "Her", m = machine.machineName
        switch s {
        case .greeting:
            return String(localized: "Hi, I’m \(name). I work on \(m). Before we start, I need a model to think and answer with. Signing in with your ChatGPT account is quickest, or you can enter an API key.")
        case .browser: return String(localized: "I’ve opened ChatGPT’s sign-in page. Come back here once you’ve signed in.")
        case .signedIn(let account?): return String(localized: "Connected to ChatGPT (\(account)).")
        case .signedIn(nil): return String(localized: "Connected to ChatGPT.")
        case .signInFailed: return String(localized: "I couldn’t connect to ChatGPT this time. You can try again, or enter an API key instead.")
        case .signInCancelled: return String(localized: "Sign-in cancelled. You can try again, or enter an API key instead.")
        case .whichProvider: return String(localized: "Which provider?")
        case .pasteKey(let p):
            return String(localized: "Paste your \(provider(p)) API key below. It’s kept only in \(m)’s credential store: it won’t show in our conversation, and it’s never sent to the model.")
        case .keyRejected(let p, let message):
            // Short words of the provider's go inside Her's sentence; long ones under it.
            if folds(message) { return String(localized: "\(provider(p)) didn’t accept this key. Paste it again?") }
            return String(localized: "\(provider(p)) didn’t accept this key: \(message.trimmingCharacters(in: CharacterSet(charactersIn: ".。!！ \n"))). Paste it again?")
        case .keyFailed: return String(localized: "I couldn’t save the key on \(m). Try again?")
        case .keyUnverified(let p): return String(localized: "Saved. I couldn’t check the key with \(provider(p)) yet, so let’s carry on.")
        case .ownerChatGPT: return String(localized: "Sign In with ChatGPT")
        case .ownerKey: return String(localized: "Enter an API Key")
        case .ownerOtherWay: return String(localized: "Try Another Way")
        case .ownerProvider(let p): return provider(p)
        }
    }

    /// An error's own words, under Her's.
    private func detail(_ s: HerOnboarding.Said) -> String? {
        switch s {
        case .signInFailed(let d), .keyFailed(let d): d.isEmpty ? nil : d
        case .keyRejected(_, let message): folds(message) ? message : nil
        default: nil
        }
    }

    private func folds(_ text: String) -> Bool { text.count > 60 || text.contains("\n") }

    @ViewBuilder private func control(_ c: HerOnboarding.Control, under s: HerOnboarding.Said) -> some View {
        switch c {
        case .ways:
            HerWrap(spacing: 8) {
                Button("Sign In with ChatGPT") { chat.signInChatGPT() }.buttonStyle(HerPill())
                Button("Enter an API Key") { chat.flow?.chooseKey() }.buttonStyle(HerPill())
            }
            if s == .greeting { Text("Keys and sign-ins stay on \(machine.machineName)").font(.caption).foregroundStyle(.tertiary) }
        case .browser:
            Button("Try Another Way") { chat.stopChatGPT() }.buttonStyle(HerPill())
        case .providers:
            HerWrap(spacing: 8) {
                ForEach(herKeyChoices(chat.models.providers), id: \.id) { p in
                    Button(p.name(language: language)) { chat.flow?.pickProvider(p.id) }.buttonStyle(HerPill())
                }
            }
            .disabled(flow.step == .checking)
        case .key:
            if let p = flow.provider { HerKeyField(chat: chat, provider: p, name: provider(p), busy: flow.step == .checking) }
        }
    }
}

/// A provider's key, typed where only her gets it (auth/set), emptied as it is saved.
private struct HerKeyField: View {
    @Environment(\.openURL) private var openURL
    let chat: HerChat
    let provider: String
    let name: String
    let busy: Bool
    @State private var key = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                SecureField("API Key", text: $key)
                    .textContentType(.oneTimeCode).autocorrectionDisabled().textInputAutocapitalization(.never)
                    .submitLabel(.done).onSubmit(save)
                    .padding(.horizontal, 12).padding(.vertical, 10)
                    .background(Palette.fill2, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                    .accessibilityLabel(Text("\(name) API key"))
                    .accessibilityIdentifier("her-key")
                Button(busy ? String(localized: "Checking…") : String(localized: "Save"), action: save)
                    .buttonStyle(.glassProminent)
                    .disabled(busy || key.trimmingCharacters(in: .whitespaces).isEmpty)
                    .accessibilityIdentifier("her-key-save")
            }
            .disabled(busy)
            if let page = herKeyPage(provider) {
                Button("Get a key from \(name)") { openURL(page) }.font(.footnote)
            }
        }
    }

    private func save() {
        let k = key.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !k.isEmpty, !busy else { return }
        key = ""
        Task { await chat.saveKey(k) }
    }
}
