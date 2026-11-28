import HermeshKit
import HermeshProto
import HermeshSessions
import ImageIO
import Photos
import SwiftUI
import UIKit

nonisolated struct ReplyImageItem: Equatable, Hashable, Sendable {
    let path: String
    let caption: String
}

nonisolated struct ReplyImages: Equatable, Sendable {
    let explicit: [String]
    let gallery: [ReplyImageItem]
}

extension EnvironmentValues {
    @Entry var replyImages: ReplyImages?
}

/// One picture in the Lightbox: a reply's file, or a picture the owner sent with a prompt (kept by the machine as a
/// blob, with nothing to save or find, so the Lightbox has no menu for it).
enum LightboxItem: Hashable {
    case file(ReplyImageItem)
    case attachment(Hermesh_Session_V1_AttachmentRef)
    /// A picture already on this iPhone (Her's conversation): its name, and whether it can be kept (one Her sent can;
    /// the owner's own has no menu).
    case picture(String, UIImage, keep: Bool)

    /// A picture already here, as the Lightbox reads it.
    var picture: UIImage? {
        if case .picture(_, let image, _) = self { return image }
        return nil
    }
}

extension SessionImageStore.Loaded {
    /// A picture already on this iPhone, as it is: its size in pixels, its name.
    init(picture: UIImage, name: String) {
        self.init(thumbnail: picture, data: Data(), mimeType: "image/png", pixelWidth: Int(picture.size.width * picture.scale),
                  pixelHeight: Int(picture.size.height * picture.scale), sizeBytes: 0, turn: 0, folder: "", resolvedPath: name)
    }
}

struct SessionLightboxPresentation: Identifiable, Equatable {
    let id = UUID()
    let sessionID: String
    let machineID: String
    let run: String
    let items: [LightboxItem]
    let initialIndex: Int
}

nonisolated private struct DecodedSessionImage: @unchecked Sendable {
    let thumbnail: UIImage
    let width: Int
    let height: Int
}

nonisolated private enum SessionImageDecoder {
    static func decode(data: Data, maxPixelSize: Int) -> DecodedSessionImage? {
        guard !data.isEmpty,
              let src = CGImageSourceCreateWithData(data as CFData, [kCGImageSourceShouldCache: false] as CFDictionary)
        else { return nil }
        var width = 0
        var height = 0
        if let props = CGImageSourceCopyPropertiesAtIndex(src, 0, nil) as? [CFString: Any] {
            width = (props[kCGImagePropertyPixelWidth] as? NSNumber)?.intValue ?? 0
            height = (props[kCGImagePropertyPixelHeight] as? NSNumber)?.intValue ?? 0
        }
        let opts: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceShouldCacheImmediately: true,
            kCGImageSourceThumbnailMaxPixelSize: maxPixelSize,
        ]
        if let cg = CGImageSourceCreateThumbnailAtIndex(src, 0, opts as CFDictionary) {
            let img = UIImage(cgImage: cg)
            if width == 0 || height == 0 {
                width = cg.width
                height = cg.height
            }
            return DecodedSessionImage(thumbnail: img, width: width, height: height)
        }
        guard let fallback = UIImage(data: data) else { return nil }
        if width == 0 || height == 0 {
            width = Int(fallback.size.width * fallback.scale)
            height = Int(fallback.size.height * fallback.scale)
        }
        return DecodedSessionImage(thumbnail: fallback, width: width, height: height)
    }
}

/// Bounded-concurrency LRU store for coding-session images on iPhone: inline cards use
/// off-main-actor downsampled thumbnails while the Lightbox decodes full resolution on demand.
@MainActor @Observable final class SessionImageStore {
    static let shared = SessionImageStore()

    struct Loaded {
        let thumbnail: UIImage
        let data: Data
        let mimeType: String
        let pixelWidth: Int
        let pixelHeight: Int
        let sizeBytes: UInt64
        let turn: UInt32
        let folder: String
        let resolvedPath: String
    }

    enum State {
        case loading
        case ready(Loaded)
        case offline
        case missing
        case tooLarge(UInt64)
        case failed(String)
    }

    private let maxEntries = 28
    private let maxInFlight = 3
    private(set) var entries: [String: State] = [:]
    @ObservationIgnored private var order: [String] = []

    private init() {
        NotificationCenter.default.addObserver(forName: UIApplication.didReceiveMemoryWarningNotification, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated {
                guard let self else { return }
                for k in self.order where !self.inFlightKeys.contains(k) && !self.queuedKeys.contains(k) {
                    self.entries.removeValue(forKey: k)
                }
                self.order.removeAll { !self.inFlightKeys.contains($0) && !self.queuedKeys.contains($0) }
            }
        }
    }
    private struct Job {
        let key: String
        let api: AppAPI
        let sessionID: String
        let run: String
        let path: String
        /// A prompt's picture, read by its blob in place of `path`.
        var attachment: Hermesh_Session_V1_AttachmentRef?
        var machineID = ""
    }

    @ObservationIgnored private var queue: [Job] = []
    @ObservationIgnored private var queuedKeys: Set<String> = []
    @ObservationIgnored private var inFlightKeys: Set<String> = []
    @ObservationIgnored private var activeCount = 0

