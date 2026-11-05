import HermeshProto
import SwiftUI

struct DemoView: View {
    @State private var model = AppModel(demo: DemoService())
    @Environment(\.dismiss) private var dismiss
    @Environment(\.scenePhase) private var phase

    var body: some View {
        MainView()
            .environment(model)
            .preferredColorScheme(model.demoAppearance == "light" ? .light : model.demoAppearance == "dark" ? .dark : nil)
            .safeAreaInset(edge: .bottom, spacing: 0) {
                HStack {
                    VStack(alignment: .leading, spacing: 2) {
                        Text("Demo Mode").font(.subheadline.weight(.semibold))
                        Text("Simulated data · No real tasks run").font(.caption).foregroundStyle(.secondary)
                    }
                    Spacer()
                    Button("Exit Demo") { dismiss() }
                        .accessibilityIdentifier("exit-demo")
                }
                .padding(.horizontal, 18).padding(.vertical, 10)
                .background(.regularMaterial)
            }
            .task { await model.start() }
            .onChange(of: phase) { _, p in
                if p == .active { model.foreground() }
                else if p == .background { model.background() }
            }
            .onDisappear { model.background(); model.api?.demo?.stop() }
            .alert("Something went wrong", isPresented: Binding(get: { model.problem != nil }, set: { if !$0 { model.problem = nil } })) {
                Button("OK", role: .cancel) {}
            } message: { Text(model.problem ?? "") }
    }
}

/// Simulate the other end asking to connect, then use the regular approval UI.
struct DemoPairingSheet: View {
    @Environment(AppModel.self) private var model
    let kind: Hermesh_Identity_V1_PairingKind
    @State private var code: String?

    var body: some View {
        Group {
            if let code { PairingSheet(code: code) }
            else { ProgressView() }
        }
        .task {
            if code == nil, let demo = model.api?.demo {
                let next = demo.pairing(kind)
                model.dismissedPairings.remove(next)
                model.pairings = (try? await model.api?.pairings()) ?? []
                code = next
            }
        }
    }
}
