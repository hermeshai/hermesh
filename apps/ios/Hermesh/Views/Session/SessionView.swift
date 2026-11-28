import HermeshProto
import HermeshSessions
import ImageIO
import QuickLook
import SwiftUI
import UIKit

// Shared conversation row, ending, scroll-follow, toolbar, and failure components used by
// SessionPage and Her sub-agent records (ProgressPage).

/// The archived note at the top of a conversation, with Unarchive.
struct ArchivedNote: View {
    @Environment(AppModel.self) private var model
    let session: Session

    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: "archivebox")
            if session.workspaceStrategy.worktreeKept {
                Text("Archived: read-only; the worktree is kept for local files.")
                    .frame(maxWidth: .infinity, alignment: .leading)
            } else {
                Text("Archived: read-only; the worktree is released and the changes stay on the branch.")
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            Button("Unarchive") { Task { await model.setArchived(session, false) } }
                .foregroundStyle(.tint)
        }
        .font(.footnote).foregroundStyle(.secondary)
        .padding(.horizontal, 12).padding(.vertical, 8)
        .background(Palette.fill, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        .padding(.bottom, 3)
        .accessibilityIdentifier("archived-note")
    }
}

/// When the turn before step i started, for the time lines.
func previousTurn(_ steps: [Step], before i: Int) -> Date? {
    for s in steps[..<i].reversed() {
        if case .item(.turn(_, _, let at, _)) = s { return at }
    }
    return nil
}

extension EnvironmentValues {
    @Entry var openBackgroundTasks: ((String) -> Void)?
}

/// One row of the conversation, as on the Mac: the owner on the right in a
/// blue bubble, the agent's words across the page, its tool calls as quiet
/// grey lines folded until opened.
struct StepView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.openBackgroundTasks) private var openBackgroundTasks
    let step: Step
    let session: Session
    let previousTurn: Date?
    var live = false

    var body: some View {
        switch step {
        case .tools(_, let calls):
            if calls.count == 1 {
                ToolRow(item: calls[0], sessionID: session.sessionID)
            } else {
                ToolGroup(calls: calls, sessionID: session.sessionID)
            }
        case .item(let item):
            row(item)
        }
    }

    /// Who sent a prompt when it isn't the owner: Her, or another of the owner's agents.
    private func sender(_ clientID: String) -> PromptSender? {
        clientID == "owner" ? nil : PromptSender(name: model.senderName(clientID), client: model.clients.first { $0.clientID == clientID })
    }

    @ViewBuilder private func row(_ item: TranscriptItem) -> some View {
        switch item {
        case .turn(_, _, let at, let wakeReason):
            // A time line before a turn, only for the first one and after a pause; isolated into TurnTimeLine so StepView
            // does not read model.now directly on every clock tick.
            if let at, turnTimeLine(at, previous: previousTurn, now: at) != nil {
                TurnTimeLine(at: at, previousTurn: previousTurn)
            }
            if !wakeReason.isEmpty {
                Button {
                    openBackgroundTasks?(wakeReason)
                } label: {
                    HStack(spacing: 6) {
                        Text(String(localized: "\(wakeReason) finished")).lineLimit(1)
                        Caret(open: false)
                    }
                    .font(.subheadline).foregroundStyle(.secondary)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("thread-wake-line")
            }
        case .user(_, let text, let at, let asker, let attachments):
            // Who asked for the turn speaks on the right: the owner in the blue bubble, with nothing over it; another
            // of the owner's agents (Her, a granted one) in a quiet bubble with its face on the corner. Each turn says
            // its own: a session Her started can hold the owner's words too.
            // Queued on this iPhone while its machine is offline: faded, and said under it, with Discard.
            PromptBubble(text: text, sent: at, from: sender(asker.isEmpty ? session.createdBy : asker),
                         attachments: attachments, sessionID: session.sessionID, machineID: session.machineID,
                         faded: session.offlineQueued,
                         note: session.offlineQueued ? PromptNote(kind: .waiting(machine: model.machineName(session.machineID)) {
                             Task { _ = await model.delete(session) }
                         }) : nil) { EmptyView() }
        case .workspace(let row):
            WorkspaceLine(row: row)
        case .agent(_, let text, let runID):
            StreamingMarkdownText(text: text, streaming: live)
                .environment(\.filesRun, runID)
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
                .contextMenu { CopyMenu(text: text, sent: nil) }
                .padding(.bottom, 8)
        case .message(_, let incoming, let name, let text, let queued):
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 6) {
                    Image(systemName: "envelope").foregroundStyle(.secondary)
                    Text(incoming ? String(localized: "Message from \(name)") : String(localized: "Message to \(name)")).fontWeight(.semibold)
                    if queued {
                        Spacer(minLength: 8)
                        Text("Queued for next turn").foregroundStyle(.secondary)
                    }
                }
                .font(.caption)
                MarkdownText(text: text, style: .small)
            }
            .padding(.horizontal, 12).padding(.vertical, 10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Palette.fill, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        case .question(_, let q, let r):
            if let r {
                SettledQuestionCard(question: q, resolution: r)
            }
        case .planReview(_, let title, let approved, let by):
            SettledPlanReviewCard(title: title, approved: approved, by: by)
        case .turnEnd(_, let end, _):
            if end.isError, isInterruption(end.hasFailure ? end.failure : nil) {
                interruptNote(end.failure)
            } else if end.isError, !(session.isFailed && sameFailure(session.progress.lastFailure, end.failure)) {
                FailLine(title: String(localized: "Turn \(end.turn) failed: \(failureTitle(end.failure.kind))"), message: end.failure.message)
            }
        case .error(_, let e):
            if e.hasFailure, isInterruption(e.failure) {
                interruptNote(e.failure)
            } else if !(session.isFailed && e.hasFailure && sameFailure(session.progress.lastFailure, e.failure)) {
                FailLine(title: e.hasFailure ? String(localized: "Failed: \(failureTitle(e.failure.kind))") : String(localized: "Failed"), message: e.message)
            }
        case .tool:
            EmptyView()
        }
    }

    /// A turn stopped on purpose: by you, by one of your agents, or by Hermesh. That it can go on, the box under it shows.
    private func interruptNote(_ f: Failure) -> some View {
        let by = interrupter(f)
        let label = model.clients.first { $0.clientID == by }?.label
        let text = by == "owner" ? String(localized: "You interrupted this turn")
            : label.map { String(localized: "\(AgentLogo.name($0)) interrupted this turn") } ?? String(localized: "This turn was interrupted")
        return TurnNote(text: text)
    }
}

/// Settled question record: in-stream secondary question prompt over a compact checked option pill
/// with a calm inline Recommended badge and no outer wrapper box.
struct SettledQuestionCard: View {
    @Environment(AppModel.self) private var model
    let question: Question
    let resolution: QuestionResolved