    func key(sessionID: String, run: String, path: String) -> String {
        "\(sessionID)\u{0}\(run)\u{0}\(path)"
    }

    func state(sessionID: String, run: String, path: String) -> State? {
        entries[key(sessionID: sessionID, run: run, path: path)]
    }

    func state(attachment: Hermesh_Session_V1_AttachmentRef) -> State? {
        entries["blob\u{0}\(attachment.blobSha256)"]
    }

    func request(api: AppAPI?, sessionID: String, run: String, path: String, retryOffline: Bool = false, urgent: Bool = false) {
        guard let api, !sessionID.isEmpty, !path.isEmpty else { return }
        let k = key(sessionID: sessionID, run: run, path: path)
        enqueue(Job(key: k, api: api, sessionID: sessionID, run: run, path: path), retryOffline: retryOffline, urgent: urgent)
    }

    /// A picture the owner sent with a prompt: from this iPhone when it was sent from here or seen before, else its blob.
    func request(api: AppAPI?, sessionID: String, machineID: String, attachment: Hermesh_Session_V1_AttachmentRef, retryOffline: Bool = false, urgent: Bool = false) {
        guard let api, !attachment.blobSha256.isEmpty else { return }
        let k = "blob\u{0}\(attachment.blobSha256)"
        enqueue(Job(key: k, api: api, sessionID: sessionID, run: "", path: attachment.name, attachment: attachment, machineID: machineID), retryOffline: retryOffline, urgent: urgent)
    }

    private func enqueue(_ job: Job, retryOffline: Bool, urgent: Bool = false) {
        let k = job.key
        if let existing = entries[k] {
            if case .offline = existing, retryOffline {
                entries.removeValue(forKey: k)
            } else {
                if urgent, queuedKeys.contains(k), let idx = queue.firstIndex(where: { $0.key == k }), idx > 0 {
                    let picked = queue.remove(at: idx)
                    queue.insert(picked, at: 0)
                }
                return
            }
        }
        if inFlightKeys.contains(k) { return }
        if queuedKeys.contains(k) {
            if urgent, let idx = queue.firstIndex(where: { $0.key == k }), idx > 0 {
                let picked = queue.remove(at: idx)
                queue.insert(picked, at: 0)
            }
            return
        }
        store(k, .loading)
        queuedKeys.insert(k)
        if urgent { queue.insert(job, at: 0) } else { queue.append(job) }
        pump()
    }

    private func store(_ key: String, _ value: State) {
        entries[key] = value
        order.removeAll { $0 == key }
        order.append(key)
        while order.count > maxEntries {
            let oldest = order.removeFirst()
            entries.removeValue(forKey: oldest)
        }
    }

    private func pump() {
        while activeCount < maxInFlight, !queue.isEmpty {
            let next = queue.removeFirst()
            queuedKeys.remove(next.key)
            guard !inFlightKeys.contains(next.key) else { continue }
            if let cur = entries[next.key], !isLoading(cur) { continue }
            inFlightKeys.insert(next.key)
            activeCount += 1
            Task {
                await loadOne(next)
                inFlightKeys.remove(next.key)
                activeCount -= 1
                pump()
            }
        }
    }

    private func isLoading(_ state: State) -> Bool {
        if case .loading = state { return true }
        return false
    }

    private func loadOne(_ job: Job) async {
        if let att = job.attachment { return await loadAttachment(job, att) }
        do {
            let res = try await job.api.readFile(job.sessionID, run: job.run, path: job.path)
            if res.kind == .image, !res.image.isEmpty {
                let raw = res.image
                let decoded = await Task.detached(priority: .userInitiated) {
                    SessionImageDecoder.decode(data: raw, maxPixelSize: 768)
                }.value
                if let decoded {
                    store(job.key, .ready(Loaded(
                        thumbnail: decoded.thumbnail,
                        data: raw,
                        mimeType: res.mimeType.isEmpty ? "image/png" : res.mimeType,
                        pixelWidth: decoded.width,
                        pixelHeight: decoded.height,
                        sizeBytes: res.size > 0 ? res.size : UInt64(raw.count),
                        turn: res.turn,
                        folder: res.folder,
                        resolvedPath: res.path.isEmpty ? job.path : res.path
                    )))
                } else {
                    store(job.key, .missing)
                }
            } else if res.kind == .tooLarge {
                store(job.key, .tooLarge(res.size))
            } else {
                store(job.key, .missing)
            }
        } catch {
            switch (error as? APIError)?.code {
            case "unavailable", "deadline_exceeded":
                store(job.key, .offline)
            case "not_found", "permission_denied":
                store(job.key, .missing)
            default:
                store(job.key, .failed(error.localizedDescription))
            }
        }
    }

    /// A prompt's picture that could not be read, asked for again.
    func retry(api: AppAPI?, sessionID: String, machineID: String, attachment: Hermesh_Session_V1_AttachmentRef) {
        entries.removeValue(forKey: "blob\u{0}\(attachment.blobSha256)")
        request(api: api, sessionID: sessionID, machineID: machineID, attachment: attachment)
    }

