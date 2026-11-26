import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI
import WebKit

// The session's folder as a tree, and the side panel (docs/session-files.md §15). On a narrow screen a file's page
// pulls the tree up from its foot; on a wide one (regular width, on its side: iPhone Duo open, a Pro Max turned) the
// session has a side panel beside its conversation, tabs as on the Mac.

// MARK: The tree

/// The session's folder as it is now, folders opening in place: folders first, then files, each by name as people
/// read it; the file in front marked. `sizes`: the tree alone (项目文件), files with their sizes.
struct SessionTree: View {
    let files: SessionFilesModel
    let activePath: String?
    var sizes = false
    let pick: (String) -> Void

    private struct Row: Identifiable {
        let path: String
        let name: String
        let dir: Bool
        let size: UInt64
        let depth: Int
        var id: String { path }
    }

    private var rows: [Row] {
        var out: [Row] = []
        func walk(_ dir: String, _ depth: Int) {
            guard case .success(let res)? = files.dirs[dir] else { return }
            for e in res.entries {
                let path = dir.isEmpty ? e.name : "\(dir)/\(e.name)"
                out.append(Row(path: path, name: e.name, dir: e.dir, size: e.size, depth: depth))
                if e.dir, files.expanded.contains(path) { walk(path, depth + 1) }
            }
        }
        walk("", 0)
        return out
    }

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0) {
                    if case .success(let root)? = files.dirs[""], root.folderGone {
                        Text("The workspace was released; these are the files as turn \(root.turn) left them")
                            .font(.footnote).foregroundStyle(.secondary).padding(.horizontal, 14).padding(.vertical, 6)
                    }
                    if case .failure(let e)? = files.dirs[""] {
                        Text(verbatim: e.localizedDescription).font(.footnote).foregroundStyle(.secondary).padding(14)
                    }
                    ForEach(rows) { r in row(r).id(r.path) }
                }
                .padding(.horizontal, 8)
            }
            .task(id: activePath) {
                files.reveal(activePath ?? "")
                if let activePath {
                    try? await Task.sleep(for: .milliseconds(150))
                    proxy.scrollTo(activePath, anchor: .center)
                }
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("file-tree")
    }

    private func row(_ r: Row) -> some View {
        let on = r.path == activePath
        let open = files.expanded.contains(r.path)
        return Button {
            if r.dir { files.toggleDir(r.path) } else { pick(r.path) }
        } label: {
            HStack(spacing: 7) {
                Image(systemName: "chevron.forward").font(.caption2.weight(.semibold)).foregroundStyle(.tertiary)
                    .rotationEffect(.degrees(open ? 90 : 0)).opacity(r.dir ? 1 : 0).frame(width: 13)
                Image(systemName: r.dir ? "folder" : "doc").foregroundStyle(r.dir ? Color.accentColor : on ? Color.accentColor : .secondary).frame(width: 20)
                Text(verbatim: r.name).foregroundStyle(on ? Color.accentColor : .primary).lineLimit(1).truncationMode(.middle)
                Spacer(minLength: 6)
                if sizes, !r.dir {
                    Text(ByteCountFormatter.string(fromByteCount: Int64(r.size), countStyle: .file)).font(.footnote).foregroundStyle(.secondary)
                }
            }
            .padding(.leading, 8 + CGFloat(r.depth) * 13).padding(.trailing, 10)
            .frame(minHeight: 40)
            .background(on ? Color.accentColor.opacity(0.13) : .clear, in: RoundedRectangle(cornerRadius: 11, style: .continuous))
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(on ? .isSelected : [])
        .accessibilityIdentifier("tree-\(r.path)")
    }
}

/// The tree pulled up over a file's page (a narrow screen), the file marked and its folders open.
struct SessionTreeSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let sessionID: String
    let activePath: String
    let pick: (String) -> Void

    private var dir: String { activePath.contains("/") ? String(activePath[..<activePath.lastIndex(of: "/")!]) : "" }

    var body: some View {
        let files = model.sessionFiles.model(sessionID, api: model.api)
        let project = model.session(sessionID)?.projectID ?? ""
        let place = [project, dir].filter { !$0.isEmpty }.joined(separator: "/")
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Project Files").font(.headline)
                    if !place.isEmpty {
                        Text(verbatim: place).font(.caption.monospaced()).foregroundStyle(.secondary).lineLimit(1).truncationMode(.head)
                    }
                }
                Spacer()
                Button { dismiss() } label: { Image(systemName: "chevron.down").font(.subheadline.weight(.semibold)).frame(width: 34, height: 34) }
                    .buttonStyle(.glass)
                    .buttonBorderShape(.circle)
                    .accessibilityLabel(String(localized: "Close"))
            }
            .padding(.horizontal, 20).padding(.top, 18).padding(.bottom, 8)
            Divider()
            SessionTree(files: files, activePath: activePath, sizes: true, pick: pick)
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("file-tree-sheet")
    }
}

