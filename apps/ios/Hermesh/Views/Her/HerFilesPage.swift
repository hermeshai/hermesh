import HermeshHer
import HermeshKit
import QuickLook
import SwiftUI

// The files Her sent, in a place of their own (docs/her-deliverables.md §3.3): a page pushed from her page's top,
// as the Mac's panel lists them. Newest first, searched and filtered by her, grouped here by time or by what each
// came from; a file sent again from the same path is one row, its versions under it. A file opens on a page of its
// own, from the copy her kept of it as it was sent, with what Her said with it, Show in Conversation and Reply under
// it. Three tabs: all of them, documents (what is read: reports, sheets, slides, PDF, Markdown, code and the rest), and
// media (pictures, video and audio), the media as a grid. Once read, a file is kept on this iPhone by its hash, so it opens while her machine is offline too.
// Where the files came from is not on their rows: the glass button at the top right opens a sheet from the bottom, half
// the screen and dragged up for all, listing the routines and sessions that sent files, the latest first, to show one
// source's (§3.5).

/// The environment her page shows, with Her as it lists her.
struct HerFilesPage: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Group {
            if let m = herPick(herEnvironments(model.machines), remembered: model.her.machine) {
                let chat = model.her.chat(m.machineID, model: model)
                Group {
                    if herAnswers(m.her, "deliveries/list") == false {
                        NeedsUpdateView(machineName: m.machineName)
                    } else if let bot = chat.bot {
                        HerFilesList(chat: chat, machine: m, bot: bot)
                    } else {
                        PageLoader(text: "Reading the conversation…", bare: true)
                    }
                }
                .herConnections(chat, machine: m)
            } else {
                ContentUnavailableView {
                    Label("Her isn’t on", systemImage: "tray")
                } description: {
                    Text("The files Her sends you show here once she runs on one of your machines.")
                }
            }
        }
        .navigationTitle("Files")
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// A file and which of its versions shows.
private struct Shown: Identifiable {
    let file: HerDeliveredFile
    var version: Int
    var id: String { file.path }
}