    private func loadAttachment(_ job: Job, _ att: Hermesh_Session_V1_AttachmentRef) async {
        do {
            let raw = try await SessionAttachmentStore.shared.data(api: job.api, sessionID: job.sessionID, machineID: job.machineID, ref: att)
            let decoded = await Task.detached(priority: .userInitiated) {
                SessionImageDecoder.decode(data: raw, maxPixelSize: 960)
            }.value
            guard let decoded else { return store(job.key, .missing) }
            // Read at last: the bubble's thumbnail too, where it could not be before.
            SessionAttachmentStore.shared.keep(sha256: att.blobSha256, name: att.name, data: raw, picture: decoded.thumbnail)
            store(job.key, .ready(Loaded(
                thumbnail: decoded.thumbnail,
                data: raw,
                mimeType: att.mimeType.isEmpty ? "image/png" : att.mimeType,
                pixelWidth: decoded.width,
                pixelHeight: decoded.height,
                sizeBytes: UInt64(raw.count),
                turn: 0,
                folder: "",
                resolvedPath: att.name
            )))
        } catch {
            switch (error as? APIError)?.code {
            case "unavailable", "deadline_exceeded": store(job.key, .offline)
            case "not_found", "permission_denied": store(job.key, .missing)
            default: store(job.key, .failed(error.localizedDescription))
            }
        }
    }

