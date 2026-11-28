import HermeshKit
import HermeshProto
import HermeshSessions
import os
import SwiftUI
import UIKit
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

    var body: some View {
        let rows = files.treeRows
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
            .scrollDismissesKeyboard(.immediately)
            .onScrollPhaseChange { _, phase in if phase == .interacting { hideKeyboard() } }
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

    private func row(_ r: SessionTreeRow) -> some View {
        let on = r.path == activePath
        let open = r.dir && files.expanded.contains(r.path)
        return Button {
            if r.dir { files.toggleDir(r.path) } else { pick(r.path) }
        } label: {
            HStack(spacing: 7) {
                Image(systemName: "chevron.forward").font(.caption2.weight(.semibold)).foregroundStyle(.tertiary)
                    .rotationEffect(.degrees(open ? 90 : 0)).opacity(r.dir ? 1 : 0).frame(width: 13)
                SessionFileIcon(name: r.name, isDir: r.dir, isOpen: open, size: 17)
                    .frame(width: 20)
                sessionFileNameText(r.name, dir: r.dir, active: on)
                    .font(.subheadline)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Spacer(minLength: 6)
                if sizes, !r.dir {
                    Text(verbatim: r.sizeLabel)
                        .font(.caption.monospaced())
                        .foregroundStyle(.tertiary)
                }
            }
            .padding(.leading, 8 + CGFloat(r.depth) * 14).padding(.trailing, 10)
            .frame(minHeight: 38)
            .overlay(alignment: .leading) {
                if r.depth > 0 {
                    ZStack(alignment: .leading) {
                        ForEach(1...r.depth, id: \.self) { d in
                            Rectangle()
                                .fill(Palette.separator)
                                .frame(width: 1)
                                .padding(.leading, 14 + CGFloat(d - 1) * 14)
                        }
                    }
                    .allowsHitTesting(false)
                }
            }
            .background(on ? Color.accentColor.opacity(0.13) : .clear, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            .contentShape(.rect)
            .opacity(r.dimmed && !on ? 0.68 : 1)
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
/// Rendered as a template `UIImage` because SwiftUI's `UIBarButtonItem` bridge rasterizes procedural
/// `Image(size:renderer:)` using `.foreground` as transparent inside navigation/vertical bars.
private let sidePanelHideUIImage: UIImage = {
    let format = UIGraphicsImageRendererFormat.default()
    format.opaque = false
    return UIGraphicsImageRenderer(size: CGSize(width: 22, height: 22), format: format).image { ctx in
        let cg = ctx.cgContext
        cg.setStrokeColor(UIColor.black.cgColor)
        cg.setLineWidth(1.65)
        cg.setLineCap(.round)
        cg.setLineJoin(.round)
        cg.addPath(UIBezierPath(roundedRect: CGRect(x: 2.5, y: 4, width: 17, height: 14), cornerRadius: 2.6).cgPath)
        cg.move(to: CGPoint(x: 14, y: 4.3))
        cg.addLine(to: CGPoint(x: 14, y: 17.7))
        cg.move(to: CGPoint(x: 8.2, y: 8.7))
        cg.addLine(to: CGPoint(x: 11, y: 11))
        cg.addLine(to: CGPoint(x: 8.2, y: 13.3))
        cg.strokePath()
    }.withRenderingMode(.alwaysTemplate)
}()

func sidePanelHideImage() -> Image {
    Image(uiImage: sidePanelHideUIImage)
}

// MARK: The side panel

/// Beside a session's conversation on a wide screen, as the Mac's side panel: a single slim strip of tabs (with iPhone
/// Duo's bars on the side, window and contextual tools move into the system's vertical bar; elsewhere a compact glass
/// capsule on the strip holds ＋, Enlarge/Shrink and Hide), and at a file tab's foot the unified bottom tree drawer.
struct SessionSidePanel: View {
    @Environment(AppModel.self) private var model
    @Bindable var files: SessionFilesModel
    /// Passed explicitly from `SessionPage` because `WideSidePanelController` hosts this view in a standalone
    /// `UIHostingController` outside `NavigationStack`, where `@Environment(\.toolbarVerticalEdge)` is `nil`.
    let sideBars: Bool

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
        .keyboardAwayOnTap()
        .simultaneousGesture(DragGesture(minimumDistance: 8).onChanged { _ in hideKeyboard() })
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
                    Button { files.panelMax.toggle() } label: {
                        Image(systemName: files.panelMax ? "arrow.down.right.and.arrow.up.left" : "arrow.up.left.and.arrow.down.right")
                            .font(.subheadline.weight(.medium)).frame(width: 36, height: 36)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(files.panelMax ? String(localized: "Shrink") : String(localized: "Enlarge"))
                    .accessibilityIdentifier("side-panel-max")
                    Button {
                        files.panelOpen = false
                        files.panelMax = false
                        files.panelDrawer = false
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
                if tab.kind == .page {
                    Section(tab.path) {
                        SessionFileMenu(sessionID: files.sessionID, run: tab.run, path: tab.path, folder: files.folders[tab.id] ?? "", sharing: $files.sharing)
                    }
                } else {
                    SessionFileMenu(sessionID: files.sessionID, run: tab.run, path: tab.path, folder: files.folders[tab.id] ?? "", sharing: $files.sharing)
                }
            } label: {
                Image(systemName: "ellipsis").font(.subheadline.weight(.medium)).frame(width: 34, height: 34)
            }
            .buttonStyle(.plain)
            .accessibilityLabel(String(localized: "More"))
            .accessibilityIdentifier("file-more")
        }
    }

    private func title(_ t: PanelTab) -> String {
        if let a = t.attachment {
            return fileView(a.name) == .page ? pageTabTitle(a.name, pageTitle: t.pageTitle) : fileBaseName(a.name)
        }
        if t.path.isEmpty { return String(localized: "Project Files") }
        return t.kind == .page ? pageTabTitle(t.path, pageTitle: t.pageTitle) : fileBaseName(t.path)
    }

    private func chip(_ t: PanelTab) -> some View {
        let on = t.id == files.activeTab
        let isTree = t.path.isEmpty && t.attachment == nil
        let isPage = t.kind == .page || t.attachment.map({ fileView($0.name) == .page }) == true
        return HStack(spacing: 6) {
            if isTree {
                Image(systemName: "list.bullet.indent").font(.footnote)
            } else {
                SessionFileIcon(name: isPage ? "page.html" : (t.attachment?.name ?? t.path), isDir: false, size: 15)
            }
            Text(verbatim: title(t)).font(.subheadline).lineLimit(1).truncationMode(.middle)
            if on {
                Button {
                    if files.tabs.count > 1 {
                        withAnimation(.snappy(duration: 0.2)) { files.closeTab(t.id) }
                    } else {
                        files.closeTab(t.id)
                    }
                } label: {
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

    /// Subtle subtitle bar only for tabs that carry metadata outside the content (attachments and standalone 项目文件).
    /// File tabs carry their folder path on the bottom drawer bar, and `.html` page tabs carry theirs in ⋯, saving vertical room.
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
        }
    }

    private var rootFolder: String {
        if case .success(let res)? = files.dirs[""] { return res.folder }
        return projectID
    }

    @ViewBuilder private func tabBody(_ t: Binding<PanelTab>) -> some View {
        let tab = t.wrappedValue
        if let a = tab.attachment {
            SessionAttachmentContent(sessionID: files.sessionID, machineID: machineID, ref: a) { t.wrappedValue.pageTitle = $0 }
        } else if tab.path.isEmpty {
            SessionTree(files: files, activePath: nil, sizes: true) { files.openFromTree($0, machineID: machineID) }
        } else {
            let dir = tab.path.contains("/") ? String(tab.path[..<tab.path.lastIndex(of: "/")!]) : ""
            ZStack {
                SessionFileContent(sessionID: files.sessionID, path: tab.path, line: tab.line, endLine: tab.endLine, turnRun: tab.replyRun,
                                   run: t.run, source: tab.source, onFolder: { files.folders[tab.id] = $0 }, onTitle: { t.wrappedValue.pageTitle = $0 })
                    .id("\(tab.id)\u{0}\(tab.path)")
                    .onChange(of: tab.run) { _, _ in
                        if tab.kind == .page { t.wrappedValue.pageTitle = "" }
                    }
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
    var onTitle: (String) -> Void = { _ in }
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
                    AttachmentWeb(data: data, mime: view == .svg ? "image/svg+xml" : "text/html", onTitle: view == .page ? onTitle : { _ in })
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
    @State private var pageTitle = ""
    @State private var sharing: URL?
    @Environment(AppModel.self) private var model

    private var titleLabel: String {
        fileView(ref.name) == .page ? pageTabTitle(ref.name, pageTitle: pageTitle) : fileBaseName(ref.name)
    }

    var body: some View {
        SessionAttachmentContent(sessionID: sessionID, machineID: machineID, ref: ref) { pageTitle = $0 }
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .principal) {
                    VStack(spacing: 1) {
                        Text(verbatim: titleLabel).font(.subheadline.weight(.semibold))
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
    var onTitle: (String) -> Void = { _ in }

    final class Coordinator: NSObject, WKNavigationDelegate {
        var onTitle: (String) -> Void
        private var titleObservation: NSKeyValueObservation?
        private var sawExplicitTitle = false

        init(onTitle: @escaping (String) -> Void) { self.onTitle = onTitle }

        func observeTitle(of web: WKWebView) {
            titleObservation = web.observe(\.title, options: [.new]) { [weak self] web, _ in
                let title = web.title?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
                Task { @MainActor [weak self] in
                    guard let self, !title.isEmpty else { return }
                    self.sawExplicitTitle = true
                    self.onTitle(title)
                }
            }
        }

        func webView(_ web: WKWebView, didFinish navigation: WKNavigation!) {
            let title = web.title?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            if !title.isEmpty {
                sawExplicitTitle = true
                onTitle(title)
            } else if !sawExplicitTitle {
                onTitle("")
            }
        }
    }

    func makeCoordinator() -> Coordinator { Coordinator(onTitle: onTitle) }

    func makeUIView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        let web = WKWebView(frame: .zero, configuration: config)
        web.navigationDelegate = context.coordinator
        web.accessibilityIdentifier = "file-page"
        context.coordinator.observeTitle(of: web)
        WKContentRuleListStore.default().compileContentRuleList(forIdentifier: "hermesh-attachment-page", encodedContentRuleList: #"[{"trigger":{"url-filter":"^(https?|wss?|ftp|file):"},"action":{"type":"block"}}]"#) { list, _ in
            if let list { web.configuration.userContentController.add(list) }
            web.load(data, mimeType: mime, characterEncodingName: "utf-8", baseURL: URL(string: "about:blank")!)
        }
        return web
    }

    func updateUIView(_ web: WKWebView, context: Context) {
        context.coordinator.onTitle = onTitle
    }
}

// MARK: Native UIKit trailing panel host (wide screens & iPhone Duo unfolded)

enum WideTrailingPanelKind: Equatable {
    case none
    case files
    case background
}

@MainActor private protocol WideSidePanelHitTarget: AnyObject {
    func hits(_ point: CGPoint, with event: UIEvent?) -> Bool
}

private final class WidePanelPassthroughView: UIView {
    weak var target: WideSidePanelHitTarget?

    override func point(inside point: CGPoint, with event: UIEvent?) -> Bool {
        target?.hits(point, with: event) ?? false
    }
}

/// Native UIKit container driving the session's trailing side panel (`SessionSidePanel` and `BackgroundTasksPanel`)
/// via CoreAnimation RenderServer springs (`UIView.animate(springDuration:bounce:)`), keeping the `UIHostingController`
/// warm across toggles and isolating the conversation `ScrollView` from per-frame width reflows.
struct WideSidePanelHost<FilesContent: View, BackgroundContent: View>: UIViewControllerRepresentable {
    let kind: WideTrailingPanelKind
    let splitWidth: CGFloat
    let isMax: Bool
    let reduceMotion: Bool
    let filesKey: ObjectIdentifier
    let sideBars: Bool
    let makeFiles: () -> FilesContent
    let makeBackground: () -> BackgroundContent
    let onSettled: (Bool) -> Void
    let onCloseFiles: () -> Void
    let onCloseBackground: () -> Void

    func makeUIViewController(context: Context) -> WideSidePanelController<FilesContent, BackgroundContent> {
        WideSidePanelController()
    }

    func updateUIViewController(_ c: WideSidePanelController<FilesContent, BackgroundContent>, context: Context) {
        c.update(
            kind: kind,
            splitWidth: splitWidth,
            isMax: isMax,
            reduceMotion: reduceMotion,
            filesKey: filesKey,
            sideBars: sideBars,
            makeFiles: makeFiles,
            makeBackground: makeBackground,
            onSettled: onSettled,
            onCloseFiles: onCloseFiles,
            onCloseBackground: onCloseBackground
        )
    }
}

final class WideSidePanelController<FilesContent: View, BackgroundContent: View>: UIViewController, UIGestureRecognizerDelegate, WideSidePanelHitTarget {
    private let card = UIView()
    private let clip = UIView()
    private let divider = UIView()
    private let grip = UIView()
    private let pill = UIView()
    private let edgePan = UIPanGestureRecognizer()
    private let dismissTap = UITapGestureRecognizer()
    private let dismissPan = UIPanGestureRecognizer()

    private var filesHost: UIHostingController<FilesContent>?
    private var bgHost: UIHostingController<BackgroundContent>?
    private var filesKey: ObjectIdentifier?
    private var sideBars = false

    private var currentKind: WideTrailingPanelKind = .none
    private var shownKind: WideTrailingPanelKind = .files
    private var splitWidth: CGFloat = 360
    private var isMax = false
    private var reduceMotion = false
    private var hasLaidOut = false
    private var closingFromSwipe = false
    private var isDraggingEdge = false
    private var generation = 0

    private var onSettled: (Bool) -> Void = { _ in }
    private var onCloseFiles: () -> Void = {}
    private var onCloseBackground: () -> Void = {}

    private let signposter = OSSignposter(subsystem: "io.hermesh.ios", category: "side-panel")
    private var moving: OSSignpostIntervalState?

    override func loadView() {
        let passthrough = WidePanelPassthroughView()
        passthrough.backgroundColor = .clear
        passthrough.target = self
        view = passthrough
    }

    override func viewDidLoad() {
        super.viewDidLoad()
        card.backgroundColor = .systemBackground
        card.isHidden = true
        card.accessibilityElementsHidden = true
        card.layer.shadowColor = UIColor.black.cgColor
        card.layer.shadowRadius = 14
        card.layer.shadowOffset = CGSize(width: -5, height: 0)
        card.layer.shadowOpacity = 0
        view.addSubview(card)

        clip.clipsToBounds = true
        card.addSubview(clip)

        divider.backgroundColor = .separator
        card.addSubview(divider)

        pill.backgroundColor = .quaternaryLabel
        pill.layer.cornerRadius = 1.75
        pill.layer.cornerCurve = .continuous
        pill.isUserInteractionEnabled = false
        grip.addSubview(pill)
        grip.isAccessibilityElement = true
        grip.accessibilityIdentifier = "side-panel-edge-grip"
        card.addSubview(grip)

        edgePan.addTarget(self, action: #selector(handleEdgePan(_:)))
        edgePan.delegate = self
        grip.addGestureRecognizer(edgePan)

        dismissTap.addTarget(self, action: #selector(handleDismissTap))
        dismissTap.cancelsTouchesInView = false
        dismissTap.delaysTouchesBegan = false
        dismissTap.delaysTouchesEnded = false
        dismissTap.delegate = self
        card.addGestureRecognizer(dismissTap)

        dismissPan.addTarget(self, action: #selector(handleDismissPan(_:)))
        dismissPan.cancelsTouchesInView = false
        dismissPan.delaysTouchesBegan = false
        dismissPan.delaysTouchesEnded = false
        dismissPan.delegate = self
        card.addGestureRecognizer(dismissPan)
    }

    func hits(_ point: CGPoint, with event: UIEvent?) -> Bool {
        guard currentKind != .none, !card.isHidden else { return false }
        return card.bounds.contains(card.convert(point, from: view))
    }

    private func targetWidth(in bounds: CGRect) -> CGFloat {
        if shownKind == .files && isMax { return bounds.width }
        return min(bounds.width, max(280, splitWidth))
    }

    private func layoutCard(width w: CGFloat, in b: CGRect) {
        let h = b.height
        let panelBounds = CGRect(x: 0, y: 0, width: w, height: h)
        // Bounds + center (never frame) so active CGAffineTransform slides remain exact.
        card.bounds = panelBounds
        card.center = CGPoint(x: b.width - w / 2, y: b.midY)
        card.layer.shadowPath = UIBezierPath(rect: panelBounds).cgPath
        clip.frame = panelBounds
        filesHost?.view.frame = panelBounds
        bgHost?.view.frame = panelBounds
        let hairline = 1 / max(traitCollection.displayScale, 1)
        divider.frame = CGRect(x: 0, y: 0, width: hairline, height: h)
        let fullBleed = shownKind == .files && isMax
        divider.isHidden = fullBleed
        grip.frame = CGRect(x: 0, y: 0, width: 18, height: h)
        grip.isHidden = fullBleed || currentKind == .none
        pill.frame = CGRect(x: (18 - 3.5) / 2, y: (h - 36) / 2, width: 3.5, height: 36)
        card.bringSubviewToFront(divider)
        card.bringSubviewToFront(grip)
    }

    override func viewDidLayoutSubviews() {
        super.viewDidLayoutSubviews()
        let b = view.bounds
        guard b.width > 0, b.height > 0 else { return }
        if !hasLaidOut {
            hasLaidOut = true
            if currentKind != .none {
                shownKind = currentKind
                layoutCard(width: targetWidth(in: b), in: b)
                card.transform = .identity
                card.isHidden = false
                card.accessibilityElementsHidden = false
                card.layer.shadowOpacity = 0
                DispatchQueue.main.async { [weak self] in self?.onSettled(true) }
            } else {
                card.isHidden = true
                card.accessibilityElementsHidden = true
            }
            return
        }
        if !card.isHidden {
            layoutCard(width: targetWidth(in: b), in: b)
        }
    }

    func update(
        kind: WideTrailingPanelKind,
        splitWidth: CGFloat,
        isMax: Bool,
        reduceMotion: Bool,
        filesKey: ObjectIdentifier,
        sideBars: Bool,
        makeFiles: () -> FilesContent,
        makeBackground: () -> BackgroundContent,
        onSettled: @escaping (Bool) -> Void,
        onCloseFiles: @escaping () -> Void,
        onCloseBackground: @escaping () -> Void
    ) {
        self.onSettled = onSettled
        self.onCloseFiles = onCloseFiles
        self.onCloseBackground = onCloseBackground
        self.reduceMotion = reduceMotion

        switch kind {
        case .files:
            if let host = filesHost {
                if self.filesKey != filesKey || self.sideBars != sideBars {
                    host.rootView = makeFiles()
                    self.filesKey = filesKey
                    self.sideBars = sideBars
                }
            } else {
                let host = UIHostingController(rootView: makeFiles())
                host.sizingOptions = []
                host.view.backgroundColor = .systemBackground
                addChild(host)
                clip.addSubview(host.view)
                host.didMove(toParent: self)
                filesHost = host
                self.filesKey = filesKey
                self.sideBars = sideBars
            }
        case .background:
            if let host = bgHost {
                host.rootView = makeBackground()
            } else {
                let host = UIHostingController(rootView: makeBackground())
                host.sizingOptions = []
                host.view.backgroundColor = .clear
                addChild(host)
                clip.addSubview(host.view)
                host.didMove(toParent: self)
                bgHost = host
            }
        case .none:
            break
        }

        let prevKind = currentKind
        let prevMax = self.isMax
        let prevSplit = self.splitWidth
        currentKind = kind
        self.isMax = isMax
        self.splitWidth = splitWidth

        if kind != .none {
            shownKind = kind
            filesHost?.view.isHidden = (kind != .files)
            filesHost?.view.accessibilityElementsHidden = (kind != .files)
            bgHost?.view.isHidden = (kind != .background)
            bgHost?.view.accessibilityElementsHidden = (kind != .background)
            grip.accessibilityIdentifier = (kind == .background) ? "bg-panel-edge-grip" : "side-panel-edge-grip"
        }

        let b = view.bounds
        guard hasLaidOut, b.width > 0, b.height > 0 else { return }

        if prevKind == .none && kind != .none {
            hideKeyboard()
            closingFromSwipe = false
            isDraggingEdge = false
            generation += 1
            let gen = generation
            let wasSlidingClosed = !card.isHidden
            let w = targetWidth(in: b)
            layoutCard(width: w, in: b)
            card.layoutIfNeeded()
            card.isHidden = false
            card.accessibilityElementsHidden = false
            if reduceMotion {
                card.layer.removeAllAnimations()
                card.transform = .identity
                card.layer.shadowOpacity = 0
                endSignpost()
                DispatchQueue.main.async { [weak self] in self?.onSettled(true) }
            } else {
                beginSignpost()
                if !wasSlidingClosed {
                    card.transform = CGAffineTransform(translationX: w + 16, y: 0)
                }
                card.layer.shadowOpacity = 0.14
                UIView.animate(springDuration: 0.26, bounce: 0, initialSpringVelocity: 0, options: [.allowUserInteraction, .beginFromCurrentState]) {
                    self.card.transform = .identity
                } completion: { _ in
                    guard self.generation == gen, self.currentKind != .none else { return }
                    self.card.layer.shadowOpacity = 0
                    self.endSignpost()
                    self.onSettled(true)
                }
            }
        } else if prevKind != .none && kind == .none {
            isDraggingEdge = false
            card.accessibilityElementsHidden = true
            grip.isHidden = true
            DispatchQueue.main.async { [weak self] in self?.onSettled(false) }
            if closingFromSwipe {
                closingFromSwipe = false
            } else if reduceMotion {
                generation += 1
                finishClose()
            } else {
                generation += 1
                let gen = generation
                beginSignpost()
                let distance = card.bounds.width + 16
                card.layer.shadowOpacity = 0.14
                UIView.animate(springDuration: 0.25, bounce: 0, initialSpringVelocity: 0, options: [.allowUserInteraction, .beginFromCurrentState]) {
                    self.card.transform = CGAffineTransform(translationX: distance, y: 0)
                } completion: { _ in
                    guard self.generation == gen, self.currentKind == .none else { return }
                    self.finishClose()
                }
            }
        } else if kind != .none {
            let newW = targetWidth(in: b)
            if prevMax != isMax && !reduceMotion && !isDraggingEdge {
                UIView.animate(springDuration: 0.26, bounce: 0, initialSpringVelocity: 0, options: [.allowUserInteraction, .beginFromCurrentState]) {
                    self.layoutCard(width: newW, in: b)
                    self.card.layoutIfNeeded()
                }
            } else if abs(newW - card.bounds.width) > 0.5 || prevKind != kind || abs(prevSplit - splitWidth) > 0.5 {
                layoutCard(width: newW, in: b)
            }
        }
    }

    private func finishClose() {
        card.layer.removeAllAnimations()
        card.transform = .identity
        card.layer.shadowOpacity = 0
        card.isHidden = true
        card.accessibilityElementsHidden = true
        endSignpost()
    }

    private func beginSignpost() {
        if moving == nil { moving = signposter.beginInterval("move") }
    }

    private func endSignpost() {
        if let m = moving {
            signposter.endInterval("move", m)
            moving = nil
        }
    }

    @objc private func handleDismissTap() {
        hideKeyboard()
    }

    @objc private func handleDismissPan(_ pan: UIPanGestureRecognizer) {
        if pan.state == .began { hideKeyboard() }
    }

    @objc private func handleEdgePan(_ pan: UIPanGestureRecognizer) {
        guard currentKind != .none, !(shownKind == .files && isMax) else { return }
        let dx = max(0, pan.translation(in: view).x)
        let vx = pan.velocity(in: view).x
        switch pan.state {
        case .began:
            isDraggingEdge = true
            generation += 1
            beginSignpost()
            hideKeyboard()
            card.layer.shadowOpacity = 0.14
            onSettled(false)
            card.transform = CGAffineTransform(translationX: dx, y: 0)
        case .changed:
            guard isDraggingEdge else { return }
            card.transform = CGAffineTransform(translationX: dx, y: 0)
        case .ended, .cancelled:
            guard isDraggingEdge else { return }
            isDraggingEdge = false
            let projected = dx + vx * 0.18
            if pan.state != .cancelled && (dx > 72 || projected > 140) {
                let distance = card.bounds.width + 16
                let remaining = max(1, distance - dx)
                let v0 = min(18, max(0, vx / remaining))
                let closedKind = currentKind
                closingFromSwipe = true
                card.accessibilityElementsHidden = true
                grip.isHidden = true
                generation += 1
                let gen = generation
                if closedKind == .files { onCloseFiles() } else { onCloseBackground() }
                if reduceMotion {
                    finishClose()
                } else {
                    UIView.animate(springDuration: 0.24, bounce: 0, initialSpringVelocity: v0, options: [.allowUserInteraction, .beginFromCurrentState]) {
                        self.card.transform = CGAffineTransform(translationX: distance, y: 0)
                    } completion: { _ in
                        guard self.generation == gen, self.currentKind == .none else { return }
                        self.finishClose()
                    }
                }
            } else {
                generation += 1
                let gen = generation
                UIView.animate(springDuration: 0.24, bounce: 0, initialSpringVelocity: 0, options: [.allowUserInteraction, .beginFromCurrentState]) {
                    self.card.transform = .identity
                } completion: { _ in
                    guard self.generation == gen, self.currentKind != .none else { return }
                    self.card.layer.shadowOpacity = 0
                    self.endSignpost()
                    self.onSettled(true)
                }
            }
        default:
            break
        }
    }

    func gestureRecognizerShouldBegin(_ gestureRecognizer: UIGestureRecognizer) -> Bool {
        if gestureRecognizer === edgePan {
            guard currentKind != .none, !(shownKind == .files && isMax) else { return false }
            let v = edgePan.velocity(in: view)
            return abs(v.x) > abs(v.y) && v.x >= 0
        }
        return true
    }

    func gestureRecognizer(_ gestureRecognizer: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith otherGestureRecognizer: UIGestureRecognizer) -> Bool {
        gestureRecognizer === dismissTap || gestureRecognizer === dismissPan
    }

    func gestureRecognizer(_ gestureRecognizer: UIGestureRecognizer, shouldReceive touch: UITouch) -> Bool {
        if gestureRecognizer === dismissTap || gestureRecognizer === dismissPan {
            var v: UIView? = touch.view
            while let cur = v, cur !== card {
                if cur is UITextField || cur is UITextView || cur is WKWebView { return false }
                v = cur.superview
            }
        }
        return true
    }
}
