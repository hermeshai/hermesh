import HermeshHer
import HermeshKit
import HermeshProto
import SwiftUI
import WebKit

// The pages under Her's Settings tab (docs/her-panel-demo.html): her model with the environment's model sign-ins and
// providers (what the console's Models page manages), a provider's models, and her environment: where she runs, the
// storage of her data there, its console, and turning her off there.

// MARK: - Memory

/// What Her remembers, for her page's Memory card.
struct HerMemoryRoute: View {
    let chat: HerChat
    let machine: HerMachine

    var body: some View {
        if let bot = chat.bot {
            HerMemoryPage(chat: chat, bot: bot.id)
        } else {
            PageLoader(text: "Reading the conversation…", bare: true)
        }
    }
}

// MARK: - Shared pieces

/// A Token Plan by the name the app gives it (her's catalog names them in English only), and the line that tells it
/// from the others.
func herPlanTitle(_ id: String) -> (name: String, note: String)? {
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

/// A provider's name in the app's language.
func herProviderTitle(_ p: HerProviderRow) -> String {
    herPlanTitle(p.id)?.name ?? p.name(language: Locale.current.language.languageCode?.identifier ?? "en")
}

/// A provider's mark: its logo where the app has one, else its first letters.
struct HerProviderTile: View {
    let id: String
    let name: String
    var size: CGFloat = 34

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: size * 0.26, style: .continuous)
        Group {
            if let logo = logo {
                Image(logo).resizable().interpolation(.high).scaledToFit().padding(size * 0.16).background(Color.white)
            } else {
                Text(String(name.prefix(1)).uppercased()).font(.system(size: size * 0.42, weight: .bold)).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, maxHeight: .infinity).background(Palette.fill2)
            }
        }
        .frame(width: size, height: size)
        .clipShape(shape)
        .overlay(shape.strokeBorder(Color.primary.opacity(0.1), lineWidth: 0.5))
        .accessibilityHidden(true)
    }

    private var logo: String? {
        let known = ["deepseek": "deepseek", "anthropic": "claude", "google": "gemini", "gemini": "gemini", "xai": "grok", "moonshot": "kimi",
                     "moonshotai": "kimi", "kimi": "kimi", "qwen": "qwen", "alibaba": "qwen", "bailian-token-plan": "qwen"]
        return known[id].map { "Logos/\($0)" }
    }
}

/// Bytes as people read them: "1.8 GB".
func herBytes(_ n: Int64) -> String { n.formatted(.byteCount(style: .file)) }

/// The calls of the console's pages, on the environment's connection.
extension HerChat {
    /// What her pages read and change, each on a request of its own (callAlone): a page asks at once, not after the
    /// environment's connection has opened through the relay, which can take long on a phone, or not come at all.
    var abilities: HerAbilityCalls { HerAbilityCalls { [weak self] m, p in
        guard let self else { throw CancellationError() }
        return try await self.callAlone(m, p)
    } }
}

// MARK: - Model

/// Her's model and how hard she thinks, then where the environment's models come from: a ChatGPT subscription, Token
/// Plans and providers' keys (the console's Models page). Each provider opens a page of its own.
struct HerModelPage: View {
    var body: some View {
        HerEnvironmentPage { chat, machine in HerModelList(chat: chat, machine: machine) }
            .navigationTitle("Model")
            .navigationBarTitleDisplayMode(.inline)
    }
}

