import HermeshHer
import HermeshKit
import SwiftUI
import UniformTypeIdentifiers

// The pages under Her's Abilities tab that her environment's console also has (docs/her-panel-demo.html,
// docs/her-server.md §10): her skills, each with a page of its own and added by uploading an archive; web search, its
// services in the order they are tried; and the browsers, with the sites she may open without asking. They write
// her user file as the console does, and read again when her says it changed.

/// A page about one of her methods, or what says the machine is too old for it (docs/version-skew.md §6.2).
private struct HerNeeds<Content: View>: View {
    let machine: HerMachine
    let method: String
    @ViewBuilder let content: Content

    var body: some View {
        if herAnswers(machine.her, method) == false { NeedsUpdateView(machineName: machine.machineName) } else { content }
    }
}

// MARK: - Skills

/// Her's skills: her own (uploaded or put there), those in configured folders, and those built into her; each turned
/// on or off here, opening a page of its own. A skill is added by uploading a .zip or .skill archive.
struct HerSkillsPage: View {
    var body: some View {
        HerEnvironmentPage { chat, machine in
            HerNeeds(machine: machine, method: "skills/list") { HerSkillsList(chat: chat, machine: machine) }
        }
        .navigationTitle("Skills")
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// The skills as a page holds them, with switches turned here shown at once.
@Observable
final class HerSkillsState {
    let chat: HerChat
    private(set) var skills: HerSkills?
    private(set) var failed: String?
    var switching: [String: Bool] = [:]

    init(chat: HerChat) { self.chat = chat }

    func shown(_ s: HerSkill) -> HerSkill {
        var s = s
        if let on = switching[s.dir] { s.on = on }
        return s
    }

    func load() async {
        do {
            skills = try await chat.abilities.skills()
            failed = nil
            switching = switching.filter { k, v in skills?.skills.first { $0.dir == k }?.on != v }
        } catch {
            failed = error.localizedDescription
        }
    }

    func set(_ s: HerSkill, on: Bool, model: AppModel) async {
        switching[s.dir] = on
        do {
            try await chat.abilities.setSkill(s, on: on)
            await load()
            model.show(on ? String(localized: "Turned on \(s.name) for new conversations") : String(localized: "Turned off \(s.name) for new conversations"))
        } catch {
            switching[s.dir] = nil
            model.show(error.localizedDescription)
        }
    }
}

private struct HerSkillsList: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let machine: HerMachine
    @State private var state: HerSkillsState
    @State private var picking = false
    @State private var found: Found?
    @State private var reading = false
    @State private var brokenShown = false

    /// An uploaded archive her read: the skill in it, where it goes, and one of the same name there.
    struct Found: Identifiable {
        let id = UUID()
        let path: String
        let skill: HerSkill?
        let target: String
        let conflict: String?
    }

    init(chat: HerChat, machine: HerMachine) {
        self.chat = chat
        self.machine = machine
        _state = State(initialValue: HerSkillsState(chat: chat))
    }

    var body: some View {
        List {
            if let skills = state.skills {
                if !skills.broken.isEmpty {
                    Section {
                        Button { brokenShown = true } label: {
                            Label { Text("\(skills.broken.count) skills didn’t load").foregroundStyle(.primary) } icon: {
                                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                            }
                        }
                    }
                }
                Section {
                    if skills.group(.user).isEmpty {
                        VStack(alignment: .leading, spacing: 4) {
                            Text("No skills of your own yet")
                            Text("Upload one as a .zip or .skill archive.").font(.footnote).foregroundStyle(.secondary)
                        }
                        .padding(.vertical, 4)
                    }
                    ForEach(skills.group(.user)) { row($0) }
                } header: {
                    Text("Installed")
                }
                if !skills.group(.extra).isEmpty {
                    Section {
                        ForEach(skills.group(.extra)) { row($0) }
                    } header: {
                        Text("Configured Paths")
                    } footer: {
                        Text("Folders in skills.paths.")
                    }
                }
                if !skills.group(.system).isEmpty {
                    Section {
                        ForEach(skills.group(.system)) { row($0) }
                    } header: {
                        Text("Built In")
                    } footer: {
                        Text("Updated with Her.")
                    }
                }
                Section {
                    Button { picking = true } label: {
                        HStack { Text("Upload a Skill"); Spacer(); if reading { ProgressView() } }
                    }
                    .disabled(reading || !herReachable(machine))
                    .accessibilityIdentifier("her-skill-upload")
                } footer: {
                    Text("A skill is a folder with a SKILL.md, uploaded as a .zip or .skill archive from Files. Her checks its header, then copies it into ~/.her/skills on \(machine.machineName).")
                }
            } else if let failed = state.failed {
                Section { Text(failed).foregroundStyle(.secondary) }
            } else {
                Section { HStack { Spacer(); ProgressView(); Spacer() } }
            }
        }
        .listSectionSpacing(.compact)
        .task(id: "\(chat.envUp) \(chat.settingsChanged)") { if chat.envUp { await state.load() } }
        .fileImporter(isPresented: $picking, allowedContentTypes: [.zip, UTType(filenameExtension: "skill") ?? .data]) { r in
            if case .success(let url) = r { read(url) }
        }
        .sheet(item: $found) { f in
            HerSkillFound(found: f, machine: machine) { replace in install(f, replace: replace) }
        }
        .sheet(isPresented: $brokenShown) {
            NavigationStack {
                List(state.skills?.broken ?? []) { s in
                    VStack(alignment: .leading, spacing: 4) {
                        Text(s.path).font(.footnote.monospaced()).textSelection(.enabled)
                        if let why = s.problem { Text(why).foregroundStyle(.secondary) }
                    }
                }
                .navigationTitle("Skills That Didn’t Load")
                .navigationBarTitleDisplayMode(.inline)
                .safeAreaInset(edge: .top) {
                    Text("Her skipped them; the other skills are not affected. Once they are fixed, the list updates by itself.")
                        .font(.footnote).foregroundStyle(.secondary).padding(.horizontal, 20).padding(.top, 4)
                }
                .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { brokenShown = false } } }
            }
        }
    }

    private func row(_ s: HerSkill) -> some View {
        let shown = state.shown(s)
        // The link and the switch side by side, not one in the other: VoiceOver reads two controls, one opening its page.
        return HStack(spacing: 12) {
            NavigationLink(value: Route.herSkill(s.dir)) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(s.name).font(.body.monospaced())
                    Text(shown.on ? (s.description.isEmpty ? " " : s.description) : String(localized: "Turned off")).font(.footnote).foregroundStyle(.secondary).lineLimit(2)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .accessibilityIdentifier("her-skill-\(s.name)")
            Toggle(String(localized: "Turn On \(s.name)"), isOn: Binding(get: { shown.on }, set: { on in Task { await state.set(s, on: on, model: model) } }))
                .labelsHidden()
                .disabled(!herReachable(machine))
                .accessibilityIdentifier("her-skill-switch-\(s.name)")
        }
    }

    /// Puts the archive on the machine and asks her to read it (a dry run): what it holds is shown before it is added.
    private func read(_ url: URL) {
        reading = true
        Task {
            defer { reading = false }
            let scoped = url.startAccessingSecurityScopedResource()
            defer { if scoped { url.stopAccessingSecurityScopedResource() } }
            do {
                let path = try await chat.upload(url)
                let r = try await chat.abilities.installSkill(from: path, dryRun: true)
                found = Found(path: path, skill: r.skill, target: r.target, conflict: r.conflict)
            } catch {
                model.show(error.localizedDescription)
            }
        }
    }

    private func install(_ f: Found, replace: Bool) {
        Task {
            do {
                let r = try await chat.abilities.installSkill(from: f.path, dryRun: false, replace: replace)
                let name = r.skill?.name ?? f.skill?.name ?? ""
                model.show(replace ? String(localized: "Replaced \(name); the old one is in the Trash") : String(localized: "Added \(name); new conversations can use it"))
                await state.load()
            } catch {
                model.show(error.localizedDescription)
            }
        }
    }
}

