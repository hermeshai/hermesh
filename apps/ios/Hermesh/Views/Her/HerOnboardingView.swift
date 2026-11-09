import HermeshHer
import SwiftUI

/// Her's onboarding in the conversation while no model works there
/// (HerOnboarding): Her's messages, written by the app, and the owner's,
/// a quiet list of choices under Her's last one, and a masked field for a
/// key that goes to her and nowhere else. Once done, one quiet line and Her's
/// first words.
struct HerOnboardingView: View {
    let chat: HerChat
    let flow: HerOnboarding
    let machine: HerMachine
    @Environment(\.locale) private var locale

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if let done = flow.done {
                Label("Connected to \(connectedName(done))", systemImage: "checkmark")
                    .font(.footnote).foregroundStyle(.secondary).frame(maxWidth: .infinity)
                HerWords(text: String(localized: "All set. I’ll talk with you using \(chat.models.title(done.model)); you can change it in Assistant Settings. What can I do for you?"))
                    .padding(.top, 13)
            } else {
                let controls = flow.controls
                ForEach(Array(flow.said.enumerated()), id: \.offset) { i, s in
                    Group {
                        if s.byOwner {
                            HerBubble(text: text(s), images: [], files: [])
                        } else {
                            // Her's words in her bubble, an error's words with them; what can be done under it.
                            VStack(alignment: .leading, spacing: 8) {
                                VStack(alignment: .leading, spacing: 4) {
                                    Text(text(s)).lineSpacing(3)
                                    if let detail = detail(s) { Text(detail).font(.caption).foregroundStyle(.secondary).lineLimit(4).textSelection(.enabled) }
                                }
                                .herFill()
                                .padding(.horizontal, 14).padding(.vertical, 9)
                                .herReply()
                                .herSide()
                                if let c = controls[i] { control(c, under: s) }
                            }
                        }
                    }
                    .padding(.top, i == 0 ? 0 : herGap(joined: s.byOwner == flow.said[i - 1].byOwner))
                }
                if flow.step == .connecting { HerTyping().padding(.top, herGap(joined: flow.said.last?.byOwner == false)) }
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("her-onboarding")
    }

    private var language: String { locale.language.languageCode?.identifier ?? "en" }

    private func provider(_ id: String) -> String { plan(id)?.name ?? chat.models.provider(id)?.name(language: language) ?? id }

    /// A Token Plan by the name the app gives it (her's catalog names them in English only), and the line that
    /// tells it from the others.
    private func plan(_ id: String) -> (name: String, note: String)? {
        switch id {
        case "bailian-token-plan": (String(localized: "Bailian Token Plan"), String(localized: "Alibaba Cloud · multi-model"))
        case "tencent-token-plan": (String(localized: "Tencent Cloud Token Plan"), String(localized: "General and Hy share a key"))
        case "volcengine-agent-plan": (String(localized: "Volcengine Ark Agent Plan"), String(localized: "Agent Plan key only"))
        case "volcengine-coding-plan": (String(localized: "Volcengine Ark Coding Plan"), String(localized: "Coding Plan key only"))
        case "zai-coding-plan": (String(localized: "Z.ai GLM Coding Plan"), String(localized: "International account"))
        case "zhipu-coding-plan": (String(localized: "Zhipu GLM Coding Plan"), String(localized: "China account"))
        default: nil
        }
    }

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
            return String(localized: "Hi, I’m \(name). I work on \(m). Before we start, I need a model to think and answer with. Pick a way to connect one.")
        case .browser: return String(localized: "I’ve opened ChatGPT’s sign-in page. Come back here once you’ve signed in.")
        case .signedIn(let account?): return String(localized: "Connected to ChatGPT (\(account)).")
        case .signedIn(nil): return String(localized: "Connected to ChatGPT.")
        case .signInFailed: return String(localized: "I couldn’t connect to ChatGPT this time. You can try again, or pick another way.")
        case .signInCancelled: return String(localized: "Sign-in cancelled. You can try again, or pick another way.")
        case .whichWay: return String(localized: "Sure. Which way instead?")
        case .whichProvider: return String(localized: "Which provider?")
        case .whichPlan: return String(localized: "Which plan?")
        case .pasteKey(let p):
            return String(localized: "Paste your \(provider(p)) API key below. It’s kept only in \(m)’s credential store: it won’t show in our conversation, and it’s never sent to the model.")
        case .keyRejected(let p, let message):
            // Short words of the provider's go inside Her's sentence; long ones under it.
            if folds(message) { return String(localized: "\(provider(p)) didn’t accept this key. Paste it again?") }
            return String(localized: "\(provider(p)) didn’t accept this key: \(message.trimmingCharacters(in: CharacterSet(charactersIn: ".。!！ \n"))). Paste it again?")
        case .keyFailed: return String(localized: "I couldn’t save the key on \(m). Try again?")
        case .keyUnverified(let p):
            // A Token Plan's key is checked only by the first reply (her has nothing to ask before).
            if chat.models.provider(p)?.plan == true { return String(localized: "Saved. A plan’s key can only be checked on the first reply.") }
            return String(localized: "Saved. I couldn’t check the key with \(provider(p)) yet, so let’s carry on.")
        case .ownerChatGPT: return String(localized: "Sign In with ChatGPT")
        case .ownerPlan: return String(localized: "Connect a Token Plan")
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