/// The unified bottom directory-tree drawer inside `SessionSidePanel` (wide / iPhone Duo): rises only within the side
/// panel column so the left-hand conversation stays unobstructed, with native grabber drag between medium and large detents.
private struct SessionPanelDrawer: View {
    let files: SessionFilesModel
    let project: String
    let activePath: String
    @Binding var isPresented: Bool
    let pick: (String) -> Void
    @State private var large = false
    @GestureState private var dragY: CGFloat = 0

    private var dir: String { activePath.contains("/") ? String(activePath[..<activePath.lastIndex(of: "/")!]) : "" }

    var body: some View {
        let place = [project, dir].filter { !$0.isEmpty }.joined(separator: "/")
        GeometryReader { geo in
            let baseH = geo.size.height * (large ? 0.88 : 0.60)
            let curH = min(geo.size.height * 0.92, max(96, baseH - dragY))
            ZStack(alignment: .bottom) {
                Color.black.opacity(0.22)
                    .ignoresSafeArea()
                    .onTapGesture { close() }
                    .accessibilityHidden(true)
                VStack(spacing: 0) {
                    Capsule()
                        .fill(.tertiary)
                        .frame(width: 38, height: 5)
                        .frame(maxWidth: .infinity)
                        .frame(height: 22)
                        .contentShape(.rect)
                        .onTapGesture { withAnimation(.snappy(duration: 0.24)) { large.toggle() } }
                        .gesture(
                            DragGesture(minimumDistance: 4)
                                .updating($dragY) { v, s, _ in s = v.translation.height }
                                .onEnded { v in
                                    let endH = baseH - v.predictedEndTranslation.height
                                    let ratio = endH / max(geo.size.height, 1)
                                    if ratio < 0.36 || v.translation.height > 110 {
                                        close()
                                    } else {
                                        withAnimation(.snappy(duration: 0.24)) { large = ratio > 0.72 || v.translation.height < -45 }
                                    }
                                }
                        )
                    HStack(spacing: 8) {
                        VStack(alignment: .leading, spacing: 2) {
                            Text("Project Files").font(.subheadline.weight(.semibold))
                            if !place.isEmpty {
                                Text(verbatim: place).font(.caption.monospaced()).foregroundStyle(.secondary).lineLimit(1).truncationMode(.head)
                            }
                        }
                        Spacer()
                        Button { close() } label: {
                            Image(systemName: "chevron.down").font(.footnote.weight(.semibold)).frame(width: 32, height: 32)
                        }
                        .buttonStyle(.glass)
                        .buttonBorderShape(.circle)
                        .accessibilityLabel(String(localized: "Close"))
                    }
                    .padding(.leading, 18).padding(.trailing, 12).padding(.bottom, 8)
                    Divider()
                    SessionTree(files: files, activePath: activePath, sizes: true) { picked in
                        close()
                        pick(picked)
                    }
                }
                .frame(height: curH)
                .frame(maxWidth: .infinity)
                .background(.regularMaterial, in: UnevenRoundedRectangle(topLeadingRadius: 26, topTrailingRadius: 26, style: .continuous))
                .shadow(color: .black.opacity(0.22), radius: 20, y: -6)
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("file-tree-sheet")
            }
        }
        .transition(.opacity)
    }

    private func close() {
        withAnimation(.snappy(duration: 0.24)) {
            isPresented = false
            large = false
        }
    }
}

