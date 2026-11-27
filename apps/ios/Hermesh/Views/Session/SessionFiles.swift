import HermeshKit
import HermeshProto
import HermeshSessions
import QuickLook
import SwiftUI
import UniformTypeIdentifiers
import WebKit

// A coding session's files on the iPhone (docs/session-files.md §4), as the Mac shows them: the
// files a reply links are tags, which the session's machine is asked about once per reply; a
// tap opens the file on a page of its own, as the reply's turn left it, with the current one a
// tap away. A page runs with no network, reading its own folder from the machine; code opens
// at the lines the reply named; what the app does not show itself, Quick Look shows.

// MARK: Tags in a reply

/// A path a reply wrote, in a link or in inline code (HermeshSessions' fileOfLink, fileOfCode).
nonisolated struct FileRefMark: Hashable, Sendable {
    let path: String
    let line: Int?
    let endLine: Int?
    /// From inline code: shown as a tag only once the machine says it is there.
    let code: Bool
}

nonisolated enum FileRefAttribute: AttributedStringKey {
    typealias Value = FileRefMark
    static let name = "hermesh.fileRef"
}

extension EnvironmentValues {
    /// The session whose replies are shown, for their tags; nil outside a session (Her's words).
    @Entry var sessionFiles: SessionFilesModel?
    /// The turn a reply belongs to: its links open the files as that turn left them.
    @Entry var filesRun: String = ""
}

/// Keeps recent sessions' resolved file tags in memory across page opens, as `Conversations` keeps projections.
@MainActor final class SessionFilesCache {
    private let keeps = 20
    private var held: [String: SessionFilesModel] = [:]
    private var order: [String] = []

    func model(_ sessionID: String, api: AppAPI?) -> SessionFilesModel {
        if sessionID.isEmpty { return SessionFilesModel(sessionID: "", api: api) }
        let m = held[sessionID] ?? SessionFilesModel(sessionID: sessionID, api: api)
        m.api = api
        held[sessionID] = m
        order.removeAll { $0 == sessionID }
        order.append(sessionID)
        while order.count > keeps { held[order.removeFirst()] = nil }
        return m
    }

    func forget(_ sessionID: String) {
        held[sessionID] = nil
        order.removeAll { $0 == sessionID }
    }

    func forgetAll() {
        held = [:]
        order = []
    }
}

/// What the session's machine said the paths its replies wrote are, asked a frame's worth at a time and concurrently per turn.
@MainActor @Observable final class SessionFilesModel {
    @ObservationIgnored var sessionID: String
    @ObservationIgnored var api: AppAPI?
    private(set) var known: [String: Hermesh_Session_V1_ResolvedFile] = [:]
    var lightbox: SessionLightboxPresentation?
    @ObservationIgnored private var galleriesByRun: [String: [[ReplyImageItem]]] = [:]
    @ObservationIgnored private var askedAt: [String: ContinuousClock.Instant] = [:]
    @ObservationIgnored private var pending: [String: Set<String>] = [:]
    @ObservationIgnored private var order: [String] = []
    @ObservationIgnored private var scheduled = false

    /// A path read before its turn had a checkpoint (`turn == 0`) may be asked again after this long, as on the Mac.
    private static let nowTTL: Duration = .seconds(10)

    init(sessionID: String, api: AppAPI? = nil) {
        self.sessionID = sessionID
        self.api = api
    }

    private func key(_ run: String, _ path: String) -> String { "\(run)\u{0}\(path)" }

    func info(run: String, path: String) -> Hermesh_Session_V1_ResolvedFile? { known[key(run, path)] }

    func registerGallery(run: String, gallery: [ReplyImageItem]) {
        guard !gallery.isEmpty else { return }
        var list = galleriesByRun[run] ?? []
        if !list.contains(gallery) { list.append(gallery) }
        galleriesByRun[run] = list
    }

    func gallery(run: String, containing path: String) -> [ReplyImageItem]? {
        galleriesByRun[run]?.first { g in g.contains { sameImagePath($0.path, path) } }
    }

    func openLightbox(run: String, path: String, machineID: String = "", gallery: [ReplyImageItem]? = nil) {
        let list = gallery ?? self.gallery(run: run, containing: path) ?? [ReplyImageItem(path: path, caption: "")]
        let idx = list.firstIndex { sameImagePath($0.path, path) } ?? 0
        lightbox = SessionLightboxPresentation(sessionID: sessionID, machineID: machineID, run: run, items: list.map(LightboxItem.file), initialIndex: idx)
    }

    // MARK: The folder's tree, as it is now (the drawer on a file's page, the side panel's tree)

    /// A folder's entries once read, or why not.
    private(set) var dirs: [String: Result<Hermesh_Session_V1_ListFilesResponse, Error>] = [:]
    /// The folders opened in the tree.
    var expanded: Set<String> = []