/// The skill an uploaded archive holds, checked before it is added: its name and what it does, where it goes, and
/// whether it replaces one of the same name.
private struct HerSkillFound: View {
    @Environment(\.dismiss) private var dismiss
    let found: HerSkillsList.Found
    let machine: HerMachine
    let add: (_ replace: Bool) -> Void

    var body: some View {
        NavigationStack {
            Form {
                if let s = found.skill {
                    Section {
                        Label("Well formed", systemImage: "checkmark.seal.fill").foregroundStyle(.green)
                        VStack(alignment: .leading, spacing: 4) {
                            Text(s.name).font(.headline.monospaced())
                            if !s.description.isEmpty { Text(s.description).foregroundStyle(.secondary) }
                        }
                    } header: {
                        Text("Found a skill. Check it:")
                    }
                    Section("Copies To") { Text(found.target).font(.footnote.monospaced()).textSelection(.enabled) }
                    if found.conflict != nil {
                        Section { Text("There is a skill of this name. Replacing it moves the old one to the Trash first.").foregroundStyle(.orange) }
                    }
                }
            }
            .navigationTitle("Upload a Skill")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button(found.conflict != nil ? String(localized: "Replace") : String(localized: "Add")) {
                        add(found.conflict != nil)
                        dismiss()
                    }
                    .disabled(found.skill == nil)
                    .accessibilityIdentifier("her-skill-add")
                }
            }
        }
        .presentationDetents([.medium, .large])
    }
}