    var body: some View {
        let nameOf: (String) -> String = { AgentLogo.name(model.clientLabel($0)) }
        let rows = settledQuestionRows(question, resolution: resolution, name: nameOf)
        let byOther: String? = (resolution.by.isEmpty || resolution.by == "owner") ? nil : nameOf(resolution.by)

        VStack(alignment: .leading, spacing: 11) {
            if let byOther, resolution.resolution == .answered {
                Text("\(byOther) answered")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            ForEach(Array(rows.enumerated()), id: \.offset) { _, row in
                VStack(alignment: .leading, spacing: 5) {
                    if !row.prompt.isEmpty {
                        Text(verbatim: row.prompt)
                            .font(.footnote)
                            .foregroundStyle(.secondary)
                            .lineSpacing(2)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    if row.status == .answered && !row.choices.isEmpty {
                        answeredChoicesBox(row.choices)
                    } else if !row.statusText.isEmpty {
                        mutedAnswerBox(status: row.status, statusText: row.statusText)
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("settled-question")
    }

    @ViewBuilder private func answeredChoicesBox(_ choices: [ParsedOptionLabel]) -> some View {
        let multi = choices.count > 1
        VStack(alignment: .leading, spacing: 0) {
            ForEach(Array(choices.enumerated()), id: \.offset) { idx, choice in
                if idx > 0 {
                    Rectangle().fill(Palette.separator).frame(height: 0.5)
                }
                HStack(alignment: .top, spacing: 8) {
                    answerMark(.answered)
                        .padding(.top, 2)
                    choiceText(choice)
                        .foregroundStyle(.primary)
                        .lineSpacing(2.5)
                        .multilineTextAlignment(.leading)
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: multi ? .infinity : nil, alignment: .leading)
                }
                .padding(.leading, 10).padding(.trailing, 12).padding(.vertical, 7)
            }
        }
        .background(Palette.settledAnswer, in: RoundedRectangle(cornerRadius: 9, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 9, style: .continuous).strokeBorder(Palette.settledAnswerBorder, lineWidth: 0.5))
        .shadow(color: Palette.settledAnswerShadow, radius: 1.5, x: 0, y: 0.5)
    }

    @ViewBuilder private func mutedAnswerBox(status: SettledQuestionRow.StatusKind, statusText: String) -> some View {
        HStack(alignment: .top, spacing: 8) {
            answerMark(status)
                .padding(.top, 1)
            Text(verbatim: statusText)
                .font(.footnote)
                .foregroundStyle(.secondary)
                .lineSpacing(2.5)
                .multilineTextAlignment(.leading)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(.leading, 10).padding(.trailing, 12).padding(.vertical, 7)
        .overlay(RoundedRectangle(cornerRadius: 9, style: .continuous).strokeBorder(Palette.separator, lineWidth: 0.5))
    }

    @ViewBuilder private func answerMark(_ status: SettledQuestionRow.StatusKind) -> some View {
        switch status {
        case .answered:
            Image(systemName: "checkmark")
                .font(.system(size: 9, weight: .bold))
                .foregroundStyle(.secondary)
                .frame(width: 15, height: 15)
                .background(Palette.fill2, in: .circle)
        case .timedOut:
            Image(systemName: "clock")
                .font(.system(size: 9.5, weight: .semibold))
                .foregroundStyle(.orange)
                .frame(width: 15, height: 15)
                .background(Color.orange.opacity(0.15), in: .circle)
        default:
            Image(systemName: "minus")
                .font(.system(size: 9.5, weight: .semibold))
                .foregroundStyle(.secondary)
                .frame(width: 15, height: 15)
                .background(Palette.fill2, in: .circle)
        }
    }

    @ViewBuilder private func choiceText(_ choice: ParsedOptionLabel) -> some View {
        let baseFont: Font = .subheadline
        let label = Text(verbatim: choice.text).font(baseFont)
        if choice.recommended {
            let badgeFont: Font = .caption2.weight(.medium)
            let space = Text(verbatim: " ").font(baseFont)
            let badge = Text(verbatim: "\u{00A0}\(String(localized: "Recommended"))\u{00A0}")
                .font(badgeFont)
                .foregroundStyle(.secondary)
                .baselineOffset(0.5)
                .customAttribute(RecommendedBadgeAttribute(selected: false))
            Text("\(label)\(space)\(badge)")
                .textRenderer(RecommendedTextRenderer(accent: .secondary))
        } else {
            label
        }
    }
}

/// Who sent work in when it isn't the owner: Her, or another of the owner's agents (its grant, when still known).
struct PromptSender {
    let name: String
    let client: Client?
}

private struct TurnTimeLine: View {
    @Environment(AppModel.self) private var model
    let at: Date
    let previousTurn: Date?

    var body: some View {
        if let line = turnTimeLine(at, previous: previousTurn, now: model.now) {
            Text(line).font(.caption).foregroundStyle(.tertiary).frame(maxWidth: .infinity)
                .padding(.top, 8)
                .accessibilityIdentifier("turn-time-line")
        }
    }
}

/// Keeps decoded user-message attachment thumbnails and preview files on the main actor so a photo just sent in the
/// composer shows in the bubble at 0 ms with no flash, and attachments from earlier or remote turns load once by sha256.
@MainActor @Observable final class SessionAttachmentStore {
    static let shared = SessionAttachmentStore()

    private let maxEntries = 32
    private(set) var pictures: [String: UIImage?] = [:]
    @ObservationIgnored private var order: [String] = []
    @ObservationIgnored private var inFlight: Set<String> = []

    private init() {
        NotificationCenter.default.addObserver(forName: UIApplication.didReceiveMemoryWarningNotification, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated {
                self?.pictures.removeAll()
                self?.order.removeAll()
            }
        }
    }

    private nonisolated static func downsampleThumbnail(_ data: Data, maxPixelSize: Int = 320) -> UIImage? {
        guard !data.isEmpty,
              let src = CGImageSourceCreateWithData(data as CFData, [kCGImageSourceShouldCache: false] as CFDictionary)
        else { return nil }
        let opts: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceShouldCacheImmediately: true,
            kCGImageSourceThumbnailMaxPixelSize: maxPixelSize,
        ]
        if let cg = CGImageSourceCreateThumbnailAtIndex(src, 0, opts as CFDictionary) {
            return UIImage(cgImage: cg)
        }
        return UIImage(data: data)
    }

    func keep(sha256: String, name: String, data: Data, picture: UIImage?) {
        guard !sha256.isEmpty else { return }
        if let img = Self.downsampleThumbnail(data) ?? picture {
            store(sha256: sha256, picture: img)
        }
        _ = try? writeTemp(sha256: sha256, name: name, data: data)
    }

    func loadPicture(api: AppAPI?, sessionID: String, machineID: String, ref: Hermesh_Session_V1_AttachmentRef) async {
        let sha = ref.blobSha256
        guard !sha.isEmpty, pictures[sha] == nil, !inFlight.contains(sha), let api else { return }
        inFlight.insert(sha)
        defer { inFlight.remove(sha) }
        do {
            let data = try await api.downloadBlob(sessionID, sha256: sha, machineID: machineID)
            _ = try? writeTemp(sha256: sha, name: ref.name, data: data)
            let thumb = await Task.detached(priority: .userInitiated) {
                Self.downsampleThumbnail(data)
            }.value
            store(sha256: sha, picture: thumb)
        } catch {
            store(sha256: sha, picture: nil)
        }
    }

    func previewURL(api: AppAPI?, sessionID: String, machineID: String, ref: Hermesh_Session_V1_AttachmentRef) async -> URL? {
        let sha = ref.blobSha256
        guard !sha.isEmpty else { return nil }
        let existing = tempURL(sha256: sha, name: ref.name)
        if FileManager.default.fileExists(atPath: existing.path) { return existing }
        guard let api, let data = try? await api.downloadBlob(sessionID, sha256: sha, machineID: machineID) else { return nil }
        return try? writeTemp(sha256: sha, name: ref.name, data: data)
    }

    /// An attachment's bytes: kept on this iPhone when it was sent from here or opened before, else downloaded once.
    func data(api: AppAPI, sessionID: String, machineID: String, ref: Hermesh_Session_V1_AttachmentRef) async throws -> Data {
        if let kept = try? Data(contentsOf: tempURL(sha256: ref.blobSha256, name: ref.name)) { return kept }
        let data = try await api.downloadBlob(sessionID, sha256: ref.blobSha256, machineID: machineID)
        _ = try? writeTemp(sha256: ref.blobSha256, name: ref.name, data: data)
        return data
    }

    private func store(sha256: String, picture: UIImage?) {
        pictures.updateValue(picture, forKey: sha256)
        order.removeAll { $0 == sha256 }
        order.append(sha256)
        while order.count > maxEntries {
            let old = order.removeFirst()
            pictures.removeValue(forKey: old)
        }
    }

    private func tempURL(sha256: String, name: String) -> URL {
        let base = fileBaseName(name)
        let safe = base.isEmpty ? "attachment" : base
        return FileManager.default.temporaryDirectory
            .appendingPathComponent("Session Attachments/\(sha256)", isDirectory: true)
            .appendingPathComponent(safe)
    }

    private func writeTemp(sha256: String, name: String, data: Data) throws -> URL {
        let url = tempURL(sha256: sha256, name: name)
        try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        if !FileManager.default.fileExists(atPath: url.path) {
            try data.write(to: url, options: .atomic)
        }
        return url
    }
}

func isSessionImageAttachment(_ ref: Hermesh_Session_V1_AttachmentRef) -> Bool {
    ref.mimeType.lowercased().hasPrefix("image/") || fileView(ref.name) == .image
}

/// What was sent to the agent, on the right, under `sender`; a long press copies it. The owner's is a blue bubble;
/// one another of the owner's agents sent (`from`) is a quiet one with that agent's face on its corner.
struct PromptBubble<Sender: View>: View {
    @Environment(AppModel.self) private var model
    let text: String
    let sent: Date?
    var from: PromptSender?
    var attachments: [Hermesh_Session_V1_AttachmentRef] = []
    var sessionID = ""
    var machineID = ""
    /// Not in the conversation yet (on its way, or waiting for its machine): the bubble faded, as Her's are.
    var faded = false
    /// Under it, where it is while it is not in the conversation, or why it was not sent.
    var note: PromptNote?
    @ViewBuilder let sender: () -> Sender
    @State private var preview: URL?
    @State private var opening: String?
    @State private var lightbox: SessionLightboxPresentation?

    var body: some View {
        let store = SessionAttachmentStore.shared
        let hasText = !text.isEmpty || attachments.isEmpty
        VStack(alignment: .trailing, spacing: 5) {
            sender()
            VStack(alignment: .trailing, spacing: 6) {
                if !attachments.isEmpty {
                    HerWrap(spacing: 6) {
                        ForEach(attachments, id: \.blobSha256) { att in
                            attachmentView(att, store: store)
                        }
                    }
                    .overlay(alignment: .topLeading) { if !hasText, let from { badge(from).offset(x: -10, y: -10) } }
                }
                if hasText {
                    Text(text)
                        .lineSpacing(3)
                        .foregroundStyle(from == nil ? Palette.bubbleText : Color.primary)
                        .padding(.horizontal, 14).padding(.vertical, 9)
                        .background(from == nil ? Palette.bubble : Palette.fill2, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                        .contentShape(.contextMenuPreview, RoundedRectangle(cornerRadius: 18, style: .continuous))
                        .contextMenu { CopyMenu(text: text, sent: sent, from: from?.name) }
                        .overlay(alignment: .topLeading) { if let from { badge(from).offset(x: -10, y: -10) } }
                }
            }
            .opacity(faded ? 0.62 : 1)
            if let note { note }
        }
        .padding(.leading, 48)
        .frame(maxWidth: .infinity, alignment: .trailing)
        .padding(.top, from == nil ? 8 : 14).padding(.bottom, 8)
        .quickLookPreview($preview)
        .fullScreenCover(item: $lightbox) { SessionImageLightbox(presentation: $0) }
    }

    @ViewBuilder private func attachmentView(_ att: Hermesh_Session_V1_AttachmentRef, store: SessionAttachmentStore) -> some View {
        let name = fileBaseName(att.name)
        let cached = store.pictures[att.blobSha256]
        let unreadable = if case .some(nil) = cached { true } else { false }
        if isSessionImageAttachment(att) && !unreadable {
            Button { openPicture(att) } label: {
                Group {
                    if let picture = cached ?? nil {
                        Image(uiImage: picture)
                            .resizable()
                            .scaledToFill()
                    } else {
                        Palette.fill2.overlay { ProgressView().controlSize(.small) }
                    }
                }
                .frame(width: 72, height: 72)
                .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
            }
            .buttonStyle(.plain)
            .accessibilityLabel(name)
            .accessibilityIdentifier("prompt-attachment-\(name)")
            .task(id: att.blobSha256) {
                await store.loadPicture(api: model.api, sessionID: sessionID, machineID: machineID, ref: att)
            }
        } else {
            // A picture that could not be read still opens large: the viewer says why, and tries again.
            Button { isSessionImageAttachment(att) ? openPicture(att) : open(att) } label: {
                HerChip(name: name, busy: opening == att.blobSha256)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("prompt-attachment-\(name)")
        }
    }

    /// The bubble's pictures in the Lightbox, from the one tapped; one that could not be read says why there.
    private func openPicture(_ att: Hermesh_Session_V1_AttachmentRef) {
        hideKeyboard()
        let pictures = attachments.filter(isSessionImageAttachment)
        lightbox = SessionLightboxPresentation(sessionID: sessionID, machineID: machineID, run: "", items: pictures.map(LightboxItem.attachment),
                                               initialIndex: pictures.firstIndex(of: att) ?? 0)
    }

    private func open(_ att: Hermesh_Session_V1_AttachmentRef) {
        hideKeyboard()
        // A page, Markdown, code or text: shown by the app, as a reply's file is (a tab of the side panel on a wide
        // screen, a page of its own on a narrow one); the rest in Quick Look.
        if attachmentOpensInApp(att) {
            if model.wide != nil, !sessionID.isEmpty {
                model.sessionFiles.model(sessionID, api: model.api).openTab(run: "", path: att.name, attachment: att)
                model.menuRail = true
            } else {
                model.path.append(.sessionAttachment(sessionID, machineID, att))
            }
            return
        }
        guard opening == nil else { return }
        opening = att.blobSha256
        Task {
            preview = await SessionAttachmentStore.shared.previewURL(api: model.api, sessionID: sessionID, machineID: machineID, ref: att)
            opening = nil
        }
    }

    /// The sender's face on the bubble's corner: its grant's avatar (Her's cat, an app's logo, an initial), ringed by the page.
    private func badge(_ from: PromptSender) -> some View {
        Group {
            if let c = from.client { GrantAvatar(client: c, size: 20) } else { AgentLogo(id: from.name, size: 20, initialOnly: true) }
        }
        .padding(2)
        .background(Color(UIColor.systemBackground), in: .circle)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text("Sent by \(from.name)"))
        .accessibilityIdentifier("prompt-sender")
    }
}

/// Under the owner's message while it is not in the conversation, in Her's words for hers (HerConversation): on its way
/// (said once that takes 400 ms), waiting for its machine to come back, or not sent and why, with what can be done.
struct PromptNote: View {
    enum Kind {
        case sending
        case waiting(machine: String, discard: () -> Void)
        case refused(why: String, retry: () -> Void, edit: () -> Void)
    }

    let kind: Kind

    var body: some View {
        HStack(spacing: 8) {
            switch kind {
            case .sending:
                Delayed(active: true) { Text("Sending…") }
            case .waiting(let machine, let discard):
                Text("Delivered when \(machine) is back")
                Button("Discard", action: discard)
                    .fontWeight(.semibold).foregroundStyle(.secondary)
                    .accessibilityIdentifier("prompt-discard")
            case .refused(let why, let retry, let edit):
                Label("Not delivered: \(why)", systemImage: "exclamationmark.triangle").lineLimit(2)
                Button("Try Again", action: retry)
                    .fontWeight(.semibold).foregroundStyle(.secondary)
                    .accessibilityIdentifier("prompt-retry")
                Button("Edit", action: edit)
                    .fontWeight(.semibold).foregroundStyle(.secondary)
                    .accessibilityIdentifier("prompt-edit")
            }
        }
        .font(.caption).foregroundStyle(.tertiary)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("prompt-note")
    }
}

/// The long-press menu of a message: Copy, who sent it when it isn't the owner, and when.
struct CopyMenu: View {
    @Environment(AppModel.self) private var model
    let text: String
    let sent: Date?
    var from: String?

    var body: some View {
        Button("Copy", systemImage: "doc.on.doc") { UIPasteboard.general.string = text }
        if let from { Text("Sent by \(from)") }
        if let sent { Text("Sent \(agoText(sent, now: model.now))") }
    }
}

/// A session page's top, drawn by the page in place of the system's navigation bar (which the page hides): the menu
/// button on a root page, or the way back on a pushed one; in the middle the session's title over where it runs, in a
/// capsule of one width that opens Session Info; ⋯ with its actions. Each is the system's glass and nothing more. The
/// page gives it to its conversation as its top bar (`sessionBar`), so the system blurs what scrolls under it, and
/// nothing of the system's bar (what it shows or hides as a page scrolls, on one iOS or another) is in it. With the
/// system's bars on the side (iPhone Duo), the buttons are in the system's bar and only the title is here.
struct SessionBar: View {
    @Environment(AppModel.self) private var model
    @Environment(\.barsOnTheSide) private var sideBars
    /// None while the session is read: the way out of the page only.
    let session: Session?
    /// The sync line in place of where it runs while what shows is not current (a page with no box to say it over).
    var sync = false
    @Binding var renaming: Bool
    @Binding var deleting: Bool
    /// A thread's background tasks, when it has any: a button beside ⋯ while one runs, and always an entry in ⋯.
    var backgroundTasks: BackgroundTasks?
    /// A session being started, before its machine has answered: its title and where it runs, in the same capsule.
    var starting: Starting?

    struct BackgroundTasks {
        let runningCount: Int
        var isOpen = false
        let open: () -> Void
        var running: Bool { runningCount > 0 }
    }

    struct Starting {
        let title: String
        let place: String
    }

    /// The title capsule's width on every session, in points: a long title ends in …, and Session Info says it whole.
    static let titleWidth: CGFloat = 220

    var body: some View {
        let running = backgroundTasks.flatMap { $0.running ? $0 : nil }
        let wide = model.wide != nil && session != nil
        let shownBg = backgroundTasks.flatMap { ($0.running || (wide && $0.isOpen) || wide) ? $0 : nil }
        // As wide on the left as on the right, so the title is in the middle.
        let buttons = 1 + (shownBg == nil ? 0 : 1) + (wide ? 1 : 0)
        let side: CGFloat = sideBars ? 0 : CGFloat(buttons) * 44 + CGFloat(buttons - 1) * 8
        HStack(spacing: 8) {
            HStack(spacing: 8) {
                if sideBars {
                } else if !model.path.isEmpty {
                    Button { model.path.removeLast() } label: { BarIcon(systemImage: "chevron.backward") }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Back")
                        .accessibilityIdentifier("BackButton")
                } else {
                    GlassMenuButton()
                }
            }
            .frame(width: side, alignment: .leading)
            Spacer(minLength: 0)
            if let session { title(session).layoutPriority(1) } else if let starting { capsule(starting.title, starting.place).layoutPriority(1) }
            Spacer(minLength: 0)
            HStack(spacing: 8) {
                if let session, !sideBars {
                    if let shownBg {
                        Button(action: shownBg.open) {
                            BarIcon(systemImage: "terminal")
                                .overlay(alignment: .topTrailing) {
                                    if shownBg.runningCount > 0 {
                                        Text(verbatim: "\(shownBg.runningCount)")
                                            .font(.system(size: 10, weight: .bold).monospacedDigit())
                                            .foregroundStyle(.white)
                                            .padding(.horizontal, 4.5)
                                            .frame(minWidth: 16, minHeight: 16)
                                            .background(Color.accentColor, in: Capsule())
                                            .offset(x: 3, y: -3)
                                    }
                                }
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Background Tasks")
                        .accessibilityIdentifier("thread-background-tasks")
                    }
                    if wide { SidePanelButton(sessionID: session.sessionID) }
                    Menu {
                        SessionMore(session: session, renaming: $renaming, deleting: $deleting, backgroundTasks: backgroundTasks)
                    } label: {
                        BarIcon(systemImage: "ellipsis")
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("More")
                    .accessibilityIdentifier("session-more")
                }
            }
            .frame(width: side, alignment: .trailing)
        }
        .padding(.horizontal, 16).padding(.bottom, 4)
        .keyboardAwayOnTap()
    }

    private func title(_ s: Session) -> some View {
        Button { model.sheet = .sessionInfo(s.sessionID) } label: {
            capsule(s.displayTitle, "\(model.machineName(s.machineID))/\(s.projectID)")
        }
        .buttonStyle(.plain)
        .accessibilityHint(Text("Session Info"))
        .accessibilityIdentifier("session-info")
    }

    private func capsule(_ title: String, _ place: String) -> some View {
        VStack(spacing: 1) {
            Text(verbatim: title).font(.subheadline.weight(.semibold)).foregroundStyle(.primary)
            if sync && model.shownSync != nil {
                SyncLine(small: true)
            } else {
                Text(verbatim: place).font(.caption2).foregroundStyle(.secondary)
            }
        }
        .lineLimit(1)
        .truncationMode(.tail)
        .padding(.horizontal, 16)
        .frame(maxWidth: Self.titleWidth)
        .frame(height: 44)
        .glassEffect(.regular.interactive(), in: .capsule)
        .contentShape(.capsule)
    }
}

/// A button of the page's own bar: its symbol in a circle of the system's glass, the size of the system's bar buttons.
struct BarIcon: View {
    let systemImage: String

    var body: some View {
        Image(systemName: systemImage).font(.body.weight(.medium)).foregroundStyle(.primary).frame(width: 44, height: 44)
            .glassEffect(.regular.interactive(), in: .circle)
            .contentShape(.circle)
    }
}

/// A wide screen's side panel, shown or put away: opened, the menu shrinks to its rail to give it room, and an empty
/// panel starts with 项目文件.
private struct SidePanelButton: View {
    @Environment(AppModel.self) private var model
    let sessionID: String
    var inBar = false

    var body: some View {
        let files = model.sessionFiles.model(sessionID, api: model.api)
        Group {
            if inBar {
                Button(action: { toggle(files) }) {
                    (files.panelOpen ? sidePanelHideImage() : Image(systemName: "sidebar.right"))
                        .foregroundStyle(files.panelOpen ? Color.accentColor : .primary)
                }
            } else {
                Button(action: { toggle(files) }) {
                    (files.panelOpen ? sidePanelHideImage() : Image(systemName: "sidebar.right"))
                        .font(.body.weight(.medium))
                        .foregroundStyle(files.panelOpen ? Color.accentColor : .primary)
                        .frame(width: 44, height: 44)
                        .glassEffect(.regular.interactive(), in: .circle)
                        .contentShape(.circle)
                }
                .buttonStyle(.plain)
            }
        }
        .accessibilityLabel(files.panelOpen ? String(localized: "Hide Side Panel") : String(localized: "Show Side Panel"))
        .accessibilityAddTraits(files.panelOpen ? .isSelected : [])
        .accessibilityIdentifier("session-side-panel")
    }

    private func toggle(_ files: SessionFilesModel) {
        hideKeyboard()
        if files.panelOpen {
            files.panelOpen = false
            files.panelMax = false
            files.panelDrawer = false
        } else if files.tabs.isEmpty {
            files.openProject()
        } else {
            files.panelOpen = true
        }
        if files.panelOpen { model.menuRail = true }
    }
}

/// What a session's ⋯ opens.
private struct SessionMore: View {
    @Environment(AppModel.self) private var model
    let session: Session
    @Binding var renaming: Bool
    @Binding var deleting: Bool
    let backgroundTasks: SessionBar.BackgroundTasks?

    var body: some View {
        // The session's folder (docs/session-files.md §4, §15): a level at a time on a narrow screen; on a wide one,
        // pulls up the bottom tree drawer over an open file tab or starts 项目文件 in the side panel.
        Button("Project Files", systemImage: "folder") {
            if model.wide != nil {
                model.sessionFiles.model(session.sessionID, api: model.api).showProjectFiles()
                model.menuRail = true
            } else {
                model.path.append(.sessionFolder(session.sessionID, ""))
            }
        }
        .accessibilityIdentifier("session-files")
        if let tasks = backgroundTasks {
            Button("Background Tasks", systemImage: "terminal", action: tasks.open)
        }

        Button("Copy Session ID", systemImage: "doc.on.doc") {
            UIPasteboard.general.string = session.sessionID
            model.show(String(localized: "Copied the session ID"))
        }
        SessionActions(session: session, renaming: $renaming, deleting: $deleting)
    }
}

/// With the system's bars on the side (iPhone Duo): a session's buttons there, as every page's are. When unfolded
/// on its side (`model.wide != nil`), window and contextual side-panel tools group into vertical Liquid Glass capsules
/// along the trailing bar, keeping Hide Side Panel pinned and merging file actions into a single sectioned `⋯` menu.
private struct SessionSideItems: ToolbarContent {
    @Environment(AppModel.self) private var model
    let session: Session?
    @Binding var renaming: Bool
    @Binding var deleting: Bool
    let backgroundTasks: SessionBar.BackgroundTasks?

    var body: some ToolbarContent {
        if model.path.isEmpty { MenuToolbarItem() }
        if let session {
            if #available(iOS 27.1, *), model.wide != nil {
                SessionDuoWideItems(session: session, files: model.sessionFiles.model(session.sessionID, api: model.api),
                                    renaming: $renaming, deleting: $deleting, backgroundTasks: backgroundTasks)
            } else {
                if let tasks = backgroundTasks, tasks.running {
                    ToolbarItem(placement: .topBarTrailing) {
                        Button("Background Tasks", systemImage: "terminal", action: tasks.open)
                            .accessibilityIdentifier("thread-background-tasks")
                    }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Menu {
                        SessionMore(session: session, renaming: $renaming, deleting: $deleting, backgroundTasks: backgroundTasks)
                    } label: {
                        Image(systemName: "ellipsis")
                    }
                    .accessibilityLabel("More")
                    .accessibilityIdentifier("session-more")
                }
            }
        }
    }
}

@available(iOS 27.1, *)
private struct SessionDuoWideItems: ToolbarContent {
    @Environment(AppModel.self) private var model
    let session: Session
    @Bindable var files: SessionFilesModel
    @Binding var renaming: Bool
    @Binding var deleting: Bool
    let backgroundTasks: SessionBar.BackgroundTasks?

    private var activeIndex: Int? { files.tabs.firstIndex { $0.id == files.activeTab } }
    private var activeTab: PanelTab? { activeIndex.map { files.tabs[$0] } }
    private var sourceable: Bool {
        guard let t = activeTab, t.kind == .files, !t.path.isEmpty else { return false }
        let v = fileView(t.path)
        return v == .markdown || v == .svg
    }

    var body: some ToolbarContent {
        // `.topBarTrailing` with `.visibilityPriority(.high)` (not `.topBarPinnedTrailing`): UIKit's
        // `pinnedTrailingGroup` is a single `UIBarButtonItemGroup?` slot, while SwiftUI emits one group per
        // child inside `ToolbarItemGroup`, dropping the second button (`side-panel-max`) on Duo's vertical bar.
        ToolbarItemGroup(placement: .topBarTrailing) {
            SidePanelButton(sessionID: session.sessionID, inBar: true)
            if files.panelOpen {
                Button {
                    files.panelMax.toggle()
                } label: {
                    Image(systemName: files.panelMax ? "arrow.down.right.and.arrow.up.left" : "arrow.up.left.and.arrow.down.right")
                }
                .accessibilityLabel(files.panelMax ? String(localized: "Shrink") : String(localized: "Enlarge"))
                .accessibilityIdentifier("side-panel-max")
            }
        }
        .visibilityPriority(.high)
        .axisBehavior(.verticalPreferred)
        if files.panelOpen {
            ToolbarSpacer(.fixed, placement: .topBarTrailing)
            ToolbarItemGroup(placement: .topBarTrailing) {
                Menu {
                    Button("Project Files", systemImage: "list.bullet.indent") { files.openProject() }
                } label: {
                    Image(systemName: "plus")
                }
                .accessibilityLabel(String(localized: "New Tab"))
                .accessibilityIdentifier("side-panel-new")
                if let i = activeIndex, sourceable {
                    Button {
                        files.tabs[i].source.toggle()
                    } label: {
                        Image(systemName: files.tabs[i].source ? "eye" : "chevron.left.forwardslash.chevron.right")
                    }
                    .accessibilityLabel(files.tabs[i].source ? String(localized: "Preview") : String(localized: "Source"))
                    .accessibilityIdentifier("file-source")
                }
            }
            .axisBehavior(.verticalPreferred)
        }
        ToolbarSpacer(.fixed, placement: .topBarTrailing)
        if let tasks = backgroundTasks {
            ToolbarItem(placement: .topBarTrailing) {
                Button(action: tasks.open) {
                    Image(systemName: tasks.isOpen ? "terminal.fill" : "terminal")
                }
                .tint(tasks.isOpen || tasks.running ? Color.accentColor : nil)
                .accessibilityLabel("Background Tasks")
                .accessibilityIdentifier("thread-background-tasks")
            }
            .axisBehavior(.verticalPreferred)
        }
        ToolbarItem(placement: .topBarTrailing) {
            Menu {
                if files.panelOpen, let tab = activeTab {
                    if let a = tab.attachment {
                        Section(fileBaseName(a.name)) {
                            Button("Share…", systemImage: "square.and.arrow.up") {
                                Task {
                                    files.sharing = await SessionAttachmentStore.shared.previewURL(api: model.api, sessionID: files.sessionID,
                                                                                                   machineID: session.machineID, ref: a)
                                }
                            }
                            .accessibilityIdentifier("file-share")
                        }
                        Section(String(localized: "Current Session")) {
                            SessionMore(session: session, renaming: $renaming, deleting: $deleting, backgroundTasks: backgroundTasks)
                        }
                    } else if !tab.path.isEmpty {
                        Section(tab.kind == .page ? tab.path : fileBaseName(tab.path)) {
                            SessionFileMenu(sessionID: files.sessionID, run: tab.run, path: tab.path,
                                            folder: files.folders[tab.id] ?? "", sharing: $files.sharing)
                        }
                        Section(String(localized: "Current Session")) {
                            SessionMore(session: session, renaming: $renaming, deleting: $deleting, backgroundTasks: backgroundTasks)
                        }
                    } else {
                        SessionMore(session: session, renaming: $renaming, deleting: $deleting, backgroundTasks: backgroundTasks)
                    }
                } else {
                    SessionMore(session: session, renaming: $renaming, deleting: $deleting, backgroundTasks: backgroundTasks)
                }
            } label: {
                Image(systemName: "ellipsis")
            }
            .accessibilityLabel("More")
            .accessibilityIdentifier("session-more")
        }
        .axisBehavior(.verticalPreferred)
    }
}

extension View {
    /// A session page's top: its own bar (SessionBar) over what scrolls in this view, the system's bar hidden.
    func sessionBar(_ session: Session?, renaming: Binding<Bool>, deleting: Binding<Bool>, backgroundTasks: SessionBar.BackgroundTasks? = nil,
                    starting: SessionBar.Starting? = nil) -> some View {
        modifier(SessionBarTop(session: session, renaming: renaming, deleting: deleting, backgroundTasks: backgroundTasks, starting: starting))
    }
}

private struct SessionBarTop: ViewModifier {
    @Environment(\.barsOnTheSide) private var sideBars
    @Environment(\.menuCoversPage) private var menuCoversPage
    let session: Session?
    @Binding var renaming: Bool
    @Binding var deleting: Bool
    let backgroundTasks: SessionBar.BackgroundTasks?
    let starting: SessionBar.Starting?

    func body(content: Content) -> some View {
        let ownsVerticalBar = sideBars && !menuCoversPage
        content
            // The soft edge, asked for by name: what scrolls under the bar blurs and dims away with no line. Left to
            // choose, iOS 27 draws a bar of its own the hard one (a band with a line under it); iOS 26 the soft.
            .scrollEdgeEffectStyle(.soft, for: .top)
            .safeAreaBar(edge: .top) {
                SessionBar(session: session, renaming: $renaming, deleting: $deleting, backgroundTasks: backgroundTasks, starting: starting)
            }
            .toolbar(ownsVerticalBar ? .visible : .hidden, for: .navigationBar)
            .background { if !sideBars { SwipesBack() } }
            .toolbar {
                if ownsVerticalBar { SessionSideItems(session: session, renaming: $renaming, deleting: $deleting, backgroundTasks: backgroundTasks) }
            }
    }
}

/// The system's swipe back from the left edge, on a page that hides the system's bar: UIKit stops that swipe with the
/// bar hidden. While the page shows, the stack's edge swipe asks this whether to begin (on a pushed page, with no
/// transition under way) and its own delegate everything else; when the page goes, the swipe is the system's again.
struct SwipesBack: UIViewControllerRepresentable {
    func makeUIViewController(context: Context) -> Controller { Controller() }
    func updateUIViewController(_ controller: Controller, context: Context) {}

    final class Controller: UIViewController {
        private var gate: Gate?

        override func viewDidAppear(_ animated: Bool) {
            super.viewDidAppear(animated)
            guard gate == nil, let stack = navigationController, let swipe = stack.interactivePopGestureRecognizer else { return }
            // Another such page's gate may still be there (it goes as this one comes): the system's delegate is under it.
            let gate = Gate(stack: stack, system: (swipe.delegate as? Gate)?.system ?? swipe.delegate)
            self.gate = gate
            swipe.delegate = gate
        }

        override func viewWillDisappear(_ animated: Bool) {
            super.viewWillDisappear(animated)
            if let gate, let swipe = navigationController?.interactivePopGestureRecognizer, swipe.delegate === gate { swipe.delegate = gate.system }
            gate = nil
        }
    }

    final class Gate: NSObject, UIGestureRecognizerDelegate {
        private weak var stack: UINavigationController?
        private(set) weak var system: UIGestureRecognizerDelegate?

        init(stack: UINavigationController, system: UIGestureRecognizerDelegate?) {
            self.stack = stack
            self.system = system
        }

        func gestureRecognizerShouldBegin(_ g: UIGestureRecognizer) -> Bool {
            guard let stack else { return false }
            return stack.viewControllers.count > 1 && stack.transitionCoordinator == nil
        }

        func gestureRecognizer(_ g: UIGestureRecognizer, shouldReceive touch: UITouch) -> Bool {
            system?.gestureRecognizer?(g, shouldReceive: touch) ?? true
        }

        func gestureRecognizer(_ g: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer) -> Bool {
            system?.gestureRecognizer?(g, shouldRecognizeSimultaneouslyWith: other) ?? false
        }

        func gestureRecognizer(_ g: UIGestureRecognizer, shouldRequireFailureOf other: UIGestureRecognizer) -> Bool {
            system?.gestureRecognizer?(g, shouldRequireFailureOf: other) ?? false
        }

        func gestureRecognizer(_ g: UIGestureRecognizer, shouldBeRequiredToFailBy other: UIGestureRecognizer) -> Bool {
            system?.gestureRecognizer?(g, shouldBeRequiredToFailBy: other) ?? false
        }
    }
}

/// A quiet note in the middle of the conversation: a turn stopped on purpose, and such.
struct TurnNote: View {
    let text: String

    var body: some View {
        Text(text)
            .font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center)
            .padding(.horizontal, 12).padding(.vertical, 5)
            .background(Palette.fill, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .frame(maxWidth: .infinity)
    }
}

/// Under a conversation whose last turn went well: a quiet line that says so.
struct DoneLine: View {
    let text: String

    var body: some View {
        Label(text, systemImage: "checkmark")
            .font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center)
            .frame(maxWidth: .infinity)
            .padding(.top, 5)
    }
}

/// The bottom of a live turn, a glass capsule over the conversation: who is at work (`lead`), what it does and for
/// how long (`words`), and how to stop it.
struct LiveBar<Lead: View, Words: View>: View {
    let stop: LocalizedStringKey
    let stopID: String
    var disabled = false
    let action: () -> Void
    @ViewBuilder let lead: () -> Lead
    @ViewBuilder let words: () -> Words

    var body: some View {
        HStack(spacing: 10) {
            lead()
            Group { words() }.lineLimit(1)
            Spacer(minLength: 0)
            Button(action: action) {
                Label(stop, systemImage: "stop.fill")
            }
            .buttonStyle(.glass)
            .disabled(disabled)
            .accessibilityIdentifier(stopID)
        }
        .font(.subheadline)
        .padding(.leading, 16).padding(.trailing, 8).padding(.vertical, 8)
        .glassEffect(.regular, in: .capsule)
        .padding(.horizontal, 16).padding(.bottom, 4)
    }
}

/// The UIScrollView backing a conversation ScrollView, once mounted.
final class ConversationScrollBox {
    weak var view: UIScrollView?
}

/// Finds the UIScrollView hosting the conversation content.
struct ConversationScrollFinder: UIViewRepresentable {
    let box: ConversationScrollBox

    func makeUIView(context: Context) -> UIView { Finder(box: box) }
    func updateUIView(_ uiView: UIView, context: Context) {}

    private final class Finder: UIView {
        let box: ConversationScrollBox
        init(box: ConversationScrollBox) {
            self.box = box
            super.init(frame: .zero)
            isUserInteractionEnabled = false
        }
        required init?(coder: NSCoder) { fatalError() }
        override func didMoveToWindow() {
            super.didMoveToWindow()
            var v = superview
            while let view = v, !(view is UIScrollView) { v = view.superview }
            box.view = v as? UIScrollView
        }
    }
}

extension View {
    func followsEnd<Bottom: View>(
        id: String,
        ready: Bool,
        spoken: Int = 0,
        waitingAgent: String? = nil,
        waitingPlan: Bool = false,
        todoSummary: TodoEndSummary? = nil,
        atEnd: Binding<Bool>,
        position: Binding<ScrollPosition>,
        scrollBox: ConversationScrollBox,
        @ViewBuilder bottom: @escaping () -> Bottom
    ) -> some View {
        modifier(FollowsEnd(id: id, ready: ready, spoken: spoken, waitingAgent: waitingAgent, waitingPlan: waitingPlan, todoSummary: todoSummary, atEnd: atEnd, position: position, scrollBox: scrollBox, bottom: bottom))
    }
}

private struct FollowsEnd<Bottom: View>: ViewModifier {
    let id: String
    let ready: Bool
    let spoken: Int
    let waitingAgent: String?
    let waitingPlan: Bool
    let todoSummary: TodoEndSummary?
    @Binding var atEnd: Bool
    @Binding var position: ScrollPosition
    let scrollBox: ConversationScrollBox
    let bottom: () -> Bottom
    @State private var away = false
    @State private var scrolling = false
    @State private var positioned = false
    @State private var alignTask: Task<Void, Never>?
    @Environment(\.accessibilityReduceMotion) private var still

    private let endThreshold: CGFloat = 64

    private var userInteracting: Bool {
        if scrolling { return true }
        guard let sv = scrollBox.view else { return false }
        return sv.isTracking || sv.isDragging || sv.isDecelerating
    }

    func body(content: Content) -> some View {
        content
            .scrollPosition($position)
            .defaultScrollAnchor(.bottom, for: .initialOffset)
            .onChange(of: id) { _, _ in
                reset()
            }
            .onChange(of: ready, initial: true) { _, isReady in
                if isReady && !positioned {
                    scheduleAlign(markPositioned: true)
                }
            }
            .onChange(of: spoken) {
                if let sv = scrollBox.view, sv.isDecelerating {
                    sv.setContentOffset(sv.contentOffset, animated: false)
                }
                scrolling = false
                atEnd = true
                away = false
                positioned = true
                position.scrollTo(edge: .bottom)
                scheduleAlign(markPositioned: false)
            }
            .onScrollPhaseChange { oldPhase, newPhase, context in
                let active = newPhase == .interacting || newPhase == .decelerating
                let wasActive = oldPhase == .interacting || oldPhase == .decelerating
                scrolling = active
                guard active || wasActive else { return }
                let gap = fromEnd(context.geometry)
                if gap > endThreshold {
                    detachFromEnd()
                } else if !active {
                    atEnd = true
                }
            }
            .onScrollGeometryChange(for: Bool.self) { fromEnd($0) <= endThreshold } action: { _, near in
                guard userInteracting else { return }
                if !near {
                    detachFromEnd()
                } else {
                    atEnd = true
                }
            }
            .onScrollGeometryChange(for: [CGFloat].self) { g in
                [g.contentSize.height, g.containerSize.height, g.contentInsets.bottom]
            } action: { _, next in
                guard atEnd, !userInteracting else { return }
                if next[0] > 0 {
                    position.scrollTo(edge: .bottom)
                }
            }
            .onScrollGeometryChange(for: Bool.self) { g in
                fromEnd(g) > (waitingAgent != nil ? 140 : 240)
            } action: { _, far in away = far }
            .onDisappear {
                alignTask?.cancel()
                alignTask = nil
            }
            .toEndButton(away && !atEnd, waitingAgent: waitingAgent, waitingPlan: waitingPlan, todoSummary: todoSummary) {
                atEnd = true
                away = false
                positioned = true
                alignTask?.cancel()
                alignTask = nil
                withAnimation(still ? nil : .smooth) { position.scrollTo(edge: .bottom) }
            }
            .scrollEdgeEffectStyle(.soft, for: .bottom)
            .safeAreaBar(edge: .bottom) {
                VStack(spacing: 0) { bottom() }
                    .frame(maxWidth: .infinity)
            }
    }

    private func reset() {
        alignTask?.cancel()
        alignTask = nil
        atEnd = true
        away = false
        scrolling = false
        positioned = false
        if ready { scheduleAlign(markPositioned: true) }
    }

    private func detachFromEnd() {
        atEnd = false
        positioned = true
        alignTask?.cancel()
        alignTask = nil
    }

    private func scheduleAlign(markPositioned: Bool) {
        guard atEnd, !userInteracting else { return }
        alignTask?.cancel()
        position.scrollTo(edge: .bottom)
        alignTask = Task { @MainActor in
            await Task.yield()
            guard !Task.isCancelled, atEnd, !userInteracting else { return }
            position.scrollTo(edge: .bottom)
            if markPositioned, ready {
                positioned = true
            }
        }
    }
}

private func sameFailure(_ a: Failure, _ b: Failure) -> Bool { a.kind == b.kind && a.message == b.message }

private let adviceResumes: Set<FailureKind> = [.rateLimit, .providerError, .network, .contextOverflow, .resumeRejected, .agentCrashed, .stalled]

struct FailedView: View {
    @Environment(AppModel.self) private var model
    let session: Session
    let failure: Failure?
    let grant: String

    var body: some View {
        FailedCard(headline: failureHeadline(failure?.kind ?? .unspecified, agent: agentTitle(session.agent)),
                   hints: [advice, String(localized: "When \(AgentLogo.name(grant)) sends another message, it picks up from here; the earlier changes are all still there.")],
                   message: failure?.message ?? session.error) {
            if session.isArchivable {
                Button("Archive") { Task { await model.setArchived(session, true) } }
            }
        }
        .accessibilityIdentifier("session-failed")
    }

    private var advice: String {
        guard let failure, !adviceResumes.contains(failure.kind) else { return "" }
        return failureAdvice(failure)
    }
}

struct FailedCard<Actions: View>: View {
    let headline: String
    let hints: [String]
    let message: String
    @ViewBuilder let actions: () -> Actions
    @State private var open = false

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Image(systemName: "exclamationmark.circle").foregroundStyle(.red)
            VStack(alignment: .leading, spacing: 3) {
                Text(headline)
                    .font(.subheadline.weight(.semibold))
                ForEach(hints.filter { !$0.isEmpty }, id: \.self) { Text($0) }
                    .font(.footnote).foregroundStyle(.secondary)
                HStack(spacing: 14) {
                    if !message.isEmpty {
                        Button { withAnimation(.snappy(duration: 0.25)) { open.toggle() } } label: {
                            HStack(spacing: 3) {
                                Text("Details")
                                Image(systemName: "chevron.right").font(.caption2.weight(.semibold)).rotationEffect(.degrees(open ? 90 : 0))
                            }
                        }
                        .accessibilityIdentifier("failure-details")
                    }
                    actions()
                }
                .font(.footnote).foregroundStyle(.secondary).buttonStyle(.plain)
                .padding(.top, 6)
                if open { RawMessage(text: message).padding(.top, 4) }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.horizontal, 14).padding(.vertical, 12)
        .background(Palette.fill, in: RoundedRectangle(cornerRadius: 13, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 13, style: .continuous).strokeBorder(Palette.separator, lineWidth: 0.5))
        .padding(.top, 6)
        .accessibilityElement(children: .contain)
    }
}

struct FailLine: View {
    let title: String
    let message: String
    @State private var open = false

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Button {
                if !message.isEmpty { withAnimation(.snappy(duration: 0.25)) { open.toggle() } }
            } label: {
                HStack(spacing: 7) {
                    Image(systemName: "exclamationmark.circle")
                    Text(title).multilineTextAlignment(.leading)
                    if !message.isEmpty {
                        Image(systemName: "chevron.right").font(.caption2.weight(.semibold)).rotationEffect(.degrees(open ? 90 : 0))
                    }
                }
                .font(.footnote).foregroundStyle(.red)
            }
            .buttonStyle(.plain)
            if open { RawMessage(text: message).padding(.leading, 21) }
        }
    }
}

private struct RawMessage: View {
    let text: String

    var body: some View {
        Text(text)
            .font(.caption.monospaced()).foregroundStyle(.secondary)
            .textSelection(.enabled)
            .padding(.horizontal, 10).padding(.vertical, 8)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Palette.code, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 8, style: .continuous).strokeBorder(Palette.separator, lineWidth: 0.5))
    }
}

struct Breathing<Content: View>: View {
    @ViewBuilder let content: () -> Content
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var dim = false

    var body: some View {
        content()
            .opacity(dim ? 0.45 : 1)
            .onAppear {
                guard !reduceMotion else { return }
                withAnimation(.easeInOut(duration: 1.2).repeatForever(autoreverses: true)) { dim = true }
            }
    }
}