    func listDir(_ dir: String) async {
        guard let api, !sessionID.isEmpty else { return }
        do { dirs[dir] = .success(try await api.listFiles(sessionID, run: "", dir: dir)) } catch { dirs[dir] = .failure(error) }
    }

    func toggleDir(_ dir: String) {
        if expanded.remove(dir) == nil {
            expanded.insert(dir)
            if dirs[dir] == nil { Task { await listDir(dir) } }
        }
    }

    /// The tree opened down to a file: every folder above it.
    func reveal(_ path: String) {
        let parts = path.split(separator: "/").dropLast()
        for i in parts.indices {
            let dir = parts[...i].joined(separator: "/")
            expanded.insert(dir)
            if dirs[dir] == nil { Task { await listDir(dir) } }
        }
        if dirs[""] == nil { Task { await listDir("") } }
    }

    // MARK: The side panel, on a wide screen (docs/session-files.md §15)

    /// Shown beside the conversation; taking the page whole.
    var panelOpen = false
    var panelMax = false
    /// Whether the unified bottom directory-tree drawer is up over the active file tab.
    var panelDrawer = false
    var tabs: [PanelTab] = []
    var activeTab: UUID?
    /// Shared file URL being presented from either the side panel or iPhone Duo's vertical bar.
    var sharing: URL?
    /// Resolved workspace folder per tab, for Copy Path.
    var folders: [UUID: String] = [:]

    /// Opens a file a reply names, or an attachment, in a tab of its own; the tab it already has comes forward.
    func openTab(run: String, path: String, line: Int? = nil, endLine: Int? = nil, attachment: Hermesh_Session_V1_AttachmentRef? = nil) {
        let kind: PanelTab.Kind = attachment == nil && fileView(path) == .page ? .page : attachment == nil ? .files : .attachment
        if let i = tabs.firstIndex(where: { $0.kind == kind && $0.run == run && $0.path == path && $0.attachment == attachment }) {
            if line != nil { tabs[i].line = line; tabs[i].endLine = endLine }
            activeTab = tabs[i].id
        } else {
            add(PanelTab(kind: kind, run: run, path: path, line: line, endLine: endLine, replyRun: run, attachment: attachment))
        }
        if attachment == nil, !path.isEmpty { reveal(path) }
        panelDrawer = false
        panelOpen = true
    }

    /// 项目文件: the tree alone, a files tab with no file yet.
    func openProject() {
        if let t = tabs.first(where: { $0.kind == .files && $0.path.isEmpty }) { activeTab = t.id } else { add(PanelTab(kind: .files)) }
        panelDrawer = false
        reveal("")
        panelOpen = true
    }

    /// From ⋯ › 项目文件 on a wide screen: pulls up the bottom tree drawer when a file tab is already in front, else opens 项目文件.
    func showProjectFiles() {
        if panelOpen, let t = tabs.first(where: { $0.id == activeTab }), t.kind == .files, !t.path.isEmpty {
            reveal(t.path)
            panelDrawer = true
        } else {
            openProject()
        }
    }

    /// A file picked in the tree: in the tab the tree is in, as a browser goes to a page; a .html is a page, its own tab; a picture, the Lightbox.
    func openFromTree(_ path: String, machineID: String = "") {
        if isSessionImage(path) {
            openLightbox(run: "", path: path, machineID: machineID)
            return
        }
        guard fileView(path) != .page, let i = tabs.firstIndex(where: { $0.id == activeTab }), tabs[i].kind == .files else {
            return openTab(run: "", path: path)
        }
        tabs[i].path = path
        tabs[i].run = ""
        tabs[i].replyRun = ""
        tabs[i].line = nil
        tabs[i].endLine = nil
        tabs[i].source = false
        reveal(path)
    }

    func closeTab(_ id: UUID) {
        guard let i = tabs.firstIndex(where: { $0.id == id }) else { return }
        tabs.remove(at: i)
        folders[id] = nil
        if activeTab == id {
            activeTab = tabs.indices.contains(i) ? tabs[i].id : tabs.last?.id
            panelDrawer = false
        }
        // The last one closed, the panel goes.
        if tabs.isEmpty { panelOpen = false; panelMax = false; panelDrawer = false }
    }

    private func add(_ tab: PanelTab) {
        let at = tabs.firstIndex(where: { $0.id == activeTab }).map { $0 + 1 } ?? tabs.count
        tabs.insert(tab, at: at)
        activeTab = tab.id
    }

    func want(run: String, paths: [String]) {
        guard !paths.isEmpty else { return }
        let now = ContinuousClock.now
        var added = false
        for p in paths {
            let k = key(run, p)
            if let had = known[k], had.turn > 0 || (askedAt[k].map { now - $0 < Self.nowTTL } ?? false) { continue }
            if known[k] == nil, let at = askedAt[k], now - at < Self.nowTTL { continue }
            askedAt[k] = now
            if pending[run] == nil { order.append(run) }
            pending[run, default: []].insert(p)
            added = true
        }
        guard added, !scheduled else { return }
        scheduled = true
        Task { [weak self] in
            await Task.yield()
            self?.flush()
        }
    }

