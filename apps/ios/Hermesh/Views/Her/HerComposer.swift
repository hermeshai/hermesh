import HermeshHer
import HermeshKit
import PhotosUI
import SwiftUI
import UniformTypeIdentifiers

/// The composer under Her's conversation: words, photos and files, and only
/// Send; every message goes in as it is sent. Without a model, the
/// onboarding in the conversation says what to do and the composer waits.
struct HerComposer: View {
    @Environment(AppModel.self) private var model
    @Bindable var chat: HerChat
    let machine: HerMachine
    let state: HerEnvState
    let noModel: Bool
    /// The message goes on the conversation itself; otherwise through the core, which may keep it here (HerSend).
    let direct: Bool
    @State private var photos: [PhotosPickerItem] = []
    @State private var pickingPhotos = false
    @State private var pickingFiles = false
    @State private var adding = false
    @State private var sending = false
    @FocusState private var focused: Bool
    /// The field's own height, and the height it keeps: it grows at once and shrinks only once the text has been
    /// still for a moment. Typing Chinese, the letters being composed are wider than the characters they become, so
    /// at a line's end they wrap and come back with each character; held, the composer doesn't jump up and down.
    @State private var fieldHeight: CGFloat = 0
    @State private var heldHeight: CGFloat = 0

    var body: some View {
        let draft = chat.draft
        VStack(alignment: .leading, spacing: 8) {
            if let note { Label(note, systemImage: "clock").font(.footnote).foregroundStyle(.secondary).padding(.horizontal, 12) }
            VStack(alignment: .leading, spacing: 6) {
                if !draft.photos.isEmpty || !draft.files.isEmpty {
                    ScrollView(.horizontal) {
                        HStack(spacing: 6) {
                            ForEach(draft.photos) { p in
                                ZStack(alignment: .topTrailing) {
                                    if let picture = UIImage(data: p.data) {
                                        Image(uiImage: picture).resizable().scaledToFill().frame(width: 56, height: 56)
                                            .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
                                    }
                                    Button { chat.draft.photos.removeAll { $0.id == p.id } } label: {
                                        Image(systemName: "xmark.circle.fill").symbolRenderingMode(.palette).foregroundStyle(.white, .black.opacity(0.55))
                                    }
                                    .padding(3)
                                    .accessibilityLabel(Text("Remove \(p.name)"))
                                }
                            }
                            ForEach(draft.files) { f in HerChip(name: f.name) { chat.draft.files.removeAll { $0.id == f.id } } }
                        }
                        .padding(.horizontal, 6).padding(.top, 6)
                    }
                    .scrollIndicators(.hidden)
                }
                HStack(alignment: .bottom, spacing: 8) {
                    // A small popover out of the plus, not a Menu: a menu opens out of the composer's whole glass
                    // and takes the field away meanwhile; the popover leaves the composer as it is.
                    Button { adding = true } label: {
                        Image(systemName: "plus").font(.body.weight(.medium)).frame(width: 36, height: 36)
                            .background(Palette.fill2, in: .circle)
                    }
                    .tint(.primary)
                    .accessibilityLabel(Text("Add Photos or Files"))
                    .accessibilityIdentifier("her-attach")
                    .popover(isPresented: $adding, arrowEdge: .bottom) {
                        VStack(alignment: .leading, spacing: 0) {
                            Button { add(photos: true) } label: { Label("Photos", systemImage: "photo.on.rectangle").frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal, 18).padding(.vertical, 12).contentShape(.rect) }
                            Button { add(photos: false) } label: { Label("Files", systemImage: "folder").frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal, 18).padding(.vertical, 12).contentShape(.rect) }
                        }
                        .buttonStyle(.plain)
                        .frame(width: 180)
                        .padding(.vertical, 6)
                        .presentationCompactAdaptation(.popover)
                    }
                    TextField(noModel ? String(localized: "I can reply once a model is connected") : String(localized: "Message \(chat.bot?.name ?? "Her")"),
                              text: $chat.draft.text, axis: .vertical)
                        .lineLimit(1 ... 6)
                        .focused($focused)
                        .padding(.vertical, 8)
                        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { h in
                            fieldHeight = h
                            if h > heldHeight || chat.draft.text.isEmpty { heldHeight = h } // sent or cleared: at once
                        }
                        .task(id: fieldHeight) {
                            guard fieldHeight < heldHeight else { return }
                            try? await Task.sleep(for: .milliseconds(800))
                            if !Task.isCancelled { heldHeight = fieldHeight }
                        }
                        .frame(minHeight: heldHeight, alignment: .top)
                        .accessibilityIdentifier("her-composer")
                    Button { send() } label: {
                        Image(systemName: "arrow.up").font(.body.weight(.semibold)).frame(width: 36, height: 36)
                            .foregroundStyle(.white)
                            // The asset itself: accentColor turns grey while the button is disabled.
                            .background(Color("AccentColor").opacity(canSend ? 1 : 0.4), in: .circle)
                    }
                    .disabled(!canSend)
                    .accessibilityLabel(Text("Send"))
                    .accessibilityIdentifier("her-send")
                }
            }
            .padding(6)
            .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 24, style: .continuous))
            .disabled(noModel)
            .opacity(noModel ? 0.6 : 1)
        }
        .padding(.horizontal, 12).padding(.bottom, 6)
        // End focus as navigation starts, so returning from settings lays the composer above the keyboard again.
        .onChange(of: model.path) { focused = false }
        .photosPicker(isPresented: $pickingPhotos, selection: $photos, maxSelectionCount: 10, matching: .images)
        .onChange(of: photos) {
            let picked = photos
            guard !picked.isEmpty else { return }
            photos = []
            Task {
                for item in picked {
                    if let data = try? await item.loadTransferable(type: Data.self), let jpeg = Self.jpeg(data) {
                        chat.draft.photos.append(.init(name: String(localized: "Photo") + ".jpg", data: jpeg))
                    }
                }
            }
        }
        .fileImporter(isPresented: $pickingFiles, allowedContentTypes: [.item], allowsMultipleSelection: true) { result in
            guard case .success(let urls) = result else { return }
            for url in urls {
                if let copy = Self.keep(url) { chat.draft.files.append(.init(path: copy.path, name: url.lastPathComponent)) }
            }
        }
    }

    private var canSend: Bool { !noModel && !sending && !chat.draft.isEmpty }

    /// What keeps the environment from answering, above the composer.
    private var note: String? {
        switch state {
        case .starting: String(localized: "Her is starting on \(machine.machineName)…")
        case .failed: String(localized: "Her couldn’t start on \(machine.machineName)")
        default: nil
        }
    }

    /// Closes the popover, then opens a picker once it has gone (one presentation at a time).
    private func add(photos: Bool) {
        adding = false
        Task {
            try? await Task.sleep(for: .milliseconds(400))
            if photos { pickingPhotos = true } else { pickingFiles = true }
        }
    }

    private func send() {
        sending = true
        Task {
            await chat.send(direct: direct)
            sending = false
        }
    }

    /// A photo as a model reads it: JPEG, at most 2048 points on its longer side.
    private static func jpeg(_ data: Data) -> Data? {
        guard let image = UIImage(data: data) else { return nil }
        let scale = min(1, 2048 / max(image.size.width, image.size.height))
        let size = CGSize(width: image.size.width * scale, height: image.size.height * scale)
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        return UIGraphicsImageRenderer(size: size, format: format).image { _ in image.draw(in: CGRect(origin: .zero, size: size)) }.jpegData(compressionQuality: 0.85)
    }

    /// A file picked from Files, copied where the core can read it (until it was uploaded, or delivered later).
    private static func keep(_ url: URL) -> URL? {
        let scoped = url.startAccessingSecurityScopedResource()
        defer { if scoped { url.stopAccessingSecurityScopedResource() } }
        let dir = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0].appendingPathComponent("her-files/\(UUID().uuidString)")
        let copy = dir.appendingPathComponent(url.lastPathComponent)
        do {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            try FileManager.default.copyItem(at: url, to: copy)
            return copy
        } catch {
            return nil
        }
    }
}