private struct HerFilesList: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let machine: HerMachine
    let bot: HerBot
    @State private var query = ""
    @State private var tab = Tab.all
    @State private var source: HerSourceFilter?
    @State private var sources: HerDeliverySources?
    @State private var choosing = false
    @State private var list: HerDeliveries?
    @State private var failed: String?
    @State private var older = false
    @State private var loadingMore = false
    @State private var shown: Shown?
    @State private var sharing: URL?
    /// The latest send the list showed, and the mark it opened with: leaving is having seen it.
    @State private var sawUpTo = 0
    @State private var seenBefore: Int?

    /// The machine can be reached: the list is asked for at once, on a connection of its own, not after the
    /// environment's connection this page holds has opened.
    private var online: Bool { herReachable(machine) }

    /// What the list asks her for, again whenever it changes.
    private struct Asked: Equatable {
        var query: String
        var tab: Tab
        var source: HerSourceFilter?
        var latest: Int
        var online: Bool
    }

    /// What a tab shows: all (nil), only media, or only documents.
    private var media: Bool? { tab == .all ? nil : tab == .media }

    var body: some View {
        let shownList = list ?? keptList
        let groups = shownList.map { herDeliveryGroups($0.files, now: model.now, zone: .current) } ?? []
        List {
            if !online, shownList != nil {
                Section {
                    Label("\(machine.machineName) is offline. This is the list as last seen; files opened before are on this iPhone, the rest wait for it.", systemImage: "icloud.slash")
                        .font(.footnote).foregroundStyle(.secondary)
                        .accessibilityIdentifier("her-files-offline")
                }
            }
            ForEach(groups) { g in
                Section {
                    if tab == .media {
                        LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 8), count: 3), spacing: 8) {
                            ForEach(g.files) { f in
                                HerFileThumb(chat: chat, file: f, fresh: f.isNew(seen: shownList?.seen ?? 0), enabled: canOpen(f, 0)) { open(f, 0) }
                            }
                        }
                        .listRowInsets(EdgeInsets(top: 8, leading: 16, bottom: 8, trailing: 16))
                    } else {
                        ForEach(g.files) { f in row(f, seen: shownList?.seen ?? 0) }
                    }
                } header: {
                    HStack {
                        Text(dayTitle(g.day))
                        Text(verbatim: "\(g.files.count)").foregroundStyle(.tertiary)
                    }
                }
            }
            if list?.next != nil {
                Button { loadMore() } label: {
                    HStack { Spacer(); if loadingMore { ProgressView() } else { Text("Load More") }; Spacer() }
                }
                .accessibilityIdentifier("her-files-more")
            }
        }
        .listStyle(.insetGrouped)
        .searchable(text: $query, placement: .navigationBarDrawer(displayMode: .always), prompt: Text("Search names, or what Her said"))
        .safeAreaInset(edge: .top, spacing: 0) { tabs }
        .toolbar {
            if let sources {
                ToolbarItem(placement: .topBarTrailing) {
                    let button = Button { choosing = true } label: {
                        Label("Filter by Source", systemImage: "line.3.horizontal.decrease")
                    }
                    .disabled(!online && source == nil)
                    .accessibilityValue(source.map { sourceName($0, sources.source($0)) } ?? "")
                    .accessibilityIdentifier("her-files-sources")
                    // Glass, as the bar's buttons are; filled with the tint while a source is chosen.
                    if source == nil { button } else { button.buttonStyle(.glassProminent) }
                }
            }
        }
        .sheet(isPresented: $choosing) {
            if let sources {
                HerSourcesSheet(sources: sources, media: media, source: source) { picked in
                    source = picked
                    choosing = false
                }
                .presentationDetents([.medium, .large])
            }
        }
        .overlay {
            if let shownList, shownList.files.isEmpty {
                let searching = !query.trimmingCharacters(in: .whitespaces).isEmpty || tab != .all || source != nil
                ContentUnavailableView {
                    Label(searching ? "Nothing Found" : "The files Her sends you are kept here", systemImage: searching ? "magnifyingglass" : "tray")
                } description: {
                    Text(searching ? "Try other words: names and what Her said with each file are both searched." : "Reports, sheets, pictures: every file she sends in the conversation can be found here again.")
                }
                .accessibilityIdentifier("her-files-empty")
            } else if shownList == nil, older {
                NeedsUpdateView(machineName: machine.machineName)
            } else if shownList == nil, let failed {
                ContentUnavailableView("Can’t Read the Files", systemImage: "exclamationmark.triangle", description: Text(failed))
            } else if shownList == nil {
                ProgressView()
            }
        }
        // The pages under this one are hidden, and their tasks with them: the environment's connection is this page's
        // to hold while it shows.
        .task(id: herReachable(machine) && machine.her.running) { if herReachable(machine) && machine.her.running { await chat.followEnvironment() } }
        .task(id: Asked(query: query.trimmingCharacters(in: .whitespaces), tab: tab, source: source, latest: bot.deliveriesLatest, online: online)) { await load() }
        .task(id: Asked(query: "", tab: .all, latest: bot.deliveriesLatest, online: online)) { await loadSources() }
        // What the list showed, from the first time it shows: leaving is having seen up to there.
        .onChange(of: shownList?.files.compactMap { $0.latest?.seq } ?? [], initial: true) {
            guard let s = shownList else { return }
            if seenBefore == nil { seenBefore = s.seen }
            sawUpTo = max(sawUpTo, s.files.compactMap { $0.latest?.seq }.max() ?? 0)
        }
        .onDisappear { markSeen() }
        .sheet(item: $shown) { s in
            HerDeliveryPage(chat: chat, bot: bot, file: s.file, version: s.version, canOpen: { canOpen(s.file, $0) },
                            show: { show(s.file, $0) }, reply: { reply(s.file, $0) })
        }
        .sheet(item: $sharing) { HerShareSheet(url: $0) }
    }

    // MARK: Reading

    /// Offline, or until her answers: the first page this iPhone keeps, searched and filtered here.
    private var keptList: HerDeliveries? {
        guard let kept = chat.kept?.deliveries else { return nil }
        let q = query.trimmingCharacters(in: .whitespaces).lowercased()
        let files = kept.files.filter { f in
            (tab == .all || f.kind.isMedia == (tab == .media)) && (source.map(f.from) ?? true) && (q.isEmpty || ([f.name] + f.versions.flatMap { [$0.text ?? "", $0.source?.name ?? ""] }).contains { $0.lowercased().contains(q) })
        }
        return HerDeliveries(files: files, seen: kept.seen)
    }

    private func params(before: Int? = nil) -> JSON {
        var p: [String: JSON] = ["bot_id": .string(bot.id)]
        let q = query.trimmingCharacters(in: .whitespaces)
        if !q.isEmpty { p["query"] = .string(q) }
        if let media { p["media"] = .bool(media) }
        if let source { p["source"] = source.json }
        if let before { p["before"] = .number(Double(before)) }
        return .object(p)
    }

    private func load() async {
        guard online else { return }
        do {
            let page = try await chat.callAlone("deliveries/list", params())
            list = HerDeliveries(page)?.only(media: media)
            failed = nil
            older = false
        } catch {
            older = needsUpdate(error)
            failed = error.localizedDescription
        }
    }

    private func loadMore() {
        guard let next = list?.next, !loadingMore else { return }
        loadingMore = true
        Task {
            defer { loadingMore = false }
            if let j = try? await chat.callAlone("deliveries/list", params(before: next)), let page = HerDeliveries(j), let had = list {
                list = had.joined(page.only(media: media))
            }
        }
    }

    /// Where the files came from, for the filter: none from a her that cannot say, and then no filter button.
    private func loadSources() async {
        guard online else { return }
        sources = (try? await chat.callAlone("deliveries/sources", ["bot_id": .string(bot.id)])).flatMap(HerDeliverySources.init)
    }

    private func markSeen() {
        guard sawUpTo > (seenBefore ?? 0), herReachable(machine) else { return }
        let (chat, id, seq) = (chat, bot.id, sawUpTo)
        seenBefore = seq
        Task { await chat.seeDeliveries(bot: id, upTo: seq) }
    }

    private func dayTitle(_ day: HerDeliveryDay) -> String {
        switch day {
        case .today: String(localized: "Today")
        case .yesterday: String(localized: "Yesterday")
        case .week: String(localized: "Past 7 Days")
        case .earlier: String(localized: "Earlier")
        }
    }

    // MARK: A row

    /// Which of the files show.
    enum Tab: String, Hashable { case all, documents, media }

    private var tabs: some View {
        VStack(spacing: 10) {
            Picker("Show", selection: $tab) {
                Text("All").tag(Tab.all)
                Text("Documents").tag(Tab.documents)
                Text("Media").tag(Tab.media)
            }
            .pickerStyle(.segmented)
            .accessibilityIdentifier("her-files-tabs")
            if let source { chosen(source) }
        }
        .padding(.horizontal, 16).padding(.vertical, 8)
        .background(.bar)
    }

    /// The source whose files show: its name and how many, and clearing it.
    private func chosen(_ f: HerSourceFilter) -> some View {
        let s = sources?.source(f)
        return HStack(spacing: 8) {
            Image(systemName: sourceSymbol(f)).font(.subheadline.weight(.semibold))
            HStack(spacing: 0) {
                Text(verbatim: sourceName(f, s)).fontWeight(.semibold).lineLimit(1)
                if let s { Text(verbatim: " · " + String(localized: "\(s.count(media: media)) files")).foregroundStyle(Color.accentColor.opacity(0.8)).layoutPriority(1) }
            }
            .font(.subheadline)
            Spacer(minLength: 4)
            Button { source = nil } label: {
                Image(systemName: "xmark").font(.caption.weight(.bold)).frame(width: 26, height: 26)
                    .background(Color.accentColor.opacity(0.16), in: .circle)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Show All Sources")
            .accessibilityIdentifier("her-files-source-clear")
        }
        .foregroundStyle(Color.accentColor)
        .padding(.leading, 12).padding(.trailing, 7).padding(.vertical, 7)
        .background(Color.accentColor.opacity(0.12), in: .rect(cornerRadius: 14, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("her-files-source")
    }

    /// A row, two lines: the name, with the new dot and when it was last sent; then Her's words with it (its size
    /// when she said nothing), and how many versions there are. The rest (its size, where it came from, its
    /// versions one by one) is on its page.
    private func row(_ f: HerDeliveredFile, seen: Int) -> some View {
        let v = f.versions[0]
        let earlier = bot.sessionID != nil && v.sessionID != bot.sessionID
        let fresh = f.isNew(seen: seen)
        let size = ByteCountFormatter.string(fromByteCount: Int64(v.size), countStyle: .file)
        return Button { open(f, 0) } label: {
            HStack(spacing: 12) {
                HerFileIcon(file: f)
                VStack(alignment: .leading, spacing: 3) {
                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                        Text(f.name).font(.body.weight(.medium)).lineLimit(1).truncationMode(.middle)
                        if fresh { Circle().fill(Color.accentColor).frame(width: 8, height: 8) }
                        Spacer(minLength: 8)
                        Text(when(v.sentAt)).font(.footnote).foregroundStyle(.secondary).monospacedDigit().layoutPriority(1)
                    }
                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                        Text(v.text ?? size).font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
                        Spacer(minLength: 8)
                        if f.versions.count > 1 {
                            Text("\(f.versions.count) versions")
                                .font(.caption.weight(.semibold)).foregroundStyle(Color.accentColor)
                                .padding(.horizontal, 7).padding(.vertical, 2)
                                .background(Color.accentColor.opacity(0.12), in: .capsule)
                                .layoutPriority(1)
                        }
                    }
                }
            }
            .padding(.vertical, 2)
            .contentShape(.rect)
            .opacity(canOpen(f, 0) ? 1 : 0.45)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(verbatim: [f.name, fresh ? String(localized: "New file") : nil, v.text, when(v.sentAt),
                                            f.versions.count > 1 ? String(localized: "\(f.versions.count) versions") : nil].compactMap { $0 }.joined(separator: ", ")))
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("her-file-row-\(f.name)")
        .contextMenu {
            Button { open(f, 0) } label: { Label("Preview", systemImage: "eye") }
            if !earlier { Button { show(f, 0) } label: { Label("Show in Conversation", systemImage: "text.bubble") } }
            Button { reply(f, 0) } label: { Label("Reply to Have Her Revise", systemImage: "arrowshape.turn.up.left") }
            Button { share(f, 0) } label: { Label("Share…", systemImage: "square.and.arrow.up") }
        }
    }

    /// When a file was last sent, as its row says it: the time under Today and Yesterday, the day before that.
    private func when(_ d: Date) -> String {
        Calendar.current.isDateInToday(d) || Calendar.current.isDateInYesterday(d)
            ? d.formatted(date: .omitted, time: .shortened)
            : d.formatted(.dateTime.month().day())
    }

    // MARK: Acting on a file

    /// A version can be opened: her machine is there, or this iPhone keeps it.
    private func canOpen(_ f: HerDeliveredFile, _ version: Int) -> Bool {
        guard f.versions.indices.contains(version) else { return false }
        return online || chat.keeps(f.versions[version].sha256, name: f.name)
    }

    private func open(_ f: HerDeliveredFile, _ version: Int) {
        guard canOpen(f, version) else { return model.show(String(localized: "\(machine.machineName) is offline; this file opens once it is back")) }
        shown = Shown(file: f, version: version)
    }

    private func show(_ f: HerDeliveredFile, _ version: Int) {
        let v = f.versions[version]
        if let current = bot.sessionID, v.sessionID != current { return model.show(String(localized: "This came from a cleared conversation")) }
        shown = nil
        model.openHer(machineID: chat.machineID)
        Task { await chat.reveal(message: v.callID) }
    }

    private func reply(_ f: HerDeliveredFile, _ version: Int) {
        shown = nil
        chat.reply(f.quote(f.versions[version]))
        model.openHer(machineID: chat.machineID)
    }

    private func share(_ f: HerDeliveredFile, _ version: Int) {
        guard canOpen(f, version) else { return model.show(String(localized: "\(machine.machineName) is offline; this file opens once it is back")) }
        let v = f.versions[version]
        Task { if let url = await chat.download(f.path, snapshot: v.snapshot, sha256: v.sha256) { sharing = url } }
    }
}