private struct HerModelList: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let machine: HerMachine
    @State private var gpt: HerChatGPT?
    @State private var providers: HerProviders?
    @State private var models = HerModels.none
    @State private var failed: String?
    @State private var signingIn: Task<Void, Never>?
    @State private var forgetting = false
    @State private var adding: HerKeySheet.Kind?

    var body: some View {
        List {
            if let s = chat.live {
                Section {
                    Picker("Model", selection: Binding(get: { s.model }, set: { m in
                        // Her keeps the level when the new model offers it, else takes the one it starts with: said when it changes.
                        let after = chat.models.thinking(for: m, current: s.thinking)
                        if m != s.model, s.thinking != "default", after != s.thinking {
                            model.show(String(localized: "\(chat.models.title(m)) has no “\(herThinkingName(s.thinking))” level; thinking is now “\(herThinkingName(after))”"))
                        }
                        Task { await chat.op(["type": "set_model", "model": .string(m)]) }
                    })) {
                        ForEach(chat.models.menu(current: s.model), id: \.id) { m in
                            Text(chat.models.provider(m.provider).map { "\(m.name) · \($0.name)" } ?? m.name).tag(m.id)
                        }
                    }
                    .accessibilityIdentifier("her-model")
                    let levels = chat.models.thinkingLevels(s.model)
                    if !levels.isEmpty {
                        Picker("Thinking", selection: Binding(get: { s.thinking }, set: { l in
                            Task { await chat.op(["type": "set_model", "model": .string(s.model), "thinking": .string(l)]) }
                        })) {
                            ForEach(["default"] + levels, id: \.self) { l in Text(herThinkingName(l)) }
                        }
                        .accessibilityIdentifier("her-thinking")
                    }
                }
            }
            if herAnswers(machine.her, "chatgpt/status") == false {
                Section { Text(APIError.needsUpdateText).foregroundStyle(.secondary) }
            } else if let providers {
                Section {
                    chatGPTRow
                } header: {
                    Text("Use a Subscription")
                } footer: {
                    Text("Uses the account’s subscription; API keys are billed separately.")
                }
                if !providers.plans.isEmpty {
                    Section("Token Plan") {
                        ForEach(providers.addedPlans) { p in providerRow(p) }
                        if !providers.plansLeft.isEmpty {
                            Button { adding = .plan } label: {
                                HStack(spacing: 12) {
                                    Image(systemName: "plus").font(.body.weight(.medium)).frame(width: 34, height: 34).background(Palette.fill2, in: RoundedRectangle(cornerRadius: 9, style: .continuous))
                                    VStack(alignment: .leading, spacing: 2) {
                                        Text("Connect a Token Plan")
                                        Text("Connect a model plan you already have").font(.footnote).foregroundStyle(.secondary)
                                    }
                                }
                            }
                            .accessibilityIdentifier("her-plan-add")
                        }
                    }
                }
                Section("API and Custom Services") {
                    ForEach(providers.keyed) { p in providerRow(p) }
                    Button("Add an API Service") { adding = .key }
                        .disabled(providers.keyChoices.isEmpty)
                        .accessibilityIdentifier("her-provider-add")
                }
            } else if let failed {
                Section { Text(failed).foregroundStyle(.secondary) }
            } else {
                Section { HStack { Spacer(); ProgressView(); Spacer() } }
            }
        }
        .listSectionSpacing(.compact)
        .task(id: "\(herReachable(machine)) \(chat.settingsChanged)") { await load() }
        .confirmationDialog("Forget the ChatGPT account?", isPresented: $forgetting, titleVisibility: .visible) {
            Button("Forget Account", role: .destructive) { forget() }
        } message: {
            Text("Her stops using the subscription on \(machine.machineName), and its sign-in is deleted there.")
        }
        .sheet(item: $adding) { kind in
            HerKeySheet(chat: chat, machine: machine, kind: kind, choices: kind == .plan ? providers?.plansLeft ?? [] : providers?.keyChoices ?? []) {
                Task { await load() }
            }
            .environment(model)
        }
        .onDisappear { signingIn?.cancel() }
    }

    @ViewBuilder private var chatGPTRow: some View {
        let count = gpt?.provider.map { p in (models.models.filter { $0.provider == p }.count, models.models.filter { $0.provider == p && $0.shown }.count) }
        HStack(spacing: 12) {
            HerProviderTile(id: "openai", name: "ChatGPT")
            VStack(alignment: .leading, spacing: 2) {
                Text(verbatim: "ChatGPT")
                Group {
                    if signingIn != nil || gpt?.pending == true {
                        Text("Waiting for the sign-in in the browser…")
                    } else if gpt?.ready == true, let count {
                        Text("Connected · \(count.1) of \(count.0) models shown")
                    } else if gpt?.expired == true {
                        Text("The sign-in expired; sign in again").foregroundStyle(.orange)
                    } else {
                        Text("Sign in with a ChatGPT account")
                    }
                }
                .font(.footnote).foregroundStyle(.secondary)
            }
            Spacer(minLength: 8)
            if signingIn != nil {
                Button("Cancel") { signingIn?.cancel() }.buttonStyle(.bordered).controlSize(.small)
            } else if gpt?.ready == true {
                Button("Forget Account") { forgetting = true }.buttonStyle(.bordered).controlSize(.small)
                    .accessibilityIdentifier("her-chatgpt-forget")
            } else {
                Button("Sign In") { signIn() }.buttonStyle(.borderedProminent).controlSize(.small)
                    .disabled(!herReachable(machine))
                    .accessibilityIdentifier("her-chatgpt-sign-in")
            }
        }
    }

    private func providerRow(_ p: HerProviderRow) -> some View {
        NavigationLink(value: Route.herProvider(p.id)) {
            HStack(spacing: 12) {
                HerProviderTile(id: p.id, name: herProviderTitle(p))
                VStack(alignment: .leading, spacing: 2) {
                    Text(herProviderTitle(p))
                    Text(herProviderLine(p, models: models)).font(.footnote).foregroundStyle(.secondary).lineLimit(2)
                    if let e = p.listingError { Text("The last refresh failed: \(e)").font(.footnote).foregroundStyle(.orange).lineLimit(2) }
                }
            }
        }
        .accessibilityIdentifier("her-provider-\(p.id)")
    }

    private func load() async {
        guard herReachable(machine) else { return }
        do {
            let (p, m) = try await chat.abilities.providers()
            providers = p
            models = m
            gpt = herAnswers(machine.her, "chatgpt/status") == false ? nil : try? await chat.abilities.chatGPT()
            failed = nil
        } catch {
            failed = error.localizedDescription
        }
    }

    private func signIn() {
        signingIn = Task {
            let end = await chat.signInChatGPTAlone()
            signingIn = nil
            switch end {
            case .connected: model.show(String(localized: "Signed in to ChatGPT"))
            case .cancelled: break
            case .failed(let why): model.show(why.isEmpty ? String(localized: "Couldn’t sign in to ChatGPT") : why)
            }
            await load()
        }
    }

    private func forget() {
        Task {
            do { gpt = try await chat.abilities.forgetChatGPT() } catch { model.show(error.localizedDescription) }
            await load()
        }
    }
}