/// A skill of Her's: on or off, where it came from, how it is called, where it is on the machine, what it does, its
/// files (each shown as text), and deleting it, which moves it to the Trash there.
struct HerSkillPage: View {
    let dir: String

    var body: some View {
        HerEnvironmentPage { chat, machine in HerSkillView(chat: chat, machine: machine, dir: dir) }
            .navigationBarTitleDisplayMode(.inline)
    }
}

private struct HerSkillView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let chat: HerChat
    let machine: HerMachine
    let dir: String
    @State private var state: HerSkillsState
    @State private var files: [HerSkillFile]?
    @State private var filesFailed: String?
    @State private var shownFile: Shown?
    @State private var removing = false

    struct Shown: Identifiable {
        let id = UUID()
        let name: String
        let text: String?
    }

    init(chat: HerChat, machine: HerMachine, dir: String) {
        self.chat = chat
        self.machine = machine
        self.dir = dir
        _state = State(initialValue: HerSkillsState(chat: chat))
    }

    var body: some View {
        let skill = state.skills?.skills.first { $0.dir == dir }.map(state.shown)
        List {
            if let s = skill {
                Section {
                    Toggle("On", isOn: Binding(get: { s.on }, set: { on in Task { await state.set(s, on: on, model: model) } }))
                        .disabled(!herReachable(machine))
                        .accessibilityIdentifier("her-skill-on")
                    LabeledContent("Source", value: source(s.source))
                    LabeledContent("Invoke") {
                        HStack(spacing: 10) {
                            Text(s.command).font(.body.monospaced())
                            Button("Copy") {
                                UIPasteboard.general.string = s.command
                                model.show(String(localized: "Copied \(s.command)"))
                            }
                            .buttonStyle(.borderless)
                        }
                    }
                } footer: {
                    if !s.modelInvocable { Text("Her uses it only when you name it.") }
                    else if s.source == .extra { Text("This skill comes from a folder in skills.paths. It can only be turned off here.") }
                }
                Section("Path") { Text(s.dir).font(.footnote.monospaced()).textSelection(.enabled) }
                if !s.description.isEmpty {
                    Section("About") { Text(s.description).textSelection(.enabled) }
                }
                Section("Files") {
                    if let files {
                        ForEach(files.filter { !$0.dir }) { f in
                            Button { open(s, f) } label: {
                                LabeledContent(f.path) { Text(herBytes(Int64(f.size))) }
                            }
                            .foregroundStyle(.primary)
                        }
                    } else if let filesFailed {
                        Text("Could not read the files: \(filesFailed)").foregroundStyle(.secondary)
                    } else {
                        ProgressView().frame(maxWidth: .infinity)
                    }
                }
                if s.removable {
                    Section {
                        Button("Delete Skill", role: .destructive) { removing = true }
                            .disabled(!herReachable(machine))
                            .accessibilityIdentifier("her-skill-delete")
                    }
                }
            } else if state.skills != nil {
                ContentUnavailableView("This Skill Is Gone", systemImage: "book.closed", description: Text("It was removed, here or elsewhere."))
            } else {
                Section { HStack { Spacer(); ProgressView(); Spacer() } }
            }
        }
        .listSectionSpacing(.compact)
        .navigationTitle(skill?.name ?? "")
        .task(id: "\(chat.envUp) \(chat.settingsChanged)") {
            guard chat.envUp else { return }
            await state.load()
            if files == nil, let s = state.skills?.skills.first(where: { $0.dir == dir }) {
                do { files = try await chat.abilities.skillFiles(s) } catch { filesFailed = error.localizedDescription }
            }
        }
        .confirmationDialog("Delete \(skill?.name ?? "")?", isPresented: $removing, titleVisibility: .visible) {
            Button("Delete", role: .destructive) { remove() }
        } message: {
            Text("It moves to the Trash on \(machine.machineName), and new conversations no longer have it.")
        }
        .sheet(item: $shownFile) { f in
            NavigationStack {
                ScrollView {
                    if let text = f.text {
                        Text(text).font(.footnote.monospaced()).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading).padding(16)
                    } else {
                        Text("This file isn’t text, so it can’t be shown here.").foregroundStyle(.secondary).padding(24)
                    }
                }
                .navigationTitle(f.name)
                .navigationBarTitleDisplayMode(.inline)
                .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { shownFile = nil } } }
            }
        }
    }

    private func source(_ s: HerSkill.Source) -> String {
        switch s {
        case .system: String(localized: "Built into Her")
        case .catalog: String(localized: "Featured")
        case .upload: String(localized: "Uploaded")
        case .local: String(localized: "Local")
        case .extra: String(localized: "Configured Paths")
        }
    }

    private func open(_ s: HerSkill, _ f: HerSkillFile) {
        Task {
            do { shownFile = Shown(name: f.path, text: try await chat.abilities.skillFile(s, f.path)) } catch { model.show(error.localizedDescription) }
        }
    }

    private func remove() {
        guard let s = state.skills?.skills.first(where: { $0.dir == dir }) else { return }
        Task {
            do {
                try await chat.abilities.removeSkill(s)
                model.show(String(localized: "Deleted \(s.name); it is in the Trash"))
                dismiss()
            } catch { model.show(error.localizedDescription) }
        }
    }
}