/// A file's icon: its extension on its kind's colour.
private struct HerFileIcon: View {
    let file: HerDeliveredFile

    var body: some View {
        let ext = (file.name as NSString).pathExtension.uppercased()
        RoundedRectangle(cornerRadius: 7, style: .continuous)
            .fill(tint.opacity(0.14))
            .frame(width: 36, height: 44)
            .overlay(alignment: .bottom) {
                Text(verbatim: String(ext.prefix(4))).font(.system(size: 8.5, weight: .bold)).foregroundStyle(tint).padding(.bottom, 5)
            }
            .overlay(alignment: .top) {
                VStack(spacing: 3) { ForEach(0..<3, id: \.self) { _ in Capsule().fill(tint.opacity(0.45)).frame(height: 2) } }
                    .padding(.horizontal, 8).padding(.top, 9)
            }
            .accessibilityHidden(true)
    }

    private var tint: Color {
        switch (file.kind, (file.name as NSString).pathExtension.lowercased()) {
        case (_, "pdf"): .red
        case (_, "md"), (.image, _), (.other, _): .gray
        case (.video, _), (.audio, _): .pink
        case (.doc, _): .blue
        case (.sheet, _): .green
        case (.slide, _): .orange
        case (.code, _): .purple
        }
    }
}

/// A picture Her sent, in the grid of pictures: read once it shows.
private struct HerFileThumb: View {
    let chat: HerChat
    let file: HerDeliveredFile
    let fresh: Bool
    let enabled: Bool
    let open: () -> Void
    @State private var picture: UIImage?

