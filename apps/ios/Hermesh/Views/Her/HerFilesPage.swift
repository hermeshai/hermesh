import HermeshHer
import HermeshKit
import QuickLook
import SwiftUI

// The files Her sent, in a place of their own (docs/her-deliverables.md §3.3): a page pushed from her page's top,
// as the Mac's panel lists them. Newest first, searched and filtered by her, grouped here by time or by what each
// came from; a file sent again from the same path is one row, its versions under it. A file opens on a page of its
// own, from the copy her kept of it as it was sent, with what Her said with it, Show in Conversation and Reply under
// it. Once read, a file is kept on this iPhone by its hash, so it opens while her machine is offline too.

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
    @State private var kind: HerDeliveredFile.Kind?
    @AppStorage("herFilesGroup") private var grouping = HerDeliveryGrouping.time.rawValue
    @State private var list: HerDeliveries?
    @State private var failed: String?
    @State private var older = false
    @State private var loadingMore = false
    @State private var expanded: Set<String> = []
    @State private var shown: Shown?
    @State private var sharing: URL?
    /// The latest send the list showed, and the mark it opened with: leaving is having seen it.
    @State private var sawUpTo = 0
    @State private var seenBefore: Int?

    private var online: Bool { herReachable(machine) && chat.envUp }

    /// What the list asks her for, again whenever it changes.
    private struct Asked: Equatable {
        var query: String
        var kind: String?
        var latest: Int
        var online: Bool
    }

    var body: some View {
        let shownList = list ?? keptList
        let by = HerDeliveryGrouping(rawValue: grouping) ?? .time
        let groups = shownList.map { herDeliveryGroups($0.files, by: by, now: model.now, zone: .current) } ?? []
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
                    if kind == .image {
                        LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 8), count: 3), spacing: 8) {
                            ForEach(g.files) { f in
                                HerFileThumb(chat: chat, file: f, fresh: f.isNew(seen: shownList?.seen ?? 0), enabled: canOpen(f, 0)) { open(f, 0) }
                            }
                        }
                        .listRowInsets(EdgeInsets(top: 8, leading: 16, bottom: 8, trailing: 16))
                    } else {
                        ForEach(g.files) { f in row(f, seen: shownList?.seen ?? 0, bySource: by == .source) }
                    }
                } header: {
                    HStack {
                        Text(groupTitle(g, by: by))
                        Text(verbatim: "\(g.files.count)").foregroundStyle(.tertiary)
                        Spacer()
                        if let s = g.source { Text(s.kind == .routine ? "Scheduled Task" : "Coding Session").foregroundStyle(.tertiary) }
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
        .safeAreaInset(edge: .top, spacing: 0) { kinds }
        .overlay {
            if let shownList, shownList.files.isEmpty {
                let searching = !query.trimmingCharacters(in: .whitespaces).isEmpty || kind != nil
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
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Menu {
                    Picker("Group", selection: $grouping) {
                        Text("By Time").tag(HerDeliveryGrouping.time.rawValue)
                        Text("By Source").tag(HerDeliveryGrouping.source.rawValue)
                    }
                } label: {
                    Label(by == .time ? "By Time" : "By Source", systemImage: "line.3.horizontal.decrease")
                        .labelStyle(.titleAndIcon)
                }
                .accessibilityIdentifier("her-files-group")
            }
        }
        // The pages under this one are hidden, and their tasks with them: the environment's connection is this page's
        // to hold while it shows.
        .task(id: herReachable(machine) && machine.her.running) { if herReachable(machine) && machine.her.running { await chat.followEnvironment() } }
        .task(id: Asked(query: query.trimmingCharacters(in: .whitespaces), kind: kind?.rawValue, latest: bot.deliveriesLatest, online: online)) { await load() }
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
            (kind == nil || f.kind == kind) && (q.isEmpty || ([f.name] + f.versions.flatMap { [$0.text ?? "", $0.source?.name ?? ""] }).contains { $0.lowercased().contains(q) })
        }
        return HerDeliveries(files: files, seen: kept.seen)
    }

    private func params(before: Int? = nil) -> JSON {
        var p: [String: JSON] = ["bot_id": .string(bot.id)]
        let q = query.trimmingCharacters(in: .whitespaces)
        if !q.isEmpty { p["query"] = .string(q) }
        if let kind { p["kind"] = .string(kind.rawValue) }
        if let before { p["before"] = .number(Double(before)) }
        return .object(p)
    }

    private func load() async {
        guard online else { return }
        do {
            let page = try await chat.call("deliveries/list", params())
            list = HerDeliveries(page)
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
            if let j = try? await chat.call("deliveries/list", params(before: next)), let page = HerDeliveries(j), let had = list {
                list = had.joined(page)
            }
        }
    }

    private func markSeen() {
        guard sawUpTo > (seenBefore ?? 0), herReachable(machine) else { return }
        let (chat, id, seq) = (chat, bot.id, sawUpTo)
        seenBefore = seq
        Task { await chat.seeDeliveries(bot: id, upTo: seq) }
    }

    private func groupTitle(_ g: HerDeliveryGroup, by: HerDeliveryGrouping) -> String {
        switch by {
        case .time:
            switch HerDeliveryDay(rawValue: g.key) {
            case .today: String(localized: "Today")
            case .yesterday: String(localized: "Yesterday")
            case .week: String(localized: "Past 7 Days")
            default: String(localized: "Earlier")
            }
        case .source: g.source?.name ?? String(localized: "Asked in the Conversation")
        }
    }

    // MARK: A row

    private var kinds: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 6) {
                chip(nil, "All")
                chip(.doc, "Documents")
                chip(.sheet, "Sheets")
                chip(.slide, "Slides")
                chip(.image, "Pictures")
                chip(.code, "Code")
            }
            .padding(.horizontal, 16).padding(.vertical, 8)
        }
        .background(.bar)
    }

    private func chip(_ k: HerDeliveredFile.Kind?, _ title: LocalizedStringKey) -> some View {
        Button { kind = k } label: {
            Text(title).font(.subheadline.weight(.medium))
                .padding(.horizontal, 13).padding(.vertical, 6)
                .foregroundStyle(kind == k ? Color(.systemBackground) : .primary)
                .background(kind == k ? Color.primary : Color(.tertiarySystemFill), in: .capsule)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(kind == k ? .isSelected : [])
        .accessibilityIdentifier("her-files-kind-\(k?.rawValue ?? "all")")
    }

    @ViewBuilder private func row(_ f: HerDeliveredFile, seen: Int, bySource: Bool) -> some View {
        let v = f.versions[0]
        let earlier = bot.sessionID != nil && v.sessionID != bot.sessionID
        Button { open(f, 0) } label: {
            HStack(alignment: .top, spacing: 12) {
                HerFileIcon(file: f)
                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 6) {
                        Text(f.name).font(.body.weight(.medium)).lineLimit(1).truncationMode(.middle)
                        if f.isNew(seen: seen) {
                            Circle().fill(Color.accentColor).frame(width: 8, height: 8).accessibilityLabel(Text("New file"))
                        }
                    }
                    if let text = v.text { Text(text).font(.subheadline).foregroundStyle(.secondary).lineLimit(1) }
                    HStack(spacing: 6) {
                        Text(v.sentAt, format: .dateTime.month().day().hour().minute())
                        Text(ByteCountFormatter.string(fromByteCount: Int64(v.size), countStyle: .file))
                        if !bySource, let s = v.source { Label(s.name, systemImage: s.kind == .routine ? "clock" : "chevron.left.forwardslash.chevron.right").labelStyle(.titleAndIcon).lineLimit(1) }
                        if earlier { Text("From a cleared conversation").lineLimit(1) }
                    }
                    .font(.caption).foregroundStyle(.secondary)
                }
                Spacer(minLength: 0)
            }
            .contentShape(.rect)
            .opacity(canOpen(f, 0) ? 1 : 0.45)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("her-file-row-\(f.name)")
        .contextMenu {
            Button { open(f, 0) } label: { Label("Preview", systemImage: "eye") }
            if !earlier { Button { show(f, 0) } label: { Label("Show in Conversation", systemImage: "text.bubble") } }
            Button { reply(f, 0) } label: { Label("Reply to Have Her Revise", systemImage: "arrowshape.turn.up.left") }
            Button { share(f, 0) } label: { Label("Share…", systemImage: "square.and.arrow.up") }
        }
        if f.versions.count > 1 {
            Button {
                withAnimation(.snappy) { if expanded.contains(f.path) { expanded.remove(f.path) } else { expanded.insert(f.path) } }
            } label: {
                Label("\(f.versions.count) versions", systemImage: expanded.contains(f.path) ? "chevron.up" : "chevron.down")
                    .font(.footnote.weight(.semibold)).foregroundStyle(Color.accentColor)
            }
            .buttonStyle(.plain)
            .padding(.leading, 48)
            .accessibilityIdentifier("her-file-versions-\(f.name)")
            if expanded.contains(f.path) {
                ForEach(Array(f.versions.enumerated()), id: \.element.seq) { i, x in
                    Button { open(f, i) } label: {
                        VStack(alignment: .leading, spacing: 2) {
                            HStack(spacing: 6) {
                                Text("Version \(f.versions.count - i)").font(.subheadline.weight(.medium))
                                if i == 0 { Text("Latest").font(.caption).foregroundStyle(.secondary) }
                                Text(x.sentAt, format: .dateTime.month().day().hour().minute()).font(.caption).foregroundStyle(.secondary)
                            }
                            if let text = x.text { Text(text).font(.footnote).foregroundStyle(.secondary).lineLimit(1) }
                        }
                        .padding(.leading, 48)
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("her-file-version-\(f.versions.count - i)")
                }
            }
        }
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
            guard enabled, let v = file.latest, let url = await chat.download(file.path, snapshot: v.snapshot, sha256: v.sha256) else { return }
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
private struct HerQuickLook: UIViewControllerRepresentable {
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
private struct HerShareSheet: UIViewControllerRepresentable {
    let url: URL
    func makeUIViewController(context: Context) -> UIActivityViewController { UIActivityViewController(activityItems: [url], applicationActivities: nil) }
    func updateUIViewController(_ c: UIActivityViewController, context: Context) {}
}

extension URL: @retroactive Identifiable {
    public var id: String { absoluteString }
}