// MARK: - Web search

/// Whether the model may search the web, and the services it uses, tried from the top down: each switched on or off,
/// moved, given a key, tested.
struct HerSearchPage: View {
    var body: some View {
        HerEnvironmentPage { chat, machine in
            HerNeeds(machine: machine, method: "web_search/status") { HerSearchList(chat: chat, machine: machine) }
        }
        .navigationTitle("Web Search")
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// What a search service says of itself: its cost and what it needs, as the console words it.
private func herSearchAbout(_ s: HerSearchSource) -> String? {
    let keyed = s.key == .saved
    return switch s.id {
    case "deepseek": String(localized: "Shares your model key · $0.75 per 1,000")
    case "exa": keyed ? String(localized: "Key added · $4 per 1,000, $10 free a month") : String(localized: "Free tier, no key needed")
    case "parallel": keyed ? String(localized: "Key added, higher limits") : String(localized: "Free tier, no key needed")
    case "tavily": keyed ? String(localized: "Key added · 1,000 free a month") : String(localized: "1,000 free a month, needs a key")
    case "brave": keyed ? String(localized: "Key added · $5 free a month") : String(localized: "$5 free a month, needs a key")
    case "bocha": keyed ? String(localized: "Key added") : String(localized: "Chinese content, needs a key")
    default: nil
    }
}

/// Why a search failed, in a few words.
private func herSearchWhy(_ kind: String) -> String {
    switch kind {
    case "auth_failed": String(localized: "key rejected")
    case "quota_exhausted": String(localized: "out of quota")
    case "rate_limited": String(localized: "limiting requests")
    case "bad_response": String(localized: "unreadable answer")
    default: String(localized: "could not be reached")
    }
}

private struct HerSearchList: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let machine: HerMachine
    @State private var search: HerSearch?
    @State private var failed: String?
    @State private var keyFor: HerSearchSource?
    @State private var removingKey: HerSearchSource?
    @State private var testing: Set<String> = []
    @State private var editing = EditMode.inactive

    var body: some View {
        List {
            if let s = search {
                Section {
                    Toggle("Let the Model Search the Web", isOn: Binding(get: { s.enabled }, set: { on in
                        search?.enabled = on
                        write(HerSearch.enabledEdits(on), done: on ? String(localized: "Web search is on") : String(localized: "Web search is off"))
                    }))
                    .accessibilityIdentifier("her-search-on")
                } footer: {
                    Text("The model searches when it needs to. What it searches for is sent to the services below.")
                }
                Section {
                    ForEach(s.sources) { src in row(src, current: s.current) }
                        .onMove { from, to in
                            let order = s.moved(from: from, to: to)
                            search?.sources = order.compactMap { id in s.sources.first { $0.id == id } }
                            write(HerSearch.orderEdits(order))
                        }
                } header: {
                    HStack {
                        Text("Search Services")
                        Spacer()
                        Button(editing.isEditing ? String(localized: "Done") : String(localized: "Reorder")) {
                            withAnimation { editing = editing.isEditing ? .inactive : .active }
                        }
                        .font(.footnote.weight(.semibold)).textCase(nil)
                        .accessibilityIdentifier("her-search-reorder")
                    }
                } footer: {
                    if s.noneLeft {
                        Text("Every service is off, so the model can’t search right now.").foregroundStyle(.orange)
                    } else {
                        Text("From the top down, the first one that can be used goes first; if it fails, the next.")
                    }
                }
                .disabled(!s.enabled)
            } else if let failed {
                Section { Text(failed).foregroundStyle(.secondary) }
            } else {
                Section { HStack { Spacer(); ProgressView(); Spacer() } }
            }
        }
        .listSectionSpacing(.compact)
        .environment(\.editMode, $editing)
        .task(id: "\(chat.envUp) \(chat.settingsChanged)") { await load() }
        .sheet(item: $keyFor) { src in
            HerSearchKeySheet(chat: chat, machine: machine, source: src) { said in
                model.show(said)
                Task { await load() }
            }
        }
        .confirmationDialog("Remove the \(removingKey?.name ?? "") key?", isPresented: Binding(get: { removingKey != nil }, set: { if !$0 { removingKey = nil } }), titleVisibility: .visible, presenting: removingKey) { src in
            Button("Remove", role: .destructive) { removeKey(src) }
        } message: { src in
            Text(src.needs == .key ? "The key is deleted from the credential store, and searches skip this service." : "The key is deleted from the credential store, and searches go on with the free tier.")
        }
    }

    @ViewBuilder private func row(_ src: HerSearchSource, current: String?) -> some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(src.name)
                    if src.id == current {
                        Text("In use").font(.caption.weight(.semibold)).foregroundStyle(Color.accentColor)
                            .padding(.horizontal, 7).padding(.vertical, 1).background(Color.accentColor.opacity(0.15), in: .capsule)
                    }
                }
                Group {
                    if src.wantsAccount {
                        Text("Connect \(src.name) under Models first")
                    } else if let f = src.failure {
                        Text("\(herSearchWhy(f.kind)) · \(f.at.formatted(.relative(presentation: .named)))").foregroundStyle(.orange)
                    } else if src.key == .env, let env = src.keyEnv {
                        Text("Key from the environment variable \(env)")
                    } else if let about = herSearchAbout(src) {
                        Text(about)
                    }
                }
                .font(.footnote).foregroundStyle(.secondary).lineLimit(2)
            }
            Spacer(minLength: 8)
            if !editing.isEditing {
                if testing.contains(src.id) {
                    ProgressView()
                } else if src.wantsKey {
                    Button("Add Key") { keyFor = src }.buttonStyle(.bordered).controlSize(.small)
                        .accessibilityIdentifier("her-search-key-\(src.id)")
                } else if src.wantsAccount {
                    Button("Connect") { model.path.append(.herModel) }.buttonStyle(.bordered).controlSize(.small)
                } else {
                    Toggle("Use \(src.name)", isOn: Binding(get: { src.enabled }, set: { on in
                        guard let s = search else { return }
                        if let i = search?.sources.firstIndex(where: { $0.id == src.id }) { search?.sources[i].enabled = on }
                        write(s.switchEdits(src.id, on: on))
                    }))
                    .labelsHidden()
                    .accessibilityIdentifier("her-search-switch-\(src.id)")
                }
            }
        }
        .contextMenu {
            if src.configured { Button("Test Search", systemImage: "magnifyingglass") { test(src) } }
            if src.needs != .account, src.key != .env {
                Button(src.key == .saved ? String(localized: "Replace Key") : String(localized: "Add Key"), systemImage: "key") { keyFor = src }
            }
            if src.key == .saved { Button("Remove Key", systemImage: "trash", role: .destructive) { removingKey = src } }
        }
        .accessibilityIdentifier("her-search-\(src.id)")
    }

    private func load() async {
        guard chat.envUp else { return }
        do {
            search = try await chat.abilities.search()
            failed = nil
        } catch { failed = error.localizedDescription }
    }

    /// Writes to her user file, then reads again: a failed write puts the page back.
    private func write(_ edits: JSON, done: String? = nil) {
        Task {
            do {
                try await chat.abilities.write(edits)
                if let done { model.show(done) }
            } catch { model.show(error.localizedDescription) }
            await load()
        }
    }

    private func test(_ src: HerSearchSource) {
        testing.insert(src.id)
        Task {
            defer { testing.remove(src.id) }
            do {
                let r = try await chat.abilities.testSearch(src.id)
                model.show(r.ok ? String(localized: "\(src.name) works · \(r.count) results · \(r.seconds.formatted(.number.precision(.fractionLength(1)))) s")
                    : String(localized: "\(src.name) did not work: \(herSearchWhy(r.error ?? ""))"))
            } catch { model.show(error.localizedDescription) }
            await load()
        }
    }

    private func removeKey(_ src: HerSearchSource) {
        Task {
            do {
                try await chat.abilities.removeSearchKey(src.id)
                model.show(src.needs == .key ? String(localized: "Key removed. Searches skip \(src.name)") : String(localized: "Key removed. Searches go on with the free tier"))
            } catch { model.show(error.localizedDescription) }
            await load()
        }
    }
}