    var body: some View {
        Button(action: open) {
            Color(.tertiarySystemFill)
                .aspectRatio(1, contentMode: .fit)
                .overlay {
                    if let picture { Image(uiImage: picture).resizable().scaledToFill() } else { HerFileIcon(file: file) }
                }
                .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
                .overlay(alignment: .topTrailing) {
                    if fresh { Circle().fill(Color.accentColor).frame(width: 9, height: 9).padding(7) }
                }
                .opacity(enabled ? 1 : 0.45)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(Text(file.name))
        .accessibilityIdentifier("her-file-thumb-\(file.name)")
        .task(id: file.latest?.seq) {
            guard enabled, file.kind == .image, let v = file.latest, let url = await chat.download(file.path, snapshot: v.snapshot, sha256: v.sha256) else { return }
            picture = UIImage(contentsOfFile: url.path)
        }
    }
}

/// A version of a file on a page of its own: what it shows as, the switch between versions, and under it what Her
/// said with it, Show in Conversation and Reply.
private struct HerDeliveryPage: View {
    @Environment(\.dismiss) private var dismiss
    let chat: HerChat
    let bot: HerBot
    let file: HerDeliveredFile
    @State var version: Int
    let canOpen: (Int) -> Bool
    let show: (Int) -> Void
    let reply: (Int) -> Void
    @State private var url: URL?
    @State private var text: (String, HerFileKind)?
    @State private var failed = false