    /// Re-asks paths that were resolved before their turn captured a checkpoint, once the turn ends.
    func refreshLive() {
        var retry: [String: [String]] = [:]
        for (k, f) in known where f.turn == 0 {
            let parts = k.split(separator: "\u{0}", maxSplits: 1, omittingEmptySubsequences: false).map(String.init)
            guard parts.count == 2, !parts[0].isEmpty else { continue }
            askedAt[k] = nil
            retry[parts[0], default: []].append(parts[1])
        }
        for (run, paths) in retry { want(run: run, paths: paths) }
    }

    private func flush() {
        let runs = order
        let batch = pending
        order = []
        pending = [:]
        scheduled = false
        guard let api, !sessionID.isEmpty else {
            for (run, set) in batch { for p in set { askedAt[key(run, p)] = nil } }
            return
        }
        let sid = sessionID
        // Newest replies first (the end of the conversation is in view), all turns in flight concurrently.
        for run in runs.reversed() {
            guard let set = batch[run], !set.isEmpty else { continue }
            let paths = Array(set)
            Task { [weak self] in
                do {
                    let res = try await api.resolveFiles(sid, run: run, paths: paths)
                    guard let self else { return }
                    for (i, f) in res.files.enumerated() where i < paths.count { self.known[self.key(run, paths[i])] = f }
                } catch {
                    // The machine away: explicit links stay tappable, and paths are asked again next time.
                    guard let self else { return }
                    for p in paths { self.askedAt[self.key(run, p)] = nil }
                }
            }
        }
    }
}

/// The paths a reply's blocks write, to ask the machine about.
func fileRefPaths(_ blocks: [MarkdownBlock]) -> [String] {
    var out: [String] = []
    func text(_ s: AttributedString) {
        for run in s.runs { if let m = run[FileRefAttribute.self], !out.contains(m.path) { out.append(m.path) } }
    }
    func walk(_ b: MarkdownBlock) {
        switch b {
        case .paragraph(let s), .heading(_, let s): text(s)
        case .images(let items, _):
            for it in items where !out.contains(it.path) { out.append(it.path) }
        case .list(_, _, let items): items.forEach { $0.blocks.forEach(walk) }
        case .quote(let bs), .callout(_, let bs): bs.forEach(walk)
        case .table(let head, let rows, _): head.forEach(text); rows.forEach { $0.forEach(text) }
        case .footnotes(let notes): notes.forEach { $0.forEach(walk) }
        default: break
        }
    }
    blocks.forEach(walk)
    return out
}

/// A tab of the side panel: a file at a turn ("" for as it is now; with no path, the tree alone: 项目文件); a .html
/// running; a file the owner sent with a prompt.
struct PanelTab: Identifiable, Equatable {
    enum Kind { case files, page, attachment }
    let id = UUID()
    var kind: Kind
    var run = ""
    var path = ""
    var line: Int?
    var endLine: Int?
    /// The turn of the reply it was opened from: where "Show Version at Reply" goes back to.
    var replyRun = ""
    var source = false
    var attachment: Hermesh_Session_V1_AttachmentRef?
}

/// Where a tag goes when tapped: the session page's openURL takes it.
enum FileRefLink {
    static let scheme = "hermesh-file-ref"

    static func url(run: String, path: String, line: Int?, endLine: Int?, folder: Bool = false) -> URL? {
        var c = URLComponents()
        c.scheme = scheme
        c.host = folder ? "folder" : "open"
        c.queryItems = [URLQueryItem(name: "r", value: run), URLQueryItem(name: "p", value: path)]
            + (line.map { [URLQueryItem(name: "l", value: String($0))] } ?? [])
            + (endLine.map { [URLQueryItem(name: "e", value: String($0))] } ?? [])
        return c.url
    }

    static func route(_ url: URL, session: String) -> Route? {
        guard url.scheme == scheme, let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems else { return nil }
        func v(_ n: String) -> String? { items.first { $0.name == n }?.value }
        guard let path = v("p") else { return nil }
        if url.host == "folder" { return .sessionFolder(session, path) }
        return .sessionFile(session, v("r") ?? "", path, Int(v("l") ?? "") ?? 0, Int(v("e") ?? "") ?? 0)
    }
}