/// What a provider's row says: where its key comes from, how many of its models the menu shows, when its list was read.
func herProviderLine(_ p: HerProviderRow, models: HerModels) -> String {
    let source: String? = if let host = p.customHost { host } else { switch p.credential {
    case "stored": String(localized: "Credential store")
    case "env": p.credentialSource.map { String(localized: "Environment variable \($0)") }
    case "config": "models.toml"
    case "command": String(localized: "From a command")
    default: nil
    } }
    let all = models.models.filter { $0.provider == p.id }
    let shown = String(localized: "\(p.shown ?? all.filter(\.shown).count) of \(p.models ?? all.count) models shown")
    let updated: String? = p.refreshing ? String(localized: "Refreshing…")
        : p.fetchedAt.map { String(localized: "Updated \($0.formatted(.relative(presentation: .named)))") }
    return [source, shown, updated].compactMap { $0 }.joined(separator: " · ")
}

// MARK: - A provider

/// A provider's models, each in the model menu or not; refreshing its list, replacing its key and removing it.
struct HerProviderPage: View {
    let id: String

    var body: some View {
        HerEnvironmentPage { chat, machine in HerProviderList(chat: chat, machine: machine, id: id) }
            .navigationBarTitleDisplayMode(.inline)
    }
}

private struct HerProviderList: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let chat: HerChat
    let machine: HerMachine
    let id: String
    @State private var provider: HerProviderRow?
    @State private var models = HerModels.none
    @State private var query = ""
    @State private var removing = false
    @State private var replacing = false
    @State private var refreshing = false
    /// Switches turned here, shown at once until the list says so too.
    @State private var showing: [String: Bool] = [:]

    var body: some View {
        let mine = models.models.filter { $0.provider == id }
        let found = query.isEmpty ? mine : mine.filter { $0.name.localizedCaseInsensitiveContains(query) || $0.id.localizedCaseInsensitiveContains(query) }
        List {
            if let p = provider {
                Section {
                    LabeledContent("Models", value: herProviderLine(p, models: models))
                    if let e = p.listingError { Text("The last refresh failed: \(e)").foregroundStyle(.orange) }
                } footer: {
                    if p.credential == "env" {
                        Text("Set in her environment on \(machine.machineName); to remove it, delete \(p.credentialSource ?? "")")
                    } else if p.credential == "config" {
                        Text("Set in ~/.her/models.toml on \(machine.machineName); to remove it, edit that file")
                    }
                }
                Section {
                    ForEach(found, id: \.id) { m in
                        Toggle(isOn: Binding(get: { showing[m.id] ?? m.shown }, set: { on in show(m, on) })) {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(m.name)
                                if m.name != herModelName(m.id) { Text(herModelName(m.id)).font(.footnote.monospaced()).foregroundStyle(.secondary) }
                            }
                        }
                        .accessibilityIdentifier("her-model-shown-\(m.id)")
                    }
                    if found.isEmpty { Text("No models match").foregroundStyle(.secondary) }
                } footer: {
                    Text("Those turned on show in the model menu.")
                }
                Section {
                    if !p.plan || p.dynamicModels {
                        Button(refreshing || p.refreshing ? String(localized: "Refreshing…") : String(localized: "Refresh the List")) { refresh() }
                            .disabled(refreshing || p.refreshing)
                    }
                    if p.removable {
                        if p.credential == "stored" {
                            Button(p.plan ? String(localized: "Replace Key…") : String(localized: "Replace Key…")) { replacing = true }
                        }
                        Button("Remove", role: .destructive) { removing = true }
                            .accessibilityIdentifier("her-provider-remove")
                    }
                }
            } else {
                Section { HStack { Spacer(); ProgressView(); Spacer() } }
            }
        }
        .listSectionSpacing(.compact)
        .searchable(text: $query, prompt: Text("Search Models"))
        .navigationTitle(provider.map(herProviderTitle) ?? "")
        .task(id: "\(herReachable(machine)) \(chat.settingsChanged)") { await load() }
        .confirmationDialog("Remove \(provider.map(herProviderTitle) ?? "")?", isPresented: $removing, titleVisibility: .visible) {
            Button("Remove", role: .destructive) { remove() }
        } message: {
            Text(provider?.plan == true ? "The key saved in this environment is removed; your plan with the provider is not cancelled."
                : provider?.customHost != nil ? "Its models leave the menu, and its saved key goes too."
                : "Its key is deleted from the credential store on \(machine.machineName), and its models leave the menu.")
        }
        .sheet(isPresented: $replacing) {
            if let p = provider {
                HerKeySheet(chat: chat, machine: machine, kind: p.plan ? .plan : .key, choices: [p], replacing: true) { Task { await load() } }
                    .environment(model)
            }
        }
    }

    private func load() async {
        guard herReachable(machine), let (p, m) = try? await chat.abilities.providers() else { return }
        provider = p.all.first { $0.id == id }
        models = m
        showing = showing.filter { k, v in m.models.first { $0.id == k }?.shown != v }
    }

    private func show(_ m: HerModel, _ on: Bool) {
        showing[m.id] = on
        Task {
            do { try await chat.abilities.showModel(m.id, shown: on) } catch {
                showing[m.id] = nil
                model.show(error.localizedDescription)
            }
            await load()
        }
    }

    private func refresh() {
        refreshing = true
        Task {
            do { try await chat.abilities.refreshModels(id) } catch { model.show(error.localizedDescription) }
            refreshing = false
            await load()
        }
    }

    private func remove() {
        Task {
            do {
                if provider?.customHost != nil { try await chat.abilities.removeProvider(id) } else { try await chat.abilities.removeKey(id) }
                model.show(String(localized: "Removed \(provider.map(herProviderTitle) ?? id)"))
                dismiss()
            } catch { model.show(error.localizedDescription) }
        }
    }
}

