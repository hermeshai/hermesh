import HermeshProto
import HermeshSessions
import QuickLook
import SwiftUI

/// Pending follow-up messages rendered inline at the foot of the conversation stream as quiet translucent bubbles,
/// keeping the bottom composer unobstructed while supporting direct inline editing, reordering, merging, steering, and withdrawal.
struct QueuedStreamView: View {
    @Environment(AppModel.self) private var model
    let runs: [Hermesh_Session_V1_Run]
    let sessionID: String
    let machineID: String
    let running: Bool
    let queueHeld: Bool
    let busyRunIDs: Set<String>
    let batchBusy: Bool
    @Binding var isTyping: Bool
    let onSteer: (Hermesh_Session_V1_Run) -> Void
    let onEdit: (Hermesh_Session_V1_Run, String) async -> Bool
    let onMoveUp: (Hermesh_Session_V1_Run, String) -> Void
    let onWithdraw: (Hermesh_Session_V1_Run) -> Void
    let onMergeAll: () -> Void
    let onSteerAll: () -> Void
    let onResume: () -> Void

    @State private var editingRunID: String?
    @State private var editingText = ""
    @State private var savingEdit = false
    @State private var preview: URL?
    @State private var opening: String?
    @State private var lightbox: SessionLightboxPresentation?
    @FocusState private var fieldFocused: Bool

    var body: some View {
        if !runs.isEmpty {
            VStack(alignment: .trailing, spacing: 11) {
                ForEach(Array(runs.enumerated()), id: \.element.runID) { i, run in
                    row(for: run, index: i)
                }
                if runs.count >= 2 {
                    batchFooter
                }
            }
            .padding(.top, 2)
            .frame(maxWidth: .infinity, alignment: .trailing)
            .quickLookPreview($preview)
            .fullScreenCover(item: $lightbox) { SessionImageLightbox(presentation: $0) }
            .onChange(of: fieldFocused) { _, now in
                isTyping = now
            }
            .onChange(of: runs.map(\.runID)) { _, ids in
                if let cur = editingRunID, !ids.contains(cur) {
                    closeEditor()
                }
            }
            .onDisappear {
                if isTyping { isTyping = false }
            }
        }
    }

