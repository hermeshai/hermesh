import HermeshProto
import HermeshSessions
import SwiftUI

/// The agent's plan confirmation, rendered inline at the bottom of the conversation stream so it
/// scrolls naturally with the plan above and aligns visually with QuestionPanel (Scheme A).
struct PlanReviewPanel: View {
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    let session: Session
    let request: Hermesh_Session_V1_RuntimeRequest
    var isTyping: Binding<Bool> = .constant(false)
    let respond: ((inout Hermesh_Session_V1_RespondRuntimeRequestRequest) -> Void) async -> Void

    @State private var revising = false
    @State private var feedback = ""
    @State private var busy = false
    @FocusState private var typing: Bool

    private var planTitle: String {
        request.planReview.title.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private var trimmedFeedback: String {
        feedback.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 11) {
            headerRow
            titleLine
            if revising {
                reviseSection
            } else {
                actionRow
            }
        }
        .padding(14)
        .background(Palette.reply, in: RoundedRectangle(cornerRadius: 22, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 22, style: .continuous).strokeBorder(Palette.separator, lineWidth: 0.5))
        .onChange(of: typing) { _, now in
            isTyping.wrappedValue = now
        }
        .onDisappear {
            if isTyping.wrappedValue { isTyping.wrappedValue = false }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("thread-plan-review")
    }

    @ViewBuilder private var headerRow: some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 8) {
                whoLabel
                Spacer(minLength: 4)
                modeBadge
            }
            VStack(alignment: .leading, spacing: 6) {
                whoLabel
                modeBadge
            }
        }
    }

    private var whoLabel: some View {
        HStack(spacing: 6) {
            AgentLogo(id: session.agent, size: 18)
            Text("\(agentTitle(session.agent)) · Plan ready")
                .font(.caption.weight(.medium))
                .foregroundStyle(.secondary)
                .lineLimit(1)
        }
    }

    @ViewBuilder private var modeBadge: some View {
        if session.interactionMode == .plan {
            Text(verbatim: "Plan → Code")
                .font(.caption2.weight(.medium))
                .foregroundStyle(.secondary)
                .padding(.horizontal, 8).padding(.vertical, 3)
                .background(Palette.fill2, in: Capsule())
        }
    }

    @ViewBuilder private var titleLine: some View {
        if !planTitle.isEmpty {
            Text(verbatim: planTitle)
                .font(.subheadline.weight(.semibold))
                .fixedSize(horizontal: false, vertical: true)
        } else {
            Text("Start on this plan?")
                .font(.subheadline.weight(.semibold))
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    @ViewBuilder private var actionRow: some View {
        Group {
            if dynamicTypeSize.isAccessibilitySize {
                VStack(spacing: 8) {
                    approveButton
                    reviseButton
                }
            } else {
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: 10) {
                        reviseButton
                        approveButton
                    }
                    VStack(spacing: 8) {
                        approveButton
                        reviseButton
                    }
                }
            }
        }
        .controlSize(.regular)
        .padding(.top, 2)
        .disabled(busy)
    }

    private var reviseButton: some View {
        Button {
            withAnimation(.snappy(duration: 0.2)) {
                revising = true
            }
            typing = true
        } label: {
            Text("Request Changes")
                .lineLimit(1)
                .padding(.vertical, 4)
                .frame(maxWidth: .infinity)
        }
        .buttonStyle(.glass)
        .accessibilityIdentifier("thread-revise-plan")
    }

    private var approveButton: some View {
        Button {
            submit { $0.approvePlan = true }
        } label: {
            Text("Approve & Start")
                .lineLimit(1)
                .padding(.vertical, 4)
                .frame(maxWidth: .infinity)
        }
        .buttonStyle(.glassProminent)
        .accessibilityIdentifier("thread-approve-plan")
    }

    private var reviseSection: some View {
        VStack(alignment: .leading, spacing: 9) {
            HStack(spacing: 8) {
                Image(systemName: "square.and.pencil")
                    .font(.footnote)
                    .foregroundStyle(trimmedFeedback.isEmpty && !typing ? Color.secondary : Color.accentColor)
                TextField("Tell \(agentTitle(session.agent)) what to adjust…", text: $feedback)
                    .font(.subheadline)
                    .focused($typing)
                    .submitLabel(.send)
                    .onSubmit {
                        if !trimmedFeedback.isEmpty { sendRevision() }
                    }
                    .accessibilityIdentifier("plan-feedback-text")
            }
            .padding(.horizontal, 12).padding(.vertical, 10)
            .background(
                trimmedFeedback.isEmpty || typing ? AnyShapeStyle(Palette.fill) : AnyShapeStyle(Color.accentColor.opacity(0.12)),
                in: RoundedRectangle(cornerRadius: 14, style: .continuous)
            )
            .overlay {
                RoundedRectangle(cornerRadius: 14, style: .continuous)
                    .strokeBorder(
                        typing || !trimmedFeedback.isEmpty ? Color.accentColor.opacity(typing ? 0.9 : 0.55) : Palette.separator,
                        lineWidth: typing ? 1.3 : (!trimmedFeedback.isEmpty ? 1 : 0.5)
                    )
            }

            Group {
                if dynamicTypeSize.isAccessibilitySize {
                    VStack(spacing: 8) {
                        sendRevisionButton
                        cancelRevisionButton
                    }
                } else {
                    ViewThatFits(in: .horizontal) {
                        HStack(spacing: 10) {
                            cancelRevisionButton
                            sendRevisionButton
                        }
                        VStack(spacing: 8) {
                            sendRevisionButton
                            cancelRevisionButton
                        }
                    }
                }
            }
            .controlSize(.regular)
            .disabled(busy)
        }
    }

    private var cancelRevisionButton: some View {
        Button {
            typing = false
            hideKeyboard()
            feedback = ""
            withAnimation(.snappy(duration: 0.2)) {
                revising = false
            }
        } label: {
            Text("Cancel")
                .lineLimit(1)
                .padding(.vertical, 4)
                .frame(maxWidth: .infinity)
        }
        .buttonStyle(.glass)
        .accessibilityIdentifier("thread-cancel-plan-feedback")
    }

    private var sendRevisionButton: some View {
        Button {
            sendRevision()
        } label: {
            Text("Send Feedback")
                .lineLimit(1)
                .padding(.vertical, 4)
                .frame(maxWidth: .infinity)
        }
        .buttonStyle(.glassProminent)
        .disabled(trimmedFeedback.isEmpty)
        .accessibilityIdentifier("thread-send-plan-feedback")
    }

    private func sendRevision() {
        let note = trimmedFeedback
        guard !note.isEmpty else { return }
        submit {
            $0.approvePlan = false
            $0.feedback = note
        }
    }

    private func submit(_ build: @escaping (inout Hermesh_Session_V1_RespondRuntimeRequestRequest) -> Void) {
        typing = false
        hideKeyboard()
        busy = true
        Task {
            await respond(build)
            busy = false
        }
    }
}