// MARK: - A key

/// Connecting a provider with its key, or a Token Plan with its own: choose one, paste its key. her asks the provider
/// first, and a key it refuses is not saved. The key goes to her's credential store on the machine; nothing here keeps it.
struct HerKeySheet: View {
    enum Kind: Identifiable { case key, plan
        var id: Self { self }
    }

    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @Environment(\.openURL) private var openURL
    let chat: HerChat
    let machine: HerMachine
    let kind: Kind
    let choices: [HerProviderRow]
    var replacing = false
    let done: () -> Void
    @State private var chosen: HerProviderRow?
    @State private var key = ""
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Group {
                if let p = chosen ?? (choices.count == 1 ? choices[0] : nil) {
                    entry(p)
                } else {
                    List {
                        Section {
                            ForEach(choices) { p in
                                Button { chosen = p } label: {
                                    HStack(spacing: 12) {
                                        HerProviderTile(id: p.id, name: herProviderTitle(p))
                                        VStack(alignment: .leading, spacing: 2) {
                                            Text(herProviderTitle(p)).foregroundStyle(.primary)
                                            Text(herPlanTitle(p.id)?.note ?? "API Key").font(.footnote).foregroundStyle(.secondary)
                                        }
                                        Spacer()
                                        Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(.tertiary)
                                    }
                                }
                                .accessibilityIdentifier("her-key-choice-\(p.id)")
                            }
                        } footer: {
                            if kind == .plan { Text("Choose the service you subscribe to; its API key comes next.") }
                        }
                    }
                }
            }
            .navigationTitle(replacing ? String(localized: "Replace Key") : kind == .plan ? String(localized: "Connect a Token Plan") : String(localized: "Add an API Service"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                if chosen != nil, choices.count > 1 {
                    ToolbarItem(placement: .navigation) { Button("Back") { chosen = nil; key = ""; error = nil } }
                }
            }
        }
    }

    private func entry(_ p: HerProviderRow) -> some View {
        Form {
            Section {
                HStack(spacing: 12) {
                    HerProviderTile(id: p.id, name: herProviderTitle(p), size: 40)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(herProviderTitle(p)).font(.headline)
                        Text(herPlanTitle(p.id)?.note ?? String(localized: "Paste this provider’s API key")).font(.footnote).foregroundStyle(.secondary)
                    }
                }
            }
            Section {
                SecureField(p.plan ? String(localized: "Paste this plan’s API key") : "API Key", text: $key)
                    .textContentType(.oneTimeCode).autocorrectionDisabled().textInputAutocapitalization(.never)
                    .submitLabel(.done).onSubmit { save(p) }
                    .accessibilityIdentifier("her-key")
                if let page = p.keyPage ?? herKeyPage(p.id) {
                    Button(p.plan ? String(localized: "Get a Key") : String(localized: "Get a key from \(herProviderTitle(p))")) { openURL(page) }
                }
            } footer: {
                VStack(alignment: .leading, spacing: 6) {
                    if let error { Text(error).foregroundStyle(.red) }
                    if p.credential == "env", let env = p.credentialSource {
                        Text("A key was read from the environment variable \(env); one saved here is used only when it is not set.")
                    }
                    Text("Kept on \(machine.machineName), where only Her reads it.")
                }
            }
            Section {
                Button { save(p) } label: {
                    HStack { Spacer(); if busy { ProgressView() } else { Text("Save") }; Spacer() }
                }
                .disabled(busy || key.trimmingCharacters(in: .whitespaces).isEmpty)
                .accessibilityIdentifier("her-key-save")
            }
        }
    }

    private func save(_ p: HerProviderRow) {
        let k = key.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !k.isEmpty, !busy else { return }
        busy = true
        error = nil
        Task {
            do {
                let verified = try await chat.abilities.setKey(p.id, key: k)
                if !verified {
                    model.show(p.plan ? String(localized: "Saved. A plan’s key is checked on its first reply.")
                        : String(localized: "Saved, though the provider couldn’t confirm this key yet"))
                } else {
                    model.show(replacing ? String(localized: "Key replaced") : String(localized: "Connected \(herProviderTitle(p))"))
                }
                done()
                dismiss()
            } catch {
                self.error = error.localizedDescription
            }
            busy = false
        }
    }
}

