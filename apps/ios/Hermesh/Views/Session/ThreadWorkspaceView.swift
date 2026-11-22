import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI

/// Native iOS v2 Thread Workspace View: polymorphic Code/Plan + 4-level RuntimeMode composer,
/// live RuntimeRequest approval/plan banners, durable queue strip, and Git checkpoint rewind.
struct ThreadWorkspaceView: View {
    let api: LocalAPI
    let initialMachineID: String
    let initialProjectID: String
    @State private var threadID: String
    @State private var projection: Hermesh_Thread_V2_ThreadProjection?
    @State private var providers: [Hermesh_Thread_V2_ProviderInstance] = []
    @State private var selectedInstanceID: String = ""
    @State private var selectedModel: String = ""
    @State private var interactionMode: Hermesh_Thread_V2_InteractionMode = .code
    @State private var runtimeMode: Hermesh_Identity_V1_RuntimeMode = .autoEdits
    @State private var draft: String = ""
    @State private var checkpoints: [Hermesh_Thread_V2_Checkpoint] = []

    init(api: LocalAPI, machineID: String, projectID: String = "scratch", threadID: String = "") {
        self.api = api
        self.initialMachineID = machineID
        self.initialProjectID = projectID
        _threadID = State(initialValue: threadID)
    }

    var body: some View {
        VStack(spacing: 0) {
            timelineView
            Divider()
            composerArea
        }
        .navigationTitle(Text(verbatim: projection?.thread.title.isEmpty == false ? projection!.thread.title : ThreadWorkspaceStrings.newThread))
        .task(id: initialMachineID) {
            if let res = try? await api.listThreadProviders(machineID: initialMachineID, projectID: initialProjectID) {
                providers = res.providers
                if selectedInstanceID.isEmpty, let first = res.providers.first {
                    selectedInstanceID = first.instanceID
                    selectedModel = first.models.first(where: \.isDefault)?.slug ?? first.models.first?.slug ?? "default"
                }
            }
        }
        .task(id: threadID) {
            guard !threadID.isEmpty else { return }
            do {
                let initial = try await api.getThread(threadID: threadID)
                projection = initial.projection
                checkpoints = initial.projection.checkpoints
                for try await ev in api.subscribeThread(threadID: threadID, afterSeq: initial.projection.thread.lastSeq) {
                    projection = applyThreadEvent(ev, to: projection)
                    checkpoints = projection?.checkpoints ?? checkpoints
                }
            } catch {}
        }
    }