/// A line of a reply with its file tags: explicit links show as clickable tags at once (grey once the machine says
/// nothing is there), inline code becomes a tag once found; math drawn as `mathText` draws it. No symbol before the
/// name, as the Mac has: an image in a Text moves its links' places for VoiceOver (and the tests) by the image's width.
func fileText(_ text: AttributedString, files: SessionFilesModel, run: String, size: CGFloat, code: Font) -> Text {
    var out = AttributedString()
    for r in text.runs {
        var part = AttributedString(text[r.range])
        guard let mark = r[FileRefAttribute.self] else {
            out += part
            continue
        }
        let info = files.info(run: run, path: mark.path)
        let found = info?.kind == .file || info?.kind == .folder
        if mark.code && !found {
            out += part
            continue
        }
        let gone = info?.kind == .missing || info?.kind == .outside
        let opens = info == nil ? !mark.code : found
        let folder = info.map { $0.kind == .folder } ?? (mark.path.hasSuffix("/") || mark.path.hasSuffix("\\"))
        let path = if let info, !gone, !info.path.isEmpty { info.path } else { mark.path }
        if let line = mark.line, !mark.code {
            var l = AttributedString(":\(line)" + (mark.endLine.map { "-\($0)" } ?? ""))
            l.font = code
            l.foregroundColor = .secondary
            part += l
        }
        var label = AttributedString("\u{2009}") + part + AttributedString("\u{2009}")
        label.backgroundColor = Palette.fill2
        label.foregroundColor = opens ? Color.accentColor : Color.secondary
        if opens, let url = FileRefLink.url(run: run, path: path, line: mark.line, endLine: mark.endLine, folder: folder) { label.link = url }
        if let line = mark.line, !mark.code, let at = label.range(of: ":\(line)") { label[at.lowerBound...].foregroundColor = .secondary }
        out += label
    }
    return mathText(out, size: size, code: code)
}

// MARK: A file's content, and its page

/// What a file shows as (HermeshSessions' fileView): its own code and text, Markdown as it reads, a page as it runs,
/// the rest in Quick Look; at its foot, the bar that says when a reply's file is as its turn left it. A file's page on
/// a narrow screen and a side panel's tab on a wide one both show it.
struct SessionFileContent: View {
    @Environment(AppModel.self) private var model
    let sessionID: String
    let path: String
    let line: Int?
    let endLine: Int?
    /// The reply's turn; "" when it was opened from the tree.
    let turnRun: String
    @Binding var run: String
    let source: Bool
    /// The session's folder, once the machine named it: for Copy Path.
    var onFolder: (String) -> Void = { _ in }
    @State private var read: Result<Hermesh_Session_V1_ReadFileResponse, Error>?
    @State private var local: URL?
    @State private var docFiles: SessionFilesModel

    init(sessionID: String, path: String, line: Int?, endLine: Int?, turnRun: String, run: Binding<String>, source: Bool, onFolder: @escaping (String) -> Void = { _ in }) {
        self.sessionID = sessionID
        self.path = path
        self.line = line
        self.endLine = endLine
        self.turnRun = turnRun
        _run = run
        self.source = source
        self.onFolder = onFolder
        _docFiles = State(initialValue: SessionFilesModel(sessionID: sessionID))
    }

    private var name: String { fileBaseName(path) }
    private var view: FileView { fileView(path) }
    private var quickLooks: Bool { [.image, .pdf, .media, .office, .other].contains(view) }

    var body: some View {
        let _ = { docFiles.sessionID = sessionID; docFiles.api = model.api }()
        content
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .safeAreaInset(edge: .bottom, spacing: 0) {
                if case .success(let res)? = read { VersionBar(res: res, run: $run, turnRun: turnRun) }
            }
            .fullScreenCover(item: Binding(get: { docFiles.lightbox }, set: { docFiles.lightbox = $0 })) {
                SessionImageLightbox(presentation: $0).environment(\.sessionFiles, docFiles)
            }
            .task(id: "\(run)\u{0}\(path)") { await load() }
    }