// MARK: - Environment

/// Where Her runs: her environments, with the one shown checked; another machine to run her on; the storage of her
/// data there and its console; and turning her off there, her data kept or erased (docs/her-server.md §5.6).
struct HerEnvironmentSettings: View {
    var body: some View {
        HerEnvironmentPage { chat, machine in HerEnvironmentList(chat: chat, machine: machine) }
            .navigationTitle("Environment")
            .navigationBarTitleDisplayMode(.inline)
    }
}

private struct HerEnvironmentList: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let machine: HerMachine
    @State private var adding = false
    @State private var turningOff = false
    @State private var total: Int64?

    var body: some View {
        let envs = herEnvironments(model.machines)
        List {
            Section("Runs On") {
                ForEach(envs, id: \.machineID) { e in
                    Button { model.her.machine = e.machineID } label: {
                        HStack {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(e.machineName).foregroundStyle(.primary)
                                let reach = herReachable(e) ? String(localized: "Online") : String(localized: "Offline")
                                Text(e.her.unread > 0 ? String(localized: "\(reach) · \(e.her.unread) unread") : reach).font(.footnote).foregroundStyle(.secondary)
                            }
                            Spacer()
                            if e.machineID == machine.machineID { Image(systemName: "checkmark").foregroundStyle(Color.accentColor).fontWeight(.semibold) }
                        }
                        .contentShape(.rect)
                    }
                    .accessibilityAddTraits(e.machineID == machine.machineID ? .isSelected : [])
                    .accessibilityIdentifier("her-env-\(e.machineName)")
                }
                Button("Turn On Her on Another Machine…") { adding = true }
                    .accessibilityIdentifier("her-env-add")
            }
            Section {
                NavigationLink(value: Route.herStorage) {
                    LabeledContent("Storage") { if let total { Text(herBytes(total)) } }
                }
                .disabled(herAnswers(machine.her, "storage/usage") == false)
                .accessibilityIdentifier("her-storage")
                NavigationLink(value: Route.herConsole) {
                    Text("Manage “\(machine.machineName)”…")
                }
                .disabled(!herReachable(machine))
                .accessibilityIdentifier("her-console")
            } footer: {
                Text("The console is the web page that manages \(machine.machineName): models, connectors, skills, web search, the browser and storage.")
            }
            Section {
                Button("Turn Off Her on \(machine.machineName)…", role: .destructive) { turningOff = true }
                    .disabled(!herReachable(machine))
                    .accessibilityIdentifier("her-turn-off")
            }
        }
        .listSectionSpacing(.compact)
        .task(id: herReachable(machine)) {
            if herReachable(machine), herAnswers(machine.her, "storage/usage") != false { total = (try? await chat.abilities.storage())?.total }
        }
        // Her data stays unless the owner chooses to erase it too (docs/her-server.md §5.6).
        .confirmationDialog("Turn off Her on “\(machine.machineName)”?", isPresented: $turningOff, titleVisibility: .visible) {
            Button("Turn Off, Keep Her Data") { turnOff(erase: false) }
            Button("Turn Off and Erase Her Data", role: .destructive) { turnOff(erase: true) }
        } message: {
            Text("Her stops there and can no longer use your computers. Kept, her data stays, and she carries on when you turn her on again. Erasing deletes her assistants, conversations, memory, routines, files, connectors and model logins on \(machine.machineName), and can’t be undone.")
        }
        .sheet(isPresented: $adding) { HerAddSheet().environment(model) }
    }

    /// Turns Her off on her machine (SetHer), erasing her data there if chosen. The dialog said which, so Face ID
    /// asks at once; the pages go, the environment with them.
    private func turnOff(erase: Bool) {
        guard let api = model.api else { return }
        model.signer.expect(.types([SigningPreview.adminCommand]))
        var r = Hermesh_Local_V1_SetHerRequest()
        r.machineID = machine.machineID
        r.enabled = false
        r.erase = erase
        Task {
            do {
                let _: Hermesh_Local_V1_SetHerResponse = try await api.call("SetHer", r, timeout: signingTimeout)
                model.show(erase ? String(localized: "Her is off on \(machine.machineName), and her data is erased") : String(localized: "Her is off on \(machine.machineName)"))
                // From the path, not dismiss(): the page may be empty by now, its environment gone.
                model.path.removeAll { [.herSettings, .herEnvironment, .herStorage, .herConsole].contains($0) }
            } catch { model.report(error) }
        }
    }
}