    /// The ways, each with what it takes.
    private var ways: [HerConnectChoices.Row] {
        var rows = [HerConnectChoices.Row(id: "chatgpt", title: String(localized: "Sign In with ChatGPT"), note: String(localized: "Use your ChatGPT subscription; you sign in in your browser")) { chat.signInChatGPT() }]
        // An older her has no Token Plans.
        if !herPlanChoices(chat.models.providers).isEmpty {
            rows.append(.init(id: "plan", title: String(localized: "Connect a Token Plan"), note: String(localized: "A model plan you subscribe to, such as Bailian, Volcengine Ark or GLM")) { chat.flow?.choosePlan() })
        }
        rows.append(.init(id: "key", title: String(localized: "Enter an API Key"), note: String(localized: "A key from DeepSeek, OpenAI, Anthropic and others")) { chat.flow?.chooseKey() })
        return rows
    }

    /// The provider or Token Plan whose key is asked for: checked in its list.
    private var asked: String? { flow.step == .key || flow.step == .checking ? flow.provider : nil }

    @ViewBuilder private func control(_ c: HerOnboarding.Control, under s: HerOnboarding.Said) -> some View {
        let busy = flow.step == .checking
        switch c {
        case .ways:
            HerConnectChoices(rows: ways)
            if s == .greeting { Text("Keys and sign-ins stay on \(machine.machineName)").font(.caption).foregroundStyle(.tertiary) }
        case .browser:
            Button("Try Another Way") { chat.stopChatGPT() }.buttonStyle(HerPill())
        case .providers:
            HerConnectChoices(rows: herKeyChoices(chat.models.providers).map { p in
                .init(id: p.id, title: provider(p.id), current: p.id == asked) { chat.flow?.pickProvider(p.id) }
            })
            .disabled(busy)
            otherWay(busy: busy)
        case .plans:
            HerConnectChoices(rows: herPlanChoices(chat.models.providers).map { p in
                .init(id: p.id, title: provider(p.id), note: plan(p.id)?.note, current: p.id == asked) { chat.flow?.pickProvider(p.id) }
            })
            .disabled(busy)
            otherWay(busy: busy)
        case .key:
            if let p = flow.provider {
                HerKeyField(chat: chat, page: herKeyPage(p, plan: chat.models.provider(p)?.keyPage), name: provider(p), busy: busy).herSide()
            }
        }
    }

    /// Back to the ways, from a provider, a Token Plan or a key.
    private func otherWay(busy: Bool) -> some View {
        Button("Try Another Way") { chat.flow?.otherWay() }.font(.footnote).disabled(busy).accessibilityIdentifier("her-other-way")
    }
}

/// What to connect, as a quiet list under Her's message (drawn as Her's secure cards are): a name, a line under
/// it when the names alone don't tell the choices apart, a check on the one picked.
private struct HerConnectChoices: View {
    struct Row {
        let id: String
        let title: String
        var note: String?
        var current = false
        let pick: () -> Void
    }

    let rows: [Row]

    var body: some View {
        VStack(spacing: 0) {
            ForEach(Array(rows.enumerated()), id: \.element.id) { i, r in
                if i > 0 { Divider().padding(.leading, 16) }
                Button(action: r.pick) {
                    HStack(spacing: 12) {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(r.title).font(.body)
                            if let note = r.note { Text(note).font(.footnote).foregroundStyle(.secondary) }
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        Image(systemName: r.current ? "checkmark" : "chevron.right")
                            .font(.footnote.weight(.semibold))
                            .foregroundStyle(r.current ? AnyShapeStyle(.tint) : AnyShapeStyle(.tertiary))
                    }
                    .padding(.horizontal, 16).padding(.vertical, 12)
                    .contentShape(.rect)
                }
                .buttonStyle(HerChoiceStyle())
                .accessibilityLabel(r.title)
                .accessibilityHint(r.note ?? "")
                .accessibilityAddTraits(r.current ? .isSelected : [])
                .accessibilityIdentifier("her-choice-\(r.id)")
            }
        }
        .herReply()
        .herSide()
    }
}

/// A row of a list in Her's messages (choices, answers, files): under the finger from the moment it touches down.
struct HerChoiceStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label.foregroundStyle(.primary).background(configuration.isPressed ? Palette.fill2 : .clear)
    }
}

/// A provider's key, typed where only her gets it (auth/set), emptied as it is saved.
private struct HerKeyField: View {
    @Environment(\.openURL) private var openURL
    let chat: HerChat
    /// Where a key is made.
    let page: URL?
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
            if let page {
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
