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

/// What the session's machine said the paths its replies wrote are, asked a reply at a time.
@MainActor @Observable final class SessionFilesModel {
    @ObservationIgnored var sessionID: String
    @ObservationIgnored var api: AppAPI?
    private(set) var known: [String: Hermesh_Session_V1_ResolvedFile] = [:]
    @ObservationIgnored private var asked: Set<String> = []
    @ObservationIgnored private var pending: [String: Set<String>] = [:]
    @ObservationIgnored private var flushing: Task<Void, Never>?

    init(sessionID: String) { self.sessionID = sessionID }

    private func key(_ run: String, _ path: String) -> String { "\(run)\u{0}\(path)" }

    func info(run: String, path: String) -> Hermesh_Session_V1_ResolvedFile? { known[key(run, path)] }

    func want(run: String, paths: [String]) {
        for p in paths where !asked.contains(key(run, p)) {
            asked.insert(key(run, p))
            pending[run, default: []].insert(p)
        }
        guard !pending.isEmpty, flushing == nil else { return }
        flushing = Task { [weak self] in
            try? await Task.sleep(for: .milliseconds(30))
            await self?.flush()
        }
    }

    private func flush() async {
        let batch = pending
        pending = [:]
        flushing = nil
        guard let api else { return }
        for (run, set) in batch {
            let paths = Array(set)
            do {
                let res = try await api.resolveFiles(sessionID, run: run, paths: paths)
                for (i, f) in res.files.enumerated() where i < paths.count { known[key(run, paths[i])] = f }
            } catch {
                // The machine away: the paths are asked again next time the reply shows.
                for p in paths { asked.remove(key(run, p)) }
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
        case .list(_, _, let items): items.forEach { $0.blocks.forEach(walk) }
        case .quote(let bs): bs.forEach(walk)
        case .table(let head, let rows, _): head.forEach(text); rows.forEach { $0.forEach(text) }
        case .footnotes(let notes): notes.forEach { $0.forEach(walk) }
        default: break
        }
    }
    blocks.forEach(walk)
    return out
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

/// A line of a reply with its file tags: the name in the link's colour on a grey ground, grey when there is nothing
/// there; math drawn as `mathText` draws it. No symbol before the name, as the Mac has: an image in a Text moves its
/// links' places for VoiceOver (and the tests) by the image's width in characters.
func fileText(_ text: AttributedString, files: SessionFilesModel, run: String, size: CGFloat, code: Font) -> Text {
    var out = AttributedString()
    for r in text.runs {
        var part = AttributedString(text[r.range])
        guard let mark = r[FileRefAttribute.self], let info = files.info(run: run, path: mark.path) else {
            out += part
            continue
        }
        let found = info.kind == .file || info.kind == .folder
        if mark.code && !found {
            out += part
            continue
        }
        if let line = mark.line, !mark.code {
            var l = AttributedString(":\(line)" + (mark.endLine.map { "-\($0)" } ?? ""))
            l.font = code
            l.foregroundColor = .secondary
            part += l
        }
        var label = AttributedString("\u{2009}") + part + AttributedString("\u{2009}")
        label.backgroundColor = Palette.fill2
        label.foregroundColor = found ? Color.accentColor : Color.secondary
        if found, let url = FileRefLink.url(run: run, path: info.path, line: mark.line, endLine: mark.endLine, folder: info.kind == .folder) { label.link = url }
        if let line = mark.line, !mark.code, let at = label.range(of: ":\(line)") { label[at.lowerBound...].foregroundColor = .secondary }
        out += label
    }
    return mathText(out, size: size, code: code)
}

// MARK: The file's page

/// What a file's page shows it as (HermeshSessions' fileView): its own code and text, Markdown as it reads, a page
/// as it runs, the rest in Quick Look.
struct SessionFilePage: View {
    @Environment(AppModel.self) private var model
    let sessionID: String
    /// The reply's turn; "" for as it is now.
    let turnRun: String
    let path: String
    let line: Int?
    let endLine: Int?
    @State private var run: String
    @State private var read: Result<Hermesh_Session_V1_ReadFileResponse, Error>?
    @State private var source = false
    @State private var local: URL?
    @State private var sharing: URL?

    init(sessionID: String, run: String, path: String, line: Int, endLine: Int) {
        self.sessionID = sessionID
        turnRun = run
        self.path = path
        self.line = line > 0 ? line : nil
        self.endLine = endLine > 0 ? endLine : nil
        _run = State(initialValue: run)
    }

    private var name: String { fileBaseName(path) }
    private var view: FileView { fileView(path) }
    /// A page is what it runs as: no source to switch to.
    private var sourceable: Bool { view == .markdown || view == .svg }
    private var quickLooks: Bool { [.image, .pdf, .media, .office, .other].contains(view) }

    var body: some View {
        content
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .safeAreaInset(edge: .bottom, spacing: 0) {
                if case .success(let res)? = read { VersionBar(res: res, run: $run, turnRun: turnRun) }
            }
            .navigationTitle(name)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                if sourceable {
                    ToolbarItem(placement: .primaryAction) {
                        Button { source.toggle() } label: { Image(systemName: source ? "eye" : "chevron.left.forwardslash.chevron.right") }
                            .accessibilityLabel(source ? String(localized: "Preview") : String(localized: "Source"))
                            .accessibilityIdentifier("file-source")
                    }
                }
                // Beside the expanded menu (a wide screen), the page takes the menu's room, as a session's does; an
                // iPhone's page is the screen's already.
                if model.menuInView { ToolbarItem(placement: .topBarLeading) { WidenPageButton() } }
                ToolbarItem(placement: .primaryAction) {
                    Button { Task { await share() } } label: { Image(systemName: "square.and.arrow.up") }
                        .accessibilityLabel(String(localized: "Share"))
                        .accessibilityIdentifier("file-share")
                }
            }
            .sheet(item: $sharing) { HerShareSheet(url: $0).presentationDetents([.medium, .large]) }

            .task(id: run) { await load() }
            // A container, so what is in it keeps its own identifiers (the bar, the code).
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("session-file-page")
    }

    @ViewBuilder private var content: some View {
        switch read {
        case nil:
            ProgressView()
        case .failure(let e)?:
            FileCard(name: name, text: errorText(e))
        case .success(let res)?:
            if view == .page && !source {
                SessionPageWeb(api: model.api, sessionID: sessionID, run: run, path: path).id(run)
            } else if quickLooks || view == .svg && !source {
                if view == .svg, !source {
                    SessionPageWeb(api: model.api, sessionID: sessionID, run: run, path: path).id(run)
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
                ScrollView {
                    MarkdownText(text: res.text, lazy: true).frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal, 20)
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
            if quickLooks { local = try await fetch(api) }
        } catch {
            read = .failure(error)
        }
    }

    /// The file in a folder of its own on this iPhone, for Quick Look and Share.
    private func fetch(_ api: AppAPI) async throws -> URL {
        let got = try await api.readWholeFile(sessionID, run: run, path: path)
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("Session Files/\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appendingPathComponent(name.isEmpty ? "file" : name)
        try got.data.write(to: url)
        return url
    }

    private func share() async {
        if let local { sharing = local; return }
        guard let api = model.api, let url = try? await fetch(api) else { return }
        sharing = url
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
private struct FileCard: View {
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
private struct CodePage: View {
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
                        let path = dir.isEmpty ? e.name : "\(dir)/\(e.name)"
                        Button {
                            model.path.append(e.dir ? .sessionFolder(sessionID, path) : .sessionFile(sessionID, "", path, 0, 0))
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