// MARK: - Storage

/// Where Her's data is on her machine, that it is the only copy, and how much of each kind there is. Only to read.
struct HerStoragePage: View {
    var body: some View {
        HerEnvironmentPage { chat, machine in HerStorageList(chat: chat, machine: machine) }
            .navigationTitle("Storage")
            .navigationBarTitleDisplayMode(.inline)
    }
}

/// A kind of her data: its name, what it holds, its colour in the bar.
private func herStorageKind(_ kind: String) -> (name: String, about: String, color: Color) {
    switch kind {
    case "conversations": (String(localized: "Conversations"), String(localized: "Every conversation, with its tools’ full output"), Color(red: 0.35, green: 0.41, blue: 0.81))
    case "sent": (String(localized: "Files Sent to You"), String(localized: "A copy of each file an assistant sent you, as it was when sent; the same content is kept once"), Color(red: 0.15, green: 0.58, blue: 0.55))
    case "files": (String(localized: "Files"), String(localized: "What assistants and conversations made"), Color(red: 0.77, green: 0.53, blue: 0.12))
    case "browser": (String(localized: "Browser"), String(localized: "The built-in browser’s sign-ins and downloads"), Color(red: 0.56, green: 0.36, blue: 0.84))
    case "cache": (String(localized: "Cache"), String(localized: "Model lists, connectors’ tools, icons and logs; fetched again when gone"), Color(red: 0.54, green: 0.55, blue: 0.6))
    case "trash": (String(localized: "Deleted Conversations"), String(localized: "Moved aside when deleted, and not removed on their own"), Color(red: 0.9, green: 0.28, blue: 0.3))
    case "memory": (String(localized: "Memory"), String(localized: "What each assistant remembers about you and your work"), Color(red: 0.91, green: 0.31, blue: 0.55))
    default: (String(localized: "Settings and Other"), String(localized: "Settings, skills, routines and the rest"), Color(red: 0.23, green: 0.59, blue: 0.32))
    }
}