/// Quiet inline receipt left in the conversation stream once a plan review is approved or sent back for changes.
struct SettledPlanReviewCard: View {
    @Environment(AppModel.self) private var model
    let title: String
    let approved: Bool
    let by: String

    private var actorName: String? {
        (by.isEmpty || by == "owner") ? nil : AgentLogo.name(model.clientLabel(by))
    }

    var body: some View {
        HStack(alignment: .top, spacing: 9) {
            badge
                .padding(.top, 1.5)
            VStack(alignment: .leading, spacing: 2) {
                statusLine
                if !title.isEmpty {
                    Text(verbatim: title)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.horizontal, 12).padding(.vertical, 10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Palette.fill, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 14, style: .continuous).strokeBorder(Palette.separator, lineWidth: 0.5))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("settled-plan-review")
    }

    @ViewBuilder private var badge: some View {
        if approved {
            Image(systemName: "checkmark")
                .font(.system(size: 10, weight: .bold))
                .foregroundStyle(.green)
                .frame(width: 18, height: 18)
                .background(Color.green.opacity(0.15), in: .circle)
                .overlay(Circle().strokeBorder(Color.green.opacity(0.3), lineWidth: 0.5))
        } else {
            Image(systemName: "pencil")
                .font(.system(size: 10, weight: .semibold))
                .foregroundStyle(.secondary)
                .frame(width: 18, height: 18)
                .background(Palette.fill2, in: .circle)
        }
    }

    @ViewBuilder private var statusLine: some View {
        if approved {
            if let actorName {
                Text("\(actorName) approved the plan")
                    .font(.footnote.weight(.medium))
            } else {
                HStack(spacing: 4) {
                    Text("Plan approved")
                        .font(.footnote.weight(.medium))
                    Text(verbatim: "·")
                        .font(.footnote)
                        .foregroundStyle(.tertiary)
                    Text("Starting code")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
            }
        } else {
            if let actorName {
                Text("\(actorName) requested plan changes")
                    .font(.footnote.weight(.medium))
                    .foregroundStyle(.secondary)
            } else {
                Text("Plan revision requested")
                    .font(.footnote.weight(.medium))
                    .foregroundStyle(.secondary)
            }
        }
    }
}