/// Template symbol for hiding the trailing side panel (`panel` outline + right sidebar divider + right chevron `›`).
func sidePanelHideImage() -> Image {
    Image(size: CGSize(width: 22, height: 22)) { ctx in
        let stroke = StrokeStyle(lineWidth: 1.65, lineCap: .round, lineJoin: .round)
        let rect = Path(roundedRect: CGRect(x: 2.5, y: 4, width: 17, height: 14), cornerRadius: 2.6)
        ctx.stroke(rect, with: .foreground, style: stroke)
        var line = Path()
        line.move(to: CGPoint(x: 14, y: 4.3))
        line.addLine(to: CGPoint(x: 14, y: 17.7))
        ctx.stroke(line, with: .foreground, style: stroke)
        var chev = Path()
        chev.move(to: CGPoint(x: 8.2, y: 8.7))
        chev.addLine(to: CGPoint(x: 11, y: 11))
        chev.addLine(to: CGPoint(x: 8.2, y: 13.3))
        ctx.stroke(chev, with: .foreground, style: stroke)
    }
}

// MARK: The side panel

/// Beside a session's conversation on a wide screen, as the Mac's side panel: a single slim strip of tabs (with iPhone
/// Duo's bars on the side, window and contextual tools move into the system's vertical bar; elsewhere a compact glass
/// capsule on the strip holds ＋, Enlarge/Shrink and Hide), and at a file tab's foot the unified bottom tree drawer.
struct SessionSidePanel: View {
    @Environment(AppModel.self) private var model
    @Environment(\.barsOnTheSide) private var sideBars
    @Bindable var files: SessionFilesModel

    private var machineID: String { model.session(files.sessionID)?.machineID ?? "" }
    private var projectID: String { model.session(files.sessionID)?.projectID ?? "" }

    var body: some View {
        let activeIndex = files.tabs.firstIndex { $0.id == files.activeTab }
        VStack(spacing: 0) {
            strip(activeIndex: activeIndex)
            Divider()
            if let i = activeIndex {
                subBar($files.tabs[i])
                tabBody($files.tabs[i]).frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                Spacer()
            }
        }
        .background(Color(UIColor.systemBackground))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("side-panel")
    }