private struct HerStorageList: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let machine: HerMachine
    @State private var storage: HerStorage?
    @State private var failed: String?

    var body: some View {
        List {
            if let s = storage {
                Section {
                    GeometryReader { g in
                        HStack(spacing: 2) {
                            ForEach(s.bySize.filter { $0.bytes > 0 }) { p in
                                herStorageKind(p.kind).color
                                    .frame(width: max(3, (g.size.width - CGFloat(s.parts.count) * 2) * CGFloat(p.bytes) / CGFloat(max(s.total, 1))))
                            }
                        }
                        .clipShape(Capsule())
                    }
                    .frame(height: 10)
                    .listRowBackground(Color.clear)
                    .listRowInsets(EdgeInsets(top: 8, leading: 4, bottom: 4, trailing: 4))
                    .accessibilityHidden(true)
                }
                Section {
                    ForEach(s.bySize) { p in
                        let k = herStorageKind(p.kind)
                        HStack(spacing: 12) {
                            RoundedRectangle(cornerRadius: 3).fill(k.color).frame(width: 10, height: 10)
                            VStack(alignment: .leading, spacing: 2) {
                                Text(k.name)
                                Text(k.about).font(.footnote).foregroundStyle(.secondary)
                            }
                            Spacer(minLength: 8)
                            Text(herBytes(p.bytes)).foregroundStyle(.secondary).monospacedDigit()
                        }
                        .accessibilityElement(children: .combine)
                    }
                } header: {
                    Text("What It Holds · \(herBytes(s.total)) in All")
                } footer: {
                    Text("Counted each time this page opens.")
                }
                Section {
                    HStack {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(s.home).font(.body.monospaced()).textSelection(.enabled)
                            Text(s.custom ? "Chosen with HER_HOME" : "The default location").font(.footnote).foregroundStyle(.secondary)
                        }
                        Spacer(minLength: 8)
                        Button("Copy Path") {
                            UIPasteboard.general.string = s.home
                            model.show(String(localized: "Path copied"))
                        }
                        .buttonStyle(.bordered).controlSize(.small)
                    }
                } header: {
                    Text("Location")
                } footer: {
                    Text("This folder is the only copy of Her’s assistants, conversations, memory and the files they make. The relay and your other devices don’t keep it, so include it in this computer’s backups. Erasing Her’s data deletes it.")
                }
            } else if let failed {
                Section { Text(failed).foregroundStyle(.secondary) }
            } else {
                Section { HStack { Spacer(); ProgressView(); Spacer() } }
            }
        }
        .listSectionSpacing(.compact)
        .task(id: herReachable(machine)) {
            guard herReachable(machine) else { return }
            do { storage = try await chat.abilities.storage() } catch { failed = error.localizedDescription }
        }
        .accessibilityIdentifier("her-storage-page")
    }
}