    var body: some View {
        let v = file.versions[version]
        let earlier = bot.sessionID != nil && v.sessionID != bot.sessionID
        NavigationStack {
            content
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .safeAreaInset(edge: .top, spacing: 0) {
                    if file.versions.count > 1 {
                        Picker("Version", selection: $version) {
                            ForEach(file.versions.indices, id: \.self) { i in Text("Version \(file.versions.count - i)").tag(i) }
                        }
                        .pickerStyle(.segmented)
                        .padding(.horizontal, 16).padding(.vertical, 8)
                        .background(.bar)
                        .accessibilityIdentifier("her-delivery-versions")
                    }
                }
                .safeAreaInset(edge: .bottom, spacing: 0) {
                    VStack(alignment: .leading, spacing: 10) {
                        HStack(alignment: .top, spacing: 10) {
                            HerAvatar(size: 30)
                            VStack(alignment: .leading, spacing: 2) {
                                Text("\(bot.name) · \(v.sentAt, format: .dateTime.month().day().hour().minute())").font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                                if let words = v.text { Text(words).font(.subheadline).lineLimit(2).accessibilityIdentifier("her-delivery-words") }
                            }
                        }
                        HStack(spacing: 8) {
                            if !earlier {
                                Button { show(version) } label: { Label("Show in Conversation", systemImage: "text.bubble").lineLimit(1).minimumScaleFactor(0.8).frame(maxWidth: .infinity) }
                                    .buttonStyle(.glass).controlSize(.large)
                                    .accessibilityIdentifier("her-delivery-show")
                            }
                            Button { reply(version) } label: { Label("Reply", systemImage: "arrowshape.turn.up.left").frame(maxWidth: .infinity) }
                                .buttonStyle(.glassProminent).controlSize(.large)
                                .accessibilityIdentifier("her-delivery-reply")
                        }
                    }
                    .padding(14)
                    .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 24, style: .continuous))
                    .padding(.horizontal, 10).padding(.bottom, 8)
                }
                .navigationTitle(file.name)
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    CloseButton()
                    ToolbarItem(placement: .primaryAction) {
                        if let url { ShareLink(item: url).accessibilityIdentifier("her-delivery-share") }
                    }
                }
        }
        .accessibilityIdentifier("her-delivery-page")
        .task(id: v.seq) { await read(v) }
    }

    @ViewBuilder private var content: some View {
        if let read = text {
            ScrollView {
                switch read.1 {
                case .markdown:
                    MarkdownText(text: read.0, lazy: true).frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal, 20)
                case .code(let lang):
                    HerCodeLines(text: read.0, lang: lang).padding(.horizontal, 13)
                }
            }
            .contentMargins(.top, 13, for: .scrollContent)
            .accessibilityIdentifier("her-delivery-text")
        } else if let url {
            HerQuickLook(url: url).ignoresSafeArea(edges: .bottom)
        } else if failed {
            ContentUnavailableView("Can’t Open This File", systemImage: "exclamationmark.triangle")
        } else {
            ProgressView()
        }
    }

    private func read(_ v: HerDelivery) async {
        url = nil
        text = nil
        failed = false
        guard canOpen(version), let got = await chat.download(file.path, snapshot: v.snapshot, sha256: v.sha256) else {
            failed = true
            return
        }
        let size = (try? got.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? .max
        if let kind = HerFileKind(name: file.name, size: size), let words = try? String(contentsOf: got, encoding: .utf8) { text = (words, kind) }
        url = got
    }
}