    private func strip(activeIndex: Int?) -> some View {
        HStack(spacing: 6) {
            ScrollViewReader { proxy in
                ScrollView(.horizontal) {
                    HStack(spacing: 6) {
                        ForEach(files.tabs) { t in chip(t).id(t.id) }
                    }
                    .padding(.vertical, 2)
                }
                .scrollIndicators(.hidden)
                .onChange(of: files.activeTab) { _, id in withAnimation { proxy.scrollTo(id) } }
            }
            .fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 0)
            // On iPhone Duo (`sideBars`), window and file tools sit in the system's right vertical bar. On other wide
            // screens (e.g. Pro Max on its side), they sit in a compact glass capsule at the trailing end of the strip.
            if !sideBars {
                if let i = activeIndex {
                    inlineTabTools($files.tabs[i])
                }
                HStack(spacing: 2) {
                    Menu {
                        Button("Project Files", systemImage: "list.bullet.indent") { files.openProject() }
                    } label: {
                        Image(systemName: "plus").font(.subheadline.weight(.medium)).frame(width: 36, height: 36)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(String(localized: "New Tab"))
                    .accessibilityIdentifier("side-panel-new")
                    Button { withAnimation(.snappy) { files.panelMax.toggle() } } label: {
                        Image(systemName: files.panelMax ? "arrow.down.right.and.arrow.up.left" : "arrow.up.left.and.arrow.down.right")
                            .font(.subheadline.weight(.medium)).frame(width: 36, height: 36)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(files.panelMax ? String(localized: "Shrink") : String(localized: "Enlarge"))
                    .accessibilityIdentifier("side-panel-max")
                    Button {
                        withAnimation(.snappy) {
                            files.panelOpen = false
                            files.panelMax = false
                            files.panelDrawer = false
                        }
                    } label: {
                        sidePanelHideImage().foregroundStyle(Color.accentColor).frame(width: 36, height: 36)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(String(localized: "Hide Side Panel"))
                    .accessibilityIdentifier("side-panel-hide")
                }
                .padding(.horizontal, 2).frame(height: 38)
                .glassEffect(.regular, in: .capsule)
            }
        }
        .tint(.primary)
        .padding(.horizontal, 10).padding(.vertical, 6)
    }

    @ViewBuilder private func inlineTabTools(_ t: Binding<PanelTab>) -> some View {
        let tab = t.wrappedValue
        if tab.kind == .files, !tab.path.isEmpty {
            let view = fileView(tab.path)
            if view == .markdown || view == .svg {
                Button { t.wrappedValue.source.toggle() } label: {
                    Image(systemName: tab.source ? "eye" : "chevron.left.forwardslash.chevron.right")
                        .font(.subheadline.weight(.medium)).frame(width: 34, height: 34)
                        .background(tab.source ? Color.secondary.opacity(0.16) : .clear, in: .circle)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(tab.source ? String(localized: "Preview") : String(localized: "Source"))
                .accessibilityIdentifier("file-source")
            }
        }
        if let a = tab.attachment {
            Menu {
                Button("Share…", systemImage: "square.and.arrow.up") {
                    Task { files.sharing = await SessionAttachmentStore.shared.previewURL(api: model.api, sessionID: files.sessionID, machineID: machineID, ref: a) }
                }
                .accessibilityIdentifier("file-share")
            } label: {
                Image(systemName: "ellipsis").font(.subheadline.weight(.medium)).frame(width: 34, height: 34)
            }
            .buttonStyle(.plain)
            .accessibilityLabel(String(localized: "More"))
            .accessibilityIdentifier("file-more")
        } else if !tab.path.isEmpty {
            Menu {
                SessionFileMenu(sessionID: files.sessionID, run: tab.run, path: tab.path, folder: files.folders[tab.id] ?? "", sharing: $files.sharing)
            } label: {
                Image(systemName: "ellipsis").font(.subheadline.weight(.medium)).frame(width: 34, height: 34)
            }
            .buttonStyle(.plain)
            .accessibilityLabel(String(localized: "More"))
            .accessibilityIdentifier("file-more")
        }
    }

    private func title(_ t: PanelTab) -> String {
        if let a = t.attachment { return fileBaseName(a.name) }
        return t.path.isEmpty ? String(localized: "Project Files") : fileBaseName(t.path)
    }

    private func chip(_ t: PanelTab) -> some View {
        let on = t.id == files.activeTab
        let glyph = t.kind == .page || t.attachment.map({ fileView($0.name) == .page }) == true ? "globe" : t.path.isEmpty && t.attachment == nil ? "list.bullet.indent" : "doc"
        return HStack(spacing: 6) {
            Image(systemName: glyph).font(.footnote)
            Text(verbatim: title(t)).font(.subheadline).lineLimit(1).truncationMode(.middle)
            if on {
                Button { withAnimation(.snappy) { files.closeTab(t.id) } } label: {
                    Image(systemName: "xmark").font(.caption2.weight(.bold)).foregroundStyle(.secondary).frame(width: 22, height: 22)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(String(localized: "Close \(title(t))"))
                .accessibilityIdentifier("side-panel-close")
            }
        }
        .foregroundStyle(on ? .primary : .secondary)
        .padding(.leading, 11).padding(.trailing, on ? 6 : 13)
        .frame(height: 34)
        .frame(maxWidth: 184)
        .background {
            if on { Capsule().fill(.clear).glassEffect(.regular, in: .capsule) } else { Capsule().fill(Color.secondary.opacity(0.12)) }
        }
        .contentShape(.capsule)
        .onTapGesture {
            files.activeTab = t.id
            files.panelDrawer = false
            if !t.path.isEmpty, t.attachment == nil { files.reveal(t.path) }
        }
        .accessibilityElement(children: .contain)
        .accessibilityAddTraits(on ? [.isSelected, .isButton] : .isButton)
        .accessibilityLabel(Text(verbatim: title(t)))
        .accessibilityIdentifier("side-panel-tab")
    }

    /// Subtle subtitle bar only for tabs that have no bottom directory-tree drawer (attachments, standalone 项目文件,
    /// and `.html` pages). File tabs carry their folder path on the bottom drawer bar instead, saving vertical room.
    @ViewBuilder private func subBar(_ t: Binding<PanelTab>) -> some View {
        let tab = t.wrappedValue
        if let a = tab.attachment {
            VStack(spacing: 0) {
                Text(verbatim: String(localized: "Attachment · \(ByteCountFormatter.string(fromByteCount: Int64(a.sizeBytes), countStyle: .file))"))
                    .font(.footnote).foregroundStyle(.secondary).frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, 14).frame(height: 36)
                Divider()
            }
        } else if tab.path.isEmpty {
            VStack(spacing: 0) {
                Text(verbatim: rootFolder).font(.caption.monospaced()).foregroundStyle(.secondary).lineLimit(1).truncationMode(.head)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, 14).frame(height: 36)
                Divider()
            }
        } else if tab.kind == .page {
            VStack(spacing: 0) {
                HStack {
                    Text(verbatim: tab.path).font(.caption.monospaced()).foregroundStyle(.secondary).lineLimit(1).truncationMode(.head)
                        .padding(.horizontal, 10).frame(height: 26)
                        .background(Capsule().fill(Color.secondary.opacity(0.12)))
                        .accessibilityIdentifier("side-panel-path")
                    Spacer(minLength: 0)
                }
                .padding(.horizontal, 12).frame(height: 38)
                Divider()
            }
        }
    }

    private var rootFolder: String {
        if case .success(let res)? = files.dirs[""] { return res.folder }
        return projectID
    }

    @ViewBuilder private func tabBody(_ t: Binding<PanelTab>) -> some View {
        let tab = t.wrappedValue
        if let a = tab.attachment {
            SessionAttachmentContent(sessionID: files.sessionID, machineID: machineID, ref: a)
        } else if tab.path.isEmpty {
            SessionTree(files: files, activePath: nil, sizes: true) { files.openFromTree($0, machineID: machineID) }
        } else {
            let dir = tab.path.contains("/") ? String(tab.path[..<tab.path.lastIndex(of: "/")!]) : ""
            ZStack {
                SessionFileContent(sessionID: files.sessionID, path: tab.path, line: tab.line, endLine: tab.endLine, turnRun: tab.replyRun,
                                   run: t.run, source: tab.source) { files.folders[tab.id] = $0 }
                    .id("\(tab.id)\u{0}\(tab.path)")
                    .safeAreaInset(edge: .bottom, spacing: 0) {
                        if tab.kind == .files {
                            SessionFileDrawerBar(project: projectID, dir: dir) {
                                files.reveal(tab.path)
                                withAnimation(.snappy(duration: 0.24)) { files.panelDrawer = true }
                            }
                        }
                    }
                if tab.kind == .files, files.panelDrawer {
                    SessionPanelDrawer(files: files, project: projectID, activePath: tab.path, isPresented: $files.panelDrawer) { picked in
                        files.openFromTree(picked, machineID: machineID)
                    }
                }
            }
        }
    }
}

// MARK: A file the owner sent

/// A file the owner sent with a prompt, read from its blob: a page running (on its own: the files beside it were not
/// sent), Markdown as it reads, code and text in numbered lines, the rest in Quick Look.
struct SessionAttachmentContent: View {
    @Environment(AppModel.self) private var model
    let sessionID: String
    let machineID: String
    let ref: Hermesh_Session_V1_AttachmentRef
    @State private var got: Result<Data, Error>?
    @State private var local: URL?

    var body: some View {
        let view = fileView(ref.name)
        Group {
            switch got {
            case nil: ProgressView()
            case .failure(let e)?: FileCard(name: fileBaseName(ref.name), text: e.localizedDescription)
            case .success(let data)?:
                if view == .page || view == .svg {
                    AttachmentWeb(data: data, mime: view == .svg ? "image/svg+xml" : "text/html")
                        .safeAreaInset(edge: .bottom, spacing: 0) {
                            if view == .page, refersToOwnFiles(String(decoding: data, as: UTF8.self)) {
                                Text("Only this file was sent; the files the page refers to aren’t here")
                                    .font(.footnote).foregroundStyle(.secondary).frame(maxWidth: .infinity, alignment: .leading)
                                    .padding(.horizontal, 16).padding(.vertical, 10)
                                    .background(.bar)
                                    .accessibilityIdentifier("attachment-alone")
                            }
                        }
                } else if view == .markdown, let text = String(data: data, encoding: .utf8) {
                    ScrollView {
                        MarkdownText(text: text, lazy: true).frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal, 20)
                    }
                    .contentMargins(.vertical, 13, for: .scrollContent)
                    .accessibilityIdentifier("file-markdown")
                } else if view == .code || view == .text, let text = String(data: data, encoding: .utf8) {
                    CodePage(text: text, name: fileBaseName(ref.name), target: nil)
                } else if let local {
                    HerQuickLook(url: local).ignoresSafeArea(edges: .bottom).accessibilityIdentifier("file-quicklook")
                } else {
                    ProgressView()
                }
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .task(id: ref.blobSha256) {
            guard let api = model.api else { return }
            do {
                got = .success(try await SessionAttachmentStore.shared.data(api: api, sessionID: sessionID, machineID: machineID, ref: ref))
                local = await SessionAttachmentStore.shared.previewURL(api: api, sessionID: sessionID, machineID: machineID, ref: ref)
            } catch {
                got = .failure(error)
            }
        }
    }
}

/// Whether an attached page refers to files beside it (a script, a style sheet, a picture by a relative address): only
/// the page was sent, so those are not there. The Mac's attachments.ts refersToOwnFiles, the same rule.
func refersToOwnFiles(_ html: String) -> Bool {
    let attrs = html.matches(of: /(?i)\b(?:src|href)\s*=\s*["']([^"']*)["']/).map { String($0.output.1) }
    let urls = html.matches(of: /(?i)url\(\s*["']?([^"')]+)["']?\s*\)/).map { String($0.output.1) }
    return (attrs + urls).map { $0.trimmingCharacters(in: .whitespaces) }.contains { r in
        !r.isEmpty && r.firstMatch(of: /(?i)^(?:[a-z][a-z0-9+.-]*:|\/\/|#)/) == nil
    }
}

/// The kinds of attachment the app shows itself; the rest go to Quick Look from the bubble, as before.
func attachmentOpensInApp(_ ref: Hermesh_Session_V1_AttachmentRef) -> Bool {
    [.page, .svg, .markdown, .code, .text].contains(fileView(ref.name))
}

/// An attachment's page, pushed (a narrow screen): its name, and under it that it is an attachment, with its size.
struct SessionAttachmentPage: View {
    let sessionID: String
    let machineID: String
    let ref: Hermesh_Session_V1_AttachmentRef
    @State private var sharing: URL?
    @Environment(AppModel.self) private var model

    var body: some View {
        SessionAttachmentContent(sessionID: sessionID, machineID: machineID, ref: ref)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .principal) {
                    VStack(spacing: 1) {
                        Text(verbatim: fileBaseName(ref.name)).font(.subheadline.weight(.semibold))
                        Text(verbatim: String(localized: "Attachment · \(ByteCountFormatter.string(fromByteCount: Int64(ref.sizeBytes), countStyle: .file))"))
                            .font(.caption2).foregroundStyle(.secondary)
                    }
                    .lineLimit(1)
                }
                if model.menuInView { ToolbarItem(placement: .topBarLeading) { WidenPageButton() } }
                ToolbarItem(placement: .primaryAction) {
                    Menu {
                        Button("Share…", systemImage: "square.and.arrow.up") {
                            Task { sharing = await SessionAttachmentStore.shared.previewURL(api: model.api, sessionID: sessionID, machineID: machineID, ref: ref) }
                        }
                        .accessibilityIdentifier("file-share")
                    } label: {
                        Image(systemName: "ellipsis")
                    }
                    .accessibilityLabel(String(localized: "More"))
                    .accessibilityIdentifier("file-more")
                }
            }
            .sheet(item: $sharing) { HerShareSheet(url: $0).presentationDetents([.medium, .large]) }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("session-attachment-page")
    }
}

/// An attached page (or picture of SVG) running in a web view whose store keeps nothing and that loads nothing from
/// the network; it is a single file, so nothing beside it is there to read.
private struct AttachmentWeb: UIViewRepresentable {
    let data: Data
    let mime: String

    func makeUIView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        let web = WKWebView(frame: .zero, configuration: config)
        web.accessibilityIdentifier = "file-page"
        WKContentRuleListStore.default().compileContentRuleList(forIdentifier: "hermesh-attachment-page", encodedContentRuleList: #"[{"trigger":{"url-filter":"^(https?|wss?|ftp|file):"},"action":{"type":"block"}}]"#) { list, _ in
            if let list { web.configuration.userContentController.add(list) }
            web.load(data, mimeType: mime, characterEncodingName: "utf-8", baseURL: URL(string: "about:blank")!)
        }
        return web
    }

    func updateUIView(_ web: WKWebView, context: Context) {}
}