// MARK: - Console

/// The environment's console (docs/her-server.md §10): the web page that manages it, served by this iPhone on a
/// loopback address under a token and reaching the machine through the tunnel, as the Mac's console window does. It
/// opens once the owner is known to hold the phone. The page stays on the console: a sign-in's page and other https
/// links open in the browser.
struct HerConsolePage: View {
    var body: some View {
        HerEnvironmentPage { chat, machine in HerConsoleView(chat: chat, machine: machine) }
            .navigationBarTitleDisplayMode(.inline)
    }
}

private struct HerConsoleView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let chat: HerChat
    let machine: HerMachine
    @State private var url: URL?
    @State private var failed: String?

    var body: some View {
        Group {
            if let url {
                HerConsoleWeb(url: url).ignoresSafeArea(edges: .bottom)
            } else if let failed {
                ContentUnavailableView("Can’t Open the Console", systemImage: "exclamationmark.triangle", description: Text(failed))
            } else {
                ProgressView()
            }
        }
        .navigationTitle(machine.machineName)
        .task {
            guard url == nil else { return }
            guard await Authentication.confirm(String(localized: "Manage \(machine.machineName)")) != nil else { return dismiss() }
            do { url = try await chat.consoleURL() } catch { failed = error.localizedDescription }
        }
        .accessibilityIdentifier("her-console-page")
    }
}

/// The console in a web view: it stays on its own origin, and https links open in the in-app browser.
private struct HerConsoleWeb: UIViewRepresentable {
    let url: URL

    func makeCoordinator() -> Coordinator { Coordinator(origin: url) }

    func makeUIView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        let web = WKWebView(frame: .zero, configuration: config)
        web.navigationDelegate = context.coordinator
        web.uiDelegate = context.coordinator
        web.allowsBackForwardNavigationGestures = false
        web.isInspectable = true
        web.accessibilityIdentifier = "her-console-web"
        web.load(URLRequest(url: url))
        return web
    }

    func updateUIView(_ web: WKWebView, context: Context) {}

    final class Coordinator: NSObject, WKNavigationDelegate, WKUIDelegate {
        let origin: URL

        init(origin: URL) { self.origin = origin }

        private func inConsole(_ u: URL) -> Bool { u.scheme == origin.scheme && u.host == origin.host && u.port == origin.port }

        func webView(_ web: WKWebView, decidePolicyFor action: WKNavigationAction) async -> WKNavigationActionPolicy {
            guard let u = action.request.url else { return .cancel }
            if inConsole(u) || u.scheme == "about" { return .allow }
            if u.scheme == "https" { await UIApplication.shared.open(u) }
            return .cancel
        }

        /// window.open: a sign-in's page, or a link the console opens apart.
        func webView(_ web: WKWebView, createWebViewWith configuration: WKWebViewConfiguration, for action: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
            // The sign-in sheet shares Safari's sign-ins, and the core on this iPhone catches its redirect.
            if let u = action.request.url, u.scheme == "https" { Task { @MainActor in HerBrowser.shared.open(u, closed: {}) } }
            return nil
        }
    }
}