    @ViewBuilder private var content: some View {
        switch read {
        case nil:
            ProgressView()
        case .failure(let e)?:
            FileCard(name: name, text: errorText(e))
        case .success(let res)?:
            let filePath = res.path.isEmpty ? path : res.path
            if view == .page {
                SessionPageWeb(api: model.api, sessionID: sessionID, run: run, path: filePath).id(run)
            } else if quickLooks || view == .svg && !source {
                if view == .svg, !source {
                    SessionPageWeb(api: model.api, sessionID: sessionID, run: run, path: filePath).id(run)
                } else if let local {
                    HerQuickLook(url: local).ignoresSafeArea(edges: .bottom).accessibilityIdentifier("file-quicklook")
                } else {
                    ProgressView()
                }
            } else if res.kind == .binary {
                FileCard(name: name, text: String(localized: "This file isn’t text, so it isn’t shown here"))
            } else if res.kind == .tooLarge {
                FileCard(name: name, text: String(localized: "This file is \(ByteCountFormatter.string(fromByteCount: Int64(res.size), countStyle: .file)), too large to show here"))
            } else if view == .markdown && !source && line == nil {
                let slashDir = filePath.contains("/") ? String(filePath[..<filePath.lastIndex(of: "/")!]) : ""
                ScrollView {
                    MarkdownText(text: res.text, basePath: slashDir, lazy: true)
                        .environment(\.sessionFiles, docFiles)
                        .environment(\.filesRun, run)
                        .frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal, 20)
                }
                .contentMargins(.vertical, 13, for: .scrollContent)
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("file-markdown")
            } else {
                CodePage(text: res.text, name: name, target: line.map { $0 ... max($0, endLine ?? $0) })
            }
        }
    }

    private func errorText(_ e: Error) -> String {
        switch (e as? APIError)?.code {
        case "not_found": String(localized: "File not found")
        case "permission_denied": String(localized: "This file is outside the session’s folder and can’t be opened here")
        case "unavailable", "deadline_exceeded":
            String(localized: "\(model.machineName(model.session(sessionID)?.machineID ?? "")) can’t be reached right now")
        default: e.localizedDescription
        }
    }

    private func load() async {
        guard let api = model.api else { return }
        read = nil
        local = nil
        do {
            let res = try await api.readFile(sessionID, run: run, path: path)
            read = .success(res)
            onFolder(res.folder)
            if quickLooks { local = try await sessionFileCopy(api, sessionID: sessionID, run: run, path: res.path.isEmpty ? path : res.path) }
        } catch {
            read = .failure(error)
        }
    }
}

/// A session's file in a folder of its own on this iPhone, for Quick Look and Share.
func sessionFileCopy(_ api: AppAPI, sessionID: String, run: String, path: String) async throws -> URL {
    let got = try await api.readWholeFile(sessionID, run: run, path: path)
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent("Session Files/\(UUID().uuidString)", isDirectory: true)
    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    let name = fileBaseName(path)
    let url = dir.appendingPathComponent(name.isEmpty ? "file" : name)
    try got.data.write(to: url)
    return url
}

/// Share… and Copy Path, the menu a file's ⋯ opens on its page and on its tab.
struct SessionFileMenu: View {
    @Environment(AppModel.self) private var model
    let sessionID: String
    let run: String
    let path: String
    /// The session's folder, for the whole path; "" until the file was read.
    let folder: String
    @Binding var sharing: URL?

    var body: some View {
        Button("Share…", systemImage: "square.and.arrow.up") {
            Task {
                guard let api = model.api, let url = try? await sessionFileCopy(api, sessionID: sessionID, run: run, path: path) else { return }
                sharing = url
            }
        }
        .accessibilityIdentifier("file-share")
        Button("Copy Path", systemImage: "doc.on.doc") {
            UIPasteboard.general.string = SessionImageStore.fullPath(folder: folder, path: path)
            model.show(String(localized: "Copied"))
        }
        .accessibilityIdentifier("file-copy-path")
    }
}

/// A file's page, pushed (a narrow screen): the file, its name at the top (and, for a page, its folder under it, as
/// it has no tree), ⋯ with Share… and Copy Path; at the foot, the bar that pulls the folder's tree up (not for a page).
/// A file picked in the tree opens here, as the Mac's tab goes to it; a page picked there is pushed.
struct SessionFilePage: View {
    @Environment(AppModel.self) private var model
    let sessionID: String
    @State private var path: String
    @State private var line: Int?
    @State private var endLine: Int?
    @State private var turnRun: String
    @State private var run: String
    @State private var source = false
    @State private var folder = ""
    @State private var sharing: URL?
    @State private var drawer = false
    @State private var lightbox: SessionLightboxPresentation?

    init(sessionID: String, run: String, path: String, line: Int, endLine: Int) {
        self.sessionID = sessionID
        _path = State(initialValue: path)
        _line = State(initialValue: line > 0 ? line : nil)
        _endLine = State(initialValue: endLine > 0 ? endLine : nil)
        _turnRun = State(initialValue: run)
        _run = State(initialValue: run)
    }

    private var name: String { fileBaseName(path) }
    private var view: FileView { fileView(path) }
    private var dir: String { path.contains("/") ? String(path[..<path.lastIndex(of: "/")!]) : "" }
    /// A page is what it runs as: no source to switch to.
    private var sourceable: Bool { view == .markdown || view == .svg }