/// Quick Look's page for a file brought here.
struct HerQuickLook: UIViewControllerRepresentable {
    let url: URL

    func makeUIViewController(context: Context) -> QLPreviewController {
        let c = QLPreviewController()
        c.dataSource = context.coordinator
        return c
    }

    func updateUIViewController(_ c: QLPreviewController, context: Context) {
        if context.coordinator.url != url {
            context.coordinator.url = url
            c.reloadData()
        }
    }

    func makeCoordinator() -> Source { Source(url: url) }

    final class Source: NSObject, QLPreviewControllerDataSource {
        var url: URL
        init(url: URL) { self.url = url }
        func numberOfPreviewItems(in controller: QLPreviewController) -> Int { 1 }
        func previewController(_ controller: QLPreviewController, previewItemAt index: Int) -> QLPreviewItem { url as NSURL }
    }
}

/// The system's share sheet for a file brought here.
struct HerShareSheet: UIViewControllerRepresentable {
    let url: URL
    func makeUIViewController(context: Context) -> UIActivityViewController { UIActivityViewController(activityItems: [url], applicationActivities: nil) }
    func updateUIViewController(_ c: UIActivityViewController, context: Context) {}
}

extension URL: @retroactive Identifiable {
    public var id: String { absoluteString }
}

/// A source's symbol: a routine's clock, a session's code, the conversation's bubble.
private func sourceSymbol(_ f: HerSourceFilter) -> String {
    switch f {
    case .conversation: "bubble.left"
    case .routine: "clock"
    case .session: "chevron.left.forwardslash.chevron.right"
    }
}