/// A tool call waiting for the owner (the Mac's Approval), Her's own or a sub-agent's, in place of the composer:
/// what Her wants to do and why, the command or the change itself, and the answers. Several waiting are answered
/// in order.
struct HerApprovalPanel: View {
    let chat: HerChat
    let approval: HerApproval
    let agent: HerAgent?
    let waiting: Int
    @State private var busy = false

    /// A website's grants are a site's or every site's; an app's start with this (a Mac's desktop tools).
    private var site: String? {
        let g = approval.grants
        guard let first = g.first, !g.contains(where: { $0.authority == "script" || $0.origin.hasPrefix("app://") }) else { return nil }
        return first.origin.replacingOccurrences(of: "https://", with: "")
    }

    var body: some View {
        let target = herCallTarget(approval.name, approval.args)
        let live = (agent?.view ?? chat.live)?.live?.tools.first { $0.callID == approval.callID }
        let category = live?.category ?? herToolCategory(target.name)
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 8) {
                Mark(size: 20)
                Text(said?.title ?? question(category: category, title: live?.title ?? target.name, name: target.name)).font(.subheadline.weight(.semibold)).lineLimit(2)
                Spacer(minLength: 0)
                if waiting > 1 { Text("1 of \(waiting)").font(.caption).foregroundStyle(.secondary) }
            }
            if let agent { Text("Sub-agent · \(agent.description.isEmpty ? agent.name : agent.description)").font(.caption).foregroundStyle(.secondary) }
            if let reason = approval.reason { Text(reason).font(.footnote).foregroundStyle(.secondary) }
            ScrollView {
                preview.font(.footnote.monospaced()).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading)
            }
            .frame(maxHeight: 160)
            .fixedSize(horizontal: false, vertical: true)
            .padding(.horizontal, 12).padding(.vertical, 10)
            .background(Palette.code, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
            HStack(spacing: 8) {
                Button { give(["type": "deny"]) } label: { Text("Deny").frame(maxWidth: .infinity) }
                    .buttonStyle(.glass)
                    .accessibilityIdentifier("her-deny")
                if let always {
                    Button { give(always.decision) } label: { Text(always.title).frame(maxWidth: .infinity).lineLimit(1).minimumScaleFactor(0.8) }
                        .buttonStyle(.glass)
                        .accessibilityIdentifier("her-allow-always")
                }
                Button { give(["type": "approve"]) } label: { Text("Allow Once").frame(maxWidth: .infinity) }
                    .buttonStyle(.glassProminent)
                    .accessibilityIdentifier("her-allow-once")
            }
            .controlSize(.large)
            .disabled(busy || !chat.convUp)
            if let note = always?.note { Text(note).font(.caption).foregroundStyle(.secondary) }
        }
        .padding(18)
        .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 36, style: .continuous))
        .padding(.horizontal, 10).padding(.bottom, 4)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("her-approval")
    }

    private func question(category: String, title: String, name: String) -> String {
        if let site { return String(localized: "Allow Her to use \(site)?") }
        if let script = approval.grants.first(where: { $0.authority == "script" }) {
            return String(localized: "Run a script on \(script.origin.replacingOccurrences(of: "https://", with: ""))?")
        }
        let object = herToolObject(name: name, title: title, category: category)
        switch category {
        case "execute": return String(localized: "Run this command?")
        case "read": return String(localized: "Read \(object)?")
        case "edit": return String(localized: "Edit \(object)?")
        case "delete": return String(localized: "Delete \(object)?")
        case "move": return String(localized: "Move \(object)?")
        case "search": return String(localized: "Search \(object)?")
        case "fetch": return String(localized: "Fetch \(object)?")
        default: return String(localized: "Call \(object)?")
        }
    }

    /// A summary of several lines (a session on the owner's computers, docs/her-mesh.md §5): its first is the
    /// question, the rest shows under it.
    private var said: (title: String, body: String)? {
        if case .args = herApprovalPreview(approval), site == nil { return herSummaryParts(approval.summary) }
        return nil
    }

    /// The call itself: a command, a change as removed and added lines, or what it writes.
    @ViewBuilder private var preview: some View {
        switch herApprovalPreview(approval) {
        case .args(let text):
            Text(said?.body ?? text)
        case .command(let text):
            Text(text)
        case .diff(let lines, let more):
            VStack(alignment: .leading, spacing: 1) {
                ForEach(Array(lines.enumerated()), id: \.offset) { _, l in
                    Text(verbatim: "\(l.sign) \(l.text)").foregroundStyle(l.sign == "+" ? Color.green : Color.red)
                }
                if more > 0 { Text("\(more) more lines").foregroundStyle(.secondary) }
            }
        case .content(let lines, let more):
            VStack(alignment: .leading, spacing: 1) {
                Text(lines.joined(separator: "\n"))
                if more > 0 { Text("\(more) more lines").foregroundStyle(.secondary) }
            }
        }
    }

    /// Allowing more than this once, when the call can be: a site from now on, a permission for the conversation,
    /// or commands like it.
    private var always: (title: String, decision: JSON, note: String)? {
        if site != nil {
            return (String(localized: "Always Allow"), ["type": "approve_for_session", "grant": 0, "persist": true],
                    String(localized: "Browsing and acting on this site won’t ask again; kept in this environment."))
        }
        if !approval.grants.isEmpty {
            return (String(localized: "Always Allow"), ["type": "approve_for_session", "grant": 0], String(localized: "Won’t ask again for this in this conversation."))
        }
        if let rule = approval.rule {
            // A project on the owner's computers reads by its name, not as the raw rule.
            let note = herMeshProject(rule).map { String(localized: "Don’t ask again for \($0)") }
                ?? String(localized: "Commands starting with “\(rule.joined(separator: " "))” won’t ask again in this conversation.")
            return (String(localized: "Always Allow"), ["type": "approve_for_session"], note)
        }
        if approval.mode != nil {
            return (String(localized: "Always Allow"), ["type": "approve_for_session"], String(localized: "Calls like this one won’t ask again in this conversation."))
        }
        return nil
    }

    private func give(_ decision: JSON) {
        busy = true
        Task {
            await chat.op(herApprovalOp(approval, agent: agent, decision: decision))
            busy = false
        }
    }
}