    func shareURL(for loaded: Loaded) -> URL? {
        let name = fileBaseName(loaded.resolvedPath)
        let safe = name.isEmpty ? "image.png" : name
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("Session Images/\(UUID().uuidString)", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appendingPathComponent(safe)
        do {
            try loaded.data.write(to: url, options: .atomic)
            return url
        } catch {
            return nil
        }
    }

    static func saveToPhotos(_ data: Data) async -> Bool {
        let status = await PHPhotoLibrary.requestAuthorization(for: .addOnly)
        guard status == .authorized || status == .limited else { return false }
        return await withCheckedContinuation { cont in
            PHPhotoLibrary.shared().performChanges({
                let req = PHAssetCreationRequest.forAsset()
                req.addResource(with: .photo, data: data, options: nil)
            }, completionHandler: { ok, _ in
                cont.resume(returning: ok)
            })
        }
    }

    static func fullPath(folder: String, path: String) -> String {
        let isAbs = path.hasPrefix("/") || path.hasPrefix("~/") || path.range(of: #"^[A-Za-z]:[\\/]"#, options: .regularExpression) != nil
        if folder.isEmpty || isAbs { return path }
        let base = folder.hasSuffix("/") ? String(folder.dropLast()) : folder
        return "\(base)/\(path)"
    }
}

// MARK: - Inline Image Strip

struct SessionImageStrip: View {
    let items: [ReplyImageItem]
    let fromLinks: Bool
    var draft = false

    @Environment(AppModel.self) private var model
    @Environment(\.sessionFiles) private var files
    @Environment(\.filesRun) private var run
    @Environment(\.replyImages) private var replyImages

    var body: some View {
        if draft {
            if !fromLinks {
                RoundedRectangle(cornerRadius: 14, style: .continuous)
                    .fill(Palette.fill)
                    .frame(maxWidth: 240, minHeight: 180, maxHeight: 180)
                    .overlay(RoundedRectangle(cornerRadius: 14, style: .continuous).strokeBorder(Palette.separator, lineWidth: 0.5))
            }
        } else if let files {
            let gallery = (replyImages?.gallery.isEmpty == false) ? replyImages!.gallery : items
            if items.count == 1 {
                SessionInlineImageCard(item: items[0], mode: .single, fromLinks: fromLinks, files: files, run: run, gallery: gallery)
            } else if items.count == 2 {
                HStack(alignment: .top, spacing: 8) {
                    ForEach(Array(items.enumerated()), id: \.offset) { _, it in
                        SessionInlineImageCard(item: it, mode: .pair, fromLinks: fromLinks, files: files, run: run, gallery: gallery)
                    }
                }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("session-image-row")
            } else {
                SessionImageStageView(items: items, fromLinks: fromLinks, files: files, run: run, gallery: gallery)
            }
        }
    }
}

private enum InlineCardMode {
    case single, pair, stage
}

private func hasDistinctImageCaption(_ caption: String, name: String) -> Bool {
    let trimmed = caption.trimmingCharacters(in: .whitespacesAndNewlines)
    return !trimmed.isEmpty && trimmed.lowercased() != name.lowercased()
}

private struct SessionImageStageView: View {
    let items: [ReplyImageItem]
    let fromLinks: Bool
    let files: SessionFilesModel
    let run: String
    let gallery: [ReplyImageItem]

    @Environment(AppModel.self) private var model
    @State private var activeIndex = 0

    private var safeIndex: Int {
        max(0, min(items.count - 1, activeIndex))
    }

    private var activeItem: ReplyImageItem {
        items[safeIndex]
    }

    private var anyCaption: Bool {
        items.contains { hasDistinctImageCaption($0.caption, name: fileBaseName($0.path)) }
    }

    var body: some View {
        let activeName = fileBaseName(activeItem.path)
        VStack(alignment: .leading, spacing: 8) {
            TabView(selection: $activeIndex) {
                ForEach(Array(items.enumerated()), id: \.offset) { idx, it in
                    SessionInlineImageCard(
                        item: it,
                        mode: .stage,
                        fromLinks: fromLinks,
                        files: files,
                        run: run,
                        gallery: gallery,
                        urgent: abs(idx - safeIndex) <= 1 || idx == 0
                    )
                    .tag(idx)
                }
            }
            .tabViewStyle(.page(indexDisplayMode: .never))
            .frame(height: 208)

            HStack(alignment: .center, spacing: 8) {
                ScrollViewReader { proxy in
                    ScrollView(.horizontal, showsIndicators: false) {
                        HStack(spacing: 4) {
                            ForEach(Array(items.enumerated()), id: \.offset) { idx, it in
                                let tag = shortImageTag(it.caption, fileName: fileBaseName(it.path))
                                let selected = idx == safeIndex
                                Button {
                                    withAnimation(.snappy(duration: 0.2)) { activeIndex = idx }
                                } label: {
                                    HStack(spacing: 4) {
                                        Text(verbatim: String(format: "%02d", idx + 1))
                                            .font(.caption2.monospaced())
                                        if !tag.isEmpty {
                                            Text(verbatim: tag)
                                                .font(.caption.weight(selected ? .semibold : .regular))
                                                .lineLimit(1)
                                        }
                                    }
                                    .foregroundStyle(selected ? .primary : .tertiary)
                                    .padding(.horizontal, 8)
                                    .padding(.vertical, 4)
                                    .background(selected ? Palette.fill2 : Color.clear, in: RoundedRectangle(cornerRadius: 6, style: .continuous))
                                }
                                .buttonStyle(.plain)
                                .accessibilityLabel(Text(verbatim: it.caption.isEmpty ? fileBaseName(it.path) : it.caption))
                                .accessibilityIdentifier("session-image-thumb-\(idx)")
                                .id(idx)
                            }
                        }
                    }
                    .onChange(of: safeIndex) { _, next in
                        withAnimation(.easeOut(duration: 0.2)) {
                            proxy.scrollTo(next, anchor: .center)
                        }
                    }
                }

                Text(verbatim: "\(safeIndex + 1) / \(items.count)")
                    .font(.caption2.monospacedDigit())
                    .foregroundStyle(.tertiary)
                    .accessibilityIdentifier("session-image-counter")
            }
            .padding(.bottom, 5)
            .overlay(alignment: .bottom) { Rectangle().fill(Palette.separator).frame(height: 0.5) }

            if anyCaption {
                Text(verbatim: activeItem.caption.isEmpty ? activeName : activeItem.caption)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .accessibilityIdentifier("session-image-caption")
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("session-image-row")
    }
}

private struct SessionInlineImageCard: View {
    let item: ReplyImageItem
    let mode: InlineCardMode
    let fromLinks: Bool
    let files: SessionFilesModel
    let run: String
    let gallery: [ReplyImageItem]
    var urgent = false

    @Environment(AppModel.self) private var model
    @State private var sharing: URL?

    private var sessionID: String { files.sessionID }
    private var machineID: String { model.session(sessionID)?.machineID ?? "" }
    private var online: Bool {
        guard let m = model.machines.first(where: { $0.machineID == machineID }) else { return true }
        return m.local || (model.status.relayConnected && m.online)
    }

    var body: some View {
        let info = files.info(run: run, path: item.path)
        let goneByInfo = info?.kind == .missing || info?.kind == .outside
        let effectivePath = if let info, !goneByInfo, !info.path.isEmpty { info.path } else { item.path }
        let state = SessionImageStore.shared.state(sessionID: sessionID, run: run, path: effectivePath)

        Group {
            if goneByInfo || isMissing(state) {
                if !fromLinks {
                    Text(verbatim: item.caption.isEmpty ? fileBaseName(item.path) : item.caption)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .padding(.horizontal, 8).padding(.vertical, 4)
                        .background(Palette.fill2, in: RoundedRectangle(cornerRadius: 6, style: .continuous))
                }
            } else if case .offline? = state {
                offlineCard(text: String(localized: "\(model.machineName(machineID)) is offline · Shows automatically when back"), icon: "icloud.slash")
                    .accessibilityIdentifier("session-image-offline")
            } else if case .tooLarge(let bytes)? = state {
                offlineCard(text: String(localized: "This file is \(ByteCountFormatter.string(fromByteCount: Int64(bytes), countStyle: .file)), too large to show here"), icon: "doc")
            } else if case .failed(let msg)? = state {
                if !fromLinks { offlineCard(text: msg, icon: "exclamationmark.triangle") }
            } else if case .ready(let loaded)? = state {
                readyCard(loaded, effectivePath: effectivePath)
            } else {
                loadingCard
            }
        }
        .sheet(item: $sharing) { HerShareSheet(url: $0).presentationDetents([.medium, .large]) }
        .task(id: "\(sessionID)\u{0}\(run)\u{0}\(effectivePath)\u{0}\(online)\u{0}\(goneByInfo)") {
            guard !goneByInfo else { return }
            let isOff: Bool = if case .offline? = state { true } else { false }
            SessionImageStore.shared.request(api: model.api, sessionID: sessionID, run: run, path: effectivePath, retryOffline: isOff && online, urgent: urgent)
        }
    }

    private func isMissing(_ state: SessionImageStore.State?) -> Bool {
        if case .missing? = state { return true }
        return false
    }

    private func offlineCard(text: String, icon: String) -> some View {
        HStack(spacing: 8) {
            Image(systemName: icon).foregroundStyle(.tertiary)
            Text(verbatim: text).font(.footnote).foregroundStyle(.secondary).lineLimit(2)
        }
        .padding(.horizontal, 12).padding(.vertical, 10)
        .frame(maxWidth: mode == .stage ? .infinity : nil, maxHeight: mode == .stage ? .infinity : nil)
        .background(mode == .stage ? Color.clear : Palette.fill, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        .overlay {
            if mode != .stage {
                RoundedRectangle(cornerRadius: 12, style: .continuous).strokeBorder(Palette.separator, lineWidth: 0.5)
            }
        }
    }

    private var loadingCard: some View {
        let h: CGFloat = mode == .single ? 200 : (mode == .stage ? 214 : 184)
        return ProgressView()
            .frame(maxWidth: mode == .single ? 200 : .infinity, minHeight: h, maxHeight: h)
            .background(mode == .stage ? Color.clear : Palette.fill, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .overlay {
                if mode != .stage {
                    RoundedRectangle(cornerRadius: 14, style: .continuous).strokeBorder(Palette.separator, lineWidth: 0.5)
                }
            }
    }

    @ViewBuilder private func readyCard(_ loaded: SessionImageStore.Loaded, effectivePath: String) -> some View {
        let name = fileBaseName(loaded.resolvedPath)
        let showPairCaption = mode == .pair && hasDistinctImageCaption(item.caption, name: name)
        let ratio = CGFloat(max(loaded.pixelWidth, 1)) / CGFloat(max(loaded.pixelHeight, 1))
        let open = {
            files.openLightbox(run: run, path: item.path, machineID: machineID, gallery: gallery)
        }

        Button(action: open) {
            VStack(alignment: .leading, spacing: 6) {
                Group {
                    switch mode {
                    case .single:
                        let maxH: CGFloat = 260
                        let naturalW = min(maxH * ratio, 310)
                        Image(uiImage: loaded.thumbnail)
                            .resizable()
                            .scaledToFit()
                            .frame(maxWidth: max(110, naturalW), maxHeight: maxH)
                    case .pair:
                        Image(uiImage: loaded.thumbnail)
                            .resizable()
                            .scaledToFit()
                            .frame(maxWidth: .infinity, maxHeight: 204)
                    case .stage:
                        Image(uiImage: loaded.thumbnail)
                            .resizable()
                            .scaledToFit()
                            .frame(maxWidth: .infinity, maxHeight: 208, alignment: .leading)
                    }
                }
                .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous).strokeBorder(Palette.separator.opacity(0.65), lineWidth: 0.5))

                if showPairCaption {
                    Text(verbatim: item.caption)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
        }
        .buttonStyle(.plain)
        .accessibilityLabel(Text(verbatim: item.caption.isEmpty ? name : item.caption))
        .accessibilityIdentifier("session-image-\(name)")
        .contextMenu {
            Button("View Larger", systemImage: "arrow.up.left.and.arrow.down.right") { open() }
            Button("Save to Photos", systemImage: "square.and.arrow.down") {
                Task {
                    let ok = await SessionImageStore.saveToPhotos(loaded.data)
                    model.show(ok ? String(localized: "Saved to Photos") : String(localized: "Couldn’t save to Photos"))
                }
            }
            Button("Share…", systemImage: "square.and.arrow.up") {
                sharing = SessionImageStore.shared.shareURL(for: loaded)
            }
            Divider()
            Button("Copy Path", systemImage: "doc.on.doc") {
                UIPasteboard.general.string = SessionImageStore.fullPath(folder: loaded.folder, path: loaded.resolvedPath)
                model.show(String(localized: "Copied"))
            }
        }
    }
}

// MARK: - Full-Screen Blurred Lightbox

struct SessionImageLightbox: View {
    let presentation: SessionLightboxPresentation
    @Environment(AppModel.self) private var model
    @Environment(\.sessionFiles) private var envFiles
    @Environment(\.dismiss) private var dismiss

    @State private var index: Int
    @State private var zoomed = false
    @State private var dragY: CGFloat = 0
    @State private var sharing: URL?

    init(presentation: SessionLightboxPresentation) {
        self.presentation = presentation
        _index = State(initialValue: max(0, min(presentation.items.count - 1, presentation.initialIndex)))
    }

    private var currentItem: LightboxItem {
        presentation.items.indices.contains(index) ? presentation.items[index] : presentation.items[0]
    }

    /// The owner's own pictures: nothing to save, share or find, so no menu.
    private var ownPictures: Bool {
        switch currentItem {
        case .attachment, .picture(_, _, keep: false): true
        default: false
        }
    }

    private var online: Bool {
        guard let m = model.machines.first(where: { $0.machineID == presentation.machineID }) else { return true }
        return m.local || (model.status.relayConnected && m.online)
    }

    var body: some View {
        let files = envFiles ?? model.sessionFiles.model(presentation.sessionID, api: model.api)
        let curPath = resolvedAskPath(files: files, item: currentItem)
        let curState = state(of: currentItem, path: curPath)
        let name = fileBaseName(loadedOf(curState)?.resolvedPath ?? curPath)
        let meta = metadataLine(loaded: loadedOf(curState))
        let fade = max(0.25, 1.0 - abs(dragY) / 320.0)

        ZStack(alignment: .top) {
            ZStack {
                Rectangle()
                    .fill(.ultraThinMaterial)
                    .environment(\.colorScheme, .dark)
                Color.black.opacity(0.54)
            }
            .opacity(fade)
            .ignoresSafeArea()
            .onTapGesture { dismiss() }

            TabView(selection: $index) {
                ForEach(Array(presentation.items.enumerated()), id: \.offset) { idx, item in
                    let askPath = resolvedAskPath(files: files, item: item)
                    LightboxPage(
                        sessionID: presentation.sessionID,
                        machineID: presentation.machineID,
                        run: presentation.run,
                        path: askPath,
                        attachment: { if case .attachment(let a) = item { a } else { nil } }(),
                        picture: item.picture,
                        online: online,
                        isCurrent: idx == index,
                        zoomed: $zoomed
                    )
                    .tag(idx)
                }
            }
            .tabViewStyle(.page(indexDisplayMode: .never))
            .offset(y: dragY)
            .scaleEffect(max(0.86, 1.0 - abs(dragY) / 1100.0))
            .ignoresSafeArea()
            .simultaneousGesture(
                DragGesture(minimumDistance: 16)
                    .onChanged { v in
                        guard !zoomed, abs(v.translation.height) > abs(v.translation.width) * 1.15 else { return }
                        dragY = v.translation.height
                    }
                    .onEnded { v in
                        guard dragY != 0 else { return }
                        if abs(dragY) > 90 || abs(v.predictedEndTranslation.height) > 220 {
                            dismiss()
                        } else {
                            withAnimation(.spring(duration: 0.24)) { dragY = 0 }
                        }
                    }
            )

            // Symmetric decluttered top bar: Close on left · Wide centered filename & metadata · More menu on right.
            HStack(spacing: 10) {
                Button { dismiss() } label: {
                    Image(systemName: "xmark")
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(.white)
                        .frame(width: 38, height: 38)
                        .background(.white.opacity(0.14), in: .circle)
                        .overlay(Circle().strokeBorder(.white.opacity(0.16), lineWidth: 0.5))
                }
                .accessibilityLabel(Text("Close"))
                .accessibilityIdentifier("lightbox-close")

                VStack(spacing: 2) {
                    Text(verbatim: name)
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(.white)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .accessibilityIdentifier("lightbox-title")
                    if !meta.isEmpty {
                        Text(verbatim: meta)
                            .font(.caption2.monospacedDigit())
                            .foregroundStyle(.white.opacity(0.66))
                            .lineLimit(1)
                            .accessibilityIdentifier("lightbox-meta")
                    }
                }
                .frame(maxWidth: .infinity)

                if ownPictures {
                    Color.clear.frame(width: 38, height: 38)
                } else {
                    Menu {
                        if case .picture(_, let image, _) = currentItem {
                            // One Her sent: kept to Photos or shared, as it is here.
                            Button("Save to Photos", systemImage: "square.and.arrow.down") {
                                Task {
                                    let ok = await SessionImageStore.saveToPhotos(image.pngData() ?? Data())
                                    model.show(ok ? String(localized: "Saved to Photos") : String(localized: "Couldn’t save to Photos"))
                                }
                            }
                            Button("Share…", systemImage: "square.and.arrow.up") {
                                sharing = SessionImageStore.shared.shareURL(for: .init(thumbnail: image, data: image.pngData() ?? Data(), mimeType: "image/png",
                                                                                     pixelWidth: 0, pixelHeight: 0, sizeBytes: 0, turn: 0, folder: "", resolvedPath: name))
                            }
                        } else if let loaded = loadedOf(curState) {
                            Button("Save to Photos", systemImage: "square.and.arrow.down") {
                                Task {
                                    let ok = await SessionImageStore.saveToPhotos(loaded.data)
                                    model.show(ok ? String(localized: "Saved to Photos") : String(localized: "Couldn’t save to Photos"))
                                }
                            }
                            Button("Share…", systemImage: "square.and.arrow.up") {
                                sharing = SessionImageStore.shared.shareURL(for: loaded)
                            }
                            Divider()
                            Button("Copy Path", systemImage: "doc.on.doc") {
                                UIPasteboard.general.string = SessionImageStore.fullPath(folder: loaded.folder, path: loaded.resolvedPath)
                                model.show(String(localized: "Copied"))
                            }
                        } else {
                            Button("Copy Path", systemImage: "doc.on.doc") {
                                UIPasteboard.general.string = curPath
                                model.show(String(localized: "Copied"))
                            }
                        }
                    } label: {
                        Image(systemName: "ellipsis")
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(.white)
                            .frame(width: 38, height: 38)
                            .background(.white.opacity(0.14), in: .circle)
                            .overlay(Circle().strokeBorder(.white.opacity(0.16), lineWidth: 0.5))
                    }
                    .accessibilityLabel(Text("More"))
                    .accessibilityIdentifier("lightbox-more")
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 8)
            .opacity(fade)
        }
        .presentationBackground(.clear)
        .preferredColorScheme(.dark)
        .onChange(of: index) { zoomed = false; dragY = 0 }
        .sheet(item: $sharing) { HerShareSheet(url: $0).presentationDetents([.medium, .large]) }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("session-image-lightbox")
    }

    private func resolvedAskPath(files: SessionFilesModel, item: LightboxItem) -> String {
        guard case .file(let item) = item else {
            if case .attachment(let a) = item { return a.name }
            if case .picture(let name, _, _) = item { return name }
            return ""
        }
        if let info = files.info(run: presentation.run, path: item.path),
           info.kind != .missing, info.kind != .outside, !info.path.isEmpty {
            return info.path
        }
        return item.path
    }

    private func state(of item: LightboxItem, path: String) -> SessionImageStore.State? {
        if case .attachment(let a) = item { return SessionImageStore.shared.state(attachment: a) }
        if let picture = item.picture { return .ready(.init(picture: picture, name: path)) }
        return SessionImageStore.shared.state(sessionID: presentation.sessionID, run: presentation.run, path: path)
    }

    private func loadedOf(_ state: SessionImageStore.State?) -> SessionImageStore.Loaded? {
        if case .ready(let loaded)? = state { return loaded }
        return nil
    }

    private func metadataLine(loaded: SessionImageStore.Loaded?) -> String {
        var parts: [String] = []
        if presentation.items.count > 1 {
            parts.append("\(index + 1) / \(presentation.items.count)")
        }
        if let loaded {
            if loaded.pixelWidth > 0, loaded.pixelHeight > 0 {
                parts.append("\(loaded.pixelWidth) × \(loaded.pixelHeight)")
            }
            if loaded.sizeBytes > 0 {
                parts.append(ByteCountFormatter.string(fromByteCount: Int64(loaded.sizeBytes), countStyle: .file))
            }
        }
        return parts.joined(separator: " · ")
    }
}

private struct LightboxPage: View {
    let sessionID: String
    let machineID: String
    let run: String
    let path: String
    /// A prompt's picture, read by its blob in place of `path` (then its name).
    let attachment: Hermesh_Session_V1_AttachmentRef?
    /// A picture already on this iPhone, shown as it is.
    var picture: UIImage?
    let online: Bool
    let isCurrent: Bool
    @Binding var zoomed: Bool

    @Environment(AppModel.self) private var model
    @State private var fullImage: UIImage?

    var body: some View {
        let state: SessionImageStore.State? = if let picture { .ready(.init(picture: picture, name: path)) } else {
            attachment.map { SessionImageStore.shared.state(attachment: $0) } ?? SessionImageStore.shared.state(sessionID: sessionID, run: run, path: path)
        }
        ZStack {
            switch state {
            case nil, .loading?:
                ProgressView().tint(.white)
                    .task(id: "\(sessionID)\u{0}\(run)\u{0}\(path)\u{0}\(online)") {
                        if picture == nil, attachment == nil, state == nil {
                            SessionImageStore.shared.request(api: model.api, sessionID: sessionID, run: run, path: path)
                        }
                    }
            case .ready(let loaded)?:
                ZoomableImageScrollView(
                    image: fullImage ?? loaded.thumbnail,
                    resetTrigger: isCurrent,
                    onZoomChanged: { isZoomed in if isCurrent { zoomed = isZoomed } }
                )
                .ignoresSafeArea()
                .accessibilityIdentifier("lightbox-image")
                .task(id: loaded.resolvedPath) {
                    let raw = loaded.data
                    let decoded = await Task.detached(priority: .userInitiated) { UIImage(data: raw) }.value
                    if let decoded { fullImage = decoded }
                }
            case .offline?:
                VStack(spacing: 8) {
                    Image(systemName: "icloud.slash").font(.title2).foregroundStyle(.white.opacity(0.6))
                    Text(verbatim: fileBaseName(path)).font(.subheadline.weight(.medium)).foregroundStyle(.white)
                    Text(verbatim: String(localized: "\(model.machineName(machineID)) is offline · Shows automatically when back"))
                        .font(.footnote).foregroundStyle(.white.opacity(0.7)).multilineTextAlignment(.center)
                    retryButton
                }
                .padding(24)
            case .tooLarge(let bytes)?:
                VStack(spacing: 8) {
                    Image(systemName: "doc").font(.title2).foregroundStyle(.white.opacity(0.6))
                    Text(verbatim: fileBaseName(path)).font(.subheadline.weight(.medium)).foregroundStyle(.white)
                    Text(verbatim: String(localized: "This file is \(ByteCountFormatter.string(fromByteCount: Int64(bytes), countStyle: .file)), too large to show here"))
                        .font(.footnote).foregroundStyle(.white.opacity(0.7)).multilineTextAlignment(.center)
                }
                .padding(24)
            case .missing?, .failed?:
                VStack(spacing: 8) {
                    Image(systemName: "doc").font(.title2).foregroundStyle(.white.opacity(0.6))
                    Text(verbatim: fileBaseName(path)).font(.subheadline.weight(.medium)).foregroundStyle(.white)
                    Text("File not found").font(.footnote).foregroundStyle(.white.opacity(0.7))
                    retryButton
                }
                .padding(24)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .task(id: "\(sessionID)\u{0}\(run)\u{0}\(path)\u{0}\(online)") {
            guard picture == nil else { return }
            let isOff: Bool = if case .offline? = state { true } else { false }
            if let attachment {
                SessionImageStore.shared.request(api: model.api, sessionID: sessionID, machineID: machineID, attachment: attachment, retryOffline: isOff && online)
            } else {
                SessionImageStore.shared.request(api: model.api, sessionID: sessionID, run: run, path: path, retryOffline: isOff && online)
            }
        }
    }
}

extension LightboxPage {
    /// A prompt's picture that could not be read: asked for again (a reply's file comes back by itself).
    @ViewBuilder var retryButton: some View {
        if let attachment {
            Button("Retry") { SessionImageStore.shared.retry(api: model.api, sessionID: sessionID, machineID: machineID, attachment: attachment) }
                .buttonStyle(.glass)
                .tint(.white)
                .padding(.top, 4)
                .accessibilityIdentifier("lightbox-retry")
        }
    }
}

private struct ZoomableImageScrollView: UIViewRepresentable {
    let image: UIImage
    let resetTrigger: Bool
    let onZoomChanged: (Bool) -> Void

    func makeCoordinator() -> Coordinator { Coordinator(onZoomChanged: onZoomChanged) }

    func makeUIView(context: Context) -> CenteringZoomScrollView {
        let sv = CenteringZoomScrollView()
        sv.delegate = context.coordinator
        sv.minimumZoomScale = 1.0
        sv.maximumZoomScale = 4.0
        sv.bouncesZoom = true
        sv.showsHorizontalScrollIndicator = false
        sv.showsVerticalScrollIndicator = false
        sv.backgroundColor = .clear
        sv.contentInsetAdjustmentBehavior = .never
        sv.imageView.image = image
        context.coordinator.scrollView = sv

        let doubleTap = UITapGestureRecognizer(target: context.coordinator, action: #selector(Coordinator.handleDoubleTap(_:)))
        doubleTap.numberOfTapsRequired = 2
        sv.addGestureRecognizer(doubleTap)
        return sv
    }

    func updateUIView(_ sv: CenteringZoomScrollView, context: Context) {
        context.coordinator.onZoomChanged = onZoomChanged
        if sv.imageView.image !== image {
            sv.imageView.image = image
            sv.setNeedsLayout()
        }
        if !resetTrigger, sv.zoomScale > 1.01 {
            sv.setZoomScale(1.0, animated: false)
        }
    }

    final class Coordinator: NSObject, UIScrollViewDelegate {
        weak var scrollView: CenteringZoomScrollView?
        var onZoomChanged: (Bool) -> Void

        init(onZoomChanged: @escaping (Bool) -> Void) {
            self.onZoomChanged = onZoomChanged
        }

        func viewForZooming(in scrollView: UIScrollView) -> UIView? {
            (scrollView as? CenteringZoomScrollView)?.imageView
        }

        func scrollViewDidZoom(_ scrollView: UIScrollView) {
            (scrollView as? CenteringZoomScrollView)?.centerImage()
            onZoomChanged(scrollView.zoomScale > 1.02)
        }

        @objc func handleDoubleTap(_ recognizer: UITapGestureRecognizer) {
            guard let sv = scrollView else { return }
            if sv.zoomScale > 1.05 {
                sv.setZoomScale(1.0, animated: true)
            } else {
                let pt = recognizer.location(in: sv.imageView)
                let scale: CGFloat = 2.4
                let w = sv.bounds.width / scale
                let h = sv.bounds.height / scale
                let rect = CGRect(x: pt.x - w / 2, y: pt.y - h / 2, width: w, height: h)
                sv.zoom(to: rect, animated: true)
            }
        }
    }
}

private final class CenteringZoomScrollView: UIScrollView {
    let imageView = UIImageView()
    private var lastBoundsSize: CGSize = .zero

    override init(frame: CGRect) {
        super.init(frame: frame)
        imageView.contentMode = .scaleAspectFit
        imageView.clipsToBounds = true
        addSubview(imageView)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override func layoutSubviews() {
        super.layoutSubviews()
        guard bounds.width > 0, bounds.height > 0 else { return }
        if bounds.size != lastBoundsSize {
            lastBoundsSize = bounds.size
            zoomScale = 1.0
            fitImage()
        } else if zoomScale <= 1.01, imageView.bounds.size == .zero {
            fitImage()
        }
        centerImage()
    }

    func fitImage() {
        guard let img = imageView.image, img.size.width > 0, img.size.height > 0, bounds.width > 0, bounds.height > 0 else { return }
        let padTop: CGFloat = 68
        let padBottom: CGFloat = 28
        let padSide: CGFloat = 12
        let availW = max(1, bounds.width - padSide * 2)
        let availH = max(1, bounds.height - padTop - padBottom)
        let scale = min(availW / img.size.width, availH / img.size.height)
        let w = floor(img.size.width * scale)
        let h = floor(img.size.height * scale)
        imageView.frame = CGRect(x: 0, y: 0, width: w, height: h)
        contentSize = CGSize(width: w, height: h)
    }

    func centerImage() {
        let offsetX = max((bounds.width - contentSize.width) * 0.5, 0)
        let offsetY = max((bounds.height - contentSize.height) * 0.5 + 14, 0)
        imageView.center = CGPoint(x: contentSize.width * 0.5 + offsetX, y: contentSize.height * 0.5 + offsetY)
    }
}