    var body: some View {
        SessionFileContent(sessionID: sessionID, path: path, line: line, endLine: endLine, turnRun: turnRun, run: $run, source: source) { folder = $0 }
            .id(path)
            .safeAreaInset(edge: .bottom, spacing: 0) {
                if view != .page {
                    SessionFileDrawerBar(project: model.session(sessionID)?.projectID ?? "", dir: dir) { drawer = true }
                }
            }
            .navigationTitle(name)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                // A page has no tree under it: where it is, under its name.
                if view == .page {
                    ToolbarItem(placement: .principal) {
                        VStack(spacing: 1) {
                            Text(verbatim: name).font(.subheadline.weight(.semibold))
                            if !dir.isEmpty { Text(verbatim: "\(dir)/").font(.caption2).foregroundStyle(.secondary) }
                        }
                        .lineLimit(1)
                        .truncationMode(.head)
                    }
                }
                // Beside the expanded menu (a wide screen), the page takes the menu's room, as a session's does; an
                // iPhone's page is the screen's already.
                if model.menuInView { ToolbarItem(placement: .topBarLeading) { WidenPageButton() } }
                if sourceable {
                    ToolbarItem(placement: .primaryAction) {
                        Button { source.toggle() } label: { Image(systemName: source ? "eye" : "chevron.left.forwardslash.chevron.right") }
                            .accessibilityLabel(source ? String(localized: "Preview") : String(localized: "Source"))
                            .accessibilityIdentifier("file-source")
                    }
                }
                ToolbarItem(placement: .primaryAction) {
                    Menu {
                        SessionFileMenu(sessionID: sessionID, run: run, path: path, folder: folder, sharing: $sharing)
                    } label: {
                        Image(systemName: "ellipsis")
                    }
                    .accessibilityLabel(String(localized: "More"))
                    .accessibilityIdentifier("file-more")
                }
            }
            .sheet(item: $sharing) { HerShareSheet(url: $0).presentationDetents([.medium, .large]) }
            .sheet(isPresented: $drawer) {
                SessionTreeSheet(sessionID: sessionID, activePath: path) { picked in
                    drawer = false
                    open(picked)
                }
                .presentationDetents([.medium, .large])
                .presentationDragIndicator(.visible)
            }
            .fullScreenCover(item: $lightbox) { SessionImageLightbox(presentation: $0) }
            // A container, so what is in it keeps its own identifiers (the bar, the code).
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("session-file-page")
    }

    /// A file picked in the tree: in place, the folder as it is now; a page is pushed; a picture, the Lightbox.
    private func open(_ picked: String) {
        if isSessionImage(picked) {
            lightbox = SessionLightboxPresentation(sessionID: sessionID, machineID: model.session(sessionID)?.machineID ?? "", run: "",
                                                   items: [.file(ReplyImageItem(path: picked, caption: ""))], initialIndex: 0)
        } else if fileView(picked) == .page {
            model.path.append(.sessionFile(sessionID, "", picked, 0, 0))
        } else {
            path = picked
            run = ""
            turnRun = ""
            line = nil
            endLine = nil
            source = false
        }
    }
}

/// The folder's tree, a pull away: the glass bar at the foot of a file's page or side-panel tab says where the file is.
struct SessionFileDrawerBar: View {
    let project: String
    let dir: String
    let action: () -> Void

    var body: some View {
        let place = [project, dir].filter { !$0.isEmpty }.joined(separator: "/")
        Button(action: action) {
            HStack(spacing: 11) {
                Image(systemName: "list.bullet.indent").foregroundStyle(Color.accentColor)
                VStack(alignment: .leading, spacing: 1) {
                    Text("Project Files").font(.subheadline.weight(.semibold)).foregroundStyle(.primary)
                    if !place.isEmpty {
                        Text(verbatim: place).font(.caption.monospaced()).foregroundStyle(.secondary).lineLimit(1).truncationMode(.head)
                    }
                }
                Spacer(minLength: 8)
                Image(systemName: "chevron.up").font(.footnote.weight(.semibold)).foregroundStyle(.tertiary)
            }
            .padding(.horizontal, 16)
            .frame(height: 50)
            .glassEffect(.regular.interactive(), in: .capsule)
            .contentShape(.capsule)
        }
        .buttonStyle(.plain)
        .padding(.horizontal, 10).padding(.top, 6).padding(.bottom, 6)
        .accessibilityIdentifier("file-drawer")
    }
}

/// What a turn's file is against the folder now, and the way to the other, as on the Mac.
private struct VersionBar: View {
    let res: Hermesh_Session_V1_ReadFileResponse
    @Binding var run: String
    let turnRun: String

    var body: some View {
        let (text, action): (String?, (String, String)?) = {
            if !run.isEmpty, res.turn > 0 {
                switch res.now {
                case .changed: return (String(localized: "This is the file as turn \(res.turn) left it; it has changed since"), (String(localized: "Show Current Version"), ""))
                case .gone: return (String(localized: "This is the file as turn \(res.turn) left it; it has since been deleted"), nil)
                case .unknown: return (String(localized: "The workspace was released; this is the file as turn \(res.turn) left it"), nil)
                default: return (nil, nil)
                }
            }
            if run.isEmpty, !turnRun.isEmpty { return (String(localized: "This is the current version"), (String(localized: "Show Version at Reply"), turnRun)) }
            return (nil, nil)
        }()
        if let text {
            HStack(spacing: 12) {
                Text(text).font(.footnote).foregroundStyle(.secondary).frame(maxWidth: .infinity, alignment: .leading)
                if let action {
                    Button(action.0) { run = action.1 }.font(.footnote).buttonStyle(.bordered).accessibilityIdentifier("file-version-switch")
                }
            }
            .padding(.horizontal, 16).padding(.vertical, 10)
            .background(.bar)
            .overlay(alignment: .top) { Rectangle().fill(Palette.separator).frame(height: 0.5) }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("file-version")
        }
    }
}