/// What a source is called: the conversation by what it is, the rest by their names as her says them now.
private func sourceName(_ f: HerSourceFilter, _ s: HerDeliverySourceCount?) -> String {
    switch f {
    case .conversation: String(localized: "Asked in the Conversation")
    case .routine(let id): s?.name ?? id
    case .session(let name): s?.name ?? name
    }
}

/// The filter, from the bottom: all sources; the conversation; the routines and the coding sessions that sent files,
/// the latest first, each with how many files the tab shows (one with none there can't be chosen); with many, a
/// search. A deleted routine stays, as its files do; the routines that sent no file are a line at the end.
private struct HerSourcesSheet: View {
    let sources: HerDeliverySources
    let media: Bool?
    let source: HerSourceFilter?
    let pick: (HerSourceFilter?) -> Void
    @State private var query = ""
    @Environment(\.dismiss) private var dismiss

    /// With more sources than this, a search.
    private static let many = 8

    var body: some View {
        let g = sources.groups(query: query)
        let searching = !query.trimmingCharacters(in: .whitespaces).isEmpty
        NavigationStack {
            List {
                if !searching {
                    Section {
                        option(icon: "tray.2", name: String(localized: "All Sources"), count: sources.sources.reduce(0) { $0 + $1.count(media: media) }, on: source == nil, gone: false) { pick(nil) }
                            .accessibilityIdentifier("her-source-all")
                    }
                }
                if let c = g.conversation {
                    Section { row(c) }
                }
                if !g.routines.isEmpty {
                    Section("Routines") { ForEach(g.routines) { row($0) } }
                }
                if !g.sessions.isEmpty {
                    Section("Coding Sessions") { ForEach(g.sessions) { row($0) } }
                }
                if !searching, sources.silent > 0 {
                    Section {} footer: {
                        Text("\(sources.silent) more routines haven’t sent a file, so they aren’t listed.")
                    }
                }
            }
            .overlay {
                if searching, g.conversation == nil, g.routines.isEmpty, g.sessions.isEmpty {
                    ContentUnavailableView.search(text: query)
                }
            }
            .navigationTitle("Filter by Source")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                if source != nil {
                    ToolbarItem(placement: .topBarLeading) {
                        Button("Clear") { pick(nil) }.accessibilityIdentifier("her-source-clear")
                    }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button(role: .close) { dismiss() }
                }
            }
            .modifier(SearchWhenMany(many: sources.sources.count > Self.many, query: $query))
        }
    }

    private func row(_ s: HerDeliverySourceCount) -> some View {
        option(icon: sourceSymbol(s.filter), name: sourceName(s.filter, s), count: s.count(media: media), on: s.filter == source, gone: s.gone) { pick(s.filter) }
            .accessibilityIdentifier("her-source-\(sourceName(s.filter, s))")
    }

    private func option(icon: String, name: String, count: Int, on: Bool, gone: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 12) {
                Image(systemName: icon)
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(on ? Color.accentColor : .secondary)
                    .frame(width: 30, height: 30)
                    .background((on ? Color.accentColor.opacity(0.16) : Color(.tertiarySystemFill)), in: .circle)
                VStack(alignment: .leading, spacing: 1) {
                    Text(verbatim: name).lineLimit(1)
                    if gone { Text("Deleted").font(.caption).foregroundStyle(.secondary) }
                }
                Spacer(minLength: 8)
                Text(verbatim: "\(count)").foregroundStyle(.secondary).monospacedDigit()
                Image(systemName: "checkmark").font(.subheadline.weight(.semibold)).foregroundStyle(Color.accentColor).opacity(on ? 1 : 0)
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .disabled(count == 0 && !on)
        .opacity(count == 0 && !on ? 0.42 : 1)
        .accessibilityAddTraits(on ? .isSelected : [])
    }
}

/// A search over the sources, only when there are many.
private struct SearchWhenMany: ViewModifier {
    let many: Bool
    @Binding var query: String

    func body(content: Content) -> some View {
        if many {
            content.searchable(text: $query, placement: .navigationBarDrawer(displayMode: .always), prompt: Text("Search routines"))
        } else {
            content
        }
    }
}