/// A search service's key: pasted here, saved in her credential store and tested at once. A key the service refuses
/// is not saved.
private struct HerSearchKeySheet: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(\.openURL) private var openURL
    let chat: HerChat
    let machine: HerMachine
    let source: HerSearchSource
    let saved: (String) -> Void
    @State private var key = ""
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    SecureField("API Key", text: $key)
                        .textContentType(.oneTimeCode).autocorrectionDisabled().textInputAutocapitalization(.never)
                        .submitLabel(.done).onSubmit(save)
                        .accessibilityIdentifier("her-search-key")
                    if let page = source.keyPage {
                        Button("Get one from \(source.name)") { openURL(page) }
                    }
                } header: {
                    if let about = sheetAbout { Text(about).textCase(nil) }
                } footer: {
                    VStack(alignment: .leading, spacing: 6) {
                        if let error { Text(error).foregroundStyle(.red) }
                        Text("Kept in the credential store on \(machine.machineName).")
                    }
                }
            }
            .navigationTitle(source.key == .saved ? String(localized: "Replace \(source.name) Key") : String(localized: "Add \(source.name) Key"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    if busy { ProgressView() } else {
                        Button("Save and Test", action: save).disabled(key.trimmingCharacters(in: .whitespaces).isEmpty)
                            .accessibilityIdentifier("her-search-key-save")
                    }
                }
            }
        }
        .presentationDetents([.medium])
    }

    private var sheetAbout: String? {
        switch source.id {
        case "exa": String(localized: "Works without a key. With one it uses your own credit, at higher limits.")
        case "parallel": String(localized: "Works without a key. With one the limits are higher.")
        case "tavily": String(localized: "1,000 searches free a month, then about $0.008 each.")
        case "brave": String(localized: "$5 free a month, about 1,000 searches. Signing up needs a card.")
        case "bocha": String(localized: "A search service for Chinese content.")
        default: nil
        }
    }

    private func save() {
        guard !busy, !key.trimmingCharacters(in: .whitespaces).isEmpty else { return }
        busy = true
        error = nil
        Task {
            defer { busy = false }
            do {
                let r = try await chat.abilities.setSearchKey(source.id, key: key)
                if !r.saved { error = String(localized: "\(source.name) refused this key. It was not saved."); return }
                saved(r.test.ok ? String(localized: "\(source.name) works · \(r.test.count) results · \(r.test.seconds.formatted(.number.precision(.fractionLength(1)))) s")
                    : String(localized: "Key saved, but the test did not get through"))
                dismiss()
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

// MARK: - Browser

/// Whether Her controls browsers and where new pages open; the owner's own Chrome on her machine and Her's built-in
/// browser there; and the sites she may open and act on without asking. Connecting the owner's Chrome is done on that
/// computer, where the extension is added; here it shows how it stands, and is used once connected.
struct HerBrowserPage: View {
    var body: some View {
        HerEnvironmentPage { chat, machine in
            HerNeeds(machine: machine, method: "browser/status") { HerBrowserList(chat: chat, machine: machine) }
        }
        .navigationTitle("Browser")
        .navigationBarTitleDisplayMode(.inline)
    }
}

private func herSiteLevelName(_ l: HerSiteLevel) -> String {
    switch l {
    case .act: String(localized: "Open and Act")
    case .navigate: String(localized: "Open Only")
    case .ask: String(localized: "Ask First")
    case .deny: String(localized: "Deny")
    }
}

private struct HerBrowserList: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let machine: HerMachine
    @State private var browser: HerBrowsers?
    @State private var failed: String?
    @State private var site = ""
    @State private var level = HerSiteLevel.act

    var body: some View {
        List {
            if let b = browser {
                let computer = b.os == "linux" ? String(localized: "Linux computer") : String(localized: "Mac")
                Section {
                    Toggle("Allow Her to Control Browsers", isOn: Binding(get: { b.controlling }, set: { write(HerBrowsers.controlEdits($0)) }))
                        .accessibilityIdentifier("her-browser-on")
                } footer: {
                    Text("Her opens web pages and acts in them. Which sites she may use without asking is below.")
                }
                Group {
                    Section {
                        Picker("Default Browser", selection: Binding(get: { b.opensInYours }, set: { write(HerBrowsers.defaultEdits(yours: $0)) })) {
                            Text("Built-in Browser").tag(false)
                            if b.yours == .on { Text("\(b.chrome.map(herChromeName) ?? "Chrome") (Your Browser)").tag(true) }
                        }
                        .disabled(b.yours != .on)
                    } footer: {
                        Text(b.yours == .on ? "When the agent doesn’t name a browser, new pages open here." : "Connect your Chrome to choose it. Until then, new pages open in the built-in browser.")
                    }
                    Section {
                        HStack {
                            Text("Your Chrome")
                            Spacer()
                            Text(badge(b.yours)).font(.footnote.weight(.semibold))
                                .foregroundStyle(b.yours == .on ? Color.green : b.yours == .outdated || b.yours == .lost ? .orange : .secondary)
                        }
                        if b.yours == .ready {
                            Button("Use") { write(b.extensionEdits(true)) }.accessibilityIdentifier("her-browser-use")
                        } else if b.yours == .on || b.yours == .lost {
                            Button("Stop Using", role: .destructive) { write(b.extensionEdits(false)) }
                        }
                    } footer: {
                        Text(yoursNote(b, computer: computer))
                    }
                    Section("Built-in Browser") {
                        Text(builtinNote(b, computer: computer)).foregroundStyle(.secondary)
                    }
                    Section {
                        Picker("All Sites", selection: Binding(get: { b.allSites }, set: { write(HerBrowsers.allSitesEdits($0)) })) {
                            ForEach([HerSiteLevel.ask, .navigate, .act], id: \.self) { Text(herSiteLevelName($0)).tag($0) }
                        }
                        .accessibilityIdentifier("her-browser-all-sites")
                    } header: {
                        Text("Site Permissions")
                    } footer: {
                        Text(allSitesNote(b.allSites))
                    }
                    Section {
                        if b.exceptions.isEmpty { Text("No exceptions yet. “Always allow” in a conversation adds a site here.").foregroundStyle(.secondary) }
                        ForEach(b.exceptions, id: \.origin) { e in
                            Picker(e.origin.replacingOccurrences(of: "https://", with: ""), selection: Binding(get: { e.level }, set: { write(HerBrowsers.siteEdits(e.origin, $0)) })) {
                                ForEach(HerSiteLevel.allCases, id: \.self) { Text(herSiteLevelName($0)).tag($0) }
                            }
                            .swipeActions { Button("Remove", role: .destructive) { write(HerBrowsers.siteEdits(e.origin, nil)) } }
                        }
                        HStack(spacing: 8) {
                            TextField("example.com", text: $site)
                                .keyboardType(.URL).textInputAutocapitalization(.never).autocorrectionDisabled()
                                .submitLabel(.done).onSubmit(add)
                                .accessibilityIdentifier("her-browser-site")
                            Menu(herSiteLevelName(level)) {
                                ForEach(HerSiteLevel.allCases, id: \.self) { l in Button(herSiteLevelName(l)) { level = l } }
                            }
                            Button("Add", action: add).disabled(site.trimmingCharacters(in: .whitespaces).isEmpty)
                                .accessibilityIdentifier("her-browser-site-add")
                        }
                        .buttonStyle(.borderless)
                    } header: {
                        Text("Exceptions")
                    } footer: {
                        Text("Both browsers share these. Addresses on a private network don’t follow All Sites: each asks the first time.")
                    }
                }
                .disabled(!b.controlling)
                .opacity(b.controlling ? 1 : 0.5)
            } else if let failed {
                Section { Text(failed).foregroundStyle(.secondary) }
            } else {
                Section { HStack { Spacer(); ProgressView(); Spacer() } }
            }
        }
        .listSectionSpacing(.compact)
        .task(id: "\(chat.envUp) \(chat.settingsChanged)") { await load() }
    }

    private func badge(_ y: HerBrowsers.Yours) -> String {
        switch y {
        case .on: String(localized: "In use")
        case .ready: String(localized: "Connected")
        case .outdated: String(localized: "Needs update")
        case .off, .lost: String(localized: "Not connected")
        }
    }

    private func yoursNote(_ b: HerBrowsers, computer: String) -> String {
        switch b.yours {
        case .off: String(localized: "Let Her open sites that need your sign-ins in your own Chrome, on the \(computer) Her runs on. It is connected there, by adding the Hermesh extension; here you can see how it stands.")
        case .ready: String(localized: "The extension is connected, but Her doesn’t use it yet.")
        case .on: String(localized: "Her’s tabs go in a “Hermesh” tab group and work in the background. Closing the “debugging” bar at the top of Chrome takes a tab back.")
        case .lost: String(localized: "The extension is not connected. Check that Chrome is open on the \(computer) Her runs on, and that Hermesh is on in its extensions page.")
        case .outdated: String(localized: "The extension and Hermesh on the \(computer) Her runs on are not the same version; the older one needs an update.")
        }
    }

    private func builtinNote(_ b: HerBrowsers, computer: String) -> String {
        guard let program = b.program else { return String(localized: "There is no Chrome on the \(computer) Her runs on. Install Google Chrome there, and Her finds it.") }
        return b.window ? String(localized: "Her opens pages in her own \(program) on the \(computer) she runs on, without your sign-ins, in a window there.")
            : String(localized: "Her opens pages in her own \(program) on the \(computer) she runs on, without your sign-ins, with no window.")
    }

    private func allSitesNote(_ l: HerSiteLevel) -> String {
        switch l {
        case .navigate: String(localized: "Sites open without asking; acting on them asks first")
        case .act: String(localized: "Sites open and are acted on without asking")
        default: String(localized: "Each site asks the first time it’s used")
        }
    }

    private func load() async {
        guard chat.envUp else { return }
        do {
            browser = try await chat.abilities.browser()
            failed = nil
        } catch { failed = error.localizedDescription }
    }

    private func write(_ edits: JSON) {
        Task {
            do { try await chat.abilities.write(edits) } catch { model.show(error.localizedDescription) }
            await load()
        }
    }

    private func add() {
        guard let origin = herSiteOrigin(site) else {
            model.show(String(localized: "Enter a site such as example.com or https://example.com."))
            return
        }
        let chosen = level
        Task {
            do {
                try await chat.abilities.write(HerBrowsers.siteEdits(origin, chosen))
                site = ""
                model.show("\(origin.replacingOccurrences(of: "https://", with: "")): \(herSiteLevelName(chosen))")
            } catch { model.show(error.localizedDescription) }
            await load()
        }
    }
}

/// "Google Chrome" of "Google Chrome 141.0.7390.54".
private func herChromeName(_ s: String) -> String {
    s.replacingOccurrences(of: #"\s+[\d.]+$"#, with: "", options: .regularExpression)
}