    private var timelineView: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 10) {
                ForEach(projection?.items ?? [], id: \.itemID) { item in
                    VStack(alignment: .leading, spacing: 4) {
                        Text(verbatim: item.text.isEmpty ? item.toolName : item.text)
                            .font(.subheadline)
                            .padding(10)
                            .background(item.kind == .userMessage ? Color.accentColor.opacity(0.14) : Color.secondary.opacity(0.1))
                            .clipShape(RoundedRectangle(cornerRadius: 10))
                    }
                }
                if let pending = pendingRequest(of: projection) {
                    pendingBanner(pending)
                }
            }
            .padding(14)
        }
    }

    @ViewBuilder
    private func pendingBanner(_ req: Hermesh_Thread_V2_RuntimeRequest) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            if req.kind == .planReview {
                Text(verbatim: req.planReview.title)
                    .font(.headline)
                Button {
                    Task {
                        var r = Hermesh_Thread_V2_RespondRuntimeRequestRequest()
                        r.commandID = UUID().uuidString
                        r.threadID = threadID
                        r.requestID = req.requestID
                        r.approvePlan = true
                        _ = try? await api.respondThreadRuntimeRequest(r)
                    }
                } label: {
                    Text(verbatim: ThreadWorkspaceStrings.approvePlan)
                }
                .buttonStyle(.borderedProminent)
            } else if req.kind == .toolApproval {
                Text(verbatim: req.approval.toolName)
                    .font(.headline)
                HStack(spacing: 8) {
                    Button {
                        respondApproval(req.requestID, decision: .acceptOnce)
                    } label: {
                        Text(verbatim: ThreadWorkspaceStrings.approveOnce)
                    }
                    .buttonStyle(.borderedProminent)

                    Button {
                        respondApproval(req.requestID, decision: .acceptForThread)
                    } label: {
                        Text(verbatim: ThreadWorkspaceStrings.allowInThread)
                    }
                    .buttonStyle(.bordered)

                    Button(role: .destructive) {
                        respondApproval(req.requestID, decision: .decline)
                    } label: {
                        Text(verbatim: ThreadWorkspaceStrings.decline)
                    }
                    .buttonStyle(.bordered)
                }
            }
        }
        .padding(12)
        .background(Color.orange.opacity(0.12))
        .clipShape(RoundedRectangle(cornerRadius: 12))
    }

    private var composerArea: some View {
        let status = projection?.thread.status ?? .idle
        let action = composerPrimaryAction(
            hasThread: !threadID.isEmpty,
            status: status,
            draftNotEmpty: !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
            machineOnline: true,
            supportsLiveSteer: true
        )
        return VStack(spacing: 8) {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 8) {
                    Button {
                        interactionMode = interactionMode == .plan ? .code : .plan
                    } label: {
                        Text(verbatim: interactionModeTitle(interactionMode))
                    }
                    .buttonStyle(.bordered)

                    ForEach(orderedRuntimeModes, id: \.rawValue) { m in
                        Button {
                            runtimeMode = m
                        } label: {
                            Text(verbatim: runtimeModeTitle(m))
                        }
                        .buttonStyle(.bordered)
                        .tint(runtimeMode == m ? .accentColor : .secondary)
                    }

                    if let firstCp = checkpoints.first, status == .idle {
                        Button {
                            Task {
                                _ = try? await api.rollbackThreadCheckpoint(
                                    threadID: threadID,
                                    checkpointID: firstCp.checkpointID,
                                    restoreFiles: true
                                )
                            }
                        } label: {
                            Text(verbatim: ThreadWorkspaceStrings.rewindFiles)
                        }
                        .buttonStyle(.bordered)
                    }
                }
            }

            HStack(spacing: 8) {
                TextField("", text: $draft, axis: .vertical)
                    .textFieldStyle(.roundedBorder)
                    .lineLimit(1 ... 5)

                Button {
                    Task { await performPrimary(action) }
                } label: {
                    Text(verbatim: action.title)
                }
                .buttonStyle(.borderedProminent)
            }
        }
        .padding(12)
    }

    private func respondApproval(_ requestID: String, decision: Hermesh_Thread_V2_ApprovalDecision) {
        Task {
            var r = Hermesh_Thread_V2_RespondRuntimeRequestRequest()
            r.commandID = UUID().uuidString
            r.threadID = threadID
            r.requestID = requestID
            r.decision = decision
            _ = try? await api.respondThreadRuntimeRequest(r)
        }
    }

    private func performPrimary(_ action: ThreadComposerAction) async {
        if action == .stop {
            _ = try? await api.interruptThreadRun(threadID: threadID)
            return
        }
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        draft = ""
        if threadID.isEmpty {
            var req = Hermesh_Thread_V2_LaunchThreadRequest()
            req.commandID = UUID().uuidString
            req.machineID = initialMachineID
            req.projectID = initialProjectID
            req.initialPrompt = text
            req.interactionMode = interactionMode
            req.runtimeMode = runtimeMode
            req.modelSelection.instanceID = selectedInstanceID
            req.modelSelection.model = selectedModel
            if let res = try? await api.launchThread(req) {
                threadID = res.thread.threadID
            }
        } else {
            var req = Hermesh_Thread_V2_DispatchMessageRequest()
            req.commandID = UUID().uuidString
            req.threadID = threadID
            req.text = text
            req.intent = action == .steer ? .steer : (action == .queue ? .queue : .auto)
            req.interactionMode = interactionMode
            req.runtimeMode = runtimeMode
            _ = try? await api.dispatchThreadMessage(req)
        }
    }
}