/// Why a file is not shown.
struct FileCard: View {
    let name: String
    let text: String

    var body: some View {
        VStack(spacing: 8) {
            Image(systemName: "doc").font(.largeTitle).foregroundStyle(.tertiary)
            Text(name).fontWeight(.medium)
            Text(text).font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center)
        }
        .padding(24)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("file-card")
    }
}

/// Code in numbered lines, the lines a reply pointed at marked and brought into view.
struct CodePage: View {
    let text: String
    let name: String
    let target: ClosedRange<Int>?

    var body: some View {
        let ext = (name as NSString).pathExtension.lowercased()
        ScrollViewReader { proxy in
            ScrollView([.vertical]) {
                HerCodeLines(text: text, lang: ext.isEmpty ? name.lowercased() : ext, target: target).padding(.horizontal, 13)
            }
            .contentMargins(.vertical, 13, for: .scrollContent)
            // The rows are laid out lazily: brought into view once they are, again a moment later in case they were not.
            .task(id: target) {
                guard let t = target else { return }
                for delay in [50, 250] {
                    try? await Task.sleep(for: .milliseconds(delay))
                    proxy.scrollTo(t.lowerBound - 1, anchor: UnitPoint(x: 0, y: 0.3))
                }
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("file-code")
    }
}

// MARK: The session's folder

/// A folder of the session's, as it is now (the Mac's file panel's tree, a level at a time): folders first, then
/// files, each by name as people read it; a folder opens a page of its own, a file its page. When the worktree is
/// gone, as the last turn left it.
struct SessionFolderPage: View {
    @Environment(AppModel.self) private var model
    let sessionID: String
    let dir: String
    @State private var list: Result<Hermesh_Session_V1_ListFilesResponse, Error>?
    @State private var lightbox: SessionLightboxPresentation?

    var body: some View {
        Group {
            switch list {
            case nil: ProgressView()
            case .failure(let e)?: FileCard(name: dir.isEmpty ? String(localized: "Project Files") : fileBaseName(dir), text: e.localizedDescription)
            case .success(let res)?:
                List {
                    if res.folderGone {
                        Text("The workspace was released; these are the files as turn \(res.turn) left them").font(.footnote).foregroundStyle(.secondary)
                    }
                    ForEach(res.entries, id: \.name) { e in
                        let path = res.dir.isEmpty ? e.name : "\(res.dir)/\(e.name)"
                        Button {
                            if !e.dir, isSessionImage(path) {
                                let mid = model.session(sessionID)?.machineID ?? ""
                                lightbox = SessionLightboxPresentation(sessionID: sessionID, machineID: mid, run: "", items: [.file(ReplyImageItem(path: path, caption: ""))], initialIndex: 0)
                            } else {
                                model.path.append(e.dir ? .sessionFolder(sessionID, path) : .sessionFile(sessionID, "", path, 0, 0))
                            }
                        } label: {
                            HStack(spacing: 10) {
                                Image(systemName: e.dir ? "folder" : "doc").foregroundStyle(.secondary).frame(width: 22)
                                Text(verbatim: e.name).foregroundStyle(.primary).lineLimit(1).truncationMode(.middle)
                                Spacer(minLength: 8)
                                if e.dir {
                                    Image(systemName: "chevron.forward").font(.footnote.weight(.semibold)).foregroundStyle(.tertiary)
                                } else {
                                    Text(ByteCountFormatter.string(fromByteCount: Int64(e.size), countStyle: .file)).font(.footnote).foregroundStyle(.secondary)
                                }
                            }
                        }
                        .accessibilityIdentifier("folder-entry-\(e.name)")
                    }
                }
                .refreshable { await load() }
            }
        }
        .navigationTitle(dir.isEmpty ? String(localized: "Project Files") : fileBaseName(dir))
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { if model.menuInView { ToolbarItem(placement: .topBarLeading) { WidenPageButton() } } }
        .fullScreenCover(item: $lightbox) { SessionImageLightbox(presentation: $0) }
        .task { await load() }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("session-folder-page")
    }

    private func load() async {
        guard let api = model.api else { return }
        do { list = .success(try await api.listFiles(sessionID, run: "", dir: dir)) } catch { list = .failure(error) }
    }
}

// MARK: A page, offline, reading its own folder

/// The kinds a page may load from its folder: no dotfiles, nothing else (the Mac's rule).
private let webKinds = #/(?i)\.(html?|css|js|mjs|json|map|png|jpe?g|gif|webp|svg|ico|avif|woff2?|ttf|otf|mp4|webm|mp3|wav|m4a|ogg|txt|csv|wasm)$/#
private func dirOf(_ p: String) -> String { p.contains("/") ? String(p[...p.lastIndex(of: "/")!]) : "" }

/// A session's page as it runs: a web view whose store keeps nothing, that loads nothing from the network, reading
/// the page's folder from the session's machine through the hermesh-file scheme.
struct SessionPageWeb: UIViewRepresentable {
    let api: AppAPI?
    let sessionID: String
    let run: String
    let path: String

    func makeCoordinator() -> SessionFileScheme { SessionFileScheme(api: api, sessionID: sessionID, run: run, base: dirOf(path)) }

    func makeUIView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        config.setURLSchemeHandler(context.coordinator, forURLScheme: SessionFileScheme.scheme)
        let web = WKWebView(frame: .zero, configuration: config)
        web.navigationDelegate = context.coordinator
        web.isInspectable = true
        web.accessibilityIdentifier = "file-page"
        // Nothing from the network: the page reads its folder and nothing else.
        WKContentRuleListStore.default().compileContentRuleList(forIdentifier: "hermesh-session-page", encodedContentRuleList: #"[{"trigger":{"url-filter":"^(https?|wss?|ftp|file):"},"action":{"type":"block"}}]"#) { list, _ in
            if let list { web.configuration.userContentController.add(list) }
            web.load(URLRequest(url: SessionFileScheme.url(sessionID: sessionID, run: run, path: path)))
        }
        return web
    }

    func updateUIView(_ web: WKWebView, context: Context) {}
}

final class SessionFileScheme: NSObject, WKURLSchemeHandler, WKNavigationDelegate {
    static let scheme = "hermesh-file"
    let api: AppAPI?
    let sessionID: String
    let run: String
    let base: String
    private var stopped: Set<ObjectIdentifier> = []

