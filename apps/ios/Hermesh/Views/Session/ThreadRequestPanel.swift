import HermeshProto
import HermeshSessions
import SwiftUI

/// What a session's coding agent waits on, over the bottom of its conversation, as Her's approval is
/// (HerApprovalPanel): a tool call to allow, or a plan to approve. A question asks as a session's does
/// (QuestionPanel); a secret is given on the Mac.
struct ThreadRequestPanel: View {
    let session: Session
    let request: Hermesh_Thread_V2_RuntimeRequest
    let respond: ((inout Hermesh_Thread_V2_RespondRuntimeRequestRequest) -> Void) async -> Void
    @State private var busy = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 8) {
                AgentLogo(id: session.agent, size: 20)
                Text(title).font(.subheadline.weight(.semibold)).lineLimit(2)
                Spacer(minLength: 0)
                if request.kind == .toolApproval, request.approval.outsideWorkspace {
                    Text("Outside the project").font(.caption).foregroundStyle(.secondary)
                }
            }
            if !reason.isEmpty { Text(reason).font(.footnote).foregroundStyle(.secondary) }
            if !preview.isEmpty {
                ScrollView {
                    Text(preview).font(.footnote.monospaced()).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading)
                }
                .frame(maxHeight: 160)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.horizontal, 12).padding(.vertical, 10)
                .background(Palette.code, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
            }
            HStack(spacing: 8) {
                switch request.kind {
                case .toolApproval:
                    Button { give { $0.decision = .decline } } label: { Text("Deny").frame(maxWidth: .infinity) }
                        .buttonStyle(.glass)
                        .accessibilityIdentifier("thread-deny")
                    Button { give { $0.decision = .acceptForThread } } label: { Text("Always Allow").frame(maxWidth: .infinity).lineLimit(1).minimumScaleFactor(0.8) }
                        .buttonStyle(.glass)
                        .accessibilityIdentifier("thread-allow-always")
                    Button { give { $0.decision = .acceptOnce } } label: { Text("Allow Once").frame(maxWidth: .infinity) }
                        .buttonStyle(.glassProminent)
                        .accessibilityIdentifier("thread-allow-once")
                case .planReview:
                    Button { give { $0.approvePlan = true } } label: { Text("Approve Plan").frame(maxWidth: .infinity) }
                        .buttonStyle(.glassProminent)
                        .accessibilityIdentifier("thread-approve-plan")
                default:
                    Text("Answer it on the Mac.").font(.footnote).foregroundStyle(.secondary)
                }
            }
            .controlSize(.large)
            .disabled(busy)
        }
        .padding(18)
        .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 36, style: .continuous))
        .padding(.horizontal, 10).padding(.bottom, 4)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("thread-request")
    }

    private var title: String {
        switch request.kind {
        case .toolApproval:
            let a = request.approval
            if !a.command.isEmpty { return String(localized: "Run this command?") }
            let file = a.targetPath.split(separator: "/").last.map(String.init) ?? ""
            return String(localized: "Use \(a.toolName.isEmpty ? String(localized: "a tool") : a.toolName) \(file)?")
        case .planReview: return String(localized: "Start on this plan?")
        case .secretInput: return String(localized: "\(agentTitle(session.agent)) needs \(request.secretInput.label)")
        default: return ""
        }
    }

    private var reason: String {
        switch request.kind {
        case .toolApproval: request.approval.reason
        case .planReview: request.planReview.title
        case .secretInput: request.secretInput.reason
        default: ""
        }
    }

    /// The call itself: the command, or the change it makes.
    private var preview: String {
        guard request.kind == .toolApproval else { return "" }
        return request.approval.command.isEmpty ? request.approval.diffPreview : request.approval.command
    }

    private func give(_ build: @escaping (inout Hermesh_Thread_V2_RespondRuntimeRequestRequest) -> Void) {
        busy = true
        Task {
            await respond(build)
            busy = false
        }
    }
}