    @ViewBuilder private func row(for run: Hermesh_Session_V1_Run, index i: Int) -> some View {
        let rowBusy = busyRunIDs.contains(run.runID) || batchBusy
        let store = SessionAttachmentStore.shared
        if editingRunID == run.runID {
            VStack(alignment: .trailing, spacing: 6) {
                if !run.attachments.isEmpty {
                    attachmentsRow(run.attachments, store: store)
                }
                inlineEditor(for: run)
            }
            .padding(.leading, 48)
            .frame(maxWidth: .infinity, alignment: .trailing)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("thread-queued-\(i)")
        } else {
            let hasText = !run.prompt.isEmpty || run.attachments.isEmpty
            VStack(alignment: .trailing, spacing: 5) {
                if !run.attachments.isEmpty {
                    attachmentsRow(run.attachments, store: store)
                }
                if hasText {
                    Button {
                        startEditing(run)
                    } label: {
                        Text(verbatim: run.prompt)
                            .lineSpacing(3)
                            .multilineTextAlignment(.leading)
                            .foregroundStyle(.primary)
                            .padding(.horizontal, 14).padding(.vertical, 9)
                            .background(Palette.pendingBubble, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                            .overlay(
                                RoundedRectangle(cornerRadius: 18, style: .continuous)
                                    .strokeBorder(Palette.pendingBubbleBorder, lineWidth: 0.5)
                            )
                    }
                    .buttonStyle(.plain)
                    .contentShape(.contextMenuPreview, RoundedRectangle(cornerRadius: 18, style: .continuous))
                    .contextMenu {
                        Button("Edit", systemImage: "pencil") { startEditing(run) }
                        CopyMenu(text: run.prompt, sent: run.hasCreatedAt ? run.createdAt.date : nil)
                    }
                    .disabled(rowBusy)
                    .accessibilityIdentifier("thread-queue-bubble-\(i)")
                }

                HStack(spacing: 6) {
                    if rowBusy {
                        ProgressView().controlSize(.mini)
                    }
                    if queueHeld && i == 0 {
                        Text("Queue paused")
                            .foregroundStyle(.orange)
                    } else if runs.count > 1 {
                        Text("Next #\(i + 1)")
                            .monospacedDigit()
                            .foregroundStyle(.tertiary)
                    } else {
                        Text("Next turn")
                            .foregroundStyle(.tertiary)
                    }

                    dot
                    Button(queueHeld && !running ? LocalizedStringKey("Resume") : LocalizedStringKey("Steer")) {
                        onSteer(run)
                    }
                    .fontWeight(.medium)
                    .foregroundStyle(Color.accentColor)
                    .buttonStyle(.plain)
                    .disabled(rowBusy)
                    .accessibilityIdentifier("thread-queue-promote-\(i)")

                    dot
                    Button("Edit") {
                        startEditing(run)
                    }
                    .foregroundStyle(.secondary)
                    .buttonStyle(.plain)
                    .disabled(rowBusy)
                    .accessibilityIdentifier("thread-queue-edit-\(i)")

                    if i > 0 {
                        dot
                        Button("Move up") {
                            onMoveUp(run, runs[i - 1].runID)
                        }
                        .foregroundStyle(.secondary)
                        .buttonStyle(.plain)
                        .disabled(rowBusy)
                        .accessibilityIdentifier("thread-queue-up-\(i)")
                    }

                    dot
                    Button("Withdraw") {
                        onWithdraw(run)
                    }
                    .foregroundStyle(.secondary)
                    .buttonStyle(.plain)
                    .disabled(rowBusy)
                    .accessibilityIdentifier("thread-queue-cancel-\(i)")
                }
                .font(.caption)
                .padding(.trailing, 2)
            }
            .padding(.leading, 48)
            .frame(maxWidth: .infinity, alignment: .trailing)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("thread-queued-\(i)")
        }
    }

    private func inlineEditor(for run: Hermesh_Session_V1_Run) -> some View {
        let trimmed = editingText.trimmingCharacters(in: .whitespacesAndNewlines)
        let canSave = !savingEdit && (!trimmed.isEmpty || !run.attachments.isEmpty)
        return VStack(alignment: .leading, spacing: 8) {
            TextField("", text: $editingText, axis: .vertical)
                .font(.body)
                .lineSpacing(3)
                .focused($fieldFocused)
                .disabled(savingEdit)
                .accessibilityIdentifier("thread-queue-edit-input")

            HStack(spacing: 14) {
                Spacer(minLength: 0)
                if savingEdit {
                    ProgressView().controlSize(.mini)
                }
                Button("Cancel") {
                    closeEditor()
                }
                .font(.footnote)
                .foregroundStyle(.secondary)
                .buttonStyle(.plain)
                .disabled(savingEdit)
                .accessibilityIdentifier("thread-queue-edit-cancel")

                Button("Save") {
                    guard canSave else { return }
                    savingEdit = true
                    Task {
                        let ok = await onEdit(run, trimmed)
                        savingEdit = false
                        if ok { closeEditor() }
                    }
                }
                .font(.footnote.weight(.semibold))
                .foregroundStyle(canSave ? Color.accentColor : Color.secondary)
                .buttonStyle(.plain)
                .disabled(!canSave)
                .accessibilityIdentifier("thread-queue-edit-save")
            }
        }
        .padding(.horizontal, 13).padding(.vertical, 9)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Palette.fill, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 16, style: .continuous)
                .strokeBorder(Color.accentColor, lineWidth: 1)
        )
    }

    private var batchFooter: some View {
        HStack(spacing: 8) {
            if batchBusy {
                ProgressView().controlSize(.mini)
            }
            Text("\(runs.count) queued")
                .monospacedDigit()
                .foregroundStyle(.tertiary)
            dot
            Button("Merge into one", action: onMergeAll)
                .foregroundStyle(.secondary)
                .buttonStyle(.plain)
                .disabled(batchBusy)
                .accessibilityIdentifier("thread-queue-merge")
            dot
            if queueHeld {
                Button("Resume queue", action: onResume)
                    .fontWeight(.medium)
                    .foregroundStyle(Color.accentColor)
                    .buttonStyle(.plain)
                    .disabled(batchBusy)
                    .accessibilityIdentifier("thread-queue-resume")
            } else {
                Button("Steer all", action: onSteerAll)
                    .fontWeight(.medium)
                    .foregroundStyle(Color.accentColor)
                    .buttonStyle(.plain)
                    .disabled(batchBusy)
                    .accessibilityIdentifier("thread-queue-steer-all")
            }
        }
        .font(.caption)
        .padding(.trailing, 2)
        .padding(.top, 1)
        .frame(maxWidth: .infinity, alignment: .trailing)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("thread-queue-batch")
    }

    private var dot: some View {
        Text(verbatim: "·").foregroundStyle(.quaternary)
    }

    private func startEditing(_ run: Hermesh_Session_V1_Run) {
        editingRunID = run.runID
        editingText = run.prompt
        Task { @MainActor in
            fieldFocused = true
        }
    }

    private func closeEditor() {
        fieldFocused = false
        editingRunID = nil
        savingEdit = false
        if isTyping { isTyping = false }
    }

    @ViewBuilder private func attachmentsRow(_ attachments: [Hermesh_Session_V1_AttachmentRef], store: SessionAttachmentStore) -> some View {
        HerWrap(spacing: 6) {
            ForEach(attachments, id: \.blobSha256) { att in
                attachmentView(att, all: attachments, store: store)
            }
        }
    }

    @ViewBuilder private func attachmentView(_ att: Hermesh_Session_V1_AttachmentRef, all: [Hermesh_Session_V1_AttachmentRef], store: SessionAttachmentStore) -> some View {
        let name = fileBaseName(att.name)
        let cached = store.pictures[att.blobSha256]
        let unreadable = if case .some(nil) = cached { true } else { false }
        if isSessionImageAttachment(att) && !unreadable {
            Button { openPicture(att, all: all) } label: {
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
            .task(id: att.blobSha256) {
                await store.loadPicture(api: model.api, sessionID: sessionID, machineID: machineID, ref: att)
            }
        } else {
            Button { isSessionImageAttachment(att) ? openPicture(att, all: all) : openFile(att) } label: {
                HerChip(name: name, busy: opening == att.blobSha256)
            }
            .buttonStyle(.plain)
        }
    }

    private func openPicture(_ att: Hermesh_Session_V1_AttachmentRef, all: [Hermesh_Session_V1_AttachmentRef]) {
        hideKeyboard()
        let pictures = all.filter(isSessionImageAttachment)
        lightbox = SessionLightboxPresentation(
            sessionID: sessionID,
            machineID: machineID,
            run: "",
            items: pictures.map(LightboxItem.attachment),
            initialIndex: pictures.firstIndex(of: att) ?? 0
        )
    }

    private func openFile(_ att: Hermesh_Session_V1_AttachmentRef) {
        hideKeyboard()
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
}