    init(api: AppAPI?, sessionID: String, run: String, base: String) {
        self.api = api
        self.sessionID = sessionID
        self.run = run
        self.base = base
    }

    static func url(sessionID: String, run: String, path: String) -> URL {
        var c = URLComponents()
        c.scheme = scheme
        c.host = "files"
        c.path = "/\(sessionID)/\(run.isEmpty ? "-" : run)/\(path)"
        return c.url!
    }

    /// The file a request is for, when it is the page's session, turn and folder, and a web kind; nil otherwise.
    private func file(_ url: URL?) -> String? {
        guard let url, url.scheme == Self.scheme, url.host == "files" else { return nil }
        let parts = url.path.split(separator: "/", omittingEmptySubsequences: false).dropFirst().map(String.init)
        guard parts.count >= 3, parts[0] == sessionID, parts[1] == (run.isEmpty ? "-" : run) else { return nil }
        let rel = parts[2...].joined(separator: "/")
        guard rel.hasPrefix(base), !rel.split(separator: "/").contains(where: { $0.hasPrefix(".") || $0.isEmpty }), rel.firstMatch(of: webKinds) != nil else { return nil }
        return rel
    }

    func webView(_ web: WKWebView, start task: WKURLSchemeTask) {
        guard let rel = file(task.request.url), let api else {
            task.didFailWithError(URLError(.fileDoesNotExist))
            return
        }
        let id = ObjectIdentifier(task)
        Task { @MainActor in
            do {
                let got = try await api.readWholeFile(sessionID, run: run, path: rel)
                guard !stopped.contains(id) else { return }
                let headers = [
                    "Content-Type": got.mime.isEmpty ? "application/octet-stream" : got.mime,
                    "Content-Length": String(got.data.count),
                    // Scripts and forms, no origin of its own, nothing from anywhere but its folder.
                    "Content-Security-Policy": "default-src \(Self.scheme): data: blob: 'unsafe-inline' 'unsafe-eval'; form-action 'none'; sandbox allow-scripts allow-forms",
                    "Access-Control-Allow-Origin": "*",
                    "Cache-Control": "no-store",
                ]
                task.didReceive(HTTPURLResponse(url: task.request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: headers)!)
                task.didReceive(got.data)
                task.didFinish()
            } catch {
                if !stopped.contains(id) { task.didFailWithError(error) }
            }
        }
    }

    func webView(_ web: WKWebView, stop task: WKURLSchemeTask) { stopped.insert(ObjectIdentifier(task)) }

    /// The page stays on its pages; a web address it links opens in Safari when tapped.
    func webView(_ web: WKWebView, decidePolicyFor action: WKNavigationAction) async -> WKNavigationActionPolicy {
        guard let u = action.request.url else { return .cancel }
        if u.scheme == Self.scheme, file(u) != nil { return .allow }
        if u.scheme == "about" { return .allow }
        if (u.scheme == "https" || u.scheme == "http"), action.navigationType == .linkActivated { await UIApplication.shared.open(u) }
        return .cancel
    }
}
